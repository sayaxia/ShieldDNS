package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type App struct {
	cfgMu      sync.Mutex
	cfg        *Config
	core       *CoreManager
	tray       *Tray
	win        *Window
	mu         sync.RWMutex
	protection bool
	startedAt  time.Time
	panelPort  int
}

func (a *App) config() *Config {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return a.cfg
}

func (a *App) reloadConfig() *Config {
	cfg := loadConfig()
	a.cfgMu.Lock()
	a.cfg = cfg
	a.cfgMu.Unlock()
	return cfg
}

func (a *App) coreURL() string {
	cfg := loadConfig()
	return fmt.Sprintf("http://127.0.0.1:%d", cfg.ControlPort)
}

type coreStatus struct {
	Protection bool `json:"protection"`
	Queries    struct {
		Total    int64 `json:"total"`
		Blocked  int64 `json:"blocked"`
		Allowed  int64 `json:"allowed"`
		CacheHit int64 `json:"cache_hits"`
	} `json:"queries"`
	RulesTotal int `json:"rules_total"`
}

func (a *App) fetchStatus() (*coreStatus, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(a.coreURL() + "/api/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("核心返回 HTTP %d", resp.StatusCode)
	}
	var st coreStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.protection = st.Protection
	a.mu.Unlock()
	return &st, nil
}

func (a *App) callCore(method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, a.coreURL()+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

func (a *App) setProtection(enabled bool) error {
	_, code, err := a.callCore("POST", "/api/protection", map[string]bool{"enabled": enabled})
	if err != nil {
		return err
	}
	if code >= 400 {
		return fmt.Errorf("核心拒绝请求（HTTP %d）", code)
	}
	a.mu.Lock()
	a.protection = enabled
	a.mu.Unlock()
	if a.tray != nil {
		a.tray.SetProtection(!enabled)
	}
	return nil
}

// toggleProtection 供托盘调用，返回切换后的“暂停”状态
func (a *App) toggleProtection() bool {
	st, err := a.fetchStatus()
	if err != nil {
		return true
	}
	newEnabled := !st.Protection
	if err := a.setProtection(newEnabled); err != nil {
		return !newEnabled
	}
	return !newEnabled
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *App) newMux() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/panel/ready", func(w http.ResponseWriter, r *http.Request) {
		// 页面画完首帧后打这个点，主程序收到才把窗口显示出来
		panelLog("收到页面首帧信号")
		markUIReady()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		cfg := loadConfig()
		target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", cfg.ControlPort))
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": "DNS 核心未运行：" + err.Error(),
			})
		}
		proxy.ServeHTTP(w, r)
	})

	mux.HandleFunc("/system/info", a.handleInfo)
	mux.HandleFunc("/system/protection", a.handleProtection)
	mux.HandleFunc("/system/settings", a.handleSettings)
	mux.HandleFunc("/system/upstreams", a.handleUpstreams)
	mux.HandleFunc("/system/helper", a.handleHelper)
	mux.HandleFunc("/system/autostart", a.handleAutoStart)
	mux.HandleFunc("/system/port", a.handlePort)
	mux.HandleFunc("/system/update", a.handleUpdate)
	mux.HandleFunc("/system/openrelease", a.handleOpenRelease)
	mux.HandleFunc("/system/elevate", a.handleElevate)
	mux.HandleFunc("/system/restart", a.handleRestart)
	mux.HandleFunc("/system/quit", a.handleQuit)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		data, err := webAssets.ReadFile("web/" + path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		switch {
		case strings.HasSuffix(path, ".html"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case strings.HasSuffix(path, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case strings.HasSuffix(path, ".js"):
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		case strings.HasSuffix(path, ".png"):
			w.Header().Set("Content-Type", "image/png")
		case strings.HasSuffix(path, ".svg"):
			w.Header().Set("Content-Type", "image/svg+xml")
		}
		_, _ = w.Write(data)
	})

	return mux
}

