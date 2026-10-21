// experience.go — H10 Phase 4 模块 1 ExperienceStore 核心结构 + Pattern/Pitfall + jsonl 读写（替换 Phase 2 noop 骨架）。
// REF-外部-同类CLI@2026-03-31快照-memory types.ts 相关性选择器选 top-5；借鉴逻辑：检索 + 注入心智；落地：Match + Save（实现不同，whale-code 用倒排索引 + LLM rerank）
// line 877-887-Pattern 8 字段定义；落地：本 struct 8 字段与总纲完全一致 + 新增 4 字段显式声明
// line 1074-H10 是 experience.go 唯一主设计方；落地：不越权改 N12/H11-N27
// 复用-替换 noop 骨架；落地：ExperienceStore struct 实现 Phase 2 noop 声明的接口
// 长路径兼容；落地：路径 > 240 加 \\?\ 前缀（短 hash 在 experience_ttl.go 处理）
// SNAPSHOT: pkg=internal/orchestrator; type=ExperienceStore(struct); paths={global:~/.whale/experience/,project:.whale/experience/}; subdirs={patterns,pitfalls,templates}; 8fields={PatternID,TaskSignature,Steps,FilesTouched,VerifiedModels,VerifiedAt,L2AdoptionRate,AvgCostCents}; 4new_fields={VerifiedState,Scope,Version,LastUsedAt}; concurrency=sync.RWMutex; cache_limit=1000(LRU); storage=append-only JSONL + _index.json

package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// VerifiedState 枚举常量（Phase 4 §3.3.2 四级生命周期）。
const (
	VerifiedStateUnverified            = "unverified"
	VerifiedStateProvisionallyVerified = "provisionally_verified"
	VerifiedStateVerified              = "verified"
	VerifiedStateDeprecated            = "deprecated"
)

// ScopeGlobal / ScopeProject 作用域枚举（Phase 4 §3.1.3）。
const (
	ScopeGlobal  = "global"
	ScopeProject = "project"
)

// 经验库子目录常量（Phase 4 §3.1.3 三层结构）。
const (
	subdirPatterns  = "patterns"
	subdirPitfalls  = "pitfalls"
	subdirTemplates = "templates"
)

// indexFileName 索引文件名（per scope）。
const indexFileName = "_index.json"

// pitfallsIndexName pitfall 索引文件名（per scope）。
const pitfallsIndexName = "_pitfalls_index.json"

// cacheLimitUpper 内存缓存上限（Phase 4 §3.1.3，LRU 淘汰最旧）。
const cacheLimitUpper = 1000

// ExperienceStore 经验库存储与检索实现（H10 主设计，§10.2）。
// 替换 Phase 2 noop 骨架，真实实现 patterns/pitfalls/templates 三层结构 + jsonl 读写。
// 并发安全：sync.RWMutex 保护并发 Save/Get/Match（致命反例 4 防竞态）。
type ExperienceStore struct {
	mu          sync.RWMutex
	globalPath  string              // ~/.whale/experience/（绝对路径）
	projectPath string              // .whale/experience/（绝对路径）
	index       *InvertedIndex      // 模块 2 注入（启动时构建，运行时增量更新）
	reranker    *LLMReranker        // 模块 3 注入（可空，空时 Match 退化为粗筛 top-1）
	cache       map[string]*Pattern // 内存缓存（上限 1000 条，LRU 淘汰）
	cacheOrder  []string            // LRU 顺序（按访问时间，最旧在头部）

	// provisionalAdoptions 内存字段（不落 jsonl）：pattern_id → orchestration_id 列表
	// 用于 isCrossSession 判定（防同编排会话误判为多次验证）。
	provisionalAdoptions map[string][]string

	// qualityStore 可选，用于 computeAdoptionRate 反查 quality.jsonl（Phase 4 §3.3.3）。
	qualityStore *QualityStore

	// nowFunc 时间函数（便于测试注入固定时间）。
	nowFunc func() time.Time
}

