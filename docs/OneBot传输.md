# OneBot 传输层

这是可选兼容层。先设置 `QQ_PLATFORM=onebot`；默认 `QQ_PLATFORM=native` 不使用任何 OneBot 服务。

`cinlan-qq-bot` 支持四种 OneBot 11 网络组合。单账号使用环境变量，多账号使用
[`examples/onebot-accounts.json`](../examples/onebot-accounts.json)。

| Transport | 事件方向 | Action 方向 | 必要配置 |
|---|---|---|---|
| `forward_ws` | Cinlan 连接 NapCat WebSocket Server | 同一 WebSocket | `ONEBOT_WS_URL` |
| `reverse_ws` | NapCat WebSocket Client 连接 Cinlan | 同一 WebSocket | `ONEBOT_REVERSE_LISTEN_ADDR`、`ONEBOT_REVERSE_PATH` |
| `http_sse` | Cinlan `GET /_events` | Cinlan `POST /{action}` | `ONEBOT_HTTP_URL` |
| `reverse_http` | NapCat HTTP Client `POST` 到 Cinlan | Cinlan `POST /{action}` 到 NapCat HTTP Server | `ONEBOT_HTTP_URL`、reverse listen/path |

## Forward WebSocket（正向 WS）

NapCat 启用 WebSocket Server，Cinlan 配置：

```dotenv
ONEBOT_TRANSPORT=forward_ws
ONEBOT_WS_URL=ws://127.0.0.1:3001
ONEBOT_ACCESS_TOKEN=<shared-token>
```

## Reverse WebSocket（反向 WS）

Cinlan 监听后，由 NapCat WebSocket Client 连接：

```dotenv
ONEBOT_TRANSPORT=reverse_ws
ONEBOT_REVERSE_LISTEN_ADDR=0.0.0.0:3002
ONEBOT_REVERSE_PATH=/onebot/v11/ws
ONEBOT_ACCESS_TOKEN=<shared-token>
```

NapCat 的连接 URL 为 `ws://<cinlan-host>:3002/onebot/v11/ws`。鉴权支持
`Authorization: Bearer <token>` 或 `access_token` query。

## HTTP + SSE

NapCat 启用 HTTP Server 和 SSE，Cinlan 配置其 base URL：

```dotenv
ONEBOT_TRANSPORT=http_sse
ONEBOT_HTTP_URL=http://127.0.0.1:3000
ONEBOT_ACCESS_TOKEN=<shared-token>
```

Cinlan 从 `GET /_events` 读取事件，并把 action 参数作为 JSON body 发送到
`POST /{action}`。SSE 断线自动退避重连；`429` 会遵循 `Retry-After`。

## Reverse HTTP（反向 HTTP）

NapCat 同时启用：

1. HTTP Server，供 Cinlan 调 action。
2. HTTP Client，把事件上报到 Cinlan。

Cinlan 配置：

```dotenv
ONEBOT_TRANSPORT=reverse_http
ONEBOT_HTTP_URL=http://127.0.0.1:3000
ONEBOT_REVERSE_LISTEN_ADDR=0.0.0.0:3002
ONEBOT_REVERSE_PATH=/onebot/v11/events
ONEBOT_ACCESS_TOKEN=<shared-token>
```

NapCat HTTP Client URL 为 `http://<cinlan-host>:3002/onebot/v11/events`。设置 token
后，Cinlan 按 NapCat 约定校验 `x-signature: sha1=<HMAC-SHA1(raw-body, token)>`，
并校验 `x-self-id` 与事件体一致。

默认 quick-operation 响应为 `{}`，客服回复继续走异步流水线和 OneBot action，避免
长时间占用 NapCat 的事件请求。嵌入式调用方可通过
`ClientConfig.QuickOperationHandler` 返回 `reply`、`at_sender`、`approve` 等
OneBot quick-operation 字段。

## 多账号

```dotenv
ONEBOT_ACCOUNTS_FILE=examples/onebot-accounts.json
SUPPORT_QQ_TOKEN=<shared-token>
```

文件中的 `access_token_env` 只保存环境变量名。出站消息、Cron 和 Admin action
按 account name 或 `self_id` 路由；`GET /api/v1/accounts` 返回各账号连接状态。

## 语音转写与合并转发

