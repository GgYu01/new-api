package common

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/mem"
)

// DiskSpaceInfo 磁盘空间信息
type DiskSpaceInfo struct {
	Total       uint64  `json:"total"`
	Free        uint64  `json:"free"`
	Used        uint64  `json:"used"`
	UsedPercent float64 `json:"used_percent"`
}

// MetricStatus 指标采集状态
const (
	MetricStatusInitializing = "initializing"
	MetricStatusNormal       = "normal"
	MetricStatusUnavailable  = "unavailable"
	MetricStatusDisabled     = "disabled"
	MetricStatusStale        = "stale"
)

// MetricScope 采集口径
const (
	MetricScopeContainer = "container"
	MetricScopeProcess   = "process"
	MetricScopeHost      = "host"
	MetricScopeUnknown   = "unknown"
)

// DetailedResourceMetric 详细资源遥测指标项
type DetailedResourceMetric struct {
	Status         string   `json:"status"`
	Scope          string   `json:"scope"`
	Source         string   `json:"source"`
	UsagePercent   *float64 `json:"usage_percent"`
	UsedValue      *uint64  `json:"used_value,omitempty"`
	TotalCapacity  *uint64  `json:"total_capacity,omitempty"`
	ParentCapacity *uint64  `json:"parent_capacity,omitempty"`
	UsedCores      *float64 `json:"used_cores,omitempty"`
	TotalCores     *float64 `json:"total_cores,omitempty"`
	ProcessRSS     *uint64  `json:"process_rss,omitempty"`
	GoHeapAlloc    *uint64  `json:"go_heap_alloc,omitempty"`
	MountPoint     string   `json:"mount_point,omitempty"`
	Unit           string   `json:"unit,omitempty"`
	SampledAt      int64    `json:"sampled_at"`
	IntervalMs     int64    `json:"interval_ms"`
	LastError      string   `json:"last_error,omitempty"`
	IsStale        bool     `json:"is_stale,omitempty"`
}

// SystemStatus 系统状态快照（只读遥测快照）
type SystemStatus struct {
	CPUUsage    float64 `json:"cpu_usage"`
	MemoryUsage float64 `json:"memory_usage"`
	DiskUsage   float64 `json:"disk_usage"`

	CPU     DetailedResourceMetric `json:"cpu"`
	Memory  DetailedResourceMetric `json:"memory"`
	Storage DetailedResourceMetric `json:"storage"`

	SampledAt int64 `json:"sampled_at"`
}

var (
	latestSystemStatus atomic.Value
	telemetryOnce      sync.Once
	telemetryRunning   int32
	telemetryStopChan  chan struct{}

	// CPU 历史累积点（用于算 Delta）
	lastCPUTimeLock sync.Mutex
	lastCPUNano     int64
	lastWallTime    time.Time
	lastCPUValid    bool

	// 遥测配置
	telemetryInterval = 10 * time.Second
)

func init() {
	now := time.Now().Unix()
	initialStatus := SystemStatus{
		CPU: DetailedResourceMetric{
			Status:    MetricStatusInitializing,
			Scope:     MetricScopeUnknown,
			Source:    "uninitialized",
			SampledAt: now,
		},
		Memory: DetailedResourceMetric{
			Status:    MetricStatusInitializing,
			Scope:     MetricScopeUnknown,
			Source:    "uninitialized",
			SampledAt: now,
		},
		Storage: DetailedResourceMetric{
			Status:    MetricStatusInitializing,
			Scope:     MetricScopeUnknown,
			Source:    "uninitialized",
			SampledAt: now,
		},
		SampledAt: now,
	}
	latestSystemStatus.Store(initialStatus)
}

// StartSystemMonitor 启动系统监控（只读遥测独立轮询，不依赖拦截开关）
func StartSystemMonitor() {
	telemetryOnce.Do(func() {
		telemetryStopChan = make(chan struct{})
		atomic.StoreInt32(&telemetryRunning, 1)

		// 启动后立即更新一次初始快照
		updateSystemStatus()

		go func() {
			ticker := time.NewTicker(telemetryInterval)
			defer ticker.Stop()
			for {
				select {
				case <-telemetryStopChan:
					return
				case <-ticker.C:
					updateSystemStatus()
				}
			}
		}()
	})
}