// Pattern 经验库条目，记录一类任务的可复用解法（Phase 4 §3.1.3）。
// 总纲 §7.1 已定 8 字段（不改名）+ v1 新增 4 字段（显式声明，用于生命周期管理）。
//
// ★ 2026-10-09：原 `AvgCostCents` 字段删除 —— 它注释里写"反查 CostRecord 算"，
// 而 CostRecord 早已随 D 批删除 ⇒ 该字段**无来源**（拍脑袋或恒 0）。
// 「不算价钱」铁律下不引入任何金额字段。
type Pattern struct {
	// === 总纲 §7.1 已定 8 字段（不改名） ===
	PatternID      string   `json:"pattern_id"`       // 形如 add-slash-command-v3
	TaskSignature  string   `json:"task_signature"`   // 任务签名（自然语言描述 + 关键动词）
	Steps          []Step   `json:"steps"`            // 解法步骤序列
	FilesTouched   []string `json:"files_touched"`    // 涉及文件路径（相对项目根）
	VerifiedModels []string `json:"verified_models"`  // 已验证可行的模型列表
	VerifiedAt     string   `json:"verified_at"`      // 最近一次验证时间 ISO8601
	L2AdoptionRate float64  `json:"l2_adoption_rate"` // 用户采纳率（0-1，滚动窗口 30 天）

	// === v1 新增 4 字段（显式声明，用于生命周期管理） ===
	VerifiedState string `json:"verified_state"` // unverified | provisionally_verified | verified | deprecated
	Scope         string `json:"scope"`          // global | project
	Version       int    `json:"version"`        // 同 task_signature 的版本号
	LastUsedAt    string `json:"last_used_at"`   // 最近一次被检索命中并注入的时间
}

// Step 解法的单步描述（Phase 4 §3.1.3）。
type Step struct {
	Action      string `json:"action"`      // edit_file / run_command / review
	Target      string `json:"target"`      // 文件路径 / 命令 / 审查项
	Description string `json:"description"` // 自然语言描述（≤100 字）
}

// Pitfall 经验库的"常见坑"条目（Phase 4 §3.1.3）。
type Pitfall struct {
	PitfallID     string `json:"pitfall_id"`
	TaskSignature string `json:"task_signature"`
	Symptom       string `json:"symptom"`     // 症状描述
	RootCause     string `json:"root_cause"`  // 根因
	Mitigation    string `json:"mitigation"`  // 规避措施
	Occurrences   int    `json:"occurrences"` // 累计出现次数
	LastSeenAt    string `json:"last_seen_at"`
	Scope         string `json:"scope"` // global | project
}

// patternIndexEntry _index.json 中的单条索引（pattern_id → 位置元数据）。
type patternIndexEntry struct {
	Scope         string   `json:"scope"`
	File          string   `json:"file"`   // patterns.jsonl 绝对路径
	Offset        int64    `json:"offset"` // 行字节偏移
	TaskSignature string   `json:"task_signature"`
	TaskType      string   `json:"task_type"`
	Tags          []string `json:"tags"`
	VerifiedState string   `json:"verified_state"`
}

// patternIndex _index.json 完整结构（per scope）。
type patternIndex struct {
	Entries map[string]patternIndexEntry `json:"entries"` // pattern_id → entry
}

// pitfallIndexEntry _pitfalls_index.json 中的单条索引。
type pitfallIndexEntry struct {
	Scope         string `json:"scope"`
	File          string `json:"file"`
	Offset        int64  `json:"offset"`
	TaskSignature string `json:"task_signature"`
}

// pitfallIndex _pitfalls_index.json 完整结构（per scope）。
type pitfallIndex struct {
	Entries map[string]pitfallIndexEntry `json:"entries"`
}

// patternsFileName 单 scope 下 patterns.jsonl 文件名。
const patternsFileName = "patterns.jsonl"

// pitfallsFileName 单 scope 下 pitfalls.jsonl 文件名。
const pitfallsFileName = "pitfalls.jsonl"

