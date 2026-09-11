package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config 是 watchpreview 唯一的配置面（JSON，--config 传入）。
// 只保留与"范式"直接相关的字段：
//
//	serveRoot       被服务的 dist 目录（幂等键）
//	watch           源码监听根；配了 onChangeCommand 时改用它
//	exclude         路径前缀过滤，只作用于当前监听树
//	onChangeCommand 编译命令；非空即启用"源变→编译→刷新"管道
//
// host/port/fallback/open 等一律删除，行为固定：127.0.0.1、自动端口、
// 纯 404、不自动开浏览器。命令工作目录固定为 config 所在目录。
type Config struct {
	ServeRoot       string   `json:"serveRoot"`
	Watch           []string `json:"watch,omitempty"`
	Exclude         []string `json:"exclude,omitempty"`
	OnChangeCommand string   `json:"onChangeCommand,omitempty"`

	// dir 是"config 文件所在目录"（隐式模式 = 当前工作目录），
	// 非 JSON 字段，只供内部使用：路径解析与编译工作目录都基于它。
	dir string
}

// HasOnChange 是否启用编译管道。
func (cfg *Config) HasOnChange() bool {
	return strings.TrimSpace(cfg.OnChangeCommand) != ""
}

// loadConfig 统一读取配置：
//
//	configPath == "" → 隐式默认：serveRoot=当前工作目录，纯静态。
//	否则从 JSON 文件读取并解析。返回前 cfg 中的 ServeRoot / Watch / Exclude
//	都被解析为绝对路径，幂等键与监听器可直接使用。
func loadConfig(configPath string) (*Config, error) {
	if configPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve current directory: %w", err)
		}
		return &Config{ServeRoot: cwd, dir: cwd}, nil
	}

	abs, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if strings.TrimSpace(cfg.ServeRoot) == "" {
		return nil, errors.New("serveRoot is required")
	}

	cfg.dir = filepath.Dir(abs)

	// 路径解析：绝对路径直接采用；相对路径拼 config 所在目录。
	// 注意不能一律 filepath.Join(base, x)：Go 的 Join 不会因后者是绝对路径
	// 而重置拼接起点，会把 base 错误地叠在前面（见 joinFromBase）。
	cfg.ServeRoot = joinFromBase(cfg.dir, cfg.ServeRoot)
	if info, err := os.Stat(cfg.ServeRoot); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("serveRoot is not an existing directory: %s", cfg.ServeRoot)
	}

	for i, w := range cfg.Watch {
		p := joinFromBase(cfg.dir, w)
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("watch entry does not exist: %s", p)
		}
		cfg.Watch[i] = filepath.Clean(p)
	}

	for i, e := range cfg.Exclude {
		cfg.Exclude[i] = filepath.Clean(joinFromBase(cfg.dir, e))
	}

	if cfg.HasOnChange() {
		cfg.OnChangeCommand = strings.TrimSpace(cfg.OnChangeCommand)
	}

	return &cfg, nil
}

// joinFromBase 如果 p 是绝对路径则直接返回，否则拼 base 目录。
// 避免 filepath.Join 将绝对路径错误叠在 base 后面。
func joinFromBase(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}

// sourceConfigHash 对"监听与编译行为"（watch/exclude/onChangeCommand）
// 做摘要。幂等复用时若与运行中实例不一致，说明期望的监听/编译行为变了，
// 需要提示重启实例生效（这些字段不重建实例）。
func (cfg *Config) sourceConfigHash() string {
	payload, _ := json.Marshal(struct {
		Watch           []string `json:"watch"`
		Exclude         []string `json:"exclude"`
		OnChangeCommand string   `json:"onChangeCommand"`
	}{cfg.Watch, cfg.Exclude, cfg.OnChangeCommand})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])[:16]
}
