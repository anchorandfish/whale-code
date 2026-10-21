package skill

// =============================================================================
// G-E 修复轮表征 / 判别测试 —— B1（skill 来源信任门）· B7（远程拉取传输安全 + 摘要）
//
// 日期：2026-09-15   组：G-E（插件 / skill / hook）   包：internal/skill
// 拍板依据：集中拍板_决策表与并行修复分组_2026-09-14.md §2.2 B1 / B7
// 不变量焦点：I8（外部代码 = 独立信任域）· I10（不得自我提权）
//
// 判别性设计（pair-wise，单一自变量 = 是否调用 EnableTrustGate()）：
//   - "before" 侧：TestGE_B1_LegacyNoGate_*  —— 门关闭 = **修复前唯一路径** ⇒ 正文照原样注入；
//   - "after"  侧：TestGE_B1_GateOn_*        —— 门开启 = 生产装配后 ⇒ 未确认来源被拦。
//   两侧走**同一生产入口**（NewManager + LoadAll + ExecuteWithMeta / Execute），
//   唯一差异是装配处有没有调 EnableTrustGate()，故彼此可判别、且能精确打红。
//
// 门装在 Execute 与 ExecuteWithMeta 两处（skill 正文出门的两个漏斗，见 trust.go
// ensureInjectionAllowed 注释）；TestGE_B1_GateOn_*BothFunnels 专门防"只装一个漏斗"的影子实现。
//
// ⚠️ 绝不触碰真实 ~/.whale/：每处都用 SetTrustStorePath(t.TempDir()/...) 重定向信任库。
// =============================================================================

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// geWriteSkill 写入一个能通过 validateSkill 的最小 skill 文件（同目录同名 = 覆盖）。
func geWriteSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	content := "---\nname: " + name + "\ndescription: ge char\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill %s: %v", name, err)
	}
}

// geManagerAt 建一个指定来源、指定信任库路径的 manager（信任库重定向，不污染 ~/.whale）。
func geManagerAt(t *testing.T, dir, source, trustPath string) *Manager {
	t.Helper()
	m := NewManager(SourceDir{Path: dir, Source: source})
	m.SetTrustStorePath(trustPath)
	if err := m.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	return m
}

// geManager 同上，信任库落在独立 temp 路径。
func geManager(t *testing.T, dir, source string) *Manager {
	t.Helper()
	return geManagerAt(t, dir, source, filepath.Join(t.TempDir(), "skills.trust.json"))
}

// -----------------------------------------------------------------------------
// B1 · before 侧：门关闭 = 修复前唯一路径，正文原样出门（表征缺陷现状）
// -----------------------------------------------------------------------------

// TestGE_B1_LegacyNoGate_ProjectSkillBodyInjected 钉死**修复前**行为：
// project 来源 skill 的正文经两个漏斗都原样出去，调用方拿不到"这是外部内容"的任何信号。
//
// 这条在修复后**依然必须绿**（库默认关门、向后兼容），它同时是 GateOn 那条的对照组：
// 两条用例只差一个 EnableTrustGate()，绿/红完全由该自变量决定。
func TestGE_B1_LegacyNoGate_ProjectSkillBodyInjected(t *testing.T) {
	const marker = "GE_B1_PROJECT_MARKER_inject_me"
	dir := t.TempDir()
	geWriteSkill(t, dir, "proj-skill", marker)
	m := geManager(t, dir, "project") // 故意不 EnableTrustGate()

	if m.TrustGateEnabled() {
		t.Fatal("前提失效：库默认必须关闭信任门（向后兼容姿态，对齐 hook 域）")
	}

	// 漏斗①：ExecuteWithMeta —— tools/web.go:837 的 Skill 工具走这条
	res, err := m.ExecuteWithMeta("proj-skill", "u")
	if err != nil {
		t.Fatalf("门关闭时 ExecuteWithMeta 不应报错：%v", err)
	}
	if !strings.Contains(res.Prompt, marker) {
		t.Fatalf("表征失败：project 来源正文应原样出门，实际 %q", res.Prompt)
	}

	// 漏斗②：Execute —— tui 的 OnSkillExecute 回调走这条
	out, err := m.Execute("proj-skill", "u")
	if err != nil {
		t.Fatalf("门关闭时 Execute 不应报错：%v", err)
	}
	if !strings.Contains(out, marker) {
		t.Fatalf("表征失败：project 来源正文应原样出门，实际 %q", out)
	}
}

