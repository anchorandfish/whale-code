package tools

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// dec047ModelProbe 是 ai.Client 的探针实现。
//
// ★ 用**计数**而不是"最终值"当判据：并发场景下最终值可能因时序巧合而正确，
// 计数是确定性的 —— 只要 tools 层还碰全局 client，SetModel 就必然被调用。
type dec047ModelProbe struct {
	mu       sync.Mutex
	model    string
	setCalls []string
	seen     []string
}

func (c *dec047ModelProbe) Stream(_ context.Context, req ai.Request) (<-chan ai.Event, error) {
	c.mu.Lock()
	c.seen = append(c.seen, req.Model)
	c.mu.Unlock()

	ch := make(chan ai.Event, 1)
	ch <- ai.ErrorEvent{Err: errors.New("probe done")}
	close(ch)
	return ch, nil
}

func (c *dec047ModelProbe) Model() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.model
}

func (c *dec047ModelProbe) SetModel(m string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setCalls = append(c.setCalls, m)
	c.model = m
}

func (c *dec047ModelProbe) Close() error { return nil }

func (c *dec047ModelProbe) snapshot() (sets []string, seen []string, model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.setCalls...), append([]string(nil), c.seen...), c.model
}

// TestDEC047_SubAgentModel_NoGlobalRewrite 走生产入口（AgentTask.Execute），
// 验证子 agent 的 model 覆盖**不再改写全局 client**。
//
// 判别性：修复前 runMultiRound 会 SetModel(input.Model) + defer SetModel(original)
// ⇒ setCalls 长度 2，本用例第一条断言必红。
// 同时断言请求真的带上了声明的 model（防"消除全局改写"变成"覆盖完全失效"）。
func TestDEC047_SubAgentModel_NoGlobalRewrite(t *testing.T) {
	probe := &dec047ModelProbe{model: "global-model"}

	executor := NewExecutor(NewRegistry(), "")
	executor.AIClient = probe

	task := &AgentTask{
		id:       generateAgentUUID(),
		depth:    1,
		status:   TaskPending,
		executor: executor,
	}

	parentCtx := context.Background()
	if _, err := task.Execute(context.Background(), AgentInput{
		Prompt:    "say hi",
		Model:     "sub-model",
		ParentCtx: &parentCtx,
	}); err != nil {
		// 探针流以 ErrorEvent 结束，Execute 可能返回错误；这里只关心 model 传递与全局状态。
		t.Logf("Execute 返回（预期探针中断）: %v", err)
	}

	sets, seen, finalModel := probe.snapshot()

	if len(sets) != 0 {
		t.Fatalf("tools 层改写了全局 client model：SetModel 被调用 %d 次 %v（期望 0 次）。\n"+
			"⇒  要求模型覆盖走请求级 ai.Request.Model，而不是进程级全局状态。",
			len(sets), sets)
	}
	if finalModel != "global-model" {
		t.Fatalf("全局 model 被污染：%q，期望 %q", finalModel, "global-model")
	}
	if len(seen) == 0 {
		t.Fatal("探针没有收到任何请求 ⇒ 本用例未覆盖被测路径（Stream 未被调用）")
	}
	if seen[0] != "sub-model" {
		t.Fatalf("请求 model = %q，期望 sub-model（seen=%v）—— 覆盖没有传到请求层", seen[0], seen)
	}
}

// TestDEC047_ConcurrentSubAgents_ModelsDoNotInterfere 并发派 3 个不同 model 的子任务。
//
// 判别性：修复前每个任务各 SetModel 两次 ⇒ setCalls 至少 6 次（且互相覆盖全局）；
// 修复后 0 次，且三个请求各自带上自己的 model。
// 这条覆盖的是"并发"这个真实故障面，而不只是单任务的静态写法。
func TestDEC047_ConcurrentSubAgents_ModelsDoNotInterfere(t *testing.T) {
	probe := &dec047ModelProbe{model: "global-model"}

	executor := NewExecutor(NewRegistry(), "")
	executor.AIClient = probe

	models := []string{"model-a", "model-b", "model-c"}

	var wg sync.WaitGroup
	for _, m := range models {
		wg.Add(1)
		go func(model string) {
			defer wg.Done()
			task := &AgentTask{
				id:       generateAgentUUID(),
				depth:    1,
				status:   TaskPending,
				executor: executor,
			}
			parentCtx := context.Background()
			_, _ = task.Execute(context.Background(), AgentInput{
				Prompt:    "probe " + model,
				Model:     model,
				ParentCtx: &parentCtx,
			})
		}(m)
	}
	wg.Wait()

	sets, seen, finalModel := probe.snapshot()

	if len(sets) != 0 {
		t.Fatalf("并发子任务改写了全局 client model：SetModel 调用 %d 次 %v（期望 0 次）",
			len(sets), sets)
	}
	if finalModel != "global-model" {
		t.Fatalf("并发后全局 model = %q，期望 global-model（被污染）", finalModel)
	}

	got := append([]string(nil), seen...)
	sort.Strings(got)
	want := append([]string(nil), models...)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("请求次数 = %d %v，期望 %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("请求 model 集合 = %v，期望 %v —— 有任务的 model 被别的任务覆盖", got, want)
		}
	}
}
