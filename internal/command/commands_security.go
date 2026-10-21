package command

import "strings"

// securityCommands 返回 Security 类别的内置命令。
// B7-P1-2 修复：原 Handler 返回 "Security review started — AI will scan..." 等静态文本
// 但不触发任何 AI 扫描（假成功）。修复后返回诚实降级提示；
// 真正的 AI 扫描由各模式 dispatchHandler 拦截触发（TUI: app.go, PlainRunner: plain.go, Bridge: bridge.go）。
func (r *Registry) securityCommands() []Command {
	return []Command{
		// O-2（拍板材料 §2 选项 a，2026-10-11）：skill 信任门的出口正门。
		// 信任门生产已开（executor.go EnableTrustGate）而出口命令缺失 = 幽灵命令
		// （拦截文案 trust.go 让用户敲 /skill trust，注册表里却没有）。
		// Handler 只做薄转发到 OnSkillManage（SSOT = skill.Manager.HandleTrustCommand，）；
		// 未接线时诚实降级，不得假装成功（对齐 B7-P1-2 纪律）。
		{Name: "skill", Description: "管理 skill 信任（trust/untrust）", DescriptionEN: "Manage skill trust (trust/untrust)", Usage: "/skill [trust <name> | trust all | untrust <name>]", Category: "Security",
			Handler: func(args string) (string, error) {
				if r.OnSkillManage != nil {
					return r.OnSkillManage(args)
				}
				return "Skill trust manager not initialized", nil
			}},
		{Name: "security-review", Hidden: true, Description: "安全审查当前代码", Usage: "/security-review [file|full|staged]", Category: "Security",
			Handler: func(args string) (string, error) {
				args = strings.TrimSpace(args)
				// 桩层不触发 AI 扫描，仅返回诚实降级提示。
				// 各模式 dispatchHandler 拦截后调用 buildSecurityReviewPrompt + streamAIRequest 执行真实扫描。
				switch {
				case args == "full":
					return "Security review (full project) is not available in this mode. Use TUI/Plain/Bridge mode for AI-powered scanning.", nil
				case args == "staged":
					return "Security review (staged changes) is not available in this mode. Use TUI/Plain/Bridge mode for AI-powered scanning.", nil
				case args != "":
					return "Security review (file: " + args + ") is not available in this mode. Use TUI/Plain/Bridge mode for AI-powered scanning.", nil
				default:
					return "Security review (git diff) is not available in this mode. Use TUI/Plain/Bridge mode for AI-powered scanning.", nil
				}
			}},
	}
}
