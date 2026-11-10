package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modShell32              = windows.NewLazySystemDLL("shell32.dll")
	modAdvapi32             = windows.NewLazySystemDLL("advapi32.dll")
	procShellExecuteW       = modShell32.NewProc("ShellExecuteW")
	procOpenProcessToken    = modAdvapi32.NewProc("OpenProcessToken")
	procGetTokenInformation = modAdvapi32.NewProc("GetTokenInformation")
)

type dnsAdapter struct {
	Index     int      `json:"index"`
	Alias     string   `json:"alias"`
	Addresses []string `json:"addresses"`
}

type dnsBackup struct {
	TakenOverAt string       `json:"taken_over_at"`
	Adapters    []dnsAdapter `json:"adapters"`
}

func unsafePtr(p *uint16) unsafe.Pointer {
	return unsafe.Pointer(p)
}

// IsAdmin 直接读进程令牌的 TokenElevation，避免依赖 x/sys 版本
func IsAdmin() bool {
	cur, err := windows.GetCurrentProcess()
	if err != nil {
		return false
	}
	var token windows.Handle
	if r, _, _ := procOpenProcessToken.Call(uintptr(cur), 0x0008, uintptr(unsafe.Pointer(&token))); r == 0 {
		return false
	}
	defer windows.CloseHandle(token)
	var info struct{ TokenIsElevated uint32 }
	var retLen uint32
	r, _, _ := procGetTokenInformation.Call(
		uintptr(token), 20, // TokenElevation
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
		uintptr(unsafe.Pointer(&retLen)),
	)
	return r != 0 && info.TokenIsElevated != 0
}

// IsAdminCapable 账号是否属于 Administrators 组（可能只是没提权）。
// whoami /groups 里会带 deny-only 标记，正好能区分"标准用户"和"管理员但未提权"。
func IsAdminCapable() bool {
	out, err := runHidden("whoami.exe", "/groups")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "S-1-5-32-544") {
			return true
		}
	}
	return false
}

