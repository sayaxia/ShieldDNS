package main

// 更新后的重启接力：新进程先等旧进程真的退出，再继续启动。
//
// 以前这 5 秒是靠临时 .cmd 里 `ping -n 6 127.0.0.1` 凑的——cmd 以 DETACHED_PROCESS 起，
// 没有控制台，它拉起的 ping 会自己新建一个控制台窗口，用户就看见一个黑框在跑 ping。
// 现在改成新进程自己用内核等待（OpenProcess + WaitForSingleObject），
// 不轮询、不起子进程、不产生任何控制台。

import (
	"time"

	"golang.org/x/sys/windows"
)

// waitForProcessExit 等 pid 退出，最多 timeout。打不开句柄（进程已经没了/权限不够）就直接返回，
// 后面 acquireSingleInstance 还有一次带超时的互斥体等待兜底。
func waitForProcessExit(pid int, timeout time.Duration) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_, _ = windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
}
