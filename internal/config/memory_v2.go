// Package config — Smart Memory Retrieval System (Phase 3)
// Provides YAML frontmatter + Markdown based memory entry storage, AI-driven relevance selection,
// automatic preference extraction and memory prefetching.
package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/util"
	"gopkg.in/yaml.v3"
)

// ============================================================
// Memory type constants
// ============================================================

// MemoryType represents the type of memory entry
type MemoryType string

const (
	// MemoryTypeUser user preference memory
	MemoryTypeUser MemoryType = "user"
	// MemoryTypeFeedback user feedback memory
	MemoryTypeFeedback MemoryType = "feedback"
	// MemoryTypeProject project-related memory
	MemoryTypeProject MemoryType = "project"
	// MemoryTypeReference reference material memory
	MemoryTypeReference MemoryType = "reference"
)

// ============================================================
// MemoryEntry — upgraded memory entry (YAML frontmatter + Markdown body)
// ============================================================

// MemoryEntryV2 represents an upgraded memory entry with YAML frontmatter metadata and Markdown body
// v0.4 扩展：接受 H11/N27 新增 9 个 frontmatter 字段 + 3 个内存字段（不持久化）。
// 字段结构：
// - N12 已有 5 字段：Name/Description/Type/CreatedAt/UpdatedAt
// - v0.3 +3 字段：TrustLevel/ProbeFn/TTlSeconds
// - v0.4 +6 字段：Verification/ValidFrom/ValidUntil/Links/Importance/Source
// - 内存字段 3 个（不持久化 yaml:"-"）：AccessCount/LastAccessedAt/ProbeFailCount（审查报告优化 2：ProbeFailCount 仅内存重启归零）
type MemoryEntryV2 struct {
	Name        string     `yaml:"name"`        // Memory entry name
	Description string     `yaml:"description"` // Brief description
	Type        MemoryType `yaml:"type"`        // Memory type: user/feedback/project/reference
	CreatedAt   time.Time  `yaml:"created_at"`  // Creation time
	UpdatedAt   time.Time  `yaml:"updated_at"`  // Update time
	// v0.3 +3 字段
	TrustLevel string `yaml:"trust_level"` // stable|capability|ephemeral|procedural
	ProbeFn    string `yaml:"probe_fn"`    // 仅 capability 必填，ProbeFn key
	TTlSeconds int64  `yaml:"ttl_seconds"` // 仅 capability 必填，默认 86400
	// v0.4 +6 字段
	Verification string     `yaml:"verification"` // verified|unverified|contradicted
	ValidFrom    *time.Time `yaml:"valid_from"`   // bi-temporal 起始（nil=回退到 created_at）
	ValidUntil   *time.Time `yaml:"valid_until"`  // bi-temporal 结束（nil=无过期时间）
	Links        []string   `yaml:"links"`        // A-MEM 关联记忆 Name 列表
	Importance   int        `yaml:"importance"`   // 0-100，0 视为未设置→默认 50（仅 Parse 时默认）
	Source       string     `yaml:"source"`       // probe_fn|user|ai_self|reflection
	// 内存字段（不持久化 yaml:"-"）
	AccessCount    int       `yaml:"-"` // 访问计数（Ebbinghaus 衰减用）
	LastAccessedAt time.Time `yaml:"-"` // 上次访问时间（Ebbinghaus 衰减用）
	ProbeFailCount int       `yaml:"-"` // P0-3: yaml 不持久化，但旁路记账到 probe_fail.jsonl 跨会话恢复（连续失败计数，3 次转 contradicted）
	Content        string    `yaml:"-"` // Markdown body (not serialized to frontmatter)
	FilePath       string    `yaml:"-"` // File path (filled at runtime, not serialized)
}

// memoryFrontMatter is the intermediate structure for YAML frontmatter serialization/deserialization
// v0.4 扩展：N12 接受 H11/N27 新增 9 个 frontmatter 字段（v0.3 +3 + v0.4 +6）。
type memoryFrontMatter struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Type        MemoryType `yaml:"type"`
	CreatedAt   time.Time  `yaml:"created_at"`
	UpdatedAt   time.Time  `yaml:"updated_at"`
	// v0.3 +3 字段
	TrustLevel string `yaml:"trust_level"`
	ProbeFn    string `yaml:"probe_fn"`
	TTlSeconds int64  `yaml:"ttl_seconds"`
	// v0.4 +6 字段
	Verification string     `yaml:"verification"`
	ValidFrom    *time.Time `yaml:"valid_from"`
	ValidUntil   *time.Time `yaml:"valid_until"`
	Links        []string   `yaml:"links"`
	Importance   int        `yaml:"importance"`
	Source       string     `yaml:"source"`
}

// ============================================================
// ParseMemoryEntry — parses YAML frontmatter + Markdown body
// ============================================================

