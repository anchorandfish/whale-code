package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// settings.json 的两条生产写路径必须共用同一把跨进程锁。
//
// 现状（修复前）：Loader.Save 自 3-C-4 起持 <path>.lock（lockfile.go acquireLock），
// 而 SaveToFile（/output-style 的 tui/runner_core.go:1308、bridge.go:191/202 的写入口）
// 是 read→merge→原子写、全程无锁 ⇒ 与 Loader.Save 并发时 last-writer-wins，
// 先写者丢规则。原子写只防撕裂、不防丢更新。
//
// 修法：SaveToFile 复用**同一把**锁（acquireLock(filePath) ⇒ 同名 <path>.lock）。
// 绝不能引入第二把锁（如 util.WithFileLock 的 flock）—— 两把不同的锁互不互斥，
// 等于没修（「两把锁」病）。
//
// 判别器（确定性，无时序依赖）：手工预置一个**有效**锁文件（PID=本测试进程 ⇒
// lockVerdict=Held，不可夺），SaveToFile 必须：
// ① 返回锁获取错误（含 "acquire lock"）；
// ② **不**改写目标文件（保住磁盘现值）。
// 修复前（无锁）SaveToFile 直接成功并覆写 ⇒ ①② 双红。
//
// 对照组（防过度拦截 / 防锁残留）：无锁时 SaveToFile 必须成功，且成功后
// .lock 不残留（release 删锁）—— 残留会让后续所有保存阻塞 5 秒后失败；
// 并复核 无损透传未被锁改动破坏。

func TestW074_SaveToFile_RespectsCrossProcessLock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	original := `{"interaction":{"ask_threshold":3},"custom_unknown_key":{"nested":1}}`
	if err := os.WriteFile(p, []byte(original), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// 模拟另一进程持锁：写一个本进程 PID 的有效锁文件（PID 存活且未超时 ⇒ Held）
	lockData := fmt.Sprintf("%d\n%d", os.Getpid(), time.Now().Unix())
	if err := os.WriteFile(p+".lock", []byte(lockData), 0600); err != nil {
		t.Fatalf("setup lock: %v", err)
	}

	err := SaveToFile(&Config{}, p)
	if err == nil {
		t.Fatal(": SaveToFile 在他者持锁时仍然成功 ⇒ 未接跨进程锁" +
			"（Loader.Save 持 <path>.lock 而 SaveToFile 裸写 ⇒ 并发丢更新）")
	}
	if !strings.Contains(err.Error(), "acquire lock") {
		t.Errorf(": 失败原因应来自锁获取（含 \"acquire lock\"），实得: %v", err)
	}
	data, readErr := os.ReadFile(p)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if string(data) != original {
		t.Fatalf(": 持锁期间文件被覆写（丢更新）。\n  期望原值: %s\n  实得: %s", original, string(data))
	}

	// ---- 对照组：释放锁后正常路径必须完好 ----
	if err := os.Remove(p + ".lock"); err != nil {
		t.Fatalf("cleanup lock: %v", err)
	}
	if err := SaveToFile(&Config{}, p); err != nil {
		t.Fatalf(" 对照组：无锁时 SaveToFile 应成功: %v", err)
	}
	if _, statErr := os.Stat(p + ".lock"); statErr == nil {
		t.Errorf(": SaveToFile 成功后 .lock 残留 ⇒ 后续所有保存将阻塞 5 秒后失败")
	}
	data, _ = os.ReadFile(p)
	if !strings.Contains(string(data), "interaction") || !strings.Contains(string(data), "custom_unknown_key") {
		t.Errorf(" 回归：加锁后无损透传被破坏（unknown key 应保留），实得: %s", string(data))
	}
}
