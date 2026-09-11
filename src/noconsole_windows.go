//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// createNoWindow 让 onChangeCommand 的 cmd 在隐藏控制台里运行，
// 避免每次源码变更编译时闪出一个 cmd.exe 窗口（watchpreview 本身
// 以 DETACHED_PROCESS 启动、无控制台，spawn 的 console 子进程若不加
// 此标志，Windows 会为新进程分配一个新控制台窗口）。
const createNoWindow uint32 = 0x08000000

// hideConsole 把 createNoWindow 合并进 CreationFlags，
// 不与 detach_windows.go 的 createNewProcessGroup|detachedProcess 冲突（按位或）。
func hideConsole(cmd *exec.Cmd) {
	flags := createNoWindow
	if cmd.SysProcAttr != nil {
		flags |= cmd.SysProcAttr.CreationFlags
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
}
