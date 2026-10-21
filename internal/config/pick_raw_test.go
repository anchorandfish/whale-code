package config

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 判据（先红）：SelectRelevant 的 AI 原始响应必须落一条 pick_raw 观测。
//
// 现场动机（P3-1b 连带发现，2026-10-08）：warmup_backfill 分支两次真机运行
// `selected=None`（warmup_ok=true）—— AI 那次调用「成功」但没选出任何条目。
// 无法从现有观测区分是「模型返了空串」还是「返了无数字的散文」
// （后者 parseSelectedIndices 会静默解析出 0 条）。本判据把模型原始响应
// 落进 selection_trace.jsonl（branch=ai_pick，只写不读、零行为变化），
// 下一次真机复现即可定因。
//
// ★ 判据即观测面：读 {store.Dir()}/selection_trace.jsonl 找 branch=ai_pick 行。

func w045ReadTrace(t *testing.T, dir string) []map[string]interface{} {
	t.Helper()
	path := filepath.Join(dir, "selection_trace.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("观测文件应已落盘: %v", err)
	}
	defer f.Close()
	var lines []map[string]interface{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("观测行应为合法 JSON: %v（line=%q）", err, line)
		}
		lines = append(lines, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读观测文件失败: %v", err)
	}
	return lines
}

func w045FindPick(lines []map[string]interface{}) map[string]interface{} {
	for _, m := range lines {
		if m["branch"] == traceBranchAIPick {
			return m
		}
	}
	return nil
}

func w045Manifest() []MemoryManifestEntry {
	now := time.Now()
	mk := func(name, desc string, chars int) MemoryManifestEntry {
		return MemoryManifestEntry{Filename: name + ".md", Name: name, Description: desc, Type: MemoryTypeUser, Modified: now, SizeChars: chars}
	}
	return []MemoryManifestEntry{
		mk("auth", "认证偏好", 100),
		mk("api", "API 约定", 200),
		mk("style", "编码风格", 150),
		mk("deploy", "部署流程", 300),
		mk("test", "testing strategy", 250),
		mk("db", "数据库配置", 180),
	}
}

// ① 模型返「无数字散文」⇒ pick_raw 必须记录散文原文（这正是 要分辨的形态）。
func TestW045_PickRawRecordsProseResponse(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir, "w045-prose")
	if err != nil {
		t.Fatalf("precondition: 创建 MemoryStore 失败: %v", err)
	}
	selector := NewMemorySelector(store, nil, &mockAIClient{response: "抱歉，我无法从清单中确定相关条目，请提供更多信息。"})
	selector.SetMaxSelections(2)

	result, err := selector.SelectRelevant(context.Background(), "查询", w045Manifest(), nil)
	if err != nil {
		t.Fatalf("SelectRelevant 不应报错: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("precondition: 无数字散文应解析出 0 条，实际 %d 条", len(result))
	}

	pick := w045FindPick(w045ReadTrace(t, filepath.Join(store.Dir())))
	if pick == nil {
		t.Fatal("W-045①：应存在 branch=ai_pick 的观测行（现在没有 ⇒ 观测盲区）")
	}
	raw, _ := pick["pick_raw"].(string)
	if raw == "" {
		t.Fatal("W-045①：pick_raw 应记录模型原始响应，实际为空")
	}
	if !strings.Contains(raw, "无法从清单") {
		t.Fatalf("W-045①：pick_raw 应含散文原文，实际=%q", raw)
	}
	if n, ok := pick["manifest_n"].(float64); !ok || int(n) != 6 {
		t.Fatalf("W-045①：manifest_n 应为 6（关联 warmup 事件的依据），实际=%v", pick["manifest_n"])
	}
}

// ② 模型返数字 ⇒ pick_indices 记录解析结果，且零行为变化（选择结果与旧逻辑一致）。
func TestW045_PickRawRecordsIndices(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir, "w045-num")
	if err != nil {
		t.Fatalf("precondition: 创建 MemoryStore 失败: %v", err)
	}
	selector := NewMemorySelector(store, nil, &mockAIClient{response: "1\n3\n"})
	selector.SetMaxSelections(5)

	result, err := selector.SelectRelevant(context.Background(), "如何implementationUser认证？", w045Manifest(), []string{"read_file"})
	if err != nil {
		t.Fatalf("SelectRelevant 返回错误: %v", err)
	}
	if len(result) != 2 || result[0].Name != "auth" || result[1].Name != "style" {
		t.Fatalf("零行为变化：AI 选 1/3 应得 [auth style]，实际 %+v", result)
	}

	pick := w045FindPick(w045ReadTrace(t, filepath.Join(store.Dir())))
	if pick == nil {
		t.Fatal("W-045②：应存在 branch=ai_pick 的观测行")
	}
	idxRaw, ok := pick["pick_indices"].([]interface{})
	if !ok || len(idxRaw) != 2 {
		t.Fatalf("W-045②：pick_indices 应记录解析出的 [0 2]，实际=%v", pick["pick_indices"])
	}
	// parseSelectedIndices 的返回口径是 0-based（1-based → 0-based 已在解析内完成），
	// 落盘值与其返回值同源，不再二次换算。
	if int(idxRaw[0].(float64)) != 0 || int(idxRaw[1].(float64)) != 2 {
		t.Fatalf("W-045②：pick_indices 应为 [0 2]（0-based，与解析返回同源），实际=%v", pick["pick_indices"])
	}
}

// ③ 守卫：观测条数恒定 —— 一次调用只落一条 ai_pick（防把落盘写进循环）。
func TestW045_PickRawSingleLinePerCall(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir, "w045-once")
	if err != nil {
		t.Fatalf("precondition: 创建 MemoryStore 失败: %v", err)
	}
	selector := NewMemorySelector(store, nil, &mockAIClient{response: "1\n"})
	selector.SetMaxSelections(2)

	if _, err := selector.SelectRelevant(context.Background(), "查询", w045Manifest(), nil); err != nil {
		t.Fatalf("SelectRelevant 返回错误: %v", err)
	}

	n := 0
	for _, m := range w045ReadTrace(t, filepath.Join(store.Dir())) {
		if m["branch"] == traceBranchAIPick {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("W-045③：一次调用应恰好落 1 条 ai_pick，实际 %d 条", n)
	}
}