func runPowerShell(script string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// 兜底：脚本一律以 base64(UTF-8 JSON) 输出，绕开控制台代码页
func decodeB64(out string) (string, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	data, err := base64.StdEncoding.DecodeString(last)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func backupPath() string {
	return filepath.Join(DataDir(), "dns_backup.json")
}

func IsTakenOver() bool {
	_, err := os.Stat(backupPath())
	return err == nil
}

func ReadBackup() (*dnsBackup, error) {
	data, err := os.ReadFile(backupPath())
	if err != nil {
		return nil, err
	}
	var b dnsBackup
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func ListAdapters() []dnsAdapter {
	script := `
$ErrorActionPreference='Stop'
$rows = Get-DnsClientServerAddress -AddressFamily IPv4 | Where-Object { $_.ServerAddresses -and $_.ServerAddresses.Count -gt 0 } | ForEach-Object {
  [pscustomobject]@{ index=$_.InterfaceIndex; alias=$_.InterfaceAlias; addresses=@($_.ServerAddresses) }
}
$json = ConvertTo-Json -InputObject @{ adapters = @($rows) } -Compress -Depth 4
[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($json))
`
	out, err := runPowerShell(script)
	if err != nil {
		return nil
	}
	text, err := decodeB64(out)
	if err != nil {
		return nil
	}
	var payload struct {
		Adapters []dnsAdapter `json:"adapters"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return nil
	}
	return payload.Adapters
}

// ---- 上游跟随系统 DNS ----

// isOwnDNS 判断某个地址是不是"指向本机"（回环 / 未指定地址），这类地址作为上游必然成环
func isOwnDNS(addr string) bool {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return true
	}
	if strings.HasPrefix(addr, "::1") || strings.HasPrefix(addr, "0.0.0.0") ||
		strings.HasPrefix(addr, "255.255.255.255") || strings.EqualFold(addr, "localhost") {
		return true
	}
	host := addr
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host, "::") {
		host = host[:i]
	}
	if strings.HasPrefix(host, "127.") {
		return true
	}
	return false
}

// normalizeUpstream 去掉本机地址、补齐 :53、去重，最多保留 4 个
func normalizeUpstream(addrs []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range addrs {
		a := strings.TrimSpace(raw)
		if a == "" || isOwnDNS(a) {
			continue
		}
		if !strings.Contains(a, ":") {
			a += ":53"
		}
		key := strings.ToLower(a)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, a)
		if len(out) >= 4 {
			break
		}
	}
	return out
}

// captureSystemUpstreams 取"开启防护前"的本机 DNS：优先用接管备份里的原值，其次实时枚举网卡。
// 接管状态下系统 DNS 已被改成 127.0.0.1，实时枚举没有意义，所以备份优先。
func captureSystemUpstreams(cfg *Config) ([]string, string, error) {
	var addrs []string
	source := ""
	if b, err := ReadBackup(); err == nil && len(b.Adapters) > 0 {
		for _, a := range b.Adapters {
			addrs = append(addrs, a.Addresses...)
		}
		names := []string{}
		for _, a := range b.Adapters {
			names = append(names, a.Alias)
		}
		source = "接管前备份（" + strings.Join(names, "、") + "）"
	}
	if len(addrs) == 0 {
		adapters := ListAdapters()
		names := []string{}
		for _, a := range adapters {
			addrs = append(addrs, a.Addresses...)
			names = append(names, a.Alias)
		}
		if len(names) > 0 {
			source = "当前网卡（" + strings.Join(names, "、") + "）"
		}
	}
	clean := normalizeUpstream(addrs)
	if len(clean) == 0 {
		// 拿不到有效上游时退回用户配置的公共 DNS，避免核心完全没有上游可用
		fallback := normalizeUpstream(cfg.CustomUpstreams)
		if len(fallback) == 0 {
			return nil, "无可用的本机 DNS", errors.New("没有读取到有效的本机 DNS，也没有可用的备用上游")
		}
		return fallback, "未取到本机 DNS，已回退备用上游", nil
	}
	// 把自定义公共 DNS 追加在后面做兜底：路由器/运营商 DNS 偶发超时或重启后冷启动时，
	// 核心会按顺序重试下一个上游，不至于整个解析失败。
	merged := append([]string{}, clean...)
	for _, f := range normalizeUpstream(cfg.CustomUpstreams) {
		dup := false
		for _, m := range merged {
			if strings.EqualFold(m, f) {
				dup = true
				break
			}
		}
		if !dup && len(merged) < 6 {
			merged = append(merged, f)
		}
	}
	if len(merged) > len(clean) {
		source += "，备用：公共 DNS"
	}
	return merged, source, nil
}

// applySystemUpstreams 把抓取结果写进配置（不改内存外的其它字段）
func applySystemUpstreams(cfg *Config) ([]string, string, error) {
	list, src, err := captureSystemUpstreams(cfg)
	if err != nil {
		return nil, src, err
	}
	cfg.Upstreams = list
	cfg.UpstreamSource = src
	cfg.UpstreamCapturedAt = time.Now().Format("2006-01-02 15:04:05")
	return list, src, nil
}

// TakeoverDNS 把所有已配置 DNS 的 IPv4 网卡指向 127.0.0.1，并记录原值以便还原
func TakeoverDNS() error {
	if !IsAdmin() {
		return errors.New("需要管理员权限才能接管系统 DNS")
	}
	script := `
$ErrorActionPreference='Stop'
$rows = @()
Get-DnsClientServerAddress -AddressFamily IPv4 | ForEach-Object {
  $addrs = @($_.ServerAddresses | Where-Object { $_ -and $_.Trim() -ne '' })
  if ($addrs.Count -gt 0) {
    $rows += [pscustomobject]@{ index=$_.InterfaceIndex; alias=$_.InterfaceAlias; addresses=$addrs }
    Set-DnsClientServerAddress -InterfaceIndex $_.InterfaceIndex -ServerAddresses @('127.0.0.1') -ErrorAction Stop
  }
}
Clear-DnsClientCache
$json = ConvertTo-Json -InputObject @{ adapters = @($rows) } -Compress -Depth 4
[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($json))
`
	out, err := runPowerShell(script)
	if err != nil {
		return err
	}
	text, err := decodeB64(out)
	if err != nil {
		return errors.New("解析网卡信息失败")
	}
	var payload struct {
		Adapters []dnsAdapter `json:"adapters"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return errors.New("解析网卡信息失败")
	}
	if len(payload.Adapters) == 0 {
		return errors.New("没有找到已配置 DNS 的 IPv4 网卡")
	}
	b := dnsBackup{
		TakenOverAt: time.Now().Format("2006-01-02 15:04:05"),
		Adapters:    payload.Adapters,
	}
	data, _ := json.MarshalIndent(b, "", "  ")
	return os.WriteFile(backupPath(), data, 0o644)
}

// ReleaseDNS 还原接管前的系统 DNS 设置
func ReleaseDNS() error {
	if !IsAdmin() {
		return errors.New("需要管理员权限才能还原系统 DNS")
	}
	b, err := ReadBackup()
	if err != nil {
		return errors.New("没有找到接管记录，无需还原")
	}
	if len(b.Adapters) == 0 {
		_ = os.Remove(backupPath())
		return nil
	}
	path := strings.ReplaceAll(backupPath(), "'", "''")
	script := fmt.Sprintf(`
$ErrorActionPreference='Stop'
$b = Get-Content -LiteralPath '%s' -Raw -Encoding UTF8 | ConvertFrom-Json
foreach ($a in $b.adapters) {
  if ($a.addresses -and @($a.addresses).Count -gt 0) {
    Set-DnsClientServerAddress -InterfaceIndex $a.index -ServerAddresses @($a.addresses) -ErrorAction Stop
  } else {
    Set-DnsClientServerAddress -InterfaceIndex $a.index -ResetServerAddresses -ErrorAction Stop
  }
}
Clear-DnsClientCache
`, path)
	if _, err := runPowerShell(script); err != nil {
		return err
	}
	return os.Remove(backupPath())
}

// Elevate 以管理员身份重新启动自身
func Elevate(extraArgs string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	args, _ := windows.UTF16PtrFromString(extraArgs)
	cwd, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	code, _, _ := procShellExecuteW.Call(
		0,
		uintptr(unsafePtr(verb)),
		uintptr(unsafePtr(file)),
		uintptr(unsafePtr(args)),
		uintptr(unsafePtr(cwd)),
		1, // SW_SHOWNORMAL
	)
	if code <= 32 {
		if code == 1223 {
			return errors.New("已取消管理员授权")
		}
		return fmt.Errorf("提权失败（错误码 %d）", code)
	}
	return nil
}
