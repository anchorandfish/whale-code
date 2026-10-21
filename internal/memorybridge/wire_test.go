// wire_test.go — P3-wire 端到端判别器。
//
// 目的：证明「本地打分器 → 注入」这条链路**在真实记忆文件上**通了。
//
// 与 config/p3_wire_rank_test.go 的分工：
// - 那份用 fake store 判别 `MemorySelector` 的**选择逻辑**（快、可控）；
// - 这份从**真实磁盘布局**出发，走 bridge（真实 memory 打分器）→ config.Select（生产入口），
// 判别"接线在端到端装配下是否真的生效" —— 即用户会看到的那条路。
//
// A/B 的唯一变量 = 数据源**是否具备排序能力**（`sourceNoRank` 包一层把 Rank 藏掉），
// 数据、查询、件全同。
package memorybridge_test

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
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/memory"
	"github.com/anchorandfish/whale-code/internal/memorybridge"
)

// p3WireClient 永不产出事件的 AI client：把后台预热钉在"在途"，测试窗口内无并发写者。
type p3WireClient struct{}

func (p3WireClient) Stream(ctx context.Context, _ ai.Request) (<-chan ai.Event, error) {
	ch := make(chan ai.Event)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}
func (p3WireClient) Model() string     { return "p3-wire-stub" }
func (p3WireClient) SetModel(_ string) {}
func (p3WireClient) Close() error      { return nil }

// sourceNoRank 满足 config.MemorySource 但**不**满足 config.MemoryRanker
// （src 是普通字段而非内嵌 ⇒ Rank 不会被提升）—— 这就是 A 位（等价于"装配用的还是旧 store"）。
type sourceNoRank struct{ src *memorybridge.Source }

func (s sourceNoRank) Dir() string                                  { return s.src.Dir() }
func (s sourceNoRank) List() ([]config.MemoryEntryV2, error)        { return s.src.List() }
func (s sourceNoRank) Read(n string) (*config.MemoryEntryV2, error) { return s.src.Read(n) }

const (
	// p3WireQuery 用户真实打法的整句（不是关键词）。
	p3WireQuery = "重建二进制的命令是什么来着"
	// p3WireTarget 唯一与查询有词法重合的那条（最旧）。
	p3WireTarget = "note-8"
	// p3WireTargetBody 目标正文里的一段，用来在注入文本里认它。
	p3WireTargetBody = "重建二进制的命令"
)

// seedRankScenario 铺 8 条 project 记忆：note-1 最新 … note-8 最旧。
//
// ★ 目标刻意放在**最旧**：这样"按最近更新取前 5"（note-1..note-5）必然取不到它 ——
// 若 A 位也能看到它，A/B 就没有区分度。
func seedRankScenario(t *testing.T, f *p2Fixture) {
	t.Helper()
	now := time.Now()
	bodies := []string{
		"部署走 CI，不手工上线",                            // note-1（最新）
		"接口超时统一设 30 秒",                            // note-2
		"周报每周五下午提交",                               // note-3
		"前端构建产物不进版本库",                             // note-4
		"日志级别默认 info，调试时开 debug",                  // note-5
		"数据库迁移脚本放在 migrations 目录",                 // note-6
		"配置文件放 configs 目录",                        // note-7
		"重建二进制的命令是 go build -o whale ./cmd/whale", // note-8（最旧）← 目标
	}
	for i, body := range bodies {
		f.writeEntry(t, f.projDir,
			fmt.Sprintf("note-%d", i+1), body,
			now.Add(-time.Duration(i+1)*time.Minute),
			memory.VerificationVerified, nil)
	}
}

