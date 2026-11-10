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
	procGetWindowRect        = modUser32.NewProc("GetWindowRect")
)

const (
	wmGetMinMaxInfo = 0x0024 // WM_GETMINMAXINFO
	wmSizing        = 0x0214 // WM_SIZING
	wmExitSizeMove  = 0x0232 // WM_EXITSIZEMOVE
	wmDpiChanged    = 0x02E0 // WM_DPICHANGED

	spiGetWorkArea = 0x0030
	swpNoMove      = 0x0002
	swpNoZOrder    = 0x0004
	swpNoActivate  = 0x0010

	// 常规窗口的逻辑尺寸（CSS 像素）：960×720（4:3），概览页可完整显示且留白合适
	baseLogicalWidth  = 960
	baseLogicalHeight = 720

	// 最小逻辑宽度：再往下整页等比缩得太小，字号开始难认
	minLogicalWidth = 790
)

// 锁定宽高比：无论怎么拖动窗口，都保持设计稿的比例
const windowAspect = float64(baseLogicalWidth) / float64(baseLogicalHeight)

// winRect 对应 Win32 RECT（物理像素）
type winRect struct {
	Left, Top, Right, Bottom int32
}

// winPoint 对应 Win32 POINT
type winPoint struct {
	X, Y int32
}

// minMaxInfo 对应 Win32 MINMAXINFO
type minMaxInfo struct {
	PtReserved     winPoint
	PtMaxSize      winPoint
	PtMaxPosition  winPoint
	PtMinTrackSize winPoint
	PtMaxTrackSize winPoint
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
	mmiLogOnce  sync.Once
)

func mainWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmClose:
		procShowWindow.Call(hwnd, swHide)
		return 0
	case wmGetMinMaxInfo:
		// 先让默认处理把 ptMaxSize/ptMaxTrackSize 等字段填好，只覆盖最小尺寸，
		// 否则未初始化的字段会被系统当成限制，把窗口莫名压小。
		def, _, _ := procCallWindowProcW.Call(winCtx.old, hwnd, msg, wp, lp)
		if lp != 0 {
			mmi := (*minMaxInfo)(unsafe.Pointer(lp))
			minW, minH := minTrackSize(hwnd)
			mmi.PtMinTrackSize.X = minW
			mmi.PtMinTrackSize.Y = minH
			mmiLogOnce.Do(func() {
				panelLog("最小窗口尺寸：dpi=%d %dx%d（默认最大 %dx%d）",
					windowDPI(hwnd), minW, minH, mmi.PtMaxTrackSize.X, mmi.PtMaxTrackSize.Y)
			})
		}
		return def
	case wmSizing:
		// 拖动过程中实时把矩形掰回设计稿的宽高比
		if lp != 0 {
			var cur winRect
			_, _, _ = procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&cur)))
			minW, minH := minTrackSize(hwnd)
			r := (*winRect)(unsafe.Pointer(lp))
			*r = fitRectToAspect(*r, wp, cur.Right-cur.Left, cur.Bottom-cur.Top, minW, minH)
		}
		return 1 // TRUE：告诉系统使用改过的矩形
	case wmExitSizeMove:
		// 拖动结束后再兜一次底（也覆盖程序内部的 SetWindowPos）
		enforceWindowConstraints(hwnd)
		return 0
	case wmDpiChanged:
		// 缩放比例变了（拖到另一块屏，或系统改缩放）：按新 DPI 重算物理尺寸
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

func windowScale(hwnd uintptr) float64 {
	return float64(windowDPI(hwnd)) / 96.0
}

