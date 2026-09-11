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
	"path/filepath"
	"syscall"
	"time"
)

// version 由发布流程通过 ldflags 注入（.goreleaser.yaml 的
// -X main.version=<tag>），本地 go build 时为默认值 "dev"。
// 必须用变量而不是常量：-X 只能注入可变变量的值。
var version = "dev"

// runPreviewCommand 是 `watchpreview preview` 的唯一入口。
// 配置只来自 --config 文件；不带 --config 时退化为"当前目录静态预览"。
func runPreviewCommand(args []string) {
	fs := flag.NewFlagSet("preview", flag.ExitOnError)
	configPath := fs.String("config", "", "配置文件路径；留空则以当前目录为 serveRoot 做纯静态预览")
	foreground := fs.Bool("foreground", false, "internal: run as the actual server process")
	fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "watchpreview:", err)
		os.Exit(1)
	}

	if *foreground {
		runForegroundServer(cfg)
		return
	}

	runPreviewWrapper(cfg, *configPath)
}

// runPreviewWrapper 负责幂等判断 + 拉起后台 server，本身立刻返回。
// 这是用户/上层框架实际调用的入口：`watchpreview preview [--config <file>]`。
func runPreviewWrapper(cfg *Config, configPath string) {
	if configPath == "" {
		fmt.Fprintln(os.Stderr, "watchpreview: no --config, previewing current directory statically")
	}

	canonical, err := canonicalizeRoot(cfg.ServeRoot)
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

	if existing, ok := readInstanceFile(instFile); ok {
		if checkInstanceAlive(existing) {
			// 幂等：已经有一个健康的实例在跑，直接复用。
			// 幂等键只有 serveRoot；监听/编译行为（watch/exclude/
			// onChangeCommand）本轮与已有实例不一致时告警，但不中断、照常复用。
			for _, m := range configMismatches(existing, cfg) {
				fmt.Fprintln(os.Stderr, "watchpreview:", m)
			}
			printURL(existing.URL)
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

	childArgs := []string{"preview", "--foreground"}
	if configPath != "" {
		abs, err := filepath.Abs(configPath)
		if err != nil {
			abs = configPath
		}
		childArgs = append(childArgs, "--config", abs)
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
			printURL(inst.URL)
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

// printURL 是 stdout 上唯一允许输出 URL 的出口。
//
// 集成契约（上层框架 / AI 消费方依赖它）：
//  1. 成功调用时 stdout 恰有一行，即预览 URL（http://127.0.0.1:<port>/）；
//  2. 任何其他信息（运行告警、状态提示、错误原因）只允许走 stderr；
//  3. --foreground 子进程的 stdout/stderr 已被 exec 接到 null device，
//     契约输出源只有本进程，这是拾一道约束：不要再往 stdout 写别的内容，
//     否则会静默破坏消费方的 `stdout 第一行 = URL` 约定。
func printURL(url string) {
	fmt.Println(url)
}

// runForegroundServer 是真正长期运行的 HTTP server 进程，
// 只应该由 runPreviewWrapper 通过 --foreground 拉起，不建议用户直接调用。
// 监听地址固定 127.0.0.1、端口自动分配。
func runForegroundServer(cfg *Config) {
	token := randomToken()

	canonical, err := canonicalizeRoot(cfg.ServeRoot)
	if err != nil {
		reportStartupError("", err.Error())
		os.Exit(1)
	}
	id := computeID(canonical)

	instFile, err := instanceFilePath(id)
	if err != nil {
		reportStartupError(id, err.Error())
		os.Exit(1)
	}

	// 清掉上一次可能的启动错误残留，避免误导本轮诊断。
	removeStartupError(id)

	// 一次性绑定 listener：自动选端口的竞态暴露在这里并立即上报，
	// 不再有"先找空闲端口、后绑定"之间的窗口。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		reportStartupError(id, err.Error())
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	hub := NewReloadHub()
	preview := NewPreview(canonical, id, token, hub)
	preview.server = &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: preview.Handler(),
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)

	inst := Instance{
		ID:         id,
		Root:       cfg.ServeRoot,
		PID:        os.Getpid(),
		Port:       port,
		URL:        url,
		Token:      token,
		SourceHash: cfg.sourceConfigHash(),
	}

	if err := writeInstanceFile(instFile, inst); err != nil {
		reportStartupError(id, err.Error())
		os.Exit(1)
	}

	// 状态文件已写出，启动错误文件不再需要。
	removeStartupError(id)

	// 监听树决定：有 onChangeCommand → 监听 watch（源）；否则 → 监听 serveRoot（纯静态）。
	roots := []string{cfg.ServeRoot}
	if cfg.HasOnChange() {
		if len(cfg.Watch) == 0 {
			fmt.Fprintln(os.Stderr, "watchpreview: onChangeCommand is set but watch is empty; no source changes will trigger rebuilds")
		}
		roots = cfg.Watch
	}

	watchCtx, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()

	watcher := NewWatcher(roots, hub, cfg.Exclude, cfg.OnChangeCommand, cfg.dir)

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
	fmt.Println("root:", cfg.ServeRoot)
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

// configMismatches 对比已有实例与本轮 config 的监听/编译行为。
//
// 幂等键只有 serveRoot。watch/exclude/onChangeCommand 属监听/编译行为，
// 变更不重建实例（避免热切换监听树的不确定性），比对基于实例记录的
// 行为摘要（sourceHash），不一致时提示重启实例生效。
func configMismatches(inst Instance, cfg *Config) []string {
	var msgs []string

	if inst.SourceHash != "" && inst.SourceHash != cfg.sourceConfigHash() {
		msgs = append(msgs, "watch/exclude/onChangeCommand differs from the running instance; run 'stop' then 'preview' again to apply")
	}

	return msgs
}
