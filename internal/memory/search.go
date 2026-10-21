// search.go — H11/N27 Phase B B3 SearchMemory hybrid 检索层。
// 三因子；落地：0.2·recency + 0.3·importance + 0.5·relevance，recency=exp(-Δt/7天)，importance=effectiveImportance/100，relevance=FTS5 BM25 分数
// Windows 不可用；落地：禁用 mattn/go-sqlite3（CGO），用 modernc.org/sqlite 纯 Go 驱动（Windows 兼容，零 CGO）
// REF-外部-GenerativeAgents@arxiv:2304.03442-三因子检索；借鉴逻辑：recency + importance + relevance 三因子加权；落地：H11/N27 SearchMemory 三因子
// Q6-SearchMemory SQLite FTS5；落地：modernc.org/sqlite + FTS5 虚拟表，索引 memory frontmatter + body
// 5-effectiveImportance 不改原 entry；落地：降权仅在排序时生效，不修改原 entry.Importance（避免共享指针被永久降权）
// SNAPSHOT: SearchMemory={BM25(SQLite FTS5 modernc.org/sqlite无CGO)+三因子(0.2·recency+0.3·importance+0.5·relevance)+top-10+unverified_procedural降权(×0.5, 不改原entry); 协同ADR-089={Phase1并存二选一, Phase2+融合top-10→AI选top-5}; driver=modernc.org/sqlite(纯Go零CGO, PIT-004防护)}

package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（PIT-004 防护：无 CGO，Windows 兼容）
)

// SearchMemoryTopK 默认 top-K 返回数量（Phase 1：纯 BM25 三因子，无 AI 融合）。
// Phase 2+： AI 从 top-10 候选中选 top-5。
const SearchMemoryTopK = 10

// BM25CandidatesLimit BM25 候选上限（FTS5 LIMIT 较大，再由三因子重排序取 top-10）。
// 避免大语料下返回过多候选拖慢排序。
const BM25CandidatesLimit = 100

// EbbinghausTauSearch 三因子检索中 recency 衰减时间常数（与 forgetting.go EbbinghausTau 一致，7 天）。
const EbbinghausTauSearch = 7 * 24 * time.Hour

// ProceduralDowngradeFactor unverified + procedural 记忆的降权因子（×0.5）。
// 防护小模型错误流程被当作已验证事实使用（审查报告优化 5）。
const ProceduralDowngradeFactor = 0.5

// ScoreWeight* 三因子权重已迁至 search_tuning.go（P3 的唯一参数声明点）。

// MaxImportance importance 取值上限（用于归一化到 0.0-1.0）。
const MaxImportance = 100

// SearchOptions 检索选项。
type SearchOptions struct {
	ScopeFilter []string // 限定 scope（user/project），空=全部
	TypeFilter  []string // 限定 MemoryType，空=全部
	TopK        int      // 默认 SearchMemoryTopK=10，Phase 2+ 改 5

	// Tuning 打分器开关与参数（P3 六步，见 search_tuning.go）。
	// nil ⇒ 用生产默认 currentTuning；判别器传 &Tuning{}（全关）取历史行为做 A/B 对照。
	Tuning *Tuning
}

// SearchMemory hybrid 检索：BM25 三因子 + AI top-5 融合（Phase 2+）。
// 对齐 v0.4 §7.4 + Q6：SQLite FTS5（modernc.org/sqlite 纯 Go，无 CGO，Windows 兼容）。
//
// Phase 1：纯 BM25 三因子 + top-10。
// Phase 2+：返回 top-10 候选， AI 从中选 top-5。
//
// 排序公式（Generative Agents @arxiv:2304.03442 三因子加权）：
//
//	score = 0.2·recency + 0.3·importance + 0.5·relevance
//	recency = exp(-Δt / τ)，τ=7 天
//	importance = effectiveImportance(entry) / 100
//	relevance = SQLite FTS5 bm25() 分数（取负值，越大越相关）
//
// 降权规则（审查报告优化 5：不改原 entry）：
// - verification=unverified && trust_level=procedural → effectiveImportance = Importance × 0.5
// - 降权仅在排序时生效，原 entry.Importance 不被修改
//
// opts.TopK <= 0 时用 SearchMemoryTopK=10 默认值。
// SearchMemory 用**包级全局** MergeLayer 检索（H11/N27 Phase B 单例，B6 命令注册时注入）。
//
// 读路径适配器（memorybridge）请改用 SearchMemoryWithLayer —— 理由见其说明。
func SearchMemory(ctx context.Context, query string, opts SearchOptions) ([]*MemoryEntry, error) {
	ml := globalMergeLayer
	if ml == nil {
		return nil, errors.New("global MergeLayer not initialized, call SetGlobalMergeLayer first")
	}
	return SearchMemoryWithLayer(ctx, ml, query, opts)
}