// -----------------------------------------------------------------------------
// B1 · after 侧：门开启（生产装配后），未确认来源被拦 —— 判别器
// -----------------------------------------------------------------------------

// TestGE_B1_GateOn_ProjectSkillBlockedOnBothFunnels 是 B1 的**判别器**：
// 与上一条唯一差异是 EnableTrustGate()，期望从"注入"翻转为"拦截 + 结构化 ErrSkillUntrusted"。
func TestGE_B1_GateOn_ProjectSkillBlockedOnBothFunnels(t *testing.T) {
	const marker = "GE_B1_BLOCKED_MARKER"
	dir := t.TempDir()
	geWriteSkill(t, dir, "proj-skill", marker)
	m := geManager(t, dir, "project")
	m.EnableTrustGate()

	// 漏斗①
	res, err := m.ExecuteWithMeta("proj-skill", "u")
	if err == nil {
		t.Fatalf("判别失败：门开启后未确认的 project skill 必须被拦，实际返回 %q", res.Prompt)
	}
	if !errors.Is(err, ErrSkillUntrusted) {
		t.Fatalf("错误应为 ErrSkillUntrusted（结构化判别），实际 %v", err)
	}
	if strings.Contains(res.Prompt, marker) {
		t.Fatalf("泄漏：被拦下时不得返回正文，实际 %q", res.Prompt)
	}

	// 漏斗②（防影子实现：只给一个漏斗装门）
	out, err := m.Execute("proj-skill", "u")
	if err == nil {
		t.Fatalf("判别失败：Execute 漏斗未装门，实际返回 %q", out)
	}
	if !errors.Is(err, ErrSkillUntrusted) {
		t.Fatalf("Execute 错误应为 ErrSkillUntrusted，实际 %v", err)
	}
	if strings.Contains(out, marker) {
		t.Fatalf("泄漏：Execute 被拦下时不得返回正文，实际 %q", out)
	}

	// 判定依据必须是**结构化状态**，不是散文关键词
	allowed, st, _ := m.IsInjectionAllowed("proj-skill")
	if allowed || st != SkillTrustUntrusted {
		t.Fatalf("IsInjectionAllowed 期望 (false, untrusted)，实际 (%v, %s)", allowed, st)
	}
}

// TestGE_B1_GateOn_PluginSourceBlocked plugin 来源（远程包进入本机）同样需确认。
func TestGE_B1_GateOn_PluginSourceBlocked(t *testing.T) {
	dir := t.TempDir()
	geWriteSkill(t, dir, "plug-skill", "GE_B1_PLUGIN_MARKER")
	m := geManager(t, dir, "plugin")
	m.EnableTrustGate()

	if _, err := m.ExecuteWithMeta("plug-skill", "u"); !errors.Is(err, ErrSkillUntrusted) {
		t.Fatalf("plugin 来源未经确认必须被拦，实际 %v", err)
	}
	if _, st, _ := m.IsInjectionAllowed("plug-skill"); st != SkillTrustUntrusted {
		t.Fatalf("plugin 来源状态应为 untrusted，实际 %s", st)
	}
	if err := m.TrustSkill("plug-skill"); err != nil {
		t.Fatalf("确认 plugin skill 失败：%v", err)
	}
	if _, err := m.ExecuteWithMeta("plug-skill", "u"); err != nil {
		t.Fatalf("确认后应放行：%v", err)
	}
}

