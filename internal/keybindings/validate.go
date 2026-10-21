package keybindings

import (
	"fmt"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// Conflict 表示按键绑定冲突
type Conflict struct {
	Key     string
	Mode    string
	Actions []string
}

// ValidateConfig 校验配置合法性，返回错误列表
// 检查项：mode 合法、key 格式合法、action 名合法、mode 字段合法
func ValidateConfig(cfg Config) []error {
	var errors []error

	if cfg.Mode != "" && cfg.Mode != "vim" && cfg.Mode != "emacs" && cfg.Mode != "custom" {
		errors = append(errors, ai.WithID(fmt.Errorf("keybindings: invalid mode '%s', must be 'vim', 'emacs', or 'custom'", cfg.Mode), ai.ErrKeybindingsInvalid))
	}

	for i, binding := range cfg.Bindings {
		if binding.Key == "" {
			errors = append(errors, ai.WithID(fmt.Errorf("keybindings: binding[%d] has empty key", i), ai.ErrKeybindingsInvalid))
			continue
		}

		if _, err := ParseString(binding.Key); err != nil {
			errors = append(errors, ai.WithID(fmt.Errorf("keybindings: binding[%d] has invalid key '%s': %w", i, binding.Key, err), ai.ErrKeybindingsInvalid))
		}

		if !IsValidAction(binding.Action) {
			errors = append(errors, ai.WithID(fmt.Errorf("keybindings: binding[%d] has invalid action '%s'", i, binding.Action), ai.ErrKeybindingsInvalid))
		}

		if !IsValidMode(binding.Mode) {
			errors = append(errors, ai.WithID(fmt.Errorf("keybindings: binding[%d] has invalid mode '%s'", i, binding.Mode), ai.ErrKeybindingsInvalid))
		}
	}

	return errors
}

// DetectConflicts 检测同 mode 下同 key 不同 action 的冲突
// 返回冲突列表（同 key 的多个 Binding）
// 空 mode（""）语义为"所有模式都生效"，与 normal/insert 均冲突
func DetectConflicts(bindings []Binding) []Conflict {
	type keyMode struct {
		key  string
		mode string
	}

	// 按 key 聚合所有 binding
	type bindingEntry struct {
		mode   string
		action string
	}
	keyBindings := make(map[string][]bindingEntry)
	for _, b := range bindings {
		mode := b.Mode
		if mode == "" {
			mode = "_all"
		}
		keyBindings[b.Key] = append(keyBindings[b.Key], bindingEntry{mode: mode, action: b.Action})
	}

	var conflicts []Conflict
	for key, entries := range keyBindings {
		// 检查同 key 下不同 action 的冲突
		// 冲突条件：同 mode 下有不同 action，或 _all 与具名 mode 之间有不同 action
		modeActions := make(map[string]map[string]bool) // mode → set of actions
		for _, e := range entries {
			if modeActions[e.mode] == nil {
				modeActions[e.mode] = make(map[string]bool)
			}
			modeActions[e.mode][e.action] = true
		}

		// 检查同 mode 内冲突
		conflictModes := make(map[string]bool)
		for mode, actions := range modeActions {
			if len(actions) > 1 {
				conflictModes[mode] = true
			}
		}

		// 检查 _all 与具名 mode 之间冲突
		// 空 mode（""→"_all"）语义为"所有模式都生效"，与 normal/insert 均冲突
		// 只报具名 mode 的冲突（避免重复计数）
		if allActions, hasAll := modeActions["_all"]; hasAll {
			for mode, actions := range modeActions {
				if mode == "_all" {
					continue
				}
				for a := range allActions {
					if !actions[a] && len(actions) > 0 {
						conflictModes[mode] = true
					}
				}
			}
		}

		if len(conflictModes) > 0 {
			var allActions []string
			seen := make(map[string]bool)
			for _, e := range entries {
				if !seen[e.action] {
					seen[e.action] = true
					allActions = append(allActions, e.action)
				}
			}
			for mode := range conflictModes {
				conflicts = append(conflicts, Conflict{
					Key:     key,
					Mode:    mode,
					Actions: allActions,
				})
			}
		}
	}

	return conflicts
}

// CheckReserved 检查用户绑定是否覆盖保留键（Ctrl+C/Ctrl+D 退出等）
// 返回被覆盖的保留键列表
func CheckReserved(bindings []Binding) []string {
	reserved := make(map[string]bool)
	for _, k := range ReservedKeys() {
		reserved[k] = true
	}

	var overridden []string
	for _, b := range bindings {
		if reserved[b.Key] {
			overridden = append(overridden, b.Key)
		}
	}

	return uniqueStrings(overridden)
}

// ReservedKeys 返回保留键列表（不可被用户覆盖）
func ReservedKeys() []string {
	return []string{"ctrl+c", "ctrl+d"}
}

// uniqueStrings 返回去重后的字符串列表
func uniqueStrings(s []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, str := range s {
		if !seen[str] {
			seen[str] = true
			result = append(result, str)
		}
	}
	return result
}
