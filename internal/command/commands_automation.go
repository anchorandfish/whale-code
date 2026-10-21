package command

// automationCommands 返回 Automation 类别的内置命令
func (r *Registry) automationCommands() []Command {
	return []Command{
		{Name: "loop", Hidden: true, Description: "创建循环 AI 提示任务", Usage: "/loop [options] <interval> <prompt> | /loop list | /loop pause <id> | /loop resume <id> | /loop cancel <id>", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnLoop != nil {
					return r.OnLoop(args), nil
				}
				return "Loop task not initialized", nil
			}},
		{Name: "loop list", Hidden: true, Description: "列出循环 AI 提示任务", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnLoop != nil {
					return r.OnLoop("list"), nil
				}
				return "Loop task not initialized", nil
			}},
		{Name: "loop pause", Hidden: true, Description: "暂停循环 AI 提示任务", Usage: "/loop pause <id>", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnLoop != nil {
					return r.OnLoop("pause " + args), nil
				}
				return "Loop task not initialized", nil
			}},
		{Name: "loop resume", Hidden: true, Description: "恢复已暂停的循环任务", Usage: "/loop resume <id>", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnLoop != nil {
					return r.OnLoop("resume " + args), nil
				}
				return "Loop task not initialized", nil
			}},
		{Name: "loop cancel", Hidden: true, Description: "取消循环 AI 提示任务", Usage: "/loop cancel <id>", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnLoop != nil {
					return r.OnLoop("cancel " + args), nil
				}
				return "Loop task not initialized", nil
			}},
		{Name: "ultraplan", Hidden: true, Description: "超级计划模式", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnUltraplan != nil {
					return r.OnUltraplan(args), nil
				}
				return "Super planning mode not initialized", nil
			}},
		{Name: "bughunter start", Hidden: true, Description: "启动 Bug Hunter 模式", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnBugHunter != nil {
					return r.OnBugHunter("start"), nil
				}
				return "Bug Hunter mode not initialized", nil
			}},
		{Name: "bughunter stop", Hidden: true, Description: "停止 Bug Hunter 模式", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnBugHunter != nil {
					return r.OnBugHunter("stop"), nil
				}
				return "Bug Hunter mode not initialized", nil
			}},
		{Name: "bughunter status", Hidden: true, Description: "查看 Bug Hunter 状态", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnBugHunter != nil {
					return r.OnBugHunter("status"), nil
				}
				return "Bug Hunter mode not initialized", nil
			}},
		{Name: "skillify", Hidden: true, Description: "从当前会话提取可复用技能并保存", Usage: "/skillify [description]", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnSkillExecute != nil {
					result := r.OnSkillExecute("skillify", args)
					if result != "" {
						return result, nil
					}
				}
				return "Skill extraction not initialized, verify .whale/skills/ directory exists", nil
			}},
		{Name: "simplify", Hidden: true, Description: "三维并行代码审查（复用/质量/效率）", Usage: "/simplify [focus]", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnSkillExecute != nil {
					result := r.OnSkillExecute("simplify", args)
					if result != "" {
						return result, nil
					}
				}
				return "Code review not initialized, verify .whale/skills/ directory exists", nil
			}},
		{Name: "verify", Hidden: true, Description: "验证代码更改是否正确工作", Usage: "/verify [focus]", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnSkillExecute != nil {
					result := r.OnSkillExecute("verify", args)
					if result != "" {
						return result, nil
					}
				}
				return "Code verification not initialized, verify .whale/skills/ directory exists", nil
			}},
		{Name: "batch", Hidden: true, Description: "大规模并行更改（拆分为独立子任务）", Usage: "/batch <target>", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnSkillExecute != nil {
					result := r.OnSkillExecute("batch", args)
					if result != "" {
						return result, nil
					}
				}
				return "Batch changes not initialized, verify .whale/skills/ directory exists", nil
			}},
	}
}
