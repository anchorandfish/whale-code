package context

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
)

// TestMicroCompact_Basic 测试基本微压缩
// 验证近期工具结果被保留，旧的超出截断阈值的结果被替换为摘要占位符
func TestMicroCompact_Basic(t *testing.T) {
	config := MicroCompactConfig{
		PreserveRecent:      2,
		TimeThreshold:       1 * time.Minute,
		TruncationThreshold: 100,
	}

	// 构造消息：两条旧的超阈值工具结果 + 两条近期的超阈值工具结果
	messages := []session.Message{
		{
			Role:      session.RoleTool,
			Timestamp: time.Now().Add(-10 * time.Minute).UnixMilli(), // 10 分钟前，旧消息
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: strings.Repeat("x", 200), ToolUseID: "id1", ToolName: "read_file"},
			},
		},
		{
			Role:      session.RoleTool,
			Timestamp: time.Now().Add(-5 * time.Minute).UnixMilli(), // 5 分钟前，旧消息
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: strings.Repeat("y", 200), ToolUseID: "id2", ToolName: "grep"},
			},
		},
		{
			Role:      session.RoleTool,
			Timestamp: time.Now().UnixMilli(), // 刚刚，近期消息
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: strings.Repeat("z", 200), ToolUseID: "id3", ToolName: "read_file"},
			},
		},
		{
			Role:      session.RoleTool,
			Timestamp: time.Now().UnixMilli(), // 刚刚，近期消息
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: strings.Repeat("w", 200), ToolUseID: "id4", ToolName: "read_file"},
			},
		},
	}

	result := MicroCompact(messages, config)

	// 最近 2 条应保留原始内容
	if result[2].Blocks[0].Output != strings.Repeat("z", 200) {
		t.Errorf("第 3 条消息（近期）应保留原始内容，actual: %s", result[2].Blocks[0].Output)
	}
	if result[3].Blocks[0].Output != strings.Repeat("w", 200) {
		t.Errorf("第 4 条消息（近期）应保留原始内容，actual: %s", result[3].Blocks[0].Output)
	}

	// 旧的超阈值结果应被清除，替换为摘要占位符
	if !strings.Contains(result[0].Blocks[0].Output, "[Old tool result content cleared") {
		t.Errorf("第 1 条消息（旧的超阈值）应被清除，actual: %s", result[0].Blocks[0].Output)
	}
	if !strings.Contains(result[1].Blocks[0].Output, "[Old tool result content cleared") {
		t.Errorf("第 2 条消息（旧的超阈值）应被清除，actual: %s", result[1].Blocks[0].Output)
	}

	// 清除后的占位符should contain字符数和行数信息
	if !strings.Contains(result[0].Blocks[0].Output, "200 chars") {
		t.Errorf("占位符should contain字符数信息，actual: %s", result[0].Blocks[0].Output)
	}
}

// TestMicroCompact_ShortResultPreserved 测试短结果保留
// 验证低于截断阈值的工具结果即使为旧消息也不会被清除
func TestMicroCompact_ShortResultPreserved(t *testing.T) {
	config := MicroCompactConfig{
		PreserveRecent:      0, // 不保留任何近期消息
		TimeThreshold:       0, // 立即过期，所有消息视为旧消息
		TruncationThreshold: 500,
	}

	messages := []session.Message{
		{
			Role:      session.RoleTool,
			Timestamp: time.Now().Add(-1 * time.Hour).UnixMilli(), // 1 小时前，旧消息
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: "short result", ToolUseID: "id1", ToolName: "read_file"},
			},
		},
	}

	result := MicroCompact(messages, config)

	// 短结果低于截断阈值，不应被清除
	if result[0].Blocks[0].Output != "short result" {
		t.Errorf("短结果不应被清除，expected: %q, actual: %q", "short result", result[0].Blocks[0].Output)
	}
}

