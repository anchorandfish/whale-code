// agent_wiring_test.go — G1/G4 冒烟缺口修复的 TDD 失败测试（2026-09-08 真实冒烟暴露）。
// G1: 子代理 UsageEvent 不落账（agent.go readLoop 只累计 CacheStats，无 RecordAgentUsage 调用）
//
//	→ /usage 看不到子代理 token 消耗，"减少 token 使用"目标失去可观测基础。
//
// G4: 子代理 executeToolCall 走 ExecuteWithPipeline，NeedConfirm 直接 fail-closed 拒绝
//
//	→ coder 子代理无写权限，编排退化为顾问模式（主代理亲自执行，省 token 落空）。
//
// 断言原则（项目 Mandatory Workflow）：断言结果（文件真实落盘 / tracker 分桶数值收敛），
// 禁止只断言"回调被调用"。
package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/hook"
	"github.com/anchorandfish/whale-code/internal/permission"
)

// makeUsageStream 构造带 UsageEvent 的 mock 流（G1）。
func makeUsageStream(input, output, cacheCreation, cacheRead int) <-chan ai.Event {
	ch := make(chan ai.Event, 4)
	ch <- ai.TextDeltaEvent{Content: "done"}
	ch <- ai.UsageEvent{InputTokens: input, OutputTokens: output, CacheCreation: cacheCreation, CacheRead: cacheRead}
	ch <- ai.MessageStopEvent{}
	close(ch)
	return ch
}

// TestAgentRunRecordsUsageToTracker G1：子代理流式 UsageEvent 必须落账到 Executor.UsageTracker。
// legacy Agent 工具路径（ctx 无 agentID）应自动生成分桶 ID。
// 结果断言：tracker.AgentUsage 恰好 1 个分桶，数值与 mock 流一致；总量同步累计。
func TestAgentRunRecordsUsageToTracker(t *testing.T) {
	tmpDir := t.TempDir()
	mockClient := &agentMockClient{
		responses: []<-chan ai.Event{
			makeUsageStream(1000, 200, 50, 30),
		},
	}
	registry := NewRegistry()
	executor := NewExecutor(registry, tmpDir)
	executor.AIClient = mockClient
	executor.UsageTracker = ai.NewUsageTracker(filepath.Join(tmpDir, "usage.json"))

	agentCtx := NewAgentContext(executor, "task", "desc", tmpDir, 1)
	if _, err := agentCtx.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	tracker := executor.UsageTracker
	if len(tracker.AgentUsage) != 1 {
		t.Fatalf("AgentUsage 分桶数 = %d, want 1（子代理用量必须落账）", len(tracker.AgentUsage))
	}
	for id, au := range tracker.AgentUsage {
		if id == "" {
			t.Error("agentID 为空（legacy 路径应自动生成非空 ID）")
		}
		if au.InputTokens != 1000 || au.OutputTokens != 200 {
			t.Errorf("agent %s 用量 = in:%d out:%d, want in:1000 out:200", id, au.InputTokens, au.OutputTokens)
		}
		if au.CacheCreation != 50 || au.CacheRead != 30 {
			t.Errorf("agent %s cache = create:%d read:%d, want create:50 read:30", id, au.CacheCreation, au.CacheRead)
		}
	}
	if tracker.TotalInputTokens != 1000 || tracker.TotalOutputTokens != 200 {
		t.Errorf("总量 = in:%d out:%d, want in:1000 out:200（子代理用量应计入总量）",
			tracker.TotalInputTokens, tracker.TotalOutputTokens)
	}
}

