// p3_wire_rank_test.go — P3-wire 判别器（config 侧）：兜底选择必须**按用户这句话**走。
//
// ## 判别的是哪一行
//
// `MemorySelector.Select` 在 `len(manifest) > maxSelections` 时走"本地兜底 + 后台预热"。
// 接线前兜底 = `manifest[:maxSelections]`（= **最近更新的 N 条**，与用户问什么无关）。
// 接线后 = 数据源实现 `MemoryRanker` 时**先按查询排序**，不足处再按最近更新补齐。
//
// ## A/B 的唯一变量
//
// 两个 fake store **数据完全相同**（同样 8 条、同样顺序），只差一件事：
// 是否实现 `MemoryRanker`（`p3PlainStore` 不实现 / `p3RankStore` 实现）。
// 于是"注入里有没有那条相关记忆"只能由**接线**解释。
//
// ## 还断言了"证据通道"
//
// 真机判别依赖落盘的 `rank_mode` / `rank_hits`（`selection_trace.jsonl`）。
// 本测试直接把观测文件读回来核对 —— 否则真机测试会因为**证据字段写错**而永远无法判绿，
// 而那会被误读成"接线没生效"。
package config

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// p3BlockingClient 一个**永不产出事件**的 AI client：把后台预热钉在"在途"状态，
// 使测试窗口内没有第二个写者（预热完成会写缓存与观测行）。
// 超时（60s）后它自行结束 —— 测试进程通常早已退出，故不构成挂起。
type p3BlockingClient struct{}

func (p3BlockingClient) Stream(ctx context.Context, _ ai.Request) (<-chan ai.Event, error) {
	ch := make(chan ai.Event)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}
func (p3BlockingClient) Model() string     { return "p3-blocking" }
func (p3BlockingClient) SetModel(_ string) {}
func (p3BlockingClient) Close() error      { return nil }
func (p3BlockingClient) String() string    { return "p3BlockingClient" }

// p3Entries 造 8 条记忆，UpdatedAt 严格递减（m1 最新 … m8 最旧）——
// 使"按最近更新取前 5"恰好取 m1..m5，把 m8 挡在外面（这就是判别所需的对照位）。
func p3Entries() []MemoryEntryV2 {
	out := make([]MemoryEntryV2, 0, 8)
	base := time.Now()
	for i := 1; i <= 8; i++ {
		out = append(out, MemoryEntryV2{
			Name:        fmt.Sprintf("m%d", i),
			Description: fmt.Sprintf("第 %d 条", i),
			Content:     fmt.Sprintf("m%d 正文：重建二进制的命令是 go build", i),
			UpdatedAt:   base.Add(-time.Duration(i) * time.Minute),
		})
	}
	return out
}

// p3PlainStore 只实现 MemorySource（**故意不实现 MemoryRanker**）—— A 位。
type p3PlainStore struct {
	dir     string
	entries []MemoryEntryV2
}

func (s *p3PlainStore) Dir() string                    { return s.dir }
func (s *p3PlainStore) List() ([]MemoryEntryV2, error) { return s.entries, nil }
func (s *p3PlainStore) Read(name string) (*MemoryEntryV2, error) {
	for i := range s.entries {
		if s.entries[i].Name == strings.TrimSuffix(name, ".md") {
			e := s.entries[i]
			return &e, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", name)
}

// p3RankStore 在 p3PlainStore 之上**再加** MemoryRanker —— B 位（唯一变量）。
type p3RankStore struct {
	p3PlainStore
	ranked []string
	calls  int
}

func (s *p3RankStore) Rank(_ context.Context, _ string, _ int) ([]string, error) {
	s.calls++
	return s.ranked, nil
}

// p3ReadTraces 把观测文件里的全部事件读回来（按写入顺序）。
func p3ReadTraces(t *testing.T, dir string) []MemorySelectEvent {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, memoryTraceFile))
	if err != nil {
		t.Fatalf("观测文件不存在（P0 观测未落盘）：%v", err)
	}
	defer f.Close()
	var out []MemorySelectEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev MemorySelectEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err == nil {
			out = append(out, ev)
		}
	}
	if len(out) == 0 {
		t.Fatalf("观测文件存在但没有可解析的事件")
	}
	return out
}

// p3FindBranch 取指定分支的那一条事件。
func p3FindBranch(t *testing.T, dir, branch string) *MemorySelectEvent {
	t.Helper()
	for _, ev := range p3ReadTraces(t, dir) {
		if ev.Branch == branch {
			e := ev
			return &e
		}
	}
	t.Fatalf("观测文件里没有 %s 分支的行", branch)
	return nil
}

