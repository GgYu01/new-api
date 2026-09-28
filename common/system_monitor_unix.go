//go:build !windows

package common

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// GetDiskSpaceInfo 获取指定或默认缓存/数据目录所在磁盘的空间信息 (Unix/Linux/macOS)
func GetDiskSpaceInfo() DiskSpaceInfo {
	return GetPathDiskSpaceInfo("")
}

// GetPathDiskSpaceInfo 获取指定路径所在挂载点的磁盘空间信息
func GetPathDiskSpaceInfo(targetPath string) DiskSpaceInfo {
	if targetPath == "" {
		targetPath = GetDiskCachePath()
		if targetPath == "" {
			if _, err := os.Stat("/data"); err == nil {
				targetPath = "/data"
			} else {
				targetPath = os.TempDir()
			}
		}
	}

	info := DiskSpaceInfo{}
	cleanPath := filepath.Clean(targetPath)

	var stat unix.Statfs_t
	err := unix.Statfs(cleanPath, &stat)
	if err != nil {
		// 若指定路径不存在，尝试上一级目录
		dir := filepath.Dir(cleanPath)
		err = unix.Statfs(dir, &stat)
		if err != nil {
			return info
		}
	}

	// 计算磁盘空间 (兼容不同平台的 Bsize 字段类型)
	bsize := uint64(stat.Bsize)
	info.Total = uint64(stat.Blocks) * bsize
	info.Free = uint64(stat.Bavail) * bsize
	info.Used = info.Total - uint64(stat.Bfree)*bsize

	if info.Total > 0 {
		info.UsedPercent = float64(info.Used) / float64(info.Total) * 100
	}

	return info
}