func (a *App) handleInfo(w http.ResponseWriter, r *http.Request) {
	cfg := loadConfig()
	adapters := ListAdapters()
	owner := statPort(cfg.ListenPort)
	info := map[string]any{
		"admin":              IsAdmin(),
		"adminCapable":       IsAdminCapable(),
		"coreRunning":        a.core.Running(),
		"corePath":           a.core.ExePath(),
		"coreError":          a.core.LastError(),
		"dataDir":            DataDir(),
		"configPath":         ConfigPath(),
		"version":            appVersion,
		"webview":            a.win != nil,
		"panelPort":          a.panelPort,
		"dnsMode":            cfg.DNSMode,
		"listenPort":         cfg.ListenPort,
		"controlPort":        cfg.ControlPort,
		"blockMode":          cfg.BlockMode,
		"takenOver":          IsTakenOver(),
		"adapters":           adapters,
		"uptime":             int64(time.Since(a.startedAt).Seconds()),
		"portOwner":          owner,
		"portBlocked":        owner != nil,
		"upstreamMode":       cfg.UpstreamMode,
		"upstreams":          cfg.Upstreams,
		"customUpstreams":    cfg.CustomUpstreams,
		"upstreamSource":     cfg.UpstreamSource,
		"upstreamCapturedAt": cfg.UpstreamCapturedAt,
		"autoStartEnabled":   AutoStartEnabled(),
		"helperInstalled":    HelperTaskRegistered(),
		"helperRunning":      HelperRunning(),
		"helperStale":        HelperStalePath(),
	}
	writeJSON(w, 200, info)
}

