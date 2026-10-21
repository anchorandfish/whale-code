package session

import (
	"testing"
)

// TestSplitAt 测试按消息索引拆分会话
func TestSplitAt(t *testing.T) {
	// 构造一个包含 6 条消息的会话
	sess := NewSession()
	sess.Version = 1
	sess.Metadata = SessionMetadata{
		Name:      "original",
		CreatedAt: 1000,
	}
	sess.SetMessages([]Message{
		UserText("msg0"),                // 0
		AssistantText("msg1", "m", nil), // 1
		UserText("msg2"),                // 2
		AssistantText("msg3", "m", nil), // 3
		UserText("msg4"),                // 4
		AssistantText("msg5", "m", nil), // 5
	})

	// 在索引 2 和 4 处拆分，得到 3 个子会话
	result := SplitAt(sess, []int{2, 4})

	if len(result.Sessions) != 3 {
		t.Fatalf("拆分后会话数 = %d, expected 3", len(result.Sessions))
	}
	if len(result.Names) != 3 {
		t.Fatalf("拆分后名称数 = %d, expected 3", len(result.Names))
	}

	// 验证第一部分: 消息 0-1
	part1 := result.Sessions[0]
	if len(part1.messages) != 2 {
		t.Errorf("第一部分消息数 = %d, expected 2", len(part1.messages))
	}
	if part1.messages[0].TextContent() != "msg0" {
		t.Errorf("第一部分首条消息 = %s, expected msg0", part1.messages[0].TextContent())
	}
	if part1.Metadata.Name != "original-part1" {
		t.Errorf("第一部分名称 = %s, expected original-part1", part1.Metadata.Name)
	}
	// 第一部分包含 1 条User消息 (msg0)
	if part1.Metadata.TurnCount != 1 {
		t.Errorf("第一部分 TurnCount = %d, expected 1", part1.Metadata.TurnCount)
	}

	// 验证第二部分: 消息 2-3
	part2 := result.Sessions[1]
	if len(part2.messages) != 2 {
		t.Errorf("第二部分消息数 = %d, expected 2", len(part2.messages))
	}
	if part2.messages[0].TextContent() != "msg2" {
		t.Errorf("第二部分首条消息 = %s, expected msg2", part2.messages[0].TextContent())
	}
	if part2.Metadata.Name != "original-part2" {
		t.Errorf("第二部分名称 = %s, expected original-part2", part2.Metadata.Name)
	}
	// 第二部分包含 1 条User消息 (msg2)
	if part2.Metadata.TurnCount != 1 {
		t.Errorf("第二部分 TurnCount = %d, expected 1", part2.Metadata.TurnCount)
	}

	// 验证第三部分: 消息 4-5
	part3 := result.Sessions[2]
	if len(part3.messages) != 2 {
		t.Errorf("第三部分消息数 = %d, expected 2", len(part3.messages))
	}
	if part3.messages[0].TextContent() != "msg4" {
		t.Errorf("第三部分首条消息 = %s, expected msg4", part3.messages[0].TextContent())
	}
	if part3.Metadata.Name != "original-part3" {
		t.Errorf("第三部分名称 = %s, expected original-part3", part3.Metadata.Name)
	}

	// 验证所有部分共享原始 CreatedAt
	if part1.Metadata.CreatedAt != 1000 || part2.Metadata.CreatedAt != 1000 || part3.Metadata.CreatedAt != 1000 {
		t.Error("所有子会话应inheritance原始 CreatedAt")
	}

	// 验证版本号一致
	if part1.Version != 1 || part2.Version != 1 || part3.Version != 1 {
		t.Error("所有子会话应inheritance原始 Version")
	}
}

// TestSplitAt_SingleIndex 测试单个拆分索引
func TestSplitAt_SingleIndex(t *testing.T) {
	sess := NewSession()
	sess.Version = 1
	sess.Metadata = SessionMetadata{
		Name:      "single-split",
		CreatedAt: 2000,
	}
	sess.SetMessages([]Message{
		UserText("a"),
		AssistantText("b", "m", nil),
		UserText("c"),
		AssistantText("d", "m", nil),
	})

	result := SplitAt(sess, []int{2})
	if len(result.Sessions) != 2 {
		t.Fatalf("拆分后会话数 = %d, expected 2", len(result.Sessions))
	}

	// 第一部分: 消息 0-1
	if len(result.Sessions[0].messages) != 2 {
		t.Errorf("第一部分消息数 = %d, expected 2", len(result.Sessions[0].messages))
	}
	// 第二部分: 消息 2-3
	if len(result.Sessions[1].messages) != 2 {
		t.Errorf("第二部分消息数 = %d, expected 2", len(result.Sessions[1].messages))
	}
}

