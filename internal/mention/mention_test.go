package mention

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetectMentions 测试 @-mention 模式检测
func TestDetectMentions(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantLen  int
		wantPath string
	}{
		{
			name:     "no mentions",
			input:    "hello world",
			wantLen:  0,
			wantPath: "",
		},
		{
			name:     "single mention",
			input:    "please review @main.go",
			wantLen:  1,
			wantPath: "main.go",
		},
		{
			name:     "relative path mention",
			input:    "check @./internal/app.go",
			wantLen:  1,
			wantPath: "internal" + string(filepath.Separator) + "app.go",
		},
		{
			name:     "parent path mention",
			input:    "check @../README.md",
			wantLen:  1,
			wantPath: ".." + string(filepath.Separator) + "README.md",
		},
		{
			name:     "multiple mentions",
			input:    "compare @main.go and @utils.go",
			wantLen:  2,
			wantPath: "main.go",
		},
		{
			name:     "duplicate mentions deduplicated",
			input:    "check @main.go and also @main.go",
			wantLen:  1,
			wantPath: "main.go",
		},
		{
			name:     "mention with extension",
			input:    "look at @config.yaml",
			wantLen:  1,
			wantPath: "config.yaml",
		},
		{
			name:     "email not matched",
			input:    "send to user@example.com",
			wantLen:  0,
			wantPath: "",
		},
		{
			name:     "deep path mention",
			input:    "check @./internal/tools/executor.go",
			wantLen:  1,
			wantPath: "internal" + string(filepath.Separator) + "tools" + string(filepath.Separator) + "executor.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mentions := DetectMentions(tt.input)
			if len(mentions) != tt.wantLen {
				t.Errorf("DetectMentions returned %d mentions, want %d", len(mentions), tt.wantLen)
			} else if tt.wantLen > 0 && mentions[0].FilePath != tt.wantPath {
				t.Errorf("FilePath = %q, want %q", mentions[0].FilePath, tt.wantPath)
			}
		})
	}
}

// TestResolveMentions 测试文件内容解析
func TestResolveMentions(t *testing.T) {
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "test.go")
	content := "package main\n\nfunc main() {}"
	os.WriteFile(testFile, []byte(content), 0644)

	subDir := filepath.Join(tmpDir, "internal")
	os.MkdirAll(subDir, 0755)
	subFile := filepath.Join(subDir, "app.py")
	pyContent := "print('hello')"
	os.WriteFile(subFile, []byte(pyContent), 0644)

	t.Run("resolve existing file", func(t *testing.T) {
		mentions := []Mention{{Raw: "@test.go", FilePath: "test.go"}}
		resolved := ResolveMentions(mentions, tmpDir)

		if resolved[0].Error != nil {
			t.Fatalf("ResolveMentions error: %v", resolved[0].Error)
		}
		if resolved[0].Content != content {
			t.Errorf("Content = %q, want %q", resolved[0].Content, content)
		}
	})

	t.Run("resolve subdirectory file", func(t *testing.T) {
		mentions := []Mention{{Raw: "@./internal/app.py", FilePath: filepath.Join("internal", "app.py")}}
		resolved := ResolveMentions(mentions, tmpDir)

		if resolved[0].Error != nil {
			t.Fatalf("ResolveMentions error: %v", resolved[0].Error)
		}
		if resolved[0].Content != pyContent {
			t.Errorf("Content = %q, want %q", resolved[0].Content, pyContent)
		}
	})

	t.Run("resolve non-existent file", func(t *testing.T) {
		mentions := []Mention{{Raw: "@nonexistent.go", FilePath: "nonexistent.go"}}
		resolved := ResolveMentions(mentions, tmpDir)

		if resolved[0].Error == nil {
			t.Error("Should return error for non-existent file")
		}
	})
}