// NewExperienceStore 创建经验库存储，globalPath/projectPath 为绝对路径（Phase 4 §3.1.3）。
// 自动创建目录结构，并从已有 _index.json 加载索引到内存（崩溃恢复）。
// index 与 reranker 通过 SetIndex / SetReranker 注入（避免循环依赖）。
func NewExperienceStore(globalPath, projectPath string) (*ExperienceStore, error) {
	if globalPath == "" {
		return nil, fmt.Errorf("experience: globalPath must not be empty")
	}
	if projectPath == "" {
		return nil, fmt.Errorf("experience: projectPath must not be empty")
	}

	// 创建三层目录结构
	for _, root := range []string{globalPath, projectPath} {
		for _, sub := range []string{subdirPatterns, subdirPitfalls, subdirTemplates} {
			dir := filepath.Join(root, sub)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("experience: mkdir %s: %w", dir, err)
			}
		}
	}

	s := &ExperienceStore{
		globalPath:           globalPath,
		projectPath:          projectPath,
		index:                NewInvertedIndex(),
		cache:                make(map[string]*Pattern),
		cacheOrder:           make([]string, 0, cacheLimitUpper),
		provisionalAdoptions: make(map[string][]string),
		nowFunc:              time.Now,
	}

	// 从已有 _index.json 加载到倒排索引（崩溃恢复）
	if err := s.loadExistingIndexes(); err != nil {
		return nil, fmt.Errorf("experience: load existing indexes: %w", err)
	}

	return s, nil
}

// SetIndex 注入倒排索引（启动后调用）。
func (s *ExperienceStore) SetIndex(idx *InvertedIndex) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.index = idx
}

// SetReranker 注入 LLM 精排器（启动后调用）。
func (s *ExperienceStore) SetReranker(r *LLMReranker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reranker = r
}

// SetQualityStore 注入 QualityStore（用于 computeAdoptionRate 反查 quality.jsonl）。
func (s *ExperienceStore) SetQualityStore(q *QualityStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.qualityStore = q
}

// SetNowFunc 注入时间函数（测试用，便于固定时间）。
func (s *ExperienceStore) SetNowFunc(fn func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nowFunc = fn
}

// Get 按 pattern_id 读取 Pattern（先查内存缓存，未命中读 jsonl，Phase 4 §3.1.3）。
func (s *ExperienceStore) Get(patternID string) (*Pattern, error) {
	if patternID == "" {
		return nil, fmt.Errorf("experience: patternID must not be empty")
	}

	// 1. 先查缓存
	s.mu.RLock()
	if p, ok := s.cache[patternID]; ok {
		s.mu.RUnlock()
		// 更新 LRU 顺序（移到尾部）
		s.mu.Lock()
		s.touchCacheLocked(patternID)
		s.mu.Unlock()
		return p, nil
	}
	s.mu.RUnlock()

	// 2. 未命中读 _index.json 定位文件:line
	p, err := s.readFromIndex(patternID)
	if err != nil {
		return nil, err
	}

	// 3. 写入缓存
	s.mu.Lock()
	s.putCacheLocked(patternID, p)
	s.mu.Unlock()

	return p, nil
}

