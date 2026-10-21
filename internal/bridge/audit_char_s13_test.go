// Package bridge — S13 日志/审计 表征测试（Snapshot 步，仅钉当前行为，不改源码）
//
// 本文件用六步闭环的 Snapshot 步钉死 whale-code 当前"审计子系统"的可观测行为。
// 全部用例在**当前源码**下必须 GREEN（钉住）；它们也是后续 Fix 的判别器/守卫：
// - 若某修复把审计改成"真·不可变追加 + 含执行者/序号"，C3(重写) 与 C5(无 Actor/Seq) 会翻红，
// 证明修复确实改动了行为（而非假绿）。
// - C1/C2/C4 描述当前"能工作"的契约，修复后应保持（守卫）。
//
// ★ 2026-09-15 翻转记录（G-C / O-C 轮，对应决策项 C1 + C5）：
//
//	C3 `...CleanupRewritesWholeFile` → **`...CleanupNeverRewritesWholeFile`**：
//
// C5（S13-HG Q1=A 的派生硬要求）已落 —— `Cleanup` 不再 read→filter→rename
// 整文件重写，改为"整文件粒度"保留（当前文件整体过期才删、归档文件按日期整文件删）。
// 断言方向**反转**：从"期望行数变少"改为"期望历史字节逐字节不变"。
//
//	C5 `...AuditEntrySchemaHasNoActorOrSeq` → **`...AuditEntrySchemaHasActorAndSeq`**：
//
// C1 已落 —— `AuditEntry` 补 `SchemaVersion/Seq/Actor` 及 delta F 的审批字段。
// 断言方向**反转**：从"期望 7 字段且无 Actor/Seq"改为"精确期望 19 字段且含 Actor/Seq"。
//
//	翻转后的用例仍具判别力：把 audit.go 的 `Cleanup` 换回整文件重写、或删掉新增字段，
//	对应用例会立刻打红（A/B 见 G-C 回执）。
//
// 范围：internal/bridge/audit.go 这一个 whale 内**唯一**的专用审计实现
// （覆盖 bridge JWT 操作 set_token/verify/refresh/reconnect/revoke + delta F 的工具审批留痕）。
package bridge

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// C1：每次 Log 向文件追加恰好一行 JSONL（含结尾换行），且每行可独立解析。
// 钉住当前"逐条追加、JSONL 格式"的契约（I9 append-only 的正面半边）。
func TestCharS13_AuditLogAppendsOneJSONLLinePerCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	l := NewFileAuditLogger(path)
	if err := l.Log(&AuditEntry{Operation: AuditOpVerify, TokenID: "jti-1", Result: AuditResultSuccess}); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(&AuditEntry{Operation: AuditOpRefresh, TokenID: "jti-2", Result: AuditResultDenied}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("audit file not written: %v", err)
	}
	lines := splitAuditLines(string(data))
	if len(lines) != 2 {
		t.Fatalf("want 2 JSONL lines, got %d: %q", len(lines), data)
	}
	for i, ln := range lines {
		var e AuditEntry
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			t.Fatalf("line %d not valid JSON: %q: %v", i, ln, err)
		}
	}
}

// C2：审计文件权限 0600，所在目录权限 0700（I7 密钥不泄露 / I9 防篡改的存储侧）。
func TestCharS13_AuditFileAndDirPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "audit.jsonl") // 含自动创建的中间目录
	l := NewFileAuditLogger(path)
	if err := l.Log(&AuditEntry{Operation: AuditOpVerify, TokenID: "jti", Result: AuditResultSuccess}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("audit file want 0600, got %o", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("audit dir want 0700, got %o", di.Mode().Perm())
	}
}

