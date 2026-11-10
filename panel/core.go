package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// CoreManager 负责 Rust 核心进程的启停与日志落盘
type CoreManager struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	logFile *os.File
	exePath string
	cfgPath string
	lastErr string
}

func NewCoreManager() *CoreManager {
	return &CoreManager{cfgPath: ConfigPath(), exePath: findCoreExe()}
}

func findCoreExe() string {
	self, err := os.Executable()
	if err != nil {
		self = "."
	}
	dir := filepath.Dir(self)
	candidates := []string{
		filepath.Join(dir, "core", "shielddns-core.exe"),
		filepath.Join(dir, "shielddns-core.exe"),
		filepath.Join(dir, "..", "core", "target", "release", "shielddns-core.exe"),
		filepath.Join(dir, "..", "core", "target", "debug", "shielddns-core.exe"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	return candidates[0]
}

func (m *CoreManager) ExePath() string { return m.exePath }

func (m *CoreManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil {
		return nil
	}
	if _, err := os.Stat(m.exePath); err != nil {
		m.lastErr = "找不到核心程序: " + m.exePath
		return errors.New(m.lastErr)
	}
	// 启动前先清掉上一轮遗留的核心进程：它可能还占着控制端口和监听端口，
	// 导致新核心绑不上端口、而 ping 却打到旧进程上，出现"假成功"。
	m.clearStaleLocked()

	_ = os.MkdirAll(filepath.Join(DataDir(), "logs"), 0o755)
	logPath := filepath.Join(DataDir(), "logs", "core.log")
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	fmt.Fprintf(lf, "\n===== %s 启动核心 =====\n", time.Now().Format("2006-01-02 15:04:05"))

	cmd := exec.Command(m.exePath, "--config", m.cfgPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.Stdout = lf
	cmd.Stderr = lf
	if err := cmd.Start(); err != nil {
		lf.Close()
		m.lastErr = err.Error()
		return err
	}
	m.cmd = cmd
	m.logFile = lf
	pid := uint32(cmd.Process.Pid)

	go func() {
		_ = cmd.Wait()
		m.mu.Lock()
		if m.cmd == cmd {
			m.cmd = nil
			if m.logFile != nil {
				m.logFile.Close()
				m.logFile = nil
			}
		}
		m.mu.Unlock()
	}()

	// 等控制接口就绪，并且必须是"我们这个子进程"在应答 ——
	// 只看到 200 不够，那可能是别人（旧核心）在应答。
	for i := 0; i < 40; i++ {
		if got, ok := healthPid(); ok && got == pid {
			m.lastErr = ""
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	m.lastErr = "核心启动超时（控制接口无响应或不属于本进程）"
	return errors.New(m.lastErr)
}

type healthResp struct {
	OK   bool   `json:"ok"`
	Pid  uint32 `json:"pid"`
	Vers string `json:"version"`
}

func healthPid() (uint32, bool) {
	cfg := loadConfig()
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/health", cfg.ControlPort))
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, false
	}
	var h healthResp
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return 0, false
	}
	if h.Pid == 0 {
		return 0, false
	}
	return h.Pid, true
}

func isOurCoreImage(pid uint32) bool {
	out, err := runHidden(sysExe("tasklist.exe"), "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	if err != nil || out == "" {
		return false
	}
	return strings.Contains(strings.ToLower(out), "shielddns-core.exe")
}

// clearStaleLocked 干掉"不是自己启动的"核心进程（上一轮崩溃/面板被强杀留下的）
func (m *CoreManager) clearStaleLocked() {
	pid, ok := healthPid()
	if !ok {
		return
	}
	if m.cmd != nil && m.cmd.Process != nil && uint32(m.cmd.Process.Pid) == pid {
		return
	}
	if !isOurCoreImage(pid) {
		return
	}
	_, _ = runHidden(sysExe("taskkill.exe"), "/PID", fmt.Sprintf("%d", pid), "/F")
	for i := 0; i < 12; i++ {
		if _, alive := healthPid(); !alive {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (m *CoreManager) ping() bool {
	cfg := loadConfig()
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/health", cfg.ControlPort))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func (m *CoreManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

func (m *CoreManager) stopLocked() {
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		m.cmd = nil
	}
	if m.logFile != nil {
		_ = m.logFile.Close()
		m.logFile = nil
	}
}

func (m *CoreManager) Restart() error {
	m.Stop()
	time.Sleep(400 * time.Millisecond)
	return m.Start()
}

func (m *CoreManager) Running() bool {
	m.mu.Lock()
	running := m.cmd != nil
	m.mu.Unlock()
	if !running {
		return false
	}
	return m.ping()
}

func (m *CoreManager) LastError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr
}