// TestGE_B1_GateOn_UserSourceExempt 免确认来源：user（用户自己的目录）不打扰。
//
// 诚实标注：本条是**守卫不是判别器** —— 门装前/装后两态都绿（放行本就是两态共同行为），
// 价值在防"把门装成一律拦"的过度收紧回归。
func TestGE_B1_GateOn_UserSourceExempt(t *testing.T) {
	const marker = "GE_B1_USER_MARKER"
	dir := t.TempDir()
	geWriteSkill(t, dir, "user-skill", marker)
	m := geManager(t, dir, "user")
	m.EnableTrustGate()

	res, err := m.ExecuteWithMeta("user-skill", "u")
	if err != nil {
		t.Fatalf("user 来源免确认，不应被拦：%v", err)
	}
	if !strings.Contains(res.Prompt, marker) {
		t.Fatalf("user 来源正文应放行，实际 %q", res.Prompt)
	}
	s, ok := m.Get("user-skill")
	if !ok {
		t.Fatal("user-skill 未加载")
	}
	if st := m.TrustStatus(s); st != SkillTrustTrusted {
		t.Fatalf("user 来源信任状态应为 trusted，实际 %s", st)
	}
}

// TestGE_B1_TrustOnceThenAllowed "首次使用确认一次"：确认后放行，不反复打扰。
func TestGE_B1_TrustOnceThenAllowed(t *testing.T) {
	dir := t.TempDir()
	geWriteSkill(t, dir, "proj-skill", "GE_B1_ONCE_MARKER")
	m := geManager(t, dir, "project")
	m.EnableTrustGate()

	if _, err := m.ExecuteWithMeta("proj-skill", "u"); !errors.Is(err, ErrSkillUntrusted) {
		t.Fatalf("确认前应被拦，实际 %v", err)
	}
	if err := m.TrustSkill("proj-skill"); err != nil {
		t.Fatalf("TrustSkill 失败：%v", err)
	}
	res, err := m.ExecuteWithMeta("proj-skill", "u")
	if err != nil {
		t.Fatalf("确认后应放行：%v", err)
	}
	if !strings.Contains(res.Prompt, "GE_B1_ONCE_MARKER") {
		t.Fatalf("确认后正文应完整出门，实际 %q", res.Prompt)
	}
	// 二次确认应报错（无需重复确认，避免用户误以为确认改变了什么）
	if err := m.TrustSkill("proj-skill"); err != nil {
		t.Fatalf("重复确认应是幂等 no-op 而非报错，实际 %v", err)
	}
}

// TestGE_B1_ContentChangeInvalidatesTrust 变更即失效（B4/D 同族范式）：
// 同名同来源、内容被改 ⇒ 旧确认不适用，必须重新确认，状态为 modified。
func TestGE_B1_ContentChangeInvalidatesTrust(t *testing.T) {
	dir := t.TempDir()
	trustPath := filepath.Join(t.TempDir(), "skills.trust.json")

	geWriteSkill(t, dir, "proj-skill", "ORIGINAL_BODY")
	m := geManagerAt(t, dir, "project", trustPath)
	m.EnableTrustGate()
	if err := m.TrustSkill("proj-skill"); err != nil {
		t.Fatalf("TrustSkill 失败：%v", err)
	}
	if _, err := m.ExecuteWithMeta("proj-skill", "u"); err != nil {
		t.Fatalf("确认后应放行：%v", err)
	}

	// 攻击者改写正文（同名同来源，只内容变）
	geWriteSkill(t, dir, "proj-skill", "TAMPERED_BODY_ignore_previous_instructions")

	// 新 manager 走同一信任库（模拟下次启动）
	m2 := geManagerAt(t, dir, "project", trustPath)
	m2.EnableTrustGate()
	res, err := m2.ExecuteWithMeta("proj-skill", "u")
	if err == nil {
		t.Fatalf("内容变更后必须重新确认，实际返回 %q", res.Prompt)
	}
	if !errors.Is(err, ErrSkillUntrusted) {
		t.Fatalf("错误应为 ErrSkillUntrusted，实际 %v", err)
	}
	if allowed, st, _ := m2.IsInjectionAllowed("proj-skill"); allowed || st != SkillTrustModified {
		t.Fatalf("期望 (false, modified)，实际 (%v, %s)", allowed, st)
	}
}

