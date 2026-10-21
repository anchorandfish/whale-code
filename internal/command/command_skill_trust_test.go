package command

// command_skill_trust_test.go — /skill trust 命令注册与转发判据（拍板材料 §2 选项 a，2026-10-11）。
//
// 修复前的生产级红：`/skill trust <name>` 在注册表中不存在（拦截文案 trust.go 指向幽灵命令），
// 本文件 TestSkillTrustCommandRegistered 在 NewRegistry()（生产注册入口）上即红。
// 转发判据钉住"注册侧 → OnSkillManage 回调"这一段；实现（SSOT）在 internal/skill 包内测。

import (
	"strings"
	"testing"
)

func TestSkillTrustCommandRegistered(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("skill")
	if !ok {
		t.Fatal("幽灵命令实锤：拦截文案让用户敲 /skill trust，但注册表里没有 /skill 命令")
	}
	if cmd.Category != "Security" {
		t.Fatalf("Category = %q, want Security（skill 信任门属安全域）", cmd.Category)
	}
	if cmd.Usage == "" || !strings.Contains(cmd.Usage, "trust") {
		t.Fatalf("Usage 应说明 trust 用法：%q", cmd.Usage)
	}
	if cmd.Hidden || cmd.Stub {
		t.Fatalf("出口命令不得隐藏/桩化（Hidden=%v Stub=%v）——门开着就必须给可见出口", cmd.Hidden, cmd.Stub)
	}
}

func TestSkillTrustHandlerForwardsArgs(t *testing.T) {
	r := NewRegistry()
	var got string
	r.OnSkillManage = func(args string) (string, error) {
		got = args
		return "ok:" + args, nil
	}
	cmd, ok := r.Get("skill")
	if !ok {
		t.Fatal("command /skill not registered")
	}
	out, err := cmd.Handler("trust deploy-docs")
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if got != "trust deploy-docs" {
		t.Fatalf("args 未原样转发到回调：got %q", got)
	}
	if out != "ok:trust deploy-docs" {
		t.Fatalf("回调返回值未透出：%q", out)
	}
}

func TestSkillTrustHandlerNotWired(t *testing.T) {
	r := NewRegistry() // 不接线 OnSkillManage：降级提示必须诚实，不得假装成功
	cmd, _ := r.Get("skill")
	out, err := cmd.Handler("trust x")
	if err != nil {
		t.Fatalf("未接线应返回提示而非 error：%v", err)
	}
	if !strings.Contains(out, "not initialized") {
		t.Fatalf("未接线应返回诚实降级提示：\n%s", out)
	}
}
