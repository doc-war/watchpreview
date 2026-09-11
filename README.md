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
const { stdout } = await execFileAsync("watchpreview", ["preview", "--root", "dist"]);
const url = stdout.trim(); // 最后一行就是 URL
```

不需要再自己算 hash、读写 JSON、管理 detached 进程——这些都在二进制内部完成了。

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

优先通过 HTTP 控制端点优雅停止（关 server、停 watcher、删状态文件）。如果联系不上（见下方"陈旧状态"），走降级路径。

### `watchpreview status --root <dir>`

打印当前实例信息（不含 token），或 `preview is not running`。

---

## 陈旧状态（stale instance）的降级容错

状态文件存放在平台惯用的用户级缓存目录下（无需提权、各平台都可写），路径为 `<缓存目录>/watchpreview/<id>.json`：

| 平台 | 实际位置 |
|---|---|
| Windows | `%LOCALAPPDATA%\watchpreview\<id>.json` |
| macOS | `~/Library/Caches/watchpreview/<id>.json`（可能被系统清理，但这相当于"陈旧状态"，下次 preview 会自动重启） |
| Linux | `$XDG_CACHE_HOME`（默认 `~/.cache`）下的 `watchpreview/<id>.json` |

这个状态文件的存在，**不代表对应实例真的还活着**。会变成陈旧状态的典型场景：

1. **进程被 `kill -9` 或主机断电**：来不及清理状态文件，PID 已经不存在了。
2. **PID 被操作系统回收，复用给了另一个无关进程**：容器/CI 环境里这个概率不低，如果只查"PID 是否存在"会误判成"还活着"，返回一个实际已经失效的旧 URL。
3. **进程还在，但 HTTP server 已经挂死**（死锁、崩溃后残留僵尸线程等）：PID 检查过不了这种情况。

### 判断策略：两层校验都过才算真的活着

```
checkInstanceAlive(inst):
    1. pidAlive(inst.PID) == false  → 直接判定陈旧
    2. GET {inst.URL}/_watchpreview/control/status，800ms 超时
       - 请求失败/超时           → 判定陈旧（进程可能挂死）
       - 返回的 id/root 对不上   → 判定陈旧（PID 被复用给了别的进程）
    3. 两层都过 → 判定存活
```

### 各命令的降级行为

| 命令 | 发现陈旧状态时的行为 |
|---|---|
| `preview` | 静默清理状态文件，直接重新启动一个新实例，用户无感 |
| `status` | 清理状态文件，报告 `preview is not running` |
| `stop` | 优雅停止（HTTP）失败后：若 PID 存活**且能通过 HTTP 控制端点核对 id/root 确认身份**，则强制终止；若 PID 存活但核对不上（很可能是 PID 被系统回收、复用给了别的进程），**不杀 PID**，只清理状态文件并提示手动确认；若 PID 已不存在则只清理状态文件。两种情况最终都报告 `preview stopped` |

### 已知的边界情况（未处理，设计上刻意从简）

- **并发 race**：两个 `watchpreview preview --root x` 在极短时间内同时执行，都可能判断"没有活实例"并各自拉起一个 server。作为本地开发工具（单用户、命令通常顺序执行），这个概率很低，代价也可控（浪费一个端口 + 一个孤儿进程，可用 `stop` 手动清理）。如果未来要用在并发调用频繁的场景（比如多个 CI job 并行跑同一目录），需要在 `instanceFilePath` 写入前加文件锁（`O_CREATE|O_EXCL`）。
- **挂死进程只能部分回收**：`stop` 只会在"HTTP 控制端点能核对上身份"时才强制终止（`SIGTERM`，Windows 是 `taskkill /F`）；若进程挂死到连控制端点都不响应，无法确认那个 PID 到底还是不是本实例，**为避免误杀 PID 被复用后的无关进程，选择不杀**，仅清理状态文件并在 stderr 提示手动确认。

### 从旧版升级

- **v1.0.0**：状态文件 schema 新增 `host`/`fallback`/`ignore` 字段（`omitempty`），旧状态文件仍可正常解析；复用时不会对旧文件缺失的字段做比对，升级后首次调用无告警。
- **v0.2.0 起**：状态目录从 `~/.watchpreview/preview/` 迁移到平台缓存目录（见上文）。旧状态文件不再读取：已启动的旧版本实例会变成孤儿（端口仍占用、无法再用 `stop` 定位），升级后请手动结束残留进程，并可删除旧的 `~/.watchpreview` 目录。

---

## HTTP 端点（内部使用，一般不需要框架层直接调用）

| 路径 | 方法 | 鉴权 |
|---|---|---|
| `/_watchpreview/reload` | GET | 无（SSE） |
| `/_watchpreview/control/stop` | POST | `X-WatchPreview-Token` header |
| `/_watchpreview/control/status` | GET | 无（仅返回 id/root，用于陈旧状态核对） |

---

## 构建

```bash
go build -o dist/watchpreview-<os>-<arch> ./src
```

按目标平台交叉编译，产物命名建议 `watchpreview-{GOOS}-{GOARCH}[.exe]`，上层工具运行时根据 `process.platform`/`process.arch` 选择对应二进制。仓库已提交 `go.sum`，全新 clone 后无需额外步骤即可构建。

---

## 安全说明

- 默认只监听 `127.0.0.1`；改成 `0.0.0.0` 前确认是在可信网络环境（比如自己的局域网做真机预览）。
- `/control/stop` 需要 token（每次启动随机生成，存在状态文件里，只有本机能读到）；`/control/status` 不需要，因为只暴露非敏感的 id/root。
- 静态文件服务做了路径越界校验，`../` 无法逃出 `root`。
