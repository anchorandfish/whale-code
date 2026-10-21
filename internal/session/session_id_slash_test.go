package session

import (
	"path/filepath"
	"strings"
	"testing"
)

// 复现：用户输入含路径分隔符时会话无法保存。
//
// 现象（2026-09-09 实测）：用户输入 "请执行 rm /tmp/whale-ab/victim.txt"，
// TUI 取前 10 个字符自动命名 → 会话名 "请执行 rm /tm..._2026-09-09_18-33-26"，
// 随后保存报错：
//
//	Save failed: failed to open session file: open
//	/Users/.../.whale/sessions/请执行 rm /tm..._2026-09-09_18-33-26.jsonl:
//	no such file or directory
//
// 根因：sessionPath 用 filepath.Join(dir, sessionID+".jsonl")，
// sessionID 里的 '/' 被当成路径分隔符，"请执行 rm " 被当成不存在的子目录。
//
// 说明：runner 路径（internal/tui/runner_core.go:1295 autoNameSession）已经
// 调 sanitizeFilename 净化过，TUI 路径（internal/tui/commands_session.go）
// 没有 —— 同一功能两份实现发生了漂移。这里在落盘层做兜底，覆盖所有来源。
func TestStore_Append_SessionIDContainingSlash(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// 与真实用户输入生成的会话名一致（含 '/'）
	sessionID := "请执行 rm /tm..._2026-09-09_18-33-26"

	if err := store.Append(sessionID, UserText("hello")); err != nil {
		t.Fatalf("含路径分隔符的会话名导致保存失败（应自动净化）: %v", err)
	}
	if !store.Exists(sessionID) {
		t.Fatalf("保存后 Exists(%q) 应为 true", sessionID)
	}

	sess, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	if len(sess.GetMessages()) == 0 {
		t.Fatal("重新加载后消息为空")
	}
}

// 同源落盘点必须全部净化：主文件之外还有 lock 与 archive sidecar。
//
// 这两个点如果只修主文件会残留同一 bug —— lock 用 O_CREATE 打开，
// sessionID 含 '/' 时报 "failed to create lock file: no such file or directory"；
// archive sidecar 同样会写到不存在的子目录。
func TestStore_SidecarPaths_SessionIDContainingSlash(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	sessionID := "请执行 rm /tm..._2026-09-09_18-33-26"

	// 1) 归档 sidecar 路径必须留在 s.dir 内
	if got := filepath.Dir(store.archivePath(sessionID)); got != dir {
		t.Errorf("archivePath 逃出了会话目录: dir=%q, want %q", got, dir)
	}

	// 2) 加锁必须成功（真实创建 lock 文件）
	lock, err := store.AcquireLock(sessionID)
	if err != nil {
		t.Fatalf("含路径分隔符的会话名导致加锁失败（应自动净化）: %v", err)
	}
	if lock != nil {
		lock.Release()
	}
}

// 净化函数本身的行为契约。
func TestSanitizeSessionID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"请执行 rm /tm...", "请执行 rm _tm..."},
		{"a/b/c", "a_b_c"},
		{`a\b`, "a_b"},
		{"no-change_2026-09-09", "no-change_2026-09-09"},
		{"", ""},
	}
	for _, c := range cases {
		if got := SanitizeSessionID(c.in); got != c.want {
			t.Errorf("SanitizeSessionID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// 净化结果不得再包含分隔符，否则 filepath.Join 仍会误判
	if strings.ContainsAny(SanitizeSessionID("a/b\\c:d"), `/\:`) {
		t.Error("净化结果仍包含路径分隔符")
	}
}
