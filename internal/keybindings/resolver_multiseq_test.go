// resolver_multiseq_test.go — M11-01 多键序列测试（收尾补全）。
//
// 覆盖 6 个测试：3+ 键序列正常路径 / 超时清空 / 部分键不在前缀表 / 并发安全 /
// sequencePrefix 构建 / Resolver 模式切换（Resolve vs Pending）。
//
// 对标 VSCode chord 多键序列 @https://code.visualstudio.com/docs/getstarted/keybindings#_chords @2026-07-21。
package keybindings

import (
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbletea"
)

// TestResolver_MultiKeySequence_HappyPath 验证 3+ 键序列正常路径：sp-k 触发 save-prefix 场景。
//
// 收尾修复验证：原 Parse 仅在 bindings 命中时返回 Action，sequenceKey 不在 bindings
// 但属于更长序列前缀时直接清空 pendingKeys，导致 3+ 键序列无法累积。本测试通过
// "sp-k" → delete_line 的 4 键序列验证修复后的累积逻辑。
func TestResolver_MultiKeySequence_HappyPath(t *testing.T) {
	// 自定义 4 键序列绑定：sp-k → delete_line（模拟 save-prefix 场景）
	bindings := []Binding{
		{Key: "sp-k", Action: "delete_line", Mode: "normal"},
	}
	resolver := NewResolver(bindings, nil)

	keys := []Key{
		{Type: tea.KeyRunes, Runes: []rune{'s'}},
		{Type: tea.KeyRunes, Runes: []rune{'p'}},
		{Type: tea.KeyRunes, Runes: []rune{'-'}},
		{Type: tea.KeyRunes, Runes: []rune{'k'}},
	}

	// 前 3 键应进入 ActionSequencePending（sp / sp- 是 sp-k 的前缀）
	for i, k := range keys[:3] {
		action := resolver.Parse(k, "normal")
		if action.Type != ActionSequencePending {
			t.Fatalf("key[%d] %q: Type = %v, want ActionSequencePending", i, k.String(), action.Type)
		}
	}

	// 第 4 键 k 完整匹配 sp-k，应返回 ActionVimDelete（delete_line）
	finalAction := resolver.Parse(keys[3], "normal")
	if finalAction.Type != ActionVimDelete {
		t.Errorf("final k: Type = %v, want ActionVimDelete", finalAction.Type)
	}
	if finalAction.Motion != "delete_line" {
		t.Errorf("final k: Motion = %q, want delete_line", finalAction.Motion)
	}
	// 完成后 pending 应清空
	if got := resolver.GetPending(); got != "" {
		t.Errorf("after sp-k: pending = %q, want empty", got)
	}
}

// TestResolver_MultiKeySequence_Timeout 验证序列超时后清空 pendingKeys。
//
// 构造 lastKeyTime 距今 > 500ms 且 pendingKeys 非空的状态，再次按 "d" 时
// 超时分支应清空 pendingKeys，使第二个 "d" 重新作为序列首键进入 pending
// 而非完成 "dd" 触发 delete_line。
func TestResolver_MultiKeySequence_Timeout(t *testing.T) {
	bindings := DefaultVimBindings()
	resolver := NewResolver(bindings, ReservedKeys())

	// 直接构造超时状态：1 秒前按过 "d"，pendingKeys=["d"]
	resolver.mu.Lock()
	resolver.lastKeyTime = time.Now().Add(-1 * time.Second)
	resolver.pendingKeys = []string{"d"}
	resolver.pendingMode = "normal"
	resolver.mu.Unlock()

	// 再次按 "d"：超时应清空 pending，"d" 重新作为首键进入 pending
	d := Key{Type: tea.KeyRunes, Runes: []rune{'d'}}
	action := resolver.Parse(d, "normal")

	if action.Type != ActionSequencePending {
		t.Errorf("after timeout, second d: Type = %v, want ActionSequencePending (pending cleared, re-entered)", action.Type)
	}
	if action.Type == ActionVimDelete {
		t.Error("after timeout, dd should NOT trigger delete_line (pending was cleared by timeout)")
	}
	// pending 应只含当前 d（旧的 d 被清空）
	if got := resolver.GetPending(); got != "d" {
		t.Errorf("after timeout, pending = %q, want \"d\" (fresh first key)", got)
	}
}

