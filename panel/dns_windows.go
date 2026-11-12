package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	// DHCP 为真时，还原走"清空 → 让 DHCP 重新下发"，而不是把地址写成静态；
	// 老备份没有这个字段，按 false 兼容。
	DHCP bool `json:"dhcp,omitempty"`
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

// tokenHasGroup 判断当前进程令牌里是否含某个组 SID。
// deny-only 的组也算在内，所以能区分「标准用户」和「管理员但没提权」。
func tokenHasGroup(sidText string) bool {
	target, err := windows.StringToSid(sidText)
	if err != nil {
		return false
	}
	var n uint32
	tok := windows.GetCurrentProcessToken()
	if err := windows.GetTokenInformation(tok, windows.TokenGroups, nil, 0, &n); err != nil &&
		!errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return false
	}
	if n == 0 {
		return false
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenGroups, &buf[0], n, &n); err != nil {
		return false
	}
	groups := (*windows.Tokengroups)(unsafe.Pointer(&buf[0]))
	want := target.String()
	for _, g := range groups.AllGroups() {
		if g.Sid != nil && g.Sid.String() == want {
			return true
		}
	}
	return false
}

// currentTokenSID 返回当前进程令牌里的用户 SID（形如 S-1-5-21-...）
func currentTokenSID() string {
	var n uint32
	tok := windows.GetCurrentProcessToken()
	if err := windows.GetTokenInformation(tok, windows.TokenUser, nil, 0, &n); err != nil &&
		!errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return ""
	}
	if n == 0 {
		return ""
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenUser, &buf[0], n, &n); err != nil {
		return ""
	}
	tu := (*windows.Tokenuser)(unsafe.Pointer(&buf[0]))
	if tu.User.Sid == nil {
		return ""
	}
	return tu.User.Sid.String()
}

// IsAdminCapable 账号是否属于 Administrators 组（可能只是没提权）
func IsAdminCapable() bool {
	return tokenHasGroup("S-1-5-32-544")
}

// ---- 网卡 DNS：全部走 Win32 原生接口，不起任何子进程 ----
//
// 早先用 powershell.exe 跑 Get-DnsClientServerAddress / Set-DnsClientServerAddress，
// 好处是省事，坏处是被安全软件的「系统加固·利用 PowerShell 执行可疑脚本」当成可疑行为拦截
// （程序一开机就拉一个带 -ExecutionPolicy Bypass 的 PowerShell，看着确实像脚本投毒）。
// 现在换成 iphlpapi 的原生实现：枚举用 GetAdaptersAddresses，写 DNS 用 SetInterfaceDnsSettings
// （netsh 和 DnsClient 模块内部也是这一套），刷缓存用 dnsapi 的 DnsFlushResolverCache。
// 顺带解决两个老毛病：不再依赖 PATH、不再有控制台代码页问题、也不再需要 base64 传 JSON。

var (
	modIphlpapi = windows.NewLazySystemDLL("iphlpapi.dll")
	modDnsapi   = windows.NewLazySystemDLL("dnsapi.dll")
	// SetInterfaceDnsSettings 是 Windows 10 1809 起提供的；更老的系统会找不到导出
	procSetInterfaceDnsSettings = modIphlpapi.NewProc("SetInterfaceDnsSettings")
	procDnsFlushResolverCache   = modDnsapi.NewProc("DnsFlushResolverCache")
)

const (
	dnsSettingsVersion1  = 1
	dnsSettingNameServer = 0x0002 // DNS_SETTING_NAMESERVER

	gAAFamilyIPv4    = 2      // AF_INET
	gAASkipAnycast   = 0x0002 // GAA_FLAG_SKIP_ANYCAST
	gAASkipMulticast = 0x0004 // GAA_FLAG_SKIP_MULTICAST
	ipAdapterDHCP    = 0x0004 // IP_ADAPTER_DHCP_ENABLED
)

