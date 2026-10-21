// Package secret — redactor.go C15 接口测试（M15-01/M15-03）。
package secret

import (
	"testing"
)

func TestRedactor_Interface(t *testing.T) {
	// 验证 defaultRedactor 与 SecretRedactor 都满足 Redactor 接口
	var _ Redactor = defaultRedactor{}
	var _ Redactor = SecretRedactor{}
}

func TestDefaultRedactor_Redact(t *testing.T) {
	r := NewRedactor()
	cases := []struct {
		input string
		want  string
	}{
		// OpenAI key
		{"sk-abcdefghijklmnopqrstuvwxyz123456", "[REDACTED]"},
		// AWS access key
		{"AKIAIOSFODNN7EXAMPLE", "[REDACTED]"},
		// GitHub token
		{"ghp_1234567890abcdefghijklmnopqrstuvwxyz", "[REDACTED]"},
		// 无密钥内容
		{"hello world", "hello world"},
		// 空字符串
		{"", ""},
	}
	for _, tc := range cases {
		got := r.Redact(tc.input)
		// [REDACTED] 替换非精确比较（redactMatch 可能保留前缀）
		if tc.want == "[REDACTED]" {
			// 应含 [REDACTED]
			if !contains(got, "[REDACTED]") {
				t.Errorf("Redact(%q) = %q, want contains [REDACTED]", tc.input, got)
			}
		} else if got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestDefaultRedactor_RedactStruct_String(t *testing.T) {
	r := NewRedactor()
	// 直接 string 类型
	out := r.RedactStruct("sk-abcdefghijklmnopqrstuvwxyz123456")
	if s, ok := out.(string); !ok || !contains(s, "[REDACTED]") {
		t.Errorf("RedactStruct(string with key) = %v, want string with [REDACTED]", out)
	}
	// 普通字符串
	out = r.RedactStruct("hello world")
	if s, ok := out.(string); !ok || s != "hello world" {
		t.Errorf("RedactStruct(hello world) = %v, want \"hello world\"", out)
	}
}

func TestDefaultRedactor_RedactStruct_Slice(t *testing.T) {
	r := NewRedactor()
	input := []string{"sk-abcdefghijklmnopqrstuvwxyz123456", "hello"}
	out := r.RedactStruct(input)
	slice, ok := out.([]any)
	if !ok {
		t.Fatalf("RedactStruct([]string) returned %T, want []any", out)
	}
	if len(slice) != 2 {
		t.Fatalf("len = %d, want 2", len(slice))
	}
	if s, ok := slice[0].(string); !ok || !contains(s, "[REDACTED]") {
		t.Errorf("slice[0] = %v, want contains [REDACTED]", slice[0])
	}
	if s, ok := slice[1].(string); !ok || s != "hello" {
		t.Errorf("slice[1] = %v, want \"hello\"", slice[1])
	}
}

func TestDefaultRedactor_RedactStruct_Map(t *testing.T) {
	r := NewRedactor()
	input := map[string]string{
		"token":  "sk-abcdefghijklmnopqrstuvwxyz123456",
		"normal": "hello",
	}
	out := r.RedactStruct(input)
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("RedactStruct(map) returned %T, want map[string]any", out)
	}
	if v, ok := m["token"].(string); !ok || !contains(v, "[REDACTED]") {
		t.Errorf("m[token] = %v, want contains [REDACTED]", m["token"])
	}
	if v, ok := m["normal"].(string); !ok || v != "hello" {
		t.Errorf("m[normal] = %v, want \"hello\"", m["normal"])
	}
}

func TestDefaultRedactor_RedactStruct_Struct(t *testing.T) {
	type Config struct {
		APIKey string
		Name   string
	}
	r := NewRedactor()
	cfg := Config{
		APIKey: "sk-abcdefghijklmnopqrstuvwxyz123456",
		Name:   "myapp",
	}
	out := r.RedactStruct(cfg)
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("RedactStruct(struct) returned %T, want map[string]any", out)
	}
	if v, ok := m["APIKey"].(string); !ok || !contains(v, "[REDACTED]") {
		t.Errorf("m[APIKey] = %v, want contains [REDACTED]", m["APIKey"])
	}
	if v, ok := m["Name"].(string); !ok || v != "myapp" {
		t.Errorf("m[Name] = %v, want \"myapp\"", m["Name"])
	}
}

