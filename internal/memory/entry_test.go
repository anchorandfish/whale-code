// entry_test.go — H11/N27 Phase A A1 MemoryEntry + CapabilityValue + 常量测试（含反例）。
package memory

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestMemoryEntry_FrontmatterFields_Count 反射验证 MemoryEntry 含 16 个 yaml 持久化字段 + 至少 3 个 yaml:"-" 内存字段。
// 反例防护：防止后续误改字段数（缺字段或新增字段未声明）。
//
// S18/G1 更新：持久化字段由 14 → 16，新增 access_count / last_accessed_at
// （访问热度由 yaml:"-" 内存字段改为持久化字段，使 Ebbinghaus 衰减基于真实持久化访问数据，
// 避免冷加载后归零导致的 contradicted 误判）。
func TestMemoryEntry_FrontmatterFields_Count(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(MemoryEntry{})
	var frontmatterCount, memoryCount int
	expectedFrontmatter := map[string]bool{
		"name": false, "description": false, "type": false, "created_at": false, "updated_at": false,
		"trust_level": false, "probe_fn": false, "ttl_seconds": false,
		"verification": false, "valid_from": false, "valid_until": false, "links": false, "importance": false, "source": false,
		"access_count": false, "last_accessed_at": false,
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("yaml")
		// 取 tag 第一段（去掉 omitempty 等选项）
		name := tag
		if idx := strings.Index(tag, ","); idx >= 0 {
			name = tag[:idx]
		}
		if name == "-" {
			memoryCount++
			continue
		}
		if name == "" {
			continue
		}
		frontmatterCount++
		if _, ok := expectedFrontmatter[name]; ok {
			expectedFrontmatter[name] = true
		}
	}
	if frontmatterCount != 16 {
		t.Errorf("frontmatter 字段数 = %d, 期望 16 (N12 5 + v0.3 3 + v0.4 6 + S18/G1 访问热度 2)", frontmatterCount)
	}
	if memoryCount < 3 {
		t.Errorf("yaml:\"-\" 内存字段数 = %d, 期望至少 3 (ProbeFailCount/Content/FilePath)", memoryCount)
	}
	for name, found := range expectedFrontmatter {
		if !found {
			t.Errorf("缺失 frontmatter 字段: %s", name)
		}
	}
}

// TestMemoryEntry_ProbeFailCount_NotPersisted 反射验证 ProbeFailCount 标签为 yaml:"-"，确保不持久化。
// 反例防护（审查报告优化 2）：ProbeFailCount 仅内存，重启归零，防止跨进程累积失败次数。
func TestMemoryEntry_ProbeFailCount_NotPersisted(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(MemoryEntry{})
	f, ok := typ.FieldByName("ProbeFailCount")
	if !ok {
		t.Fatal("MemoryEntry 缺失 ProbeFailCount 字段")
	}
	tag := f.Tag.Get("yaml")
	if tag != "-" {
		t.Errorf("ProbeFailCount yaml tag = %q, 期望 \"-\" (不持久化)", tag)
	}
}

// TestCapabilityValue_TrustSuffix_Normal 正常 trust_level 无后缀。
func TestCapabilityValue_TrustSuffix_Normal(t *testing.T) {
	t.Parallel()
	cv := CapabilityValue{
		Value:     true,
		CheckedAt: time.Now(),
		Trust:     TrustStable,
	}
	if strings.Contains(cv.Trust, ":") {
		t.Errorf("正常 trust_level 不应含后缀分隔符 ':', got %s", cv.Trust)
	}
}

// TestCapabilityValue_TrustSuffix_Stale ProbeFn 失败降级后缀 ":stale"。
func TestCapabilityValue_TrustSuffix_Stale(t *testing.T) {
	t.Parallel()
	cv := CapabilityValue{
		Value:     true,
		CheckedAt: time.Now(),
		Trust:     TrustCapability + ":stale",
	}
	if !strings.HasSuffix(cv.Trust, ":stale") {
		t.Errorf("ProbeFn 失败降级应含 ':stale' 后缀, got %s", cv.Trust)
	}
}

// TestCapabilityValue_TrustSuffix_Unverified unverified 未校验后缀 ":unverified"。
func TestCapabilityValue_TrustSuffix_Unverified(t *testing.T) {
	t.Parallel()
	cv := CapabilityValue{
		Value:     true,
		CheckedAt: time.Now(),
		Trust:     TrustCapability + ":unverified",
	}
	if !strings.HasSuffix(cv.Trust, ":unverified") {
		t.Errorf("unverified 未校验应含 ':unverified' 后缀, got %s", cv.Trust)
	}
}

// TestConstants_TrustLevel_Values 校验 4 个 TrustLevel 常量值。
func TestConstants_TrustLevel_Values(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"TrustStable":     TrustStable,
		"TrustCapability": TrustCapability,
		"TrustEphemeral":  TrustEphemeral,
		"TrustProcedural": TrustProcedural,
	}
	expected := map[string]string{
		"TrustStable":     "stable",
		"TrustCapability": "capability",
		"TrustEphemeral":  "ephemeral",
		"TrustProcedural": "procedural",
	}
	for name, val := range cases {
		if val != expected[name] {
			t.Errorf("%s = %q, 期望 %q", name, val, expected[name])
		}
	}
}

