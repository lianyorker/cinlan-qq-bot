# Cron 持久化

`Cron` 任务由管理 API 创建，并默认保存到 `CRON_STORE_PATH`（默认
`data/cron.json`）。保存使用临时文件加原子替换，目录权限为 `0700`，任务文件权限
为 `0600`；将路径设为空字符串可以恢复纯内存模式。

任务创建、下一次执行时间、最近一次错误和启用状态都会落盘。进程重启后会恢复任务；
如果 `next_run` 已经到期，下一轮调度会执行一次，随后按原间隔推进。单任务仍然
不会并发执行，最小间隔为 10 秒。

```http
POST /api/v1/jobs
Authorization: Bearer <ADMIN_API_TOKEN>
Content-Type: application/json

{
  "id": "daily-notice",
  "name": "daily notice",
  "chat_type": "group",
  "chat_id": "123456789",
  "text": "今日服务公告",
  "interval_seconds": 86400
}
```

删除任务使用 `DELETE /api/v1/jobs/{id}`。如果落盘失败，创建会失败并回滚内存状态；
删除接口返回 `500`，并保留任务以便管理员重试。发送失败只记录 `last_error`，不会
丢失下一次调度。
