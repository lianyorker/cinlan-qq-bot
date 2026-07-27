# Upstream Feature Migration Matrix

本文档记录 Cinlan 对 NapCatQQ 和 AstrBot 的 clean-room 功能对照。只参考公开协议、文档和可观察行为，不复制受限实现代码。

## Source Baseline

| Project | Snapshot | License / constraint |
|---|---|---|
| NapCatQQ | `33546b936e008c017b2b9c1c41a0bb4f9e86c5be` | `Limited Redistribution License`；不内置、修改或再分发其实现 |
| AstrBot | `f9c6129b9eecdd0a5c4069954baffc27bea02a0a` | `AGPL-3.0-or-later`；不复制实现，独立实现协议兼容能力 |

默认主链不运行 NapCatQQ 或 AstrBot。官方 QQNT 负责账号登录和 QQ 协议，Cinlan 自有 loader/runtime 只附加到官方会话；OneBot transport 是显式开启的兼容模式。

## Runtime Layers

```text
QQ runtime
  -> Platform Adapter
  -> Unified MessageChain
  -> Ordered Pipeline
  -> Command / Plugin / Agent
  -> Provider / Tool / Knowledge
  -> Persistence
  -> Admin API
```

| Layer | Current implementation | Status |
|---|---|---|
| Protocol/runtime | Cinlan loader/hook + QQNT Node runtime + authenticated loopback IPC | Native v1 implemented; official QQNT remains the protocol/login provider |
| Platform | `internal/platform` registry + native QQNT adapter + optional OneBot adapter | Implemented |
| Message | `internal/message` chain、native element converter 和 OneBot/CQ parser | Native inbound text/mention/quote/common rich metadata and outbound text/quote implemented |
| Pipeline | wake, command, session, rate limit, plugin before/after, agent, decorate, respond | Implemented and tested |
| Provider | custom API, OpenAI-compatible API, default provider registry | Implemented |
| Plugin/command | hook registry, command aliases and admin permission | Implemented |
| Tool/MCP boundary | local Tool Registry, JSON limits, timeout and permission; MCP Streamable HTTP connector | Registry and Streamable HTTP discovery/call implemented; stdio and legacy standalone SSE pending |
| Knowledge | Markdown/TXT loader and BM25-style retrieval plugin | Implemented |
| Persistence | atomic JSON session store and durable Cron store, TTL and metadata list | Implemented |
| Admin | health/readiness/status and token-protected runtime API | Implemented; full WebUI pending |
| Operations | JSON logs、runtime error status、counters、reconnect、Cron interval jobs | Implemented for current native/OneBot scope |

## Native QQNT Runtime

```text
Official QQNT
  -> cinlan-qq-loader.exe
  -> cinlan-qq-hook.dll
  -> runtime/qqnt/runtime.cjs
  <-> QQNT IPC v1
  -> cinlan-qq-bot.exe
```

`Source` 表示本地源码是否实现并接线。`Evidence` 表示验证强度，分三档：

- `unit`：单元测试覆盖，但对手方是模拟 runtime 或 fixture 安装目录，不接触真实 QQ。
- `syntax`：只做过语法检查，没有行为测试。
- `live-runtime`：真实 QQNT 已启动并附加 wrapper/session，但未发送测试消息。
- `not verified`：没有针对真实 QQNT 的运行验证。

当前已完成真实 QQNT 的启动、wrapper/session attach 和 ready 验证，但尚未完成
真实消息的 `event -> Agent -> sendMsg` 端到端验证。`internal/platform/qqnt/adapter_test.go`
仍使用本地 TCP fixture 验证协议边界，不能替代真实消息测试。

| Native capability | Source | Evidence |
|---|---|---|
| QQNT version discovery | Implemented | unit + live-runtime（真实安装 `9.9.31-49738`） |
| Suspended launch and pre-start hook | Implemented; Windows x64 | unit（3 个 Rust tests）+ live-runtime（两次冷启动超过 200 秒） |
| Official package entry redirect | QQ.exe `GetProcAddress` IAT + QQNT guard/IAT patch；官方文件不变 | unit + live-runtime |
| Official wrapper/session attach | Implemented | live-runtime（wrapper/session ready） |
| IPC authentication and limits | Token handshake、loopback-only listener、timeout、action correlation and configurable frame limit implemented | unit + live-runtime |
| Login/runtime status | `booting`/`waiting_login`/`ready` plus wrapper/session flags and last attach error | unit + live-runtime |
| Group/private inbound message | QQNT runtime 可转换两类事件；客服 pipeline 当前只接受群消息；历史同步会过滤 | not verified（未收过本轮真实测试消息） |
| Group/private text send | QQNT adapter 支持两类发送；客服统一回复路径当前只接群发送 | not verified（未调用过本轮真实 `sendMsg`） |
| Immediate quote reply | Implemented with bounded native-message cache | not verified |
| Rich media upload/transcoding | Pending | — |
| Notice/request/moderation native actions | Pending | — |
| Multi-account native attach | Pending | — |