// Save 持久化 Pattern 到 jsonl（append-only + 索引同步更新，致命反例 4 加锁，Phase 4 §3.1.3）。
// 加写锁 → JSON marshal → 按 scope 定位 patterns.jsonl → append 写一行 + \n → 更新 _index.json + 倒排索引 + 缓存。
func (s *ExperienceStore) Save(p *Pattern) error {
	if p == nil {
		return fmt.Errorf("experience: pattern must not be nil")
	}
	if p.PatternID == "" {
		return fmt.Errorf("experience: PatternID must not be empty")
	}
	if p.Scope != ScopeGlobal && p.Scope != ScopeProject {
		return fmt.Errorf("experience: invalid Scope %q (must be global|project)", p.Scope)
	}
	if p.VerifiedState == "" {
		p.VerifiedState = VerifiedStateUnverified
	}
	if p.VerifiedAt == "" {
		p.VerifiedAt = s.nowFunc().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("experience: marshal pattern %s: %w", p.PatternID, err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	// 定位 scope 对应的 patterns.jsonl 路径
	root := s.scopeRoot(p.Scope)
	patternsPath := filepath.Join(root, subdirPatterns, patternsFileName)

	// Windows 长路径处理
	patternsPath, err = windowsLongPathHandler(patternsPath, p.PatternID)
	if err != nil {
		return fmt.Errorf("experience: windows long path: %w", err)
	}

	// O_APPEND|O_CREATE|O_WRONLY 追加写
	f, err := os.OpenFile(patternsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("experience: open %s: %w", patternsPath, err)
	}

	// 获取写入前文件大小（即新行起始偏移）
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("experience: stat %s: %w", patternsPath, err)
	}
	offset := info.Size()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("experience: write %s: %w", patternsPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("experience: close %s: %w", patternsPath, err)
	}

	// 同步更新 _index.json（同事务，崩溃恢复从 jsonl 重建）
	if err := s.updatePatternIndexLocked(p.Scope, p.PatternID, patternIndexEntry{
		Scope:         p.Scope,
		File:          patternsPath,
		Offset:        offset,
		TaskSignature: p.TaskSignature,
		TaskType:      extractTaskType(p),
		Tags:          extractTags(p),
		VerifiedState: p.VerifiedState,
	}); err != nil {
		return fmt.Errorf("experience: update index: %w", err)
	}

	// 更新倒排索引
	if s.index != nil {
		s.index.Add(p)
	}

	// 更新缓存
	s.putCacheLocked(p.PatternID, p)

	return nil
}

// SavePitfall 持久化 Pitfall 到 pitfalls.jsonl（Phase 4 §3.1.3）。
func (s *ExperienceStore) SavePitfall(p *Pitfall) error {
	if p == nil {
		return fmt.Errorf("experience: pitfall must not be nil")
	}
	if p.PitfallID == "" {
		return fmt.Errorf("experience: PitfallID must not be empty")
	}
	if p.Scope != ScopeGlobal && p.Scope != ScopeProject {
		return fmt.Errorf("experience: invalid pitfall Scope %q", p.Scope)
	}
	if p.LastSeenAt == "" {
		p.LastSeenAt = s.nowFunc().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("experience: marshal pitfall %s: %w", p.PitfallID, err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	root := s.scopeRoot(p.Scope)
	pitfallsPath := filepath.Join(root, subdirPitfalls, pitfallsFileName)
	pitfallsPath, err = windowsLongPathHandler(pitfallsPath, p.PitfallID)
	if err != nil {
		return fmt.Errorf("experience: windows long path (pitfall): %w", err)
	}

	f, err := os.OpenFile(pitfallsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("experience: open pitfall %s: %w", pitfallsPath, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("experience: stat pitfall %s: %w", pitfallsPath, err)
	}
	offset := info.Size()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("experience: write pitfall %s: %w", pitfallsPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("experience: close pitfall %s: %w", pitfallsPath, err)
	}

	if err := s.updatePitfallIndexLocked(p.Scope, p.PitfallID, pitfallIndexEntry{
		Scope:         p.Scope,
		File:          pitfallsPath,
		Offset:        offset,
		TaskSignature: p.TaskSignature,
	}); err != nil {
		return fmt.Errorf("experience: update pitfall index: %w", err)
	}

	return nil
}

// ListByScope 列出指定作用域的所有 pattern（扫描 patterns.jsonl 全量，Phase 4 §3.1.3）。
// append-only jsonl 中同 PatternID 可能有多行（更新后追加），去重保留最后一条（最新版本）。
func (s *ExperienceStore) ListByScope(scope string) ([]*Pattern, error) {
	if scope != ScopeGlobal && scope != ScopeProject {
		return nil, fmt.Errorf("experience: invalid scope %q", scope)
	}

	s.mu.RLock()
	root := s.scopeRoot(scope)
	s.mu.RUnlock()

	patternsPath := filepath.Join(root, subdirPatterns, patternsFileName)
	data, err := os.ReadFile(patternsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("experience: read %s: %w", patternsPath, err)
	}

	// 用 map 去重：同 PatternID 保留最后一条（append-only jsonl，后写入为最新）
	seen := make(map[string]*Pattern)
	for _, line := range splitJSONLines(data) {
		if len(line) == 0 {
			continue
		}
		var p Pattern
		if err := json.Unmarshal(line, &p); err != nil {
			continue // 跳过损坏行
		}
		seen[p.PatternID] = &p // 后写入覆盖先写入（最新版本）
	}
	result := make([]*Pattern, 0, len(seen))
	for _, p := range seen {
		result = append(result, p)
	}
	return result, nil
}

// ListPitfallsByScope 列出指定作用域的所有 pitfall（扫描 pitfalls.jsonl）。
// append-only jsonl 中同 PitfallID 可能有多行（更新后追加），去重保留最后一条（最新版本）。
func (s *ExperienceStore) ListPitfallsByScope(scope string) ([]*Pitfall, error) {
	if scope != ScopeGlobal && scope != ScopeProject {
		return nil, fmt.Errorf("experience: invalid scope %q", scope)
	}

	s.mu.RLock()
	root := s.scopeRoot(scope)
	s.mu.RUnlock()

	pitfallsPath := filepath.Join(root, subdirPitfalls, pitfallsFileName)
	data, err := os.ReadFile(pitfallsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("experience: read pitfalls %s: %w", pitfallsPath, err)
	}

	// 用 map 去重：同 PitfallID 保留最后一条（append-only jsonl，后写入为最新）
	seen := make(map[string]*Pitfall)
	for _, line := range splitJSONLines(data) {
		if len(line) == 0 {
			continue
		}
		var p Pitfall
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		seen[p.PitfallID] = &p // 后写入覆盖先写入（最新版本）
	}
	result := make([]*Pitfall, 0, len(seen))
	for _, p := range seen {
		result = append(result, p)
	}
	return result, nil
}

// Match 检索经验库，返回 top-K 候选 pattern（对齐总纲 §7.2 line 892）。
// 内部委托 InvertedIndex.Search 粗筛 + LLMReranker.Rerank 精排。
// 作用域合并查询：global + project 合并，同 task_signature 命中时 project 优先。
// LLM reranker 不可用时降级为粗筛 top-1（Phase 4 §5.1）。
func (s *ExperienceStore) Match(signature string, taskType string, tags []string) ([]*Pattern, error) {
	if signature == "" {
		return nil, fmt.Errorf("experience: signature must not be empty")
	}

	s.mu.RLock()
	idx := s.index
	reranker := s.reranker
	s.mu.RUnlock()

	if idx == nil {
		return nil, fmt.Errorf("experience: inverted index not set")
	}

	// 1. 倒排索引粗筛 top-20
	candidateIDs := idx.Search(signature, taskType, tags, 20)
	if len(candidateIDs) == 0 {
		return []*Pattern{}, nil
	}

	// 2. 加载候选 Pattern（去重：同 task_signature 保留 project scope）
	seen := make(map[string]string) // task_signature → pattern_id（已选）
	var candidates []*Pattern
	for _, id := range candidateIDs {
		p, err := s.Get(id)
		if err != nil {
			continue
		}
		// 作用域合并：同 task_signature 优先 project
		if existingID, ok := seen[p.TaskSignature]; ok {
			existing, err := s.Get(existingID)
			if err == nil && existing.Scope == ScopeProject {
				// 已选的是 project，跳过当前
				continue
			}
			if p.Scope == ScopeProject {
				// 当前是 project，替换已选的
				candidates = removePatternByID(candidates, existingID)
				candidates = append(candidates, p)
				seen[p.TaskSignature] = id
				continue
			}
			// 都是 global，跳过当前（保留首个）
			continue
		}
		seen[p.TaskSignature] = id
		candidates = append(candidates, p)
	}

	// 3. 取 top-5
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}

	// 4. LLM 精排（reranker 不可用时降级为粗筛 top-1，§5.1）
	if reranker == nil || len(candidates) <= 1 {
		return candidates, nil
	}

	summaries := make([]PatternSummary, 0, len(candidates))
	for _, p := range candidates {
		summaries = append(summaries, PatternSummary{
			PatternID:     p.PatternID,
			TaskSignature: p.TaskSignature,
			Tags:          extractTags(p),
			VerifiedState: p.VerifiedState,
			AdoptionRate:  p.L2AdoptionRate,
		})
	}

	sortedIDs, _, err := reranker.Rerank(nil, signature, summaries)
	if err != nil || len(sortedIDs) == 0 {
		// 降级：返回粗筛结果（不阻塞编排，§5.1）
		return candidates, nil
	}

	// 按 LLM 排序重排 candidates
	idToPattern := make(map[string]*Pattern, len(candidates))
	for _, p := range candidates {
		idToPattern[p.PatternID] = p
	}
	reranked := make([]*Pattern, 0, len(sortedIDs))
	for _, id := range sortedIDs {
		if p, ok := idToPattern[id]; ok {
			reranked = append(reranked, p)
		}
	}
	if len(reranked) == 0 {
		return candidates, nil
	}
	return reranked, nil
}

// ClearCache 清空内存缓存（测试用，模拟缓存未命中）。
func (s *ExperienceStore) ClearCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = make(map[string]*Pattern)
	s.cacheOrder = make([]string, 0, cacheLimitUpper)
}

// CacheSize 返回当前缓存大小（测试用）。
func (s *ExperienceStore) CacheSize() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.cache)
}

