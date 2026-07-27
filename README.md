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
- 群聊/私聊文本发送，以及对当前 runtime 消息缓存中消息的引用回复。
- `/healthz`、`/readyz`、`/status` 和 Admin API。

OneBot 11 的四种 transport 仍保留为可选兼容出口，只有设置 `QQ_PLATFORM=onebot` 才会启用。

## Agent Runtime

- 有序流水线：wake、command、session、rate limit、plugin、agent、decorate、respond。
- 群白名单、`@bot` 唤醒、会话隔离、去重、冷却、分片和引用回复。
- `/清空`、`/人工`、`/恢复`、`/帮助` 和可注册命令。
- Custom Agent API v1 和 OpenAI-compatible `chat/completions`。
- Provider、Persona、Plugin、Command、Tool registry。
- HTTP webhook 插件、MCP Streamable HTTP、`SKILL.md`、同步 subagent handoff。
- 配置化文件目录和 `deliver_file` Agent Tool；群聊引导加好友，私聊发送指定文件。
- Markdown/TXT 本地知识库、JSON 会话持久化、Cron 通知任务。
- Bearer 保护的 `/api/v1/*` 管理 API。

## 快速启动

要求：

- Windows 10/11 x64。
- 已安装官方 QQNT。
- 可访问的 Agent API。
- 首次源码构建需要 Go 和 Rust；构建完成后的运行不依赖 NapCat/AstrBot。

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
| `QQ_GROUP_AT_SENDER` | `true` | 群聊首个回复分片是否 `@` 提问者 |
| `AGENT_API_MODE` | `custom` | `custom` 或 `openai` |
| `AGENT_API_URL` | required | Agent endpoint |
| `FILE_CATALOG_PATH` | empty | Agent 可发送的文件目录 JSON；空值禁用文件交付 |
| `FILE_DELIVERY_MAX_BYTES` | `104857600` | 单个目录文件的最大字节数 |
| `FILE_DELIVERY_TIMEOUT` | `30s` | 文件发送 Tool 超时 |
| `SESSION_STORE_PATH` | `data/sessions.json` | 会话持久化路径 |
| `CRON_STORE_PATH` | `data/cron.json` | Cron 持久化路径 |
| `HTTP_LISTEN_ADDR` | `127.0.0.1:18080` | 健康检查和 Admin API |
| `ADMIN_API_TOKEN` | empty | Admin API Bearer token |

完整默认值见 [.env.example](.env.example)，native 协议和启动细节见 [docs/native-runtime.md](docs/native-runtime.md)。

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

## 当前边界

- native v1 支持文本、群成员 `@`、即时引用回复和私聊在线文件；图片、语音、视频及群文件上传尚未接入官方上传链。
- QQNT 更新可能改变 native interface；runtime 会保持 `/readyz` 为 503 并输出实际错误，需按版本做兼容验证。
- 已经普通方式启动的 QQ 无法补做启动期 package redirect。默认要求先退出 QQ，再运行 `start.cmd`。
- 官方 QQ 登录、账号风控和协议实现仍由官方 QQNT 提供；Cinlan 不复制 QQ 协议栈。
