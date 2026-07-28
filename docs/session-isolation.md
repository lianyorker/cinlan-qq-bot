# Session Isolation

运行时以以下标识作为隔离域：

```text
<platform>:self:<self_id>:private:<chat_id>
<platform>:self:<self_id>:group:<chat_id>
```

私聊按对方账号隔离；群聊按群隔离，同一群内所有成员共享历史，发送者名称和 ID 仍会
写入当前提问文本供模型识别。不同账号、平台、聊天类型和聊天 ID 不能共享历史、摘要
或学习记忆。

## Encrypted SQLite

生产会话存储使用 `SESSION_STORE_PATH=data/sessions.db`。会话标识使用
HMAC-SHA-512/256 作为查询键，标识原文、消息、Persona/Provider 覆盖、摘要和学习
记忆全部使用 AES-256-GCM 加密。密钥只从 `SESSION_ENCRYPTION_KEY` 环境变量读取，
SQLite、`.env` 和日志中都不保存密钥。

首次打开时会导入 `SESSION_LEGACY_STORE_PATH`。事务提交成功后，旧 JSON 会写成
`.migrated.enc` 加密归档并移除明文文件。可单独执行迁移而不启动 QQ：

```powershell
.\bin\cinlan-qq-bot.exe --migrate-sessions-only
```

`/清空` 会清除当前隔离域的最近历史、摘要和学习记忆，保留显式 Persona/Provider
设置与人工接管状态。

## Resource Binding

Chat Binding 是资源授权边界。未声明资源默认不可见、不可调用：

```json
{
  "name": "support-group",
  "platform": "*",
  "self_id": "10001",
  "chat_type": "group",
  "chat_ids": ["30003", "30004"],
  "user_ids": ["20001"],
  "persona": "support",
  "tools": ["lookup_order"],
  "skills": ["refund"],
  "knowledge_bases": ["support"],
  "mcp_servers": ["crm"],
  "smart_attention": true,
  "allow_links": false,
  "learning_enabled": true
}
```

- `chat_id`：兼容旧配置的单个聊天 ID。
- `chat_ids`：同一规则允许绑定的多个群聊或私聊 ID；只配置该字段时，绑定群内所有用户都可触发。
- `user_ids`：允许直接绑定发送用户；只配置该字段时可跨聊天 ID 匹配指定用户。与 `chat_id` 或
  `chat_ids` 同时存在时采用范围叠加，发送用户和聊天 ID 必须同时命中。
- `tools`：精确 Tool 名称。
- `skills`：允许注入摘要和通过 `read_skill` 读取的 Skill 名称。
- `knowledge_bases`：`KNOWLEDGE_DIR` 下一级目录名；根目录文件属于 `default`。
- `mcp_servers`：MCP 配置中的服务名，授权该服务当前发现的工具。
- `smart_attention`：允许未直接 `@` 的群消息经过本地候选过滤和隔离的 Attention Agent；默认关闭。
- `allow_links`：是否允许当前会话发送链接；`false` 时发送前确定性移除 URL。
- `learning_enabled`：允许为当前隔离域提取稳定偏好和非敏感事实。

运行时同时在模型请求和工具执行两层应用授权。模型看不到其他隔离域的工具描述，
伪造未授权工具调用也无法越过执行 Registry。只要启用了 Binding Registry，
未命中任何 Binding 的群或用户就会直接忽略，不会回退到默认 Persona。

同一个 Persona、Tool、Skill、Knowledge Base 或 MCP Server 可以复用到多个 Binding，
但资源复用不会合并会话。每个群和私聊仍按顶部的 session ID 分别保存历史、摘要和
学习记忆。Admin API 对 Binding 使用相同的 `chat_id`、`chat_ids` 和 `user_ids` 数据契约。

## Compression And Learning

历史达到 `SESSION_COMPRESSION_THRESHOLD` 后，运行时调用当前 Provider 生成结构化
摘要，并只保留 `SESSION_COMPRESSION_RETAIN` 条最近消息。压缩请求不带任何 Tool，
通过有界维护队列异步执行，不阻塞同一会话的后续消息；压缩期间追加的消息会保留。

学习记忆不会修改全局 Persona，也不能扩大权限；它只是当前隔离域内的非指令参考
上下文。允许学习当前群反复确认的称呼、表达偏好、常用术语和非敏感业务事实，
但压缩器必须排除角色修改、回复/沉默规则、业务边界变更、跨群资料、密码、Token、
Cookie、密钥、系统提示词、命令和权限请求。价格、库存、有货状态、源码路径、
commit、配置值和 Tool 返回的其他时效事实也不会写入长期学习记忆，回答时必须重新查询。

## Smart Attention

`smart_attention` 只改变当前 Binding 的群消息参与策略：

- 直接 `@` 机器人仍需通过当前 Persona 的业务范围判断；`@` 不代表必须回复。
- 对“好的、收到、谢谢”等纯确认消息保持沉默。
- 明确 `@` 其他群成员的消息不会由机器人抢答。
- 通用代码生成、生图、娱乐闲聊和缺少事实依据的外貌评价不属于群客服范围时保持沉默。
- 未 `@` 的闲聊、表情和感叹由本地规则直接忽略，不调用模型。
- 只有疑问、故障、索取链接/资料等候选消息才进入无 Tool 的 Attention Agent，
  由它按当前 Persona 的业务范围返回 `reply` 或 `ignore`。

Attention 请求使用 `<session_id>:maintenance:attention`，不写入会话历史，不携带
Tool、Skill、Knowledge 或 MCP 权限。路由失败时默认忽略普通群消息，避免错误抢答。