// ParseMemoryEntry parses YAML frontmatter and Markdown body from raw string content
// Input format example:
//
//	---
//	name: my-preference
//	description: User preference
//	type: user
//	created_at: 2025-01-01T00:00:00Z
//	updated_at: 2025-01-01T00:00:00Z
//	---
//	Markdown body content...
//
// v0.4 扩展：yaml.Unmarshal 后补 3 项零值默认值处理（零值默认值）：
// - Importance==0 视为未设置 → 默认 50（中间值，避免极端偏向）
// - ValidFrom==nil 回退到 &CreatedAt（bi-temporal 起始语义对齐）
// - Verification=="" 默认 "unverified"（NEVER 默认 verified，防 PIT-001 capability 失效被当真）
func ParseMemoryEntry(content string) (*MemoryEntryV2, error) {
	// Check frontmatter boundary
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return nil, ai.WithID(fmt.Errorf("memory entry missing YAML frontmatter (---)"), ai.ErrMemoryParse)
	}

	// Find frontmatter closing marker
	endIdx := strings.Index(content[3:], "---")
	if endIdx < 0 {
		return nil, ai.WithID(fmt.Errorf("memory entry YAML frontmatter not properly closed"), ai.ErrMemoryParse)
	}

	fmContent := content[3 : endIdx+3]
	bodyContent := content[endIdx+6:] // Skip second ---

	// Parse YAML frontmatter
	var fm memoryFrontMatter
	if err := yaml.Unmarshal([]byte(fmContent), &fm); err != nil {
		return nil, ai.WithID(fmt.Errorf("failed to parse YAML frontmatter: %w", err), ai.ErrMemoryParse)
	}

	// Validate type
	if fm.Type == "" {
		fm.Type = MemoryTypeUser
	}
	if !isValidMemoryType(fm.Type) {
		return nil, ai.WithID(fmt.Errorf("invalid memory type: %s", fm.Type), ai.ErrMemoryParse)
	}

	// v0.4 零值默认值处理（3 项，）
	// 1. Importance==0 视为未设置 → 默认 50
	if fm.Importance == 0 {
		fm.Importance = 50
	}
	// 2. ValidFrom==nil 回退到 &CreatedAt（bi-temporal 起始语义）
	// 注意：必须取 fm.CreatedAt 的地址，不能用局部变量地址（否则 CreatedAt 后续变更不影响 ValidFrom）
	if fm.ValidFrom == nil {
		createdAt := fm.CreatedAt
		fm.ValidFrom = &createdAt
	}
	// 3. Verification=="" 默认 "unverified"
	// NEVER 默认 "verified"，防止 PIT-001 capability 失效被当真
	// 注：此处使用字面量 "unverified"，与 internal/memory 包的 VerificationUnverified 常量同值；
	// config 包不能导入 internal/memory（防止循环依赖），常量重复可接受（桥接层降级）
	if fm.Verification == "" {
		fm.Verification = "unverified"
	}

	return &MemoryEntryV2{
		Name:        fm.Name,
		Description: fm.Description,
		Type:        fm.Type,
		CreatedAt:   fm.CreatedAt,
		UpdatedAt:   fm.UpdatedAt,
		// v0.3 +3 字段
		TrustLevel: fm.TrustLevel,
		ProbeFn:    fm.ProbeFn,
		TTlSeconds: fm.TTlSeconds,
		// v0.4 +6 字段
		Verification: fm.Verification,
		ValidFrom:    fm.ValidFrom,
		ValidUntil:   fm.ValidUntil,
		Links:        fm.Links,
		Importance:   fm.Importance,
		Source:       fm.Source,
		// Content / FilePath 由调用方或 ReadMemoryEntry 填充
		Content: strings.TrimSpace(bodyContent),
	}, nil
}

// isValidMemoryType checks if the given memory type is valid
func isValidMemoryType(t MemoryType) bool {
	switch t {
	case MemoryTypeUser, MemoryTypeFeedback, MemoryTypeProject, MemoryTypeReference:
		return true
	}
	return false
}

// ============================================================
// WriteMemoryEntry — writes memory entry to file (with frontmatter)
// ============================================================

// serializeMemoryEntry 序列化 MemoryEntryV2 为 YAML frontmatter + Markdown body 字节。
// 被 WriteMemoryEntry 和 MemoryStore.Write 共用，避免重复代码。
// v0.4 扩展：与 ParseMemoryEntry 对称，序列化 9 新字段（v0.3 +3 + v0.4 +6）。
// 内存字段（AccessCount/LastAccessedAt/ProbeFailCount/Content/FilePath）不序列化（yaml:"-"）。
func serializeMemoryEntry(entry *MemoryEntryV2) ([]byte, error) {
	// Serialize frontmatter（含 v0.3 +3 与 v0.4 +6 新字段，与 ParseMemoryEntry 对称）
	fm := memoryFrontMatter{
		Name:        entry.Name,
		Description: entry.Description,
		Type:        entry.Type,
		CreatedAt:   entry.CreatedAt,
		UpdatedAt:   entry.UpdatedAt,
		// v0.3 +3 字段
		TrustLevel: entry.TrustLevel,
		ProbeFn:    entry.ProbeFn,
		TTlSeconds: entry.TTlSeconds,
		// v0.4 +6 字段
		Verification: entry.Verification,
		ValidFrom:    entry.ValidFrom,
		ValidUntil:   entry.ValidUntil,
		Links:        entry.Links,
		Importance:   entry.Importance,
		Source:       entry.Source,
	}

	fmBytes, err := yaml.Marshal(&fm)
	if err != nil {
		return nil, ai.WithID(fmt.Errorf("failed to serialize frontmatter: %w", err), ai.ErrMemoryParse)
	}

	// Assemble complete file content
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(fmBytes)
	sb.WriteString("---\n")
	if entry.Content != "" {
		sb.WriteString(entry.Content)
		sb.WriteString("\n")
	}
	return []byte(sb.String()), nil
}

// WriteMemoryEntry writes MemoryEntry in YAML frontmatter + Markdown format to specified path.
// 权限遵循 H5 ：文件 0600，目录 0700。
func WriteMemoryEntry(path string, entry *MemoryEntryV2) error {
	// Ensure directory exists (0700 per H5 )
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ai.WithID(fmt.Errorf("failed to create directory: %w", err), ai.ErrMemoryStore)
	}

	// Update timestamp
	now := time.Now()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now

	data, err := serializeMemoryEntry(entry)
	if err != nil {
		return err
	}

	if err := util.AtomicWriteFile(path, data, 0600); err != nil {
		return ai.WithID(fmt.Errorf("failed to write memory file: %w", err), ai.ErrMemoryStore)
	}

	entry.FilePath = path
	return nil
}

// ============================================================
// ReadMemoryEntry — reads and parses memory entry
// ============================================================

// ReadMemoryEntry reads file from specified path and parses as MemoryEntry
func ReadMemoryEntry(path string) (*MemoryEntryV2, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ai.WithID(fmt.Errorf("failed to read memory file: %w", err), ai.ErrMemoryStore)
	}

	entry, err := ParseMemoryEntry(string(data))
	if err != nil {
		return nil, err
	}

	entry.FilePath = path
	return entry, nil
}

// ============================================================
// ScanMemoryDirectory — scans .md memory files in directory
// ============================================================

// ScanMemoryDirectory scans specified directory and subdirectories, returns path list of all .md memory files
func ScanMemoryDirectory(dir string) ([]string, error) {
	var files []string

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip inaccessible files
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			files = append(files, path)
		}
		return nil
	})

	if err != nil {
		return nil, ai.WithID(fmt.Errorf("failed to scan memory directory: %w", err), ai.ErrMemoryStore)
	}

	// Sort by filename for stable results
	sort.Strings(files)
	return files, nil
}

// ============================================================
// MemoryManifestEntry — memory manifest entry
// ============================================================

// MemoryManifestEntry represents an entry in the memory manifest, used as summary info for AI selection
type MemoryManifestEntry struct {
	Filename    string     `json:"filename"`    // Filename
	Name        string     `json:"name"`        // Memory name
	Description string     `json:"description"` // Brief description
	Type        MemoryType `json:"type"`        // Memory type
	Modified    time.Time  `json:"modified"`    // Last modified time
	SizeChars   int        `json:"size_chars"`  // Content character count
}

