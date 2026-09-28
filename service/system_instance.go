package service

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"

	"github.com/bytedance/gopkg/util/gopool"
)

const systemInstanceReportInterval = 30 * time.Second

var systemInstanceReporterOnce sync.Once

type SystemInstanceInfo struct {
	SchemaVersion int                       `json:"schema_version"`
	Node          common.NodeIdentity       `json:"node"`
	Role          SystemInstanceRoleInfo    `json:"role"`
	Runtime       SystemInstanceRuntimeInfo `json:"runtime"`
	Host          SystemInstanceHostInfo    `json:"host"`
	Resources     SystemInstanceResources   `json:"resources"`
	Extra         map[string]any            `json:"extra,omitempty"`
}

type SystemInstanceRoleInfo struct {
	IsMaster bool `json:"is_master"`
}

type SystemInstanceRuntimeInfo struct {
	Version   string `json:"version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	StartedAt int64  `json:"started_at"`
}

type SystemInstanceHostInfo struct {
	Hostname string `json:"hostname"`
}

type SystemInstanceResources struct {
	CPU     SystemInstanceResourceUsage  `json:"cpu"`
	Memory  SystemInstanceResourceUsage  `json:"memory"`
	Storage SystemInstanceStorageMetrics `json:"storage"`
}

type SystemInstanceResourceUsage struct {
	Status        string   `json:"status,omitempty"`
	Scope         string   `json:"scope,omitempty"`
	Source        string   `json:"source,omitempty"`
	UsagePercent  *float64 `json:"usage_percent"`
	UsedValue     *uint64  `json:"used_value,omitempty"`
	TotalCapacity *uint64  `json:"total_capacity,omitempty"`
	UsedCores     *float64 `json:"used_cores,omitempty"`
	TotalCores    *float64 `json:"total_cores,omitempty"`
	ProcessRSS    *uint64  `json:"process_rss,omitempty"`
	GoHeapAlloc   *uint64  `json:"go_heap_alloc,omitempty"`
	Unit          string   `json:"unit,omitempty"`
	SampledAt     int64    `json:"sampled_at,omitempty"`
	IntervalMs    int64    `json:"interval_ms,omitempty"`
	LastError     string   `json:"last_error,omitempty"`
}

type SystemInstanceStorageMetrics struct {
	Status      string   `json:"status,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	Source      string   `json:"source,omitempty"`
	MountPoint  string   `json:"mount_point,omitempty"`
	TotalBytes  uint64   `json:"total_bytes"`
	UsedBytes   uint64   `json:"used_bytes"`
	FreeBytes   uint64   `json:"free_bytes"`
	UsedPercent *float64 `json:"used_percent"`
	SampledAt   int64    `json:"sampled_at,omitempty"`
	LastError   string   `json:"last_error,omitempty"`
}

func StartSystemInstanceReporter() {
	systemInstanceReporterOnce.Do(func() {
		gopool.Go(func() {
			reportSystemInstanceWithLog()

			ticker := time.NewTicker(systemInstanceReportInterval)
			defer ticker.Stop()
			for range ticker.C {
				reportSystemInstanceWithLog()
			}
		})
	})
}

func ReportCurrentSystemInstance() error {
	identity := common.GetNodeIdentity()
	hostname, hostnameErr := os.Hostname()
	if strings.TrimSpace(identity.Name) == "" {
		if hostnameErr != nil || strings.TrimSpace(hostname) == "" {
			return fmt.Errorf("system instance node name is empty")
		}
		identity.Name = hostname
		identity.Source = common.NodeNameSourceHostname
		identity.ManuallyConfigured = false
		identity.ShouldConfigureManually = true
	}

	systemStatus := common.GetSystemStatus()
	cpuMetric := systemStatus.CPU
	memMetric := systemStatus.Memory
	storageMetric := systemStatus.Storage
	diskInfo := common.GetDiskSpaceInfo()

	info := SystemInstanceInfo{
		SchemaVersion: 1,
		Node:          identity,
		Role: SystemInstanceRoleInfo{
			IsMaster: common.IsMasterNode,
		},
		Runtime: SystemInstanceRuntimeInfo{
			Version:   common.Version,
			GOOS:      runtime.GOOS,
			GOARCH:    runtime.GOARCH,
			StartedAt: common.StartTime,
		},
		Host: SystemInstanceHostInfo{
			Hostname: hostname,
		},
		Resources: SystemInstanceResources{
			CPU: SystemInstanceResourceUsage{
				Status:        cpuMetric.Status,
				Scope:         cpuMetric.Scope,
				Source:        cpuMetric.Source,
				UsagePercent:  cpuMetric.UsagePercent,
				UsedValue:     cpuMetric.UsedValue,
				TotalCapacity: cpuMetric.TotalCapacity,
				UsedCores:     cpuMetric.UsedCores,
				TotalCores:    cpuMetric.TotalCores,
				ProcessRSS:    cpuMetric.ProcessRSS,
				GoHeapAlloc:   cpuMetric.GoHeapAlloc,
				Unit:          cpuMetric.Unit,
				SampledAt:     cpuMetric.SampledAt,
				IntervalMs:    cpuMetric.IntervalMs,
				LastError:     cpuMetric.LastError,
			},
			Memory: SystemInstanceResourceUsage{
				Status:        memMetric.Status,
				Scope:         memMetric.Scope,
				Source:        memMetric.Source,
				UsagePercent:  memMetric.UsagePercent,
				UsedValue:     memMetric.UsedValue,
				TotalCapacity: memMetric.TotalCapacity,
				ProcessRSS:    memMetric.ProcessRSS,
				GoHeapAlloc:   memMetric.GoHeapAlloc,
				Unit:          memMetric.Unit,
				SampledAt:     memMetric.SampledAt,
				IntervalMs:    memMetric.IntervalMs,
				LastError:     memMetric.LastError,
			},
			Storage: SystemInstanceStorageMetrics{
				Status:      storageMetric.Status,
				Scope:       storageMetric.Scope,
				Source:      storageMetric.Source,
				MountPoint:  storageMetric.MountPoint,
				TotalBytes:  diskInfo.Total,
				UsedBytes:   diskInfo.Used,
				FreeBytes:   diskInfo.Free,
				UsedPercent: storageMetric.UsagePercent,
				SampledAt:   storageMetric.SampledAt,
				LastError:   storageMetric.LastError,
			},
		},
	}
	return model.UpsertSystemInstance(identity.Name, info, common.StartTime, common.GetTimestamp())
}

func reportSystemInstanceWithLog() {
	if err := ReportCurrentSystemInstance(); err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf("system instance report failed: %v", err))
	}
}
