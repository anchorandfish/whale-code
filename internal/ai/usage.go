package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// UsageTracker is the API usage tracker
// Aligned with a comparable CLI cost-tracker.ts: precisely tracks token usage based on API-returned usage
//
// T1-BSNAPSHOT：新增 RecentModelUsage 字段（按 model 记录最近一次远端 usage）
// 供 TokenSource.PostCallActual 读取最近一次真实 usage（非累计值）。
// 字段为 map[string]*ModelUsage，序列化到 JSON 兼容旧 settings.json（向后兼容）。
type UsageTracker struct {
	mu sync.RWMutex
	// 脏标记 —— 有未落盘的新记录才允许 saveLocked 写盘。
	// unexported ⇒ encoding/json 跳过，落盘 schema 零变化。
	dirty               atomic.Bool
	TotalInputTokens    int                    `json:"total_input_tokens"`
	TotalOutputTokens   int                    `json:"total_output_tokens"`
	TotalCacheCreation  int                    `json:"total_cache_creation"`
	TotalCacheRead      int                    `json:"total_cache_read"`
	TotalAPICalls       int                    `json:"total_api_calls"`
	ModelUsage          map[string]*ModelUsage `json:"model_usage"`
	TotalAPIDurationMs  int64                  `json:"total_api_duration_ms"`
	TotalToolDurationMs int64                  `json:"total_tool_duration_ms"`
	AgentUsage          map[string]*AgentUsage `json:"agent_usage"`
	// RecentModelUsage 按 model 记录最近一次远端 usage（T1-B SNAPSHOT）
	// 供 TokenSource.PostCallActual 读取「最近一次真实 usage」（区别于 ModelUsage 的累计计数）。
	// 每次 RecordUsage/RecordAgentUsage 时覆盖写入（保留最新一次）。
	RecentModelUsage map[string]*ModelUsage `json:"recent_model_usage,omitempty"`
	storePath        string
	stopAutoSave     chan struct{}
	autoSaveRunning  bool
}

// ModelUsage tracks usage per model
type ModelUsage struct {
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	CacheCreation int `json:"cache_creation"`
	CacheRead     int `json:"cache_read"`
	APICalls      int `json:"api_calls"`
}

// AgentUsage tracks sub-agent usage records
type AgentUsage struct {
	AgentID       string                 `json:"agent_id"`
	ModelUsage    map[string]*ModelUsage `json:"model_usage"`
	InputTokens   int                    `json:"input_tokens"`
	OutputTokens  int                    `json:"output_tokens"`
	CacheCreation int                    `json:"cache_creation"`
	CacheRead     int                    `json:"cache_read"`
}

// NewUsageTracker creates a usage tracker
// Automatically starts periodic auto-save every 30 seconds
//
// T1-B：初始化 RecentModelUsage map（避免 nil map 写入 panic）。
func NewUsageTracker(storePath string) *UsageTracker {
	t := &UsageTracker{
		ModelUsage:       make(map[string]*ModelUsage),
		RecentModelUsage: make(map[string]*ModelUsage),
		storePath:        storePath,
	}
	if storePath != "" {
		t.StartAutoSave(30 * time.Second)
	}
	return t
}

// RecordUsage records an API usage
//
// T1-B：同步覆盖写入 RecentModelUsage[model]，记录最近一次远端 usage
// 供 TokenSource.PostCallActual 读取（区别于 ModelUsage 的累计计数）。
func (t *UsageTracker) RecordUsage(model string, inputTokens, outputTokens, cacheCreation, cacheRead int) {
	t.mu.Lock()
	t.dirty.Store(true) // 有新记录，允许下次落盘

	t.TotalInputTokens += inputTokens
	t.TotalOutputTokens += outputTokens
	t.TotalCacheCreation += cacheCreation
	t.TotalCacheRead += cacheRead
	t.TotalAPICalls++

	if _, ok := t.ModelUsage[model]; !ok {
		t.ModelUsage[model] = &ModelUsage{}
	}
	mu := t.ModelUsage[model]
	mu.InputTokens += inputTokens
	mu.OutputTokens += outputTokens
	mu.CacheCreation += cacheCreation
	mu.CacheRead += cacheRead
	mu.APICalls++

	// T1-B：覆盖写入 RecentModelUsage（最近一次 usage，非累计）
	t.RecentModelUsage[model] = &ModelUsage{
		InputTokens:   inputTokens,
		OutputTokens:  outputTokens,
		CacheCreation: cacheCreation,
		CacheRead:     cacheRead,
		APICalls:      1,
	}

	shouldSave := t.storePath != "" && t.TotalAPICalls%3 == 0
	t.mu.Unlock()

	if shouldSave {
		_ = t.Save()
	}
}

