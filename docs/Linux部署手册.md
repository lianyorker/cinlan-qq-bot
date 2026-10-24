# Linux 部署手册

本文档描述 `cinlan-qq-bot` 当前生产部署的完整实况：目录布局、`.env` 全量配置、systemd、部署/升级流程、NapCat 接入与风控注意事项。真实密钥一律打码不入库，其余为生产实际值。

## 生产环境一览

- **部署目录**：`/opt/codepany/cinlan-qq-bot/bundle`
- **systemd 单元**：`cinlan-qq-bot.service`（`WorkingDirectory=/opt/codepany/cinlan-qq-bot/bundle`）
- **QQ 接入**：NapCat OneBot forward WebSocket `ws://127.0.0.1:3001`
- **本机 HTTP**：`127.0.0.1:8080`（admin API，配 `ADMIN_API_TOKEN`）
- **Agent 模式**：`custom`，多 provider（`data/providers.json`，当前主力 gemini，deepseek 备用）
- **知识库**：`data/knowledge/`（cinlan / zkmall 两域隔离，绑定级分发）

## 前置条件

- Linux x86_64（glibc ≥ 2.28），当前 1C2G 阿里云 ECS 可跑
- Go 1.22+（Windows 交叉编译，服务器不装 Go）
- 官方 QQNT Linux 客户端 + NapCat Shell 模式
- Xvfb、xauth、jq（无桌面环境必需）

```bash
# Debian / Ubuntu
sudo apt install -y xvfb xauth jq
# Fedora / Alibaba Cloud Linux（当前生产）
sudo dnf install -y xorg-x11-server-Xvfb xorg-x11-xauth jq
```

## 构建与部署

Windows 交叉编译（本地仓库根目录）：

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'
go build -o cinlan-qq-bot-linux ./cmd/cinlan-qq-bot
$env:GOOS='windows'
```

部署（生产实际流程，先传 `.new` 再原子替换）：

```powershell
scp -i _deploy_bot_key -o StrictHostKeyChecking=no cinlan-qq-bot-linux root@SERVER:/opt/codepany/cinlan-qq-bot/bundle/cinlan-qq-bot.new
ssh -i _deploy_bot_key root@SERVER "cd /opt/codepany/cinlan-qq-bot/bundle && chmod +x cinlan-qq-bot.new && mv cinlan-qq-bot.new cinlan-qq-bot && systemctl restart cinlan-qq-bot.service && systemctl is-active cinlan-qq-bot.service"
```

> **注意**：服务器对高频 SSH 连接有风控会丢包，别连续猛连；失败等 30-60 秒再试。

## 目录布局（生产实况）

```text
/opt/codepany/cinlan-qq-bot/bundle/
├─ cinlan-qq-bot              # 二进制（部署时 .new → 原子替换）
├─ .env                       # 环境变量（不入库）
└─ data/                      # 运行时数据（不入库，全部被 Git 忽略）
   ├─ chat-bindings.json      # 人格/聊天/工具/知识库绑定表
   ├─ personas.json           # 人格定义
   ├─ providers.json          # LLM provider 配置
   ├─ chat-prefs.json         # 每聊天备忘/心情/静音/事实/印象
   ├─ affinity.json           # 亲密度
   ├─ sessions.db             # 会话持久化（bolt）
   ├─ cron.json               # 定时任务（有任务才生成）
   ├─ moderation.json         # 审核状态
   ├─ chat-images/            # /记住 存的生图参考
   ├─ generated-images/       # 生图产物
   ├─ web-screenshots/        # 网页截图产物
   ├─ knowledge/              # cinlan / zkmall 知识库
   ├─ voice/                  # TTS 语音缓存
   └─ zkmall.sql              # 好友申请自动发送的 SQL 文件
