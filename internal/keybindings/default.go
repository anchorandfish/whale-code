package keybindings

// DefaultVimBindings 返回 Vim 模式默认绑定表（对标 app.go:1817-1931 现有硬编码）
func DefaultVimBindings() []Binding {
	return []Binding{
		{Key: "i", Action: "mode_insert", Mode: "normal"},
		{Key: "h", Action: "cursor_left", Mode: "normal"},
		{Key: "l", Action: "cursor_right", Mode: "normal"},
		{Key: "j", Action: "cursor_down", Mode: "normal"},
		{Key: "k", Action: "cursor_up", Mode: "normal"},
		{Key: "x", Action: "delete_char", Mode: "normal"},
		{Key: "dd", Action: "delete_line", Mode: "normal"},
		{Key: "0", Action: "line_start", Mode: "normal"},
		{Key: "$", Action: "line_end", Mode: "normal"},
		{Key: "G", Action: "goto_bottom", Mode: "normal"},
		{Key: "gg", Action: "goto_top", Mode: "normal"},
		{Key: "esc", Action: "mode_normal", Mode: "insert"},
	}
}

// DefaultEmacsBindings 返回 Emacs 模式默认绑定表（Ctrl+A 行首/Ctrl+E 行尾/Ctrl+P 上/Ctrl+N 下）
// B2 修复：移除 ctrl+d（保留键不可绑定，包括默认表），ctrl+d 始终由 app.go 处理 EOF 退出
func DefaultEmacsBindings() []Binding {
	return []Binding{
		{Key: "ctrl+a", Action: "line_start", Mode: "insert"},
		{Key: "ctrl+e", Action: "line_end", Mode: "insert"},
		{Key: "ctrl+p", Action: "cursor_up", Mode: "insert"},
		{Key: "ctrl+n", Action: "cursor_down", Mode: "insert"},
		{Key: "ctrl+f", Action: "cursor_right", Mode: "insert"},
		{Key: "ctrl+b", Action: "cursor_left", Mode: "insert"},
		{Key: "ctrl+k", Action: "delete_line", Mode: "insert"},
		{Key: "esc", Action: "mode_normal", Mode: "insert"},
	}
}

// ValidActions 返回所有合法的动作名列表
func ValidActions() []string {
	return []string{
		"cursor_left",
		"cursor_right",
		"cursor_down",
		"cursor_up",
		"line_start",
		"line_end",
		"goto_top",
		"goto_bottom",
		"mode_insert",
		"mode_normal",
		"delete_char",
		"delete_line",
	}
}

// IsValidAction 判断动作名是否合法
func IsValidAction(action string) bool {
	for _, a := range ValidActions() {
		if a == action {
			return true
		}
	}
	return false
}

// ValidModes 返回所有合法的模式名列表
func ValidModes() []string {
	return []string{"normal", "insert", ""}
}

// IsValidMode 判断模式名是否合法
func IsValidMode(mode string) bool {
	for _, m := range ValidModes() {
		if m == mode {
			return true
		}
	}
	return false
}
