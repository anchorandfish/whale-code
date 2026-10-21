package keybindings

import (
	"errors"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbletea"
)

// Key 按键的规范化表示，封装 bubbletea.KeyMsg
type Key struct {
	Type  tea.KeyType
	Runes []rune
	Alt   bool
}

// FromKeyMsg 从 bubbletea.KeyMsg 构造 Key
func FromKeyMsg(msg tea.KeyMsg) Key {
	return Key{
		Type:  msg.Type,
		Runes: msg.Runes,
		Alt:   msg.Alt,
	}
}

// String 返回按键的规范化字符串（"ctrl+d"/"alt+x"/"j"/"esc"/"up"/"dd"）
// 用于配置文件匹配和日志输出
func (k Key) String() string {
	var parts []string

	if k.Alt {
		parts = append(parts, "alt")
	}

	switch k.Type {
	case tea.KeyCtrlA, tea.KeyCtrlB, tea.KeyCtrlC, tea.KeyCtrlD,
		tea.KeyCtrlE, tea.KeyCtrlF, tea.KeyCtrlG, tea.KeyCtrlH,
		tea.KeyCtrlJ, tea.KeyCtrlK, tea.KeyCtrlL,
		tea.KeyCtrlN, tea.KeyCtrlO, tea.KeyCtrlP,
		tea.KeyCtrlQ, tea.KeyCtrlR, tea.KeyCtrlS, tea.KeyCtrlT,
		tea.KeyCtrlU, tea.KeyCtrlV, tea.KeyCtrlW, tea.KeyCtrlX,
		tea.KeyCtrlY, tea.KeyCtrlZ:
		parts = append(parts, "ctrl")
		parts = append(parts, string(rune('a'+int(k.Type)-int(tea.KeyCtrlA))))
	case tea.KeyEsc:
		parts = append(parts, "esc")
	case tea.KeyEnter:
		parts = append(parts, "enter")
	case tea.KeySpace:
		parts = append(parts, "space")
	case tea.KeyTab:
		parts = append(parts, "tab")
	case tea.KeyBackspace:
		parts = append(parts, "backspace")
	case tea.KeyDelete:
		parts = append(parts, "delete")
	case tea.KeyUp:
		parts = append(parts, "up")
	case tea.KeyDown:
		parts = append(parts, "down")
	case tea.KeyLeft:
		parts = append(parts, "left")
	case tea.KeyRight:
		parts = append(parts, "right")
	case tea.KeyHome:
		parts = append(parts, "home")
	case tea.KeyEnd:
		parts = append(parts, "end")
	case tea.KeyPgUp:
		parts = append(parts, "pgup")
	case tea.KeyPgDown:
		parts = append(parts, "pgdown")
	case tea.KeyRunes:
		if len(k.Runes) > 0 {
			parts = append(parts, string(k.Runes))
		}
	default:
		if len(k.Runes) > 0 {
			parts = append(parts, string(k.Runes))
		}
	}

	return strings.Join(parts, "+")
}

// ParseString 将配置字符串解析为 Key（"ctrl+d" → Key{Type: KeyCtrlD}）
// 支持格式：单字符 "j"、组合键 "ctrl+d"/"alt+x"、功能键 "up"/"down"/"esc"
func ParseString(s string) (Key, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), "+")
	if len(parts) == 0 {
		return Key{}, ai.WithID(errors.New("keybindings: empty key string"), ai.ErrKeybindingsInvalid)
	}

	var k Key
	for _, part := range parts {
		switch part {
		case "ctrl":
			k.Type = tea.KeyCtrlA
		case "alt":
			k.Alt = true
		case "esc":
			k.Type = tea.KeyEsc
		case "enter":
			k.Type = tea.KeyEnter
		case "space":
			k.Type = tea.KeySpace
		case "tab":
			k.Type = tea.KeyTab
		case "backspace":
			k.Type = tea.KeyBackspace
		case "delete":
			k.Type = tea.KeyDelete
		case "up":
			k.Type = tea.KeyUp
		case "down":
			k.Type = tea.KeyDown
		case "left":
			k.Type = tea.KeyLeft
		case "right":
			k.Type = tea.KeyRight
		case "home":
			k.Type = tea.KeyHome
		case "end":
			k.Type = tea.KeyEnd
		case "pgup":
			k.Type = tea.KeyPgUp
		case "pgdown":
			k.Type = tea.KeyPgDown
		default:
			if len(part) == 1 {
				r := rune(part[0])
				k.Runes = []rune{r}
				if k.Type == tea.KeyCtrlA {
					k.Type = tea.KeyType(int(tea.KeyCtrlA) + int(r) - int('a'))
				} else {
					k.Type = tea.KeyRunes
				}
			} else {
				return Key{}, ai.WithID(errors.New("keybindings: invalid key part: "+part), ai.ErrKeybindingsInvalid)
			}
		}
	}

	return k, nil
}

// Equal 判断两个 Key 是否相等
func (k Key) Equal(other Key) bool {
	if k.Type != other.Type || k.Alt != other.Alt {
		return false
	}
	if len(k.Runes) != len(other.Runes) {
		return false
	}
	for i := range k.Runes {
		if k.Runes[i] != other.Runes[i] {
			return false
		}
	}
	return true
}

// ToKeyMap 将 Key 转换为 bubbletea 的 key.Binding，用于兼容性
func (k Key) ToKeyMap() key.Binding {
	return key.NewBinding(key.WithKeys(k.String()))
}
