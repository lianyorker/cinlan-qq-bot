# Windows PE Analysis: QQNT Native Runtime

## 1. Basic Info

- **Observed**: `2026-07-26T16:47:01.0140238+08:00`
- **Redirect probe**: `2026-07-27T00:55:13.8699885+08:00`
- **Installation**: `D:\SoftWare\Tencent\QQNT`
- **QQ Version**: `9.9.31-49738`
- **Architecture**: x64
- **Inventory**: [addon-inventory.json](../exports/qqnt/addon-inventory.json)
- **wrapper.node SHA256**: `A1E59891E743C271D641EE011F47AA887D9F7DFAE6B3BB9292A03AF759DEC203`
- **major.node SHA256**: `89BE0BA17B9A93EA825F24200BF05AA25E4569B6262CBBC7D642F40A11251C24`
- **QQNT.dll SHA256**: `8EC0A4088F4ED2B4C37805F03757E1AAC47AA96C55779CD7136D9EFF475B1805`
- **cinlan-qq-hook.dll SHA256**: `3F16E1BEC5BF200DE3864F6B44DB0A0B03AA36A8B3D773AA7EA3E655E92BE407`
- **Analysis tools**: `pefile 2024.8.26`、`capstone 5.0.7`

## 2. PE Structure

本轮未做完整 section map。目标是确认最小启动期 hook 面，不修改或 patch 官方 PE 文件。

## 3. Imports

| Module | DLL | Relevant API | Purpose |
|---|---|---|---|
| `QQ.exe` | `KERNEL32.dll` | `CreateFileW`、`GetFileAttributesW`、`GetProcAddress` | app entry 读取与动态 API 解析 |
| `QQNT.dll` | `KERNEL32.dll` | `CreateFileW`、`GetFileAttributesExW`、`GetFileAttributesW`、`GetFileInformationByHandleEx`、`GetProcAddress` | 文件读取和 stat 路径 |
| NapCat reference hook | `KERNEL32.dll` | `CreateFileW`、`GetFileInformationByHandleEx`、`GetModuleHandleA`、`GetProcAddress` | clean-room 行为对照，不进入构建 |

导入由 `pefile 2024.8.26` 结构化解析确认。字符串扫描也命中相同 API，但不单独作为导入证据。

## 4. Exports

Cinlan hook 构建产物导出 `DllMain`。官方 addon exports 通过 QQNT 内置 Node runtime 动态观察，不在系统 Node 中加载。

## 5. Static Analysis

官方 `resources/app/package.json`：

```json
{
  "main": "./application.asar/app_launcher/index.js",
  "version": "9.9.31-49738",
  "buildVersion": "49738",
  "eleArch": "x64"
}
```

`wrapper.node` 依赖 QQNT 自带的 Node/V8 环境，系统 Node 不能作为其宿主。当前 QQNT 对 `ELECTRON_RUN_AS_NODE` 探针不生效，因此采用 official main process attach。

Cinlan 只重写以下读取：

```text
\resources\app\package.json
\resources\app\loadCinlan.js
\resources\app\application.asar\loadCinlan.js
```

patched package 仅把 `main` 改为 `./loadCinlan.js`。自有入口加载官方 launcher 后，再启动 `runtime.cjs`。

参考 hook（SHA256
`962BD5CAF59E9792C37EED99C9130BF1464D619D0209820BE570874017D66925`）
的 x64 反汇编表明：

- DllMain patch `QQ.exe` 的 `GetProcAddress` IAT。
- 查询 `ExportedContentMain` 时匹配
  `E8 ???? E8 ???? 84 C0 0F 85 ???? 48 8D 0D ????`，将分支改为
  `0F 84`，然后 patch QQNT `CreateFileW` IAT。
- `GetFileInformationByName` 兼容路径会打开目标文件，调用
  `GetFileInformationByHandleEx` 后关闭句柄。

Cinlan 按该可观察行为独立实现，没有安装全局 `CreateFileW` 或 attributes
inline hook。当前 QQNT 首个签名命中 RVA `0x4D6ADD`。

## 6. Dynamic Analysis

已验证：

- suspended launch、remote `LoadLibraryW` 和 resume 的自有 Rust 构建通过。
- QQNT IPC token 握手、版本检查、帧上限、事件转换和 action correlation 通过 Go 测试。
- `cargo test` 通过 3 个 hook 路径/签名测试，`cargo clippy -D warnings` 通过。
- release hook SHA256 为
  `3F16E1BEC5BF200DE3864F6B44DB0A0B03AA36A8B3D773AA7EA3E655E92BE407`。
- 真实 QQNT 连续两次完全冷启动，分别稳定运行至少 200 秒和 201 秒。
- 两次 `/readyz` 均持续返回 HTTP 200，wrapper/session attach 成功。
- 普通方式启动官方 QQ 也正常，官方文件 hash 未变化。

未执行：

- 尚未完成真实群消息或私聊消息的 `event -> Agent -> sendMsg` live test。
- 客服 pipeline 当前只处理群消息；private event 尚未接入 session 和回复链。

## 7. Runtime Contract

