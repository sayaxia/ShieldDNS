# SignPath Foundation 申请表（可直接粘贴）

SignPath 的申请是英文表单（<https://signpath.org/apply>）。下面是针对 ShieldDNS 的填写草稿。方括号里是需要你按实际情况替换的地方。

---

## Project

**Project name**

```
ShieldDNS
```

**Project description (short, one or two sentences)**

```
A local DNS filtering app for Windows: a Rust DNS filtering core plus a Go/WebView2 desktop panel that blocks ads and trackers for the whole machine, with optional system-wide DNS takeover and automatic recovery.
```

**Project description (longer, if the form allows)**

```
ShieldDNS is a Windows desktop application that runs a local DNS filtering engine on 127.0.0.1 and serves the whole machine after a one-click system DNS takeover. The DNS core is written in Rust (AdGuard-style rule syntax, subscription auto-update, cache, query logs), the desktop panel in Go with an embedded WebView2 UI and a system tray icon. It supports two modes: listen-only on a custom port, or listen on port 53 plus system-wide DNS takeover with a one-click restore, a watchdog scheduled task that puts the original DNS back if the app is killed or the machine reboots unexpectedly, and an optional SYSTEM helper service so that DNS takeover no longer requires a UAC prompt on every launch.

The project is a free, open-source, no-telemetry tool built for users who want DNS-level ad blocking on Windows without installing a service, a driver, or a router-side DNS server. All settings and query logs stay on the local machine.
```

**Repository URL**

```
https://github.com/sayaxia/ShieldDNS
```

**Home page / download page**

```
https://github.com/sayaxia/ShieldDNS#readme
```

**License (OSI-approved, no dual licensing)**

```
MIT
```

**Programming languages / build system**

```
Rust (Cargo) for the DNS core, Go for the Windows panel, GitHub Actions for builds (windows-latest)
```

**Is the project already released in the form to be signed?**

```
Yes. Releases are built by GitHub Actions and published as a zip containing ShieldDNS.exe and core/shielddns-core.exe. Release: https://github.com/sayaxia/ShieldDNS/releases
```

---

## Why we need signing

```
Unsigned Windows executables show the SmartScreen "Unknown publisher" warning, and antivirus heuristics are more suspicious of an unsigned binary that modifies DNS settings. Signing makes the download verifiable and lets our users confirm the binary was built from the public repository. The app is distributed only through GitHub Releases; there is no installer, no bundled third-party software, and no monetization.
```

---

## Team and roles

**Maintainers**

```
@sayaxia
```

**Committers and reviewers**

```
[仓库协作者链接，例如 https://github.com/sayaxia/ShieldDNS/graphs/contributors]
```

**Approvers (approve every signing request)**

```
@sayaxia
```

**MFA enabled on GitHub and on the SignPath account?**

```
Yes
```

---

## Build and artifact details

**Trusted build system**

```
GitHub.com — GitHub Actions, workflow .github/workflows/build.yml in the repository
```

**Artifact to be signed**

```
A zip file (ShieldDNS-<version>-win-x64.zip) containing:
  ShieldDNS.exe            (Go panel, windowsgui subsystem, built with -trimpath)
  core/shielddns-core.exe  (Rust DNS core, release profile)
```

**Are all binaries built from this repository's source by CI?**

```
Yes. Both binaries are built by the GitHub Actions workflow from this repository; no locally built binaries are published.
```

**Third-party components included**

```
None as binary. Go modules (jchv/go-webview2, golang.org/x/sys) and Rust crates are compiled in from source via Cargo / go.mod; no third-party filter subscription is bundled or preconfigured; users add their own rule sources at runtime.
```

**Does the software modify the system?**

```
Only when the user explicitly enables "53 + system DNS takeover". It changes the DNS server address of the network adapters to 127.0.0.1, keeps a backup in dns_backup.json, restores it on disable/exit/watchdog trigger, and requires administrator rights or an optionally installed SYSTEM helper task. This behaviour is documented in the README and shown in the UI.
```

**Uninstall instructions provided?**

```
Yes, in README section 6 (退出应用 → 删除目录 → 注销可选计划任务与开机自启).
```

**Privacy / telemetry**

```
No telemetry. The only outbound requests are the filter subscription downloads the user configured (one built-in subscription by default) and DNS queries forwarded to the user's DNS upstreams. Query logs are stored locally only.
```

---

## Anything else worth mentioning

```
The repository documents a code signing policy section in the README as required by SignPath Foundation, and the CI workflow already contains the SignPath signing step, which is enabled once the organization/project variables are configured.
```

---

## 提交后的跟进

1. 若被退回，常见原因是「没有可验证的发布记录」或「README 缺少功能说明」—— 补一个 Release、在 README 写清下载与使用步骤后重申。
2. 通过后把 Organization ID 等填进仓库 Variables（见 `docs/SIGNING.md` 3.2 第 8 步），再打 tag 验证一次签名产物。
3. 签名产物下载后跑 `Get-AuthenticodeSignature`，`Status` 必须是 `Valid`。
