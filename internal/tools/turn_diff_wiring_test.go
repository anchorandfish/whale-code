package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/diff"
	"github.com/anchorandfish/whale-code/internal/testutil"
)

// turn_diff_wiring_test.go — 接线判据。
//
// 为什么必须有这一条（否则是 R1 的"接线断言假绿"）：
// `FileHistory.TurnChanges` 的输入是**写类工具自己写下的 track 记录**。单测直接调
// `Track/TrackAfter`（见 filehistory/turnchanges_test.go）只能证明"聚合口径对"，
// **证不了**生产里那条线真的通 —— 例如 trackBefore 被放到写盘之后、或 TrackAfter
// 拿不到 toolCallId、或 msgId 在轮内被改成别的值。那些缺陷会让轮末卡片**恒空**，
// 而聚合层单测全绿。
//
// ⇒ 本判据从**生产工具入口**走：真实 `write_file` 工具 → 它自己调 Track/TrackAfter，
// 再断言"这一轮的净变更"（msgId 由生产侧设置，见 stream.go:300 的双写注释）。
//
// 判别性（overlay 变体，读数见回执 §A/B）：
// - 把 `file.go` 的 `e.trackBefore(...)` 移到写盘之后 ⇒ 本判据必须红（Before 变成新内容）；
// - 把 `file.go` 的 pushFileChange（TrackAfter）摘掉 ⇒ 本判据必须红（变更被剔除 ⇒ 空集）。

// writeThroughTool 经真实工具入口写一次文件（Track/TrackAfter 由 handler 自己调）。
func writeThroughTool(t *testing.T, e *Executor, path, content, callID string) {
	t.Helper()
	res := e.Execute(context.Background(), Call{
		ID:    callID,
		Name:  "write_file",
		Input: mustMarshal(map[string]string{"path": path, "content": content}),
	})
	if res.IsError {
		t.Fatalf("write_file(%s) 失败：%s", callID, res.Output)
	}
}

// 同一轮内两次真实写入 ⇒ 轮末拿到的是**净变更**（基线=轮首内容，终态=轮末内容）。
func TestTurnDiff_NetChangeFromRealToolCalls(t *testing.T) {
	// 隔离 HOME：FileHistory 存储落在临时目录，不污染用户真实 ~/.whale/filehistory。
	// ★ 走 testutil.IsolatedHomeDir（宽松清理），不得用 t.TempDir()：后台协程会在
	// 用例结束后继续写 $HOME，与 t.TempDir 的 RemoveAll 断言竞态（仓库根卫生门禁）。
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))

	workDir := t.TempDir()
	e := NewExecutor(NewRegistry(), workDir)
	if e.FileHistory == nil {
		t.Fatal("前置失败：FileHistory 未初始化（降级态）⇒ 本判据不可执行")
	}

	const msgID = "user-msg-turn-1" // 生产中由 sendInput 设置（stream.go:300）
	e.SetCurrentMessageId(msgID)

	target := filepath.Join(workDir, "wired.txt")
	if err := os.WriteFile(target, []byte("a\nb\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}

	writeThroughTool(t, e, target, "a\nB\nc\n", "call-1")
	writeThroughTool(t, e, target, "a\nB\nC\n", "call-2")

	assertWiredTurnChanges(t, e, msgID)

	// finishTurn 的真实顺序是「先 Snapshot（轮末快照）→ 再采卡片」，快照不得吃掉 track 记录。
	if _, err := e.FileHistory.Snapshot(msgID, "session-under-test", 3); err != nil {
		t.Fatalf("轮末 Snapshot: %v", err)
	}
	assertWiredTurnChanges(t, e, msgID)
}

// assertWiredTurnChanges 断言这一轮的净变更 + 组装层统计（卡片与 /diff 共用同一份）。
func assertWiredTurnChanges(t *testing.T, e *Executor, msgID string) {
	t.Helper()

	changes, err := e.FileHistory.TurnChanges(msgID)
	if err != nil {
		t.Fatalf("TurnChanges: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("真实工具改同一文件两次应得 1 条净变更，实际 %d 条：%+v", len(changes), changes)
	}
	got := changes[0]
	if got.Path != mustAbs(t, got.Path) {
		t.Errorf("Path 应为绝对路径，实际 %q", got.Path)
	}
	if got.IsNew {
		t.Errorf("轮首文件已存在，不得标 IsNew：%+v", got)
	}
	if got.Before != "a\nb\nc\n" {
		t.Errorf("基线应为**轮首**内容（a\\nb\\nc），实际 %q —— 基线若变成中间态/新内容，说明 trackBefore 与写盘的顺序错了", got.Before)
	}
	if got.After != "a\nB\nC\n" {
		t.Errorf("终态应为**轮末**内容（a\\nB\\nC），实际 %q", got.After)
	}

	// 组装层（轮末卡片与 /diff 的唯一统计口径）
	td := diff.NewTurnDiff(1, "", []diff.FileChange{{
		Path:       got.Path,
		OldContent: got.Before,
		NewContent: got.After,
		IsNew:      got.IsNew,
		IsDeleted:  got.IsDeleted,
	}})
	if td.Stats.FilesChanged != 1 || td.Stats.Insertions != 2 || td.Stats.Deletions != 2 {
		t.Errorf("统计应为 1 文件 +2 -2（两处单行替换），实际 %+v", td.Stats)
	}
}

// mustAbs 校验路径已是绝对路径（生产索引里存的是 safePath 解析后的绝对路径）。
func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", p, err)
	}
	return abs
}
