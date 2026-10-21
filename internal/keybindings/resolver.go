package keybindings

import (
	"strings"
	"sync"
	"time"
)

// sequenceTimeout 序列键超时时间（M11-01）。
//
// 对标 VSCode chord timeout：500ms 内无后续键则清空 pendingKeys，
// 防止用户中途分心导致序列键悬挂。
const sequenceTimeout = 500 * time.Millisecond

// Resolver 按键解析器，持有合并后的绑定表和序列键状态。
//
// （M11-01）改造：pending string → pendingKeys []string，
// 支持任意长度多键序列（对标 VSCode chord 无上限）。
type Resolver struct {
	bindings       map[string]map[string]Action // key.String() → mode → Action
	pendingKeys    []string                     // 序列键累积（"d"/"g" 等），仅 normal 模式生效（M11-01：多键序列）
	pendingMode    string                       // pending 生效的 mode（序列键只在对应 mode 触发，防 insert 误吞）
	lastKeyTime    time.Time                    // 上次按键时间（M11-01：序列超时检测）
	reserved       map[string]bool              // 保留键集合
	sequencePrefix map[string]map[string]bool   // 序列键首键 → mode → 是否存在序列键（按 mode 隔离，防 insert 误吞）
	mu             sync.RWMutex                 // 并发安全（GetPending 读锁；Parse/SetPending/Reset 写锁）
}

// NewResolver 从绑定表构造 Resolver
func NewResolver(bindings []Binding, reserved []string) *Resolver {
	r := &Resolver{
		bindings:       make(map[string]map[string]Action),
		reserved:       make(map[string]bool),
		sequencePrefix: make(map[string]map[string]bool),
	}

	for _, key := range reserved {
		r.reserved[key] = true
	}

	for _, binding := range bindings {
		keyStr := binding.Key
		mode := binding.Mode
		if mode == "" {
			mode = "_all"
		}

		if _, ok := r.bindings[keyStr]; !ok {
			r.bindings[keyStr] = make(map[string]Action)
		}

		// （M11-01）：多键序列前缀树——记录多键绑定所有前缀（不只是首字符），
		// 支持 3+/4+ 键序列（如 "gdd" 记录 "g" + "gd" 为前缀）。
		// B1 修复：按 mode 隔离记录，避免 insert 模式字母 d/g 被误判为序列键首键。
		// 对标 VSCode chord 无上限 @https://code.visualstudio.com/docs/getstarted/keybindings#_chords @2026-07-21。
		if len(keyStr) > 1 {
			for i := 1; i < len(keyStr); i++ {
				prefix := keyStr[:i]
				if r.sequencePrefix[prefix] == nil {
					r.sequencePrefix[prefix] = make(map[string]bool)
				}
				r.sequencePrefix[prefix][mode] = true
			}
		}

		var actionType ActionType
		var motion string

		switch binding.Action {
		case "cursor_left", "cursor_right", "cursor_down", "cursor_up",
			"line_start", "line_end", "goto_top", "goto_bottom":
			actionType = ActionVimMotion
			motion = binding.Action
		case "mode_insert", "mode_normal":
			actionType = ActionVimModeSwitch
			motion = binding.Action
		case "delete_char", "delete_line":
			actionType = ActionVimDelete
			motion = binding.Action
		}

		r.bindings[keyStr][mode] = Action{
			Type:   actionType,
			Motion: motion,
		}
	}

	return r
}

