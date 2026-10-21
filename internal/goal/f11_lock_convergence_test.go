// Package goal — F11 局部收敛的判别器（goal 侧）。
//
// 与 memory/f11_lock_convergence_test.go 成对：断言 goal/store_helpers.go 的
// isStaleLock 也是 util.LockVerdict 的薄转发，不再自带二值判定体。
package goal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestF11_IsStaleLockIsThinForward 结构判别器：isStaleLock 必须只是 lockVerdict 的薄转发。
//
// 反例注入（会打红本用例）：把 isStaleLock 改回自带 os.ReadFile + strconv 解析的二值
// 分支（本轮修复前 store_helpers.go:33-56 的形态）。
func TestF11_IsStaleLockIsThinForward(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("store_helpers.go"))
	if err != nil {
		t.Fatalf("读 store_helpers.go 失败: %v", err)
	}
	src := string(data)

	idx := strings.Index(src, "func isStaleLock(")
	if idx < 0 {
		t.Fatal("store_helpers.go 里找不到 func isStaleLock —— 判定声明丢失")
	}
	nextIdx := strings.Index(src[idx:], "func lockVerdict(")
	var body string
	if nextIdx < 0 {
		body = src[idx:]
	} else {
		body = src[idx : idx+nextIdx]
	}

	if !strings.Contains(body, "lockVerdict(lockPath).MaySteal()") {
		t.Errorf("isStaleLock 不再是 lockVerdict 的薄转发：函数体 =\n%s", body)
	}
	for _, forbidden := range []string{"os.ReadFile", "strconv.Atoi", "strconv.ParseInt", "time.Since"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("isStaleLock 残留本地判定体（含 %q）：未收敛为唯一声明 util.LockVerdict", forbidden)
		}
	}
}