// TestResolver_MultiKeySequence_PartialNoMatch 验证部分键不在前缀表时清空 pendingKeys。
//
// 进入 "sp-k" 序列的 "s" pending 后，按 "x"（sx 不在 sequencePrefix 表），
// pending 应被清空，"x" 走单键匹配（无绑定 → ActionPassthrough）。
func TestResolver_MultiKeySequence_PartialNoMatch(t *testing.T) {
	bindings := []Binding{
		{Key: "sp-k", Action: "delete_line", Mode: "normal"},
	}
	resolver := NewResolver(bindings, nil)

	s := Key{Type: tea.KeyRunes, Runes: []rune{'s'}}
	x := Key{Type: tea.KeyRunes, Runes: []rune{'x'}}

	// s 进入 pending
	action1 := resolver.Parse(s, "normal")
	if action1.Type != ActionSequencePending {
		t.Fatalf("s: Type = %v, want ActionSequencePending", action1.Type)
	}

	// x 不构成任何序列前缀，pending 应清空
	action2 := resolver.Parse(x, "normal")
	if action2.Type != ActionPassthrough {
		t.Errorf("x after s: Type = %v, want ActionPassthrough (sx not in prefix table)", action2.Type)
	}
	if got := resolver.GetPending(); got != "" {
		t.Errorf("after x: pending = %q, want empty (cleared)", got)
	}
}