// ============================================================
// GenerateMemoryManifest — generates memory manifest
// ============================================================

// GenerateMemoryManifest scans .md memory files in directory, generates manifest entry list
func GenerateMemoryManifest(dir string) ([]MemoryManifestEntry, error) {
	files, err := ScanMemoryDirectory(dir)
	if err != nil {
		return nil, err
	}

	var manifest []MemoryManifestEntry
	for _, f := range files {
		entry, err := ReadMemoryEntry(f)
		if err != nil {
			continue // Skip files that cannot be parsed
		}

		info, err := os.Stat(f)
		if err != nil {
			continue
		}

		manifest = append(manifest, MemoryManifestEntry{
			Filename:    filepath.Base(f),
			Name:        entry.Name,
			Description: entry.Description,
			Type:        entry.Type,
			Modified:    info.ModTime(),
			SizeChars:   len(entry.Content),
		})
	}

	// Sort by modification time descending (newest first)
	sort.Slice(manifest, func(i, j int) bool {
		return manifest[i].Modified.After(manifest[j].Modified)
	})

	return manifest, nil
}

// memoryDriftCaveat 记忆漂移警告（对标同类 CLI memoryTypes.ts MEMORY_DRIFT_CAVEAT）。
// 注入 system prompt，提醒 AI 记忆可能过时，决策前需验证。
const memoryDriftCaveat = "\n\n# Memory Caveat\n以下记忆可能已过时。在基于记忆做出决策前，请先通过代码/文件验证记忆的准确性。\n不要在未验证的情况下直接推荐记忆中的库版本、API 用法或配置路径。"

// ============================================================
// MemoryFreshnessText — memory freshness warning
// ============================================================

// MemoryFreshnessText generates freshness warning text based on last modified time
// Memories beyond a certain age include reminders to help AI determine if info may be outdated
func MemoryFreshnessText(mtime time.Time) string {
	age := time.Since(mtime)
	switch {
	case age < 24*time.Hour:
		return "" // 近期记忆无需警告
	case age < 7*24*time.Hour:
		days := int(age.Hours() / 24)
		return fmt.Sprintf("[Note: this memory is ~%d days old, some details may have changed]", days)
	case age < 30*24*time.Hour:
		weeks := int(age.Hours() / (24 * 7))
		return fmt.Sprintf("[Warning: this memory is ~%d weeks old, may be outdated]", weeks)
	default:
		months := int(age.Hours() / (24 * 30))
		return fmt.Sprintf("[Caution: this memory is ~%d months old, likely outdated — verify before use]", months)
	}
}

// ============================================================
// MemorySelector — AI-driven memory selector
// ============================================================

// MemorySelector uses AI client to select memory entries most relevant to user query from manifest
type MemorySelector struct {
	store           MemorySource // 记忆读路径数据源（P2：接口化，可注入 memory 双 scope 适配器）
	index           *MemoryIndex // MEMORY.md 入口索引（always loaded）
	aiClient        ai.Client    // AI client
	maxSelections   int          // Max selection count (default 5)
	maxMemoryChars  int          // Max characters per memory (default 4096)
	maxSessionChars int          // Total memory character limit per session (default 60KB)
	sessionUsed     atomic.Int64 // Memory characters used in current session (并发安全，FACT-CONC-001)
	coordinator     *ai.RecoveryCoordinator
	retryConfig     ai.RetryConfig
	fallbackTracker *ai.FallbackTracker
	usageTracker    *ai.UsageTracker // D4: 记忆 AI 选择调用的 cost 记录（nil 时降级跳过）

	// ── ：Select 结果缓存 ──────────────────────────────
	//
	// 动机：SelectRelevant 在 manifest > maxSelections 时会发起**真实 LLM 调用**
	// 并同步等到流结束，而调用方 buildRequest 的入参是会话内恒定的 FirstUserInput
	// ⇒ 同一会话每一轮（含每个工具轮次续轮）都重复发起完全相同的调用，
	// 全程同步阻塞主循环 ⇒ 界面冻结。
	//
	// 键 = (userInput, entrypoint, manifest 指纹)；任一变化即失效。
	selectMu    sync.Mutex
	selectKey   string // 空串 = 尚无缓存
	selectValue string

	// ── ：冷启动不等的后台预热 ──────────────────────
	//
	// warmKey = **正在后台预热**的那个缓存键（空串 = 无预热在途）。
	// 作用：同一 key 只允许一个预热 goroutine 在途 —— 否则每一轮 Select
	// 都会再起一个，把"省掉一次调用"放大成"N 次调用"。
	// 预热失败时清空，让后续 Select 有机会重试。
	warmKey string
}

// NewMemorySelector creates memory selector instance.
// store 提供 manifest 构建（List）与完整记忆读取（Read），index 提供 MEMORY.md always loaded 入口。
//
// P2：store 参数类型由 *MemoryStore 放宽为 MemorySource 接口 —— 使读路径可
// 注入 internal/memory 的双 scope 适配器（见 memory_source.go 的依赖倒置说明）。
// 向后兼容：*MemoryStore 隐式满足接口，现有调用方无需改动。
func NewMemorySelector(store MemorySource, index *MemoryIndex, client ai.Client) *MemorySelector {
	return &MemorySelector{
		store:           store,
		index:           index,
		aiClient:        client,
		maxSelections:   5,
		maxMemoryChars:  4096,
		maxSessionChars: 60 * 1024,
		retryConfig:     ai.DefaultRetryConfig(),
	}
}

// buildManifest 从 store.List() 构建 MemoryManifestEntry 列表（替代 GenerateMemoryManifest 的目录扫描）。
// 复用 store 的排序（UpdatedAt 降序）与文件解析。
func (ms *MemorySelector) buildManifest() []MemoryManifestEntry {
	if ms.store == nil {
		return nil
	}
	list, err := ms.store.List()
	if err != nil {
		return nil
	}
	manifest := make([]MemoryManifestEntry, 0, len(list))
	for _, e := range list {
		manifest = append(manifest, MemoryManifestEntry{
			Filename:    e.Name + ".md",
			Name:        e.Name,
			Description: e.Description,
			Type:        e.Type,
			Modified:    e.UpdatedAt,
			SizeChars:   len(e.Content),
		})
	}
	return manifest
}

