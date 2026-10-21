package keybindings

// ActionType 动作类型
type ActionType string

const (
	ActionPassthrough     ActionType = "passthrough"      // 不拦截，交给 N5/其他段
	ActionVimMotion       ActionType = "vim_motion"       // Vim 移动（h/j/k/l/0/$/G）
	ActionVimModeSwitch   ActionType = "vim_mode_switch"  // 模式切换（i/esc）
	ActionVimDelete       ActionType = "vim_delete"       // 删除（x/dd）
	ActionSequencePending ActionType = "sequence_pending" // 序列键等待第二个键（d/g）
	ActionReserved        ActionType = "reserved"         // 保留键（Ctrl+C 等，不处理）
)

// Action 按键解析结果
type Action struct {
	Type    ActionType
	Motion  string // "cursor_left"/"cursor_right"/"cursor_down"/"cursor_up"/"line_start"/"line_end"/"goto_top"/"goto_bottom"/"mode_insert"/"mode_normal"/"delete_char"/"delete_line"
	Pending string // 序列键 pending（"d"/"g"），非空时表示等待下一个键
}

// IsPassthrough 判断是否为透传动作
func (a Action) IsPassthrough() bool {
	return a.Type == ActionPassthrough
}

// IsReserved 判断是否为保留键动作
func (a Action) IsReserved() bool {
	return a.Type == ActionReserved
}

// IsSequencePending 判断是否为序列键等待状态
func (a Action) IsSequencePending() bool {
	return a.Type == ActionSequencePending
}
