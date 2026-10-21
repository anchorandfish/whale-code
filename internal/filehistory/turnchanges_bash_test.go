package filehistory

import (
	"os"
	"path/filepath"
	"testing"
)

// turnchanges_bash_test.go — 迭代③「bash 写重定向入卡」事实层判据。
//
// 拍板（2026-10-09，用户）：bash 的 `>` / `>>` 字面写法目标进轮末卡片；
// 提不出静态目标的（变量/命令替换/tee/mv/脚本内部写）**不猜、不编造**。
// （2026-10-16 用户拍板：卡片提示行及其 BashRan 旗标整链取消 —— 每轮必现太吵。）
//
// 本文件钉两条不变量：
// 1. **no-op 剔除**：重定向写进去的内容与轮首完全一致（`cat a > b` 且 b 本来就等于 a）
// ⇒ 不是变更，不得出现在卡片上（否则会显示 `+0 -0` 的伪变更行）。
//
// 判别性（R11：新增承载体型 ⇒ before 态编译红不算证据）由 overlay 变体自证：
// - 摘掉 TurnChanges 的 no-op 过滤 ⇒ TestTurnChanges_SkipsNoOp 红。

// 重定向写入的内容与轮首一致 ⇒ 不是变更，聚合层必须剔除。
func TestTurnChanges_SkipsNoOp(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	target := filepath.Join(workDir, "noop.txt")
	if err := os.WriteFile(target, []byte("same\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := h.Track("turn-noop", "tc-1", "bash", []string{target}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	// 写入与轮首**完全相同**的内容（模拟 `printf 'same\n' > noop.txt` 的重定向）。
	if err := os.WriteFile(target, []byte("same\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := h.TrackAfter("turn-noop", "tc-1", []string{target}); err != nil {
		t.Fatalf("TrackAfter: %v", err)
	}

	changes, err := h.TurnChanges("turn-noop")
	if err != nil {
		t.Fatalf("TurnChanges: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("内容未变的重定向不是变更，不得出现在卡片上；实际 %d 条：%+v", len(changes), changes)
	}
}

// 对照半边：同样的写法、内容真的变了 ⇒ 必须仍然在列（防"过滤过宽"把真变更也吃了）。
func TestTurnChanges_NoOpFilterDoesNotEatRealChanges(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	target := writeFileHelper(t, workDir, "real.txt", "old\n")

	if err := h.Track("turn-real", "tc-1", "bash", []string{target}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := os.WriteFile(target, []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := h.TrackAfter("turn-real", "tc-1", []string{target}); err != nil {
		t.Fatalf("TrackAfter: %v", err)
	}

	changes, err := h.TurnChanges("turn-real")
	if err != nil {
		t.Fatalf("TurnChanges: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("真变更必须仍然在列，实际 %d 条：%+v", len(changes), changes)
	}
}

// 重定向目标**创建失败**（`> 坏路径`：shell 起了进程但文件没写成）⇒ 轮首不存在、
// 轮末也不存在 ⇒ 净变化为零，不得报成"新建+删除"的伪变更。
func TestTurnChanges_NeverCreatedRedirectNotReported(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	target := filepath.Join(workDir, "never_created.txt") // 故意不预创建

	if err := h.Track("turn-nc", "tc-1", "bash", []string{target}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	// 不写文件 —— 模拟重定向目标创建失败（TrackAfter 读到 NotExist ⇒ 标 Deleted）。
	if err := h.TrackAfter("turn-nc", "tc-1", []string{target}); err != nil {
		t.Fatalf("TrackAfter: %v", err)
	}

	changes, err := h.TurnChanges("turn-nc")
	if err != nil {
		t.Fatalf("TurnChanges: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("轮首不存在、轮末也不存在 ⇒ 净零，不得报成变更；实际 %d 条：%+v", len(changes), changes)
	}
}
