# Cinlan QQ Bot

`cinlan-qq-bot` 是面向个人 QQ 账号的拟人化群聊/私聊运行时：按绑定挂人格（persona）、长期记忆、知识库和工具，可扮演陪伴、客服等多种角色。QQ 通道经由外部 OneBot 11 服务（如 NapCat）接入。

```text
OneBot 11 服务 (NapCat / Lagrange / ...)
  <-> forward_ws / http_sse / reverse_ws / reverse_http
  -> Cinlan Go Agent runtime
  -> plugin / session / RAG / MCP / subagent / provider
```

OneBot adapter 和 Agent runtime 均位于本目录，QQ 协议栈由外部 OneBot 服务负责。

> 曾有的 `qq-native` 通道（注入官方 QQNT 客户端 + Rust loader + JS runtime）已退役移除：维护成本高且功能覆盖不完整。历史上依赖它的配置（`QQ_PLATFORM=native`、`QQNT_*`）不再生效。

## 架构

### 运行链路

```text
┌──────────────────── 任意 OS ────────────────────┐
│  NapCat / 其它 OneBot 11 实现                    │
│    └─ WebSocket / HTTP 连接                      │
│         └─ Go Agent runtime (cinlan-qq-bot)      │
│              └─ 消息事件/发送动作统一协议          │
└──────────────────────────────────────────────────┘
```

Go runtime 直接连接外部 OneBot 11 服务（默认 forward_ws，也支持 reverse_ws / http_sse / reverse_http 和多账号）。

### 目录结构

```text
cinlan-qq-bot/
├─ cmd/                  # 入口 main
├─ internal/             # Go 核心代码
│  ├─ platform/          # 平台适配层：OneBot adapter
│  ├─ message/           # 统一 MessageChain 与组件转换
│  ├─ pipeline/          # 有序消息流水线编排
│  ├─ bot/               # 业务编排、白名单、唤醒与回复策略
│  ├─ binding/           # Chat Binding：路由与注意力决策
│  ├─ agent/             # Agent 调度与 Custom/OpenAI Provider 对接
│  ├─ provider/          # Provider registry
│  ├─ persona/           # 人格注入
│  ├─ session/           # 加密 SQLite 会话与压缩
│  ├─ knowledge/         # Markdown/TXT 知识库检索
│  ├─ plugin/ command/   # 插件与命令 registry
│  ├─ tool/ skill/       # Agent Tool 与 SKILL.md
│  ├─ mcp/ subagent/     # MCP Streamable HTTP 与 subagent handoff
│  ├─ media/             # 生图与IQ图
│  ├─ filedelivery/      # 文件目录与发送
│  ├─ cron/              # 定时通知任务
│  ├─ security/          # 危险请求指纹与防护
│  └─ config/ status/    # 配置加载与健康状态
├─ scripts/              # 构建、启动、发布与检查脚本
├─ examples/             # Binding、Persona、插件等示例配置
└─ docs/                 # 功能与部署文档
```

### 消息流水线

```text
wake -> security -> command -> session -> attention
     -> rate limit -> plugin -> agent -> decorate -> respond
```

各阶段职责见 [docs/OneBot传输.md](docs/OneBot传输.md)，内部核查记录见
[docs/dev/完整性审计.md](docs/dev/完整性审计.md)。

## QQ 通道（OneBot 11）

- 四种 transport：forward_ws（默认）、reverse_ws、http_sse、reverse_http。
- 多账号聚合：`ONEBOT_ACCOUNTS_FILE` 声明多个 OneBot 连接，消息按账号路由。
- 群聊/私聊消息事件、文本和常见富消息的统一 `MessageChain` 转换。
- 群聊/私聊文本、图片、语音、文件发送，引用回复、表情回应、戳一戳、撤回。
- 进退群/私聊 notice、好友/群欢迎、好友请求自动通过等会话事件。
- `/healthz`、`/readyz`、`/status` 和 Admin API。

## Agent Runtime

