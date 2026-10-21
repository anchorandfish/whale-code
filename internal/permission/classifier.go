package permission

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ⚠️ R5 死代码风险声明（阶段 2 收模式，2026-09-10）：
//
// 本文件（Classifier / AIClassifier）**不在权限决策主路径上**。
// 唯一集成入口 ClassifyActionWithMemory（permission_dialog.go）没有任何生产调用方，
// 仅有测试引用。历史上它服务的是 LevelAuto（"AI 分类器决定权限"）档，
// 该档已在阶段 2 移除 —— 对齐业内既有实现：权限决策是确定性/规则驱动的，不做 AI 猜测
// （AI 裁决不可审计，且与规则引擎职责冲突）。
//
// 保留原因（非死代码豁免）：PERMISSION_STAGE2-5_HANDOFF.md §4 计划把
// 「执行后验证 / 审计日志」与 AgentTypeVerification 一起设计，本分类器基础设施
// （allowlist / 缓存 / denial 计数）可作为其复用底座。
// 若将来确认不再需要，应连同 permission_dialog.go 的 M12-05 集成一并删除。
//
// 结论：本文件当前**第一次被生产执行的位置不存在**；改动权限决策时不要把它当作
// 生效路径，也不要在 Check/checkModeLocked 里引用它。

// ClassifierDecision is the classifier decision result
type ClassifierDecision int

const (
	DecisionAllow ClassifierDecision = iota
	DecisionDeny
	DecisionUncertain
)

// ClassifierResult is the classifier result
type ClassifierResult struct {
	Decision   ClassifierDecision
	Reason     string
	Confidence float64
	Source     string // "allowlist", "fastpath", "ai"
	DurationMs int64
}

// ClassifierConfig is the classifier configuration
type ClassifierConfig struct {
	Enabled         bool
	FailClosed      bool
	MaxContextTurns int
	Timeout         time.Duration
}

// ClassifierAction represents a tool call to classify
type ClassifierAction struct {
	ToolName    string
	ToolInput   string
	Description string
}

// AIClassifier is the interface for calling AI model for classification
type AIClassifier interface {
	Classify(ctx context.Context, action ClassifierAction, context []string) (*ClassifierResult, error)
}

// Classifier is the auto-mode classifier
type Classifier struct {
	mu        sync.RWMutex
	config    ClassifierConfig
	aiClient  AIClassifier
	allowlist map[string]bool
	cache     map[string]ClassifierResult
	cacheKeys []string
	maxCache  int
	tracker   *DenialTracker
}

// NewClassifier creates a classifier
func NewClassifier(config ClassifierConfig) *Classifier {
	return &Classifier{
		config:    config,
		allowlist: defaultAllowlist(),
		cache:     make(map[string]ClassifierResult),
		cacheKeys: make([]string, 0),
		maxCache:  1024,
		tracker:   NewDenialTracker(),
	}
}

// SetAIClient sets the AI classifier client
func (c *Classifier) SetAIClient(client AIClassifier) {
	c.aiClient = client
}

// InvalidateCache clears the classification result cache
func (c *Classifier) InvalidateCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]ClassifierResult)
}

// SetDenialTracker sets the denial tracker
func (c *Classifier) SetDenialTracker(tracker *DenialTracker) {
	c.tracker = tracker
}

// DenialTracker gets the denial tracker
func (c *Classifier) DenialTracker() *DenialTracker {
	return c.tracker
}

// defaultAllowlist is the safe operation allowlist
func defaultAllowlist() map[string]bool {
	allowlist := map[string]bool{
		"read_file":         true,
		"grep_search":       true,
		"glob_search":       true,
		"LSP":               true,
		"todo_write":        true,
		"web_fetch":         true,
		"web_search":        true,
		"ask_user_question": true,
		"Task":              true,
		"Skill":             true,
	}

	// add lowercase variants
	for k, v := range allowlist {
		allowlist[strings.ToLower(k)] = v
	}

	return allowlist
}

// IsAllowlisted returns true if the tool is on the safe allowlist
func (c *Classifier) IsAllowlisted(toolName string) bool {
	return c.allowlist[strings.ToLower(toolName)]
}

// Classify decides whether a tool call should be allowed in auto mode
func (c *Classifier) Classify(ctx context.Context, action ClassifierAction) ClassifierResult {
	start := time.Now()
	toolName := strings.ToLower(action.ToolName)

	cacheKey := toolName + ":" + action.ToolInput

	c.mu.RLock()
	if cached, ok := c.cache[cacheKey]; ok {
		c.mu.RUnlock()
		return cached
	}
	c.mu.RUnlock()

	result := c.classifyInternal(ctx, action, start)

	c.mu.Lock()
	if len(c.cache) >= c.maxCache {
		evictKey := c.cacheKeys[0]
		c.cacheKeys = c.cacheKeys[1:]
		delete(c.cache, evictKey)
	}
	c.cache[cacheKey] = result
	c.cacheKeys = append(c.cacheKeys, cacheKey)
	c.mu.Unlock()

	return result
}

