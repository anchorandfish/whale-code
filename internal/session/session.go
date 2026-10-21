// Package session defines core session data structures
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Role message role
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ThinkingBlockType 是「模型思考过程」在会话消息里的块类型名。
//
// 为什么要有这个常量（而不是各处写裸字面量 "thinking"）：
// 它同时被 session.TokenCount / micro_compact / context.SessionMessagesToAIEntries /
// export.{html,markdown,json} 消费 —— 是**跨包的事实名**。散落的字面量一旦有人
// 拼错（"think" / "reasoning"），症状是**静默不匹配**，没有任何编译或运行期报错。
const ThinkingBlockType = "thinking"

// ContentBlock content block — messages composed of multiple content blocks
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`

	// Plain 标记本 text 块承载的是**程序生成的纯文本**，不是模型撰写的 Markdown。
	//
	// 为什么需要这个标记（，2026-10-13）：
	// 双语文案（`util.Bi`）的形态是「英文主行 \n 三个空格 + 中文辅助行」，而
	// Markdown 有两条规矩正好打在这个形态上 ——
	//
	//	① 段落内的软换行被折成**一个空格**；
	//	② 行首 **≤3 个空格**被剥掉（4 个以上才变代码块，更难看）。
	//
	// 于是渲染后变成 "English main 中文辅助行" 挤在同一行 —— 正是用户
	// 2026-09-18 明确否决的行内并列形态。而且**终端越宽越必然**：窄终端上
	// 英文恰好折行会让人误以为"我这边是两行"（假绿）。
	//
	// 根因不在文案、也不在 Markdown，而在**渲染层分不清文本来源**：
	// `Role=assistant` 同时承担了「模型写的」与「程序写的」两种来源。
	// Plain 把这条区分在**类型上**显式化，渲染层据此走纯文本通道
	// （唯一读点：`tui/format.go` 的 `formatAssistantMessageWithThinkingSource`）。
	//
	// 为什么用布尔标记而不是新增块类型（如 "notice"）：按 `Type == "text"` 分派的
	// 消费者遍布 context 转换 / export / token 统计（十余处 switch）。新增类型会被它们
	// **静默跳过** —— 后果是模型再也看不到拒绝说明（而拒绝说明必须让模型看到，
	// 否则它会对同一目标换个工具重试）。标记法保 Type="text" ⇒ 这些消费者行为不变。
	Plain bool `json:"plain,omitempty"`

	// ToolUse field
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input string `json:"input,omitempty"`

	// ToolResult field
	ToolUseID string `json:"tool_use_id,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	Output    string `json:"output,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// Image field (multimodal image content)
	MediaType  string `json:"media_type,omitempty"`
	Base64Data string `json:"base64_data,omitempty"`
	// Base64Hash is the SHA256 hash of Base64Data (hex-encoded).
	// Used for O(1) comparison in messagesEqual instead of comparing
	// the full base64 string (P0-12). Empty when Base64Data is empty
	// or ComputeBase64Hash has not been called.
	Base64Hash string `json:"base64_hash,omitempty"`
}

// ComputeBase64Hash computes the SHA256 hash of Base64Data and stores it
// in Base64Hash (P0-12). If Base64Data is empty, Base64Hash is cleared.
//
// Call this after setting Base64Data to enable O(1) comparison via
// Base64Hash in messagesEqual. The hash is deterministic: the same
// Base64Data always produces the same Base64Hash.
func (b *ContentBlock) ComputeBase64Hash() {
	if b.Base64Data == "" {
		b.Base64Hash = ""
		return
	}
	sum := sha256.Sum256([]byte(b.Base64Data))
	b.Base64Hash = hex.EncodeToString(sum[:])
}

