package common

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
)

var (
	// 编译期通过 -ldflags "-X 'github.com/QuantumNous/new-api/common.BuildRevision=...'" 注入
	CompiledVersion        = ""
	BuildRevision          = ""
	BuildTime              = ""
	BuildPlatform          = ""
	RuntimeVersionOverride = ""
)

func init() {
	if CompiledVersion == "" && Version != "" && Version != "v0.0.0" {
		CompiledVersion = Version
	}
}

// BuildInfo 真实构建信息
type BuildInfo struct {
	Version          string `json:"version"`
	CompiledVersion  string `json:"compiled_version"`
	RuntimeOverride  string `json:"runtime_override,omitempty"`
	Revision         string `json:"revision"`
	SnapshotID       string `json:"snapshot_id,omitempty"`
	FrontendRevision string `json:"frontend_revision,omitempty"`
	BuildTime        string `json:"build_time"`
	GoVersion        string `json:"go_version"`
	Platform         string `json:"platform"`
	Compiler         string `json:"compiler"`
	IsDirty          bool   `json:"is_dirty"`
	DirtyStatus      string `json:"dirty_status"` // "clean", "dirty", "unknown"
	HasVcs           bool   `json:"has_vcs"`
}

// GetBuildInfo 获取当前运行实例的真实构建与版本元数据
func GetBuildInfo() BuildInfo {
	platform := BuildPlatform
	if platform == "" {
		platform = fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
	}

	compiled := CompiledVersion
	if compiled == "" {
		if RuntimeVersionOverride != "" {
			compiled = "unknown"
		} else if Version != "" && Version != "v0.0.0" {
			compiled = Version
		} else {
			compiled = "unknown"
		}
	}

	info := BuildInfo{
		Version:          Version,
		CompiledVersion:  compiled,
		RuntimeOverride:  RuntimeVersionOverride,
		Revision:         BuildRevision,
		SnapshotID:       os.Getenv("SNAPSHOT_ID"),
		FrontendRevision: os.Getenv("FRONTEND_REVISION"),
		BuildTime:        BuildTime,
		GoVersion:        runtime.Version(),
		Platform:         platform,
		Compiler:         runtime.Compiler,
		DirtyStatus:      "unknown",
		HasVcs:           false,
	}

	// 尝试从 runtime/debug.ReadBuildInfo 补齐 revision 与 dirty 状态
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Revision == "" {
					info.Revision = s.Value
				}
			case "vcs.time":
				if info.BuildTime == "" {
					info.BuildTime = s.Value
				}
			case "vcs.modified":
				info.HasVcs = true
				if s.Value == "true" {
					info.IsDirty = true
					info.DirtyStatus = "dirty"
				} else if s.Value == "false" {
					info.IsDirty = false
					info.DirtyStatus = "clean"
				}
			}
		}
	}

	// 检查当前环境变量是否有覆盖
	if envVer := os.Getenv("VERSION"); envVer != "" && RuntimeVersionOverride == "" {
		info.RuntimeOverride = envVer
	}

	return info
}
