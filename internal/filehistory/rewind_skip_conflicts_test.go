package filehistory

import (
	"os"
	"testing"
)

// rewind/restore 对「外部修改」的判据**已存在**（rewind.go diffAndRestore：
// 内容 hash 不在 collectKnownHashes() 中 ⇒ external modification），但修复前该判据
// 只被写进 Warnings、紧接着照样 restoreFile 覆写 —— 可靠信号被降级为警告，
// 而动作是不可逆覆写（违反本仓「信号可靠性 → 权限匹配」原则）。
//
// 用户已拍板：**仅跳过冲突文件、其余照常恢复**，冲突面以**结构化字段**
// 上报（R4：可判定的结构化事实不得由散文断言）。跳过的理由：盖掉他人写入是不可逆
// 数据丢失；回滚不完整是可补救的不一致（冲突文件恰是外部刚改过的，用户知情）。
//
// 判别器：
// ① 外部修改的文件**未被回滚**（内容保持外部版）—— 修复前被恢复成旧快照 ⇒ 红；
// ② RewindResult.Conflicts 结构化列出该文件（新增字段，before 态编译失败属预期，
// 判别性由「只摘 skip 行为」的 mutant 注入补证，见回执）；
// ③ 正向对照：会话自身写入的文件必须照常回滚（gate 不得过度拦截）。

func TestW073_Rewind_SkipsExternallyModifiedFile(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	sessionFile := writeFileHelper(t, workDir, "session_only.go", "base")   // 会话写，无外部干预
	bothFile := writeFileHelper(t, workDir, "externally_modified.go", "v1") // 会话写 + 后被外部改

	h.Track("msg-001", "tc-001", "write_file", []string{sessionFile, bothFile})
	h.TrackAfter("msg-001", "tc-001", []string{sessionFile, bothFile})
	h.Snapshot("msg-001", "first", 1)

	// 会话推进：两个文件都写到 v2（hash 进入本会话 knownHashes ⇒ 不算外部修改）
	if err := os.WriteFile(sessionFile, []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bothFile, []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	h.Track("msg-002", "tc-002", "write_file", []string{sessionFile, bothFile})
	h.TrackAfter("msg-002", "tc-002", []string{sessionFile, bothFile})
	h.Snapshot("msg-002", "second", 2)

	// 外部只改 bothFile（不经 Track/TrackAfter ⇒ hash 不在 knownHashes）
	if err := os.WriteFile(bothFile, []byte("external work"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := h.Rewind("msg-001", nil, "session-001")
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}

	// ① 外部修改的文件必须保持外部版（不被盖掉）
	data, _ := os.ReadFile(bothFile)
	if string(data) != "external work" {
		t.Fatalf(": 外部修改的文件被回滚盖写。\n  期望: external work\n  实得: %q", string(data))
	}

	// ② 结构化冲突面：Conflicts 必须点名该文件
	found := false
	for _, c := range result.Conflicts {
		if c.Path == bothFile {
			found = true
		}
	}
	if !found {
		t.Fatalf(": result.Conflicts 应包含外部修改文件 %s，实得: %+v（warnings=%v）",
			bothFile, result.Conflicts, result.Warnings)
	}

	// ③ 正向对照：会话自身写入必须照常回滚（gate 不得过度拦截）
	data, _ = os.ReadFile(sessionFile)
	if string(data) != "base" {
		t.Fatalf(": 会话自身写入应被正常回滚到 base，实得 %q（gate 过度拦截？）", string(data))
	}

	// ④ 人类可读 warning 保留（既有消费方/既有测试依赖 Warnings 非空）
	if len(result.Warnings) == 0 {
		t.Errorf(": 冲突跳过仍应留 warning（既有消费面）")
	}
}