// RecordAPIDuration records API call duration
func (t *UsageTracker) RecordAPIDuration(durationMs int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dirty.Store(true) //
	t.TotalAPIDurationMs += durationMs
}

// GetTotalAPICalls 返回累计 API 调用次数（并发安全，持 RLock）。
// N2 新增：供 command.usageTrackerAdapter 适配器读取，激活 /stats 死代码。
// TotalAPICalls 字段为非原子 int，必须通过此 getter 持锁读取，避免并发读写竞争。
func (t *UsageTracker) GetTotalAPICalls() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.TotalAPICalls
}

// RecordToolDuration records tool execution duration
func (t *UsageTracker) RecordToolDuration(durationMs int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dirty.Store(true) //
	t.TotalToolDurationMs += durationMs
}

// RecordAgentUsage records sub-agent API usage
//
// T1-B：同步覆盖写入 RecentModelUsage[model]，与 RecordUsage 行为一致。
func (t *UsageTracker) RecordAgentUsage(agentID, model string, inputTokens, outputTokens, cacheCreation, cacheRead int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dirty.Store(true) //

	// Accumulate to total
	t.TotalInputTokens += inputTokens
	t.TotalOutputTokens += outputTokens
	t.TotalCacheCreation += cacheCreation
	t.TotalCacheRead += cacheRead

	// Accumulate to model usage
	if _, ok := t.ModelUsage[model]; !ok {
		t.ModelUsage[model] = &ModelUsage{}
	}
	mu := t.ModelUsage[model]
	mu.InputTokens += inputTokens
	mu.OutputTokens += outputTokens
	mu.CacheCreation += cacheCreation
	mu.CacheRead += cacheRead
	mu.APICalls++

	// T1-B：覆盖写入 RecentModelUsage（最近一次 usage，非累计）
	t.RecentModelUsage[model] = &ModelUsage{
		InputTokens:   inputTokens,
		OutputTokens:  outputTokens,
		CacheCreation: cacheCreation,
		CacheRead:     cacheRead,
		APICalls:      1,
	}

	// Accumulate to agent usage
	if t.AgentUsage == nil {
		t.AgentUsage = make(map[string]*AgentUsage)
	}
	if _, ok := t.AgentUsage[agentID]; !ok {
		t.AgentUsage[agentID] = &AgentUsage{
			AgentID:    agentID,
			ModelUsage: make(map[string]*ModelUsage),
		}
	}
	au := t.AgentUsage[agentID]
	au.InputTokens += inputTokens
	au.OutputTokens += outputTokens
	au.CacheCreation += cacheCreation
	au.CacheRead += cacheRead
	if _, ok := au.ModelUsage[model]; !ok {
		au.ModelUsage[model] = &ModelUsage{}
	}
	au.ModelUsage[model].InputTokens += inputTokens
	au.ModelUsage[model].OutputTokens += outputTokens
	au.ModelUsage[model].CacheCreation += cacheCreation
	au.ModelUsage[model].CacheRead += cacheRead
	au.ModelUsage[model].APICalls++
}

// MergeAgentUsage merges sub-agent usage tracker into parent
func (t *UsageTracker) MergeAgentUsage(agentID string, child *UsageTracker) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dirty.Store(true) //

	child.mu.RLock()
	defer child.mu.RUnlock()

	// Accumulate child tracker total to parent
	t.TotalInputTokens += child.TotalInputTokens
	t.TotalOutputTokens += child.TotalOutputTokens
	t.TotalCacheCreation += child.TotalCacheCreation
	t.TotalCacheRead += child.TotalCacheRead

	// Merge model usage
	for model, usage := range child.ModelUsage {
		if _, ok := t.ModelUsage[model]; !ok {
			t.ModelUsage[model] = &ModelUsage{}
		}
		t.ModelUsage[model].InputTokens += usage.InputTokens
		t.ModelUsage[model].OutputTokens += usage.OutputTokens
		t.ModelUsage[model].CacheCreation += usage.CacheCreation
		t.ModelUsage[model].CacheRead += usage.CacheRead
		t.ModelUsage[model].APICalls += usage.APICalls
	}

	// Store as agent usage
	if t.AgentUsage == nil {
		t.AgentUsage = make(map[string]*AgentUsage)
	}
	t.AgentUsage[agentID] = &AgentUsage{
		AgentID:       agentID,
		ModelUsage:    child.ModelUsage,
		InputTokens:   child.TotalInputTokens,
		OutputTokens:  child.TotalOutputTokens,
		CacheCreation: child.TotalCacheCreation,
		CacheRead:     child.TotalCacheRead,
	}
}