// StopSystemMonitor 停止系统监控
func StopSystemMonitor() {
	if atomic.CompareAndSwapInt32(&telemetryRunning, 1, 0) {
		close(telemetryStopChan)
	}
}

// updateSystemStatus 更新只读遥测快照
func updateSystemStatus() {
	now := time.Now()
	nowUnix := now.Unix()

	cpuMetric := sampleCPU(now)
	memMetric := sampleMemory(now)
	diskInfo := GetDiskSpaceInfo()
	storageMetric := buildStorageMetric(diskInfo, nowUnix)

	status := SystemStatus{
		CPUUsage:    getPercentOrZero(cpuMetric.UsagePercent),
		MemoryUsage: getPercentOrZero(memMetric.UsagePercent),
		DiskUsage:   getPercentOrZero(storageMetric.UsagePercent),
		CPU:         cpuMetric,
		Memory:      memMetric,
		Storage:     storageMetric,
		SampledAt:   nowUnix,
	}

	latestSystemStatus.Store(status)
}

func getPercentOrZero(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// sampleCPU 采集容器/宿主机 CPU
func sampleCPU(now time.Time) DetailedResourceMetric {
	sampledAt := now.Unix()

	if runtime.GOOS == "linux" {
		if metric, ok := sampleCPULinuxCgroup(now); ok {
			return metric
		}
	}

	// Fallback 到宿主机 gopsutil
	lastCPUTimeLock.Lock()
	defer lastCPUTimeLock.Unlock()

	percents, err := cpu.Percent(0, false)
	if err != nil || len(percents) == 0 {
		return DetailedResourceMetric{
			Status:     MetricStatusUnavailable,
			Scope:      MetricScopeHost,
			Source:     "gopsutil",
			SampledAt:  sampledAt,
			IntervalMs: 10000,
			LastError:  fmt.Sprintf("cpu.Percent failed: %v", err),
		}
	}

	val := percents[0]
	numCPU := float64(runtime.NumCPU())
	usedCores := (val / 100.0) * numCPU

	return DetailedResourceMetric{
		Status:       MetricStatusNormal,
		Scope:        MetricScopeHost,
		Source:       "gopsutil/host",
		UsagePercent: &val,
		UsedCores:    &usedCores,
		TotalCores:   &numCPU,
		Unit:         "percent",
		SampledAt:    sampledAt,
		IntervalMs:   10000,
	}
}

// sampleCPULinuxCgroup 从 Linux cgroup 采集累积 CPU 时间并按两点单调时间算核数与百分比
func sampleCPULinuxCgroup(now time.Time) (DetailedResourceMetric, bool) {
	sampledAt := now.Unix()
	cgroupDir := findLinuxCgroupDir()

	usageUsec, errV2 := readCgroupV2CPUUsage(cgroupDir)
	source := "cgroupv2"
	var totalCores float64 = 0
	var hasQuota bool = false

	if errV2 == nil {
		quotaCores, cpusetCores := readCgroupV2CPUQuota(cgroupDir)
		if quotaCores > 0 && cpusetCores > 0 {
			// 取配额与亲和性/有效cpuset交集的更紧约束
			totalCores = math.Min(quotaCores, cpusetCores)
			hasQuota = true
		} else if quotaCores > 0 {
			totalCores = quotaCores
			hasQuota = true
		} else if cpusetCores > 0 {
			totalCores = cpusetCores
		} else {
			totalCores = float64(runtime.NumCPU())
		}
	} else {
		usageNs, errV1 := readCgroupV1CPUUsage(cgroupDir)
		if errV1 != nil {
			return DetailedResourceMetric{}, false
		}
		usageUsec = usageNs / 1000
		source = "cgroupv1"
		quotaCores := readCgroupV1CPUQuota(cgroupDir)
		cpusetCores := readCgroupCPUSet(cgroupDir)
		if quotaCores > 0 && cpusetCores > 0 {
			totalCores = math.Min(quotaCores, cpusetCores)
			hasQuota = true
		} else if quotaCores > 0 {
			totalCores = quotaCores
			hasQuota = true
		} else if cpusetCores > 0 {
			totalCores = cpusetCores
		} else {
			totalCores = float64(runtime.NumCPU())
		}
	}

	currentCPUNano := int64(usageUsec) * 1000

	lastCPUTimeLock.Lock()
	defer lastCPUTimeLock.Unlock()

	if !lastCPUValid {
		lastCPUNano = currentCPUNano
		lastWallTime = now
		lastCPUValid = true
		return DetailedResourceMetric{
			Status:     MetricStatusInitializing,
			Scope:      MetricScopeContainer,
			Source:     source,
			SampledAt:  sampledAt,
			IntervalMs: 0,
		}, true
	}

	deltaWall := now.Sub(lastWallTime)
	deltaCPU := currentCPUNano - lastCPUNano

	// 时钟倒退或容器重建/计数 reset
	if deltaWall <= 0 || deltaCPU < 0 {
		lastCPUNano = currentCPUNano
		lastWallTime = now
		return DetailedResourceMetric{
			Status:     MetricStatusInitializing,
			Scope:      MetricScopeContainer,
			Source:     source,
			SampledAt:  sampledAt,
			IntervalMs: 0,
		}, true
	}

	lastCPUNano = currentCPUNano
	lastWallTime = now

	deltaWallSec := deltaWall.Seconds()
	usedCores := (float64(deltaCPU) / 1e9) / deltaWallSec
	if usedCores < 0 {
		usedCores = 0
	}

	var pct *float64
	if totalCores > 0 {
		p := (usedCores / totalCores) * 100.0
		if p > 100.0 && hasQuota {
			p = 100.0
		}
		pct = &p
	}

	usedNano := uint64(currentCPUNano)
	return DetailedResourceMetric{
		Status:        MetricStatusNormal,
		Scope:         MetricScopeContainer,
		Source:        source,
		UsagePercent:  pct,
		UsedValue:     &usedNano,
		UsedCores:     &usedCores,
		TotalCores:    &totalCores,
		TotalCapacity: uint64Ptr(uint64(totalCores * 100)),
		Unit:          "percent",
		SampledAt:     sampledAt,
		IntervalMs:    deltaWall.Milliseconds(),
	}, true
}

func uint64Ptr(v uint64) *uint64 {
	return &v
}

func findLinuxCgroupDir() string {
	// 检查 /proc/self/cgroup
	data, err := os.ReadFile("/proc/self/cgroup")
	if err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) == 3 {
				relPath := strings.TrimSpace(parts[2])
				if relPath != "" && relPath != "/" {
					candidate := filepath.Join("/sys/fs/cgroup", relPath)
					if fi, statErr := os.Stat(candidate); statErr == nil && fi.IsDir() {
						return candidate
					}
				}
			}
		}
	}
	return "/sys/fs/cgroup"
}

