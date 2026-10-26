package onboarding

import (
	"runtime"
	"strings"
	"testing"
)

// TestRenderDifferentiatedWelcome_ContainsThreeSellingPoints 验证首屏文案含三大卖点关键词。
// 关键词：本地优先 / H12 自我进化 / 当前平台对应的沙箱实现（W-121 起按平台分支）。
// 对标 D1-1 改动 1 验收要求。
func TestRenderDifferentiatedWelcome_ContainsThreeSellingPoints(t *testing.T) {
	ob := &Onboarding{}
	out := ob.renderDifferentiatedWelcome()

	// 沙箱卖点随平台变化，期望值从同一个 SSOT 取，避免测试与实现各写一份文案。
	_, sandboxZH := sandboxClaims()
	wantKeywords := []string{
		"本地优先",
		"H12 自我进化",
		sandboxZH,
	}
	for _, kw := range wantKeywords {
		if !strings.Contains(out, kw) {
			t.Errorf("renderDifferentiatedWelcome() output missing keyword %q\nOutput:\n%s", kw, out)
		}
	}
}

// TestSandboxClaims_PerPlatform 钉住 W-121：每个平台只说自己的沙箱实现，不得越界。
//
// 判别力：改造前（文案写死 Windows Job Object）本测试会命中 "Job Object" 而变红
// ⇒ 这是一道真门，不是事后追认。
func TestSandboxClaims_PerPlatform(t *testing.T) {
	markers := map[string]string{
		"darwin":  "Seatbelt",
		"linux":   "Landlock",
		"windows": "Job Object",
	}
	for goos, marker := range markers {
		en, zh := sandboxClaimsFor(goos)
		if !strings.Contains(en, marker) || !strings.Contains(zh, marker) {
			t.Errorf("%s 的沙箱文案应含 %q（en=%q zh=%q）", goos, marker, en, zh)
		}
		for otherGOOS, otherMarker := range markers {
			if otherGOOS == goos {
				continue
			}
			if strings.Contains(en, otherMarker) || strings.Contains(zh, otherMarker) {
				t.Errorf("%s 的沙箱文案越界出现 %s 的实现标记 %q（en=%q zh=%q）",
					goos, otherGOOS, otherMarker, en, zh)
			}
		}
	}

	// Linux 额外要求 seccomp：这是真实存在的逃逸面拦截能力，漏写不会报错。
	if en, zh := sandboxClaimsFor("linux"); !strings.Contains(en, "seccomp") || !strings.Contains(zh, "seccomp") {
		t.Error("Linux 沙箱文案应含 seccomp（拦截 ptrace/process_vm_*/io_uring_* 的实现）")
	}

	// 未知平台：只说共性，不冒充任何具体实现。
	en, zh := sandboxClaimsFor("freebsd")
	for goos, marker := range markers {
		if strings.Contains(en, marker) || strings.Contains(zh, marker) {
			t.Errorf("未知平台不应出现 %s 的标记 %q（en=%q zh=%q）", goos, marker, en, zh)
		}
	}
}

// TestRenderDifferentiatedWelcome_NoForeignPlatformClaim 证明生产渲染路径
// （runtime.GOOS → 首屏文本）不带其他平台的实现标记。
func TestRenderDifferentiatedWelcome_NoForeignPlatformClaim(t *testing.T) {
	ob := &Onboarding{}
	out := ob.renderDifferentiatedWelcome()

	markers := map[string]string{
		"darwin":  "Seatbelt",
		"linux":   "Landlock",
		"windows": "Job Object",
	}
	for goos, marker := range markers {
		if goos == runtime.GOOS {
			continue
		}
		if strings.Contains(out, marker) {
			t.Errorf("当前平台 %s 的首屏出现 %s 的实现标记 %q（平台文案错位）\nOutput:\n%s",
				runtime.GOOS, goos, marker, out)
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
