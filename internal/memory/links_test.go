// links_test.go — H11/N27 Phase B B4 测试。
// 含致命反例：
//   - TestLinkMemories_bidirectional（双向链接建立：A.Links 含 B 且 B.Links 含 A）
//   - TestLinkMemories_cross_scope（跨 scope 双向链接：A 在 user，B 在 project）
//
// 测试覆盖：
//   - TestLinkMemories_bidirectional（致命反例）：双向链接建立
//   - TestLinkMemories_dedup：重复调用去重
//   - TestLinkMemories_self_link：自反防护（nameA == nameB 返回 error）
//   - TestLinkMemories_empty_name：空 name 返回 error
//   - TestLinkMemories_not_found：name 不存在返回 error
//   - TestLinkMemories_reason_stored：reason 存入 linkRegistry
//   - TestLinkMemories_cross_scope（致命反例）：跨 scope 双向链接
//   - TestGetLinks：返回 entry.Links 副本
//   - TestGetLinks_not_found：name 不存在返回 error
//   - TestGetLinkReason：返回 reason + 不存在返回空字符串

package memory

import (
	"strings"
	"testing"
)

// setupLinkTest 创建测试用 MergeLayer + 清理 linkRegistry。
// 复用 setupTestMergeLayer（已注入 globalMergeLayer + t.Cleanup），
// 额外清理 linkRegistry 避免测试间状态污染。
func setupLinkTest(t *testing.T, workDir string) (baseDir string, ml *MergeLayer) {
	t.Helper()
	baseDir, ml = setupTestMergeLayer(t, workDir)
	// 清理 linkRegistry（全局状态，避免跨测试污染）
	t.Cleanup(resetLinkRegistryForTest)
	return baseDir, ml
}

// TestLinkMemories_bidirectional 致命反例（双向链接建立）：
// LinkMemories("A", "B", "reflection source") 后：
//   - entryA.Links 含 "B"
//   - entryB.Links 含 "A"
//   - 双向链接建立，非单向
func TestLinkMemories_bidirectional(t *testing.T) {
	_, ml := setupLinkTest(t, "/test/work/bidirectional")

	// 写入两条记忆
	writeEntryToStore(t, ml.userStore, "entry-a", "entry A description", "content A", VerificationVerified)
	writeEntryToStore(t, ml.userStore, "entry-b", "entry B description", "content B", VerificationVerified)

	// 建立双向链接
	if err := LinkMemories("entry-a", "entry-b", "reflection source"); err != nil {
		t.Fatalf("LinkMemories 失败: %v", err)
	}

	// 验证 A.Links 含 B
	linksA, err := GetLinks("entry-a")
	if err != nil {
		t.Fatalf("GetLinks(entry-a) 失败: %v", err)
	}
	if !containsString(linksA, "entry-b") {
		t.Errorf("致命反例失效: entryA.Links 不含 entry-b, 实际 %v", linksA)
	}

	// 验证 B.Links 含 A（关键反例：双向链接，非单向）
	linksB, err := GetLinks("entry-b")
	if err != nil {
		t.Fatalf("GetLinks(entry-b) 失败: %v", err)
	}
	if !containsString(linksB, "entry-a") {
		t.Errorf("致命反例失效: entryB.Links 不含 entry-a, 实际 %v", linksB)
	}

	// 验证 reason 存入 linkRegistry
	reason := GetLinkReason("entry-a", "entry-b")
	if reason != "reflection source" {
		t.Errorf("linkRegistry[entry-a][entry-b] = %q, 期望 %q", reason, "reflection source")
	}
	reasonReverse := GetLinkReason("entry-b", "entry-a")
	if reasonReverse != "reflection source" {
		t.Errorf("linkRegistry[entry-b][entry-a] = %q, 期望 %q（双向 reason 对称）", reasonReverse, "reflection source")
	}
}

