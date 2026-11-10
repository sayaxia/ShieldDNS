package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// coreProtectionOn 用核心自己的状态做判据（助手是 SYSTEM，本地回环随便访问）
func coreProtectionOn(controlPort int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", controlPort))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	var st struct {
		Protection bool `json:"protection"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return false
	}
	return st.Protection
}

// helperGuard 由后台助手常驻执行，取代原先单独的看门狗计划任务：
//   - 防护开着、端口是 53，但系统 DNS 没指向 127.0.0.1（被 VPN / 手动改掉）→ 补回接管
//   - 接管记录还在，但 127.0.0.1 连续 4 次没有 DNS 应答（核心崩溃且没起来）→ 还原系统 DNS
//
// 只在这两种确定的情况下动手，核心正常应答时什么都不做，重复检查也无害。
func helperGuard() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	fails := 0
	for range ticker.C {
		cfg := loadConfig()
		if cfg.ListenPort != 53 {
			// 非 53 端口本来就不接管本机 DNS，没有可守护的东西
			fails = 0
			continue
		}
		protection := coreProtectionOn(cfg.ControlPort)

		if !IsTakenOver() {
			fails = 0
			if protection {
				if err := TakeoverDNS(); err != nil {
					helperLog("守护：防护开着但接管失败：%v", err)
				} else {
					helperLog("守护：检测到系统 DNS 未指向 127.0.0.1，已补回接管")
				}
			}
			continue
		}

		if protection || probeDNS(cfg.ListenPort) {
			fails = 0
			continue
		}
		fails++
		helperLog("守护：127.0.0.1:%d 无应答且核心未在防护（连续 %d 次）", cfg.ListenPort, fails)
		if fails >= 4 {
			if err := ReleaseDNS(); err != nil {
				helperLog("守护：还原系统 DNS 失败：%v", err)
			} else {
				helperLog("守护：核心长时间无应答，已还原系统 DNS，避免全机断网")
			}
			fails = 0
		}
	}
}