// dnsInterfaceSettings 对应 DNS_INTERFACE_SETTINGS（netioapi.h）。
//
// 关键点：Flags 是 ULONG64，不是 ULONG。写成 uint32 会让 Go 把它放在偏移 4，
// 而 API 在偏移 8 读 8 字节 → 实际读到的是对齐填充（全 0）→ Flags=0 → 一个字段都不应用，
// 但返回值仍是 0（成功）。这个坑的表现就是：接管"成功"、备份也写了，网卡 DNS 却纹丝不动。
// 后面几个字段按官方声明补齐，即便当前只用到 NameServer，也让布局和系统完全一致。
type dnsInterfaceSettings struct {
	Version             uint32
	_                   uint32 // 对齐填充，让 Flags 落在偏移 8
	Flags               uint64
	Domain              *uint16
	NameServer          *uint16
	SearchList          *uint16
	RegistrationEnabled uint32
	RegisterAdapterName uint32
	EnableLLMNR         uint32
	QueryAdapterName    uint32
	_                   uint32 // 让 ProfileNameServer 落在偏移 64
	ProfileNameServer   *uint16
}

// adapterInfo 是枚举出来的网卡：多了 GUID（写 DNS 必须用 GUID）和是否由 DHCP 下发
type adapterInfo struct {
	Index     int
	Alias     string
	GUID      windows.GUID
	Addresses []string
	DHCP      bool
}

// enumAdapters 列出所有已配置 IPv4 DNS 的网卡（等价于 Get-DnsClientServerAddress -AddressFamily IPv4 过滤空值）
func enumAdapters() ([]adapterInfo, error) {
	var size uint32
	// 第一次传空指针只拿大小，返回 ERROR_BUFFER_OVERFLOW 属正常
	err := windows.GetAdaptersAddresses(gAAFamilyIPv4, gAASkipAnycast|gAASkipMulticast, 0, nil, &size)
	if err != nil && !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
		return nil, err
	}
	if size == 0 {
		return nil, errors.New("枚举网卡失败：系统返回了空缓冲区")
	}
	// 多给一倍余量：网卡可能在两次调用之间增减
	buf := make([]byte, size*2)
	head := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
	size = uint32(len(buf))
	if err := windows.GetAdaptersAddresses(gAAFamilyIPv4, gAASkipAnycast|gAASkipMulticast, 0, head, &size); err != nil {
		return nil, err
	}

	var out []adapterInfo
	for aa := head; aa != nil; aa = aa.Next {
		var addrs []string
		for d := aa.FirstDnsServerAddress; d != nil; d = d.Next {
			ip := d.Address.IP()
			if ip == nil {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				addrs = append(addrs, v4.String())
			}
		}
		if len(addrs) == 0 {
			continue
		}
		name := ""
		if aa.FriendlyName != nil {
			name = windows.UTF16PtrToString(aa.FriendlyName)
		}
		if name == "" {
			name = fmt.Sprintf("网卡 %d", aa.IfIndex)
		}
		guidStr := ""
		if aa.AdapterName != nil {
			guidStr = windows.BytePtrToString(aa.AdapterName)
		}
		// AdapterName 就是 "{GUID}" 形式；GUIDFromString 底层是 CLSIDFromString，
		// 必须连大括号一起传，剥掉就解析失败（"Invalid class string"）。
		guid, err := windows.GUIDFromString(guidStr)
		if err != nil {
			continue // 拿不到 GUID 就没法写 DNS，跳过
		}
		out = append(out, adapterInfo{
			Index:     int(aa.IfIndex),
			Alias:     name,
			GUID:      guid,
			Addresses: addrs,
			DHCP:      aa.Flags&ipAdapterDHCP != 0,
		})
	}
	return out, nil
}

// setAdapterDNS 把某块网卡的 IPv4 DNS 设成 servers；传空切片等于清空（回到 DHCP 下发的 DNS）
func setAdapterDNS(guid windows.GUID, servers []string) error {
	if err := procSetInterfaceDnsSettings.Find(); err != nil {
		return errors.New("当前系统不支持原生设置网卡 DNS（需要 Windows 10 1809 及以上）")
	}
	ns, err := windows.UTF16PtrFromString(strings.Join(servers, ","))
	if err != nil {
		return err
	}
	s := dnsInterfaceSettings{
		Version:    dnsSettingsVersion1,
		Flags:      dnsSettingNameServer,
		NameServer: ns,
	}
	// GUID 是 16 字节结构体，x64 下按指针传参
	r, _, _ := procSetInterfaceDnsSettings.Call(
		uintptr(unsafe.Pointer(&guid)),
		uintptr(unsafe.Pointer(&s)),
	)
	if r != 0 {
		return fmt.Errorf("设置网卡 DNS 失败（错误码 %d）", r)
	}
	return nil
}