func readCgroupV2CPUUsage(baseDir string) (uint64, error) {
	paths := []string{
		filepath.Join(baseDir, "cpu.stat"),
		"/sys/fs/cgroup/cpu.stat",
	}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "usage_usec ") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					val, err := strconv.ParseUint(parts[1], 10, 64)
					if err == nil {
						return val, nil
					}
				}
			}
		}
	}
	return 0, fmt.Errorf("cgroupv2 cpu.stat not found or unreadable")
}

func getCgroupV2Ancestors(baseDir string) []string {
	var dirs []string
	curr := filepath.Clean(baseDir)
	for {
		if fi, err := os.Stat(curr); err == nil && fi.IsDir() {
			dirs = append(dirs, curr)
		}
		if curr == "/sys/fs/cgroup" || curr == "/" || curr == "." {
			break
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	if len(dirs) == 0 {
		dirs = append(dirs, "/sys/fs/cgroup")
	}
	return dirs
}

func readCgroupV2CPUQuota(baseDir string) (float64, float64) {
	dirs := getCgroupV2Ancestors(baseDir)
	var minQuota float64 = 0
	var minCPUSet float64 = 0

	for _, d := range dirs {
		// 1. Quota 从 cpu.max 读取
		p := filepath.Join(d, "cpu.max")
		data, err := os.ReadFile(p)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) >= 2 && fields[0] != "max" {
				quota, err1 := strconv.ParseFloat(fields[0], 64)
				period, err2 := strconv.ParseFloat(fields[1], 64)
				if err1 == nil && err2 == nil && period > 0 {
					q := quota / period
					if q > 0 && (minQuota == 0 || q < minQuota) {
						minQuota = q
					}
				}
			}
		}

		// 2. CPUSet 从 cpuset.cpus.effective 或 cpuset.cpus 读取
		setPaths := []string{
			filepath.Join(d, "cpuset.cpus.effective"),
			filepath.Join(d, "cpuset.cpus"),
		}
		for _, sp := range setPaths {
			sData, errS := os.ReadFile(sp)
			if errS == nil {
				content := strings.TrimSpace(string(sData))
				if count := parseCPUSetRange(content); count > 0 {
					c := float64(count)
					if minCPUSet == 0 || c < minCPUSet {
						minCPUSet = c
					}
					break
				}
			}
		}
	}
	return minQuota, minCPUSet
}