`runtime/qqnt/runtime.cjs` 目前有 `node --check` 和真实 wrapper/session attach
证据，但没有独立 JS 单元测试；`runtime/loader` 有 3 个 Rust 测试。真机消息验证步骤见
[native-runtime.md](native-runtime.md) 的「更新验证」，结果应回写到
[qqnt_runtime_analysis.md](../notes/qqnt_runtime_analysis.md)。

## NapCat / OneBot Compatibility

常用客服动作已提供 typed wrapper：

- `send_group_msg`、`send_private_msg`、`delete_msg`
- `get_login_info`、`get_group_info`、`get_group_member_info`
- `get_group_list`、`get_friend_list`、`get_msg`
- `set_group_ban`、`set_group_whole_ban`、`set_group_kick`
- text/image/record/file message segments

其余动作可以通过 `onebot.Client.CallAction` 发送，仍由统一的 token、`echo`、超时和错误处理保护。

| NapCat feature group | Cinlan status |
|---|---|
| Message events and group replies | Native and OneBot implemented |
| Notice/request/meta events | OneBot raw fields preserved; native v1 pending |
| HTTP/SSE/forward/reverse WebSocket | `forward_ws`、`reverse_ws`、`http_sse`、`reverse_http` implemented and tested |
| Multi-account routing | Account registry、name/`self_id` routing and status API implemented |
| Group moderation and queries | Common typed wrappers implemented; remaining actions available through authenticated generic action API |
| Rich media / Highway / FFmpeg | Chain and references preserved; upload/transcoding pending |
| Process supervision / QQNT startup attach | Cinlan native loader/hook implemented; login UI remains official QQ |
| Network/plugin WebUI | JSON admin API; WebUI pending |
| Guild/new API and packet/OIDB extensions | Pending; requires independent protocol work or permission |

## AstrBot Compatibility

| AstrBot capability | Cinlan status |
|---|---|
| Ordered message pipeline | Implemented |
| 18-platform adapter model | Registry and OneBot adapter implemented; more adapters pending |
| Unified message components | Implemented |
| Provider registry | Implemented |
| Session routing/history | Account-group-user isolation, TTL, JSON persistence and per-session Provider/Persona overrides implemented；private session routing pending |
| Plugin lifecycle/hooks | Before/after/event hooks and config-defined HTTP webhook hot reload implemented |
| Commands/filters/permissions | Command aliases and admin role check implemented |
| Agent handoff | `handoff` response and `/人工`/`/恢复` implemented |
| Tool loop | OpenAI-compatible tool-call loop implemented with local registry |
| MCP / skills / subagents | MCP Streamable HTTP transport, `SKILL.md` progressive disclosure and synchronous config-defined subagent handoff implemented; stdio/background subagent tasks pending |
| Knowledge base / retrieval | Local Markdown/TXT retrieval implemented; vector/rerank/parsers pending |
| Persona/prompt management | Persona registry, default/session switching, environment prompt and RAG context implemented |
| Streaming / segmented replies | Unicode segmentation and optional OpenAI SSE stream implemented; progressive QQ delivery policy pending |
| STT/TTS/T2I/multimodal | Message markers only; providers pending |
| Cron/proactive messages | Interval scheduler, authenticated admin API and atomic JSON job persistence implemented |
| Web search/computer tools | Not enabled by default; register explicit tools only |
| Dashboard / backup / tracing | Status/admin JSON API; WebUI, backup and distributed tracing pending |

## Delivery Batches

1. **Runtime foundation**: platform/message/pipeline, OneBot action boundary, durable sessions. **Delivered.**
2. **Customer-service platform**: provider registry, RAG, handoff, command/plugin hooks, authenticated admin API. **Delivered as first usable slice.**
3. **Extension runtime**: tool loop, MCP Streamable HTTP transport, dynamic plugins, skills, subagents, streaming. **Tool loop, MCP HTTP, skills and synchronous subagent slice delivered; remaining items pending.**
4. **Multimodal and operations**: rich media upload, STT/TTS, backup, tracing, multi-account. **Durable Cron and multi-account routing delivered; remaining items pending.**
5. **Runtime independence**: self-owned loader/hook、QQNT runtime、authenticated IPC and native adapter. **Native v1 delivered; rich media and management actions remain pending.**

每个后续功能必须同时补齐入站事件、领域模型、持久化、出站动作、API 契约、测试和文档，不能只添加一个未接线的类型或接口。
