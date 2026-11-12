package main

// 应用内自动更新：下载 GitHub Release 的 win-x64 压缩包，校验后直接把面板和核心
// 换成本地文件，然后自己重启，用户不需要去浏览器下载再解压覆盖。
//
// 关键点：正在运行的 exe 不能被覆盖（Windows 会报「文件被占用」），但**可以改名**。
// 所以顺序是：停核心 → 把当前 ShieldDNS.exe 改名成 .old → 写入新文件 → 起新进程 →
// 当前进程退出（旧镜像留在 .old 里当回滚备份，下次启动时删掉）。

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type updateStatus struct {
	Running bool   `json:"running"`
	Stage   string `json:"stage"`
	Percent int    `json:"percent"`
	Message string `json:"message"`
	Version string `json:"version"`
	Error   string `json:"error"`
	Done    bool   `json:"done"`
}

var (
	updateApplyMu   sync.Mutex
	updateApplyStat = updateStatus{}
)

func setUpdateStat(stage string, percent int, msg string) {
	updateApplyMu.Lock()
	updateApplyStat.Stage = stage
	updateApplyStat.Percent = percent
	updateApplyStat.Message = msg
	updateApplyMu.Unlock()
	panelLog("更新：%s", msg)
}

func failUpdateStat(err error, msg string) {
	updateApplyMu.Lock()
	updateApplyStat.Error = msg
	updateApplyMu.Unlock()
	panelLog("更新失败：%v", err)
}

func (a *App) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	updateApplyMu.Lock()
	st := updateApplyStat
	updateApplyMu.Unlock()
	writeJSON(w, http.StatusOK, st)
}

func (a *App) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "仅支持 POST"})
		return
	}
	updateApplyMu.Lock()
	if updateApplyStat.Running {
		updateApplyMu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "更新已经在进行中"})
		return
	}
	updateApplyMu.Unlock()

	res := checkUpdate(true)
	if !res.OK {
		writeJSON(w, 500, map[string]string{"error": res.Error})
		return
	}
	if !res.HasUpdate {
		writeJSON(w, 409, map[string]string{"error": "当前已是最新版本 v" + res.Current})
		return
	}
	if res.DownloadURL == "" || !strings.HasSuffix(strings.ToLower(res.DownloadURL), ".zip") {
		writeJSON(w, 409, map[string]string{"error": "该版本没有可用的 Windows 压缩包，请用「发布页」手动下载"})
		return
	}

	updateApplyMu.Lock()
	updateApplyStat = updateStatus{Running: true, Stage: "prepare", Message: "准备更新到 v" + res.Latest, Version: res.Latest}
	updateApplyMu.Unlock()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				failUpdateStat(fmt.Errorf("panic: %v", p), "更新过程异常，未改动文件")
			}
			// 整个流程退出后才放开并发闸门：错误刚发生时如果立刻放开，
			// 界面再点一次就会和收尾中的这次撞在一起
			updateApplyMu.Lock()
			updateApplyStat.Running = false
			updateApplyMu.Unlock()
		}()
		applyUpdate(a, res)
	}()

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": res.Latest})
}

func applyUpdate(a *App, res updateResult) {
	dir := filepath.Join(DataDir(), "update")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		failUpdateStat(err, "无法创建更新目录："+err.Error())
		return
	}

	zipPath := filepath.Join(dir, "ShieldDNS-"+res.Latest+"-win-x64.zip")
	if _, err := os.Stat(zipPath); err != nil || (res.Size > 0 && fileSize(zipPath) != res.Size) {
		_ = os.Remove(zipPath)
		setUpdateStat("download", 0, "开始下载 v"+res.Latest)
		if err := downloadTo(zipPath, res.DownloadURL, res.Size); err != nil {
			failUpdateStat(err, "下载失败："+err.Error())
			return
		}
	}

	stage := filepath.Join(dir, "stage-"+res.Latest)
	_ = os.RemoveAll(stage)
	setUpdateStat("extract", 0, "解压更新包")
	if err := unzipTo(zipPath, stage); err != nil {
		failUpdateStat(err, "解压失败："+err.Error())
		return
	}
	// 解压完才动手之前先确认写得了安装目录：装在 Program Files 又只是普通权限时，
	// 走提权分支（记录待办 + UAC + 本实例退出），而不是等到改名失败才报一句 Access denied。
	if err := a.ensureCanInstall(zipPath, "v"+res.Latest); err != nil {
		if errors.Is(err, errElevateStarted) {
			return
		}
		failUpdateStat(err, err.Error())
		return
	}
	if err := installStaged(a, stage, "v"+res.Latest); err != nil {
		failUpdateStat(err, err.Error())
	}
}