// SearchMemoryWithLayer 与 SearchMemory 同语义，但**显式接收合并层**，不读包级全局。
//
// 为什么必须提供这一条：memorybridge 自己持有两份 store（user + project），它的 `List`
// 走**自己的** merge 层。若它的 `Rank` 走 `globalMergeLayer`，同一条读路径就会有
// **两个数据来源**（全局层有没有被注入、指向哪个 workDir，都能让 List 与 Rank 看到不同
// 的记忆集）—— 正是本仓 G5–G7 / 那类"同一语义两处实现"的复发形态。
// 显式传层把这条隐患从"靠约定"变成"靠类型"。
func SearchMemoryWithLayer(ctx context.Context, ml *MergeLayer, query string, opts SearchOptions) ([]*MemoryEntry, error) {
	if ml == nil {
		return nil, errors.New("SearchMemoryWithLayer: nil MergeLayer")
	}
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("SearchMemory: query cannot be empty")
	}

	topK := opts.TopK
	if topK <= 0 {
		topK = SearchMemoryTopK
	}

	// 收集候选 entry（从 user + project scope 加载，支持 scope/type filter）
	candidates, err := collectCandidates(ctx, ml, opts)
	if err != nil {
		return nil, fmt.Errorf("collect candidates: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// P3：本次检索生效的打分器配置（显式传参优先，否则取生产默认）。
	tuning := resolveTuning(opts.Tuning)

	// 用词法通道（SQLite FTS5 内存索引 / 短查询时降级为 Go 侧子串扫描）算相关度分。
	// 历史语义：只返回词法命中的候选，未命中的不参与排序（硬门，P3-2 起可关）。
	matchedCandidates, bm25Scores, err := computeBM25Scores(candidates, query, tuning)
	if err != nil {
		return nil, fmt.Errorf("compute BM25 scores: %w", err)
	}
	if len(matchedCandidates) == 0 {
		// 无 FTS5 匹配 → 返回空切片（不报错）
		return nil, nil
	}

	// P3-2：并集候选 —— 把「未词法命中、但先验足够强」的记忆并进排序池。
	//
	// ★ 位置很关键：必须在上面 `len(matchedCandidates)==0` 的早退**之后**。
	// 那次早退是 SearchMemory 的**对外契约**（无词法命中 ⇒ 空），compact.go 的
	// 主题链接依赖它 —— 若在这里也并集，一次没有语义关联的压缩会把任务链到
	// 一堆无关的近期记忆上。所以并集只在"已有词法锚点"时放宽。
	pool := matchedCandidates
	if tuning.UnionCandidates {
		pool = unionWithPrior(candidates, matchedCandidates)
	}

	// 词法分（以 entry 指针索引）：只有**词法命中**的条目在这张表里。
	// 这张表同时承担两个职责：① 融合时 relevance 通道的原始分；
	// ② RRF 时"该条是否占词法通道名次"的判据 —— 未命中者不占（这正是并集能与词法共存的原因）。
	lexScores := make(map[*MemoryEntry]float64, len(matchedCandidates))
	for i, e := range matchedCandidates {
		lexScores[e] = bm25Scores[i]
	}

	// 融合打分行（P3-3）：RRF 开 = 名次融合（有界）；关 = 历史「原始分加权和」。
	fused := fuseScores(pool, lexScores, tuning)

	// 排序
	type scoredEntry struct {
		entry *MemoryEntry
		score float64
	}
	scored := make([]scoredEntry, 0, len(pool))
	for i, e := range pool {
		scored = append(scored, scoredEntry{entry: e, score: fused[i]})
	}

	// 按分数降序排序
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// 取 top-K
	if len(scored) > topK {
		scored = scored[:topK]
	}

	result := make([]*MemoryEntry, 0, len(scored))
	for _, s := range scored {
		result = append(result, s.entry)
	}
	return result, nil
}

// effectiveImportance 计算排序时的有效重要性（unverified procedural 降权 ×0.5）。
// 不修改原 entry，仅返回局部计算结果（审查报告优化 5：避免共享指针被永久降权）。
//
// 降权规则：
// - verification=unverified && trust_level=procedural → Importance × ProceduralDowngradeFactor(0.5)
// - 其他情况返回原 Importance（不降权）
func effectiveImportance(entry *MemoryEntry) int {
	if entry == nil {
		return 0
	}
	if entry.DefaultVerification() == VerificationUnverified && entry.TrustLevel == TrustProcedural {
		// 降权 ×0.5（整数除法向下取整）
		return entry.Importance / 2
	}
	return entry.Importance
}

// calculateScore 三因子加权（Generative Agents @arxiv:2304.03442）。
// effectiveImportance 由调用方传入（含降权逻辑），不直接读 entry.Importance。
//
// 公式：
//
//	score = ScoreWeightRecency·recency + ScoreWeightImportance·importance + ScoreWeightRelevance·relevance
//	recency = exp(-Δt / τ)，τ=7 天，Δt = time.Since(entry.LastAccessedAt)
//	importance = effectiveImportance / MaxImportance(100)，归一化到 0.0-1.0
//	relevance = bm25 分数（取负值，越大越相关），由调用方传入
//
// 注：LastAccessedAt 为零值时（从未访问），recency 用 CreatedAt 替代（首次访问视为刚创建）。
// recencyOf recency = exp(-Δt / τ)，τ 由调用方给（第 5 步起按类型不同）。
//
// Δt = now - LastAccessedAt；LastAccessedAt 为零值（从未访问过）时用 CreatedAt 替代
// （首次访问视为刚创建）。
//
// ★ 抽成独立函数是**刻意**的：「总分」「先验分」「RRF 的 recency 通道」三者必须共用
// 同一个 recency 定义，否则第 5 步（分类型 τ）会变成"三处各写一遍 τ"——
// 正是本仓 （命令双实现）/ G5–G7（工具面 SSOT）反复踩的坑。
func recencyOf(entry *MemoryEntry, tau time.Duration) float64 {
	refTime := entry.LastAccessedAt
	if refTime.IsZero() {
		refTime = entry.CreatedAt
	}
	return math.Exp(-float64(time.Since(refTime)) / float64(tau))
}

func calculateScore(entry *MemoryEntry, query string, effectiveImportance int, relevance float64) float64 {
	if entry == nil {
		return 0
	}

	// recency = exp(-Δt / τ)，τ = EbbinghausTauSearch（7 天）
	recency := recencyOf(entry, EbbinghausTauSearch)

	// importance = effectiveImportance / 100
	importance := float64(effectiveImportance) / float64(MaxImportance)

	// score = 0.2·recency + 0.3·importance + 0.5·relevance
	return ScoreWeightRecency*recency +
		ScoreWeightImportance*importance +
		ScoreWeightRelevance*relevance
}

// rankBy 返回「entry → 名次（0 起，0 = 最好）」，按 key 降序。
//
// 用 SliceStable 排序并按 pool 的顺序打平：key 相同时名次由 pool 原序决定
// ⇒ 同一输入必得同一输出（方案 §3 I6 可复现）。
func rankBy(pool []*MemoryEntry, key func(*MemoryEntry) float64) map[*MemoryEntry]int {
	idx := make([]int, len(pool))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return key(pool[idx[a]]) > key(pool[idx[b]])
	})
	out := make(map[*MemoryEntry]int, len(pool))
	for r, i := range idx {
		out[pool[i]] = r
	}
	return out
}

