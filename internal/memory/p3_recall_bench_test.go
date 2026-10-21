// p3_recall_bench_test.go — P3 召回率**实测**（回答「这套召回到底准不准」）。
//
// ## 为什么必须有这个文件
//
// 方案 §2.3 的原话：「**没有 ground truth，就没有『更好』**」。在此之前所有关于
// 「搜得到 / 搜不到」的说法都是定性描述（"查『组织方式』返回 0 条"）—— 换一次语料
// 结论就可能翻转，也无法回答"改了以后好了多少"。
//
// 本文件用一份**手工标注的小语料**给出可复现、可回归的数字：
//
//	42 条记忆（10 条主题 + 32 条填充）× 34 条查询，按**四类分开度量**：
//
// · 整句（sentence） ：用户真实打法 —— 一整句口语里含目标记忆的字面片段
// · 关键词（keyword） ：≥3 rune 的关键词，原样出现在目标记忆里
// · 短词（short） ：<3 rune（trigram 索引层天然做不到，专测兜底通道）
// · 改写（paraphrase）：同一件事换个说法，与目标**无任何 ≥3 rune 公共子串**
//
// ★ 为什么必须分开报、不能给一个平均数：
//
//	① 「整句」与「关键词」在**历史行为下的差距极大** —— 关键词常整好等于 unicode61
//
// 的一个分词而侥幸命中，整句则必然落空。混在一起报会掩盖这个事实。
//
//	② 「改写类」几乎是 0。把它摊进平均分会把真实边界糊成一个"还行"的分数。
//
// ## 三档对照（同一 ground truth，同一 TopK=5）
//
//	A. 生产默认 Tuning=nil（trigram + 兜底 + 并集 + RRF）
//	B. 历史行为 Tuning{}（全关：unicode61 + FTS 硬门 + 原始分加权和）
//	C. 旧注入兜底 「按最近更新取前 5 条」（= 接线前 config.fallbackSelection 的语义）
//
// ## 标注是**机器校验**的
//
// `TestP3_Recall_LabelSanity` 断言每条标注自身成立（整句/关键词必须有 ≥3 rune 重合、
// 改写类必须**没有**）—— 否则"召回高"会变成循环论证（把查询写短一点就更"字面"了）。
package memory

import (
	"context"
	"sort"
	"strings"
	"testing"
)

// ============================================================
// 标注语料
// ============================================================

// p3RecallDoc 一条记忆。内容取自本项目的**真实记忆主题**（不是随机词）：
// 随机词会让召回率虚高，因为它绕开了真实场景里的长句、标点与近义。
type p3RecallDoc struct {
	name, desc, body string
}

var p3RecallDocs = []p3RecallDoc{
	{"prefer-first-principles", "写代码的方式", "用户要求第一性原理，不缝缝补补地改代码"},
	{"ui-bilingual-copy", "界面文案风格", "界面文案英文为主、中文为辅，中文只做辅助层"},
	{"forbidden-vendor-names", "禁用产品名", "不允许出现竞品产品名称，展示文案与注释都要清"},
	{"build-binary-cmd", "重建命令", "重建二进制的命令是 go build -o whale ./cmd/whale"},
	{"handtest-window-freeze", "手测窗口", "手测窗口期间禁止改码与重建二进制"},
	{"memory-scope-key", "作用域键", "记忆作用域键用 cwd 的哈希，换仓库就换目录"},
	{"permission-modes", "权限档位", "权限收成 8 到 3 档模式"},
	{"tui-alt-screen", "TUI 备用屏", "TUI 使用备用屏，退出后整屏消失"},
	{"no-cost-estimate", "不算价钱", "不算价钱，只用 provider 报的 token"},
	{"ab-baseline-first", "A/B 基线", "改动前先做 A/B 基线，再判断是不是本次引入"},
}

// p3RecallQuery 一条标注查询。
type p3RecallQuery struct {
	q, want, kind string
}