func readCgroupV1CPUUsage(baseDir string) (uint64, error) {
	paths := []string{
		filepath.Join(baseDir, "cpuacct.usage"),
		"/sys/fs/cgroup/cpu/cpuacct.usage",
		"/sys/fs/cgroup/cpuacct/cpuacct.usage",
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			val, errParse := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
			if errParse == nil {
				return val, nil
			}
		}
	}
	return 0, fmt.Errorf("cgroupv1 cpuacct.usage not found")
}

func readCgroupV1CPUQuota(baseDir string) float64 {
	paths := []string{
		filepath.Join(baseDir, "cpu.cfs_quota_us"),
		"/sys/fs/cgroup/cpu/cpu.cfs_quota_us",
	}
	for _, p := range paths {
		quotaData, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		periodPath := strings.Replace(p, "cpu.cfs_quota_us", "cpu.cfs_period_us", 1)
		periodData, errP := os.ReadFile(periodPath)
		if errP != nil {
			continue
		}
		quota, err1 := strconv.ParseFloat(strings.TrimSpace(string(quotaData)), 64)
		period, err2 := strconv.ParseFloat(strings.TrimSpace(string(periodData)), 64)
		if err1 == nil && err2 == nil && quota > 0 && period > 0 {
			return quota / period
		}
	}
	return 0
}

func readCgroupCPUSet(baseDir string) float64 {
	paths := []string{
		filepath.Join(baseDir, "cpuset.cpus.effective"),
		filepath.Join(baseDir, "cpuset.cpus"),
		"/sys/fs/cgroup/cpuset.cpus.effective",
		"/sys/fs/cgroup/cpuset/cpuset.cpus",
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			content := strings.TrimSpace(string(data))
			if count := parseCPUSetRange(content); count > 0 {
				return float64(count)
			}
		}
	}
	return 0
}

func parseCPUSetRange(content string) int {
	if content == "" {
		return 0
	}
	total := 0
	groups := strings.Split(content, ",")
	for _, g := range groups {
		parts := strings.Split(strings.TrimSpace(g), "-")
		if len(parts) == 1 {
			if _, err := strconv.Atoi(parts[0]); err == nil {
				total++
			}
		} else if len(parts) == 2 {
			start, err1 := strconv.Atoi(parts[0])
			end, err2 := strconv.Atoi(parts[1])
			if err1 == nil && err2 == nil && end >= start {
				total += (end - start + 1)
			}
		}
	}
	return total
}

