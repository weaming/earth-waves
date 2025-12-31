//go:build !darwin

package main

import (
	"os"
	"time"
)

// getCreationTime 为非 macOS 系统提供备用方案，返回文件的修改时间
func getCreationTime(info os.FileInfo) time.Time {
	// 对于非 macOS 系统，标准库无法直接获取创建时间，
	// 因此我们回退到使用修改时间。
	return info.ModTime()
}
