package context

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// TestCollapseCommitLog_Commit 测试折叠提交日志的 Commit 方法
func TestCollapseCommitLog_Commit(t *testing.T) {
	log := NewCollapseCommitLog()

	// 初始状态should be empty
	if log.Len() != 0 {
		t.Fatalf("expected 0 commits, got %d", log.Len())
	}

	// 提交一条记录
	_, err := log.Commit(CollapseCommit{
		Summary:    "折叠了5条消息",
		StartIndex: 0,
		EndIndex:   5,
	}, 5)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if log.StagedLen() != 1 {
		t.Fatalf("expected 1 staged commit, got %d", log.StagedLen())
	}

	// 再提交一条
	_, err = log.Commit(CollapseCommit{
		Summary:    "折叠了3条消息",
		StartIndex: 5,
		EndIndex:   8,
	}, 5)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if log.StagedLen() != 2 {
		t.Fatalf("expected 2 staged commits, got %d", log.StagedLen())
	}
}

// TestCollapseCommitLog_ProjectView 测试折叠提交日志的 ProjectView 方法
func TestCollapseCommitLog_ProjectView(t *testing.T) {
	messages := []session.Message{
		session.UserText("消息1"),
		session.UserText("消息2"),
		session.UserText("消息3"),
		session.UserText("消息4"),
		session.UserText("消息5"),
	}

	t.Run("无提交时返回原始消息", func(t *testing.T) {
		log := NewCollapseCommitLog()
		result := log.ProjectView(messages)
		if len(result) != len(messages) {
			t.Fatalf("expected %d messages, got %d", len(messages), len(result))
		}
	})

	t.Run("有提交时生成投影视图", func(t *testing.T) {
		log := NewCollapseCommitLog()
		_, err := log.Commit(CollapseCommit{
			StartIndex: 0,
			EndIndex:   2,
			Summary:    "折叠了2条消息",
		}, 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		result := log.ProjectView(messages)
		// should contain1条摘要 + 3条保留消息（消息3、4、5）
		if len(result) != 4 {
			t.Fatalf("expected 4 messages (1 summary + 3 preserved), got %d", len(result))
		}
		// 第一条应为系统摘要消息
		if result[0].Role != session.RoleSystem {
			t.Fatalf("expected first message role to be system, got %s", result[0].Role)
		}
	})

	t.Run("不修改原始消息数组", func(t *testing.T) {
		log := NewCollapseCommitLog()
		_, _ = log.Commit(CollapseCommit{
			StartIndex: 0,
			EndIndex:   2,
			Summary:    "折叠了2条消息",
		}, 5)

		originalLen := len(messages)
		log.ProjectView(messages)
		if len(messages) != originalLen {
			t.Fatalf("original messages were modified: expected %d, got %d", originalLen, len(messages))
		}
	})

	t.Run("折叠数量覆盖全部消息", func(t *testing.T) {
		log := NewCollapseCommitLog()
		_, _ = log.Commit(CollapseCommit{
			StartIndex: 0,
			EndIndex:   5,
			Summary:    "折叠了所有消息",
		}, 5)

		result := log.ProjectView(messages)
		// 应仅返回摘要消息（没有未折叠的消息）
		if len(result) != 1 {
			t.Fatalf("expected 1 summary message, got %d", len(result))
		}
		if result[0].Role != session.RoleSystem {
			t.Fatalf("expected system message, got %s", result[0].Role)
		}
	})
}

// TestCollapseCommitLog_Drain 测试折叠提交日志的 Drain 方法
func TestCollapseCommitLog_Drain(t *testing.T) {
	log := NewCollapseCommitLog()
	_, _ = log.Commit(CollapseCommit{Summary: "提交1", StartIndex: 0, EndIndex: 1}, 5)
	_, _ = log.Commit(CollapseCommit{Summary: "提交2", StartIndex: 1, EndIndex: 2}, 5)
	_, _ = log.Commit(CollapseCommit{Summary: "提交3", StartIndex: 2, EndIndex: 3}, 5)

	t.Run("排空所有暂存提交", func(t *testing.T) {
		count := log.Drain()
		if count != 3 {
			t.Fatalf("expected 3 drained, got %d", count)
		}
		if log.StagedLen() != 0 {
			t.Fatalf("expected 0 staged, got %d", log.StagedLen())
		}
		if log.AppliedLen() != 3 {
			t.Fatalf("expected 3 applied, got %d", log.AppliedLen())
		}
	})
}

// TestCollapseCommitLog_DrainCommits 测试按数量排空暂存提交
func TestCollapseCommitLog_DrainCommits(t *testing.T) {
	log := NewCollapseCommitLog()
	_, _ = log.Commit(CollapseCommit{Summary: "提交1", StartIndex: 0, EndIndex: 1}, 5)
	_, _ = log.Commit(CollapseCommit{Summary: "提交2", StartIndex: 1, EndIndex: 2}, 5)
	_, _ = log.Commit(CollapseCommit{Summary: "提交3", StartIndex: 2, EndIndex: 3}, 5)

	t.Run("排空指定数量的提交", func(t *testing.T) {
		drained := log.DrainCommits(2)
		if len(drained) != 2 {
			t.Fatalf("expected 2 drained commits, got %d", len(drained))
		}
		if drained[0].Summary != "提交1" {
			t.Fatalf("expected first drained commit to be '提交1', got %s", drained[0].Summary)
		}
		if drained[1].Summary != "提交2" {
			t.Fatalf("expected second drained commit to be '提交2', got %s", drained[1].Summary)
		}
		// 暂存区应剩1条，已应用区应有2条
		if log.StagedLen() != 1 {
			t.Fatalf("expected 1 staged, got %d", log.StagedLen())
		}
		if log.AppliedLen() != 2 {
			t.Fatalf("expected 2 applied, got %d", log.AppliedLen())
		}
	})

	t.Run("排空数量超过暂存区长度", func(t *testing.T) {
		drained := log.DrainCommits(10)
		if len(drained) != 1 {
			t.Fatalf("expected 1 drained commit, got %d", len(drained))
		}
		if log.StagedLen() != 0 {
			t.Fatalf("expected 0 staged, got %d", log.StagedLen())
		}
	})

	t.Run("排空空日志", func(t *testing.T) {
		emptyLog := NewCollapseCommitLog()
		drained := emptyLog.DrainCommits(5)
		if drained != nil {
			t.Fatalf("expected nil for empty log drain, got %v", drained)
		}
	})

	t.Run("排空0条", func(t *testing.T) {
		singleLog := NewCollapseCommitLog()
		_, _ = singleLog.Commit(CollapseCommit{Summary: "提交", StartIndex: 0, EndIndex: 1}, 5)
		drained := singleLog.DrainCommits(0)
		if drained != nil {
			t.Fatalf("expected nil for drain(0), got %v", drained)
		}
	})
}

// TestCollapseCommitLog_Reset 测试折叠提交日志的 Reset 方法
func TestCollapseCommitLog_Reset(t *testing.T) {
	log := NewCollapseCommitLog()
	_, _ = log.Commit(CollapseCommit{Summary: "提交1", StartIndex: 0, EndIndex: 1}, 5)
	_, _ = log.Commit(CollapseCommit{Summary: "提交2", StartIndex: 1, EndIndex: 2}, 5)
	log.Drain() // 移到 applied

	if log.Len() != 2 {
		t.Fatalf("expected 2 commits before reset, got %d", log.Len())
	}

	log.Reset()

	if log.Len() != 0 {
		t.Fatalf("expected 0 commits after reset, got %d", log.Len())
	}
}

// TestContextCollapseManager_CheckCollapsePhase 测试折叠管理器的阶段判断
func TestContextCollapseManager_CheckCollapsePhase(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	budget := DefaultBudget(100000)
	config := DefaultCollapseConfig()
	mgr := NewContextCollapseManager(config, estimator, budget)

	t.Run("使用率低于90%应为Idle阶段", func(t *testing.T) {
		phase := mgr.CheckCollapsePhase(0.85)
		if phase != CollapsePhaseIdle {
			t.Fatalf("expected CollapsePhaseIdle at 85%%, got %v", phase)
		}
	})

	t.Run("使用率达到90%应为Staging阶段", func(t *testing.T) {
		phase := mgr.CheckCollapsePhase(0.90)
		if phase != CollapsePhaseStaging {
			t.Fatalf("expected CollapsePhaseStaging at 90%%, got %v", phase)
		}
	})

	t.Run("使用率在90%-95%之间应为Staging阶段", func(t *testing.T) {
		phase := mgr.CheckCollapsePhase(0.92)
		if phase != CollapsePhaseStaging {
			t.Fatalf("expected CollapsePhaseStaging at 92%%, got %v", phase)
		}
	})

	t.Run("使用率达到95%应为Blocking阶段", func(t *testing.T) {
		phase := mgr.CheckCollapsePhase(0.95)
		if phase != CollapsePhaseBlocking {
			t.Fatalf("expected CollapsePhaseBlocking at 95%%, got %v", phase)
		}
	})

	t.Run("使用率超过95%应为Blocking阶段", func(t *testing.T) {
		phase := mgr.CheckCollapsePhase(0.98)
		if phase != CollapsePhaseBlocking {
			t.Fatalf("expected CollapsePhaseBlocking at 98%%, got %v", phase)
		}
	})
}

// TestContextCollapseManager_StageCollapse 测试折叠管理器的暂存折叠
func TestContextCollapseManager_StageCollapse(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	budget := DefaultBudget(100000)
	config := DefaultCollapseConfig()
	mgr := NewContextCollapseManager(config, estimator, budget)

	messages := make([]session.Message, 20)
	for i := range messages {
		messages[i] = session.UserText("消息内容")
	}

	t.Run("成功暂存折叠", func(t *testing.T) {
		err := mgr.StageCollapse(messages)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mgr.GetCommitLog().StagedLen() != 1 {
			t.Fatalf("expected 1 staged commit, got %d", mgr.GetCommitLog().StagedLen())
		}
	})

	t.Run("消息数量不足不执行折叠", func(t *testing.T) {
		mgr2 := NewContextCollapseManager(config, estimator, budget)
		smallMessages := make([]session.Message, 5)
		for i := range smallMessages {
			smallMessages[i] = session.UserText("短消息")
		}
		err := mgr2.StageCollapse(smallMessages)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mgr2.GetCommitLog().StagedLen() != 0 {
			t.Fatalf("expected 0 staged commits for small messages, got %d", mgr2.GetCommitLog().StagedLen())
		}
	})
}

// TestContextCollapseManager_ShouldAutoCompact 测试自动压缩判断
func TestContextCollapseManager_ShouldAutoCompact(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	budget := DefaultBudget(100000)
	config := DefaultCollapseConfig()
	mgr := NewContextCollapseManager(config, estimator, budget)

	t.Run("Idle阶段应允许自动压缩", func(t *testing.T) {
		mgr.CheckCollapsePhase(0.80)
		if !mgr.ShouldAutoCompact() {
			t.Fatal("expected ShouldAutoCompact to be true in Idle phase")
		}
	})

	t.Run("Staging阶段不应自动压缩", func(t *testing.T) {
		mgr.CheckCollapsePhase(0.90)
		if mgr.ShouldAutoCompact() {
			t.Fatal("expected ShouldAutoCompact to be false in Staging phase")
		}
	})

	t.Run("Blocking阶段不应自动压缩", func(t *testing.T) {
		mgr.CheckCollapsePhase(0.95)
		if mgr.ShouldAutoCompact() {
			t.Fatal("expected ShouldAutoCompact to be false in Blocking phase")
		}
	})
}

// TestContextCollapseManager_RecoverFromOverflow 测试溢出恢复
func TestContextCollapseManager_RecoverFromOverflow(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	budget := DefaultBudget(100000)
	config := DefaultCollapseConfig()
	_ = NewContextCollapseManager(config, estimator, budget)

	messages := make([]session.Message, 20)
	for i := range messages {
		messages[i] = session.UserText("消息内容")
	}

	t.Run("无折叠记录时恢复失败", func(t *testing.T) {
		mgrClean := NewContextCollapseManager(config, estimator, budget)
		recovered, ok := mgrClean.RecoverFromOverflow(messages)
		if ok {
			t.Fatal("expected recovery to fail with no commits")
		}
		if len(recovered) != len(messages) {
			t.Fatalf("expected original messages, got %d messages", len(recovered))
		}
	})

	t.Run("有折叠记录时恢复成功", func(t *testing.T) {
		mgr2 := NewContextCollapseManager(config, estimator, budget)
		// 先暂存折叠
		err := mgr2.StageCollapse(messages)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mgr2.GetCommitLog().StagedLen() == 0 {
			t.Fatal("expected commits after StageCollapse")
		}

		recovered, ok := mgr2.RecoverFromOverflow(messages)
		if !ok {
			t.Fatal("expected recovery to succeed")
		}
		// 恢复后消息应被投影（摘要 + 保留消息）
		if len(recovered) >= len(messages) {
			t.Fatalf("expected fewer messages after recovery, got %d >= %d", len(recovered), len(messages))
		}
	})
}

// TestContextCollapseManager_ResetAfterCompact 测试压缩后重置
func TestContextCollapseManager_ResetAfterCompact(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	budget := DefaultBudget(100000)
	config := DefaultCollapseConfig()
	mgr := NewContextCollapseManager(config, estimator, budget)

	messages := make([]session.Message, 20)
	for i := range messages {
		messages[i] = session.UserText("消息内容")
	}

	// 进入 Staging 阶段并暂存折叠
	mgr.CheckCollapsePhase(0.91)
	_ = mgr.StageCollapse(messages)

	// 重置
	mgr.ResetAfterCompact()

	// 验证状态已重置
	if mgr.GetCommitLog().Len() != 0 {
		t.Fatalf("expected 0 commits after reset, got %d", mgr.GetCommitLog().Len())
	}
	if !mgr.ShouldAutoCompact() {
		t.Fatal("expected ShouldAutoCompact to be true after reset")
	}
}

// TestContextCollapseManager_Disabled 测试禁用状态的向后compatibility
func TestContextCollapseManager_Disabled(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	budget := DefaultBudget(100000)
	config := CollapseConfig{
		Enabled:          false,
		StageThreshold:   0.90,
		BlockThreshold:   0.95,
		PreserveCount:    10,
		MaxStagedCommits: 5,
		DrainRetryMax:    2,
	}
	mgr := NewContextCollapseManager(config, estimator, budget)

	t.Run("禁用时IsEnabled返回false", func(t *testing.T) {
		if mgr.IsEnabled() {
			t.Fatal("expected IsEnabled to be false")
		}
	})

	t.Run("禁用时CheckCollapsePhase始终返回Idle", func(t *testing.T) {
		phase := mgr.CheckCollapsePhase(0.95)
		if phase != CollapsePhaseIdle {
			t.Fatalf("expected CollapsePhaseIdle when disabled, got %v", phase)
		}
	})

	t.Run("禁用时ShouldAutoCompact始终返回true", func(t *testing.T) {
		if !mgr.ShouldAutoCompact() {
			t.Fatal("expected ShouldAutoCompact to be true when disabled")
		}
	})

	t.Run("禁用时StageCollapse不执行折叠", func(t *testing.T) {
		messages := make([]session.Message, 20)
		for i := range messages {
			messages[i] = session.UserText("消息内容")
		}
		err := mgr.StageCollapse(messages)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mgr.GetCommitLog().Len() != 0 {
			t.Fatalf("expected 0 commits when disabled, got %d", mgr.GetCommitLog().Len())
		}
	})

	t.Run("禁用时RecoverFromOverflow返回失败", func(t *testing.T) {
		messages := []session.Message{session.UserText("测试")}
		_, ok := mgr.RecoverFromOverflow(messages)
		if ok {
			t.Fatal("expected recovery to fail when disabled")
		}
	})
}
