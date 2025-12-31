package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"time"
)

// runCommand 运行一个 shell 命令并返回其标准输出和错误
func runCommand(name string, arg ...string) (string, string, error) {
	cmd := exec.Command(name, arg...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("command failed: %s %v, stdout: %s, stderr: %s, error: %w", name, arg, stdout.String(), stderr.String(), err)
	}
	return stdout.String(), stderr.String(), nil
}

// formatDuration 将秒数格式化为 HH:MM:SS 或 MM:SS
func formatDuration(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}
