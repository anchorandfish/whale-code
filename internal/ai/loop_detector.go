// Package ai — V3-6-2 AI 循环检测
//
// 双重检测：1) 同一工具+同一参数连续调用 2) 连续 N 轮无进展。
//
// 设计要点（审查修订建议-2）：
// - 同一工具+同一参数（SHA256 前 8 位指纹）连续 N 次触发循环警告
// - 区分成功调用（threshold=3）与失败重试（failThreshold=5）：
// 工具调用失败时不计入循环计数，单独统计失败重试次数
// - **只在首次跨阈报一次**（判定用 `==` 而非 `>=`）：同一串连续重复不复读，
// 避免 3/4/5/6… 连刷（2026-09-30 真机 I-8.2）。本检测器是 **advisory**
// 层——只负责"让人看见正在发生的循环"；**强制停机不在本包**，由 agentguard
// P0-4 的「同一工具 + 同一参数反复调用」判据承担（S17 纵审 §6 的分工判定）。
// - 无进展检测：连续 noProgressMax 轮无新工具调用且无文件变更 → 警告
// - 并发安全：history 加 sync.Mutex 保护（审查建议-8）
package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// 默认阈值常量
const (
	defaultLoopThreshold    = 3 // 成功调用循环阈值
	defaultFailThreshold    = 5 // 失败重试阈值
	defaultNoProgressMax    = 5 // 无进展最大轮数
	inputHashTruncateLength = 8 // 输入哈希截断长度（SHA256 前 8 位）
)

// LoopDetector 检测 AI 循环行为。
// 双重检测：1) 同一工具+同一参数连续调用 2) 连续 N 轮无进展。
type LoopDetector struct {
	mu              sync.Mutex  // 保护 history 和 noProgressCount 的并发访问
	history         []LoopEntry // 工具调用历史
	threshold       int         // 循环阈值（成功调用，默认 3）
	failThreshold   int         // 失败重试阈值（默认 5）
	noProgressMax   int         // 无进展最大轮数（默认 5）
	noProgressCount int         // 当前无进展计数
}

// LoopEntry 记录一次工具调用的循环检测关键信息。
type LoopEntry struct {
	ToolName  string    // 工具名
	InputHash string    // 工具输入的 SHA256 前 8 位
	Success   bool      // 调用是否成功（审查建议-2：区分失败重试）
	Timestamp time.Time // 调用时间
}

// Call 描述一次工具调用，供 CheckLoopBatch 批量检测。
// 与 CheckLoop 的 (toolName, input, success) 三参数同构。
type Call struct {
	ToolName string
	Input    string
	Success  bool
}

// NewLoopDetector 创建循环检测器，阈值可配置。
// threshold: 成功调用循环阈值；failThreshold: 失败重试阈值；noProgressMax: 无进展最大轮数。
// 任一参数 ≤ 0 时使用默认值（3/5/5）。
func NewLoopDetector(threshold, failThreshold, noProgressMax int) *LoopDetector {
	if threshold <= 0 {
		threshold = defaultLoopThreshold
	}
	if failThreshold <= 0 {
		failThreshold = defaultFailThreshold
	}
	if noProgressMax <= 0 {
		noProgressMax = defaultNoProgressMax
	}
	return &LoopDetector{
		history:       make([]LoopEntry, 0),
		threshold:     threshold,
		failThreshold: failThreshold,
		noProgressMax: noProgressMax,
	}
}

// CheckLoop 检测是否存在循环行为（线程安全）。
// toolName: 工具名；input: 工具输入；success: 调用是否成功。
// 成功调用连续 threshold 次相同参数 → 循环警告
// 失败调用连续 failThreshold 次相同参数 → 重试警告（非循环）
// 返回：警告消息（空表示无循环）。
//
// 注意：CheckLoop 每次调用视为一个独立操作（跨调用累计）。若一次工具执行对应
// 同一条 assistant 消息里的多个并列 tool_use，应改用 CheckLoopBatch，否则会把
// 合法并列误判为跨轮失控（见 CheckLoopBatch 文档）。
func (d *LoopDetector) CheckLoop(toolName, input string, success bool) string {
	return d.recordAndCheck(toolName, hashInput(input), success)
}

