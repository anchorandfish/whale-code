package config

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// ── · 记忆选择的重复 LLM 调用 ─────────────────────────
//
// 真凶定位：`MemorySelector.SelectRelevant` 在 `len(manifest) > maxSelections`
// （默认 5）时会发起一次**真实 LLM 调用**（`ai.StreamWithRetry` / `aiClient.Stream`）
// 并**同步等待整个流结束**。
//
// 而它的调用链是：
//
//	sendInput → buildRequest → buildSystemPrompt
// → MemSelector().Select(ctx, sessionService.FirstUserInput())
//
// `FirstUserInput()` 在**会话内不变** ⇒ 同一条用户消息的每一轮、以及**每个工具
// 轮次续轮**都会重复发起**完全相同**的 LLM 调用，且全部发生在主循环的同步段里
// ⇒ 界面冻结。这正是"输入换行卡半天"的结构性来源。
//
// 三条判据：缓存命中（主）/ 输入变化必须重选（反例）/ 记忆变化必须重选（反例）。
// 缺任一条反例，"缓存"就可能退化成"永远返回陈旧值"。
//
// ★ 后的口径变化（如实记录，勿当"顺手放宽"）：
// 那次 AI 调用**不再同步发生在首次 Select 的调用栈上**，而是落在后台预热里
// （见 `first_turn_nonblocking_test.go`）。不变量本身没变、而且更强了 ——
// 从"同步调用一次"变成"**全程只调用一次**"。因此本文件三条判据改为
// **先等异步调用落地、再断言**，否则会退化成靠调度碰运气（flaky）。

// fakeCountingClient 计数 Stream 调用次数；返回一个立即关闭的空流。
// 这是 R2 允许的「只掐 sink」：AI 客户端是**外部副作用出口**，
// 断言打在**调用次数**（决策面）而不是网络结果上。
type fakeCountingClient struct {
	calls int64
}

func (f *fakeCountingClient) Stream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	atomic.AddInt64(&f.calls, 1)
	ch := make(chan ai.Event)
	close(ch)
	return ch, nil
}
func (f *fakeCountingClient) Model() string   { return "fake-model" }
func (f *fakeCountingClient) SetModel(string) {}
func (f *fakeCountingClient) Close() error    { return nil }

func (f *fakeCountingClient) count() int64 { return atomic.LoadInt64(&f.calls) }

// callCounter 让 waitCallCount 同时服务两个假客户端（计数型 / 阻塞型）。
type callCounter interface{ count() int64 }

