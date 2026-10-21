package permission

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// RuleBehavior rule behavior
type RuleBehavior string

const (
	RuleAllow RuleBehavior = "allow"
	RuleDeny  RuleBehavior = "deny"
	RuleAsk   RuleBehavior = "ask"
)

// RuleSource rule source (priority from low to high)
type RuleSource string

const (
	SourceCLIArg          RuleSource = "cliArg"
	SourceSession         RuleSource = "session"
	SourceLocalSettings   RuleSource = "localSettings"
	SourceProjectSettings RuleSource = "projectSettings"
	SourceUserSettings    RuleSource = "userSettings"
)

// RepoDistributed 报告该规则来源是否**落在工作区内、可随仓库分发**。
//
// 项目级/本地级规则文件（`<workDir>/.whale/settings*.json`）
// 是随目录走的 —— 从 clone 一个仓库到运行 `whale` 之间，没有任何一步让用户
// 看过这份文件。因此「他人写的 allow 规则」与「本机用户自己按下的 Yes」
// 在安全账上不等价：前者**不得**降低本机的确认门槛。
//
// ★ 这是**唯一**的「来源是否可分发」判据。调用方（permission.ruleCheckLocked）
// 必须用它，不得就地再写 `s == SourceProjectSettings || s == SourceLocalSettings`
// —— 那正是本仓"两份清单漂移"的经典形态（新增来源时会漏改一侧）。
//
// 为什么不算 SourceSession / SourceCLIArg：session 规则来自用户在本次运行中
// 的显式批准（`/allow`、确认框上的"本会话记住"），CLI 参数来自用户**本次**
// 敲下的命令行 —— 两者都是本机用户的即时意图，不存在"随仓库分发"的路径。
func (s RuleSource) RepoDistributed() bool {
	return s == SourceProjectSettings || s == SourceLocalSettings
}

// PriorityMode 规则来源优先级模式（，02 §4 仲裁契约变更）。
// - PriorityLegacy（默认）：user < project < local < session < cliArg（向后兼容）
// - PriorityPosix：local < project < user < session < cliArg（对齐 POSIX ACL）
type PriorityMode string

const (
	// PriorityLegacy 旧优先级模式（默认，向后兼容）。
	// cliArg > session > local > project > user（CLI 显式意图覆盖所有）。
	PriorityLegacy PriorityMode = "legacy"
	// PriorityPosix POSIX ACL 对齐模式（opt-in）。
	// 对标 POSIX ACL（@https://pubs.opengroup.org/onlinepubs/9699919799/ 访问 2026-07-21）：
	// config 层 user > project > local（user:: > group:: > other:: 特异性递减）；
	// runtime override 层 session > cliArg 仍最高（REF-CLAUDE CLI 显式意图优先）。
	PriorityPosix PriorityMode = "posix"
)

// sourcePriorityLegacy 旧优先级映射（legacy 模式，向后兼容）。
// cliArg > session > local > project > user（CLI 显式意图覆盖所有）
var sourcePriorityLegacy = map[RuleSource]int{
	SourceUserSettings:    1,
	SourceProjectSettings: 2,
	SourceLocalSettings:   3,
	SourceSession:         4,
	SourceCLIArg:          5,
}

// sourcePriorityPosix POSIX ACL 对齐优先级映射（posix 模式）。
// 对标 POSIX ACL（@https://pubs.opengroup.org/onlinepubs/9699919799/ 访问 2026-07-21）：
// user:: > group:: > other:: 特异性递减原则。
// config 层：user(3) > project(2) > local(1)；
// runtime override 层：session(4) > cliArg(5) 保留最高（REF-CLAUDE CLI 显式意图优先）。
var sourcePriorityPosix = map[RuleSource]int{
	SourceLocalSettings:   1, // other::（最不特定）
	SourceProjectSettings: 2, // group::（项目组）
	SourceUserSettings:    3, // user::（最特定，config 最高）
	SourceSession:         4, // runtime override
	SourceCLIArg:          5, // runtime override（显式意图最高）
}

// sourcePriority 向后兼容别名（= sourcePriorityLegacy，）。
// 旧代码直接引用 sourcePriority 的测试（bug_test.go 等）保持工作。
var sourcePriority = sourcePriorityLegacy

// PermissionRule permission rule
type PermissionRule struct {
	ToolName    string       `json:"tool_name"`
	RuleContent string       `json:"rule_content,omitempty"`
	Behavior    RuleBehavior `json:"behavior"`
	Source      RuleSource   `json:"source"`
	Negate      bool         `json:"negate,omitempty"`
}

