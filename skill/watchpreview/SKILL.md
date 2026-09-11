---
name: watchpreview
description: Use when integrating the watchpreview binary — static preview server with file-watch auto reload and single-instance idempotency. Covers spawning `preview --root <dir>`, reading the URL from stdout, idempotent reuse with the same root, status/stop lifecycle, and the strict stdout/stderr/exit-code contract. Trigger keywords: watchpreview, preview --root, 静态预览, 自动刷新, dist 预览 URL.
---

# watchpreview 集成速查

`watchpreview` 是与前端框架无关的静态预览二进制：**静态文件服务 + 文件变化自动刷新 + 单实例幂等**。
集成姿势只有一个：**spawn 一个命令，读 stdout 拿 URL**。不需要自己算 hash、读写状态文件或管理 detached 进程，全部在二进制内部完成。

## 怎么用

```bash
watchpreview preview --root dist
# → 立即返回（stdout 恰一行）：http://127.0.0.1:54231/
watchpreview preview --root dist        # 再次执行：同 root 幂等，复用同一 URL
watchpreview status --root dist         # 实例信息 / preview is not running
watchpreview stop --root dist           # 优雅停止 → preview stopped
```

最小集成示例（TS）：

```ts
import { execFile } from "node:child_process";
import { promisify } from "node:util";
const execFileAsync = promisify(execFile);

try {
  const { stdout } = await execFileAsync("watchpreview", ["preview", "--root", "dist"]);
  const url = stdout.split(/\r?\n/).find((l) => l.trim())?.trim(); // 契约：URL 行
  void url;
} catch (err) {
  // 失败：非零退出、原因在 stderr（内部 .err 诊断文件转述，不会丢）
  throw new Error(`preview failed: ${(err as Error & { stderr?: string }).stderr}`);
}
```

## 参数速查

| 参数 | 默认 | 说明 |
|---|---|---|
| `--root` | `.` | 预览根目录（幂等键） |
| `--host` | `127.0.0.1` | 真机预览设 `0.0.0.0` 或网卡 IP |
| `--port` | `0`（自动） | 固定端口时用 |
| `--fallback` | `none` | `none` \| `html-suffix` \| `spa` |
| `--ignore` | 空 | 额外忽略的路径片段，逗号分隔 |
| `--open` | `false` | 启动后自动开浏览器 |

**只读一次**：`--host/--port/--fallback/--ignore` 仅首次启动生效，后调不一致**只有 stderr 告警，URL 照旧**，不要据此重建实例。

## 契约硬规则（必须遵守）

1. **stdout 只有那一个 URL 行**；告警/提示/错误全部走 stderr（UTF-8、换行结尾）。
2. **每次以本次 stdout 为准**：不要缓存旧 URL；自动端口下两次可能不同，但同 root 二次调用一定复用。
3. **失败 = 非零退出 + 原因在 stderr**；stdout 解析不到 URL 行按失败处理。
4. **非零退出不是重试信号**；"陈旧状态下次自动重启"是正常自愈，不属于失败语义。