// Message single conversation message
//
// C7 冲突解决：Session 模块权威定义 Message 结构。
// Subtype 和 Metadata 字段供 Context 模块使用（compact_boundary/collapse_summary 等）。
// Session 模块本身不解释 Subtype/Metadata 语义，只负责存储。
//
// UUID/ParentUUID 构成消息链（用于 Compact 死分支清理和 Fork）。
// Tombstoned 标记软删除的消息（Compact 时跳过）。
type Message struct {
	Role       Role                   `json:"role"`
	UUID       string                 `json:"uuid,omitempty"`
	ParentUUID string                 `json:"parent_uuid,omitempty"`
	Blocks     []ContentBlock         `json:"blocks"`
	Usage      *TokenUsage            `json:"usage,omitempty"`
	Model      string                 `json:"model,omitempty"`
	Timestamp  int64                  `json:"timestamp"`
	Tags       []string               `json:"tags,omitempty"`
	Tombstoned bool                   `json:"tombstoned,omitempty"`
	Subtype    string                 `json:"subtype,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// TokenUsage token usage
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// SessionMetadata session metadata
type SessionMetadata struct {
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	TurnCount int    `json:"turn_count"`
	// CompactionCount is persisted in the transcript metadata record so resume
	// keeps the user-visible compaction history atomically with rewritten messages.
	CompactionCount int      `json:"compaction_count,omitempty"`
	Tags            []string `json:"tags,omitempty"`
}

// Session complete session
// All access to messages must go through the public methods:
// GetMessages(), SetMessages(), AddMessage(), AddMessageWithParent(),
// ClearMessages(), TruncateMessages()
//
// The lastUUID field tracks the UUID of the most recent non-tombstoned
// message so AddMessage can auto-link the chain (ParentUUID) without callers
// having to track it themselves. It is maintained by AddMessage /
// AddMessageWithParent and is re-derived by loadMessagesWithChain when
// loading from disk.
type Session struct {
	Version  int             `json:"version"`
	Metadata SessionMetadata `json:"metadata"`
	// messages holds the session messages (private). Use the accessor methods
	// to read or modify the message list.
	messages []Message
	mu       sync.RWMutex `json:"-"`
	// lastUUID is the UUID of the chain tail (last non-tombstoned message).
	// AddMessage auto-links new messages to this UUID via ParentUUID.
	lastUUID string `json:"-"`
	// revision changes on every transcript mutation and participates in the
	// in-process compaction CAS.
	revision uint64 `json:"-"`
}

// NewSession create new session
func NewSession() *Session {
	now := time.Now().UnixMilli()
	return &Session{
		Version: 1,
		Metadata: SessionMetadata{
			CreatedAt: now,
			UpdatedAt: now,
		},
		messages: []Message{},
	}
}

// UserText create user text message
func UserText(text string) Message {
	return Message{
		Role: RoleUser,
		UUID: generateUUID(),
		Blocks: []ContentBlock{
			{Type: "text", Text: text},
		},
		Timestamp: time.Now().UnixMilli(),
	}
}

// ImageBlock image content block definition
type ImageBlock struct {
	MediaType  string
	Base64Data string
}

// UserWithImages create user message with text and images
// for multimodal input, combine text and images into one message
func UserWithImages(text string, images []ImageBlock) Message {
	blocks := make([]ContentBlock, 0, 1+len(images))
	if text != "" {
		blocks = append(blocks, ContentBlock{Type: "text", Text: text})
	}
	for _, img := range images {
		blocks = append(blocks, ContentBlock{
			Type:       "image",
			MediaType:  img.MediaType,
			Base64Data: img.Base64Data,
		})
	}
	return Message{
		Role:      RoleUser,
		UUID:      generateUUID(),
		Blocks:    blocks,
		Timestamp: time.Now().UnixMilli(),
	}
}

// AssistantText create assistant text message
//
// ★ (b)：入参先过入口清洗（`sanitizeAssistantBlocks`），
// 上游泄漏的孤立推理边界标记不得进入会话文本（见 reasoning_tags.go）。
func AssistantText(text string, model string, usage *TokenUsage) Message {
	blocks := sanitizeAssistantBlocks([]ContentBlock{
		{Type: "text", Text: text},
	})
	return Message{
		Role:      RoleAssistant,
		UUID:      generateUUID(),
		Blocks:    blocks,
		Usage:     usage,
		Model:     model,
		Timestamp: time.Now().UnixMilli(),
	}
}

// AssistantNotice create assistant-side **program-authored** notice message（纯文本）。
//
// 与 AssistantText 的唯一差别：text 块带 `Plain` 标记 ⇒ 渲染层走纯文本通道、
// **不经 Markdown**（否则 `util.Bi` 的双语形态会被软换行折叠 + 行首剥缩进毁掉，
// 见 ContentBlock.Plain 的完整来由）。
//
// 用途：程序自己写、但显示在助手位置的**说明**（如 completeBlockedTurn 的
// 沙箱拒绝 / 权限拒绝解释）。这类文案的字面是**程序契约**（用户要看懂"没执行"），
// 不是模型输出 ⇒ 不该被交给 Markdown 重排。
//
// ★ 判据（不变量）：凡「程序生成、含双语文案、且落成 assistant 消息」的文本，
// 必须走本构造器（而不是 AssistantText + 自带换行）—— 见
// `tui/plain_notice_render_test.go` 的渲染后形态门。
func AssistantNotice(text string, model string) Message {
	blocks := sanitizeAssistantBlocks([]ContentBlock{
		{Type: "text", Text: text, Plain: true},
	})
	return Message{
		Role:      RoleAssistant,
		UUID:      generateUUID(),
		Blocks:    blocks,
		Model:     model,
		Timestamp: time.Now().UnixMilli(),
	}
}

// AssistantWithTools create assistant message with tool calls
//
// ★ (b)：本函数是**全部四个会话落盘点**（`tui/stream.go` 的回合收尾、
// `tui/plain.go` ×2、`tui/bridge.go`）共用的组装点 ⇒ 入口清洗落在这里，
// 四条路一次生效（“：同一事实只声明一次）。见 reasoning_tags.go。
func AssistantWithTools(blocks []ContentBlock, model string, usage *TokenUsage) Message {
	return Message{
		Role:      RoleAssistant,
		UUID:      generateUUID(),
		Blocks:    sanitizeAssistantBlocks(blocks),
		Usage:     usage,
		Model:     model,
		Timestamp: time.Now().UnixMilli(),
	}
}

// WithThinkingBlock 返回在 blocks 头部插入 thinking 块后的新切片。
//
// 为什么把「思考文本 → 内容块」收敛到这一个函数（而不是各调用点手写 append）：
// thinking 块是模型思考过程在**会话记录**里的唯一落点 —— export.{html,markdown,json}、
// session.TokenCount、context.SessionMessagesToAIEntries 早就认得这个块类型，
// 但生产侧**从未写入过**（`internal/tui/format.go` 里那句「完整思考内容永远在会话存档
// `~/.whale/sessions/*.jsonl` 可查」因此一直是假话）。转换点只留一处，调用方就无从
// 各写一份、也无从把顺序写错（族：同一语义多处实现）。
//
// 顺序：thinking 必须排在 text/tool_use **之前** —— 与模型返回内容块的顺序一致，
// 保证存档里的块序与线上协议里的块序同构。
//
// thinking 为空白（或全为空白字符）时**原样返回 blocks**，方便调用点无条件调用。
//
// ★ (b)：入参先过入口清洗 —— 本函数返回的切片正是 `tui/stream.go` 里
// **渲染与落盘共用的那一份**（`persisted`）⇒ 在这里清洗，屏幕与磁盘同源。
func WithThinkingBlock(blocks []ContentBlock, thinking string) []ContentBlock {
	blocks = sanitizeAssistantBlocks(blocks)
	trimmed := strings.TrimSpace(thinking)
	if trimmed == "" {
		return blocks
	}
	out := make([]ContentBlock, 0, len(blocks)+1)
	out = append(out, ContentBlock{Type: ThinkingBlockType, Thinking: trimmed})
	return append(out, blocks...)
}

// ThinkingContent 拼接消息中**全部** thinking 块的内容，与 TextContent 对称。
//
// 调用方（如 `/thinkback`）必须走这个访问器而不是自己遍历 Blocks：
// 遍历写法一旦有人只取第一个块、或把 Type 判断写错，都会静默少读。
func (m Message) ThinkingContent() string {
	var sb strings.Builder
	for _, b := range m.Blocks {
		if b.Type == ThinkingBlockType {
			sb.WriteString(b.Thinking)
		}
	}
	return sb.String()
}

// HasThinking 报告消息是否带有非空思考内容。
func (m Message) HasThinking() bool {
	for _, b := range m.Blocks {
		if b.Type == ThinkingBlockType && strings.TrimSpace(b.Thinking) != "" {
			return true
		}
	}
	return false
}

// WithoutThinkingBlocks 返回剔除全部 thinking 块后的副本（`/thinkback clear` 的落点）。
//
// 只删 thinking 块，text/tool_use/tool_result/image 一律原样保留 —— 清空的是
// 「思考记录」，不是对话本身。返回新切片，不就地修改原消息的 Blocks。
func (m Message) WithoutThinkingBlocks() Message {
	if len(m.Blocks) == 0 {
		return m
	}
	kept := make([]ContentBlock, 0, len(m.Blocks))
	for _, b := range m.Blocks {
		if b.Type == ThinkingBlockType {
			continue
		}
		kept = append(kept, b)
	}
	if len(kept) == len(m.Blocks) {
		return m
	}
	m.Blocks = kept
	return m
}

// EmptyToolOutputPlaceholder 是「工具执行成功、但没有任何输出」时的占位文案。
//
// 为什么必须非空（2026-09-30 · I-8.2 真机手测）：空串会让 block 的 `output` 字段被
// `omitempty` 抹掉，模型收到一个**没有内容**的 tool_result，分不清「成功但无输出」与
// 「压根没跑」⇒ 把同一条命令连发 4 次（现场：`echo hi > out/x.txt` 的 stdout 本就为空，
// 连发 4 次后触发「已连续执行 3 次 / 4 次」两条循环告警）。
const EmptyToolOutputPlaceholder = "(no output)"

// NormalizeToolOutput 是「空输出 ⇒ 占位」的**唯一实现** —— 所有消费方都经它，
// 不要再写第二份 `if TrimSpace == ""`（那正是本仓「同一语义多份实现」的老形态）。
//
// 语义：成功且 trim 后为空 ⇒ EmptyToolOutputPlaceholder；**其余原样返回**。
// 「失败 + 空」保持原样是**有意**的：失败路径的 Output 在生产上都由 fmt.Sprintf 生成，
// 逐处核过（tui 的 pipelineRejectionResult / abortRestOfBatch、executor 的错误出口）
// ⇒ 该形态当前没有生产者，不替它编一句没人验证过的话（R5）；将来真出现再定文案。
//
// 消费方（**新增出口必须也走它**）：
// - session.ToolResult —— 回传给模型的那条 tool_result
// - tools.FormatToolResultStructWithHint —— TUI 转录里的 `📋 <tool>:` 块
// - tui 的 `--plain` 结果打印（plain.go 的 `[Result: …]`）
func NormalizeToolOutput(output string, isError bool) string {
	if !isError && strings.TrimSpace(output) == "" {
		return EmptyToolOutputPlaceholder
	}
	return output
}

// ToolResult create tool result message
func ToolResult(toolUseID, toolName, output string, isError bool) Message {
	output = NormalizeToolOutput(output, isError)
	return Message{
		Role: RoleTool,
		UUID: generateUUID(),
		Blocks: []ContentBlock{
			{
				Type:      "tool_result",
				ToolUseID: toolUseID,
				ToolName:  toolName,
				Output:    output,
				IsError:   isError,
			},
		},
		Timestamp: time.Now().UnixMilli(),
	}
}

// TextContent extract plain text from message
func (m Message) TextContent() string {
	result := ""
	for _, b := range m.Blocks {
		if b.Type == "text" {
			result += b.Text
		}
	}
	return result
}

// ToolUses extract all tool calls from message
func (m Message) ToolUses() []ContentBlock {
	var tools []ContentBlock
	for _, b := range m.Blocks {
		if b.Type == "tool_use" {
			tools = append(tools, b)
		}
	}
	return tools
}

// AddTag add tag
func (m *Message) AddTag(tag string) {
	for _, t := range m.Tags {
		if t == tag {
			return
		}
	}
	m.Tags = append(m.Tags, tag)
}

// RemoveTag remove tag
func (m *Message) RemoveTag(tag string) {
	filtered := m.Tags[:0]
	for _, t := range m.Tags {
		if t != tag {
			filtered = append(filtered, t)
		}
	}
	m.Tags = filtered
}

// IncrementTurn increment turn
func (s *Session) IncrementTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Metadata.TurnCount++
	s.Metadata.UpdatedAt = time.Now().UnixMilli()
	s.revision++
}

// SetCompactionCount updates persisted compaction metadata through the same
// revisioned lock used by transcript mutations. Installing a Rewrite
// candidate may already have set this value, in which case it is a no-op.
func (s *Session) SetCompactionCount(count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Metadata.CompactionCount == count {
		return
	}
	s.Metadata.CompactionCount = count
	s.revision++
}

// CompactionCount returns the persisted compaction count under the session
// read lock.
func (s *Session) CompactionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Metadata.CompactionCount
}

// AddMessage appends a message to the session with write lock protection.
//
// UUID chain maintenance (S6): if msg has no UUID, one is generated. If msg
// has no ParentUUID and the session already has a chain tail (lastUUID), the
// new message's ParentUUID is set to lastUUID, linking it into the chain.
// The chain tail is advanced to the new message's UUID unless the message is
// tombstoned (tombstoned messages are dead branches and do not extend the
// active chain).
func (s *Session) AddMessage(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg.UUID == "" {
		msg.UUID = generateUUID()
	}
	if msg.ParentUUID == "" && s.lastUUID != "" {
		msg.ParentUUID = s.lastUUID
	}
	if !msg.Tombstoned {
		s.lastUUID = msg.UUID
	}
	s.messages = append(s.messages, msg)
	s.revision++
}

// AddMessageWithParent appends a message with an explicit parent UUID. Used
// by fork (the forked copy re-links the chain) and compact (the
// compact_boundary message's parent is the last pre-compact message). The
// caller-supplied parentUUID always wins; if empty the message becomes a new
// chain root (ParentUUID left empty). The chain tail is advanced to the new
// message's UUID unless the message is tombstoned.
func (s *Session) AddMessageWithParent(msg Message, parentUUID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg.UUID == "" {
		msg.UUID = generateUUID()
	}
	msg.ParentUUID = parentUUID
	if !msg.Tombstoned {
		s.lastUUID = msg.UUID
	}
	s.messages = append(s.messages, msg)
	s.revision++
}

// LastUUID returns the UUID of the chain tail (last non-tombstoned message),
// or "" if the session is empty. Mainly used by callers (e.g. Context module
// inserting a compact_boundary) that need to know the current tail before
// appending.
func (s *Session) LastUUID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastUUID
}

// loadMessagesWithChain backfills UUID/ParentUUID for legacy JSONL files that
// predate the UUID chain. Messages without a UUID get a deterministic UUID v5
// (stable across loads, based on message content + line index); messages
// without a ParentUUID are linked to the previous non-tombstoned message's
// UUID. The returned slice is the same backing array as the input (mutated
// in place); the caller is expected to own it.
//
// This is called by Store.Load / LoadWithReport after parsing so the in-memory
// session always has a well-formed chain regardless of file vintage.
func loadMessagesWithChain(rawMsgs []Message) []Message {
	if len(rawMsgs) == 0 {
		return rawMsgs
	}
	var parentUUID string
	for i := range rawMsgs {
		if rawMsgs[i].UUID == "" {
			rawMsgs[i].UUID = deterministicUUID(rawMsgs[i], i)
		}
		if rawMsgs[i].ParentUUID == "" && parentUUID != "" {
			rawMsgs[i].ParentUUID = parentUUID
		}
		if !rawMsgs[i].Tombstoned {
			parentUUID = rawMsgs[i].UUID
		}
	}
	return rawMsgs
}

// GetMessages returns a defensive copy of the session messages.
// This is the preferred read accessor; mutations to the returned slice
// will not affect the session's internal state.
func (s *Session) GetMessages() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs := make([]Message, len(s.messages))
	copy(msgs, s.messages)
	return msgs
}

// Snapshot atomically captures the transcript and its in-process revision.
func (s *Session) Snapshot() ([]Message, SessionMetadata, uint64, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return CloneMessages(s.messages), s.Metadata, s.revision, s.lastUUID
}

// CloneMessages returns a deep copy suitable for speculative transformations.
// In particular, Metadata maps and content slices do not alias the source.
func CloneMessages(src []Message) []Message {
	dst := make([]Message, len(src))
	for i, msg := range src {
		dst[i] = msg
		dst[i].Blocks = append([]ContentBlock(nil), msg.Blocks...)
		dst[i].Tags = append([]string(nil), msg.Tags...)
		dst[i].Metadata = cloneMetadataMap(msg.Metadata)
		if msg.Usage != nil {
			usage := *msg.Usage
			dst[i].Usage = &usage
		}
	}
	return dst
}

func cloneMetadataMap(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}
	dst := make(map[string]interface{}, len(src))
	for key, value := range src {
		dst[key] = cloneMetadataValue(value)
	}
	return dst
}

func cloneMetadataValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneMetadataMap(typed)
	case []interface{}:
		result := make([]interface{}, len(typed))
		for i := range typed {
			result[i] = cloneMetadataValue(typed[i])
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

// SetMessages replaces all messages in the session with write lock protection.
// The chain tail (lastUUID) is re-derived from the last non-tombstoned message
// so subsequent AddMessage calls link correctly.
func (s *Session) SetMessages(msgs []Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = make([]Message, len(msgs))
	copy(s.messages, msgs)
	s.lastUUID = deriveLastUUID(s.messages)
	s.revision++
}

// ReplaceIfRevision installs a prepared transcript only if no in-process
// message mutation occurred since the caller's snapshot.
func (s *Session) ReplaceIfRevision(expected uint64, msgs []Message, metadata SessionMetadata) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revision != expected {
		return false
	}
	s.messages = CloneMessages(msgs)
	s.Metadata = metadata
	s.lastUUID = deriveLastUUID(s.messages)
	s.revision++
	return true
}

// ClearMessages removes all messages from the session with write lock protection
func (s *Session) ClearMessages() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = nil
	s.lastUUID = ""
	s.revision++
}

// TruncateMessages keeps only the last n messages, removing older ones.
// The chain tail is re-derived so the chain remains consistent after
// truncation (the new tail is the last surviving message).
func (s *Session) TruncateMessages(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || len(s.messages) <= n {
		return
	}
	s.messages = s.messages[len(s.messages)-n:]
	s.lastUUID = deriveLastUUID(s.messages)
	s.revision++
}

// NormalizeMessageChain deep-copies messages, assigns a fresh UUID to missing
// or duplicate IDs, and re-links every live message to the previous live
// candidate message. No parent may point into a removed prefix.
func NormalizeMessageChain(messages []Message) []Message {
	result := CloneMessages(messages)
	seen := make(map[string]struct{}, len(result))
	parent := ""
	for i := range result {
		if result[i].UUID == "" {
			result[i].UUID = generateUUID()
		} else if _, duplicate := seen[result[i].UUID]; duplicate {
			result[i].UUID = generateUUID()
		}
		seen[result[i].UUID] = struct{}{}
		result[i].ParentUUID = parent
		if !result[i].Tombstoned {
			parent = result[i].UUID
		}
	}
	return result
}

// deriveLastUUID returns the UUID of the last non-tombstoned message in msgs,
// or "" if the slice is empty or all messages are tombstoned. Caller must hold
// the write lock (or be in a constructor path where no other goroutine has
// access to the session yet).
func deriveLastUUID(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if !msgs[i].Tombstoned {
			return msgs[i].UUID
		}
	}
	return ""
}

// MessagesRaw returns a copy of the internal message slice.
//
// FACT-CONC-001 fix: previously this returned the internal slice directly,
// allowing external mutation of session state. It now returns a defensive
// copy so callers cannot corrupt the internal state.
func (s *Session) MessagesRaw() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs := make([]Message, len(s.messages))
	copy(msgs, s.messages)
	return msgs
}

// TokenCount estimates total token usage across all messages.
// Accounts for text, tool input/output, and image content blocks.
// Text estimation: ~4 chars per token. Image estimation: fixed overhead per image.
//
// This legacy session-level diagnostic uses a conservative 4/3 padding and
// fixed image allowance. /context and compaction decisions use
// ai.TokenEstimator instead; callers must not compare this value with their
// user-visible estimates.
func (s *Session) TokenCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	const charsPerToken = 4
	// Legacy conservative image allowance.
	const imageTokenEstimate = 2000
	total := 0
	for _, msg := range s.messages {
		for _, block := range msg.Blocks {
			switch block.Type {
			case "text":
				total += len(block.Text) / charsPerToken
				if len(block.Text)%charsPerToken > 0 {
					total++
				}
			case ThinkingBlockType:
				total += len(block.Thinking) / charsPerToken
				if len(block.Thinking)%charsPerToken > 0 {
					total++
				}
			case "tool_use":
				total += len(block.Input) / charsPerToken
				if len(block.Input)%charsPerToken > 0 {
					total++
				}
				total += len(block.Name) / charsPerToken
			case "tool_result":
				total += len(block.Output) / charsPerToken
				if len(block.Output)%charsPerToken > 0 {
					total++
				}
			case "image":
				total += imageTokenEstimate
			}
		}
	}
	// Legacy conservative padding.
	return total * 4 / 3
}

// MessageCount returns the number of messages with read lock protection
func (s *Session) MessageCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}

// ExportMarkdown export session to Markdown format.
//
// FACT-CONC-001 fix: now acquires the read lock before iterating over
// messages to prevent data races with concurrent AddMessage calls.
func (s *Session) ExportMarkdown() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("# Whale Code Export\n\n")
	for _, msg := range s.messages {
		switch msg.Role {
		case RoleUser:
			sb.WriteString("## User\n\n")
			sb.WriteString(msg.TextContent())
			sb.WriteString("\n\n")
		case RoleAssistant:
			sb.WriteString("## Assistant\n\n")
			sb.WriteString(msg.TextContent())
			sb.WriteString("\n\n")
		case RoleTool:
			for _, b := range msg.Blocks {
				sb.WriteString("## Tool: ")
				sb.WriteString(b.ToolName)
				sb.WriteString("\n\n```\n")
				sb.WriteString(b.Output)
				sb.WriteString("\n```\n\n")
			}
		}
	}
	return sb.String()
}