// String string representation of rule
func (r PermissionRule) String() string {
	if r.RuleContent != "" {
		if r.Negate {
			return fmt.Sprintf("!(%s(%s))", r.ToolName, r.RuleContent)
		}
		return fmt.Sprintf("%s(%s)", r.ToolName, r.RuleContent)
	}
	if r.Negate {
		return fmt.Sprintf("!(%s)", r.ToolName)
	}
	return r.ToolName
}

// ParseRuleString parse rule string into PermissionRule
// format: [behavior] toolName or [behavior] toolName(content)
// negation: !(toolName) or !(toolName(content))
func ParseRuleString(input string, behavior RuleBehavior, source RuleSource) (*PermissionRule, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty rule string")
	}

	toolName := input
	ruleContent := ""
	negate := false

	// parse negation syntax !(...)
	if strings.HasPrefix(input, "!(") && strings.HasSuffix(input, ")") {
		negate = true
		inner := input[2 : len(input)-1]
		toolName = inner
	}

	// parse content-level toolName(content)
	if idx := strings.Index(toolName, "("); idx > 0 {
		if !strings.HasSuffix(toolName, ")") {
			return nil, fmt.Errorf("unclosed parenthesis in rule: %s", input)
		}
		ruleContent = toolName[idx+1 : len(toolName)-1]
		toolName = toolName[:idx]

		if ruleContent == "" {
			return nil, fmt.Errorf("empty rule content in: %s", input)
		}
		if strings.Contains(ruleContent, "(") || strings.Contains(ruleContent, ")") {
			return nil, fmt.Errorf("nested parentheses not allowed in: %s", input)
		}
	}

	// validate toolName
	if toolName == "" {
		return nil, fmt.Errorf("empty tool name in rule: %s", input)
	}

	// check illegal characters
	if strings.ContainsAny(toolName, "()!") {
		return nil, fmt.Errorf("invalid characters in tool name: %s", toolName)
	}

	return &PermissionRule{
		ToolName:    toolName,
		RuleContent: ruleContent,
		Behavior:    behavior,
		Source:      source,
		Negate:      negate,
	}, nil
}

// ruleKey generates a unique key for deduplication
func (r PermissionRule) ruleKey() string {
	return fmt.Sprintf("%s|%s|%s|%s", r.ToolName, r.RuleContent, r.Behavior, r.Source)
}

// 能力家族别名（`Read(*)` / `Edit(*)` / `Exec(*)` / …）的真相源在 capability.go。
// 此前这里是 `capabilityToolFamilies`（只有 read/edit 两个特例）；R2 起改为通用机制：
// **能力 → 工具集合**单一来源（`toolCapabilities` + `CapabilityToolsOf`），
// `Edit(*)` 从特例降为该机制的一个实例。
//
// 语义：**仅当规则内容为 "*"** 时按能力匹配；带内容的规则（`Read(/etc/*)`）
// 仍走具体工具名 + 内容匹配，避免含义不清。
//
// 未知规则名（既不是工具名也不是能力名）→ `capabilityCovers` 返回 ok=false，
// 调用方回落工具名语义，不会把具体规则误判成能力别名。

// matchesTool checks if the rule applies to the given tool name.
//
// ★ ：需一并传入 input —— 能力别名的成员判定不再只看**静态工具名**
// （`capabilityCoversCall` 会叠加「效果视图」：全只读的命令载体调用计入读家族）。
// 传空串只影响效果视图（回落到静态分桶），不影响具体工具名/内容语义。
func (r PermissionRule) matchesTool(toolName, input string) bool {
	normalizedTool := strings.ToLower(toolName)
	normalizedRule := strings.ToLower(r.ToolName)

	// exact match
	if normalizedTool == normalizedRule {
		return !r.Negate
	}

	// MCP prefix match: mcp__server matches mcp__server__tool
	if strings.HasPrefix(normalizedRule, "mcp__") && !hasDoubleUnderscoreAfterPrefix(normalizedRule, 5) {
		if strings.HasPrefix(normalizedTool, normalizedRule+"__") {
			return !r.Negate
		}
	}

	// 能力家族别名：仅当内容为 "*" 时按能力匹配。
	if r.RuleContent == "*" {
		if covered, ok := capabilityCoversCall(normalizedRule, normalizedTool, input); ok {
			return covered != r.Negate
		}
	}

	// negation rule: non-matching tools = match
	if r.Negate {
		return true
	}

	return false
}

// matchesContent checks if the rule content matches the tool input
func (r *PermissionRule) matchesContent(input string) bool {
	if r.RuleContent == "" {
		return true // tool-level rule, content not checked
	}

	content := strings.ToLower(r.RuleContent)

	// parse tool input
	parsed, cmd := extractToolCommand(input)

	_ = parsed

	// extract sub-commands for separate matching
	subCommands := splitCompoundCommand(cmd)
	for _, subCmd := range subCommands {
		subCmd = strings.TrimSpace(strings.ToLower(subCmd))
		if subCmd == "" {
			continue
		}
		if matchPattern(subCmd, content) {
			return true
		}
	}

	return false
}