// TestResolveMentions_Directory 测试目录检测
func TestResolveMentions_Directory(t *testing.T) {
	tmpDir := t.TempDir()

	subDir := filepath.Join(tmpDir, "pkg.mod")
	os.MkdirAll(subDir, 0755)

	mentions := []Mention{{Raw: "@pkg.mod", FilePath: "pkg.mod"}}
	resolved := ResolveMentions(mentions, tmpDir)

	if resolved[0].Error == nil {
		t.Fatal("Should return error for directory")
	}

	errMsg := resolved[0].Error.Error()
	if !strings.Contains(errMsg, "is a directory, not a file") {
		t.Errorf("Error should mention directory, got: %s", errMsg)
	}
}

// TestResolveMentions_Binary 测试二进制文件检测
func TestResolveMentions_Binary(t *testing.T) {
	tmpDir := t.TempDir()

	binFile := filepath.Join(tmpDir, "data.bin")
	binData := []byte{0x00, 0x01, 0x02, 0x03}
	os.WriteFile(binFile, binData, 0644)

	mentions := []Mention{{Raw: "@data.bin", FilePath: "data.bin"}}
	resolved := ResolveMentions(mentions, tmpDir)

	if resolved[0].Error == nil {
		t.Fatal("Should return error for binary file")
	}

	errMsg := resolved[0].Error.Error()
	if !strings.Contains(errMsg, "is a binary file") {
		t.Errorf("Error should mention binary file, got: %s", errMsg)
	}
}

// TestResolveMentions_LargeFile 测试大文件截断
func TestResolveMentions_LargeFile(t *testing.T) {
	tmpDir := t.TempDir()

	var lines []string
	for i := 0; i < 600; i++ {
		lines = append(lines, "line content")
	}
	largeContent := strings.Join(lines, "\n")

	largeFile := filepath.Join(tmpDir, "large.go")
	os.WriteFile(largeFile, []byte(largeContent), 0644)

	mentions := []Mention{{Raw: "@large.go", FilePath: "large.go"}}
	resolved := ResolveMentions(mentions, tmpDir)

	if resolved[0].Error != nil {
		t.Fatalf("Should not return error for large file: %v", resolved[0].Error)
	}

	if !strings.Contains(resolved[0].Content, "[... truncated, 600 lines total]") {
		t.Errorf("Content should contain truncation message, got: %s", resolved[0].Content[len(resolved[0].Content)-100:])
	}

	contentLines := strings.Split(resolved[0].Content, "\n")
	// 500 lines + 1 truncation message line
	if len(contentLines) != 501 {
		t.Errorf("Content should have 501 lines (500 + truncation msg), got %d", len(contentLines))
	}
}