// fuseScores 计算排序池中每条记忆的融合分（越大越相关）。返回与 pool 等长同序。
//
// RRF=false：历史行为 —— `calculateScore` 的原始分加权和。relevance = -bm25 是**原始分**，
// 量纲随语料规模漂（实测 N=11→1001 时 bm25 −2.3→−7.7）⇒ 总分可越出 [0,1]（I2 红）。
//
// RRF=true ：名次融合（Reciprocal Rank Fusion，Candela et al. 2009）
//
// score = Σ_ch w_ch / (RRFK + rank_ch)，rank 从 0 起
//
// 只用**名次**不用原始分值 ⇒ 天然免疫量纲漂移，且
// score ∈ (0, (ScoreWeightRecency+importance+relevance)/RRFK] = (0, 1/60] ⊂ (0,1]
// ⇒ I2（量纲有界）成为**结构性**成立，而不是靠"记得归一化"。
//
// 三条通道与三因子同源（权重仍用 ScoreWeight*，见 search_tuning.go 的"单一声明点"纪律）：
//
//	relevance ← 词法名次（bm25 升序）。**未命中的条目不占该通道名次**（这是并集能与词法共存的原因）
//	recency ← recency 降序
//	importance ← effectiveImportance 降序
func fuseScores(pool []*MemoryEntry, lexScores map[*MemoryEntry]float64, tuning Tuning) []float64 {
	out := make([]float64, len(pool))
	if !tuning.RRF {
		for i, e := range pool {
			var relevance float64
			if s, ok := lexScores[e]; ok {
				relevance = -s
			}
			out[i] = calculateScore(e, "", effectiveImportance(e), relevance)
		}
		return out
	}

	// 词法通道名次：只含命中者，bm25 升序（值越小越相关）
	lexRank := make(map[*MemoryEntry]int, len(lexScores))
	{
		matched := make([]*MemoryEntry, 0, len(lexScores))
		for _, e := range pool {
			if _, ok := lexScores[e]; ok {
				matched = append(matched, e)
			}
		}
		sort.SliceStable(matched, func(i, j int) bool {
			return lexScores[matched[i]] < lexScores[matched[j]]
		})
		for r, e := range matched {
			lexRank[e] = r
		}
	}

	recRank := rankBy(pool, func(e *MemoryEntry) float64 { return recencyOf(e, EbbinghausTauSearch) })
	impRank := rankBy(pool, func(e *MemoryEntry) float64 { return float64(effectiveImportance(e)) })

	for i, e := range pool {
		var s float64
		if r, ok := lexRank[e]; ok {
			s += ScoreWeightRelevance / (RRFK + float64(r))
		}
		s += ScoreWeightRecency / (RRFK + float64(recRank[e]))
		s += ScoreWeightImportance / (RRFK + float64(impRank[e]))
		out[i] = s
	}
	return out
}