// Match check if rule matches the given tool call
func (r *PermissionRule) Match(toolName, input string) bool {
	if !r.matchesTool(toolName, input) {
		return false
	}
	if r.RuleContent == "" {
		return true
	}
	return r.matchesContent(input)
}

// extractToolCommand extracts the command/input from tool input JSON
func extractToolCommand(input string) (map[string]interface{}, string) {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return nil, input
	}

	// Extract command field (Bash/PowerShell)
	if cmd, ok := parsed["command"].(string); ok {
		return parsed, cmd
	}

	// Extract file_path / path field
	if fp, ok := parsed["file_path"].(string); ok {
		return parsed, fp
	}
	if p, ok := parsed["path"].(string); ok {
		return parsed, p
	}

	return parsed, input
}

// splitCompoundCommand splits compound commands by &&, ; and |
func splitCompoundCommand(cmd string) []string {
	if cmd == "" {
		return nil
	}

	var parts []string
	current := ""
	inQuote := false
	quoteChar := rune(0)

	for _, ch := range cmd {
		switch {
		case ch == '\'' || ch == '"':
			if inQuote && ch == quoteChar {
				inQuote = false
				quoteChar = 0
			} else if !inQuote {
				inQuote = true
				quoteChar = ch
			}
			current += string(ch)
		case (ch == '&' || ch == ';' || ch == '|') && !inQuote:
			trimmed := strings.TrimSpace(current)
			if trimmed != "" {
				parts = append(parts, trimmed)
			}
			current = ""
		default:
			current += string(ch)
		}
	}

	trimmed := strings.TrimSpace(current)
	if trimmed != "" {
		parts = append(parts, trimmed)
	}

	if len(parts) == 0 {
		return []string{cmd}
	}
	return parts
}

// matchPattern matches a string against a rule pattern
// * = match all
// xxx* = prefix match
// *xxx = suffix match
// *xxx* = contains
// xxx = exact contains
func matchPattern(s, pattern string) bool {
	if pattern == "*" {
		return true
	}

	hasPrefix := strings.HasPrefix(pattern, "*")
	hasSuffix := strings.HasSuffix(pattern, "*")

	core := strings.Trim(pattern, "*")

	// for suffix wildcard patterns (e.g. "echo:*"), remove trailing colon and do prefix match
	// "echo:*" should match "echo hello" etc
	if hasSuffix && !hasPrefix {
		coreNoColon := strings.TrimSuffix(core, ":")
		if strings.HasPrefix(s, coreNoColon) {
			return true
		}
		return strings.HasPrefix(s, core)
	}

	switch {
	case hasPrefix && hasSuffix:
		return strings.Contains(s, core)
	case hasPrefix:
		return strings.HasSuffix(s, core)
	default:
		// exact match or prefix match (for command matching like "rm" matching "rm -rf ...")
		if s == core {
			return true
		}
		// prefix match: core followed by whitespace or end-of-string
		if strings.HasPrefix(s, core) {
			rest := s[len(core):]
			if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
				return true
			}
		}
		return false
	}
}

// RuleSet rule set
type RuleSet struct {
	mu           sync.RWMutex
	rules        []PermissionRule
	priorityMode PriorityMode // 优先级模式，默认 PriorityPosix（user > project > default）
}

// NewRuleSet creates a new RuleSet
// 修复 P1：默认优先级从 PriorityLegacy 改为 PriorityPosix，对齐行业惯例 user > project > default。
// Legacy 模式（user 最低）反直觉，POSIX 模式（user > project > local）符合 ACL 特异性递减原则。
// 对标：POSIX ACL（@https://pubs.opengroup.org/onlinepubs/9699919799/ 访问 2026-07-21）
func NewRuleSet() *RuleSet {
	return &RuleSet{
		rules:        make([]PermissionRule, 0),
		priorityMode: PriorityPosix, // 默认 posix（user > project > default，行业惯例）
	}
}

// PriorityMode 返回当前优先级模式（，OPT-2，供 TUI /permissions list 显示）。
func (rs *RuleSet) PriorityMode() PriorityMode {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return rs.priorityMode
}

// SetPriorityMode 设置优先级模式（，运行时切换）。
// 非法值 fail-closed 回填 PriorityPosix（与默认值一致，行业惯例 user > project > default）。
func (rs *RuleSet) SetPriorityMode(mode PriorityMode) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if mode == PriorityPosix || mode == PriorityLegacy {
		rs.priorityMode = mode
	} else {
		rs.priorityMode = PriorityPosix // 非法值 fail-closed 回填 posix（默认值）
	}
}

