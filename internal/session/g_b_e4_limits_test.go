// g_b_e4_limits_test.go — G-B（O-B）E4 判别性测试：session 资源硬上限（I11 资源有界）。
//
// 决策依据：集中拍板_决策表与并行修复分组_2026-09-14.md §2 E4 行（S18-G5/G6）：
//
//	E4 | session 无消息数/大小/总数上限、无年龄 GC；memory 条目上限待确认
//	   | **补硬上限**（与 E1/E2 同族）
//
// 语义：上限是「安全阀」，触及时**明确失败**（类型化错误、不写半条消息），
// 而不是静默截断对话历史。断言全部走生产写入口（Append / AppendBatch / Save）。
package session

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// withLimits 临时收紧上限并自动恢复（上限是包级策略变量，测试内不可并行）。
func withLimits(t *testing.T, messages, bytes, sessions int) {
	t.Helper()
	oldM, oldB, oldS := MaxSessionMessages, MaxSessionBytes, MaxSessions
	MaxSessionMessages, MaxSessionBytes, MaxSessions = messages, bytes, sessions
	t.Cleanup(func() {
		MaxSessionMessages, MaxSessionBytes, MaxSessions = oldM, oldB, oldS
	})
}

func bigText(n int) string { return strings.Repeat("x", n) }

// TestG_B_E4_AppendRejectsBeyondByteCap —— 单会话字节上限（增量追加路径）。
func TestG_B_E4_AppendRejectsBeyondByteCap(t *testing.T) {
	withLimits(t, 0, 2048, 0) // 字节上限 2KiB
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "e4-bytes-1"

	if err := store.Append(id, UserText("small")); err != nil {
		t.Fatalf("首条消息不应被拒：%v", err)
	}
	before, err := os.Stat(store.sessionPath(id))
	if err != nil {
		t.Fatal(err)
	}

	err = store.Append(id, UserText(bigText(4096)))
	if err == nil {
		t.Fatal("E4：超过单会话字节上限的追加必须被拒绝")
	}
	var limitErr *SessionLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("E4：期望 *SessionLimitError，实际 %T (%v)", err, err)
	}
	if limitErr.Kind != SessionLimitBytes {
		t.Fatalf("E4：Kind 错误：%q", limitErr.Kind)
	}
	if limitErr.Limit <= 0 || limitErr.Actual <= limitErr.Limit {
		t.Fatalf("E4：上限/实际值不合理：limit=%d actual=%d", limitErr.Limit, limitErr.Actual)
	}

	// 拒绝必须是「不落盘」而不是「写了半条」
	after, err := os.Stat(store.sessionPath(id))
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("E4：被拒写的消息不得落盘（%d -> %d 字节）", before.Size(), after.Size())
	}

	// 上限不是「一次越界即永久锁死」：小消息仍可继续写
	if err := store.Append(id, UserText("ok")); err != nil {
		t.Fatalf("未越界的小消息仍应可写：%v", err)
	}
}

// TestG_B_E4_AppendBatchRejectsAtomicallyBeyondByteCap —— 批量写入必须整体拒绝（all-or-nothing）。
func TestG_B_E4_AppendBatchRejectsAtomicallyBeyondByteCap(t *testing.T) {
	withLimits(t, 0, 2048, 0)
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "e4-batch-1"

	err = store.AppendBatch(id, []Message{
		UserText("first"),
		UserText(bigText(8192)),
	})
	if err == nil {
		t.Fatal("E4：超限批量追加必须被拒绝")
	}
	var limitErr *SessionLimitError
	if !errors.As(err, &limitErr) || limitErr.Kind != SessionLimitBytes {
		t.Fatalf("E4：期望 bytes 类型的 *SessionLimitError，实际 %T (%v)", err, err)
	}
	if _, statErr := os.Stat(store.sessionPath(id)); !os.IsNotExist(statErr) {
		t.Fatalf("E4：批量被拒时不得留下任何已写消息（文件不应存在）：%v", statErr)
	}
}