// TestP3Wire_FallbackIsQueryAware 核心 A/B：唯一变量 = 数据源**是否具备排序能力**。
func TestP3Wire_FallbackIsQueryAware(t *testing.T) {
	const query = "重建二进制的命令是什么" // 只有 m8 与之有词法重合

	// ── A 位：不能排序 ⇒ 退回「最近更新的 5 条」= m1..m5，**m8 不该出现** ───────
	dirA := t.TempDir()
	a := &p3PlainStore{dir: dirA, entries: p3Entries()}
	outA, err := NewMemorySelector(a, nil, p3BlockingClient{}).Select(context.Background(), query)
	if err != nil {
		t.Fatalf("A位 Select 失败: %v", err)
	}
	if !strings.Contains(outA, "- [") {
		t.Fatalf("A位注入内容不含记忆段，测试前提不成立：%q", outA)
	}
	if !strings.Contains(outA, "m1 正文") {
		t.Errorf("A位应含最近更新的 m1（兜底语义 = 取最近 N 条），实际注入：%q", outA)
	}
	if strings.Contains(outA, "m8 正文") {
		t.Fatalf("A位不该含 m8（它最旧，在最近 5 条之外）—— 若含，说明 A/B 无区分度")
	}
	evA := p3FindBranch(t, dirA, traceBranchFallback)
	if evA.RankMode != rankModeRecencyOnly {
		t.Errorf("A位 rank_mode = %q，期望 %q（数据源未实现 MemoryRanker ⇒ 观测须如实记录降级）",
			evA.RankMode, rankModeRecencyOnly)
	}

	// ── B 位：能排序，且打分器给出 m8 ⇒ **必须**进注入（挂在前面） ─────────────
	dirB := t.TempDir()
	b := &p3RankStore{p3PlainStore: p3PlainStore{dir: dirB, entries: p3Entries()}, ranked: []string{"m8", "m7"}}
	outB, err := NewMemorySelector(b, nil, p3BlockingClient{}).Select(context.Background(), query)
	if err != nil {
		t.Fatalf("B位 Select 失败: %v", err)
	}
	if b.calls == 0 {
		t.Fatalf("B位打分器从未被调用 —— 接线没接上")
	}
	if !strings.Contains(outB, "m8 正文") {
		t.Fatalf("B位注入里**没有** m8（相关的那条）—— 接线未生效。实际注入：%q", outB)
	}
	if strings.Contains(outB, "m6 正文") {
		t.Errorf("B位不该含 m6（既非相关、也被挤出了最近 5 条），实际注入：%q", outB)
	}
	// 补齐语义：不足 5 条时用"最近更新"补足 ⇒ 总数仍是 5，且 m1 在。
	if !strings.Contains(outB, "m1 正文") {
		t.Errorf("B位应把名额补满（用最近更新补齐），m1 应仍在：%q", outB)
	}
	evB := p3FindBranch(t, dirB, traceBranchFallback)
	if evB.RankMode != rankModeRank {
		t.Errorf("B位 rank_mode = %q，期望 %q", evB.RankMode, rankModeRank)
	}
	if evB.RankHits != 2 {
		t.Errorf("B位 rank_hits = %d，期望 2（打分器贡献 m8/m7 两条）", evB.RankHits)
	}

	// ── 反向对照：打分器**零命中**时必须退回旧行为（不是注入空） ───────────────
	dirC := t.TempDir()
	c := &p3RankStore{p3PlainStore: p3PlainStore{dir: dirC, entries: p3Entries()}}
	outC, err := NewMemorySelector(c, nil, p3BlockingClient{}).Select(context.Background(), query)
	if err != nil {
		t.Fatalf("C位 Select 失败: %v", err)
	}
	if !strings.Contains(outC, "m1 正文") || strings.Contains(outC, "m8 正文") {
		t.Errorf("C位（打分器零命中）应完全退回『最近更新的 5 条』：%q", outC)
	}
	if evC := p3FindBranch(t, dirC, traceBranchFallback); evC.RankMode != rankModeNoHint {
		t.Errorf("C位 rank_mode = %q，期望 %q", evC.RankMode, rankModeNoHint)
	}
}

// TestP3Wire_UnderLimitNotAffected 条目 ≤ maxSelections 时**不走排序**（沿用旧行为）。
//
// 这条断言的是"接线没有顺手改掉不该改的东西"：条目少的时候"全给"比"排序"更省心，
// 也是 之前就有的语义。
func TestP3Wire_UnderLimitNotAffected(t *testing.T) {
	dir := t.TempDir()
	entries := p3Entries()[:3] // 3 ≤ maxSelections(5)
	s := &p3RankStore{p3PlainStore: p3PlainStore{dir: dir, entries: entries}, ranked: []string{"m3"}}
	out, err := NewMemorySelector(s, nil, p3BlockingClient{}).Select(context.Background(), "任意")
	if err != nil {
		t.Fatalf("Select 失败: %v", err)
	}
	if s.calls != 0 {
		t.Errorf("条目未超上限却调用了打分器（calls=%d）—— 不该动这条路径", s.calls)
	}
	for i := 1; i <= 3; i++ {
		if !strings.Contains(out, fmt.Sprintf("m%d 正文", i)) {
			t.Errorf("条目 ≤ 上限时应全量注入，缺 m%d：%q", i, out)
		}
	}
	if ev := p3FindBranch(t, dir, traceBranchSyncAll); ev.RankMode != "" {
		t.Errorf("sync_all 分支不该带 rank_mode，实际 rank_mode=%q", ev.RankMode)
	}
}
