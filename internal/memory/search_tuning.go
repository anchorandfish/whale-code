// search_tuning.go — P3 打分器六步的**唯一参数声明点**（开关 + 全部数值参数）。
//
// 方案依据：`记忆系统_实施方案与不偏航纪律_2026-10-08.md`
// - §5-4「参数集中到**单一声明点**，并用门断言"全仓只有一处定义"（0.2/0.3/0.5、τ、θ 都属于此类）」
// - §6 P3 六步「换 trigram（+ <3 字符兜底）→ 取消 FTS 硬门改并集 → RRF → 字段权重 → 分类型 τ → MMR」
//
// ★ 纪律（本文件的存在理由）：
// 1. 下列开关与数值参数**只许在此处定义**；`search.go` 及测试一律引用这些名字，不得再写字面值。
// 2. **默认 = 现状（全部关闭）** ⇒ 任何一步都能单独 A/B：关掉开关必须退回旧行为，
// 否则该步的判别器是"恒绿装饰"（本仓 `verify_infra_acceptance.py` 的同名戒律）。
// 3. 每步落地后在 `currentTuning`（本文件底部）**单行翻转**，那一行就是该步的行为边界。
package memory

import "time"

// Tuning 打分器开关与参数。零值 = 历史行为（P3 之前）。
type Tuning struct {
	// ── 第 1 步：中文召回 ────────────────────────────────────────────────
	// TrigramTokenizer: FTS5 建表是否带 `tokenize='trigram'`。
	// false = 历史行为：不指定 tokenize ⇒ unicode61 ⇒ 把**整串中文当一个 token**
	// （实测：查询「组织方式」对含该子串的正文 0 命中）⇒ 中文召回实质不存在。
	// true = trigram：任意 ≥3 rune 的**子串**可命中。
	TrigramTokenizer bool
	// SubstringFallback: 查询 rune 数 < TrigramMinRunes 时的兜底通道。
	// trigram 分词器对 <3 rune 的查询**天然无法命中**（实测 2 字中文 0 命中），
	// 故此时降级为 Go 侧子串扫描（候选此刻已全在内存里，无需再问数据库）。
	SubstringFallback bool
	// QueryTermOR: 查询侧是否把整句拆成 3 rune 词项 OR（第 1 步的另一半）。
	// false = 历史行为：双引号包裹整句 ⇒ FTS5 **短语查询** ⇒ 等价于"整句必须是文档的
	// 连续子串"。关键词查询能命中（本身就是子串），但**用户真实整句输入零召回**
	// （实测 10/10，见 p3_recall_bench_test.go 的 sentence 类）。
	// true = 拆词 OR（buildTermOrQuery）⇒ 词级召回，词序与整句连续性不再要求。
	QueryTermOR bool

	// ── 第 2 步：候选集 ────────────────────────────────────────────────
	// UnionCandidates: false = 历史行为「FTS 硬门」（未命中的候选不参与排序
	// ⇒ 一条极重要、极新但用词不同的记忆**永远进不来**）；true = 并集候选。
	UnionCandidates bool

	// ── 第 3 步：融合方式 ──────────────────────────────────────────────
	// RRF: false = 历史行为「原始分加权和」（吃 bm25 原始分，**无界**、随语料规模漂移）；
	// true = RRF 名次融合（只用名次 ⇒ 天然免疫量纲漂移）。
	RRF bool

	// ── 第 4 步：字段权重 ──────────────────────────────────────────────
	// FieldWeights: false = name/description/content 等权；true = 按 FieldWeight* 加权。
	FieldWeights bool

	// ── 第 5 步：分类型 τ ──────────────────────────────────────────────
	// TypeTau: false = 全类型共用 EbbinghausTauSearch；true = 按 TypeTauByType（偏好类衰减更慢）。
	TypeTau bool

	// ── 第 6 步：MMR 去重 ──────────────────────────────────────────────
	// MMRLambda: 近重复抑制强度（rel − λ·max sim）。<=0 = 关闭。
	MMRLambda float64
}

// ============================================================
// 参数（★ 唯一声明点，别处不得重复）
// ============================================================

// TrigramMinRunes trigram 分词器可命中的最小查询长度（rune 计）。
// SQLite trigram 按「连续 3 字符窗口」建索引 ⇒ <3 rune 的查询不可能命中（实测确认）。
const TrigramMinRunes = 3

// RRFK RRF 平滑常数。原文 Candela et al. (2009) 建议 60；本方案沿用。
const RRFK = 60.0

// MaxQueryTerms 查询侧拆词后的词项数上界（P3-1b）。防用户一次粘贴整篇文档把查询式撑爆；
// 正常长度提问（≤60 rune ⇒ ≤58 个窗口）永不触发。
const MaxQueryTerms = 64