// sampleMemory 采集容器/进程/宿主机内存
func sampleMemory(now time.Time) DetailedResourceMetric {
	sampledAt := now.Unix()
	var rssBytes *uint64
	var heapAllocBytes *uint64

	// 获取 Go 堆内存
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	heapAllocBytes = &memStats.Alloc

	// 获取进程物理内存 (RSS)
	if rss := readProcessRSS(); rss > 0 {
		rssBytes = &rss
	}

	if runtime.GOOS == "linux" {
		if metric, ok := sampleMemoryLinuxCgroup(sampledAt, rssBytes, heapAllocBytes); ok {
			return metric
		}
	}

	// Fallback 到宿主机内存
	vMem, err := mem.VirtualMemory()
	if err != nil {
		return DetailedResourceMetric{
			Status:      MetricStatusUnavailable,
			Scope:       MetricScopeHost,
			Source:      "gopsutil",
			ProcessRSS:  rssBytes,
			GoHeapAlloc: heapAllocBytes,
			SampledAt:   sampledAt,
			LastError:   fmt.Sprintf("mem.VirtualMemory failed: %v", err),
		}
	}

	used := vMem.Used
	total := vMem.Total
	pct := vMem.UsedPercent

	return DetailedResourceMetric{
		Status:        MetricStatusNormal,
		Scope:         MetricScopeHost,
		Source:        "gopsutil/host",
		UsagePercent:  &pct,
		UsedValue:     &used,
		TotalCapacity: &total,
		ProcessRSS:    rssBytes,
		GoHeapAlloc:   heapAllocBytes,
		Unit:          "bytes",
		SampledAt:     sampledAt,
	}
}

func readProcessRSS() uint64 {
	// 读取 /proc/self/statm (第二项是 RSS 页面数)
	data, err := os.ReadFile("/proc/self/statm")
	if err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 2 {
			pages, errP := strconv.ParseUint(fields[1], 10, 64)
			if errP == nil {
				return pages * uint64(os.Getpagesize())
			}
		}
	}

	// Fallback: 读取 /proc/self/status VmRSS
	statusData, errStatus := os.ReadFile("/proc/self/status")
	if errStatus == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(statusData)))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, errK := strconv.ParseUint(fields[1], 10, 64)
					if errK == nil {
						return kb * 1024
					}
				}
			}
		}
	}
	return 0
}

// sampleMemoryLinuxCgroup 从 cgroup v2 / v1 采集容器真实使用与上限
func sampleMemoryLinuxCgroup(sampledAt int64, rssBytes, heapAllocBytes *uint64) (DetailedResourceMetric, bool) {
	cgroupDir := findLinuxCgroupDir()

	// 1. 尝试 cgroup v2: memory.current
	currentBytes, errCurrent := readUint64FromPaths([]string{
		filepath.Join(cgroupDir, "memory.current"),
		"/sys/fs/cgroup/memory.current",
	})
	if errCurrent == nil {
		effectiveMax, parentMax := readCgroupV2MemoryMax(cgroupDir)
		var pct *float64
		if effectiveMax != nil && *effectiveMax > 0 {
			p := (float64(currentBytes) / float64(*effectiveMax)) * 100.0
			if p > 100.0 {
				p = 100.0
			}
			pct = &p
		}

		return DetailedResourceMetric{
			Status:         MetricStatusNormal,
			Scope:          MetricScopeContainer,
			Source:         "cgroupv2",
			UsagePercent:   pct,
			UsedValue:      &currentBytes,
			TotalCapacity:  effectiveMax,
			ParentCapacity: parentMax,
			ProcessRSS:     rssBytes,
			GoHeapAlloc:    heapAllocBytes,
			Unit:           "bytes",
			SampledAt:      sampledAt,
		}, true
	}

	// 2. 尝试 cgroup v1: memory.usage_in_bytes
	currentV1, errV1 := readUint64FromPaths([]string{
		filepath.Join(cgroupDir, "memory.usage_in_bytes"),
		"/sys/fs/cgroup/memory/memory.usage_in_bytes",
	})
	if errV1 == nil {
		limitV1 := readCgroupV1MemoryLimit(cgroupDir)
		var pct *float64
		if limitV1 != nil && *limitV1 > 0 {
			p := (float64(currentV1) / float64(*limitV1)) * 100.0
			if p > 100.0 {
				p = 100.0
			}
			pct = &p
		}

		return DetailedResourceMetric{
			Status:        MetricStatusNormal,
			Scope:         MetricScopeContainer,
			Source:        "cgroupv1",
			UsagePercent:  pct,
			UsedValue:     &currentV1,
			TotalCapacity: limitV1,
			ProcessRSS:    rssBytes,
			GoHeapAlloc:   heapAllocBytes,
			Unit:          "bytes",
			SampledAt:     sampledAt,
		}, true
	}

	return DetailedResourceMetric{}, false
}

