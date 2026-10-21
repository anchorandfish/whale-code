package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestManager_StopAll_KillsGrandchildren 钉住「退出 whale 时后台**孙进程**必须一起死」。
//
// ── 为什么需要它（判别力来源）───────────────────────────────────────────────
// 既有 TestManager_StopAll_ReleasesAllSemaphore 用的是 `sleep 60`：单命令会被
// `sh -c` **exec 优化**成单个进程（sh 自身变成 sleep）⇒ StopAll 只 `cancel()`
// （exec.CommandContext 只杀**直接子进程**）也照样"停得掉" ⇒ 该用例对「孙进程是否残留」
// **判别力为零**（结构性假绿）。真实长跑命令（`npm run dev`、`while` 循环、`a & b`）都有孙进程。
//
// 本用例刻意用复合命令 `( …loop… ) & wait` 让心跳由**孙进程**产出 —— 这是唯一能与
// 「只杀组长」区分开的形态（等价模型实测：单命令看不出差别，复合命令会分岔）。
//
// ── 判据 ────────────────────────────────────────────────────────────────────
// StopAll 之后心跳文件**不再增长**。
//
//	修复前：StopAll 只 cancel() ⇒ 孙进程成孤儿、心跳继续长 ⇒ **本用例红**
//	修复后：StopAll 走 killTree（KillProcessGroup 整组）⇒ 绿
func TestManager_StopAll_KillsGrandchildren(t *testing.T) {
	hb := filepath.Join(t.TempDir(), "hb.txt")

	// 复合命令：父 sh 用 wait 驻留；子 shell 循环写心跳（= sh 的孙进程）。
	cmd := fmt.Sprintf("( while true; do echo tick >> %s; sleep 0.05; done ) & wait", hb)

	m := NewManager(WithMaxConcurrency(2))
	proc, err := m.Start(cmd, "grandchild fixture", "")
	if err != nil {
		t.Fatalf("Start 失败（夹具不健全）：%v", err)
	}
	if !proc.WaitForStatus(StatusRunning, 5*time.Second) {
		t.Fatalf("进程未进入 running（夹具不健全）：status=%v", proc.GetStatus())
	}

	// 配对前置：心跳必须真的在长 —— 否则下面「不再增长」会被"恒空"蒙成假绿。
	if !hbGrew(hb, 3*time.Second) {
		t.Fatalf("夹具不健全：心跳文件未增长，孙进程可能没起来（%s）", hb)
	}

	m.StopAll()

	// 给组杀一点时间生效，再取两个读数：必须相等（= 生产端已停）。
	time.Sleep(600 * time.Millisecond)
	before := hbLines(hb)
	time.Sleep(900 * time.Millisecond)
	after := hbLines(hb)

	if after != before {
		t.Errorf("StopAll 后后台**孙进程仍在跑**（心跳 %d → %d）⇒ 退出 whale 会留孤儿进程；"+
			"根因：StopAll 只 cancel()，未 KillProcessGroup 整棵树", before, after)
	}
}

// hbLines 返回心跳文件行数；查不到返回 -1。
func hbLines(p string) int {
	b, err := os.ReadFile(p)
	if err != nil {
		return -1
	}
	return strings.Count(string(b), "\n")
}

// hbGrew 轮询等待心跳文件出现并在增长（两读不等），超时返回 false。
func hbGrew(p string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	prev := hbLines(p)
	for time.Now().Before(deadline) {
		if cur := hbLines(p); cur > prev {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
