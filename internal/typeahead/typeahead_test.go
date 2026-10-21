package typeahead

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractToken_AtStart 测试 @ 在输入开头时的 token 提取
func TestExtractToken_AtStart(t *testing.T) {
	input := "@internal/mention"
	token, start, isAt := ExtractToken(input, len(input))
	if !isAt {
		t.Fatal("expected isAt=true")
	}
	if token != "internal/mention" {
		t.Fatalf("expected token=internal/mention, got %s", token)
	}
	if start != 0 {
		t.Fatalf("expected start=0, got %d", start)
	}
}

// TestExtractToken_AfterSpace 测试 @ 在空格后的 token 提取
func TestExtractToken_AfterSpace(t *testing.T) {
	input := "hello @internal/ment"
	token, start, isAt := ExtractToken(input, len(input))
	if !isAt {
		t.Fatal("expected isAt=true")
	}
	if token != "internal/ment" {
		t.Fatalf("expected token=internal/ment, got %s", token)
	}
	if start != 6 {
		t.Fatalf("expected start=6, got %d", start)
	}
}

// TestExtractToken_EmptyToken 测试 @ 后无字符的 showOnEmpty 场景
func TestExtractToken_EmptyToken(t *testing.T) {
	input := "hello @"
	token, start, isAt := ExtractToken(input, len(input))
	if !isAt {
		t.Fatal("expected isAt=true for @ at end")
	}
	if token != "" {
		t.Fatalf("expected empty token, got %s", token)
	}
	if start < 0 {
		t.Fatalf("expected start >= 0, got %d", start)
	}
}

// TestExtractToken_NoAtSymbol 测试无 @ 时返回 false
func TestExtractToken_NoAtSymbol(t *testing.T) {
	input := "hello world"
	_, _, isAt := ExtractToken(input, len(input))
	if isAt {
		t.Fatal("expected isAt=false")
	}
}

// TestExtractToken_EmailNotTriggered 测试邮箱地址不触发 @ 补全
func TestExtractToken_EmailNotTriggered(t *testing.T) {
	input := "contact@example.com"
	_, _, isAt := ExtractToken(input, len(input))
	if isAt {
		t.Fatal("email should not trigger @ completion")
	}
}

// TestExtractToken_WindowsPath 测试 Windows 路径分隔符
func TestExtractToken_WindowsPath(t *testing.T) {
	input := `@.\internal\mention`
	token, _, isAt := ExtractToken(input, len(input))
	if !isAt {
		t.Fatal("expected isAt=true for Windows path")
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}
}

// TestApplySuggestion_File 测试文件补全（加空格后缀）
func TestApplySuggestion_File(t *testing.T) {
	input := "@inter"
	suggestion := Completion{
		DisplayText: "internal/mention/mention.go",
		IsDir:       false,
	}
	newInput, newCursor := ApplySuggestion(input, suggestion, 0, 5)
	if newInput != "@internal/mention/mention.go " {
		t.Fatalf("expected file suffix with space, got %q", newInput)
	}
	_ = newCursor
}

// TestApplySuggestion_Directory 测试目录补全（加路径分隔符后缀）
func TestApplySuggestion_Directory(t *testing.T) {
	input := "@inter"
	suggestion := Completion{
		DisplayText: "internal/mention",
		IsDir:       true,
	}
	newInput, _ := ApplySuggestion(input, suggestion, 0, 5)
	expected := "@internal/mention" + string(os.PathSeparator)
	if newInput != expected {
		t.Fatalf("expected dir suffix with separator, got %q", newInput)
	}
}

// TestApplySuggestion_MiddleInput 测试在输入中间应用补全
func TestApplySuggestion_MiddleInput(t *testing.T) {
	input := "hello @ment world"
	suggestion := Completion{
		DisplayText: "internal/mention/mention.go",
		IsDir:       false,
	}
	// @ 在位置 6, token "ment" 长度 4
	newInput, _ := ApplySuggestion(input, suggestion, 6, 4)
	if newInput != "hello @internal/mention/mention.go  world" {
		t.Fatalf("unexpected result: %q", newInput)
	}
}