- 有序流水线：wake、security、command、session、attention、rate limit、plugin、agent、decorate、respond。
- 群白名单、`@bot` 唤醒、Binding 级智能注意力、短消息合并、加密会话隔离、去重、冷却和自然分段回复。
- `/清空`、`/人工`、`/恢复`、`/帮助` 和可注册命令。
- Custom Agent API v1 和 OpenAI-compatible `chat/completions`。
- Provider、Persona、Plugin、Command、Tool registry。
- HTTP webhook 插件、MCP Streamable HTTP、`SKILL.md`、同步 subagent handoff。
- 配置化文件目录和 `deliver_file` Agent Tool；群聊引导加好友，私聊发送指定文件。
- 内置 `web_search` 联网搜索 Tool：配 `WEB_SEARCH_API_URL` 走 SearXNG 风格 JSON 端点，未配自动走 Bing → DuckDuckGo 免配置兜底（Bing 国内机房直连可达）。
- 可选的真实图片生成和业务查询 Tool；具体能力由本地 Binding 配置决定。
- Markdown/TXT 本地知识库、加密 SQLite 会话、自动压缩和 Cron 通知任务。
- Bearer 保护的 `/api/v1/*` 管理 API。
- 拟人化亲历层：会话内的心情计（`QQ_MOOD_ENABLED`）随 @她的消息起伏，
  而她自己的一举一动——被戳、撤广告、禁言、贴表情、听懂语音、偷查名片、
  整理记忆——会经 `feelEvent`/`feelAmbient` 触发人格化内心独白（`[心情]`），
  记入会话历史并注入「你刚刚的真实感受」，使情绪始终依附于亲历事件。
- 群管理链：Binding 级 `moderation` 配置（patterns 硬匹配 + `llm_check` 模型
  判官）按阶梯执行撤回/警告/禁言/移出；仅白名单（`allowed_users` ∪
  `BOT_ADMIN_USER_IDS`）豁免，群主/管理员头衔不放权。
  前提是 bot 账号拥有群管理员权限。
- 群事件静默感知：进退群、戳一戳、撤回、群文件、管理员变动、群名片、
  精华消息等 notice 写入 `[群事件]` 观察，不回复但被问起能对得上。
- 真人群聊小动作：`[[REACT]]` 表情回应、`[[POKE]]` 戳一戳、`[[DICE]]`/
  `[[RPS]]`、`[[OOPS]]` 手滑撤回、`[[SELFIE]]` 人格贴纸、`[[MEME]]` 表情包。
- 语音往返：OpenAI-compatible `/audio/transcriptions` 入站转写（`ASR_*`），
  Edge/OpenAI/火山 TTS 出站语音回复（`VOICE_*`）。
- 记忆维护引擎（`QQ_REFLECT_*`）：定期合并事实、折叠印象、写当日日记。

内置真人化行为层：可塑记忆、情绪节律、好感度、粘性上下文与主动开口
等内部状态共同驱动自然的说话节奏（内部设计文档不随公开仓库分发）。

## 快速启动

要求：

- 可访问的 Agent API。
- 一个已登录的 OneBot 11 服务（推荐 NapCat）。
- 构建需要 Go（无其它原生依赖）。

### 平台支持

| 平台 | 核心 Go bot | OneBot | 发布包 |
|---|---|---|---|
| Windows 10/11 x64 | 支持 | 支持，需外部 OneBot 11 服务 | `windows-amd64` |
| Linux amd64/arm64 | 支持 | 支持，需外部 OneBot 11 服务 | `linux-amd64` / `linux-arm64` |
| macOS amd64/arm64 | 支持 | 支持，需外部 OneBot 11 服务 | `darwin-amd64` / `darwin-arm64` |

所有平台均通过外部 OneBot 11 服务接入 QQ。Linux 解压后可直接执行 `chmod +x cinlan-qq-bot && ./cinlan-qq-bot`；
Docker 镜像默认使用 OneBot 模式并监听 `:8080`，可通过环境变量覆盖配置。

准备配置：

```powershell
cd D:\path\to\cinlan-qq-bot
Copy-Item .env.example .env
notepad .env
```

至少修改：

```dotenv
QQ_GROUP_ALLOWLIST=123456789
QQ_PRIVATE_ALLOWLIST=123456789
AGENT_API_MODE=custom
AGENT_API_URL=http://127.0.0.1:9000/v1/qq/reply
ADMIN_API_TOKEN=<strong-random-value>
ONEBOT_WS_URL=ws://127.0.0.1:3001
ONEBOT_ACCESS_TOKEN=<token>
```

首次构建：

```powershell
.\scripts\build.ps1
```

启动 NapCat 并确认 QQ 已登录后，双击 [start.cmd](start.cmd)，或执行：

```powershell
.\start.cmd
```

需要持续查看日志并手动停止时，也可以直接打开前台 CMD：

```powershell
cmd.exe /k powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\start.ps1
```

群白名单、Persona、工具和知识库授权全部使用你自己的本地配置，不要把真实群号、
账号、业务资料或访问凭据写入公开仓库。配置方式见
[docs/公开部署指南.md](docs/公开部署指南.md)。

`start.cmd` 会：

1. 读取 `.env`。
2. 缺少二进制时自动构建。
3. 启动 Go runtime 并连接配置的 OneBot 11 服务。
4. 保留窗口显示错误和日志，不会立即闪退。

## 验证

```powershell
Invoke-RestMethod http://127.0.0.1:18080/healthz
Invoke-RestMethod http://127.0.0.1:18080/readyz
Invoke-RestMethod http://127.0.0.1:18080/status
```

