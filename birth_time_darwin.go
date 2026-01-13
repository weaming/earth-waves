package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"
)

var setfileChecked = false

// SetBirthTime sets the creation and modification time for a file on macOS.
// It attempts to use the 'setfile' command-line tool, falling back to os.Chtimes.
func SetBirthTime(path string, t time.Time) error {
	if !setfileChecked {
		_, err := exec.LookPath("setfile")
		if err != nil {
			log.Printf("Warning: 'setfile' command not found. To enable setting creation time, install Xcode Command Line Tools: 'xcode-select --install'. Falling back to setting modification time only.")
			return fmt.Errorf("setfile command not found")
		}
		setfileChecked = true
	}

	// Use setfile if available
	// setfile requires the date in "MM/dd/yyyy hh:mm:ss" format.
	format := "01/02/2006 15:04:05"
	dateStr := t.Format(format)

	cmd := exec.Command("setfile", "-d", dateStr, "-m", dateStr, path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("setfile command failed for %s: %w. Output: %s", path, err, string(output))
	}
	return nil
}

// GetBirthTime 在 macOS 上返回文件的创建时间
func GetBirthTime(info os.FileInfo) time.Time {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.ModTime() // 如果无法获取，则回退到修改时间
	}
	// stat.Birthtimespec 是创建时间
	return time.Unix(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec)
}