// Select 是 SelectRelevant 的包装方法，内部构建 manifest + 加载 MEMORY.md entrypoint + 注入 drift caveat。
// 返回格式化后的记忆文本，可直接注入 system prompt（对标 app.go 旧 V2 逻辑 + MEMORY.md always loaded）。
// 简化 app.go 调用方，不删除/不重命名 SelectRelevant（向后兼容）。
//
// ★ ：**冷启动不在请求路径上等 AI** —— 见步骤 4/5 与 startWarmUp。
func (ms *MemorySelector) Select(ctx context.Context, userInput string) (string, error) {
	// 1. 加载 MEMORY.md entrypoint（always loaded 层）
	var entrypoint string
	if ms.index != nil {
		if ep, err := ms.index.LoadEntrypoint(); err == nil && ep != "" {
			entrypoint = ep
		}
	}

	// 2. 构建 manifest
	manifest := ms.buildManifest()

	// 3. 会话级缓存 —— **那批修复的「卡半天」根治点**。
	//
	// 键 = (userInput, entrypoint, maxSelections, manifest 指纹)，任一变化即失效。
	// 命中即原样返回，**不再发起同步 LLM 调用**。
	// 修复前：调用方每轮、以及每个工具轮次续轮都以同一 FirstUserInput 调本函数，
	// 每次都重新走 SelectRelevant 的同步 AI 流（数秒）⇒ 主循环被阻塞 ⇒ 界面冻结。
	key := selectCacheKey(userInput, entrypoint, ms.maxSelections, manifest)

	// ── P0观测：**不改行为**，只在每个出口追加一行事实 ──────────
	// traceHead 承载与分支无关的公共字段（key/entrypoint/manifest），各分支补自己的。
	traceHead := MemorySelectEvent{
		Key:             traceKey(key),
		EntrypointChars: len(entrypoint),
		EntrypointOK:    entrypoint != "",
		ManifestN:       len(manifest),
		Manifest:        traceManifest(manifest),
	}

	if cached, ok := ms.cacheGet(key); ok {
		ev := traceHead
		ev.Branch = traceBranchCacheHit
		ev.OutputChars = len(cached)
		ms.observe(ev)
		return cached, nil
	}

	// 4. 不需要 AI 的情形（条目数不过上限）：SelectRelevant 会原样返回全部，
	// 本地即可算完 —— 同步给出，不引入任何异步面。
	if len(manifest) <= ms.maxSelections {
		result := ms.formatMemoryOutput(entrypoint, manifest)
		ms.cachePut(key, result)
		ev := traceHead
		ev.Branch = traceBranchSyncAll
		ev.Selected = traceNames(manifest)
		ev.SelectedN = len(manifest)
		ev.OutputChars = len(result)
		ms.observe(ev)
		return result, nil
	}

	// 5. ★ ：确实需要 AI 时**不等** —— 先给本地兜底，再后台预热。
	//
	// 语义变化：记忆选择从「首轮就精确」变成「**首轮兜底、次轮起精确**」。
	// 代价有二（如实记录，勿当免费）：
	// ① 首轮注入的记忆可能不是最相关的（兜底只按「最近更新」取，不判相关性）；
	// ② 首轮与次轮的 system prompt 记忆段会**变一次** ⇒ provider 侧前缀缓存
	// 可能失效一次。换来的是首 token 不再等那一次 LLM 往返。
	//
	// ★ P3-wire：① 已修 —— 兜底改成**按用户这句话做本地排序**
	// （数据源实现 MemoryRanker 时）。以前 manifest > maxSelections 的会话，首轮注入的
	// 5 条与用户问什么**完全无关**（只按更新时间），这是"明明记过却想不起来"的直接来源。
	// 代价 ② 不变（首轮与次轮的记忆段仍会变一次）。
	ms.startWarmUp(ctx, key, userInput, entrypoint, manifest)
	fallback, rank := ms.fallbackSelection(ctx, userInput, manifest)
	result := ms.formatMemoryOutput(entrypoint, fallback)
	ev := traceHead
	ev.Branch = traceBranchFallback
	ev.RankMode = rank.mode
	ev.RankHits = rank.hits
	ev.Selected = traceNames(fallback)
	ev.SelectedN = len(fallback)
	ev.OutputChars = len(result)
	ev.WarmupStarted = true
	ms.observe(ev)
	return result, nil
}

// 兜底排序来源（落盘字段 RankMode 的取值，供离线统计与真机判别）。
const (
	rankModeUnderLimit  = "under_limit"  // manifest ≤ maxSelections：全给，不需要排序
	rankModeEmptyQuery  = "empty_query"  // 用户这句话为空：无从排序
	rankModeRecencyOnly = "recency_only" // 数据源未实现 MemoryRanker（旧 store / 降级装配）
	rankModeNoHint      = "no_hint"      // 打分器零命中或报错 ⇒ 退回最近更新
	rankModeRank        = "rank"         // 本地打分器参与了选择
)

// memoryRankInfo 记录本轮兜底用了哪种排序（P0 观测字段）。
type memoryRankInfo struct {
	mode string // 见 rankMode* 常量
	hits int    // 最终注入里来自本地排序的条数
}

