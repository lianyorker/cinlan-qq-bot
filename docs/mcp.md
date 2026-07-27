# MCP 扩展

`cinlan-qq-bot` 可以把受信任的 MCP Streamable HTTP 服务发现为 OpenAI-compatible
tool-call。实现遵循 MCP `2025-11-25` 的初始化、协议版本协商、`MCP-Session-Id`、
`tools/list` 分页和 `tools/call` 契约，并兼容服务端协商的
`2025-06-18`、`2025-03-26`、`2024-11-05` 版本。

## 配置

设置：

```dotenv
MCP_SERVERS_FILE=data/mcp.json
```

`data/mcp.json` 使用常见的 `mcpServers` 结构：

```json
{
  "mcpServers": {
    "crm": {
      "url": "https://mcp.example.com/mcp",
      "transport": "streamable_http",
      "permission": "everyone",
      "timeout": "10s",
      "tool_timeout": "30s",
      "header_env": {
        "Authorization": "CRM_MCP_AUTH"
      },
      "allow_tools": [
        "search_customer"
      ]
    }
  }
}
```

不要把 token 写入 JSON。`header_env` 的值是环境变量名，启动时必须存在且非空。
`permission` 默认是 `admin`；只有明确配置为 `everyone` 的远程工具才允许普通群成员
触发。`allow_tools` 为空表示允许该服务发现的全部工具。

每个 MCP 工具会注册为 `mcp_<server>_<tool>`，名称会清理为
`[A-Za-z0-9_-]` 并限制在 64 个字符以内。远程工具描述和结果属于不可信输入，仍受
本地 JSON 参数大小、权限和超时限制。

当前只启动 HTTP(S) Streamable HTTP，不执行 MCP 配置中的 `command` 或其他本地进程。
stdio、旧版独立 SSE endpoint、server-to-client sampling/elicitation 和 task-augmented
execution 尚未接入。

## 运维

需要 `ADMIN_API_TOKEN`，请求使用：

```http
GET /api/v1/mcp
Authorization: Bearer <ADMIN_API_TOKEN>
```

重新初始化会话并重新发现工具：

```http
POST /api/v1/mcp/refresh
Authorization: Bearer <ADMIN_API_TOKEN>
```

刷新失败时返回 `502`，但上一次成功发现的工具会保留，避免瞬时网络故障导致客服能力
消失。进程退出前会尝试用 `DELETE` 关闭 MCP session；服务端返回 `405` 或 `404` 会被
视为已关闭。