```

## `.env` 生产配置参考

以下为当前生产实际配置（密钥打码），按功能分组：

### 接入层

```ini
QQ_PLATFORM=onebot
ONEBOT_TRANSPORT=forward_ws
ONEBOT_WS_URL=ws://127.0.0.1:3001
ONEBOT_ACTION_TIMEOUT=10s
ONEBOT_AUTO_ACCEPT_FRIEND=true        # 自动通过好友申请（配合 FRIEND_* 发文件）
HTTP_LISTEN_ADDR=127.0.0.1:8080
ADMIN_API_TOKEN=***
LOG_LEVEL=info
```

### 回复行为

```ini
QQ_GROUP_ALLOWLIST=1087767311,973636133,1023570334,984772612,1126402476
QQ_PRIVATE_ALLOWLIST=<管理员QQ>        # 真实号码只写生产 .env，不入库
BOT_ADMIN_USER_IDS=<管理员QQ>         # 操作员私聊
QQ_REQUIRE_MENTION=true               # 群里必须@才进管线（ai_decide 另有判定）
QQ_QUOTE_REPLY=true                   # 回复带引用
QQ_GROUP_AT_SENDER=true               # 回复@发言者
QQ_MAX_REPLY_RUNES=1500
QQ_MAX_REPLY_CHUNKS=1
QQ_GROUP_BATCH_WINDOW=900ms           # 群消息攒批窗口
QQ_REPLY_PART_DELAY=350ms             # 分段间隔（拟人节奏）
```

### 拟人化

```ini
QQ_TYPING_SIM=true                    # 回复前按字数模拟"正在输入"延迟
QQ_MOOD_ENABLED=true                  # 每聊天心情值，<= -60 自动装死30min
QQ_SLEEP_START=1                      # 睡眠窗口 01:00-08:00 不主动说话
QQ_SLEEP_END=8
QQ_PROACTIVE=true                     # 自发扫群说话总开关
QQ_PROACTIVE_IDLE=45m                 # 群安静这么久才考虑开腔
QQ_PROACTIVE_COOLDOWN=4h              # 每群两次主动说话间隔
QQ_PROACTIVE_CHANCE=0.12              # 每次扫到的触发概率
QQ_PROACTIVE_DAILY=5                  # 每群每日上限
QQ_PROACTIVE_GROUPS=973636133,1023570334,1087767311,1126402476  # 主动搭话群白名单（空=所有绑定群）
QQ_PROACTIVE_TICK=5m                  # 扫描周期（默认）
```

### LLM / 会话

```ini
PROVIDERS_FILE=data/providers.json
DEEPSEEK_API_KEY=***
GEMINI_API_KEY=***
SESSION_ENCRYPTION_KEY=***
SESSION_TTL=24h
SESSION_MAX_HISTORY=20
SESSION_LEARNING_ENABLED=true         # 会话内学习（记忆层另见 remember 工具）
BOT_ATTENTION_TIMEOUT=60s
```

### 生图

```ini
IMAGE_API_MODE=openai                 # openai 兼容端点
IMAGE_API_URL=https://api.cinlan.online/v1/images/generations
IMAGE_API_KEY=***
IMAGE_MODEL=gpt-image-2-5-sunburst
IMAGE_OUTPUT_DIR=data/generated-images
IMAGE_TIMEOUT=120s
MEDIA_TOOL_USER_LIMIT=5               # 每用户每日生图额度
MEDIA_TOOL_WINDOW=24h
MEDIA_TOOL_USER_COOLDOWN=15s
MEDIA_TOOL_MAX_CONCURRENCY=4
```

### 语音

```ini
VOICE_MODE=edge                       # edge TTS
VOICE_NAME=zh-CN-XiaoyiNeural
VOICE_DAILY_LIMIT=50
# 其余可选：VOICE_API_URL / VOICE_API_KEY / VOICE_COMMAND / VOICE_COOLDOWN / VOICE_MAX_RUNES / VOICE_DIR / VOICE_TIMEOUT
```

### 其它业务钩子

```ini
FRIEND_FILE_PATH=/opt/codepany/cinlan-qq-bot/bundle/data/zkmall.sql
WELCOME_FILE=data/welcome.json      # 好友/进群欢迎语（非空值才覆盖 env；FRIEND_WELCOME_TEXT 已清空走文件）
BASE_PROMPT_FILE=data/prompts/base-human.md   # 公共真人准则，所有人格共用
PEER_BOT_IDS=<同伴bot的QQ列表>       # 同伴 bot：只观察不回复、不计刷屏/疲劳、进群不欢迎
QQ_ERROR_SILENT=true                # 报错不往群里发，只记日志
CHAT_PREFS_FILE=data/chat-prefs.json
CRON_STORE_PATH=data/cron.json        # 默认值，schedule_message 落盘
KNOWLEDGE_DIR=data/knowledge
# 自动记忆复盘整理（默认开启：facts>20自动合并、impressions>40浓缩群友速写、活跃聊天写日记·MM.DD）
QQ_REFLECT=true
QQ_REFLECT_TICK=30m
QQ_REFLECT_FACTS_AT=20
QQ_REFLECT_IMPS_AT=40
QQ_REFLECT_JOURNAL_AGE=24h
# 双引擎语义向量检索（不配 EMBEDDING_API_URL 时自动走内置 256 维本地概念簇+n-gram 语义向量；配了则走 /v1/embeddings + 本地混合检索）
# EMBEDDING_API_URL=https://api.openai.com/v1
# EMBEDDING_API_KEY=sk-xxx
# EMBEDDING_MODEL=text-embedding-3-small
EMBEDDING_CACHE_FILE=data/vec-cache.json
IQ_CHART_INTERVAL=90m                 # 智商曲线聚合周期
QQNT_IMAGE_ALLOWED_ROOTS=/root/.config/QQ;/opt/codepany/cinlan-qq-bot/bundle/data;/opt/codepany/cinlan-qq-bot/bundle
GROUP_WELCOME_IMAGE=/opt/codepany/cinlan-qq-bot/bundle/data/cinlan-welcome.png
GROUP_WELCOME_GROUPS=1087767311,973636133,1023570334
WEB_SCREENSHOT_ENABLED=true
WEB_SCREENSHOT_BROWSER_PATH=/usr/lib64/chromium-browser/headless_shell
WEB_SCREENSHOT_ALLOWED_HOSTS=codexradar.com
WEB_SCREENSHOT_OUTPUT_DIR=data/web-screenshots
WEB_SCREENSHOT_MAX_BYTES=8388608
WEB_SCREENSHOT_WIDTH=1400
WEB_SCREENSHOT_HEIGHT=1900
WEB_SCREENSHOT_WAIT=5s
```

## systemd 服务

生产单元（`/etc/systemd/system/cinlan-qq-bot.service`）：

```ini
[Unit]
Description=Cinlan QQ Bot
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/codepany/cinlan-qq-bot/bundle
EnvironmentFile=/opt/codepany/cinlan-qq-bot/bundle/.env
ExecStart=/opt/codepany/cinlan-qq-bot/bundle/cinlan-qq-bot
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now cinlan-qq-bot
journalctl -u cinlan-qq-bot -f        # 结构化 JSON 日志
```

## 聊天绑定（`data/chat-bindings.json`）

生产绑定概览（改完需 `systemctl restart` 生效）：

| 绑定 | 范围 | 人格 | 工具白名单 | 知识库 |
|---|---|---|---|---|
| admin-private | 私聊 <管理员QQ> | 贴心女友 | 生图+定时+戳+装死+remember+channel_action | cinlan+zkmall |
| cinlan-tsundere | 群 973636133/1023570334/1087767311 | 邻家女孩 | 生图+智商曲线+channel_action | cinlan |
| zkmall-community | 群 984772612/287214287 | 哈基咪·ZK | 生图 | zkmall |
| friend-private-zk / friend-private-cinlan | 私聊 * | 哈基咪·ZK / 邻家女孩 | 生图 | - |
| linjia-group-* | 群 1126402476/480214827/1057952511 | 邻家女孩 | channel_action（禁链接/禁学习） | - |

> cinlan 与 zkmall 知识库按绑定分发、刻意隔离，不要互通。

## 命令权限速查

操作员 = 群主/管理员（admin/owner）或 `BOT_ADMIN_USER_IDS` 白名单用户。

| 命令 | 别名 | 权限 | 作用域 |
|---|---|---|---|
| /清空 | /clear | 所有人 | 当前会话 |
| /亲密度 | /affinity | 所有人 | 群聊真实分；私聊恒满级 |
| /帮助 | /help | 所有人 | 内建简版 |
| /开启通知 | 通知开/on | 操作员 | 当前聊天总唤醒：通知+装死+接管全解除 |
| /关闭通知 | 通知关/off | 操作员 | 当前聊天 |
| /记住 | 记/remember | 操作员 | 当前聊天备忘，带图存生图参考 |
| /唱歌 | 语音/voice | 操作员 | 当前聊天语音开关 |
| /切换 | 形态/persona | 所有人→固定拒绝 | 人格后台绑定不可切 |
| /指令 | 命令/菜单 | 白名单用户 | 仅私聊 |

> **说明**：群聊要求@才回（RequireMention）或 /关闭通知 后，slash 命令仍可直达（注册命令+内建集），垃圾 slash 文本照样丢弃。

## NapCat 接入（Shell 手动安装模式）

Linux 没有官方 Framework 包（`NapCat.Framework.zip` 为 Windows 专用）。推荐官方 Shell 手动安装：

1. 将 `NapCat.Shell.zip` 解压到 `/opt/QQ/resources/app/napcat`
2. 备份原始入口并写入加载器：

```bash
sudo cp -a /opt/QQ/resources/app/package.json /opt/QQ/resources/app/package.napcat-original.json
sudo tee /opt/QQ/resources/app/loadNapCat.cjs >/dev/null <<'EOF'
const path = require('node:path');
const { pathToFileURL } = require('node:url');
if (process.argv.includes('--no-sandbox')) {
    import(pathToFileURL(path.join(__dirname, 'napcat/napcat.mjs')).href);
} else {
    const original = require('./package.napcat-original.json');
    require(path.resolve(__dirname, original.main));
    setTimeout(() => {
        global.launcher.installPathPkgJson.main = original.main;
    }, 0);
}
EOF
```

3. 修改 `package.json` 的 `main` 为 `./loadNapCat.cjs`
4. 启动（快速登录加 `-q <QQ号>`）。生产用 detached screen + xvfb 常驻：

```bash
screen -dmS napcat xvfb-run -a /opt/QQ/qq --no-sandbox -q 123456789
```

> **必须用 xvfb 虚拟显示**：QQNT 需要桌面环境，`xvfb-run` 给它一个假 X display。
> 纯无头（无 X display）直接跑更容易被风控封号，不要用 `--headless` 之类
> 的无显示启动方式替代。

### OneBot 配置

`config/onebot11_<QQ号>.json` 中开启 WebSocket 服务端（生产在跑）：

```json
{
  "network": {
    "websocketServers": [{
      "name": "cinlan-bot",
      "host": "127.0.0.1",
      "port": 3001,
      "messagePostFormat": "array",
      "enable": true,
      "enableHeartBeat": true,
      "heartBeatInterval": 15000
    }]
  }
}
```

bot 侧对应 `ONEBOT_WS_URL=ws://127.0.0.1:3001`。

