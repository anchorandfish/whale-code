// Package config — model_registry_test.go 模型能力注册表测试（候选 B）。
package config

import (
	"testing"
)

// TestLookupModelCapabilities_BuiltinHit 验证内置注册表命中。
func TestLookupModelCapabilities_BuiltinHit(t *testing.T) {
	defer ClearUserRegistry()

	tests := []struct {
		name       string
		model      string
		wantEffort bool
		wantMax    bool
	}{
		{"opus-4 完整匹配", "opus-4-20250514", true, true},
		{"opus 前缀匹配", "opus-3-20240229", true, false},
		{"sonnet-4 前缀匹配", "claude-sonnet-4-5-20250514", true, false},
		{"sonnet 前缀匹配（不支持 effort）", "claude-3-5-sonnet-20241022", false, false},
		{"gpt-4 前缀匹配", "gpt-4-turbo", true, false},
		{"deepseek-reasoner 完整匹配", "deepseek-reasoner", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caps, found := LookupModelCapabilities(tt.model)
			if !found {
				t.Fatalf("LookupModelCapabilities(%q) found=false, want true (builtin hit)", tt.model)
			}
			if caps.SupportsEffort != tt.wantEffort {
				t.Errorf("LookupModelCapabilities(%q).SupportsEffort = %v, want %v",
					tt.model, caps.SupportsEffort, tt.wantEffort)
			}
			if caps.SupportsMaxEffort != tt.wantMax {
				t.Errorf("LookupModelCapabilities(%q).SupportsMaxEffort = %v, want %v",
					tt.model, caps.SupportsMaxEffort, tt.wantMax)
			}
		})
	}
}

// TestLookupModelCapabilities_NotFoundZeroValue 验证未命中返回零值 + found=false。
func TestLookupModelCapabilities_NotFoundZeroValue(t *testing.T) {
	defer ClearUserRegistry()

	caps, found := LookupModelCapabilities("unknown-model-xyz")
	if found {
		t.Error("LookupModelCapabilities(unknown) found=true, want false")
	}
	if caps != (ModelCapabilities{}) {
		t.Errorf("LookupModelCapabilities(unknown) = %+v, want zero value", caps)
	}
}

// TestLookupModelCapabilities_UserOverride 验证用户注册表覆盖内置。
func TestLookupModelCapabilities_UserOverride(t *testing.T) {
	defer ClearUserRegistry()

	// 注册用户自定义模型能力
	RegisterModelCapabilities("my-custom-model", ModelCapabilities{
		SupportsEffort:    true,
		SupportsMaxEffort: true,
	})

	caps, found := LookupModelCapabilities("my-custom-model-v2")
	if !found {
		t.Fatal("用户注册表 my-custom-model 应命中 (found=true)")
	}
	if !caps.SupportsEffort {
		t.Error("用户注册表 my-custom-model SupportsEffort should be true")
	}
	if !caps.SupportsMaxEffort {
		t.Error("用户注册表 my-custom-model SupportsMaxEffort should be true")
	}
}

// TestLookupModelCapabilities_UserOverridesBuiltin 验证用户注册表覆盖内置注册表。
func TestLookupModelCapabilities_UserOverridesBuiltin(t *testing.T) {
	defer ClearUserRegistry()

	// 内置 opus-4 支持 max effort
	// 用户覆盖为不支持 max effort
	RegisterModelCapabilities("opus-4", ModelCapabilities{
		SupportsEffort:    true,
		SupportsMaxEffort: false, // 覆盖内置的 true
	})

	caps, found := LookupModelCapabilities("opus-4-20250514")
	if !found {
		t.Fatal("用户覆盖后 opus-4 应命中 (found=true)")
	}
	if !caps.SupportsEffort {
		t.Error("用户覆盖后 opus-4 SupportsEffort should be true")
	}
	if caps.SupportsMaxEffort {
		t.Error("用户覆盖后 opus-4 SupportsMaxEffort should be false (user override)")
	}
}

// TestLookupModelCapabilities_LongestPrefixMatch 验证最长前缀匹配优先
// （落实审查建议优化 2：避免 "sonnet-4" 误匹配 "sonnet-40"）。
func TestLookupModelCapabilities_LongestPrefixMatch(t *testing.T) {
	defer ClearUserRegistry()

	// 注册两个前缀：sonnet-4 和 sonnet-40（假设）
	// sonnet-40 应优先匹配（最长前缀）
	RegisterModelCapabilities("sonnet-4", ModelCapabilities{
		SupportsEffort:    true,
		SupportsMaxEffort: false,
	})
	RegisterModelCapabilities("sonnet-40", ModelCapabilities{
		SupportsEffort:    false,
		SupportsMaxEffort: false,
	})

	// "sonnet-40-xxx" 应匹配 "sonnet-40"（最长前缀），SupportsEffort=false
	caps, found := LookupModelCapabilities("sonnet-40-future")
	if !found {
		t.Fatal("sonnet-40-future 应命中 (found=true)")
	}
	if caps.SupportsEffort {
		t.Error("sonnet-40-future should match sonnet-40 (longest prefix), SupportsEffort=false")
	}

	// "sonnet-4-5" 应匹配 "sonnet-4"（sonnet-4 是最长匹配前缀），SupportsEffort=true
	caps2, found2 := LookupModelCapabilities("sonnet-4-5")
	if !found2 {
		t.Fatal("sonnet-4-5 应命中 (found=true)")
	}
	if !caps2.SupportsEffort {
		t.Error("sonnet-4-5 should match sonnet-4 (longest prefix), SupportsEffort=true")
	}
}

// TestRegisterModelCapabilitiesFromMap 验证批量注册。
func TestRegisterModelCapabilitiesFromMap(t *testing.T) {
	defer ClearUserRegistry()

	m := map[string]ModelCapabilities{
		"model-a": {SupportsEffort: true},
		"model-b": {SupportsNativeSearch: true},
	}
	RegisterModelCapabilitiesFromMap(m)

	capsA, foundA := LookupModelCapabilities("model-a-v1")
	if !foundA {
		t.Fatal("model-a-v1 应命中 (found=true)")
	}
	if !capsA.SupportsEffort {
		t.Error("model-a SupportsEffort should be true")
	}
	capsB, foundB := LookupModelCapabilities("model-b-v1")
	if !foundB {
		t.Fatal("model-b-v1 应命中 (found=true)")
	}
	if !capsB.SupportsNativeSearch {
		t.Error("model-b SupportsNativeSearch should be true")
	}
}

// TestClearUserRegistry 验证清空用户注册表。
func TestClearUserRegistry(t *testing.T) {
	RegisterModelCapabilities("temp-model", ModelCapabilities{SupportsEffort: true})
	ClearUserRegistry()

	// 清空后应未命中（found=false），且由于内置注册表无 temp-model，返回零值
	caps, found := LookupModelCapabilities("temp-model")
	if found {
		t.Error("after ClearUserRegistry, temp-model should not be found (found=false)")
	}
	if caps != (ModelCapabilities{}) {
		t.Errorf("after ClearUserRegistry, temp-model should return zero value, got %+v", caps)
	}
}