// C3（★2026-09-15 翻转，对应决策项 C5）：Cleanup **绝不**整体重写文件。
//
// 原行为（已废弃）：read→filter→rename 整文件重写，逐条丢弃过期条目。
// 现行为（S13-HG Q1=A 的派生硬要求）：保留策略只做**整文件粒度**删除/保留 ——
// 不读、不改写历史字节；"逐条过期"被"逐文件过期"取代（append-only 的必然代价）。
//
// 判别力：把 `Cleanup` 换回整文件重写 ⇒ 本用例立刻打红（历史字节被改写）。
func TestCharS13_AuditCleanupNeverRewritesWholeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	l := NewFileAuditLogger(path)
	old := time.Now().Add(-(auditRetentionDays + 1) * 24 * time.Hour).UnixMilli()
	fresh := time.Now().UnixMilli()
	if err := l.Log(&AuditEntry{Timestamp: old, Operation: AuditOpVerify, TokenID: "old", Result: AuditResultSuccess}); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(&AuditEntry{Timestamp: fresh, Operation: AuditOpVerify, TokenID: "new", Result: AuditResultSuccess}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(splitAuditLines(string(before))) != 2 {
		t.Fatalf("precondition: expected 2 lines before cleanup, got %q", before)
	}

	if err := l.Cleanup(); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("当前文件整体未过期，Cleanup 不得删除它: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("Cleanup 改写了历史字节（append-only 已破）：\n before=%q\n after =%q", before, after)
	}
	// 旧条目仍在文件里是**预期**的（append-only 不按条目过滤），
	// 过期语义改由"整个文件/整个归档的删除"承担 —— 见 TestDeltaF_T6b。
	var e AuditEntry
	if err := json.Unmarshal([]byte(splitAuditLines(string(after))[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.TokenID != "old" {
		t.Fatalf("历史行顺序/内容被改动: token_id=%q", e.TokenID)
	}
}

// C4：auditPath 为空时降级为内存 logger，不落盘、且查询命中内存条目。
// 钉住当前"路径未配置即无持久审计"的降级契约（关联 G1：生产可能传入空路径 → 审计静默失效）。
func TestCharS13_AuditEmptyPathDegradesToMemory(t *testing.T) {
	l := NewFileAuditLogger("")
	if err := l.Log(&AuditEntry{Operation: AuditOpVerify, TokenID: "x", Result: AuditResultSuccess}); err != nil {
		t.Fatal(err)
	}
	q, err := l.Query(AuditOpVerify, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 1 {
		t.Fatalf("memory logger should hold 1 entry, got %d", len(q))
	}
}

// C5（★2026-09-15 翻转，对应决策项 C1）：`AuditEntry` schema 必须含 Actor/Seq。
//
// 原断言（已废弃）：精确等于 7 个 token 字段、且**无** Actor/Seq（钉住缺口）。
// 现断言：前 7 个 token 字段语义不变，**追加** delta F 的 12 个字段（含 Actor/Seq/SchemaVersion）。
//
// 精确列字段（而非"包含"）是刻意的：字段增删/改名都必须让本用例可见 ——
// 审计 schema 是 append-only 的对外契约，静默漂移等于历史条目不可解析。
//
// 判别力：删掉 Actor 或 Seq ⇒ 本用例立刻打红。
func TestCharS13_AuditEntrySchemaHasActorAndSeq(t *testing.T) {
	typ := reflect.TypeOf(AuditEntry{})
	got := make([]string, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		got[i] = typ.Field(i).Name
	}
	want := []string{
		// 既有 7 个 token 审计字段（语义不变）
		"Timestamp", "Operation", "TokenID", "TokenMask", "Result", "Detail", "Source",
		// delta F / C1 新增 12 个（token 审计留空，全部 omitempty ⇒ 既有 JSONL 行零变化）
		"SchemaVersion", "Seq", "Actor", "Origin", "Tool", "ToolInput", "ToolInputHash",
		"Decision", "DecisionSource", "SessionID", "RequestID", "McpServer",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AuditEntry schema 与 C1 冻结后的契约不一致。\n got=%v\nwant=%v", got, want)
	}
	// token 审计的既有字段必须仍然**未**加 omitempty（否则旧行形状会变）。
	for _, name := range want[:7] {
		f, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("缺失既有字段 %s", name)
		}
		if tag := f.Tag.Get("json"); tag == "" || strings.HasSuffix(tag, ",omitempty") {
			t.Errorf("既有字段 %s 的 json tag 被加了 omitempty（会改变既有条目形状）: %q", name, tag)
		}
	}
}
