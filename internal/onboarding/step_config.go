package onboarding

import (
	"fmt"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/util"
)

// RunStepConfig 步骤 4：生成 settings.json + 写哨兵文件 + 完成标记。
// 将步骤 2/3 收集的 Provider/APIKey/Model/CustomBaseURL 写入 settings.json，
// 并通过 MarkComplete 写哨兵文件 ~/.whale/.onboarded。
// 对标同类 CLI completeOnboarding + 同类 CLI 首次运行生成 config.toml。
func (o *Onboarding) RunStepConfig(state *OnboardingState) error {
	// 1. 以现有配置为基础（保留用户已有字段），或用默认配置
	cfg := o.cfg
	if cfg == nil {
		cfg = config.DefaultConfigPtr()
	}

	// 2. 写入 Onboarding 收集的字段
	cfg.Model = state.Model
	cfg.Provider = state.Provider
	cfg.APIKey = state.APIKey
	cfg.CustomBaseURL = state.CustomBaseURL
	cfg.Onboarded = true
	// TODO（P7-4， 后续优化）：onboarding 流程当前不收集 FallbackModel/FastModel，
	// 用户需手动在 settings.json 中配置或通过 /model、/fast 命令切换。
	// 未来可在 onboarding 步骤 2/3 补充收集，或提供"使用主模型作为降级/快速模型"的默认选项。

	// 3. 写 settings.json（复用 config 3-C 跨进程文件锁）
	loader := config.NewLoader(state.HomeDir, state.WorkDir)
	if err := loader.Save(cfg); err != nil {
		return fmt.Errorf("write settings.json: %w", err)
	}

	// 4. 写哨兵文件 ~/.whale/.onboarded（MarkComplete 内部处理目录创建与权限）
	if err := MarkComplete(state.HomeDir, cfg); err != nil {
		return fmt.Errorf("write onboarding sentinel file: %w", err)
	}

	// 5. 渲染完成提示（批 5：双语）
	fmt.Println(util.Bi("  ✅ Onboarding complete!", "  ✅ Onboarding 完成！"))
	fmt.Println(util.Bi("    - Config file: ~/.whale/settings.json", "    - 配置文件：~/.whale/settings.json"))
	fmt.Println(util.Bi("    - Sentinel file: ~/.whale/.onboarded", "    - 哨兵文件：~/.whale/.onboarded"))
	fmt.Println(util.Bi("    - Next launch will skip onboarding and enter the TUI directly",
		"    - 下次启动将跳过 Onboarding，直接进入 TUI"))
	return nil
}
