# 代码签名与 SmartScreen

本文说明 ShieldDNS 的签名现状、可用的免费/低成本渠道、以及具体申请与集成步骤。

> **当前状态（2026-11-10）**：0.1.0 产物**未签名**。SignPath Foundation 免费开源签名申请已于 2026-11-10 通过官网表单提交（项目 ShieldDNS / 仓库 <https://github.com/sayaxia/ShieldDNS> / MIT / GitHub Actions 构建 / 申请人 Saya Xia），等待审核结果。审核通过后按第 3 节配置仓库变量即可自动签名。

---

## 1. 为什么签名，签了就能解决什么

| 现象 | 未签名 | 已签名（受信任 CA） |
| --- | --- | --- |
| 双击 exe 的 SmartScreen 提示 | 「Windows 已保护你的电脑 · 未知发布者」，要「更多信息 → 仍要运行」 | 首次下载仍可能提示，但发布者显示为证书主体；随后靠**同一身份**持续发布积累信誉，提示逐步消失 |
| 属性 → 数字签名 | 空白 | 有签名者、时间戳 |
| 杀软启发式（改 DNS + 无签名） | 更容易被判可疑 | 显著降低误报概率 |

要强调的是：**签名不等于立刻没有 SmartScreen 警告**。自 2024 年微软取消了 EV 证书的「首次下载直接放行」，任何新证书都要靠累计下载/运行信誉。签名解决的是「未知发布者」与可校验性，信誉仍要时间。

**自签名证书（New-SelfSignedCertificate）对公开发布没有任何用处**：不在 Microsoft 信任根里，用户没手动装根证书时依旧是「未知发布者」，甚至触发更强拦截。只适合本机调试或企业内部（用组策略/Intune 下发根证书）。

---

## 2. 渠道对比（2026 年实况）

| 渠道 | 费用 | 证书主体 | 中国个人可用 | SmartScreen | 备注 |
| --- | --- | --- | --- | --- | --- |
| **SignPath Foundation**（开源免费） | 0 | SignPath Foundation | ✅ | 受信任（Sectigo 签发） | 需公开仓库 + OSI 许可证 + 已有发布 + 全部 CI 构建 + 每次发布人工审批；密钥在对方 HSM |
| **Certum Open Source Code Signing** | 云服务版 €49/年（约 ¥380）；含卡套装 $89 | 你的名字 + "Open Source Developer" | ✅ | 受信任 | 需提交开源项目证明；有卡套装要寄实体卡 + 读卡器；2026-02-27 起单张证书最长 459 天 |
| **Azure Artifact Signing**（原 Trusted Signing） | $9.99/月（含 5000 次签名） | 你的组织/个人 | ❌ 个人仅限美国/加拿大；组织限美/加/欧盟/英国 | 受信任（微软 CA） | 中国大陆主体目前无法通过身份验证 |
| **Microsoft Store（MSIX）** | 0 | Microsoft 代签 | ✅ | 无警告 | 需要 MSIX 打包并过审；本项目是绿色单目录 exe，暂不适用 |
| OV 证书（DigiCert/Sectigo/GlobalSign） | $150–300/年 | 你的公司 | ✅ | 受信任 | 2023-06 起私钥必须在 HSM/硬件令牌里，必须走硬件；无公司主体时个人 OV 也难 |
| EV 证书 | $400+/年 | 你的公司 | ✅ | 与 OV 相同（2024 起无秒过） | 只为「秒过 SmartScreen」买 EV 已无意义 |
| 自签名 | 0 | 你自己 | ✅ | ❌ 仍是未知发布者 | 仅调试/企业内网 |

**结论**：本项目（个人开发者、开源、无公司主体、在中国大陆）唯一可行的免费路径是 **SignPath Foundation**；备选是 **Certum 开源证书（€49/年）**。Azure 因地区限制不可用。

---

## 3. SignPath Foundation 申请

### 3.1 硬性条件（官方条款）

- 公开可访问的源码仓库（GitHub 即可）。
- **OSI 认可的开源许可证**，且无商业双许可 —— 本项目 MIT，满足。
- 不含闭源/专有组件；上游二进制可以打包进已签名产物。
- **项目已在要被签名的形态下发布过**（需要有 Release 记录）。
- 功能在下载页/商店页有文档说明（README 即满足）。
- 全程 MFA：SignPath 账号 + GitHub 账号都要开。
- 团队角色：Authors（可直接改代码）/ Reviewers（审 PR）/ Approvers（批准每次签名）。
- 构建必须可验证来自该仓库（GitHub Actions 构建，不允许本机产物上传）。
- **每次发布需要人工在 SignPath 界面批准**。
- 首页必须有「Code signing policy」段落（模板见第 6 节，本项目已写入 README）。

### 3.2 操作顺序