// priorScore 计算一条记忆的**先验分**：三因子公式里 relevance 取 0（= 无词法信号）。
//
// ★ 为什么不另写一个公式：本仓 “（命令双实现）/ `G5–G7`（工具面 SSOT）的教训是
// 「同一语义两处实现 ⇒ 必然漂移」。先验与总分共用同一组权重与同一个 recency 定义，
// 第 5 步（分类型 τ）改一处即两处同时生效。
func priorScore(entry *MemoryEntry) float64 {
	return calculateScore(entry, "", effectiveImportance(entry), 0)
}

// unionWithPrior 第 2 步（并集候选）：matched ∪ {未命中且先验 ≥ UnionPriorThreshold}。
//
// 只负责**组池**，不打分 —— 打分统一收在 `fuseScores`。这样「谁进池」与「怎么排」
// 是两个可分别开关的决策，第 3 步换融合方式时不需要动这里。
//
// 上界：pool 总长不超过 MaxCandidatesForRank（与"进入排序的候选上限"同一约束），
// 避免大语料下排序与内存 FTS5 建表被拖长。
func unionWithPrior(all, matched []*MemoryEntry) []*MemoryEntry {
	pool := make([]*MemoryEntry, len(matched), MaxCandidatesForRank)
	copy(pool, matched)

	seen := make(map[*MemoryEntry]bool, len(matched))
	for _, e := range matched {
		seen[e] = true
	}

	// 未命中者按先验降序。用 SliceStable：先验相同时保持候选原序 ⇒ 结果可复现（方案 §3 I6）。
	rest := make([]*MemoryEntry, 0, len(all))
	for _, e := range all {
		if e == nil || seen[e] {
			continue
		}
		rest = append(rest, e)
	}
	sort.SliceStable(rest, func(i, j int) bool {
		return priorScore(rest[i]) > priorScore(rest[j])
	})

	for _, e := range rest {
		if len(pool) >= MaxCandidatesForRank {
			break
		}
		if priorScore(e) < UnionPriorThreshold {
			// 已按先验降序 ⇒ 后续只会更低，可直接停（θ 是准入阈，不是排序键）。
			break
		}
		pool = append(pool, e)
	}
	return pool
}

