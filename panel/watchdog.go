package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func watchdogLog(format string, args ...any) {
	dir := filepath.Join(DataDir(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "watchdog.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	_, _ = f.WriteString("[" + time.Now().Format("2006-01-02 15:04:05") + "] " + msg + "\r\n")
}

func buildDNSQuery(id uint16, name string) []byte {
	buf := make([]byte, 0, 300)
	buf = binary.BigEndian.AppendUint16(buf, id)
	buf = binary.BigEndian.AppendUint16(buf, 0x0100) // RD
	buf = binary.BigEndian.AppendUint16(buf, 1)      // QDCOUNT
	buf = binary.BigEndian.AppendUint16(buf, 0)      // ANCOUNT
	buf = binary.BigEndian.AppendUint16(buf, 0)      // NSCOUNT
	buf = binary.BigEndian.AppendUint16(buf, 0)      // ARCOUNT
	for _, label := range strings.Split(name, ".") {
		if len(label) > 63 || label == "" {
			continue
		}
		buf = append(buf, byte(len(label)))
		buf = append(buf, label...)
	}
	buf = append(buf, 0)
	buf = binary.BigEndian.AppendUint16(buf, 1) // QTYPE A
	buf = binary.BigEndian.AppendUint16(buf, 1) // QCLASS IN
	return buf
}

// probeDNS 向 127.0.0.1:<port> 发一次真实 A 查询。
// 判断依据不是"进程在不在"，而是"这个地方到底有没有 DNS 在应答"——
// 因为网卡指向 127.0.0.1 却没有服务时，全机就是断网状态。
func probeDNS(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("udp", addr, time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	id := uint16(rand.Intn(65535))
	if _, err := conn.Write(buildDNSQuery(id, "www.baidu.com")); err != nil {
		return false
	}
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil || n < 12 {
		return false
	}
	return binary.BigEndian.Uint16(buf[:2]) == id
}

func adaptersPointToLoopback() bool {
	adapters := ListAdapters()
	for _, a := range adapters {
		for _, addr := range a.Addresses {
			if strings.HasPrefix(strings.TrimSpace(addr), "127.") {
				return true
			}
		}
	}
	return false
}

// runWatchdog 看门狗入口（由计划任务在开机 / 登录时以最高权限拉起）。
// 语义：只有"网卡还指向 127.0.0.1"且"这个地址上没有 DNS 应答"时才还原，
// 其它任何情况都直接退出 —— 幂等，重复执行无害。
func runWatchdog() int {
	cfg := loadConfig()
	watchdogLog("看门狗启动：监听端口 %d，接管记录=%v", cfg.ListenPort, IsTakenOver())

	// 顺手自愈：助手任务装了但管道连不上（被杀掉/异常退出），看门狗是 SYSTEM，
	// 可以在这里把它重新拉起来，免得用户为了"重启助手"再弹一次 UAC。
	if HelperTaskRegistered() && !HelperRunning() {
		if err := RunHelperTask(); err != nil {
			watchdogLog("后台助手没在跑，重新拉起失败：%v", err)
		} else {
			watchdogLog("后台助手没在跑，已重新拉起计划任务")
		}
	}

	if !IsTakenOver() {
		watchdogLog("没有接管记录，无需还原")
		return 0
	}
	if probeDNS(cfg.ListenPort) {
		watchdogLog("127.0.0.1:%d 有 DNS 应答，核心正常工作，跳过", cfg.ListenPort)
		return 0
	}
	if !adaptersPointToLoopback() {
		watchdogLog("网卡 DNS 已不指向 127.0.0.1，跳过")
		return 0
	}
	if !IsAdmin() {
		watchdogLog("缺少管理员权限，无法还原；请把计划任务设为最高权限运行")
		return 2
	}
	if err := ReleaseDNS(); err != nil {
		watchdogLog("还原系统 DNS 失败：%v", err)
		return 3
	}
	watchdogLog("检测到 127.0.0.1 无 DNS 应答，已还原系统 DNS（异常退出 / 重启兜底）")
	return 0
}

// startupSelfCheck 启动时的轻量诊断：只记日志不擅自动网卡，
// 因为紧接着核心就会启动并接管，此时还原反而会造成抖动。
func startupSelfCheck(cfg *Config) {
	if !IsTakenOver() {
		return
	}
	if probeDNS(cfg.ListenPort) {
		return
	}
	if IsAdmin() {
		watchdogLog("启动自检：网卡仍指向 127.0.0.1 且当前无应答，等待核心启动接管")
	} else {
		watchdogLog("启动自检：网卡仍指向 127.0.0.1，当前无应答；本进程无管理员权限，若核心启动失败需手动还原")
	}
}
