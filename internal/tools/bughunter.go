// Package tools — Bug Hunter mode manager
// BugHunterManager manages Bug Hunter mode start/stop and issue tracking
package tools

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// BugIssue represents an issue found by Bug Hunter
type BugIssue struct {
	ID          string     `json:"id"`
	File        string     `json:"file"`
	Line        int        `json:"line"`
	Severity    string     `json:"severity"` // high, medium, low
	Description string     `json:"description"`
	Status      string     `json:"status"` // open, fixed, ignored
	FoundAt     time.Time  `json:"found_at"`
	FixedAt     *time.Time `json:"fixed_at,omitempty"`
	Confidence  float64    `json:"confidence"`         // 0.0 - 1.0
	RuleIDs     []string   `json:"rule_ids,omitempty"` // matching rule IDs for correlation
}

// confidenceWeights maps severity to base confidence weight
var confidenceWeights = map[string]float64{
	"high":   0.8,
	"medium": 0.5,
	"low":    0.3,
}

// CalculateConfidence computes confidence score considering rule correlation and base weight
func CalculateConfidence(file, severity string, ruleIDs []string, existingIssues []BugIssue) float64 {
	base, ok := confidenceWeights[severity]
	if !ok {
		base = 0.3
	}

	correlationBonus := 0.0
	if len(ruleIDs) > 1 {
		correlationBonus = 0.1 * float64(len(ruleIDs)-1)
		if correlationBonus > 0.3 {
			correlationBonus = 0.3
		}
	}

	sameFileCount := 0
	for _, issue := range existingIssues {
		if issue.File != file {
			continue
		}
		for _, rid := range ruleIDs {
			for _, eid := range issue.RuleIDs {
				if rid == eid {
					sameFileCount++
				}
			}
		}
	}
	if sameFileCount > 0 {
		correlationBonus += 0.05 * float64(sameFileCount)
		if correlationBonus > 0.4 {
			correlationBonus = 0.4
		}
	}

	confidence := base + correlationBonus
	if confidence > 1.0 {
		confidence = 1.0
	}
	return confidence
}

// BugHunterManager manages Bug Hunter mode state and issue tracking
// Modeled after a comparable CLI's bughunter feature, controls whether AI runs in Bug detection mode
type BugHunterManager struct {
	mu           sync.RWMutex
	running      bool
	startedAt    time.Time
	lastScan     time.Time
	issues       []BugIssue
	nextID       atomic.Int64
	MaxScanFiles int // maximum number of files to scan, default 500
}

// NewBugHunterManager creates a Bug Hunter mode manager
func NewBugHunterManager() *BugHunterManager {
	bh := &BugHunterManager{
		MaxScanFiles: 500,
	}
	bh.nextID.Store(0)
	return bh
}

// IsRunning returns whether Bug Hunter mode is running
func (b *BugHunterManager) IsRunning() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.running
}

// IssueCount returns the total number of issues found
func (b *BugHunterManager) IssueCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.issues)
}

// CanScanMore checks whether more files can be scanned based on MaxScanFiles limit
// should be called before each scan to respect scan limits
func (b *BugHunterManager) CanScanMore(scannedCount int) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return scannedCount < b.MaxScanFiles
}

// GetMaxScanFiles returns the maximum number of files to scan
func (b *BugHunterManager) GetMaxScanFiles() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.MaxScanFiles
}

// Start 启动 Bug Hunter 模式并返回状态描述
func (b *BugHunterManager) Start() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.running {
		return util.Bi(
			"Bug Hunter mode is already running\nUse /bughunter status to check progress",
			"Bug Hunter 模式已在运行\n使用 /bughunter status 查看进度",
		)
	}

	b.running = true
	b.startedAt = time.Now()
	return formatBugHunterStarted()
}

// Stop 停止 Bug Hunter 模式并返回状态描述（含本次扫描汇总）
func (b *BugHunterManager) Stop() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.running {
		return util.Bi(
			"Bug Hunter mode is not running\nUse /bughunter start to begin",
			"Bug Hunter 模式未运行\n使用 /bughunter start 启动",
		)
	}

	b.running = false
	b.lastScan = time.Now()

	openCount := 0
	fixedCount := 0
	for _, issue := range b.issues {
		if issue.Status == "open" {
			openCount++
		} else if issue.Status == "fixed" {
			fixedCount++
		}
	}

	return util.BiSame(
		"Bug Hunter mode stopped\nThis scan: %d issues found, %d fixed, %d pending",
		"Bug Hunter 模式已停止\n本次扫描：发现 %d 个问题，已fixed %d，待处理 %d",
		len(b.issues), fixedCount, openCount)
}

// Status returns the current status description
func (b *BugHunterManager) Status() string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.running {
		return b.formatRunningStatus()
	}
	return b.formatStoppedStatus()
}

// AddIssue records a newly found issue and returns the issue ID
func (b *BugHunterManager) AddIssue(issue BugIssue) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := fmt.Sprintf("BH-%03d", b.nextID.Add(1))
	issue.ID = id
	issue.FoundAt = time.Now()
	if issue.Status == "" {
		issue.Status = "open"
	}
	b.issues = append(b.issues, issue)
	return id
}

// MarkIssueFixed marks the specified issue as fixed
func (b *BugHunterManager) MarkIssueFixed(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i := range b.issues {
		if b.issues[i].ID == id {
			if b.issues[i].Status == "fixed" {
				return fmt.Errorf("issue %s is already marked as fixed", id)
			}
			now := time.Now()
			b.issues[i].Status = "fixed"
			b.issues[i].FixedAt = &now
			return nil
		}
	}
	return fmt.Errorf("issue %s does not exist", id)
}