预期：

- `/healthz` 始终返回 `status: ok`。
- OneBot 连接建立并登录后，`/readyz` 返回 `status: ready` 和 `platform_connected: true`。
- 未连接时 `/readyz` 返回 HTTP 503，这是实际状态，不会伪装为 ready。
- 实际适配器名称见 `platform: qq-onebot`。

然后在白名单群发送：

```text
@机器人 你好
```

`QQ_REQUIRE_MENTION=false` 时可不 `@`。

## Agent API

Custom API 完整契约见 [docs/自定义AgentAPI.md](docs/自定义AgentAPI.md)。最小响应：

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

管理员指定文件的配置和群转私聊流程见 [docs/文件交付.md](docs/文件交付.md)。
面向公开部署的配置、资源隔离和发布前检查见
[docs/公开部署指南.md](docs/公开部署指南.md)。

## Public release safety

发布前运行：

```powershell
.\scripts\check-public-release.ps1
```

检查失败时不要提交或推送。真实账号、群号、业务 Persona、私有知识库、会话库、
日志、业务源码和凭据只能保留在本地忽略路径中。

## 主要配置

| Variable | Default | Meaning |
|---|---:|---|
| `ONEBOT_TRANSPORT` | `forward_ws` | `forward_ws` / `reverse_ws` / `http_sse` / `reverse_http` |
| `ONEBOT_WS_URL` / `ONEBOT_HTTP_URL` | `ws://127.0.0.1:3001` / `http://127.0.0.1:3000` | OneBot 服务地址 |
| `ONEBOT_ACCESS_TOKEN` | empty | OneBot 服务访问令牌 |
| `ONEBOT_ACCOUNTS_FILE` | empty | 多账号声明 JSON；设置后忽略单连接配置 |
| `QQNT_LOGIN_UIN` | empty | 用于推断本机 QQNT 图片缓存根目录（发送者本地图片路径映射） |
| `QQNT_IMAGE_ALLOWED_ROOTS` | auto | 允许映射到本地的 QQNT 缓存图片根目录（分号分隔） |
| `QQNT_IMAGE_MAX_BYTES` | `20971520` | 单张本地图片最大字节数 |
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
| `BOT_USER_RATE_LIMIT` / `BOT_USER_RATE_WINDOW` | `10` / `1m` | 同一 QQ 的正式回答滑动窗口消息配额 |
| `BOT_ATTENTION_TIMEOUT` | `5s` | Binding 启用 AI 判断时，单次回复决策的独立超时 |
| `BOT_ATTENTION_RATE_LIMIT` / `BOT_ATTENTION_RATE_WINDOW` | `20` / `1m` | 同一 QQ 的 AI 回复决策独立预算 |
| `MEDIA_TOOL_USER_COOLDOWN` | `30s` | 同一 QQ 媒体工具共用冷却 |
| `MEDIA_TOOL_USER_LIMIT` / `MEDIA_TOOL_WINDOW` | `10` / `1h` | 同一 QQ 媒体 Tool 滑动窗口配额 |
| `MEDIA_TOOL_MAX_CONCURRENCY` | `2` | 全局并行媒体任务上限 |
| `CRON_STORE_PATH` | `data/cron.json` | Cron 持久化路径 |
| `HTTP_LISTEN_ADDR` | `127.0.0.1:18080` | 健康检查和 Admin API |
| `ADMIN_API_TOKEN` | empty | Admin API Bearer token |
| `QQ_MOOD_ENABLED` | `true` | 每会话心情计：@她的消息与亲历事件驱动，极差时自闭 30 分钟 |
| `QQ_REFLECT` | `true` | 定期记忆维护（事实合并、印象折叠、日记） |
| `QQ_REFLECT_TICK` | `30m` | 记忆维护扫描间隔 |
| `QQ_REFLECT_FACTS_AT` / `QQ_REFLECT_IMPS_AT` | `20` / `40` | 事实/印象超过该数才触发合并 |
| `QQ_REFLECT_JOURNAL_AGE` | `24h` | 仅最近活跃的绑定会话写日记 |
| `PEER_BOT_IDS` | empty | 同一群里其他 bot 账号，只观察不回复、不计入心情/疲劳 |
| `ASR_API_URL` / `ASR_API_KEY` / `ASR_MODEL` | empty | 入站语音转写 endpoint；空值禁用 |
| `WEB_SEARCH_API_URL` / `WEB_SEARCH_API_KEY` | empty | `web_search` 工具可选 SearXNG JSON 端点；空值走 Bing → DuckDuckGo 免 key 兜底 |
| `WEB_SEARCH_PROXY` | empty | `web_search` 显式 http/socks5 出口代理；空值尊重 HTTPS_PROXY 环境变量 |
| `VOICE_MODE` | `edge` | 语音回复合成后端：`edge` / `openai` / `volc` / `command` |
| `VOICE_API_URL` / `VOICE_API_KEY` / `VOICE_MODEL` | empty | OpenAI-compatible TTS endpoint |
| `VOICE_NAME` / `VOICE_STYLE` | `zh-CN-XiaoyiNeural` / empty | Edge TTS 声线与风格 |
| `VOICE_COOLDOWN` / `VOICE_DAILY_LIMIT` | `45s` / `50` | 语音回复会话冷却与每日上限 |
| `VOICE_MAX_RUNES` | `200` | 单段语音合成最大字数 |
| `FRIEND_WELCOME_TEXT` / `GROUP_WELCOME_TEXT` | empty | 新好友/新群首次问候文案 |
| `GROUP_WELCOME_GROUPS` / `GROUP_WELCOME_IMAGE` | empty | 群欢迎生效群号与配图 |
| `WELCOME_FILE` | `data/welcome.json` | JSON 欢迎语配置，优先于 env（非空才生效） |