// fallbackSelection 返回 的**本地兜底选择**（P3-wire 起查询相关）。
//
// 语义：
// 1. manifest ≤ maxSelections ⇒ 全给（沿用旧行为；此时"少而全"比"排序"更省心）。
// 2. 数据源实现 MemoryRanker ⇒ 先按**用户这句话**取相关度最高的若干条放前面，
// 不足 maxSelections 的位子按 manifest 原序（UpdatedAt 降序）补齐。
// 3. 数据源不支持、用户这句为空、或打分器零命中/报错 ⇒ 退回"最近更新的前 N 条"
// （= 接线前的行为，一个字都没改）。
//
// ★ 为什么"不足就补"，而不是"只给命中的"：
// - 打分器是**词法**的（trigram），用户换一种说法时它会零命中 —— 实测「改写类」
// 召回率 0.000（见 internal/memory/p3_recall_bench_test.go）。若那时只给命中的，
// 首轮注入会**变空**，直接违背 的"首轮有东西可用、不必等"。
// - 补齐用的是"最近更新"，正是接线前的行为 ⇒ **接线不可能让首轮条数变少**，
// 最多把不相关的几条换成相关的几条。这条性质让接线成为"单调改进"。
func (ms *MemorySelector) fallbackSelection(ctx context.Context, userInput string, manifest []MemoryManifestEntry) ([]MemoryManifestEntry, memoryRankInfo) {
	if len(manifest) <= ms.maxSelections {
		return manifest, memoryRankInfo{mode: rankModeUnderLimit}
	}
	if strings.TrimSpace(userInput) == "" {
		return manifest[:ms.maxSelections], memoryRankInfo{mode: rankModeEmptyQuery}
	}

	ranker, ok := ms.store.(MemoryRanker)
	if !ok {
		return manifest[:ms.maxSelections], memoryRankInfo{mode: rankModeRecencyOnly}
	}
	names, err := ranker.Rank(ctx, userInput, ms.maxSelections)
	if err != nil || len(names) == 0 {
		return manifest[:ms.maxSelections], memoryRankInfo{mode: rankModeNoHint}
	}

	byName := make(map[string]MemoryManifestEntry, len(manifest))
	for _, e := range manifest {
		byName[e.Name] = e
	}
	out := make([]MemoryManifestEntry, 0, ms.maxSelections)
	used := make(map[string]bool, ms.maxSelections)
	for _, n := range names {
		e, inManifest := byName[n]
		if !inManifest || used[n] {
			continue // 打分器给的名字不在 manifest 里（竞态/已作废）⇒ 跳过，不报错
		}
		out = append(out, e)
		used[n] = true
		if len(out) >= ms.maxSelections {
			break
		}
	}
	hits := len(out)
	for _, e := range manifest { // 补齐：manifest 已按 UpdatedAt 降序
		if len(out) >= ms.maxSelections {
			break
		}
		if used[e.Name] {
			continue
		}
		out = append(out, e)
		used[e.Name] = true
	}
	return out, memoryRankInfo{mode: rankModeRank, hits: hits}
}

// memoryWarmUpTimeout 是后台预热的超时上界。
//
// 取值理由：真机实测一次真实 LLM 往返约 17s（首测 `buildRequest.total 17.43s`），
// 60s 留足余量；超时即放弃预热（本会话退回本地兜底），不让 goroutine 无限期挂着。
const memoryWarmUpTimeout = 60 * time.Second

// startWarmUp 为 key **幂等地**起一个后台 goroutine 跑真正的 AI 选择，并把结果写回缓存。
//
// 幂等性：同一 key 只允许一个预热在途（warmKey 守卫）—— 调用方在预热完成前
// 会反复走本函数（每轮 Select 都未命中），没有守卫就会每轮各起一个 goroutine。
//
// 预热服务的是「后面的轮次」，不属于任何一次请求，因此**自带超时、且不继承调用方的取消**：
// 若继承，用户按 Esc 结束当前请求会连带把预热杀掉，而这件事跟后面的轮次无关。
func (ms *MemorySelector) startWarmUp(ctx context.Context, key, userInput, entrypoint string, manifest []MemoryManifestEntry) {
	ms.selectMu.Lock()
	if ms.warmKey == key {
		ms.selectMu.Unlock()
		return
	}
	ms.warmKey = key
	ms.selectMu.Unlock()

	go func() {
		warmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), memoryWarmUpTimeout)
		defer cancel()

		start := time.Now()
		selected, err := ms.SelectRelevant(warmCtx, userInput, manifest, nil)
		elapsedMS := time.Since(start).Milliseconds()
		if err != nil {
			// 预热失败 ⇒ 撤掉守卫，让后续 Select 有机会重试；否则这条会话会
			// 永远停在兜底上，且再也投不起预热。
			ms.clearWarmKey(key)
			ms.observe(MemorySelectEvent{
				Key:             traceKey(key),
				Branch:          traceBranchWarmBackup,
				ManifestN:       len(manifest),
				WarmupOK:        false,
				WarmupElapsedMS: elapsedMS,
			})
			return
		}
		out := ms.formatMemoryOutput(entrypoint, selected)
		ms.cachePut(key, out)
		ms.observe(MemorySelectEvent{
			Key:             traceKey(key),
			Branch:          traceBranchWarmBackup,
			ManifestN:       len(manifest),
			Selected:        traceNames(selected),
			SelectedN:       len(selected),
			OutputChars:     len(out),
			WarmupOK:        true,
			WarmupElapsedMS: elapsedMS,
		})
	}()
}

// clearWarmKey 撤销预热守卫（仅当仍是同一个 key —— 避免误撤是后来者设的守卫）。
func (ms *MemorySelector) clearWarmKey(key string) {
	ms.selectMu.Lock()
	if ms.warmKey == key {
		ms.warmKey = ""
	}
	ms.selectMu.Unlock()
}

// formatMemoryOutput 把 entrypoint + 选中的记忆条目渲染成注入 system prompt 的文本。
// 三条路径（同步全量 / AI 选中 / 本地兜底）共用同一渲染，避免措辞各自漂移。
func (ms *MemorySelector) formatMemoryOutput(entrypoint string, selected []MemoryManifestEntry) string {
	var sb strings.Builder
	if entrypoint != "" {
		sb.WriteString(entrypoint)
		sb.WriteString("\n")
	}

	if len(selected) > 0 {
		sb.WriteString("=== User Preference Memory ===\n")
		for _, entry := range selected {
			freshness := MemoryFreshnessText(entry.Modified)
			// 读取完整记忆内容
			if ms.store != nil {
				if fullEntry, err := ms.store.Read(entry.Filename); err == nil {
					sb.WriteString(fmt.Sprintf("- [%s] %s %s\n", entry.Type, fullEntry.Content, freshness))
					continue
				}
			}
			// 读取失败，用 manifest 描述兜底
			sb.WriteString(fmt.Sprintf("- [%s] %s: %s %s\n", entry.Type, entry.Name, entry.Description, freshness))
		}
	}

	// 追回 drift caveat
	sb.WriteString(memoryDriftCaveat)
	return sb.String()
}

// cacheGet 在锁保护下读取 Select 结果缓存；ok=false 表示未命中。
// 「无缓存」用 selectKey == "" 表示，而 selectCacheKey 永不返回空串，故无歧义。
func (ms *MemorySelector) cacheGet(key string) (string, bool) {
	ms.selectMu.Lock()
	defer ms.selectMu.Unlock()
	if ms.selectKey != "" && ms.selectKey == key {
		return ms.selectValue, true
	}
	return "", false
}

// cachePut 在锁保护下写入 Select 结果缓存。
func (ms *MemorySelector) cachePut(key, value string) {
	ms.selectMu.Lock()
	defer ms.selectMu.Unlock()
	ms.selectKey = key
	ms.selectValue = value
}