// collectCandidates 从给定合并层的 user + project scope 收集候选 entry，应用 scope/type filter。
// 不走 SQLite（仅扫描 .md 文件），FTS5 索引在 computeBM25Scores 中临时构建。
//
// ml 由调用方显式传入（不再读包级 globalMergeLayer）—— 见 SearchMemoryWithLayer 的说明。
func collectCandidates(ctx context.Context, ml *MergeLayer, opts SearchOptions) ([]*MemoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context canceled: %w", err)
	}
	if ml == nil {
		return nil, errors.New("collectCandidates: nil MergeLayer")
	}

	merged, err := ml.ListMerged()
	if err != nil {
		return nil, fmt.Errorf("ListMerged: %w", err)
	}

	// 应用 scope filter（merged entry 没有显式 scope 字段，需通过 FilePath 推断）
	// 简化：scope filter 留空时全部通过；指定时按文件路径包含 _user/ 或 hash 判断
	//
	// P2：有效性过滤 —— 作废/过期记忆不得进入检索候选。
	// 判定唯一权威是 memory 包 LiveAt（liveness.go），此处只调用，不复制条件。
	//
	// ★ 为什么过滤放在这里而不是 ListMerged：
	// invalidate.go 的 transitionVerificationOnFile 需要用 ListMerged **按名字找回**
	// 已被 contradicted 的条目来做状态流转（invalidate_test.go:250 锁定该行为）。
	// ListMerged 是「原始视图」（管理面），过滤必须发生在消费面（检索/注入）。
	now := time.Now()
	var filtered []*MemoryEntry
	for _, e := range merged {
		if !matchScopeFilter(e, opts.ScopeFilter) {
			continue
		}
		if !matchTypeFilter(e, opts.TypeFilter) {
			continue
		}
		if !e.LiveAt(now) {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered, nil
}

// matchScopeFilter 检查 entry 是否匹配 scope 过滤器。
// 空 filter = 全部通过；非空时按 FilePath 包含 _user/ 或 hash 判断。
func matchScopeFilter(entry *MemoryEntry, scopeFilter []string) bool {
	if len(scopeFilter) == 0 {
		return true
	}
	for _, scope := range scopeFilter {
		switch scope {
		case "user":
			if strings.Contains(entry.FilePath, filepath.Join("memory", "_user")) {
				return true
			}
		case "project":
			// project scope 路径含 hash（16 位 hex），不含 _user
			if !strings.Contains(entry.FilePath, filepath.Join("memory", "_user")) &&
				strings.Contains(entry.FilePath, "memory") {
				return true
			}
		}
	}
	return false
}

// matchTypeFilter 检查 entry 是否匹配 MemoryType 过滤器。
func matchTypeFilter(entry *MemoryEntry, typeFilter []string) bool {
	if len(typeFilter) == 0 {
		return true
	}
	for _, t := range typeFilter {
		if string(entry.Type) == t {
			return true
		}
	}
	return false
}

// computeBM25Scores 用词法通道计算每个候选的相关度分。
//
// 返回：
// - matchedCandidates：词法命中的候选 entry 切片
// - scores：与 matchedCandidates 等长的分数（**负值，越小越相关** —— 沿用 bm25 符号约定）
// - 未命中的候选不返回（硬门；P3-2 起由 UnionCandidates 决定是否并集，见 search_tuning.go）
//
// P3-1两处改动，均由 tuning 开关控制（关掉即退回历史行为）：
// 1. `TrigramTokenizer` —— 建表带 `tokenize='trigram'`。历史行为（不指定 tokenize）用
// unicode61，它把**整串中文当一个 token** ⇒ 「组织方式」对含该子串的正文 **0 命中**
// （Go 侧实测：SQLite 3.53.3，unicode61=0 行 / trigram=1 行）。
// 2. `SubstringFallback` —— trigram 按「连续 3 字符窗口」建索引，对 <3 rune 的查询
// **天然无法命中**（实测 2 字中文 0 命中）⇒ 此时降级为 Go 侧子串扫描
// （候选此刻已全在内存里，不必再问数据库）。
//
// FTS5 虚拟表 schema（详细设计 §3.3）：
//
//	CREATE VIRTUAL TABLE memory_fts USING fts5(name, description, content, type, scope UNINDEXED)
//
// 注：scope 字段标 UNINDEXED，仅用于过滤不参与全文索引（避免 scope 名污染相关性）。
func computeBM25Scores(candidates []*MemoryEntry, query string, tuning Tuning) ([]*MemoryEntry, []float64, error) {
	if len(candidates) == 0 {
		return nil, nil, nil
	}

	// 短查询兜底通道：trigram 索引对 <3 rune 的查询不可能命中。
	if tuning.TrigramTokenizer && tuning.SubstringFallback && runeLen(query) < TrigramMinRunes {
		m, s := substringScores(candidates, query, tuning)
		return m, s, nil
	}

	// 内存 SQLite 数据库（每次 SearchMemory 临时构建，避免持久化索引同步问题）
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, nil, fmt.Errorf("open sqlite: %w", err)
	}
	defer db.Close()

	// 创建 FTS5 虚拟表（P3-1：分词器由 tuning 决定）
	ddl := `CREATE VIRTUAL TABLE memory_fts USING fts5(name, description, content, type, scope UNINDEXED)`
	if tuning.TrigramTokenizer {
		ddl = `CREATE VIRTUAL TABLE memory_fts USING fts5(name, description, content, type, scope UNINDEXED, tokenize='trigram')`
	}
	if _, err := db.Exec(ddl); err != nil {
		return nil, nil, fmt.Errorf("create FTS5 table: %w", err)
	}

	// 插入候选 entry
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("begin tx: %w", err)
	}
	for _, e := range candidates {
		_, err := tx.Exec(
			`INSERT INTO memory_fts (name, description, content, type, scope) VALUES (?, ?, ?, ?, ?)`,
			e.Name, e.Description, e.Content, string(e.Type), e.scopeForIndex(),
		)
		if err != nil {
			_ = tx.Rollback()
			return nil, nil, fmt.Errorf("insert FTS5 row: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit tx: %w", err)
	}

	// 查询 BM25 分数（FTS5 bm25() 返回负值，越小越相关）
	// 注：modernc.org/sqlite 支持 FTS5 bm25() 函数
	//
	// 查询式有两种构造（P3-1b 起由 QueryTermOR 决定）：
	//
	//	历史行为：`"整句"` —— FTS5 里这是**短语查询**，trigram 分词下等价于
	// 「整句必须作为连续子串出现在文档里」。关键词查询能命中，是因为
	// 关键词本身就是子串；**用户真实输入是句子**，句子几乎不可能是某条
	// 记忆的子串 ⇒ 实测零召回 10/10（见 p3_recall_bench_test.go 的
	// `sentence` 类）。即换掉分词器后，中文召回对真实输入**仍然不可用**。
	// （双引号包裹的原始用意保留：避免 "common-keyword" 被 FTS5 解析成
	// `common` 列的 `keyword` 查询。）
	//	QueryTermOR：拆成 3 rune 词项后 OR（见 buildTermOrQuery）—— 词级召回。
	// bm25 自带 IDF ⇒ 「共享更多/更稀有词项」的记忆自然排前。
	escapedQuery := `"` + strings.ReplaceAll(query, `"`, ``) + `"`
	if tuning.QueryTermOR {
		escapedQuery = buildTermOrQuery(query)
	}
	rows, err := db.Query(
		`SELECT name, bm25(memory_fts) AS score FROM memory_fts WHERE memory_fts MATCH ? ORDER BY score ASC LIMIT ?`,
		escapedQuery, BM25CandidatesLimit,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("query FTS5: %w", err)
	}
	defer rows.Close()

	// 构建 name → score 映射（仅含匹配的行）
	scoreMap := make(map[string]float64)
	for rows.Next() {
		var name string
		var score float64
		if err := rows.Scan(&name, &score); err != nil {
			return nil, nil, fmt.Errorf("scan FTS5 row: %w", err)
		}
		scoreMap[name] = score
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("rows iteration: %w", err)
	}

	// 按候选顺序构建匹配的 entry + 分数切片（未匹配的不返回）
	matched := make([]*MemoryEntry, 0, len(scoreMap))
	scores := make([]float64, 0, len(scoreMap))
	for _, e := range candidates {
		if s, ok := scoreMap[e.Name]; ok {
			matched = append(matched, e)
			scores = append(scores, s)
		}
	}
	return matched, scores, nil
}