var p3RecallQueries = []p3RecallQuery{
	// ── 整句：用户真实打法（一整句口语，含目标记忆的字面片段）
	{"你之前说过要第一性原理，不缝缝补补，对吧", "prefer-first-principles", "sentence"},
	{"界面文案应该做成英文为主吗", "ui-bilingual-copy", "sentence"},
	{"为什么不允许出现竞品产品名称", "forbidden-vendor-names", "sentence"},
	{"重建二进制的命令是什么来着", "build-binary-cmd", "sentence"},
	{"什么时候禁止改码", "handtest-window-freeze", "sentence"},
	{"记忆作用域键是怎么算的", "memory-scope-key", "sentence"},
	{"权限收成几档了", "permission-modes", "sentence"},
	{"TUI 为什么用备用屏", "tui-alt-screen", "sentence"},
	{"为什么不算价钱", "no-cost-estimate", "sentence"},
	{"要不要先做 A/B 基线", "ab-baseline-first", "sentence"},

	// ── 关键词（≥3 rune，原样出现在目标记忆里）
	{"第一性原理", "prefer-first-principles", "keyword"},
	{"中文为辅", "ui-bilingual-copy", "keyword"},
	{"竞品产品名称", "forbidden-vendor-names", "keyword"},
	{"重建二进制的命令", "build-binary-cmd", "keyword"},
	{"禁止改码", "handtest-window-freeze", "keyword"},
	{"作用域键", "memory-scope-key", "keyword"},
	{"权限收成", "permission-modes", "keyword"},
	{"备用屏", "tui-alt-screen", "keyword"},
	{"不算价钱", "no-cost-estimate", "keyword"},
	{"A/B 基线", "ab-baseline-first", "keyword"},
	{"缝缝补补", "prefer-first-principles", "keyword"},
	{"英文为主", "ui-bilingual-copy", "keyword"},
	{"go build", "build-binary-cmd", "keyword"},
	{"手测窗口", "handtest-window-freeze", "keyword"},
	{"cwd 的哈希", "memory-scope-key", "keyword"},
	{"整屏消失", "tui-alt-screen", "keyword"},
	{"provider 报的 token", "no-cost-estimate", "keyword"},
	{"本次引入", "ab-baseline-first", "keyword"},

	// ── 短词（<3 rune）：trigram 索引层就不存在对应窗口 ⇒ 只能靠 Go 侧兜底扫描
	{"备用", "tui-alt-screen", "short"},
	{"档位", "permission-modes", "short"},
	{"竞品", "forbidden-vendor-names", "short"},
	{"模式", "permission-modes", "short"},

	// ── 改写（同义换词，与目标无 ≥3 rune 公共子串）
	{"怎么重新编译这个程序", "build-binary-cmd", "paraphrase"},
	{"界面语言该用什么", "ui-bilingual-copy", "paraphrase"},
	{"为什么记忆会串仓库", "memory-scope-key", "paraphrase"},
	{"代码风格有什么讲究", "prefer-first-principles", "paraphrase"},
}

const (
	// p3RecallFillers 填充条数 —— 把语料撑到 42 条，使「随机命中」概率降到 ~12%，
	// 否则 10 条语料下 TopK=5 本身就有一半概率蒙对，测不出真实差异。
	p3RecallFillers = 32
	// p3RecallK 检索返回条数 = 注入侧 maxSelections（5）。
	p3RecallK = 5
)

// p3RecallKinds 报告与断言用的类别顺序。
var p3RecallKinds = []string{"sentence", "keyword", "short", "paraphrase"}

var p3RecallKindName = map[string]string{
	"sentence":   "整句(真实打法)",
	"keyword":    "关键词(>=3 rune)",
	"short":      "短词(<3 rune 兜底)",
	"paraphrase": "改写(换个说法)",
}