1. 先把仓库 push 成 **public**，并打一个 tag（例如 `v0.1.0`）让 Actions 产出 Release —— 「已发布」是硬条件。
2. 打开 <https://signpath.org/apply>，填表。可直接用 [docs/SIGNPATH-APPLICATION.md](SIGNPATH-APPLICATION.md) 里的英文模板。
3. 审核通常几天到两周。通过后拿到：
   - Organization ID（`https://app.signpath.io/Web/<ORG_ID>/...`）
   - Project slug
   - Signing policy slug（开源项目一般 `release-signing`）
4. 在 GitHub 仓库安装 SignPath GitHub App（<https://github.com/apps/signpath>），只授权本仓库。
5. SignPath 控制台 → Trusted Build Systems → 添加 GitHub.com 并绑定仓库。
6. Artifact Configurations 新建（`windows-zip`）：

   ```xml
   <artifact-configuration xmlns="http://signpath.io/artifact-configuration/v1">
     <zip-file>
       <pe-file path="*.exe">
         <authenticode-sign />
       </pe-file>
       <pe-file path="core/*.exe">
         <authenticode-sign />
       </pe-file>
     </zip-file>
   </artifact-configuration>
   ```

7. My Profile → API Tokens 建一个 token，权限只要 Submitter。
8. 仓库 Settings → Secrets and variables → Actions：
   - Secrets：`SIGNPATH_API_TOKEN`
   - Variables：`SIGNPATH_ORG_ID`、`SIGNPATH_PROJECT_SLUG`、`SIGNPATH_POLICY_SLUG`、`SIGNPATH_ARTIFACT_CONFIG`
9. 打 tag 触发 `.github/workflows/build.yml`：签名步骤已写好，变量为空时自动跳过。

### 3.3 代价与限制（要接受再申请）

- 发布者显示为 **SignPath Foundation**，不是个人名字。
- 每次发布要进 SignPath 点一次批准，不能全自动。
- 证书属于基金会，违反其 Code of Conduct 可能被**追溯吊销**（历史上签过的版本也变无效）。
- 只能签本仓库源码构建的产物；不能把别的开源项目的二进制签成自己的。

---

## 4. Certum 开源证书（备选，€49/年）

- 云服务版（SimplySign，无实体卡）约 **€49/年**：<https://www.certumcodesign.cn/shop/>
- 证书主体为「你的姓名 + Open Source Developer」，需要提交开源项目证明（仓库地址、许可证）。
- 优点：发布者显示你自己的名字，可脱离 CI 在本机签（signtool + 云签名）。
- 缺点：要付费；有认证与人工审核；单证书最长 459 天，需按时续。

本机签名（拿到证书后）：

```powershell
# 需要 Windows SDK 的 signtool.exe
signtool sign /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 /a dist\ShieldDNS.exe
signtool sign /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 /a dist\core\shielddns-core.exe
signtool verify /pa /v dist\ShieldDNS.exe
```

验证签名：

```powershell
Get-AuthenticodeSignature .\dist\ShieldDNS.exe | Format-List Status, SignerCertificate
```

`Status = Valid` 且 `SignerCertificate.Subject` 是预期主体，才算签成功；带时间戳后证书到期也不影响已签版本的有效性。

---

## 5. 签名前的过渡做法

在签名审批期间，至少让用户能判断「这个 exe 是不是我发的」：

- 每个 Release 附上 `SHA256` 校验值：

  ```powershell
  Get-FileHash .\ShieldDNS-0.1.0-win-x64.zip -Algorithm SHA256
  ```

- Release 说明里写清「未签名 → SmartScreen 会提示未知发布者 → 点『更多信息 → 仍要运行』」，并给出源码仓库链接。
- 杀软误报时，引导用户把 `ShieldDNS.exe` 与 `core\shielddns-core.exe` 加入白名单，并到厂商误报提交页上报（Microsoft：<https://www.microsoft.com/en-us/wdsi/filesubmission>）。

---

## 6. README 里必须有的「Code signing policy」

SignPath 要求项目首页出现该段落。ShieldDNS 的 README 已包含：

> **Code signing policy**
> Free code signing provided by SignPath.io, certificate by SignPath Foundation.
> Committers and reviewers: 仓库协作者（当前仅 @sayaxia）。
> Approvers: @sayaxia。
> Privacy policy: 本程序除用户主动添加的订阅 URL 与 DNS 上游外，不向任何第三方传输信息；DNS 查询日志仅保存在本机 `%LOCALAPPDATA%\ShieldDNS\`。

审批后再把实际的角色链接补上即可。

---

## 7. 常见问题

- **签了还被 SmartScreen 拦**：新证书无信誉，正常。持续用同一身份发版，累积到数百次下载量后会明显好转。
- **杀软仍报**：签名只降低启发式误报概率；「修改 DNS + 监听 53」这个行为本身仍可能被标为「RiskWare」，需要向厂商逐个提交误报（微软 WDSI、火绒、360、卡巴斯基都有提交入口）。
- **能不能先自签名试试**：可以，对端到端验证签名流程有用，但不能用于对外发布。
- **CI 构建产物和本机产物不一致**：签名只覆盖 CI 产物。**对外发布的必须是从 Actions 下载回来的、已签名的那份**，不要用本机编的。
