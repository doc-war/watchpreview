package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const version = "v1.0.0"

func runPreviewCommand(args []string) {
	fs := flag.NewFlagSet("preview", flag.ExitOnError)

	root := fs.String("root", ".", "预览的根目录")
	host := fs.String("host", "127.0.0.1", "监听地址；局域网真机预览时可设为 0.0.0.0 或具体网卡 IP")
	fallback := fs.String("fallback", "none", "静态资源找不到时的回退策略：none | html-suffix | spa")
	ignore := fs.String("ignore", "", "额外忽略的路径片段，逗号分隔")
	port := fs.Int("port", 0, "固定端口，0 表示自动分配")
	open := fs.Bool("open", false, "启动后自动打开浏览器")

	// 内部标记：由本命令自己 spawn 自己时携带，用户不需要手动传。
	foreground := fs.Bool("foreground", false, "internal: run as the actual server process")

	fs.Parse(args)

	fallbackMode := FallbackMode(*fallback)
	switch fallbackMode {
	case FallbackNone, FallbackHTMLSuffix, FallbackSPA:
	default:
		fmt.Fprintln(os.Stderr, "watchpreview: invalid --fallback:", *fallback)
		os.Exit(1)
	}

	canonical, err := canonicalizeRoot(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "watchpreview:", err)
		os.Exit(1)
	}

	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "watchpreview: invalid root:", canonical)
		os.Exit(1)
	}

	id := computeID(canonical)

	instFile, err := instanceFilePath(id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "watchpreview:", err)
		os.Exit(1)
	}

	if *foreground {
		runForegroundServer(canonical, id, instFile, *host, fallbackMode, *ignore, *port)
		return
	}

	runPreviewWrapper(canonical, id, instFile, *host, *fallback, *ignore, *port, *open)
}

// runPreviewWrapper 负责幂等判断 + 拉起后台 server，本身立刻返回。
// 这是用户/上层框架实际调用的入口：`watchpreview preview --root <dir>`。
func runPreviewWrapper(canonical, id, instFile, host, fallback, ignore string, port int, open bool) {
	if existing, ok := readInstanceFile(instFile); ok {
		if checkInstanceAlive(existing) {
			// 幂等：已经有一个健康的实例在跑，直接复用。
			// 幂等键只有 root：行为参数只在首次启动时生效，
			// 本轮与已有实例不一致时逐条告警，但不中断、照常复用。
			for _, m := range configMismatches(existing, host, fallback, ignore, port) {
				fmt.Fprintln(os.Stderr, "watchpreview:", m)
			}
			fmt.Println(existing.URL)
			if open {
				openBrowser(existing.URL)
			}
			return
		}

		// 降级容错：状态文件存在，但 PID 已死 / 被复用 / HTTP 不响应，
		// 一律视为陈旧状态，清理后重新启动，而不是报错让用户手动处理。
		fmt.Fprintln(os.Stderr, "watchpreview: found stale instance state, cleaning up and restarting")
		removeInstanceFile(instFile)
	}

	selfPath, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "watchpreview:", err)
		os.Exit(1)
	}

	childArgs := []string{
		"preview",
		"--root", canonical,
		"--host", host,
		"--fallback", fallback,
		"--foreground",
	}

	if ignore != "" {
		childArgs = append(childArgs, "--ignore", ignore)
	}

	if port != 0 {
		childArgs = append(childArgs, "--port", strconv.Itoa(port))
	}

	cmd := exec.Command(selfPath, childArgs...)
	setDetached(cmd)

	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "watchpreview:", err)
		os.Exit(1)
	}

	// 轮询等待子进程写出 instance.json，超时 5 秒。
	// 期间一旦出现启动错误文件，立即转述真实原因退出，不用干等超时。
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)

		if inst, ok := readInstanceFile(instFile); ok && inst.PID == cmd.Process.Pid {
			fmt.Println(inst.URL)
			if open {
				openBrowser(inst.URL)
			}
			return
		}

		if msg, ok := readStartupError(id); ok {
			fmt.Fprintln(os.Stderr, "watchpreview: server failed to start:", msg)
			os.Exit(1)
		}
	}

	if msg, ok := readStartupError(id); ok {
		fmt.Fprintln(os.Stderr, "watchpreview: server failed to start:", msg)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "watchpreview: timeout waiting for server to start")
	os.Exit(1)
}

