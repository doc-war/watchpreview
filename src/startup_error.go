package main

import "os"

// 后台进程是 detached 状态、没有可见控制台，启动失败直接写 stderr
// 等于把诊断丢掉。这里通过 <id>.err 文件把真实原因转述给前台包装器，
// 父进程轮询期间一旦读到立即打印退出。
func reportStartupError(id string, msg string) {
	path, err := startupErrPath(id)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(msg), 0600)
}

// readStartupError 读出 <id>.err 的内容并顺手清掉，
// 避免陈旧错误文件干扰下一次启动。
func readStartupError(id string) (string, bool) {
	path, err := startupErrPath(id)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	_ = os.Remove(path)
	return string(data), true
}

// removeStartupError 清掉 <id>.err，防御上次失败残留的误导。
func removeStartupError(id string) {
	path, err := startupErrPath(id)
	if err == nil {
		_ = os.Remove(path)
	}
}