// buildTermOrQuery 把**整句查询**拆成 3 rune 词项、用 OR 连接（P3-1b）。
//
// ## 为什么必须拆（这条是**实测**逼出来的，不是设计推演）
//
// FTS5 里 `"整句"` 是**短语查询**：要求整句作为连续序列出现。配上 trigram 分词器，
// 这等价于「整句必须是文档的连续子串」。于是：
//
//	查询 "组织方式" → 命中（它本身就是子串，P3-1 的判别器即此形态）
//	查询 "你之前说过要第一性原理，不缝缝补补，对吧" → **0 命中**（整句不是任何记忆的子串）
//
// 实测（p3_recall_bench_test.go，42 条语料 / 10 条整句查询）：**零召回 10/10**。
// ⇒ 只换分词器只解决了"关键词召回"，**真实整句输入仍然搜不到** —— 而注入路径喂进来的
// 恰恰是用户整句（`MemorySelector.Select(ctx, userInput)`）。若不拆词直接就线，
// 注入侧的本地排序会恒返回空、静默退回"按最近更新"，即又一次"修了没生效"。
//
// ## 拆法
//
// 中文没有词边界 ⇒ 用**连续 3 rune 窗口**（与 trigram 索引同构）；ASCII 单词同样被
// 窗口覆盖（"build" → bui/uil/ild），故一套拆法同时服务中英。
// 排序交给 bm25 的 IDF：罕见词项（"性原理"）权重高、通用词项（"为什"）权重低
// ⇒ 共享更多/更稀有词项的记忆排前，无需另写权重表。
//
// 跳过两类窗口：
// - 纯空白窗口（拆词时把空格当词项只会引入噪声）；
// - 含双引号的窗口（引号无法安全内嵌进 FTS5 的引号短语，剥离又会让长度 <3）。
//
// 上界 MaxQueryTerms：防超长输入（用户可能一次粘贴整篇文档）把查询式撑爆。
// 截断取**前 N 个**窗口——对正常长度的提问（≤60 rune）永不触发。
func buildTermOrQuery(query string) string {
	r := []rune(query)
	seen := make(map[string]bool, len(r))
	terms := make([]string, 0, len(r))
	for i := 0; i+TrigramMinRunes <= len(r); i++ {
		w := string(r[i : i+TrigramMinRunes])
		if strings.TrimSpace(w) == "" || strings.Contains(w, `"`) {
			continue
		}
		if seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, `"`+w+`"`)
		if len(terms) >= MaxQueryTerms {
			break
		}
	}
	if len(terms) == 0 {
		// 退化（<3 rune 或全空白）：此处不是兜底通道的入口 ——
		// <3 rune 在 computeBM25Scores 开头就被 SubstringFallback 接走了。
		// 走到这里只可能是"全空白"这类畸形输入，用短语查询保持历史行为。
		return `"` + strings.ReplaceAll(query, `"`, ``) + `"`
	}
	return strings.Join(terms, " OR ")
}