// TestBuildCompactPromptV2_Structure 测试九段式压缩提示词结构
// 验证生成的提示词包含 <analysis>、<summary> 标签及 9 个子章节
func TestBuildCompactPromptV2_Structure(t *testing.T) {
	messages := []session.Message{
		{
			Role: session.RoleUser,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: "帮我fixed bug"},
			},
		},
		{
			Role: session.RoleAssistant,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: "我来看看问题"},
				{Type: "tool_use", Name: "grep_search", Input: `{"pattern":"bug"}`},
			},
		},
	}

	prompt := BuildCompactPromptV2(messages)

	// 验证 <analysis> 和 </analysis> 标签
	if !strings.Contains(prompt, "<analysis>") {
		t.Error("提示词should contain <analysis> 标签")
	}
	if !strings.Contains(prompt, "</analysis>") {
		t.Error("提示词should contain </analysis> 标签")
	}

	// 验证 <summary> 和 </summary> 标签
	if !strings.Contains(prompt, "<summary>") {
		t.Error("提示词should contain <summary> 标签")
	}
	if !strings.Contains(prompt, "</summary>") {
		t.Error("提示词should contain </summary> 标签")
	}

	// 验证 9 个子章节
	expectedSections := []string{
		"1. main request",
		"2. technical concepts",
		"3. files and code",
		"4. errors and fixes",
		"5. problem resolution",
		"6. all user messages",
		"7. pending tasks",
		"8. current work",
		"9. optional next steps",
	}
	for _, section := range expectedSections {
		if !strings.Contains(prompt, section) {
			t.Errorf("提示词should contain章节 %q", section)
		}
	}

	// 验证对话历史标记
	if !strings.Contains(prompt, "--- Conversation History Start ---") {
		t.Error("提示词should contain对话历史开始标记")
	}
	if !strings.Contains(prompt, "--- Conversation History End ---") {
		t.Error("提示词should contain对话历史结束标记")
	}

	// 验证User消息出现在提示词中
	if !strings.Contains(prompt, "帮我fixed bug") {
		t.Error("提示词should containUser消息内容")
	}

	// 验证工具调用出现在提示词中
	if !strings.Contains(prompt, "grep_search") {
		t.Error("提示词should contain工具调用信息")
	}
}

// TestPostCompactRecovery_RecoverFiles 测试压缩后文件恢复
// 验证 Recover 函数根据 MaxRecoveredFiles 限制恢复文件数量，并生成正确的恢复消息
func TestPostCompactRecovery_RecoverFiles(t *testing.T) {
	dir := t.TempDir()

	// 构造最近读取的文件路径列表
	readFiles := []string{
		dir + "/old_file.go",
		dir + "/main.go",
		dir + "/app.go",
	}

	config := PostCompactRecovery{
		MaxRecoveredFiles: 1,
		MaxFileChars:      100,
		RecoverPlan:       false,
	}

	recovered := Recover(readFiles, nil, config)

	// 应只恢复 1 个文件（MaxRecoveredFiles=1），取列表末尾最近的
	if len(recovered) != 1 {
		t.Fatalf("应只恢复 1 个文件，actual恢复: %d 个", len(recovered))
	}

	// 恢复的消息应为系统角色
	if recovered[0].Role != session.RoleSystem {
		t.Errorf("恢复消息角色应为 system，actual: %s", recovered[0].Role)
	}

	// 恢复的消息should contain最近读取的文件路径（列表末尾的文件）
	if !strings.Contains(recovered[0].Blocks[0].Text, "app.go") {
		t.Errorf("恢复消息should contain最近的文件路径 app.go，actual: %s", recovered[0].Blocks[0].Text)
	}

	// 恢复的消息should not contain旧的文件路径
	if strings.Contains(recovered[0].Blocks[0].Text, "old_file.go") {
		t.Errorf("恢复消息should not contain旧文件路径 old_file.go，actual: %s", recovered[0].Blocks[0].Text)
	}
}