// currentPriorityMapLocked 根据当前模式返回对应的优先级映射（调用方须持锁）。
func (rs *RuleSet) currentPriorityMapLocked() map[RuleSource]int {
	if rs.priorityMode == PriorityPosix {
		return sourcePriorityPosix
	}
	return sourcePriorityLegacy
}

// Add adds a rule (deduplicates by key)
func (rs *RuleSet) Add(rule PermissionRule) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	key := rule.ruleKey()
	for i, existing := range rs.rules {
		if existing.ruleKey() == key {
			rs.rules[i] = rule
			return
		}
	}
	rs.rules = append(rs.rules, rule)
}

// Remove removes rules matching criteria
func (rs *RuleSet) Remove(toolName, ruleContent string, behavior RuleBehavior, source RuleSource) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	removed := 0
	filtered := make([]PermissionRule, 0, len(rs.rules))
	for _, r := range rs.rules {
		match := r.ToolName == toolName &&
			r.RuleContent == ruleContent &&
			r.Behavior == behavior &&
			r.Source == source
		if match {
			removed++
		} else {
			filtered = append(filtered, r)
		}
	}
	rs.rules = filtered
	return removed
}

// RemoveByRule removes rules matching the given rule's tool/behavior/source
func (rs *RuleSet) RemoveByRule(rule PermissionRule) int {
	return rs.Remove(rule.ToolName, rule.RuleContent, rule.Behavior, rule.Source)
}

// List returns a copy of all rules
func (rs *RuleSet) List() []PermissionRule {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	result := make([]PermissionRule, len(rs.rules))
	copy(result, rs.rules)
	return result
}

// ListBySource returns rules filtered by source
func (rs *RuleSet) ListBySource(source RuleSource) []PermissionRule {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	var result []PermissionRule
	for _, r := range rs.rules {
		if r.Source == source {
			result = append(result, r)
		}
	}
	return result
}

// GetMatchingRules returns all rules that match the given tool call,
// sorted by priority (deny > ask > allow, then by source priority).
// 根据 RuleSet.priorityMode 选择 sourcePriorityLegacy 或 sourcePriorityPosix。
func (rs *RuleSet) GetMatchingRules(toolName, input string) []PermissionRule {
	// 先在锁内读取优先级映射与匹配规则，避免排序时持锁（减少锁临界区）。
	rs.mu.RLock()
	priorityMap := rs.currentPriorityMapLocked()
	var matched []PermissionRule
	for _, r := range rs.rules {
		if r.Match(toolName, input) {
			matched = append(matched, r)
		}
	}
	rs.mu.RUnlock()

	sort.SliceStable(matched, func(i, j int) bool {
		// P1 决策：deny 绝对优先，与来源无关。
		// 对齐业内既有实现 execpolicy Decision::Forbidden 用 max 取最严（无来源权重），
		// 与 同类 CLI「deny > ask > allow 绝对优先」一致。
		// 先按行为优先级排序（deny=3 > ask=2 > allow=1），来源仅作同级 tiebreak。
		bi := behaviorPriority(matched[i].Behavior)
		bj := behaviorPriority(matched[j].Behavior)
		if bi != bj {
			return bi > bj
		}
		si := priorityMap[matched[i].Source]
		sj := priorityMap[matched[j].Source]
		return si > sj
	})

	return matched
}

// ResolveRuleDecision resolves the final decision from matching rules
func (rs *RuleSet) ResolveRuleDecision(toolName, input string) *PermissionRule {
	rules := rs.GetMatchingRules(toolName, input)
	if len(rules) == 0 {
		return nil
	}
	return &rules[0]
}

// behaviorPriority returns numeric priority for behavior comparison
func behaviorPriority(b RuleBehavior) int {
	switch b {
	case RuleDeny:
		return 3
	case RuleAsk:
		return 2
	case RuleAllow:
		return 1
	default:
		return 0
	}
}

// Count returns the number of rules
func (rs *RuleSet) Count() int {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return len(rs.rules)
}

// Clear removes all rules
func (rs *RuleSet) Clear() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.rules = make([]PermissionRule, 0)
}

// hasDoubleUnderscoreAfterPrefix checks if "__" appears at or after position `start`
func hasDoubleUnderscoreAfterPrefix(s string, start int) bool {
	if start >= len(s) {
		return false
	}
	return strings.Contains(s[start:], "__")
}

// Replace replaces all rules from a source with new rules
func (rs *RuleSet) Replace(source RuleSource, newRules []PermissionRule) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	filtered := make([]PermissionRule, 0, len(rs.rules))
	for _, r := range rs.rules {
		if r.Source != source {
			filtered = append(filtered, r)
		}
	}
	for _, r := range newRules {
		r.Source = source
		filtered = append(filtered, r)
	}
	rs.rules = filtered
}
