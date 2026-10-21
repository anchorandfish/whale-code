// alias.go — 命令别名机制（M09-02 改造，）
// 支持用户自定义别名 + $1/$2/$* 参数替换，对标 shell alias + 同类 CLI code aliases.json @2026-03-31。
// 实现 hook.Reloadable 接口，可被 HotReloader 监听外部修改（C14 复用承诺）。
// 单层展开（不递归），避免 A→B→A 循环。
package command

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Alias 定义一条命令别名（M09-02 改造，）
// 对标 shell alias + 同类 CLI code aliases.json @2026-03-31
type Alias struct {
	Name        string `json:"name"`        // 别名（如 cp，不含 / 前缀）
	Target      string `json:"target"`      // 目标命令（如 commit-push-pr，不含 / 前缀）
	Args        string `json:"args"`        // 参数模板（如 "$1 --draft"，支持 $1/$2/$*）
	Description string `json:"description"` // 描述
}

// AliasStore 别名存储器（M09-02 改造，）
// 实现 hook.Reloadable 接口，可被 HotReloader 监听外部修改（C14 复用承诺）
// 单层展开（Expand 不递归调用自身），避免 A→B→A 循环
type AliasStore struct {
	mu       sync.RWMutex
	aliases  map[string]Alias
	filePath string // ~/.whale/aliases.json
}

// NewAliasStore 创建别名存储器，filePath 为持久化文件路径（M09-02 改造，）
// 启动期自动加载已有别名文件（失败降级为空别名表）
func NewAliasStore(filePath string) *AliasStore {
	a := &AliasStore{
		aliases:  make(map[string]Alias),
		filePath: filePath,
	}
	if filePath != "" {
		_ = a.loadFromFile() // 加载失败降级为空别名表
	}
	return a
}

// Expand 展开别名，支持 $1/$2/$* 参数替换（单层展开，不递归）
// 输入：/cp feature-x → 别名 cp = {Target: commit-push-pr, Args: "$1 --draft"}
// 输出：/commit-push-pr feature-x --draft
// OPT-03：$1 未提供时替换为空字符串（shell 语义）
// 非别名输入原样返回（不修改）
func (a *AliasStore) Expand(input string) string {
	if input == "" || a == nil {
		return input
	}
	// 去掉 / 前缀
	cmd := strings.TrimPrefix(input, "/")
	// 按空格切分首词
	parts := strings.SplitN(cmd, " ", 2)
	aliasName := strings.ToLower(parts[0])
	userArgs := ""
	if len(parts) > 1 {
		userArgs = parts[1]
	}
	a.mu.RLock()
	alias, ok := a.aliases[aliasName]
	a.mu.RUnlock()
	if !ok {
		return input // 非别名，原样返回
	}
	// 单层展开（不递归，避免循环 A→B→A）
	expanded := alias.Target
	if alias.Args != "" {
		// 替换 $1/$2/$* 占位符
		argList := splitArgs(userArgs)
		expanded += " " + substituteArgs(alias.Args, argList)
	} else if userArgs != "" {
		expanded += " " + userArgs
	}
	return "/" + expanded
}

// Add 添加别名（同名覆盖，持久化到文件）
func (a *AliasStore) Add(name, target, args, desc string) error {
	if name == "" || target == "" {
		return fmt.Errorf("alias name and target must not be empty")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.aliases[strings.ToLower(name)] = Alias{
		Name:        strings.ToLower(name),
		Target:      target,
		Args:        args,
		Description: desc,
	}
	return a.saveToFileLocked()
}

// List 列出所有别名（按名称排序）
func (a *AliasStore) List() []Alias {
	a.mu.RLock()
	defer a.mu.RUnlock()
	result := make([]Alias, 0, len(a.aliases))
	for _, alias := range a.aliases {
		result = append(result, alias)
	}
	// 按名称排序
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// Remove 删除别名（持久化到文件）
func (a *AliasStore) Remove(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	name = strings.ToLower(name)
	if _, ok := a.aliases[name]; !ok {
		return fmt.Errorf("alias %q not found", name)
	}
	delete(a.aliases, name)
	return a.saveToFileLocked()
}

// Reload 实现 hook.Reloadable 接口（C14 复用承诺，别名文件热重载）
// 失败时保留旧别名表 + 返回错误
// 文件不存在视为空别名表（清空内存）
func (a *AliasStore) Reload(path string) error {
	if path == "" {
		return fmt.Errorf("reload: path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在视为空别名表
			a.mu.Lock()
			a.aliases = make(map[string]Alias)
			a.filePath = path
			a.mu.Unlock()
			return nil
		}
		return fmt.Errorf("reload aliases from %s: %w", path, err)
	}
	var aliases []Alias
	if err := json.Unmarshal(data, &aliases); err != nil {
		return fmt.Errorf("parse aliases file %s: %w", path, err)
	}
	a.mu.Lock()
	a.aliases = make(map[string]Alias, len(aliases))
	for _, al := range aliases {
		a.aliases[strings.ToLower(al.Name)] = al
	}
	a.filePath = path
	a.mu.Unlock()
	return nil
}

// loadFromFile 从文件加载别名（启动期，失败返回 error 但构造函数忽略）
func (a *AliasStore) loadFromFile() error {
	return a.Reload(a.filePath)
}

// saveToFileLocked 保存别名到文件（调用方需持锁）
func (a *AliasStore) saveToFileLocked() error {
	if a.filePath == "" {
		return nil
	}
	// 收集别名并排序（与 List 一致，保证文件内容稳定）
	result := make([]Alias, 0, len(a.aliases))
	for _, alias := range a.aliases {
		result = append(result, alias)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal aliases: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(a.filePath), 0700); err != nil {
		return fmt.Errorf("mkdir for aliases file: %w", err)
	}
	// OPT-05：文件权限 0600（别名可能含敏感命令）
	return os.WriteFile(a.filePath, data, 0600)
}

// splitArgs 按空格切分参数（简单实现，不处理引号；引号场景由 M09-05 shellquote 处理）
func splitArgs(args string) []string {
	args = strings.TrimSpace(args)
	if args == "" {
		return nil
	}
	return strings.Fields(args)
}

// argPlaceholderRe 匹配 $1/$2/$* 占位符
var argPlaceholderRe = regexp.MustCompile(`\$(\d+|\*)`)

// substituteArgs 替换参数模板中的 $1/$2/$* 占位符
// OPT-03：未提供的 $N 替换为空字符串（shell 语义）
// $* 替换为所有参数用空格连接
func substituteArgs(template string, args []string) string {
	return argPlaceholderRe.ReplaceAllStringFunc(template, func(match string) string {
		if match == "$*" {
			return strings.Join(args, " ")
		}
		// $1 → args[0], $2 → args[1], ...
		idx := int(match[1] - '0')
		if idx >= 1 && idx <= len(args) {
			return args[idx-1]
		}
		return "" // 未提供的 $N 替换为空（shell 语义）
	})
}