// GlobalPath 返回 global 作用域根路径（测试用）。
func (s *ExperienceStore) GlobalPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.globalPath
}

// ProjectPath 返回 project 作用域根路径（测试用）。
func (s *ExperienceStore) ProjectPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.projectPath
}

// IndexFilePath 返回指定 scope 的 _index.json 路径（测试用）。
func (s *ExperienceStore) IndexFilePath(scope string) string {
	return filepath.Join(s.scopeRoot(scope), subdirPatterns, indexFileName)
}

// PitfallsIndexFilePath 返回指定 scope 的 _pitfalls_index.json 路径（测试用）。
func (s *ExperienceStore) PitfallsIndexFilePath(scope string) string {
	return filepath.Join(s.scopeRoot(scope), subdirPitfalls, pitfallsIndexName)
}

// PatternsFilePath 返回指定 scope 的 patterns.jsonl 路径（测试用）。
func (s *ExperienceStore) PatternsFilePath(scope string) string {
	return filepath.Join(s.scopeRoot(scope), subdirPatterns, patternsFileName)
}

// PitfallsFilePath 返回指定 scope 的 pitfalls.jsonl 路径（测试用）。
func (s *ExperienceStore) PitfallsFilePath(scope string) string {
	return filepath.Join(s.scopeRoot(scope), subdirPitfalls, pitfallsFileName)
}