// p3RecallCorpus 搭语料：**先写填充、后写目标**。
//
// 顺序是刻意的：写目标在后 ⇒ 目标更新 ⇒ 「按最近更新取前 5」会取到较新的那几条目标，
// 使 C 档（旧兜底）不至于人为地拿 0 分。若反过来写，C 档恒 0，对照就失去意义。
func p3RecallCorpus(t *testing.T) *MergeLayer {
	t.Helper()
	_, ml := setupTestMergeLayer(t, "/test/work/p3-recall")
	p1WriteFillers(t, ml.userStore, p3RecallFillers)
	for _, d := range p3RecallDocs {
		writeEntryToStore(t, ml.userStore, d.name, d.desc, d.body, VerificationVerified)
	}
	return ml
}

// p3DocText 取目标的全部可检索文本（用于标注校验）。
func p3DocText(name string) string {
	for _, d := range p3RecallDocs {
		if d.name == name {
			return d.name + " " + d.desc + " " + d.body
		}
	}
	return ""
}

// p3CommonTrigram 判断 q 与 text 是否共享任意 **≥3 rune** 的连续子串。
// 这是"词法通道该不该命中"的判据 —— 与 FTS5 trigram 分词器的语义对齐。
func p3CommonTrigram(q, text string) bool {
	qr, tr := []rune(q), []rune(text)
	if len(qr) < 3 || len(tr) < 3 {
		return false
	}
	set := make(map[string]bool, len(tr))
	for i := 0; i+3 <= len(tr); i++ {
		set[string(tr[i:i+3])] = true
	}
	for i := 0; i+3 <= len(qr); i++ {
		if set[string(qr[i:i+3])] {
			return true
		}
	}
	return false
}

// ============================================================
// 度量
// ============================================================

// p3RecallMetrics 一档配置的度量结果（按 kind 分组）。
type p3RecallMetrics struct {
	label  string
	recall map[string]float64
	mrr    map[string]float64
	zero   map[string]int // 该类里"一条都没召回"的查询数
	total  map[string]int
	ranks  map[string]int // q → 命中名次（0 = 未命中）
}

// p3Eval 用给定的"取 top-K 名字"函数跑完所有查询。
// top 返回空 表示该查询**一条都没召回**（这正是 I1 那种"硬门返回空"的形态）。
func p3Eval(t *testing.T, label string, top func(q string) []string) p3RecallMetrics {
	t.Helper()
	m := p3RecallMetrics{
		label:  label,
		recall: map[string]float64{},
		mrr:    map[string]float64{},
		zero:   map[string]int{},
		total:  map[string]int{},
		ranks:  map[string]int{},
	}
	kindHit := map[string]int{}
	kindRR := map[string]float64{}

	for _, q := range p3RecallQueries {
		m.total[q.kind]++
		got := top(q.q)
		if len(got) == 0 {
			m.zero[q.kind]++
		}
		rank := 0
		for i, n := range got {
			if n == q.want {
				rank = i + 1
				break
			}
		}
		m.ranks[q.q] = rank
		if rank > 0 {
			kindHit[q.kind]++
			kindRR[q.kind] += 1.0 / float64(rank)
		}
	}
	for kind, n := range m.total {
		if n == 0 {
			continue
		}
		m.recall[kind] = float64(kindHit[kind]) / float64(n)
		m.mrr[kind] = kindRR[kind] / float64(n)
	}
	return m
}

// p3SearchTop 生产/历史配置的取数函数。
func p3SearchTop(t *testing.T, tuning *Tuning) func(string) []string {
	t.Helper()
	return func(q string) []string {
		res, err := SearchMemory(context.Background(), q, SearchOptions{TopK: p3RecallK, Tuning: tuning})
		if err != nil {
			t.Fatalf("SearchMemory(%q) 失败: %v", q, err)
		}
		return p3SearchNames(res)
	}
}

