package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// 构造一个和真实 Release 包结构相同的小压缩包：ShieldDNS.exe + core\shielddns-core.exe
func writeTestZip(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{
		"ShieldDNS.exe":            "panel-binary",
		"core/shielddns-core.exe":  "core-binary",
		"README.md":                "readme",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("file close: %v", err)
	}
}

// 联网冒烟：SDNS_TEST_UPDATE=1 go test -run TestRealReleaseDownload
// 走真实 GitHub 资产，验证 downloadTo 的进度/长度校验与 unzipTo 能解析真实压缩包。
func TestRealReleaseDownload(t *testing.T) {
	if os.Getenv("SDNS_TEST_UPDATE") != "1" {
		t.Skip("需要 SDNS_TEST_UPDATE=1 才跑（会联网下载约 7MB）")
	}
	url := "https://github.com/sayaxia/ShieldDNS/releases/download/v1.0.0/ShieldDNS-1.0.0-win-x64.zip"
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "pkg.zip")
	if err := downloadTo(zipPath, url, 7052152); err != nil {
		t.Fatalf("downloadTo: %v", err)
	}
	if got := fileSize(zipPath); got != 7052152 {
		t.Fatalf("zip size = %d, want 7052152", got)
	}
	stage := filepath.Join(dir, "stage")
	if err := unzipTo(zipPath, stage); err != nil {
		t.Fatalf("unzipTo: %v", err)
	}
	// 真实包外面套一层版本目录，必须靠递归查找定位
	exe := findFile(stage, "ShieldDNS.exe")
	core := findFile(stage, "shielddns-core.exe")
	if fileSize(exe) < 1<<20 {
		t.Fatalf("找不到合理的 ShieldDNS.exe（%q，%d 字节）", exe, fileSize(exe))
	}
	if fileSize(core) < 512<<10 {
		t.Fatalf("找不到合理的 shielddns-core.exe（%q，%d 字节）", core, fileSize(core))
	}
}

func TestUnzipTo(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "pkg.zip")
	writeTestZip(t, zipPath)

	stage := filepath.Join(dir, "stage")
	if err := unzipTo(zipPath, stage); err != nil {
		t.Fatalf("unzipTo: %v", err)
	}
	for name, want := range map[string]string{
		"ShieldDNS.exe":           "panel-binary",
		"core/shielddns-core.exe": "core-binary",
	} {
		got, err := os.ReadFile(filepath.Join(stage, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s content = %q, want %q", name, string(got), want)
		}
	}
}

func TestReplaceFileKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "ShieldDNS.exe")
	src := filepath.Join(dir, "new.exe")
	if err := os.WriteFile(dst, []byte("old-binary"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}
	if err := os.WriteFile(src, []byte("new-binary"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := replaceFile(src, dst); err != nil {
		t.Fatalf("replaceFile: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "new-binary" {
		t.Fatalf("dst = %q", string(got))
	}
	if got, _ := os.ReadFile(dst + ".old"); string(got) != "old-binary" {
		t.Fatalf("backup = %q", string(got))
	}

	// 第二次替换：旧备份要先删掉，不能因为 .old 已存在就失败
	if err := os.WriteFile(src, []byte("newer-binary"), 0o644); err != nil {
		t.Fatalf("write src again: %v", err)
	}
	if err := replaceFile(src, dst); err != nil {
		t.Fatalf("replaceFile second run: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "newer-binary" {
		t.Fatalf("dst after second = %q", string(got))
	}
	if got, _ := os.ReadFile(dst + ".old"); string(got) != "new-binary" {
		t.Fatalf("backup after second = %q", string(got))
	}
}
