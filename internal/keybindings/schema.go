package keybindings

// Binding 单个按键绑定
type Binding struct {
	Key    string `json:"key"`    // 按键字符串（"ctrl+d"/"j"/"dd"），序列键用双字符
	Action string `json:"action"` // 动作名（"cursor_down"/"delete_line"/"goto_top"）
	Mode   string `json:"mode"`   // 生效模式："normal"/"insert"（Vim 状态），空则两模式都生效
}

// Config keybinding 配置
type Config struct {
	Mode     string    `json:"mode"`     // "vim"/"emacs"/"custom"
	Bindings []Binding `json:"bindings"` // 用户自定义绑定（覆盖默认表）
}

// IsEmpty 判断配置是否为空
func (c Config) IsEmpty() bool {
	return c.Mode == "" && len(c.Bindings) == 0
}
