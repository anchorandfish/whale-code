package config

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/testutil"
)

// ── · 首轮不等：冷启动不得在请求路径上等 AI ─────────────
//
// 与 的分界：
// （`memory_select_cache_test.go`）治的是「界面冻结」——把 buildRequest
// 挪出主循环；本文件治的是「首 token 要等约 17 秒」——冷启动首次未命中缓存时，
// `Select` 仍**同步等**一次真实 LLM 往返（真机 `buildRequest.total 17.43s`）。
//
// A 方案：需要 AI 时先返回**本地兜底**（entrypoint + 按最近更新取 top-K），
// 同时后台起一次真 AI 选择写缓存 ⇒ 次轮起命中「AI 选」的结果。
//
// ★ 判据的判别法：用**阻塞式假客户端**模拟慢 LLM。
// 若实现仍是"同步等 AI"，`Select` 会一直不返回 ⇒ 主判据红。
// 若把 AI 选择**丢掉**（只做兜底、不预热），反例红。
// 两条一起，才同时钉住"不等"和"没丢"。

// w039BlockingClient：Stream 立刻返回通道，但**直到 release 关闭才产出** ——
// 用来模拟一次慢 LLM 往返（真机约 17s，判据里不能真等 17s）。
type w039BlockingClient struct {
	calls   int64
	release chan struct{}
	reply   string // release 之后吐出的文本，喂给 parseSelectedIndices
}

