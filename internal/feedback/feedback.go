// Package feedback provides user feedback collection, storage and management functionality
package feedback

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/secret"
	"github.com/anchorandfish/whale-code/internal/util"
)

// Entry represents a user feedback record
type Entry struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Model     string `json:"model,omitempty"`
	Message   string `json:"message"`
}

// redactSensitive 对输入字符串做敏感信息脱敏。
// 委托 internal/secret.RedactSensitive（全仓唯一脱敏 SSOT），不再自持正则副本。
// 命中后替换为 [REDACTED]；未命中返回原字符串。
// 收口背景：原副本（8 条正则）缺 gcp_api_key / pem_private_key 两类 ⇒ 明文外发，
// 且与 secret 包漂移（，方案 A 已拍板）。详见 新发现_分诊规则与登记表 §7 附录 A。
func redactSensitive(s string) string {
	return secret.RedactSensitive(s)
}

// NewEntry creates a new feedback record
// 6-A 改动：对 message 做敏感信息脱敏，落盘的 Entry.Message 已是脱敏后内容
func NewEntry(message, version, model string) *Entry {
	now := time.Now()
	entry := &Entry{
		ID:        generateID(now),
		Timestamp: now.Format(time.RFC3339),
		Version:   version,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Model:     model,
		Message:   redactSensitive(message), // 6-A 新增：脱敏
	}
	return entry
}

// Save saves feedback to the specified directory
// Auto-creates directory if it does not exist
// 6-A 改动：保存后调 enforceFileLimit 清理超限旧文件
func (e *Entry) Save(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create feedback directory: %w", err)
	}

	filePath := filepath.Join(dir, e.ID+".json")
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize feedback: %w", err)
	}

	if err := util.AtomicWriteFile(filePath, data, 0o600); err != nil {
		return fmt.Errorf("failed to write feedback file: %w", err)
	}

	// 6-A 新增：清理超限旧文件（失败不阻塞，仅记录到 stderr）
	if err := enforceFileLimit(dir, MaxFeedbackFiles); err != nil {
		fmt.Fprintf(os.Stderr, "[feedback] 清理旧文件失败: %v\n", err)
	}

	return nil
}

// MaxFeedbackFiles 是 ~/.whale/feedback/ 目录的文件数量上限。
// 超过后按文件名 ID 字典序删除最旧的（ID 格式 YYYYMMDD_HHMMSS_xxxxxxxx，字典序=时间序）。
const MaxFeedbackFiles = 100

// enforceFileLimit 检查目录下 .json 文件数量，超出 max 时删除最旧的。
// 按文件名（含 .json 后缀）字典序升序排序，删除前 (count - max) 个。
// 删除失败不阻塞（记录到 stderr，继续），避免清理失败影响主流程。
func enforceFileLimit(dir string, max int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("读取反馈目录失败: %w", err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}

	if len(names) <= max {
		return nil
	}

	// 字典序升序（ID 格式 YYYYMMDD_HHMMSS_xxxxxxxx，字典序=时间序）
	sort.Strings(names)

	// 删除最旧的 (count - max) 个
	toDelete := len(names) - max
	for i := 0; i < toDelete; i++ {
		path := filepath.Join(dir, names[i])
		if err := os.Remove(path); err != nil {
			// 删除失败不阻塞，记录到 stderr
			fmt.Fprintf(os.Stderr, "[feedback] 删除旧反馈文件失败 %s: %v\n", path, err)
		}
	}
	return nil
}

// Load loads a feedback record from the specified directory
func Load(dir, id string) (*Entry, error) {
	filePath := filepath.Join(dir, id+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("feedback not found: %s", id)
		}
		return nil, fmt.Errorf("failed to read feedback file: %w", err)
	}

	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("failed to parse feedback file: %w", err)
	}

	return &entry, nil
}

// List lists all feedback records in the specified directory, sorted by time descending
func List(dir string) ([]*Entry, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create feedback directory: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read feedback directory: %w", err)
	}

	var feedbacks []*Entry
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		id := strings.TrimSuffix(entry.Name(), ".json")
		fb, err := Load(dir, id)
		if err != nil {
			continue
		}
		feedbacks = append(feedbacks, fb)
	}

	sort.Slice(feedbacks, func(i, j int) bool {
		return feedbacks[i].Timestamp > feedbacks[j].Timestamp
	})

	return feedbacks, nil
}