func TestSecretRedactor_SatisfiesBothInterfaces(t *testing.T) {
	// SecretRedactor 应同时满足 security.Redactor（RedactSensitive 方法）+ secret.Redactor
	r := SecretRedactor{}
	// RedactSensitive 方法
	got := r.RedactSensitive("sk-abcdefghijklmnopqrstuvwxyz123456")
	if !contains(got, "[REDACTED]") {
		t.Errorf("SecretRedactor.RedactSensitive = %q, want contains [REDACTED]", got)
	}
	// Redact 方法（C15 接口）
	got = r.Redact("sk-abcdefghijklmnopqrstuvwxyz123456")
	if !contains(got, "[REDACTED]") {
		t.Errorf("SecretRedactor.Redact = %q, want contains [REDACTED]", got)
	}
	// RedactStruct 方法（C15 接口）
	out := r.RedactStruct("hello")
	if s, ok := out.(string); !ok || s != "hello" {
		t.Errorf("SecretRedactor.RedactStruct(hello) = %v, want \"hello\"", out)
	}
}

func TestGlobalRedactor(t *testing.T) {
	original := globalProjectIgnore
	defer func() { globalProjectIgnore = original }()

	// 默认全局 Redactor 是 defaultRedactor
	r1 := GetGlobalRedactor()
	if r1 == nil {
		t.Fatal("GetGlobalRedactor() should not be nil")
	}

	// 注入自定义 Redactor
	custom := SecretRedactor{}
	SetGlobalRedactor(custom)
	r2 := GetGlobalRedactor()
	if r2 != custom {
		t.Errorf("GetGlobalRedactor() = %v, want %v", r2, custom)
	}

	// 包级便捷函数
	got := Redact("sk-abcdefghijklmnopqrstuvwxyz123456")
	if !contains(got, "[REDACTED]") {
		t.Errorf("Redact() = %q, want contains [REDACTED]", got)
	}
}

func TestRedactMap(t *testing.T) {
	input := map[string]string{
		"token":  "sk-abcdefghijklmnopqrstuvwxyz123456",
		"normal": "hello",
	}
	out := RedactMap(input)
	if out["token"] == input["token"] {
		t.Error("token should be redacted")
	}
	if !contains(out["token"], "[REDACTED]") {
		t.Errorf("token = %q, want contains [REDACTED]", out["token"])
	}
	if out["normal"] != "hello" {
		t.Errorf("normal = %q, want \"hello\"", out["normal"])
	}
}

func TestRedactSlice(t *testing.T) {
	input := []string{
		"sk-abcdefghijklmnopqrstuvwxyz123456",
		"hello",
	}
	out := RedactSlice(input)
	if !contains(out[0], "[REDACTED]") {
		t.Errorf("out[0] = %q, want contains [REDACTED]", out[0])
	}
	if out[1] != "hello" {
		t.Errorf("out[1] = %q, want \"hello\"", out[1])
	}
}

func TestRedactField_FastPath(t *testing.T) {
	// 不含密钥特征前缀 → 快速路径，原值返回
	got := RedactField("hello world")
	if got != "hello world" {
		t.Errorf("RedactField(hello world) = %q, want \"hello world\"", got)
	}
	// 含密钥前缀 → 走完整脱敏
	got = RedactField("sk-abcdefghijklmnopqrstuvwxyz123456")
	if !contains(got, "[REDACTED]") {
		t.Errorf("RedactField(sk-...) = %q, want contains [REDACTED]", got)
	}
	// 空字符串快速返回
	got = RedactField("")
	if got != "" {
		t.Errorf("RedactField(\"\") = %q, want \"\"", got)
	}
}

func TestRedactor_Concurrent(t *testing.T) {
	r := NewRedactor()
	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				_ = r.Redact("sk-abcdefghijklmnopqrstuvwxyz123456")
				_ = r.RedactStruct("hello")
			}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
