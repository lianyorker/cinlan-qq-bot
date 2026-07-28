# Web Screenshot Tool

`capture_webpage` 发送的是浏览器实际渲染的 PNG，不是 AI 生图。它只在绑定允许该 Tool 时暴露给模型，截图文件与生图文件都位于 `BOT_ALLOWED_ROOT` 内。

## 安全边界

- 入口只接受 `https`，拒绝 credentials、fragment、IP literal、localhost、非 443 端口和未列入白名单的 host。
- `WEB_SCREENSHOT_ALLOWED_HOSTS` 是精确匹配，不支持通配符。页面发生 redirect 时，redirect 的每一跳都重新校验。
- Chrome 使用独立临时 profile，不复用用户登录态。
- Chrome 的网络请求经过一次性本地代理；代理解析 DNS 后拒绝 loopback、private、link-local、CGNAT、保留地址和 multicast，只连接公网地址，并限制到 80/443。
- 输出文件会校验普通文件、PNG magic、解码尺寸和最大字节数；超过保留时间的 PNG 会在下一次任务开始时清理。

## 配置

```dotenv
WEB_SCREENSHOT_ENABLED=true
WEB_SCREENSHOT_BROWSER_PATH=C:\Program Files\Google\Chrome\Application\chrome.exe
WEB_SCREENSHOT_ALLOWED_HOSTS=example.com,github.com,gitee.com
WEB_SCREENSHOT_OUTPUT_DIR=data/generated-images
WEB_SCREENSHOT_TIMEOUT=45s
WEB_SCREENSHOT_RETENTION=24h
WEB_SCREENSHOT_MAX_BYTES=20971520
WEB_SCREENSHOT_WIDTH=1365
WEB_SCREENSHOT_HEIGHT=768
WEB_SCREENSHOT_WAIT=3s
```

当前绑定还需要在 `data/chat-bindings.json` 的 `tools` 中显式加入 `capture_webpage`。Persona 应明确区分“截图网页”和“生成图片”：前者调用 `capture_webpage`，后者调用 `generate_image`。

## 使用

私聊或已绑定群聊中，直接发送：

```text
@机器人 访问官方站点首页截图给我
```

如果只说“截图”但没有网站，助手会先追问网站，不会猜测地址。实际允许范围以当前
`.env` 与 Chat Binding 为准；公开配置不要包含内部域名、账号信息或带凭据的 URL。

## 限流

`generate_image` 与 `capture_webpage` 共用媒体限流器，按
`<platform>:<self_id>:<user_id>` 计数，因此同一 QQ 不能通过切换群聊绕过限制。默认同一用户 30 秒一次、1 小时最多 10 次，全局最多 2 个媒体任务并行。超过限制时直接返回固定的短提示，不再让模型重复调用。