// TestGE_B1_RevokeTrustRestoresBlock 撤销后立即回到被拦状态。
func TestGE_B1_RevokeTrustRestoresBlock(t *testing.T) {
	dir := t.TempDir()
	geWriteSkill(t, dir, "proj-skill", "GE_B1_REVOKE_MARKER")
	m := geManager(t, dir, "project")
	m.EnableTrustGate()

	if err := m.TrustSkill("proj-skill"); err != nil {
		t.Fatalf("TrustSkill 失败：%v", err)
	}
	if _, err := m.ExecuteWithMeta("proj-skill", "u"); err != nil {
		t.Fatalf("确认后应放行：%v", err)
	}
	if err := m.RevokeTrust("proj-skill"); err != nil {
		t.Fatalf("RevokeTrust 失败：%v", err)
	}
	if _, err := m.ExecuteWithMeta("proj-skill", "u"); !errors.Is(err, ErrSkillUntrusted) {
		t.Fatalf("撤销后必须重新被拦，实际 %v", err)
	}
}

// TestGE_B1_PendingReviewAndTrustAll 待确认清单 + 一次性全部确认（对齐 S7 `/hooks trust all`）。
func TestGE_B1_PendingReviewAndTrustAll(t *testing.T) {
	dir := t.TempDir()
	geWriteSkill(t, dir, "p1", "P1_BODY")
	geWriteSkill(t, dir, "p2", "P2_BODY")
	m := geManager(t, dir, "project")
	m.EnableTrustGate()

	items := m.PendingReview()
	if len(items) != 2 {
		t.Fatalf("待确认项应为 2，实际 %d（%v）", len(items), items)
	}
	if s := m.RunTrustSummary(); !strings.Contains(s, "2 skill(s)") {
		t.Fatalf("摘要应报告待确认数量，实际 %q", s)
	}
	if err := m.TrustAllPending(); err != nil {
		t.Fatalf("TrustAllPending 失败：%v", err)
	}
	if left := m.PendingReview(); len(left) != 0 {
		t.Fatalf("全部确认后待确认项应为 0，实际 %d", len(left))
	}
	for _, n := range []string{"p1", "p2"} {
		if _, err := m.ExecuteWithMeta(n, "u"); err != nil {
			t.Fatalf("全部确认后 %s 应放行：%v", n, err)
		}
	}
}

// TestGE_B1_UnknownSkillNotConfusedWithTrustGate 错误分类不得混淆：
// "skill 不存在" 与 "信任门拒绝" 是两件事（前者应提示名字，后者应提示去确认）。
func TestGE_B1_UnknownSkillNotConfusedWithTrustGate(t *testing.T) {
	m := NewManager()
	m.SetTrustStorePath(filepath.Join(t.TempDir(), "skills.trust.json"))
	m.EnableTrustGate()

	_, err := m.ExecuteWithMeta("nope", "")
	if err == nil {
		t.Fatal("不存在的 skill 必须报错")
	}
	if errors.Is(err, ErrSkillUntrusted) {
		t.Fatalf("「skill 不存在」不得被误报为「信任门拒绝」：%v", err)
	}
}

