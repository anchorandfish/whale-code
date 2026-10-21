package github

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/secret"
)

// TestDetectSecretLeakForCommit_PATBlocked 验证含 GitHub PAT 的 commit message 被检测阻断（REF-OWASP-LLM02）。
// 直接测试 detectSecretLeakForCommit：PRCreator.CreatePR 依赖真实 git 仓库 + aiClient，
// 难以 mock，按方案降级为直接测试检测函数。
func TestDetectSecretLeakForCommit_PATBlocked(t *testing.T) {
	// ghp_ + 36 个字符，满足 github_token 正则 (ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36}
	msg := "chore: update config ghp_" + strings.Repeat("a", 36)
	labels, hit := detectSecretLeakForCommit(msg)
	if !hit {
		t.Fatal("含 GitHub PAT 的 commit message 应被检测命中")
	}
	if !strings.Contains(labels, "GitHub Token") {
		t.Errorf("labels 应含 'GitHub Token'，实际: %s", labels)
	}
}

// TestDetectSecretLeakForCommit_NormalMessage 验证正常 commit message 不被误报为密钥泄露。
func TestDetectSecretLeakForCommit_NormalMessage(t *testing.T) {
	msg := "fix: update README and refactor utils"
	_, hit := detectSecretLeakForCommit(msg)
	if hit {
		t.Errorf("正常 commit message 不应被误报为密钥泄露，输入: %q", msg)
	}
}

// TestDetectSecretLeakForCommit_EmptySafe 验证空字符串不 panic 且不命中。
func TestDetectSecretLeakForCommit_EmptySafe(t *testing.T) {
	labels, hit := detectSecretLeakForCommit("")
	if hit {
		t.Errorf("空字符串不应命中，labels: %s", labels)
	}
}

// TestDetectLeakForPRTitle_PATBlocked 验证含 GitHub PAT 的 PR title 被检测阻断（D7-2 安全增强）。
// 直接测试 secret.DetectLeakWithLabels 对 title 文本的检测：
// PRCreator.CreatePR 依赖真实 git 仓库 + aiClient，难以 mock 整个流程，
// 按方案降级为直接测试检测函数（与 TestDetectSecretLeakForCommit_PATBlocked 同策略）。
// 覆盖 CreatePR 中 title 检测分支：if leaked, hit := secret.DetectLeakWithLabels(title); hit { ... }。
func TestDetectLeakForPRTitle_PATBlocked(t *testing.T) {
	// 模拟 AI 生成的 PR title 不慎复述了 diff 中的 GitHub PAT。
	// ghp_ + 36 个字符，满足 github_token 正则 (ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36}。
	title := "feat: update auth with token ghp_" + strings.Repeat("a", 36)
	leaked, hit := secret.DetectLeakWithLabels(title)
	if !hit {
		t.Fatal("含 GitHub PAT 的 PR title 应被检测命中")
	}
	if !strings.Contains(leaked, "GitHub Token") {
		t.Errorf("leaked labels 应含 'GitHub Token'，实际: %s", leaked)
	}
}

// TestDetectLeakForPRBody_AnthropicKeyBlocked 验证含 Anthropic API Key 的 PR body 被检测阻断（D7-2 安全增强）。
// 覆盖 CreatePR 中 body 检测分支：if leaked, hit := secret.DetectLeakWithLabels(body); hit { ... }。
func TestDetectLeakForPRBody_AnthropicKeyBlocked(t *testing.T) {
	// 模拟 AI 生成的 PR body 不慎复述了 diff 中的 Anthropic API Key。
	// sk-ant- + 30 个字符，满足 anthropic_api_key 正则 sk-ant-[A-Za-z0-9_-]{20,}。
	body := "## Changes\n- update api client key to sk-ant-api03-" + strings.Repeat("a", 30)
	leaked, hit := secret.DetectLeakWithLabels(body)
	if !hit {
		t.Fatal("含 Anthropic API Key 的 PR body 应被检测命中")
	}
	if !strings.Contains(leaked, "Anthropic API Key") {
		t.Errorf("leaked labels 应含 'Anthropic API Key'，实际: %s", leaked)
	}
}
