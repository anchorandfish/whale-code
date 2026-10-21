// p1_invariants_test.go — 记忆检索的 P1 不变式判别器。
//
// ★ 本文件的性质：**判别器先红**（生产路径 TDD 的"先红"一步）——
// 三条不变式在**现状**下**必须红**；它们**不随实现变化**：换分词器 / 换打分公式 /
// 换存储后端，这三条都得成立。所以它们是可以长期留着的"守门测试"。
//
// 为什么是这三条（对应 `记忆系统_实施方案与不偏航纪律_2026-10-08.md` 的 P1）：
//
//	I1 中文召回：FTS5 建表**未指定 tokenize** ⇒ 默认 unicode61 把一整串中文当一个 token
//
// （search.go:279）⇒ 中文**子串**查询 0 命中；而 computeBM25Scores 是**硬门**
// （未命中的候选直接不参与排序，search.go:99-102）⇒ 中文场景下"地板层"等于不存在。
//
//	I2 量纲有界：公式 score = 0.2·recency + 0.3·importance + 0.5·relevance（search.go:180-182），
//
// 三项**各自归一化到 [0,1]** ⇒ 总分必须 ∈[0,1]。
// P3-1 前 relevance = -bm25 **原始分**（无界，实测 1000+ 条语料下可达 7.7）⇒ 总分越界；
// **P3-3 起**生产融合改为 RRF 名次融合 ⇒ 上界 = Σ权重/RRFK = 1/60 ⇒ 结构性成立。
// 本判别器保留 A 位（关 RRF）作反例证明，B 位（开 RRF）断生产有界。
//
//	I3 作废不回流：verification=contradicted 是**终态**（用户 /memory invalidate 显式作废，
//
// entry.go:91-92）⇒ 不得再被注入。
// 现状 ListMerged 只"合并 + 按名去重"（merge_layer.go:44-87），
// calculateScore 只降权 `unverified && procedural`（search.go:144）⇒ 作废记忆照样进候选。
//
// ★ 装配纪律（**不许让被测对象消失**，本仓最容易出的假绿）：
// - I1/I3 走**生产入口 `SearchMemory`**：用真实 `MemoryStore` 写盘 → 读回 → 检索。
// **不**替换成内存假 store、**不**手工塞 entry 绕过序列化/路径/scope。
// - 语料 **≥6 条**（本仓 `I-13.21` 同类前置：语料太少会让硬门/量纲问题"看不见"）。
// - I2 直接喂 `computeBM25Scores` + `calculateScore` —— 这两个**就是** `SearchMemory`
// 内部实际调用的生产函数（search.go:95/113），**不构造替身**。
//
// ★ 落地时（P2/P3）不要"改判据让它变绿"：红的是**实现**，不是断言。若认为某条不变式
//
//	本身不对，改之前先改那份方案文档并说明理由。
package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// p1Names 取一组成员的 Name（失败信息里打印用）。
func p1Names(entries []*MemoryEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

// p1WriteFillers 写入 n 条**互不相关**的普通记忆（撑起语料规模，避免"被测对象消失"）。
// 内容与 query 无词面交集，确保它们不干扰目标条目的命中。
func p1WriteFillers(t *testing.T, store *MemoryStore, n int) {
	t.Helper()
	fillers := []string{
		"数据库迁移脚本放在 migrations 目录",
		"日志级别默认 info，调试时开 debug",
		"前端构建产物不进版本库",
		"周报每周五下午提交",
		"部署走 CI，不手工上线",
		"接口超时统一设 30 秒",
		"配置文件放 configs 目录",
	}
	for i := 0; i < n; i++ {
		writeEntryToStore(t, store,
			fmt.Sprintf("filler-%02d", i),
			"通用约定",
			fmt.Sprintf("%s（第 %d 条）", fillers[i%len(fillers)], i),
			VerificationVerified)
	}
}

// TestP1_I1_ChineseSubstringRecall 不变式 I1：**中文子串**查询必须能命中含它的记忆。
//
// 现状必红：SQLite FTS5 默认 unicode61 分词器把「记忆的组织方式」整串当一个 token，
// 查询「组织方式」无法命中；且 computeBM25Scores 是硬门 ⇒ SearchMemory 返回 0 条。
func TestP1_I1_ChineseSubstringRecall(t *testing.T) {
	_, ml := setupTestMergeLayer(t, "/test/work/p1-i1-cjk")

	// 目标：正文含「记忆**组织方式**」这一子串
	writeEntryToStore(t, ml.userStore, "memory-layout",
		"记忆分层", "我们讨论过记忆的组织方式与分层预算", VerificationVerified)
	// 语料 ≥6 条（1 目标 + 7 填充）
	p1WriteFillers(t, ml.userStore, 7)

	ctx := context.Background()
	results, err := SearchMemory(ctx, "组织方式", SearchOptions{})
	if err != nil {
		t.Fatalf("SearchMemory 失败: %v", err)
	}
	if findEntryByName(results, "memory-layout") == nil {
		t.Fatalf("不变式 I1 违反：查询「组织方式」未命中正文含该子串的记忆 memory-layout；"+
			"SearchMemory 返回 %d 条 %v\n"+
			"⇒ 现状必红（FTS5 未指定 tokenize ⇒ unicode61 把整串中文当一个 token，"+
			"且无命中即硬门返回空，search.go:279 / :99-102）",
			len(results), p1Names(results))
	}
}

// TestP1_I3_ContradictedMustNotBeInjected 不变式 I3：**作废（contradicted）**的记忆不得被检索注入。
//
// 现状必红：ListMerged 只做"合并 + 按名去重"（merge_layer.go:44-87），
// calculateScore 只降权 unverified+procedural（search.go:144）⇒ 作废条目照样进候选、照样返回。
func TestP1_I3_ContradictedMustNotBeInjected(t *testing.T) {
	_, ml := setupTestMergeLayer(t, "/test/work/p1-i3-contradicted")

	// 一条已被用户显式作废的记忆（终态）
	writeEntryToStore(t, ml.userStore, "obsolete-build",
		"构建命令", "构建请用 make build（此约定已作废）", VerificationContradicted)
	p1WriteFillers(t, ml.userStore, 7)

	ctx := context.Background()
	results, err := SearchMemory(ctx, "make build", SearchOptions{})
	if err != nil {
		t.Fatalf("SearchMemory 失败: %v", err)
	}
	if findEntryByName(results, "obsolete-build") != nil {
		t.Fatalf("不变式 I3 违反：作废记忆 obsolete-build 被检索返回（返回 %d 条 %v）\n"+
			"⇒ 现状必红（ListMerged 零有效性过滤；calculateScore 不排除 contradicted）",
			len(results), p1Names(results))
	}
}

// TestP1_I2_ScoreMustBeDimensionless 不变式 I2：三因子总分必须**有界**（∈[0,1]）。
//
// 依据：公式三项各自归一化到 [0,1]、权重和 = 0.2+0.3+0.5 = 1.0 ⇒ score ∈[0,1]（search.go:180-182）。
// 现状必红：relevance 直接取 -bm25 **原始分**（search.go:112），而 BM25 的量纲随语料规模漂移
// ⇒ 语料一大，0.5·relevance 单项就冲破 1.0，另外两项被压成噪声。
//
// ★ 本测试用**内存候选**喂 computeBM25Scores：这不改变被测对象 —— 该函数本身就是
// SearchMemory 内部调的同一个函数（search.go:95），且 FTS5 索引天然是临时内存库（:memory:）。
// 这样做的唯一目的是把 N 拉到 1000 量级而不用往磁盘写 1000 个文件。
func TestP1_I2_ScoreMustBeDimensionless(t *testing.T) {
	const n = 1000
	now := time.Now()

	cands := make([]*MemoryEntry, 0, n+1)
	for i := 0; i < n; i++ {
		cands = append(cands, &MemoryEntry{
			Name:        fmt.Sprintf("bulk-%04d", i),
			Description: "bulk corpus entry",
			// ★ 刻意**不含**查询词：若所有文档都含该词，IDF→0、bm25≈0 ⇒ 判别器自己失效（假绿）。
			Content:      "bulk corpus 正文 " + strings.Repeat("padding ", 8),
			Type:         MemoryTypeUser,
			CreatedAt:    now,
			Importance:   50,
			Verification: VerificationVerified,
		})
	}
	target := &MemoryEntry{
		Name:         "target",
		Description:  "target entry",
		Content:      "共享词 目标条目正文",
		Type:         MemoryTypeUser,
		CreatedAt:    now,
		Importance:   50,
		Verification: VerificationVerified,
	}
	cands = append(cands, target)

	// ★ P3-3：本判别器已按原注要求**改绑生产融合函数 `fuseScores`**。
	// 它现在同时是「守门测试」与「自带反例证明」：
	// A 位 = 关掉 RRF（历史原始分公式）⇒ 在 N=1001 下**必然越界**（证明量纲问题真实存在）；
	// B 位 = 生产默认（RRF 开）⇒ 全部 ∈[0,1]（I2 成立）。
	// 若不做 A 位，B 位绿了也说不清是"RRF 修好了"还是"语料没撑开量纲"。
	lexTuning := currentTuning
	matched, bm25Scores, err := computeBM25Scores(cands, "共享词", lexTuning)
	if err != nil {
		t.Fatalf("computeBM25Scores 失败: %v", err)
	}
	lexScores := make(map[*MemoryEntry]float64, len(matched))
	for i, e := range matched {
		lexScores[e] = bm25Scores[i]
	}
	if _, ok := lexScores[target]; !ok {
		t.Fatalf("目标条目未进入词法匹配集（matched=%d）—— 判别器自身失效，先查语料/查询词", len(matched))
	}

	idxOf := func(pool []*MemoryEntry, e *MemoryEntry) int {
		for i, x := range pool {
			if x == e {
				return i
			}
		}
		return -1
	}
	ti := idxOf(cands, target)

	// ── A 位（反例证明）：关掉 RRF ⇒ 历史「原始分加权和」在 N=1001 下越界 ──────────
	rawTuning := lexTuning
	rawTuning.RRF = false
	rawScores := fuseScores(cands, lexScores, rawTuning)
	rawTarget := rawScores[ti]
	t.Logf("A位/RRF关：N=%d · target 原始分加权和 = %.4f", len(cands), rawTarget)
	if rawTarget >= 0 && rawTarget <= 1 {
		t.Fatalf("反例证明失效：关掉 RRF 后历史公式在 N=%d 下**没有**越界（score=%.4f）—— "+
			"说明语料规模没把 BM25 量纲撑开，本判别器就证明不了 RRF 修了 I2", len(cands), rawTarget)
	}

	// ── B 位（本不变式）：生产融合（RRF 开）⇒ 有界且非退化 ────────────────────────
	prodScores := fuseScores(cands, lexScores, lexTuning)
	var maxScore float64
	for _, s := range prodScores {
		if s > maxScore {
			maxScore = s
		}
	}
	t.Logf("B位/RRF开：N=%d · target 融合分 = %.6f · 池内最大分 = %.6f",
		len(cands), prodScores[ti], maxScore)

	if prodScores[ti] <= 0 {
		t.Fatalf("不变式 I2 退化：RRF 融合分 %.6f 非正 —— 词法命中的条目必须有正分，"+
			"否则判别器会被「全返回 0」的实现骗过", prodScores[ti])
	}
	if maxScore > 1 {
		t.Fatalf("不变式 I2 违反：RRF 融合分最大值 %.4f 越出 [0,1]（RRFK=%.0f，"+
			"理论上界 = (0.2+0.3+0.5)/RRFK = %.4f）",
			maxScore, RRFK, (ScoreWeightRecency+ScoreWeightImportance+ScoreWeightRelevance)/RRFK)
	}
}