// UnionPriorThreshold 第 2 步（并集候选）的**先验准入阈 θ**。
//
// 语义：未被词法通道命中的候选，只有当它的**先验分**（= 同样三项公式但 relevance 取 0，
// 即 0.2·recency + 0.3·importance/100）≥ θ 时，才被并进排序池。
//
// 为什么必须有 θ：没有它，「并集」就退化成「把全部未命中者灌进排序池」——
// 一条与查询毫不相干的普通记忆会挤占 top-K 槽位（本仓 `I-13.21` 同类语料下限问题的反面）。
//
// 取值依据（不是拍的）：默认 importance=50 的普通记忆先验 ≈ 0.2·recency + 0.15 ≈ 0.35
// ⇒ θ=0.4 恰好把「普通」挡在外面，而 importance=100 的高先验记忆 ≈ 0.5 能进。
// ⚠️ 该值**未用真实数据调过**（方案 §8 诚实边界）⇒ 属未调参数，改它前先看 P0 日志。
const UnionPriorThreshold = 0.4

// MaxCandidatesForRank 进入排序的候选上限（防大语料把 O(n log n) 排序与
// 内存 FTS5 建表拖长）。历史值 BM25CandidatesLimit=100，此处复用它作为同一概念的单点。
const MaxCandidatesForRank = BM25CandidatesLimit

// 三因子权重（Generative Agents @arxiv:2304.03442）。
// ★ 这两个用途共用同一组数：① 历史算法「0.2·recency + 0.3·importance + 0.5·relevance」；
// ② P3-3 起作为 RRF 的各通道权重（Σ = 1.0 ⇒ 融合分天然有界，见 I2）。
const (
	ScoreWeightRecency    = 0.2
	ScoreWeightImportance = 0.3
	ScoreWeightRelevance  = 0.5
)

// 字段权重（P3-4）。name 是最强信号（用户/AI 取的标题即语义压缩），
// description 次之，content 兜底。
const (
	FieldWeightName        = 20.0
	FieldWeightDescription = 5.0
	FieldWeightContent     = 1.0
)

// 分类型 recency 时间常数（P3-5）。语义：
// - 偏好/约定（user/feedback）：月级 ⇒ 两周不放权重也仍显著；
// - 项目事实（project/reference）：两周级 ⇒ 会缓慢退场；
// - 情景/过程（episodic 等其余类型）：天级 ⇒ 只服务当下。
const (
	TypeTauUserLike    = 30 * 24 * time.Hour
	TypeTauProjectLike = 14 * 24 * time.Hour
	TypeTauDefault     = EbbinghausTauSearch // 7 天（历史值，作为其余类型的兜底）
)

// ============================================================
// 生产默认（每步落地后在此**单行翻转**）
// ============================================================

// currentTuning 生产默认配置。
//
// ★ P3 进度（每落地一步改一行，这一行就是该步的行为边界）：
// - P3-1 中文召回：已开（`TrigramTokenizer` / `SubstringFallback`）。
// - P3-1b 整句召回：已开（`QueryTermOR`）。★ 这一步是**实测发现漏项**后才补的：
// P3-1 只让"关键词"能命中，"用户整句输入"仍零召回（10/10）⇒ 注入侧会恒空。
// - P3-2 并集候选：已开（`UnionCandidates`，先验准入阈 θ=`UnionPriorThreshold`）。
// - P3-3 RRF 名次融合：已开（`RRF`）。★ 这一步是 I2（量纲有界）转绿的原因。
// - P3-4 字段权重 / P3-5 分类型 τ / P3-6 MMR：**未开**。
var currentTuning = Tuning{
	TrigramTokenizer:  true, // P3-1 已落地
	SubstringFallback: true, // P3-1 已落地
	QueryTermOR:       true, // P3-1b 已落地（2026-10-08，实测驱动）
	UnionCandidates:   true, // P3-2 已落地
	RRF:               true, // P3-3 已落地
}

// CurrentTuning 返回生产默认配置的副本。
func CurrentTuning() Tuning { return currentTuning }

// SetCurrentTuning 覆盖生产默认配置（供装配层/测试显式切换；勿在并发测试里写入）。
func SetCurrentTuning(t Tuning) { currentTuning = t }

// resolveTuning 解析本次检索生效的配置：显式传参优先，否则取生产默认。
func resolveTuning(explicit *Tuning) Tuning {
	if explicit != nil {
		return *explicit
	}
	return currentTuning
}

// baseTuning 返回"全关"的历史行为配置（供 A/B 判别器当对照位用）。
func baseTuning() Tuning { return Tuning{} }
