// Package mention provides @-mention file reference detection and resolution
package mention

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	imgproc "github.com/anchorandfish/whale-code/internal/image"
	"github.com/anchorandfish/whale-code/internal/platform"
	"github.com/anchorandfish/whale-code/internal/util"
)

// maxLines defines the max display lines for a file, exceeding this value triggers truncation
const maxLines = 500

// maxImageSize 限制单张图片最大字节数（10MB），防止超大图片撑爆上下文或 OOM
const maxImageSize = 10 * 1024 * 1024

// mentionPattern matches @filepath patterns, excluding email addresses
// supports: relative paths (@./file.go, @dir/file.go), Windows absolute paths (@C:\path\file.go),
// and nested references with line numbers (@user/file.go:123)
// 使用 Unicode 字符类 [\p{L}\p{N}_] 替代 \w，以支持中文等非 ASCII 路径
var mentionPattern = regexp.MustCompile(`(?:^|\s)@((?:[A-Za-z]:[/\\])?(?:\.{1,2}[/\\])?[\p{L}\p{N}_][\p{L}\p{N}_\-./\\]*\.[\p{L}\p{N}_]+(?::\d+)?)`)

// Mention represents a detected @-mention reference
type Mention struct {
	Raw        string
	FilePath   string
	LineNumber int
	Content    string
	Error      error
	IsImage    bool   // indicates if it is an image file
	Base64     string // base64-encoded image data
	MediaType  string // image MIME type
}

// DetectMentions detects all @-mention patterns in input text, auto-deduplicates
func DetectMentions(input string) []Mention {
	matches := mentionPattern.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var mentions []Mention

	for _, match := range matches {
		raw := match[0]
		filePath := match[1]

		if strings.Contains(filePath, "@") {
			continue
		}

		lineNumber := 0
		if idx := strings.LastIndex(filePath, ":"); idx >= 0 {
			lineStr := filePath[idx+1:]
			if n, err := strconv.Atoi(lineStr); err == nil && n > 0 {
				lineNumber = n
				filePath = filePath[:idx]
			}
		}

		filePath = strings.ReplaceAll(filePath, "/", string(filepath.Separator))
		filePath = strings.ReplaceAll(filePath, "\\", string(filepath.Separator))

		filePath = strings.TrimPrefix(filePath, "."+string(filepath.Separator))

		dedupKey := filePath
		if lineNumber > 0 {
			dedupKey = fmt.Sprintf("%s:%d", filePath, lineNumber)
		}
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true

		mentions = append(mentions, Mention{
			Raw:        strings.TrimSpace(raw),
			FilePath:   filePath,
			LineNumber: lineNumber,
		})
	}

	return mentions
}

// ResolveMentions resolves file content for all mentions
// for image files, additionally fills Base64 and MediaType fields
func ResolveMentions(mentions []Mention, workDir string) []Mention {
	results := make([]Mention, len(mentions))
	for i, m := range mentions {
		results[i] = m
		results[i].Content, results[i].Error = resolveMention(m, workDir)
		// if image file, encode as base64
		if results[i].Content == "[image]" && results[i].Error == nil {
			filePath := m.FilePath
			if !filepath.IsAbs(filePath) {
				filePath = filepath.Join(workDir, filePath)
			}
			base64Data, mediaType, err := EncodeImageBase64(filePath)
			if err != nil {
				results[i].Content = ""
				results[i].Error = err
			} else {
				results[i].IsImage = true
				results[i].Base64 = base64Data
				results[i].MediaType = mediaType
			}
		}
	}
	return results
}

