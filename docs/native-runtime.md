# QQNT Native Runtime

## 运行链

```text
cinlan-qq-bot.exe
  -> listen 127.0.0.1:18081
  -> cinlan-qq-loader.exe creates QQ.exe suspended
  -> cinlan-qq-hook.dll is loaded into QQ.exe
  -> QQ resumes and loads QQNT.dll
  -> hook patches QQNT's startup guard and redirects only Cinlan entry reads
  -> official app launcher starts
  -> runtime/qqnt/runtime.cjs attaches wrapper/session services
  -> authenticated NDJSON IPC
```

官方 `package.json`、`application.asar`、`wrapper.node` 和其他 QQ 文件不会被改写。

## Loader 边界

`cinlan-qq-loader.exe` 只执行：

1. `CreateProcessW(..., CREATE_SUSPENDED)`。
2. `VirtualAllocEx` + `WriteProcessMemory` 写入 Cinlan hook 路径。
3. `CreateRemoteThread(LoadLibraryW)` 加载 Cinlan hook。
4. `ResumeThread`。

hook 在主线程恢复前完成以下最小 patch：

1. patch `QQ.exe` 主模块的 `KERNEL32.dll!GetProcAddress` IAT。
2. inline hook `kernelbase.dll!GetFileInformationByName`（系统提供时）。
3. `QQ.exe` 查询 `ExportedContentMain` 时，在 `QQNT.dll` 的 executable section
   中匹配受约束的 25-byte 控制流签名，将该启动保护分支从 `0F 85` 改为
   `0F 84`。
4. 签名 patch 成功后，patch `QQNT.dll` 的 `KERNEL32.dll!CreateFileW` IAT。

签名或 QQNT IAT 匹配失败时不安装文件重定向；IAT patch 失败时会恢复已修改的
分支。不会全局 inline hook `CreateFileW`、`LoadLibraryExW` 或 attributes API。

`GetFileInformationByName` 使用预先保存的系统 `CreateFileW` +
`GetFileInformationByHandleEx` 兼容 shim，保证 Node/libuv 在没有打开文件句柄的
stat 路径上也能发现自有入口。路径重定向只匹配以下后缀：

```text
\resources\app\package.json
\resources\app\loadCinlan.js
\resources\app\application.asar\loadCinlan.js
```

其他路径不替换目标文件；QQNT `CreateFileW` IAT 的非目标调用继续转发到原始
实现。

## IPC v1

传输：

- TCP loopback only。
- UTF-8 NDJSON，一行一个 envelope。
- 默认单帧最大 1 MiB，Go 和 QQNT 两端共同使用 `QQNT_MAX_FRAME_BYTES`。
- 首帧必须为 `hello`，token 使用 constant-time comparison。
- action 默认 10 秒超时。
- QQ 登录时同步的、早于 Cinlan runtime 启动时间的历史消息不会进入客服流水线。

通用 envelope：

```json
{
  "v": 1,
  "type": "action",
  "id": "native-1",
  "payload": {}
}
```

Runtime -> Go：

```text
hello
runtime_status
event
action_result
```

Go -> Runtime：

```text
hello_ack
action
```

握手：

```json
{
  "v": 1,
  "type": "hello",
  "token": "<runtime-token>",
  "payload": {
    "runtime": "cinlan-qqnt",
    "pid": 1234,
    "qq_version": "9.9.31-49738",
    "capabilities": ["message_event", "send_message", "send_file", "runtime_status"]
  }
}
```

ready 状态要求同时满足：

```text
state == ready
self_id != empty
wrapper_loaded == true
session_attached == true
```

`runtime_status.last_error` 会把 wrapper、login 或 session attach 的最近错误回传到 Go 日志。
错误不会被 fallback 隐藏；错误消失后该字段自动清空。

## Action

`send_message`：

```json
{
  "name": "send_message",
  "params": {
    "chat_type": "group",
    "chat_id": "123456789",
    "reply_to": "7200000000000000000",
    "quote": true,
    "chain": [
      {
        "type": "text",
        "data": {
          "text": "客服回复"
        }
      }
    ]
  }
}
```

`runtime_status` 无参数，返回当前账号、wrapper 和 session 状态。

私聊在线文件仍使用 `send_message`，但 chain 中只能包含一个 `file` component，
不能与引用或文本混发：

```json
{
  "name": "send_message",
  "params": {
    "chat_type": "private",
    "chat_id": "10000002",
    "quote": false,
    "chain": [
      {
        "type": "file",
        "data": {
          "file": "D:\\files\\database.sql",
          "name": "database.sql"
        }
      }
    ]
  }
}
```

runtime 会验证绝对路径和普通文件类型，通过 QQNT 在线文件消息发送，并查询消息
记录确认成功。群文件尚未开放。

## 更新验证

QQ 更新后先执行：

```powershell
Get-ChildItem <QQNT>\versions -Recurse -Filter wrapper.node
Get-FileHash <wrapper.node> -Algorithm SHA256
.\scripts\build.ps1
```

再检查：

```powershell
Invoke-RestMethod http://127.0.0.1:18080/readyz
Invoke-RestMethod http://127.0.0.1:18080/api/v1/platforms `
  -Headers @{ Authorization = "Bearer $env:ADMIN_API_TOKEN" }
```

如果 `/readyz` 为 503，按日志中的 `wrapper_loaded`、`session_attached` 和 `state` 定位，不通过 fallback 隐藏失败。

可直接查询 runtime：

```powershell
$headers = @{ Authorization = "Bearer $env:ADMIN_API_TOKEN" }
$body = @{ action = "runtime_status"; params = @{} } | ConvertTo-Json
Invoke-RestMethod http://127.0.0.1:18080/api/v1/actions `
  -Method Post -Headers $headers -ContentType application/json -Body $body
```

## 当前实机基线

版本 `9.9.31-49738` 已验证：

- release hook SHA256：
  `3F16E1BEC5BF200DE3864F6B44DB0A0B03AA36A8B3D773AA7EA3E655E92BE407`。
- 两次完全冷启动分别稳定运行至少 200 秒和 201 秒。
- QQ 主窗口和子进程树正常，`/readyz` 持续返回 HTTP 200。
- `wrapper_loaded`、`session_attached` 和平台连接均进入 ready。
- 官方 `package.json`、`wrapper.node` 和 `QQNT.dll` hash 未变化。

尚未验证真实群消息或私聊消息的 `event -> Agent -> sendMsg` 闭环。当前客服
pipeline 仅处理群消息；QQNT runtime 虽能上报 private event，私聊尚未接入
Agent pipeline 和统一回复路径。
