package main

import (
	"errors"
	"sync"
	"syscall"

	"github.com/jchv/go-webview2"
)

var (
	procSetWindowLongPtrW = modUser32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW   = modUser32.NewProc("CallWindowProcW")
)

// Window 包装 go-webview2 的窗口，并接管关闭行为（关闭时收进托盘而不是退出）
type Window struct {
	wv      webview2.WebView
	hwnd    uintptr
	oldProc uintptr
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
	}
	r, _, _ := procCallWindowProcW.Call(winCtx.old, hwnd, msg, wp, lp)
	return r
}

func NewWindow(url string, iconID uint) (*Window, error) {
	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  DataDir() + "\\webview2",
		WindowOptions: webview2.WindowOptions{
			Title:  "ShieldDNS · DNS 广告拦截",
			Width:  1220,
			Height: 820,
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
	procShowWindow.Call(hwnd, 5) // SW_SHOW
	procShowWindow.Call(hwnd, 9) // SW_RESTORE
	procSetForegroundWin.Call(hwnd)
}

func (w *Window) Destroy() {
	w.wv.Dispatch(func() {
		w.wv.Destroy()
	})
}
