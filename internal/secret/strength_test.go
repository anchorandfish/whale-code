// Package secret — strength.go 单元测试（M15-03）。
package secret

import (
	"testing"
)

func TestEvaluateStrength_Basic(t *testing.T) {
	cases := []struct {
		name     string
		secret   string
		minScore Strength
	}{
		{"empty", "", StrengthVeryWeak},
		{"short", "abc", StrengthVeryWeak},
		{"common_password", "password", StrengthVeryWeak},
		{"common_123456", "12345678", StrengthVeryWeak},
		// "abcdefgh" 是典型序列 + 含 "abcde" 弱模式，正确判定为 VeryWeak
		{"weak_sequence", "abcdefgh", StrengthVeryWeak},
		// "abdfgceh" 打乱无序列，长度 8 → Weak
		{"weak_simple", "abdfgceh", StrengthWeak},
		// 12 字符 4 类混合 → Fair
		{"medium_mixed", "Abc123!@#def", StrengthFair},
		// 16+ 字符高熵 → Good
		{"strong_long", "Tr0ub4dor&3RandomSuffix2024", StrengthGood},
		// 20 字符 4 类随机 → Strong（精确 20 字符）
		{"very_strong_20char", "aB3$xY7^kM2@vQ9*Lz5+", StrengthStrong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := EvaluateStrength(tc.secret)
			if report.Score < tc.minScore {
				t.Errorf("EvaluateStrength(%q) score=%v, want >= %v (entropy=%.1f)",
					tc.secret, report.Score, tc.minScore, report.EntropyBits)
			}
			if tc.secret != "" && report.Length == 0 {
				t.Errorf("Length should be > 0 for non-empty input")
			}
		})
	}
}

func TestEvaluateStrength_DetectsWeakPatterns(t *testing.T) {
	cases := []struct {
		name    string
		secret  string
		warning string // 应在 Warnings 中包含此子串
	}{
		{"repeats", "aaaaaa123", "重复"},
		{"sequence_abc", "abcDEF456", "序列"},
		{"common_qwerty", "qwerty12345", "qwerty"},
		{"dict_password", "myPassword123", "password"},
		{"dict_admin", "admin1234567", "admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := EvaluateStrength(tc.secret)
			found := false
			for _, w := range report.Warnings {
				if contains(w, tc.warning) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("EvaluateStrength(%q) warnings should contain %q, got %v",
					tc.secret, tc.warning, report.Warnings)
			}
		})
	}
}

// contains 简单子串检测（避免引入 strings 包）。
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestStrength_String(t *testing.T) {
	cases := []struct {
		s    Strength
		want string
	}{
		{StrengthVeryWeak, "very-weak"},
		{StrengthWeak, "weak"},
		{StrengthFair, "fair"},
		{StrengthGood, "good"},
		{StrengthStrong, "strong"},
	}
	for _, tc := range cases {
		if tc.s.String() != tc.want {
			t.Errorf("Strength(%d).String() = %q, want %q", tc.s, tc.s.String(), tc.want)
		}
	}
}

func TestStrength_Label(t *testing.T) {
	if StrengthVeryWeak.Label() != "极弱" {
		t.Errorf("VeryWeak.Label() = %q, want 极弱", StrengthVeryWeak.Label())
	}
	if StrengthStrong.Label() != "极强" {
		t.Errorf("Strong.Label() = %q, want 极强", StrengthStrong.Label())
	}
}

func TestEvaluateStrength_DiversitySuggestions(t *testing.T) {
	// 缺少大写/符号时应给出建议
	report := EvaluateStrength("abcdefghijk1")
	foundUpper, foundSymbol := false, false
	for _, s := range report.Suggestions {
		if contains(s, "大写") {
			foundUpper = true
		}
		if contains(s, "符号") {
			foundSymbol = true
		}
	}
	if !foundUpper {
		t.Errorf("Suggestions should mention 大写, got %v", report.Suggestions)
	}
	if !foundSymbol {
		t.Errorf("Suggestions should mention 符号, got %v", report.Suggestions)
	}
}

func TestEvaluateStrength_EntropyCalculation(t *testing.T) {
	// 纯小写字母 8 字符（打乱避免序列扣分）：entropy = log2(26) * 8 ≈ 37.6
	report := EvaluateStrength("abdfgceh")
	if report.EntropyBits < 35 || report.EntropyBits > 40 {
		t.Errorf("EntropyBits for 8 lowercase = %.2f, expected 35-40", report.EntropyBits)
	}
	// 4 类字符混合 16 字符（避免连续序列）：entropy = log2(94) * 16 ≈ 104
	report = EvaluateStrength("aB3$xY7^kM2@vQ9*")
	if report.EntropyBits < 80 {
		t.Errorf("EntropyBits for 16 mixed = %.2f, expected > 80", report.EntropyBits)
	}
}
