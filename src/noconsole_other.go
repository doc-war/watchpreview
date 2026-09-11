//go:build !windows

package main

import "os/exec"

// hideConsole 非 Windows 平台无需处理：unix 的 sh -c 本就无窗口概念。
func hideConsole(cmd *exec.Cmd) {}
