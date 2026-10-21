package tui

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/testutil"
)

// newIsolatedTestHome 创建隔离 HOME（只返回路径，由调用方 `t.Setenv("HOME", ...)`）。
//
// 机制单一事实源在 `internal/testutil.IsolatedHomeDir` —— 本函数只作**包内别名**，
// 让既有调用点与说明保持可读。
//
// 为什么不用 `t.TempDir()`：NewModel 启动的后台 goroutine（filehistory persist /
// usage tracker / memory wiring 的 ForgettingScanner）会在用例结束后仍向 HOME 写文件，
// 与 TempDir 的 RemoveAll 清理断言竞态 ⇒
// `TempDir RemoveAll cleanup: unlinkat .../.whale: directory not empty`（非确定性）。
// 机械门：仓库根 `tempdir_home_gate_test.go` 全仓禁止 `t.TempDir()` 作 HOME。
func newIsolatedTestHome(t *testing.T) string {
	t.Helper()
	return testutil.IsolatedHomeDir(t)
}

// TestResumeLastSession_AfterNewModelValueCopy 复刻 main.go 的真实调用序列：
//
//	model := tui.NewModel(cfg, client, registry)   // NewModel 按值返回
//	if continueSession { model.ResumeLastSession() } // 立即调用，中间无 reassignServices
//
// NewModel 内部 reassignServices() 让接口字段指向 NewModel 的局部变量；
// 按值返回后调用方副本的接口字段仍指向该局部副本（悬空别名）。
// 若 ResumeLastSession 入口不做 reassignServices，续接结果会写进旧副本，
// 真实 model 仍是空会话 —— 用户现象：whale -c 看不到上次历史。
//
// 本测试断言的是**结果**：续接后真实 model 的会话名、历史消息、
// SavedMsgCount（决定下一轮是追加还是重写）必须全部就位。
func TestResumeLastSession_AfterNewModelValueCopy(t *testing.T) {
	tmpHome := newIsolatedTestHome(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	// 模拟上一进程保存的 marker 会话（与人工测试 SESSION-MARKER-2026 同构）
	store, err := session.NewStore(filepath.Join(tmpHome, ".whale", "sessions"))
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	markerMsgs := []session.Message{
		session.UserText("请只回复 SESSION-MARKER-2026。"),
		session.AssistantText("SESSION-MARKER-2026", "test-model", nil),
	}
	markerName := "请只回复 SESSI..._2026-08-31_16-46-05"
	saved := session.NewSession()
	saved.SetMessages(markerMsgs)
	if err := store.Save(markerName, saved); err != nil {
		t.Fatalf("save marker session failed: %v", err)
	}

	// 生产路径：NewModel 按值返回 → 直接 ResumeLastSession（不手动 reassign）
	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	m.ResumeLastSession()

	// 结果断言 1：会话名被真实 model 采纳（否则下一轮 autoNameSession 会另起新会话）
	if got := m.SessionModel.SessionName(); got != markerName {
		t.Fatalf("SessionName 未采纳续接会话，want %q got %q —— 续接结果写进了悬空别名副本", markerName, got)
	}
	// 结果断言 2：历史消息进入真实 model 的当前会话（AI 上下文来源）
	msgs := m.SessionModel.CurrentSession().GetMessages()
	if len(msgs) != len(markerMsgs) {
		t.Fatalf("当前会话消息数错误，want %d got %d —— AI 上下文为空，模型无法回忆 marker", len(markerMsgs), len(msgs))
	}
	if !strings.Contains(msgs[0].TextContent(), "SESSION-MARKER-2026") {
		t.Fatalf("首条消息应含 marker，got %q", msgs[0].TextContent())
	}
	// 结果断言 3：SavedMsgCount 与消息数一致（否则下一轮保存会重复追加全部历史）
	if got := m.SessionModel.SavedMsgCount(); got != len(markerMsgs) {
		t.Fatalf("SavedMsgCount 错误，want %d got %d —— 下一轮保存将重复写历史", len(markerMsgs), got)
	}
	// 结果断言 4：TUI 消息列表应显示续接提示与历史（用户可见结果）
	rendered := m.SessionModel.Messages()
	joined := strings.Join(rendered, "\n")
	if !strings.Contains(joined, "Resumed session") {
		t.Fatalf("TUI 消息列表缺少 Resumed session 提示，got %q", joined)
	}
}

// TestForkLastSession_AfterNewModelValueCopy 同一缺陷类：main.go 在 p.Run()
// 之前调用 ForkLastSession/ForkSessionByID（--fork-session），同样必须先修复别名。
func TestForkLastSession_AfterNewModelValueCopy(t *testing.T) {
	tmpHome := newIsolatedTestHome(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	store, err := session.NewStore(filepath.Join(tmpHome, ".whale", "sessions"))
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	saved := session.NewSession()
	saved.SetMessages([]session.Message{
		session.UserText("请只回复 SESSION-MARKER-2026。"),
		session.AssistantText("SESSION-MARKER-2026", "test-model", nil),
	})
	if err := store.Save("origin-session-2026", saved); err != nil {
		t.Fatalf("save origin session failed: %v", err)
	}

	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	m.ForkLastSession()

	if got := m.SessionModel.SessionName(); got == "" || got == "origin-session-2026" {
		t.Fatalf("Fork 后 SessionName 应为新的 fork ID，got %q", got)
	}
	msgs := m.SessionModel.CurrentSession().GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("Fork 应复制历史消息，want 2 got %d", len(msgs))
	}
}

// captureStdLog 把标准库 log 输出重定向到缓冲区，测试结束恢复。
// lifecycle.go 的 canary 日志走标准库 log（生产被 main.go 重定向到
// ~/.whale/logs/whale.log），因此故障可观测性断言必须捕获这条通道。
func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// brokenStoreDir 故障注入：让 store 的目录路径被同名普通文件占据，
// 迫使 List() 的 MkdirAll/ReadDir 失败（复刻磁盘/权限类故障）。
func brokenStoreDir(t *testing.T, path string) *session.Store {
	t.Helper()
	store, err := session.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("RemoveAll failed: %v", err)
	}
	if err := os.WriteFile(path, []byte("not a dir"), 0644); err != nil {
		t.Fatalf("inject broken dir failed: %v", err)
	}
	return store
}