完整默认值见 [.env.example](.env.example)，OneBot 网络组合见 [docs/OneBot传输.md](docs/OneBot传输.md)。
群聊与私聊可在 Chat Binding 中选择 `disabled`、`mention_only`、`always` 或 `ai_decide`；
结构化路由范围、隐私默认值和兼容规则见 [docs/会话隔离.md](docs/会话隔离.md)。
自然但不冒充真人的 Persona 模板见 [docs/人格模板.md](docs/人格模板.md)。
自定义 Go 扩展、配置型扩展和各作用域边界见 [docs/自定义扩展.md](docs/自定义扩展.md)。
平台、功能、验证证据和剩余风险见 [docs/dev/完整性审计.md](docs/dev/完整性审计.md)。

更多专题文档：

- [docs/定时任务.md](docs/定时任务.md)：Cron 定时通知任务与持久化。
- [docs/MCP接入.md](docs/MCP接入.md)：MCP Streamable HTTP 接入。
- [docs/技能.md](docs/技能.md)：`SKILL.md` 技能加载。
- [docs/子Agent.md](docs/子Agent.md)：同步 subagent handoff。
- [docs/扩展API.md](docs/扩展API.md)：Go 扩展 API（命令、插件注册）。
- [docs/dev/上游审计.md](docs/dev/上游审计.md) 与
  [docs/dev/上游迁移矩阵.md](docs/dev/上游迁移矩阵.md)：
  NapCatQQ/AstrBot clean-room 对照记录（无运行时依赖）。

## OneBot 连接

四种网络组合和多账号配置见 [docs/OneBot传输.md](docs/OneBot传输.md)。`compose.yaml` 是 OneBot 部署示例。

## 本地构建

```powershell
.\scripts\build.ps1
```

等价验证命令：

```powershell
go test ./...
go vet ./...
```

产物：

```text
bin/cinlan-qq-bot.exe
```

## 发布打包

在公开工作树通过敏感信息检查后，一次生成五个压缩包：

```powershell
.\scripts\package-release.ps1 -Version 0.3.0
```

输出：

```text
dist/cinlan-qq-bot-0.3.0-windows-amd64.zip
dist/cinlan-qq-bot-0.3.0-windows-arm64.zip
dist/cinlan-qq-bot-0.3.0-linux-amd64.zip
dist/cinlan-qq-bot-0.3.0-linux-arm64.zip
dist/cinlan-qq-bot-0.3.0-darwin-amd64.zip
dist/cinlan-qq-bot-0.3.0-darwin-arm64.zip
dist/SHA256SUMS-0.3.0.txt
```

所有发布包均为纯 Go + OneBot 接入，不包含原生组件。Linux 和 macOS 解压后执行 `chmod +x cinlan-qq-bot`，
配置外部 OneBot 11 服务后运行：

```sh
./cinlan-qq-bot
```

打包脚本固定使用公开默认构建，不读取本地业务 build tag；执行前会运行
`check-public-release.ps1`、Go tests 和 `go vet`。

## 当前边界

- 通过 OneBot 11 支持文本、群成员 `@`、即时引用回复、图片/语音/文件收发、表情回应、戳一戳与撤回等能力（取决于 OneBot 实现）。
- 收到的图片可交给支持视觉的 OpenAI-compatible 模型；发送者侧 QQNT 缓存路径按 `QQNT_IMAGE_*` 白名单策略映射到本地。
- 收到的视频目前只保留消息组件，不做视频理解；红包收发不支持。
- QQ 登录、账号风控和协议实现由外部 OneBot 服务提供；Cinlan 不复制 QQ 协议栈。