// CheckLoopBatch 检测"同一轮（同一条 assistant 消息）"内多个工具结果的循环行为。
// 语义：批内同一 (toolName, inputHash, success) 指纹只计一次——同批并列委派不算循环；
// 跨批相同指纹连续出现达到 threshold 才触发循环警告；失败重试同理（failThreshold）。
//
// 修复背景（TUI 实测误报）：并发发多个同参数 explore 子 agent 读 README 时，
// 原逐条 CheckLoop 把同一轮的 6 个并列 tool_use 当 6 轮失控重复，连续弹
// "循环 3/4/5/6"。本方法按批去重后统计，既消除该误报，又保留跨批失控检测。
//
// 返回本批内触发的警告列表（空表示本批无循环）。并发安全。
func (d *LoopDetector) CheckLoopBatch(calls []Call) []string {
	if len(calls) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var warns []string
	seen := make(map[string]struct{}, len(calls))
	for _, c := range calls {
		hash := hashInput(c.Input)
		key := c.ToolName + "\x00" + hash + "\x00" + boolStr(c.Success)
		if _, dup := seen[key]; dup {
			continue // 批内去重：同批并列同指纹只算一次
		}
		seen[key] = struct{}{}
		if w := d.recordAndCheckLocked(c.ToolName, hash, c.Success); w != "" {
			warns = append(warns, w)
		}
	}
	return warns
}

// recordAndCheck 记录一次工具调用并检测循环（供 CheckLoop 使用，每次自动加锁）。
func (d *LoopDetector) recordAndCheck(toolName, hash string, success bool) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.recordAndCheckLocked(toolName, hash, success)
}

// recordAndCheckLocked 记录一次工具调用并检测循环；调用方须已持有 d.mu。
func (d *LoopDetector) recordAndCheckLocked(toolName, hash string, success bool) string {
	entry := LoopEntry{
		ToolName:  toolName,
		InputHash: hash,
		Success:   success,
		Timestamp: time.Now(),
	}

	d.history = append(d.history, entry)

	// 计算当前连续相同指纹次数（按 success 分组：成功/失败计数互不干扰）
	consecutive := 1
	for i := len(d.history) - 2; i >= 0; i-- {
		prev := d.history[i]
		if prev.ToolName == toolName && prev.InputHash == hash && prev.Success == success {
			consecutive++
		} else {
			break
		}
	}

	if success {
		// ★ 只在**首次跨阈**报一次（2026-09-30 真机 I-8.2 去噪）。
		//
		// 原实现是 `>= d.threshold` ⇒ 3/4/5/6… 每多一次再刷一条；实测同一条
		// `echo hi > out/i8_created.txt` 连发 4 次就刷出 2 条几乎相同的告警，
		// 把窗口占满却没多给任何信息。改用 `==` 后：同一串连续重复只报一次；
		// 被别的调用打断后**重新**累计到阈值会再报——那是新的一串重复，
		// 必须仍然可见（不退化守卫见 loop_alert_throttle_test.go 第 ② 条）。
		//
		// 文案与行为对齐：消费点（tui/tool_exec.go）只 addMessage，**不会中断**
		// ⇒ 原文案问「是否中断？」是承诺一个不存在的交互。现在给真实可行的动作
		// （TUI 里 Esc / Ctrl+C），并说明"继续重复将被自动暂停"——该承诺**不在本包实现**，
		// 由 agentguard P0-4 的「同一工具 + 同一参数反复调用」判据兑现
		// （internal/agentguard/progress.go 的 maxSameToolCalls）。
		if consecutive == d.threshold {
			return fmt.Sprintf("⚠️ 检测到循环行为：%s 已连续执行 %d 次相同操作（继续重复将被自动暂停；按 Esc 立即中断）", toolName, consecutive)
		}
	} else {
		// 失败重试同理：只在首次跨阈报一次，不复读。
		if consecutive == d.failThreshold {
			return fmt.Sprintf("⚠️ 检测到重试失败：%s 已连续失败 %d 次，建议检查问题", toolName, consecutive)
		}
	}
	return ""
}

// boolStr 把 bool 转成稳定 key 片段（避免引入 strconv）。
func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// CheckNoProgress 检测是否无进展（线程安全）。
// hasNewToolCall: 本轮是否有新工具调用；hasFileChange: 本轮是否有文件变更。
// 返回：警告消息（空表示有进展）。
func (d *LoopDetector) CheckNoProgress(hasNewToolCall, hasFileChange bool) string {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 有新工具调用或文件变更 → 有进展，重置计数
	if hasNewToolCall || hasFileChange {
		d.noProgressCount = 0
		return ""
	}

	d.noProgressCount++
	if d.noProgressCount >= d.noProgressMax {
		return fmt.Sprintf("⚠️ 任务似乎无进展，已 %d 轮无新操作", d.noProgressCount)
	}
	return ""
}

// Reset 重置检测器状态。
func (d *LoopDetector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.history = make([]LoopEntry, 0)
	d.noProgressCount = 0
}

// hashInput 计算工具输入的 SHA256 前 8 位作为指纹。
func hashInput(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:inputHashTruncateLength]
}
