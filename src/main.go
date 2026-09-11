package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Fprintln(os.Stderr, `watchpreview - 静态预览 + 源码监听编译刷新守护进程

用法:
  watchpreview preview [--config <file>]
  watchpreview stop    [--config <file>]
  watchpreview status  [--config <file>]

配置（JSON）: serveRoot（必填）、watch、exclude、onChangeCommand。
不带 --config 则预览当前目录（纯静态）。`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "-v", "--version", "version":
		// version 变量在 cmd_preview.go 中定义，
		// 发布时由 ldflags 注入真实版本号（如 v1.0.0）。
		fmt.Println(version)
	case "preview":
		runPreviewCommand(os.Args[2:])
	case "stop":
		runStopCommand(os.Args[2:])
	case "status":
		runStatusCommand(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}
