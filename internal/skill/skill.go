// Package skill - skill loading, discovery and execution
package skill

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// skillNameReg matches characters that are not allowed in skill names
var skillNameReg = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// Skill 是 skill 定义结构体
// Source 字段重定义为信任级别（builtin/user/project/plugin），原路径语义迁至 FilePath
type Skill struct {
	Name         string   `json:"name"          yaml:"name"`
	Description  string   `json:"description"   yaml:"description"`
	Prompt       string   `json:"prompt"        yaml:"-"`
	Source       string   `json:"source"        yaml:"-"` // 信任级别：builtin/user/project/plugin
	FilePath     string   `json:"file_path"     yaml:"-"` // 原 Source 的路径语义
	MinVersion   string   `json:"min_version"   yaml:"min_version"`
	AllowedTools []string `json:"allowed_tools" yaml:"allowed-tools"` // 工具白名单
	WhenToUse    string   `json:"when_to_use"   yaml:"when_to_use"`   // LLM 选择提示
	Model        string   `json:"model"         yaml:"model"`         // 强制 model
}

// SourceDir 标记 skill 加载目录的来源信任级别
// Source 字段值：builtin / user / project / plugin（本批次仅 user/project）
type SourceDir struct {
	Path   string
	Source string
}

// SkillResult skill 执行结果含元数据（allowedTools/model 等供调用方注入）
type SkillResult struct {
	Prompt       string
	AllowedTools []string
	Model        string
	WhenToUse    string
	Source       string
}

// Manager skill 管理器，dirs 携带来源信任级别
type Manager struct {
	mu             sync.RWMutex
	skills         map[string]Skill
	dirs           []SourceDir
	CurrentVersion string

	// 来源信任门（B1 = S19-H-A）。字段与 `internal/hook` 的信任门同构（见 trust.go）。
	trustEnabled bool
	trustPath    string
	trustRecords map[string]skillTrustRecord
}

// NewManager 创建 skill 管理器，dirs 参数为可变 SourceDir（携带来源信任级别）
func NewManager(dirs ...SourceDir) *Manager {
	return &Manager{
		skills: make(map[string]Skill),
		dirs:   dirs,
	}
}

// LoadAll 按 dirs 顺序加载所有 skill（后者覆盖前者，project 优先级高于 user）
func (m *Manager) LoadAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dir := range m.dirs {
		if err := m.loadFromDir(dir.Path, dir.Source); err != nil {
			continue // 目录不存在视为无 skill，不向上传播错误
		}
	}
	return nil
}

// loadFromDir 从指定目录加载 skill，source 标记来源信任级别
// 同名 skill 按目录顺序覆盖（后者胜），覆盖时记录日志
func (m *Manager) loadFromDir(dir string, source string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") && !strings.HasSuffix(name, ".skill") {
			continue
		}

		fullPath := filepath.Join(dir, name)
		s, err := parseSkillFile(fullPath)
		if err != nil {
			continue
		}

		if !validateSkill(s, m.CurrentVersion) {
			log.Printf("[skill] 校验失败跳过: %s (name=%q)", fullPath, s.Name)
			continue
		}

		s.Source = source

		// 覆盖日志：同名 skill 后加载者覆盖先加载者
		if _, exists := m.skills[s.Name]; exists {
			log.Printf("[skill] 同名 skill 覆盖: %s (旧=%s ← 新=%s)", s.Name, m.skills[s.Name].Source, source)
		}

		m.skills[s.Name] = s
	}
	return nil
}

// parseSkillFile 解析 skill 文件（YAML frontmatter + Markdown body）
// 用 yaml.v3 替代手写 strings.HasPrefix 逐行解析，支持多行 description 和数组 allowed-tools
// 按行扫描找首尾 --- 行再切片，避免正文 --- 误解析
func parseSkillFile(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	content := string(data)
	var s Skill
	s.FilePath = path

	// 按行扫描找首尾 --- 行
	lines := strings.Split(content, "\n")
	frontmatterStart := -1
	frontmatterEnd := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if frontmatterStart == -1 {
				frontmatterStart = i
			} else {
				frontmatterEnd = i
				break
			}
		}
	}

	if frontmatterStart >= 0 && frontmatterEnd > frontmatterStart {
		// 提取 frontmatter 块（首尾 --- 之间）
		frontmatter := strings.Join(lines[frontmatterStart+1:frontmatterEnd], "\n")
		var meta struct {
			Name         string   `yaml:"name"`
			Description  string   `yaml:"description"`
			MinVersion   string   `yaml:"min_version"`
			AllowedTools []string `yaml:"allowed-tools"`
			WhenToUse    string   `yaml:"when_to_use"`
			Model        string   `yaml:"model"`
		}
		if err := yaml.Unmarshal([]byte(frontmatter), &meta); err != nil {
			// 解析失败降级：prompt 用全文，meta 字段为空
			log.Printf("[skill] yaml 解析失败降级用全文: %s (%v)", path, err)
			s.Prompt = content
		} else {
			s.Name = meta.Name
			s.Description = meta.Description
			s.MinVersion = meta.MinVersion
			s.AllowedTools = meta.AllowedTools
			s.WhenToUse = meta.WhenToUse
			s.Model = meta.Model
			s.Prompt = strings.TrimSpace(strings.Join(lines[frontmatterEnd+1:], "\n"))
		}
	} else {
		// 无 frontmatter，prompt 用全文
		s.Prompt = content
	}

	// 无 name 用文件名
	if s.Name == "" {
		base := filepath.Base(path)
		s.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	s.Name = skillNameReg.ReplaceAllString(s.Name, "-")

	return s, nil
}

