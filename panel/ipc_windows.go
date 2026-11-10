package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 免 UAC 通道：面板以普通权限运行，改网卡 DNS 这类受保护操作交给常驻的 SYSTEM 助手进程。
// 助手由计划任务在开机时拉起（-helper），通过命名管道接收 takeover/release/status 请求。
const (
	helperPipeName   = `\\.\pipe\ShieldDNS-DNS-Helper`
	helperTaskArg    = "-helper"
	pipeAccessDup    = 0x00000003 // PIPE_ACCESS_DUPLEX
	pipeTypeMessage  = 0x00000004 // PIPE_TYPE_MESSAGE
	pipeReadMessage  = 0x00000002 // PIPE_READMODE_MESSAGE
	pipeUnlimited    = 255
	sddlRevision1    = 1
	errorPipeBusy    = 231
	errorPipeConnect = 535
)

var (
	// modKernel32 / modAdvapi32 在 tray_windows.go、dns_windows.go 里已声明，这里复用
	procCreateNamedPipeW    = modKernel32.NewProc("CreateNamedPipeW")
	procConnectNamedPipe    = modKernel32.NewProc("ConnectNamedPipe")
	procDisconnectNamedPipe = modKernel32.NewProc("DisconnectNamedPipe")
	procWaitNamedPipeW      = modKernel32.NewProc("WaitNamedPipeW")
	procConvertStringSD     = modAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
)

type helperRequest struct {
	Action string `json:"action"`
}

type helperResponse struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	TakenOver bool   `json:"takenOver"`
	Detail    string `json:"detail,omitempty"`
}

func helperSIDPath() string {
	return filepath.Join(DataDir(), "helper_user.sid")
}

// CurrentUserSID 取当前登录用户的 SID（形如 S-1-5-21-...），用于给管道 ACL 授最小权限
func CurrentUserSID() string {
	out, err := runHidden(sysExe("whoami.exe"), "/user", "/fo", "csv", "/nh")
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(out)
	parts := strings.Split(line, ",")
	for _, p := range parts {
		v := strings.Trim(strings.TrimSpace(p), `"`)
		if strings.HasPrefix(v, "S-1-5-") {
			return v
		}
	}
	return ""
}

