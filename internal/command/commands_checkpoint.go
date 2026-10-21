package command

// checkpointCommands 返回 Checkpoint 类别的内置命令
func (r *Registry) checkpointCommands() []Command {
	return []Command{
		{Name: "checkpoint", Description: "创建会话检查点（快照）", Usage: "/checkpoint [label]", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnCheckpoint != nil {
					return r.OnCheckpoint("save " + args), nil
				}
				return "Checkpoint feature not initialized", nil
			}},
		{Name: "checkpoint save", Description: "创建带标签的会话检查点", Usage: "/checkpoint save <label>", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnCheckpoint != nil {
					return r.OnCheckpoint("save " + args), nil
				}
				return "Checkpoint feature not initialized", nil
			}},
		{Name: "checkpoint list", Description: "列出当前会话所有检查点", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnCheckpoint != nil {
					return r.OnCheckpoint("list"), nil
				}
				return "Checkpoint feature not initialized", nil
			}},
		{Name: "checkpoint restore", Description: "恢复到指定检查点", Usage: "/checkpoint restore <id>", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnCheckpoint != nil {
					return r.OnCheckpoint("restore " + args), nil
				}
				return "Checkpoint feature not initialized", nil
			}},
		{Name: "checkpoint delete", Description: "删除指定检查点", Usage: "/checkpoint delete <id>", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnCheckpoint != nil {
					return r.OnCheckpoint("delete " + args), nil
				}
				return "Checkpoint feature not initialized", nil
			}},
		{Name: "checkpoint show", Description: "查看检查点详情", Usage: "/checkpoint show <id>", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnCheckpoint != nil {
					return r.OnCheckpoint("show " + args), nil
				}
				return "Checkpoint feature not initialized", nil
			}},
	}
}
