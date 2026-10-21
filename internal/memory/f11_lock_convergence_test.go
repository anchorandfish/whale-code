// Package memory — F11 局部收敛的判别器（memory 侧）。
//
// 规格（本轮任务）：只改 memory/scope.go 与 goal/store_helpers.go 两处，
// isStaleLock 薄转发 util.LockVerdict.MaySteal()，删掉本地二值判定体。
//
// 判别器分两层：
//  1. 结构层：读源码断言 `isStaleLock` 的函数体是**薄转发**（不再含 os.ReadFile /
//     本地时间戳解析的分支判定）。若有人把本地判定体加回来，本用例必须红。
//  2. 行为层：`lockVerdict` 的三值语义（读不到=Stale / 无法解析=Unknown / 超时=Stale）。
package memory

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// TestF11_IsStaleLockIsThinForward 结构判别器：isStaleLock 必须只是 lockVerdict 的薄转发。
//
// 反例注入（会打红本用例）：把 isStaleLock 改回自带 os.ReadFile + 时间戳解析的二值分支
// （即本轮修复前 memory/scope.go:450-472 的形态）。本用例读当前源文件，断言函数体里
// 只有「调用 lockVerdict + MaySteal」这一条 return，没有本地判定分支。
func TestF11_IsStaleLockIsThinForward(t *testing.T) {
	src := readScopeSource(t)

	idx := strings.Index(src, "func isStaleLock(")
	if idx < 0 {
		t.Fatal("scope.go 里找不到 func isStaleLock —— 判定声明丢失")
	}
	nextIdx := strings.Index(src[idx:], "func lockVerdict(")
	var body string
	if nextIdx < 0 {
		body = src[idx:]
	} else {
		body = src[idx : idx+nextIdx]
	}

	// 薄转发 = 必须调用 lockVerdict(...).MaySteal()。
	if !strings.Contains(body, "lockVerdict(lockPath).MaySteal()") {
		t.Errorf("isStaleLock 不再是 lockVerdict 的薄转发：函数体 =\n%s", body)
	}
	// 本地二值判定体的标志：直接读文件 / 直接解析时间戳。若出现 = 判定体又长回来了。
	for _, forbidden := range []string{"os.ReadFile", "parseUnixTimestamp", "time.Since", "lockStaleTimeout"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("isStaleLock 残留本地判定体（含 %q）：未收敛为唯一声明 util.LockVerdict", forbidden)
		}
	}
}

func readScopeSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("scope.go"))
	if err != nil {
		t.Fatalf("读 scope.go 失败: %v", err)
	}
	return string(data)
}

// TestF11_LockVerdictThreeValued 行为判别器：lockVerdict 的三值语义。
func TestF11_LockVerdictThreeValued(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "x.lock")

	// 1) 读不到 ⇒ Stale（可夺）。
	if got := lockVerdict(lockPath); got != util.LockVerdictStale {
		t.Errorf("锁文件不存在 = %v，必须 Stale", got)
	}

	// 2) 无法解析（行数 < 2）⇒ Unknown（不夺）。
	os.WriteFile(lockPath, []byte("only-one-line\n"), 0o600)
	if got := lockVerdict(lockPath); got != util.LockVerdictUnknown {
		t.Errorf("行数不足 = %v，必须 Unknown（未知不夺）", got)
	}

	// 3) 时间戳非数字 ⇒ Unknown。
	os.WriteFile(lockPath, []byte("123\nnot-a-number\n"), 0o600)
	if got := lockVerdict(lockPath); got != util.LockVerdictUnknown {
		t.Errorf("时间戳非数字 = %v，必须 Unknown", got)
	}

	// 4) 未超时（新锁）⇒ Held。
	now := time.Now().Unix()
	os.WriteFile(lockPath, []byte("123\n"+strconv.FormatInt(now, 10)+"\n"), 0o600)
	if got := lockVerdict(lockPath); got != util.LockVerdictHeld {
		t.Errorf("未超时的锁 = %v，必须 Held", got)
	}

	// 5) 超时（> 5 分钟）⇒ Stale。
	os.WriteFile(lockPath, []byte("123\n"+strconv.FormatInt(now-360, 10)+"\n"), 0o600)
	if got := lockVerdict(lockPath); got != util.LockVerdictStale {
		t.Errorf("超时 5 分钟的锁 = %v，必须 Stale", got)
	}
}
