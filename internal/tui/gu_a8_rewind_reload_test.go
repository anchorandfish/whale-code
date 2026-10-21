package tui

// gu_a8_rewind_reload_test.go — A8（移交单 G-B-001）在 `internal/tui` 一侧的验收。
//
// A8 改判（`集中拍板…§2` A8）：`session.Load` 从 fail-open（跳过损坏行）改为
// **fail-closed T2**（拒绝加载 + 隔离 + **显著告警**）。
//
// ⚠️ **本文件的判别性边界（先说清，避免误读）**：
//
//	G-B-001 的票面反向判据是「构造含损坏行的会话文件 → 走 rewind 路径 → 必须不再出现成功文案」。
//	**该判据在 before 态就已经是绿的** —— 实测（本文件 TestGU_A8_CorruptSessionRewindIsLoud）：
//	损坏会话在 `fh.Rewind` 的**第一次** `Load`（经 `sessionRestorerAdapter.LoadMessages`）就失败，
//	`SessionRestored` 保持 false，于是三个静默分支**根本不会进入**，输出里已经带着
//	`Session restored: false` 与隔离路径的告警。
//	⇒ 那三处 `if loadErr == nil` 是**不可达**的静默吞错（详见回执异议），故：
//	   - `..._ReloadAppliesFreshSession` / `..._CorruptSessionRewindIsLoud` 是**回归守卫**（两态皆绿，非修复证明）；
//	   - `..._ReloadFailureIsNotSilent` 直接打新 helper 的契约（唯一能证明"去掉静默吞错"的判据）。
//
// 生产入口：`Model.doRewind`（经 `/rewind` 分发 → `handleRewind`），不直调 fh 内部函数。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// guA8Setup 经生产入口构造 Model，并用**产品自身的快照 API** 造一个可用快照。
func guA8Setup(t *testing.T, sid string) (Model, string, string) {
	t.Helper()
	m, home := guModelIsolatedHome(t)
	m.sessionService.SetSessionName(sid)

	fh := m.toolService.FileHistory()
	if fh == nil {
		t.Fatal("前置失败：Model 未装配 filehistory")
	}
	ws := m.toolService.Executor().WorkDir()
	target := filepath.Join(ws, "gu_a8_target.txt")
	if err := os.WriteFile(target, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := fh.Track("msg-001", "tc-001", "write_file", []string{target}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := fh.TrackAfter("msg-001", "tc-001", []string{target}); err != nil {
		t.Fatalf("TrackAfter: %v", err)
	}
	if _, err := fh.Snapshot("msg-001", "gu-a8", 1); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return m, home, target
}

// guA8SessionPath 给出会话文件路径（按 store 的公开命名契约拼接）。
func guA8SessionPath(home, sid string) string {
	return filepath.Join(home, ".whale", "sessions", session.SanitizeSessionID(sid)+".jsonl")
}

// guA8TwoMsgSession 造一条有 2 条消息的会话（快照 SessionMsgCount=1 ⇒ 回退后应截到 1 条）。
func guA8TwoMsgSession(t *testing.T, m Model, sid string) {
	t.Helper()
	st := m.sessionService.Store()
	if st == nil {
		t.Fatal("前置失败：Model 未装配 session store")
	}
	sess := session.NewSession()
	sess.AddMessage(session.Message{Role: "user", Blocks: []session.ContentBlock{{Type: "text", Text: "one"}}})
	sess.AddMessage(session.Message{Role: "assistant", Blocks: []session.ContentBlock{{Type: "text", Text: "two"}}})
	if err := st.Save(sid, sess); err != nil {
		t.Fatalf("save session: %v", err)
	}
}

// TestGU_A8_RewindReloadAppliesFreshSession 回归守卫（两态皆绿）：
// 成功路径上，回退后内存里的当前会话必须**已被重新加载**（= apply 真的执行了），
// 而不是停留在回退前的旧内容。若哪天 reload 被整段摘掉，本用例变红。
func TestGU_A8_RewindReloadAppliesFreshSession(t *testing.T) {
	const sid = "gu-a8-ok"
	m, _, _ := guA8Setup(t, sid)
	guA8TwoMsgSession(t, m, sid)

	// 让内存里的当前会话与磁盘不同步（模拟"回退前"的旧内容）。
	stale := session.NewSession()
	stale.AddMessage(session.Message{Role: "user", Blocks: []session.ContentBlock{{Type: "text", Text: "stale"}}})
	m.sessionService.SetCurrentSession(stale)

	out := m.doRewind("msg-001")
	t.Logf("A8 成功路径输出=%q", out)

	if !strings.Contains(out, "Session restored: true") {
		t.Fatalf("成功路径应报告 Session restored: true；got %q", out)
	}
	got := len(m.sessionService.CurrentSession().GetMessages())
	if got != 1 {
		t.Fatalf("回退后内存会话应已重载为截断后的 1 条，got %d（说明 reload 没有真正执行）", got)
	}
}

// TestGU_A8_CorruptSessionRewindIsLoud 回归守卫 + **票面前提实测**（两态皆绿）：
// 损坏的当前会话在 rewind 上表现为"拒绝 + 隔离路径可见"，而不是"静默成功"。
//
// 这条用例的作用是**证伪 G-B-001 的反向判据**（见文件头）：before 态输出里
// 已经同时有 `Session restored: false` 和隔离路径 ⇒ 不存在"用户以为回退成功"。
func TestGU_A8_CorruptSessionRewindIsLoud(t *testing.T) {
	const sid = "gu-a8-corrupt"
	m, home, _ := guA8Setup(t, sid)
	guA8TwoMsgSession(t, m, sid)

	// 追加一行损坏内容（A8 语义下 ⇒ Load 必须拒绝并隔离）。
	f, err := os.OpenFile(guA8SessionPath(home, sid), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString("{not json}\n"); err != nil {
		t.Fatalf("append corrupt line: %v", err)
	}
	_ = f.Close()

	out := m.doRewind("msg-001")
	t.Logf("A8 损坏路径输出=%q", out)

	if strings.Contains(out, "Session restored: true") {
		t.Errorf("A8：损坏会话不得被报告为成功回退；got %q", out)
	}
	if !strings.Contains(out, "quarantine") {
		t.Errorf("A8：拒绝加载必须把**隔离路径**讲给用户（显著告警）；got %q", out)
	}
	if !strings.Contains(out, "fail-closed") {
		t.Errorf("A8：告警须点明 fail-closed 语义；got %q", out)
	}
}

// TestGU_A8_ReloadFailureIsNotSilent 新 helper 的契约（唯一直接判据）：
// 加载失败必须①返回可读告警（含结构化隔离路径）②**不得**应用 reload
// ③不得把错误吞成 nil。
func TestGU_A8_ReloadFailureIsNotSilent(t *testing.T) {
	dir := t.TempDir()
	st, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	const sid = "gu-a8-helper"
	if err := st.Save(sid, session.NewSession()); err != nil {
		t.Fatalf("save: %v", err)
	}
	p := filepath.Join(dir, session.SanitizeSessionID(sid)+".jsonl")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString("{not json}\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	applied := false
	warn := rewindReloadAfterRestore(st, sid, func(*session.Session) { applied = true })

	if warn == "" {
		t.Fatal("A8：加载失败被静默吞掉（warn 为空）—— 正是本单要消掉的 fail-open 报错形态")
	}
	if applied {
		t.Error("A8：加载失败却仍然应用了 reload（拿零值会话顶替内存会话）")
	}
	if !strings.Contains(warn, "quarantine") {
		t.Errorf("A8：告警须含结构化隔离路径（errors.As 取 QuarantinePath，非散文匹配）；got %q", warn)
	}
	if !strings.Contains(warn, "NOT refreshed") {
		t.Errorf("A8：告警须明说内存会话**未被刷新**，否则用户仍以为回退成功；got %q", warn)
	}
}
