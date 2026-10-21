package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// ── P0 · 记忆选择观测落盘 ──────────────────────────────────────
//
// 目的：在**零行为变化**的前提下，把「每次记忆选择到底发生了什么」落盘成
// 只写不读的 JSONL，作为后续所有记忆改造（地板层 / 检索工具 / 常驻卡片）的
// 判据数据源。没有它，后面每一次调参都是盲的。
//
// 落盘位置 = {store.Dir()}/selection_trace.jsonl。
// · 与记忆同域（天然按 workDir hash 隔离，A 仓库的选择不会记到 B 仓库）
// · 目录 0700、文件 0600（与 ~/.whale/memory 同权限，不含 logs 的 0755）
// · 后缀 .jsonl 不是 .md ⇒ 不会被 MemoryStore.List() 扫成一条"记忆"（已核 memory_store.go:160-165）
//
// ★ 判别法：D1/D2 在**现状**下必红（trace 文件根本不存在）；
// D3/D4 是**守卫**（改动前后都应绿），防"观测把行为改坏了"。

// p0FailClient：Stream 直接报错，代表"没有可用 AI"。
// 用于让后台预热**可控地失败**（避免真实网络，也避免 nil client 引发的意外）。
type p0FailClient struct{}

func (c *p0FailClient) Stream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	return nil, fmt.Errorf("p0 probe: no network available")
}

func (c *p0FailClient) Model() string   { return "p0-fail" }
func (c *p0FailClient) SetModel(string) {}
func (c *p0FailClient) Close() error    { return nil }

// p0Fixture 建一个真实 MemoryStore（临时目录）+ n 条记忆，返回 store / index / tracePath。
func p0Fixture(t *testing.T, n int) (*MemoryStore, *MemoryIndex, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewMemoryStore(dir, "/p0/probe/workdir")
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("mem%02d", i)
		if err := store.Write(MemoryEntryV2{
			Name:        name,
			Description: "desc-" + name,
			Type:        MemoryTypeUser,
		}); err != nil {
			t.Fatalf("写记忆 %s: %v", name, err)
		}
	}
	idx := NewMemoryIndex(store)
	tracePath := filepath.Join(store.Dir(), "selection_trace.jsonl")
	return store, idx, tracePath
}

// readTrace 读回 trace 的每一行并解析为 map（测试侧只读，不假设字段之外的结构）。
func readTrace(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("P0 判据红：Select 未落盘观测 trace（%s）：%v", path, err)
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		t.Fatalf("P0 判据红：trace 文件存在但为空（%s）", path)
	}
	var events []map[string]any
	for i, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("trace 第 %d 行不是合法 JSON：%v\n原始行：%s", i+1, err, line)
		}
		events = append(events, ev)
	}
	return events
}

func findBranch(events []map[string]any, branch string) map[string]any {
	for _, ev := range events {
		if ev["branch"] == branch {
			return ev
		}
	}
	return nil
}

// D1（主判据，现状必红）：条目 ≤ maxSelections 时走**同步全量**分支，
// 必须落盘一行 branch=sync_all，并带上 manifest 事实。
func TestP0_TraceWrittenOnSyncAllBranch(t *testing.T) {
	store, idx, tracePath := p0Fixture(t, 3)
	sel := NewMemorySelector(store, idx, nil)

	if _, err := sel.Select(context.Background(), "hello"); err != nil {
		t.Fatalf("Select: %v", err)
	}

	events := readTrace(t, tracePath)
	if len(events) != 1 {
		t.Fatalf("期望 1 行 trace，实得 %d 行", len(events))
	}
	ev := events[0]
	if ev["branch"] != "sync_all" {
		t.Fatalf("期望 branch=sync_all，实得 %v", ev["branch"])
	}
	if n, _ := ev["manifest_n"].(float64); int(n) != 3 {
		t.Fatalf("期望 manifest_n=3，实得 %v", ev["manifest_n"])
	}
	// manifest 明细必须够离线分析：至少含每条记忆的 name/type。
	man, ok := ev["manifest"].([]any)
	if !ok || len(man) != 3 {
		t.Fatalf("期望 manifest 明细 3 条，实得 %v", ev["manifest"])
	}
	first, _ := man[0].(map[string]any)
	if first["name"] == "" || first["type"] == "" {
		t.Fatalf("manifest 明细缺 name/type：%v", first)
	}
}

// D2（现状必红）：条目 > maxSelections 时走**兜底 + 后台预热**分支，
// 同步返回前必须已落盘一行 branch=fallback_warmup（兜底也只取 top-K）。
func TestP0_TraceRecordsFallbackBranch(t *testing.T) {
	store, idx, tracePath := p0Fixture(t, 7)
	sel := NewMemorySelector(store, idx, &p0FailClient{})

	if _, err := sel.Select(context.Background(), "hello"); err != nil {
		t.Fatalf("Select: %v", err)
	}

	ev := findBranch(readTrace(t, tracePath), "fallback_warmup")
	if ev == nil {
		t.Fatalf("未落盘 fallback_warmup 分支事件")
	}
	if n, _ := ev["manifest_n"].(float64); int(n) != 7 {
		t.Fatalf("期望 manifest_n=7，实得 %v", ev["manifest_n"])
	}
	// 兜底取 top-K（K=maxSelections=5）
	if n, _ := ev["selected_n"].(float64); int(n) != 5 {
		t.Fatalf("兜底应取 top-%d，实得 selected_n=%v", 5, ev["selected_n"])
	}
	if started, _ := ev["warmup_started"].(bool); !started {
		t.Fatalf("期望 warmup_started=true，实得 %v", ev["warmup_started"])
	}
}

// D3（守卫）：trace 文件**不得**被 List() 当成一条记忆。
// 这条在改动前后都必须绿 —— 它是"观测污染记忆"的判别器。
func TestP0_TraceDoesNotPolluteManifest(t *testing.T) {
	store, idx, tracePath := p0Fixture(t, 3)
	sel := NewMemorySelector(store, idx, nil)

	if _, err := sel.Select(context.Background(), "hello"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	// 先确认 trace 确实落了盘（否则本守卫无意义）
	if _, err := os.Stat(tracePath); err != nil {
		t.Fatalf("前置不成立：trace 未落盘，守卫无意义：%v", err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("trace 污染了记忆扫描：期望 3 条，实得 %d 条", len(list))
	}
}

// D4（守卫）：观测**不得**改变 Select 的产出。
// 同一输入连调两次（第一次走同步分支、第二次走缓存），产出必须逐字节相同。
func TestP0_TraceDoesNotChangeSelectOutput(t *testing.T) {
	store, idx, _ := p0Fixture(t, 3)
	sel := NewMemorySelector(store, idx, nil)

	first, err := sel.Select(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Select#1: %v", err)
	}
	if first == "" {
		t.Fatalf("Select 产出为空，判据无意义")
	}
	second, err := sel.Select(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Select#2: %v", err)
	}
	if first != second {
		t.Fatalf("观测改变了 Select 产出：\n first=%q\nsecond=%q", first, second)
	}
}