func (c *w039BlockingClient) Stream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	atomic.AddInt64(&c.calls, 1)
	ch := make(chan ai.Event)
	go func() {
		defer close(ch)
		select {
		case <-c.release:
		case <-ctx.Done():
			return
		}
		select {
		case ch <- ai.TextDeltaEvent{Content: c.reply}:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

func (c *w039BlockingClient) Model() string   { return "blocking-fake" }
func (c *w039BlockingClient) SetModel(string) {}
func (c *w039BlockingClient) Close() error    { return nil }

func (c *w039BlockingClient) count() int64 { return atomic.LoadInt64(&c.calls) }

// newBlockingFixture 造「确实需要 AI」的选择器：manifest 4 条 > maxSelections 2。
func newBlockingFixture(t *testing.T, release chan struct{}, reply string) (*MemorySelector, *w039BlockingClient) {
	t.Helper()
	// ⚠️ 基座目录不能用 t.TempDir()：预热是**后台协程**，用例结束后仍可能写缓存目录，
	// 而 t.TempDir() 的清理会断言"目录已空" ⇒ 非确定性红
	// （实测：`TempDir RemoveAll cleanup: unlinkat .../d3471f16...: directory not empty`）。
	// 修法 = testutil.IsolatedHomeDir（宽松清理）；workDir 也按用例隔离，避免同一 /tmp 固定
	// 路径让不同用例共享 project scope 缓存（那是另一类串味）。
	store, err := NewMemoryStore(testutil.IsolatedHomeDir(t), testutil.IsolatedHomeDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := store.Write(MemoryEntryV2{
			Name:    "mem-" + strconv.Itoa(i),
			Content: "memory body " + strconv.Itoa(i),
			Type:    MemoryTypeProject,
		}); err != nil {
			t.Fatal(err)
		}
	}
	client := &w039BlockingClient{release: release, reply: reply}
	ms := NewMemorySelector(store, nil, client)
	ms.maxSelections = 2

	// fixture 自检（R4-12）：若没超过上限，`Select` 根本不会走 AI 分支，
	// 本文件所有判据都会退化成"测了个不存在的东西"。
	if n := len(ms.buildManifest()); n <= ms.maxSelections {
		t.Fatalf("fixture 失效：manifest=%d 未超过 maxSelections=%d ⇒ 判据失去判别力", n, ms.maxSelections)
	}
	return ms, client
}

// TestW039_ColdStartDoesNotBlockOnAISelection 是**主判据**：
// 冷启动首次 Select 在 AI 客户端阻塞时仍必须立刻返回本地兜底。
func TestW039_ColdStartDoesNotBlockOnAISelection(t *testing.T) {
	release := make(chan struct{})
	ms, _ := newBlockingFixture(t, release, "1")
	defer close(release) // 放行可能已在飞的预热，避免 goroutine 挂在测试之后

	type res struct {
		out string
		err error
	}
	done := make(chan res, 1)
	go func() {
		out, err := ms.Select(context.Background(), "第一句话")
		done <- res{out, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("冷启动 Select 出错: %v", r.err)
		}
		if strings.TrimSpace(r.out) == "" {
			t.Fatal("冷启动必须返回非空本地兜底（否则首轮会丢掉全部记忆）")
		}
		if !strings.Contains(r.out, "=== User Preference Memory ===") {
			t.Fatalf("兜底应带上本地选出的记忆条目；实得:\n%s", r.out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Select 在 AI 选择上阻塞 ⇒ 首轮仍要等 LLM（ 未生效）")
	}
}

// TestW039_WarmUpReplacesFallbackWithAISelection 是反例之一：
// 证明"不等"**不是**"把 AI 选择丢掉"——预热完成后，缓存必须换成 AI 的结果。
//
// 造法：让假 AI 回一段**没有数字**的文本 ⇒ 真 AI 路径选出 0 条。
// 于是两条路径在输出上结构可判：
//
//	兜底 = entrypoint + "=== User Preference Memory ===" + 2 条
//	AI = entrypoint（无记忆段）
//
// 预热完成后第二次 Select 若仍是兜底形态 ⇒ 结果没进缓存 ⇒ 红。
func TestW039_WarmUpReplacesFallbackWithAISelection(t *testing.T) {
	release := make(chan struct{})
	ms, client := newBlockingFixture(t, release, "no match")

	first, err := ms.Select(context.Background(), "第一句话")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "=== User Preference Memory ===") {
		t.Fatalf("冷启动应给本地兜底（带记忆段）；实得:\n%s", first)
	}

	close(release) // 放行后台预热

	// 必须等**缓存被写回**（不是等 Stream 被调用）——预热是异步的，直接断言会假红。
	var second string
	deadline := time.Now().Add(5 * time.Second)
	for {
		second, err = ms.Select(context.Background(), "第一句话")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(second, "=== User Preference Memory ===") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("预热完成后第二次 Select 仍是本地兜底 ⇒ AI 结果没写进缓存；实得:\n%s", second)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 同一 key 的预热只许起一次 —— 否则"省一次调用"会变成"每轮放大一次"。
	if got := client.count(); got != 1 {
		t.Fatalf("同一 key 的预热应恰好发起 1 次 AI 调用，实得 %d", got)
	}
}

// TestW039_NoAICallWhenManifestFitsUnderLimit 是反例之二：
// 条目不过上限时本地就能算完，**不得**平白引入一次 AI 调用（异步面不能被滥用）。
func TestW039_NoAICallWhenManifestFitsUnderLimit(t *testing.T) {
	// 同上：基座与 workDir 都按用例隔离（预热协程可能写到用例之后）。
	store, err := NewMemoryStore(testutil.IsolatedHomeDir(t), testutil.IsolatedHomeDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write(MemoryEntryV2{Name: "only-one", Content: "唯一一条记忆", Type: MemoryTypeProject}); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	defer close(release)
	client := &w039BlockingClient{release: release, reply: "1"}
	ms := NewMemorySelector(store, nil, client) // maxSelections 默认 5，manifest 仅 1 条

	// fixture 自检：必须真的没超过上限，否则本反例测的不是它想测的路径。
	if n := len(ms.buildManifest()); n > ms.maxSelections {
		t.Fatalf("fixture 失效：manifest=%d 已超过 maxSelections=%d", n, ms.maxSelections)
	}

	out, err := ms.Select(context.Background(), "随便一句话")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "唯一一条记忆") {
		t.Fatalf("条目不过上限时应本地全量注入；实得:\n%s", out)
	}

	time.Sleep(50 * time.Millisecond) // 给"万一起了后台 goroutine"冒头的时间
	if got := client.count(); got != 0 {
		t.Fatalf("条目不过上限却在后台发起了 %d 次 AI 调用 ⇒ 异步面被滥用", got)
	}
}