// TestSplitAt_EmptyIndices 测试空索引列表（should return原始会话）
func TestSplitAt_EmptyIndices(t *testing.T) {
	sess := NewSession()
	sess.Version = 1
	sess.Metadata = SessionMetadata{
		Name: "no-split",
	}
	sess.SetMessages([]Message{
		UserText("only-msg"),
	})

	result := SplitAt(sess, []int{})
	if len(result.Sessions) != 1 {
		t.Fatalf("空索引拆分后会话数 = %d, expected 1", len(result.Sessions))
	}
	if len(result.Sessions[0].messages) != 1 {
		t.Errorf("消息数 = %d, expected 1", len(result.Sessions[0].messages))
	}
	if result.Names[0] != "no-split" {
		t.Errorf("名称 = %s, expected no-split", result.Names[0])
	}
}

// TestSplitAt_InvalidIndex 测试无效索引的处理
func TestSplitAt_InvalidIndex(t *testing.T) {
	sess := NewSession()
	sess.Version = 1
	sess.Metadata = SessionMetadata{
		Name:      "invalid-idx",
		CreatedAt: 3000,
	}
	sess.SetMessages([]Message{
		UserText("msg0"),
		AssistantText("msg1", "m", nil),
		UserText("msg2"),
	})

	tests := []struct {
		name      string
		indices   []int
		wantParts int
	}{
		{
			name:      "索引0应被忽略（idx <= start）",
			indices:   []int{0},
			wantParts: 1, // 0 被跳过，剩余部分为整个会话
		},
		{
			name:      "负数索引应被忽略",
			indices:   []int{-1},
			wantParts: 1,
		},
		{
			name:      "索引等于消息长度应被忽略",
			indices:   []int{3},
			wantParts: 1, // idx >= len(Messages) 被跳过
		},
		{
			name:      "索引超出scope应被忽略",
			indices:   []int{100},
			wantParts: 1,
		},
		{
			name:      "混合有效和无效索引",
			indices:   []int{-1, 2, 100},
			wantParts: 2, // 只有索引 2 有效
		},
		{
			name:      "索引1等于start时应被忽略",
			indices:   []int{1, 1},
			wantParts: 2, // 第一个1有效，第二个1等于start被跳过
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SplitAt(sess, tt.indices)
			if len(result.Sessions) != tt.wantParts {
				t.Errorf("拆分后会话数 = %d, expected %d (indices=%v)",
					len(result.Sessions), tt.wantParts, tt.indices)
			}
		})
	}
}

// TestMerge 测试合并多个会话
func TestMerge(t *testing.T) {
	sess1 := NewSession()
	sess1.Version = 1
	sess1.Metadata = SessionMetadata{
		Name:      "part1",
		CreatedAt: 1000,
		UpdatedAt: 2000,
		TurnCount: 2,
	}
	sess1.SetMessages([]Message{
		{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "UserA"}}, Timestamp: 1000},
		{Role: RoleAssistant, Blocks: []ContentBlock{{Type: "text", Text: "AssistantA"}}, Timestamp: 1500},
	})

	sess2 := NewSession()
	sess2.Version = 1
	sess2.Metadata = SessionMetadata{
		Name:      "part2",
		CreatedAt: 3000,
		UpdatedAt: 4000,
		TurnCount: 1,
	}
	sess2.SetMessages([]Message{
		{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "UserB"}}, Timestamp: 3000},
		{Role: RoleAssistant, Blocks: []ContentBlock{{Type: "text", Text: "AssistantB"}}, Timestamp: 3500},
	})

	sess3 := NewSession()
	sess3.Version = 1
	sess3.Metadata = SessionMetadata{
		Name:      "part3",
		CreatedAt: 5000,
		UpdatedAt: 6000,
		TurnCount: 1,
	}
	sess3.SetMessages([]Message{
		{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "UserC"}}, Timestamp: 5000},
		{Role: RoleAssistant, Blocks: []ContentBlock{{Type: "text", Text: "AssistantC"}}, Timestamp: 5500},
	})

	merged := Merge([]*Session{sess1, sess2, sess3}, "merged-session")

	// 验证名称
	if merged.Metadata.Name != "merged-session" {
		t.Errorf("合并后名称 = %s, expected merged-session", merged.Metadata.Name)
	}

	// 验证消息数量
	if len(merged.messages) != 6 {
		t.Errorf("合并后消息数 = %d, expected 6", len(merged.messages))
	}

	// 验证消息按时间顺序排列
	if merged.messages[0].TextContent() != "UserA" {
		t.Errorf("第一条消息 = %s, expected UserA", merged.messages[0].TextContent())
	}
	if merged.messages[5].TextContent() != "AssistantC" {
		t.Errorf("最后一条消息 = %s, expected AssistantC", merged.messages[5].TextContent())
	}

	// 验证轮次累加
	if merged.Metadata.TurnCount != 4 {
		t.Errorf("合并后 TurnCount = %d, expected 4", merged.Metadata.TurnCount)
	}

	// 验证时间戳取首尾消息
	if merged.Metadata.CreatedAt != 1000 {
		t.Errorf("合并后 CreatedAt = %d, expected 1000", merged.Metadata.CreatedAt)
	}
	if merged.Metadata.UpdatedAt != 5500 {
		t.Errorf("合并后 UpdatedAt = %d, expected 5500", merged.Metadata.UpdatedAt)
	}
}

