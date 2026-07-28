# Cinlan QQ Bot

`cinlan-qq-bot` 是面向个人 QQ 账号的群聊智能客服运行时。默认主链直接加载到官方 QQNT，不要求安装、启动或运行 NapCat/AstrBot。

```text
Official QQNT
  -> Cinlan loader + hook
  -> Cinlan QQNT runtime (runtime.cjs)
  <-> authenticated loopback IPC
  -> Cinlan Go Agent runtime
  -> plugin / session / RAG / MCP / subagent / provider
```

loader、hook、IPC、QQNT adapter 和 Agent runtime 均位于本目录。NapCatQQ/AstrBot 只用于功能调研和公开接口行为对照，其源码不会被打包或作为运行依赖。

## Native Runtime

当前 `qq-native` v1 已实现：

- 自动发现 Windows QQNT 安装目录和最高 `buildVersion`。
- suspended launch + Cinlan 自有 hook，不改写 QQ 安装目录。
- 在 QQNT main process 中加载 `wrapper.node`，复用官方 UI 的登录会话。
- QQNT runtime 与 Go 进程之间的协议版本、token 鉴权、帧上限、握手超时和 action correlation。
- QQ 登录历史同步过滤，以及 wrapper/session attach 错误回传。
- 群聊/私聊消息事件、文本和常见富消息的统一 `MessageChain` 转换。
- 群聊/私聊文本和本地图片发送，以及对当前 runtime 消息缓存中消息的引用回复。
- AVSDK 只读事件观测；通话事件与客服消息隔离，不会触发 Agent 回复。
- `/healthz`、`/readyz`、`/status` 和 Admin API。

OneBot 11 的四种 transport 仍保留为可选兼容出口，只有设置 `QQ_PLATFORM=onebot` 才会启用。

## Agent Runtime

- 有序流水线：wake、security、command、session、attention、rate limit、plugin、agent、decorate、respond。
- 群白名单、`@bot` 唤醒、Binding 级智能注意力、短消息合并、加密会话隔离、去重、冷却和自然分段回复。
- `/清空`、`/人工`、`/恢复`、`/帮助` 和可注册命令。
- Custom Agent API v1 和 OpenAI-compatible `chat/completions`。
- Provider、Persona、Plugin、Command、Tool registry。
- HTTP webhook 插件、MCP Streamable HTTP、`SKILL.md`、同步 subagent handoff。
- 配置化文件目录和 `deliver_file` Agent Tool；群聊引导加好友，私聊发送指定文件。
- 可选的真实图片生成、网页截图和业务查询 Tool；具体能力由本地 Binding 配置决定。
- Markdown/TXT 本地知识库、加密 SQLite 会话、自动压缩和 Cron 通知任务。
- Bearer 保护的 `/api/v1/*` 管理 API。

## 快速启动

要求：

- Windows 10/11 x64。
- 已安装官方 QQNT。
- 可访问的 Agent API。
- 首次源码构建需要 Go 和 Rust；构建完成后的运行不依赖 NapCat/AstrBot。

Windows 是 `qq-native` 的唯一支持平台。macOS 构建仅提供 Go Agent runtime，
必须配置 `QQ_PLATFORM=onebot` 并连接已有的 OneBot 11 服务，不包含 QQNT
loader、hook 或自动登录能力。

准备配置：

```powershell
cd D:\path\to\cinlan-qq-bot
Copy-Item .env.example .env
notepad .env
```

至少修改：

```dotenv
QQ_PLATFORM=native
QQ_GROUP_ALLOWLIST=123456789
QQ_PRIVATE_ALLOWLIST=123456789
AGENT_API_MODE=custom
AGENT_API_URL=http://127.0.0.1:9000/v1/qq/reply
ADMIN_API_TOKEN=<strong-random-value>
```

如果自动发现不到 QQ，显式设置：

```dotenv
QQNT_PATH=D:\SoftWare\Tencent\QQNT\QQ.exe
```

首次构建：

```powershell
.\scripts\build.ps1
```

启动前必须从托盘完全退出现有 QQ。随后双击 [start.cmd](start.cmd)，或执行：

```powershell
.\start.cmd
```

需要持续查看日志并手动停止时，也可以直接打开前台 CMD：