// TestResolver_MultiKeySequence_ConcurrentSafe 验证并发 Parse 不 panic（-race 通过）。
//
// 多 goroutine 并发按多键序列键 + 普通键，验证 Resolver.mu 保护 pendingKeys 读写。
func TestResolver_MultiKeySequence_ConcurrentSafe(t *testing.T) {
	bindings := []Binding{
		{Key: "sp-k", Action: "delete_line", Mode: "normal"},
		{Key: "j", Action: "cursor_down", Mode: "normal"},
	}
	resolver := NewResolver(bindings, nil)

	keys := []Key{
		{Type: tea.KeyRunes, Runes: []rune{'s'}},
		{Type: tea.KeyRunes, Runes: []rune{'p'}},
		{Type: tea.KeyRunes, Runes: []rune{'-'}},
		{Type: tea.KeyRunes, Runes: []rune{'k'}},
		{Type: tea.KeyRunes, Runes: []rune{'j'}},
		{Type: tea.KeyRunes, Runes: []rune{'x'}},
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				k := keys[(id+j)%len(keys)]
				_ = resolver.Parse(k, "normal")
				// 偶尔重置以制造不同的 pending 起点
				if j%10 == 0 {
					resolver.Reset()
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestResolver_SequencePrefix_Build 验证 BuildSequencePrefix 正确构建多键前缀表。
//
// 注册 "sp-k"（4 键）应记录 "s"/"sp"/"sp-" 三个前缀（按 mode 隔离）。
// 注册 "dd"（2 键）应记录 "d" 前缀。
func TestResolver_SequencePrefix_Build(t *testing.T) {
	bindings := []Binding{
		{Key: "sp-k", Action: "delete_line", Mode: "normal"},
		{Key: "dd", Action: "delete_line", Mode: "normal"},
		{Key: "gg", Action: "goto_top", Mode: "normal"},
	}
	resolver := NewResolver(bindings, nil)

	// sp-k 应记录 s / sp / sp- 前缀（normal mode）
	wantPrefixes := []string{"s", "sp", "sp-"}
	for _, p := range wantPrefixes {
		modes, ok := resolver.sequencePrefix[p]
		if !ok {
			t.Errorf("sequencePrefix[%q] not registered, want registered", p)
			continue
		}
		if !modes["normal"] {
			t.Errorf("sequencePrefix[%q][normal] = false, want true", p)
		}
		// insert mode 不应被注册（B1 修复：按 mode 隔离）
		if modes["insert"] {
			t.Errorf("sequencePrefix[%q][insert] = true, want false (B1 mode isolation)", p)
		}
	}

	// dd 应记录 d 前缀
	if modes, ok := resolver.sequencePrefix["d"]; !ok || !modes["normal"] {
		t.Errorf("sequencePrefix[d][normal] not registered for dd binding")
	}

	// gg 应记录 g 前缀
	if modes, ok := resolver.sequencePrefix["g"]; !ok || !modes["normal"] {
		t.Errorf("sequencePrefix[g][normal] not registered for gg binding")
	}

	// 完整键 "sp-k" 不应作为前缀注册（前缀是 keyStr[:i] for i in [1, len-1]）
	if _, ok := resolver.sequencePrefix["sp-k"]; ok {
		t.Error("sequencePrefix[sp-k] should not be registered (full key not a prefix)")
	}
}

// TestResolver_ResolverMode_ResolveVsPending 验证 Resolve vs Pending 模式切换正确性。
//
// normal 模式：d 是序列首键 → ActionSequencePending
// insert 模式：d 应 passthrough（B1 修复，sequencePrefix 按 mode 隔离）
// pendingMode 不匹配时：序列匹配不触发，走单键路径
func TestResolver_ResolverMode_ResolveVsPending(t *testing.T) {
	bindings := DefaultVimBindings()
	resolver := NewResolver(bindings, ReservedKeys())

	d := Key{Type: tea.KeyRunes, Runes: []rune{'d'}}

	// normal 模式：d 是 dd 序列首键 → ActionSequencePending
	resolver.Reset()
	actionNormal := resolver.Parse(d, "normal")
	if actionNormal.Type != ActionSequencePending {
		t.Errorf("normal d: Type = %v, want ActionSequencePending", actionNormal.Type)
	}
	if got := resolver.GetPending(); got != "d" {
		t.Errorf("normal d: pending = %q, want \"d\"", got)
	}

	// insert 模式：d 应 passthrough（B1 修复）
	resolver.Reset()
	actionInsert := resolver.Parse(d, "insert")
	if actionInsert.Type != ActionPassthrough {
		t.Errorf("insert d: Type = %v, want ActionPassthrough (B1 mode isolation)", actionInsert.Type)
	}
	if got := resolver.GetPending(); got != "" {
		t.Errorf("insert d: pending = %q, want empty (no sequence in insert mode)", got)
	}

	// 模式切换：normal → d (pending, pendingMode=normal) → insert → j
	// 序列匹配因 pendingMode != "insert" 跳过，j 在 insert 模式无绑定 → passthrough
	resolver.Reset()
	_ = resolver.Parse(d, "normal") // pendingMode = "normal"
	j := Key{Type: tea.KeyRunes, Runes: []rune{'j'}}
	actionSwitch := resolver.Parse(j, "insert")
	if actionSwitch.Type != ActionPassthrough {
		t.Errorf("insert j after normal d: Type = %v, want ActionPassthrough (pendingMode mismatch, sequence skipped)", actionSwitch.Type)
	}

	// pendingMode 不匹配后，第二个 d（normal）应能继续完成 dd（验证 pending 未被错误清空）
	resolver.Reset()
	_ = resolver.Parse(d, "normal")         // pendingMode = "normal"
	_ = resolver.Parse(j, "insert")         // pendingMode 不匹配，序列不触发，但 pendingKeys 保留
	actionDD := resolver.Parse(d, "normal") // 回到 normal，应能完成 dd
	// 期望 dd 完成 delete_line（pendingKeys=["d"] 在 insert 调用时未触发序列匹配，未被清空）
	if actionDD.Type != ActionVimDelete {
		t.Errorf("normal d after pending: Type = %v, want ActionVimDelete (dd completed)", actionDD.Type)
	}
	if actionDD.Motion != "delete_line" {
		t.Errorf("normal d after pending: Motion = %q, want delete_line", actionDD.Motion)
	}
}
