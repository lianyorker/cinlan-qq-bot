# Extension API

扩展点都在核心流水线之外注册；扩展不需要了解 OneBot WebSocket 连接细节。

## Registering a command

```go
commands := service.CommandRegistry()
_ = commands.Register(command.Definition{
    Name:        "order",
    Aliases:     []string{"订单"},
    Description: "查询订单状态",
    Handler: func(ctx context.Context, input *command.Context) (command.Result, error) {
        return command.Result{
            Handled: true,
            Reply:   "请提供订单号。",
        }, nil
    },
})
```

命令只在已通过 wake、群白名单和会话路由后执行。`PermissionAdmin` 命令只接受 OneBot `sender.role` 为 `admin` 或 `owner` 的消息。

## Registering a plugin

插件可以实现 `plugin.EventHook`、`plugin.BeforeHook`、`plugin.AfterHook` 中的一个或多个。`EventHook` 在 wake/filter 之前接收 message、notice、request、meta 等全部平台事件。

```go
type faqPlugin struct {
    store *knowledge.Store
}

func (p faqPlugin) Name() string { return "faq" }

func (p faqPlugin) BeforeMessage(
    _ context.Context,
    event *plugin.MessageContext,
) (plugin.Decision, error) {
    hits := p.store.Search(event.Text, 3)
    if len(hits) == 0 {
        return plugin.Decision{}, nil
    }
    event.Values["agent.prompt_context"] = hits[0].Snippet
    return plugin.Decision{}, nil
}
```

`BeforeMessage` 返回 `Ignore` 可以停止事件，返回 `Reply` 或 `Handled` 可以短路 Agent。`AfterMessage` 可以修饰最终回复，但不应把敏感数据写入 `event.Values`。

## Registering a persona

```go
_ = service.PersonaRegistry().Register(persona.Profile{
    Name:         "after-sales",
    Description:  "售后客服",
    SystemPrompt: "你是售后客服，只处理退款、退货和维修问题。",
})
```

管理 API 只能切换到已注册 Persona，不能远程写入任意 system prompt。

## Registering a tool

```go
_ = service.ToolRegistry().Register(tool.Definition{
    Name:        "lookup_order",
    Description: "从内部订单服务读取只读状态",
    Permission:  tool.PermissionEveryone,
    Parameters: map[string]any{
        "type": "object",
        "properties": map[string]any{
            "order_id": map[string]any{"type": "string"},
        },
        "required": []string{"order_id"},
    },
    Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
        // 使用 ctx 的截止时间；不要在这里执行未授权的 shell 或任意 URL。
        return tool.Result{Content: map[string]string{"status": "pending"}}, nil
    },
})
```

OpenAI-compatible provider 只会收到已注册工具。每次调用会校验 JSON 大小（64 KiB）、注册状态、权限和超时；最大 tool-call 轮数由 `AGENT_MAX_TOOL_ROUNDS` 控制。

## Lifecycle and limits

- 在启动阶段注册扩展，避免运行中替换函数导致竞态。
- Registry 会复制列表后执行 hook；单个 hook 的错误会终止当前事件并返回客服错误。
- 插件和工具不能直接访问 OneBot token；需要平台动作时，通过 `platform.Adapter` 或受控的业务服务注入。
- 所有扩展都应添加公共接口测试，并验证空输入、超时、重复消息和权限失败。