// p3RecencyTop 旧注入兜底的取数函数：「按 UpdatedAt 降序取前 K 条」，与查询无关。
//
// ★ 这正是接线前 `config.fallbackSelection` 的语义（manifest[:maxSelections]）。
// 保留它作为 C 档，是为了回答"**接线到底带来多少增量**"——只报新数字是自说自话。
func p3RecencyTop(t *testing.T, ml *MergeLayer, k int) func(string) []string {
	t.Helper()
	merged, err := ml.ListMerged()
	if err != nil {
		t.Fatalf("ListMerged 失败: %v", err)
	}
	sorted := append([]*MemoryEntry(nil), merged...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].UpdatedAt.After(sorted[j].UpdatedAt)
	})
	if len(sorted) > k {
		sorted = sorted[:k]
	}
	names := p3SearchNames(sorted)
	return func(string) []string { return names }
}

// p3PrintReport 打印人类可读的对照表。
func p3PrintReport(t *testing.T, ms []p3RecallMetrics) {
	t.Helper()
	t.Logf("=== P3 召回率实测（语料 %d 条 = 目标 %d + 填充 %d，TopK=%d，查询 %d 条）===",
		len(p3RecallDocs)+p3RecallFillers, len(p3RecallDocs), p3RecallFillers, p3RecallK, len(p3RecallQueries))
	for _, kind := range p3RecallKinds {
		t.Logf("── %s ──", p3RecallKindName[kind])
		for _, m := range ms {
			if m.total[kind] == 0 {
				continue
			}
			hit := int(m.recall[kind]*float64(m.total[kind]) + 0.5)
			t.Logf("   %-16s Recall@%d = %.3f (%d/%d) · MRR = %.3f · 零召回 %d",
				m.label, p3RecallK, m.recall[kind], hit, m.total[kind], m.mrr[kind], m.zero[kind])
		}
	}
	t.Logf("── 「整句」逐条名次明细（生产默认 1 = 第一顺位命中，0 = 未召回）──")
	for _, q := range p3RecallQueries {
		if q.kind != "sentence" {
			continue
		}
		t.Logf("   %-28s want=%-24s rank=%d", q.q, q.want, ms[0].ranks[q.q])
	}
}

// ============================================================
// 判别器
// ============================================================

// TestP3_Recall_LabelSanity 标注自检：标签必须**机器可验**，否则度量是循环论证。
func TestP3_Recall_LabelSanity(t *testing.T) {
	for _, q := range p3RecallQueries {
		text := p3DocText(q.want)
		if text == "" {
			t.Errorf("查询 %q 的 want=%q 不在语料里 —— 标注失效", q.q, q.want)
			continue
		}
		switch q.kind {
		case "sentence":
			if !p3CommonTrigram(q.q, text) {
				t.Errorf("标了 sentence 但与目标无 ≥3 rune 公共子串：q=%q want=%q —— "+
					"词法通道**不该**命中它，把它记成『整句命中』是虚报", q.q, q.want)
			}
		case "keyword":
			if runeLen(q.q) < TrigramMinRunes {
				t.Errorf("标了 keyword 但 rune 数 %d < %d：它走的是兜底通道，请改标 short", runeLen(q.q), TrigramMinRunes)
			}
			if !strings.Contains(text, q.q) {
				t.Errorf("标了 keyword 但不是子串：q=%q 未原样出现在 want=%q 里（"+
					"要么改查询、要么改标签 —— 否则『关键词召回高』是循环论证）", q.q, q.want)
			}
		case "short":
			if runeLen(q.q) >= TrigramMinRunes {
				t.Errorf("标了 short 但 rune 数 %d >= %d：它走的不是兜底通道，标注失效", runeLen(q.q), TrigramMinRunes)
			}
			if !strings.Contains(text, q.q) {
				t.Errorf("标了 short 但不是子串：q=%q want=%q", q.q, q.want)
			}
		case "paraphrase":
			if p3CommonTrigram(q.q, text) {
				t.Errorf("标了 paraphrase 但与目标共享 ≥3 rune 子串：q=%q want=%q —— "+
					"词法通道**本该**命中它，把它算进『改写类』会低估词法能力。请换一个说法。", q.q, q.want)
			}
		default:
			t.Errorf("未知 kind=%q（q=%q）", q.kind, q.q)
		}
	}
}

