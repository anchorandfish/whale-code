package keybindings

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/charmbracelet/bubbletea"
)

// TestResolverConcurrentParse 并发安全测试（开发 prompt §3 破坏性反例 #2：并发场景）
// 验证多 goroutine 并发调用 Parse 时不崩溃、不死锁、不数据竞争
func TestResolverConcurrentParse(t *testing.T) {
	bindings := DefaultVimBindings()
	resolver := NewResolver(bindings, ReservedKeys())

	keys := []Key{
		{Type: tea.KeyRunes, Runes: []rune{'j'}},
		{Type: tea.KeyRunes, Runes: []rune{'k'}},
		{Type: tea.KeyRunes, Runes: []rune{'d'}},
		{Type: tea.KeyRunes, Runes: []rune{'g'}},
		{Type: tea.KeyCtrlC},
		{Type: tea.KeyCtrlD},
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune{'x'}},
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := keys[(id+j)%len(keys)]
				mode := "normal"
				if j%3 == 0 {
					mode = "insert"
				}
				_ = resolver.Parse(key, mode)
			}
		}(i)
	}
	wg.Wait()
}

// TestResolverConcurrentParseAndSetPending 并发 Parse + SetPending
// 验证读写锁正确性（RWMutex：Parse 读锁，SetPending 写锁）
func TestResolverConcurrentParseAndSetPending(t *testing.T) {
	bindings := DefaultVimBindings()
	resolver := NewResolver(bindings, ReservedKeys())

	var wg sync.WaitGroup
	// 100 个 goroutine 并发 Parse
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := Key{Type: tea.KeyRunes, Runes: []rune{'d'}}
				_ = resolver.Parse(key, "normal")
			}
		}()
	}
	// 10 个 goroutine 并发 SetPending
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				resolver.SetPending("d")
				resolver.SetPending("")
			}
		}()
	}
	// 10 个 goroutine 并发 Reset
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				resolver.Reset()
			}
		}()
	}
	wg.Wait()
}

// TestManagerConcurrentParseAndReload 并发 Parse + Reload
// 验证 Manager 级别的读写安全（Reload 重建 Resolver，Parse 代理到旧 Resolver）
func TestManagerConcurrentParseAndReload(t *testing.T) {
	mgr, err := NewManager("", "")
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	key := Key{Type: tea.KeyRunes, Runes: []rune{'j'}}
	var wg sync.WaitGroup

	// 50 个 goroutine 并发 Parse
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = mgr.Parse(key, "normal")
			}
		}()
	}
	// 5 个 goroutine 并发 Reload
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := mgr.Reload(); err != nil {
					t.Errorf("Reload failed: %v", err)
				}
			}
		}()
	}
	wg.Wait()
}

// TestResolverB1Regression B1 专项回归测试
// 验证 insert 模式下字母 d/g 不被误判为序列键首键
func TestResolverB1Regression(t *testing.T) {
	bindings := DefaultVimBindings()
	resolver := NewResolver(bindings, ReservedKeys())

	t.Run("insert_mode_d_passthrough", func(t *testing.T) {
		// 先确保 pending 清空
		resolver.Reset()
		d := Key{Type: tea.KeyRunes, Runes: []rune{'d'}}
		action := resolver.Parse(d, "insert")
		if action.Type != ActionPassthrough {
			t.Errorf("insert 模式字母 d: Type = %v, want ActionPassthrough（B1 修复：insert 不应误判序列键）", action.Type)
		}
		// 验证 pending 未被设置
		if resolver.GetPending() != "" {
			t.Errorf("insert 模式字母 d 后 pending = %q, want 空", resolver.GetPending())
		}
	})

	t.Run("insert_mode_g_passthrough", func(t *testing.T) {
		resolver.Reset()
		g := Key{Type: tea.KeyRunes, Runes: []rune{'g'}}
		action := resolver.Parse(g, "insert")
		if action.Type != ActionPassthrough {
			t.Errorf("insert 模式字母 g: Type = %v, want ActionPassthrough（B1 修复）", action.Type)
		}
	})

	t.Run("normal_mode_d_sequence_pending", func(t *testing.T) {
		resolver.Reset()
		d := Key{Type: tea.KeyRunes, Runes: []rune{'d'}}
		action := resolver.Parse(d, "normal")
		if action.Type != ActionSequencePending {
			t.Errorf("normal 模式字母 d: Type = %v, want ActionSequencePending", action.Type)
		}
	})

	t.Run("insert_mode_c_passthrough", func(t *testing.T) {
		// 'c' 不在任何绑定中，应 passthrough
		resolver.Reset()
		c := Key{Type: tea.KeyRunes, Runes: []rune{'c'}}
		action := resolver.Parse(c, "insert")
		if action.Type != ActionPassthrough {
			t.Errorf("insert 模式字母 c: Type = %v, want ActionPassthrough", action.Type)
		}
	})

	t.Run("insert_mode_dd_not_triggered", func(t *testing.T) {
		// insert 模式连按 dd 不应触发 delete_line
		resolver.Reset()
		d1 := Key{Type: tea.KeyRunes, Runes: []rune{'d'}}
		d2 := Key{Type: tea.KeyRunes, Runes: []rune{'d'}}
		_ = resolver.Parse(d1, "insert")
		action2 := resolver.Parse(d2, "insert")
		if action2.Type == ActionVimDelete {
			t.Errorf("insert 模式 dd 不应触发 delete_line（B1 修复），got ActionVimDelete")
		}
	})
}

