package main

import (
	"errors"
	"math"
	"sync"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2"
)

var (
	procSetWindowLongPtrW    = modUser32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW      = modUser32.NewProc("CallWindowProcW")
	procGetDpiForWindow      = modUser32.NewProc("GetDpiForWindow")
	procSetWindowPos         = modUser32.NewProc("SetWindowPos")
	procSystemParametersInfo = modUser32.NewProc("SystemParametersInfoW")
)

const (
	wmDpiChanged = 0x02E0 // WM_DPICHANGED

	spiGetWorkArea = 0x0030
	swpNoMove      = 0x0002
	swpNoZOrder    = 0x0004
	swpNoActivate  = 0x0010

	// 设计稿的逻辑尺寸（CSS 像素），实际窗口按当前 DPI 换算成物理像素
	baseLogicalWidth  = 1220
	baseLogicalHeight = 820
)

// winRect 对应 Win32 RECT（物理像素）
type winRect struct {
	Left, Top, Right, Bottom int32
}

// Window 包装 go-webview2 的窗口，并接管关闭行为（关闭时收进托盘而不是退出）
type Window struct {
	wv      webview2.WebView
	hwnd    uintptr
	oldProc uintptr
	fitOnce sync.Once
	mu      sync.Mutex
}

var mainWindow *Window

type windowCtx struct {
	old uintptr
}

var (
	wndProcAddr = syscall.NewCallback(mainWndProc)
	winCtx      windowCtx
)

func mainWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmClose:
		procShowWindow.Call(hwnd, swHide)
		return 0
	case wmDpiChanged:
		// 窗口被拖到另一种缩放比例的显示器（或首次拿到 DPI）：按新的 DPI 重算尺寸，
		// 保持页面看到的「逻辑尺寸」不变，位置仍在当前显示器内。
		fitWindowToDPI(hwnd, false)
		return 0
	}
	r, _, _ := procCallWindowProcW.Call(winCtx.old, hwnd, msg, wp, lp)
	return r
}

func windowDPI(hwnd uintptr) uint32 {
	if hwnd != 0 {
		if dpi, _, _ := procGetDpiForWindow.Call(hwnd); dpi != 0 {
			return uint32(dpi)
		}
	}
	return 96
}

// fitWindowToDPI 按当前显示器缩放比例反算窗口的物理尺寸。
// CreateWindow/SetWindowPos 收的是物理像素，而页面布局用的是 CSS 像素：
// 150% 缩放下如果还按 1220×820 物理像素建窗，WebView2 就只有 813×546 个 CSS 像素，
// 侧栏、卡片、表格全被挤变形。这里统一按 dpi/96 放大，并对小屏做收敛。
// center=true 时按工作区居中（首次显示）；false 时只改尺寸、保持左上角（跨屏拖动时用）。
func fitWindowToDPI(hwnd uintptr, center bool) {
	if hwnd == 0 {
		return
	}
	dpi := windowDPI(hwnd)
	scale := float64(dpi) / 96.0
	w := int(math.Round(float64(baseLogicalWidth) * scale))
	h := int(math.Round(float64(baseLogicalHeight) * scale))

	var wa winRect
	if ok, _, _ := procSystemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0); ok != 0 {
		workW := int(wa.Right - wa.Left)
		workH := int(wa.Bottom - wa.Top)
		maxW := int(float64(workW) * 0.92)
		maxH := int(float64(workH) * 0.92)
		if maxW > 0 && w > maxW {
			w = maxW
		}
		if maxH > 0 && h > maxH {
			h = maxH
		}
		if center {
			x := int(wa.Left) + (workW-w)/2
			y := int(wa.Top) + (workH-h)/2
			procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
				swpNoZOrder|swpNoActivate)
		} else {
			procSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(w), uintptr(h),
				swpNoMove|swpNoZOrder|swpNoActivate)
		}
		panelLog("窗口按 DPI 适配：dpi=%d 目标=%dx%d 工作区=%dx%d", dpi, w, h, workW, workH)
		return
	}
	procSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(w), uintptr(h),
		swpNoMove|swpNoZOrder|swpNoActivate)
	panelLog("窗口按 DPI 适配（无工作区信息）：dpi=%d 目标=%dx%d", dpi, w, h)
}

func NewWindow(url string, iconID uint) (*Window, error) {
	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  DataDir() + "\\webview2",
		WindowOptions: webview2.WindowOptions{
			Title:  "ShieldDNS · DNS 广告拦截",
			Width:  baseLogicalWidth,
			Height: baseLogicalHeight,
			IconId: iconID,
			Center: true,
		},
	})
	if wv == nil {
		return nil, errors.New("WebView2 初始化失败（可能未安装 WebView2 运行时）")
	}
	win := &Window{wv: wv, hwnd: uintptr(wv.Window())}
	if win.hwnd != 0 {
		old, _, _ := procSetWindowLongPtrW.Call(win.hwnd, ^uintptr(3), wndProcAddr) // GWLP_WNDPROC = -4
		winCtx.old = old
		win.oldProc = old
		// 建窗用的是物理像素，这里按 DPI 校正到设计稿的逻辑尺寸
		fitWindowToDPI(win.hwnd, true)
	}
	wv.Navigate(url)
	return win, nil
}

func (w *Window) Run() {
	w.wv.Run()
}

func (w *Window) Show() {
	w.mu.Lock()
	hwnd := w.hwnd
	w.mu.Unlock()
	if hwnd == 0 {
		return
	}
	// 首次显示时再校一次：建窗那一刻窗口还没真正落到显示器上，
	// GetDpiForWindow 可能还报 96，这里拿到的才是真实比例。
	w.fitOnce.Do(func() {
		fitWindowToDPI(hwnd, true)
	})
	procShowWindow.Call(hwnd, 5) // SW_SHOW
	procShowWindow.Call(hwnd, 9) // SW_RESTORE
	procSetForegroundWin.Call(hwnd)
}

func (w *Window) Destroy() {
	w.wv.Dispatch(func() {
		w.wv.Destroy()
	})
}