// scopeRoot 返回 scope 对应的根路径（调用方需持锁）。
func (s *ExperienceStore) scopeRoot(scope string) string {
	if scope == ScopeGlobal {
		return s.globalPath
	}
	return s.projectPath
}

// readFromIndex 从 _index.json 定位 pattern_id 对应文件:offset，读取 jsonl 行（Phase 4 §3.1.3）。
func (s *ExperienceStore) readFromIndex(patternID string) (*Pattern, error) {
	// 先查 global，再查 project
	for _, scope := range []string{ScopeGlobal, ScopeProject} {
		entry, err := s.lookupPatternIndex(scope, patternID)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(entry.File)
		if err != nil {
			continue
		}
		line := readLineAtOffset(data, entry.Offset)
		if len(line) == 0 {
			continue
		}
		var p Pattern
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		return &p, nil
	}
	return nil, fmt.Errorf("experience: pattern %s not found in any scope", patternID)
}

// loadExistingIndexes 启动时从 _index.json 加载到倒排索引（崩溃恢复）。
//
// V3-5-1 接入：使用 util.LoadWithRecovery 替换 os.ReadFile + json.Unmarshal。
// 文件损坏时隔离到 ~/.whale/corrupt/ 并跳过该 scope（不 panic，不阻塞启动）。
func (s *ExperienceStore) loadExistingIndexes() error {
	for _, scope := range []string{ScopeGlobal, ScopeProject} {
		root := s.scopeRoot(scope)
		idxPath := filepath.Join(root, subdirPatterns, indexFileName)
		// V3-5-1：使用 LoadWithRecovery 替换 os.ReadFile + json.Unmarshal，损坏时隔离文件。
		// corruptDir 推导：globalPath 为 ~/.whale/experience/，向上 1 级到 ~/.whale/
		corruptDir := filepath.Join(filepath.Dir(s.globalPath), "corrupt")
		var idx patternIndex
		if err := util.LoadWithRecovery(idxPath, &idx, corruptDir); err != nil {
			if errors.Is(err, util.ErrFileCorrupted) {
				// 索引文件损坏已被隔离，跳过该 scope 的倒排索引重建
				log.Printf("[orchestrator] experience index file corrupted and quarantined: %s", idxPath)
				continue
			}
			if os.IsNotExist(err) {
				continue // 首次启动，无索引文件
			}
			return fmt.Errorf("read index %s: %w", idxPath, err)
		}
		// 重建倒排索引：从 patterns.jsonl 读全量 Pattern 并 Add
		for _, entry := range idx.Entries {
			fileData, err := os.ReadFile(entry.File)
			if err != nil {
				continue
			}
			line := readLineAtOffset(fileData, entry.Offset)
			if len(line) == 0 {
				continue
			}
			var p Pattern
			if err := json.Unmarshal(line, &p); err != nil {
				continue
			}
			if s.index != nil {
				s.index.Add(&p)
			}
		}
	}
	return nil
}