// MergeFrom merges another tracker usage into the current tracker
func (t *UsageTracker) MergeFrom(other *UsageTracker) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dirty.Store(true) //

	other.mu.RLock()
	defer other.mu.RUnlock()

	t.TotalInputTokens += other.TotalInputTokens
	t.TotalOutputTokens += other.TotalOutputTokens
	t.TotalCacheCreation += other.TotalCacheCreation
	t.TotalCacheRead += other.TotalCacheRead

	for model, usage := range other.ModelUsage {
		if _, ok := t.ModelUsage[model]; !ok {
			t.ModelUsage[model] = &ModelUsage{}
		}
		t.ModelUsage[model].InputTokens += usage.InputTokens
		t.ModelUsage[model].OutputTokens += usage.OutputTokens
		t.ModelUsage[model].CacheCreation += usage.CacheCreation
		t.ModelUsage[model].CacheRead += usage.CacheRead
		t.ModelUsage[model].APICalls += usage.APICalls
	}
}

// GetAgentBreakdown gets sub-agent usage breakdown
func (t *UsageTracker) GetAgentBreakdown() map[string]*AgentUsage {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]*AgentUsage)
	for k, v := range t.AgentUsage {
		result[k] = v
	}
	return result
}

// FormatUsageReport formats the usage report.
//
// 只展示 provider 上报的 token 计数，不展示本地估算费用：
// provider 不返回费用详情，本地价格表估算与真实账单不符（会误导用户）。
// token 数显示为精确带逗号格式，与 /context 的 formatIntWithComma 口径一致。
func (t *UsageTracker) FormatUsageReport() string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	report := "📊 Token Usage Report\n\n"
	report += "  Total:\n"
	report += formatUsageLine("  ", "Input", t.TotalInputTokens)
	report += formatUsageLine("  ", "Output", t.TotalOutputTokens)
	if t.TotalCacheCreation > 0 {
		report += formatUsageLine("  ", "Cache Create", t.TotalCacheCreation)
	}
	if t.TotalCacheRead > 0 {
		report += formatUsageLine("  ", "Cache Read", t.TotalCacheRead)
	}
	report += "\n"

	if len(t.ModelUsage) > 1 {
		report += "\n  By Model:\n"
		for model, usage := range t.ModelUsage {
			report += "    " + model + ": " +
				formatTokens(usage.InputTokens) + " in, " +
				formatTokens(usage.OutputTokens) + " out, " +
				formatInt(usage.APICalls) + " calls\n"
		}
	}

	if len(t.AgentUsage) > 0 {
		report += "\n  By Agent:\n"
		for agentID, usage := range t.AgentUsage {
			report += fmt.Sprintf("    %s: (%s in, %s out)\n",
				agentID, formatTokens(usage.InputTokens), formatTokens(usage.OutputTokens))
		}
	}

	return report
}

// Save persists usage data
func (t *UsageTracker) Save() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.saveLocked()
}

// saveLocked saves data while already holding the lock (caller must hold the lock)
//
// dirty-skip —— 无新记录不写盘。此前 30s autosave 与
// RecordUsage 的每 3 次落盘都是**无条件全量写**：tui 测试期 ~255 个泄漏 tracker
// ≈ 6000+ 次无谓 I/O（全量撞超时的动态拖累主力）；生产空闲期同样每 30s 重写
// 相同内容（≈2880 次/天）。语义改为：**Save 只负责持久化「磁盘上还没有的状态」**。
// CAS 领取标记保证并发 Save 只有一个写者；写失败还原标记等下一轮重试。
func (t *UsageTracker) saveLocked() error {
	if t.storePath == "" {
		return nil
	}

	if !t.dirty.CompareAndSwap(true, false) {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(t.storePath), 0755); err != nil {
		t.dirty.Store(true)
		return fmt.Errorf("failed to create directory: %w", err)
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		t.dirty.Store(true)
		return err
	}
	if err := util.AtomicWriteFile(t.storePath, data, 0o600); err != nil {
		t.dirty.Store(true)
		return err
	}
	return nil
}