// installStaged 是自动更新和「本地更新包」共用的安装步骤：
// 从解压目录里找出 exe/core → 停核心、还原 DNS → 改名替换（运行中的 exe 只能改名）
// → 停掉后台助手（让新面板用新 exe 重新拉起）→ 交给脱离父进程的启动器重启。
func installStaged(a *App, stage, label string) error {
	// Release 包外面套了一层目录（如 ShieldDNS-1.0.0-win-x64/），所以按文件名在
	// 解压结果里递归找，而不是假定文件就在根上。
	newExe := findFile(stage, "ShieldDNS.exe")
	newCore := findFile(stage, "shielddns-core.exe")
	if fileSize(newExe) < 1<<20 || fileSize(newCore) < 512<<10 {
		return fmt.Errorf("更新包内容不完整（找不到合理的 ShieldDNS.exe 或 core\\shielddns-core.exe，来自 %s）", label)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("取不到当前程序路径：%w", err)
	}
	exeDir := filepath.Dir(exe)
	corePath := filepath.Join(exeDir, "core", "shielddns-core.exe")

	// 先把核心停掉，否则核心 exe 被占用，改不了名也写不进去
	setUpdateStat("stop", 10, "停止本地核心")
	a.core.Stop()
	time.Sleep(1200 * time.Millisecond)

	// 更新会让面板先退出再重启，这中间 53 端口没人监听；
	// 若系统 DNS 还指着 127.0.0.1，用户这段时间就是断网的。所以先把 DNS 还回去，
	// 新面板起来后按用户操作重新接管（启动时本来就是待命状态）。
	if IsTakeoverEffective() {
		setUpdateStat("stop", 20, "还原系统 DNS")
		if err := releaseSystemDNS(); err != nil {
			panelLog("更新前还原系统 DNS 失败（继续更新）：%v", err)
		}
	}

	setUpdateStat("replace", 40, "替换程序文件")
	if err := replaceFile(newExe, exe); err != nil {
		// 走到这里说明权限已经够（ensureCanInstall 先做过写入探针），
		// 再失败基本是杀软/索引锁着文件，别把用户往「以管理员身份重启」上引
		return fmt.Errorf("替换主程序失败（文件被安全软件或索引服务锁住了，稍后重试或运行发布包里的 update.cmd）：%w", err)
	}
	if _, err := os.Stat(corePath); err == nil {
		if err := replaceFile(newCore, corePath); err != nil {
			return fmt.Errorf("主程序已更新，但核心替换失败：%w", err)
		}
	}

	// 后台助手跑的是同一个 exe 的另一个进程：替换文件不受它影响（改名 + 写新文件即可），
	// 但它还映射着旧镜像、占着旧镜像的句柄，而且代码是旧的。
	// 这里把它停掉，等新面板启动时由 ensureHelperAlive 用新 exe 重新拉起。
	if HelperTaskRegistered() {
		_, _ = runHidden(sysExe("schtasks.exe"), "/End", "/TN", helperTaskName)
		if !waitHelperStopped(1500 * time.Millisecond) {
			stopHelperProcesses()
			_ = waitHelperStopped(3 * time.Second)
		}
		panelLog("更新：已停止后台助手，待新面板启动后重新拉起")
	}

	setUpdateStat("restart", 100, "更新完成，正在重启")
	updateApplyMu.Lock()
	updateApplyStat.Done = true
	updateApplyMu.Unlock()

	// 不能直接在这里起新实例：此刻自己还活着，单实例互斥体和 WebView2 用户目录
	// 都还占着，新实例会因为抢不到这些资源而立刻退出（实测就是这么失败的）。
	// 改成交给一个脱离父进程的小启动器，等当前进程退出后再拉起新 exe。
	if err := launchAfterExit(exe, exeDir); err != nil {
		panelLog("更新后拉起新实例失败：%v", err)
	}
	a.quit()
	return nil
}

// launchAfterExit 写一个临时 cmd 脚本并以 DETACHED_PROCESS 启动：延迟几秒后 start 新 exe。
// 延迟是给旧进程留退出时间（释放互斥体、WebView2 用户目录、核心端口）。
func launchAfterExit(exe, exeDir string) error {
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	}
	if _, err := os.Stat(shell); err != nil {
		return fmt.Errorf("找不到 cmd.exe（%s）：%v", shell, err)
	}
	script := filepath.Join(os.TempDir(), "shielddns-restart.cmd")
	content := "@echo off\r\n" +
		"ping -n 6 127.0.0.1 >nul\r\n" +
		"start \"\" /d \"" + exeDir + "\" \"" + exe + "\"\r\n" +
		"del \"%~f0\"\r\n"
	if err := os.WriteFile(script, []byte(content), 0o644); err != nil {
		return err
	}
	// 注意：必须用绝对路径。本机 PATH 里没有 System32，裸名 cmd.exe 会直接报找不到。
	cmd := exec.Command(shell, "/c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x00000008, // DETACHED_PROCESS：不随父进程退出而被带走
	}
	return cmd.Start()
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

