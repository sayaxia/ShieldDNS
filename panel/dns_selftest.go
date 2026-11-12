package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runDNSSelfTest 是 DNS 写路径的诊断探针：
// 在指定网卡上「写 127.0.0.1 → 读回 → 清空回 DHCP → 读回」，把每一步的结果写进日志。
//
// 存在的理由：SetInterfaceDnsSettings 的返回值不可信（结构体布局差一个字段就会"成功但无变化"），
// 以前正是靠返回值判断成功，结果接管"成功"、备份照写、网卡 DNS 一个没变。
// 用得着的场合：怀疑接管/还不生效时，挑一块没有流量的网卡（例如 Wi-Fi Direct 虚拟网卡）跑一遍。
func runDNSSelfTest(index int) int {
	logPath := filepath.Join(DataDir(), "dns-selftest.log")
	var lines []string
	push := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	finish := func(code int) int {
		_ = os.WriteFile(logPath, []byte(strings.Join(lines, "\r\n")+"\r\n"), 0o644)
		return code
	}

	push("=== %s dns-selftest index=%d admin=%v ===", time.Now().Format("2006-01-02 15:04:05"), index, IsAdmin())
	if !IsAdmin() {
		push("缺少管理员权限，无法写网卡 DNS")
		return finish(2)
	}

	infos, err := enumAdapters()
	if err != nil {
		push("枚举网卡失败：%v", err)
		return finish(3)
	}
	var target *adapterInfo
	for i := range infos {
		if infos[i].Index == index {
			target = &infos[i]
			break
		}
	}
	if target == nil {
		names := make([]string, 0, len(infos))
		for _, a := range infos {
			names = append(names, fmt.Sprintf("%d:%s", a.Index, a.Alias))
		}
		push("找不到 Index=%d 的网卡，现有：%s", index, strings.Join(names, " "))
		return finish(4)
	}
	push("目标网卡：index=%d alias=%s dhcp=%v 当前 DNS=%s", target.Index, target.Alias, target.DHCP, strings.Join(target.Addresses, ","))

	if err := setAdapterDNS(target.GUID, []string{"127.0.0.1"}); err != nil {
		push("写入 127.0.0.1 失败：%v", err)
	} else {
		flushDNSCache()
		got := adapterDNSNow(target.GUID)
		push("写入 127.0.0.1 → 读回：%s（期望 127.0.0.1，%s）",
			strings.Join(got, ","), map[bool]string{true: "生效", false: "未生效"}[serversEqual(got, []string{"127.0.0.1"})])
	}

	if err := setAdapterDNS(target.GUID, nil); err != nil {
		push("恢复 DHCP 失败：%v", err)
	} else {
		flushDNSCache()
		got := adapterDNSNow(target.GUID)
		push("清空回 DHCP → 读回：%s（%s）", strings.Join(got, ","),
			map[bool]string{true: "已脱离 127.0.0.1", false: "仍指向 127.0.0.1"}[!containsOwnDNS(got)])
	}
	push("=== 结束 ===")
	return finish(0)
}