// p3ReadBridgeTrace 读 bridge 目录下的观测文件，取**最后**一条 fallback_warmup。
//
// ★ 为什么"最后一条"：A/B 两个 selector 共用同一个 trace 目录，A 先写、B 后写。
// 取第一条会拿到 A 位的读数 —— 本判别器首跑就栽在这一处（报 rank_mode=recency_only，
// 而实际 B 位已按查询注入）。这类"读错证据行"的错会伪装成"接线没生效"，
// 故在此显式写明，并断言 last 的 selected 首位就是目标。
func p3ReadBridgeTrace(t *testing.T, dir string) config.MemorySelectEvent {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "selection_trace.jsonl"))
	if err != nil {
		t.Fatalf("观测文件不存在（P0 观测未落盘）：%v", err)
	}
	defer f.Close()
	var last config.MemorySelectEvent
	found := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev config.MemorySelectEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Branch == "fallback_warmup" {
			last, found = ev, true
		}
	}
	if !found {
		t.Fatalf("观测文件里没有 fallback_warmup 分支的行（接线未走到兜底分支？）")
	}
	return last
}

// TestP3Wire_EndToEnd_InjectionIsQueryAware 端到端 A/B。
func TestP3Wire_EndToEnd_InjectionIsQueryAware(t *testing.T) {
	f := newP2Fixture(t)
	seedRankScenario(t, f)

	src, err := memorybridge.NewSource(f.home, f.workDir)
	if err != nil {
		t.Fatalf("memorybridge.NewSource: %v", err)
	}

	// ── 数据面先自证：bridge.Rank 必须把目标排在第一顺位 ─────────────────────
	ranked, err := src.Rank(context.Background(), p3WireQuery, 5)
	if err != nil {
		t.Fatalf("bridge.Rank 失败: %v", err)
	}
	if len(ranked) == 0 {
		t.Fatalf("bridge.Rank 零命中 —— 词法通道未命中目标（先查内部打分器，不是接线）")
	}
	if ranked[0] != p3WireTarget {
		t.Fatalf("bridge.Rank 首位 = %q，期望 %q（全序 %v）—— 打分器语义变化",
			ranked[0], p3WireTarget, ranked)
	}
	t.Logf("bridge.Rank(%q) = %v", p3WireQuery, ranked)

	// ── A 位：数据源不暴露 Rank ⇒ 退回「最近更新的 5 条」 ─────────────────────
	aSelector := config.NewMemorySelector(sourceNoRank{src: src}, nil, p3WireClient{})
	outA, err := aSelector.Select(context.Background(), p3WireQuery)
	if err != nil {
		t.Fatalf("A位 Select 失败: %v", err)
	}
	if strings.Contains(outA, p3WireTargetBody) {
		t.Fatalf("A位（无排序能力）不该注入最旧的那条目标 —— 若注入了，A/B 无区分度。注入=%.200q", outA)
	}

	// ── B 位：数据源具备 Rank ⇒ 目标必须进注入 ──────────────────────────────
	bSelector := config.NewMemorySelector(src, nil, p3WireClient{})
	outB, err := bSelector.Select(context.Background(), p3WireQuery)
	if err != nil {
		t.Fatalf("B位 Select 失败: %v", err)
	}
	if !strings.Contains(outB, p3WireTargetBody) {
		t.Fatalf("B位未注入目标记忆 —— **接线未生效**。注入=%.200q", outB)
	}

	// ── 观测通道：真机判别依赖落盘的 rank_mode / rank_hits，必须真的写上 ──────
	evB := p3ReadBridgeTrace(t, src.Dir())
	if evB.RankMode != "rank" || evB.RankHits < 1 {
		t.Errorf("观测字段不对：rank_mode=%q rank_hits=%d（真机判别依赖它，写错会让真机永远无法判绿）",
			evB.RankMode, evB.RankHits)
	}
	if evB.SelectedN == 0 || len(evB.Selected) == 0 {
		t.Errorf("观测未记录注入名单：selected_n=%d selected=%v", evB.SelectedN, evB.Selected)
	}
	if len(evB.Selected) == 0 || evB.Selected[0] != p3WireTarget {
		t.Errorf("观测里的注入名单首位应为 %q，实际 %v", p3WireTarget, evB.Selected)
	}
	t.Logf("观测（B位）：branch=%s rank_mode=%s rank_hits=%d selected=%v",
		evB.Branch, evB.RankMode, evB.RankHits, evB.Selected)
}
