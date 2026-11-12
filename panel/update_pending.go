package main

// 更新要写安装目录：装在 Program Files 这类受保护目录、而面板只是普通权限时，
// 直接替换必然 Access denied。这里把「请求提权 → 提权后的新实例续跑」串起来：
//
//   非管理员点更新 → 下载好 zip、写 pending_update.json → ShellExecute runas 起管理员实例
//   → 旧实例让出单实例互斥体并退出 → 新实例启动时发现待办，直接复用那份 zip 完成替换。
//
// 用户取消 UAC 时不会留下半截状态：待办被清掉，当前实例继续跑，界面照旧显示更新入口。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// errElevateStarted 表示「已经发起提权、当前实例即将退出」，不是失败
var errElevateStarted = errors.New("已请求管理员权限")

type pendingUpdate struct {
	ZipPath   string `json:"zip_path"`
	Label     string `json:"label"`
	Attempts  int    `json:"attempts"`
	CreatedAt string `json:"created_at"`
}

func pendingUpdatePath() string { return filepath.Join(DataDir(), "pending_update.json") }

func savePendingUpdate(p pendingUpdate) error {
	if err := os.MkdirAll(DataDir(), 0o755); err != nil {
		return err
	}
	p.CreatedAt = time.Now().Format("2006-01-02 15:04:05")
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(pendingUpdatePath(), b, 0o644)
}

func loadPendingUpdate() (pendingUpdate, bool) {
	var p pendingUpdate
	b, err := os.ReadFile(pendingUpdatePath())
	if err != nil {
		return p, false
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, false
	}
	return p, p.ZipPath != ""
}

func clearPendingUpdate() { _ = os.Remove(pendingUpdatePath()) }

// installDirWritable 是真凭实据：不看 IsAdmin，直接试着在安装目录里建个文件。
// 便携目录（用户自己的文件夹）本来就写得进去，不该为它弹 UAC。
func installDirWritable(dir string) bool {
	probe := filepath.Join(dir, ".shielddns-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return true
}

// ensureCanInstall 确认能写安装目录；写不了就记录待办并请求提权。
// 返回 errElevateStarted 时调用方应立刻收尾退出，别把提权当成更新失败。
func (a *App) ensureCanInstall(zipPath, label string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("取不到当前程序路径：%w", err)
	}
	dir := filepath.Dir(exe)
	if installDirWritable(dir) {
		return nil
	}
	panelLog("安装目录 %s 不可写（当前非管理员），请求提权后继续更新", dir)
	if err := savePendingUpdate(pendingUpdate{ZipPath: zipPath, Label: label}); err != nil {
		return fmt.Errorf("需要管理员权限才能更新到 %s，且保存待办失败：%w", dir, err)
	}
	setUpdateStat("elevate", 30, "需要管理员权限：正在请求提权，允许后会自动继续更新")
	if err := Elevate(""); err != nil {
		clearPendingUpdate()
		return fmt.Errorf("需要管理员权限才能写入 %s，但提权请求没成功：%w", dir, err)
	}
	// 让出单实例互斥体，否则提权后的新实例会被自己的旧实例挡住（同「以管理员身份重启」）
	releaseSingleInstance()
	go func() {
		time.Sleep(200 * time.Millisecond)
		a.quit()
	}()
	return errElevateStarted
}

// resumePendingUpdate 在启动时调用：上一个实例因为权限不足把更新挂起并请求了提权，
// 现在这个实例如果是管理员，就把那份 zip 接着装完。
func (a *App) resumePendingUpdate() {
	p, ok := loadPendingUpdate()
	if !ok {
		return
	}
	clearPendingUpdate()
	if p.Attempts >= 1 {
		panelLog("待续更新已重试过一次，放弃：%s", p.ZipPath)
		failUpdateStat(fmt.Errorf("pending update retried"), "提权后更新仍失败，请手动运行发布包里的 update.cmd")
		return
	}
	if _, err := os.Stat(p.ZipPath); err != nil {
		panelLog("待续更新的更新包不见了：%s", p.ZipPath)
		failUpdateStat(err, "更新包已被清理，请重新点一次更新")
		return
	}
	if !installDirWritable(filepath.Dir(mustExecutable())) {
		panelLog("提权实例仍然写不了安装目录，放弃自动更新")
		failUpdateStat(fmt.Errorf("still not writable"), "仍然没有写入安装目录的权限，请手动运行发布包里的 update.cmd")
		return
	}
	p.Attempts++
	_ = savePendingUpdate(p)

	panelLog("发现待续更新（%s），在提权实例里继续：%s", p.Label, p.ZipPath)
	updateApplyMu.Lock()
	updateApplyStat = updateStatus{Running: true, Stage: "extract", Message: "已获得管理员权限，继续更新"}
	updateApplyMu.Unlock()
	go func() {
		// 让主流程先把服务、托盘和窗口建起来，界面才有地方显示这段进度
		time.Sleep(1500 * time.Millisecond)
		applyLocalPackageWithLabel(a, p.ZipPath, p.Label)
	}()
}

func mustExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}
