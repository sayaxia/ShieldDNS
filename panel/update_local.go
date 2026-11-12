package main

// 本地更新包：GitHub 打不开（或走代理不方便）的用户，可以把 Release 的 zip 下载回来，
// 在设置页选中它直接更新。走的是和自动更新完全相同的安装流程（改名替换 + 重启 + 换助手），
// 用户不用自己去杀进程、也不要手动覆盖 Program Files 里的文件。

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// 更新包（含核心）现在不到 10 MB，200 MB 足够宽松，同时挡住误拖进来的大文件
const maxLocalPkgBytes = 200 << 20

func (a *App) handleUpdateLocal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}
	updateApplyMu.Lock()
	busy := updateApplyStat.Running
	updateApplyMu.Unlock()
	if busy {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "更新已经在进行中"})
		return
	}
	if r.ContentLength > maxLocalPkgBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "更新包太大（上限 200 MB），请确认选的是 Release 的 zip"})
		return
	}

	dir := filepath.Join(DataDir(), "update")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法创建更新目录：" + err.Error()})
		return
	}
	zipPath := filepath.Join(dir, "local-"+time.Now().Format("20060102-150405")+".zip")
	f, err := os.Create(zipPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法写入更新目录：" + err.Error()})
		return
	}
	written, copyErr := io.Copy(f, http.MaxBytesReader(w, r.Body, maxLocalPkgBytes))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(zipPath)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取上传的更新包失败：" + fmt.Sprint(copyErr, closeErr)})
		return
	}
	// 真实的更新包 7 MB 左右；太小的东西基本是选错了文件，没必要真的去动安装目录
	if written < 512<<10 {
		_ = os.Remove(zipPath)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "这个文件太小了（" + fmt.Sprintf("%d", written) + " 字节），不是 Release 的 zip 包"})
		return
	}
	panelLog("本地更新包：收到 %.1f MB", float64(written)/1048576)

	updateApplyMu.Lock()
	updateApplyStat = updateStatus{Running: true, Stage: "extract", Message: "准备用本地更新包更新"}
	updateApplyMu.Unlock()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				failUpdateStat(fmt.Errorf("panic: %v", p), "更新过程异常，未改动文件")
			}
			updateApplyMu.Lock()
			updateApplyStat.Running = false
			updateApplyMu.Unlock()
		}()
		applyLocalPackage(a, zipPath)
	}()

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func applyLocalPackage(a *App, zipPath string) {
	stage := filepath.Join(DataDir(), "update", "stage-local")
	_ = os.RemoveAll(stage)
	setUpdateStat("extract", 0, "解压本地更新包")
	if err := unzipTo(zipPath, stage); err != nil {
		// 这一步失败时安装目录一点没动，把包删掉，避免留下垃圾
		_ = os.Remove(zipPath)
		failUpdateStat(err, "这个文件不是有效的更新包（zip 打不开）："+err.Error())
		return
	}
	if err := installStaged(a, stage, "本地包"); err != nil {
		failUpdateStat(err, err.Error())
	}
}
