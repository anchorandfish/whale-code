// entry.go — H11/N27 Phase A 核心字段层 MemoryEntry struct + CapabilityValue + 常量。
// 14 字段结构；落地：MemoryEntry 14 frontmatter（N12 5+v0.3 3+v0.4 6）+ 3 内存字段
// 字段；落地：TrustLevel/ProbeFn/TTlSeconds/Verification/ValidFrom/ValidUntil/Links/Importance/Source
// metadata；落地：4 取值 stable|capability|ephemeral|procedural（v0.4 扩展 procedural）
// REF-外部-CoALA@arxiv:2309.02427+2026-07-15访问-procedural 三分类；落地：TrustProcedural 常量
// REF-外部-MemorySurvey@arxiv:2512.13564+2026-07-15访问-verification 三态；落地：verified/unverified/contradicted 常量
// SNAPSHOT: MemoryEntry={N12:5字段 + v0.3:3字段 + v0.4:6字段 + 内存:3字段}; CapabilityValue={Value(interface{}),CheckedAt(time.Time),Trust(string含:stale/:unverified后缀)}

package memory

import "time"

// MemoryType 复用 N12 已有定义（user/feedback/project/reference）。
// H11/N27 不引入 scope 字段（v0.3 放弃，复用 N12 type 做语义主分类；v0.4 用物理目录 _user/ 替代）。
type MemoryType string

// N12 已有 MemoryType 取值常量（保留与 config.MemoryType 对齐）。
const (
	// MemoryTypeUser 用户偏好记忆（user 偏好，可跨项目）。
	MemoryTypeUser MemoryType = "user"
	// MemoryTypeFeedback 用户反馈记忆（用户校正/隐式反馈）。
	MemoryTypeFeedback MemoryType = "feedback"
	// MemoryTypeProject 项目相关记忆（项目特定配置/约定）。
	MemoryTypeProject MemoryType = "project"
	// MemoryTypeReference 参考材料记忆（外部文档/示例）。
	MemoryTypeReference MemoryType = "reference"
)

// MemoryEntry 记忆条目完整结构，含 v0.3 +3 字段 + v0.4 +6 字段。
// 对齐 02 §2.5.2 ：N12 frontmatter 9 新字段（v0.3 +3 + v0.4 +6）+ 3 内存字段（不持久化）。
type MemoryEntry struct {
	// N12 已有 5 字段
	Name        string     `yaml:"name"`        // 记忆条目名（文件名 sanitized 后）
	Description string     `yaml:"description"` // 简短描述
	Type        MemoryType `yaml:"type"`        // user/feedback/project/reference（语义主分类）
	CreatedAt   time.Time  `yaml:"created_at"`  // 创建时间
	UpdatedAt   time.Time  `yaml:"updated_at"`  // 更新时间
	// v0.3 +3 字段
	TrustLevel string `yaml:"trust_level"` // stable|capability|ephemeral|procedural
	ProbeFn    string `yaml:"probe_fn"`    // 仅 capability 必填，ProbeFn key
	TTlSeconds int64  `yaml:"ttl_seconds"` // 仅 capability 必填，默认 86400
	// v0.4 +6 字段
	Verification string     `yaml:"verification"` // verified|unverified|contradicted
	ValidFrom    *time.Time `yaml:"valid_from"`   // bi-temporal 起始（nil=回退到 created_at）
	ValidUntil   *time.Time `yaml:"valid_until"`  // bi-temporal 结束（nil=无过期时间）
	Links        []string   `yaml:"links"`        // A-MEM 关联记忆 Name 列表
	Importance   int        `yaml:"importance"`   // 0-100，0 视为未设置→默认 50
	Source       string     `yaml:"source"`       // probe_fn|user|ai_self|reflection
	// 访问热度字段（S18/G1 修复：持久化到 frontmatter）
	// 修复前为 yaml:"-"，冷加载（新进程首次读盘）后 LastAccessedAt 归零
	// → Ebbinghaus retention≈0 → 有效 ephemeral 记忆在首次后台扫描即被静默
	// 判 contradicted（误杀）。现持久化，使衰减基于「真实持久化访问数据」。
	AccessCount    int       `yaml:"access_count"`     // 访问计数（Ebbinghaus 衰减用）
	LastAccessedAt time.Time `yaml:"last_accessed_at"` // 上次访问时间（Ebbinghaus 衰减用）
	ProbeFailCount int       `yaml:"-"`                // 审查报告优化 2：仅内存，重启归零（连续失败计数，3 次转 contradicted）
	// Content Markdown body（不序列化到 frontmatter，与 N12 MemoryEntryV2.Content 对齐）
	Content string `yaml:"-"`
	// FilePath 运行时填充（不持久化）
	FilePath string `yaml:"-"`
}

