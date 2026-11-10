# ShieldDNS

<img src="assets/shield_on.png" width="96" align="right" alt="ShieldDNS 图标" />

Windows 本地 DNS 广告拦截应用。**Rust 写 DNS 防护核心**，**Go 写桌面面板**（WebView2 窗口 + 系统托盘）。

图标为「绿色盾牌 + 白色闪电」：保护开启时显示闪电版，关闭保护时符号切换为白色暂停条。

- 平台：Windows 10 / 11（x64）
- 形态：绿色免安装单目录（`ShieldDNS.exe` + `core\shielddns-core.exe`），无服务、无驱动、不写入系统目录
- 许可：[MIT](LICENSE)
- 当前版本：`0.1.0`（首个公开版本）

典型用法：双击运行 → 托盘出现绿盾 → 打开面板点「开启防护」，核心监听 `127.0.0.1:53`，本机所有域名查询先过过滤引擎；装一次后台助手后，切 53 端口与接管系统 DNS 都不再弹 UAC。

---

## 1. 功能概览

| 能力 | 说明 |
| --- | --- |
| DNS 代理 | UDP + TCP 双协议监听，UDP 应答被截断（TC 位）时自动走 TCP 重查 |
| 过滤引擎 | 纯内存匹配：精确表 + 后缀表 + 正则；支持 AdGuard 语法与 hosts 格式 |
| 拦截应答 | `nxdomain`（默认，直接 NXDOMAIN）或 `zero`（应答 0.0.0.0 / ::） |
| 上游可跟随本机 | 可切成「跟随本机 DNS」：开启防护前先读取各网卡 DNS 作为上游，接管的瞬间自动取接管前备份值 |
| 异常还原保护 | 可选注册计划任务：异常退出/重启后若 127.0.0.1 上没有 DNS 应答，自动还原网卡 DNS |
| 免 UAC 接管 | 可选安装「后台助手」（SYSTEM 计划任务常驻 + 命名管道）：之后普通权限也能切 53 端口并接管/还原系统 DNS，不再每次弹 UAC |
| 启动行为 | 应用启动时不自动开启防护、也不主动接管；若上次是接管状态会先把网卡 DNS 还原成原值 |
| 默认端口 | 新装默认监听 53（系统解析器只认 53）；53 被占用时启动失败并提示占用进程，不做静默换端口 |
| 缓存 | 按 `name|qtype` 缓存上游应答，TTL 夹取 10–3600 秒，可设容量上限并定期清理 |
| 订阅过滤器 | 通过 URL 添加自定义过滤器，可单独开关、立即更新、删除 |
| 规则自动更新 | 后台每 60 秒心跳，按每个过滤器的 `update_interval_minutes`（默认 720 分钟）判断到期 |
| 自定义规则 | 面板内直接编辑规则文本（一行一条），保存即重建引擎并清空缓存 |
| 查询日志 | 记录域名 / 类型 / 处置 / 命中规则 / 客户端 / 耗时，支持搜索与筛选 |
| 统计 | 查询总数、拦截数、拦截率、缓存命中、拦截 Top 域名、运行时长 |
| 保护开关 | 一键暂停/恢复，托盘图标同步切换闪电 ↔ 暂停 |
| DNS 接管 | 监听 53 时开启防护即自动把各网卡 DNS 指向 127.0.0.1，关防护/退出自动还原；非 53 端口不接管 |
| 托盘常驻 | 关闭窗口只收进托盘，程序继续守护核心 |
| 版本与更新 | 右上角与设置页显示当前版本；「检查更新」查询 GitHub Releases 的最新 tag，有新版时可点「立即更新」由程序自己下载并替换文件后重启，也可点「发布页」手动下载 |

**发行版不内置任何第三方规则订阅**：程序只提供引擎与订阅管理（URL 添加、定时更新、条数统计），用哪份规则由使用者自己决定。首次安装后规则数为 0，到「过滤订阅 → 添加订阅」填一个自己信任的 AdGuard 语法规则源即可。

---

## 2. 目录结构

```
ShieldDNS/
├─ build.ps1                  一键构建（cargo + windres + go build + 组装 dist）
├─ core/                      Rust DNS 防护核心（crate: shielddns-core）
│  ├─ Cargo.toml              发布档：opt-level="z" + lto + strip + panic=abort
│  └─ src/
│     ├─ main.rs              启动编排（--config / --version）
│     ├─ config.rs            配置读写（默认端口 53 / 控制口 5399 / 面板口 8899）
│     ├─ engine.rs            AdGuard 语法过滤引擎
│     ├─ state.rs             统计、日志、缓存、时间格式化
│     ├─ dns.rs               UDP/TCP 服务、拦截应答、上游转发、缓存
│     ├─ updater.rs           订阅下载、自动更新循环
│     └─ api.rs               控制 HTTP API（仅绑 127.0.0.1）
├─ panel/                     Go 桌面面板（module: shielddns-panel）
│  ├─ main.go                 内嵌 UI、单实例、默认 53 + 失败回退、启动行为、托盘/窗口编排
│  ├─ config.go               面板侧配置（与 core 同构）
│  ├─ core.go                 核心进程守护（启动、健康检查 pid 校验、日志落盘、重启）
│  ├─ server.go               HTTP 面板：/system/* 自有路由 + /api/* 反代到核心
│  ├─ ipc_windows.go          后台助手：命名管道服务端/客户端、免 UAC 接管与还原
│  ├─ task_windows.go         计划任务（看门狗 / 后台助手）的 XML、注册、查询、注销
│  ├─ watchdog.go             看门狗逻辑：探测真实应答 → 必要时还原 → 顺手重启助手
│  ├─ tray_windows.go         纯 syscall 托盘（NOTIFYICONDATAW + 弹出菜单）
│  ├─ window_windows.go       WebView2 窗口 + WndProc 子类化（关窗即收托盘）
│  ├─ dns_windows.go          管理员检测、PowerShell 执行、网卡 DNS 接管/还原、上游抓取
│  ├─ app.rc / app.manifest   图标资源与 PerMonitorV2 DPI 清单 → 编译成 app.syso
│  ├─ vendor/                 Go 依赖（离线可编译）
│  ├─ web/                    内嵌单页 UI（index.html / style.css / app.js / assets）
│  └─ dist/                   面板产物（ShieldDNS.exe）
├─ assets/                    图标母版与生成脚本
│  ├─ shield_on_raw.png       闪电版原始图（透明底 1536×1536）
│  ├─ shield_off_raw.png      暂停版原始图（黑底 2048×2048，脚本会去背）
│  ├─ make_icons.py           Pillow 生成透明底 PNG + 多尺寸 ICO
│  └─ shield_on.png / shield_off.png / shield_on.ico / shield_off.ico / app.ico / logo.png
├─ scripts/
│  └─ verify_core.py          DNS 端到端自检（拦截 / 放行 / 保护开关）
└─ dist/                      组装后的可运行产物
   ├─ ShieldDNS.exe
   └─ core/shielddns-core.exe
```

