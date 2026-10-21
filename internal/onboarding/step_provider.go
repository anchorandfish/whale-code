package onboarding

import (
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/util"
)

// 默认模型名（对标改进方案 §1.6，用户可在 /config 中后续修改）。
const (
	defaultModelAnthropic = "claude-sonnet-4-5"
	defaultModelOpenAI    = "gpt-4o"
	// maxProviderRetries provider 选项无效输入时的最大重试次数（审查 v1 B6 修复）。
	maxProviderRetries = 3
)

// RunStepProvider 步骤 2：交互式选择 AI Provider。
// 提供三个选项：Anthropic / OpenAI / 自定义（兼容 OpenAI API 的 provider）。
// 无效选项时循环重试（最多 maxProviderRetries 次），提升首次用户体验。
// 对标同类 CLI 首次运行交互式 provider 选择（同类 CLI 用 OAuth，whale-code 不需要）。
func (o *Onboarding) RunStepProvider(state *OnboardingState) error {
	reader := o.stdinBufio()

	fmt.Println(util.Bi("  Select your AI provider:", "  请选择 AI Provider："))
	fmt.Println(util.Bi("    1. Anthropic (Claude models)", "    1. Anthropic（Claude 系列）"))
	fmt.Println(util.Bi("    2. OpenAI (GPT models)", "    2. OpenAI（GPT 系列）"))
	fmt.Println(util.Bi("    3. Custom (OpenAI-compatible provider)", "    3. 自定义（兼容 OpenAI API 的 provider）"))

	for attempt := 1; attempt <= maxProviderRetries; attempt++ {
		fmt.Print(util.Bi("\n  Choose [1-3] (default 1): ", "\n  请输入选项 [1-3]（默认 1）："))
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read provider choice: %w", err)
		}
		input = strings.TrimSpace(input)

		switch input {
		case "", "1":
			state.Provider = "anthropic"
			state.Model = defaultModelAnthropic
			state.EnvVar = "ANTHROPIC_API_KEY"
			fmt.Println(util.BiSame("  ✅ Provider: %s, Model: %s", "  ✅ 已选择 Provider: %s, Model: %s", state.Provider, state.Model))
			return nil
		case "2":
			state.Provider = "openai"
			state.Model = defaultModelOpenAI
			state.EnvVar = "OPENAI_API_KEY"
			fmt.Println(util.BiSame("  ✅ Provider: %s, Model: %s", "  ✅ 已选择 Provider: %s, Model: %s", state.Provider, state.Model))
			return nil
		case "3":
			state.Provider = "openai" // 自定义 provider 复用 openai 兼容协议
			state.EnvVar = "OPENAI_API_KEY"
			fmt.Print(util.Bi("  Enter custom provider base URL: ", "  请输入自定义 provider base URL："))
			url, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("read base URL: %w", err)
			}
			state.CustomBaseURL = strings.TrimSpace(url)
			fmt.Print(util.Bi("  Enter model name: ", "  请输入模型名称："))
			model, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("read model name: %w", err)
			}
			state.Model = strings.TrimSpace(model)
			if state.Model == "" {
				return fmt.Errorf("model name must not be empty")
			}
			fmt.Println(util.BiSame("  ✅ Provider: %s, Model: %s", "  ✅ 已选择 Provider: %s, Model: %s", state.Provider, state.Model))
			return nil
		default:
			// 无效选项：提示并重试（最后一次仍无效则返回 error）
			if attempt < maxProviderRetries {
				fmt.Println(util.BiSame("  ⚠️  Invalid choice: %s (enter 1-3), %d attempts left",
					"  ⚠️  无效选项：%s（请输入 1-3），剩余 %d 次尝试", input, maxProviderRetries-attempt))
				continue
			}
			return fmt.Errorf("invalid choice: %s (retried %d times, enter 1-3)", input, maxProviderRetries)
		}
	}
	return fmt.Errorf("provider selection failed: exceeded max retries (%d)", maxProviderRetries)
}
