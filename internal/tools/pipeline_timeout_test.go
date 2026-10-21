// 判别器（2026-09-20 批 D 落盘）。
//
// 设计依据（两条都是实测结论，见 内部工作回执）：
// 1. 既有守卫 internal/tools/bash_test.go 的 `TestBash_Timeout` 用 `sleep 30`（**无管道**）
// ⇒ 缺陷态**依然绿**（实测 2.0s 返回）= 不可判别的假绿守卫。
// 根因：`sh -c "sleep 30"` 会直接 exec 掉 sleep，不存在持有 stdout 写端的孙进程，
// 只杀组长就够。**触发条件是「管道里有活着的孙进程」**。
// 2. 所以本文件 T1/T2 一律使用**含管道**的命令。
//
// ⚠️ 本文件走**生产入口** `Executor.Execute`（不另造 harness）。
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// T1 主判据：超时必须真的生效（缺陷态 30s → 修复后 ≈2s）
//
// before（缺陷态）：`sleep 30 | head -10` 的孙进程持有管道写端 ⇒ cmd.Wait() 被拖到
// 命令自然结束（30s）⇒ elapsed ≈ 30s ⇒ **本断言红**。
// after （修复后）：ctx 到期即杀整个进程组 ⇒ elapsed ≈ timeout(2s) ⇒ 绿。
func TestN019_BashTimeoutKillsPipelineGrandchildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(" 复现依赖 POSIX 管道与进程组语义")
	}

	r := NewRegistry()
	e := NewExecutor(r, t.TempDir())

	start := time.Now()
	_ = e.Execute(context.Background(), Call{
		ID:   "n019-1",
		Name: "bash",
		Input: mustMarshal(map[string]interface{}{
			// ★ 命令选择有讲究（实测两次踩坑）：
			// - 不能用 `sleep 30 | head -10`：它命中前置守卫
			// `monitor.DetectBlockedSleep` 的 `^sleep\s+\d+` ⇒ 命令在**超时逻辑之前**
			// 就被拦成 "sleep pattern blocked"，0.05s 返回、is_error=true
			// ⇒ 实测 elapsed≈0.05s < 6s，**假绿**（根本没走到被测代码）。
			// - 也不能用无管道命令（`sleep 30`）：sh 会直接 exec 掉 sleep，不存在持有写端的孙进程
			// ⇒ 只杀组长就够（probe2 实测 2.0s 返回）= 既有 `TestBash_Timeout` 的假绿形态。
			// - 本命令：左端是**独立子 shell**，其 `sleep 30` 持有管道写端 ⇒ 拖住 cmd.Wait，
			// 且整串不含 `^sleep` / `;sleep` / `&&sleep` ⇒ 不被守卫拦截。
			"command": "sh -c 'sleep 30' | head -10",
			"timeout": 2,
		}),
	})
	elapsed := time.Since(start)

	// 阈值取 6s：明显大于 timeout+余量，又远小于命令自然寿命 30s。
	// 判别力实测：before 30.0s（红）/ after ≈2.0s（绿）。
	if elapsed > 6*time.Second {
		t.Errorf(" 超时未生效：`sh -c 'sleep 30' | head -10` 在 timeout=2 下耗时 %v\n"+
			"（预期 < 6s；≈30s 说明管道孙进程持有 stdout 写端拖住了 cmd.Wait，"+
			"其后的 KillProcessGroup 从未执行）", elapsed)
	}
}

// T2 孙进程逃逸判据：超时后进程组必须真的被终止（不只杀掉 sh 组长）
//
// 命令里的 `(sleep 5; touch <marker>)` 是**管道的左端成员 = sh 的孙进程**。
//
//	before：只杀 sh ⇒ 该子 shell 存活 ⇒ 5s 后照常创建 marker ⇒ **断言红**。
//	after ：整个进程组被 SIGKILL ⇒ marker 永不出现 ⇒ 绿。
//
// 这条判据回答的是「沙箱是否只管住了 sh、把后代漏在外面」—— 里带弱安全含义的那一半。
func TestN019_BashTimeoutKillsGrandchildProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(" 复现依赖 POSIX 管道与进程组语义")
	}

	tmpDir := t.TempDir()
	marker := filepath.Join(tmpDir, "grandchild_survived")
	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	// 孙进程 5s 后创建 marker；超时设 2s ⇒ 修复到位时它必须没机会执行
	command := fmt.Sprintf(`(sleep 5; touch %q) | head -10`, marker)

	start := time.Now()
	_ = e.Execute(context.Background(), Call{
		ID:   "n019-2",
		Name: "bash",
		Input: mustMarshal(map[string]interface{}{
			"command": command,
			"timeout": 2,
		}),
	})

	// 等到超过孙进程原本的 5s 期限（再留 2s 余量），才能证明它「确实没活下来」
	remaining := 7*time.Second - time.Since(start)
	if remaining > 0 {
		time.Sleep(remaining)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Errorf(" 管道孙进程逃逸：超时后仍存活并创建了 %s\n"+
			"（说明只杀掉了 sh 组长，进程组未被终止）", marker)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat marker: %v", err)
	}
}