// TestPostCompactRecovery_TruncateLargeFile 测试大文件截断恢复
// 验证当读取文件列表超过 MaxRecoveredFiles 时，只恢复最近读取的文件（截断旧文件）
func TestPostCompactRecovery_TruncateLargeFile(t *testing.T) {
	dir := t.TempDir()

	// 构造大量最近读取的文件路径列表
	var readFiles []string
	for i := 0; i < 10; i++ {
		readFiles = append(readFiles, dir+string(rune('A'+i))+".go")
	}

	config := PostCompactRecovery{
		MaxRecoveredFiles: 3,
		MaxFileChars:      1000,
		RecoverPlan:       false,
	}

	recovered := Recover(readFiles, nil, config)

	// 应只恢复 MaxRecoveredFiles 个文件
	if len(recovered) != 3 {
		t.Fatalf("应只恢复 3 个文件，actual恢复: %d 个", len(recovered))
	}

	// 恢复的文件应为列表末尾的最近文件
	expectedFiles := readFiles[7:] // 最后 3 个文件
	for i, msg := range recovered {
		if !strings.Contains(msg.Blocks[0].Text, expectedFiles[i]) {
			t.Errorf("第 %d 条恢复消息should contain文件路径 %s，actual: %s", i, expectedFiles[i], msg.Blocks[0].Text)
		}
	}

	// should not contain旧文件路径
	for _, msg := range recovered {
		if strings.Contains(msg.Blocks[0].Text, "A.go") || strings.Contains(msg.Blocks[0].Text, "B.go") {
			t.Errorf("恢复消息should not contain旧文件路径，actual: %s", msg.Blocks[0].Text)
		}
	}
}

// TestMicroCompact_Truncation 测试旧工具结果超过 500 字符被截断
// 验证微压缩对超出截断阈值的旧工具结果进行清除替换
func TestMicroCompact_Truncation(t *testing.T) {
	config := MicroCompactConfig{
		PreserveRecent:      0,
		TimeThreshold:       0, // 立即过期，所有消息视为旧消息
		TruncationThreshold: 500,
	}

	// 创建超过 500 字符的旧工具结果
	longOutput := strings.Repeat("x", 600)
	messages := []session.Message{
		{
			Role:      session.RoleTool,
			Timestamp: time.Now().Add(-1 * time.Hour).UnixMilli(),
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: longOutput, ToolUseID: "id1", ToolName: "read_file"},
			},
		},
	}

	result := MicroCompact(messages, config)

	// 旧的超阈值结果应被清除替换
	if !strings.Contains(result[0].Blocks[0].Output, "[Old tool result content cleared") {
		t.Errorf("旧的超阈值工具结果应被清除，actual: %s", result[0].Blocks[0].Output)
	}

	// 替换内容should contain字符数信息
	if !strings.Contains(result[0].Blocks[0].Output, "600 chars") {
		t.Errorf("占位符should contain字符数 '600 chars'，actual: %s", result[0].Blocks[0].Output)
	}
}

// TestMicroCompact_PreserveRecent 测试最近 5 条工具结果不被截断
// 验证 PreserveRecent 配置能保护近期的工具结果不被清除
func TestMicroCompact_PreserveRecent(t *testing.T) {
	config := MicroCompactConfig{
		PreserveRecent:      5,
		TimeThreshold:       1 * time.Minute,
		TruncationThreshold: 100,
	}

	// 创建 7 条近期工具结果（超过截断阈值）
	now := time.Now()
	messages := make([]session.Message, 7)
	for i := range messages {
		messages[i] = session.Message{
			Role:      session.RoleTool,
			Timestamp: now.UnixMilli(),
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: strings.Repeat("x", 200), ToolUseID: fmt.Sprintf("id%d", i), ToolName: "read_file"},
			},
		}
	}

	result := MicroCompact(messages, config)

	// 最近 5 条应保留原始内容（从最新往前数 5 条）
	preservedCount := 0
	clearedCount := 0
	for i := len(result) - 1; i >= 0; i-- {
		output := result[i].Blocks[0].Output
		if strings.Contains(output, "[Old tool result content cleared") {
			clearedCount++
		} else {
			preservedCount++
		}
	}

	// 近期消息应至少保留 5 条
	if preservedCount < 5 {
		t.Errorf("应至少保留 5 条近期工具结果，actual保留 %d 条", preservedCount)
	}
}

