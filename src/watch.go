package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// 防抖窗口固定为常量：构建工具批量写文件时合并成一次 reload。
// 这个值几乎不需要按场景调整，做成参数只会增加调用方的认知负担。
const debounceWindow = 1000 * time.Millisecond

// compileTimeout 是 onChange 编译命令的兜底超时。
// 超过即杀掉命令且不刷新页面，保证编译进程永远不会失控到无法 stop。
const compileTimeout = 60 * time.Second

var defaultIgnoreDirs = []string{".git", "node_modules"}

// Watcher 负责监听变更并决定刷新时机。
//
// 两种工作模式，由 onChangeCommand 是否配置决定：
//   - 有 onChangeCommand：监听 watch 集合（源码），防抖后执行编译命令，
//     命令成功 → reload，失败 → 不刷新页面（浏览器保留旧内容）；
//   - 无（含隐式当前目录模式）：纯静态，监听 serveRoot 树，防抖后直接 reload。
type Watcher struct {
	roots           []string
	hub             *ReloadHub
	exclude         []string
	onChangeCommand string

	// workDir 是编译命令的工作目录，固定为 config 所在目录。
	workDir string

	compileMu sync.Mutex
}

func NewWatcher(roots []string, hub *ReloadHub, exclude []string, onChangeCommand, workDir string) *Watcher {
	return &Watcher{roots: roots, hub: hub, exclude: exclude, onChangeCommand: onChangeCommand, workDir: workDir}
}

// matchesExclude 是路径前缀 + 路径边界匹配：
// 命中 exclude 的路径及其整棵子树都不参与监听与刷新。
// 与 v1.0 的 Contains 子串匹配不同，不会有"排除 i18n 却把包含该串的
// 任意深层路径一起排除"的误伤。
func matchesExclude(path string, excludes []string) bool {
	for _, e := range excludes {
		if e == "" {
			continue
		}
		if path == e || strings.HasPrefix(path, e+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (w *Watcher) shouldIgnore(name, fullPath string) bool {
	for _, d := range defaultIgnoreDirs {
		if name == d {
			return true
		}
	}

	if strings.HasPrefix(name, ".") {
		return true
	}

	return matchesExclude(fullPath, w.exclude)
}

func (w *Watcher) Run(ctx context.Context) error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fw.Close()

	for _, root := range w.roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if path != root && w.shouldIgnore(d.Name(), path) {
				return filepath.SkipDir
			}
			return fw.Add(path)
		})
		if err != nil {
			return err
		}
	}

	var timer *time.Timer
	fire := make(chan struct{}, 1)

	resetTimer := func() {
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(debounceWindow, func() {
			select {
			case fire <- struct{}{}:
			default:
			}
		})
	}

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return nil

		case <-fire:
			// 防抖火后处置可能慢（编译可能耗时），放 goroutine 里执行，
			// 不阻塞事件循环；编译本身的串行性由 compileMu 保证。
			go w.changed()

		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "watchpreview: watcher error:", err)
			}

		case event, ok := <-fw.Events:
			if !ok {
				return nil
			}

			if event.Op&fsnotify.Create != 0 {
				if info, statErr := os.Stat(event.Name); statErr == nil && info.IsDir() {
					if !w.shouldIgnore(filepath.Base(event.Name), event.Name) {
						_ = fw.Add(event.Name)
					}
				}
			}

			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				if matchesExclude(event.Name, w.exclude) {
					continue
				}
				resetTimer()
			}
		}
	}
}

// changed 处理一轮防抖后的变更集。
func (w *Watcher) changed() {
	if w.onChangeCommand == "" {
		w.hub.Reload()
		return
	}

	w.compileMu.Lock()
	defer w.compileMu.Unlock()

	if !w.executeCompile() {
		// 编译失败或超时：不刷新，浏览器保留旧页面，
		// 具体原因已打印到 stderr 供直接跑 --foreground 调试时观察。
		return
	}
	w.hub.Reload()
}

// executeCompile 用平台 shell 执行编译命令，超时自动杀掉。
// 返回 true 表示命令成功结束。
func (w *Watcher) executeCompile() bool {
	ctx, cancel := context.WithTimeout(context.Background(), compileTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		scriptPath, err := writeCmdScript(w.workDir, w.onChangeCommand)
		if err != nil {
			fmt.Fprintf(os.Stderr, "watchpreview: write onChangeCommand script: %v\n", err)
			return false
		}
		defer os.Remove(scriptPath)

		// cmd 以 workDir 为工作目录，脚本用无空格随机相对文件名传参，
		// 避免"cmd /C 带引号尾随路径"的解析坑（见 writeCmdScript 注释）。
		cmd = exec.CommandContext(ctx, "cmd", "/D", "/C", filepath.Base(scriptPath))
		cmd.Dir = w.workDir
		hideConsole(cmd)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", w.onChangeCommand)
		cmd.Dir = w.workDir
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchpreview: onChangeCommand failed: %v\n%s", err, out)
		return false
	}
	return true
}

// writeCmdScript 把 onChangeCommand 原样写进临时批处理文件（UTF-8 无 BOM）。
//
// 为什么不用 `cmd /C <命令>` 直接执行：
//  1. Go 拼接命令行时会把参数里的 " 转义成 \"（CommandLineToArgvW 算法），
//     而 cmd.exe 及批处理是 parse 的例外，不认反斜杠引号——任何带引号的
//     命令串都会碎。Go 官方文档明确建议这种场景自拼 SysProcAttr.CmdLine，
//     而我们选择更稳的路径：命令行里不出现引号，只有无空格脚本文件名。
//  2. 命令写进批处理后按行原生解析，引号与中文路径都正确。
//
// 码页处理：中文 Windows 上 cmd 默认按 ANSI(GBK) 读批处理文件，UTF-8 写出
// 的中文路径会乱码。脚本首行放全 ASCII 的 `chcp 65001 >nul`，cmd 从第二行起
// 按 UTF-8 解析，命令中的中文路径即正确。该两行必须在任何非 ASCII 内容之前。
func writeCmdScript(dir, command string) (string, error) {
	tmp, err := os.CreateTemp(dir, ".watchpreview-*.cmd")
	if err != nil {
		return "", err
	}
	path := tmp.Name()

	content := "@echo off\r\nchcp 65001 >nul\r\n" + command + "\r\n"
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(path)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}