```powershell
cmd.exe /k powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\start.ps1
```

群白名单、Persona、工具和知识库授权全部使用你自己的本地配置，不要把真实群号、
账号、业务资料或访问凭据写入公开仓库。配置方式见
[docs/public-usage.md](docs/public-usage.md)。

`start.cmd` 会：

1. 读取 `.env`。
2. 缺少二进制时自动构建。
3. 检测已运行但未加载 Cinlan runtime 的 QQ。
4. 启动 Go runtime，再由它启动官方 QQ。
5. 保留窗口显示错误和日志，不会立即闪退。

QQ 界面出现后按正常方式登录。以下日志表示主链完成：

```text
QQNT IPC listener ready
QQNT process launched
QQNT runtime authenticated
QQNT runtime status changed state=ready
```

## 验证

```powershell
Invoke-RestMethod http://127.0.0.1:18080/healthz
Invoke-RestMethod http://127.0.0.1:18080/readyz
Invoke-RestMethod http://127.0.0.1:18080/status
```

预期：

- `/healthz` 始终返回 `status: ok`。
- QQNT runtime 登录并附加消息服务后，`/readyz` 返回 `status: ready` 和 `platform_connected: true`。
- 未登录时 `/readyz` 返回 HTTP 503，这是实际状态，不会伪装为 ready。
- native 模式下兼容字段 `onebot_connected` 为 `false`，实际适配器名称见 `platform: qq-native`。

然后在白名单群发送：

```text
@机器人 你好
```

`QQ_REQUIRE_MENTION=false` 时可不 `@`。

## Agent API

Custom API 完整契约见 [docs/custom-agent-api.md](docs/custom-agent-api.md)。最小响应：

```json
{
  "reply": "客服回复",
  "handoff": false
}
```

OpenAI-compatible 模式：

```dotenv
AGENT_API_MODE=openai
AGENT_API_URL=https://api.example.com/v1/chat/completions
AGENT_API_KEY=<secret>
AGENT_MODEL=<model>
AGENT_MAX_TOOL_ROUNDS=4
```

只有注册到 `ToolRegistry` 的工具会发送给模型。未注册工具、非法 JSON、超限参数或权限不足都不会执行。

管理员指定文件的配置和群转私聊流程见 [docs/file-delivery.md](docs/file-delivery.md)。
真实网页截图的白名单、网络隔离和 Chrome 配置见
[docs/web-screenshot.md](docs/web-screenshot.md)。
面向公开部署的配置、资源隔离和发布前检查见
[docs/public-usage.md](docs/public-usage.md)。

## Public release safety

发布前运行：

```powershell
.\scripts\check-public-release.ps1
```

检查失败时不要提交或推送。真实账号、群号、业务 Persona、私有知识库、会话库、
截图/日志、业务源码和凭据只能保留在本地忽略路径中。

## 主要配置

