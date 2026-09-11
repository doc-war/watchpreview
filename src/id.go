package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// canonicalizeRoot 把用户传入的路径归一化成一个稳定的绝对路径，
// 保证同一个目录（不管通过相对路径、符号链接还是不同 cwd 传入）
// 每次都算出同一个 id。
//
// 这是"id 由内部计算"这个设计能成立的前提：如果 canonicalize 逻辑
// 分散在多处实现（比如以前 TS 和 Go 各算一遍），任何一点不一致
// 都会导致同一目录被认成两个不同实例。现在只有这一份实现。
func canonicalizeRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}

	// EvalSymlinks 失败（比如目录暂时不可读）不应该致命，
	// 退化为只用 Abs 的结果即可。
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}

	return abs, nil
}

func computeID(canonicalRoot string) string {
	sum := sha256.Sum256([]byte(canonicalRoot))
	return hex.EncodeToString(sum[:])[:16]
}

// stateDir 按平台惯例选择状态目录并逐级回退，保证在绝大多数环境里
// 都能拿到一个用户可写、无需提权的目录：
//
//	Windows: %LOCALAPPDATA%\watchpreview
//	macOS:   ~/Library/Caches/watchpreview（可能被系统清理，但状态丢失
//	         会自动自愈为"陈旧→重启"，影响很小）
//	Linux:   $XDG_CACHE_HOME 或 ~/.cache 下的 watchpreview
//
// 回退链：UserCacheDir -> UserConfigDir -> UserHomeDir -> TempDir。
// 不再使用旧的 ~/.watchpreview（不迁移，历史孤儿实例需手动处理）。
func stateDir() (string, error) {
	base := ""
	for _, fn := range []func() (string, error){os.UserCacheDir, os.UserConfigDir, os.UserHomeDir} {
		if d, err := fn(); err == nil && d != "" {
			base = d
			break
		}
	}
	if base == "" {
		base = os.TempDir()
	}

	dir := filepath.Join(base, "watchpreview")

	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}

	return dir, nil
}

// instanceFilePath 是固定约定，不再对外暴露成参数：
// 平台缓存目录下的 watchpreview/<id>.json
func instanceFilePath(id string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".json"), nil
}

// startupErrPath 是后台进程启动失败时的诊断文件位置：
// 与状态文件同目录的 watchpreview/<id>.err。
func startupErrPath(id string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".err"), nil
}
