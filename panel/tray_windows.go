package main

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW  = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW   = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow    = modUser32.NewProc("DestroyWindow")
	procGetMessageW      = modUser32.NewProc("GetMessageW")
	procTranslateMessage = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW = modUser32.NewProc("DispatchMessageW")
	procLoadImageW       = modUser32.NewProc("LoadImageW")
	procShellNotifyIconW = modShell32.NewProc("Shell_NotifyIconW")
	procCreatePopupMenu  = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW      = modUser32.NewProc("AppendMenuW")
	procTrackPopupMenuEx = modUser32.NewProc("TrackPopupMenuEx")
	procDestroyMenu      = modUser32.NewProc("DestroyMenu")
	procGetCursorPos     = modUser32.NewProc("GetCursorPos")
	procSetForegroundWin = modUser32.NewProc("SetForegroundWindow")
	procPostMessageW     = modUser32.NewProc("PostMessageW")
	procShowWindow       = modUser32.NewProc("ShowWindow")
	procGetSystemMetrics = modUser32.NewProc("GetSystemMetrics")
	procMessageBoxW      = modUser32.NewProc("MessageBoxW")
	procGetModuleHandleW = modKernel32.NewProc("GetModuleHandleW")
	// RegisterWindowMessageW 用来接 explorer 广播的 "TaskbarCreated"
	procRegisterWindowMsgW = modUser32.NewProc("RegisterWindowMessageW")
)

// msgTaskbarCreated 是资源管理器重启后广播的消息号：收到就得把托盘图标重新加回去，
// 否则任务栏重建一次，图标就永远消失（进程还在跑，但用户找不到入口）。
var msgTaskbarCreated uint32

const (
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmApp           = 0x8000
	trayCallbackID  = wmApp + 1

	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4

	imageIcon     = 1
	lrShared      = 0x00008000
	lrDefaultSize = 0x00000040

	mfString       = 0x0
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	smCXSmIcon = 49
	smCYSmIcon = 50

	swHide = 0
	swShow = 5

	iconOn  = 2
	iconOff = 3

	cmdOpen   = 1001
	cmdToggle = 1002
	cmdQuit   = 1003
)

type point struct {
	X int32
	Y int32
}

type msgStruct struct {
	HWnd     windows.HWND
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type notifyIconData struct {
	CbSize          uint32
	HWnd            windows.HWND
	UID             uint32
	Flags           uint32
	CallbackMessage uint32
	HIcon           windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	TimeoutVersion  uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            [16]byte
	BalloonIcon     windows.Handle
}

type Tray struct {
	mu       sync.Mutex
	hwnd     windows.HWND
	iconOn   windows.Handle
	iconOff  windows.Handle
	paused   bool
	ready    bool
	onOpen   func()
	onToggle func() bool
	onQuit   func()
}

var (
	globalTray   *Tray
	trayProcAddr = syscall.NewCallback(trayWndProc)
)

func NewTray() *Tray {
	return &Tray{}
}

func (t *Tray) SetHandlers(onOpen func(), onToggle func() bool, onQuit func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onOpen = onOpen
	t.onToggle = onToggle
	t.onQuit = onQuit
}

func (t *Tray) Start() {
	globalTray = t
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hInst, _, _ := procGetModuleHandleW.Call(0)
		className, _ := windows.UTF16FromString("ShieldDNSTrayWindow")
		wc := wndClassEx{
			Size:      uint32(unsafe.Sizeof(wndClassEx{})),
			WndProc:   trayProcAddr,
			Instance:  windows.Handle(hInst),
			ClassName: &className[0],
		}
		procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

		title, _ := windows.UTF16FromString("ShieldDNS")
		hwnd, _, _ := procCreateWindowExW.Call(
			0,
			uintptr(unsafe.Pointer(&className[0])),
			uintptr(unsafe.Pointer(&title[0])),
			0, 0, 0, 0, 0,
			0, 0,
			hInst,
		)
		if hwnd == 0 {
			return
		}
		t.mu.Lock()
		t.hwnd = windows.HWND(hwnd)
		t.iconOn = loadIcon(iconOn)
		t.iconOff = loadIcon(iconOff)
		t.mu.Unlock()

		// 先登记 "TaskbarCreated"：之后 explorer 重启时它会广播，我们在窗口过程里补图标
		name, _ := windows.UTF16FromString("TaskbarCreated")
		if id, _, _ := procRegisterWindowMsgW.Call(uintptr(unsafe.Pointer(&name[0]))); id != 0 {
			msgTaskbarCreated = uint32(id)
		}

		t.addIcon()

		var msg msgStruct
		for {
			ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(ret) <= 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}()
}

func loadIcon(id uintptr) windows.Handle {
	hInst, _, _ := procGetModuleHandleW.Call(0)
	cx, _, _ := procGetSystemMetrics.Call(smCXSmIcon)
	cy, _, _ := procGetSystemMetrics.Call(smCYSmIcon)
	h, _, _ := procLoadImageW.Call(hInst, id, imageIcon, cx, cy, lrShared)
	return windows.Handle(h)
}

func (t *Tray) addIcon() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hwnd == 0 {
		return
	}
	nid := notifyIconData{
		CbSize:          uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:            t.hwnd,
		UID:             1,
		Flags:           nifMessage | nifIcon | nifTip,
		CallbackMessage: trayCallbackID,
		HIcon:           t.iconOn,
	}
	setTip(&nid, t.paused)
	if r, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); r == 0 {
		// 返回 FALSE 说明图标根本没加进去（任务栏还没建好、或上一次注册残留）
		panelLog("托盘图标 NIM_ADD 失败")
	} else {
		panelLog("托盘图标已添加")
	}
	t.ready = true
}

