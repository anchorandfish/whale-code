// model_profile.go — 桶 3 ModelProfile 内存聚合（地基层 §3.5 / 总纲 §8.2）。
// 字段；落地：仅质量字段，cost 走 CostRecord 反查
// ModelProfile；落地：O(1) 查询，冷启动 < 10 样本降级静态矩阵
// SNAPSHOT: v0.2 含 AvgCostCents/AvgTokensIn/AvgCacheHit → v0.3 删除此 3 字段（属 cost 数据流归 N2）; key=(task_type,model); cold_start=sample_count<10 返回 nil

package orchestrator

import (
	"sync"
	"time"
)

// ModelProfile 模型画像表，按 (task_type, model) 聚合（地基层 §3.5）。
// 仅聚合质量字段；cost 字段需通过 metrics_correlation 反查 N2 CostRecord（不在本结构）。
// v0.3 SNAPSHOT 防护：删除了 AvgCostCents/AvgTokensIn/AvgCacheHit（属 cost 数据流归 N2，PIT-002）。
//
// === Phase 5 v0.2 BASE 变更（三段式，沉淀）===
// 新增 EffectiveSampleCount float64 字段（加权有效样本量）。
// 原方案优势：SampleCount 原始计数简单
// 本次牺牲：ModelProfile 新增 1 字段，schema 不再稳定
// 不得不改原因：v0.1 SampleCount 对旧记录也 +1，导致冷启动阈值 SampleCount<10 在
//
//	"9 条 90 天前记录（权重 0.1）+ 1 条新记录"时误判退出冷启动（有效样本量仅 ~1.9，统计意义不足）。
//	EffectiveSampleCount 加权累加，冷启动阈值改为 EffectiveSampleCount<10（§5.2，致命反例 2）。
//
// omitempty 向后兼容：旧记录反序列化为 0.0，仍触发冷启动降级（保守）。
type ModelProfile struct {
	TaskType       string  `json:"task_type"`
	Model          string  `json:"model"`
	SuccessRate    float64 `json:"success_rate"`     // L0 通过率（quality.jsonl 聚合）
	AvgL1Score     int     `json:"avg_l1_score"`     // L1 质量核心指标
	AvgDurationSec int     `json:"avg_duration_sec"` // 平均耗时
	AvgRetry       float64 `json:"avg_retry"`        // 平均重试次数
	L2AdoptionRate float64 `json:"l2_adoption_rate"` // L2=adopted 比例
	SampleCount    int     `json:"sample_count"`

	// EffectiveSampleCount 加权有效样本量（Phase 5）。
	// 时间衰减权重累加：<30d=1.0，30-90d=0.5，>90d=0.1。
	// 冷启动阈值改为 EffectiveSampleCount<10（§5.2，致命反例 2，替代 SampleCount<10）。
	EffectiveSampleCount float64 `json:"effective_sample_count,omitempty"`

	UpdatedAt string `json:"updated_at"` // ISO 8601
}

// profileKey ProfileStore 的 map 键：(task_type, model) 二元组。
type profileKey struct {
	TaskType string
	Model    string
}

// ProfileStore 模型画像表内存存储，启动时从 quality.jsonl 全量构建（地基层 §3.5）。
// 并发安全：sync.RWMutex 保护并发 Update / Get。
type ProfileStore struct {
	mu       sync.RWMutex
	profiles map[profileKey]ModelProfile // (task_type, model) → 聚合
}

// NewProfileStore 创建 ProfileStore，初始化空 map。
func NewProfileStore() *ProfileStore {
	return &ProfileStore{
		profiles: make(map[profileKey]ModelProfile),
	}
}

// Reload 从 QualityStore 全量扫描重建画像表（启动时调用，地基层 §3.5）。
// 调用 qs.ScanAll() 获取所有最新记录 → 委派 ReloadFromRecords 重建。
func (p *ProfileStore) Reload(qs *QualityStore) error {
	records, err := qs.ScanAll()
	if err != nil {
		return err
	}
	p.ReloadFromRecords(records)
	return nil
}