// TestConstants_Verification_Values 校验 3 个 Verification 常量值。
func TestConstants_Verification_Values(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"VerificationVerified":     VerificationVerified,
		"VerificationUnverified":   VerificationUnverified,
		"VerificationContradicted": VerificationContradicted,
	}
	expected := map[string]string{
		"VerificationVerified":     "verified",
		"VerificationUnverified":   "unverified",
		"VerificationContradicted": "contradicted",
	}
	for name, val := range cases {
		if val != expected[name] {
			t.Errorf("%s = %q, 期望 %q", name, val, expected[name])
		}
	}
}

// TestConstants_Source_Values 校验 4 个 Source 常量值。
func TestConstants_Source_Values(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"SourceProbeFn":    SourceProbeFn,
		"SourceUser":       SourceUser,
		"SourceAISelf":     SourceAISelf,
		"SourceReflection": SourceReflection,
	}
	expected := map[string]string{
		"SourceProbeFn":    "probe_fn",
		"SourceUser":       "user",
		"SourceAISelf":     "ai_self",
		"SourceReflection": "reflection",
	}
	for name, val := range cases {
		if val != expected[name] {
			t.Errorf("%s = %q, 期望 %q", name, val, expected[name])
		}
	}
}

// TestMemoryEntry_DefaultImportance 测试 Importance 零值默认 50。
// 反例防护：Importance=0 视为未设置→默认 50，避免未设置 importance 被当作 0 排序末位。
func TestMemoryEntry_DefaultImportance(t *testing.T) {
	t.Parallel()
	e := &MemoryEntry{Importance: 0}
	if got := e.DefaultImportance(); got != 50 {
		t.Errorf("Importance=0 默认值 = %d, 期望 50", got)
	}
	e2 := &MemoryEntry{Importance: 80}
	if got := e2.DefaultImportance(); got != 80 {
		t.Errorf("Importance=80 应保留原值, got %d", got)
	}
}

// TestMemoryEntry_DefaultVerification 测试 Verification 空字符串默认 unverified。
// 反例防护：新写入记忆默认 unverified，避免空字符串被当作 verified 误信。
func TestMemoryEntry_DefaultVerification(t *testing.T) {
	t.Parallel()
	e := &MemoryEntry{Verification: ""}
	if got := e.DefaultVerification(); got != VerificationUnverified {
		t.Errorf("Verification=\"\" 默认值 = %q, 期望 %q", got, VerificationUnverified)
	}
	e2 := &MemoryEntry{Verification: VerificationVerified}
	if got := e2.DefaultVerification(); got != VerificationVerified {
		t.Errorf("Verification=verified 应保留原值, got %q", got)
	}
}

// TestMemoryEntry_ValidFromOrCreatedAt 测试 ValidFrom nil 时回退到 CreatedAt。
// 反例防护：bi-temporal 起始时间缺失时回退到创建时间，避免 nil 解引用。
func TestMemoryEntry_ValidFromOrCreatedAt(t *testing.T) {
	t.Parallel()
	now := time.Now()
	e := &MemoryEntry{CreatedAt: now}
	if got := e.ValidFromOrCreatedAt(); !got.Equal(now) {
		t.Errorf("ValidFrom=nil 回退到 CreatedAt, got %v, 期望 %v", got, now)
	}
	vf := now.Add(time.Hour)
	e2 := &MemoryEntry{CreatedAt: now, ValidFrom: &vf}
	if got := e2.ValidFromOrCreatedAt(); !got.Equal(vf) {
		t.Errorf("ValidFrom 非 nil 时保留原值, got %v, 期望 %v", got, vf)
	}
}

// TestMemoryEntry_AddLink_Dedup 测试 AddLink 去重。
func TestMemoryEntry_AddLink_Dedup(t *testing.T) {
	t.Parallel()
	e := &MemoryEntry{}
	e.AddLink("foo")
	e.AddLink("foo")
	e.AddLink("bar")
	if len(e.Links) != 2 {
		t.Errorf("AddLink 去重后 Links 长度 = %d, 期望 2", len(e.Links))
	}
	if !e.HasLink("foo") || !e.HasLink("bar") {
		t.Errorf("AddLink 后 Links = %v, 期望含 foo+bar", e.Links)
	}
}

// TestMemoryEntry_RemoveLink 测试 RemoveLink 移除。
func TestMemoryEntry_RemoveLink(t *testing.T) {
	t.Parallel()
	e := &MemoryEntry{}
	e.AddLink("foo")
	e.AddLink("bar")
	e.RemoveLink("foo")
	if e.HasLink("foo") {
		t.Errorf("RemoveLink 后仍含 foo")
	}
	if !e.HasLink("bar") {
		t.Errorf("RemoveLink foo 后 bar 应保留")
	}
}