// minTrackSize 返回当前缩放下的最小窗口尺寸（物理像素）
func minTrackSize(hwnd uintptr) (int32, int32) {
	scale := windowScale(hwnd)
	return int32(math.Round(float64(minLogicalWidth) * scale)),
		int32(math.Round(float64(minLogicalWidth) / windowAspect * scale))
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// fitRectToAspect 把任意矩形改造成设计稿比例。
// edge 是 WM_SIZING 的 wParam（1=左 2=右 3=上 4=下 5=左上 6=右上 7=左下 8=右下，0=不定）；
// curW/curH 是当前窗口尺寸，用来判断这次是横向还是纵向拖动；
// minW/minH 是最小尺寸（物理像素）。
func fitRectToAspect(r winRect, edge uintptr, curW, curH, minW, minH int32) winRect {
	w := r.Right - r.Left
	h := r.Bottom - r.Top
	if w <= 0 || h <= 0 {
		return r
	}

	widthDriven := true
	switch edge {
	case 1, 2:
		widthDriven = true
	case 3, 4:
		widthDriven = false
	default:
		// 角落拖动：变化更大的那条边说了算
		widthDriven = abs32(w-curW) >= abs32(h-curH)
	}

	if widthDriven {
		h = int32(math.Round(float64(w) / windowAspect))
	} else {
		w = int32(math.Round(float64(h) * windowAspect))
	}

	if w < minW {
		w = minW
		h = int32(math.Round(float64(w) / windowAspect))
	}
	if h < minH {
		h = minH
		w = int32(math.Round(float64(h) * windowAspect))
	}

	// 按拖动的那条边做锚定，窗口不会“跳”
	leftEdge := edge == 1 || edge == 5 || edge == 7
	topEdge := edge == 3 || edge == 5 || edge == 6
	if leftEdge {
		r.Left = r.Right - w
	} else {
		r.Right = r.Left + w
	}
	if topEdge {
		r.Top = r.Bottom - h
	} else {
		r.Bottom = r.Top + h
	}
	return r
}

// enforceWindowConstraints 在当前尺寸上补一次比例与最小尺寸的约束
func enforceWindowConstraints(hwnd uintptr) {
	var r winRect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return
	}
	minW, minH := minTrackSize(hwnd)
	fixed := fitRectToAspect(r, 0, r.Right-r.Left, r.Bottom-r.Top, minW, minH)
	if fixed == r {
		return
	}
	panelLog("窗口尺寸校正：dpi=%d 最小=%dx%d %dx%d -> %dx%d",
		windowDPI(hwnd), minW, minH, r.Right-r.Left, r.Bottom-r.Top, fixed.Right-fixed.Left, fixed.Bottom-fixed.Top)
	procSetWindowPos.Call(hwnd, 0,
		uintptr(fixed.Left), uintptr(fixed.Top),
		uintptr(fixed.Right-fixed.Left), uintptr(fixed.Bottom-fixed.Top),
		swpNoZOrder|swpNoActivate)
}

// fitWindowToDPI 按当前显示器缩放比例反算窗口的物理尺寸。
// CreateWindow/SetWindowPos 收的是物理像素，而页面布局用的是 CSS 像素：
// 175% 缩放下如果还按 1220×820 物理像素建窗，WebView2 只有约 697×469 个 CSS 像素，
// 侧栏、卡片、表格全被挤变形。这里按 dpi/96 放大，并整体收敛到工作区 92% 以内
// （收敛时两轴等比缩，比例不会走样）。
// center=true 时居中（首次显示）；false 时只改尺寸、保持左上角（跨屏拖动时用）。
func fitWindowToDPI(hwnd uintptr, center bool) {
	if hwnd == 0 {
		return
	}
	scale := windowScale(hwnd)
	w := float64(baseLogicalWidth) * scale
	h := float64(baseLogicalHeight) * scale

	var wa winRect
	if ok, _, _ := procSystemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0); ok != 0 {
		workW := float64(wa.Right - wa.Left)
		workH := float64(wa.Bottom - wa.Top)
		if workW > 0 && workH > 0 {
			factor := math.Min(1.0, math.Min(workW*0.92/w, workH*0.92/h))
			w *= factor
			h *= factor
		}
		tw := int(math.Round(w))
		th := int(math.Round(h))
		if center {
			x := int(wa.Left) + (int(workW)-tw)/2
			y := int(wa.Top) + (int(workH)-th)/2
			procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(tw), uintptr(th),
				swpNoZOrder|swpNoActivate)
		} else {
			procSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(tw), uintptr(th),
				swpNoMove|swpNoZOrder|swpNoActivate)
		}
		panelLog("窗口按 DPI 适配：dpi=%d 尺寸=%dx%d 工作区=%.0fx%.0f", windowDPI(hwnd), tw, th, workW, workH)
		return
	}
	tw := int(math.Round(w))
	th := int(math.Round(h))
	procSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(tw), uintptr(th),
		swpNoMove|swpNoZOrder|swpNoActivate)
	panelLog("窗口按 DPI 适配（无工作区信息）：dpi=%d 尺寸=%dx%d", windowDPI(hwnd), tw, th)
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
		// 建窗用的是物理像素，这里按 DPI 校正成设计稿的逻辑尺寸
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
	// 首次显示时再校一次：建窗那一刻窗口还没真正落到显示器上，GetDpiForWindow 可能还报 96
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
