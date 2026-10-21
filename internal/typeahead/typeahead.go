// Package typeahead 提供 @ 文件路径输入补全（typeahead），对标同类 CLI useTypeahead.tsx + fileSuggestions.ts。
// 新增 internal/typeahead 包，实现 @/path/to 输入补全，解决"用户须手输完整路径"的高频痛点。
// P0-08 扩展：支持 # skill 触发符补全（M11-03 + M25-01 合并），与 @ 文件触发符并存。
package typeahead

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/anchorandfish/whale-code/internal/command"
)

// MaxSuggestions 最大候选数（对标 fileSuggestions.ts:617）
const MaxSuggestions = 15

// Trigger 标识补全触发符类型，用于区分 @ 文件、# skill 与 slash command 补全链路
type Trigger int

const (
	// TriggerNone 表示未命中任何触发符
	TriggerNone Trigger = iota
	// TriggerAt 表示 @ 文件路径触发符
	TriggerAt
	// TriggerHash 表示 # skill 触发符
	TriggerHash
	// TriggerSlash 表示输入开头的 / 命令触发符
	TriggerSlash
)

// SkillProvider 由调用方（tui/lifecycle.go）实现并注入，避免 typeahead 直接依赖 skill 包
// 返回的 SkillItem 列表用于 # 触发符补全候选
type SkillProvider interface {
	// ListSkills 返回所有可用 skill 列表（按 name 排序不强制，由调用方决定）
	ListSkills() []SkillItem
}

// SkillItem 描述一个可补全的 skill 元数据，与 skill.Skill 解耦
type SkillItem struct {
	Name        string // skill 名称（不含 # 前缀）
	Description string // skill 描述（用于弹窗副标题展示）
}

// CommandItem 描述一个可补全的 slash command，由 TUI 从命令注册表转换而来。
type CommandItem struct {
	Name                 string
	Description          string
	SecondaryDescription string
	Priority             int
}

// skillProvider 全局注入的 SkillProvider 实例（默认 nil，# 触发符将返回空候选）
var skillProvider SkillProvider

// SetSkillProvider 由 tui 初始化期注入 SkillProvider 实现
// 注入后 # 触发符补全将调 ListSkills() 获取候选；未注入时 # 触发符返回空列表不弹窗
func SetSkillProvider(p SkillProvider) {
	skillProvider = p
}

// Completion 补全候选项
type Completion struct {
	DisplayText          string  // 显示文本（相对 cwd 的路径、skill 或命令名称）
	Description          string  // 主说明
	SecondaryDescription string  // 辅助说明（slash command 使用中文）
	Score                int     // 模糊匹配评分（越高越优）
	IsDir                bool    // 是否目录（目录加 os.PathSeparator 后缀）
	Trigger              Trigger // 标识候选项的触发链路
}

// atTokenPattern 检测 cursor 前 @ token 的正则（对标 useTypeahead.tsx:41）
// 匹配 (^|\s)@ 后紧跟的路径字符序列，支持 Unicode 路径
var atTokenPattern = regexp.MustCompile(`(?:^|\s)@([\p{L}\p{N}\p{M}_\-./\\()[\]~:]*)$`)

// hashTokenPattern 检测 cursor 前 # token 的正则（P0-08 新增，对标同类 CLI # 触发符）
// 匹配 (^|\s)# 后紧跟的 skill 名称字符序列（仅字母数字下划线连字符）
var hashTokenPattern = regexp.MustCompile(`(?:^|\s)#([\p{L}\p{N}_\-]*)$`)

// Suggest 输入补全主入口（02 §2.2.1 主设计接口，单参数）
// input: 已提取的 token（由调用方通过 ExtractToken(input, cursorOffset) 提取，不含 @ 前缀）
// 返回按 Score 降序的候选列表（最多 MaxSuggestions=15）
// 行为：
// - token 为空 → showOnEmpty：返回顶层目录（@ 后立即弹出）
// - token 非空 → fileIndex.search(token, 15) 模糊匹配
//
// 注：cursor 处是否 @ token 由 ExtractToken 判断，Suggest 不感知 cursorOffset（保持 02 §2.2.1 单参数契约）
// 注：本函数保持向后兼容，仅处理 @ 文件补全；# skill 补全请用 SuggestWithTrigger
func Suggest(input string) []Completion {
	if input == "" {
		return showOnEmpty()
	}

	// 超长 token 截断，防止卡死
	if len(input) > 1000 {
		input = input[:1000]
	}

	// 统一路径分隔符用于匹配
	query := strings.ReplaceAll(input, "\\", "/")
	query = strings.TrimPrefix(query, "./")

	results := fi.search(query, MaxSuggestions)
	if results == nil {
		return nil
	}
	return results
}