### 反检测配置

`config/napcat.json` 保持默认反检测即可：

```json
{
  "o3HookMode": 1,
  "autoTimeSync": true,
  "bypass": { "hook": true, "window": true, "module": true, "process": true, "container": true, "js": true }
}
```

注意：Linux 上 `bypass.module` 在部分版本会导致启动段错误，如遇启动失败先关闭该项。

## 登录

- **快速登录**：`-q <QQ号>` 使用本地缓存会话，无需扫码
- **扫码登录**：快速登录失效时 NapCat 会生成二维码，路径通常在 `/opt/QQ/resources/app/cache/qrcode.png`，扫码有效期约 2 分钟
- **WebUI**：`http://127.0.0.1:6099/webui`（token 见启动日志或 `config/webui.json`），仅监听本地，需 SSH 隧道访问

## 风控注意事项

- **假显示启动**：一律 `xvfb-run` 提供虚拟桌面；无显示/纯无头启动更容易被风控封号
- **固定 IP**：不要频繁切换服务器出口 IP，数据中心 IP 本身风险较高
- **不要频繁重启**：每次重启都是一次新的登录记录；稳定挂机数天可降低风控评分
- **不要多端同时登录**：同一账号两个 PC 端（Windows QQ + Linux NapCat）会互踢
- **版本选择**：官方文档推荐 QQ 3.2.34-53644；社区反馈旧版（3.2.23）可能被禁止登录，降级前先验证
- **养号**：账号被标记时，先在常用设备正常使用数天，再回服务器登录
- **时间同步**：开启 NTP，时间不准会导致协议验签失败
- **SSH 部署**：高频连接触发云商风控丢包，部署失败后等 30-60 秒