func setTip(nid *notifyIconData, paused bool) {
	text := "ShieldDNS · 保护已开启"
	if paused {
		text = "ShieldDNS · 保护已暂停"
	}
	u, _ := windows.UTF16FromString(text)
	copy(nid.Tip[:], u)
}

// SetProtection 更新托盘图标与提示文字
func (t *Tray) SetProtection(paused bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.paused = paused
	if !t.ready {
		return
	}
	icon := t.iconOn
	if paused {
		icon = t.iconOff
	}
	nid := notifyIconData{
		CbSize: uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:   t.hwnd,
		UID:    1,
		Flags:  nifIcon | nifTip,
		HIcon:  icon,
	}
	setTip(&nid, paused)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func (t *Tray) Quit() {
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(uintptr(hwnd), 0x0012, 0, 0) // WM_QUIT
	}
}

func trayWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	t := globalTray
	if t == nil {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}
	// explorer 重启（崩溃、更新、"重启资源管理器"）后会广播 TaskbarCreated，
	// 此时旧的图标注册已经随任务栏一起没了，必须重新 NIM_ADD 一次
	if msgTaskbarCreated != 0 && uint32(msg) == msgTaskbarCreated {
		// 任务栏重建了：旧的注册随 explorer 一起没了，先清一次残留再加回去
		t.mu.Lock()
		hwnd := t.hwnd
		ready := t.ready
		t.mu.Unlock()
		if ready && hwnd != 0 {
			del := notifyIconData{CbSize: uint32(unsafe.Sizeof(notifyIconData{})), HWnd: hwnd, UID: 1}
			procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&del)))
			t.ready = false
		}
		panelLog("收到 TaskbarCreated，重新注册托盘图标")
		t.addIcon()
		return 0
	}
	switch msg {
	case trayCallbackID:
		switch uint32(lp) & 0xFFFF {
		case wmLButtonDblClk, wmLButtonUp:
			t.mu.Lock()
			fn := t.onOpen
			t.mu.Unlock()
			if fn != nil {
				go fn()
			}
		case wmRButtonUp:
			t.showMenu()
		}
		return 0
	case wmCommand:
		id := uint32(wp) & 0xFFFF
		switch id {
		case cmdOpen:
			t.mu.Lock()
			fn := t.onOpen
			t.mu.Unlock()
			if fn != nil {
				go fn()
			}
		case cmdToggle:
			t.mu.Lock()
			fn := t.onToggle
			t.mu.Unlock()
			if fn != nil {
				paused := fn()
				t.SetProtection(paused)
			}
		case cmdQuit:
			t.mu.Lock()
			fn := t.onQuit
			t.mu.Unlock()
			if fn != nil {
				fn()
			}
		}
		return 0
	case wmDestroy:
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func (t *Tray) showMenu() {
	t.mu.Lock()
	hwnd := t.hwnd
	paused := t.paused
	t.mu.Unlock()
	if hwnd == 0 {
		return
	}

	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	appendItem(hMenu, cmdOpen, "打开面板")
	if paused {
		appendItem(hMenu, cmdToggle, "开启保护")
	} else {
		appendItem(hMenu, cmdToggle, "暂停保护")
	}
	appendSeparator(hMenu)
	appendItem(hMenu, cmdQuit, "退出 ShieldDNS")

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWin.Call(uintptr(hwnd))
	cmd, _, _ := procTrackPopupMenuEx.Call(
		hMenu,
		tpmRightButton|tpmReturnCmd,
		uintptr(pt.X), uintptr(pt.Y),
		uintptr(hwnd),
		0,
	)
	procPostMessageW.Call(uintptr(hwnd), 0x0000, 0, 0)

	switch uint32(cmd) & 0xFFFF {
	case cmdOpen:
		t.mu.Lock()
		fn := t.onOpen
		t.mu.Unlock()
		if fn != nil {
			go fn()
		}
	case cmdToggle:
		t.mu.Lock()
		fn := t.onToggle
		t.mu.Unlock()
		if fn != nil {
			t.SetProtection(fn())
		}
	case cmdQuit:
		t.mu.Lock()
		fn := t.onQuit
		t.mu.Unlock()
		if fn != nil {
			fn()
		}
	}
}

func appendItem(menu uintptr, id uint32, label string) {
	text, _ := windows.UTF16FromString(label)
	procAppendMenuW.Call(menu, mfString, uintptr(id), uintptr(unsafe.Pointer(&text[0])))
}

func appendSeparator(menu uintptr) {
	procAppendMenuW.Call(menu, 0x00000800, 0, 0) // MF_SEPARATOR
}

func showMessageBox(title, text string) {
	t, _ := windows.UTF16FromString(title)
	m, _ := windows.UTF16FromString(text)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(&m[0])), uintptr(unsafe.Pointer(&t[0])), 0x40)
}

func init() {
	// 结构体布局与 Windows SDK 对齐校验，避免不同架构下错位导致托盘异常
	if unsafe.Offsetof(notifyIconData{}.HIcon) != 32 ||
		unsafe.Offsetof(notifyIconData{}.Tip) != 40 ||
		unsafe.Offsetof(notifyIconData{}.State) != 296 ||
		unsafe.Offsetof(notifyIconData{}.Info) != 304 ||
		unsafe.Offsetof(notifyIconData{}.InfoTitle) != 820 ||
		unsafe.Offsetof(notifyIconData{}.GUID) != 952 ||
		unsafe.Sizeof(notifyIconData{}) != 976 {
		panic(fmt.Sprintf("NOTIFYICONDATAW 布局不符: size=%d", unsafe.Sizeof(notifyIconData{})))
	}
}