// findFile 在解压目录里递归找指定文件名，返回体积最大的那个（防止包里带多份）
func findFile(root, name string) string {
	var best string
	var bestSize int64
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if !strings.EqualFold(info.Name(), name) {
			return nil
		}
		if info.Size() > bestSize {
			best = p
			bestSize = info.Size()
		}
		return nil
	})
	return best
}

// downloadTo 边下边报进度；先写 .part 再改名，避免中断留下半个包被当成完整包
func downloadTo(dst, url string, expected int64) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ShieldDNS/"+appVersion)
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("连不上 GitHub 下载地址：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载地址返回 HTTP %d", resp.StatusCode)
	}
	// 跟随重定向后 Content-Length 就是真实大小；拿不到时用 Release 里报的尺寸
	total := expected
	if resp.ContentLength > 0 {
		total = resp.ContentLength
	}

	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	var written int64
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				_ = os.Remove(tmp)
				return werr
			}
			written += int64(n)
			if total > 0 {
				pct := int(written * 100 / total)
				if pct > 99 {
					pct = 99
				}
				setUpdateStat("download", pct, fmt.Sprintf("下载中 %d%%（%.1f / %.1f MB）",
					pct, float64(written)/1048576, float64(total)/1048576))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			_ = os.Remove(tmp)
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if total > 0 && written != total {
		_ = os.Remove(tmp)
		return fmt.Errorf("下载不完整：%d / %d 字节", written, total)
	}
	// 刚关闭的 7MB 文件常被杀软/索引临时占用，改名会偶发失败，这里重试几次
	var renameErr error
	for i := 0; i < 8; i++ {
		if renameErr = os.Rename(tmp, dst); renameErr == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return renameErr
}

func unzipTo(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("压缩包打不开：%v", err)
	}
	defer r.Close()
	for _, f := range r.File {
		name := filepath.Clean(f.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		target := filepath.Join(dst, name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		_, cpErr := io.Copy(out, rc)
		out.Close()
		rc.Close()
		if cpErr != nil {
			return cpErr
		}
	}
	return nil
}

// replaceFile 用 src 覆盖 dst：旧的先改名成带时间戳的 .old（正在运行的 exe 只能改名，不能删改），
// 拷贝失败就把备份改回去，避免程序处于「新旧都没有」的状态。
//
// 两点是被真实场景逼出来的：
//  1. 备份名不能固定成 dst+".old"：后台助手是同一个 exe 的另一个进程，上一版的 .old 可能
//     还被它开着，删不掉；用固定名的话第二次更新会在「先删旧备份」这一步直接失败。
//  2. 改名本身也有偶发失败（杀软/索引正在扫这个文件），所以重试几次再报错。
func replaceFile(src, dst string) error {
	// 先把上一版遗留的备份清掉（删不掉的留给启动清理，不能阻塞更新）
	cleanupOldBinaries(dst)

	old := dst + ".old-" + time.Now().Format("20060102-150405")
	renamed := false
	if _, err := os.Stat(dst); err == nil {
		var renameErr error
		for i := 0; i < 8; i++ {
			if renameErr = os.Rename(dst, old); renameErr == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if renameErr != nil {
			return fmt.Errorf("旧文件改名失败：%w", renameErr)
		}
		renamed = true
	}
	if err := copyFile(src, dst); err != nil {
		_ = os.Remove(dst)
		if renamed {
			_ = os.Rename(old, dst)
		}
		return err
	}
	return nil
}

// cleanupOldBinaries 删除安装目录里上一版留下的 exe 备份（*.old / *.old-<时间戳> / *~）。
// 当前进程或后台助手可能还映射着这些镜像，删不掉就后台重试几轮，绝不阻塞更新。
func cleanupOldBinaries(path string) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	d := struct{ dir, prefix string }{dir, base}
	coreDir := filepath.Join(filepath.Dir(path), "core")
	dirs := []struct{ dir, prefix string }{
		{d.dir, d.prefix + ".old"},
		{d.dir, d.prefix + "~"},
		{coreDir, "shielddns-core.exe.old"},
		{coreDir, "shielddns-core.exe~"},
	}
	removeMatching := func(t struct{ dir, prefix string }) []string {
		var left []string
		entries, err := os.ReadDir(t.dir)
		if err != nil {
			return nil
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), t.prefix) {
				continue
			}
			full := filepath.Join(t.dir, e.Name())
			if err := os.Remove(full); err != nil {
				left = append(left, full)
			}
		}
		return left
	}
	var leftovers []string
	for _, t := range dirs {
		leftovers = append(leftovers, removeMatching(t)...)
	}
	if len(leftovers) == 0 {
		return
	}
	go func() {
		for i := 0; i < 6; i++ {
			time.Sleep(2 * time.Second)
			kept := leftovers[:0]
			for _, p := range leftovers {
				if err := os.Remove(p); err != nil {
					kept = append(kept, p)
				}
			}
			leftovers = kept
			if len(leftovers) == 0 {
				return
			}
		}
	}()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