// TestAgentRunRecordsUsageUnderCtxAgentID G1：N1 路径（ctx 已携带 agentID）按 ctx ID 落账。
// 结果断言：分桶键 = ctx 注入的 agentID，数值一致。
func TestAgentRunRecordsUsageUnderCtxAgentID(t *testing.T) {
	tmpDir := t.TempDir()
	mockClient := &agentMockClient{
		responses: []<-chan ai.Event{
			makeUsageStream(500, 80, 0, 0),
		},
	}
	registry := NewRegistry()
	executor := NewExecutor(registry, tmpDir)
	executor.AIClient = mockClient
	executor.UsageTracker = ai.NewUsageTracker(filepath.Join(tmpDir, "usage.json"))

	agentCtx := NewAgentContext(executor, "task", "", tmpDir, 1)
	ctx := WithAgentID(context.Background(), "agent-xyz-123")
	if _, err := agentCtx.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	tracker := executor.UsageTracker
	if len(tracker.AgentUsage) != 1 {
		t.Fatalf("AgentUsage 分桶数 = %d, want 1", len(tracker.AgentUsage))
	}
	au, ok := tracker.AgentUsage["agent-xyz-123"]
	if !ok {
		t.Fatalf("分桶键应为 ctx 注入的 agentID，实际键集: %v", agentUsageKeys(tracker))
	}
	if au.InputTokens != 500 || au.OutputTokens != 80 {
		t.Errorf("用量 = in:%d out:%d, want in:500 out:80", au.InputTokens, au.OutputTokens)
	}
}

// agentUsageKeys 返回分桶键列表（测试诊断用）。
func agentUsageKeys(tracker *ai.UsageTracker) []string {
	keys := make([]string, 0, len(tracker.AgentUsage))
	for k := range tracker.AgentUsage {
		keys = append(keys, k)
	}
	return keys
}

// TestAgentRunUsageTrackerNilNoPanic G1 降级：UsageTracker 未接线时 Run 不崩溃（静默降级 + CacheStats 仍累计）。
func TestAgentRunUsageTrackerNilNoPanic(t *testing.T) {
	tmpDir := t.TempDir()
	mockClient := &agentMockClient{
		responses: []<-chan ai.Event{makeUsageStream(100, 20, 0, 0)},
	}
	registry := NewRegistry()
	executor := NewExecutor(registry, tmpDir)
	executor.AIClient = mockClient
	// UsageTracker 故意不设置（nil）

	agentCtx := NewAgentContext(executor, "task", "", tmpDir, 1)
	result, err := agentCtx.Run(context.Background())
	if err != nil {
		t.Fatalf("Run 不应因 tracker nil 失败: %v", err)
	}
	if result == "" {
		t.Error("结果不应为空")
	}
}

// newConfirmTestExecutor 构造带 Pipeline（LevelWorkspaceWrite 策略）的测试 Executor（G4）。
func newConfirmTestExecutor(t *testing.T, tmpDir string) *Executor {
	t.Helper()
	registry := NewRegistry()
	executor := NewExecutor(registry, tmpDir)
	executor.InitPipeline(hook.NewManager(tmpDir), permission.NewPolicy(permission.LevelWorkspaceWrite), tmpDir)
	return executor
}

// confirmRequiringCall 返回一个**仍会触发 NeedConfirm** 的子代理调用（G4 家族的 fixture）。
//
// ⚠️ 2026-09-15 规格翻转：本家族原先用「工作区内的 write_file」当 fixture，依据是
// "write_file 在 workspace-write 下触发 NeedConfirm"。该依据已随「工作区内的写类调用免问」
// 一并推翻（新规格见 pipeline_ww_write_noconfirm_test.go，对齐业内既有实现 assess_patch_safety）。
// 若继续用 write_file，本家族 4 个用例会**全部变成空跑** —— 没有询问时，
// 「通道是否接对」自然测不出来，这条安全不变量就失去判别力。
// 故 fixture 改为 bash：它不在写类工具表内，在所有档位下都仍需确认；
// 命令自带写重定向，因此「未经确认则文件不应存在」的断言依然成立。
func confirmRequiringCall(t *testing.T, target string) (string, json.RawMessage) {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": "echo hello > " + target})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return "bash", b
}

