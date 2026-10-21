package command

import (
	"testing"
)

// TestCommitPushPRCommandRegistered 验证 /commit-push-pr 命令已在 Registry 中正确注册
func TestCommitPushPRCommandRegistered(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("commit-push-pr")
	if !ok {
		t.Fatal("命令 /commit-push-pr 未在 Registry 中注册")
	}
	if cmd.Name != "commit-push-pr" {
		t.Errorf("Name = %q, want %q", cmd.Name, "commit-push-pr")
	}
	if cmd.Description == "" {
		t.Error("Description 为空")
	}
	if cmd.Category != "Git" {
		t.Errorf("Category = %q, want %q", cmd.Category, "Git")
	}
	if cmd.Handler == nil {
		t.Error("Handler 为 nil")
	}
}

// TestCommitPushPRCommandHandlerReturnsMagicString 验证 Handler 返回魔数字符串
func TestCommitPushPRCommandHandlerReturnsMagicString(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("commit-push-pr")

	output, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("Handler(\"\") 返回错误: %v", err)
	}
	if output != "COMMIT_PUSH_PR_REQUEST" {
		t.Errorf("Handler(\"\") 返回 %q, want %q", output, "COMMIT_PUSH_PR_REQUEST")
	}

	output, err = cmd.Handler("some args")
	if err != nil {
		t.Fatalf("Handler(\"some args\") 返回错误: %v", err)
	}
	if output != "COMMIT_PUSH_PR_REQUEST" {
		t.Errorf("Handler(\"some args\") 返回 %q, want %q", output, "COMMIT_PUSH_PR_REQUEST")
	}
}

// TestCommitPushPRCommandDescription 验证命令描述包含关键信息
func TestCommitPushPRCommandDescription(t *testing.T) {
	r := NewRegistry()
	cmd, _ := r.Get("commit-push-pr")

	if !containsString(cmd.Description, "提交") {
		t.Error("Description should contain '提交'")
	}
	if !containsString(cmd.Description, "PR") {
		t.Error("Description should contain 'PR'")
	}
}

// TestParseCommitPushPRCaseInsensitive 验证 Parse 对大小写不敏感
func TestParseCommitPushPRCaseInsensitive(t *testing.T) {
	name, args := Parse("/COMMIT-PUSH-PR")
	if name != "commit-push-pr" {
		t.Errorf("Parse('/COMMIT-PUSH-PR'): name = %q, want %q", name, "commit-push-pr")
	}
	if args != "" {
		t.Errorf("Parse('/COMMIT-PUSH-PR'): args = %q, want empty", args)
	}

	name, args = Parse("/Commit-Push-Pr")
	if name != "commit-push-pr" {
		t.Errorf("Parse('/Commit-Push-Pr'): name = %q, want %q", name, "commit-push-pr")
	}
}

// TestCommitPushPRCommandInCategory 验证命令出现在 ByCategory() 的 Git 分类中
func TestCommitPushPRCommandInCategory(t *testing.T) {
	r := NewRegistry()
	categories := r.ByCategory()

	gitCmds, ok := categories["Git"]
	if !ok {
		t.Fatal("Registry 中缺少 Git 分类")
	}

	found := false
	for _, cmd := range gitCmds {
		if cmd.Name == "commit-push-pr" {
			found = true
			break
		}
	}
	if !found {
		t.Error("/commit-push-pr 未出现在 Git 分类中")
	}
}