// SuggestWithTrigger 按触发符类型分流补全（P0-08 新增，对标同类 CLI 三态补全）
// trigger: TriggerAt 走文件补全链路；TriggerHash 走 skill 补全链路
// token: 已提取的 token（不含触发符前缀）
// 返回按 Score 降序的候选列表（最多 MaxSuggestions=15）
func SuggestWithTrigger(trigger Trigger, token string) []Completion {
	switch trigger {
	case TriggerAt:
		return Suggest(token)
	case TriggerHash:
		return suggestSkills(token)
	default:
		return nil
	}
}

// HasSubcommands 判定 query 是否为「父命令」：候选集中存在以 "query " 为前缀的
// 子命令（registry 全名命令，如 "checkpoint save"）。：TUI 回车展开语义与
// SuggestCommands 的空格分流共用同一谓词实现，避免双实现漂移（铁律）。
// 注意：判定基于传入的候选集（弹窗可见面已过滤 stub/hidden），与弹窗语义一致。
func HasSubcommands(query string, commands []CommandItem) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return false
	}
	prefix := q + " "
	for _, cmd := range commands {
		name := strings.ToLower(strings.TrimSpace(cmd.Name))
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// SuggestCommands 返回 slash command 候选。常用命令按显式 Priority 排在前面，
// 其余命令按名称排序；不限制结果数，确保所有注册命令都可通过滚动访问。
//
// 分流语义：token 为「exact 命令名 + 尾随空格」时——
// - 叶子命令（无子命令注册）⇒ 返回 nil（既有语义：等参数输入，弹窗关闭）；
// - 父命令（registry 注册了 "name ..." 子命令）⇒ 返回子命令候选（不含父命令本身），
// 对齐 同类 CLI「弹窗过滤器只取首 token、参数编辑期提示不消失」与 Claude argumentHint 常驻。
func SuggestCommands(token string, commands []CommandItem) []Completion {
	query := strings.ToLower(strings.TrimSpace(token))
	excludeExactParent := false
	if strings.TrimRightFunc(token, unicode.IsSpace) != token {
		exactExists := false
		for _, cmd := range commands {
			if strings.ToLower(strings.TrimSpace(cmd.Name)) == query {
				exactExists = true
				break
			}
		}
		if exactExists {
			if !HasSubcommands(query, commands) {
				// 叶子命令等待参数：保持既有语义（/permissions 判例，关闭弹窗）
				return nil
			}
			// 父命令 + 空格：展开子命令候选，父命令本身已被敲完，不再作为候选
			excludeExactParent = true
		}
	}

	type matchedCommand struct {
		item  CommandItem
		exact bool
	}
	matches := make([]matchedCommand, 0, len(commands))
	seen := make(map[string]struct{}, len(commands))
	for _, cmd := range commands {
		name := strings.ToLower(strings.TrimSpace(cmd.Name))
		if name == "" || (query != "" && !strings.HasPrefix(name, query)) {
			continue
		}
		if excludeExactParent && name == query {
			// 父命令+空格展开时，父命令本身已被敲完，不再作为候选
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		matches = append(matches, matchedCommand{item: cmd, exact: name == query})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].exact != matches[j].exact {
			return matches[i].exact
		}
		leftPriority := matches[i].item.Priority
		rightPriority := matches[j].item.Priority
		if leftPriority > 0 || rightPriority > 0 {
			if leftPriority == 0 {
				return false
			}
			if rightPriority == 0 {
				return true
			}
			if leftPriority != rightPriority {
				return leftPriority < rightPriority
			}
		}
		return strings.ToLower(matches[i].item.Name) < strings.ToLower(matches[j].item.Name)
	})

	results := make([]Completion, 0, len(matches))
	for _, match := range matches {
		results = append(results, Completion{
			DisplayText:          match.item.Name,
			Description:          match.item.Description,
			SecondaryDescription: match.item.SecondaryDescription,
			Trigger:              TriggerSlash,
		})
	}
	return results
}

// suggestSkills 从注入的 SkillProvider 获取 skill 列表并模糊匹配
// token 为空时返回全部 skill（最多 MaxSuggestions），非空时按前缀/子串模糊匹配
// SkillProvider 未注入时返回 nil（不弹窗）
func suggestSkills(token string) []Completion {
	if skillProvider == nil {
		return nil
	}

	skills := skillProvider.ListSkills()
	if len(skills) == 0 {
		return nil
	}

	// 超长 token 截断
	if len(token) > 1000 {
		token = token[:1000]
	}

	token = strings.ToLower(token)

	var results []Completion
	for _, s := range skills {
		name := s.Name
		desc := s.Description
		score := skillMatchScore(token, name, desc)
		if score < 0 {
			continue
		}
		results = append(results, Completion{
			DisplayText: name,
			Description: desc,
			Score:       score,
			IsDir:       false,
			Trigger:     TriggerHash,
		})
		if len(results) >= MaxSuggestions {
			break
		}
	}

	// 按 Score 降序排列
	sortByScoreDesc(results)
	return results
}