// TestG_B_E4_SaveRejectsBeyondMessageCap —— 单会话消息数上限（整会话写路径）。
func TestG_B_E4_SaveRejectsBeyondMessageCap(t *testing.T) {
	withLimits(t, 3, 0, 0) // 消息数上限 3
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "e4-messages-1"

	over := NewSession()
	for i := 0; i < 4; i++ {
		over.AddMessage(UserText("m"))
	}
	err = store.Save(id, over)
	if err == nil {
		t.Fatal("E4：超过单会话消息数上限的保存必须被拒绝")
	}
	var limitErr *SessionLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("E4：期望 *SessionLimitError，实际 %T (%v)", err, err)
	}
	if limitErr.Kind != SessionLimitMessages {
		t.Fatalf("E4：Kind 错误：%q", limitErr.Kind)
	}
	if _, statErr := os.Stat(store.sessionPath(id)); !os.IsNotExist(statErr) {
		t.Fatalf("E4：被拒的保存不得落盘：%v", statErr)
	}

	// 恰好等于上限仍应成功（边界不得差一）
	exact := NewSession()
	for i := 0; i < 3; i++ {
		exact.AddMessage(UserText("m"))
	}
	if err := store.Save(id, exact); err != nil {
		t.Fatalf("E4：等于上限的会话必须仍可保存（边界差一）：%v", err)
	}
	if _, err := store.Load(id); err != nil {
		t.Fatalf("E4：边界内的会话必须可加载：%v", err)
	}
}

// TestG_B_E4_NewSessionRefusedBeyondSessionCountCap —— 会话总数上限（新建路径）。
func TestG_B_E4_NewSessionRefusedBeyondSessionCountCap(t *testing.T) {
	withLimits(t, 0, 0, 2) // 会话总数上限 2
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Append("e4-count-a", UserText("a")); err != nil {
		t.Fatalf("第 1 个会话应可创建：%v", err)
	}
	if err := store.Append("e4-count-b", UserText("b")); err != nil {
		t.Fatalf("第 2 个会话应可创建：%v", err)
	}

	err = store.Append("e4-count-c", UserText("c"))
	if err == nil {
		t.Fatal("E4：超过会话总数上限的新会话必须被拒绝")
	}
	var limitErr *SessionLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("E4：期望 *SessionLimitError，实际 %T (%v)", err, err)
	}
	if limitErr.Kind != SessionLimitCount {
		t.Fatalf("E4：Kind 错误：%q", limitErr.Kind)
	}
	if _, statErr := os.Stat(store.sessionPath("e4-count-c")); !os.IsNotExist(statErr) {
		t.Fatalf("E4：被拒的新会话不得落盘：%v", statErr)
	}

	// 上限只约束「新建」，不得把已有会话变成只读
	if err := store.Append("e4-count-a", UserText("a2")); err != nil {
		t.Fatalf("E4：已有会话的追加不得被会话总数上限阻塞：%v", err)
	}
}

// TestG_B_E4_DefaultLimitsAllowNormalUse —— 反例守卫：默认上限不得误伤正常使用。
func TestG_B_E4_DefaultLimitsAllowNormalUse(t *testing.T) {
	// 默认值必须是「安全阀」量级：正常规模的会话远在其下。
	if MaxSessionMessages <= 0 || MaxSessionBytes <= 0 || MaxSessions <= 0 {
		t.Fatalf("E4：默认上限必须为正数：messages=%d bytes=%d sessions=%d",
			MaxSessionMessages, MaxSessionBytes, MaxSessions)
	}
	if MaxSessionMessages < 10_000 {
		t.Fatalf("E4：单会话消息数默认上限过小（%d），会误伤正常长会话", MaxSessionMessages)
	}
	if MaxSessionBytes < 64<<20 {
		t.Fatalf("E4：单会话字节默认上限过小（%d），会误伤含工具输出的正常会话", MaxSessionBytes)
	}
	if MaxSessions < 100 {
		t.Fatalf("E4：会话总数默认上限过小（%d）", MaxSessions)
	}

	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "e4-default-1"
	if err := store.Append(id, UserText("hello")); err != nil {
		t.Fatalf("默认上限下常规追加被误拒：%v", err)
	}
	sess := NewSession()
	sess.AddMessage(UserText("a"))
	sess.AddMessage(UserText("b"))
	if err := store.Save(id, sess); err != nil {
		t.Fatalf("默认上限下常规保存被误拒：%v", err)
	}
	if _, err := store.Load(id); err != nil {
		t.Fatalf("默认上限下常规加载失败：%v", err)
	}
}
