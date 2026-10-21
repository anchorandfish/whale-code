// Package tui — 占用闸门在**生产入口**上的判据。
//
// 与 internal/session 的跨进程探针分工：那边证「Store.HoldSession 本身拦得住」；
// 这里证「TUI 真的把闸门接在了用户按得着的入口上」—— /resume 与 /session switch
// 在目标会话被另一个窗口占用时必须**拒绝装载并给出出路**，而不是照旧切过去把
// 两段对话写进同一份 transcript。
//
// 「另一个窗口」用**另一个 Store 实例**表示：flock 是 fd 级，同进程另一个文件描述符
// 与另一个进程的文件描述符在锁语义上等价（仓库既有注释已确认，见
// l1_flock_migration_test.go「同进程另一 fd 也会冲突」）。占用本身不是伪造的，
// 是真锁住了同一个 .lock。
package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// w097TargetSession 是判据用的会话主键（UUID 形状，Resolve 可直接点名）。
const w097TargetSession = "01a13661-75ad-7000-8000-75adc0ffee00"

// w097Prepare 在临时 HOME 下建一份会话，并让「另一个窗口」占用它；
// 返回释放函数。前置条件（另一个窗口真的占上了）不成立时直接失败 ——
// 否则后面的断言会因为「根本没被占」而变成假绿。
func w097Prepare(t *testing.T, tmpHome string) func() {
	t.Helper()
	dir := filepath.Join(tmpHome, ".whale", "sessions")

	writer, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(writer): %v", err)
	}
	sess := session.NewSession()
	sess.SetMessages([]session.Message{session.UserText("hello")})
	if err := writer.Save(w097TargetSession, sess); err != nil {
		t.Fatalf("Save 目标会话: %v", err)
	}

	other, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(other): %v", err)
	}
	if err := other.HoldSession(w097TargetSession); err != nil {
		t.Fatalf("前置条件失败：另一个窗口应能占用该会话，实际: %v", err)
	}
	return other.ReleaseHeldSession
}

// TestW097_ResumeRefusedWhileSessionOpenElsewhere `/resume <id>` 在生产入口被拦下。
func TestW097_ResumeRefusedWhileSessionOpenElsewhere(t *testing.T) {
	tmpHome := newIsolatedTestHome(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	release := w097Prepare(t, tmpHome)
	defer release()

	m := newTUIRegressionModel(t)
	got := m.handleResume(w097TargetSession)

	if got == "" {
		t.Fatal("会话被另一个窗口占用时 /resume 静默成功 —— 闸门没生效（会写串）")
	}
	if !strings.Contains(got, "--fork-session") {
		t.Fatalf("被拒时必须给出出路（--fork-session / 开新会话），实际返回 %q", got)
	}
	// 最关键的一条：绝不能已经切过去。
	if name := m.SessionModel.SessionName(); name == w097TargetSession {
		t.Fatalf("被占用时仍切换到了 %q —— 两个窗口会往同一份 transcript 追加", name)
	}
}

// TestW097_SessionSwitchRefusedWhileSessionOpenElsewhere `/session switch` 同样被拦下。
//
// 单独钉这条的理由：/session switch 与 /resume 是两个入口、历史上各自实现过装载
// 逻辑（现已收敛到 applyResumedSession）。只验一个入口会漏掉另一个没接闸门的情况。
func TestW097_SessionSwitchRefusedWhileSessionOpenElsewhere(t *testing.T) {
	tmpHome := newIsolatedTestHome(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	release := w097Prepare(t, tmpHome)
	defer release()

	m := newTUIRegressionModel(t)
	got := m.handleSessionSwitch(w097TargetSession)

	if got == "" {
		t.Fatal("会话被另一个窗口占用时 /session switch 静默成功 —— 闸门没生效")
	}
	if !strings.Contains(got, "--fork-session") {
		t.Fatalf("被拒时必须给出出路，实际返回 %q", got)
	}
	if name := m.SessionModel.SessionName(); name == w097TargetSession {
		t.Fatalf("被占用时仍切换到了 %q", name)
	}
}

// TestW097_ResumeSucceedsWhenNobodyHoldsSession 无人占用时必须照常恢复。
//
// 这条是**反向判别器**：没有它，「一律拒绝」也能让上面两条通过 ——
// 那样闸门就从「防止写串」变成了「谁都用不了」。
func TestW097_ResumeSucceedsWhenNobodyHoldsSession(t *testing.T) {
	tmpHome := newIsolatedTestHome(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	dir := filepath.Join(tmpHome, ".whale", "sessions")
	writer, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sess := session.NewSession()
	sess.SetMessages([]session.Message{session.UserText("hello")})
	if err := writer.Save(w097TargetSession, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m := newTUIRegressionModel(t)
	if got := m.handleResume(w097TargetSession); got != "" {
		t.Fatalf("无人占用时应能正常恢复，实际返回 %q", got)
	}
	if name := m.SessionModel.SessionName(); name != w097TargetSession {
		t.Fatalf("无人占用时 /resume 应切到 %q，实际 %q", w097TargetSession, name)
	}
}

// TestW097_ResumeSucceedsAfterHolderReleases 持有方释放之后必须立刻能恢复。
//
// 把「拒绝」与「释放」配成一对：证明拒绝**由占用这一个原因**引起，
// 而不是闸门坏掉后的一刀切拒绝（那种情况下释放了也照样进不去）。
func TestW097_ResumeSucceedsAfterHolderReleases(t *testing.T) {
	tmpHome := newIsolatedTestHome(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	release := w097Prepare(t, tmpHome)

	m := newTUIRegressionModel(t)
	if got := m.handleResume(w097TargetSession); got == "" {
		t.Fatal("前置条件失败：被占用时本应被拒")
	}

	release()
	if got := m.handleResume(w097TargetSession); got != "" {
		t.Fatalf("持有方已释放，应当能恢复，实际返回 %q", got)
	}
	if name := m.SessionModel.SessionName(); name != w097TargetSession {
		t.Fatalf("释放后应切到 %q，实际 %q", w097TargetSession, name)
	}
}
