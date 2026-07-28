# Skills

`SKILLS_DIR` 指向一个本地目录。每个一级子目录可以包含 `SKILL.md`（也兼容
小写的 `skill.md`）：

```text
data/skills/
  refund/
    SKILL.md
  delivery/
    SKILL.md
```

文件可以使用简单 frontmatter：

```markdown
---
name: refund
description: 处理退款、退货和支付争议问题。
---

# Refund

这里是模型在触发该能力后需要遵守的详细流程。
```

启动时只读取名称、描述和文件大小。只有 Chat Binding 的 `skills` 明确授权的能力
摘要才会加入当前隔离域的 system prompt。模型必须
调用 `read_skill` 读取正文后才能使用该能力；`list_skills` 和 `read_skill` 都会进入
现有的隔离域、权限、参数大小和超时检查。即使模型伪造其他 Skill 名称，执行端也会
拒绝。运行时不会执行 skill 目录中的脚本或拼接任意路径。

限制：

- 最多 256 个 skill；
- 每个 `SKILL.md` 最大 256 KiB；
- 单次 `read_skill` 返回正文最多 12000 Unicode rune；
- skill 名称只能包含 ASCII 字母、数字、`.`、`_`、`-`。

管理接口（需要 `ADMIN_API_TOKEN`）：

```http
GET /api/v1/skills
POST /api/v1/skills/refresh
Authorization: Bearer <ADMIN_API_TOKEN>
```

刷新会原子替换内存中的元数据；刷新失败时保留上一份可用目录。