func readCgroupV2MemoryMax(baseDir string) (effectiveMax *uint64, parentMax *uint64) {
	dirs := getCgroupV2Ancestors(baseDir)
	var leafMax *uint64
	var minLimit *uint64
	var tighterParent *uint64

	for i, d := range dirs {
		p := filepath.Join(d, "memory.max")
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		str := strings.TrimSpace(string(data))
		if str == "max" || str == "" {
			continue
		}
		val, err := strconv.ParseUint(str, 10, 64)
		if err == nil && val > 0 {
			if i == 0 {
				v := val
				leafMax = &v
			}
			if minLimit == nil || val < *minLimit {
				v := val
				minLimit = &v
				if i > 0 {
					tighterParent = &v
				}
			}
		}
	}

	if minLimit != nil {
		return minLimit, tighterParent
	}
	return leafMax, nil
}

func readCgroupV1MemoryLimit(baseDir string) *uint64 {
	paths := []string{
		filepath.Join(baseDir, "memory.limit_in_bytes"),
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	}
	for _, p := range paths {
		val, err := readUint64File(p)
		if err == nil {
			// cgroup v1 无限制时通常为 9223372036854771712 或 18446744073709551615
			if val >= (1 << 62) {
				return nil
			}
			return &val
		}
	}
	return nil
}

func readUint64FromPaths(paths []string) (uint64, error) {
	for _, p := range paths {
		val, err := readUint64File(p)
		if err == nil {
			return val, nil
		}
	}
	return 0, fmt.Errorf("no valid uint64 file found in %v", paths)
}

func readUint64File(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

func buildStorageMetric(diskInfo DiskSpaceInfo, sampledAt int64) DetailedResourceMetric {
	if diskInfo.Total == 0 {
		return DetailedResourceMetric{
			Status:     MetricStatusUnavailable,
			Scope:      MetricScopeHost,
			Source:     "statfs",
			SampledAt:  sampledAt,
			LastError:  "statfs returned 0 total bytes",
		}
	}

	cacheDir := GetDiskCacheDir()
	if cacheDir == "" {
		if _, err := os.Stat("/data"); err == nil {
			cacheDir = "/data"
		} else {
			cacheDir = os.TempDir()
		}
	}

	pct := diskInfo.UsedPercent
	total := diskInfo.Total
	used := diskInfo.Used

	return DetailedResourceMetric{
		Status:        MetricStatusNormal,
		Scope:         MetricScopeHost,
		Source:        filepath.Clean(cacheDir),
		MountPoint:    filepath.Clean(cacheDir),
		UsagePercent:  &pct,
		UsedValue:     &used,
		TotalCapacity: &total,
		Unit:          "bytes",
		SampledAt:     sampledAt,
	}
}

// GetSystemStatus 获取当前系统状态快照（快速只读读取，过期按时间戳返回 stale 状态）
func GetSystemStatus() SystemStatus {
	val := latestSystemStatus.Load()
	if val == nil {
		return SystemStatus{}
	}
	status := val.(SystemStatus)
	nowUnix := time.Now().Unix()

	// 遥测快照超过 2.5 个采样周期（25秒）未刷新时，将 normal 标记为 stale，但保留最后真实采集值
	staleThreshold := int64(25)
	if nowUnix-status.SampledAt > staleThreshold {
		if status.CPU.Status == MetricStatusNormal {
			status.CPU.Status = MetricStatusStale
			status.CPU.IsStale = true
		}
		if status.Memory.Status == MetricStatusNormal {
			status.Memory.Status = MetricStatusStale
			status.Memory.IsStale = true
		}
		if status.Storage.Status == MetricStatusNormal {
			status.Storage.Status = MetricStatusStale
			status.Storage.IsStale = true
		}
	}
	return status
}
