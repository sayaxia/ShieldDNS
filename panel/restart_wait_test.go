package main

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// 让测试二进制能充当一个「活 0.9 秒的子进程」：设了 SHIELDDNS_TEST_SLEEP 就直接睡完退出，
// 不跑任何测试。用来验证 waitForProcessExit 真的是等对方退出，而不是定点睡个常数。
func TestMain(m *testing.M) {
	if d := os.Getenv("SHIELDDNS_TEST_SLEEP"); d != "" {
		if dur, err := time.ParseDuration(d); err == nil {
			time.Sleep(dur)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWaitForProcessExitWaits(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestNoSuchTestRuns")
	cmd.Env = append(os.Environ(), "SHIELDDNS_TEST_SLEEP=900ms")
	if err := cmd.Start(); err != nil {
		t.Fatalf("起测试子进程失败：%v", err)
	}
	start := time.Now()
	waitForProcessExit(cmd.Process.Pid, 5*time.Second)
	elapsed := time.Since(start)
	_ = cmd.Wait()
	if elapsed < 600*time.Millisecond {
		t.Fatalf("子进程还活着就返回了（等了 %v），重启接力会撞上互斥体", elapsed)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("等太久（%v）：WaitForSingleObject 没在进程退出时立刻返回", elapsed)
	}
}

// 等一个不会退出的进程（自己）时必须按 timeout 收手，不能把启动卡死。
func TestWaitForProcessExitHonoursTimeout(t *testing.T) {
	start := time.Now()
	waitForProcessExit(os.Getpid(), 400*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed < 300*time.Millisecond {
		t.Fatalf("超时还没到就返回了（%v）", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("超时没生效（%v）", elapsed)
	}
}
