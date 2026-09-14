package common

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelemetryDoesNotInterfereWithPerformanceGuardDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. 验证默认配置下 Guard 是关闭的
	config := GetPerformanceMonitorConfig()
	assert.False(t, config.Enabled, "Performance guard should be disabled by default in production")

	// 2. 模拟系统状态有值
	updateSystemStatus()
	status := GetSystemStatus()

	// 3. 构造使用 Guard 的测试 Gin 引擎
	router := gin.New()
	router.Use(func(c *gin.Context) {
		cfg := GetPerformanceMonitorConfig()
		if !cfg.Enabled {
			c.Next()
			return
		}
		st := GetSystemStatus()
		if cfg.CPUThreshold > 0 && int(st.CPUUsage) > cfg.CPUThreshold {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "overloaded"})
			return
		}
		c.Next()
	})

	router.GET("/v1/models", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "cpu": status.CPUUsage})
	})

	// 4. 并发 100 次请求，验证全部正常 200 通过，绝不产生 503
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", "/v1/models", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusOK, w.Code)
		}()
	}
	wg.Wait()
}

func TestConcurrentSnapshotReadsDoNotTriggerRedundantSampling(t *testing.T) {
	// 验证 1000 次并发读取 GetSystemStatus 仅为 atomic.Value 读取，耗时在毫秒级且无竞争
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := GetSystemStatus()
			assert.NotEmpty(t, st.CPU.Status)
		}()
	}
	wg.Wait()
	duration := time.Since(start)
	assert.Less(t, duration, 200*time.Millisecond, "1000 concurrent snapshot reads should be non-blocking atomic operations")
}

func TestBuildInfoCorrectness(t *testing.T) {
	info := GetBuildInfo()
	require.NotEmpty(t, info.Version)
	require.NotEmpty(t, info.Platform)
	require.NotEmpty(t, info.GoVersion)
	assert.Contains(t, info.Platform, "/")
}

func TestJSONCompatibilityAndNullSemantics(t *testing.T) {
	metric := DetailedResourceMetric{
		Status:       MetricStatusInitializing,
		Scope:        MetricScopeContainer,
		Source:       "test-cgroup",
		UsagePercent: nil, // 未知/初始样本必须为 null，不能假充 0
		SampledAt:    time.Now().Unix(),
	}

	jsonBytes, err := Marshal(metric)
	require.NoError(t, err)

	jsonStr := string(jsonBytes)
	assert.Contains(t, jsonStr, `"usage_percent":null`, "UsagePercent should serialize to null when unmeasured")

	var decoded DetailedResourceMetric
	err = Unmarshal(jsonBytes, &decoded)
	require.NoError(t, err)
	assert.Nil(t, decoded.UsagePercent)
	assert.Equal(t, MetricStatusInitializing, decoded.Status)
}

func TestStaleStatusTransition(t *testing.T) {
	past := time.Now().Unix() - 40 // 40秒前，超过 25s 阈值
	used := uint64(104857600)
	pct := 25.5

	oldSnapshot := SystemStatus{
		SampledAt: past,
		CPU: DetailedResourceMetric{
			Status:       MetricStatusNormal,
			Scope:        MetricScopeContainer,
			Source:       "cgroupv2",
			UsagePercent: &pct,
			UsedValue:    &used,
			SampledAt:    past,
		},
		Memory: DetailedResourceMetric{
			Status:       MetricStatusNormal,
			Scope:        MetricScopeContainer,
			Source:       "cgroupv2",
			UsagePercent: &pct,
			UsedValue:    &used,
			SampledAt:    past,
		},
		Storage: DetailedResourceMetric{
			Status:       MetricStatusNormal,
			Scope:        MetricScopeHost,
			Source:       "statfs",
			UsagePercent: &pct,
			UsedValue:    &used,
			SampledAt:    past,
		},
	}

	latestSystemStatus.Store(oldSnapshot)

	status := GetSystemStatus()
	assert.Equal(t, MetricStatusStale, status.CPU.Status)
	assert.True(t, status.CPU.IsStale)
	assert.NotNil(t, status.CPU.UsagePercent)
	assert.Equal(t, pct, *status.CPU.UsagePercent, "Last valid usage percent must be preserved when stale")
	assert.Equal(t, past, status.CPU.SampledAt, "SampledAt must retain the real historical sample timestamp, not be bumped")

	assert.Equal(t, MetricStatusStale, status.Memory.Status)
	assert.True(t, status.Memory.IsStale)
	assert.Equal(t, MetricStatusStale, status.Storage.Status)
	assert.True(t, status.Storage.IsStale)
}

func TestCompiledVersionPreservedOnRuntimeOverride(t *testing.T) {
	origCompiled := CompiledVersion
	origVersion := Version
	origOverride := RuntimeVersionOverride
	defer func() {
		CompiledVersion = origCompiled
		Version = origVersion
		RuntimeVersionOverride = origOverride
		_ = os.Unsetenv("VERSION")
	}()

	CompiledVersion = "v1.0.0-immutable-compile"
	Version = "v1.0.0-immutable-compile"
	RuntimeVersionOverride = ""

	_ = os.Setenv("VERSION", "v1.0.0-runtime-override")
	InitEnv()

	info := GetBuildInfo()
	assert.Equal(t, "v1.0.0-immutable-compile", info.CompiledVersion, "CompiledVersion must not be wiped by runtime VERSION env")
	assert.Equal(t, "v1.0.0-runtime-override", info.Version, "Display version should reflect runtime override")
	assert.Equal(t, "v1.0.0-runtime-override", info.RuntimeOverride)
	assert.Equal(t, "unknown", info.DirtyStatus, "Dirty status should default to unknown when VCS metadata is missing")
}

func TestCgroupV2AncestorLimitsAndCPUSetIntersection(t *testing.T) {
	tmpDir := t.TempDir()
	podSliceDir := filepath.Join(tmpDir, "pod.slice")
	containerDir := filepath.Join(podSliceDir, "container")
	require.NoError(t, os.MkdirAll(containerDir, 0755))

	// 1. 叶子声明 6GiB，父级 Pod 声明 1.5GiB
	require.NoError(t, os.WriteFile(filepath.Join(containerDir, "memory.max"), []byte("6442450944\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(podSliceDir, "memory.max"), []byte("1610612736\n"), 0644))

	// 2. 叶子 cpuset 有效核数 2 核，父级配额 4 核
	require.NoError(t, os.WriteFile(filepath.Join(containerDir, "cpuset.cpus.effective"), []byte("0-1\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(podSliceDir, "cpu.max"), []byte("400000 100000\n"), 0644))

	// 验证内存祖先收紧识别
	effMem, parentMem := readCgroupV2MemoryMax(containerDir)
	require.NotNil(t, effMem)
	assert.Equal(t, uint64(1610612736), *effMem, "Effective memory limit must take the tighter pod ancestor (1.5GiB), not the leaf 6GiB")
	require.NotNil(t, parentMem)
	assert.Equal(t, uint64(1610612736), *parentMem, "Parent tighter capacity must be identified")

	// 验证 CPU 配额与 cpuset 取交集
	quota, cpuset := readCgroupV2CPUQuota(containerDir)
	assert.Equal(t, 4.0, quota)
	assert.Equal(t, 2.0, cpuset)
	assert.Equal(t, 2.0, math.Min(quota, cpuset), "Intersection of 4-core quota and 2-core cpuset must be 2 cores")
}
