// Package feedback User反馈功能unit test
package feedback

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestNewEntry 测试创建新的反馈记录
func TestNewEntry(t *testing.T) {
	entry := NewEntry("测试反馈消息", "0.2.0", "test-model")

	if entry == nil {
		t.Fatal("NewEntry 返回 nil")
	}

	if entry.ID == "" {
		t.Error("反馈 ID should not be empty")
	}

	if entry.Timestamp == "" {
		t.Error("时间戳should not be empty")
	}

	if entry.Version != "0.2.0" {
		t.Errorf("Version = %q, want %q", entry.Version, "0.2.0")
	}

	if entry.OS != runtime.GOOS {
		t.Errorf("OS = %q, want %q", entry.OS, runtime.GOOS)
	}

	if entry.Arch != runtime.GOARCH {
		t.Errorf("Arch = %q, want %q", entry.Arch, runtime.GOARCH)
	}

	if entry.Model != "test-model" {
		t.Errorf("Model = %q, want %q", entry.Model, "test-model")
	}

	if entry.Message != "测试反馈消息" {
		t.Errorf("Message = %q, want %q", entry.Message, "测试反馈消息")
	}
}

// TestNewEntryIDFormat 测试反馈 ID 格式
func TestNewEntryIDFormat(t *testing.T) {
	entry := NewEntry("test", "0.1.0", "")

	parts := strings.Split(entry.ID, "_")
	if len(parts) != 3 {
		t.Fatalf("ID 格式应为 YYYYMMDD_HHMMSS_XXXXXXXX, actual: %s", entry.ID)
	}

	if len(parts[0]) != 8 {
		t.Errorf("日期部分长度应为 8, actual: %s (%d)", parts[0], len(parts[0]))
	}

	if len(parts[1]) != 6 {
		t.Errorf("时间部分长度应为 6, actual: %s (%d)", parts[1], len(parts[1]))
	}

	if len(parts[2]) != 8 {
		t.Errorf("随机部分长度应为 8, actual: %s (%d)", parts[2], len(parts[2]))
	}
}

// TestSaveAndLoad 测试反馈的保存和加载
func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	entry := NewEntry("保存测试消息", "0.2.0", "")

	if err := entry.Save(dir); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	filePath := filepath.Join(dir, entry.ID+".json")
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Errorf("反馈文件未创建: %s", filePath)
	}

	loaded, err := Load(dir, entry.ID)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}

	if loaded.ID != entry.ID {
		t.Errorf("加载的 ID = %q, want %q", loaded.ID, entry.ID)
	}
	if loaded.Message != entry.Message {
		t.Errorf("加载的 Message = %q, want %q", loaded.Message, entry.Message)
	}
	if loaded.Version != entry.Version {
		t.Errorf("加载的 Version = %q, want %q", loaded.Version, entry.Version)
	}
}

// TestSaveCreatesDirectory 测试保存时自动创建目录
func TestSaveCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "feedback", "subdir")

	entry := NewEntry("目录创建测试", "0.1.0", "")

	if err := entry.Save(dir); err != nil {
		t.Fatalf("Save 应自动创建目录: %v", err)
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("目录应该被自动创建")
	}
}

// TestLoadNonExistent 测试加载不存在的反馈
func TestLoadNonExistent(t *testing.T) {
	dir := t.TempDir()

	_, err := Load(dir, "nonexistent")
	if err == nil {
		t.Error("加载不存在的反馈should return error")
	}

	if !strings.Contains(err.Error(), "不存在") && !strings.Contains(err.Error(), "not found") {
		t.Errorf("错误信息should contain'不存在', actual: %v", err)
	}
}