// validateSkill 校验 skill 字段合法性
// name 非空、description ≤ 500 字符（按 utf8 rune 计，对中文友好；500 中文字 = 1500 字节，按字节计会误拦）、min_version 兼容
// 校验失败返回 false，调用方应静默跳过并记录日志
func validateSkill(s Skill, currentVersion string) bool {
	if s.Name == "" {
		return false
	}
	if utf8.RuneCountInString(s.Description) > 500 {
		return false
	}
	if s.MinVersion != "" && currentVersion != "" {
		if !isVersionCompatible(currentVersion, s.MinVersion) {
			return false
		}
	}
	return true
}

// Get 获取指定名称的 skill
func (m *Manager) Get(name string) (Skill, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	skill, ok := m.skills[name]
	return skill, ok
}

// Set 直接注入或覆盖 skill（供测试与未来 plugin 注册链路使用）
func (m *Manager) Set(name string, s Skill) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.skills[name] = s
}

// List 列出所有 skill
func (m *Manager) List() []Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var skills []Skill
	for _, s := range m.skills {
		skills = append(skills, s)
	}
	return skills
}

// Execute 执行 skill，返回渲染后的 prompt 字符串（向后兼容保留）
func (m *Manager) Execute(name string, userPrompt string) (string, error) {
	m.mu.RLock()
	skill, ok := m.skills[name]
	m.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("skill %q does not exist, available skills: %s", name, m.listNames())
	}

	// B1（S19-H-A）：正文出门前的唯一兜底判定（门未启用 ⇒ 放行）。见 trust.go ensureInjectionAllowed。
	if err := m.ensureInjectionAllowed(name); err != nil {
		return "", err
	}

	// 简单模板替换：将 {{prompt}} 替换为用户输入
	result := skill.Prompt
	if userPrompt != "" {
		result = strings.ReplaceAll(result, "{{prompt}}", userPrompt)
		// 模板无 {{prompt}} 占位符时追加用户输入
		if !strings.Contains(skill.Prompt, "{{prompt}}") {
			result = result + "\n\n" + userPrompt
		}
	}

	return result, nil
}

// ExecuteWithMeta 执行 skill 并返回元数据（allowedTools/model 等供调用方注入）
// 保留旧 Execute 向后兼容
func (m *Manager) ExecuteWithMeta(name string, userPrompt string) (SkillResult, error) {
	m.mu.RLock()
	skill, ok := m.skills[name]
	m.mu.RUnlock()

	if !ok {
		return SkillResult{}, fmt.Errorf("skill %q does not exist, available skills: %s", name, m.listNames())
	}

	// B1（S19-H-A）：正文出门前的唯一兜底判定（门未启用 ⇒ 放行）。见 trust.go ensureInjectionAllowed。
	if err := m.ensureInjectionAllowed(name); err != nil {
		return SkillResult{}, err
	}

	// 复用 Execute 的模板替换逻辑
	result := skill.Prompt
	if userPrompt != "" {
		result = strings.ReplaceAll(result, "{{prompt}}", userPrompt)
		if !strings.Contains(skill.Prompt, "{{prompt}}") {
			result = result + "\n\n" + userPrompt
		}
	}

	return SkillResult{
		Prompt:       result,
		AllowedTools: skill.AllowedTools,
		Model:        skill.Model,
		WhenToUse:    skill.WhenToUse,
		Source:       skill.Source,
	}, nil
}

// listNames 列出所有 skill 名称（逗号分隔）
func (m *Manager) listNames() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.skills))
	for name := range m.skills {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// FormatList 格式化 skill 列表
func (m *Manager) FormatList() string {
	skills := m.List()
	if len(skills) == 0 {
		return "No skills loaded. Create .md files in .whale/skills/ directory to add skills。"
	}

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("loaded %d skills:\n", len(skills)))
	// 信任门可见性：门开启时，project/plugin 来源的 skill 必须亮出
	// [untrusted]/[trusted]/[modified] 状态列 —— 否则用户只在首次使用被拦那一刻
	// 才知道门存在（拦截文案教它去 /skill trust，但列表不给任何信号）。
	// user/builtin 免确认来源不带标记；门未启用时整列隐藏（避免引导用户去确认
	// 一个不影响行为的门）。
	showTrust := m.TrustGateEnabled()
	for _, s := range skills {
		if showTrust && skillSourceNeedsConfirmation(s.Source) {
			buf.WriteString(fmt.Sprintf("  - %s [%s]: %s\n", s.Name, m.TrustStatus(s), s.Description))
			continue
		}
		buf.WriteString(fmt.Sprintf("  - %s: %s\n", s.Name, s.Description))
	}
	return buf.String()
}

// isVersionCompatible 检查 currentVersion 是否满足 minVersion 要求
func isVersionCompatible(currentVersion, minVersion string) bool {
	cv := parseVersionParts(currentVersion)
	mv := parseVersionParts(minVersion)

	maxLen := len(cv)
	if len(mv) > maxLen {
		maxLen = len(mv)
	}

	for i := 0; i < maxLen; i++ {
		var c, mvPart int
		if i < len(cv) {
			c = cv[i]
		}
		if i < len(mv) {
			mvPart = mv[i]
		}
		if c < mvPart {
			return false
		}
		if c > mvPart {
			return true
		}
	}
	return true
}

// parseVersionParts 将版本字符串解析为数字部分切片
func parseVersionParts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	if idx := strings.Index(v, "-"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	result := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, ch := range p {
			if ch >= '0' && ch <= '9' {
				n = n*10 + int(ch-'0')
			} else {
				break
			}
		}
		result = append(result, n)
	}
	return result
}
