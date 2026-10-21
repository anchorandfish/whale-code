package filehistory

import (
	"os"
	"path/filepath"
	"testing"
)

// turnchanges_test.go — 事实层判据。
//
// 被测对象：`FileHistory.TurnChanges(msgId)` —— "这一轮到底提交了哪些文件变更"。
// 它是轮末折叠卡片与 `/diff` 的**唯一数据源**（同源），
// 所以这里钉的是**净变更口径**本身，而不是某个渲染形态。
//
// 判别性（R11：本改动属"新增承载体"型 ⇒ before 态只会是 `undefined: TurnChanges`，
// 拿编译失败冒充红测没有意义）⇒ 判别性由 overlay 反例变体自证：
// 把聚合口径改回"逐条 track 各报一次"（等价于 old 的朴素实现）⇒ 本文件的两条净值判据必须转红。
// 派生命令与读数见 `内部工作回执` §A/B。

// turnChangesByPath 把结果按路径取出来，便于逐条断言。
func turnChangesByPath(changes []TurnChange) map[string]TurnChange {
	m := make(map[string]TurnChange, len(changes))
	for _, c := range changes {
		m[c.Path] = c
	}
	return m
}

// 一轮内同一文件被改两次 ⇒ **只报一条净变更**（基线=第一次 Track 前，终态=最后一次 TrackAfter 后）。
// 这条是"净变更 vs 逐条累加"的分水岭：逐条累加会把中间态算成结果，用户看到的 +N 会虚高。
func TestTurnChanges_NetAcrossMultipleEditsInOneTurn(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	target := writeFileHelper(t, workDir, "retry.go", "a\nb\nc\n")

	// 第一次编辑：a/b/c -> a/B/c
	if err := h.Track("turn-1", "tc-1", "edit_file", []string{target}); err != nil {
		t.Fatalf("Track#1: %v", err)
	}
	if err := os.WriteFile(target, []byte("a\nB\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := h.TrackAfter("turn-1", "tc-1", []string{target}); err != nil {
		t.Fatalf("TrackAfter#1: %v", err)
	}

	// 第二次编辑：a/B/c -> a/B/C（同一轮、同一 msgId）
	if err := h.Track("turn-1", "tc-2", "edit_file", []string{target}); err != nil {
		t.Fatalf("Track#2: %v", err)
	}
	if err := os.WriteFile(target, []byte("a\nB\nC\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := h.TrackAfter("turn-1", "tc-2", []string{target}); err != nil {
		t.Fatalf("TrackAfter#2: %v", err)
	}

	changes, err := h.TurnChanges("turn-1")
	if err != nil {
		t.Fatalf("TurnChanges: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("同一文件一轮内改两次应只报 1 条净变更，实际 %d 条：%+v", len(changes), changes)
	}
	got := changes[0]
	if got.Before != "a\nb\nc\n" {
		t.Errorf("基线应取**本轮第一次** Track 前的内容（a\\nb\\nc），实际 %q", got.Before)
	}
	if got.After != "a\nB\nC\n" {
		t.Errorf("终态应取**最后一次** TrackAfter 后的内容（a\\nB\\nC），实际 %q", got.After)
	}
	if got.IsNew || got.IsDeleted {
		t.Errorf("修改既有文件不得标成新建/删除：IsNew=%v IsDeleted=%v", got.IsNew, got.IsDeleted)
	}
}

// 本轮新建的文件 ⇒ IsNew=true 且 Before 为空（区别于"空文件"）。
func TestTurnChanges_NewFileInTurn(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	target := filepath.Join(workDir, "brand_new.txt") // 故意不预创建

	if err := h.Track("turn-2", "tc-1", "write_file", []string{target}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := os.WriteFile(target, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := h.TrackAfter("turn-2", "tc-1", []string{target}); err != nil {
		t.Fatalf("TrackAfter: %v", err)
	}

	changes, err := h.TurnChanges("turn-2")
	if err != nil {
		t.Fatalf("TurnChanges: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("应有 1 条变更，实际 %d：%+v", len(changes), changes)
	}
	if !changes[0].IsNew {
		t.Errorf("本轮新建的文件应标 IsNew=true，实际 %+v", changes[0])
	}
	if changes[0].Before != "" || changes[0].After != "hello\n" {
		t.Errorf("新建文件应 Before 为空、After 为新内容，实际 before=%q after=%q",
			changes[0].Before, changes[0].After)
	}
}

// 只 Track 未 TrackAfter（工具失败的中途态）⇒ **不报**。宁可少报，也不把没提交的事说成变更。
func TestTurnChanges_UncommittedTrackIsNotAChange(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	target := writeFileHelper(t, workDir, "failed.txt", "v1\n")

	if err := h.Track("turn-3", "tc-1", "write_file", []string{target}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	// 模拟工具失败：不调用 TrackAfter（文件内容也没变）

	changes, err := h.TurnChanges("turn-3")
	if err != nil {
		t.Fatalf("TurnChanges: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("未提交的 track 不得算作变更，实际返回 %+v", changes)
	}
}

// 本轮删除的文件 ⇒ IsDeleted=true（钉住 `FileChange.Deleted` 这一事实字段的承重）。
// 反例：若 TrackAfter 不落 Deleted，本用例与"工具失败"完全同形 ⇒ 删除会被静默吞掉。
func TestTurnChanges_DeletedFileIsReported(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	target := writeFileHelper(t, workDir, "gone.txt", "line1\nline2\n")

	if err := h.Track("turn-4", "tc-1", "apply_unified_diff", []string{target}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := h.TrackAfter("turn-4", "tc-1", []string{target}); err != nil {
		t.Fatalf("TrackAfter: %v", err)
	}

	changes, err := h.TurnChanges("turn-4")
	if err != nil {
		t.Fatalf("TurnChanges: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("删除应报 1 条变更，实际 %d：%+v", len(changes), changes)
	}
	if !changes[0].IsDeleted {
		t.Errorf("被删除的文件应标 IsDeleted=true，实际 %+v", changes[0])
	}
	if changes[0].Before != "line1\nline2\n" || changes[0].After != "" {
		t.Errorf("删除的正文应为旧内容→空，实际 before=%q after=%q", changes[0].Before, changes[0].After)
	}
}

// 只吃**本轮**的条目，且输出按路径排序（确定序 ⇒ 卡片行序不抖动）。
func TestTurnChanges_ScopedToTurnAndSortedByPath(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	fb := writeFileHelper(t, workDir, "b.txt", "b0\n")
	fa := writeFileHelper(t, workDir, "a.txt", "a0\n")
	other := writeFileHelper(t, workDir, "other.txt", "o0\n")

	// 别的轮次的变更（msgId 不同）—— 不得混进来
	if err := h.Track("turn-other", "tc-0", "write_file", []string{other}); err != nil {
		t.Fatalf("Track(other): %v", err)
	}
	if err := h.TrackAfter("turn-other", "tc-0", []string{other}); err != nil {
		t.Fatalf("TrackAfter(other): %v", err)
	}

	for _, p := range []string{fb, fa} {
		if err := h.Track("turn-5", "tc-1", "write_file", []string{p}); err != nil {
			t.Fatalf("Track(%s): %v", p, err)
		}
		// ★ 本夹具已于 2026-10-09 有意修改（迭代③）——**下一个人别当回归**：
		// 原来在 Track 与 TrackAfter 之间不改内容（no-op 写入），旧口径把 no-op 也算变更；
		// 迭代③起「内容未变的写入」不再入账（`+0 -0` 伪变更行），故夹具改为真实变更。
		// 本用例的意图（轮隔离 + 排序）不受该翻转影响。
		if err := os.WriteFile(p, []byte("updated\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := h.TrackAfter("turn-5", "tc-1", []string{p}); err != nil {
			t.Fatalf("TrackAfter(%s): %v", p, err)
		}
	}

	changes, err := h.TurnChanges("turn-5")
	if err != nil {
		t.Fatalf("TurnChanges: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("本轮应有 2 条变更，实际 %d：%+v", len(changes), changes)
	}
	if filepath.Base(changes[0].Path) != "a.txt" || filepath.Base(changes[1].Path) != "b.txt" {
		t.Errorf("输出应按路径升序，实际 %s, %s", changes[0].Path, changes[1].Path)
	}
	if _, leaked := turnChangesByPath(changes)[other]; leaked {
		t.Error("别的轮次（msgId 不同）的变更泄漏进了本轮结果")
	}
}

// 空 msgId ⇒ 空集（Track/Snapshot 对空 msgId 本就是 no-op，这里保持一致，不报错）。
func TestTurnChanges_EmptyMsgIDIsEmpty(t *testing.T) {
	h, _ := newTestFileHistory(t)
	changes, err := h.TurnChanges("")
	if err != nil {
		t.Fatalf("空 msgId 不应报错：%v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("空 msgId 应返回空集，实际 %+v", changes)
	}
}
