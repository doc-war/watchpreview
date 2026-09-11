# watchpreview

一个与前端框架无关的基础设施二进制：

**静态文件服务 + 文件变化监听 + 浏览器自动刷新 + 单实例幂等管理**。

它不知道、也不关心内容是怎么产生的——手写静态 HTML、Vue/webpack 编译出的 dist、还是某个框架的母版渲染产物都一样。核心契约只有一句话：**给定一个目录，把它当静态站点 serve 出去，目录里的文件变了就通知浏览器刷新。**

所有生命周期管理（幂等判断、id 计算、状态文件、优雅停止）都在二进制内部完成，上层框架只需要 `spawn` 这一个命令、读 stdout 拿 URL，不需要自己维护 instance.json。

---

## 快速使用

```bash
watchpreview preview --root dist
# → 打印 URL，立即返回（不阻塞终端）
# http://127.0.0.1:54231/

watchpreview preview --root dist
# → 再次执行，识别到已有健康实例，直接复用同一个 URL

watchpreview status --root dist
watchpreview stop --root dist
```

对上层框架来说，集成方式就是：

```ts
import { execFile } from "node:child_process";
import { promisify } from "node:util";
const execFileAsync = promisify(execFile);

let stdout: string;
try {
  ({ stdout } = await execFileAsync("watchpreview", ["preview", "--root", "dist"]));
} catch (err) {
  // 失败时保持非零退出码，真实原因在 stderr（由内部 .err 诊断文件转述，不会丢）
  throw new Error(`preview failed: ${(err as Error & { stderr?: string }).stderr}`);
}
const url = stdout.split(/\r?\n/).find((l) => l.trim())?.trim(); // 契约：首行即 URL
```

不需要再自己算 hash、读写 JSON、管理 detached 进程——这些都在二进制内部完成了。

---

## 集成契约（上层开发 / AI 消费方必读）

`watchpreview preview --root <dir>` 的 stdout/stderr/退出码有**严格约定**，请按此实现：

| 场景 | exit code | stdout | stderr |
|---|---|---|---|
| 启动成功（含幂等复用） | `0` | **恰一行**，即预览 URL `http://<host>:<port>/` | 没有；若本轮参数与运行中实例不一致会有告警 |
| 启动失败（端口占用、root 无效等） | 非 `0` | 无可用内容 | 失败原因（由内部 `.err` 诊断文件转述，保证不丢） |
| 运行期信息（陈旧清理提示、参数告警） | — | 无 | 有 |

三条硬规则：

1. **stdout 只有那一个 URL 行**。其余一切（告警、提示、错误）都走 stderr，永远是 UTF-8 文本、以换行结尾。若 stdout 解析不到 URL 行，按失败处理。
2. **每次调用以本次 stdout 为准**。自动端口下 URL 可能每次不同；同 `--root` 二次调用则一定复用同一个 URL（幂等），不要自己缓存或用之前的值。

### 上层集成容易踩的边界

- **换行是平台相关的**：Windows 上是 `\r\n`。用 `trim()` 或按 `/\r?\n/` 分行，不要直接 `split('\n')`（会残留 `\r`）。
- **`--host 0.0.0.0` 打印的 URL 是 `http://0.0.0.0:...`**：该字面值在真机/其他设备上不可达，真机预览需自行替换为机器局域网 IP。
- **行为参数只在首次启动生效**：`--port`/`--host`/`--fallback`/`--ignore` 不会重建实例；后调与运行中实例不一致时只有 stderr 告警，URL 照旧。
- **失败不是重试信号**：非零退出即失败；"陈旧状态下次调用自动重启"是正常自愈，不在失败语义里。
- **同一个 root 同时只维护一个实例**：多个框架进程同时 `preview` 同一 root 时不保证串行安全，多进程/集群场景需自行串行化调用。

改动二进制或需了解内部机制、回归验证方式（契约守卫测试），见根目录 `设计.md`；IDE/Copilot 集成速查见 `skill/watchpreview/SKILL.md`。上层消费方不需要这些细节。

---

## 子命令

### `watchpreview preview`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--root` | `.` | 预览根目录 |
| `--host` | `127.0.0.1` | 监听地址；局域网真机预览设为 `0.0.0.0` 或具体网卡 IP |
| `--fallback` | `none` | `none` \| `html-suffix` \| `spa`，见下方说明 |
| `--ignore` | 空 | 额外忽略的路径片段，逗号分隔 |
| `--port` | `0`（自动） | 固定端口时使用，一般不需要设置 |
| `--open` | `false` | 启动后自动打开浏览器 |

**幂等语义**：同一个 `--root`（规范化之后）第二次调用会复用已有实例，直接打印同一个 URL，不会重复启动。命令本身立刻返回——真正的 HTTP server 是它 fork 出的一个 detached 子进程，不占用调用方的终端/子进程句柄。

注意 `--host`/`--port`/`--fallback`/`--ignore` **只在首次启动时生效**：幂等键只有 `--root`，后调参数不构成新实例。若后调参数与运行中的实例不一致，会向 stderr 打印一条告警（例如 `instance already running on port 8080, ignoring --port 8081`），但仍照常复用已有实例。`--port 0`（自动分配）不参与比对，不指定端口不会产生告警。

### `--fallback` 取值（唯一需要框架自行选择的行为差异点）

- `none`：找不到文件就是 404。纯静态目录预览用这个。
- `html-suffix`：`/foo` 找不到时尝试 `/foo.html`。适合"路由即文件名"约定的框架（比如 ds）。
- `spa`：找不到任何静态文件时回退到根目录 `index.html`。适合 Vue Router / React Router 这类客户端路由。

### `watchpreview stop --root <dir>`

优先通过 HTTP 控制端点优雅停止（关 server、停 watcher、删状态文件）。如果联系不上，走陈旧状态的降级路径（见 `设计.md`）。

### `watchpreview status --root <dir>`

打印当前实例信息（不含 token），或 `preview is not running`。

---

## 安全提示

- 默认只监听 `127.0.0.1`；`--host 0.0.0.0` 仅用于可信局域网真机预览，且打印的字面 URL 真机不可达，需替换为局域网 IP。
- 内部机制（陈旧状态降级、HTTP 控制端点、构建、升级）与改动二进制后的回归验证，见根目录 `设计.md`。
