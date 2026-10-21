// Package sandbox — 审计日志（C26/B10 + 哈希链防篡改）
package sandbox

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxAuditFileSize = 100 * 1024 * 1024
const genesisSeed = "genesis"

// maxDegradeReasons 内存级降级原因上限（防长会话无限增长；文件日志不受限）。
const maxDegradeReasons = 32

// AuditLog 沙箱审计日志。
// 子审查优化：为可观测性增加内存级事件计数（供测试"静默防护"canary 断言）。
type AuditLog struct {
	mu              sync.Mutex
	logDir          string
	current         *os.File
	currentPath     string
	date            string
	prevHash        string
	degradeCount    int
	lastDegradeFrom string
	lastDegradeTo   string
	degradeReasons  []string
	violationCount  int
	pathViolCount   int
}

type auditRecord struct {
	Timestamp string `json:"timestamp"`
	Event     string `json:"event"`
	Decision  string `json:"decision,omitempty"`
	Severity  string `json:"severity,omitempty"`
	CheckID   string `json:"check_id,omitempty"`
	Command   string `json:"command,omitempty"`
	Path      string `json:"path,omitempty"`
	Resolved  string `json:"resolved,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Message   string `json:"message,omitempty"`
	PrevHash  string `json:"prev_hash"`
}

func NewAuditLog() *AuditLog {
	// 收口：Dir() 返回空（home 不可得）⇒ 空日志器（仅内存），行为同前。
	if whalehome.Dir() == "" {
		return &AuditLog{}
	}
	logDir := filepath.Join(whalehome.Dir(), "logs")
	_ = os.MkdirAll(logDir, 0o755)
	return &AuditLog{logDir: logDir}
}

// ===== 内存级可观测性查询（测试 canary + 运行时监控） =====

func (al *AuditLog) ContainsDegradation() bool {
	if al == nil {
		return false
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	return al.degradeCount > 0
}

func (al *AuditLog) DegradationCount() int {
	if al == nil {
		return 0
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	return al.degradeCount
}

func (al *AuditLog) LastDegrade() (from, to string) {
	if al == nil {
		return "", ""
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	return al.lastDegradeFrom, al.lastDegradeTo
}

// DegradeReasons 返回内存级降级原因快照（门 2：供启动 banner 用户可见展示，
// 上限 maxDegradeReasons；完整轨迹见文件审计日志）。
func (al *AuditLog) DegradeReasons() []string {
	if al == nil {
		return nil
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	if len(al.degradeReasons) == 0 {
		return nil
	}
	out := make([]string, len(al.degradeReasons))
	copy(out, al.degradeReasons)
	return out
}

func (al *AuditLog) EventCounts() (degrade, violation, pathViol int) {
	if al == nil {
		return 0, 0, 0
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	return al.degradeCount, al.violationCount, al.pathViolCount
}

// ===== 事件记录方法 =====

func (al *AuditLog) LogViolations(command string, violations []SecurityViolation, blocked bool) {
	if al == nil {
		return
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	ts := time.Now().Format(time.RFC3339)
	decision := "ALLOW"
	if blocked {
		decision = "BLOCK"
	}
	for _, v := range violations {
		al.violationCount++
		rec := auditRecord{
			Timestamp: ts, Event: "VIOLATION", Decision: decision,
			Severity: severityName(v.Severity), CheckID: v.CheckID,
			Command: command, Message: v.Message,
		}
		al.writeRecord(rec)
	}
}

func (al *AuditLog) LogPathViolation(path, absPath string) {
	if al == nil {
		return
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	al.pathViolCount++
	rec := auditRecord{
		Timestamp: time.Now().Format(time.RFC3339), Event: "PATH_VIOLATION",
		Decision: "BLOCK", Severity: "high", CheckID: "PATH",
		Path: path, Resolved: absPath, Message: "path outside allowed range",
	}
	al.writeRecord(rec)
}

func (al *AuditLog) LogDegradation(from, to SandboxLevel, reason string) {
	if al == nil {
		return
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	al.degradeCount++
	al.lastDegradeFrom = from.String()
	al.lastDegradeTo = to.String()
	if len(al.degradeReasons) < maxDegradeReasons {
		al.degradeReasons = append(al.degradeReasons, reason)
	}
	rec := auditRecord{
		Timestamp: time.Now().Format(time.RFC3339), Event: "DEGRADE",
		From: from.String(), To: to.String(), Reason: reason,
	}
	al.writeRecord(rec)
}

// LogStatus 记录信息性状态事件（如"namespaces enabled"）。
// 门 2 修复：此前信息性记录复用 LogDegradation（from==to），污染降级计数与
// DegradeReasons，导致启动 banner 把正常启用误报为降级。STATUS 事件写文件
// 审计轨迹但不计入内存级降级计数。
func (al *AuditLog) LogStatus(message string) {
	if al == nil {
		return
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	rec := auditRecord{
		Timestamp: time.Now().Format(time.RFC3339), Event: "STATUS", Message: message,
	}
	al.writeRecord(rec)
}

func (al *AuditLog) writeRecord(rec auditRecord) {
	if al.logDir == "" {
		return
	}
	today := time.Now().Format("20060102")
	if al.current == nil || al.date != today {
		al.rotateFile(today)
	}
	if al.current != nil {
		if info, err := al.current.Stat(); err == nil && info.Size()+int64(2048) > maxAuditFileSize {
			al.rotateFile(today)
		}
	}
	if al.current == nil {
		return
	}
	rec.PrevHash = al.prevHash
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if _, err := al.current.WriteString(string(data) + "\n"); err != nil {
		return
	}
	al.prevHash = fmt.Sprintf("%x", sha256.Sum256(data))
}

func (al *AuditLog) rotateFile(date string) {
	if al.current != nil {
		_ = al.current.Close()
		al.current = nil
	}
	al.date = date
	name := fmt.Sprintf("sandbox-audit-%s.log", date)
	path := filepath.Join(al.logDir, name)
	if info, err := os.Stat(path); err == nil && info.Size() >= maxAuditFileSize {
		stamp := time.Now().Format("150405")
		name = fmt.Sprintf("sandbox-audit-%s-%s.log", date, stamp)
		path = filepath.Join(al.logDir, name)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	al.current = f
	al.currentPath = path
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		al.prevHash = readLastLineHash(path)
	} else {
		al.prevHash = computeHash([]byte(genesisSeed))
	}
}

func readLastLineHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return computeHash([]byte(genesisSeed))
	}
	defer f.Close()
	var lastLine string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) != "" {
			lastLine = line
		}
	}
	if lastLine == "" {
		return computeHash([]byte(genesisSeed))
	}
	return computeHash([]byte(lastLine))
}

func computeHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func (al *AuditLog) VerifyIntegrity() error {
	if al == nil {
		return fmt.Errorf("audit log is nil")
	}
	if al.logDir == "" {
		return fmt.Errorf("audit log directory not initialized")
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	path := al.currentPath
	if path == "" {
		today := time.Now().Format("20060102")
		path = filepath.Join(al.logDir, fmt.Sprintf("sandbox-audit-%s.log", today))
	}
	return verifyFileIntegrity(path)
}

func verifyFileIntegrity(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open audit log: %w", err)
	}
	defer f.Close()
	expectedHash := computeHash([]byte(genesisSeed))
	lineNum := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		lineNum++
		raw := scanner.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var rec auditRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("line %d: invalid JSON: %w", lineNum, err)
		}
		if rec.PrevHash != expectedHash {
			return fmt.Errorf("line %d: hash mismatch (expected %s, got %s)",
				lineNum, expectedHash, rec.PrevHash)
		}
		expectedHash = computeHash(raw)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read audit log: %w", err)
	}
	return nil
}

func (al *AuditLog) Close() {
	if al == nil {
		return
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	if al.current != nil {
		_ = al.current.Close()
		al.current = nil
	}
}

func severityName(sev int) string {
	switch sev {
	case SeverityCritical:
		return "critical"
	case SeverityHigh:
		return "high"
	case SeverityMedium:
		return "medium"
	case SeverityLow:
		return "low"
	default:
		return "unknown"
	}
}
