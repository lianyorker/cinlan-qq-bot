# Linux 部署手册

本文档描述 `cinlan-qq-bot` 在 Linux 服务器上的完整部署流程：构建、目录布局、systemd、NapCat 接入、登录与风控注意事项。所有示例使用占位符，真实 IP、密钥、账号与人格内容一律不入库。

## 前置条件

- Linux x86_64（glibc ≥ 2.28），建议 2C4G 以上
- Go 1.22+（本机构建或交叉编译）
- 官方 QQNT Linux 客户端（最低构建号 40768，推荐 3.2.34-53644）
- Xvfb、xauth、jq（无桌面环境必需）

```bash
# Debian / Ubuntu
sudo apt install -y xvfb xauth jq
# Fedora / Alibaba Cloud Linux
sudo dnf install -y xorg-x11-server-Xvfb xorg-x11-xauth jq
```

## 构建

Windows 交叉编译：

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'
go build -o cinlan-qq-bot-linux ./cmd/cinlan-qq-bot
Remove-Item Env:GOOS; Remove-Item Env:GOARCH
```

Linux 本机构建：

```bash
go build -o cinlan-qq-bot ./cmd/cinlan-qq-bot
```

## 目录布局

```text
/opt/cinlan-qq-bot/
├─ bundle/
│  ├─ cinlan-qq-bot          # 二进制
│  ├─ .env                   # 环境变量（不入库）
│  └─ data/                  # 运行时数据（不入库）
│     ├─ chat-bindings.json
│     ├─ personas.json
│     ├─ chat-prefs.json
│     └─ generated-images/
└─ ...
```

`data/` 整个目录被 Git 忽略，可存放部署专属的人格、绑定、会话库与生成媒体。

## systemd 服务

参考 `deploy/cinlan-qq-bot.service`，按实际路径调整：

```ini
[Unit]
Description=Cinlan QQ Bot
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/cinlan-qq-bot/bundle
EnvironmentFile=/opt/cinlan-qq-bot/bundle/.env
ExecStart=/opt/cinlan-qq-bot/bundle/cinlan-qq-bot
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now cinlan-qq-bot
journalctl -u cinlan-qq-bot -f
```

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
4. 启动（快速登录加 `-q <QQ号>`）：

```bash
xvfb-run -a /opt/QQ/qq --no-sandbox -q 123456789
```

### OneBot 配置

`config/onebot11_<QQ号>.json` 中开启 WebSocket 服务端：

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
- **扫码登录**：快速登录失效时 NapCat 会生成二维码，路径通常在
  `/opt/QQ/resources/app/cache/qrcode.png`，扫码有效期约 2 分钟
- **WebUI**：`http://127.0.0.1:6099/webui`（token 见启动日志或 `config/webui.json`），仅监听本地，需 SSH 隧道访问

## 风控注意事项

- **固定 IP**：不要频繁切换服务器出口 IP，数据中心 IP 本身风险较高
- **不要频繁重启**：每次重启都是一次新的登录记录；稳定挂机数天可降低风控评分
- **不要多端同时登录**：同一账号两个 PC 端（Windows QQ + Linux NapCat）会互踢
- **版本选择**：官方文档推荐 QQ 3.2.34-53644；社区反馈旧版（3.2.23）可能被禁止登录，降级前先验证
- **养号**：账号被标记时，先在常用设备正常使用数天，再回服务器登录
- **时间同步**：开启 NTP，时间不准会导致协议验签失败

## 排错

| 现象 | 排查 |
| --- | --- |
| `dial tcp 127.0.0.1:3001: connect: connection refused` | NapCat 未登录或未启动；`ss -tlnp \| grep 3001` 确认端口 |
| 二维码过期 | 重新拉取 `cache/qrcode.png`，或通过 WebUI 刷新 |
| 启动即段错误 | 关闭 `bypass.module` 后重试 |
| `GLIBC_2.34 not found`（使用第三方 launcher） | 服务器 glibc 过旧，改用官方 Shell 手动安装模式 |
| 频繁 `KickedOffline` | 风控；检查 IP 稳定性、版本、是否多端登录 |

## 升级与回滚

升级二进制：

```bash
scp cinlan-qq-bot-linux root@SERVER:/opt/cinlan-qq-bot/bundle/cinlan-qq-bot.new
ssh root@SERVER 'cd /opt/cinlan-qq-bot/bundle && cp cinlan-qq-bot cinlan-qq-bot.bak && mv cinlan-qq-bot.new cinlan-qq-bot && chmod +x cinlan-qq-bot && systemctl restart cinlan-qq-bot'
```

回滚：把 `.bak` 覆盖回去并重启服务。NapCat 侧回滚：恢复 `package.napcat-original.json` 为 `package.json` 的 `main` 即可。