func helperLog(format string, args ...any) {
	dir := filepath.Join(DataDir(), "logs")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "helper.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// createPipe 建一个只允许 SYSTEM / Administrators / 指定用户连接的管道实例
func createPipe(sid string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(helperPipeName)
	if err != nil {
		return 0, err
	}
	// 没有用户 SID 就只放给 SYSTEM + 管理员，避免误放行所有人
	acl := "D:(A;;GA;;;SY)(A;;GA;;;BA)"
	if sid != "" {
		acl = "D:(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;" + sid + ")"
	}
	var sdPtr uintptr
	var sdLen uint32
	sddl, _ := windows.UTF16PtrFromString(acl)
	r, _, cerr := procConvertStringSD.Call(uintptr(unsafe.Pointer(sddl)), sddlRevision1, uintptr(unsafe.Pointer(&sdPtr)), uintptr(unsafe.Pointer(&sdLen)))
	if r == 0 || sdPtr == 0 {
		return 0, errors.New("构造管道安全描述符失败: " + cerr.Error())
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: (*windows.SECURITY_DESCRIPTOR)(unsafe.Pointer(sdPtr)),
		InheritHandle:      0,
	}
	h, _, callErr := procCreateNamedPipeW.Call(
		uintptr(unsafe.Pointer(name)),
		pipeAccessDup,
		pipeTypeMessage|pipeReadMessage|0, // PIPE_WAIT = 0
		pipeUnlimited,
		4096,
		4096,
		0,
		uintptr(unsafe.Pointer(sa)),
	)
	errno, _ := callErr.(syscall.Errno)
	if h == 0 || (errno != 0 && errno != errorPipeConnect) {
		return 0, callErr
	}
	_ = sdLen
	return windows.Handle(h), nil
}

func pipeWrite(h windows.Handle, payload []byte) error {
	var n uint32
	return syscall.WriteFile(syscall.Handle(uintptr(h)), payload, &n, nil)
}

func pipeRead(h windows.Handle) ([]byte, error) {
	buf := make([]byte, 4096)
	var n uint32
	if err := syscall.ReadFile(syscall.Handle(uintptr(h)), buf, &n, nil); err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// handleHelperRequest 助手侧的实际动作：这里直接复用面板里的接管/还原实现（SYSTEM 身份天然有管理员令牌）
func handleHelperRequest(req helperRequest) helperResponse {
	resp := helperResponse{TakenOver: IsTakenOver()}
	switch req.Action {
	case "ping":
		resp.OK = true
		resp.Detail = "helper alive"
	case "status":
		resp.OK = true
		adapters := ListAdapters()
		names := []string{}
		for _, a := range adapters {
			names = append(names, fmt.Sprintf("%s=%s", a.Alias, strings.Join(a.Addresses, "/")))
		}
		resp.Detail = strings.Join(names, "; ")
	case "takeover":
		if err := TakeoverDNS(); err != nil {
			resp.Error = err.Error()
			return resp
		}
		resp.OK = true
		resp.TakenOver = true
		resp.Detail = "系统 DNS 已指向 127.0.0.1"
	case "release":
		if err := ReleaseDNS(); err != nil {
			resp.Error = err.Error()
			return resp
		}
		resp.OK = true
		resp.TakenOver = false
		resp.Detail = "系统 DNS 已还原"
	default:
		resp.Error = "未知动作: " + req.Action
	}
	return resp
}

// runHelper 常驻循环：一次服务一个连接，处理完断开再建下一个实例
func runHelper() int {
	if !IsAdmin() {
		helperLog("后台助手不是以管理员/SYSTEM 身份运行，拒绝启动")
		return 2
	}
	sid, _ := os.ReadFile(helperSIDPath())
	allowed := strings.TrimSpace(string(sid))
	if allowed == "" {
		allowed = CurrentUserSID()
		helperLog("没有找到授权用户 SID 文件，按当前用户 %s 放行", allowed)
	}
	helperLog("后台助手启动，数据目录 %s，授权用户 %s", DataDir(), allowed)
	go helperGuard()
	for {
		h, err := createPipe(allowed)
		if err != nil {
			// 已经有实例在跑（maxInstances 已满）就安静退出
			if errors.Is(err, syscall.Errno(errorPipeBusy)) || strings.Contains(err.Error(), fmt.Sprint(errorPipeBusy)) {
				helperLog("已有助手实例在运行，本次退出")
				return 0
			}
			helperLog("创建管道失败: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		connected, _, callErr := procConnectNamedPipe.Call(uintptr(h), 0)
		errno, _ := callErr.(syscall.Errno)
		if connected == 0 && errno != errorPipeConnect && errno != 0 {
			if !errors.Is(errno, syscall.Errno(errorPipeConnect)) {
				helperLog("等待连接失败: %v", errno)
				_ = windows.CloseHandle(h)
				continue
			}
		}
		if data, err := pipeRead(h); err == nil && len(data) > 0 {
			var req helperRequest
			resp := helperResponse{}
			if err := json.Unmarshal(data, &req); err != nil {
				resp.Error = "请求格式错误"
			} else {
				resp = handleHelperRequest(req)
				helperLog("动作 %s -> ok=%v %s %s", req.Action, resp.OK, resp.Error, resp.Detail)
			}
			out, _ := json.Marshal(resp)
			_ = pipeWrite(h, out)
		}
		_, _, _ = procDisconnectNamedPipe.Call(uintptr(h))
		_ = windows.CloseHandle(h)
	}
}

// helperCall 面板侧调用：管道不在（助手没跑）时立刻返回错误，不阻塞界面
func helperCall(action string, timeout time.Duration) (helperResponse, error) {
	var zero helperResponse
	name, _ := windows.UTF16PtrFromString(helperPipeName)
	waitMs := uint32(timeout / time.Millisecond)
	if waitMs == 0 {
		waitMs = 500
	}
	if r, _, _ := procWaitNamedPipeW.Call(uintptr(unsafe.Pointer(name)), uintptr(waitMs)); r == 0 {
		return zero, errors.New("后台助手未运行")
	}
	h, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return zero, errors.New("连接后台助手失败：" + err.Error())
	}
	defer windows.CloseHandle(h)
	req, _ := json.Marshal(helperRequest{Action: action})
	if err := pipeWrite(h, req); err != nil {
		return zero, err
	}
	data, err := pipeRead(h)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, syscall.ERROR_BROKEN_PIPE) {
			return zero, errors.New("后台助手连接中断")
		}
		return zero, err
	}
	var resp helperResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return zero, errors.New("后台助手返回无法解析")
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "后台助手执行失败"
		}
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// HelperRunning 面板用来判断"免 UAC 接管"是否可用
func HelperRunning() bool {
	_, err := helperCall("ping", 700*time.Millisecond)
	return err == nil
}

// takeoverSystemDNS 优先自己干（面板已提权时），否则交给常驻的 SYSTEM 助手——两种路径对调用方无差别
func takeoverSystemDNS() error {
	if IsAdmin() {
		return TakeoverDNS()
	}
	if _, err := helperCall("takeover", 10*time.Second); err != nil {
		return errors.New("接管系统 DNS 需要管理员权限，而后台助手未运行：请到设置页点『安装后台助手』（只需一次 UAC）。原始错误：" + err.Error())
	}
	return nil
}

func releaseSystemDNS() error {
	if IsAdmin() {
		return ReleaseDNS()
	}
	if _, err := helperCall("release", 10*time.Second); err != nil {
		return errors.New("还原系统 DNS 需要管理员权限，而后台助手未运行：请到设置页点『安装后台助手』（只需一次 UAC）。原始错误：" + err.Error())
	}
	return nil
}
