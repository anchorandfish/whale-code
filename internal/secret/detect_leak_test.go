// Package secret — DetectLeakWithLabels 单元测试（D7-2 重构，验证共用检测入口）。
package secret

import (
	"strings"
	"testing"
)

// TestDetectLeakWithLabels_APIKey 验证含 Anthropic API Key 的文本被检测命中。
// 输入：sk-ant-api03- + 30 个字符（满足 sk-ant-[A-Za-z0-9_-]{20,} 规则，总长 35+ 字符）。
// 期望：hit=true，labels 含 "Anthropic API Key"（规则 anthropic_api_key 的 Label）。
func TestDetectLeakWithLabels_APIKey(t *testing.T) {
	// sk-ant- 前缀后接 30 个字符，满足 {20,} 量词；总长 13+30=43 字符（35+）。
	content := "sk-ant-api03-" + strings.Repeat("a", 30)
	labels, hit := DetectLeakWithLabels(content)
	if !hit {
		t.Fatal("含 Anthropic API Key 的文本应被检测命中")
	}
	// labels 返回的是人类可读 Label（"Anthropic API Key"），不是 RuleID（"anthropic_api_key"）。
	if !strings.Contains(labels, "Anthropic API Key") {
		t.Errorf("labels 应含 'Anthropic API Key'，实际: %q", labels)
	}
}

// TestDetectLeakWithLabels_PAT 验证含 GitHub PAT 的文本被检测命中。
// 输入：ghp_ + 36 个字符（满足 (ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36} 规则）。
// 期望：hit=true，labels 含 "GitHub Token"（规则 github_token 的 Label）。
func TestDetectLeakWithLabels_PAT(t *testing.T) {
	// ghp_ + 36 个字母数字字符，满足 [A-Za-z0-9]{36} 量词。
	content := "ghp_" + strings.Repeat("a", 36)
	labels, hit := DetectLeakWithLabels(content)
	if !hit {
		t.Fatal("含 GitHub PAT 的文本应被检测命中")
	}
	// labels 返回的是人类可读 Label（"GitHub Token"），不是 RuleID（"github_token"）。
	if !strings.Contains(labels, "GitHub Token") {
		t.Errorf("labels 应含 'GitHub Token'，实际: %q", labels)
	}
}

// TestDetectLeakWithLabels_Normal 验证正常文本不被误报为密钥泄露。
// 输入：普通 Go 代码 "package main"。
// 期望：hit=false, labels=""。
func TestDetectLeakWithLabels_Normal(t *testing.T) {
	labels, hit := DetectLeakWithLabels("package main")
	if hit {
		t.Errorf("正常文本不应被误报为密钥泄露，labels: %q", labels)
	}
	if labels != "" {
		t.Errorf("未命中时 labels 应为空字符串，实际: %q", labels)
	}
}

// TestDetectLeakWithLabels_Empty 验证空字符串不命中且不 panic。
// 输入：""。
// 期望：hit=false, labels=""。
func TestDetectLeakWithLabels_Empty(t *testing.T) {
	labels, hit := DetectLeakWithLabels("")
	if hit {
		t.Errorf("空字符串不应命中，labels: %q", labels)
	}
	if labels != "" {
		t.Errorf("空字符串命中时 labels 应为空，实际: %q", labels)
	}
}

// TestDetectLeakWithLabels_NoPanic 验证超长字符串（1MB）不触发 panic（fail-open 兜底）。
// 输入：1MB 的普通文本（无密钥）。
// 期望：不 panic；hit=false（无密钥）；labels=""。
// 注：即使内部正则引擎对超长输入异常，defer recover 也应兜底返回 ("", false)。
func TestDetectLeakWithLabels_NoPanic(t *testing.T) {
	// 构造 1MB 普通文本（1 << 20 = 1048576 字节）。
	large := strings.Repeat("a", 1<<20)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("超长字符串不应 panic，实际 recover: %v", r)
		}
	}()
	labels, hit := DetectLeakWithLabels(large)
	if hit {
		t.Errorf("1MB 普通文本不应命中密钥，labels: %q", labels)
	}
	if labels != "" {
		t.Errorf("未命中时 labels 应为空，实际: %q", labels)
	}
}
