package main

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// pidAlive 只回答"这个 PID 当前是否对应一个在跑的进程"，
// 不回答"这个进程是不是我认为的那个 watchpreview 实例"——
// 后一个问题需要结合 checkInstanceAlive 里的 HTTP 核对一起判断，
// 因为 PID 存在被操作系统回收复用给无关进程的可能性。
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid)).Output()
		if err != nil {
			return false
		}
		return strings.Contains(string(out), strconv.Itoa(pid))
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// unix 下 FindProcess 总是成功，真正的存活检查是发 signal 0：
	// 不会真的杀死进程，只是探测它是否存在、当前用户是否有权限操作它。
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// pidTerminate 是 stop 流程里的最后手段：
// 当 HTTP 优雅停止端点联系不上，但 PID 确实还活着时，
// 尽力用操作系统信号终止它，避免留下无法回收的孤儿进程。
func pidTerminate(pid int) error {
	if runtime.GOOS == "windows" {
		return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F").Run()
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	return process.Signal(syscall.SIGTERM)
}