入站语音段经过 `get_record`（`out_format=mp3`，由 NapCat 服务端转码，
不需要本地解 silk）取回本地文件后，交给 OpenAI 兼容的
`/audio/transcriptions` 端点转写，模型收到的就是文字而不是 `[语音]` 标记。
整个链路是 best-effort：取文件失败、文件为空或超过 8 MB、转写超时报错，
都只记日志并保留原标记，不阻塞正常回复。

```dotenv
ASR_API_URL=https://api.siliconflow.cn/v1
ASR_API_KEY=sk-...
ASR_MODEL=FunAudioLLM/SenseVoiceSmall
```

`ASR_API_URL` 为空则客户端不创建、特性整体关闭；URL 已含
`/audio/transcriptions` 时原样使用，否则自动补路径。鉴权走
`Authorization: Bearer`（密钥可不配）。推荐 SiliconFlow 的
`FunAudioLLM/SenseVoiceSmall`：免费额度、中文识别好、国内直连；
Groq `whisper-large-v3` 或自建 whisper.cpp 服务同样兼容。

入站合并转发段（`type=forward`）走 `get_forward_msg`，结果摊平成
`昵称: 内容` 的文本行交给模型；非文本内容降级为 `[图片]`、`[语音]`、
`[套娃转发]` 等占位标记，上限 15 行 / 500 字，超长自动截断追加
`…（后面还有）`。

两类 action（`get_record`、`get_forward_msg`，以及表情回应、戳一戳、
发后撤回、`get_stranger_info` 等查询）在多账号部署中都会按收到事件的
`self_id` 路由到对应账号，不再打到默认账号。

## 群事件感知与平台盲区

### 静默感知（不回复、只写入 session 供上下文）
notice 事件经 `observeGroupMemberNotice` 转成 `[群事件]`/`[私聊事件]` 行写入群聊 session 历史，
模型不会主动开口但知道群里发生了什么。当前覆盖：

| notice_type | sub_type | 渲染 |
|---|---|---|
| group_increase | - | `[群事件] 新成员 昵称(QQ号) 加入了本群`（顺带预热 UserCard） |
| group_decrease | - | `[群事件] 成员 昵称(QQ号) 离开了本群` |
| notify | poke | 戳他人 → `[群事件] A 戳了戳 B`；戳自己 → `pokeBudget` 冷却后放行全管线（"对方戳了戳你"） |
| notify | honor | `[群事件] A 拿到了本群的「龙王/群聊之火/…」` |
| group_recall / friend_recall | - | `[群事件] A 撤回了一条消息` / `[私聊事件] 对方撤回了一条消息` |
| group_upload | - | `[群事件] A 上传了群文件「filename」` |
| group_admin | set/unset | `[群事件] A 被设置成了管理员` / `被取消了管理员` |
| group_card | - | `[群事件] A 把群名片改成了「new」`（含旧名片） |
| group_ban | - | `[群事件] A 被禁言了 N分钟` / `被解除禁言了` |
| essence | add | `[群事件] A 的一条消息被设为了精华` |

`group_upload`/`group_card`/`group_ban` 的字段不在 adapter 预提取里，
`noticeString`/`noticeInt` 直接从 `metadata.raw_payload` 现挖 JSON，低频事件零改动 adapter。

### 平台盲区（做不到或不值得做）
- **抢/发红包**：QQ 钱包需实名+绑定，bot 拿不到领取接口权限；只能看到 `[红包:封面文案]`。也做不了"没抢到"的戏份。
- **视频内容**：inbound 只知道文件名（`[视频:xxx.mp4]`），不理解画面——走 ffmpeg 抽帧+VLM 成本过高，暂不做。
- **链接内容**：`[链接:标题]` 只有卡片标题，不去抓网页正文（隐私+成本；用户贴链接问内容时按人设糊弄过去）。
- **群文件内容**：`[文件:x.pdf]` 只有文件名，不下不读。
- **语音入站**：`ASR_API_URL`+`ASR_API_KEY` 未配则 `[语音]` 占位；配了才有转写文字。
- **对方撤回的内容**：只知道"撤回了一条消息"，QQ 不提供原文（NapCat 也拿不到历史缓存的）。
- **精华/禁言权限**：bot 账号非管理员时收不到 group_ban/essence 等部分 notice——不是代码问题。
