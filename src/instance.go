package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Instance struct {
	ID    string `json:"id"`
	Root  string `json:"root"`
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	URL   string `json:"url"`
	Token string `json:"token"`

	// SourceHash 是启动时"监听/编译行为"（watch/exclude/onChangeCommand）的摘要。
	// 幂等复用时若与本轮 config 不一致，提示重启实例生效。
	SourceHash string `json:"sourceHash,omitempty"`

	// StartedAt 是实例启动时间，用于超限轮换时选出"最老"的实例。
	// 旧的（无此字段的）状态文件 unmarshal 后为零值，会被视为最老、优先停掉。
	StartedAt time.Time `json:"startedAt,omitempty"`
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

// maxInstances 是全局并发实例上限：同一用户/同一台机器下，所有
// serveRoot 的 watchpreview 后台进程合计不能超过这个数（硬编码，不暴露参数）。
const maxInstances = 5

// listActiveInstances 枚举状态目录下所有真正存活的实例。
// 只认 16 位 hex（computeID 输出形状）的 <id>.json；陈旧文件
// （进程已死 / HTTP 不响应）不占名额，返回空即代表没有可清理的实例。
func listActiveInstances() ([]Instance, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var active []Instance
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || len(strings.TrimSuffix(name, ".json")) != 16 {
			continue
		}
		if inst, ok := readInstanceFile(filepath.Join(dir, name)); ok && checkInstanceAlive(inst) {
			active = append(active, inst)
		}
	}
	return active, nil
}

// ensureCapacity 实施"全局最多 maxInstances 个实例"的约束。
// 注意：自身通常带不带新实例——调用方（preview wrapper）接下来会 fork
// 一个新进程占一个名额，所以达到 maxInstances 就必须开始驱逐。
// 策略（用户指定）：启动新实例时顺带主动停最老的那个
// （StartedAt 升序取首个）；停不掉则静默忽略——不返回错误、
// 不阻断新实例启动，"停最老"只是启动动作里的一个附带尝试。
func ensureCapacity() {
	active, err := listActiveInstances()
	if err != nil {
		fmt.Fprintln(os.Stderr, "watchpreview: warning: cannot list instances:", err)
		return
	}
	if len(active) < maxInstances {
		return
	}

	sort.Slice(active, func(i, j int) bool {
		return active[i].StartedAt.Before(active[j].StartedAt)
	})

	oldest := active[0]
	if instFile, err := instanceFilePath(oldest.ID); err == nil && evictInstance(oldest) {
		removeInstanceFile(instFile)
	}
}

// evictInstance 尽力停掉一个实例，返回是否已停机（或确认进程已死）。
// 语义与 cmd_stop 的降级链一致；PID 在但 HTTP 核对不上、可能已被
// 系统回收复用时，绝不强杀无辜进程，直接当作"停不掉"忽略。
func evictInstance(inst Instance) bool {
	if tryHTTPStop(inst) {
		return true
	}
	if !pidAlive(inst.PID) {
		// 进程已死，状态文件归调用方清理
		return true
	}
	if checkInstanceAlive(inst) {
		// HTTP 控制端点核上了 id/root：确实是我们自己的实例，升强杀
		return pidTerminate(inst.PID) == nil
	}
	return false
}

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