// TestResumeLastSession_CanaryLogOnListFailure 对抗式审查遗留项：
// ResumeLastSession 中 List() 失败的静默 return 没有任何可观测出口，
// 用户只会看到"无历史"，无法区分"确实没有会话"和"存储故障"。
// 结果断言：故障发生时 must 打 canary 日志（区分"无会话"与"读失败"）。
func TestResumeLastSession_CanaryLogOnListFailure(t *testing.T) {
	tmpHome := newIsolatedTestHome(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	buf := captureStdLog(t)
	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	m.SessionModel.store = brokenStoreDir(t, filepath.Join(tmpHome, "broken-sessions"))

	m.ResumeLastSession()

	out := buf.String()
	if !strings.Contains(out, "ResumeLastSession") || !strings.Contains(out, "list") {
		t.Fatalf("List 失败应打 canary 日志（区分无会话与存储故障），got %q", out)
	}
}

// TestForkLastSession_CanaryLogOnListFailure 同一缺陷类：ForkLastSession 的
// List() 静默 return 无可观测出口。
func TestForkLastSession_CanaryLogOnListFailure(t *testing.T) {
	tmpHome := newIsolatedTestHome(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	buf := captureStdLog(t)
	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	m.SessionModel.store = brokenStoreDir(t, filepath.Join(tmpHome, "broken-sessions"))

	m.ForkLastSession()

	out := buf.String()
	if !strings.Contains(out, "ForkLastSession") || !strings.Contains(out, "list") {
		t.Fatalf("List 失败应打 canary 日志，got %q", out)
	}
}

// TestResumeLastSession_CanaryLogOnLoadFailure List 成功但 Load 失败
// （.jsonl 文件不可读）同样静默 return，用户无法感知会话文件损坏。
// 结果断言：Load 失败时 must 打 canary 日志。
func TestResumeLastSession_CanaryLogOnLoadFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0000 在 Windows 上无权限语义")
	}
	tmpHome := newIsolatedTestHome(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	store, err := session.NewStore(filepath.Join(tmpHome, ".whale", "sessions"))
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	saved := session.NewSession()
	saved.SetMessages([]session.Message{session.UserText("marker")})
	if err := store.Save("perm-denied-session", saved); err != nil {
		t.Fatalf("save session failed: %v", err)
	}
	// 故障注入：会话文件不可读 → List 成功、Load 失败
	if err := os.Chmod(filepath.Join(store.Dir(), "perm-denied-session.jsonl"), 0000); err != nil {
		t.Fatalf("chmod failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(store.Dir(), "perm-denied-session.jsonl"), 0644) })

	buf := captureStdLog(t)
	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	m.SessionModel.store = store

	m.ResumeLastSession()

	out := buf.String()
	if !strings.Contains(out, "ResumeLastSession") || !strings.Contains(out, "load") {
		t.Fatalf("Load 失败应打 canary 日志（会话文件损坏/不可读不可静默），got %q", out)
	}
}
