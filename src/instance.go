package main

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

type Instance struct {
	ID    string `json:"id"`
	Root  string `json:"root"`
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	URL   string `json:"url"`
	Token string `json:"token"`

	// 行为参数。幂等键只有 root，行为参数只在首次启动时生效；
	// 复用已有实例时用这几个字段兜底告警"本轮参数与运行中实例不一致"。
	// omitempty 保证旧版本（无这些字段）写入的状态文件仍可正常解析。
	Host     string `json:"host,omitempty"`
	Fallback string `json:"fallback,omitempty"`
	Ignore   string `json:"ignore,omitempty"`
}

func writeInstanceFile(path string, inst Instance) error {
	data, err := json.MarshalIndent(inst, "", "  ")
	if err != nil {
		return err
	}

	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}

func readInstanceFile(path string) (Instance, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Instance{}, false
	}

	var inst Instance
	if err := json.Unmarshal(data, &inst); err != nil {
		return Instance{}, false
	}

	return inst, true
}

func removeInstanceFile(path string) {
	_ = os.Remove(path)
}

var statusCheckClient = &http.Client{Timeout: 800 * time.Millisecond}

// checkInstanceAlive 是"降级容错"的核心：instance.json 的存在
// 不代表实例真的还活着，可能的陈旧原因包括：
//   - 进程被 kill -9 / 主机断电，来不及清理状态文件
//   - PID 被系统回收，复用给了另一个无关进程（纯 PID 检查会误判）
//   - 进程还在但已经挂死、HTTP server 不响应
//
// 所以这里做两层判断：PID 存活 + HTTP /control/status 语义核对
// （返回的 id/root 必须和状态文件里记录的一致），两者都通过
// 才认为实例真的可用；任何一层失败都视为陈旧，交给调用方清理重启。
func checkInstanceAlive(inst Instance) bool {
	if !pidAlive(inst.PID) {
		return false
	}

	resp, err := statusCheckClient.Get(inst.URL + statusEndpointPath[1:])
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var body struct {
		ID   string `json:"id"`
		Root string `json:"root"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}

	return body.ID == inst.ID && body.Root == inst.Root
}
