// eval_config_test.go — M25-04 fast model 可配置测试。
//
// 覆盖 SetEvalModel/GetEvalModel 的并发安全 + 默认值 + 空值防护。

package goal

import (
	"sync"
	"testing"
)

// TestSetEvalModel_DefaultValue 验证 NewManager 后默认值为 claude-3-haiku。
func TestSetEvalModel_DefaultValue(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)

	got := m.GetEvalModel()
	want := "claude-3-haiku"
	if got != want {
		t.Errorf("GetEvalModel() default = %q, want %q", got, want)
	}
}

// TestSetEvalModel_ValidModel 验证设置有效模型后 GetEvalModel 返回新值。
func TestSetEvalModel_ValidModel(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)

	m.SetEvalModel("gemini-flash")
	got := m.GetEvalModel()
	want := "gemini-flash"
	if got != want {
		t.Errorf("GetEvalModel() after SetEvalModel = %q, want %q", got, want)
	}
}

// TestSetEvalModel_EmptyStringIgnored 验证空字符串被忽略（保留当前值）。
func TestSetEvalModel_EmptyStringIgnored(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)

	m.SetEvalModel("deepseek-chat")
	m.SetEvalModel("") // 空字符串应被忽略
	got := m.GetEvalModel()
	want := "deepseek-chat"
	if got != want {
		t.Errorf("GetEvalModel() after empty SetEvalModel = %q, want %q (should be ignored)", got, want)
	}
}

// TestSetEvalModel_ConcurrentSafety 验证并发调用 SetEvalModel 不 panic（锁保护）。
func TestSetEvalModel_ConcurrentSafety(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)

	models := []string{"gemini-flash", "deepseek-chat", "claude-3-haiku", "gpt-4o-mini"}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			m.SetEvalModel(models[idx%len(models)])
		}(i)
	}
	wg.Wait()

	// 验证最终值是 models 中的某一个（不 panic 即通过）
	got := m.GetEvalModel()
	valid := false
	for _, model := range models {
		if got == model {
			valid = true
			break
		}
	}
	if !valid {
		t.Errorf("GetEvalModel() after concurrent set = %q, not in expected set", got)
	}
}

// TestSetEvalModel_MultipleChanges 验证多次设置最终值为最后一次有效设置。
func TestSetEvalModel_MultipleChanges(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)

	m.SetEvalModel("model-a")
	m.SetEvalModel("model-b")
	m.SetEvalModel("") // 忽略
	m.SetEvalModel("model-c")

	got := m.GetEvalModel()
	want := "model-c"
	if got != want {
		t.Errorf("GetEvalModel() after multiple changes = %q, want %q", got, want)
	}
}