// TestLinkMemories_dedup 验证重复调用 LinkMemories 不重复添加 link。
func TestLinkMemories_dedup(t *testing.T) {
	_, ml := setupLinkTest(t, "/test/work/dedup")

	writeEntryToStore(t, ml.userStore, "dedup-a", "A", "content", VerificationVerified)
	writeEntryToStore(t, ml.userStore, "dedup-b", "B", "content", VerificationVerified)

	// 第一次链接
	if err := LinkMemories("dedup-a", "dedup-b", "first"); err != nil {
		t.Fatalf("第一次 LinkMemories 失败: %v", err)
	}
	// 第二次链接（重复，reason 更新为 second）
	if err := LinkMemories("dedup-a", "dedup-b", "second"); err != nil {
		t.Fatalf("第二次 LinkMemories 失败: %v", err)
	}

	// 验证 A.Links 只含 1 个 B（去重）
	linksA, err := GetLinks("dedup-a")
	if err != nil {
		t.Fatalf("GetLinks(dedup-a) 失败: %v", err)
	}
	countB := 0
	for _, l := range linksA {
		if l == "dedup-b" {
			countB++
		}
	}
	if countB != 1 {
		t.Errorf("去重失效: A.Links 含 %d 个 dedup-b, 期望 1, 实际 %v", countB, linksA)
	}

	// 验证 B.Links 也只含 1 个 A
	linksB, err := GetLinks("dedup-b")
	if err != nil {
		t.Fatalf("GetLinks(dedup-b) 失败: %v", err)
	}
	countA := 0
	for _, l := range linksB {
		if l == "dedup-a" {
			countA++
		}
	}
	if countA != 1 {
		t.Errorf("去重失效: B.Links 含 %d 个 dedup-a, 期望 1, 实际 %v", countA, linksB)
	}

	// reason 应更新为 second（覆盖语义）
	reason := GetLinkReason("dedup-a", "dedup-b")
	if reason != "second" {
		t.Errorf("reason 未更新: 期望 %q, 实际 %q", "second", reason)
	}
}

