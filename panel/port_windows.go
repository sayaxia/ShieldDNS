package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// PortOwner 描述"谁把某个端口占住了"，用于面板上的失败提示。
type PortOwner struct {
	Port int    `json:"port"`
	PID  uint32 `json:"pid"`
	Name string `json:"name"`
	Path string `json:"path"`
}

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procTerminateProcess           = kernel32.NewProc("TerminateProcess")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
)

const (
	processTerminate               = 0x0001
	processQueryLimitedInformation = 0x1000
)

// processName 取进程可执行文件名；取不到就返回空串，不阻断主流程。
func processName(pid uint32) (name, path string) {
	h, _, _ := procOpenProcess.Call(uintptr(processQueryLimitedInformation), 0, uintptr(pid))
	if h == 0 {
		return "", ""
	}
	defer procCloseHandle.Call(h)
	var buf [syscall.MAX_PATH * 2]uint16
	size := uint32(len(buf))
	if r, _, _ := procQueryFullProcessImageNameW.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return "", ""
	}
	p := syscall.UTF16ToString(buf[:size])
	if p == "" {
		return "", ""
	}
	return filepath.Base(p), p
}

// netstatExe 用绝对路径：本机 PATH 里没有 System32，裸名会直接 "not recognized"。
func netstatExe() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "netstat.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "netstat.exe"
}

// listListeners 解析 netstat -ano，返回监听指定端口的 PID（UDP/TCP 都算）。
func listListeners(port int) []uint32 {
	out, err := runHidden(netstatExe(), "-ano")
	if err != nil && out == "" {
		return nil
	}
	suffix := ":" + strconv.Itoa(port)
	seen := map[uint32]bool{}
	var pids []uint32
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 4 {
			continue
		}
		proto := strings.ToUpper(fields[0])
		if proto != "UDP" && proto != "TCP" {
			continue
		}
		// 只认"本机监听"：UDP 行没有 State，TCP 行必须是 LISTENING
		if proto == "TCP" && len(fields) >= 5 && strings.ToUpper(fields[3]) != "LISTENING" {
			continue
		}
		if !strings.HasSuffix(fields[1], suffix) {
			continue
		}
		pid, err := strconv.ParseUint(fields[len(fields)-1], 10, 32)
		if err != nil || pid == 0 {
			continue
		}
		if seen[uint32(pid)] {
			continue
		}
		seen[uint32(pid)] = true
		pids = append(pids, uint32(pid))
	}
	return pids
}

// statPort 返回端口占用者；空闲时返回 nil。
// 自己（面板/核心）不算"占用"，否则重启核心时会把自己认成占用者。
func statPort(port int) *PortOwner {
	self := uint32(os.Getpid())
	corePid := uint32(0)
	if p, ok := healthPid(); ok {
		corePid = p
	}
	for _, pid := range listListeners(port) {
		if pid == self || pid == corePid {
			continue
		}
		name, path := processName(pid)
		return &PortOwner{Port: port, PID: pid, Name: name, Path: path}
	}
	return nil
}

// portBindable 用一次真实 bind 判断端口是否可用（比只看 netstat 更准）。
func portBindable(port int) bool {
	if _, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		return false
	}
	return true
}

// criticalProcess 里的进程不是"能一键结束"的对象：
// 结束它们会连带把系统网络栈/会话搞坏，必须让用户自己决定怎么处理。
var criticalProcess = map[string]string{
	"svchost.exe":  "Windows 系统服务（Internet 连接共享 / DNS 客户端 / mDNS 都可能在里面）",
	"system":       "Windows 内核",
	"services.exe": "Windows 服务控制管理器",
	"lsass.exe":    "Windows 安全认证",
	"csrss.exe":    "Windows 会话管理",
	"wininit.exe":  "Windows 初始化进程",
	"winlogon.exe": "Windows 登录进程",
	"smss.exe":     "Windows 会话管理器",
	"dwm.exe":      "桌面窗口管理器",
}

// KillPortOwner 结束占用该端口的进程，等到端口真的空出来为止。
func KillPortOwner(port int) (*PortOwner, error) {
	o := statPort(port)
	if o == nil {
		return nil, nil
	}
	lower := strings.ToLower(o.Name)
	if why, bad := criticalProcess[lower]; bad {
		return o, fmt.Errorf("%s（PID %d）是%s，不能一键结束；请手动在「服务」里停掉相关服务，或换用 53 以外的端口", o.Name, o.PID, why)
	}
	h, _, callErr := procOpenProcess.Call(uintptr(processTerminate|processQueryLimitedInformation), 0, uintptr(o.PID))
	if h == 0 {
		// 打不开通常是权限不够（SYSTEM 级服务），提示用户提权
		msg := "结束进程失败：权限不足"
		if callErr != nil {
			msg = "结束进程失败：" + callErr.Error()
		}
		if !IsAdmin() {
			msg += "（该进程以更高权限运行，请点「以管理员身份重启」后再试）"
		}
		return o, errors.New(msg)
	}
	defer procCloseHandle.Call(h)
	if r, _, _ := procTerminateProcess.Call(h, 1); r == 0 {
		return o, fmt.Errorf("结束进程失败：TerminateProcess 被拒绝（PID %d）", o.PID)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if statPort(port) == nil && portBindable(port) {
			return o, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return o, fmt.Errorf("已结束 %s（PID %d），但 %d 端口仍未释放，请稍后手动重试", o.Name, o.PID, port)
}