// updatePatternIndexLocked 更新 _index.json（调用方需持写锁）。
func (s *ExperienceStore) updatePatternIndexLocked(scope, patternID string, entry patternIndexEntry) error {
	idxPath := filepath.Join(s.scopeRoot(scope), subdirPatterns, indexFileName)

	// 读现有索引
	var idx patternIndex
	data, err := os.ReadFile(idxPath)
	if err == nil {
		_ = json.Unmarshal(data, &idx)
	}
	if idx.Entries == nil {
		idx.Entries = make(map[string]patternIndexEntry)
	}
	idx.Entries[patternID] = entry

	// 原子写入：先写临时文件再 rename
	newData, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}
	tmp := idxPath + ".tmp"
	if err := os.WriteFile(tmp, newData, 0o644); err != nil {
		return fmt.Errorf("write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, idxPath); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmp, idxPath, err)
	}
	return nil
}

// updatePitfallIndexLocked 更新 _pitfalls_index.json（调用方需持写锁）。
func (s *ExperienceStore) updatePitfallIndexLocked(scope, pitfallID string, entry pitfallIndexEntry) error {
	idxPath := filepath.Join(s.scopeRoot(scope), subdirPitfalls, pitfallsIndexName)

	var idx pitfallIndex
	data, err := os.ReadFile(idxPath)
	if err == nil {
		_ = json.Unmarshal(data, &idx)
	}
	if idx.Entries == nil {
		idx.Entries = make(map[string]pitfallIndexEntry)
	}
	idx.Entries[pitfallID] = entry

	newData, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal pitfall index: %w", err)
	}
	tmp := idxPath + ".tmp"
	if err := os.WriteFile(tmp, newData, 0o644); err != nil {
		return fmt.Errorf("write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, idxPath); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmp, idxPath, err)
	}
	return nil
}

// lookupPatternIndex 查询 pattern_id 在指定 scope 的索引条目。
func (s *ExperienceStore) lookupPatternIndex(scope, patternID string) (patternIndexEntry, error) {
	idxPath := filepath.Join(s.scopeRoot(scope), subdirPatterns, indexFileName)
	data, err := os.ReadFile(idxPath)
	if err != nil {
		return patternIndexEntry{}, fmt.Errorf("read index: %w", err)
	}
	var idx patternIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return patternIndexEntry{}, fmt.Errorf("unmarshal index: %w", err)
	}
	entry, ok := idx.Entries[patternID]
	if !ok {
		return patternIndexEntry{}, fmt.Errorf("pattern_id %s not in scope %s index", patternID, scope)
	}
	return entry, nil
}

