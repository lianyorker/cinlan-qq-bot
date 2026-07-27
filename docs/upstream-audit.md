# Upstream Audit Notes

审计对象是只读源码快照。Cinlan 只记录公开接口形状和可观察行为，不复制实现，也不把快照内容编译或打包进 `cinlan-qq-bot`。

| Project | Snapshot | Runtime dependency |
|---|---|---|
| NapCatQQ | `33546b936e008c017b2b9c1c41a0bb4f9e86c5be` | No |
| AstrBot | `f9c6129b9eecdd0a5c4069954baffc27bea02a0a` | No |

## NapCatQQ

关键观察路径：

| Path | Observed responsibility | Cinlan mapping |
|---|---|---|
| `packages/napcat-onebot/network/` | HTTP、SSE、forward/reverse WebSocket 适配 | `internal/onebot/client.go` + `internal/platform/onebot` |
| `packages/napcat-onebot/action/` | action 注册、参数和 retcode 处理 | `CallAction`、typed wrappers |
| `packages/napcat-onebot/event/` | message、notice、request、meta event | `onebot.Event` 原始字段和 `platform.Event.Metadata` |
| `packages/napcat-onebot/types/` | OneBot message segment schema | `internal/message.Component` |
| `packages/napcat-shell-loader/` | QQ 启动和内存 patch 的可观察行为 | `runtime/loader` 的独立实现 |
| `packages/napcat-shell/` | 登录和 wrapper/session 生命周期 | `runtime/qqnt` 的独立实现 |
| `packages/napcat-core/packet/` | 私有 QQ packet、rich media 和 Highway | 未复制；native v1 不实现私有 packet 栈 |

OneBot WebSocket action 的行为边界是：请求带 `action`、`params`、`echo`；HTTP action 使用 `POST /{action}` 和 params JSON body。两者响应均保留 `status`、`retcode`、`data`、`message/wording`。WebSocket 为每个 echo 维护等待通道并在断线时释放；HTTP/SSE 和 reverse HTTP 使用独立超时、响应大小限制及不跟随重定向的 client。

NapCat reverse HTTP 事件带 `x-self-id`，设置 token 时带
`x-signature: sha1=<HMAC-SHA1(raw-body, token)>`。Cinlan 在 JSON 解码前验证原始请求体，
默认返回空 quick operation；确定性的嵌入式处理可注册 quick-operation handler。

QQNT native 接口只核对了以下形状：

- `NodeIKernelLoginService.get()`、`getLoginList()`、`addKernelLoginListener()`。
- `NodeIQQNTWrapperSession.getNTWrapperSession("nt_1")`。
- `getMsgService()`、`addKernelMsgListener()`、`sendMsg("0", peer, elements, new Map())`。
- `ChatType`、`ElementType`、`RawMessage` 和 reply element 的公开字段形状。

对应实现位于 [runtime.cjs](../runtime/qqnt/runtime.cjs)，不是从上游文件生成或移植。

Windows loader 的 clean-room 对照确认：

- `QQ.exe` 的 `GetProcAddress` IAT 是 QQNT 加载时的控制点。
- 查询 `ExportedContentMain` 时先修改 QQNT 启动保护分支，再 patch QQNT
  `CreateFileW` IAT。
- `GetFileInformationByName` 使用
  `CreateFileW + GetFileInformationByHandleEx` stat shim。

Cinlan 未采用会影响所有模块的全局 `CreateFileW` inline hook。

## AstrBot

关键观察路径：

| Path | Observed responsibility | Cinlan mapping |
|---|---|---|
| `astrbot/core/pipeline/` | 有序 stage、停止和错误传播 | `internal/pipeline` + `internal/bot/pipeline.go` |
| `astrbot/core/platform/` | 多平台消息接入 | `internal/platform.Registry` |
| `astrbot/core/message/components.py` | 统一消息组件 | `internal/message.Chain` |
| `astrbot/core/provider/` | provider 注册和选择 | `internal/provider.Registry` |
| `astrbot/core/star/` | plugin 生命周期和 event hook | `internal/plugin.Registry` |
| `astrbot/core/knowledge_base/` | 文档解析和检索 | `internal/knowledge` 第一阶段 |
| `astrbot/core/agent/` | tool loop、MCP、subagent | `internal/tool` + OpenAI/custom tool loop；MCP Streamable HTTP、skills 和同步 subagent handoff 已独立实现 |
| `astrbot/core/cron/` | proactive/cron event | `internal/cron` interval scheduler + atomic JSON persistence |
| `dashboard/` | provider、plugin、session、日志 UI | token 保护的 JSON admin API；WebUI 待后续 |

## Clean-room constraints

- 不复制上游 TypeScript/Python 实现、私有 QQ packet、登录流程或受限资源。
- native 主链只加载本机官方 QQNT 自带的 `QQNT.dll`、`wrapper.node` 和官方登录会话。
- Cinlan loader、受约束 Win32 hook、QQNT runtime、IPC、Agent runtime 均为本项目独立实现。
- OneBot 11 仅作为 `QQ_PLATFORM=onebot` 的兼容 wire contract，不是 native 主链依赖。
- 不修改或再分发官方 QQ 文件；运行时只在内存中定向重写
  `package.json` 和两个 `loadCinlan.js` 路径后缀。
- 上游快照和临时研究目录不进入发布构建。

本机验证基线和 hash 见 [qqnt_runtime_analysis.md](../notes/qqnt_runtime_analysis.md) 与
[addon-inventory.json](../exports/qqnt/addon-inventory.json)。