// TestApplySuggestion_InvalidPosition 测试无效位置返回原输入
func TestApplySuggestion_InvalidPosition(t *testing.T) {
	input := "@test"
	suggestion := Completion{
		DisplayText: "test.go",
		IsDir:       false,
	}
	newInput, _ := ApplySuggestion(input, suggestion, -1, 4)
	if newInput != input {
		t.Fatalf("expected unchanged input for invalid position")
	}
}

// TestFindLongestCommonPrefix 测试最长公共前缀
func TestFindLongestCommonPrefix(t *testing.T) {
	suggestions := []Completion{
		{DisplayText: "internal/mention/mention.go"},
		{DisplayText: "internal/mention/mention_test.go"},
		{DisplayText: "internal/mention/mention_utils.go"},
	}
	prefix := FindLongestCommonPrefix(suggestions)
	if prefix != "internal/mention/mention" {
		t.Fatalf("expected prefix=internal/mention/mention, got %q", prefix)
	}
}

// TestFindLongestCommonPrefix_Empty 测试空列表
func TestFindLongestCommonPrefix_Empty(t *testing.T) {
	prefix := FindLongestCommonPrefix(nil)
	if prefix != "" {
		t.Fatalf("expected empty prefix, got %q", prefix)
	}
}

// TestFindLongestCommonPrefix_Single 测试单元素
func TestFindLongestCommonPrefix_Single(t *testing.T) {
	suggestions := []Completion{
		{DisplayText: "only_one.go"},
	}
	prefix := FindLongestCommonPrefix(suggestions)
	if prefix != "only_one.go" {
		t.Fatalf("expected only_one.go, got %q", prefix)
	}
}

// TestFindLongestCommonPrefix_NoCommon 测试无公共前缀
func TestFindLongestCommonPrefix_NoCommon(t *testing.T) {
	suggestions := []Completion{
		{DisplayText: "aaa.go"},
		{DisplayText: "bbb.go"},
	}
	prefix := FindLongestCommonPrefix(suggestions)
	if prefix != "" {
		t.Fatalf("expected empty prefix, got %q", prefix)
	}
}

// TestSuggest_EmptyToken 测试空 token 返回 showOnEmpty
func TestSuggest_EmptyToken(t *testing.T) {
	// 清空缓存避免干扰
	ClearCache()
	results := Suggest("")
	// showOnEmpty 应返回顶层目录或 nil（取决于当前目录是否有非隐藏文件）
	// 只要不 panic 即可
	_ = results
}

// TestSuggest_LongToken 测试超长 token 截断
func TestSuggest_LongToken(t *testing.T) {
	ClearCache()
	longToken := strings.Repeat("a", 2000)
	// 不应 panic
	_ = Suggest(longToken)
}

// TestExtractToken_CursorOffsetOutOfRange 测试 cursor 超出范围
func TestExtractToken_CursorOffsetOutOfRange(t *testing.T) {
	input := "@test"
	_, _, isAt := ExtractToken(input, 999)
	// 应截断到 len(input) 并正确检测
	if !isAt {
		t.Fatal("expected isAt=true even with out-of-range cursor")
	}
}

// TestExtractToken_NegativeCursor 测试负 cursor
func TestExtractToken_NegativeCursor(t *testing.T) {
	_, _, isAt := ExtractToken("@test", -1)
	if isAt {
		t.Fatal("expected isAt=false for negative cursor")
	}
}

// TestApplySuggestion_DirectoryWithSeparator 测试目录补全在 Windows 下的分隔符
func TestApplySuggestion_DirectoryWithSeparator(t *testing.T) {
	input := "@dir"
	suggestion := Completion{
		DisplayText: "mydir",
		IsDir:       true,
	}
	newInput, _ := ApplySuggestion(input, suggestion, 0, 3)
	sep := string(filepath.Separator)
	expected := "@" + "mydir" + sep
	if newInput != expected {
		t.Fatalf("expected %q, got %q", expected, newInput)
	}
}
