package github

import (
	"strings"
	"testing"
)

// TestLegalSeverities 验证 LegalSeverities 返回完整的三级枚举且顺序稳定。
func TestLegalSeverities(t *testing.T) {
	got := LegalSeverities()
	want := []Severity{SeverityBlocker, SeverityWarning, SeveritySuggestion}
	if len(got) != len(want) {
		t.Fatalf("LegalSeverities 长度不匹配：期望 %d，实际 %d", len(want), len(got))
	}
	for i, s := range want {
		if got[i] != s {
			t.Errorf("LegalSeverities[%d] 不匹配：期望 %s，实际 %s", i, s, got[i])
		}
	}
}

// TestValidateSeverity_Valid 校验三个合法严重度均通过 ValidateSeverity。
func TestValidateSeverity_Valid(t *testing.T) {
	for _, s := range LegalSeverities() {
		if err := ValidateSeverity(s); err != nil {
			t.Errorf("合法严重度 %s 不应返回错误，实际 %v", s, err)
		}
	}
}

// TestValidateSeverity_Invalid 校验非法严重度返回错误且错误信息含三级提示。
func TestValidateSeverity_Invalid(t *testing.T) {
	invalids := []Severity{"", "critical", "high", "low", "none", "INFO", "blocker "}
	for _, s := range invalids {
		err := ValidateSeverity(s)
		if err == nil {
			t.Errorf("非法严重度 %q 应返回错误", s)
			continue
		}
		// 错误信息应包含三级提示，便于调用方定位契约
		if !strings.Contains(err.Error(), "blocker") || !strings.Contains(err.Error(), "warning") || !strings.Contains(err.Error(), "suggestion") {
			t.Errorf("错误信息应包含三级提示，实际：%v", err)
		}
	}
}

// TestParseSeverity_Valid 校验合法字符串可严格解析（含大小写/空白容错）。
func TestParseSeverity_Valid(t *testing.T) {
	cases := []struct {
		in   string
		want Severity
	}{
		{"blocker", SeverityBlocker},
		{"WARNING", SeverityWarning},
		{"  Suggestion  ", SeveritySuggestion},
		{"Blocker", SeverityBlocker},
	}
	for _, c := range cases {
		got, err := ParseSeverity(c.in)
		if err != nil {
			t.Errorf("ParseSeverity(%q) 不应报错：%v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSeverity(%q) = %s，期望 %s", c.in, got, c.want)
		}
	}
}

// TestParseSeverity_Invalid 校验非法字符串严格返回错误（不静默回退）。
func TestParseSeverity_Invalid(t *testing.T) {
	invalids := []string{"critical", "info", "high", "", "???"}
	for _, in := range invalids {
		got, err := ParseSeverity(in)
		if err == nil {
			t.Errorf("ParseSeverity(%q) 应返回错误，实际返回 %s（静默回退）", in, got)
		}
	}
}

// TestValidateFinding_Valid 校验符合契约的 Finding 通过校验。
func TestValidateFinding_Valid(t *testing.T) {
	cases := []Finding{
		{Severity: SeverityBlocker, File: "a.go", Line: 10, Title: "nil pointer"},
		{Severity: SeverityWarning, File: "b.go", Line: 0, Title: "unused var"},
		{Severity: SeveritySuggestion, File: "c.go", Line: 1, Title: "rename", Body: "", Suggestion: ""},
	}
	for i, f := range cases {
		if err := ValidateFinding(f); err != nil {
			t.Errorf("用例 %d 不应报错：%v", i, err)
		}
	}
}

// TestValidateFinding_Invalid 校验各类违反契约的 Finding 均返回错误。
func TestValidateFinding_Invalid(t *testing.T) {
	cases := []struct {
		name    string
		finding Finding
		want    string // 期望错误信息包含的关键词
	}{
		{"非法严重度", Finding{Severity: "critical", File: "a.go", Line: 1, Title: "x"}, "severity"},
		{"空文件", Finding{Severity: SeverityBlocker, File: "", Line: 1, Title: "x"}, "file"},
		{"负行号", Finding{Severity: SeverityBlocker, File: "a.go", Line: -1, Title: "x"}, "line"},
		{"空标题", Finding{Severity: SeverityBlocker, File: "a.go", Line: 1, Title: "  "}, "title"},
	}
	for _, c := range cases {
		err := ValidateFinding(c.finding)
		if err == nil {
			t.Errorf("%s：应返回错误", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：错误信息应包含 %q，实际：%v", c.name, c.want, err)
		}
	}
}

// TestSeverityJSONSchema 校验 schema 常量包含 P0-3 强制 JSON 契约的全部字段。
func TestSeverityJSONSchema(t *testing.T) {
	required := []string{"severity", "file", "line", "column", "title", "body", "suggestion"}
	for _, k := range required {
		if !strings.Contains(severityJSONSchema, k) {
			t.Errorf("severityJSONSchema 应包含字段 %q，实际：%s", k, severityJSONSchema)
		}
	}
	// 契约必须明确列出三个合法严重度，避免 AI 自创等级
	for _, s := range LegalSeverities() {
		if !strings.Contains(severityJSONSchema, string(s)) {
			t.Errorf("severityJSONSchema 应包含严重度 %s", s)
		}
	}
}