// skillMatchScore 计算 skill 名称/描述与查询的匹配得分
// 返回 -1 表示不匹配；正数越大越优
// 匹配规则：前缀匹配 > 子串匹配 > 模糊字符序列匹配
func skillMatchScore(query, name, desc string) int {
	if query == "" {
		return 100 // 空查询全匹配（showOnEmpty 场景）
	}
	nameLower := strings.ToLower(name)
	// 前缀完全匹配最高分
	if strings.HasPrefix(nameLower, query) {
		return 200
	}
	// 子串匹配次之
	if strings.Contains(nameLower, query) {
		return 150
	}
	// 模糊字符序列匹配（按顺序包含 query 各字符）
	if fuzzyContains(nameLower, query) {
		return 100
	}
	return -1
}

// fuzzyContains 检查 s 是否按顺序包含 query 的所有字符（不限连续）
// 用于支持如 "mem" 匹配 "memory-add" 这类缩写输入
func fuzzyContains(s, query string) bool {
	if query == "" {
		return true
	}
	i := 0
	for j := 0; j < len(s) && i < len(query); j++ {
		if s[j] == query[i] {
			i++
		}
	}
	return i == len(query)
}

// showOnEmpty 返回顶层目录候选项（@ 后立即弹出，对标 useTypeahead.tsx:453 showOnEmpty）
func showOnEmpty() []Completion {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}

	entries, err := os.ReadDir(cwd)
	if err != nil {
		return nil
	}

	var results []Completion
	for _, entry := range entries {
		name := entry.Name()
		// 跳过隐藏目录和常见忽略目录
		if strings.HasPrefix(name, ".") {
			continue
		}
		isDir := entry.IsDir()
		results = append(results, Completion{
			DisplayText: name,
			Score:       100,
			IsDir:       isDir,
		})
		if len(results) >= MaxSuggestions {
			break
		}
	}
	return results
}

// ExtractToken 提取 cursor 处的可补全 token（对标 useTypeahead.tsx:41）
// 返回 token（不含 @ 前缀）、tokenStartPos（@ 位置）、isAtSymbol（是否 @ 触发）
// 若 cursor 不在 @ token 上，返回 ("", -1, false)
// 注：本函数保持向后兼容，仅识别 @ 触发符；如需同时识别 # 请用 ExtractTokenWithTrigger
func ExtractToken(input string, cursorOffset int) (token string, startPos int, isAtSymbol bool) {
	if input == "" || cursorOffset < 0 {
		return "", -1, false
	}

	// cursor 超出范围截断
	if cursorOffset > len(input) {
		cursorOffset = len(input)
	}

	// 取 cursor 前的子串
	before := input[:cursorOffset]

	// 正则匹配 cursor 前 @ token
	match := atTokenPattern.FindStringSubmatch(before)
	if match == nil {
		return "", -1, false
	}

	// match[0] 是完整匹配（含前导空格或 ^），match[1] 是 token（不含 @）
	token = match[1]
	if token == "" {
		// @ 后无字符，showOnEmpty 场景
		// startPos 指向 @ 位置
		atIdx := strings.LastIndex(before, "@")
		if atIdx < 0 {
			return "", -1, false
		}
		return "", atIdx, true
	}

	// 计算 @ 的位置
	// match[0] 可能以空格开头，@ 位置 = cursorOffset - len(token) - 1
	startPos = cursorOffset - len(token) - 1
	if startPos < 0 {
		startPos = 0
	}

	return token, startPos, true
}