// serversEqual 比较两个 DNS 列表（忽略顺序差异之外的空格/大小写）
func serversEqual(a, b []string) bool {
	clean := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, s := range in {
			if t := strings.TrimSpace(s); t != "" {
				out = append(out, strings.ToLower(t))
			}
		}
		return out
	}
	ca, cb := clean(a), clean(b)
	if len(ca) != len(cb) {
		return false
	}
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}

// adapterDNSNow 读回某块网卡当前的 IPv4 DNS（读不到就返回空，代表没有静态 DNS）
func adapterDNSNow(guid windows.GUID) []string {
	infos, err := enumAdapters()
	if err != nil {
		return nil
	}
	for _, a := range infos {
		if a.GUID == guid {
			return a.Addresses
		}
	}
	return nil
}

// flushDNSCache 清解析缓存，避免改完 DNS 还在用旧记录
func flushDNSCache() {
	if err := procDnsFlushResolverCache.Find(); err == nil {
		procDnsFlushResolverCache.Call()
	}
}

func backupPath() string {
	return filepath.Join(DataDir(), "dns_backup.json")
}

func IsTakenOver() bool {
	_, err := os.Stat(backupPath())
	return err == nil
}

// IsTakeoverEffective 才算"真的在接管"：备份在，而且确实有网卡指着 127.0.0.1。
// 只看备份文件会踩坑：上一次还原失败的残留备份会让新一轮接管被跳过，
// 界面上显示防护已开，实际网卡 DNS 还是路由器的，等于没防。
func IsTakeoverEffective() bool {
	if !IsTakenOver() {
		return false
	}
	infos, err := enumAdapters()
	if err != nil {
		return true // 枚举失败时保守认为在接管，避免重复写入
	}
	for _, a := range infos {
		if containsOwnDNS(a.Addresses) {
			return true
		}
	}
	return false
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
	infos, err := enumAdapters()
	if err != nil {
		return nil
	}
	out := make([]dnsAdapter, 0, len(infos))
	for _, a := range infos {
		out = append(out, dnsAdapter{Index: a.Index, Alias: a.Alias, Addresses: a.Addresses})
	}
	return out
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

// cleanOriginalDNS 过滤掉"指向本机"的地址。
// 上一次接管没还原成功时，枚举出来的"原值"就是 127.0.0.1——把它当原值写进备份，
// 还原时就会把网卡写回一个没人监听的死地址，整机解析直接断掉。
// 这类值一律视为"没有原值"，还原时走 DHCP。
func cleanOriginalDNS(addrs []string) []string {
	var out []string
	for _, a := range addrs {
		if isOwnDNS(a) {
			continue
		}
		out = append(out, strings.TrimSpace(a))
	}
	return out
}

// TakeoverDNS 把所有已配置 DNS 的 IPv4 网卡指向 127.0.0.1，并记录原值以便还原
func TakeoverDNS() error {
	if !IsAdmin() {
		return errors.New("需要管理员权限才能接管系统 DNS")
	}
	infos, err := enumAdapters()
	if err != nil {
		return fmt.Errorf("枚举网卡失败：%w", err)
	}
	if len(infos) == 0 {
		return errors.New("没有找到已配置 DNS 的 IPv4 网卡")
	}
	// 逐块网卡改成 127.0.0.1，成功的记进备份；失败的收集起来一起报，但不影响其它网卡
	var rows []dnsAdapter
	var failed []string
	type pending struct {
		row  dnsAdapter
		guid windows.GUID
	}
	var written []pending
	for _, a := range infos {
		if err := setAdapterDNS(a.GUID, []string{"127.0.0.1"}); err != nil {
			failed = append(failed, fmt.Sprintf("%s（%v）", a.Alias, err))
			continue
		}
		written = append(written, pending{
			row: dnsAdapter{
				Index:     a.Index,
				Alias:     a.Alias,
				Addresses: cleanOriginalDNS(a.Addresses),
				DHCP:      a.DHCP,
			},
			guid: a.GUID,
		})
	}
	flushDNSCache()
	// 读回校验：SetInterfaceDnsSettings 返回 0 并不代表生效（结构体布局差一个字段就会"成功但无变化"），
	// 所以必须读回来确认真的是 127.0.0.1，否则这块网卡不能记进备份、也不能算接管成功。
	if len(written) > 0 {
		nowByIndex := make(map[int][]string, len(written))
		if after, err := enumAdapters(); err == nil {
			for _, a := range after {
				nowByIndex[a.Index] = a.Addresses
			}
		}
		for _, p := range written {
			got := nowByIndex[p.row.Index]
			if serversEqual(got, []string{"127.0.0.1"}) {
				rows = append(rows, p.row)
				continue
			}
			failed = append(failed, fmt.Sprintf("%s（写入未生效，当前为 %s）", p.row.Alias, strings.Join(got, ",")))
		}
	}
	if len(rows) == 0 {
		return fmt.Errorf("接管失败：%s", strings.Join(failed, "、"))
	}
	b := dnsBackup{
		TakenOverAt: time.Now().Format("2006-01-02 15:04:05"),
		Adapters:    rows,
	}
	data, _ := json.MarshalIndent(b, "", "  ")
	if err := os.WriteFile(backupPath(), data, 0o644); err != nil {
		return fmt.Errorf("写入接管记录失败：%w", err)
	}
	if len(failed) > 0 {
		return fmt.Errorf("部分网卡接管失败：%s", strings.Join(failed, "、"))
	}
	return nil
}

// ReleaseDNS 还原接管前的系统 DNS 设置。
//
// 三条硬性要求（都是被真实事故教出来的）：
//  1. 原值本身是 127.0.0.1 的（老版本备份里可能有），一律按"没有原值"处理，走 DHCP，不能写回死地址；
//  2. 还原完要看一眼结果，仍有网卡指着 127.0.0.1 就再清一次；
//  3. 只要有一块没还原成功，备份文件必须留着，否则程序再也不知道原值是什么，只能人工救。
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
	// 备份里只存了 InterfaceIndex，写 DNS 要 GUID，所以现枚举一次做映射
	infos, _ := enumAdapters()
	byIndex := make(map[int]windows.GUID, len(infos))
	for _, a := range infos {
		byIndex[a.Index] = a.GUID
	}
	var failed []string
	for _, a := range b.Adapters {
		guid, ok := byIndex[a.Index]
		if !ok {
			failed = append(failed, a.Alias+"（网卡已不存在）")
			continue
		}
		// DHCP 下发的、或原值为空/环回的 → 清空让 DHCP 重新下发
		var servers []string
		if !a.DHCP {
			servers = cleanOriginalDNS(a.Addresses)
		}
		if err := setAdapterDNS(guid, servers); err != nil {
			failed = append(failed, fmt.Sprintf("%s（%v）", a.Alias, err))
		}
	}
	flushDNSCache()

	// 二次校验：读回来还有指向 127.0.0.1 的，再清一次；清不掉就记为失败（备份随之保留）
	if after, err := enumAdapters(); err == nil {
		want := make(map[int]string, len(b.Adapters))
		for _, a := range b.Adapters {
			want[a.Index] = a.Alias
		}
		for _, a := range after {
			if _, ok := want[a.Index]; !ok || !containsOwnDNS(a.Addresses) {
				continue
			}
			if err := setAdapterDNS(a.GUID, nil); err != nil {
				failed = append(failed, fmt.Sprintf("%s（补救清空失败：%v）", a.Alias, err))
				continue
			}
			if still := adapterDNSNow(a.GUID); containsOwnDNS(still) {
				failed = append(failed, fmt.Sprintf("%s（仍指向 127.0.0.1）", a.Alias))
			}
		}
		flushDNSCache()
	}

	if len(failed) > 0 {
		// 保留备份，下次开程序还能再还原一次
		return fmt.Errorf("还原未完成：%s", strings.Join(failed, "、"))
	}
	return os.Remove(backupPath())
}

// containsOwnDNS 地址列表里有没有指向本机的死地址
func containsOwnDNS(addrs []string) bool {
	for _, a := range addrs {
		if isOwnDNS(a) {
			return true
		}
	}
	return false
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
