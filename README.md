# watchpreview

一个进程，接管前端开发中最繁琐的链路：

```
源码 (watch) ──变更──▶ 编译 (onChangeCommand) ──成功──▶ dist (serveRoot) ───▶ 浏览器实时预览
      自动                      自动                          自动刷新（SSE）
```

**框架只需 spawn 一个命令、读 stdout 拿 URL。** 不需要自己监听源码、触发编译、起 dist 服务、通知浏览器刷新——整条链路由一份 JSON 配置委托给 watchpreview 接管。

---

## 快速使用

```bash
# 零配置零参数：以当前目录为静态服务并监听变化，相当于watchpreview preview
watchpreview
# → http://127.0.0.1:54231/

# 零配置：以当前目录为静态服务并监听变化
watchpreview preview
# → http://127.0.0.1:54231/

# 静态预览（只 serve 某个目录）
# config.json: { "serveRoot": "dist" }
watchpreview preview --config config.json

# 委托编译：源码变 → build → dist 自动刷新
# config.json: { "serveRoot": "dist", "watch": ["src"], "onChangeCommand": "npm run build" }
watchpreview preview --config config.json

# 幂等 / 生命周期（不带 --config 按当前目录定位）
watchpreview status  --config config.json
watchpreview stop    --config config.json
watchpreview --version
```

集成示例（TS）：

```ts
const { stdout } = await execFileAsync("watchpreview", ["preview", "--config", wpPath]);
const url = stdout.split(/\r?\n/).find(l => l.trim())?.trim(); // 契约：首行即 URL
```

---

## 配置

JSON 文件，`--config <file>` 传入。所有路径**相对 config 所在目录**（也接受绝对路径）。

| 字段 | 必填 | 说明 |
|---|---|---|
| `serveRoot` | 是 | 被服务的 dist 目录（幂等键）；缺省 `--config` 时为当前工作目录 |
| `watch` | 否 | 源码监听根，可多个，递归；有 `onChangeCommand` 时生效 |
| `exclude` | 否 | 路径前缀过滤，作用于当前监听树；内置忽略 `.git`、`node_modules`、点开头目录 |
| `onChangeCommand` | 否 | 编译命令，非空即启用"源变→编译→刷新"管道；执行细节见下 |

示例
```json
{ "serveRoot": "dist", "watch": ["src"], "exclude": [".tmp"], "onChangeCommand": "npm run build" }
```

两种形态：

| 形态 | 监听树 | 变化后行为 |
|---|---|---|
| 有 `onChangeCommand` | `watch` | 防抖 1s → 执行命令（超时 60s）→ **成功才刷新**；失败不刷新，保留旧页 |
| 无（含零配置） | `serveRoot` | 防抖 1s → 直接刷新 |

编译命令工作目录固定为 config 文件所在目录。命令成功（exit 0）→ 刷新；失败/超时 → stderr 报错，页面不动。

**`onChangeCommand` 执行方式**（Windows）：watchpreview 会把整条命令**原样写入临时 `.cmd` 脚本**、在**隐藏窗口**中执行、跑完即删。因此：

- 命令里的**引号、中文路径**都正确解析（无需自己包一层批处理或 `chcp`，框架已内置 `chcp 65001`）；
- git 仓库里不会出现命令痕迹，编译不会闪出 cmd 窗口；
- 命令语义与在 cmd 里手敲一致，可用 `&&`、重定向等。

macOS/Linux 直接以 `sh -c` 执行，行为不变。

---

## 集成契约

`watchpreview preview [--config <file>]` 的 stdout/stderr/退出码：

| 场景 | exit code | stdout | stderr |
|---|---|---|---|
| 启动成功 / 幂等复用 | `0` | **恰一行**：`http://127.0.0.1:<port>/` | 无；监听/编译行为变更时有告警 |
| 启动失败 | 非 `0` | 无 | 失败原因（`.err` 文件转述，不丢） |

硬规则：

1. **stdout 只有那一行 URL**；其余一切走 stderr。
2. **每次以本次 stdout 为准**；同 `serveRoot` 二次调用一定复用同一 URL（幂等）。
3. **失败 ≠ 重试信号**；非零即失败；陈旧状态自动重启属正常自愈。
4. **不带 `--config` = 当前目录纯静态**，stderr 有提示（不算错误）。
5. **同一 serveRoot 只维护一个实例**，多进程场景需自行串行化调用。

Windows 上换行是 `\r\n`，按 `/\r?\n/` 分行。

---

## 子命令

| 命令 | 说明 |
|---|---|
| `preview [--config <file>]` | 启动或复用实例，stdout 输出 URL，命令本身立刻返回 |
| `stop [--config <file>]` | 优雅停止（HTTP 端点）；失败则降级清理（见 `设计.md`） |
| `status [--config <file>]` | 打印实例 JSON 或 `preview is not running` |

幂等键是 `serveRoot`（规范化后）。行为参数（`watch`/`exclude`/`onChangeCommand`）变更时不重建实例，stderr 会提示重启生效。

---

## 发布流程

```bash
git tag v1.1.0 && git push origin v1.1.0   # 自动触发 GitHub Actions 发布
```

CI 平时跑 `go test` / `go vet`；发版时 goreleaser 跨平台编译（linux/darwin/windows × amd64/arm64，`CGO_ENABLED=0`）挂载到 GitHub Release，自动附 `checksums.txt`。

预发布标签（如 `v1.1.0-rc1`）自动标记为 Prerelease。

---

内部机制、回归验证方式见 `设计.md`；AI/Copilot 速查见 `skill/watchpreview/SKILL.md`。