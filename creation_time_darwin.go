//go:build darwin

package main

import (
	"os"
	"syscall"
	"time"
)

// getCreationTime 在 macOS 上返回文件的创建时间
func getCreationTime(info os.FileInfo) time.Time {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.ModTime() // 如果无法获取，则回退到修改时间
	}
	// stat.Birthtimespec 是创建时间
	return time.Unix(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec)
}
