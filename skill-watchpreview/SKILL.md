---
name: watchpreview
description: Use when integrating the watchpreview binary — static preview server with file-watch auto reload, optional source-watch + auto-compile pipeline, and single-instance idempotency. Covers spawning `preview [--config <file>]`, reading the URL from stdout, idempotent reuse with the same serveRoot, status/stop lifecycle, the strict stdout/stderr/exit-code contract, and the config schema (serveRoot/watch/exclude/onChangeCommand). Trigger keywords: watchpreview, preview --config, 静态预览, 自动刷新, dist 预览 URL, onChangeCommand 编译刷新.
---

# watchpreview 集成速查

`watchpreview` 是与前端框架无关的静态预览二进制：**静态文件服务 + 文件变化自动刷新 + 单实例幂等**。
集成姿势只有一个：**spawn 一个命令，读 stdout 拿 URL**。不需要自己算 hash、读写状态文件或管理 detached 进程，全部在二进制内部完成。

## 怎么用

```bash
# 零配置：以当前目录为静态服务并监听变化
watchpreview preview
# → 立即返回（stdout 恰一行）：http://127.0.0.1:54231/

# 显式配置（静态或带编译管道，见下）
watchpreview preview --config wp.json

# 幂等与生命周期（同用 --config；不带则按当前目录定位）
watchpreview preview --config wp.json    # 再次执行：同 serveRoot 幂等，复用同一 URL
watchpreview status  --config wp.json
watchpreview stop    --config wp.json
```

最小集成示例（TS）：

```ts
import { execFile } from "node:child_process";
import { promisify } from "node:util";
const execFileAsync = promisify(execFile);

try {
  const { stdout } = await execFileAsync("watchpreview", ["preview", "--config", wpPath]);
  const url = stdout.split(/\r?\n/).find((l) => l.trim())?.trim(); // 契约：URL 行
  void url;
} catch (err) {
  // 失败：非零退出、原因在 stderr（内部 .err 诊断文件转述，不会丢）
  throw new Error(`preview failed: ${(err as Error & { stderr?: string }).stderr}`);
}
```

## 配置 schema

`--config`，路径相对 config 所在目录

```jsonc
{
  "serveRoot": "dist",            // 必填；被 serve 的目录（幂等键）
  "watch": ["src"],               // 有 onChangeCommand 时监听它；否则默认监听 serveRoot
  "exclude": [".preCompilePageMasters"], // 路径前缀过滤当前监听树
  "onChangeCommand": "npm run build"     // 非空则启用 源变→编译→刷新
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `serveRoot` | 是 | 被 serve 的目录（**幂等键**）；缺省 `--config` 时为当前工作目录 |
| `watch` | 否 | 源码监听根（递归）；有 `onChangeCommand` 时生效 |
| `exclude` | 否 | 路径**前缀**过滤（非子串），只作用于当前监听树；内置忽略 `.git`/`node_modules`/点开头目录 |
| `onChangeCommand` | 否 | 编译命令，非空启用"源变→编译→刷新"管道；工作目录固定为 config 所在目录 |

Windows 下执行：命令**原样写入临时 `.cmd` 脚本**、隐藏窗口运行、跑完即删，内置 `chcp 65001`。**引号、中文路径都直接可用**，无需自行包批处理。

关键行为：
- `serveRoot` 是幂等键；同一 serveRoot 二次 `preview` 复用同一 URL。
- 有 `onChangeCommand`：监听 `watch`；源变 → 防抖 1s → 跑命令（60s 超时）→ **成功才刷新**；失败/超时不刷新，浏览器保留旧页。
- 无 `onChangeCommand`：监听 `serveRoot`；变化直接刷新。
- `watch`/`exclude`/`onChangeCommand` 变更 → 不自动重建实例；`stop` 后重跑才生效（stderr 有提示）。

固定行为（不可配置）：监听 `127.0.0.1`；端口自动分配；无 fallback 路由（404）；不自动开浏览器。

## 契约硬规则（必须遵守）

1. **stdout 只有那一个 URL 行**；告警/提示/错误全部走 stderr（UTF-8、换行结尾）。
2. **每次以本次 stdout 为准**：不要缓存旧 URL；自动端口下两次可能不同，但同 serveRoot 二次调用一定复用。
3. **失败 = 非零退出 + 原因在 stderr**；stdout 解析不到 URL 行按失败处理。
4. **非零退出不是重试信号**；"陈旧状态下次自动重启"是正常自愈，不属于失败语义。
5. **零配置模式会在 stderr 打印提示**（"no --config, previewing current directory"），不影响契约，勿据此报错。