// CapabilityValue capability 值（v0.3 结构，v0.4 不改签名，对齐总纲 §4.2）。
// Trust 字段携带 trust_level 快照 + 降级后缀：
// - 正常：stable|capability|ephemeral|procedural
// - ProbeFn 失败降级：后缀 ":stale"（返回旧值）
// - unverified 未校验：后缀 ":unverified"（让 H10 调用方决定 abstention）
type CapabilityValue struct {
	Value     interface{} // 实际能力值
	CheckedAt time.Time   // 上次 ProbeFn 校验时间戳
	Trust     string      // trust_level 快照 + 降级后缀（:stale / :unverified）
}

// TrustLevel 取值常量（v0.4 扩展 +procedural，CoALA @arxiv:2309.02427 三分类）。
const (
	// TrustStable 稳定事实（项目根目录/系统常量，永不过期，不衰减）。
	TrustStable = "stable"
	// TrustCapability 能力探测（gcc/git 可用性，ProbeFn 周期校验，失败转 stale）。
	TrustCapability = "capability"
	// TrustEphemeral 短期记忆（会话状态/临时偏好，走 Ebbinghaus 衰减）。
	TrustEphemeral = "ephemeral"
	// TrustProcedural 过程性记忆（ReflectMemories 合成，待用户 confirm 转 verified）。
	TrustProcedural = "procedural"
)

// Verification 取值常量（Memory Survey @arxiv:2512.13564）。
const (
	// VerificationVerified 已验证（ProbeFn 成功或用户 /memory confirm）。
	VerificationVerified = "verified"
	// VerificationUnverified 未验证（默认值，新写入记忆初始状态）。
	VerificationUnverified = "unverified"
	// VerificationContradicted 已失效（ProbeFn 连续 3 次失败或用户 /memory invalidate，终态不可流转）。
	VerificationContradicted = "contradicted"
)

// Source 取值常量（记忆来源标记）。
const (
	// SourceProbeFn 来源 ProbeFn 探测（capability 类型默认）。
	SourceProbeFn = "probe_fn"
	// SourceUser 来源用户手动写入（/memory add 命令）。
	SourceUser = "user"
	// SourceAISelf 来源 AI 自身写入（CompactContext 压缩）。
	SourceAISelf = "ai_self"
	// SourceReflection 来源反思合成（ReflectMemories）。
	SourceReflection = "reflection"
)

// LinkReasons 链接原因 map（内存字段，不持久化）。
// 通过 MemoryEntry 内存字段维护，记录 Links 中每个目标 name 对应的链接原因。
// 注：当前为概念性字段，实际存储在 linkRegistry 中（见 links.go Phase B）。

// Content 内联方法：返回 entry 的 Markdown body（与 N12 MemoryEntryV2.Content 对齐）。
// 注：直接通过 entry.Content 字段访问，无需方法包装。

// SetContent 设置 entry 的 Markdown body。
func (e *MemoryEntry) SetContent(content string) {
	e.Content = content
}

// ValidFromOrCreatedAt 返回有效起始时间（ValidFrom nil 时回退到 CreatedAt）。
// 对齐 02 §2.5.2 ValidFrom nil 语义。
func (e *MemoryEntry) ValidFromOrCreatedAt() time.Time {
	if e.ValidFrom != nil {
		return *e.ValidFrom
	}
	return e.CreatedAt
}

// DefaultImportance 返回有效重要性（Importance==0 时默认 50）。
// 对齐 02 §2.5.2 Importance 零值默认值规则。
func (e *MemoryEntry) DefaultImportance() int {
	if e.Importance == 0 {
		return 50
	}
	return e.Importance
}

// DefaultVerification 返回有效 verification（空字符串时默认 unverified）。
// 对齐 02 §2.5.2 Verification 默认值规则。
func (e *MemoryEntry) DefaultVerification() string {
	if e.Verification == "" {
		return VerificationUnverified
	}
	return e.Verification
}

// HasLink 检查 entry.Links 是否包含指定 name。
func (e *MemoryEntry) HasLink(name string) bool {
	for _, n := range e.Links {
		if n == name {
			return true
		}
	}
	return false
}

// AddLink 添加 link 到 entry.Links（去重，已存在不重复添加）。
func (e *MemoryEntry) AddLink(name string) {
	if e.HasLink(name) {
		return
	}
	e.Links = append(e.Links, name)
}

// RemoveLink 从 entry.Links 移除指定 name（不存在则无操作）。
func (e *MemoryEntry) RemoveLink(name string) {
	for i, n := range e.Links {
		if n == name {
			e.Links = append(e.Links[:i], e.Links[i+1:]...)
			return
		}
	}
}
