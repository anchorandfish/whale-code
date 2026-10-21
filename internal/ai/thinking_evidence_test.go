// Package ai — B3：thinking 声明的「来源」记录。
//
// ★ 本批的结论是 **"不做门"**。B3 的原始设计（标 supported 必须带证据、空则门红）
// 经重估被**否决** —— 一个无法核实的 URL 字段做成必填门，随手填任何网址都能过，
// 是"信则灵"的假门，与本仓「判据必须可判别」的纪律相悖。
//
// 落地形态 = **可选**的 `--thinking-evidence <url>`：只**记录**来源、
// 在 `/model info` **展示**；不解析、不验证、不参与判定。
//
// 下列判据**一条语义一个函数**（不是塞成一个大函数）—— 判别性自检时
// 每个 mutant 打出唯一的一个红点，归因才不会含糊：
//
//	可选不强制 ⇒ TestThinkingEvidence_OptionalNotRequired
//	add 路径 ⇒ TestThinkingEvidence_AddPathParsed
//	edit 路径 ⇒ TestThinkingEvidence_EditPathParsed
//	应用层落点 ⇒ TestThinkingEvidence_ApplyModelUpdates
//	持久化契约 ⇒ TestThinkingEvidence_RoundTrip
//	展示面 ⇒ TestFormatModelInfo_ShowsEvidence
package ai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestThinkingEvidence_OptionalNotRequired 是"**否决必填门**"的机械表达：
// 只声明 --supports-thinking、不记来源，必须**合法且不报错**。
//
// ★ 判别性：把"必填门"加回去（`if cfg.ThinkingEvidence == "" { return err }`）
// 会精确打红本函数，而其余判据保持绿。
func TestThinkingEvidence_OptionalNotRequired(t *testing.T) {
	cfg, err := ParseModelAddArgs("kimi-k2 --supports-thinking supported")
	if err != nil {
		t.Fatalf("不带 --thinking-evidence 必须合法（B3 已否决必填门），实际报错: %v", err)
	}
	if cfg.ThinkingEvidence != "" {
		t.Errorf("未指定时 ThinkingEvidence 应为空串，实际 %q", cfg.ThinkingEvidence)
	}
	if cfg.ThinkingSupport != "supported" {
		t.Errorf("同批声明的 ThinkingSupport 丢了，实际 %q", cfg.ThinkingSupport)
	}
}

// TestThinkingEvidence_AddPathParsed 钉住 add 解析路径。
func TestThinkingEvidence_AddPathParsed(t *testing.T) {
	cfg, err := ParseModelAddArgs("kimi-k2 --thinking-evidence https://example.com/docs/thinking")
	if err != nil {
		t.Fatalf("add 解析失败: %v", err)
	}
	if cfg.ThinkingEvidence != "https://example.com/docs/thinking" {
		t.Errorf("add: ThinkingEvidence = %q，期望 %q", cfg.ThinkingEvidence, "https://example.com/docs/thinking")
	}

	// 与 --supports-thinking 同现时必须**两者都在**（flag 顺序无关）。
	cfg, err = ParseModelAddArgs("kimi-k2 --thinking-evidence file:内部工作回执 --supports-thinking unsupported")
	if err != nil {
		t.Fatalf("add 解析失败（两个 flag 同现）: %v", err)
	}
	if cfg.ThinkingEvidence != "file:内部工作回执" || cfg.ThinkingSupport != "unsupported" {
		t.Errorf("两个 flag 同现时有值被吞: evidence=%q support=%q", cfg.ThinkingEvidence, cfg.ThinkingSupport)
	}

	// 缺值必须报错（否则会把下一个 flag 当值吞掉）。
	if _, err := ParseModelAddArgs("kimi-k2 --thinking-evidence"); err == nil {
		t.Error("add: --thinking-evidence 缺值应报错，实际 nil")
	}
}

// TestThinkingEvidence_EditPathParsed 钉住 edit 解析路径。
//
// ★ 与 add 是**分开实现**的（ParseModelAddArgs / ParseModelEditArgs）——
// 只测一条会放过"另一条忘了加"，是本仓的复发坑。
func TestThinkingEvidence_EditPathParsed(t *testing.T) {
	_, updates, err := ParseModelEditArgs("kimi-k2 --thinking-evidence https://example.com/e")
	if err != nil {
		t.Fatalf("edit 解析失败: %v", err)
	}
	if updates["thinking_evidence"] != "https://example.com/e" {
		t.Errorf("edit: updates[thinking_evidence] = %q，期望 %q", updates["thinking_evidence"], "https://example.com/e")
	}
	if _, _, err := ParseModelEditArgs("kimi-k2 --thinking-evidence"); err == nil {
		t.Error("edit: --thinking-evidence 缺值应报错，实际 nil")
	}
}

