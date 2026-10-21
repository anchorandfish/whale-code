package onboarding

import (
	"strings"
	"testing"
)

// TestRenderDifferentiatedWelcome_ContainsThreeSellingPoints 验证首屏文案含三大卖点关键词。
// 关键词：本地优先 / H12 自我进化 / Windows 原生。
// 对标 D1-1 改动 1 验收要求。
func TestRenderDifferentiatedWelcome_ContainsThreeSellingPoints(t *testing.T) {
	ob := &Onboarding{}
	out := ob.renderDifferentiatedWelcome()

	wantKeywords := []string{
		"本地优先",
		"H12 自我进化",
		"Windows 原生",
	}
	for _, kw := range wantKeywords {
		if !strings.Contains(out, kw) {
			t.Errorf("renderDifferentiatedWelcome() output missing keyword %q\nOutput:\n%s", kw, out)
		}
	}
}

// TestRenderDifferentiatedWelcome_NoPanic 验证渲染函数不 panic（即使 Onboarding 字段为零值或 receiver 为 nil）。
// fail-open 要求：panic 时返回空串而非崩溃。
// 对标 D1-1 改动 1 验收要求 + 项目 fail-open 降级原则。
func TestRenderDifferentiatedWelcome_NoPanic(t *testing.T) {
	// 零值 Onboarding
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("renderDifferentiatedWelcome panicked on zero-value Onboarding: %v", r)
		}
	}()
	ob := &Onboarding{}
	out := ob.renderDifferentiatedWelcome()
	if out == "" {
		t.Fatal("renderDifferentiatedWelcome should return non-empty string for zero-value Onboarding")
	}

	// nil receiver 也应安全（方法不 dereference receiver）
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("renderDifferentiatedWelcome panicked on nil receiver: %v", r)
		}
	}()
	var nilOb *Onboarding
	out2 := nilOb.renderDifferentiatedWelcome()
	if out2 == "" {
		t.Fatal("renderDifferentiatedWelcome should return non-empty string for nil receiver")
	}
}

// TestRenderDifferentiatedWelcome_StableWidth 验证每行显示宽度一致（box drawing 对齐）。
// 所有行（含边框行）显示宽度应等于 welcomeBoxWidth + 2（左右 ║/╔/╚ 各 1 列）。
// 对标 D1-1 改动 1 验收要求。
func TestRenderDifferentiatedWelcome_StableWidth(t *testing.T) {
	ob := &Onboarding{}
	out := ob.renderDifferentiatedWelcome()
	if out == "" {
		t.Fatal("renderDifferentiatedWelcome should return non-empty string")
	}

	lines := strings.Split(out, "\n")
	if len(lines) < 5 {
		t.Fatalf("expected at least 5 lines, got %d", len(lines))
	}

	expectedWidth := welcomeBoxWidth + 2 // 1(左边框) + 64(内容) + 1(右边框)
	for i, line := range lines {
		w := displayWidth(line)
		if w != expectedWidth {
			t.Errorf("line %d displayWidth = %d, want %d (content mismatch breaks box alignment)\nline: %q",
				i, w, expectedWidth, line)
		}
	}
}

// TestRenderDifferentiatedWelcome_HasBoxBorders 验证首屏包含 box drawing 边框字符。
// 顶部 ╔...╗、分隔 ╠...╣、底部 ╚...╝ 应各出现一次。
// 补充测试：确保 box 结构完整（非纯文本）。
func TestRenderDifferentiatedWelcome_HasBoxBorders(t *testing.T) {
	ob := &Onboarding{}
	out := ob.renderDifferentiatedWelcome()

	if !strings.Contains(out, "╔") || !strings.Contains(out, "╗") {
		t.Error("output missing top border ╔...╗")
	}
	if !strings.Contains(out, "╚") || !strings.Contains(out, "╝") {
		t.Error("output missing bottom border ╚...╝")
	}
	if !strings.Contains(out, "╠") || !strings.Contains(out, "╣") {
		t.Error("output missing separator border ╠...╣")
	}
}