// handlePort：查询端口占用者，或「一键结束占用进程并重试」。
// 只做两件事——告诉用户是谁占着，以及在用户明确点按钮后结束它并重拉核心。
func (a *App) handlePort(w http.ResponseWriter, r *http.Request) {
	port := loadConfig().ListenPort
	if v := r.URL.Query().Get("port"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			port = p
		}
	}
	switch r.Method {
	case http.MethodGet:
		owner := statPort(port)
		writeJSON(w, 200, map[string]any{
			"port":        port,
			"blocked":     owner != nil,
			"owner":       owner,
			"coreRunning": a.core.Running(),
			"coreError":   a.core.LastError(),
		})
	case http.MethodPost:
		var req struct {
			Action string `json:"action"`
			Port   int    `json:"port"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Port > 0 && req.Port < 65536 {
			port = req.Port
		}
		var owner *PortOwner
		switch req.Action {
		case "kill", "":
			var err error
			owner, err = KillPortOwner(port)
			if err != nil {
				writeJSON(w, 500, map[string]any{"error": err.Error(), "portOwner": owner})
				return
			}
		case "retry", "restart":
			owner = statPort(port)
		default:
			writeJSON(w, 400, map[string]string{"error": "不支持的操作"})
			return
		}
		if owner != nil {
			writeJSON(w, 409, map[string]any{
				"error":     fmt.Sprintf("端口 %d 仍被 %s（PID %d）占用", port, owner.Name, owner.PID),
				"portOwner": owner,
			})
			return
		}
		a.core.Stop()
		if err := a.core.Start(); err != nil {
			writeJSON(w, 500, map[string]any{
				"error":     "端口已释放，但核心仍启动失败：" + err.Error(),
				"coreError": a.core.LastError(),
				"portOwner": statPort(port),
			})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "port": port, "portOwner": owner})
	default:
		writeJSON(w, 405, map[string]string{"error": "仅支持 GET / POST"})
	}
}

func (a *App) handleProtection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "仅支持 POST"})
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "参数错误"})
		return
	}
	if req.Enabled {
		// 开防护前先确认端口是自己的：53 被别的程序占着时核心根本没在监听，
		// 继续往下走只会得到一个"连不上核心"的模糊错误。
		pre := loadConfig()
		if owner := statPort(pre.ListenPort); owner != nil {
			writeJSON(w, 409, map[string]any{
				"error":     fmt.Sprintf("端口 %d 被 %s（PID %d）占用，核心无法监听，防护未开启", pre.ListenPort, owner.Name, owner.PID),
				"portOwner": owner,
				"enabled":   false,
			})
			return
		}
		if !a.core.Running() {
			a.core.Stop()
			if err := a.core.Start(); err != nil {
				writeJSON(w, 500, map[string]any{
					"error":     "核心启动失败：" + err.Error(),
					"coreError": a.core.LastError(),
					"portOwner": statPort(pre.ListenPort),
					"enabled":   false,
				})
				return
			}
		}
	}
	if err := a.setProtection(req.Enabled); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error(), "enabled": !req.Enabled})
		return
	}
	cfg := loadConfig()
	// 端口 53 就等同于"接管本机 DNS"，不再需要用户在界面上挑模式
	if cfg.ListenPort == 53 && !req.Enabled && IsTakenOver() {
		// 关防护就把网卡 DNS 还回去，避免"程序还开着但已不拦截，解析却还挂在 127.0.0.1 上"
		_ = releaseSystemDNS()
	}
	if cfg.ListenPort == 53 && req.Enabled && !IsTakenOver() {
		// 已经接管过就不要再写一次，否则会把 127.0.0.1 当成原始 DNS 记进备份
		if err := takeoverSystemDNS(); err != nil {
			writeJSON(w, 500, map[string]any{"error": "防护已开，但接管系统 DNS 失败：" + err.Error(), "enabled": true})
			return
		}
	}
	writeJSON(w, 200, map[string]any{"enabled": req.Enabled})
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "仅支持 POST"})
		return
	}
	var req struct {
		ListenPort   int      `json:"listen_port"`
		DNSMode      string   `json:"dns_mode"`
		BlockMode    string   `json:"block_mode"`
		Upstreams    []string `json:"upstreams"`
		UpstreamMode string   `json:"upstream_mode"`
		CacheEnabled bool     `json:"cache_enabled"`
		LogQueries   bool     `json:"log_queries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "参数错误"})
		return
	}
	if req.UpstreamMode != "" && req.UpstreamMode != "custom" && req.UpstreamMode != "system" {
		writeJSON(w, 400, map[string]string{"error": "上游模式不合法"})
		return
	}
	if req.ListenPort < 1 || req.ListenPort > 65535 {
		writeJSON(w, 400, map[string]string{"error": "端口范围应为 1-65535"})
		return
	}
	if req.BlockMode != "nxdomain" && req.BlockMode != "zero" {
		writeJSON(w, 400, map[string]string{"error": "拦截响应模式不合法"})
		return
	}

	// 目标端口被别人占着就别保存：保存了也起不来，还会把用户上一份好配置冲掉。
	if owner := statPort(req.ListenPort); owner != nil {
		writeJSON(w, 409, map[string]any{
			"error":     fmt.Sprintf("端口 %d 被 %s（PID %d）占用，无法切换到该端口", req.ListenPort, owner.Name, owner.PID),
			"portOwner": owner,
		})
		return
	}

	cfg := loadConfig()
	cfg.ListenPort = req.ListenPort
	// 模式由端口决定：53 = 接管本机，其它 = 仅本机定向
	if cfg.ListenPort == 53 {
		cfg.DNSMode = "system"
	} else {
		cfg.DNSMode = "custom"
	}
	cfg.BlockMode = req.BlockMode
	cfg.CacheEnabled = req.CacheEnabled
	cfg.LogQueries = req.LogQueries
	if req.UpstreamMode != "" {
		cfg.UpstreamMode = req.UpstreamMode
	}
	if len(req.Upstreams) > 0 {
		cfg.CustomUpstreams = req.Upstreams
		if cfg.UpstreamMode == "custom" {
			cfg.Upstreams = req.Upstreams
		}
	}
	// 跟随本机 DNS：必须在还原/接管系统 DNS 之前抓，此时系统 DNS 还是真实值
	// （若已接管，则从 dns_backup.json 里的原值取）
	upstreamNote := ""
	if cfg.UpstreamMode == "system" {
		list, src, err := applySystemUpstreams(cfg)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": "读取本机 DNS 失败：" + err.Error()})
			return
		}
		upstreamNote = src
		cfg.Upstreams = list
	}
	if err := saveConfig(cfg); err != nil {
		writeJSON(w, 500, map[string]string{"error": "保存配置失败：" + err.Error()})
		return
	}

	// 切端口时：新端口不是 53 就先还原旧 DNS，再重启核心
	wasTakenOver := IsTakenOver()
	if wasTakenOver && req.ListenPort != 53 {
		if err := releaseSystemDNS(); err != nil {
			writeJSON(w, 500, map[string]string{"error": "还原系统 DNS 失败：" + err.Error()})
			return
		}
	}

	if err := a.core.Restart(); err != nil {
		a.reloadConfig()
		writeJSON(w, 500, map[string]any{"error": err.Error(), "coreError": a.core.LastError()})
		return
	}
	a.reloadConfig()

	// 端口是 53 且防护在开 → 自动接管，用户不需要再点任何东西
	takeoverNote := ""
	if cfg.ListenPort == 53 {
		if st, err := a.fetchStatus(); err == nil && st.Protection && !IsTakenOver() {
			if err := takeoverSystemDNS(); err != nil {
				takeoverNote = "接管系统 DNS 失败：" + err.Error()
			}
		}
	}
	resp := map[string]any{
		"ok":             true,
		"listenPort":     req.ListenPort,
		"dnsMode":        cfg.DNSMode,
		"upstreamMode":   cfg.UpstreamMode,
		"upstreams":      cfg.Upstreams,
		"upstreamSource": upstreamNote,
	}
	if takeoverNote != "" {
		resp["takeoverError"] = takeoverNote
	}
	writeJSON(w, 200, resp)
}

