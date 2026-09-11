//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// setDetached 让子进程脱离当前终端会话，
// 这样父进程（preview 子命令的"包装器"部分）退出后，
// 子进程（真正的 HTTP server，--foreground）不会被一起杀掉。
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