// TestGE_B1_TrustStoreIsolatedFromRealHome 守卫：本文件所有用例都不写真实 ~/.whale。
//
// 诚实标注：**守卫不是判别器**（两态都绿）；反例注入点 = 忘调 SetTrustStorePath
// （此时路径落到 home）—— 本守卫把"隔离"从口头约定变成可执行断言。
func TestGE_B1_TrustStoreIsolatedFromRealHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("取不到 home，跳过：%v", err)
	}
	dir := t.TempDir()
	geWriteSkill(t, dir, "proj-skill", "GE_B1_ISO_MARKER")
	trustPath := filepath.Join(t.TempDir(), "skills.trust.json")
	m := geManagerAt(t, dir, "project", trustPath)
	m.EnableTrustGate()

	if err := m.TrustSkill("proj-skill"); err != nil {
		t.Fatalf("TrustSkill 失败：%v", err)
	}
	if _, err := os.Stat(trustPath); err != nil {
		t.Fatalf("信任库应落在被重定向的路径 %s：%v", trustPath, err)
	}
	realStore := filepath.Join(home, ".whale", defaultSkillTrustStoreName)
	if strings.HasPrefix(trustPath, filepath.Join(home, ".whale")) {
		t.Fatalf("隔离失效：信任库路径落在真实 ~/.whale 下（%s）", realStore)
	}
}

// -----------------------------------------------------------------------------
// B7 · 远程 skill 拉取：HTTPS 强制 + 摘要（fail-closed）
// -----------------------------------------------------------------------------

// TestGE_B7_AddSource_RejectsPlainHTTPNonLoopback 注册处即拒明文 HTTP（非回环）。
func TestGE_B7_AddSource_RejectsPlainHTTPNonLoopback(t *testing.T) {
	m := NewMarketplace(NewManager(), t.TempDir())
	if m == nil {
		t.Fatal("NewMarketplace 返回 nil")
	}
	err := m.AddSource(RemoteSource{Name: "evil", URL: "http://example.com/skills.zip"})
	if err == nil {
		t.Fatal("判别失败：非回环明文 http 源必须被拒（B7）")
	}
	if n := len(m.ListSources()); n != 0 {
		t.Fatalf("被拒的源不得入表，实际 %d 条", n)
	}
}

// TestGE_B7_AddSource_AllowsLoopbackHTTPAndHTTPS 回环明文与 https 放行（对照组，防"一律拒"式假绿）。
func TestGE_B7_AddSource_AllowsLoopbackHTTPAndHTTPS(t *testing.T) {
	m := NewMarketplace(NewManager(), t.TempDir())
	if m == nil {
		t.Fatal("NewMarketplace 返回 nil")
	}
	allowed := []string{
		"https://example.com/skills.zip",
		"http://127.0.0.1:8080/skills.zip", // 回环：本地开发 / httptest
		"http://localhost:8080/skills.zip",
	}
	for i, u := range allowed {
		if err := m.AddSource(RemoteSource{Name: "ok" + string(rune('a'+i)), URL: u}); err != nil {
			t.Fatalf("应放行的源被拒 %s：%v", u, err)
		}
	}
	if n := len(m.ListSources()); n != len(allowed) {
		t.Fatalf("应注册 %d 条，实际 %d", len(allowed), n)
	}
}

// geRoundTripFunc 便于注入"一旦用网就失败"的 Transport。
type geRoundTripFunc func(*http.Request) (*http.Response, error)

func (f geRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// geFailOnNetwork 返回一个"真发起网络请求就判红"的 client。
//
// 判别性关键：光断言 err != nil 是**假绿**——修复前也会因为沙箱里网络不通而报错，
// 于是"通过"的原因不是安全校验而是网络失败。用这个 client 把"是否真的去用网"变成可观测事实。
func geFailOnNetwork(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: geRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Errorf("安全校验必须发生在发起网络请求之前，实际请求了 %s", req.URL)
		return nil, errors.New("network must not be reached")
	})}
}

