package session

import (
	"strings"
	"testing"
)

// w049SameTailIDPair 构造两个「末 8 位 hex 完全相同」的合法主键。
//
// 为什么需要这个构造（第二轮）：第一轮把短码从「前 8 位（UUIDv7 时间戳）」
// 改成「末 8 位（随机）」后，碰撞从「同分钟必撞」降为「生日碰撞」——但**概率非零**：
// 8 位 hex = 32 bit，n 个会话的碰撞概率约 n²/2³³（135 个 ≈ 2e-6，1000 个 ≈ 1.2e-4）。
// 一旦真的撞上，列表又会退回「两行一模一样的 [xxxx]」= 的原症状。
// 「随机」只能压低概率，**不能给出保证**；保证必须由「按需加长到组内不撞」来出。
// 本对主键就是把那个低概率事件确定性地造出来，用来钉住「撞了会怎样」。
func w049SameTailIDPair() (string, string) {
	// 前 3 组照 UUIDv7 形态；末组前 4 位不同、后 8 位相同 ⇒ 末 8 位 hex 相同。
	return "01a13661-75ad-7223-8dca-1111deadbeef",
		"01a13661-75ad-7223-8dca-2222deadbeef"
}

// TestW049_HandlesGuaranteeDistinctWithinGroup 两个最短短码真的撞车的会话，
// 句柄必须被**加长到互不相同** —— 这是「保证」的落点，不是「概率」。
func TestW049_HandlesGuaranteeDistinctWithinGroup(t *testing.T) {
	a, b := w049SameTailIDPair()
	if ShortID(a) != ShortID(b) {
		t.Fatalf("前置条件：两个主键的最短短码应相同，实际 %q / %q（%s / %s）",
			ShortID(a), ShortID(b), a, b)
	}

	h := Handles([]string{a, b})
	if h[a] == h[b] {
		t.Fatalf("最短短码撞车时 Handles 仍给出相同句柄 [%s] ⇒ 列表分不清（与  原症状相同）", h[a])
	}
	for _, id := range []string{a, b} {
		hh := h[id]
		if len(hh) <= ShortIDLen {
			t.Errorf("撞车时会话 %s 的句柄应加长（> %d 位），实际 %q（%d 位）",
				id, ShortIDLen, hh, len(hh))
		}
		// 句柄必须仍是该主键的**尾段** —— Resolve 的句柄匹配就是这么判的。
		flat := strings.ReplaceAll(strings.ToLower(id), "-", "")
		if !strings.HasSuffix(flat, strings.ToLower(hh)) {
			t.Errorf("句柄 %q 不是主键 %s 的尾段 ⇒ Resolve 认不出来", hh, id)
		}
	}
}

// TestW049_HandlesKeepsShortestWhenNoCollision 没撞车就别加长：
// 句柄长度是「组内最短可用」，不是固定值 —— 否则列表行宽会无谓变长。
func TestW049_HandlesKeepsShortestWhenNoCollision(t *testing.T) {
	a, _ := w049SameTailIDPair()
	other := "01a13661-75ad-7223-8dca-abcdef012345" // 末 8 位 ef012345 ≠ deadbeef

	if got := Handles([]string{a})[a]; got != ShortID(a) {
		t.Errorf("单个会话无需加长：got %q, want %q", got, ShortID(a))
	}
	h := Handles([]string{a, other})
	if h[a] != ShortID(a) || h[other] != ShortID(other) {
		t.Errorf("无碰撞时应保持最短句柄：a=%q(want %q) other=%q(want %q)",
			h[a], ShortID(a), h[other], ShortID(other))
	}
}

// TestW049_HandlesLegacyNamesPassThrough 旧式可读名不参与 hex 尾段计算，原样返回。
func TestW049_HandlesLegacyNamesPassThrough(t *testing.T) {
	const legacy = "旧会话_2026-09-01_09-00-00"
	a, _ := w049SameTailIDPair()
	h := Handles([]string{legacy, a})
	if h[legacy] != legacy {
		t.Errorf("旧式名应原样返回：got %q, want %q", h[legacy], legacy)
	}
}

// TestW049_ResolveAcceptsWidenedHandle 列表里印出来的**加长**句柄，
// 回填给 Resolve 必须命中自己 —— 这是「用户照抄列表就能用」的保证。
func TestW049_ResolveAcceptsWidenedHandle(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	a, b := w049SameTailIDPair()
	for _, id := range []string{a, b} {
		if err := store.AppendBatchNamed(id, "说你好", []Message{UserText("说你好")}); err != nil {
			t.Fatalf("AppendBatchNamed(%s): %v", id, err)
		}
	}

	h := Handles([]string{a, b})
	for _, id := range []string{a, b} {
		got, err := store.Resolve(h[id])
		if err != nil {
			t.Fatalf("加长句柄 %q（%s）应可点名：%v", h[id], id, err)
		}
		if got != id {
			t.Errorf("加长句柄 %q 解析为 %s，want %s", h[id], got, id)
		}
	}

	// 最短短码此时必然歧义 —— 这正是「必须加长」的理由，且 Resolve 不猜。
	if got, err := store.Resolve(ShortID(a)); err == nil {
		t.Errorf("两个会话最短短码相同，Resolve 必须报歧义，实际返回 %q", got)
	}
}