// runForegroundServer 是真正长期运行的 HTTP server 进程，
// 只应该由 runPreviewWrapper 通过 --foreground 拉起，不建议用户直接调用。
func runForegroundServer(root, id, instFile, host string, fallback FallbackMode, ignoreCSV string, port int) {
	token := randomToken()

	// 清掉上一次可能的启动错误残留，避免误导本轮诊断。
	removeStartupError(id)

	// 一次性绑定 listener：指定端口被占用、或自动选端口被抢的竞态
	// 都暴露在这里并立即上报，不再有"先找空闲端口、后绑定"之间的窗口。
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		reportStartupError(id, err.Error())
		os.Exit(1)
	}
	port = listener.Addr().(*net.TCPAddr).Port

	hub := NewReloadHub()
	preview := NewPreview(root, id, token, fallback, hub)
	preview.server = &http.Server{
		Addr:    fmt.Sprintf("%s:%d", host, port),
		Handler: preview.Handler(),
	}

	url := fmt.Sprintf("http://%s:%d/", host, port)

	inst := Instance{
		ID:       id,
		Root:     root,
		PID:      os.Getpid(),
		Port:     port,
		URL:      url,
		Token:    token,
		Host:     host,
		Fallback: string(fallback),
		Ignore:   ignoreCSV,
	}

	if err := writeInstanceFile(instFile, inst); err != nil {
		reportStartupError(id, err.Error())
		os.Exit(1)
	}

	// 状态文件已写出，启动错误文件不再需要。
	removeStartupError(id)

	var ignore []string
	if ignoreCSV != "" {
		ignore = strings.Split(ignoreCSV, ",")
	}

	watchCtx, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()

	watcher := NewWatcher(root, hub, ignore)

	go func() {
		if err := watcher.Run(watchCtx); err != nil {
			fmt.Fprintln(os.Stderr, "watchpreview: watcher:", err)
			preview.RequestStop()
		}
	}()

	go func() {
		err := preview.server.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "watchpreview: server:", err)
			preview.RequestStop()
		}
	}()

	fmt.Println("watchpreview", version)
	fmt.Println("root:", root)
	fmt.Println("url:", url)

	sigCtx, stopSig := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSig()

	select {
	case <-preview.Done():
	case <-sigCtx.Done():
	}

	cancelWatch()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelShutdown()

	_ = preview.server.Shutdown(shutdownCtx)

	removeInstanceFile(instFile)

	fmt.Println("watchpreview: stopped")
}

// configMismatches 对比已有实例与本轮调用的行为参数，返回不一致的告警描述。
//
// 幂等键只有 root：--host/--port/--fallback/--ignore 只在首次启动时生效，
// 复用已有实例时这些参数会被忽略。这里只做可观察性提示，不改变复用行为。
//
// 两个宽容规则，避免误报：
//   - 请求 port == 0 表示"自动分配/无要求"，不构成明确意图，不做比对；
//   - 已有实例的 host/fallback/ignore 为空（旧版本写入的状态文件缺这些字段），
//     无法还原当时的配置意图，跳过比对该项。
func configMismatches(inst Instance, host, fallback, ignore string, port int) []string {
	var msgs []string

	if inst.Host != "" && inst.Host != host {
		msgs = append(msgs, fmt.Sprintf("instance already running on host %s, ignoring --host %s", inst.Host, host))
	}

	if port != 0 && inst.Port != port {
		msgs = append(msgs, fmt.Sprintf("instance already running on port %d, ignoring --port %d", inst.Port, port))
	}

	if inst.Fallback != "" && inst.Fallback != fallback {
		msgs = append(msgs, fmt.Sprintf("instance already running with fallback %q, ignoring --fallback %q", inst.Fallback, fallback))
	}

	if inst.Ignore != "" && inst.Ignore != ignore {
		msgs = append(msgs, fmt.Sprintf("instance already running with ignore %q, ignoring --ignore %q", inst.Ignore, ignore))
	}

	return msgs
}
