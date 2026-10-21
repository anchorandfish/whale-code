// Package export 提供会话导出功能，支持多格式导出、脱敏和剪贴板复制。
package export

import (
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/util"
)

// Format 导出格式
type Format string

const (
	FmtMarkdown Format = "markdown"
	FmtJSON     Format = "json"
	FmtHTML     Format = "html"
)

// ExportOption 导出选项
type ExportOption struct {
	Format     Format
	UserOnly   bool
	OutputPath string
	FullMeta   bool
}

// ExportResult 导出结果
type ExportResult struct {
	Path         string
	Format       Format
	MessageCount int
	Redacted     int
}

// SessionReader 会话读取接口（避免直接依赖 session 包）
type SessionReader interface {
	GetMessages() []session.Message
}

// Redactor 脱敏接口
type Redactor interface {
	Scrub(text string) (string, int)
}

// Exporter 会话导出器
type Exporter struct {
	session  SessionReader
	redactor Redactor
	homeDir  string
}

// NewExporter 创建导出器
//
// 参数：
//   - s: 会话读取器，用于获取消息列表
//   - r: 脱敏器，用于敏感信息替换
//   - homeDir: 用户主目录，用于构建默认导出路径
func NewExporter(s SessionReader, r Redactor, homeDir string) *Exporter {
	return &Exporter{
		session:  s,
		redactor: r,
		homeDir:  homeDir,
	}
}

// Export 执行导出
//
// 行为规约：
//  1. 过滤 Tombstoned 消息
//  2. UserOnly 模式仅保留用户消息
//  3. 脱敏在格式化前执行
//  4. OutputPath 为空时使用默认路径 ~/.whale/exports/{timestamp}-{sanitized}.{ext}
//  5. 使用 AtomicWriteFile 原子写入，权限 0o600
//
// 参数：
//   - opt: 导出选项
//
// 返回：
//   - *ExportResult: 导出结果（路径、格式、消息数、脱敏数）
//   - error: 导出过程中的错误
func (e *Exporter) Export(opt ExportOption) (*ExportResult, error) {
	msgs := e.filterMessages(e.session.GetMessages(), opt.UserOnly)

	var content string
	var redacted int

	switch opt.Format {
	case FmtMarkdown:
		content, redacted = FormatMarkdown(msgs, e.redactor)
	case FmtJSON:
		content, redacted = FormatJSON(msgs, opt.FullMeta, e.redactor)
	case FmtHTML:
		content, redacted = FormatHTML(msgs, e.redactor)
	default:
		return nil, fmt.Errorf("不支持的导出格式：%s", opt.Format)
	}

	outPath := opt.OutputPath
	if outPath == "" {
		outPath = e.defaultOutputPath(opt.Format)
	}

	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建导出目录失败: %w", err)
	}

	if err := util.AtomicWriteFile(outPath, []byte(content), 0o600); err != nil {
		return nil, fmt.Errorf("写入导出文件失败: %w", err)
	}

	return &ExportResult{
		Path:         outPath,
		Format:       opt.Format,
		MessageCount: len(msgs),
		Redacted:     redacted,
	}, nil
}

// filterMessages 过滤消息列表
//
// 过滤规则：
//   - 跳过 Tombstoned == true 的消息
//   - userOnly 为 true 时仅保留 RoleUser 消息
func (e *Exporter) filterMessages(msgs []session.Message, userOnly bool) []session.Message {
	var filtered []session.Message
	for _, msg := range msgs {
		if msg.Tombstoned {
			continue
		}
		if userOnly && msg.Role != session.RoleUser {
			continue
		}
		filtered = append(filtered, msg)
	}
	return filtered
}

// defaultOutputPath 生成默认导出文件路径
//
// 路径格式：~/.whale/exports/{timestamp}-{sanitized_session_name}.{ext}
// filename sanitize：仅允许 [a-zA-Z0-9_-]
func (e *Exporter) defaultOutputPath(format Format) string {
	ext := "md"
	switch format {
	case FmtJSON:
		ext = "json"
	case FmtHTML:
		ext = "html"
	}

	timestamp := time.Now().Format("20060102-150405")
	sessionName := e.getSessionName()
	sanitized := sanitizeFilename(sessionName)
	if sanitized == "" {
		sanitized = "session"
	}

	filename := fmt.Sprintf("%s-%s.%s", timestamp, sanitized, ext)
	return filepath.Join(whalehome.Dir(), "exports", filename)
}

// getSessionName 获取会话名称
//
// 尝试从 SessionReader 中获取会话名，失败时返回空字符串。
func (e *Exporter) getSessionName() string {
	type namedSession interface {
		GetName() string
	}
	if ns, ok := e.session.(namedSession); ok {
		return ns.GetName()
	}
	return ""
}

// sanitizeFilename 清理文件名，仅保留安全字符
//
// 仅允许 [a-zA-Z0-9_-]，参考同类实现
// 连续非安全字符替换为单个下划线
func sanitizeFilename(name string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	sanitized := re.ReplaceAllString(name, "_")
	sanitized = strings.Trim(sanitized, "_")
	if len(sanitized) > 50 {
		sanitized = sanitized[:50]
	}
	return sanitized
}