// TestBuildCompactPromptV2 测试九段式结构化提示词
// 验证 BuildCompactPromptV2 生成包含 <analysis> 和 9 个子章节 <summary> 的提示词
func TestBuildCompactPromptV2(t *testing.T) {
	messages := []session.Message{
		{
			Role: session.RoleUser,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: "请帮我fixed登录 bug"},
			},
		},
		{
			Role: session.RoleAssistant,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: "我来检查 auth.go 中的问题"},
				{Type: "tool_use", Name: "read_file", Input: `{"path":"auth.go"}`},
			},
		},
	}

	prompt := BuildCompactPromptV2(messages)

	// 验证 <analysis> 标签
	if !strings.Contains(prompt, "<analysis>") || !strings.Contains(prompt, "</analysis>") {
		t.Error("提示词should contain <analysis> 和 </analysis> 标签")
	}

	// 验证 <summary> 标签
	if !strings.Contains(prompt, "<summary>") || !strings.Contains(prompt, "</summary>") {
		t.Error("提示词should contain <summary> 和 </summary> 标签")
	}

	// 验证 9 个子章节
	sections := []string{
		"1. main request", "2. technical concepts", "3. files and code",
		"4. errors and fixes", "5. problem resolution", "6. all user messages",
		"7. pending tasks", "8. current work", "9. optional next steps",
	}
	for _, section := range sections {
		if !strings.Contains(prompt, section) {
			t.Errorf("提示词should contain章节 %q", section)
		}
	}

	// 验证对话历史标记
	if !strings.Contains(prompt, "--- Conversation History Start ---") {
		t.Error("提示词should contain对话历史开始标记")
	}
}

// === N11 修复测试 ===

// TestMicroCompact_TimestampZeroTruncated 验证 Timestamp==0 的 tool_result 被截断（N11 修复 G4，P0-8）。
// 修复前：msgTime==0 被视为"近期"（保留），导致无时间戳的旧消息被错误保留。
// 修复后：msgTime==0 视为远古（截断），与 02 实施计划 P0-8 契约一致。
func TestMicroCompact_TimestampZeroTruncated(t *testing.T) {
	config := MicroCompactConfig{
		PreserveRecent:      1, // 仅保留最近 1 条
		TimeThreshold:       30 * time.Minute,
		TruncationThreshold: 10, // 极小阈值，任何 tool_result 都超阈值
	}

	// 构造：3 条 tool_result，前 2 条 Timestamp=0（远古），最后 1 条近期
	messages := []session.Message{
		{
			Role:      session.RoleTool,
			Timestamp: 0, // G4 修复：应视为远古
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: strings.Repeat("a", 100), ToolUseID: "id1"},
			},
		},
		{
			Role:      session.RoleTool,
			Timestamp: 0, // G4 修复：应视为远古
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: strings.Repeat("b", 100), ToolUseID: "id2"},
			},
		},
		{
			Role:      session.RoleTool,
			Timestamp: time.Now().UnixMilli(), // 近期
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: strings.Repeat("c", 100), ToolUseID: "id3"},
			},
		},
	}

	result := MicroCompact(messages, config)

	// 第 1 条（Timestamp=0，远古，超出 preserve 范围）应被截断
	if !strings.Contains(result[0].Blocks[0].Output, "[Old tool result content cleared") {
		t.Errorf("G4: Timestamp=0 的 tool_result 应被截断，actual output: %q",
			result[0].Blocks[0].Output)
	}
	// 第 2 条（Timestamp=0，远古，但可能被 preserveRecent 保留）
	// preserveRecent=1，从最新开始计数，最后 1 条保留，前 2 条截断
	if !strings.Contains(result[1].Blocks[0].Output, "[Old tool result content cleared") {
		t.Errorf("G4: Timestamp=0 的第 2 条 tool_result 应被截断，actual output: %q",
			result[1].Blocks[0].Output)
	}
	// 第 3 条（近期）应保留原始内容
	if strings.Contains(result[2].Blocks[0].Output, "[Old tool result content cleared") {
		t.Error("G4: 近期 tool_result 不应被截断")
	}
}