// TestList 测试列出所有反馈
func TestList(t *testing.T) {
	dir := t.TempDir()

	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List 空目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("空目录should return 0 条, actual: %d", len(entries))
	}

	e1 := NewEntry("消息1", "0.1.0", "")
	e2 := NewEntry("消息2", "0.1.0", "")
	e3 := NewEntry("消息3", "0.1.0", "")

	if err := e1.Save(dir); err != nil {
		t.Fatalf("Save e1 失败: %v", err)
	}
	if err := e2.Save(dir); err != nil {
		t.Fatalf("Save e2 失败: %v", err)
	}
	if err := e3.Save(dir); err != nil {
		t.Fatalf("Save e3 失败: %v", err)
	}

	entries, err = List(dir)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("应有 3 条反馈, actual: %d", len(entries))
	}

	for i := 0; i < len(entries)-1; i++ {
		if entries[i].Timestamp < entries[i+1].Timestamp {
			t.Errorf("反馈应按时间倒序排列, 但 %q < %q", entries[i].Timestamp, entries[i+1].Timestamp)
		}
	}
}

// TestGenerateIssueMarkdown 测试生成 Issue 模板
func TestGenerateIssueMarkdown(t *testing.T) {
	entry := NewEntry("这是一个测试反馈", "0.2.0", "claude-test")

	markdown := entry.GenerateIssueMarkdown()

	if !strings.Contains(markdown, "反馈") {
		t.Error("Issue 模板should contain'反馈'")
	}

	if !strings.Contains(markdown, "这是一个测试反馈") {
		t.Error("Issue 模板should contain原始消息")
	}

	if !strings.Contains(markdown, "0.2.0") {
		t.Error("Issue 模板should contain版本号")
	}

	if !strings.Contains(markdown, entry.OS) {
		t.Error("Issue 模板should contain操作系统信息")
	}

	if !strings.Contains(markdown, "claude-test") {
		t.Error("Issue 模板should contain模型名称")
	}
}

// TestGenerateIssueMarkdownNoModel 测试不包含模型时的 Issue 模板
func TestGenerateIssueMarkdownNoModel(t *testing.T) {
	entry := NewEntry("无模型测试", "0.1.0", "")

	markdown := entry.GenerateIssueMarkdown()

	if strings.Contains(markdown, "| 模型 |") {
		t.Error("Issue 模板should not contain模型行（当模型为空时）")
	}
}

// TestGetGitHubIssuesURL 测试获取 GitHub Issues 链接
func TestGetGitHubIssuesURL(t *testing.T) {
	url := GetGitHubIssuesURL()

	if url == "" {
		t.Error("GitHub Issues URL should not be empty")
	}

	if !strings.Contains(url, "github.com") {
		t.Errorf("URL should contain github.com, actual: %s", url)
	}
}

// TestGetDefaultDir 测试获取默认反馈目录
func TestGetDefaultDir(t *testing.T) {
	dir, err := GetDefaultDir()

	if err != nil {
		t.Fatalf("GetDefaultDir 失败: %v", err)
	}

	if dir == "" {
		t.Error("默认目录should not be empty")
	}

	if !strings.Contains(dir, ".whale") {
		t.Errorf("默认目录should contain .whale, actual: %s", dir)
	}
}

// TestSaveUnicodeMessage 测试保存包含 Unicode 字符的消息
func TestSaveUnicodeMessage(t *testing.T) {
	dir := t.TempDir()

	message := "中文消息 🎉 emoji テスト"
	entry := NewEntry(message, "0.1.0", "")

	if err := entry.Save(dir); err != nil {
		t.Fatalf("Save Unicode 消息失败: %v", err)
	}

	loaded, err := Load(dir, entry.ID)
	if err != nil {
		t.Fatalf("Load Unicode 消息失败: %v", err)
	}

	if loaded.Message != message {
		t.Errorf("Unicode 消息不匹配: %q != %q", loaded.Message, message)
	}
}

// TestSaveLongMessage 测试保存长消息
func TestSaveLongMessage(t *testing.T) {
	dir := t.TempDir()

	longMsg := strings.Repeat("很长的消息内容", 200)
	entry := NewEntry(longMsg, "0.1.0", "")

	if err := entry.Save(dir); err != nil {
		t.Fatalf("Save 长消息失败: %v", err)
	}

	loaded, err := Load(dir, entry.ID)
	if err != nil {
		t.Fatalf("Load 长消息失败: %v", err)
	}

	if loaded.Message != longMsg {
		t.Error("长消息被截断")
	}
}