运行时数据（不在仓库里）默认落在 `%LOCALAPPDATA%\ShieldDNS\`：

```
config.json          配置（核心与面板共用）
logs/core.log        核心 stdout/stderr（面板启动核心时重定向到这里）
logs/watchdog.log    看门狗每次运行的结论（跳过原因或还原结果）
logs/helper.log      后台助手每次收到动作的结果
dns_backup.json      接管系统 DNS 前各网卡原 DNS 的备份
helper_user.sid      安装助手时记录的本机用户 SID（管道 ACL 只放行它）
filters\<id>.txt     各订阅过滤器的本地副本（规则原文）
webview2\            WebView2 用户数据目录
```

需要换位置时设置环境变量 `SHIELDDNS_DATA_DIR`。

---

## 3. 架构

```
┌──────────────────────────────┐        HTTP 127.0.0.1:8899        ┌────────────────────────┐
│  浏览器 / 内嵌 WebView2 UI    │ ────────────────────────────────▶ │  Go 面板 (ShieldDNS.exe) │
└──────────────────────────────┘                                   │  · 静态页 (embed.FS)     │
                                                                   │  · /system/* 系统操作     │
       其他设备/本机应用                                            │  · /api/* 反代到核心      │
             │  DNS 查询 (UDP/TCP)                                 └──────────┬─────────────┘
             ▼                                                                │ 子进程守护
┌──────────────────────────────┐      HTTP 127.0.0.1:5399                     │
│ Rust 核心 (shielddns-core.exe)│ ◀───────────────────────────────────────────┘
│  · UDP/TCP 监听 127.0.0.1:53（唯一能被系统解析命中的端口）
│  · 过滤引擎（内存） → 拦截则应答 NXDOMAIN/0.0.0.0
│  · 放行则转发上游（223.5.5.5 / 119.29.29.29 / 1.1.1.1，可改）
│  · 缓存 + 统计 + 查询日志 + 订阅自动更新
└──────────────────────────────┘
```

面板只在需要时读写核心：UI 的所有 `/api/*` 请求都由面板反向代理到 `127.0.0.1:<control_port>`，因此核心控制口不对外暴露，也不会有跨域问题。

---

## 4. 环境依赖

**终端用户**（只运行，不编译）：

| 项目 | 要求 |
| --- | --- |
| 系统 | Windows 10 1809+ / Windows 11，x64 |
| WebView2 Runtime | Win11 自带；Win10 若缺失，装一次 [Evergreen Runtime](https://developer.microsoft.com/microsoft-edge/webview2/) |
| 权限 | 普通用户即可运行；只有「切 53 端口并接管系统 DNS」与「注册计划任务」需要管理员（或装一次后台助手免弹 UAC） |
| 网络 | 首次启动拉取默认订阅需要联网（约 1 MB） |

**开发者**（编译源码）：

| 组件 | 版本要求 | 本机实测 |
| --- | --- | --- |
| Rust | stable，target `x86_64-pc-windows-gnu` | rustc/cargo 1.99.1 |
| MinGW-w64 | 需含 `gcc.exe` 与 `windres.exe`（WinLibs 即可） | gcc 16.2.0 |
| Go | 1.21+（本项目用 1.27.3 编译） | go1.27.3 windows/amd64 |
| WebView2 Runtime | Windows 10/11 一般已自带 | 157.0.4341.42 |

Rust 工具链（GNU ABI，配 MinGW 链接器）：

```powershell
winget install BrechtSanders.WinLibs.POSIX.UCRT
winget install Rustlang.Rustup
rustup default stable-x86_64-pc-windows-gnu
```

`build.ps1` 会自动找 `gcc/windres`（先看 `$env:SHIELDDNS_MINGW`，再扫 WinGet 的 WinLibs 目录，兜底 `C:\mingw64\bin` 等）和 `go.exe`（先看 `%USERPROFILE%\sdk\go\bin`）。找不到时按提示装好或设环境变量。

Go 依赖已 `vendor`（`panel/vendor/`），无网也能编译。

---

## 5. 构建

```powershell
# 全量：Rust 核心（release）+ 图标资源 + Go 面板 + 组装 dist
powershell -ExecutionPolicy Bypass -File .\build.ps1

# 只重建面板（核心已编译过）
.\build.ps1 -SkipCore

# 编 debug 核心（排查问题用）
.\build.ps1 -DebugCore
```

产物：

```
dist\ShieldDNS.exe               面板（约 10 MB，-H windowsgui -s -w）
dist\core\shielddns-core.exe     核心（约 3.3 MB，release）
```

手动构建等价命令：

```powershell
$env:Path = "$env:USERPROFILE\.cargo\bin;<mingw64\bin>;$env:USERPROFILE\sdk\go\bin;" + $env:Path

cd core;  cargo build --release --bin shielddns-core; cd ..
cd panel; & <mingw64\bin>\windres.exe -I . -i app.rc -O coff -o app.syso
          go build -trimpath -ldflags "-H windowsgui -s -w" -o ..\dist\ShieldDNS.exe .
```

改了 `app.rc` 或 `panel\assets\*.ico` 之后必须重新生成 `app.syso`，否则图标还是旧资源。

### vendor 里的两处手改（会被 `go mod vendor` 覆盖）

WebView2 的两个行为在原库上做不到，因此直接改了 `panel\vendor\` 下的文件。重新 vendor 之后必须手动补回，否则启动会退化成"先露白窗口 / 首帧不画"：

| 文件 | 改动 | 作用 |
| --- | --- | --- |
| `vendor\github.com\jchv\go-webview2\webview.go` | `WindowOptions` 增加 `Hidden bool`；为 true 时跳过 `ShowWindow` / `UpdateWindow` / `SetFocus` | 建窗时不显示，避免先露出一块白底客户区 |
| `vendor\github.com\jchv\go-webview2\pkg\edge\chromium.go` | `CreateCoreWebView2ControllerCompleted` 里 `PutIsVisible(true)` + `PutDefaultBackgroundColor(11,15,13)` | 隐藏建出来的控件不会自己变可见、默认背景是白色，不补这两句页面一直不画 |

记忆点：**跳过 ShowWindow 只是"看不见"，不等于"会画"**。控件必须显式置为可见、默认背景改成面板底色，页面才会在隐藏窗口里正常渲染。

---

## 6. 运行

### 快速开始（下载版）

1. 从 [Releases](../../releases) 下载 `ShieldDNS-<version>-win-x64.zip`（或 clone 仓库后自行 `build.ps1`）。
2. 解压到**同一个目录**，确认目录里有 `ShieldDNS.exe` 与 `core\shielddns-core.exe`（两者相对位置不能变）。
3. 双击 `ShieldDNS.exe`。托盘出现绿盾，面板窗口打开；右上角与设置页显示当前版本号，点「检查更新」会去 GitHub 查最新 Release。
4. 程序**不内置任何规则**，先到「过滤订阅 → 添加订阅」填一个自己信任的 AdGuard 语法规则源（首次更新完成后规则数才会变成非 0）。
5. 面板首页打开「开启防护」。此时核心已在 `127.0.0.1:53` 应答，本机解析立刻走过滤引擎；端口是 53 时开启防护会自动把网卡 DNS 指向 `127.0.0.1`（普通权限会先用后台助手，没有助手就提示装一次，之后长期免弹 UAC）。
6. 卸载：托盘「退出」→ 删掉整个目录。若之前注册过计划任务/开机自启，先在设置页点「卸载助手」「关闭开机自启」，或手动删除 `ShieldDNS-DNS-Helper` 计划任务与 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` 下的 `ShieldDNS` 值；用户数据在 `%LOCALAPPDATA%\ShieldDNS\`，可直接删。

### 启动流程

直接双击 `dist\ShieldDNS.exe`（保持 `dist\core\shielddns-core.exe` 的相对位置）。行为：

1. 单实例互斥（`Local\ShieldDNS-Panel-Mutex`），已在运行时提示从托盘打开。
2. 启动即把 `protection_enabled` 置为 `false` 并落盘 —— **不默认开启防护**，也不主动接管系统 DNS。
3. 若检测到上次是接管状态（`dns_backup.json` 存在），先把各网卡 DNS 还原成原值，再继续启动。
4. 面板 HTTP 从 `panel_port`（默认 8899）起连续试 15 个端口，占用则顺延。
5. 拉起核心（带 `--config`），最多轮询 40×250 ms 等 `/api/health` 就绪；若 53 被别的程序占住，核心直接以 `exit(2)` 退出，面板改为在顶部提示条里给出占用者信息 + 一键结束进程重试（不会自动换端口）。
6. 清理上一轮残留的可执行镜像（`ShieldDNS.exe~`）。
7. 弹出 WebView2 窗口（1220×820，居中，标题「ShieldDNS · DNS 广告拦截」）。
8. 托盘图标常驻：绿盾=保护开启，暂停版=保护关闭。

托盘菜单：

* **打开面板**
* **暂停保护 / 开启保护**（与主界面的开关、图标三方同步）
* **退出**

窗口右上角关闭按钮不会退出程序，只是 `SW_HIDE` 收进托盘；真正退出用托盘菜单或面板右上角「退出应用」。

> 从命令行以 `-WindowStyle Hidden` 之类方式启动时，Windows 会用启动参数覆盖第一次 `ShowWindow`，窗口可能不显示也不绘制页面。正常双击/普通启动不受影响。

### 版本号与检查更新

* 版本号只有一个来源：`panel/version.go` 里的 `appVersion`（当前 `0.1.0`）。界面右上角 chip、设置页「版本」行、`/system/info` 都读它；核心侧 `Cargo.toml` 的版本同步即可。
* 「检查更新」走 `GET /system/update`：请求 `https://api.github.com/repos/sayaxia/ShieldDNS/releases/latest`，比对 tag（如 `v0.1.2`）与本地版本，按数字分段比较（`0.1.1` vs `0.1.2`、`0.10.0` 都能正确排序）。
* 结果里带 `releaseURL`、`downloadURL`（优先 `*win-x64*.zip` 资产）与资产体积；有新版时界面放出「立即更新」和「发布页」两个按钮。
* **应用内自动更新**：`POST /system/update/apply` 让面板自己下载 Release 压缩包、解压校验后替换 `ShieldDNS.exe` 与 `core\shielddns-core.exe`，然后拉起新实例并退出当前进程，用户不用去浏览器下载解压。
  * 替换用的是「旧文件改名 → 写新文件 → 失败回滚」：Windows 不允许覆盖正在运行的 exe，但允许给它改名（改名后的旧镜像留在 `.old` 里，下次启动时删）。
  * 更新前先停核心，避免核心 exe 被占用；更新后程序按既有逻辑重启，防护回到待命状态，需要手动再开。
  * 进度通过 `GET /system/update/status`（`{running,stage,percent,message,version,error,done}`）给界面轮询；下载先写 `.part`，长度对不上就当失败重下，不会留下半截包。
  * 解压做了 zip-slip 过滤，并且要求包里确实有体积合理的 `ShieldDNS.exe` 和 `core\shielddns-core.exe`，否则不动现有文件。
  * 装在没有写入权限的目录（如 `Program Files`）时会失败并提示先「以管理员身份重启」；「发布页」按钮仍保留，用作手动兜底。
* 结果缓存 10 分钟（`?force=1` 跳过），界面启动时静默查一次，只把结论写进设置页，不弹 toast。

---

## 7. 端口、接管与上游

默认监听 **`127.0.0.1:53`**，并且**不做任何静默换端口**。

原因很简单：Windows 网卡 DNS 只能填 IP、带不了端口，系统解析器和浏览器默认都打 53。监听 25353 时，只有 `nslookup -port=25353` 这类显式指定端口的客户端才会命中，日常上网完全绕过；而 `netsh portproxy` 做转发本身又得先占住 53，绕回同一个问题。所以「默认 53、绑不上就明确失败」才是唯一诚实的做法。

### 53 被占用时

（IIS、Docker、AdGuard Home、其它 DNS 工具等）核心会直接启动失败，`core.log` 里留下：

```
[core] DNS 服务启动失败: 127.0.0.1:53 被占用：通常每个套接字地址(协议/网络地址/端口)只允许使用一次。 (os error 10048)
```

面板顶部随即弹出黄色提示条，写明**占用者进程名 + PID + 完整路径**，并提供两颗按钮：

* **结束占用进程并重试**：`port_windows.go` 用 `TerminateProcess` 结束它，然后 `core.Stop() + Start()` 重启核心；成功后提示条自动收起。`svchost / system / services / lsass / wininit / winlogon / smss / dwm` 这类关键进程会被拒绝，提示你手动停对应服务或换端口。
* **稍后处理**：只关掉这条提示，不做事。

判定做了双保险：先解析 `netstat -ano` 的 UDP + TCP LISTENING 行拿 PID，再真做一次 UDP `bind` 复核；结束后同样两项都复核一次才算释放（并排除面板自身与当前核心 PID，避免把自己当成占用者）。

### 接管：端口是 53 就自动接管

界面上不再有「DNS 运行模式」这个选项，因为语义已经合并成一句：

* **端口 = 53** → 开启防护时自动接管：备份每块网卡的现有 DNS → `Set-DnsClientServerAddress` 指向 `127.0.0.1` → `Clear-DnsClientCache`，记录写进 `dns_backup.json`。
* **关防护 / 退出程序** → 自动还原（原本 DHCP 自动获取的网卡用 `-ResetServerAddresses` 复原）。
* 权限：管理员直接改；普通权限但有后台助手在跑 → 走命名管道交给 SYSTEM 助手，**不弹 UAC**；两者都没有 -> 提示先装一次助手。

所以设置页不再需要「系统 DNS 接管」那张卡片，也不再需要「接管 / 还原」按钮。

### 自定义端口（高级用法）

「监听端口」输入框仍然接受 1–65535。填非 53 时不会接管系统 DNS，只有你显式把设备 / 应用 / 路由器的 DNS 指到这个端口才会经过 ShieldDNS——留给「只防护某个程序」「路由器转发进来」这类场景。

### 上游：一个勾选框

* **不勾「使用自定义上游」**（默认）：跟随本机 DNS。开启防护 / 保存设置前枚举网卡现有 DNS，过滤 `127.*`、`::1`、`0.0.0.0`、`255.255.255.255` 与自身监听地址（避免解析成环），去重补 `:53`，最多 4 个。
* **勾上**：只用手填的列表，行为固定、可复现。

两种模式都会把保存的**公共 DNS 追加在后面做兜底**：路由器 DNS 偶发超时或开机冷启动时，核心按顺序重试下一个，不至于整条解析失败。读不到任何本机 DNS 时直接回退自定义列表，并在界面上标注「未取到本机 DNS，已回退备用上游」。

### 守护：合并进后台助手

后台助手（SYSTEM 身份常驻）每 30 秒自查一次，取代原先单独的看门狗计划任务：

1. 防护开着、端口是 53，但系统 DNS 没指向 `127.0.0.1`（被 VPN 或手动改掉）→ 补回接管。
2. 接管记录还在，但 `127.0.0.1` 连续 4 次没有 DNS 应答（核心崩溃且没重启成功）→ 还原系统 DNS，避免全机断网。

核心正常应答时它什么都不做，重复检查无害。安装助手那一次仍需要管理员（会弹一次 UAC），之后长期免弹。

实现细节（`dns_windows.go` + `server.go`）：

1. 取数优先用 `dns_backup.json` 里的接管前原值（这是最准确的“开启前 DNS”）；没有备份时实时枚举网卡。
2. 过滤 `127.0.0.1`、`127.*`、`::1`、`0.0.0.0`、`255.255.255.255`，去重、补 `:53`，最多留 4 个本机上游。
3. 本机上游之后自动追加你保存的**自定义公共 DNS 作为兜底**：路由器/运营商 DNS 偶发超时或开机冷启动时，核心会按顺序重试下一个，不至于整条解析失败（实测中确实出现过路由器首次查询超时）。
4. 读不到任何有效本机 DNS 时，直接回退自定义列表，并在 UI 标注「未取到本机 DNS，已回退备用上游」。
5. 时间点：程序启动、保存设置、点「重新读取本机 DNS」、接管系统 DNS 之前，各触发一次；每次都会写 `upstream_source` / `upstream_captured_at` 便于核对。

需要注意：本机上游若是路由器（如 `192.168.100.1`），路由器的家长控制/广告过滤会先过一遍；公共 DNS 兜底只在前面全部超时时才用，不会“抢答”。

### 异常还原保护（已并入后台助手）

场景：接管后网卡 DNS 是 `127.0.0.1`。如果面板/核心被强杀、或断电重启后核心没起来，全机就没有 DNS 了。

这件事现在由**后台助手常驻守护**完成，不再需要单独注册一个看门狗计划任务，设置页也没有对应的卡片：

* 助手每 30 秒查一次：防护开着、端口 53，但系统 DNS 没指向 `127.0.0.1` → 补回接管（VPN、手动改动会被自动纠正）。
* 接管记录还在，但 `127.0.0.1` 连续 4 次真实 A 查询（2 秒超时）都没应答 → 按 `dns_backup.json` 还原系统 DNS，避免全机断网。
* 核心正常应答时什么都不做，重复检查无害；结论写进 `logs\helper.log`，前缀为「守护：」。

**边界**：

* 备份文件被清理 → 无备份可还原，守护保持不动（不会瞎改 DNS），需要手动改回自动获取。
* 助手没装（或没在跑）→ 没有守护；此时异常退出只能手动还原，或重新启动面板让核心把解析接回来。

> 兼容说明：`<exe> -watchdog -data "<数据目录>"` 这个入口还在（老机器上已注册的 `ShieldDNS-DNS-Guard` 任务继续可用，`logs\watchdog.log` 仍会记录），但注册/注销/手动触发的代码与 `/system/guard` 接口已经删除，产品里也不再提示安装。想把老任务彻底去掉，管理员执行：
>
> ```powershell
> schtasks /Delete /TN "ShieldDNS-DNS-Guard" /F
> ```

**启动自检**：面板启动时若发现「仍有接管记录 + 当前无应答」，只写日志不擅自动网卡（因为紧接着核心就会启动并重新接管，此时还原反而造成抖动）。

### 后台助手（免 UAC 接管系统 DNS）

改网卡 DNS 是受 UAC 保护的操作，普通权限必然失败。后台助手把这一步交给一个常驻的高权限进程，面板自己永远用普通权限跑：

* 计划任务 `ShieldDNS-DNS-Helper`，`SYSTEM` 身份，`ExecutionTimeLimit=PT0S`（不限制时长，常驻）。触发器有两个：`BootTrigger` 开机 5 秒后启动；`LogonTrigger` 登录 8 秒后启动，并挂 **30 分钟重复**——助手被杀掉或异常退出后任务回到 Ready，下一轮重复会把它重新拉起，普通权限用户不必为"重启助手"去提权。
* 动作 `<exe> -helper -data "<数据目录>"`（同样因为 SYSTEM 的 `%LOCALAPPDATA%` 是 `systemprofile`）。
* 助手在命名管道 `\\.\pipe\ShieldDNS-DNS-Helper` 上服务，**管道 ACL 只放行 SYSTEM、Administrators 和安装时的那个用户 SID**（SID 写在 `helper_user.sid`），其它账户连不上。
* 协议只有四个动作：`ping` / `status` / `takeover` / `release`。收到请求后用同一个 PowerShell 实现去写/还原网卡 DNS，成功即回 `{"ok":true}`。
* 面板调用顺序：自己是管理员就直接干；否则先试助手。都不行才返回「需要管理员权限：请安装后台助手或提权重启」。
* 安装（设置页「后台助手」→ 安装后台助手）需要一次 UAC；装完立刻 `schtasks /Run` 拉起并 `ping` 校验，之后开机/登录自启、随用随连**全程零弹窗**。卸载同理只需一次提权。
* 日志写在 `logs\helper.log`（每次动作一行，含 ok/错误/detail）；面板自己的启动自愈记录写在 `logs\panel.log`。
* **助手自己挂掉怎么办**：面板启动时若发现「任务装了但管道连不上」，是管理员就直接 `schtasks /Run` 拉一把并把结果写进 `logs\panel.log`；普通权限拉不动也没关系——登录触发与 30 分钟重复会自动恢复，不需要重装。想立刻恢复就点「以管理员身份重启」，再点「重启」（该按钮现在走「重新注册任务」，顺手把旧定义升级成带登录触发/重复的新版本）。
* **多实例 / 先后启动**：助手是 SYSTEM 常驻进程，生命周期跟面板无关。同一账户、同一目录先后开第二个实例，看到的是「已安装·运行中」，直接复用、不弹 UAC。只有三种情况会显示「已安装·未运行」：换 Windows 账户（管道 ACL 没放行该 SID）、换了 exe 目录（任务里记的是安装时那份路径）、助手进程刚被杀（等 30 分钟自动恢复或提权重启）。
* **普通权限怎么看任务装没装**：`schtasks /Query` 查 SYSTEM 任务会返回「拒绝访问」，这本身就是"任务存在"的证据（不存在时报的是"找不到"），面板据此显示「已安装」。

**启动行为**：应用启动时一律 `protection_enabled=false`，不自动接管；如果检测到上次是接管状态（`dns_backup.json` 还在），会先把网卡 DNS 还原（有提权用提权，没提权用助手），避免「程序开着但没开防护、解析却挂在 127.0.0.1」。默认端口 53；53 被占用时启动失败，由面板顶部提示条告知占用进程并支持一键结束重试（不再自动回退）。

---

## 8. AdGuard 语法支持

一行一条规则。解析顺序：注释 → 例外规则（`@@`）优先 → 拦截规则。

| 语法 | 支持 | 说明 |
| --- | --- | --- |
| `||example.com^` | ✅ | 域名及其子域（后缀匹配） |
| `|http://example.com` | ✅ | 前缀匹配（`|` 锚点） |
| `example.com` | ✅ | 裸域名按后缀匹配（等价 `||example.com^`） |
| `@@||example.com^` | ✅ | 例外/白名单，优先级高于拦截 |
| `/ads[0-9]+/` | ✅ | 正则规则 |
| hosts 格式 `0.0.0.0 ads.com` | ✅ | 自动取域名 |
| `*` 通配 | ✅ | 通配符转正则 |
| `$third-party` 等修饰符 | ✅（剥离） | DNS 层无法区分，修饰符被忽略后按域名规则匹配 |
| `!` 注释 / `#` 注释 / `[Adblock Plus 2.0]` | ✅ | 跳过 |
| 单段无点规则（如 `ads`） | ❌ 丢弃 | 避免误杀 |
| 元素隐藏 `##`、脚本 `#%#` | ❌ 忽略 | DNS 层不涉及页面元素 |

拦截时直接返回 **NXDOMAIN**（默认），也可在设置里切成 `zero`（应答 `0.0.0.0` / `::`）。白名单命中即放行，用于保护正片/登录/支付等正常访问——策略是「精确规则优先、命中即 NXDOMAIN、白名单放行」，不放宽到整个裸域。

---

## 9. 订阅与自动更新

* 添加：面板「过滤订阅 → 添加订阅」，填 URL + 名称即可（URL 必须以 `http`/`https` 开头）。
* 拉取：下载上限 64 MB → 校验通过后原子写入 `filters\<id>.txt` → 统计有效规则条数 → 落配置 → 重建引擎 → 清缓存。
* 更新：每个过滤器有开关和「立即更新」；后台循环每 60 秒心跳一次，按 `update_interval_minutes` 判断是否到期，到期且 `auto_update=true` 才真正下载。
* 失败会写进 `last_error` 并在面板上标出来，不影响已有规则继续生效。

---

## 10. 配置文件

`%LOCALAPPDATA%\ShieldDNS\config.json`（核心与面板共用同一份；面板写入用「临时文件 + rename」保证原子性）：

```json
{
  "listen_address": "127.0.0.1",
  "listen_port": 53,
  "control_port": 5399,
  "protection_enabled": false,
  "block_mode": "nxdomain",
  "upstreams": ["192.168.100.1:53", "114.114.114.114:53", "223.5.5.5:53"],
  "upstream_mode": "system",
  "custom_upstreams": ["223.5.5.5:53", "119.29.29.29:53", "1.1.1.1:53"],
  "upstream_source": "当前网卡（WLAN、本地连接* 3），备用：公共 DNS",
  "upstream_captured_at": "2026-11-10 12:50:35",
  "cache_size": 4096,
  "cache_enabled": true,
  "log_queries": true,
  "max_log_entries": 500,
  "filters": [],
  "custom_rules": ["||ads.example.com^"],
  "panel_port": 8899,
  "dns_mode": "system",
  "auto_start": true
}
```

字段说明：

* `listen_address` / `listen_port`：DNS 监听地址与端口（新装默认 `127.0.0.1:53`；53 被占用时不会自动换端口，而是启动失败并提示占用者）。
* `control_port`：核心控制 API 端口（默认 5399，只绑 127.0.0.1）。
* `block_mode`：`nxdomain` 或 `zero`。
* `upstreams`：实际生效的上游，按顺序重试；面板里每行一个（自定义模式下就是用户填的）。
* `upstream_mode`：设置页那颗「使用自定义上游」勾选框的落点——勾上 = `custom`（只用 `custom_upstreams`）；不勾 = `system`（按本机 DNS 抓取，含公共 DNS 兜底）。
* `custom_upstreams`：用户保存的自定义/备用上游，切回自定义模式时用它。
* `upstream_source` / `upstream_captured_at`：上次抓取来源与时间，面板上展示，便于确认“抓的到底是谁”。
* `cache_size` / `cache_enabled` / `max_log_entries`：缓存与日志容量。
* `filters[]`：订阅列表，新装为空；添加订阅后每项含 `url` / `enabled` / `auto_update` / `interval` / `rule_count` / `kind=subscription`。
* `custom_rules[]`：面板里手写的规则。
* `panel_port` / `dns_mode` / `auto_start`：面板专属。`dns_mode` 已经不再由用户选择，而是按端口推导：`listen_port == 53` → `system`（自动接管），其它端口 → `custom`；保留该字段只为兼容旧配置与日志。`auto_start` 对应设置页「开机自动启动」。

核心也接受 `--config <path>` 指定其它配置文件，`--version` 打印版本。

面板的内置命令（一般不用手动敲）：

* `ShieldDNS.exe` —— 正常启动（窗口 + 托盘）。
* `ShieldDNS.exe -elevated` —— 以管理员身份启动（面板内部通过 UAC 调用）。
* `ShieldDNS.exe -watchdog` —— 看门狗模式：不建窗口、不抢单实例，只做一次「探测 → 必要时还原」，供计划任务调用。
* `ShieldDNS.exe -watchdog -data "<目录>"` —— 以 SYSTEM 运行时指定数据目录（计划任务就是这么注册的）。
* `ShieldDNS.exe -helper -data "<目录>"` —— 后台助手模式：SYSTEM 常驻，监听命名管道供面板执行 DNS 接管/还原。
* `SHIELDDNS_DATA_DIR=<目录>` 环境变量可覆盖数据目录，调试看门狗时很有用。

---

## 11. 核心控制 API（只监听 127.0.0.1:5399）

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| GET | `/api/health` | 健康检查（面板用来判断核心是否就绪） |
| GET | `/api/status` | 运行状态、统计、过滤器摘要、上游列表 |
| GET | `/api/filters` | 过滤器列表 |
| POST | `/api/filters` | 新增订阅 `{url, name?, id?}` |
| PATCH | `/api/filters/{id}` | 改开关 / 自动更新 / 名称 / 更新间隔 |
| DELETE | `/api/filters/{id}` | 删除订阅（同时删本地规则文件） |
| POST | `/api/filters/update` | 更新全部订阅 |
| POST | `/api/filters/{id}/update` | 更新指定订阅 |
| POST | `/api/protection` | `{enabled:true/false}` 开关保护 |
| GET / POST | `/api/rules` | 读取 / 保存自定义规则文本 |
| GET / DELETE | `/api/logs` | 查询日志 / 清空日志 |
| GET | `/api/stats/top` | 拦截 Top 域名 |
| POST | `/api/cache/clear` | 清空 DNS 缓存 |
| POST | `/api/reload` | 重建引擎（规则改动后手动触发） |

面板自有接口（`127.0.0.1:8899`）：

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| GET | `/system/info` | 管理员状态、网卡列表、端口、核心是否运行、WebView 是否可用 |
| POST | `/system/protection` | 开关保护并同步托盘图标 |
| POST | `/system/settings` | 保存端口 / 上游 / 拦截模式（`dns_mode` 由端口推导，必要时重启核心、接管或还原） |
| POST | `/system/upstreams` | `{"action":"capture"}` 重新读取本机 DNS 作为上游 |
| GET/POST | `/system/helper` | 后台助手：GET 返回 `{installed,running,admin}`；POST `{"action":"register"|"unregister"|"start"}`（注册/卸载需管理员，一步一次 UAC） |
| POST | `/system/autostart` | `{"enabled":bool}` 设置开机自启（写 `HKCU\...\Run`，无需管理员） |
| GET | `/system/update` | 检查 GitHub Releases 最新版本（`?force=1` 跳过 10 分钟缓存），返回 `{ok,current,latest,hasUpdate,releaseURL,downloadURL,error}` |
| POST | `/system/update/apply` | 应用内自动更新：下载并替换程序文件后自动重启（进行中再次调用返回 409） |
| GET | `/system/update/status` | 更新进度 `{running,stage,percent,message,version,error,done}`，供界面轮询 |
| POST | `/system/openrelease` | `{"url":"https://github.com/sayaxia/ShieldDNS/..."}` 用系统默认浏览器打开下载页（只放行本仓库链接） |
| POST | `/system/elevate` | UAC 提权重启面板 |
| POST | `/system/restart`、`/system/quit` | 重启 / 退出 |

---

## 12. 端到端自检

核心跑起来后：

```powershell
python scripts\verify_core.py 53 5399
```

实测输出（保护开启）：

```
== 保护开启 ==
  1rtb.com               -> NXDOMAIN answers=0
  www.baidu.com          -> NOERROR  answers=4
== 关闭保护 ==
  1rtb.com               -> NOERROR  answers=6 (关闭后应放行)
== 重新开启保护 ==
  1rtb.com               -> NXDOMAIN answers=0
```

拉取订阅成功的日志示例：`过滤器 <id> 更新完成，共 N 条规则`（条数取决于你选的规则源）。

---

## 13. 图标

* 母版在 `assets/*_raw.png`，统一由 `assets\make_icons.py` 生成交付用图（Pillow）：

  ```powershell
  pip install pillow
  python assets\make_icons.py
  ```

* 闪电版按 alpha 裁切居中；暂停版来自黑底生成图，脚本按灰度阈值生成蒙版 → 轻微模糊 → 裁切居中，得到透明底。
* 输出 `shield_on.png` / `shield_off.png`（1024×1024 透明底）、`shield_on.ico` / `shield_off.ico`（多尺寸）、`app.ico`（16/24/32/48/64/128/256）、`logo.png`（256）。
* 资源编号（`panel\app.rc`）：`1 ICON app.ico`（窗口/任务栏）、`2 ICON shield_on.ico`（托盘-保护开启）、`3 ICON shield_off.ico`（托盘-保护关闭）。
* 面板里显示的大图标用的是 `web\assets\shield_on.png` / `shield_off.png`，随保护状态切换。

---

## 14. 已知限制与排障

* **管理员权限**：改系统 DNS 需要管理员。两种解法：点「以管理员身份重启」（一次 UAC），或装一次「后台助手」之后长期免 UAC。当前开发机为普通权限会话，但 **53 + 接管链路已实测通过**：普通权限实例（`admin=false`）在装有助手时切 53 返回 `ok:true`、两块网卡 DNS 变 `127.0.0.1`、`127.0.0.1:53` 解析正常、`1rtb.com` 返回 NXDOMAIN、关闭防护后网卡回 `192.168.100.1` / `114.114.114.114`。
* **端口避让**：`5353` 常被 Windows mDNS（`svchost`）双栈占用，`15353` 等高位端口也可能被其他 DNS 工具占用；遇到 `os error 10013` 或「过滤看起来没生效但统计为 0」，先确认实际监听端口与查询打到的是哪个进程。
* **NXDOMAIN 策略**：拦截返回 NXDOMAIN，部分应用会把它当作「域名不存在」而非「被拦截」，属预期行为；需要 0.0.0.0 应答时切 `zero`。
* **只监听 127.0.0.1**：默认只服务本机。要让局域网其它设备用，把 `listen_address` 改成 `0.0.0.0`（注意放通防火墙，且此时控制口仍只绑 127.0.0.1）。
* **WebView2 空白窗口**：若窗口只有白底，先确认已装 WebView2 Runtime；另外用命令行 `-WindowStyle Hidden` / 被启动器以隐藏方式拉起的进程，Windows 会用启动参数覆盖首次 `ShowWindow`，表现为窗口不显示或页面不绘制，正常双击启动即可。
* **本机上游的可用性**：跟随本机 DNS 时，首个上游常见是路由器。实测出现过「路由器 DNS 首次查询超时」：核心此时会按顺序重试后续上游（已把自定义公共 DNS 自动追加为兜底），单个上游 UDP 超时 2s。若路由器长期不稳定，建议仍用自定义上游。
* **看门狗未注册时**：接管状态下异常退出/重启，网卡会一直指向 `127.0.0.1`，表现为「网页打不开、提示 DNS 错误」。此时要么重新启动面板（核心一起来就恢复解析），要么在设置页点「还原系统 DNS」，或直接手动把网卡 DNS 改回自动获取。
* **接管按钮提示需要管理员权限**：说明既没提权、后台助手也没装（或没在跑）。装一次助手即可长期免弹；`logs\helper.log` 里没有新行就是助手没起来，点「重启助手」。
* **设置页里改完的端口又被改回旧值**：旧版设置页每 2 秒轮询一次，会把还没保存的编辑用服务器旧值覆盖。现在只要动过端口/模式/上游（未点「应用设置」）就加脏标记，轮询只更新状态、不回写表单；保存成功才清标记。若点应用后弹「需要管理员权限」，说明当前实例没提权，点右下角「以管理员身份重启」再来。
* **旧进程残留**：曾出现旧版 DNS 程序占用端口导致「过滤失效」的假象，排查时用 `Get-NetTCPConnection -LocalPort <port>` 看所属 PID，别只看进程名。
* **切到 53 后仍然没有解析**：核心启动时会先杀掉上一轮遗留的 `shielddns-core.exe`，并校验 `/api/health` 返回的 `pid` 必须是本次拉起的子进程——不再出现「旧核心在应答、新核心根本没起来」的假成功。确认可用 `Get-NetUDPEndpoint -LocalPort 53` 看归属，或看 `/system/info` 的 `coreRunning` / `coreError`。

---

## 15. 许可证与致谢

* 本项目代码：[MIT](LICENSE)。
* 规则：本项目不内置、也不分发任何第三方规则源；用哪份规则由使用者自行添加并遵守对应源的许可。
* Go 面板依赖：[jchv/go-webview2](https://github.com/jchv/go-webview2)（MIT）、`golang.org/x/sys`。
* 图标为本项目自制（AI 生成母版 + 脚本后处理）。

---

## 16. 版本记录

### 1.0.1

**应用内自动更新**

* 设置页有新版时的「前往下载」换成「立即更新」：由面板自己下载 Release 压缩包、解压校验，把 `ShieldDNS.exe` 与 `core\shielddns-core.exe` 换成新文件，再拉起新实例、退出当前进程，不用再去浏览器下载解压。
* 替换顺序是「停核心 → 旧文件改名 `.old` → 写新文件 → 起新进程 → 退出」。Windows 不允许覆盖正在运行的 exe，但允许改名；拷贝失败会把 `.old` 改回去，`.old` 下次启动时清掉。
* Release 包外面套了一层版本目录（`ShieldDNS-1.0.1-win-x64/`），所以按文件名递归定位程序与核心，不假定文件在压缩包根上。
* 下载先写 `.part`，长度和 Release 报的尺寸对不上就当失败重下；解压过滤 zip-slip；解出的程序与核心体积不合理时不动现有文件。
* 进度走 `GET /system/update/status`，界面 700ms 轮询 `stage/percent/message`；更新完成后程序重启，防护按既有逻辑回到待命状态，需要手动再开。
* 装在没有写入权限的目录（如 `Program Files`）时会失败并提示先「以管理员身份重启」；「发布页」按钮保留作手动兜底。

**发布**

* 版本号 1.0.1（`panel/version.go`、`core/Cargo.toml`、`core/Cargo.lock` 三处同步）。

### 1.0.0

**后台助手**

* 修「换了程序路径后助手重启没用，必须卸载重装」：根因是 `schtasks /End` 只结束任务实例，SYSTEM 助手进程还活着并占着命名管道，任务定义 `MultipleInstancesPolicy=IgnoreNew` 让新实例拉不起来，界面 ping 到旧进程就显示「运行中」，成功的假象直到卸载（连带删进程）才消失。
* 现在注册 / 卸载助手任务都会在 `/End` 后等管道真正断开（1.5s），没断就用 `taskkill /F /FI "IMAGENAME eq ShieldDNS.exe" /FI "SESSION eq 0"` 清掉，再等 3s；仍失败就明确报错，不再假装成功。
* 助手 ping 回传自己的 exe 路径，`/system/info` 新增 `helperStale`；设置页助手卡显示「路径已变」并提示点「重启」，不用卸载重装。
* 启动自愈：自启项存在就按当前路径重写；`helperStale` 且当前有管理员权限时自动重注册助手任务。

**托盘与退出**

* 托盘补 `TaskbarCreated` 处理：注册 `RegisterWindowMessageW("TaskbarCreated")`，资源管理器重启 / 更新 / 「重启资源管理器」后先清残留再重新 `NIM_ADD`，不再出现「托盘图标消失、程序还在后台跑且再也不回来」。
* 退出路径全部打点：`quit()` 入口（带 `DNSMode`、是否已接管 DNS）、`/system/quit` 的来源与 UA、托盘菜单「退出应用」、`WM_CLOSE` / `WM_DESTROY`、窗口消息循环结束、`NIM_ADD` 的返回值。事后能一句话分清是用户主动退出、接口调用，还是进程自己没了——之前这条路径不写日志，只能靠猜。
* 点 × 收托盘的行为不变：`WM_CLOSE` 接管成 `SW_HIDE`，进程、核心、防护都继续跑，只有托盘菜单「退出应用」或设置页「退出」才真正结束。

**发布**

* 版本号 1.0.0（`panel/version.go`、`core/Cargo.toml`、`core/Cargo.lock` 三处同步）。

### 0.1.2

**窗口与界面**

* 窗口锁 960:720 宽高比，随系统 DPI 自适应（175% 缩放下建窗即按物理像素算），最小逻辑宽 790；拖拽缩放时比例由 `WM_SIZING` / `WM_GETMINMAXINFO` / `WM_EXITSIZEMOVE` 三段约束。
* 窗口变小时内容整页等比缩放（`documentElement.style.zoom`，clamp 0.7–2.4），不再依赖 CSS 断点把卡片挤成两三列。常规尺寸 960×720。
* 概览 hero 卡去重：去掉「保护已暂停」pill 与时间戳，监听地址 / 规则数 / 拦截率一行上移到标题上方；右列「开关 + 状态」并排，下面是「更新全部订阅」与订阅更新时间（取 `filters[].last_update` 的最大值，不是轮询时间）。
* 设置页精简：未勾选自定义上游时，上游卡不再列出那串 IP，只留「读取于 HH:MM」与「重新读取本机 DNS」。
* 底部说明改为「无内置规则，规则需自行添加」。

**启动体验**

* 建窗不显示，等页面首帧信号（`/panel/ready`）到了再显示窗口，2 秒超时兜底——启动不再先露一个无内容的白窗口。
* 隐藏建窗有两个必须补的前提：控制器要显式 `PutIsVisible(true)`、默认背景底色要改成面板深色，否则页面一直不画、显示出来仍是白的；首帧打点也不能只靠 `requestAnimationFrame`（隐藏窗口不回调），补 `setTimeout` 兜底。
* 进程第一次 `ShowWindow` 会被 STARTUPINFO 覆盖（父进程用最小化/隐藏方式拉起时窗口直接变最小化，看着像启动即退出）→ 连续两次 `SW_SHOWNORMAL` + `IsIconic → SW_RESTORE` 兜底。

**清理**

* 删除前端已不再使用的 `/system/guard`、`/system/takeover`、`/system/release` 接口，以及看门狗任务的注册 / 注销 / 手动触发代码；`-watchdog` 入口保留，老机器上已注册的 `ShieldDNS-DNS-Guard` 任务继续可用（要清掉见第 7 节的命令）。

### 0.1.0（首个公开版本）

**核心（Rust）**

* AdGuard / hosts 语法过滤引擎（精确 + 后缀 + 正则），命中返回 NXDOMAIN 或 0.0.0.0。
* UDP/TCP 双协议、TC 位截断自动走 TCP、`name|qtype` 缓存、查询日志与统计。
* 订阅按 `update_interval_minutes` 自动更新，60 秒心跳；单过滤器失败只记 `last_error`，不影响其它规则。

**面板（Go + WebView2）**

* 内嵌单页 UI、系统托盘、绿盾/暂停双图标、关窗进托盘。
* 端口决定接管语义：53 = 开防护即自动接管、关防护自动还原；非 53 只做定向监听。不再有「DNS 运行模式」选项。
* 上游：单勾选框（勾上=自定义列表，不勾=跟随本机 DNS，接管前取值 + 公共 DNS 兜底）。
* 后台助手（`ShieldDNS-DNS-Helper`）：SYSTEM 常驻 + 命名管道，普通权限即可切 53 / 接管 / 还原，装一次 UAC 后长期免弹；同时每 30 秒自查（漏接管补回、核心失联 4 次还原系统 DNS），原「看门狗」职责已并入这里。
* 版本与更新：界面显示版本号，一键查询 GitHub Releases 最新 tag，有新版本给下载入口。

**本版本修掉的问题**

* 提权重启后弹「已有实例」：旧实例 1.2s 才释放互斥体 → 改为先 `releaseSingleInstance()` 再退出，新实例按 deadline 重试（提权 15s / 普通 1.5s）。
* 提权后切 53「假成功」：孤儿核心仍在应答 `/api/health` → 健康检查增加 `pid` 校验，并清理镜像名不符的残留进程。
* 设置页里填的端口被轮询改回旧值：旧版每 2s 回写表单 → 加脏标记，保存成功才清。
* 退出卡死：`quit()` 里托盘/WebView 清理偶尔卡消息循环 → 2.5s 兜底 `os.Exit`，退出前先还原系统 DNS。
* 任务存在性误判：普通权限 `schtasks /Query` 查 SYSTEM 任务返回「拒绝访问」被当成「未安装」→ 把「拒绝访问」视为存在，并兼容中文系统 GBK 报错字节。
* UI 缩放变形：标签被压成竖排、列互相挤压 → 标签 `nowrap`、表格 `min-width` + 内部横向滚动、长域名省略号。
* 页面重复标题：顶栏已有「查询日志 / 自定义规则」→ 卡片内重复标题删除，工具栏右对齐。

---

## 17. Code signing policy（代码签名政策）

Free code signing provided by [SignPath.io](https://signpath.io), certificate by [SignPath Foundation](https://signpath.org).

* Committers and reviewers: 仓库协作者（当前仅 [@sayaxia](https://github.com/sayaxia)）
* Approvers: [@sayaxia](https://github.com/sayaxia)
* Privacy policy: 本程序不会向任何第三方传输信息，除非用户主动添加订阅 URL 或自行指定 DNS 上游；查询日志与配置只保存在本机 `%LOCALAPPDATA%\ShieldDNS\`。

签名当前状态、免费证书渠道对比与接入步骤见 [docs/SIGNING.md](docs/SIGNING.md)。

---

## 18. 代码签名

Windows 上未签名的 exe 双击会触发 SmartScreen「未知发布者」，部分杀软也会因为「无签名 + 改 DNS」双重特征报可疑。签名相关说明、免费证书渠道对比与本项目当前状态见 [docs/SIGNING.md](docs/SIGNING.md)。

---

## 19. 参与与反馈

* 问题与建议：开 [Issue](../../issues)，附上系统版本、`listen_port`、`logs\core.log` 相关片段。
* 规则问题：先确认是规则没命中（放行了）还是被误拦（该放行却 NXDOMAIN），再把域名发上来。
* 想改代码：Fork → 分支 → `build.ps1` 自测 → 提 PR。提交前请确保 `python scripts\verify_core.py 53 5399` 通过。
* 提 Issue / PR 时请**不要**粘贴完整 `config.json`（里面有你的网卡与内网 DNS 地址）。

---

## 20. 免责声明

* 本软件通过修改本机 DNS 指向实现拦截，属于**系统网络配置变更**。作者不对因误配置、异常退出、断电等导致的断网、数据丢失或其它损失负责。
* 默认只监听 `127.0.0.1`；若你改成 `0.0.0.0` 供局域网使用，请自行评估暴露风险并配置防火墙。
* 拦截规则来自第三方订阅仓库，作者不保证规则的正确性、完整性与时效性。
