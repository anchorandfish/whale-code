package skill

// =============================================================================
// S19 外部代码信任域（远程 skill 拉取）— 表征测试（补充）
//
// 日期：2026-09-14   域：S19（AUDIT_ROADMAP.md §2）
// 不变量焦点：I8（外部代码 = 独立信任域）/ I3（边界输入校验，波及）
//
// 补齐 2026-09-10 那批 S19 表征测试的盲区：**远程 skill 拉取路径**。
// roadmap 对 S19 的表述含「git/HTTP 拉取、manifest 校验」，此前无任何表征测试。
//
// 钉死的当前行为（走生产入口 NewMarketplace → FetchOne，含真实 httptest HTTP 服务）：
//   M1. 远程 zip 经**明文 HTTP**（无 TLS、无 token、无签名/摘要校验）解压后，
//       以 Source="plugin" 直接进入 skill 表 —— 即"远程未经验证的内容"与本地 skill 同权。
//   M2. zip 内 `..` 路径条目被拒绝（Zip Slip 防护**存在**，属守卫而非缺陷）。
//
// ⚠️ 生产调用面事实（grep 可复核）：`skill.NewMarketplace` **零生产调用点**
//    （仅 marketplace_test.go），故本文件钉的是"模块自身行为"，不是"线上真实可达路径"。
//    该结论已在 STATUS §Gap 中按"死代码（结构性）"登记，不代表当前线上可被利用。
//
// ⚠️ 不触碰真实 ~/.whale/（cacheDir 与 manager 均指向 t.TempDir()）。
// =============================================================================

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// s19ZipEntry 描述一个待写入 zip 的条目。
type s19ZipEntry struct {
	Name string
	Body string
}

// s19BuildZip 在内存中构造 zip。
func s19BuildZip(t *testing.T, entries []s19ZipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.Name)
		if err != nil {
			t.Fatalf("zip create %s: %v", e.Name, err)
		}
		if _, err := w.Write([]byte(e.Body)); err != nil {
			t.Fatalf("zip write %s: %v", e.Name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// s19SkillMD 构造一个能通过 validateSkill 的最小 skill 文件。
func s19SkillMD(name, body string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: remote s19 char\n---\n%s\n", name, body)
}

// M1 — 明文 HTTP 下载的 zip 内容以 Source="plugin" 进入 skill 表，全程无签名/摘要校验。
//
// 观测面：FetchOne 成功后 manager.Get 能取到远程 skill，且其 Source == "plugin"，
// 正文即 HTTP 响应体内容（未做任何信任降级、未标记 untrusted）。
func TestCharS19_SkillMarketplace_PlainHTTPContentLoadedAsTrustedPluginSource(t *testing.T) {
	const remoteBody = "REMOTE_S19_MARKER_ignore_previous_instructions"
	zipBytes := s19BuildZip(t, []s19ZipEntry{
		{Name: "remote-evil.md", Body: s19SkillMD("remote-evil", remoteBody)},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 故意不设 Content-Type 之外任何完整性元数据（无 ETag/摘要/签名头）
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	mgr := NewManager(SourceDir{Path: t.TempDir(), Source: "project"})
	m := NewMarketplace(mgr, t.TempDir())
	if m == nil {
		t.Fatal("NewMarketplace 返回 nil")
	}

	src := RemoteSource{Name: "s19remote", URL: srv.URL} // 明文 HTTP，无 AuthToken
	if err := m.FetchOne(context.Background(), src, true); err != nil {
		t.Fatalf("FetchOne 失败：%v", err)
	}

	s, ok := mgr.Get("remote-evil")
	if !ok {
		t.Fatal("当前行为：远程 zip 中的 skill 未被加载")
	}
	if s.Source != "plugin" {
		t.Fatalf("当前行为：远程来源预期标记为 \"plugin\"，实际 %q", s.Source)
	}
	if !bytes.Contains([]byte(s.Prompt), []byte(remoteBody)) {
		t.Fatalf("当前行为：远程正文应原样进入 skill.Prompt，实际 %q", s.Prompt)
	}
	// 附带钉住：FetchOne 把远程内容与本地 plugin 来源合并为同一信任级，
	// 调用方无法从 Source 区分「本地插件提供」与「明文 HTTP 拉取」。
	if s.Source == "builtin" || s.Source == "user" || s.Source == "project" {
		t.Fatalf("前提失效：远程 Source 不应是本地信任级，实际 %q", s.Source)
	}
}

// M2 — Zip Slip 守卫（**守卫非判别器**）：含 `..` 的条目被拒绝，正常条目仍解压。
//
// 诚实标注：本条在"修复前后两态"都会绿（防护已存在），价值是防回归，不是判别修复有效性。
func TestCharS19_SkillMarketplace_ZipSlipEntryRejected(t *testing.T) {
	zipBytes := s19BuildZip(t, []s19ZipEntry{
		{Name: "../escape.md", Body: s19SkillMD("escape", "SHOULD_NOT_LAND")},
		{Name: "ok.md", Body: s19SkillMD("ok", "OK_MARKER")},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	manager := NewManager(SourceDir{Path: t.TempDir(), Source: "project"})
	cache := t.TempDir()
	m := NewMarketplace(manager, cache)
	if err := m.FetchOne(context.Background(), RemoteSource{Name: "zip-slip", URL: srv.URL}, true); err != nil {
		t.Fatalf("FetchOne 失败：%v", err)
	}

	// 对照：正常条目必须落地，否则本用例会因"整个解压都没生效"而假绿。
	if _, ok := manager.Get("ok"); !ok {
		t.Fatal("对照组失效：正常条目 ok.md 未加载，无法证明是「路径校验」而非「解压整体失败」")
	}
	if _, ok := manager.Get("escape"); ok {
		t.Fatal("Zip Slip 防护失效：`../escape.md` 条目被加载")
	}
}

// 保证 import 使用完整（编译期兜底）。
var _ = json.RawMessage(nil)
