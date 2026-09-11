package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func runStatusCommand(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	configPath := fs.String("config", "", "配置文件路径；留空则以当前目录定位实例")
	fs.Parse(args)

	// 与 preview 完全对称：定位实例的幂等键是 serveRoot，
	// 它来自 --config 文件或"当前工作目录"的隐式默认配置。
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "watchpreview:", err)
		os.Exit(1)
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

	inst, ok := readInstanceFile(instFile)

	if ok && !checkInstanceAlive(inst) {
		// 降级容错：状态文件存在但实例已经不可用，顺手清理掉，
		// 避免下次 preview/status 还要重新判断一次陈旧状态。
		removeInstanceFile(instFile)
		ok = false
	}

	if !ok {
		fmt.Println("preview is not running")
		return
	}

	// 不打印 token，避免它出现在终端历史 / CI 日志里。
	output := struct {
		ID   string `json:"id"`
		Root string `json:"root"`
		PID  int    `json:"pid"`
		Port int    `json:"port"`
		URL  string `json:"url"`
	}{inst.ID, inst.Root, inst.PID, inst.Port, inst.URL}

	data, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(data))
}