// GenerateIssueMarkdown generates a Markdown template for submitting to GitHub Issues
func (e *Entry) GenerateIssueMarkdown() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## Feedback: %s\n\n", truncateMessage(e.Message, 80)))
	sb.WriteString("### Feedback Content\n\n")
	sb.WriteString(e.Message)
	sb.WriteString("\n\n")

	sb.WriteString("### System Info\n\n")
	sb.WriteString("| Field | Value |\n")
	sb.WriteString("|------|-----|\n")
	sb.WriteString(fmt.Sprintf("| Version | %s |\n", e.Version))
	sb.WriteString(fmt.Sprintf("| OS | %s |\n", e.OS))
	sb.WriteString(fmt.Sprintf("| Arch | %s |\n", e.Arch))
	if e.Model != "" {
		sb.WriteString(fmt.Sprintf("| Model | %s |\n", e.Model))
	}
	sb.WriteString(fmt.Sprintf("| Feedback ID | %s |\n", e.ID))
	sb.WriteString(fmt.Sprintf("| Time | %s |\n", e.Timestamp))

	return sb.String()
}

// GetGitHubIssuesURL returns the GitHub Issues creation URL
func GetGitHubIssuesURL() string {
	return "https://github.com/anchorandfish/whale-code/issues/new"
}

// generateID generates a unique Feedback ID based on timestamp and random number
func generateID(now time.Time) string {
	dateStr := now.Format("20060102")
	timeStr := now.Format("150405")

	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		randomStr := fmt.Sprintf("%d", now.UnixNano()%100000000)
		return fmt.Sprintf("%s_%s_%s", dateStr, timeStr, randomStr)
	}
	randomStr := hex.EncodeToString(randomBytes)

	return fmt.Sprintf("%s_%s_%s", dateStr, timeStr, randomStr)
}

// truncateMessage truncates message for display
func truncateMessage(msg string, maxLen int) string {
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")

	runes := []rune(msg)
	if len(runes) <= maxLen {
		return msg
	}
	return string(runes[:maxLen]) + "..."
}

// GetDefaultDir returns the default feedback storage directory
func GetDefaultDir() (string, error) {
	// 收口：Dir() 返回空（home 不可得）⇒ 报错，行为同前。
	if whalehome.Dir() == "" {
		return "", fmt.Errorf("unable to get user home directory")
	}
	return filepath.Join(whalehome.Dir(), "feedback"), nil
}

// maxSubmitRetries is the maximum number of submit retries
const maxSubmitRetries = 3

// Submit submits feedback to the remote endpoint with exponential backoff retry
func (e *Entry) Submit(endpoint string) error {
	if !strings.HasPrefix(endpoint, "https://") {
		return fmt.Errorf("feedback endpoint must use HTTPS: %s", endpoint)
	}

	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("failed to serialize feedback: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < maxSubmitRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<attempt) * time.Second
			time.Sleep(delay)
		}

		lastErr = e.doSubmit(endpoint, data)
		if lastErr == nil {
			return nil
		}
	}

	return fmt.Errorf("feedback submit failed after %d retries: %w", maxSubmitRetries, lastErr)
}

