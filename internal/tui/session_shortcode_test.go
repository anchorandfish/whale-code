package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// w049SamePrefixIDPair 返回两个「同一 65.5 秒窗口」内生成的主键（UUIDv7 前 8 位相同）。
//
// 为什么这样构造： 的症状是「同一分钟内开的几个会话短码一模一样」。
// 主键是 UUIDv7，前 48 bit 是 Unix 毫秒时间戳、占满前 12 位 hex ⇒ 前 8 位 hex
// 每 2^16 ms = 65.536 秒才跳一次。因此「连续两次 GenerateID 落在同一前缀窗口」
// 不是人造场景，而是生产里的常态（真机 2026-10-14 01:48:39 / 01:48:50 两次
// 新开会话实测同为 `01a13661`）。
func w049SamePrefixIDPair(t *testing.T) (string, string) {
	t.Helper()
	for i := 0; i < 64; i++ {
		a, b := session.GenerateID(), session.GenerateID()
		if strings.EqualFold(a[:8], b[:8]) {
			return a, b
		}
	}
	t.Fatal("连续 64 轮都没取到同前缀主键对 —— 主键形态可能已变，" +
		"本判据的前提（v7 时间戳前缀）失效，需重新审视 ")
	return "", ""
}

// w049ShortCodes 从 /session list 的输出里摘出「含 name 的行」的展示短码（方括号内）。
func w049ShortCodes(out, name string) []string {
	var codes []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, name) {
			continue
		}
		open := strings.Index(line, "[")
		closeIdx := strings.Index(line, "]")
		if open < 0 || closeIdx <= open {
			continue
		}
		codes = append(codes, line[open+1:closeIdx])
	}
	return codes
}

// TestW049_SessionListShortCodeIdentifiesSession 列表里的短码必须能「指认」一个会话。
//
// 走的生产入口：`/session list` 的渲染函数 handleSessions → formatSessionLine
// （唯一实现，与 /sessions 交互选择器共用同一行格式）。判据摘的就是用户看到的
// 那个字符串，再原样回填给 store.Resolve —— 与用户「看到短码 → /resume <短码>」
// 的动作逐字一致。
//
// 修前（真机 2026-10-14 01:48 实录）两个会话都显示 `[01a13661]`：
// - 列表面：两行短码相同 ⇒ 用户无法靠它分辨；
// - 点名面：用该短码 Resolve 必然撞上「多个候选」歧义错 ⇒ 短码事实上不可用。
//
// 本用例同时钉住修复后的两个性质：短码互不相同，且回填后各自命中自己。
func TestW049_SessionListShortCodeIdentifiesSession(t *testing.T) {
	m := isolatedHomeModel(t)
	store := m.sessionService.Store()
	if store == nil {
		t.Fatal("前置条件：Model 应持有 session store")
	}

	idA, idB := w049SamePrefixIDPair(t)
	if !strings.EqualFold(idA[:8], idB[:8]) {
		t.Fatalf("前置条件：两个主键应共享前 8 位，实际 %s / %s", idA, idB)
	}
	for _, id := range []string{idA, idB} {
		if err := store.AppendBatchNamed(id, "说你好",
			[]session.Message{session.UserText("说你好")}); err != nil {
			t.Fatalf("AppendBatchNamed(%s): %v", id, err)
		}
	}

	out := m.handleSessions()
	codes := w049ShortCodes(out, "说你好")
	if len(codes) != 2 {
		t.Fatalf("应渲染 2 行「说你好」会话，实际 %d 行：\n%s", len(codes), out)
	}

	if codes[0] == codes[1] {
		t.Fatalf("两个会话的展示短码相同（ 原症状）：[%s] ⇒ 列表分不清\n列表：\n%s",
			codes[0], out)
	}

	// 把列表里看到的短码原样回填，必须各自命中自己。
	got := make(map[string]string, 2)
	for _, c := range codes {
		resolved, err := store.Resolve(c)
		if err != nil {
			t.Fatalf("短码 [%s] 点名失败（用户照抄列表却选不中）：%v", c, err)
		}
		got[c] = resolved
	}
	if got[codes[0]] == got[codes[1]] {
		t.Fatalf("两个短码都解析到同一会话 %s", got[codes[0]])
	}
	want := map[string]bool{idA: true, idB: true}
	for c, resolved := range got {
		if !want[resolved] {
			t.Errorf("短码 [%s] 解析到 %s，不在两个已建会话 {A,B} 内", c, resolved)
		}
	}
}