```text
Official QQNT
  -> Cinlan loader/hook
  -> official launcher + Cinlan runtime.cjs
  <-> authenticated loopback NDJSON
  -> Cinlan Go Agent runtime
```

ready 条件：

```text
state == ready
self_id != empty
wrapper_loaded == true
session_attached == true
```

runtime 启动前的历史同步消息必须丢弃。attach 错误通过 `runtime_status.last_error` 回传，不能用 fallback 把 `/readyz` 伪装为成功。

## 8. Reproduction

```powershell
Get-FileHash <path> -Algorithm SHA256
python -c "import pefile; print(pefile.__version__)"
go test ./...
go vet ./...
node --check runtime/qqnt/load-cinlan.cjs
node --check runtime/qqnt/runtime.cjs
cargo test --manifest-path runtime/loader/Cargo.toml
cargo clippy --manifest-path runtime/loader/Cargo.toml --all-targets -- -D warnings
.\scripts\build.ps1
```

## 9. Conclusion

默认主链已不依赖 NapCatQQ/AstrBot。Cinlan 自有代码负责启动、定向 hook、QQNT
会话附加、IPC 和 Agent pipeline；官方 QQNT 仍负责账号登录与 QQ 协议。版本
`9.9.31-49738` 的静态接口、构建、真实 QQNT 启动和 wrapper/session ready
已验证；真实群消息收发及私聊 pipeline 仍未验证/完成。

## 10. Headless Implementation Audit (2026-10-09)

本轮仅使用官方 QQNT PE/binding 的只读检查和自有源码，不读取、复制或反编译
NapCat/LLOneBot。用户提供的 `docs/原生无头化方案.md` 保持未修改。

版本核实纠正：`9.9.36-53644` 已在 versions 中落盘，但官方
`versions/config.json.curVersion` 和现有 QQ renderer app-path 都为
`9.9.31-49738`。discovery 现在优先激活版本；不修改官方 config.json。

官方接口证据（可运行 `scripts/inspect-qqnt-bindings.cjs` 复核）：

| Binding / converter | 9.9.31 RVA | 9.9.36 RVA | 观察 |
|---|---|---|---|
| approvalFriendRequest | `0xEBCED8` | `0x1072D44` | argc == 1；参数转换字段 friendUid/accept/refuseMsg/reqTime |
| approval 参数转换 | `0xEBD160` | `0x1072FDA` | 精确对象字段，非第三方示例猜测 |
| onBuddyReqChange | `0x202BAD1` | `0x24D2885` | 官方 NodeIKernelBuddyListener 回调 |
| onQRCodeGetPicture | `0x1E5AD5B` | `0x22869A5` | 官方 login listener 回调 |
| QR 数据转换 | `0x1E5AEBC` | `0x2286B18` | pngBase64QrcodeData/qrcodeUrl/expireTime/pollTimeInterval |

USER32 CreateWindowExW/ShowWindow/SetWindowPos/SetWindowPlacement 位于 QQNT.dll
delay-import table，而非普通 import table。hook 对这四条显示路径及动态解析定向；
默认关闭；失败阻止 ContentMain 启动，loader 15 秒内等待 hook 状态文件并 fail-closed。

实现与自动化验证：friend_request 独立 IPC、随机 flag 缓存、好友审批/remark、
有界 Go 自动同意 worker；login_qr IPC、Bearer QR/status API、刷新、PNG 校验、
ready/断连清空；登录等待 onLoginConnected，headless 不信任未确认的缓存账号身份。

验证结果：`go test ./...`、`go vet ./...`、Node tests（28 项）、`node --check`、Rust tests
（lib 5 项 + loader 1 项）、`cargo clippy --all-targets -- -D warnings` 通过；release 构建通过。`go test -race` 未运行成功，
当前 CGO_ENABLED=0，缺少可用 C compiler，错误为 `-race requires cgo`。

官方当前激活版本文件本轮 SHA256：

自有新 release hook SHA256：
`BDD8BDE73E6115691F604063CE6A5F55DE80EF36803277A35987792AADC04106`。
这是构建产物 hash，未经过本轮真实冷启动。

- package.json: `56387B6EC1B4D129D3790C09FB5C5949A6E92F01CAE162D7DEF4A49D9ED402E3`
- wrapper.node: `A1E59891E743C271D641EE011F47AA887D9F7DFAE6B3BB9292A03AF759DEC203`
- QQNT.dll: `8EC0A4088F4ED2B4C37805F03757E1AAC47AA96C55779CD7136D9EFF475B1805`

`scripts/verify-qqnt-files.ps1` baseline + Compare 通过，仅证明本轮只读检查和构建
没有改写官方文件，不等于经过新 hook 冷启动后的 hash 验收。

实机测试状态：用户授权关闭/restart QQ 后，执行平台拒绝冷启动命令，整个命令
没有执行；原有 QQ 进程未被关闭，不换方式绕过。**以下均未验证**：新 hook 冷启动
耗时、所有子进程窗口抑制、headless=false UI 回归、真实扫码/过期刷新/quick login、
真实好友申请/审批、双方好友关系、deliver_file、event -> Agent -> sendMsg、
Windows 无交互桌面/Session 0。验收不能标记完成。人工复测按 docs/原生运行时.md。