// LookupPitfallBySignature 按 task_signature 查询 pitfall（用于 RecordPitfall 累加）。
func (s *ExperienceStore) LookupPitfallBySignature(scope, signature string) (*Pitfall, error) {
	idxPath := filepath.Join(s.scopeRoot(scope), subdirPitfalls, pitfallsIndexName)
	data, err := os.ReadFile(idxPath)
	if err != nil {
		return nil, nil // 无索引视为未找到
	}
	var idx pitfallIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, nil
	}
	for pitfallID, entry := range idx.Entries {
		if entry.TaskSignature != signature {
			continue
		}
		fileData, err := os.ReadFile(entry.File)
		if err != nil {
			continue
		}
		line := readLineAtOffset(fileData, entry.Offset)
		if len(line) == 0 {
			continue
		}
		var p Pitfall
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		_ = pitfallID
		return &p, nil
	}
	return nil, nil
}

// putCacheLocked 写入缓存（调用方需持写锁），LRU 淘汰最旧。
func (s *ExperienceStore) putCacheLocked(patternID string, p *Pattern) {
	if _, exists := s.cache[patternID]; exists {
		// 已存在，更新并 touch
		s.cache[patternID] = p
		s.touchCacheLocked(patternID)
		return
	}
	// 新增：检查容量
	if len(s.cache) >= cacheLimitUpper && len(s.cacheOrder) > 0 {
		// 淘汰最旧
		oldest := s.cacheOrder[0]
		delete(s.cache, oldest)
		s.cacheOrder = s.cacheOrder[1:]
	}
	s.cache[patternID] = p
	s.cacheOrder = append(s.cacheOrder, patternID)
}

// touchCacheLocked 更新 LRU 顺序（调用方需持写锁）。
func (s *ExperienceStore) touchCacheLocked(patternID string) {
	for i, id := range s.cacheOrder {
		if id == patternID {
			// 移到尾部
			s.cacheOrder = append(s.cacheOrder[:i], s.cacheOrder[i+1:]...)
			s.cacheOrder = append(s.cacheOrder, patternID)
			break
		}
	}
}

// extractTaskType 从 Pattern 提取 task_type（从 VerifiedModels 或 TaskSignature 启发式）。
func extractTaskType(p *Pattern) string {
	// 简化：从 TaskSignature 中匹配已知 task_type
	sig := strings.ToLower(p.TaskSignature)
	for _, tt := range []string{TaskTypeBugFix, TaskTypeDocGen, TaskTypePlan, TaskTypeReview, TaskTypeImplement} {
		if strings.Contains(sig, tt) || strings.Contains(sig, strings.ReplaceAll(tt, "-", "_")) {
			return tt
		}
	}
	return ""
}

// extractTags 从 Pattern 提取 tags（FilesTouched 文件扩展名 + TaskSignature tokens）。
func extractTags(p *Pattern) []string {
	tagSet := make(map[string]struct{})
	// 文件扩展名作为 tag
	for _, f := range p.FilesTouched {
		ext := strings.ToLower(filepath.Ext(f))
		if ext != "" {
			tagSet[ext] = struct{}{}
		}
	}
	// TaskSignature 的 tokens 作为 tag
	for _, tok := range tokenize(p.TaskSignature) {
		tagSet[tok] = struct{}{}
	}
	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	return tags
}

// removePatternByID 从切片移除指定 pattern_id。
func removePatternByID(slice []*Pattern, patternID string) []*Pattern {
	for i, p := range slice {
		if p.PatternID == patternID {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

// splitJSONLines 按字节切片切分为多行（去掉空行）。
func splitJSONLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			line := data[start:i]
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// readLineAtOffset 从 data 的 offset 位置读取一行（到 \n 或 EOF）。
func readLineAtOffset(data []byte, offset int64) []byte {
	if offset < 0 || int(offset) >= len(data) {
		return nil
	}
	start := int(offset)
	for i := start; i < len(data); i++ {
		if data[i] == '\n' {
			return data[start:i]
		}
	}
	return data[start:]
}
