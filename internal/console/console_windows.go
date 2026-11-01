//go:build windows

// Package console 负责控制台输出环境初始化。
package console

import (
	"syscall"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleCP       = kernel32.NewProc("SetConsoleCP")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
)

func init() {
	// 控制台切换为 UTF-8 代码页，保证中文日志正常显示
	procSetConsoleCP.Call(65001)
	procSetConsoleOutputCP.Call(65001)
}