## 排错

| 现象 | 排查 |
| --- | --- |
| `dial tcp 127.0.0.1:3001: connect: connection refused` | NapCat 未登录或未启动；`ss -tlnp \| grep 3001` 确认端口 |
| 二维码过期 | 重新拉取 `cache/qrcode.png`，或通过 WebUI 刷新 |
| 启动即段错误 | 关闭 `bypass.module` 后重试 |
| `GLIBC_2.34 not found`（使用第三方 launcher） | 服务器 glibc 过旧，改用官方 Shell 手动安装模式 |
| 频繁 `KickedOffline` | 风控；检查 IP 稳定性、版本、是否多端登录 |
| 生图 HTTP 400 | 日志里 `returned HTTP 400: <body>`——body 直接写明上游审核/参数原因 |
| 主动说话乱入未授权群 | 检查 `QQ_PROACTIVE_GROUPS` 群号是否圈对 |
| 定时任务重启后消失 | 查 `data/cron.json` 是否生成，日志搜 persist 报错 |

## 升级与回滚

升级二进制（生产实际流程）：

```bash
scp -i KEY cinlan-qq-bot-linux root@SERVER:/opt/codepany/cinlan-qq-bot/bundle/cinlan-qq-bot.new
ssh -i KEY root@SERVER 'cd /opt/codepany/cinlan-qq-bot/bundle && cp cinlan-qq-bot cinlan-qq-bot.bak && chmod +x cinlan-qq-bot.new && mv cinlan-qq-bot.new cinlan-qq-bot && systemctl restart cinlan-qq-bot'
```

回滚：把 `.bak` 覆盖回去并重启服务。NapCat 侧回滚：恢复 `package.napcat-original.json` 为 `package.json` 的 `main` 即可。