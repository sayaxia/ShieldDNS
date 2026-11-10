package main

import (
	"os"
	"path/filepath"
)

// sysExe 返回 System32 下系统工具的绝对路径。
// 直接把裸名丢给 exec 并不可靠：面板可能被一个裁剪过 PATH 的父进程拉起
// （脚本、计划任务、精简过的 shell），此时 System32 不在 PATH 里，
// schtasks / netstat 这类命令会直接失败，调用方再把失败当成"任务不存在 / 端口没被占"。
func sysExe(name string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", name)
}
