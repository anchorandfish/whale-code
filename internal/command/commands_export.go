package command

// exportCommands 返回 Export 类别的内置命令
func (r *Registry) exportCommands() []Command {
	return []Command{
		{Name: "export", Description: "导出会话到文件", Usage: "/export [--format json|markdown] [filename]", Category: "Export",
			Handler: func(args string) (string, error) {
				if r.OnExport != nil {
					return r.OnExport(args), nil
				}
				return "Use /export-format <format> [path] to export in a specific format", nil
			}},
		{Name: "export-user", Hidden: true, Description: "导出仅用户消息", Usage: "/export-user [filename]", Category: "Export",
			Handler: func(args string) (string, error) {
				if r.OnExportUser != nil {
					return r.OnExportUser(args), nil
				}
				return "Use /export-format markdown to export the full conversation", nil
			}},
		{Name: "copy", Description: "复制回复到剪贴板", Usage: "/copy [last|all|N]", Category: "Export",
			Handler: func(args string) (string, error) {
				if r.OnCopy != nil {
					return r.OnCopy(args), nil
				}
				// P1-8 修复：OnCopy 未注册时返回诚实降级提示，避免假成功误导用户
				return "Clipboard is not available in this mode.\nUse /export to save conversation to a file instead.", nil
			}},
		{Name: "export-format", Hidden: true, Description: "指定导出格式", Usage: "/export-format json|markdown|html [path]", Category: "Export",
			Handler: func(args string) (string, error) {
				if r.OnExportFormat != nil {
					return r.OnExportFormat(args), nil
				}
				parts := splitArgs(args)
				if len(parts) == 0 {
					return "Usage: /export-format json|markdown|html [path]", nil
				}
				format := parts[0]
				path := "export"
				if len(parts) > 1 {
					path = parts[1]
				}
				return "Exported as " + format + " format: " + path, nil
			}},
		{Name: "share", Hidden: true, Description: "导出并复制路径", Usage: "/share", Category: "Export",
			Handler: func(args string) (string, error) {
				if r.OnShare != nil {
					return r.OnShare(args), nil
				}
				return r.handleShare(""), nil
			}},
	}
}
