# OneBot 传输层

OneBot 11 是 QQ 接入的唯一通道，连接外部服务（如 NapCat）。

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

## 拟人化能力四维盘点

### 消息回复
- **引用回复**：`QQ_QUOTE_REPLY`（默认开）+ `QQ_QUOTE_GROUP_PROB`（群 40%）+ `QQ_GROUP_AT_SENDER` 兜底 @——群聊只在上下文歧义时带引用，私聊永不带，和真人习惯一致
- **打字节奏**：readDelay（按入站字数）+ replyPartDelay（按出站字数）模拟读完才回
- **消息合并**：batch.go 双窗口 + 悬挂标点续等，连发合并成一个 run
- **拆分回复**：splitReply 按 `MaxReplyRunes`/`MaxReplyChunks` 截断，句末优先回退

### 安全防御（按 pipeline 顺序）
`wake(解析/富化)` → `security` → `command` → `moderation` → `session` → `attention` → `rate_limit` → `plugin` → `agent` → `decorate` → `plugin_after` → `respond`
- 转发展开/语音转写文本在 wake 完成，**全部过 security** —— 嵌套转发里藏 prompt 注入也会被拦
- 注入命中：静默 ignore + 记安全事件，不回固定拒信（不暴露 bot 身份）
- 未 @ 她但 @ 了别人的攻击消息：直接静默，不替人挡枪
- 反混淆：`\xNN`/`\uNNNN` 自动解码；hex 正则字符集交集探测拦截
- 敏感文件：`.env`/`_deploy_bot_key`/`id_rsa`/`chat-bindings.json`/`sessions.db`/`mcp-servers.json` 读取匹配请求直接拦
- 出站熔断：`sk-`/`AIza`/`Bearer`/`PRIVATE KEY`/env 键名命中即熔断不发
- 上下文投毒：`AddObservation` 在 security 通过之后才写 session，拦掉的恶意 payload 不留痕
- attention unsafe 分类硬拦，不看 confidence

### 提示词拟人化（每次回复注入的"活"上下文）
- 当前时间+星期（`当前时间：2026-10-25 01:30 周日`）；深夜 `Asleep` 注入"你很困，回复短一点迷糊一点"
- 距上次活动间隔（prevActivityAt → "上次聊是 N 小时前"）
- 说话人身份：群员/管理员/群主 roleLabel、UserCard 资料行（昵称/性别/地区/头衔）
- 关系层：affinity 亲密度提示、persona 印象（chat-local 优先）、learned facts ≤420 字、recalled 旧记忆 ≤300 字（带"N 小时前记的"锚点）
- 状态层：mood 分提示、peer bot 声明、语音/唱歌开关、贴纸池 note、管理员备忘
- 边界规则：群聊不复述他人内容/链接、同一话题结论保持一致（改口明说"我刚说错了"）、被叫 AI 当场否认

### 操控能力（tool/MCP 现装配）
- **已有**：generate_image（mediaLimiter 限流+失败文案兜底）、fileCatalog 文件投递、cron 定时任务、poke、channelaction（管理动作封装）、remember 记忆工具、MCP 服务器（amap 地图、my-coffee 下单）+ 47 个 skill 文档包
- **没有**：浏览器自动化、手机操控、本地软件/桌面操控——QQ 协议栈外，接进来等于给聊天窗口开系统权限，攻击面太大，**不做**

### 剩余真盲区（平台/成本限制，见上一节）
红包收发（钱包实名）、视频内容理解（抽帧+VLM 成本）、链接/文件正文抓取（隐私+成本）、撤回原文（QQ 不给）、非管理员群管理 notice（平台权限）
