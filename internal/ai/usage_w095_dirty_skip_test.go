// usage_w095_dirty_skip_test.go — 修复B 判据。
//
// 现象（tui 全量 panic 栈 + 源码实锤）：NewUsageTracker 无条件 StartAutoSave(30s)，
// 每次 tick 调 Save() → saveLocked() 全量写盘，**无论有没有新记录**。
// - tui 包一次全量 ≈255 个泄漏 tracker × 12 分钟 ≈ 6000+ 次无谓写盘（每写一次 =
// MkdirAll + tempfile + rename），与测试抢 I/O，是全量撞超时的动态拖累主力；
// - 生产侧空闲时同样每 30s 重写一次内容完全相同的文件（≈2880 次/天）。
//
// 修法：dirty 标记（atomic.Bool）。mutator 打标，saveLocked CAS 领取后落盘，
// 写失败还原标记等下轮重试。语义：**Save 的唯一职责是持久化「磁盘上还没有的状态」**。
//
// 判别器（机器可判别，不依赖 mtime 粒度）：把已落盘文件**移走**，再无新记录地 Save
// ⇒ 文件不得被重建（真跳过写盘）；一旦有新 Record ⇒ 必须重新落盘（防过度跳过）。
// 修复前：无记录 Save 也会全量写盘 ⇒ 移走后文件被重建 ⇒ 红。
package ai

import (
	"os"
	"path/filepath"
	"testing"
)

// TestW095_SaveSkipsWhenNoNewUsage 从未记录任何 usage ⇒ Save 不应创建文件。
func TestW095_SaveSkipsWhenNoNewUsage(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "usage.json")
	tracker := NewUsageTracker(storePath)

	if err := tracker.Save(); err != nil {
		t.Fatalf("Save 返回错误: %v", err)
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatal("：从未记录 usage 的 Save 仍创建了文件 —— dirty-skip 未生效" +
			"（测试期 ~255 个泄漏 tracker 每 30s 全量写盘一次 ≈ 6000+ 次无谓 I/O）")
	}
}

// TestW095_SaveWritesWhenDirtyAndSkipsWhenClean 有新记录必写；写后无新记录必跳过；再记录再写。
func TestW095_SaveWritesWhenDirtyAndSkipsWhenClean(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "usage.json")
	tracker := NewUsageTracker(storePath)

	// ① 有新记录 ⇒ 必须落盘
	tracker.RecordUsage("claude-sonnet-4-20250514", 1000, 500, 0, 0)
	if err := tracker.Save(); err != nil {
		t.Fatalf("有新记录的 Save 失败: %v", err)
	}
	if _, err := os.Stat(storePath); err != nil {
		t.Fatalf("有新记录的 Save 必须落盘: %v", err)
	}

	// ② 判别核心：移走文件，无新记录 Save ⇒ 不得重建（证明真的跳过了写盘）
	if err := os.Rename(storePath, storePath+".bak"); err != nil {
		t.Fatalf("前置失败：移走已落盘文件: %v", err)
	}
	if err := tracker.Save(); err != nil {
		t.Fatalf("Save 返回错误: %v", err)
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatal("：无新记录的 Save 仍重写了文件（移走后被重建）—— dirty-skip 未生效")
	}

	// ③ 再有新记录 ⇒ 必须重新落盘（防"过度跳过"把真数据丢了）
	tracker.RecordUsage("claude-sonnet-4-20250514", 100, 50, 0, 0)
	if err := tracker.Save(); err != nil {
		t.Fatalf("新记录后的 Save 失败: %v", err)
	}
	if _, err := os.Stat(storePath); err != nil {
		t.Fatalf("新记录后的 Save 必须重新落盘: %v", err)
	}

	// ④ 落盘内容必须含新记录（跨进程可见性口径）
	loaded, err := LoadUsageTracker(storePath)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	input, _, _, _ := loaded.GetSnapshot()
	if input != 1100 {
		t.Fatalf("落盘内容应含全部累计记录（期望 input=1100，得 %d）", input)
	}
}
