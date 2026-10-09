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
