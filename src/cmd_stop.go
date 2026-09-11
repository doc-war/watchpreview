package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

func runStopCommand(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	root := fs.String("root", ".", "预览的根目录")
	fs.Parse(args)

	canonical, err := canonicalizeRoot(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "watchpreview:", err)
		os.Exit(1)
	}

	id := computeID(canonical)

	instFile, err := instanceFilePath(id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "watchpreview:", err)
		os.Exit(1)
	}

	inst, ok := readInstanceFile(instFile)
	if !ok {
		fmt.Println("preview is not running")
		return
	}

	// 优先走优雅路径：HTTP 控制端点会让进程自己关 server、
	// 停 watcher、删状态文件，比直接杀进程干净。
	if tryHTTPStop(inst) {
		removeInstanceFile(instFile) // 双保险，防止进程侧清理失败
		fmt.Println("preview stopped")
		return
	}

	// 降级容错：HTTP stop 联系不上，说明状态文件可能已陈旧。
	switch {
	case pidAlive(inst.PID) && checkInstanceAlive(inst):
		// HTTP 控制端点核上了 id/root：确实是我们自己的实例，
		// 只是 stop 端点不通，作为最后手段用系统信号强制终止。
		if err := pidTerminate(inst.PID); err != nil {
			fmt.Fprintln(os.Stderr, "watchpreview: force-terminate failed:", err)
		} else {
			fmt.Println("watchpreview: graceful stop failed, force-terminated instance")
		}

	case pidAlive(inst.PID):
		// PID 在但 HTTP 核对不上：很可能是 PID 已被系统回收、复用给了
		// 另一个无关进程。强杀会误伤无辜进程，这里只清理状态文件、
		// 保留进程，让用户自行确认。
		fmt.Fprintf(os.Stderr,
			"watchpreview: cannot confirm pid %d still belongs to this instance, leaving it untouched (pid may have been reused by another process); terminate manually if it is a hung watchpreview\n",
			inst.PID)

	default:
		fmt.Println("watchpreview: cleaned up stale state (process was already gone)")
	}

	removeInstanceFile(instFile)
	fmt.Println("preview stopped")
}

func tryHTTPStop(inst Instance) bool {
	req, err := http.NewRequest(http.MethodPost, inst.URL+stopEndpointPath[1:], nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-WatchPreview-Token", inst.Token)

	client := &http.Client{Timeout: 2 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusNoContent
}
