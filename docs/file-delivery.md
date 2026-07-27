# Agent 文件交付

文件交付由 `deliver_file` Agent Tool 完成。模型只能选择管理员配置的
`file_id`，不能提交本地路径。

## 配置

参考 [examples/file-catalog.json](../examples/file-catalog.json)：

```json
{
  "version": 1,
  "files": [
    {
      "id": "sql",
      "name": "SQL 脚本",
      "aliases": ["sql", "数据库脚本", "初始化脚本"],
      "description": "项目数据库初始化脚本",
      "path": "files/database.sql",
      "display_name": "database.sql",
      "enabled": true,
      "private_only": true
    }
  ]
}
```

`path` 可以是绝对路径，也可以是相对 catalog 文件所在目录的路径。启动时会解析
符号链接并验证文件存在、为普通文件且不超过大小上限；发送前会再次验证。

启用：

```dotenv
FILE_CATALOG_PATH=data/file-catalog.json
FILE_DELIVERY_MAX_BYTES=104857600
FILE_DELIVERY_TIMEOUT=30s
```

需要让任意群成员添加 Bot 后私聊取文件时，私聊白名单必须允许这些用户：

```dotenv
QQ_PRIVATE_ALLOWLIST=*
```

生产环境也可以改为明确的 QQ 号列表。

## 对话流程

群聊：

```text
用户：@Bot 给我 SQL
Bot：该文件仅通过私聊发送。请先添加 QQ <Bot QQ> 为好友，然后私聊发送“sql”获取。
```

私聊：

```text
用户：sql
Bot：发送 database.sql
Bot：已发送文件“database.sql”，请查收。
```

`deliver_file` 是终止型工具。群聊引导和私聊成功确认由工具直接生成，不依赖模型
二次转述；同一个入站消息重复触发不会重复发送文件。

## 平台行为

- QQ Native：`private_only=true` 使用 QQNT 在线文件消息发送。
- OneBot 11：通过统一 `file` message segment 发送。
- 其他平台：后续 Adapter 只需实现 `platform.Outbound` 中的 `file` component。

QQ Native 当前只支持私聊在线文件。需要群文件时应配置 `private_only=true`，
或后续单独接入群文件上传 API。
