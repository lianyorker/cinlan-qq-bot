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
      "private_only": true,
      "allowed_group_ids": ["123456789"]
    }
  ]
}
```

`path` 可以是绝对路径，也可以是相对 catalog 文件所在目录的路径。启动时会解析
符号链接并验证文件存在、为普通文件且不超过大小上限；发送前会再次验证。

`allowed_group_ids` 限制文件所属群聊。群聊请求必须来自其中一个群；私聊请求会在
发送前实时查询用户是否属于其中任一群。查询失败、已退群或仅添加 Bot 好友都不会
获得文件。省略或配置空数组时保持兼容，不额外限制群聊归属。

启用：

```dotenv
FILE_CATALOG_PATH=data/file-catalog.json
FILE_DELIVERY_MAX_BYTES=104857600
FILE_DELIVERY_TIMEOUT=30s
```

需要让关联群成员添加 Bot 后私聊取文件时，私聊白名单必须允许这些用户：

```dotenv
QQ_PRIVATE_ALLOWLIST=*
```

生产环境也可以改为明确的 QQ 号列表。

## 对话流程

群聊：

```text
用户：@Bot 给我 SQL
Bot 已经是用户好友：
  Bot：已通过私聊发送文件“database.sql”，请查收。

Bot 还不是用户好友：
  Bot：该文件仅通过私聊发送。请先添加 QQ <Bot QQ> 为好友，然后私聊发送“sql”获取。
```

私聊：

```text
用户：sql
Bot：发送 database.sql
Bot：已发送文件“database.sql”，请查收。
```

`deliver_file` 是终止型工具。群聊请求会先尝试向发起人私聊发送；发送失败时再返回
加好友引导。成功确认和引导均由工具直接生成，不依赖模型二次转述；同一个入站消息
重复触发不会重复发送文件。

## 平台行为

- QQ Native：`private_only=true` 使用 QQNT 普通文件上传消息发送；群聊请求会转为向发起人
  私聊发送，临时会话无法发送时返回加好友引导。
- OneBot 11：通过统一 `file` message segment 发送。
- 其他平台：后续 Adapter 只需实现 `platform.Outbound` 中的 `file` component。

QQ Native 当前只支持私聊普通文件上传。需要群文件时应配置 `private_only=true`，
或后续单独接入群文件上传 API。