// selectCacheKey 派生 Select 结果缓存的键（sha256 十六进制串）。
//
// 组成：userInput + entrypoint + maxSelections + manifest 指纹。
// 用**长度前缀**串接各字段，避免拼接歧义（"a"+"bc" 与 "ab"+"c" 撞键）。
//
// manifest 指纹取每个条目的「文件名/名称/类型/更新时间/字符数/描述长度」——
// 记忆的增、删、改（含同名重写导致的 UpdatedAt 变化）都会改变指纹，
// 从而让缓存**自动失效**：Select 无需反向感知 store.Write（它本来也感知不到）。
func selectCacheKey(userInput, entrypoint string, maxSelections int, manifest []MemoryManifestEntry) string {
	h := sha256.New()
	fmt.Fprintf(h, "u=%d:%s\n", len(userInput), userInput)
	fmt.Fprintf(h, "e=%d:%s\n", len(entrypoint), entrypoint)
	fmt.Fprintf(h, "k=%d\n", maxSelections)
	fmt.Fprintf(h, "n=%d\n", len(manifest))
	for _, m := range manifest {
		fmt.Fprintf(h, "m=%d:%s|%d:%s|%d:%s|%s|%d|%d\n",
			len(m.Filename), m.Filename,
			len(m.Name), m.Name,
			len(string(m.Type)), string(m.Type),
			m.Modified.UTC().Format(time.RFC3339Nano),
			m.SizeChars, len(m.Description))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SetRecoveryConfig sets the recovery coordinator, retry config, and fallback tracker
func (ms *MemorySelector) SetRecoveryConfig(coordinator *ai.RecoveryCoordinator, retryConfig ai.RetryConfig, fallbackTracker *ai.FallbackTracker) {
	ms.coordinator = coordinator
	ms.retryConfig = retryConfig
	ms.fallbackTracker = fallbackTracker
}

// SetUsageTracker 注入 UsageTracker，供 SelectRelevant 记录 AI 选择调用的 token cost（D4）。
// nil 时降级跳过 cost 记录，不影响选择功能。
func (ms *MemorySelector) SetUsageTracker(tracker *ai.UsageTracker) {
	ms.usageTracker = tracker
}

// SetMaxSelections sets max selection count
func (ms *MemorySelector) SetMaxSelections(n int) {
	if n > 0 {
		ms.maxSelections = n
	}
}

// SetMaxMemoryChars sets max characters per memory
func (ms *MemorySelector) SetMaxMemoryChars(n int) {
	if n > 0 {
		ms.maxMemoryChars = n
	}
}

// SetMaxSessionChars sets total memory character limit per session
func (ms *MemorySelector) SetMaxSessionChars(n int) {
	if n > 0 {
		ms.maxSessionChars = n
	}
}

// buildSelectionPrompt builds prompt for AI to select memory entries
func (ms *MemorySelector) buildSelectionPrompt(userQuery string, manifest []MemoryManifestEntry, recentTools []string) string {
	var sb strings.Builder

	sb.WriteString("You are a memory retrieval assistant. Select the most relevant entries from the following memory manifest based on the user query.\n\n")

	sb.WriteString(fmt.Sprintf("User query: %s\n\n", userQuery))

	if len(recentTools) > 0 {
		sb.WriteString(fmt.Sprintf("Recently used tools: %s\n\n", strings.Join(recentTools, ", ")))
	}

	sb.WriteString(fmt.Sprintf("Please select up to %d most relevant memory entries.\n", ms.maxSelections))
	sb.WriteString("Only return the selected entry numbers (one per line), do not return other content.\n\n")

	sb.WriteString("Memory manifest:\n")
	for i, m := range manifest {
		freshness := MemoryFreshnessText(m.Modified)
		sb.WriteString(fmt.Sprintf("%d. [%s] %s — %s (type: %s, %d chars) %s\n",
			i+1, m.Filename, m.Name, m.Description, m.Type, m.SizeChars, freshness))
	}

	return sb.String()
}

// SelectRelevant uses AI client to select memory entries relevant to user query from manifest
// Returns list of selected manifest entry indices
func (ms *MemorySelector) SelectRelevant(ctx context.Context, userQuery string, manifest []MemoryManifestEntry, recentTools []string) ([]MemoryManifestEntry, error) {
	if len(manifest) == 0 {
		return nil, nil
	}

	// If manifest entry count does not exceed max selection count, return all directly
	if len(manifest) <= ms.maxSelections {
		return manifest, nil
	}

	prompt := ms.buildSelectionPrompt(userQuery, manifest, recentTools)

	req := ai.Request{
		SystemPrompt: []string{"You are a precise memory retrieval assistant, only return numbers, do not return other content."},
		Messages: []ai.MessageEntry{
			{Role: "user", Content: prompt},
		},
		MaxTokens: 256,
	}

	// Use StreamWithRetry with RecoveryCoordinator for coordinated retry/degrade/compress
	var eventCh <-chan ai.Event
	var err error
	if ms.coordinator != nil {
		eventCh, err = ai.StreamWithRetry(ctx, ms.aiClient, req, ms.retryConfig, ms.fallbackTracker, ms.coordinator)
	} else {
		eventCh, err = ms.aiClient.Stream(ctx, req)
	}
	if err != nil {
		return nil, ai.WithID(fmt.Errorf("AI memory selection request failed: %w", err), ai.ErrMemoryAI)
	}

	// Collect AI response text
	var responseText strings.Builder
	for event := range eventCh {
		switch e := event.(type) {
		case ai.TextDeltaEvent:
			responseText.WriteString(e.Content)
		case ai.UsageEvent:
			// D4: 记录记忆选择 AI 调用的 token cost
			if ms.usageTracker != nil && ms.aiClient != nil {
				ms.usageTracker.RecordUsage(ms.aiClient.Model(), e.InputTokens, e.OutputTokens, e.CacheCreation, e.CacheRead)
			}
		case ai.ErrorEvent:
			return nil, ai.WithID(fmt.Errorf("AI memory selection error: %w", e.Err), ai.ErrMemoryAI)
		}
	}

	// Parse AI returned numbers
	selectedIndices := parseSelectedIndices(responseText.String(), len(manifest))

	// AI 原始响应落一条 pick_raw 观测（只写不读，零行为变化）。
	// 动机：warmup_backfill 两次真机 selected=None（warmup_ok=true）无法定因 ——
	// 「返空串」与「返无数字散文」解析结果同为 0 条；pick_raw 让下一次复现可直接分辨。
	ms.observe(MemorySelectEvent{
		Branch:      traceBranchAIPick,
		ManifestN:   len(manifest),
		PickRaw:     traceTruncateRunes(responseText.String(), maxTracePickRunes),
		PickIndices: selectedIndices,
	})

	var result []MemoryManifestEntry
	// 2c 修复：用原子 Load 读取 sessionUsed，避免并发数据竞争（FACT-CONC-001）
	remaining := ms.maxSessionChars - int(ms.sessionUsed.Load())

	for _, idx := range selectedIndices {
		if len(result) >= ms.maxSelections {
			break
		}
		entry := manifest[idx]
		charCount := entry.SizeChars
		if charCount > ms.maxMemoryChars {
			charCount = ms.maxMemoryChars
		}
		if charCount > remaining {
			break
		}
		result = append(result, entry)
		remaining -= charCount
	}

	// Update session used character count
	// 2c 修复：先在局部变量累加，最后一次原子 Add，避免并发数据竞争（FACT-CONC-001）
	var usedTotal int64
	for _, e := range result {
		used := e.SizeChars
		if used > ms.maxMemoryChars {
			used = ms.maxMemoryChars
		}
		usedTotal += int64(used)
	}
	if usedTotal > 0 {
		ms.sessionUsed.Add(usedTotal)
	}

	return result, nil
}

// parseSelectedIndices parses selected numbers from AI response text (1-based → 0-based)
func parseSelectedIndices(text string, maxCount int) []int {
	var indices []int
	seen := make(map[int]bool)

	// Match numbers
	re := regexp.MustCompile(`\d+`)
	matches := re.FindAllString(text, -1)

	for _, m := range matches {
		var num int
		if _, err := fmt.Sscanf(m, "%d", &num); err != nil {
			continue
		}
		idx := num - 1 // Convert to 0-based
		if idx >= 0 && idx < maxCount && !seen[idx] {
			indices = append(indices, idx)
			seen[idx] = true
		}
	}

	return indices
}

// ============================================================
// AutoMemoryExtractor — automatic preference extractor
// ============================================================

// AutoMemoryExtractor automatically detects and extracts user preferences from conversation messages
type AutoMemoryExtractor struct {
	preferencePatterns []*regexp.Regexp // Regex patterns for preference expressions
	store              *MemoryStore     // 记忆存储层（写入提取结果，跨进程锁 + 0600）
	coordinator        *ai.RecoveryCoordinator
	retryConfig        ai.RetryConfig
	fallbackTracker    *ai.FallbackTracker
	usageTracker       *ai.UsageTracker // D4: 记忆提取 AI 调用的 cost 记录（nil 时降级跳过）
}

// NewAutoMemoryExtractor creates automatic preference extractor.
// store 提供记忆写入（跨进程锁 + 0600 权限 + EnforceFileLimit）。
func NewAutoMemoryExtractor(store *MemoryStore) *AutoMemoryExtractor {
	extractor := &AutoMemoryExtractor{
		store:       store,
		retryConfig: ai.DefaultRetryConfig(),
	}

	// 初始化中英文偏好检测正则
	extractor.preferencePatterns = []*regexp.Regexp{
		// 中文偏好表达
		regexp.MustCompile(`(?i)(我(?:习惯|喜欢|偏好|通常|总是|不想|讨厌|要|不要))`),
		regexp.MustCompile(`(?i)(请(?:总是|永远|确保|不要|别|用))`),
		regexp.MustCompile(`(?i)(从今以后(?:请|总是|记住|不要|别|用))`),
		regexp.MustCompile(`(?i)(以后请(?:用|总是|不要|别))`),
		regexp.MustCompile(`(?i)(记住我(?:喜欢|习惯|偏好|要|的))`),
		regexp.MustCompile(`(?i)(我(?:的|平时|日常)?(?:习惯|风格|偏好|设置))`),
		// English preference expressions
		regexp.MustCompile(`(?i)(I\s+(?:prefer|like|usually|always|never|want|don't\s+want|hate|dislike))`),
		regexp.MustCompile(`(?i)(please\s+(?:always|never|make\s+sure|don't))`),
		regexp.MustCompile(`(?i)(from\s+now\s+on)`),
		regexp.MustCompile(`(?i)(remember\s+(?:that\s+)?I)`),
		regexp.MustCompile(`(?i)(my\s+(?:preference|style|habit|setting))`),
	}

	return extractor
}

// SetRecoveryConfig sets the recovery coordinator, retry config, and fallback tracker
func (ame *AutoMemoryExtractor) SetRecoveryConfig(coordinator *ai.RecoveryCoordinator, retryConfig ai.RetryConfig, fallbackTracker *ai.FallbackTracker) {
	ame.coordinator = coordinator
	ame.retryConfig = retryConfig
	ame.fallbackTracker = fallbackTracker
}

// SetUsageTracker 注入 UsageTracker，供 ExtractAndSave 记录 AI 提取调用的 token cost（D4）。
// nil 时降级跳过 cost 记录，不影响提取功能。
func (ame *AutoMemoryExtractor) SetUsageTracker(tracker *ai.UsageTracker) {
	ame.usageTracker = tracker
}

// ShouldExtract checks if message list contains user preference expressions
// Returns true if messages contain extractable preference information
func (ame *AutoMemoryExtractor) ShouldExtract(messages []session.Message) bool {
	for _, msg := range messages {
		if msg.Role != session.RoleUser {
			continue
		}
		text := msg.TextContent()
		if text == "" {
			continue
		}
		for _, pattern := range ame.preferencePatterns {
			if pattern.MatchString(text) {
				return true
			}
		}
	}
	return false
}

// ExtractAndSave extracts preferences from messages and saves as memory entries
// Uses AI client to analyze message content, extracts structured preference information
func (ame *AutoMemoryExtractor) ExtractAndSave(ctx context.Context, client ai.Client, messages []session.Message) error {
	if !ame.ShouldExtract(messages) {
		return nil
	}

	// Collect user message text
	var userTexts []string
	for _, msg := range messages {
		if msg.Role == session.RoleUser {
			text := msg.TextContent()
			if text != "" {
				userTexts = append(userTexts, text)
			}
		}
	}

	if len(userTexts) == 0 {
		return nil
	}

	// Use AI to extract preferences
	prompt := buildExtractionPrompt(userTexts)

	req := ai.Request{
		SystemPrompt: []string{
			"You are a user preference extraction assistant. Extract explicit preferences and habits from user conversations." +
				"Please return in YAML format, containing name, description, type, content fields." +
				"type can only be user or feedback. Only return YAML, do not return other content.",
		},
		Messages: []ai.MessageEntry{
			{Role: "user", Content: prompt},
		},
		MaxTokens: 1024,
	}

	// Always use StreamWithRetry for consistent retry/degrade/compress behavior.
	// When coordinator is nil, only Phase 1 retry runs (no degrade/compress).
	rc := ame.retryConfig
	if rc.MaxRetries == 0 {
		rc = ai.DefaultRetryConfig()
	}
	eventCh, err := ai.StreamWithRetry(ctx, client, req, rc, ame.fallbackTracker, ame.coordinator)
	if err != nil {
		return ai.WithID(fmt.Errorf("AI preference extraction request failed: %w", err), ai.ErrMemoryAI)
	}

	var responseText strings.Builder
	for event := range eventCh {
		switch e := event.(type) {
		case ai.TextDeltaEvent:
			responseText.WriteString(e.Content)
		case ai.UsageEvent:
			// D4: 记录记忆提取 AI 调用的 token cost
			if ame.usageTracker != nil && client != nil {
				ame.usageTracker.RecordUsage(client.Model(), e.InputTokens, e.OutputTokens, e.CacheCreation, e.CacheRead)
			}
		case ai.ErrorEvent:
			return ai.WithID(fmt.Errorf("AI preference extraction error: %w", e.Err), ai.ErrMemoryAI)
		}
	}

	// Parse AI returned preference information
	entries, err := parseExtractedPreferences(responseText.String())
	if err != nil {
		return ai.WithID(fmt.Errorf("failed to parse preference extraction result: %w", err), ai.ErrMemoryAI)
	}

	// Save extracted preferences (via MemoryStore: 跨进程锁 + 0600 权限 + EnforceFileLimit)
	for _, entry := range entries {
		if err := ame.store.Write(*entry); err != nil {
			continue // Skip entries that failed to write
		}
	}

	return nil
}

// buildExtractionPrompt builds prompt for preference extraction
func buildExtractionPrompt(userTexts []string) string {
	var sb strings.Builder
	sb.WriteString("Below are conversation snippets with the user, please extract user preferences and habits:\n\n")
	for i, text := range userTexts {
		sb.WriteString(fmt.Sprintf("Conversation %d:\n%s\n\n", i+1, text))
	}
	sb.WriteString("Please return extracted preferences in the following YAML format (can be multiple):\n")
	sb.WriteString("```yaml\n")
	sb.WriteString("- name: preference-name\n")
	sb.WriteString("  description: Brief description\n")
	sb.WriteString("  type: user\n")
	sb.WriteString("  content: Detailed preference content\n")
	sb.WriteString("```\n")
	return sb.String()
}

// extractedPreference for parsing AI returned preference extraction result
type extractedPreference struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Type        MemoryType `yaml:"type"`
	Content     string     `yaml:"content"`
}

// parseExtractedPreferences parses AI returned preference extraction YAML result
func parseExtractedPreferences(text string) ([]*MemoryEntryV2, error) {
	// Try to extract YAML code block
	yamlContent := text
	if idx := strings.Index(text, "```yaml"); idx >= 0 {
		start := idx + 7
		end := strings.Index(text[start:], "```")
		if end >= 0 {
			yamlContent = text[start : start+end]
		}
	} else if idx := strings.Index(text, "```"); idx >= 0 {
		start := idx + 3
		end := strings.Index(text[start:], "```")
		if end >= 0 {
			yamlContent = text[start : start+end]
		}
	}

	var prefs []extractedPreference
	if err := yaml.Unmarshal([]byte(strings.TrimSpace(yamlContent)), &prefs); err != nil {
		// Try to parse single entry
		var single extractedPreference
		if err2 := yaml.Unmarshal([]byte(strings.TrimSpace(yamlContent)), &single); err2 != nil {
			return nil, ai.WithID(fmt.Errorf("failed to parse preference YAML: %w", err), ai.ErrMemoryParse)
		}
		prefs = []extractedPreference{single}
	}

	var entries []*MemoryEntryV2
	now := time.Now()
	for _, p := range prefs {
		if p.Name == "" || p.Content == "" {
			continue
		}
		if p.Type == "" {
			p.Type = MemoryTypeUser
		}
		if !isValidMemoryType(p.Type) {
			p.Type = MemoryTypeUser
		}
		entries = append(entries, &MemoryEntryV2{
			Name:        p.Name,
			Description: p.Description,
			Type:        p.Type,
			CreatedAt:   now,
			UpdatedAt:   now,
			Content:     p.Content,
		})
	}

	return entries, nil
}

// ============================================================
// MemoryPrefetch — memory prefetcher
// ============================================================

// MemoryPrefetch provides async memory prefetching capability, avoiding blocking main flow
type MemoryPrefetch struct {
	result   []MemoryManifestEntry // Prefetch result
	err      error                 // Prefetch error
	done     chan struct{}         // Completion signal
	consumed bool                  // Whether consumed
}

// StartMemoryPrefetch starts async memory prefetch, executes AI selection in background
// Returns MemoryPrefetch instance, results can be obtained via TryGet()
func StartMemoryPrefetch(ctx context.Context, selector *MemorySelector, userQuery string, manifest []MemoryManifestEntry, recentTools []string) *MemoryPrefetch {
	pf := &MemoryPrefetch{
		done: make(chan struct{}),
	}

	go func() {
		defer close(pf.done)
		result, err := selector.SelectRelevant(ctx, userQuery, manifest, recentTools)
		pf.result = result
		pf.err = err
	}()

	return pf
}

// TryGet tries to get prefetch result, returns nil if prefetch not yet complete (non-blocking)
// Second return value indicates if result is ready, third indicates if already consumed
func (pf *MemoryPrefetch) TryGet() ([]MemoryManifestEntry, error, bool) {
	if pf.consumed {
		return nil, nil, false
	}

	select {
	case <-pf.done:
		pf.consumed = true
		return pf.result, pf.err, true
	default:
		return nil, nil, false
	}
}

// WaitGet blocks waiting for prefetch result and returns
func (pf *MemoryPrefetch) WaitGet() ([]MemoryManifestEntry, error) {
	<-pf.done
	if pf.consumed {
		return pf.result, pf.err
	}
	pf.consumed = true
	return pf.result, pf.err
}