func TestSplitAndMergeCompactionCountSemantics(t *testing.T) {
	source := NewSession()
	source.Metadata.Name = "source"
	source.Metadata.CompactionCount = 2
	source.SetMessages([]Message{UserText("a"), AssistantText("a", "m", nil), UserText("b"), AssistantText("b", "m", nil)})

	parts := SplitAt(source, []int{2})
	if len(parts.Sessions) != 2 {
		t.Fatalf("parts=%d, want 2", len(parts.Sessions))
	}
	for i, part := range parts.Sessions {
		if part.Metadata.CompactionCount != 2 {
			t.Fatalf("part %d CompactionCount=%d, want inherited 2", i, part.Metadata.CompactionCount)
		}
	}

	merged := Merge(parts.Sessions, "merged")
	if merged.Metadata.CompactionCount != 4 {
		t.Fatalf("merged CompactionCount=%d, want sum 4", merged.Metadata.CompactionCount)
	}
}

// TestMerge_TwoSessions 测试合并两个会话
func TestMerge_TwoSessions(t *testing.T) {
	sess1 := NewSession()
	sess1.Version = 1
	sess1.Metadata = SessionMetadata{
		Name:      "a",
		TurnCount: 3,
	}
	sess1.SetMessages([]Message{
		{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "x"}}, Timestamp: 100},
	})

	sess2 := NewSession()
	sess2.Version = 1
	sess2.Metadata = SessionMetadata{
		Name:      "b",
		TurnCount: 2,
	}
	sess2.SetMessages([]Message{
		{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "y"}}, Timestamp: 200},
	})

	merged := Merge([]*Session{sess1, sess2}, "two-merged")

	if len(merged.messages) != 2 {
		t.Errorf("合并后消息数 = %d, expected 2", len(merged.messages))
	}
	if merged.Metadata.TurnCount != 5 {
		t.Errorf("合并后 TurnCount = %d, expected 5", merged.Metadata.TurnCount)
	}
}

// TestMerge_EmptySessions 测试合并空会话列表
func TestMerge_EmptySessions(t *testing.T) {
	merged := Merge([]*Session{}, "empty-merge")

	if merged.Metadata.Name != "empty-merge" {
		t.Errorf("合并后名称 = %s, expected empty-merge", merged.Metadata.Name)
	}
	if len(merged.messages) != 0 {
		t.Errorf("合并空列表后消息数 = %d, expected 0", len(merged.messages))
	}
}

// TestSplitAndMergeRoundTrip 测试拆分后再合并的往返consistency
func TestSplitAndMergeRoundTrip(t *testing.T) {
	original := NewSession()
	original.Version = 1
	original.Metadata = SessionMetadata{
		Name:      "roundtrip",
		CreatedAt: 5000,
	}
	original.SetMessages([]Message{
		{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "m0"}}, Timestamp: 5000},
		{Role: RoleAssistant, Blocks: []ContentBlock{{Type: "text", Text: "m1"}}, Timestamp: 5100},
		{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "m2"}}, Timestamp: 5200},
		{Role: RoleAssistant, Blocks: []ContentBlock{{Type: "text", Text: "m3"}}, Timestamp: 5300},
		{Role: RoleUser, Blocks: []ContentBlock{{Type: "text", Text: "m4"}}, Timestamp: 5400},
		{Role: RoleAssistant, Blocks: []ContentBlock{{Type: "text", Text: "m5"}}, Timestamp: 5500},
	})

	// 拆分
	result := SplitAt(original, []int{2, 4})

	// 合并回来
	merged := Merge(result.Sessions, "roundtrip")

	// 验证消息数量一致
	if len(merged.messages) != len(original.messages) {
		t.Errorf("往返后消息数 = %d, expected %d", len(merged.messages), len(original.messages))
	}

	// 验证消息内容顺序一致
	for i, msg := range merged.messages {
		if msg.TextContent() != original.messages[i].TextContent() {
			t.Errorf("消息[%d] 内容 = %s, expected %s", i, msg.TextContent(), original.messages[i].TextContent())
		}
	}

	// 验证时间戳一致
	if merged.Metadata.CreatedAt != original.messages[0].Timestamp {
		t.Errorf("往返后 CreatedAt = %d, expected %d", merged.Metadata.CreatedAt, original.messages[0].Timestamp)
	}
	if merged.Metadata.UpdatedAt != original.messages[len(original.messages)-1].Timestamp {
		t.Errorf("往返后 UpdatedAt = %d, expected %d", merged.Metadata.UpdatedAt, original.messages[len(original.messages)-1].Timestamp)
	}
}