// TestTruncateMessage 测试消息截断
func TestTruncateMessage(t *testing.T) {
	tests := []struct {
		msg    string
		maxLen int
		want   string
	}{
		{"短消息", 50, "短消息"},
		{"很长很长很长很长很长很长很长很长很长很长很长很长很长很长的消息", 10, "很长很长很长很长很长..."},
		{"hello\nworld", 50, "hello world"},
		{"hello\rworld", 50, "hello world"},
	}

	for _, tt := range tests {
		got := truncateMessage(tt.msg, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateMessage(%q, %d) = %q, want %q", tt.msg, tt.maxLen, got, tt.want)
		}
	}
}

// testNow 返回当前时间，用于 generateID 测试
func testNow() time.Time {
	return time.Now()
}

// ===== 6-A 新增测试：敏感信息脱敏 + 文件上限 =====

// TestRedactSensitive_GitHubToken 验证：GitHub PAT/OAuth/user/server/refresh token 全部脱敏。
// 方案 A 后：标签统一为 [REDACTED]（委托 secret SSOT）。
func TestRedactSensitive_GitHubToken(t *testing.T) {
	// 5 种前缀 + 36 字符后缀（真实 GitHub token 格式）
	cases := []struct {
		input string
		want  string
	}{
		{"ghp_" + strings.Repeat("a", 36), "[REDACTED]"},
		{"gho_" + strings.Repeat("b", 36), "[REDACTED]"},
		{"ghu_" + strings.Repeat("c", 36), "[REDACTED]"},
		{"ghs_" + strings.Repeat("d", 36), "[REDACTED]"},
		{"ghr_" + strings.Repeat("e", 36), "[REDACTED]"},
	}
	for _, c := range cases {
		got := redactSensitive(c.input)
		if got != c.want {
			t.Errorf("redactSensitive(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestRedactSensitive_AWSKey 验证：AWS 9 个官方前缀的 Access Key ID 脱敏。
func TestRedactSensitive_AWSKey(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"AKIA" + strings.Repeat("X", 16), "[REDACTED]"},
		{"ASIA" + strings.Repeat("Y", 16), "[REDACTED]"},
		{"AGPA" + strings.Repeat("Z", 16), "[REDACTED]"},
		{"AIDA" + strings.Repeat("0", 16), "[REDACTED]"},
		{"AROA" + strings.Repeat("1", 16), "[REDACTED]"},
		{"AIPA" + strings.Repeat("2", 16), "[REDACTED]"},
		{"ANPA" + strings.Repeat("3", 16), "[REDACTED]"},
		{"ANVA" + strings.Repeat("4", 16), "[REDACTED]"},
		{"ASCA" + strings.Repeat("5", 16), "[REDACTED]"},
	}
	for _, c := range cases {
		got := redactSensitive(c.input)
		if got != c.want {
			t.Errorf("redactSensitive(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestRedactSensitive_SlackToken 验证：Slack bot/user/oauth/app/workspace token 脱敏。
func TestRedactSensitive_SlackToken(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"xoxb-" + strings.Repeat("a", 30), "[REDACTED]"}, // bot token
		{"xoxu-" + strings.Repeat("b", 30), "[REDACTED]"}, // user token
		{"xoxo-" + strings.Repeat("c", 30), "[REDACTED]"}, // oauth token
		{"xoxa-" + strings.Repeat("d", 30), "[REDACTED]"}, // app token
		{"xoxp-" + strings.Repeat("e", 30), "[REDACTED]"}, // workspace token
	}
	for _, c := range cases {
		got := redactSensitive(c.input)
		if got != c.want {
			t.Errorf("redactSensitive(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestRedactSensitive_OpenAIKey 验证：OpenAI sk- 前缀 key 脱敏（含 sk-proj-/sk-ant- 变体）。
func TestRedactSensitive_OpenAIKey(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"sk-" + strings.Repeat("a", 40), "[REDACTED]"},                    // 经典格式
		{"sk-proj-" + strings.Repeat("b", 40), "[REDACTED]"},               // proj 格式
		{"sk-ant-" + strings.Repeat("c", 40), "[REDACTED]"},                // ant 格式
		{"sk-" + strings.Repeat("d", 20), "[REDACTED]"},                    // 下限 20 字符
		{"sk-" + strings.Repeat("e", 19), "sk-" + strings.Repeat("e", 19)}, // 19 字符不匹配（下限 20）
	}
	for _, c := range cases {
		got := redactSensitive(c.input)
		if got != c.want {
			t.Errorf("redactSensitive(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestRedactSensitive_JWT 验证：三段式 JWT（eyJ.eyJ.eyJ）脱敏。
// 方案 A 后：JWT 规则委托 secret SSOT（各段要求 ≥20 字符）。
func TestRedactSensitive_JWT(t *testing.T) {
	// 三段各 ≥20 字符的真实 JWT（secret 侧规则下限），应被完整脱敏。
	jwt := "eyJ" + strings.Repeat("a", 30) + "." + "eyJ" + strings.Repeat("b", 30) + "." + "eyJ" + strings.Repeat("c", 30)
	got := redactSensitive(jwt)
	if got != "[REDACTED]" {
		t.Errorf("redactSensitive(JWT) = %q, want [REDACTED]", got)
	}

	// 短 JWT（三段各不足 secret 侧 20 字符下限）不匹配，原样返回 —— 非缺陷，
	// 真实 JWT 各段远长于 20 字符，此例仅为规则严苛度差异的记录。
	shortJWT := "eyJabcdefghij.eyJklmnopqrst.eyJuvwxyz1234"
	got = redactSensitive(shortJWT)
	if got != shortJWT {
		t.Errorf("redactSensitive(shortJWT) = %q, want 原样返回 %q", got, shortJWT)
	}
}

// TestRedactSensitive_Bearer 验证：Bearer token 脱敏（大小写不敏感）。
func TestRedactSensitive_Bearer(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Bearer abc123def456", "Bearer [REDACTED]"},
		{"bearer xyz789ghi012", "Bearer [REDACTED]"}, // 小写 → 归一为 "Bearer "
		{"BEARER TOKEN_HERE", "Bearer [REDACTED]"},   // 全大写 → 归一为 "Bearer "
		{"Authorization: Bearer my-secret-token", "Authorization: Bearer [REDACTED]"},
	}
	for _, c := range cases {
		got := redactSensitive(c.input)
		if got != c.want {
			t.Errorf("redactSensitive(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestRedactSensitive_Password 验证：password=/api_key:/secret: 等赋值脱敏。
func TestRedactSensitive_Password(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"password=secret123", "password=[REDACTED]"},
		{"api_key: mykey", "api_key: [REDACTED]"},
		{"api-key=abc", "api-key=[REDACTED]"},
		{"apikey=\"xyz\"", "apikey=[REDACTED]"},
		{"auth_token: bearer123", "auth_token: [REDACTED]"},
		{"access_token=xyz", "access_token=[REDACTED]"},
		{"secret_key: mysecret", "secret_key: [REDACTED]"},
		{"private_key=\"priv\"", "private_key=[REDACTED]"},
		{"PASSWORD=SecretCase", "PASSWORD=[REDACTED]"}, // 大小写不敏感
	}
	for _, c := range cases {
		got := redactSensitive(c.input)
		if got != c.want {
			t.Errorf("redactSensitive(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestRedactSensitive_URLUserinfo 验证：://user:pass@host 格式脱敏。
func TestRedactSensitive_URLUserinfo(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"postgres://user:pass@localhost:5432", "postgres://[REDACTED]:[REDACTED]@localhost:5432"},
		{"https://admin:secret@example.com", "https://[REDACTED]:[REDACTED]@example.com"},
		{"mongodb://root:pwd@cluster.mongodb.net", "mongodb://[REDACTED]:[REDACTED]@cluster.mongodb.net"},
		{"redis://default:redis@cache:6379", "redis://[REDACTED]:[REDACTED]@cache:6379"},
		{"HTTPS://User:Pass@Host", "HTTPS://[REDACTED]:[REDACTED]@Host"}, // 大小写不敏感
	}
	for _, c := range cases {
		got := redactSensitive(c.input)
		if got != c.want {
			t.Errorf("redactSensitive(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestRedactSensitive_NoSensitive 验证：不含敏感信息的文本原样返回。
func TestRedactSensitive_NoSensitive(t *testing.T) {
	cases := []string{
		"这是一个普通反馈",
		"希望增加深色模式",
		"The app crashes when I click save",
		"中文消息 🎉 emoji テスト",
		"", // 空字符串
		"no secrets here, just normal feedback about the UI",
	}
	for _, c := range cases {
		got := redactSensitive(c)
		if got != c {
			t.Errorf("redactSensitive(%q) = %q, want 原样返回", c, got)
		}
	}
}

// TestRedactSensitive_MultipleTypes 验证：一条消息含多种密钥，全部脱敏。
func TestRedactSensitive_MultipleTypes(t *testing.T) {
	input := "我的 GitHub token 是 ghp_" + strings.Repeat("a", 36) +
		"，AWS key 是 AKIA" + strings.Repeat("B", 16) +
		"，OpenAI key 是 sk-" + strings.Repeat("c", 40) +
		"，数据库连接 postgres://user:pass@db:5432"

	got := redactSensitive(input)

	// 验证每种类型都被脱敏（不验证精确字符串，因正则顺序可能影响重叠匹配）
	mustContain := []string{
		"[REDACTED]",
	}
	for _, tag := range mustContain {
		if !strings.Contains(got, tag) {
			t.Errorf("redactSensitive 结果缺少 %q, got: %s", tag, got)
		}
	}

	// 验证原始密钥不再出现
	mustNotContain := []string{
		"ghp_" + strings.Repeat("a", 36),
		"AKIA" + strings.Repeat("B", 16),
		"sk-" + strings.Repeat("c", 40),
		"user:pass@db",
	}
	for _, secret := range mustNotContain {
		if strings.Contains(got, secret) {
			t.Errorf("redactSensitive 结果仍含敏感信息 %q, got: %s", secret, got)
		}
	}
}

// TestNewEntry_RedactsMessage 验证：NewEntry 构造的 Entry.Message 已脱敏，原始密钥不残留。
func TestNewEntry_RedactsMessage(t *testing.T) {
	secretMsg := "我的 token 是 ghp_" + strings.Repeat("x", 36) + " 请帮我看下"
	entry := NewEntry(secretMsg, "0.2.0", "test-model")

	// Entry.Message 应已脱敏
	if strings.Contains(entry.Message, "ghp_"+strings.Repeat("x", 36)) {
		t.Errorf("NewEntry 未脱敏 GitHub token, Message = %q", entry.Message)
	}
	if !strings.Contains(entry.Message, "[REDACTED]") {
		t.Errorf("NewEntry 脱敏后应含 [REDACTED], Message = %q", entry.Message)
	}

	// 其他字段不受影响
	if entry.Version != "0.2.0" {
		t.Errorf("Version = %q, want 0.2.0", entry.Version)
	}
	if entry.Model != "test-model" {
		t.Errorf("Model = %q, want test-model", entry.Model)
	}
}

// TestEnforceFileLimit_DeletesOldest 验证：文件数超 max 时，按文件名字典序删除最旧的。
func TestEnforceFileLimit_DeletesOldest(t *testing.T) {
	dir := t.TempDir()

	// 创建 105 个文件，文件名按 ID 格式 YYYYMMDD_HHMMSS_xxxxxxxx
	for i := 0; i < 105; i++ {
		sec := i % 60
		min := i / 60
		id := fmt.Sprintf("20260703_%02d%02d00_abcd%04d", min, sec, i)
		path := filepath.Join(dir, id+".json")
		if err := os.WriteFile(path, []byte(`{"id":"`+id+`"}`), 0600); err != nil {
			t.Fatalf("创建测试文件失败: %v", err)
		}
	}

	// 调用 enforceFileLimit，max=100
	if err := enforceFileLimit(dir, 100); err != nil {
		t.Fatalf("enforceFileLimit 失败: %v", err)
	}

	// 验证剩余文件数 = 100
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir 失败: %v", err)
	}
	jsonCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			jsonCount++
		}
	}
	if jsonCount != 100 {
		t.Errorf("剩余文件数 = %d, want 100", jsonCount)
	}

	// 验证最旧的 5 个被删除（字典序最小的 5 个）
	for i := 0; i < 5; i++ {
		sec := i % 60
		min := i / 60
		id := fmt.Sprintf("20260703_%02d%02d00_abcd%04d", min, sec, i)
		path := filepath.Join(dir, id+".json")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("最旧文件 %s 应被删除, 但仍存在", path)
		}
	}

	// 验证最新的 100 个仍存在（从 i=5 到 i=104）
	for i := 5; i < 105; i++ {
		sec := i % 60
		min := i / 60
		id := fmt.Sprintf("20260703_%02d%02d00_abcd%04d", min, sec, i)
		path := filepath.Join(dir, id+".json")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("文件 %s 应保留, 但已删除", path)
		}
	}
}

// TestEnforceFileLimit_UnderLimit 验证：文件数未超 max 时不删除任何文件。
func TestEnforceFileLimit_UnderLimit(t *testing.T) {
	dir := t.TempDir()

	// 创建 50 个文件（< max=100）
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("20260703_%06d_abcd%04d", i, i)
		path := filepath.Join(dir, id+".json")
		if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
			t.Fatalf("创建测试文件失败: %v", err)
		}
	}

	if err := enforceFileLimit(dir, 100); err != nil {
		t.Fatalf("enforceFileLimit 失败: %v", err)
	}

	// 验证 50 个文件全部保留
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir 失败: %v", err)
	}
	jsonCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			jsonCount++
		}
	}
	if jsonCount != 50 {
		t.Errorf("剩余文件数 = %d, want 50（未超限不应删除）", jsonCount)
	}
}

// TestSave_TriggersEnforceFileLimit 验证：Save 后若目录超 100 文件，自动清理最旧。
func TestSave_TriggersEnforceFileLimit(t *testing.T) {
	dir := t.TempDir()

	// 预创建 100 个旧反馈文件（按 ID 格式，字典序最小）
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("20260101_%06d_old000%02d", i, i)
		path := filepath.Join(dir, id+".json")
		if err := os.WriteFile(path, []byte(`{"id":"`+id+`"}`), 0600); err != nil {
			t.Fatalf("预创建文件失败: %v", err)
		}
	}

	// 新建一个 Entry 并 Save（ID 会是当前时间 20260703_xxxxxx，字典序最大）
	entry := NewEntry("新反馈消息", "0.2.0", "")
	if err := entry.Save(dir); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	// 验证目录文件数 = 100（清理了 1 个最旧的）
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir 失败: %v", err)
	}
	jsonCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			jsonCount++
		}
	}
	if jsonCount != 100 {
		t.Errorf("Save 后文件数 = %d, want 100（应清理 1 个最旧）", jsonCount)
	}

	// 验证新保存的文件仍存在
	newPath := filepath.Join(dir, entry.ID+".json")
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		t.Errorf("新保存的反馈文件 %s 不应被删除", newPath)
	}

	// 验证最旧的 1 个被删除（20260101_000000_old00000）
	oldestPath := filepath.Join(dir, "20260101_000000_old00000.json")
	if _, err := os.Stat(oldestPath); !os.IsNotExist(err) {
		t.Errorf("最旧文件 %s 应被删除, 但仍存在", oldestPath)
	}
}

// ===== M19-04 改造新增测试：/feedback 直创 GitHub Issue =====

// mockIssueCreator 是 IssueCreator 接口的测试替身。
// 捕获最后一次 CreateIssue 的参数，并按预设返回 issueURL / err。
type mockIssueCreator struct {
	lastTitle  string
	lastBody   string
	lastLabels []string
	lastCtx    context.Context
	returnURL  string
	returnErr  error
	called     int
}

// CreateIssue 实现 IssueCreator 接口，记录参数并返回预设结果。
func (m *mockIssueCreator) CreateIssue(ctx context.Context, title, body string, labels []string) (string, error) {
	m.called++
	m.lastCtx = ctx
	m.lastTitle = title
	m.lastBody = body
	m.lastLabels = labels
	return m.returnURL, m.returnErr
}

// TestRedactSensitive_Exported 验证：导出的 RedactSensitive 与内部 redactSensitive 行为一致。
// CONFLICT-08 / 方案 A：command 包等外部调用方通过此导出函数复用 secret SSOT 脱敏逻辑。
func TestRedactSensitive_Exported(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"ghp_" + strings.Repeat("a", 36), "[REDACTED]"},
		{"sk-" + strings.Repeat("b", 40), "[REDACTED]"},
		{"普通反馈消息", "普通反馈消息"},
		{"", ""},
	}
	for _, c := range cases {
		got := RedactSensitive(c.input)
		if got != c.want {
			t.Errorf("RedactSensitive(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestBuildIssueTitle 验证：Issue 标题格式为 `[用户反馈] <前 30 字符>`，超长追加 "..."。
func TestBuildIssueTitle(t *testing.T) {
	// 短消息：完整保留
	short := "这是一个短反馈"
	got := BuildIssueTitle(short)
	want := "[用户反馈] 这是一个短反馈"
	if got != want {
		t.Errorf("BuildIssueTitle(short) = %q, want %q", got, want)
	}

	// 刚好 30 字符：不截断
	exact30 := strings.Repeat("字", 30)
	got = BuildIssueTitle(exact30)
	want = "[用户反馈] " + exact30
	if got != want {
		t.Errorf("BuildIssueTitle(30 runes) = %q, want %q", got, want)
	}

	// 31 字符：截断为前 30 + "..."
	long31 := strings.Repeat("字", 31)
	got = BuildIssueTitle(long31)
	want = "[用户反馈] " + strings.Repeat("字", 30) + "..."
	if got != want {
		t.Errorf("BuildIssueTitle(31 runes) = %q, want %q", got, want)
	}

	// 含换行：替换为空格
	multiline := "第一行\n第二行"
	got = BuildIssueTitle(multiline)
	want = "[用户反馈] 第一行 第二行"
	if got != want {
		t.Errorf("BuildIssueTitle(multiline) = %q, want %q", got, want)
	}
}

// TestBuildIssueBody 验证：Issue body 含用户内容 + 系统环境信息。
func TestBuildIssueBody(t *testing.T) {
	entry := NewEntry("测试反馈内容", "0.3.0", "test-model")
	body := BuildIssueBody(entry)

	mustContain := []string{
		"### 用户反馈内容",
		"测试反馈内容",
		"### 系统环境信息",
		"| Whale Version | 0.3.0 |",
		"| OS | " + entry.OS + " |",
		"| Arch | " + entry.Arch + " |",
		"| Model | test-model |",
		"| Feedback ID | " + entry.ID + " |",
		"| Time | " + entry.Timestamp + " |",
	}
	for _, s := range mustContain {
		if !strings.Contains(body, s) {
			t.Errorf("BuildIssueBody 缺少 %q, body:\n%s", s, body)
		}
	}
}

// TestBuildIssueBody_NoModel 验证：Model 为空时 body 不含 Model 行。
func TestBuildIssueBody_NoModel(t *testing.T) {
	entry := NewEntry("无模型反馈", "0.1.0", "")
	body := BuildIssueBody(entry)
	if strings.Contains(body, "| Model |") {
		t.Errorf("Model 为空时 body 不应含 Model 行, body:\n%s", body)
	}
}

// TestSubmitToGitHub_Success 验证：mock creator 返回成功时，SubmitToGitHub 返回 Issue URL。
// 同时验证传给 creator 的 labels 包含 user-feedback。
func TestSubmitToGitHub_Success(t *testing.T) {
	entry := NewEntry("正常反馈消息", "0.2.0", "")
	mock := &mockIssueCreator{returnURL: "https://github.com/owner/repo/issues/42"}

	url, err := SubmitToGitHub(entry, mock)
	if err != nil {
		t.Fatalf("SubmitToGitHub 失败: %v", err)
	}
	if url != "https://github.com/owner/repo/issues/42" {
		t.Errorf("url = %q, want https://github.com/owner/repo/issues/42", url)
	}
	if mock.called != 1 {
		t.Errorf("creator 调用次数 = %d, want 1", mock.called)
	}
	if !strings.HasPrefix(mock.lastTitle, "[用户反馈]") {
		t.Errorf("creator 接收的 title 不带前缀: %q", mock.lastTitle)
	}
	found := false
	for _, l := range mock.lastLabels {
		if l == "user-feedback" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("creator 接收的 labels 不含 user-feedback: %v", mock.lastLabels)
	}
	if mock.lastCtx == nil {
		t.Error("creator 接收的 ctx 不应为 nil")
	}
}

// TestSubmitToGitHub_Failure 验证：mock creator 返回错误时，SubmitToGitHub 透传错误。
func TestSubmitToGitHub_Failure(t *testing.T) {
	entry := NewEntry("失败反馈", "0.2.0", "")
	mock := &mockIssueCreator{returnErr: fmt.Errorf("network timeout")}

	_, err := SubmitToGitHub(entry, mock)
	if err == nil {
		t.Fatal("SubmitToGitHub 应返回错误")
	}
	if !strings.Contains(err.Error(), "network timeout") {
		t.Errorf("err 应包含原始错误信息, got: %v", err)
	}
	if !strings.Contains(err.Error(), "create GitHub issue failed") {
		t.Errorf("err 应包含包装前缀, got: %v", err)
	}
}

// TestSubmitToGitHub_NilCreator 验证：creator 为 nil 时返回错误。
func TestSubmitToGitHub_NilCreator(t *testing.T) {
	entry := NewEntry("test", "0.1.0", "")
	_, err := SubmitToGitHub(entry, nil)
	if err == nil {
		t.Error("nil creator 应返回错误")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("err 应提示 nil, got: %v", err)
	}
}

// TestSubmitToGitHub_NilEntry 验证：entry 为 nil 时返回错误。
func TestSubmitToGitHub_NilEntry(t *testing.T) {
	mock := &mockIssueCreator{returnURL: "https://example.com"}
	_, err := SubmitToGitHub(nil, mock)
	if err == nil {
		t.Error("nil entry 应返回错误")
	}
	if mock.called != 0 {
		t.Errorf("entry 为 nil 时不应调用 creator, called = %d", mock.called)
	}
}

// TestSubmitToGitHub_Redacted 验证：含密钥的反馈消息经 NewEntry 脱敏后，
// 传给 creator 的 title 和 body 不含原始密钥。
// 项目宪法硬约束 4：脱敏必须先于 Issue 创建（防泄漏）。
func TestSubmitToGitHub_Redacted(t *testing.T) {
	// 构造含多种密钥的反馈消息
	ghToken := "ghp_" + strings.Repeat("x", 36)
	awsKey := "AKIA" + strings.Repeat("B", 16)
	openaiKey := "sk-" + strings.Repeat("c", 40)
	rawMsg := "我的 GitHub token 是 " + ghToken +
		"，AWS key 是 " + awsKey +
		"，OpenAI key 是 " + openaiKey

	entry := NewEntry(rawMsg, "0.2.0", "")
	mock := &mockIssueCreator{returnURL: "https://github.com/owner/repo/issues/1"}

	_, err := SubmitToGitHub(entry, mock)
	if err != nil {
		t.Fatalf("SubmitToGitHub 失败: %v", err)
	}

	// 验证传给 creator 的 title 和 body 不含原始密钥
	secretFragments := []string{ghToken, awsKey, openaiKey}
	for _, secret := range secretFragments {
		if strings.Contains(mock.lastTitle, secret) {
			t.Errorf("title 含原始密钥 %q, title: %s", secret, mock.lastTitle)
		}
		if strings.Contains(mock.lastBody, secret) {
			t.Errorf("body 含原始密钥 %q, body: %s", secret, mock.lastBody)
		}
	}

	// 验证脱敏标签出现（方案 A 后标签统一为 [REDACTED]）
	mustContainTags := []string{
		"[REDACTED]",
	}
	for _, tag := range mustContainTags {
		if !strings.Contains(mock.lastBody, tag) {
			t.Errorf("body 缺少脱敏标签 %q, body: %s", tag, mock.lastBody)
		}
	}
}