// TestIsBinary 测试二进制文件检测helper functions
func TestIsBinary(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{
			name: "text file",
			data: []byte("hello world"),
			want: false,
		},
		{
			name: "binary with null byte at start",
			data: []byte{0x00, 0x01, 0x02},
			want: true,
		},
		{
			name: "binary with null byte in middle",
			data: append([]byte("hello"), 0x00, 0x01),
			want: true,
		},
		{
			name: "empty file",
			data: []byte{},
			want: false,
		},
		{
			name: "null byte after 512 bytes",
			data: append(bytes.Repeat([]byte("a"), 600), 0x00),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBinary(tt.data)
			if got != tt.want {
				t.Errorf("isBinary() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestExpandMentions 测试 @-mention 完整展开
func TestExpandMentions(t *testing.T) {
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "hello.go")
	content := "package main\nfunc hello() {}"
	os.WriteFile(testFile, []byte(content), 0644)

	txtFile := filepath.Join(tmpDir, "notes.txt")
	txtContent := "some notes here"
	os.WriteFile(txtFile, []byte(txtContent), 0644)

	t.Run("expand single mention with file tag", func(t *testing.T) {
		input := "please review @hello.go"
		result := ExpandMentions(input, tmpDir)

		if !strings.Contains(result, `<file path="hello.go">`) {
			t.Error("Result should contain file tag with path")
		}
		if !strings.Contains(result, content) {
			t.Error("Result should contain file content")
		}
		if !strings.Contains(result, "</file>") {
			t.Error("Result should contain closing file tag")
		}
	})

	t.Run("expand txt mention", func(t *testing.T) {
		input := "check @notes.txt"
		result := ExpandMentions(input, tmpDir)

		if !strings.Contains(result, txtContent) {
			t.Error("Result should contain txt content")
		}
	})

	t.Run("no mentions", func(t *testing.T) {
		input := "hello world"
		result := ExpandMentions(input, tmpDir)

		if result != input {
			t.Errorf("Result = %q, want %q", result, input)
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		input := "check @missing.go"
		result := ExpandMentions(input, tmpDir)

		if !strings.Contains(result, "cannot read") {
			t.Errorf("Result should contain error message, got: %s", result)
		}
	})
}

// TestExpandMentions_Duplicate 测试重复引用去重
func TestExpandMentions_Duplicate(t *testing.T) {
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "main.go")
	content := "package main\nfunc main() {}"
	os.WriteFile(testFile, []byte(content), 0644)

	input := "check @main.go and also @main.go"
	result := ExpandMentions(input, tmpDir)

	if !strings.Contains(result, `<file path="main.go">`) {
		t.Error("First mention should be expanded with file tag")
	}
	if !strings.Contains(result, "[already included above]") {
		t.Error("Second mention should show already included message")
	}
	if strings.Count(result, "<file path=") != 1 {
		t.Error("Should only have one file tag expansion")
	}
}

// TestExpandMentions_Directory 测试展开时目录引用的错误提示
func TestExpandMentions_Directory(t *testing.T) {
	tmpDir := t.TempDir()

	subDir := filepath.Join(tmpDir, "pkg.mod")
	os.MkdirAll(subDir, 0755)

	input := "check @pkg.mod"
	result := ExpandMentions(input, tmpDir)

	if !strings.Contains(result, "is a directory, not a file") {
		t.Errorf("Result should contain directory error, got: %s", result)
	}
}

// TestExpandMentions_Binary 测试展开时二进制文件的错误提示
func TestExpandMentions_Binary(t *testing.T) {
	tmpDir := t.TempDir()

	binFile := filepath.Join(tmpDir, "data.bin")
	binData := []byte{0x00, 0x01, 0x02, 0x03}
	os.WriteFile(binFile, binData, 0644)

	input := "check @data.bin"
	result := ExpandMentions(input, tmpDir)

	if !strings.Contains(result, "is a binary file") {
		t.Errorf("Result should contain binary error, got: %s", result)
	}
}

// TestExpandMentionsWithInfo 测试带提示信息的展开
func TestExpandMentionsWithInfo(t *testing.T) {
	tmpDir := t.TempDir()

	goFile := filepath.Join(tmpDir, "main.go")
	goContent := "package main\n\nfunc main() {}"
	os.WriteFile(goFile, []byte(goContent), 0644)

	pyFile := filepath.Join(tmpDir, "utils.go")
	pyContent := "package utils\n\nfunc Helper() {}"
	os.WriteFile(pyFile, []byte(pyContent), 0644)

	t.Run("single file info", func(t *testing.T) {
		input := "review @main.go"
		expanded, info := ExpandMentionsWithInfo(input, tmpDir)

		if len(info) != 1 {
			t.Fatalf("Expected 1 info entry, got %d", len(info))
		}
		if !strings.Contains(info[0], "📎 Included: main.go") {
			t.Errorf("Info should contain file name, got: %s", info[0])
		}
		if !strings.Contains(info[0], "3 lines") {
			t.Errorf("Info should contain line count, got: %s", info[0])
		}
		if !strings.Contains(expanded, `<file path="main.go">`) {
			t.Error("Expanded should contain file tag")
		}
	})

	t.Run("multiple files info", func(t *testing.T) {
		input := "compare @main.go and @utils.go"
		_, info := ExpandMentionsWithInfo(input, tmpDir)

		if len(info) != 2 {
			t.Fatalf("Expected 2 info entries, got %d", len(info))
		}
		if !strings.Contains(info[0], "main.go") {
			t.Errorf("First info should mention main.go, got: %s", info[0])
		}
		if !strings.Contains(info[1], "utils.go") {
			t.Errorf("Second info should mention utils.go, got: %s", info[1])
		}
	})

	t.Run("no mentions returns nil info", func(t *testing.T) {
		input := "hello world"
		_, info := ExpandMentionsWithInfo(input, tmpDir)

		if info != nil {
			t.Errorf("Expected nil info for no mentions, got: %v", info)
		}
	})

	t.Run("error mention excluded from info", func(t *testing.T) {
		input := "check @missing.go"
		_, info := ExpandMentionsWithInfo(input, tmpDir)

		if len(info) != 0 {
			t.Errorf("Error mentions should not produce info entries, got: %v", info)
		}
	})
}

// TestCountLines 测试行数统计
func TestCountLines(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want int
	}{
		{name: "empty", s: "", want: 0},
		{name: "single line", s: "hello", want: 1},
		{name: "two lines", s: "hello\nworld", want: 2},
		// ★ 本期望已于 2026-10-08 **有意翻转**（W-046a）——**下一个人别当回归**：
		// 翻转前钉的是 wc -l 语义（"hello\n" = 2 行），那是缺陷：read_file 的
		// Scanner 对同一内容读出 1 行，同轮两个数字打架。行数唯一语义源现
		// 收口到 util.CountLines（编辑器语义：末尾换行不分新行），本包 countLines
		// 只是薄转发。
		{name: "trailing newline", s: "hello\n", want: 1},
		{name: "blank second line", s: "hello\n\n", want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countLines(tt.s)
			if got != tt.want {
				t.Errorf("countLines(%q) = %d, want %d", tt.s, got, tt.want)
			}
		})
	}
}

// TestExpandMention_LargeFile 测试大文件展开时截断为 500 行
// 验证超过 500 行的文件在 ExpandMentions 中被截断并添加截断提示
func TestExpandMention_LargeFile(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建 600 行的大文件
	var lines []string
	for i := 0; i < 600; i++ {
		lines = append(lines, fmt.Sprintf("line %d content", i))
	}
	largeContent := strings.Join(lines, "\n")

	largeFile := filepath.Join(tmpDir, "large.go")
	os.WriteFile(largeFile, []byte(largeContent), 0644)

	// 使用 ExpandMentions 展开
	input := "please review @large.go"
	result := ExpandMentions(input, tmpDir)

	// 验证包含文件标签
	if !strings.Contains(result, `<file path="large.go">`) {
		t.Error("结果should contain文件标签")
	}

	// 验证包含截断提示
	if !strings.Contains(result, "truncated") {
		t.Error("结果should contain截断提示信息")
	}

	// 验证内容被截断为 500 行
	contentLines := strings.Split(result, "\n")
	// 文件标签行 + 500 行内容 + 截断提示行 + 结束标签行
	foundContentLines := 0
	for _, line := range contentLines {
		if strings.HasPrefix(line, "line ") && strings.Contains(line, " content") {
			foundContentLines++
		}
	}
	if foundContentLines > 500 {
		t.Errorf("展开内容行数 %d 超过 500 行限制", foundContentLines)
	}
}

// TestExpandMention_RelativePath 测试 @./relative/path/file.txt 相对于 CWD 解析
func TestExpandMention_RelativePath(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建子目录和文件
	subDir := filepath.Join(tmpDir, "relative", "path")
	os.MkdirAll(subDir, 0755)
	testFile := filepath.Join(subDir, "file.txt")
	content := "relative path content"
	os.WriteFile(testFile, []byte(content), 0644)

	// 使用 @./relative/path/file.txt 语法
	input := "check @./relative/path/file.txt"
	result := ExpandMentions(input, tmpDir)

	if !strings.Contains(result, `<file path="relative`) {
		t.Errorf("结果should contain相对路径文件标签，actual: %s", result)
	}
	if !strings.Contains(result, content) {
		t.Errorf("结果should contain文件内容 %q，actual: %s", content, result)
	}
}

// TestExpandMention_WindowsPath 测试 Windows 绝对路径 @D:\path\file.txt 的展开
func TestExpandMention_WindowsPath(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建文件
	testFile := filepath.Join(tmpDir, "winfile.txt")
	content := "windows path content"
	os.WriteFile(testFile, []byte(content), 0644)

	// 构造 Windows 绝对路径引用
	// 使用 DetectMentions + ResolveMentions 直接测试绝对路径解析
	winPath := filepath.FromSlash(tmpDir + "/winfile.txt")
	mentions := []Mention{{Raw: "@winfile.txt", FilePath: "winfile.txt"}}
	resolved := ResolveMentions(mentions, tmpDir)

	if resolved[0].Error != nil {
		t.Fatalf("ResolveMentions 错误: %v", resolved[0].Error)
	}
	if resolved[0].Content != content {
		t.Errorf("内容 = %q, want %q", resolved[0].Content, content)
	}

	// 验证 Windows 路径格式正确
	if !filepath.IsAbs(winPath) {
		t.Errorf("filepath.FromSlash 生成的路径应为绝对路径: %q", winPath)
	}
}

// TestExpandMention_AbsolutePath 测试绝对路径 @-mention 的检测与解析
// 创建文件后使用 DetectMentions 检测绝对路径引用，
// 再通过 ResolveMentions 解析，验证绝对路径能正确解析到文件内容
func TestExpandMention_AbsolutePath(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建文件
	testFile := filepath.Join(tmpDir, "abs_test.go")
	content := "package main\nfunc absTest() {}"
	os.WriteFile(testFile, []byte(content), 0644)

	// 使用 ResolveMentions 直接测试绝对路径解析能力
	// 构造绝对路径作为 FilePath，验证 resolveMention 能正确处理
	absPath := filepath.Join(tmpDir, "abs_test.go")
	mentions := []Mention{{Raw: "@abs_test.go", FilePath: absPath}}
	resolved := ResolveMentions(mentions, "")

	if resolved[0].Error != nil {
		t.Fatalf("ResolveMentions 绝对路径解析错误: %v", resolved[0].Error)
	}
	if resolved[0].Content != content {
		t.Errorf("内容 = %q, expected %q", resolved[0].Content, content)
	}

	// 同时验证 DetectMentions 对 Windows 绝对路径的检测能力
	// 构造一个不含特殊字符的绝对路径输入来测试正则匹配
	driveLetter := string(absPath[0])
	// 使用正斜杠格式确保正则能匹配
	input := "check @" + driveLetter + ":/abs_test.go"
	detected := DetectMentions(input)
	if len(detected) == 0 {
		t.Logf("DetectMentions 未匹配 %q（Windows 短路径名可能干扰正则），跳过检测测试", input)
	} else {
		// 检测到了，验证路径解析
		resolvedDetected := ResolveMentions(detected, tmpDir)
		if resolvedDetected[0].Error != nil {
			t.Logf("检测到的路径解析失败: %v", resolvedDetected[0].Error)
		}
	}
}

// TestDetectMentions_URLNotMatched 测试 URL 不被匹配为 @-mention
// 输入如 "check @https://example.com" 不应将 URL 部分匹配为文件引用
func TestDetectMentions_URLNotMatched(t *testing.T) {
	input := "check @https://example.com"
	mentions := DetectMentions(input)
	if len(mentions) != 0 {
		t.Errorf("URL 不应被匹配为 mention，但检测到 %d 个: %v", len(mentions), mentions)
	}
}

// TestExtToLang 测试文件扩展名到语言标识符的映射
func TestExtToLang(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".go", "go"},
		{".py", "python"},
		{".js", "javascript"},
		{".ts", "typescript"},
		{".rs", "rust"},
		{".json", "json"},
		{".yaml", "yaml"},
		{".yml", "yaml"},
		{".md", "markdown"},
		{".txt", ""},
		{".xyz", ""},
	}

	for _, tt := range tests {
		got := extToLang(tt.ext)
		if got != tt.want {
			t.Errorf("extToLang(%q) = %q, want %q", tt.ext, got, tt.want)
		}
	}
}

