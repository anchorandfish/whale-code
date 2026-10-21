package session

import (
	"strings"
	"testing"
)

// w049SamePrefixIDPair 返回两个「同一 65.5 秒窗口」内生成的主键（UUIDv7 前 8 位相同）。
//
// 为什么连续两次 GenerateID 就够：主键是 UUIDv7，前 48 bit 是 Unix 毫秒时间戳，
// 占满前 12 位 hex ⇒ 前 8 位 hex 每 2^16 ms（65.536 秒）才跳一次。生产里「在同一分钟
// 内开两个会话」必然落在同一窗口（真机 2026-10-14 01:48:39 / 01:48:50 两次新开会话
// 实测同为 `01a13661`）。失败概率约 1e-5，故 64 轮重试后仍取不到即说明主键形态已变。
func w049SamePrefixIDPair(t *testing.T) (string, string) {
	t.Helper()
	for i := 0; i < 64; i++ {
		a, b := GenerateID(), GenerateID()
		if strings.EqualFold(a[:8], b[:8]) {
			return a, b
		}
	}
	t.Fatal("连续 64 轮都没取到同前缀主键对 —— 主键形态可能已变，" +
		"本判据的前提（v7 时间戳前缀）失效，需重新审视 ")
	return "", ""
}

// TestW049_ShortIDIsRandomTailNotTimestampPrefix 钉住短码的**取值面**。
//
// 时间戳前缀对「区分近期会话」零贡献 —— 前 8 位 hex 在同一个 65.536 秒窗口里恒定，
// 而 8 位随机尾段在几百个会话量级上碰撞概率可忽略。本用例同时排除两种回归：
// 有人把 shortSessionID 改回 `id[:8]`，或改成取主键头部。
func TestW049_ShortIDIsRandomTailNotTimestampPrefix(t *testing.T) {
	a, b := w049SamePrefixIDPair(t)
	if !strings.EqualFold(a[:8], b[:8]) {
		t.Fatalf("前置条件：两个主键应共享前 8 位（时间戳窗口相同），实际 %s / %s", a, b)
	}

	if ShortID(a) == ShortID(b) {
		t.Fatalf("同一时间戳窗口内两个主键的短码相同：%q（a=%s b=%s）"+
			" ⇒ 列表分不清、短码点名必然歧义", ShortID(a), a, b)
	}
	if got, want := ShortID(a), a[len(a)-ShortIDLen:]; got != want {
		t.Errorf("ShortID 应取主键随机尾段：got %q, want %q（主键 %s）", got, want, a)
	}
	if strings.EqualFold(ShortID(a), a[:ShortIDLen]) {
		t.Errorf("短码不得是主键前缀（时间戳区）：%q", ShortID(a))
	}
}

// TestW049_ResolveAcceptsShortCodeWhenPrefixCollides 两个同时间戳窗口的会话：
// - 展示短码各自点名必须命中自己（这是 修复后用户能用的那条路）；
// - 时间戳前缀仍然撞车 ⇒ Resolve 必须**不猜**，报歧义并列出候选。
//
// 后者是既有行为（fail-safe，安全面无损），此前没有判别器钉住 —— 一并补上，
// 免得日后有人「顺手让它自动挑第一个」把错误会话接上来。
func TestW049_ResolveAcceptsShortCodeWhenPrefixCollides(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	idA, idB := w049SamePrefixIDPair(t)
	for _, id := range []string{idA, idB} {
		if err := store.AppendBatchNamed(id, "说你好", []Message{UserText("说你好")}); err != nil {
			t.Fatalf("AppendBatchNamed(%s): %v", id, err)
		}
	}

	// 1) 短码点名：各自命中自己。
	for _, id := range []string{idA, idB} {
		got, err := store.Resolve(ShortID(id))
		if err != nil {
			t.Fatalf("短码 %q（%s）应可点名：%v", ShortID(id), id, err)
		}
		if got != id {
			t.Errorf("短码 %q 解析为 %s，want %s", ShortID(id), got, id)
		}
	}

	// 2) 时间戳前缀点名：必须报歧义 + 列候选，不得替用户猜。
	prefix := idA[:ShortIDLen]
	got, err := store.Resolve(prefix)
	if err == nil {
		t.Fatalf("两个会话共享前缀 %q，Resolve 必须报歧义，实际返回 %q", prefix, got)
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("歧义错误应含 ambiguous，实际：%v", err)
	}
	for _, id := range []string{idA, idB} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("歧义错误应列出候选 %s，实际：%v", id, err)
		}
	}
}

// TestW049_ShortIDKeepsLegacyNamesIntact 旧式会话（文件名即可读名，非 UUID 形状）
// 必须原样返回：截段会把可读名变成无意义片段，也会让 Resolve 的第 4 条回退失效。
func TestW049_ShortIDKeepsLegacyNamesIntact(t *testing.T) {
	const legacy = "旧会话_2026-09-01_09-00-00"
	if got := ShortID(legacy); got != legacy {
		t.Errorf("旧式名应原样返回：got %q, want %q", got, legacy)
	}
}

// TestW049_ResolveShortCodeIsUniqueHandle 短码不得把用户带偏：同一 store 里
// 存在一个「前缀恰好等于另一个会话短码」的会话时，也必须先按短码精确命中。
//
// 构造：idB 的随机尾段 = idA 的主键前缀（人为拼一个合法 UUIDv7 字符串）。
// 这类巧合在真实数据里概率极低，但一旦发生就是「点 A 进 B」——所以要么唯一命中、
// 要么报歧义，绝不能静默选错。本用例锁死「唯一命中」这一半。
func TestW049_ResolveShortCodeIsUniqueHandle(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	idA, _ := w049SamePrefixIDPair(t)
	// idB：v7 形状（第 3 组首位 7、第 4 组首位 8），末组 = 4 位占位 + idA 的前 8 位
	// ⇒ ShortID(idB) == idA 的主键前缀。前缀撞短码，是「点 A 进 B」的构造机。
	tail := idA[:ShortIDLen]
	idB := "01a13661-0000-7000-8000-0000" + tail

	for _, id := range []string{idA, idB} {
		if err := store.AppendBatchNamed(id, "会话"+ShortID(id), []Message{UserText("hi")}); err != nil {
			t.Fatalf("AppendBatchNamed(%s): %v", id, err)
		}
	}

	if ShortID(idB) != tail {
		t.Fatalf("前置条件：idB 的短码应等于 idA 的前缀 %q，实际 %q（idB=%s）", tail, ShortID(idB), idB)
	}

	got, err := store.Resolve(ShortID(idB))
	if err != nil {
		t.Fatalf("短码 %q 应唯一命中 idB：%v", ShortID(idB), err)
	}
	if got != idB {
		t.Errorf("短码 %q 解析为 %s，want idB=%s —— 用户点 A 进了 B", ShortID(idB), got, idB)
	}
}
