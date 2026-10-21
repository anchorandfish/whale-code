package keybindings

import (
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/platform"
)

// LoadFromFile 从 JSON 文件加载配置（纯文件读取，路径由调用方 Manager 控制）
// D2 修复：落实 R6 路径校验，复用 platform.IsPathAllowed（EvalSymlinks→Abs→前缀检查）
// 白名单：~/.whale/（user）+ 含 .whale/ 子目录的路径（project）
// 路径校验在文件存在时进行（文件不存在直接降级返回空 Config，不触发校验）
func LoadFromFile(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}

	// 文件不存在时降级返回空 Config（不触发路径校验）
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}

	// D2 修复：R6 路径校验，阻断路径穿越/symlink 逃逸
	// D7 修复：路径校验失败接入 N6 ErrorID（ErrKeybindingsLoad）
	if !isKeybindingsPathAllowed(path) {
		return Config{}, ai.WithID(fmt.Errorf("keybindings: path '%s' is outside allowed directories (~/.whale/ or <workDir>/.whale/)", path), ai.ErrKeybindingsLoad)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// isKeybindingsPathAllowed 校验路径是否在 keybindings 配置允许的白名单内
// 复用 platform.IsPathAllowed（EvalSymlinks→Abs→前缀检查，阻断路径穿越/symlink 逃逸）
// 白名单：~/.whale/ 目录（user 配置）或任何 .whale/ 子目录（project 配置）
func isKeybindingsPathAllowed(path string) bool {
	// 收口：Dir() 返回空（home 不可得）⇒ 拒绝，行为同前。
	whaleDir := whalehome.Dir()
	if whaleDir == "" {
		return false
	}

	// 构造白名单根目录列表：~/.whale/ + 路径所在 .whale/ 目录
	roots := []string{whaleDir}

	// 若路径含 .whale/，追加其所在的 .whale 目录为白名单根
	absPath, err := filepath.Abs(path)
	if err == nil {
		cleanPath := filepath.ToSlash(absPath)
		if idx := indexOfLastDotWhale(cleanPath); idx >= 0 {
			// 截取到 .whale/ 结束的路径
			whaleRoot := filepath.FromSlash(cleanPath[:idx+len("/.whale")])
			roots = append(roots, whaleRoot)
		}
	}

	return platform.IsPathAllowed(path, roots)
}

// indexOfLastWhale 返回 cleanPath 中最后一个 "/.whale" 的起始索引，未找到返回 -1
func indexOfLastDotWhale(cleanPath string) int {
	const marker = "/.whale"
	idx := -1
	searchFrom := 0
	for {
		pos := indexOfString(cleanPath, marker, searchFrom)
		if pos < 0 {
			break
		}
		// 确认是目录边界（后面是 / 或结尾）
		end := pos + len(marker)
		if end == len(cleanPath) || cleanPath[end] == '/' {
			idx = pos
		}
		searchFrom = pos + 1
	}
	return idx
}

// indexOfString 返回 substr 在 s 中从 from 开始的首次出现索引，未找到返回 -1
func indexOfString(s, substr string, from int) int {
	if from < 0 {
		from = 0
	}
	if from >= len(s) {
		return -1
	}
	n := len(s) - len(substr)
	for i := from; i <= n; i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// MergeWithDefault 将用户配置与默认表合并
// mode=vim：DefaultVimBindings() + 用户 bindings（用户覆盖同 key）
// mode=emacs：DefaultEmacsBindings() + 用户 bindings
// mode=custom：仅用户 bindings（不合并默认表）
func MergeWithDefault(cfg Config) []Binding {
	var baseBindings []Binding

	switch cfg.Mode {
	case "emacs":
		baseBindings = DefaultEmacsBindings()
	case "custom":
		return cfg.Bindings
	case "vim":
		baseBindings = DefaultVimBindings()
	case "":
		return cfg.Bindings
	default:
		return cfg.Bindings
	}

	bindings := make([]Binding, len(baseBindings))
	copy(bindings, baseBindings)

	for _, userBinding := range cfg.Bindings {
		found := false
		for i, b := range bindings {
			if b.Key == userBinding.Key && (b.Mode == userBinding.Mode || b.Mode == "" || userBinding.Mode == "") {
				bindings[i] = userBinding
				found = true
				break
			}
		}
		if !found {
			bindings = append(bindings, userBinding)
		}
	}

	return bindings
}

// LoadAll 从多个路径加载配置并合并（project 覆盖 user）
func LoadAll(userPath, projectPath string) (Config, error) {
	userCfg, err := LoadFromFile(userPath)
	if err != nil {
		return Config{}, err
	}

	projectCfg, err := LoadFromFile(projectPath)
	if err != nil {
		return Config{}, err
	}

	if projectCfg.Mode != "" {
		userCfg.Mode = projectCfg.Mode
	}

	userCfg.Bindings = append(userCfg.Bindings, projectCfg.Bindings...)

	return userCfg, nil
}