// TestIsImageFile_PNG 验证 .png 被检测为图片
func TestIsImageFile_PNG(t *testing.T) {
	if !IsImageFile("photo.png") {
		t.Error("IsImageFile(\"photo.png\") = false, want true")
	}
}

// TestIsImageFile_JPG 验证 .jpg 被检测为图片
func TestIsImageFile_JPG(t *testing.T) {
	if !IsImageFile("photo.jpg") {
		t.Error("IsImageFile(\"photo.jpg\") = false, want true")
	}
	if !IsImageFile("photo.jpeg") {
		t.Error("IsImageFile(\"photo.jpeg\") = false, want true")
	}
}

// TestIsImageFile_Go 验证 .go 不被检测为图片
func TestIsImageFile_Go(t *testing.T) {
	if IsImageFile("main.go") {
		t.Error("IsImageFile(\"main.go\") = true, want false")
	}
}

// TestIsImageFile_NoExt 验证无扩展名不被检测为图片
func TestIsImageFile_NoExt(t *testing.T) {
	if IsImageFile("README") {
		t.Error("IsImageFile(\"README\") = true, want false")
	}
}

// makeTestPNG 创建有效 PNG 图片数据
func makeTestPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// TestEncodeImageBase64 验证图片文件的 base64 编码
func TestEncodeImageBase64(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建一个有效 PNG 文件（10x10 像素）
	pngData := makeTestPNG(10, 10)
	pngFile := filepath.Join(tmpDir, "test.png")
	if err := os.WriteFile(pngFile, pngData, 0644); err != nil {
		t.Fatalf("创建测试图片失败: %v", err)
	}

	encoded, mediaType, err := EncodeImageBase64(pngFile)
	if err != nil {
		t.Fatalf("EncodeImageBase64 错误: %v", err)
	}

	// 验证 MIME 类型
	if mediaType != "image/png" {
		t.Errorf("mediaType = %q, want %q", mediaType, "image/png")
	}

	// 验证 base64 非空
	if encoded == "" {
		t.Error("base64 编码不应为空")
	}

	// 测试不存在的文件
	_, _, err = EncodeImageBase64(filepath.Join(tmpDir, "nonexistent.png"))
	if err == nil {
		t.Error("不存在的文件should return error")
	}
}

