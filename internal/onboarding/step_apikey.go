package onboarding

import (
	"fmt"
	"os"
	"strings"

	"github.com/anchorandfish/whale-code/internal/util"
)

// RunStepAPIKey 步骤 3：API Key 输入（不回显，脱敏存储）。
// 优先检测环境变量是否已设置（已有用户场景）；未设置则交互式输入（终端模式 term.ReadPassword 不回显）。
// 输入完成后通过 os.Setenv 透传到当前进程，使后续 createClient/resolveProviderConfig 能读取。
// 对标同类 CLI Onboarding.tsx 的 api-key 步骤（ApproveApiKey）。
func (o *Onboarding) RunStepAPIKey(state *OnboardingState) error {
	// 冲突检测：用户选了非 anthropic provider，但 ANTHROPIC_API_KEY 环境变量已设置，
	// 由于 main.go 环境变量优先级 ANTHROPIC_API_KEY > OPENAI_API_KEY，可能覆盖 settings.json 配置，
	// 因此需要提示用户确认（改进方案 §1.7 L241-255）。
	if state.Provider != "anthropic" {
		if anthropicKey := os.Getenv("ANTHROPIC_API_KEY"); anthropicKey != "" {
			fmt.Println(util.Bi("  ⚠️  ANTHROPIC_API_KEY environment variable is already set",
				"  ⚠️  检测到 ANTHROPIC_API_KEY 环境变量已设置"))
			fmt.Println(util.Bi("    Note: ANTHROPIC_API_KEY takes priority over OPENAI_API_KEY and settings.json,",
				"    注意：ANTHROPIC_API_KEY 优先级高于 OPENAI_API_KEY 和 settings.json，"))
			fmt.Println(util.Bi("    which may override the provider you selected.",
				"    可能导致实际使用的 provider 与你选择的不一致。"))
			fmt.Print(util.Bi("    Continue with the currently selected provider? [y/N]: ",
				"    确认继续使用当前选择的 provider 吗？[y/N]："))
			reader := o.stdinBufio()
			confirm, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("read confirmation input: %w", err)
			}
			confirm = strings.TrimSpace(strings.ToLower(confirm))
			if confirm != "y" && confirm != "yes" {
				return fmt.Errorf("cancelled by user: ANTHROPIC_API_KEY env var conflicts with the selected provider")
			}
		}
	}

	// 检测环境变量是否已设置
	if key := os.Getenv(state.EnvVar); key != "" {
		fmt.Println(util.BiSame("  ✅ Detected %s env var (will use its value)", "  ✅ 检测到 %s 环境变量已设置（将使用该值）", state.EnvVar))
		state.APIKey = key
		state.APIKeySource = "env"
		return nil
	}

	// 交互式输入（终端模式下不回显）
	fmt.Print(util.Bi("  Enter your API key (stored in ~/.whale/settings.json; input is hidden):\n  > ",
		"  请输入 API Key（将存储到 ~/.whale/settings.json，输入不回显）：\n  > "))
	password, err := o.readPassword()
	if err != nil {
		return fmt.Errorf("read API key: %w", err)
	}
	fmt.Println() // ReadPassword 不输出换行，补一个

	state.APIKey = strings.TrimSpace(password)
	state.APIKeySource = "settings"
	if state.APIKey == "" {
		return fmt.Errorf("API key must not be empty")
	}

	// 透传到当前进程环境变量，使 createClient/resolveProviderConfig 能读取
	if err := os.Setenv(state.EnvVar, state.APIKey); err != nil {
		return fmt.Errorf("set env var %s: %w", state.EnvVar, err)
	}

	fmt.Println(util.Bi("  ✅ API key entered", "  ✅ API key 已输入"))
	return nil
}
