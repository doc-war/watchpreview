package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Fprintln(os.Stderr, `watchpreview - 通用静态预览 + 自动刷新守护进程

用法:
  watchpreview preview [--root .] [--host 127.0.0.1] [--fallback none|html-suffix|spa] [--ignore a,b] [--port 0] [--open]
  watchpreview stop    [--root .]
  watchpreview status  [--root .]`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
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
