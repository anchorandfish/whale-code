package tools

// p4_memory_search_test.go — P4：`memory_search` 工具（模型**主动**检索记忆）。
//
// 背景（P3-1b 收口后遗留的缺口）：注入路径是**被动**的（每轮最多 5 条），
// 对话中的模型没有任何"搜记忆"工具 —— 相关记忆没被注入时它无法自己去找。
// 对标 同类 CLI（对标证据件：memories.search/read/list 工具 + 摘要注入），
// 本步只做**只读检索工具**（写路径已有 AutoMemoryExtractor，不在此扩）。
//
// 架构约束（本仓既有纪律，实现必须遵守）：
//   - tools→memory 循环依赖禁止 ⇒ 检索能力经**函数字段注入**（对齐
//     CapabilityLookup / ReflectTrigger 先例，executor.go:165-180），接线层（tui）装配；
//   - 工具面 = 能力面（S3 裁定 C=②）：spec（tools.go）/ handler（executor.go）/
//     能力分桶（permission/capability.go）三处同批落地，否则覆盖守卫红；
//   - 零命中是**正常情况**不是错误（对齐 MemoryRanker 契约：词法检索有语义上限）。
//
// 判别器设计（先红后绿；实现缺任何一块本文件编译期/断言即红）：
//   1. spec 面：注册表可见 + 只读档 + 并发安全 + query 必填；
//   2. handler 面：注入假 MemorySearchFunc ⇒ 输出含命中条目；query 透传；
//   3. 未接线：MemorySearchFunc=nil ⇒ 明确"not wired"软失败（不 panic、不假成功）；
//   4. 单调性：零命中 ⇒ IsError=false（模型可继续正常对话）；
//   5. 钳制：topK 缺省 5、>10 收敛到 10（防模型一次拖全库）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ---- 测试替身：记录调用参数的可注入假检索函数 ----

type p4FakeSearch struct {
	gotQuery string
	gotTopK  int
	hits     []MemorySearchHit
}

func (f *p4FakeSearch) fn(ctx context.Context, query string, topK int) ([]MemorySearchHit, error) {
	f.gotQuery = query
	f.gotTopK = topK
	return f.hits, nil
}

func p4NewExecutorWithFake(t *testing.T, fake *p4FakeSearch) *Executor {
	t.Helper()
	e := NewExecutor(NewRegistry(), t.TempDir())
	e.MemorySearchFunc = fake.fn
	return e
}

// ---- 1. spec 面 ----

func TestP4_MemorySearch_SpecFace(t *testing.T) {
	reg := NewRegistry()
	spec, ok := reg.Get("memory_search")
	if !ok {
		t.Fatal("memory_search spec 未注册 —— registerBuiltinTools 缺条目（工具面承诺缺失）")
	}
	if spec.RequiredPermission != LevelReadOnly {
		t.Errorf("RequiredPermission = %v, 期望 LevelReadOnly（纯读，不写盘不跑命令）", spec.RequiredPermission)
	}
	if !spec.ConcurrencySafe {
		t.Error("ConcurrencySafe = false, 期望 true（纯读检索可并发）")
	}
	if spec.Schema.Type != "object" {
		t.Fatalf("Schema.Type = %q, 期望 object", spec.Schema.Type)
	}
	if spec.Schema.Properties["query"] == nil {
		t.Fatal("schema 缺 query 属性")
	}
	reqOK := false
	for _, r := range spec.Schema.Required {
		if r == "query" {
			reqOK = true
		}
	}
	if !reqOK {
		t.Errorf("schema.Required = %v, 必须含 query", spec.Schema.Required)
	}
	if spec.Desc.Purpose == "" || !strings.Contains(strings.ToLower(spec.Desc.Purpose), "memory") {
		t.Errorf("Desc.Purpose 未描述记忆检索语义: %q", spec.Desc.Purpose)
	}
}

// ---- 2. handler 面：命中输出 + 参数透传 ----

func TestP4_MemorySearch_Handler_ReturnsHits(t *testing.T) {
	fake := &p4FakeSearch{hits: []MemorySearchHit{
		{Name: "tui超时归因", Type: "project", Description: "tui 全包超时 = CPU 争用，非回归",
			Path: "/tmp/x/tui.md", Updated: "2026-10-08"},
		{Name: "判据先红", Type: "user", Description: "守卫测试必须先红后绿",
			Path: "/tmp/y/red.md", Updated: "2026-10-07"},
	}}
	e := p4NewExecutorWithFake(t, fake)

	result := e.Execute(context.Background(), Call{
		ID:   "p4-1",
		Name: "memory_search",
		Input: mustMarshal(map[string]any{
			"query": "tui 超时",
			"top_k": 3,
		}),
	})
	if result.IsError {
		t.Fatalf("memory_search 报错: %s", result.Output)
	}
	if fake.gotQuery != "tui 超时" {
		t.Errorf("query 未透传: got %q", fake.gotQuery)
	}
	if fake.gotTopK != 3 {
		t.Errorf("topK 未透传: got %d, 期望 3", fake.gotTopK)
	}
	for _, want := range []string{"tui超时归因", "CPU 争用", "tui.md", "判据先红"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("输出缺 %q；actual:\n%s", want, result.Output)
		}
	}
}

