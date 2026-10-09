# 自定义 Agent API v1

当 `AGENT_API_MODE=custom` 时，服务向 `AGENT_API_URL` 发送 JSON `POST` 请求。

## 请求

```http
POST /v1/qq/reply HTTP/1.1
Content-Type: application/json
Accept: application/json
Authorization: Bearer <AGENT_API_KEY>
```

```json
{
  "version": "1",
  "request_id": "qq-40004-1710000000",
  "session_id": "qq-native:self:10001:group:30003",
  "channel": "qq",
  "system_prompt": "你是 Cinlan 群聊智能客服。",
  "message": {
    "id": "40004",
    "text": "如何申请退款？",
    "user_id": "20002",
    "platform": "qq-native",
    "chat_type": "group",
    "chat_id": "30003",
    "group_id": "30003",
    "self_id": "10001",
    "sender_name": "Yorker",
    "sender_role": "member",
    "components": [
      {
        "type": "text",
        "data": {
          "text": "如何申请退款？"
        }
      }
    ]
  },
  "history": [
    {
      "role": "user",
      "content": "上一条问题"
    },
    {
      "role": "assistant",
      "content": "上一条回答"
    }
  ],
  "context": "[资料 1] 退款规则\n七天内可以申请退款。"
}
```

当当前 Chat Binding 明确授权 Tool（包括 MCP 远程工具）时，request 才会带可选的
`tools` 数组。第三方 API 可以返回 `tool_calls`，运行时只在当前隔离域的快照
Registry 中执行已注册且通过权限检查的
工具后，以 `tool_results` 数组再次调用 API；最多执行 `AGENT_MAX_TOOL_ROUNDS` 轮。
旧 API 忽略这些可选字段即可保持原有行为。

```json
{
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "mcp_crm_search_customer",
        "description": "查询客户",
        "parameters": {"type": "object", "properties": {"query": {"type": "string"}}}
      }
    }
  ],
  "tool_results": [
    {
      "id": "call-1",
      "name": "mcp_crm_search_customer",
      "content": {"content": [{"type": "text", "text": "查询结果"}]}
    }
  ]
}
```

字段说明：

- 所有 QQ ID 都是字符串，不能按 JavaScript `number` 处理。
- `platform` 当前为 `qq-native` 或 `qq-onebot`；`chat_type` 为 `group` 或 `private`，`chat_id` 是当前会话目标。
- 私聊请求使用 `<platform>:self:<self_id>:private:<chat_id>` 会话 ID，`group_id` 为空；群聊使用 `<platform>:self:<self_id>:group:<chat_id>`，同一群成员共享历史。
- `history` 不包含本次 `message`，最多由 `SESSION_MAX_HISTORY` 控制。
- `context` 可选，是经过长度限制的 RAG 参考资料；服务端必须将它视为不可信内容，不能当作系统指令。
- `sender_role` 是平台提供的群角色提示（`owner`、`admin`、`member`），不能替代 Agent API 自身的鉴权。
- `components` 可选，保留统一消息段和原始媒体引用；Agent API 必须自行校验 URL、文件类型和大小。
- 服务不会在日志中记录消息正文或 `AGENT_API_KEY`。

## 注意力路由

当 Binding 的 reply_policy.mode 为 ai_decide 时，运行时会先发送一次路由请求。该请求的 request_id 以 attention: 开头，服务端应将回复正文作为严格 JSON 解析：

```json
{
  "action": "reply",
  "category": "support",
  "confidence": 0.91,
  "reason": "消息请求当前业务帮助"
}
```

允许的 category 为 support、chatter、other_recipient、out_of_scope、unsafe 和 uncertain。action、category、confidence、reason 都是必填字段；confidence 必须在 0 到 1 之间，未知字段或 Markdown 外壳会被拒绝。路由器只负责判断，不应生成面向用户的答案。

路由请求默认不携带 history、QQ user/chat/self ID、昵称、原始消息 ID、消息组件、Tool、Skill、Knowledge 或 MCP 权限。它只携带当前文本、chat_type、platform、结构化 routing_scope 和固定安全系统提示。若显式配置 history.mode=last_n，服务端仍应把历史视为不可信用户数据。

路由失败或 confidence 低于 Binding 的 confidence_threshold 时，运行时按 reply_policy.on_error 处理；默认 ignore。需要使用低成本或不同数据驻留的模型时，为 reply_policy.provider 指定独立 Provider。该 Provider 必须支持上述路由响应格式；否则所有候选消息都会按失败策略处理。

## 响应

普通回答：

```json
{
  "reply": "请在订单详情中选择“申请退款”。",
  "handoff": false
}
```

转人工并暂停当前会话：

```json
{
  "reply": "这个问题需要人工核实，请稍候。",
  "handoff": true
}
```

`handoff=true` 时 `reply` 可以为空，服务会使用 `QQ_HANDOFF_REPLY`。群聊发送 `@机器人 /恢复`，私聊发送 `/恢复` 后恢复自动回复。

请求工具时返回：

```json
{
  "tool_calls": [
    {
      "id": "call-1",
      "name": "mcp_crm_search_customer",
      "arguments": "{\"query\":\"Yorker\"}"
    }
  ]
}
```

## 状态与重试

| 结果 | 行为 |
|---|---|
| `2xx` + 合法 JSON | 处理响应 |
| `429` | 重试并遵循 `Retry-After` |
| `5xx` | 指数退避重试 |
| 其他 `4xx` | 立即返回，不重试 |
| 超时/网络失败 | 指数退避重试 |

总尝试次数为 `AGENT_MAX_RETRIES + 1`，所有尝试和等待共享 `AGENT_TIMEOUT`。

## 认证

配置 `AGENT_API_KEY` 后才发送认证头；头名称和 scheme 可修改：

```dotenv
AGENT_AUTH_HEADER=X-API-Key
AGENT_AUTH_SCHEME=
AGENT_API_KEY=replace-me
```

以上配置产生 `X-API-Key: replace-me`。