// writeToolInput 构造 write_file 工具入参。
func writeToolInput(t *testing.T, path, content string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestAgentExecuteToolCallConfirmApproved G4：子代理 write_file 触发 NeedConfirm 时经确认通道放行。
// 结果断言：文件真实落盘且内容正确（不是只断言回调被调用）。
func TestAgentExecuteToolCallConfirmApproved(t *testing.T) {
	tmpDir := t.TempDir()
	executor := newConfirmTestExecutor(t, tmpDir)
	executor.OnConfirm = func(toolName, input string) bool { return true }

	target := filepath.Join(tmpDir, "out.txt")
	toolName, input := confirmRequiringCall(t, target)
	agentCtx := NewAgentContext(executor, "task", "", tmpDir, 1)
	if _, err := agentCtx.executeToolCall(context.Background(), toolName, input); err != nil {
		t.Fatalf("用户确认通过后子代理调用应执行成功: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("确认通过后文件应已落盘: %v", err)
	}
	if strings.TrimSpace(string(data)) != "hello" {
		t.Errorf("文件内容 = %q, want %q", string(data), "hello")
	}
}

// TestAgentExecuteToolCallConfirmDenied G4：用户拒绝时子代理写文件必须失败且不落盘。
func TestAgentExecuteToolCallConfirmDenied(t *testing.T) {
	tmpDir := t.TempDir()
	executor := newConfirmTestExecutor(t, tmpDir)
	executor.OnConfirm = func(toolName, input string) bool { return false }

	target := filepath.Join(tmpDir, "out.txt")
	toolName, input := confirmRequiringCall(t, target)
	agentCtx := NewAgentContext(executor, "task", "", tmpDir, 1)
	_, err := agentCtx.executeToolCall(context.Background(), toolName, input)
	if err == nil {
		t.Fatal("用户拒绝后应返回错误")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Error("用户拒绝后文件不应存在")
	}
}

// TestAgentExecuteToolCallAgentConfirmFnPriority G4：AgentConfirmFn（子代理专用通道）优先于 OnConfirm。
// 结果断言：AgentConfirmFn=true 且 OnConfirm=false 时写入成功（若优先级接反会失败）。
func TestAgentExecuteToolCallAgentConfirmFnPriority(t *testing.T) {
	tmpDir := t.TempDir()
	executor := newConfirmTestExecutor(t, tmpDir)
	executor.AgentConfirmFn = func(ctx context.Context, toolName, input string) ConfirmOutcome {
		return ConfirmOutcome{Decision: DecisionApproved, Source: SourceUser}
	}
	executor.OnConfirm = func(toolName, input string) bool { return false }

	target := filepath.Join(tmpDir, "out.txt")
	toolName, input := confirmRequiringCall(t, target)
	agentCtx := NewAgentContext(executor, "task", "", tmpDir, 1)
	if _, err := agentCtx.executeToolCall(context.Background(), toolName, input); err != nil {
		t.Fatalf("AgentConfirmFn 批准后应执行成功（优先于 OnConfirm）: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || strings.TrimSpace(string(data)) != "hello" {
		t.Errorf("文件应落盘: err=%v content=%q", err, string(data))
	}
}

// TestAgentExecuteToolCallNoConfirmChannel G4 fail-closed：两个确认通道均未接线时，
// 子代理写文件必须失败且不落盘，错误信息须可定位（含 confirm 字样）。
func TestAgentExecuteToolCallNoConfirmChannel(t *testing.T) {
	tmpDir := t.TempDir()
	executor := newConfirmTestExecutor(t, tmpDir)
	// OnConfirm 与 AgentConfirmFn 均不设置

	target := filepath.Join(tmpDir, "out.txt")
	toolName, input := confirmRequiringCall(t, target)
	agentCtx := NewAgentContext(executor, "task", "", tmpDir, 1)
	_, err := agentCtx.executeToolCall(context.Background(), toolName, input)
	if err == nil {
		t.Fatal("无确认通道时应 fail-closed 返回错误")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "confirm") {
		t.Errorf("错误信息应含 confirm 提示（可定位是无通道而非其他故障）: %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Error("无确认通道时文件不应存在")
	}
}