| Variable | Default | Meaning |
|---|---:|---|
| `QQ_PLATFORM` | `native` | `native` 或 `onebot` |
| `QQNT_PATH` | auto | 官方 `QQ.exe` 路径 |
| `QQNT_AUTO_LAUNCH` | `true` | 由 Cinlan loader 启动 QQ |
| `QQNT_ALLOW_RUNNING` | `false` | 是否允许 QQ 已运行时再启动隔离实例 |
| `QQNT_IPC_LISTEN_ADDR` | `127.0.0.1:18081` | native runtime loopback IPC |
| `QQNT_IPC_TOKEN` | generated | 显式 token；留空则每次随机生成 |
| `QQNT_ACTION_TIMEOUT` | `10s` | native action 超时 |
| `QQNT_MAX_FRAME_BYTES` | `1048576` | IPC 单帧上限 |
| `QQ_GROUP_ALLOWLIST` | required | 逗号分隔群号；`*` 表示所有群 |
| `QQ_PRIVATE_ALLOWLIST` | empty | 逗号分隔私聊 QQ 号；空值禁用私聊，`*` 表示所有私聊 |
| `QQ_REQUIRE_MENTION` | `true` | 是否必须 `@机器人` |
| `QQ_QUOTE_REPLY` | `true` | 首个回复分片是否引用原消息 |
| `QQ_GROUP_AT_SENDER` | `true` | 未启用引用回复时，群聊首条回复是否 `@` 提问者 |
| `QQ_GROUP_BATCH_WINDOW` | `900ms` | 同一群、同一发送者的连续消息合并等待窗口；`0` 禁用 |
| `QQ_REPLY_PART_DELAY` | `350ms` | 自然分段回复的基础间隔；实际间隔会按上一段长度小幅增加 |
| `AGENT_API_MODE` | `custom` | `custom` 或 `openai` |
| `AGENT_API_URL` | required | Agent endpoint |
| `IMAGE_API_MODE` | empty | 图片生成 Provider：`openai`、`pollinations`；空值禁用 |
| `IMAGE_API_URL` | empty | 图片生成 endpoint |
| `IMAGE_MODEL` | empty | 图片生成模型；启用生图时必填 |
| `IMAGE_OUTPUT_DIR` | `data/generated-images` | 生成图片目录，必须位于 `BOT_ALLOWED_ROOT` 内 |
| `IMAGE_MAX_BYTES` | `20971520` | 单张生成图片最大字节数 |
| `IMAGE_TIMEOUT` | `120s` | 图片生成请求超时 |
| `IMAGE_RETENTION` | `24h` | 生成图片本地保留时间 |
| `IMAGE_ENHANCE_PROMPT` | `true` | Pollinations 请求启用 prompt enhancement；仍使用随机 seed |
| `WEB_SCREENSHOT_ENABLED` | `false` | 启用真实网页截图 Tool |
| `WEB_SCREENSHOT_BROWSER_PATH` | empty | Chrome `chrome.exe` 的绝对路径 |
| `WEB_SCREENSHOT_ALLOWED_HOSTS` | empty | 逗号分隔的精确 HTTPS host 白名单 |
| `WEB_SCREENSHOT_OUTPUT_DIR` | `data/generated-images` | 截图目录，必须位于 `BOT_ALLOWED_ROOT` 内 |
| `WEB_SCREENSHOT_TIMEOUT` | `45s` | 单次网页截图超时 |
| `WEB_SCREENSHOT_RETENTION` | `24h` | 截图本地保留时间 |
| `WEB_SCREENSHOT_MAX_BYTES` | `20971520` | 单张截图最大字节数 |
| `WEB_SCREENSHOT_WIDTH` / `HEIGHT` | `1365` / `768` | 截图 viewport 尺寸，最大 4096 |
| `WEB_SCREENSHOT_WAIT` | `3s` | 页面加载后的虚拟时间等待 |
| `FILE_CATALOG_PATH` | empty | Agent 可发送的文件目录 JSON；空值禁用文件交付 |
| `FILE_DELIVERY_MAX_BYTES` | `104857600` | 单个目录文件的最大字节数 |
| `FILE_DELIVERY_TIMEOUT` | `30s` | 文件发送 Tool 超时 |
| `BOT_ALLOWED_ROOT` | `.` | 模型触发的本地路径只允许位于该项目根目录内 |
| `SECURITY_INCIDENT_STORE_PATH` | `data/security-incidents.json` | 危险请求匿名化指纹记录，必须位于允许根目录内 |
| `SESSION_STORE_PATH` | `data/sessions.db` | AES-256-GCM 加密字段的 SQLite 会话库 |
| `SESSION_LEGACY_STORE_PATH` | `data/sessions.json` | 首次启动时导入并加密归档的旧 JSON 会话库 |
| `SESSION_ENCRYPTION_KEY` | required | 32 字节 base64/hex 密钥；只从进程或用户环境读取 |
| `SESSION_COMPRESSION_THRESHOLD` | `16` | 达到该消息数后自动生成隔离会话摘要 |
| `SESSION_COMPRESSION_RETAIN` | `6` | 压缩后保留的最近消息数 |
| `SESSION_LEARNING_ENABLED` | `true` | 允许启用了 `learning_enabled` 的绑定提取隔离学习记忆 |
| `BOT_USER_COOLDOWN` | `3s` | 同一 QQ 的跨群消息冷却 |
| `BOT_USER_RATE_LIMIT` / `BOT_USER_RATE_WINDOW` | `10` / `1m` | 同一 QQ 的滑动窗口消息配额 |
| `MEDIA_TOOL_USER_COOLDOWN` | `30s` | 同一 QQ 生图和截图共用冷却 |
| `MEDIA_TOOL_USER_LIMIT` / `MEDIA_TOOL_WINDOW` | `10` / `1h` | 同一 QQ 媒体 Tool 滑动窗口配额 |
| `MEDIA_TOOL_MAX_CONCURRENCY` | `2` | 全局并行生图/截图任务上限 |
| `CRON_STORE_PATH` | `data/cron.json` | Cron 持久化路径 |
| `HTTP_LISTEN_ADDR` | `127.0.0.1:18080` | 健康检查和 Admin API |
| `ADMIN_API_TOKEN` | empty | Admin API Bearer token |

