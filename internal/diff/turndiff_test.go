package diff

import "testing"

// turndiff_test.go — 组装层判据。
//
// 钉的是「一轮的统计口径」：NewTurnDiff 的 Stats 必须与 CountChanges 同源，
// 否则轮末卡片的一行 `+N -M` 与 `/diff` 正文的合计会各说各话。
//
// 判别性：把 `stats.Insertions += added` / `Deletions += d` 任一行摘掉 ⇒ 对应子测试转红
// （见回执 §A/B 的 mutant 读数）。

func TestNewTurnDiff_StatsFromChanges(t *testing.T) {
	files := []FileChange{
		// 修改：a/b/c → a/X/c，净 +1 -1
		{Path: "mod.txt", OldContent: "a\nb\nc\n", NewContent: "a\nX\nc\n"},
		// 新建：整文件计入 insertions
		{Path: "new.txt", OldContent: "", NewContent: "n1\nn2\n", IsNew: true},
		// 删除：整文件计入 deletions
		{Path: "del.txt", OldContent: "d1\nd2\nd3\n", NewContent: "", IsDeleted: true},
		// 无变化（内容逐字相同）⇒ 计 1 个文件，但 0 行
		{Path: "same.txt", OldContent: "s\n", NewContent: "s\n"},
	}

	got := NewTurnDiff(7, "把重试逻辑抽成共享函数", files)

	if got.TurnIndex != 7 || got.PromptPreview != "把重试逻辑抽成共享函数" {
		t.Errorf("TurnIndex/PromptPreview 应透传，实际 %d / %q", got.TurnIndex, got.PromptPreview)
	}
	if len(got.Files) != len(files) {
		t.Errorf("Files 应原样透传，期望 %d 条，实际 %d", len(files), len(got.Files))
	}
	if got.Stats.FilesChanged != len(files) {
		t.Errorf("FilesChanged 应=变更文件数 %d，实际 %d", len(files), got.Stats.FilesChanged)
	}
	// 修改 +1/-1、新建 +2/-0、删除 +0/-3、无变化 +0/-0
	if got.Stats.Insertions != 3 {
		t.Errorf("Insertions 应为 3（修改 1 + 新建 2），实际 %d", got.Stats.Insertions)
	}
	if got.Stats.Deletions != 4 {
		t.Errorf("Deletions 应为 4（修改 1 + 删除 3），实际 %d", got.Stats.Deletions)
	}

	// 与逐个 CountChanges 的合计必须逐字相等（同源证明，不是"看着差不多"）
	var manual Stats
	for _, f := range files {
		a, d := CountChanges(f)
		manual.Insertions += a
		manual.Deletions += d
	}
	if manual.Insertions != got.Stats.Insertions || manual.Deletions != got.Stats.Deletions {
		t.Errorf("NewTurnDiff 与 CountChanges 合计不同源：%+v vs %+v", got.Stats, manual)
	}
}