// TestLinkMemories_self_link 验证自反防护（nameA == nameB 返回 error）。
func TestLinkMemories_self_link(t *testing.T) {
	_, ml := setupLinkTest(t, "/test/work/self-link")
	writeEntryToStore(t, ml.userStore, "self", "self", "content", VerificationVerified)

	err := LinkMemories("self", "self", "self link")
	if err == nil {
		t.Fatal("LinkMemories(self, self) 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), "must not be equal") {
		t.Errorf("error 信息不匹配: %v", err)
	}

	// 验证 self.Links 不含 self（自反被拒绝）
	links, err := GetLinks("self")
	if err != nil {
		t.Fatalf("GetLinks(self) 失败: %v", err)
	}
	if containsString(links, "self") {
		t.Errorf("自反未被拒绝: self.Links 含 self, 实际 %v", links)
	}
}

// TestLinkMemories_empty_name 验证空 name 返回 error。
func TestLinkMemories_empty_name(t *testing.T) {
	_, _ = setupLinkTest(t, "/test/work/empty-name")

	cases := []struct {
		nameA, nameB string
		desc         string
	}{
		{"", "b", "nameA 空"},
		{"a", "", "nameB 空"},
		{"", "", "两者都空"},
	}
	for _, c := range cases {
		err := LinkMemories(c.nameA, c.nameB, "test")
		if err == nil {
			t.Errorf("LinkMemories(%q, %q) 应返回 error, 实际 nil (%s)", c.nameA, c.nameB, c.desc)
		}
		if !strings.Contains(err.Error(), "must not be empty") {
			t.Errorf("error 信息不匹配 (%s): %v", c.desc, err)
		}
	}
}

// TestLinkMemories_not_found 验证 name 不存在返回 error。
func TestLinkMemories_not_found(t *testing.T) {
	_, ml := setupLinkTest(t, "/test/work/not-found")
	writeEntryToStore(t, ml.userStore, "exists", "exists", "content", VerificationVerified)

	// A 不存在
	err := LinkMemories("nonexistent", "exists", "test")
	if err == nil {
		t.Fatal("LinkMemories(nonexistent, exists) 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), ErrEntryNotFound.Error()) {
		t.Errorf("error 应含 ErrEntryNotFound: %v", err)
	}

	// B 不存在
	err = LinkMemories("exists", "also-nonexistent", "test")
	if err == nil {
		t.Fatal("LinkMemories(exists, also-nonexistent) 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), ErrEntryNotFound.Error()) {
		t.Errorf("error 应含 ErrEntryNotFound: %v", err)
	}

	// 验证 exists.Links 未被污染（链接未建立）
	links, err := GetLinks("exists")
	if err != nil {
		t.Fatalf("GetLinks(exists) 失败: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("链接失败后 exists.Links 应为空, 实际 %v", links)
	}
}

// TestLinkMemories_reason_stored 验证 reason 存入 linkRegistry（内存字段，不持久化）。
func TestLinkMemories_reason_stored(t *testing.T) {
	_, ml := setupLinkTest(t, "/test/work/reason")
	writeEntryToStore(t, ml.userStore, "r-a", "A", "content", VerificationVerified)
	writeEntryToStore(t, ml.userStore, "r-b", "B", "content", VerificationVerified)

	if err := LinkMemories("r-a", "r-b", "shared build context"); err != nil {
		t.Fatalf("LinkMemories 失败: %v", err)
	}

	// 验证 reason 双向对称存储
	if got := GetLinkReason("r-a", "r-b"); got != "shared build context" {
		t.Errorf("GetLinkReason(r-a, r-b) = %q, 期望 %q", got, "shared build context")
	}
	if got := GetLinkReason("r-b", "r-a"); got != "shared build context" {
		t.Errorf("GetLinkReason(r-b, r-a) = %q, 期望 %q（双向对称）", got, "shared build context")
	}

	// 不存在的链接 reason 返回空字符串
	if got := GetLinkReason("r-a", "nonexistent"); got != "" {
		t.Errorf("GetLinkReason(r-a, nonexistent) = %q, 期望空字符串", got)
	}
	if got := GetLinkReason("nonexistent", "r-a"); got != "" {
		t.Errorf("GetLinkReason(nonexistent, r-a) = %q, 期望空字符串", got)
	}
}

// TestLinkMemories_cross_scope 致命反例（跨 scope 双向链接）：
// A 在 user scope，B 在 project scope，LinkMemories 后双向链接建立。
// 验证：
//  1. A.Links 含 B + B.Links 含 A（跨 scope 双向）
//  2. 写回时根据 FilePath 推断 scope，A 写回 userStore，B 写回 projectStore
func TestLinkMemories_cross_scope(t *testing.T) {
	_, ml := setupLinkTest(t, "/test/work/cross-scope")

	// A 写入 user scope
	writeEntryToStore(t, ml.userStore, "user-entry", "user entry", "user content", VerificationVerified)
	// B 写入 project scope
	writeEntryToStore(t, ml.projectStore, "project-entry", "project entry", "project content", VerificationVerified)

	// 跨 scope 建立双向链接
	if err := LinkMemories("user-entry", "project-entry", "cross scope link"); err != nil {
		t.Fatalf("跨 scope LinkMemories 失败: %v", err)
	}

	// 验证 A（user scope）.Links 含 B（project scope）
	linksA, err := GetLinks("user-entry")
	if err != nil {
		t.Fatalf("GetLinks(user-entry) 失败: %v", err)
	}
	if !containsString(linksA, "project-entry") {
		t.Errorf("致命反例失效: user-entry.Links 不含 project-entry, 实际 %v", linksA)
	}

	// 验证 B（project scope）.Links 含 A（user scope）
	linksB, err := GetLinks("project-entry")
	if err != nil {
		t.Fatalf("GetLinks(project-entry) 失败: %v", err)
	}
	if !containsString(linksB, "user-entry") {
		t.Errorf("致命反例失效: project-entry.Links 不含 user-entry, 实际 %v", linksB)
	}

	// 验证持久化（重新 Read 验证文件已写入）
	readA, err := ml.userStore.Read("user-entry")
	if err != nil {
		t.Fatalf("userStore.Read(user-entry) 失败: %v", err)
	}
	if !containsString(readA.Links, "project-entry") {
		t.Errorf("持久化失效: userStore 中 user-entry.Links 不含 project-entry, 实际 %v", readA.Links)
	}

	readB, err := ml.projectStore.Read("project-entry")
	if err != nil {
		t.Fatalf("projectStore.Read(project-entry) 失败: %v", err)
	}
	if !containsString(readB.Links, "user-entry") {
		t.Errorf("持久化失效: projectStore 中 project-entry.Links 不含 user-entry, 实际 %v", readB.Links)
	}
}

// TestGetLinks 验证 GetLinks 返回 entry.Links 副本（外部修改不污染内存）。
func TestGetLinks(t *testing.T) {
	_, ml := setupLinkTest(t, "/test/work/get-links")
	writeEntryToStore(t, ml.userStore, "gl-a", "A", "content", VerificationVerified)
	writeEntryToStore(t, ml.userStore, "gl-b", "B", "content", VerificationVerified)
	writeEntryToStore(t, ml.userStore, "gl-c", "C", "content", VerificationVerified)

	if err := LinkMemories("gl-a", "gl-b", "first"); err != nil {
		t.Fatalf("LinkMemories(gl-a, gl-b) 失败: %v", err)
	}
	if err := LinkMemories("gl-a", "gl-c", "second"); err != nil {
		t.Fatalf("LinkMemories(gl-a, gl-c) 失败: %v", err)
	}

	links, err := GetLinks("gl-a")
	if err != nil {
		t.Fatalf("GetLinks(gl-a) 失败: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("GetLinks(gl-a) 返回 %d 条, 期望 2", len(links))
	}

	// 外部修改切片不应污染内存
	links[0] = "tampered"
	links = append(links, "injected")

	// 再次 GetLinks 验证未污染
	links2, err := GetLinks("gl-a")
	if err != nil {
		t.Fatalf("第二次 GetLinks(gl-a) 失败: %v", err)
	}
	if containsString(links2, "tampered") || containsString(links2, "injected") {
		t.Errorf("副本失效: GetLinks 返回的切片被外部修改污染, 实际 %v", links2)
	}
	if len(links2) != 2 {
		t.Errorf("GetLinks 副本长度变化: 期望 2, 实际 %d (%v)", len(links2), links2)
	}
}

// TestGetLinks_not_found 验证 name 不存在返回 error。
func TestGetLinks_not_found(t *testing.T) {
	_, _ = setupLinkTest(t, "/test/work/get-links-not-found")

	_, err := GetLinks("nonexistent")
	if err == nil {
		t.Fatal("GetLinks(nonexistent) 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), ErrEntryNotFound.Error()) {
		t.Errorf("error 应含 ErrEntryNotFound: %v", err)
	}
}

// TestGetLinkReason 验证 GetLinkReason 返回 reason + 不存在返回空字符串。
func TestGetLinkReason(t *testing.T) {
	_, ml := setupLinkTest(t, "/test/work/get-reason")
	writeEntryToStore(t, ml.userStore, "gr-a", "A", "content", VerificationVerified)
	writeEntryToStore(t, ml.userStore, "gr-b", "B", "content", VerificationVerified)

	if err := LinkMemories("gr-a", "gr-b", "test reason"); err != nil {
		t.Fatalf("LinkMemories 失败: %v", err)
	}

	if got := GetLinkReason("gr-a", "gr-b"); got != "test reason" {
		t.Errorf("GetLinkReason(gr-a, gr-b) = %q, 期望 %q", got, "test reason")
	}
	if got := GetLinkReason("gr-b", "gr-a"); got != "test reason" {
		t.Errorf("GetLinkReason(gr-b, gr-a) = %q, 期望 %q（双向对称）", got, "test reason")
	}
	if got := GetLinkReason("nonexistent", "gr-a"); got != "" {
		t.Errorf("GetLinkReason(nonexistent, gr-a) = %q, 期望空字符串", got)
	}
	if got := GetLinkReason("gr-a", "nonexistent"); got != "" {
		t.Errorf("GetLinkReason(gr-a, nonexistent) = %q, 期望空字符串", got)
	}
}

// containsString 检查切片是否含指定字符串（测试辅助）。
func containsString(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}