// TestMicroCompact_TimestampZeroNotTreatedAsRecent 验证 Timestamp==0 不被视为近期。
// 这是 G4 修复的核心断言：isRecent = msgTime != 0 && within threshold。
func TestMicroCompact_TimestampZeroNotTreatedAsRecent(t *testing.T) {
	config := MicroCompactConfig{
		PreserveRecent:      5, // 保留 5 条，足够覆盖所有消息
		TimeThreshold:       30 * time.Minute,
		TruncationThreshold: 10,
	}

	messages := []session.Message{
		{
			Role:      session.RoleTool,
			Timestamp: 0, // G4：应视为远古，不应被 isRecent 判定为近期
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: strings.Repeat("x", 100), ToolUseID: "id1"},
			},
		},
	}

	result := MicroCompact(messages, config)

	// 即使 PreserveRecent=5（足够保留），Timestamp=0 的消息仍应被截断
	// 因为 isRecent=false（G4 修复），但 preservedCount < PreserveRecent 时仍保留
	// 关键：isRecent=false 时，仅靠 preservedCount 保留，不靠 isRecent
	// 当 PreserveRecent 足够大时，消息被保留（preservedCount < PreserveRecent）
	// 但 isRecent=false 意味着不因"近期"而保留
	// 验证：消息被保留（因为 preservedCount=0 < 5），但 isRecent=false
	// 这通过行为间接验证：当 PreserveRecent=0 时，Timestamp=0 的消息应被截断
	config.PreserveRecent = 0
	result = MicroCompact(messages, config)
	if !strings.Contains(result[0].Blocks[0].Output, "[Old tool result content cleared") {
		t.Errorf("G4: PreserveRecent=0 且 Timestamp=0 时应截断，actual: %q",
			result[0].Blocks[0].Output)
	}
}

// TestMaybeTimeBasedMicroCompact_Triggered 验证 time-based 微压缩触发
// （N11 新增，借鉴同类 CLI）。
// 触发条件：距上次 assistant 消息超过 GapThreshold（默认 60min）。
func TestMaybeTimeBasedMicroCompact_Triggered(t *testing.T) {
	config := TimeBasedMicroCompactConfig{
		Enabled:             true,
		GapThreshold:        60 * time.Minute,
		KeepRecent:          1,  // 仅保留最近 1 条 tool_result
		TruncationThreshold: 10, // 极小阈值，任何 tool_result 都超阈值
	}

	// 构造：2 小时前的 assistant 消息 + 多条旧 tool_result
	// 需要 > KeepRecent 条 tool_result，否则全部被 PreserveRecent 保留
	oldTime := time.Now().Add(-2 * time.Hour).UnixMilli()
	messages := []session.Message{
		{
			Role:      session.RoleAssistant,
			Timestamp: oldTime,
			Blocks:    []session.ContentBlock{{Type: "text", Text: "old assistant response"}},
		},
		{
			Role:      session.RoleTool,
			Timestamp: oldTime + 1000, // 2 小前，超过 60min 阈值
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: strings.Repeat("old1", 50), ToolUseID: "id1"},
			},
		},
		{
			Role:      session.RoleTool,
			Timestamp: oldTime + 2000, // 2 小时前，超过 60min 阈值
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: strings.Repeat("old2", 50), ToolUseID: "id2"},
			},
		},
		{
			Role:      session.RoleTool,
			Timestamp: oldTime + 3000, // 2 小时前，超过 60min 阈值
			Blocks: []session.ContentBlock{
				{Type: "tool_result", Output: strings.Repeat("old3", 50), ToolUseID: "id3"},
			},
		},
	}

	compacted, triggered := MaybeTimeBasedMicroCompact(messages, config)
	if !triggered {
		t.Error("time-based 微压缩应触发（距上次 assistant > 60min）")
	}
	// 验证至少有一条旧 tool_result 被截断（KeepRecent=1 保留最近 1 条，前 2 条截断）
	truncatedCount := 0
	for i, msg := range compacted {
		if msg.Role == session.RoleTool {
			for _, block := range msg.Blocks {
				if strings.Contains(block.Output, "[Old tool result content cleared") {
					truncatedCount++
				}
			}
		} else {
			_ = i
		}
	}
	if truncatedCount == 0 {
		t.Errorf("time-based 触发后应至少有 1 条 tool_result 被截断，actual truncated: %d", truncatedCount)
	}
}