// TestResolverEmptyBindings 空绑定表边界测试（开发 prompt §3 边界条件）
func TestResolverEmptyBindings(t *testing.T) {
	resolver := NewResolver(nil, nil)

	key := Key{Type: tea.KeyRunes, Runes: []rune{'j'}}
	action := resolver.Parse(key, "normal")
	if action.Type != ActionPassthrough {
		t.Errorf("空绑定表: Type = %v, want ActionPassthrough", action.Type)
	}
}

// TestResolverNilReserved 空保留键集合边界测试
func TestResolverNilReserved(t *testing.T) {
	resolver := NewResolver(DefaultVimBindings(), nil)

	// 无保留键时 ctrl+c 应走正常绑定表（不存在则 passthrough）
	ctrlC := Key{Type: tea.KeyCtrlC}
	action := resolver.Parse(ctrlC, "normal")
	// ctrl+c 不在绑定表，应 passthrough（无保留键时）
	if action.Type != ActionPassthrough {
		t.Errorf("无保留键时 ctrl+c: Type = %v, want ActionPassthrough", action.Type)
	}
}

// TestResolverSequenceTimeout 序列键超时（第二个键不匹配）
func TestResolverSequenceTimeout(t *testing.T) {
	bindings := DefaultVimBindings()
	resolver := NewResolver(bindings, ReservedKeys())

	t.Run("d_then_j_clears_pending", func(t *testing.T) {
		resolver.Reset()
		d := Key{Type: tea.KeyRunes, Runes: []rune{'d'}}
		j := Key{Type: tea.KeyRunes, Runes: []rune{'j'}}

		// d 进入 pending
		action1 := resolver.Parse(d, "normal")
		if action1.Type != ActionSequencePending {
			t.Fatalf("d: Type = %v, want ActionSequencePending", action1.Type)
		}

		// j 应触发 cursor_down（pending 清空后单键匹配）
		action2 := resolver.Parse(j, "normal")
		if action2.Type != ActionVimMotion {
			t.Errorf("d→j: Type = %v, want ActionVimMotion", action2.Type)
		}
		if action2.Motion != "cursor_down" {
			t.Errorf("d→j: Motion = %v, want cursor_down", action2.Motion)
		}
		if resolver.GetPending() != "" {
			t.Errorf("d→j 后 pending = %q, want 空", resolver.GetPending())
		}
	})
}

// TestParseStringInvalidFormat 非法格式边界测试（开发 prompt §3 破坏性反例 #1：错误输入）
func TestParseStringInvalidFormat(t *testing.T) {
	invalidInputs := []string{
		"",
		"   ",
		"ctrl+",
		"+d",
		"ctrl++",
		"abc",
		"ctrl+ab",
	}

	for _, input := range invalidInputs {
		t.Run(fmt.Sprintf("invalid_%q", input), func(t *testing.T) {
			_, err := ParseString(input)
			if err == nil {
				t.Errorf("ParseString(%q) 应返回错误，实际 nil", input)
			}
		})
	}
}

// TestValidateConfigExtremeConfig 超长配置边界测试（开发 prompt §3 破坏性反例 #1：超长输入）
func TestValidateConfigExtremeConfig(t *testing.T) {
	// 构造 1000 条绑定
	var bindings []Binding
	for i := 0; i < 1000; i++ {
		bindings = append(bindings, Binding{
			Key:    fmt.Sprintf("ctrl+%c", 'a'+(i%26)),
			Action: "cursor_down",
			Mode:   "normal",
		})
	}
	cfg := Config{Mode: "custom", Bindings: bindings}
	errs := ValidateConfig(cfg)
	// 应检测到冲突（同 key 不同 action 不会，但同 key 同 action 多条不算冲突）
	// 主要验证不崩溃、不超时
	_ = errs
}

// TestErrorIDIntegration 验证 keybindings 错误接入 N6 ErrorID 命名空间
// D7 修复：所有 keybindings 错误应携带可提取的 ErrorID
func TestErrorIDIntegration(t *testing.T) {
	t.Run("ParseString error has ErrorID", func(t *testing.T) {
		_, err := ParseString("")
		if err == nil {
			t.Fatal("ParseString('') error = nil, want error")
		}
		id := ai.GetErrorID(err)
		if id != ai.ErrKeybindingsInvalid {
			t.Errorf("ParseString('') ErrorID = %q, want %q", id, ai.ErrKeybindingsInvalid)
		}
	})

	t.Run("ValidateConfig error has ErrorID", func(t *testing.T) {
		cfg := Config{Mode: "invalid_mode"}
		errs := ValidateConfig(cfg)
		if len(errs) == 0 {
			t.Fatal("ValidateConfig() errors empty, want at least 1")
		}
		id := ai.GetErrorID(errs[0])
		if id != ai.ErrKeybindingsInvalid {
			t.Errorf("ValidateConfig() ErrorID = %q, want %q", id, ai.ErrKeybindingsInvalid)
		}
	})

	t.Run("LoadFromFile path error has ErrorID", func(t *testing.T) {
		tmpDir := t.TempDir()
		outsidePath := filepath.Join(tmpDir, "outside.json")
		os.WriteFile(outsidePath, []byte(`{"mode":"vim"}`), 0644)

		_, err := LoadFromFile(outsidePath)
		if err == nil {
			t.Fatal("LoadFromFile() error = nil, want error for path outside .whale/")
		}
		id := ai.GetErrorID(err)
		if id != ai.ErrKeybindingsLoad {
			t.Errorf("LoadFromFile() ErrorID = %q, want %q", id, ai.ErrKeybindingsLoad)
		}
	})
}