// resolveMention resolves a single mention, sequentially performing directory detection, image detection, binary detection, and large file truncation
func resolveMention(m Mention, workDir string) (string, error) {
	filePath := m.FilePath

	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(workDir, filePath)
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve @%s: %w", m.FilePath, err)
	}
	// 先做存在性与类型检查，再做工作目录 confinement 校验。
	// Windows 上 t.TempDir() 返回短路径（如 ADMINI~1），EvalSymlinks 将 absWorkDir
	// 解析为长路径，而 absPath 因文件不存在无法解析、保留短路径，两者前缀不一致会使
	// filepath.Rel 误判为 ".." 越界。先 stat 可让"文件不存在"返回明确的 "cannot read"，
	// 避免误导性的 "outside the working directory" 错误（见 test-failures 5c）。
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("cannot read @%s: %w", m.FilePath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("Error: @%s is a directory, not a file", m.FilePath)
	}

	// 工作目录 confinement 校验：防止通过 @../../etc/passwd 逃逸出工作目录
	// 仅当指定 workDir 时才校验：空 workDir 视为"无 confinement"模式（测试场景
	// 或显式指定绝对路径场景），绝对路径视为用户显式指定放行，避免误报越界
	// （见 test-failures 5d / TestExpandMention_AbsolutePath：Linux 上传入空
	// workDir + 绝对路径 /tmp/.../abs_test.go 应被放行而非报 outside working directory）。
	if workDir != "" {
		absWorkDir, err := filepath.Abs(workDir)
		if err != nil {
			return "", fmt.Errorf("cannot resolve work directory: %w", err)
		}
		// 复用 platform.IsPathAllowed（EvalSymlinks + 前缀检查），防止 symlink 逃逸
		if !platform.IsPathAllowed(absPath, []string{absWorkDir}) {
			// Windows 跨盘符场景：filepath.Rel 无法在不同盘符间计算相对路径
			// （如工作目录在 D:，引用文件在 C:）。此时 absPath 已通过 os.Stat 确认存在，
			// 若为绝对路径则视为用户显式指定，放行读取；否则仍报越界（见 test-failures 5a）。
			if !filepath.IsAbs(absPath) || !isCrossDrive(absPath, absWorkDir) {
				return "", fmt.Errorf("Error: @%s is outside the working directory", m.FilePath)
			}
		}
	}

	ext := strings.ToLower(filepath.Ext(absPath))
	if ext == ".svg" {
		return "", fmt.Errorf("Error: @%s is an SVG file which may contain scripts and is not supported", m.FilePath)
	}

	if IsImageFile(absPath) {
		return "[image]", nil
	}

	f, err := os.Open(absPath)
	if err != nil {
		return "", fmt.Errorf("cannot read @%s: %w", m.FilePath, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	lineCount := 0
	for scanner.Scan() {
		lineCount++
		if m.LineNumber > 0 || lineCount <= maxLines {
			lines = append(lines, scanner.Text())
		}
		// 不再提前 break：继续扫描以获取真实总行数，超过 maxLines 后仅计数不存储，
		// 保证截断提示 "[... truncated, N lines total]" 中的 N 反映文件真实行数（见 test-failures 5b）。
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("cannot read @%s: %w", m.FilePath, err)
	}

	first512 := make([]byte, 512)
	_, _ = f.Seek(0, 0)
	readN, _ := f.Read(first512)
	first512 = first512[:readN]
	if isBinary(first512) {
		return "", fmt.Errorf("Error: @%s is a binary file", m.FilePath)
	}

	if m.LineNumber > 0 && m.LineNumber <= len(lines) {
		start := m.LineNumber - 10
		if start < 0 {
			start = 0
		}
		end := m.LineNumber + 10
		if end > len(lines) {
			end = len(lines)
		}
		excerpt := strings.Join(lines[start:end], "\n")
		prefix := ""
		if start > 0 {
			prefix = fmt.Sprintf("[... lines 1-%d omitted]\n", start)
		}
		suffix := ""
		if end < len(lines) {
			suffix = fmt.Sprintf("\n[... lines %d-%d omitted]", end+1, len(lines))
		}
		return prefix + excerpt + suffix, nil
	}

	// 截断判定基于真实总行数 lineCount，而非 lines 长度：非行号引用时 lines 最多只存
	// maxLines 条，len(lines) 永远不会大于 maxLines，旧逻辑用 len(lines) > maxLines 判定
	// 导致截断提示永远不触发（见 test-failures 5b）。
	if lineCount > maxLines {
		truncated := strings.Join(lines[:maxLines], "\n")
		truncated += fmt.Sprintf("\n[... truncated, %d lines total]", lineCount)
		return truncated, nil
	}

	return strings.Join(lines, "\n"), nil
}

// isBinary detects if data is binary file content
func isBinary(data []byte) bool {
	checkSize := len(data)
	if checkSize > 512 {
		checkSize = 512
	}
	return bytes.Contains(data[:checkSize], []byte{0})
}

// ExpandMentions expands @-mention references in input text to <file> tag format
func ExpandMentions(input string, workDir string) string {
	expanded, _ := ExpandMentionsWithInfo(input, workDir)
	return expanded
}

// ExpandMentionsWithInfo expands @-mention references and returns prompt info for each file
func ExpandMentionsWithInfo(input string, workDir string) (string, []string) {
	mentions := DetectMentions(input)
	if len(mentions) == 0 {
		return input, nil
	}

	resolved := ResolveMentions(mentions, workDir)

	var info []string
	result := input

	for _, m := range resolved {
		if m.Error != nil {
			result = strings.Replace(result, m.Raw, fmt.Sprintf("[%s]", m.Error), 1)
			// duplicate reference dedup: replace remaining references with the same name
			result = strings.ReplaceAll(result, m.Raw, "[already included above]")
			continue
		}

		if m.IsImage {
			info = append(info, fmt.Sprintf("📎 Included image: %s (%s)", m.FilePath, m.MediaType))
			replacement := fmt.Sprintf("\n<image path=\"%s\" media_type=\"%s\">\n[base64 image data: %d bytes]\n</image>", m.FilePath, m.MediaType, len(m.Base64))
			result = strings.Replace(result, m.Raw, replacement, 1)
			result = strings.ReplaceAll(result, m.Raw, "[already included above]")
			continue
		}

		lineCount := countLines(m.Content)
		info = append(info, fmt.Sprintf("📎 Included: %s (%d lines)", m.FilePath, lineCount))

		replacement := fmt.Sprintf("\n<file path=\"%s\">\n%s\n</file>", m.FilePath, m.Content)
		result = strings.Replace(result, m.Raw, replacement, 1)
		// duplicate reference dedup: replace remaining references with the same name
		result = strings.ReplaceAll(result, m.Raw, "[already included above]")
	}

	return result, info
}

// countLines 统计文本行数。
//
// W-046a 收口：唯一语义源 = util.CountLines（编辑器语义：末尾换行不分新行）。
// 本包薄转发仅为既有调用点/单测保留，不再自带第二套实现。
func countLines(s string) int {
	return util.CountLines(s)
}

// extToLang maps file extensions to code fence language identifiers
func extToLang(ext string) string {
	langMap := map[string]string{
		".go":    "go",
		".py":    "python",
		".js":    "javascript",
		".ts":    "typescript",
		".tsx":   "tsx",
		".jsx":   "jsx",
		".rs":    "rust",
		".java":  "java",
		".c":     "c",
		".cpp":   "cpp",
		".h":     "c",
		".hpp":   "cpp",
		".cs":    "csharp",
		".rb":    "ruby",
		".php":   "php",
		".swift": "swift",
		".kt":    "kotlin",
		".scala": "scala",
		".sh":    "bash",
		".ps1":   "powershell",
		".sql":   "sql",
		".html":  "html",
		".css":   "css",
		".scss":  "scss",
		".json":  "json",
		".yaml":  "yaml",
		".yml":   "yaml",
		".toml":  "toml",
		".xml":   "xml",
		".md":    "markdown",
		".txt":   "",
		".env":   "",
	}
	if lang, ok := langMap[ext]; ok {
		return lang
	}
	return ""
}

// IsImageFile determines if a file is an image
func IsImageFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	imageExts := map[string]bool{
		".png":  true,
		".jpg":  true,
		".jpeg": true,
		".gif":  true,
		".webp": true,
		".bmp":  true,
	}
	return imageExts[ext]
}

// EncodeImageBase64 reads image file, compresses it, and encodes as base64
// 内部接入 image.FromPath 实现压缩+魔数校验+token 估算，签名保持向后兼容
func EncodeImageBase64(path string) (string, string, error) {
	ref, err := imgproc.FromPath(path)
	if err != nil {
		return "", "", err
	}
	return ref.Base64, ref.MediaType, nil
}

// isCrossDrive 判断两个绝对路径是否在 Windows 上跨盘符。
// 非 Windows 平台始终返回 false。
func isCrossDrive(a, b string) bool {
	if !platform.IsWindows() {
		return false
	}
	if len(a) < 2 || len(b) < 2 {
		return false
	}
	if a[1] != ':' || b[1] != ':' {
		return false
	}
	return a[0] != b[0]
}