// ---- 3. 未接线：软失败且语义明确 ----

func TestP4_MemorySearch_Handler_NotWired_SoftFail(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir()) // 不注入 MemorySearchFunc
	result := e.Execute(context.Background(), Call{
		ID:    "p4-2",
		Name:  "memory_search",
		Input: mustMarshal(map[string]any{"query": "任意"}),
	})
	if !result.IsError {
		t.Fatalf("未接线时应报错，actual IsError=false output=%q", result.Output)
	}
	if !strings.Contains(result.Output, "not wired") && !strings.Contains(result.Output, "not available") {
		t.Errorf("错误信息应说明'未接线/不可用'，actual: %s", result.Output)
	}
}

// ---- 4. 零命中 = 正常情况，不是错误 ----

func TestP4_MemorySearch_Handler_ZeroHits_NoError(t *testing.T) {
	fake := &p4FakeSearch{hits: nil}
	e := p4NewExecutorWithFake(t, fake)
	result := e.Execute(context.Background(), Call{
		ID:    "p4-3",
		Name:  "memory_search",
		Input: mustMarshal(map[string]any{"query": "完全不相干的查询"}),
	})
	if result.IsError {
		t.Fatalf("零命中应是正常输出（MemoryRanker 契约），actual error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "0") && !strings.Contains(strings.ToLower(result.Output), "no ") {
		t.Errorf("零命中输出应可判读（含 0 / no hits），actual: %s", result.Output)
	}
}

// ---- 5. 钳制：缺省 5、上限 10 ----

func TestP4_MemorySearch_Handler_TopKClamp(t *testing.T) {
	cases := []struct {
		in   int // 0 = 缺省
		want int
	}{
		{in: 0, want: 5},
		{in: 3, want: 3},
		{in: 999, want: 10},
	}
	for _, tc := range cases {
		fake := &p4FakeSearch{}
		e := p4NewExecutorWithFake(t, fake)
		input := map[string]any{"query": "q"}
		if tc.in != 0 {
			input["top_k"] = tc.in
		}
		result := e.Execute(context.Background(), Call{
			ID:    "p4-4",
			Name:  "memory_search",
			Input: mustMarshal(input),
		})
		if result.IsError {
			t.Fatalf("top_k=%d 报错: %s", tc.in, result.Output)
		}
		if fake.gotTopK != tc.want {
			t.Errorf("top_k=%d ⇒ 传给检索层 %d, 期望 %d", tc.in, fake.gotTopK, tc.want)
		}
	}
}

// ---- 6. 空查询：输入校验拒绝（不透传给检索层） ----

func TestP4_MemorySearch_Handler_EmptyQuery(t *testing.T) {
	fake := &p4FakeSearch{}
	e := p4NewExecutorWithFake(t, fake)
	result := e.Execute(context.Background(), Call{
		ID:    "p4-5",
		Name:  "memory_search",
		Input: mustMarshal(map[string]any{"query": "   "}),
	})
	if !result.IsError {
		t.Fatal("空 query 应被输入校验拒绝")
	}
	if fake.gotQuery != "" {
		t.Errorf("空 query 不应到达检索层, got %q", fake.gotQuery)
	}
}

// ---- 7. 判别器自检（反例注入）：schema/handler 断言不是恒绿装饰 ----
//
// 若把 spec 从注册表摘掉 ⇒ TestP4_MemorySearch_SpecFace 红（这是它的红态，由
// 编译期+断言保证）；若 handler 改名/未注册 ⇒ Execute 落"未知工具"出口、
// TestP4_MemorySearch_Handler_ReturnsHits 红。此处钉一条反例：**未知工具路径**
// 与"not wired"路径必须可区分（前者连 handler 都没有）。

func TestP4_MemorySearch_UnknownTool_DistinctFromNotWired(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())
	result := e.Execute(context.Background(), Call{
		ID:    "p4-6",
		Name:  "memory_search_typo",
		Input: mustMarshal(map[string]any{"query": "q"}),
	})
	if !result.IsError {
		t.Fatal("未知工具应报错")
	}
	// 反例通道活着：确认它走的是"未知工具"出口而不是我们的 handler。
	if strings.Contains(result.Output, "not wired") {
		t.Error("typo 工具名不该命中 memory_search handler（判别器退化）")
	}
}

// ---- 8. spec JSON 序列化面（模型可见面）含 memory_search ----

func TestP4_MemorySearch_DefinitionsContainTool(t *testing.T) {
	reg := NewRegistry()
	defs := reg.Definitions()
	found := false
	for _, d := range defs {
		if name, _ := d["name"].(string); name == "memory_search" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Definitions()（模型可见 tools 数组）不含 memory_search —— 模型根本看不到它")
	}
}

// mustMarshal 局部别名：search_test.go 已有同名 helper，同包直接复用；
// 为防将来 helper 改名导致本文件编译失败，这里兜底一个（仅在未定义时生效不可行，
// Go 同包不可重名 —— 故直接依赖既有 helper，此处仅注释说明）。
var _ = json.Marshal
