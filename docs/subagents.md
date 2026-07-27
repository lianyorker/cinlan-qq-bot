# Subagents

`SUBAGENTS_FILE` 配置同步 handoff 子 Agent。它们使用同一个 Agent API，但每个实例有
独立的 system prompt；主 Agent 通过 `transfer_to_<name>` tool 决定是否转交。

```dotenv
SUBAGENTS_FILE=data/subagents.json
```

配置格式：

```json
{
  "agents": [
    {
      "name": "after_sales",
      "description": "处理退款、退货和维修问题",
      "system_prompt": "你是售后专员，只处理售后问题；不确定时返回需要人工核实。",
      "permission": "everyone",
      "tools": ["read_skill", "mcp_crm_search_customer"]
    }
  ]
}
```

也兼容 AstrBot 风格的 `subagent_orchestrator.agents` 外层。`permission` 默认
`everyone`；`tools` 为空表示子 Agent 不使用工具，配置后只会得到列出的工具快照，
不会修改主 Agent 的工具集合。每次 handoff 是同步调用，结果以 tool result 返回给主
Agent；当前未实现后台任务、跨 provider override 或任务队列。

子 Agent 请求包含原 QQ 用户/群 ID 和 `task`，但不会把主会话历史自动复制进去。需要
历史时由主 Agent 在 task 中提供摘要，避免跨角色泄露完整会话。

管理接口（需要 `ADMIN_API_TOKEN`）：

```http
GET /api/v1/subagents
POST /api/v1/subagents/refresh
Authorization: Bearer <ADMIN_API_TOKEN>
```

刷新失败时保留上一版 handoff tools。