func (a *App) handleUpstreams(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "仅支持 POST"})
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Action != "" && req.Action != "capture" {
		writeJSON(w, 400, map[string]string{"error": "不支持的操作"})
		return
	}
	cfg := loadConfig()
	if cfg.UpstreamMode != "system" {
		writeJSON(w, 400, map[string]string{"error": "当前是自定义上游模式，先切到「跟随本机 DNS」"})
		return
	}
	old := strings.Join(cfg.Upstreams, ",")
	list, src, err := applySystemUpstreams(cfg)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "读取本机 DNS 失败：" + err.Error()})
		return
	}
	cfg.Upstreams = list
	if err := saveConfig(cfg); err != nil {
		writeJSON(w, 500, map[string]string{"error": "保存配置失败：" + err.Error()})
		return
	}
	restarted := false
	if strings.Join(list, ",") != old {
		if err := a.core.Restart(); err != nil {
			writeJSON(w, 500, map[string]string{"error": "应用新上游失败：" + err.Error()})
			return
		}
		a.reloadConfig()
		restarted = true
	}
	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"upstreams":  list,
		"source":     src,
		"capturedAt": cfg.UpstreamCapturedAt,
		"restarted":  restarted,
	})
}

func (a *App) handleHelper(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{
			"installed": HelperTaskRegistered(),
			"running":   HelperRunning(),
			"admin":     IsAdmin(),
		})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "仅支持 GET / POST"})
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "参数错误"})
		return
	}
	switch req.Action {
	case "register":
		if err := RegisterHelperTask(); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
	case "unregister":
		if err := UnregisterHelperTask(); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
	case "start":
		if !IsAdmin() {
			writeJSON(w, 403, map[string]string{"error": "启动后台助手需要管理员权限：点「以管理员身份重启」后再点「重启」即可；平时登录与每 30 分钟会自动拉起，通常无需手动重启"})
			return
		}
		// 用注册（而不是单纯 /Run）来重启：顺带把任务定义升级成带登录触发 + 30 分钟重复的新版本
		if err := RegisterHelperTask(); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
	default:
		writeJSON(w, 400, map[string]string{"error": "不支持的操作"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "installed": HelperTaskRegistered(), "running": HelperRunning()})
}

func (a *App) handleAutoStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "仅支持 POST"})
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "参数错误"})
		return
	}
	if err := SetAutoStart(req.Enabled); err != nil {
		writeJSON(w, 500, map[string]string{"error": "写入启动项失败：" + err.Error()})
		return
	}
	cfg := loadConfig()
	cfg.AutoStart = req.Enabled
	_ = saveConfig(cfg)
	a.reloadConfig()
	writeJSON(w, 200, map[string]any{"ok": true, "enabled": req.Enabled})
}

func (a *App) handleElevate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "仅支持 POST"})
		return
	}
	if IsAdmin() {
		writeJSON(w, 200, map[string]any{"ok": true, "message": "当前已是管理员权限"})
		return
	}
	if err := Elevate(""); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	// 先把单实例互斥体交出去，再退出，否则提权后的新实例会被自己的旧实例挡住
	releaseSingleInstance()
	writeJSON(w, 200, map[string]any{"ok": true, "message": "已启动管理员实例，本窗口即将退出"})
	go func() {
		time.Sleep(150 * time.Millisecond)
		a.quit()
	}()
}

func (a *App) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "仅支持 POST"})
		return
	}
	if err := a.core.Restart(); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleQuit(w http.ResponseWriter, r *http.Request) {
	panelLog("HTTP 请求退出：method=%s from=%s ua=%s", r.Method, r.RemoteAddr, r.UserAgent())
	writeJSON(w, 200, map[string]any{"ok": true})
	go func() {
		time.Sleep(200 * time.Millisecond)
		a.quit()
	}()
}

func (a *App) quit() {
	// 退出痕迹：托盘「退出应用」、设置页「退出」、退出后核心清理都走这里，
	// 之前这条路径不写日志，事后无法区分「用户主动退出」和「进程自己没了」。
	panelLog("退出流程开始：DNSMode=%s takenOver=%v", loadConfig().DNSMode, IsTakenOver())
	// 兜底：清理步骤（托盘/WebView）偶尔会卡住消息循环，
	// 2.5 秒后无论如何强制退出，避免进程残留导致下次启动报「已有实例」。
	go func() {
		time.Sleep(2500 * time.Millisecond)
		os.Exit(0)
	}()
	// 正常退出时把网卡 DNS 还回去，否则核心一停就整机没有 DNS
	if cfg := loadConfig(); cfg.DNSMode == "system" && IsTakenOver() {
		_ = releaseSystemDNS()
	}
	a.core.Stop()
	if a.tray != nil {
		a.tray.Quit()
	}
	if a.win != nil {
		a.win.Destroy()
	}
	releaseSingleInstance()
	// 给 WebView 一点时间退出消息循环
	time.Sleep(400 * time.Millisecond)
	os.Exit(0)
}