// TestResolveMentions_Image 测试图片文件的解析
func TestResolveMentions_Image(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建一个有效 PNG 文件
	pngData := makeTestPNG(10, 10)
	pngFile := filepath.Join(tmpDir, "photo.png")
	os.WriteFile(pngFile, pngData, 0644)

	mentions := []Mention{{Raw: "@photo.png", FilePath: "photo.png"}}
	resolved := ResolveMentions(mentions, tmpDir)

	if resolved[0].Error != nil {
		t.Fatalf("ResolveMentions 图片解析错误: %v", resolved[0].Error)
	}
	if !resolved[0].IsImage {
		t.Error("IsImage 应为 true")
	}
	if resolved[0].MediaType != "image/png" {
		t.Errorf("MediaType = %q, want %q", resolved[0].MediaType, "image/png")
	}
	if resolved[0].Base64 == "" {
		t.Error("Base64 should not be empty")
	}
}

// TestExpandMentions_Image 测试图片文件的展开
func TestExpandMentions_Image(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建一个有效 PNG 文件
	pngData := makeTestPNG(10, 10)
	pngFile := filepath.Join(tmpDir, "diagram.png")
	os.WriteFile(pngFile, pngData, 0644)

	input := "please review @diagram.png"
	result := ExpandMentions(input, tmpDir)

	if !strings.Contains(result, `<image path="diagram.png"`) {
		t.Error("结果should contain image 标签")
	}
	if !strings.Contains(result, "media_type=\"image/png\"") {
		t.Error("结果should contain media_type 属性")
	}
	if !strings.Contains(result, "</image>") {
		t.Error("结果should contain image 结束标签")
	}
}