// ExtractTokenWithTrigger 提取 cursor 处的可补全 token 并返回触发符类型（P0-08 新增）
// 同时支持 @ 文件、# skill 和输入开头的 / command 触发符，与 ExtractToken 互补
// 返回 token（不含触发符前缀）、tokenStartPos（触发符位置）、trigger（触发符类型）
// 若 cursor 不在任何触发符 token 上，返回 ("", -1, TriggerNone)
func ExtractTokenWithTrigger(input string, cursorOffset int) (token string, startPos int, trigger Trigger) {
	if input == "" || cursorOffset < 0 {
		return "", -1, TriggerNone
	}

	// cursor 超出范围截断
	if cursorOffset > len(input) {
		cursorOffset = len(input)
	}

	// 取 cursor 前的子串
	before := input[:cursorOffset]

	// 优先匹配 @ 文件触发符（保持与 ExtractToken 一致的优先级）
	if match := atTokenPattern.FindStringSubmatch(before); match != nil {
		token = match[1]
		if token == "" {
			atIdx := strings.LastIndex(before, "@")
			if atIdx < 0 {
				return "", -1, TriggerNone
			}
			return "", atIdx, TriggerAt
		}
		startPos = cursorOffset - len(token) - 1
		if startPos < 0 {
			startPos = 0
		}
		return token, startPos, TriggerAt
	}

	// 匹配 # skill 触发符
	if match := hashTokenPattern.FindStringSubmatch(before); match != nil {
		token = match[1]
		if token == "" {
			hashIdx := strings.LastIndex(before, "#")
			if hashIdx < 0 {
				return "", -1, TriggerNone
			}
			return "", hashIdx, TriggerHash
		}
		startPos = cursorOffset - len(token) - 1
		if startPos < 0 {
			startPos = 0
		}
		return token, startPos, TriggerHash
	}

	// Slash commands 只在输入开头触发，避免普通文本中的 / 和 URL 误触。
	// 改走唯一谓词 —— 但用的是 `IsCommandCompletionInput`（补全侧），**不是** `IsCommandInput`。
	// 两者是**两个问题**：提交侧裸 `/` 无可执行命令名 ⇒ false；补全侧裸 `/` 正是"看全部命令" ⇒ true。
	// 前导空白是**正式逃逸语义**（三入口统一），故不再 TrimLeft 后判
	// （那会把 ` /permissions` 也当命令，与 TUI / --plain 打架）。
	if command.IsCommandCompletionInput(before) {
		startPos = 0
		return before[1:], startPos, TriggerSlash
	}

	return "", -1, TriggerNone
}

// ApplySuggestion 应用补全到输入（对标 useTypeahead.tsx:237）
// tokenStartPos: 触发符（@、# 或 /）的位置；tokenLength: 被替换的 token 长度
// 文件目录加 string(os.PathSeparator)，文件加空格；skill 一律加空格
// 返回新 input 和新 cursorOffset
// 注：根据 suggestion.Trigger 决定后缀；旧调用方未设置 Trigger 时按 IsDir 决定（向后兼容）
func ApplySuggestion(input string, suggestion Completion, tokenStartPos, tokenLength int) (newInput string, newCursorOffset int) {
	if tokenStartPos < 0 || tokenStartPos >= len(input) {
		return input, len(input)
	}

	// 替换范围：触发符后的 token 部分
	replaceStart := tokenStartPos + 1 // 跳过触发符（@、# 或 /）
	replaceEnd := replaceStart + tokenLength
	if replaceEnd > len(input) {
		replaceEnd = len(input)
	}

	// 构建替换文本，按 Trigger 决定后缀
	replacement := suggestion.DisplayText
	if suggestion.Trigger == TriggerHash || suggestion.Trigger == TriggerSlash {
		// skill 和 slash command 补全一律加空格（如 "#memory-add "、"/permissions "）
		// FindLongestCommonPrefix may include the separator for multi-word commands;
		// normalize it here so applying a completion never creates two spaces.
		replacement = strings.TrimRightFunc(replacement, unicode.IsSpace)
		replacement += " "
	} else if suggestion.IsDir {
		// 文件目录加路径分隔符
		replacement += string(os.PathSeparator)
	} else {
		// 文件加空格
		replacement += " "
	}

	// 拼接新输入
	before := input[:replaceStart]
	after := ""
	if replaceEnd < len(input) {
		after = input[replaceEnd:]
	}
	if suggestion.Trigger == TriggerSlash {
		// Command completion owns the separator before arguments; avoid producing
		// two spaces when the user already has horizontal whitespace after the
		// command. Preserve line breaks and everything after them verbatim.
		after = strings.TrimLeftFunc(after, isHorizontalWhitespace)
	}
	if (suggestion.Trigger == TriggerHash || suggestion.Trigger == TriggerSlash) && startsLineBreak(after) {
		replacement = strings.TrimRightFunc(replacement, unicode.IsSpace)
	}
	newInput = before + replacement + after
	newCursorOffset = len(before) + len(replacement)

	return newInput, newCursorOffset
}

func isHorizontalWhitespace(r rune) bool {
	if !unicode.IsSpace(r) {
		return false
	}
	switch r {
	case '\n', '\r', '\v', '\f', '\u0085', '\u2028', '\u2029':
		return false
	default:
		return true
	}
}

func startsLineBreak(input string) bool {
	if input == "" {
		return false
	}
	r := []rune(input)[0]
	switch r {
	case '\n', '\r', '\u0085', '\u2028', '\u2029':
		return true
	default:
		return false
	}
}

// FindLongestCommonPrefix 取候选列表的最长公共前缀（对标 fileSuggestions.ts:587-598）
// 用于 Tab 一键补全
func FindLongestCommonPrefix(suggestions []Completion) string {
	if len(suggestions) == 0 {
		return ""
	}
	strs := make([]string, len(suggestions))
	for i, s := range suggestions {
		strs[i] = s.DisplayText
	}
	return longestCommonPrefix(strs)
}