// TestP3_Recall_ProductionVsHistorical 主判别器：三档对照 + 断言。
//
// 断言全部来自**实测值**（见 内部工作回执），并留余量。
// 其中最硬的两条：
// - 生产：整句类**零召回 = 0**（I1 的"硬门返回空"不得回归）；
// - 历史：整句类零召回 ≥ 5（证明度量对开关敏感 ⇒ 本文件不是恒绿装饰）。
func TestP3_Recall_ProductionVsHistorical(t *testing.T) {
	ml := p3RecallCorpus(t)

	prod := p3Eval(t, "生产默认", p3SearchTop(t, nil))
	hist := p3Eval(t, "历史行为(全关)", p3SearchTop(t, &Tuning{}))
	base := p3Eval(t, "旧兜底(最近更新)", p3RecencyTop(t, ml, p3RecallK))

	p3PrintReport(t, []p3RecallMetrics{prod, hist, base})

	const (
		sentenceFloor    = 0.90 // 生产：整句类召回下限
		histSentenceCeil = 0.20 // 历史：整句类召回上限（证明可判别）
		histZeroFloor    = 5    // 历史：整句类"零召回"下限量
		keywordFloor     = 0.90 // 生产：关键词类召回下限
		shortFloor       = 0.75 // 生产：<3 rune 兜底下限
	)

	if prod.recall["sentence"] < sentenceFloor {
		t.Errorf("[字面能力] 生产整句类召回 %.3f < %.2f —— 词法通道能力下降（I1 回归？）",
			prod.recall["sentence"], sentenceFloor)
	}
	if prod.zero["sentence"] != 0 {
		t.Errorf("[I1 硬门] 整句类有 %d 条查询零召回（『无命中即返回空』回归）", prod.zero["sentence"])
	}
	if hist.recall["sentence"] > histSentenceCeil {
		t.Errorf("[可判别性] 历史行为整句类召回 %.3f > %.2f —— 若历史行为也能召回，"+
			"说明这批查询在 unicode61 下侥幸命中，本文件的判别器无效，先换查询再谈结论",
			hist.recall["sentence"], histSentenceCeil)
	}
	if hist.zero["sentence"] < histZeroFloor {
		t.Errorf("[可判别性] 历史行为整句类零召回只有 %d 条（期望 ≥%d）：判据没有区分度",
			hist.zero["sentence"], histZeroFloor)
	}
	if prod.recall["keyword"] < keywordFloor {
		t.Errorf("[字面能力] 生产关键词类召回 %.3f < %.2f", prod.recall["keyword"], keywordFloor)
	}
	if prod.recall["short"] < shortFloor {
		t.Errorf("[兜底通道] 短词类召回 %.3f < %.2f —— <3 rune 兜底失效", prod.recall["short"], shortFloor)
	}

	// 改写类：**只记录、不断言**。它是已知语义上限（词法检索无法跨过），
	// 未来引入向量检索后这个数会上升；断言"它必须低"会反过来惩罚改进。
	t.Logf("★ 改写类（已知边界）：生产 %.3f · 历史 %.3f · 旧兜底 %.3f —— "+
		"词法检索只能命中字面重合；跨过它必须引入语义(向量)检索（方案 §7 后续线，不在 P3 内）",
		prod.recall["paraphrase"], hist.recall["paraphrase"], base.recall["paraphrase"])
	t.Logf("★ 接线增益（整句类）：生产 %.3f − 旧兜底(最近更新) %.3f = %+.3f",
		prod.recall["sentence"], base.recall["sentence"], prod.recall["sentence"]-base.recall["sentence"])
}