// TestThinkingEvidence_ApplyModelUpdates 钉住 edit 的落点（公开函数）。
func TestThinkingEvidence_ApplyModelUpdates(t *testing.T) {
	cfg := &ModelConfig{Name: "kimi-k2"}
	if err := ApplyModelUpdates(cfg, map[string]string{"thinking_evidence": "file:内部工作回执"}); err != nil {
		t.Fatalf("ApplyModelUpdates 失败: %v", err)
	}
	if cfg.ThinkingEvidence != "file:内部工作回执" {
		t.Errorf("ApplyModelUpdates: ThinkingEvidence = %q，期望 %q", cfg.ThinkingEvidence, "file:内部工作回执")
	}

	// 空串 = 清除已记来源（合法操作，不报错）。
	if err := ApplyModelUpdates(cfg, map[string]string{"thinking_evidence": ""}); err != nil {
		t.Fatalf("清除来源不应报错: %v", err)
	}
	if cfg.ThinkingEvidence != "" {
		t.Errorf("空串应清除来源，实际 %q", cfg.ThinkingEvidence)
	}
}

// TestThinkingEvidence_RoundTrip 固化持久化**契约**：来源要能写进 models.json
// 再读回，且磁盘上的 key 名必须就是约定的那个。
func TestThinkingEvidence_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")

	reg := NewModelRegistry()
	reg.Add(&ModelConfig{
		Name:             "kimi-k2",
		Provider:         ProviderOpenAI,
		ContextWindow:    131072,
		ThinkingSupport:  "supported",
		ThinkingEvidence: "https://example.com/docs/thinking#declared",
	})
	if err := SaveModels(path, reg); err != nil {
		t.Fatalf("SaveModels 失败: %v", err)
	}

	// ★ 只看"往返自洽"是不够的：写与读共用同一个 struct tag，tag 名写错
	// （如 thinking_evidence_typo）往返**照样成功** —— 判别性自检实测如此
	// （mutant「JSON tag 写错」一度未被抓住）。所以必须看**磁盘上的原始 key**：
	// 它是对外契约（用户手写 models.json 时会用它），改名 = 破坏兼容。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回 models.json 失败: %v", err)
	}
	if !strings.Contains(string(raw), `"thinking_evidence"`) {
		t.Errorf("models.json 必须含约定的 key \"thinking_evidence\"（tag 名是对外契约），实际:\n%s", raw)
	}

	back, err := LoadModels(path)
	if err != nil {
		t.Fatalf("LoadModels 失败: %v", err)
	}
	got := back.Get("kimi-k2")
	if got == nil {
		t.Fatal("往返后模型丢了")
	}
	if got.ThinkingEvidence != "https://example.com/docs/thinking#declared" {
		t.Errorf("往返后 ThinkingEvidence = %q，期望 %q", got.ThinkingEvidence, "https://example.com/docs/thinking#declared")
	}
	if got.ThinkingSupport != "supported" {
		t.Errorf("往返后 ThinkingSupport = %q，期望 supported", got.ThinkingSupport)
	}
}

// TestFormatModelInfo_ShowsEvidence 固化展示面：记了来源就显示一行，没记就不显示。
//
// ★ 后半条不是吹毛求疵 —— "空串"的语义是**没记来源**，不是"没有证据"。
// 若无条件打印，会让用户以为系统替他核过（凭空造出一个不存在的结论）。
func TestFormatModelInfo_ShowsEvidence(t *testing.T) {
	cfg := &ModelConfig{Name: "kimi-k2", Provider: ProviderOpenAI, BaseURL: officialDashScopeURL}

	if out := FormatModelInfo(cfg); strings.Contains(out, "Thinking evidence:") {
		t.Errorf("未记来源时不得显示 evidence 行，实际:\n%s", out)
	}

	cfg.ThinkingEvidence = "https://example.com/docs"
	out := FormatModelInfo(cfg)
	if !strings.Contains(out, "Thinking evidence: https://example.com/docs") {
		t.Errorf("记了来源应显示一行，实际:\n%s", out)
	}
}
