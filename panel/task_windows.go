package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

const helperTaskName = "ShieldDNS-DNS-Helper"
const autoRunValue = "ShieldDNS"

func runHidden(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return "", errors.New(text)
	}
	return text, nil
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return r.Replace(s)
}

// schtasks /Create /XML 要求 UTF-16LE + BOM
func writeUTF16LE(path, text string) error {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xFE})
	for _, v := range utf16.Encode([]rune(text)) {
		b.Write([]byte{byte(v), byte(v >> 8)})
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func createTaskFromXML(name, xmlBody string) error {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("shielddns-task-%d.xml", os.Getpid()))
	if err := writeUTF16LE(path, xmlBody); err != nil {
		return err
	}
	defer os.Remove(path)
	_, err := runHidden(sysExe("schtasks.exe"), "/Create", "/TN", name, "/XML", path, "/F")
	return err
}

// taskExists 问任务计划程序。SYSTEM 任务对普通权限会话是「看得见但打不开」：
// 查询会返回 Access is denied，这本身就是"任务存在"的证据（不存在时报的是"找不到"）。
func taskExists(name string) bool {
	_, err := runHidden(sysExe("schtasks.exe"), "/Query", "/TN", name)
	if err == nil {
		return true
	}
	// schtasks 的报错在中文系统上是 GBK 字节（"错误: 拒绝访问。"），
	// 直接按 UTF-8 比会变成乱码，所以两种编码都认。
	raw := err.Error()
	if strings.Contains(strings.ToLower(raw), "access is denied") {
		return true
	}
	return strings.Contains(raw, "\xbe\xdc\xbe\xf8\xb7\xc3\xce\xca")
}

// helperTaskXML 后台助手任务：开机与登录时拉起，常驻，执行时限设为无限，靠命名管道对外提供服务。
// 登录触发上挂 30 分钟重复：助手被杀死 / 异常退出后任务回到 Ready，下一轮重复会把它重新拉起，
// 普通权限用户不必再为了"重启助手"去提权（多个客户端先后启动时也不会因为助手已停而卡住）。
func helperTaskXML(exe, dataDir, userId string) string {
	upper := "<UserId>" + userId + "</UserId>"
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Author>ShieldDNS</Author>
    <Description>ShieldDNS 后台助手：以 SYSTEM 常驻，供普通权限的面板执行系统 DNS 接管/还原（免 UAC）</Description>
  </RegistrationInfo>
  <Triggers>
    <BootTrigger>
      <Enabled>true</Enabled>
      <Delay>PT5S</Delay>
    </BootTrigger>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <Delay>PT8S</Delay>
      <Repetition>
        <Interval>PT30M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      ` + upper + `
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(exe) + `</Command>
      <Arguments>` + helperTaskArg + ` -data ` + xmlEscape(`"`+dataDir+`"`) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

func HelperTaskRegistered() bool {
	return taskExists(helperTaskName)
}

// RegisterHelperTask 注册并立刻拉起后台助手。需要管理员（只在这一步弹一次 UAC）。
func RegisterHelperTask() error {
	if !IsAdmin() {
		return errors.New("安装后台助手需要管理员权限，请先以管理员身份重启面板")
	}
	sid := CurrentUserSID()
	if sid == "" {
		return errors.New("读取当前用户 SID 失败")
	}
	if err := os.MkdirAll(DataDir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(helperSIDPath(), []byte(sid), 0o644); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// 已有实例在跑时先停掉，否则 /Create /F 覆盖不了正在运行的任务，
	// 旧定义（没有登录触发/重复）也就不会被更新。
	_, _ = runHidden(sysExe("schtasks.exe"), "/End", "/TN", helperTaskName)
	if err := createTaskFromXML(helperTaskName, helperTaskXML(exe, DataDir(), "S-1-5-18")); err != nil {
		return err
	}
	if !HelperTaskRegistered() {
		return errors.New("任务已提交但查询不到，可能被组策略拦截")
	}
	_, _ = runHidden(sysExe("schtasks.exe"), "/Run", "/TN", helperTaskName)
	time.Sleep(1500 * time.Millisecond)
	if !HelperRunning() {
		return errors.New("任务已注册，但后台助手没有连上命名管道，请稍后点『重启助手』")
	}
	return nil
}

func RunHelperTask() error {
	if !HelperTaskRegistered() {
		return errors.New("后台助手还没安装")
	}
	_, err := runHidden(sysExe("schtasks.exe"), "/Run", "/TN", helperTaskName)
	return err
}

func UnregisterHelperTask() error {
	if !IsAdmin() {
		return errors.New("卸载后台助手需要管理员权限")
	}
	_, err := runHidden(sysExe("schtasks.exe"), "/End", "/TN", helperTaskName)
	_ = err // 没在跑时 /End 会报错，忽略
	if _, err := runHidden(sysExe("schtasks.exe"), "/Delete", "/TN", helperTaskName, "/F"); err != nil && HelperTaskRegistered() {
		return err
	}
	_ = os.Remove(helperSIDPath())
	return nil
}

func AutoStartEnabled() bool {
	_, err := runHidden(sysExe("reg.exe"), "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", autoRunValue)
	return err == nil
}

func SetAutoStart(enabled bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if enabled {
		_, err = runHidden(sysExe("reg.exe"), "add", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
			"/v", autoRunValue, "/t", "REG_SZ", "/d", `"`+exe+`"`, "/f")
		return err
	}
	_, err = runHidden(sysExe("reg.exe"), "delete", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
		"/v", autoRunValue, "/f")
	if err != nil && !AutoStartEnabled() {
		return nil // 本来就不存在，视为成功
	}
	return err
}