// classifyInternal performs the actual classification logic
func (c *Classifier) classifyInternal(ctx context.Context, action ClassifierAction, start time.Time) ClassifierResult {
	toolName := strings.ToLower(action.ToolName)

	// Step 1: Safe allowlist direct allow
	if c.IsAllowlisted(toolName) {
		return ClassifierResult{
			Decision:   DecisionAllow,
			Reason:     fmt.Sprintf("Tool %q is in the safe allowlist", action.ToolName),
			Confidence: 1.0,
			Source:     "allowlist",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	// Step 2: acceptEdits fast path
	if isEditTool(toolName) {
		return ClassifierResult{
			Decision:   DecisionAllow,
			Reason:     "File edit operation, auto-accepted within workspace",
			Confidence: 0.9,
			Source:     "fastpath",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	// Step 3: PowerShell requires user confirmation (unless explicitly configured)
	if toolName == "powershell" {
		return ClassifierResult{
			Decision:   DecisionUncertain,
			Reason:     "PowerShell operation requires user confirmation",
			Confidence: 0.0,
			Source:     "fastpath",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	// Step 4: Bash command classification
	if toolName == "bash" {
		return c.classifyBash(ctx, action, start)
	}

	// Step 5: AI classifier
	if c.aiClient != nil {
		result, err := c.aiClient.Classify(ctx, action, nil)
		if err == nil && result != nil {
			result.DurationMs = time.Since(start).Milliseconds()
			return *result
		}
	}

	// Step 6: Cannot determine, decide based on failClosed config
	if c.config.FailClosed {
		return ClassifierResult{
			Decision:   DecisionUncertain,
			Reason:     "Cannot determine safety, user confirmation required",
			Confidence: 0.0,
			Source:     "fallback",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	// fail-open: allow unknown tools
	return ClassifierResult{
		Decision:   DecisionAllow,
		Reason:     fmt.Sprintf("Cannot classify tool %q, fail-open allows by default", action.ToolName),
		Confidence: 0.3,
		Source:     "fail-open",
		DurationMs: time.Since(start).Milliseconds(),
	}
}

// classifyBash classifies bash commands based on risk analysis
func (c *Classifier) classifyBash(ctx context.Context, action ClassifierAction, start time.Time) ClassifierResult {
	cmd := extractCommand(action.ToolInput)
	risk := AnalyzeBashCommand(cmd)

	switch risk {
	case BashRiskReadOnly:
		return ClassifierResult{
			Decision:   DecisionAllow,
			Reason:     "Read-only command, safe to execute",
			Confidence: 1.0,
			Source:     "fastpath",
			DurationMs: time.Since(start).Milliseconds(),
		}
	case BashRiskDangerous:
		return ClassifierResult{
			Decision:   DecisionUncertain,
			Reason:     fmt.Sprintf("Dangerous command: %s", truncateCommand(cmd, 80)),
			Confidence: 0.0,
			Source:     "fastpath",
			DurationMs: time.Since(start).Milliseconds(),
		}
	case BashRiskDestructive:
		// Destructive command: hand to AI classifier or fallback
		if c.aiClient != nil {
			result, err := c.aiClient.Classify(ctx, action, nil)
			if err == nil && result != nil {
				result.DurationMs = time.Since(start).Milliseconds()
				return *result
			}
		}
		return ClassifierResult{
			Decision:   DecisionUncertain,
			Reason:     fmt.Sprintf("Destructive command requires confirmation: %s", truncateCommand(cmd, 80)),
			Confidence: 0.0,
			Source:     "fastpath",
			DurationMs: time.Since(start).Milliseconds(),
		}
	default:
		return ClassifierResult{
			Decision:   DecisionUncertain,
			Reason:     "Cannot analyze command risk",
			Confidence: 0.0,
			Source:     "fallback",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}
}

// isEditTool returns true for file editing tools
func isEditTool(toolName string) bool {
	editTools := map[string]bool{
		"write_file":   true,
		"edit_file":    true,
		"notebookedit": true,
	}
	return editTools[toolName]
}

// truncateCommand truncates a command string for display
func truncateCommand(cmd string, maxLen int) string {
	if len(cmd) <= maxLen {
		return cmd
	}
	return cmd[:maxLen-3] + "..."
}

// ResolveWithTracking resolves classifier decision with denial tracking
func (c *Classifier) ResolveWithTracking(result ClassifierResult) (allowed bool, needConfirm bool) {
	switch result.Decision {
	case DecisionAllow:
		if c.tracker != nil {
			c.tracker.RecordSuccess()
		}
		return true, false

	case DecisionDeny:
		if c.tracker != nil {
			c.tracker.RecordDenial()
			if c.tracker.ShouldFallback() {
				if c.tracker.IsTotalFallback() {
					return false, false // agent abort level
				}
				return true, true // Fallback to manual confirmation
			}
		}
		return false, false

	case DecisionUncertain:
		if c.tracker != nil {
			c.tracker.RecordDenial()
			if c.tracker.ShouldFallback() {
				if c.tracker.IsTotalFallback() {
					return false, false
				}
				return true, true
			}
		}
		return true, true

	default:
		return true, true // Default manual confirmation
	}
}
