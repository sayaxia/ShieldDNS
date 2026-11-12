package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 待办是跨进程（普通权限实例 → 提权实例）唯一的交接物，读回来必须完全一致。
func TestPendingUpdateRoundTrip(t *testing.T) {
	t.Setenv("SHIELDDNS_DATA_DIR", t.TempDir())
	in := pendingUpdate{ZipPath: filepath.Join(DataDir(), "update", "local-1.zip"), Label: "本地包", Attempts: 1}
	if err := savePendingUpdate(in); err != nil {
		t.Fatalf("保存待办失败：%v", err)
	}
	out, ok := loadPendingUpdate()
	if !ok {
		t.Fatal("应该读回待办")
	}
	if out.ZipPath != in.ZipPath || out.Label != in.Label || out.Attempts != 1 {
		t.Fatalf("待办内容不一致：%+v", out)
	}
	if out.CreatedAt == "" {
		t.Fatal("应该记录创建时间")
	}
	clearPendingUpdate()
	if _, ok := loadPendingUpdate(); ok {
		t.Fatal("清理后不应该再读到待办")
	}
}

// 空文件 / 坏 JSON 不能当成有效待办，否则启动时会对不存在的包做一次假更新。
func TestPendingUpdateIgnoresGarbage(t *testing.T) {
	t.Setenv("SHIELDDNS_DATA_DIR", t.TempDir())
	if err := os.MkdirAll(DataDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pendingUpdatePath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadPendingUpdate(); ok {
		t.Fatal("坏 JSON 不应该被当成待办")
	}
	if err := os.WriteFile(pendingUpdatePath(), []byte(`{"label":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadPendingUpdate(); ok {
		t.Fatal("缺少 zip_path 的待办不应该被采纳")
	}
}

// 探针要真的反映「能不能写」：临时目录可以，系统受保护目录在普通权限下不行。
func TestInstallDirWritable(t *testing.T) {
	if !installDirWritable(t.TempDir()) {
		t.Fatal("临时目录应该可写")
	}
	progFiles := filepath.Join(os.Getenv("ProgramFiles"), "ShieldDNS")
	if IsAdmin() {
		t.Skip("当前是管理员，受保护目录也可写，跳过反向验证")
	}
	if installDirWritable(progFiles) {
		t.Fatalf("非管理员时 %s 不应该可写", progFiles)
	}
	// 探针不能留下垃圾文件
	if _, err := os.Stat(filepath.Join(progFiles, ".shielddns-write-probe")); err == nil {
		// 只有本来就存在（且可写）才可能是我们留下的；不可写时连创建都失败
		t.Log("受保护目录存在探针文件（应为创建失败，此处仅记录）")
	}
}