// ListIssues filters issues by status, empty string means all statuses
func (b *BugHunterManager) ListIssues(status string) []BugIssue {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if status == "" {
		result := make([]BugIssue, len(b.issues))
		copy(result, b.issues)
		return result
	}

	var result []BugIssue
	for _, issue := range b.issues {
		if issue.Status == status {
			result = append(result, issue)
		}
	}
	return result
}

// GetSystemPrompt returns the system prompt for Bug Hunter mode
func (b *BugHunterManager) GetSystemPrompt() string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.running {
		return ""
	}

	sb := &strings.Builder{}
	fmt.Fprintln(sb, "[SYSTEM_MODE: BugHunter]")
	fmt.Fprintln(sb, "You are now in BugHunter mode. When reviewing or writing code:")
	fmt.Fprintln(sb, "1. SCAN: Actively look for potential bugs, security vulnerabilities, and code smells")
	fmt.Fprintln(sb, "2. IDENTIFY: For each issue found, describe the problem clearly with file and line")
	fmt.Fprintln(sb, "3. REPORT: Report each issue with its severity (high/medium/low)")
	fmt.Fprintln(sb, "4. FIX: Propose and implement fixes for identified issues, then mark as fixed")
	fmt.Fprintln(sb, "5. TRACK: Use /bughunter status to track overall progress")
	fmt.Fprintln(sb, "Use /bughunter stop to end this mode.")
	return sb.String()
}

// formatRunningStatus 格式化运行中的状态描述
func (b *BugHunterManager) formatRunningStatus() string {
	bld := &strings.Builder{}
	fmt.Fprintln(bld, util.Bi("Bug Hunter status: running", "Bug Hunter 状态：运行中"))
	fmt.Fprintln(bld, util.BiSame("Started: %s", "启动时间: %s", b.startedAt.Format("2006-01-02 15:04:05")))
	fmt.Fprintln(bld, util.BiSame("Uptime: %v", "运行时长: %v", time.Since(b.startedAt).Round(time.Second)))

	openCount := 0
	fixedCount := 0
	for _, issue := range b.issues {
		if issue.Status == "open" {
			openCount++
		} else if issue.Status == "fixed" {
			fixedCount++
		}
	}
	fmt.Fprintln(bld, util.BiSame(
		"Issues found: %d (pending: %d, fixed: %d)",
		"发现问题: %d（待处理: %d，已fixed: %d）",
		len(b.issues), openCount, fixedCount))

	if len(b.issues) > 0 {
		fmt.Fprintln(bld, "\n"+util.Bi("Issues:", "问题列表:"))
		for _, issue := range b.issues {
			statusIcon := "O"
			if issue.Status == "fixed" {
				statusIcon = "X"
			} else if issue.Status == "ignored" {
				statusIcon = "-"
			}
			fmt.Fprintf(bld, "  [%s] %s — %s:%d — %s (%s)\n",
				statusIcon, issue.ID, issue.File, issue.Line,
				issue.Severity, issue.Description)
		}
	}
	return bld.String()
}

// formatStoppedStatus 格式化已停止的状态描述
func (b *BugHunterManager) formatStoppedStatus() string {
	bld := &strings.Builder{}
	fmt.Fprintln(bld, util.Bi("Bug Hunter status: not running", "Bug Hunter 状态：未运行"))

	if !b.lastScan.IsZero() {
		fmt.Fprintln(bld, util.BiSame("Last scan: %s", "上次扫描: %s", b.lastScan.Format("2006-01-02 15:04:05")))
	}
	if len(b.issues) > 0 {
		openCount := 0
		fixedCount := 0
		for _, issue := range b.issues {
			if issue.Status == "open" {
				openCount++
			} else if issue.Status == "fixed" {
				fixedCount++
			}
		}
		fmt.Fprintln(bld, util.BiSame(
			"Issues found: %d (pending: %d, fixed: %d)",
			"共发现: %d 个问题（待处理: %d，已fixed: %d）",
			len(b.issues), openCount, fixedCount))
	}
	fmt.Fprintln(bld, util.Bi("Use /bughunter start to begin", "使用 /bughunter start 启动"))
	return bld.String()
}

// formatBugHunterStarted 返回启动后的状态描述
func formatBugHunterStarted() string {
	b := &strings.Builder{}
	fmt.Fprintln(b, util.Bi("Bug Hunter mode started", "Bug Hunter 模式已启动"))
	fmt.Fprintln(b)
	fmt.Fprintln(b, util.Bi("The AI will automatically:", "AI 将自动："))
	fmt.Fprintln(b, util.Bi("1. Scan the codebase for potential bugs", "1. 扫描代码库中的潜在 bug"))
	fmt.Fprintln(b, util.Bi("2. Identify security vulnerabilities and code smells", "2. 识别安全漏洞和代码异味"))
	fmt.Fprintln(b, util.Bi("3. Track the list of issues found", "3. 跟踪发现的问题列表"))
	fmt.Fprintln(b)
	fmt.Fprintln(b, util.Bi("Use /bughunter status to check progress", "使用 /bughunter status 查看进度"))
	fmt.Fprintln(b, util.Bi("Use /bughunter stop to stop scanning", "使用 /bughunter stop 停止扫描"))
	return b.String()
}
