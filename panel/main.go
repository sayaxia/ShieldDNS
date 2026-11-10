package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed web
var webAssets embed.FS

var (
	procCreateMutexW = windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateMutexW")
	mutexHandle      windows.Handle

	// uiReady 在页面画完首帧后关闭：在那之前窗口一直藏着，避免先露出一块白底。
	uiReady     = make(chan struct{})
	uiReadyOnce sync.Once
)

func markUIReady() {
	uiReadyOnce.Do(func() { close(uiReady) })
}

func main() {
	showWindow := flag.Bool("elevated", false, "以管理员身份启动（内部使用）")
	watchdog := flag.Bool("watchdog", false, "看门狗模式：检查并在必要时还原系统 DNS（内部使用）")
	helper := flag.Bool("helper", false, "后台助手模式：以 SYSTEM 常驻，提供免 UAC 的 DNS 接管通道（内部使用）")
	dataDir := flag.String("data", "", "数据目录（看门狗/助手模式内部使用，计划任务以 SYSTEM 运行时必须指定）")
	flag.Parse()
	_ = showWindow

	// 看门狗是无人值守的短命进程，不能碰托盘/窗口，也不该抢单实例互斥体
	if *watchdog {
		// SYSTEM 身份下 %LOCALAPPDATA% 是 systemprofile，读不到用户配置和接管备份，
		// 所以计划任务必须把数据目录显式传进来。
		if *dataDir != "" {
			_ = os.Setenv("SHIELDDNS_DATA_DIR", *dataDir)
		}
		os.Exit(runWatchdog())
	}

	// 后台助手同样是 SYSTEM 下的常驻进程，没有窗口也不碰托盘
	if *helper {
		if *dataDir != "" {
			_ = os.Setenv("SHIELDDNS_DATA_DIR", *dataDir)
		}
		os.Exit(runHelper())
	}

	// 提权重启时，旧实例正在退出、互斥体还没释放，新实例必须给它留时间；
	// 否则就会自己弹"已有实例"，结果两个都退出，谁也起不来。
	wait := time.Duration(0)
	if *showWindow {
		wait = 15 * time.Second
	} else {
		wait = 1500 * time.Millisecond
	}
	acquireSingleInstance(wait)

	// 覆盖正在运行的同名 exe 时，Windows 会把旧镜像改名成 "ShieldDNS.exe~" 留在旁边。
	// 上一次遗留的文件只要当前没被占用就顺手删掉，避免 dist 里堆垃圾。
	// 应用内自动更新还会把旧文件改成 .old 当回滚备份，重启后同样清掉。
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + "~")
		_ = os.Remove(exe + ".old")
		_ = os.Remove(filepath.Join(filepath.Dir(exe), "core", "shielddns-core.exe.old"))
		_ = os.RemoveAll(filepath.Join(DataDir(), "update"))
	}

	cfg := loadConfig()
	startupSelfCheck(cfg)
	// 助手装了但没在跑（被杀 / 上次异常退出）就先自己拉一把；拉不动则等登录触发兜底。
	ensureHelperAlive()
	// 应用启动时保持"待命"状态：不自动开启防护，也不主动接管系统 DNS。
	// 若上次是接管状态（网卡还指向 127.0.0.1），先把 DNS 还原，避免"程序开着但没防护"时全机解析挂在回环上。
	if cfg.ProtectionEnabled {
		cfg.ProtectionEnabled = false
		_ = saveConfig(cfg)
	}
	if IsTakenOver() {
		if IsAdmin() {
			_ = ReleaseDNS()
		} else if HelperRunning() {
			_, _ = helperCall("release", 5*time.Second)
		}
	}
	// 跟随本机 DNS：启动核心之前先把本机网卡 DNS 抓成上游
	// （此时若已处于接管状态，系统 DNS 是 127.0.0.1，需要从接管备份里取原值）
	if cfg.UpstreamMode == "system" {
		if list, src, err := applySystemUpstreams(cfg); err == nil {
			cfg.Upstreams = list
			cfg.UpstreamSource = src
			_ = saveConfig(cfg)
		}
	}
	app := &App{
		cfg:       cfg,
		core:      NewCoreManager(),
		startedAt: time.Now(),
	}

	port, err := startServer(app, cfg.PanelPort)
	if err != nil {
		showMessageBox("ShieldDNS", "面板服务启动失败："+err.Error())
		os.Exit(1)
	}
	app.panelPort = port

	// 核心放到后台启动，失败也不阻塞界面。
	// 端口不做任何自动回退：53 是唯一能被系统解析器命中的端口，换成别的端口只会
	// "看着在跑、实际一条都不拦"。绑不上就留在失败状态，由界面把占用者摊开给用户。
	go func() {
		_ = app.core.Start()
	}()

	tray := NewTray()
	app.tray = tray
	tray.SetHandlers(
		func() {
			if app.win != nil {
				app.win.Show()
			} else {
				openInBrowser(fmt.Sprintf("http://127.0.0.1:%d", port))
			}
		},
		app.toggleProtection,
		func() {
			panelLog("托盘菜单点击「退出应用」")
			app.quit()
		},
	)
	tray.Start()

	go func() {
		for i := 0; i < 60; i++ {
			if st, err := app.fetchStatus(); err == nil {
				tray.SetProtection(!st.Protection)
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
	}()

	// 换了程序路径之后的自愈：开机自启项里存的还是旧路径时直接重写（写 HKCU，不需要管理员）；
	// 后台助手任务还指着旧 exe 时，有管理员权限就顺手重注册一次，否则只记日志、由界面提示。
	go func() {
		if AutoStartEnabled() {
			if err := SetAutoStart(true); err != nil {
				panelLog("开机自启项刷新失败：%v", err)
			}
		}
		time.Sleep(2 * time.Second)
		if !HelperStalePath() {
			return
		}
		if !IsAdmin() {
			panelLog("后台助手任务仍指向旧路径，当前权限无法自动重注册：请以管理员身份重启后点『重启』")
			return
		}
		panelLog("后台助手任务指向旧路径，按当前路径重新注册")
		if err := RegisterHelperTask(); err != nil {
			panelLog("自动重注册后台助手失败：%v", err)
			return
		}
		panelLog("后台助手已切到当前路径并重启")
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	win, err := NewWindow(url, 1)
	if err != nil {
		openInBrowser(url)
		select {}
	}
	app.win = win
	// 窗口此刻是隐藏的：等页面首帧画出来（/panel/ready）再露面；2 秒兜底，
	// 免得页面加载失败时窗口永远不出现。
	go func() {
		select {
		case <-uiReady:
			panelLog("页面首帧完成，显示窗口")
		case <-time.After(2 * time.Second):
			panelLog("等待页面首帧超时（2s），直接显示窗口")
		}
		win.Show()
	}()
	win.Run()
	panelLog("窗口消息循环已结束（收到 WM_QUIT），开始退出")
	app.quit()
}

func startServer(app *App, preferred int) (int, error) {
	handler := app.newMux()
	for p := preferred; p < preferred+15; p++ {
		addr := fmt.Sprintf("127.0.0.1:%d", p)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}
		srv := &http.Server{Handler: handler}
		go func() {
			_ = srv.Serve(ln)
		}()
		return p, nil
	}
	return 0, errors.New("8899 起连续 15 个端口都被占用")
}

// acquireSingleInstance 抢单实例互斥体。
// wait>0 时会在窗口期内反复重试，给"提权重启"留出旧实例退出的时间；
// ERROR_ACCESS_DENIED 也当作"已被（更高权限的）实例占用"，而不是成功。
func acquireSingleInstance(wait time.Duration) {
	name, _ := windows.UTF16FromString("Local\\ShieldDNS-Panel-Mutex")
	deadline := time.Now().Add(wait)
	for {
		h, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(&name[0])))
		errno, _ := err.(syscall.Errno)
		switch {
		case h != 0 && errno != windows.ERROR_ALREADY_EXISTS:
			mutexHandle = windows.Handle(h)
			return
		case errno == windows.ERROR_ALREADY_EXISTS || errno == windows.ERROR_ACCESS_DENIED:
			if time.Now().Before(deadline) {
				time.Sleep(300 * time.Millisecond)
				continue
			}
			showMessageBox("ShieldDNS", "ShieldDNS 已经在运行，请从托盘图标打开面板。")
			os.Exit(0)
		case errno == 0 && h != 0:
			mutexHandle = windows.Handle(h)
			return
		default:
			showMessageBox("ShieldDNS", "单实例初始化失败："+err.Error())
			os.Exit(1)
		}
	}
}

func releaseSingleInstance() {
	if mutexHandle != 0 {
		_ = windows.CloseHandle(mutexHandle)
		mutexHandle = 0
	}
}

func openInBrowser(url string) {
	cmd := exec.Command(sysExe("rundll32.exe"), "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	_ = cmd.Start()
}
