// audit_gc_test.go — G-C 落盘验收（delta F 侧）：C6 降级 sink / C8 脱敏复检 / C1 Actor+Seq。
//
// 本文件只用**既有**符号（NewApprovalAudit / ApprovalRecord / Record / splitAuditLines …），
// 因此同一份测试能在"after"与"单点回退版 before"两颗构建上同时编译，并给出可判别对照：
//
//	C6：after 主 sink 写失败 ⇒ stderr 出现 [approval-audit-degraded] 行；before（摘掉
//	    `a.degrade(...)` 调用）⇒ 该断言精确打红。
//	C8：after 尾残形状入参 ⇒ 落盘 tool_input 必须是 [REDACTED-BLOB]；before（摘掉
//	    `|| residualSecretTail(redacted)` 复检）⇒ 该断言精确打红。
//	C1：after 条目含 actor/seq；before（audit.go 回退到 7 字段 schema）⇒ 打红。
package bridge

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/secret"
)

// gcAuditEntries 读回审计文件里的全部 JSONL 行。
func gcAuditEntries(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读审计文件失败: %v", err)
	}
	var out []map[string]any
	for _, line := range splitAuditLines(string(data)) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("审计行不是合法 JSON: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

// gcCaptureStderr 在 fn 执行期间把 os.Stderr 接到管道上，返回捕获到的内容。
//
// 生产实现 `(*approvalAudit).fallbackWriter()` 在**写入时**读取 os.Stderr
// （不是构造时快照），因此这里能可靠捕获到降级输出。
func gcCaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	data, err := io.ReadAll(r)
	_ = r.Close()
	os.Stderr = old
	if err != nil {
		t.Fatalf("读 stderr 管道失败: %v", err)
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// C6 · 审计写失败：不阻断，但必须可见（返回 error + 降级到 stderr 备用 sink）
// ---------------------------------------------------------------------------

// TestDeltaF_T7_PrimarySinkFailureDegradesToStderr 主 sink 不可写时的降级出口。
func TestDeltaF_T7_PrimarySinkFailureDegradesToStderr(t *testing.T) {
	dir := t.TempDir()
	// 让主 sink 必然失败：把"父目录"做成普通文件 ⇒ Log 的 os.MkdirAll 返回 ENOTDIR。
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "audit.jsonl")

	const plainKey = "sk-ant-api03-AAAABBBBCCCCDDDDEEEEFFFF"
	sink := NewApprovalAudit(path)

	var recordErr error
	captured := gcCaptureStderr(t, func() {
		recordErr = sink.Record(ApprovalRecord{
			Origin:    OriginTUISubagent,
			Tool:      "bash",
			ToolInput: `{"command":"echo ` + plainKey + `"}`,
			Decision:  "approved",
			Source:    "user",
		})
	})

	// ① 不阻断：审批结果不受影响，但失败必须能被调用方看到。
	if recordErr == nil {
		t.Fatal("C6 失败：主 sink 不可写却返回 nil —— 静默失败比没有留痕更危险")
	}
	// ② 降级到 stderr 备用 sink（这一条是 C6 的判别点）。
	if !strings.Contains(captured, approvalAuditDegradedMarker) {
		t.Fatalf("C6 失败：主 sink 写失败后未降级到 stderr 备用 sink\nstderr=%q", captured)
	}
	// ③ 降级行已经是脱敏后的条目（不得因为"走了备用通道"就泄明文）。
	if strings.Contains(captured, plainKey) || strings.Contains(captured, "sk-ant-") {
		t.Fatalf("C6 失败：降级行里出现未脱敏入参\nstderr=%q", captured)
	}
	// ④ 仍可关联：hash 必须在。
	if !strings.Contains(captured, "sha256:") {
		t.Errorf("降级行应保留 tool_input_hash 以便事后关联: %q", captured)
	}
}

// TestDeltaF_T7b_HealthySinkWritesNothingToStderr 反向对照：主 sink 正常时**不得**降级
// （否则 stderr 会被审计行刷屏，反而淹没真正的告警）。
func TestDeltaF_T7b_HealthySinkWritesNothingToStderr(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	sink := NewApprovalAudit(path)

	captured := gcCaptureStderr(t, func() {
		if err := sink.Record(ApprovalRecord{
			Origin: OriginTUIMain, Tool: "bash", ToolInput: `{"command":"ls"}`,
			Decision: "approved", Source: "user",
		}); err != nil {
			t.Errorf("正常写入不应失败: %v", err)
		}
	})
	if strings.Contains(captured, approvalAuditDegradedMarker) {
		t.Fatalf("主 sink 正常却发生降级: %q", captured)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("正常路径应已落盘: %v", err)
	}
}

// ---------------------------------------------------------------------------
// C8 · tool_input 脱敏：RedactSensitive **之后必须复检**（不能只调脱敏）
// ---------------------------------------------------------------------------

// TestDeltaF_T5_ToolInputIsRedactedThenDoubleChecked 三种入参形态的落盘形态。
func TestDeltaF_T5_ToolInputIsRedactedThenDoubleChecked(t *testing.T) {
	// ★ 前置（本用例判别力的来源）：确认"定长量词残留"确实是 IsSecretLeak **抓不到**的形状
	// —— 若将来 S14 把 `{36}` 改成 `{36,}` 根治了该形状，这里会明确失败并提示改构造，
	// 而不是悄悄退化成"永远绿的假守卫"。
	const residual = "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAZZZZ" // ghp_ + 36×A + ZZZZ
	if got := secret.RedactSensitive(residual); got != "[REDACTED]ZZZZ" {
		t.Fatalf("前置失效：RedactSensitive 对该形状的输出已变（got %q）—— 若 S14-G3 已根治，请更新本用例构造", got)
	}
	if secret.IsSecretLeak(secret.RedactSensitive(residual)) {
		t.Fatal("前置失效：该残留形状竟能被 IsSecretLeak 复检命中 —— 那尾残复检就不是必需项")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "tool-approval.jsonl")
	sink := NewApprovalAudit(path)

	const clean = `{"command":"ls -la"}`
	const fullKey = "sk-ant-api03-AAAABBBBCCCCDDDDEEEEFFFF"

	for _, in := range []string{clean, fullKey, residual} {
		if err := sink.Record(ApprovalRecord{
			Origin: OriginTUIMain, Tool: "bash", ToolInput: in,
			Decision: "approved", Source: "user",
		}); err != nil {
			t.Fatalf("Record(%q): %v", in, err)
		}
	}

	entries := gcAuditEntries(t, path)
	if len(entries) != 3 {
		t.Fatalf("期望 3 条留痕，实际 %d", len(entries))
	}

	// 1) 干净入参：预览保留（可读性），并带 hash。
	if got, _ := entries[0]["tool_input"].(string); got != clean {
		t.Errorf("干净入参的预览被改动: %q", got)
	}
	if got, _ := entries[0]["tool_input_hash"].(string); !strings.HasPrefix(got, "sha256:") {
		t.Errorf("干净入参应带 sha256 关联摘要: %q", got)
	}

	// 2) 完整密钥：常规脱敏即足够，落盘不得含明文。
	if got, _ := entries[1]["tool_input"].(string); strings.Contains(got, "sk-ant-") || !strings.Contains(got, "[REDACTED]") {
		t.Errorf("完整密钥未被脱敏: %q", got)
	}

	// 3) ★ 尾残形状：必须 fail-closed 降级为整串占位（这是"复检"的判别点）。
	got, _ := entries[2]["tool_input"].(string)
	if got != redactedBlobPlaceholder {
		t.Errorf("C8 失败：残留形状未 fail-closed（want %q, got %q）", redactedBlobPlaceholder, got)
	}
	if got, _ := entries[2]["detail"].(string); !strings.Contains(got, "input=redacted-blob") {
		t.Errorf("detail 应显式标注已降级: %q", got)
	}

	// 4) 全文件兜底：任何一行都不许含密钥残片。
	raw := ""
	if b, err := os.ReadFile(path); err == nil {
		raw = string(b)
	}
	if strings.Contains(raw, "sk-ant-") || strings.Contains(raw, "ZZZZ") {
		t.Fatalf("C8 失败：审计文件里出现密钥残片:\n%s", raw)
	}
}

// ---------------------------------------------------------------------------
// C1 · 条目必须含 Actor 与进程内单调 Seq
// ---------------------------------------------------------------------------

// TestDeltaF_T8_EntriesCarryActorAndMonotonicSeq C1 的 schema 断言。
func TestDeltaF_T8_EntriesCarryActorAndMonotonicSeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool-approval.jsonl")
	sink := NewApprovalAudit(path)

	for i := 0; i < 3; i++ {
		if err := sink.Record(ApprovalRecord{
			Origin: OriginTUIMain, Tool: "bash", ToolInput: `{"command":"ls"}`,
			Decision: "denied", Source: "user",
		}); err != nil {
			t.Fatal(err)
		}
	}

	entries := gcAuditEntries(t, path)
	if len(entries) != 3 {
		t.Fatalf("期望 3 条留痕，实际 %d", len(entries))
	}
	for i, e := range entries {
		if got := e["actor"]; got != "user" {
			t.Errorf("第 %d 条 actor = %v，want user（留痕的是人的决定）", i, got)
		}
		if got := e["schema_version"]; got != float64(1) {
			t.Errorf("第 %d 条 schema_version = %v，want 1", i, got)
		}
		if got := e["seq"]; got != float64(i+1) {
			t.Errorf("第 %d 条 seq = %v，want %d（进程内单调）", i, got, i+1)
		}
	}
	// 族C-3 的 wall_time 载体 = **timestamp**（Unix 毫秒，与同类 CLI
	// `rollout-trace/raw_event.rs` 的 `wall_time_unix_ms` 同义）；不再另立 `wall_time` 字段
	// —— 同一事实两个字段是漂移源，不是冗余保险。
	// 这里只钉住它存在且是真实 wall clock。
	if ts, _ := entries[0]["timestamp"].(float64); ts <= 0 {
		t.Errorf("timestamp 缺失/为 0（wall_time 载体）: %v", entries[0]["timestamp"])
	}
	for i := 1; i < len(entries); i++ {
		prev, _ := entries[i-1]["timestamp"].(float64)
		cur, _ := entries[i]["timestamp"].(float64)
		if cur < prev {
			t.Errorf("第 %d 条 timestamp 回退（%v → %v）：wall clock 应单调不减", i, prev, cur)
		}
	}
}