// Parse 解析按键，返回对应的 Action（契约 02 §2.2.1 L140）。
// （M11-01）：pending string → pendingKeys []string，支持任意长度多键序列。
//
// 行为规约：
// 1. 检查序列超时：若 lastKeyTime 距今 > sequenceTimeout（500ms），清空 pendingKeys
// 2. 若 pendingKeys 非空且 pendingMode 匹配当前 mode，拼接累积键 + 当前键查 bindings
// - 完整匹配：清空 pendingKeys，返回对应 Action
// - 部分前缀匹配：累积当前键到 pendingKeys，返回 ActionSequencePending（支持 3+ 键序列）
// - 匹配失败：清空 pendingKeys，继续单键匹配
//
// 3. 若 key 在保留键集合，返回 ActionReserved（不拦截，交给 app.go 默认处理）
// 4. 若 key 在 bindings 表且 mode 匹配，返回对应 Action
// 5. 若 key 是序列键首键且当前 mode 存在对应序列键绑定，返回 ActionSequencePending 并追加到 pendingKeys
// 6. 否则返回 ActionPassthrough（交给 N5/其他段）
//
// B1 修复：sequencePrefix 按 mode 隔离，insert 模式不再误判 d/g 为序列键首键
// N1 修复：Parse 全程持写锁（Lock/Unlock），消除 RUnlock→Lock→Unlock→RLock 锁升级模式
// 收尾修复：步骤 2 增加"部分前缀匹配"分支，使 3+ 键序列（如 sp-k）能跨多键累积 pendingKeys
func (r *Resolver) Parse(key Key, mode string) Action {
	r.mu.Lock()
	defer r.mu.Unlock()

	keyStr := key.String()
	now := time.Now()

	// （M11-01）：序列超时检测
	if !r.lastKeyTime.IsZero() && now.Sub(r.lastKeyTime) > sequenceTimeout && len(r.pendingKeys) > 0 {
		r.pendingKeys = nil
		r.pendingMode = ""
	}
	r.lastKeyTime = now

	// 保留键优先级最高
	if r.reserved[keyStr] {
		r.pendingKeys = nil
		r.pendingMode = ""
		return Action{Type: ActionReserved}
	}

	// （M11-01）：多键序列匹配
	// 若 pendingKeys 非空且 pendingMode 匹配当前 mode，拼接累积键 + 当前键查 bindings
	if len(r.pendingKeys) > 0 && (r.pendingMode == mode || r.pendingMode == "_all") {
		// 拼接累积键 + 当前键
		sequenceKey := strings.Join(r.pendingKeys, "") + keyStr
		if actionMap, ok := r.bindings[sequenceKey]; ok {
			if action, ok := actionMap[mode]; ok {
				r.pendingKeys = nil
				r.pendingMode = ""
				return action
			}
			if action, ok := actionMap["_all"]; ok {
				r.pendingKeys = nil
				r.pendingMode = ""
				return action
			}
		}
		// 收尾修复：sequenceKey 不是完整绑定，但是更长序列键的前缀时，
		// 累积当前键到 pendingKeys 并返回 ActionSequencePending（支持 3+/4+ 键序列）
		if prefixModes, ok := r.sequencePrefix[sequenceKey]; ok {
			if prefixModes[mode] || prefixModes["_all"] {
				r.pendingKeys = append(r.pendingKeys, keyStr)
				return Action{Type: ActionSequencePending, Pending: sequenceKey}
			}
		}
		// 序列键未匹配，清空 pendingKeys，继续单键匹配
		r.pendingKeys = nil
		r.pendingMode = ""
	}

	// 单键精确匹配
	if actionMap, ok := r.bindings[keyStr]; ok {
		if action, ok := actionMap[mode]; ok {
			return action
		}
		if action, ok := actionMap["_all"]; ok {
			return action
		}
	}

	// 序列键首键检测：仅当当前 mode 存在对应序列键绑定时才进入 pending
	// B1 修复：insert 模式下 sequencePrefix["d"]["normal"]=true 但 sequencePrefix["d"]["insert"] 不存在，不会误吞
	if prefixModes, ok := r.sequencePrefix[keyStr]; ok {
		if prefixModes[mode] || prefixModes["_all"] {
			r.pendingKeys = []string{keyStr}
			r.pendingMode = mode
			return Action{Type: ActionSequencePending, Pending: keyStr}
		}
	}

	return Action{Type: ActionPassthrough}
}

// SetPending 手动设置 pending（供 app.go 在特殊场景使用）。
// （M11-01）：兼容旧接口，将 p 作为单键存入 pendingKeys。
func (r *Resolver) SetPending(p string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p == "" {
		r.pendingKeys = nil
	} else {
		r.pendingKeys = []string{p}
	}
	r.pendingMode = ""
}

// GetPending 获取当前 pending 状态。
// （M11-01）：返回 pendingKeys 拼接字符串（兼容旧接口）。
func (r *Resolver) GetPending() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.pendingKeys) == 0 {
		return ""
	}
	return strings.Join(r.pendingKeys, "")
}

// Reset 重置 Resolver 状态（清空 pendingKeys）。
func (r *Resolver) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingKeys = nil
	r.pendingMode = ""
}