// LoadUsageTracker loads usage data from file
//
// T1-B：兼容旧 settings.json 无 RecentModelUsage 字段的情况（nil → 初始化空 map）。
func LoadUsageTracker(storePath string) (*UsageTracker, error) {
	data, err := os.ReadFile(storePath)
	if err != nil {
		return NewUsageTracker(storePath), nil
	}

	tracker := &UsageTracker{storePath: storePath}
	if err := json.Unmarshal(data, tracker); err != nil {
		return NewUsageTracker(storePath), nil
	}
	if tracker.ModelUsage == nil {
		tracker.ModelUsage = make(map[string]*ModelUsage)
	}
	if tracker.AgentUsage == nil {
		tracker.AgentUsage = make(map[string]*AgentUsage)
	}
	// T1-B：旧 settings.json 无 RecentModelUsage 字段时初始化空 map
	if tracker.RecentModelUsage == nil {
		tracker.RecentModelUsage = make(map[string]*ModelUsage)
	}
	return tracker, nil
}

// Reset resets usage data
//
// T1-B：同步重置 RecentModelUsage（避免脏数据残留）。
func (t *UsageTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dirty.Store(true) // 清零也是状态变化，须落盘
	t.TotalInputTokens = 0
	t.TotalOutputTokens = 0
	t.TotalCacheCreation = 0
	t.TotalCacheRead = 0
	t.ModelUsage = make(map[string]*ModelUsage)
	t.RecentModelUsage = make(map[string]*ModelUsage)
	t.TotalAPIDurationMs = 0
	t.TotalToolDurationMs = 0
	t.AgentUsage = nil
}

// GetRecentModelUsage 获取指定 model 最近一次记录的 usage（供 TokenSource.PostCallActual 调用）。
//
// 返回 (usage, ok)，ok=false 表示该 model 从未记录过（吸纳优化 3）。
// 并发安全：持 RLock 读取 RecentModelUsage[model]。
//
// 与 ModelUsage 的累计计数区别：
// - ModelUsage[model]：累计 InputTokens/OutputTokens（多次调用累加）
// - RecentModelUsage[model]：最近一次 InputTokens/OutputTokens（覆盖写入）
//
// T1-B新增方法。
func (t *UsageTracker) GetRecentModelUsage(model string) (UnifiedUsage, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	mu, ok := t.RecentModelUsage[model]
	if !ok || mu == nil {
		return UnifiedUsage{}, false
	}
	return UnifiedUsage{
		InputTokens:   mu.InputTokens,
		OutputTokens:  mu.OutputTokens,
		CacheCreation: mu.CacheCreation,
		CacheRead:     mu.CacheRead,
	}, true
}

// GetSnapshot gets a usage snapshot (thread-safe)
func (t *UsageTracker) GetSnapshot() (input, output, cacheCreation, cacheRead int) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.TotalInputTokens, t.TotalOutputTokens, t.TotalCacheCreation, t.TotalCacheRead
}

func formatUsageLine(prefix, label string, tokens int) string {
	return prefix + label + ": " + formatTokens(tokens) + "\n"
}

// formatTokens 精确值 + 千位逗号（12461 → "12,461"）。
// 与 /context 的 formatIntWithComma 口径一致：缩略值（12.5k）会让用户
// 误以为和 /context 的精确值"对不上"，实际是同一个数。
func formatTokens(n int) string {
	return formatIntWithCommas(n)
}

func formatIntWithCommas(n int) string {
	s := fmt.Sprintf("%d", n)
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func formatInt(n int) string {
	return fmt.Sprintf("%d", n)
}

// CacheHitRate calculates cache hit rate
// Hit rate = cache read tokens / (cache read + cache write) tokens
func (t *UsageTracker) CacheHitRate() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	total := t.TotalCacheRead + t.TotalCacheCreation
	if total == 0 {
		return 0
	}
	return float64(t.TotalCacheRead) / float64(total)
}

// StartAutoSave starts periodic auto-save at the given interval
// Only one auto-save goroutine runs at a time; calling again stops the previous
func (t *UsageTracker) StartAutoSave(interval time.Duration) {
	t.mu.Lock()
	if t.autoSaveRunning {
		close(t.stopAutoSave)
	}
	t.stopAutoSave = make(chan struct{})
	t.autoSaveRunning = true
	t.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = t.Save()
			case <-t.stopAutoSave:
				return
			}
		}
	}()
}

// Close performs graceful shutdown: stops auto-save and persists current state
func (t *UsageTracker) Close() error {
	t.mu.Lock()
	if t.autoSaveRunning {
		close(t.stopAutoSave)
		t.autoSaveRunning = false
	}
	t.mu.Unlock()
	return t.Save()
}