// scopeForIndex 返回 entry 的 scope 字符串（用于 FTS5 索引）。
// 通过 FilePath 推断：含 _user/ → "user"，否则 → "project"。
func (e *MemoryEntry) scopeForIndex() string {
	if strings.Contains(e.FilePath, filepath.Join("memory", "_user")) {
		return "user"
	}
	return "project"
}

// runeLen 返回字符串的 rune 长度。
// ★ 中文场景必须按 rune 计：trigram 的「3 个字符」也是 rune 语义，
// 用 len(byte) 会把一个汉字算成 3 ⇒ 2 字查询被误判为够长、从而绕过兜底、静默 0 命中。
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// fieldWeights 返回词法通道的字段权重。
// P3-4 关（默认）= name/description/content 等权（= 历史 `bm25(t)` 无参数行为）。
func fieldWeights(tuning Tuning) (name, desc, body float64) {
	if !tuning.FieldWeights {
		return 1, 1, 1
	}
	return FieldWeightName, FieldWeightDescription, FieldWeightContent
}

// substringScores 短查询兜底：在候选的 name/description/content 上做子串命中扫描。
//
// 为什么必须是"扫描"而不是"问数据库"：trigram 分词器按连续 3 字符窗口建索引，
// <3 rune 的查询在**索引层面**就不存在对应窗口 ⇒ MATCH 永远 0 行（实测确认），
// 不是参数问题。而候选 entry 此刻已经全在内存（collectCandidates 刚读盘），
// 直接扫比再建一张索引更省、也更可测。
//
// 返回分数沿用 bm25 的符号约定（**负值，越小越相关**）：
//
//	score = -(Σ_field W_field · count(query ∈ field))
//
// 无命中（score 为 0）的候选不返回，与 FTS 通道的"只回命中"语义一致。
func substringScores(candidates []*MemoryEntry, query string, tuning Tuning) ([]*MemoryEntry, []float64) {
	if query == "" {
		return nil, nil
	}
	wName, wDesc, wBody := fieldWeights(tuning)

	matched := make([]*MemoryEntry, 0, len(candidates))
	scores := make([]float64, 0, len(candidates))
	for _, e := range candidates {
		if e == nil {
			continue
		}
		hit := float64(strings.Count(e.Name, query))*wName +
			float64(strings.Count(e.Description, query))*wDesc +
			float64(strings.Count(e.Content, query))*wBody
		if hit <= 0 {
			continue
		}
		matched = append(matched, e)
		scores = append(scores, -hit) // 负值：越小越相关，与 bm25 同向
	}
	return matched, scores
}

// ============================================================
// 全局 MergeLayer 注入（B6 命令注册时设置，供 SearchMemory 使用）
// ============================================================

// globalMergeLayer 全局 MergeLayer 单例（H11/N27 Phase B 默认实例，B6 命令注册时注入）。
// SearchMemory 默认从此实例加载候选 entry。
// 测试可通过 SetGlobalMergeLayer 注入自定义实例。
var globalMergeLayer *MergeLayer

// SetGlobalMergeLayer 注入全局 MergeLayer（B6 命令注册时调用，测试也可注入）。
// 传入 nil 可清除全局实例。
func SetGlobalMergeLayer(ml *MergeLayer) {
	globalMergeLayer = ml
}

// GetGlobalMergeLayer 返回全局 MergeLayer（仅供测试或调试使用）。
func GetGlobalMergeLayer() *MergeLayer {
	return globalMergeLayer
}