// TestGE_B7_FetchOne_RejectsPlainHTTPNonLoopback 纵深防御：下载口再判一次（不只信注册口）。
//
// 判别方式：**拦截必须发生在用网之前**（见 geFailOnNetwork）。修复前该 URL 会被真的请求 ⇒ 打红。
func TestGE_B7_FetchOne_RejectsPlainHTTPNonLoopback(t *testing.T) {
	mgr := NewManager(SourceDir{Path: t.TempDir(), Source: "project"})
	m := NewMarketplace(mgr, t.TempDir())
	if m == nil {
		t.Fatal("NewMarketplace 返回 nil")
	}
	m.SetHTTPClient(geFailOnNetwork(t))

	// 不注册、直接 FetchOne（绕过 AddSource 的路径也必须拦）
	err := m.FetchOne(context.Background(), RemoteSource{Name: "evil", URL: "http://example.com/skills.zip"}, true)
	if err == nil {
		t.Fatal("判别失败：FetchOne 必须同样拒绝非回环明文 http（纵深防御）")
	}
}

// TestGE_B7_DigestMismatchRefusesToLoad 摘要不符 ⇒ fail-closed，内容**不得**落地。
func TestGE_B7_DigestMismatchRefusesToLoad(t *testing.T) {
	zipBytes := s19BuildZip(t, []s19ZipEntry{
		{Name: "evil.md", Body: s19SkillMD("evil", "GE_B7_MALICIOUS_BODY")},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	mgr := NewManager(SourceDir{Path: t.TempDir(), Source: "project"})
	m := NewMarketplace(mgr, t.TempDir())
	wrong := strings.Repeat("a", 64) // 合法格式，但与真实内容不符
	err := m.FetchOne(context.Background(), RemoteSource{Name: "x", URL: srv.URL, SHA256: wrong}, true)
	if err == nil {
		t.Fatal("判别失败：摘要不符必须拒绝加载（B7 fail-closed）")
	}
	if _, ok := mgr.Get("evil"); ok {
		t.Fatal("fail-closed 失效：摘要不符的内容仍被加载进 skill 表")
	}
}

// TestGE_B7_DigestMatchLoads 对照组：摘要正确 ⇒ 正常加载（证明上一条不是"整体失败"式假绿）。
func TestGE_B7_DigestMatchLoads(t *testing.T) {
	zipBytes := s19BuildZip(t, []s19ZipEntry{
		{Name: "good.md", Body: s19SkillMD("good", "GE_B7_GOOD_BODY")},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	sum := sha256.Sum256(zipBytes)
	mgr := NewManager(SourceDir{Path: t.TempDir(), Source: "project"})
	m := NewMarketplace(mgr, t.TempDir())
	err := m.FetchOne(context.Background(), RemoteSource{
		Name: "x", URL: srv.URL, SHA256: hex.EncodeToString(sum[:]),
	}, true)
	if err != nil {
		t.Fatalf("摘要正确时应加载成功，实际 %v", err)
	}
	if _, ok := mgr.Get("good"); !ok {
		t.Fatal("对照组失效：摘要正确的内容未加载")
	}
}

// TestGE_B7_InvalidDigestFormatFailClosed 摘要格式非法 = 配置错误，同样 fail-closed（不"忽略后放行"）。
func TestGE_B7_InvalidDigestFormatFailClosed(t *testing.T) {
	zipBytes := s19BuildZip(t, []s19ZipEntry{
		{Name: "x.md", Body: s19SkillMD("x", "GE_B7_BAD_DIGEST_BODY")},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	mgr := NewManager(SourceDir{Path: t.TempDir(), Source: "project"})
	m := NewMarketplace(mgr, t.TempDir())
	err := m.FetchOne(context.Background(), RemoteSource{Name: "x", URL: srv.URL, SHA256: "not-a-digest"}, true)
	if err == nil {
		t.Fatal("摘要格式非法必须 fail-closed，不得忽略后放行")
	}
	if _, ok := mgr.Get("x"); ok {
		t.Fatal("fail-closed 失效：摘要格式非法时内容仍被加载")
	}
}