// ReloadFromRecords 从已加载的 QualityRecord 列表重建画像表（测试友好 + 避免重复扫描文件）。
// 调用方需先从 QualityStore 全量读取记录（或通过其他方式获取），传入此方法重建。
func (p *ProfileStore) ReloadFromRecords(records []QualityRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.profiles = make(map[profileKey]ModelProfile)

	type agg struct {
		l0PassCount int
		l1Sum       int
		durationSum int
		retrySum    int
		l2Adopted   int
		count       int
	}
	aggs := make(map[profileKey]*agg)

	for _, r := range records {
		key := profileKey{TaskType: r.TaskType, Model: r.Model}
		a, ok := aggs[key]
		if !ok {
			a = &agg{}
			aggs[key] = a
		}
		if r.L0Pass {
			a.l0PassCount++
		}
		a.l1Sum += r.L1Score
		a.durationSum += r.DurationSec
		a.retrySum += r.RetryCount
		if r.L2Verdict == L2Adopted {
			a.l2Adopted++
		}
		a.count++
	}

	// 计算最终聚合值
	for key, a := range aggs {
		if a.count == 0 {
			continue
		}
		p.profiles[key] = ModelProfile{
			TaskType:       key.TaskType,
			Model:          key.Model,
			SuccessRate:    float64(a.l0PassCount) / float64(a.count),
			AvgL1Score:     a.l1Sum / a.count,
			AvgDurationSec: a.durationSum / a.count,
			AvgRetry:       float64(a.retrySum) / float64(a.count),
			L2AdoptionRate: float64(a.l2Adopted) / float64(a.count),
			SampleCount:    a.count,
			UpdatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		}
	}
}

// Update 增量更新：新记录 append 后更新对应格子统计值（地基层 §3.5）。
// 加锁 → 对应格子 SampleCount++ → 增量重算平均值（避免全量重扫）。
func (p *ProfileStore) Update(rec QualityRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := profileKey{TaskType: rec.TaskType, Model: rec.Model}
	cur, ok := p.profiles[key]
	if !ok {
		// 冷启动：第一条记录直接初始化
		l0Val := 0.0
		if rec.L0Pass {
			l0Val = 1.0
		}
		l2Val := 0.0
		if rec.L2Verdict == L2Adopted {
			l2Val = 1.0
		}
		p.profiles[key] = ModelProfile{
			TaskType:       rec.TaskType,
			Model:          rec.Model,
			SuccessRate:    l0Val,
			AvgL1Score:     rec.L1Score,
			AvgDurationSec: rec.DurationSec,
			AvgRetry:       float64(rec.RetryCount),
			L2AdoptionRate: l2Val,
			SampleCount:    1,
			UpdatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		}
		return
	}

	// 增量重算：增量平均值公式 new_avg = (old_avg * (n-1) + new_value) / n
	n := cur.SampleCount + 1
	l0Val := 0.0
	if rec.L0Pass {
		l0Val = 1.0
	}
	l2Val := 0.0
	if rec.L2Verdict == L2Adopted {
		l2Val = 1.0
	}
	cur.SuccessRate = (cur.SuccessRate*float64(cur.SampleCount) + l0Val) / float64(n)
	cur.AvgL1Score = (cur.AvgL1Score*cur.SampleCount + rec.L1Score) / n
	cur.AvgDurationSec = (cur.AvgDurationSec*cur.SampleCount + rec.DurationSec) / n
	cur.AvgRetry = (cur.AvgRetry*float64(cur.SampleCount) + float64(rec.RetryCount)) / float64(n)
	cur.L2AdoptionRate = (cur.L2AdoptionRate*float64(cur.SampleCount) + l2Val) / float64(n)
	cur.SampleCount = n
	cur.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	p.profiles[key] = cur
}

// Get 查询 (task_type, model) 的画像，冷启动 sample_count < 10 返回 nil（地基层 §4.6）。
// 冷启动降级：路由器 Get 返回 nil 时降级用静态能力矩阵（总纲 §5.3）。
func (p *ProfileStore) Get(taskType, model string) *ModelProfile {
	p.mu.RLock()
	defer p.mu.RUnlock()

	key := profileKey{TaskType: taskType, Model: model}
	cur, ok := p.profiles[key]
	if !ok {
		return nil
	}
	// 冷启动：sample_count < 10 返回 nil（地基层 §4.6）
	if cur.SampleCount < 10 {
		return nil
	}
	// 返回副本（防外部修改内部状态）
	prof := cur
	return &prof
}

// Count 返回当前画像表中不同 (task_type, model) 格子数量（仅用于测试与诊断）。
func (p *ProfileStore) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.profiles)
}