// doSubmit performs a single HTTP POST to submit feedback
func (e *Entry) doSubmit(endpoint string, data []byte) error {
	resp, err := http.Post(endpoint, "application/json", strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ===== M19-04 改造新增：/feedback 直创 GitHub Issue =====
//
// CONFLICT-08 接口依赖：复用 M23 feedback 包的脱敏逻辑（redactSensitive），
// 脱敏先于 Issue 创建（项目宪法硬约束 4）。NewEntry 构造时已对 Message 脱敏，
// SubmitToGitHub 不再二次脱敏，避免规则重叠导致输出不稳定。

// RedactSensitive 对输入字符串做敏感信息脱敏，命中后替换为 [REDACTED]。
// 薄包装 internal/secret.RedactSensitive（唯一 SSOT），供 command 包等外部调用方复用
// 同一脱敏逻辑（CONFLICT-08 / 方案 A）。
// 多次命中分别替换；未命中返回原字符串。
func RedactSensitive(s string) string {
	return redactSensitive(s)
}

// IssueTitlePrefix 是用户反馈 Issue 的标题前缀。
const IssueTitlePrefix = "[用户反馈]"

// IssueLabel 是用户反馈 Issue 的固定标签。
const IssueLabel = "user-feedback"

// IssueTitleMaxRunes 是 Issue 标题中用户内容部分的最大字符数（按 rune 计）。
const IssueTitleMaxRunes = 30

// IssueCreator 抽象 GitHub Issue 创建能力，便于测试注入 mock。
// 实现方需保证 CreateIssue 调用 GitHub API 创建 Issue 并返回可访问的 HTML URL。
// 生产实现由 command 包提供（封装 github.Client）；测试可提供替身。
type IssueCreator interface {
	// CreateIssue 创建 Issue，labels 为附加标签列表（可为空）。
	// 返回 issueURL 是 GitHub Issue 的网页地址（html_url）。
	CreateIssue(ctx context.Context, title, body string, labels []string) (issueURL string, err error)
}

// BuildIssueTitle 构造 Issue 标题：`[用户反馈] <内容前 30 字符>`。
// 按 rune 截取，避免中文截断；超长追加 "..."。
// 输入应为已脱敏的 message（由 NewEntry 保证）。
func BuildIssueTitle(message string) string {
	cleaned := strings.ReplaceAll(message, "\n", " ")
	cleaned = strings.ReplaceAll(cleaned, "\r", " ")
	runes := []rune(cleaned)
	if len(runes) <= IssueTitleMaxRunes {
		return fmt.Sprintf("%s %s", IssueTitlePrefix, cleaned)
	}
	return fmt.Sprintf("%s %s...", IssueTitlePrefix, string(runes[:IssueTitleMaxRunes]))
}

// BuildIssueBody 构造 Issue 正文：用户反馈内容 + 系统环境信息（不含 API Key）。
// entry 字段中的 Version/OS/Arch/Model/ID/Timestamp 全部来自系统，无敏感信息。
// entry.Message 已由 NewEntry 脱敏，直接嵌入 body 安全。
func BuildIssueBody(entry *Entry) string {
	var sb strings.Builder
	sb.WriteString("### 用户反馈内容\n\n")
	sb.WriteString(entry.Message)
	sb.WriteString("\n\n")
	sb.WriteString("### 系统环境信息\n\n")
	sb.WriteString("| Field | Value |\n")
	sb.WriteString("|------|-------|\n")
	sb.WriteString(fmt.Sprintf("| Whale Version | %s |\n", entry.Version))
	sb.WriteString(fmt.Sprintf("| OS | %s |\n", entry.OS))
	sb.WriteString(fmt.Sprintf("| Arch | %s |\n", entry.Arch))
	if entry.Model != "" {
		sb.WriteString(fmt.Sprintf("| Model | %s |\n", entry.Model))
	}
	sb.WriteString(fmt.Sprintf("| Feedback ID | %s |\n", entry.ID))
	sb.WriteString(fmt.Sprintf("| Time | %s |\n", entry.Timestamp))
	return sb.String()
}

// SubmitToGitHub 通过 creator 创建 GitHub Issue，返回 Issue URL。
// 脱敏已由 NewEntry 在构造 entry 时完成，本函数不再二次脱敏（避免规则重叠）。
// 失败时返回原始错误，由调用方决定降级策略（本地保存 + 手动提交指引）。
func SubmitToGitHub(entry *Entry, creator IssueCreator) (issueURL string, err error) {
	if creator == nil {
		return "", fmt.Errorf("IssueCreator is nil")
	}
	if entry == nil {
		return "", fmt.Errorf("feedback entry is nil")
	}
	title := BuildIssueTitle(entry.Message)
	body := BuildIssueBody(entry)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url, err := creator.CreateIssue(ctx, title, body, []string{IssueLabel})
	if err != nil {
		return "", fmt.Errorf("create GitHub issue failed: %w", err)
	}
	return url, nil
}