// waitCallCount 轮询等到"已发起的 AI 调用数 ≥ want"，超时即判红。
//
// 为什么必须等而不是立刻断言： 后 AI 选择在**后台**发生，
// "调用次数"这个观测量有**到达延迟**。立刻断言会变成"谁先被调度谁赢"
// —— 判据时红时绿（flaky）比没有判据更糟。
func waitCallCount(t *testing.T, c callCounter, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.count() < want {
		if time.Now().After(deadline) {
			t.Fatalf("等待 AI 调用数达到 %d 超时（实得 %d）⇒ 该发起的调用没发生", want, c.count())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// newSelectionFixture 造一个「manifest 条数 > maxSelections」的选择器（触发 AI 选择）。
func newSelectionFixture(t *testing.T, entries int) (*MemorySelector, *MemoryStore, *fakeCountingClient) {
	t.Helper()
	store, err := NewMemoryStore(t.TempDir(), "/tmp/whale-select-cache-test")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < entries; i++ {
		if err := store.Write(MemoryEntryV2{
			Name:    fmt.Sprintf("mem-%d", i),
			Content: fmt.Sprintf("memory content %d", i),
			Type:    MemoryTypeProject,
		}); err != nil {
			t.Fatal(err)
		}
	}
	client := &fakeCountingClient{}
	ms := NewMemorySelector(store, nil, client)
	ms.maxSelections = 1 // 让 manifest(>1) 超过上限 ⇒ 走 AI 选择路径

	// fixture 自检（R4-12）：判据必须有两个可能取值。若 manifest 没超过上限，
	// 三条判据都会因"压根没走 AI 分支"而假红/假绿 ⇒ 先说清而不是让它悄悄通过。
	if n := len(ms.buildManifest()); n <= ms.maxSelections {
		t.Fatalf("fixture 失效：manifest=%d 未超过 maxSelections=%d ⇒ 判据失去判别力", n, ms.maxSelections)
	}
	return ms, store, client
}

// TestMemorySelectDoesNotRepeatLLMCallForSameInput 是**主判据**：
// 同一个 userInput（会话内 FirstUserInput 不变）⇒ 整个会话**只发起 1 次** AI 选择调用，
// 第二次 Select 不得再新增。
//
// ★ 判别性自检：修复前本判据必红 —— 见 回执的 before 取证。
func TestMemorySelectDoesNotRepeatLLMCallForSameInput(t *testing.T) {
	ms, _, client := newSelectionFixture(t, 3)

	if _, err := ms.Select(context.Background(), "同一个用户输入"); err != nil {
		t.Fatalf("首次 Select 出错: %v", err)
	}
	// 仍要证明"确实发起过"——否则下面那句"没新增"是空的（没走到 AI 分支）。
	waitCallCount(t, client, 1)
	afterFirst := client.count()

	if _, err := ms.Select(context.Background(), "同一个用户输入"); err != nil {
		t.Fatalf("第二次 Select 出错: %v", err)
	}
	if got := client.count(); got != afterFirst {
		t.Fatalf("相同输入的第二次 Select 又发起了 %d 次 LLM 调用 ⇒ 缓存未生效",
			got-afterFirst)
	}
}

// TestMemorySelectRecomputesWhenInputChanges 是反例①：输入变了必须重选。
func TestMemorySelectRecomputesWhenInputChanges(t *testing.T) {
	ms, _, client := newSelectionFixture(t, 3)

	if _, err := ms.Select(context.Background(), "输入 A"); err != nil {
		t.Fatal(err)
	}
	waitCallCount(t, client, 1) // 等首次的异步调用落地，否则 before 可能取到 0
	before := client.count()

	if _, err := ms.Select(context.Background(), "完全不同的输入 B"); err != nil {
		t.Fatal(err)
	}
	// 新 key 的选择同样在后台 ⇒ 必须等它到达；等到就说明"确实重选了"。
	waitCallCount(t, client, before+1)
}

// TestMemorySelectRecomputesWhenManifestChanges 是反例②：记忆增删改必须重选。
func TestMemorySelectRecomputesWhenManifestChanges(t *testing.T) {
	ms, store, client := newSelectionFixture(t, 3)

	if _, err := ms.Select(context.Background(), "输入 A"); err != nil {
		t.Fatal(err)
	}
	waitCallCount(t, client, 1)
	before := client.count()
	beforeManifest := len(ms.buildManifest())

	// Type 必须是**合法枚举**：ReadMemoryEntry 对非法 Type 直接报错，而 List() 会
	// **静默跳过**解析失败的文件 ⇒ 若用非法 Type，新记忆根本进不了 manifest，
	// 本反例会退化成"永远空转"（写了个看不见的文件）。
	if err := store.Write(MemoryEntryV2{Name: "brand-new", Content: "新加的记忆", Type: MemoryTypeProject}); err != nil {
		t.Fatal(err)
	}
	// fixture 自检（R4-12）：证明"记忆集合确实变了"，否则本反例失去判别力。
	if after := len(ms.buildManifest()); after == beforeManifest {
		t.Fatalf("fixture 失效：Write 后 manifest 仍为 %d 条 ⇒ 新记忆未被 List 收录，反例空转", after)
	}

	if _, err := ms.Select(context.Background(), "输入 A"); err != nil {
		t.Fatal(err)
	}
	waitCallCount(t, client, before+1)
}