完整默认值见 [.env.example](.env.example)，native 协议和启动细节见 [docs/native-runtime.md](docs/native-runtime.md)。
会话、Persona、Tool、Skill、Knowledge 和 MCP 的隔离模型见
[docs/session-isolation.md](docs/session-isolation.md)。

## OneBot 兼容模式

已有 OneBot 服务时：

```dotenv
QQ_PLATFORM=onebot
ONEBOT_TRANSPORT=forward_ws
ONEBOT_WS_URL=ws://127.0.0.1:3001
ONEBOT_ACCESS_TOKEN=<token>
```

四种网络组合和多账号配置见 [docs/onebot-transports.md](docs/onebot-transports.md)。`compose.yaml` 也是 OneBot 兼容部署，不属于默认 native 主链。

## 本地构建

```powershell
.\scripts\build.ps1
```

等价验证命令：

```powershell
go test ./...
go vet ./...
cargo test --manifest-path runtime/loader/Cargo.toml
node --check runtime/qqnt/load-cinlan.cjs
node --check runtime/qqnt/runtime.cjs
```

产物：

```text
bin/cinlan-qq-bot.exe
bin/cinlan-qq-loader.exe
bin/cinlan-qq-hook.dll
```

## 发布打包

在公开工作树通过敏感信息检查后，一次生成三个压缩包：

```powershell
.\scripts\package-release.ps1 -Version 0.2.0
```

输出：

```text
dist/cinlan-qq-bot-0.2.0-windows-amd64.zip
dist/cinlan-qq-bot-0.2.0-darwin-amd64.zip
dist/cinlan-qq-bot-0.2.0-darwin-arm64.zip
dist/SHA256SUMS-0.2.0.txt
```

Windows 包包含 Go runtime、QQNT loader、hook 和 `runtime/qqnt`。macOS 包
不包含 Windows 原生组件，解压后先执行 `chmod +x cinlan-qq-bot`，将
`.env` 的 `QQ_PLATFORM` 改为 `onebot` 后运行 `./cinlan-qq-bot`。

打包脚本固定使用公开默认构建，不读取本地业务 build tag；执行前会运行
`check-public-release.ps1`、Go tests、`go vet` 和 QQNT runtime tests。

## 当前边界

- native v1 支持文本、群成员 `@`、即时引用回复、私聊普通文件上传、本地 JPEG/PNG/GIF 主动发送，以及把收到的图片交给支持视觉的 OpenAI-compatible 模型。
- AVSDK listener 只上报固定格式摘要：回调名、动作码候选、参数类型、长度和 SHA256；不转发原始 Buffer，事件不进入普通消息流水线。
- QQ 视频接听、拒绝、挂断和媒体收发尚未开放；动作码必须通过白名单私聊实机采样确认后才能实现，模型永远不能主动拨号。
- 主动语音、视频及群文件发送尚未开放；收到的视频目前只保留消息组件，不做视频理解。
- `capture_webpage` 只截图管理员白名单中的 HTTPS 页面；它不复用 QQ 登录态，也不等价于桌面 QQ 截图。
- QQNT 更新可能改变 native interface；runtime 会保持 `/readyz` 为 503 并输出实际错误，需按版本做兼容验证。
- 已经普通方式启动的 QQ 无法补做启动期 package redirect。默认要求先退出 QQ，再运行 `start.cmd`。
- 官方 QQ 登录、账号风控和协议实现仍由官方 QQNT 提供；Cinlan 不复制 QQ 协议栈。
