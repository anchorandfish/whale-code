// p3_search_tuning_test.go — P3 打分器六步的判别器（每步一组，先红后绿）。
//
// ★ 本文件的性质：与 p1_invariants_test.go 互补 ——
//
//	P1 那三条断言的是「**不随实现变化**的不变式」（中文召回 / 量纲有界 / 作废不回流）；
//	本文件断言的是「**每一步开关关掉必须退回旧行为**」（即方案 §6 P3 的"每步一个开关"）。
//	两者合起来才成立：只有不变式 ⇒ 可能一步到位把开关写死；只有开关 ⇒ 可能开关是装饰。
//
// ★ 判别器不许恒绿：每条都取两个及以上位点，且**至少一个位点必须红**（关 = 旧行为）。
package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// p3SearchNames 取结果里的 Name 列表（失败信息用）。
func p3SearchNames(entries []*MemoryEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

// p3Step1Corpus 写入第 1 步的语料：1 条目标 + 7 条不相干填充（撑规模，避免"被测对象消失"）。
func p3Step1Corpus(t *testing.T) *MergeLayer {
	t.Helper()
	_, ml := setupTestMergeLayer(t, "/test/work/p3-step1")
	// 目标：正文含「记忆**组织方式**」子串（整串中文，正是 unicode61 会踩的场景）
	writeEntryToStore(t, ml.userStore, "memory-layout",
		"记忆分层", "我们讨论过记忆的组织方式与分层预算", VerificationVerified)
	p1WriteFillers(t, ml.userStore, 7)
	return ml
}

// TestP3_Step1_TrigramRecall 第 1 步切换位点：**A/B 两态**（关=旧行为必须红）。
//
// 关（baseTuning）= 历史 unicode61 ⇒ 整串中文当一个 token ⇒ 查询「组织方式」0 命中。
// 开（生产默认） = trigram ⇒ 子串命中。
func TestP3_Step1_TrigramRecall(t *testing.T) {
	p3Step1Corpus(t)
	ctx := context.Background()

	// ── A 位：全关 —— 必须 0 命中，否则说明"关"没关（开关是装饰） ──────────────
	off := baseTuning()
	gotOff, err := SearchMemory(ctx, "组织方式", SearchOptions{Tuning: &off})
	if err != nil {
		t.Fatalf("[A位/全关] SearchMemory 失败: %v", err)
	}
	if len(gotOff) != 0 {
		t.Fatalf("[A位/全关] 判别器失效：关掉开关后仍命中 %d 条 %v —— "+
			"这说明开关没真正控制分词器（或 unicode61 行为已变），本判别器不能证明任何事",
			len(gotOff), p3SearchNames(gotOff))
	}

	// ── B 位：生产默认 —— 必须命中子串 ─────────────────────────────────────
	on := currentTuning
	gotOn, err := SearchMemory(ctx, "组织方式", SearchOptions{Tuning: &on})
	if err != nil {
		t.Fatalf("[B位/生产默认] SearchMemory 失败: %v", err)
	}
	if findEntryByName(gotOn, "memory-layout") == nil {
		t.Fatalf("[B位/生产默认] 第 1 步未生效：查询「组织方式」仍未命中正文含该子串的 memory-layout；"+
			"返回 %d 条 %v", len(gotOn), p3SearchNames(gotOn))
	}
}

// TestP3_Step1_ShortQueryFallback 第 1 步**兜底通道**的**三态承重证明**。
//
// 为什么必须三态：`trigram` 只解决 ≥3 rune 的查询，2 字中文查询它**天然做不到**（实测 0 命中）。
// 所以「只开 TrigramTokenizer」与「TrigramTokenizer + SubstringFallback」必须**结果不同** ——
// 若两者相同，说明兜底通道根本没被走到（搭便车），这条判据就是装饰。
func TestP3_Step1_ShortQueryFallback(t *testing.T) {
	p3Step1Corpus(t)
	ctx := context.Background()

	// ── A 位：全关（历史行为）—— 必须 0 命中 ───────────────────────────────
	off := baseTuning()
	gotOff, err := SearchMemory(ctx, "组织", SearchOptions{Tuning: &off})
	if err != nil {
		t.Fatalf("[A位/全关] SearchMemory 失败: %v", err)
	}
	if len(gotOff) != 0 {
		t.Fatalf("[A位/全关] 判别器失效：2 字查询在历史行为下竟命中 %d 条 %v",
			len(gotOff), p3SearchNames(gotOff))
	}

	// ── B 位：只开 trigram、**不开兜底** —— 必须仍然 0 命中 ─────────────────
	// 这是本判据的核心：证明 trigram 对 2 字查询真的无能（不是我的推断）。
	onlyTrigram := baseTuning()
	onlyTrigram.TrigramTokenizer = true
	gotTri, err := SearchMemory(ctx, "组织", SearchOptions{Tuning: &onlyTrigram})
	if err != nil {
		t.Fatalf("[B位/仅trigram] SearchMemory 失败: %v", err)
	}
	if len(gotTri) != 0 {
		t.Fatalf("[B位/仅trigram] 与实测不符：trigram 分词器对 %d rune 的查询本应无法命中，"+
			"却返回 %d 条 %v —— 若这步变绿，说明兜底通道是搭便车（可删），请重估第 1 步设计",
			2, len(gotTri), p3SearchNames(gotTri))
	}

	// ── C 位：trigram + 兜底（生产默认）—— 必须命中 ────────────────────────
	on := currentTuning
	gotOn, err := SearchMemory(ctx, "组织", SearchOptions{Tuning: &on})
	if err != nil {
		t.Fatalf("[C位/生产默认] SearchMemory 失败: %v", err)
	}
	if findEntryByName(gotOn, "memory-layout") == nil {
		t.Fatalf("[C位/生产默认] 短查询兜底未生效：2 字查询「组织」仍未命中 memory-layout；"+
			"返回 %d 条 %v", len(gotOn), p3SearchNames(gotOn))
	}
}

// ============================================================
// 第 2 步：取消 FTS 硬门 → 并集候选
// ============================================================

// p3WriteEntryImportance 写入一条**指定 importance**的记忆（测试辅助）。
// 为什么不能用 writeEntryToStore：它不设 Importance ⇒ store 落盘时按零值规则补 50
// （scope.go:195）⇒ 造不出"高先验"对照条，第 2 步判别器就没法把
// 「高先验进入」与「任何未命中者都进入」区分开（后者会让判据退化成"恒真"）。
func p3WriteEntryImportance(t *testing.T, store *MemoryStore, name, description, content, verification string, importance int) {
	t.Helper()
	entry := &MemoryEntry{
		Name:         name,
		Description:  description,
		Type:         MemoryTypeUser,
		CreatedAt:    time.Now(),
		Content:      content,
		Verification: verification,
		Importance:   importance,
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write %q 失败: %v", name, err)
	}
}

// p3Step2Corpus 写入第 2 步的语料：
//   - deploy-script：**词法命中**者（正文含查询子串「部署流程」），importance 默认 50；
//   - hotfix-policy：**不含**查询子串，但 importance=100 ⇒ 先验分 0.2·recency+0.3·1.0 ≈ 0.5；
//   - 7 条 filler：不含子串、importance 默认 50 ⇒ 先验 ≈ 0.2·recency+0.3·0.5 ≈ 0.35。
//
// ⇒ 三者先验分层清晰：0.5（高）/ 0.35（普通）。并集阈值 θ=UnionPriorThreshold(0.4) 正好把
// 「高先验」与「普通」分开 ⇒ 判别器能断言「谁该进、谁不该进」，而不是"随便什么都进"。
func p3Step2Corpus(t *testing.T) *MergeLayer {
	t.Helper()
	_, ml := setupTestMergeLayer(t, "/test/work/p3-step2")
	writeEntryToStore(t, ml.userStore, "deploy-script",
		"部署脚本", "部署流程见 scripts/deploy.sh", VerificationVerified)
	p3WriteEntryImportance(t, ml.userStore, "hotfix-policy",
		"紧急修复策略", "上线出问题时先回滚再排查", VerificationVerified, 100)
	p1WriteFillers(t, ml.userStore, 7)
	return ml
}

// TestP3_Step2_UnionCandidates 第 2 步的切换位点：**取消 FTS 硬门改并集**。
//
// 语义（方案 §2 硬伤 3）：历史行为下「没被 FTS 匹配 ⇒ 直接不参与排序」，
// 于是一条**极重要、但用词不同**的记忆**永远进不来**。并集把它们放进排序池。
//
// ★ 单一变量纪律：A/B 之间**只**改 `UnionCandidates` 一个字段；分词器（第 1 步）两态都开着，
// 否则 A 位可能是"因为中文搜不到"而不是"因为硬门"。
func TestP3_Step2_UnionCandidates(t *testing.T) {
	p3Step2Corpus(t)
	ctx := context.Background()

	// 基准：第 1 步分词器**两态都开**（隔离第 2 步），只有 UnionCandidates 一个变量。
	base := baseTuning()
	base.TrigramTokenizer = true
	base.SubstringFallback = true

	// ── A 位：硬门（历史行为）—— 未命中者**不得**进入结果 ──────────────────
	off := base // UnionCandidates = false
	gotOff, err := SearchMemory(ctx, "部署流程", SearchOptions{Tuning: &off})
	if err != nil {
		t.Fatalf("[A位/硬门] SearchMemory 失败: %v", err)
	}
	if findEntryByName(gotOff, "deploy-script") == nil {
		t.Fatalf("[A位/硬门] 判别器失效：词法命中者 deploy-script 竟不在结果里（%v）—— "+
			"本判据的前提是有词法命中，先查第 1 步语料/查询词", p3SearchNames(gotOff))
	}
	if findEntryByName(gotOff, "hotfix-policy") != nil {
		t.Fatalf("[A位/硬门] 判别器失效：关掉第 2 步后**未命中**的高先验记忆 hotfix-policy "+
			"仍进入结果（%v）—— 说明「取消硬门」这个开关没关住，A/B 没有区分度", p3SearchNames(gotOff))
	}

	// ── B 位：并集 —— 高先验的未命中者**必须**进入；普通未命中者仍被 θ 挡住 ──
	on := base
	on.UnionCandidates = true
	gotOn, err := SearchMemory(ctx, "部署流程", SearchOptions{Tuning: &on})
	if err != nil {
		t.Fatalf("[B位/并集] SearchMemory 失败: %v", err)
	}
	if findEntryByName(gotOn, "hotfix-policy") == nil {
		t.Fatalf("[B位/并集] 第 2 步未生效：高先验（importance=100）但**用词不同**的记忆 "+
			"hotfix-policy 仍未进入排序池；返回 %d 条 %v\n"+
			"⇒ 这正是硬门的老毛病：极重要但用词不同的记忆永远进不来",
			len(gotOn), p3SearchNames(gotOn))
	}
	if findEntryByName(gotOn, "deploy-script") == nil {
		t.Fatalf("[B位/并集] 词法命中者 deploy-script 被并集挤掉了（%v）—— "+
			"并集只应**新增**候选，不得挤掉有词法证据的条目", p3SearchNames(gotOn))
	}
	// θ 承重证明：普通先验（≈0.35 < θ=0.4）的 filler 不得进来，
	// 否则并集就是"把所有未命中者全灌进来"，θ 是装饰。
	if findEntryByName(gotOn, "filler-00") != nil {
		t.Fatalf("[B位/并集] θ(UnionPriorThreshold=%.2f) 未生效：普通先验的 filler-00 也进来了（%v）\n"+
			"⇒ 若这是有意为之（不要 θ），请改这里并说明理由；否则并集退化成「全灌」",
			UnionPriorThreshold, p3SearchNames(gotOn))
	}
}

// ============================================================
// 第 3 步：RRF 名次融合（修 I2 量纲）
// ============================================================

// p3Step3Corpus 造一份**大语料**（N≈1001），只为把 BM25 的量纲撑开。
// ★ 与 I2 用同一构造法（内联候选、不走盘）：FTS5 索引本就是临时内存库（:memory:），
// 且 computeBM25Scores/fuseScores 就是 SearchMemory 内部调的生产函数，不是替身。
// 唯一目的是"不用往磁盘写 1000 个文件"。
func p3Step3Corpus(t *testing.T) (cands []*MemoryEntry, target *MemoryEntry) {
	t.Helper()
	const n = 1000
	now := time.Now()
	cands = make([]*MemoryEntry, 0, n+1)
	for i := 0; i < n; i++ {
		cands = append(cands, &MemoryEntry{
			Name:        fmt.Sprintf("bulk-%04d", i),
			Description: "bulk corpus entry",
			// 刻意不含查询词：若全体文档都含该词，IDF→0、bm25≈0 ⇒ 判别器自己失效（假绿）。
			Content:      "bulk corpus 正文 " + strings.Repeat("padding ", 8),
			Type:         MemoryTypeUser,
			CreatedAt:    now,
			Importance:   50,
			Verification: VerificationVerified,
		})
	}
	target = &MemoryEntry{
		Name:         "target",
		Description:  "target entry",
		Content:      "共享词 目标条目正文",
		Type:         MemoryTypeUser,
		CreatedAt:    now,
		Importance:   50,
		Verification: VerificationVerified,
	}
	cands = append(cands, target)
	return cands, target
}

// TestP3_Step3_RRFBounded 第 3 步的切换位点：**原始分加权和 → 名次融合**。
//
// 与 I2 的分工（都留，不合并）：
//   - I2 断言「**生产**融合有界」（守门测试，长期有效）；
//   - 本判据断言「**这个开关**就是有界性的原因」—— 关掉即越界。
//     前者绿了并不能推出后者（可能语料没撑开量纲），所以必须分开。
func TestP3_Step3_RRFBounded(t *testing.T) {
	cands, target := p3Step3Corpus(t)

	base := baseTuning()
	base.TrigramTokenizer = true
	matched, bm25, err := computeBM25Scores(cands, "共享词", base)
	if err != nil {
		t.Fatalf("computeBM25Scores 失败: %v", err)
	}
	lex := make(map[*MemoryEntry]float64, len(matched))
	for i, e := range matched {
		lex[e] = bm25[i]
	}
	if _, ok := lex[target]; !ok {
		t.Fatalf("目标条目未进入词法通道（matched=%d）—— 判别器自身失效，先查语料/查询词", len(matched))
	}
	ti := -1
	for i, e := range cands {
		if e == target {
			ti = i
			break
		}
	}

	// ── A 位：关掉 RRF（历史行为）—— N=1000 量级下**必须**越界 ─────────────────
	off := base // RRF = false
	offScores := fuseScores(cands, lex, off)
	t.Logf("[A位/RRF关] N=%d · target 原始分加权和 = %.4f", len(cands), offScores[ti])
	if offScores[ti] >= 0 && offScores[ti] <= 1 {
		t.Fatalf("[A位/RRF关] 判别器失效：历史原始分公式在 N=%d 下**没有**越界（%.4f）—— "+
			"说明语料没把 BM25 量纲撑开，本判据证明不了任何事", len(cands), offScores[ti])
	}

	// ── B 位：开 RRF —— 全池**必须**有界，且目标非退化（有正分） ────────────────
	on := base
	on.RRF = true
	onScores := fuseScores(cands, lex, on)
	maxS := 0.0
	for i, s := range onScores {
		if s < 0 || s > 1 {
			t.Fatalf("[B位/RRF开] 第 3 步未生效：第 %d 条融合分 %.4f 越出 [0,1]", i, s)
		}
		if s > maxS {
			maxS = s
		}
	}
	t.Logf("[B位/RRF开] N=%d · target 融合分 = %.6f · 池内最大分 = %.6f", len(cands), onScores[ti], maxS)
	if onScores[ti] <= 0 {
		t.Fatalf("[B位/RRF开] 词法命中者的融合分非正（%.6f）—— 判别器会被「全 0」实现骗过", onScores[ti])
	}
}

// TestP3_Step1b_QueryTermOR 第 1b 步切换位点：**整句查询**的 A/B 两态。
//
// 为什么单独有这一步：P3-1（换 trigram）只让**关键词**能命中 —— 关键词本身是子串。
// 而 FTS5 里 `"整句"` 是短语查询，等价于"整句必须是文档的连续子串"，于是**用户真实的
// 整句输入零召回**（实测 10/10，见 p3_recall_bench_test.go）。注入路径喂进来的正是整句
// ⇒ 不补这一步，接线后本地排序会恒空、静默退回"按最近更新"。
//
//   - A 位（QueryTermOR 关）：必须**零召回**（否则说明整句恰好是子串，判别器无区分度）；
//   - B 位（QueryTermOR 开）：目标必须**第一顺位**。
func TestP3_Step1b_QueryTermOR(t *testing.T) {
	_, ml := setupTestMergeLayer(t, "/test/work/p3-step1b")
	writeEntryToStore(t, ml.userStore, "memory-layout",
		"写代码的方式", "用户要求第一性原理，不缝缝补补地改代码", VerificationVerified)
	p1WriteFillers(t, ml.userStore, 7)

	// 关键：这是一个**整句**，它不是任何一条记忆的子串（用户真实打法）。
	const q = "你之前说过要第一性原理，不缝缝补补，对吧"

	off := baseTuning()
	off.TrigramTokenizer = true
	off.SubstringFallback = true
	gotOff, err := SearchMemory(context.Background(), q, SearchOptions{TopK: 5, Tuning: &off})
	if err != nil {
		t.Fatalf("SearchMemory(A位) 失败: %v", err)
	}
	if len(gotOff) != 0 {
		t.Fatalf("[A位/短语查询] 期望零召回（整句不是任何记忆的子串），实际 %d 条 %v —— "+
			"A/B 无区分度，先确认查询确实是整句", len(gotOff), p3SearchNames(gotOff))
	}

	on := off
	on.QueryTermOR = true
	gotOn, err := SearchMemory(context.Background(), q, SearchOptions{TopK: 5, Tuning: &on})
	if err != nil {
		t.Fatalf("SearchMemory(B位) 失败: %v", err)
	}
	if findEntryByName(gotOn, "memory-layout") == nil {
		t.Fatalf("[B位/拆词OR] 整句查询仍未召回目标（返回 %v）—— 第 1b 步未生效",
			p3SearchNames(gotOn))
	}
	if gotOn[0].Name != "memory-layout" {
		t.Fatalf("[B位/拆词OR] 目标未排第一顺位（首位是 %q，全序 %v）—— bm25 的 IDF 未把"+
			"共享更多词项的条目排前", gotOn[0].Name, p3SearchNames(gotOn))
	}

	// 拆词器自身的可判别性：必须真的拆成多个词项、且含内容词项（而非退化成短语）。
	expr := buildTermOrQuery(q)
	if !strings.Contains(expr, " OR ") {
		t.Fatalf("buildTermOrQuery 未产出 OR 表达式：%q", expr)
	}
	if !strings.Contains(expr, `"第一性"`) {
		t.Fatalf("buildTermOrQuery 少了内容词项「第一性」：%q", expr)
	}
	t.Logf("[拆词] %q → %d 个词项 · 表达式：%s", q, strings.Count(expr, " OR ")+1, expr)
}