// TestMaybeTimeBasedMicroCompact_NotTriggered 验证不触发条件。
func TestMaybeTimeBasedMicroCompact_NotTriggered(t *testing.T) {
	config := DefaultTimeBasedMicroCompactConfig()

	// 场景 1：距上次 assistant 消息未超过阈值（5 分钟前）
	recentTime := time.Now().Add(-5 * time.Minute).UnixMilli()
	messages := []session.Message{
		{
			Role:      session.RoleAssistant,
			Timestamp: recentTime,
			Blocks:    []session.ContentBlock{{Type: "text", Text: "recent"}},
		},
	}
	_, triggered := MaybeTimeBasedMicroCompact(messages, config)
	if triggered {
		t.Error("距上次 assistant 仅 5min，不应触发 time-based 微压缩")
	}

	// 场景 2：config.Enabled=false
	config.Enabled = false
	messages2 := []session.Message{
		{
			Role:      session.RoleAssistant,
			Timestamp: time.Now().Add(-2 * time.Hour).UnixMilli(),
			Blocks:    []session.ContentBlock{{Type: "text", Text: "old"}},
		},
	}
	_, triggered2 := MaybeTimeBasedMicroCompact(messages2, config)
	if triggered2 {
		t.Error("Enabled=false 时不应触发")
	}

	// 场景 3：空消息列表
	config.Enabled = true
	_, triggered3 := MaybeTimeBasedMicroCompact([]session.Message{}, config)
	if triggered3 {
		t.Error("空消息列表不应触发")
	}

	// 场景 4：无 assistant 消息（只有 user）
	messages4 := []session.Message{
		{
			Role:      session.RoleUser,
			Timestamp: time.Now().Add(-2 * time.Hour).UnixMilli(),
			Blocks:    []session.ContentBlock{{Type: "text", Text: "user msg"}},
		},
	}
	_, triggered4 := MaybeTimeBasedMicroCompact(messages4, config)
	if triggered4 {
		t.Error("无 assistant 消息不应触发")
	}

	// 场景 5：assistant 消息 Timestamp=0
	messages5 := []session.Message{
		{
			Role:      session.RoleAssistant,
			Timestamp: 0, // 零值
			Blocks:    []session.ContentBlock{{Type: "text", Text: "no timestamp"}},
		},
	}
	_, triggered5 := MaybeTimeBasedMicroCompact(messages5, config)
	if triggered5 {
		t.Error("assistant Timestamp=0 不应触发")
	}
}

// TestDefaultTimeBasedMicroCompactConfig 验证默认配置值。
func TestDefaultTimeBasedMicroCompactConfig(t *testing.T) {
	cfg := DefaultTimeBasedMicroCompactConfig()
	if !cfg.Enabled {
		t.Error("默认 Enabled 应为 true")
	}
	if cfg.GapThreshold != 60*time.Minute {
		t.Errorf("默认 GapThreshold = %v, want 60min", cfg.GapThreshold)
	}
	if cfg.KeepRecent != 5 {
		t.Errorf("默认 KeepRecent = %d, want 5", cfg.KeepRecent)
	}
	if cfg.TruncationThreshold != 500 {
		t.Errorf("默认 TruncationThreshold = %d, want 500", cfg.TruncationThreshold)
	}
}

// TestPostCompactRecovery 测试压缩后恢复最近文件内容和计划
// 验证 Recover 函数能恢复最近读取的文件内容并提取待处理计划
func TestPostCompactRecovery(t *testing.T) {
	dir := t.TempDir()

	readFiles := []string{
		dir + "/config.go",
		dir + "/handler.go",
		dir + "/main.go",
	}

	// 创建包含待办事项的会话
	sess := session.NewSession()
	sess.SetMessages([]session.Message{
		{
			Role: session.RoleAssistant,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: "TODO: 添加错误处理\nFIXME: fixed空指针问题\n下一步: implementationunit test"},
			},
		},
	})

	config := PostCompactRecovery{
		MaxRecoveredFiles: 2,
		MaxFileChars:      1000,
		RecoverPlan:       true,
	}

	recovered := Recover(readFiles, sess, config)

	// 应恢复 2 个文件 + 1 条计划消息
	if len(recovered) != 3 {
		t.Fatalf("应恢复 3 条消息（2 文件 + 1 计划），actual %d 条", len(recovered))
	}

	// 验证文件恢复消息
	hasFileRecovery := false
	hasPlanRecovery := false
	for _, msg := range recovered {
		text := msg.Blocks[0].Text
		if strings.Contains(text, "Recovered file") {
			hasFileRecovery = true
		}
		if strings.Contains(text, "Recovered pending plan") {
			hasPlanRecovery = true
		}
	}

	if !hasFileRecovery {
		t.Error("should contain文件恢复消息")
	}
	if !hasPlanRecovery {
		t.Error("should contain计划恢复消息")
	}
}
