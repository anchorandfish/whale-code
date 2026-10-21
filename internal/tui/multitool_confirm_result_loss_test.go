package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/session"
)

// 本文件守护一条不变量（见 app.go pipelineConfirmMsg 注释）：
//
//	确认暂停是「挂起-恢复」语义 —— 暂停时已发生的一切副作用，其产出必须
//	随恢复上下文完整保存。否则恢复后只会从 currentIndex 继续重跑，
//	下标 < currentIndex 的工具既不会被重跑、其结果也无处重现，被永久丢弃。
//
// 表现症状（2026-09-14 S10-M2 手测）：一轮里 N 个工具调用 + 中途人工确认时，
// 会话里只留下 M<N 个 tool_result（TUI 少渲染 📋，且模型在残缺上下文上作答）。
//
// 覆盖的两个丢弃点（都在 internal/tui/tool_exec.go）：
// - executeToolsDirect 的 pipeline 分支（首个工具已执行、后续才需确认）
// - executePipelineRemaining 的再次暂停分支（连续两个工具都需确认）
//
// ⚠️ 本测试必须走**真实消息分发**（m.Update），而不是手工构造 PendingConfirm
// 再调 executePipelineRemaining —— 否则 app.go 里 pipelineConfirmMsg →
// PendingConfirm 的字段透传一旦漏掉，测试不会发现（假绿）。

// execForResult 执行 tea.Cmd，穿透 tea.BatchMsg，取出执行/确认类消息。
func execForResult(cmd tea.Cmd) any {
	if cmd == nil {
		return nil
	}
	switch v := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range v {
			if c == nil {
				continue
			}
			switch r := c().(type) {
			case pipelineConfirmMsg:
				return r
			case toolResultMsg:
				return r
			}
		}
		return nil
	case pipelineConfirmMsg:
		return v
	case toolResultMsg:
		return v
	}
	return nil
}

// driveConfirmsByProductionPath 复刻用户真实操作序列，全程走生产消息分发：
//
//	executeTools() → pipelineConfirmMsg
//
// → m.Update(pipelineConfirmMsg) 【生产处理器：构造 PendingConfirm，含字段透传】
// → m.Update(tea.KeyMsg{Enter}) 【生产确认入口：选择第 0 项 Allow once】
// → m.Update(confirmResultMsg) 【生产分发：executePipelineRemaining】
// → 执行批处理 cmd → 下一次 pipelineConfirmMsg 或最终 toolResultMsg
//
// 返回最终消息与被暂停的 currentIndex 序列。
//
// ⚠️ Model.Update 是**值接收者**（app.go:1202），内部会 reassignServices() 把
// toolService 重新指向**副本自己的** ToolModel。因此必须接住返回值并继续用新
// Model —— 否则所有写入都发生在读不到的另一个对象上（TUI 按值复制陷阱）。
func driveConfirmsByProductionPath(t *testing.T, m *Model, first any) (any, []int) {
	t.Helper()
	out := first
	var pausedAt []int
	for step := 0; ; step++ {
		pc, isConfirm := out.(pipelineConfirmMsg)
		if !isConfirm {
			return out, pausedAt
		}
		if step >= 5 {
			t.Fatalf("确认次数超过 5 次仍在暂停，疑似死循环：last currentIndex=%d", pc.currentIndex)
		}
		pausedAt = append(pausedAt, pc.currentIndex)
		t.Logf("第 %d 次确认：currentIndex=%d pendingTools=%d priorResults=%d",
			step+1, pc.currentIndex, len(pc.pendingTools), len(pc.priorResults))

		// ① 生产处理器：构造 PendingConfirm（这里也是 priorResults 的透传点）
		updated, cmd := m.Update(pc)
		m = adoptModel(t, updated)
		if cmd != nil {
			_ = cmd()
		}
		pending := m.toolService.PendingConfirm()
		if pending == nil {
			t.Fatalf("pipelineConfirmMsg 处理后应设置 PendingConfirm")
		}
		if len(pending.PipelinePriorResults) != len(pc.priorResults) {
			t.Fatalf("PendingConfirm 未完整承接 priorResults：msg=%d pending=%d"+
				"（app.go 的透传点漏字段）",
				len(pc.priorResults), len(pending.PipelinePriorResults))
		}

		// ② 生产确认入口：按 Enter = 选项 0（Allow once）
		updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = adoptModel(t, updated)
		if cmd == nil {
			t.Fatalf("确认 UI 未产出决策命令（state=%v）", m.sessionService.State())
		}
		decision := cmd()
		if _, ok := decision.(confirmResultMsg); !ok {
			t.Fatalf("期望 confirmResultMsg，实际 %T", decision)
		}

		// ③ 生产分发：confirmResultMsg → Batch(executePipelineRemaining, restartTick)
		updated, execCmd := m.Update(decision)
		m = adoptModel(t, updated)
		out = execForResult(execCmd)
		if out == nil {
			t.Fatalf("confirmResultMsg 分发后未得到 pipelineConfirmMsg/toolResultMsg")
		}
	}
}

// adoptModel 接住 Update 返回的新 Model（值接收者 ⇒ 必须继续用新值）。
func adoptModel(t *testing.T, updated tea.Model) *Model {
	t.Helper()
	mv, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update 返回类型应为 Model，实际 %T", updated)
	}
	return &mv
}

// newMultiToolModel 构造一个处于工具执行态、workspace-write 权限的 Model。
// workDir 取自 os.Getwd()（lifecycle.go:146），由调用方 t.Chdir(t.TempDir())
// 保证沙箱允许写入且不污染仓库。
func newMultiToolModel(t *testing.T, blocks []session.ContentBlock) *Model {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	model := NewModel(&cfg, newTestClient("test-model"), newTestRegistry())
	m := &model
	m.toolService.Policy().SetMode(permission.LevelWorkspaceWrite)
	m.state = StateToolExec
	// ⚠️ TUI 按值复制陷阱（见 .workbuddy/memory/domains/TUI按值复制陷阱.md）：
	// NewModel 内部把 toolService 指向 &m.ToolModel，但返回值是 Model 副本，
	// 于是 m.toolService 与本次 m 的字段是**两处存储**。executeTools 读副本字段
	// （m.pendingTools），executeToolsDirect 读 service（m.toolService.PendingTools()）。
	// 测试必须两条路径都设，否则会出现「service 有 2 条、字段 0 条」而静默返回空结果。
	m.toolService.SetPendingTools(blocks)
	m.pendingTools = blocks
	return m
}

func resultIDs(res toolResultMsg) []string {
	ids := make([]string, 0, len(res.results))
	for _, r := range res.results {
		ids = append(ids, fmt.Sprintf("%s(err=%v)", r.ID, r.IsError))
	}
	return ids
}

// TestFix_MultiToolConfirm_KeepsEarlierToolResults —— 场景 A：
// 两个工具都需确认（首次在 index=0 暂停，批准后 index=1 再次暂停）。
// 覆盖 executePipelineRemaining 的再次暂停分支。
//
// ⚠️ 2026-09-15 fixture 适配（记录在案，勿"修回"）：目标由 a.txt/b.txt 改为
// .git/config / .git/HEAD —— 两个**受保护路径**（permission.ProtectedPaths 含 ".git"）。
//
// 为什么必须换：新语义下「目标落在工作区内且非受保护」的写免确认
// （internal/tools/pipeline.go workspaceContainedWrite，对齐业内既有实现
// assess_patch_safety），a.txt/b.txt 经 t.Chdir(t.TempDir()) 落在工作区内 ⇒ 都不再暂停，
// 本用例「连续两次暂停」的前提消失。
//
// 为什么受保护路径是**正交**的驱动源（而非又一个依赖被改行为的道具）：
// SafetyCheckStage 排在 PermissionStage **之前**，命中受保护路径即短路返回 NeedConfirm
// （pipeline.go:259-267），PermissionStage 里的免问收窄结构上够不着它。
// ⇒ 本 fixture 在修复前后行为**一致**（两个都仍需确认），既不引入虚假翻转，
// 又顺带把"豁免不得绕过 SafetyCheckStage"钉进了这条既有不变量测试里。
func TestFix_MultiToolConfirm_KeepsEarlierToolResults(t *testing.T) {
	t.Chdir(t.TempDir())

	m := newMultiToolModel(t, []session.ContentBlock{
		{Type: "tool_use", ID: "call_a", Name: "write_file", Input: `{"path":".git/config","content":"AAA"}`},
		{Type: "tool_use", ID: "call_b", Name: "write_file", Input: `{"path":".git/HEAD","content":"BBB"}`},
	})

	out, pausedAt := driveConfirmsByProductionPath(t, m, m.executeTools()())

	if len(pausedAt) < 2 {
		t.Fatalf("预期两个 write_file 各暂停一次（共 2 次），实际 %d 次：%v", len(pausedAt), pausedAt)
	}
	res, ok := out.(toolResultMsg)
	if !ok {
		t.Fatalf("确认结束后应返回 toolResultMsg，实际 %T", out)
	}
	ids := resultIDs(res)
	t.Logf("最终 results（%d 条）：%v", len(res.results), ids)

	if len(res.results) != 2 {
		t.Fatalf("【缺陷复现】2 条 write_file 只回传 %d 条结果（丢弃 %d 条）：%v\n"+
			"—— 确认暂停点之前已执行工具的结果被丢弃了", len(res.results), 2-len(res.results), ids)
	}
	if ids[0] != "call_a(err=false)" {
		t.Fatalf("结果顺序/内容不符：期望自首条 call_a 开始，实际 %v", ids)
	}
}

// TestFix_MultiToolConfirm_FirstToolAllowed_KeepsItsResult —— 场景 B：
// 首个工具免确认、已执行产出结果，第二个才需确认。
// 覆盖 executeToolsDirect 的暂停分支 —— 该分支同样必须携带已产出结果。
//
// ⚠️ 2026-09-15 fixture 适配（见场景 A 注释）：**第二个**工具由 b.txt 改为 .git/config
// —— 受保护路径（ProtectedPaths 含 ".git"），经 SafetyCheckStage 短路（排在 PermissionStage
// 之前）恒需确认，与免问收窄正交。若仍用普通路径，它落在工作区内会免确认 ⇒ 本用例
// 不再有任何暂停点。
// **首个**工具保持 write_file 到普通路径 a.txt：它在两种语义下都免确认
// （旧：下方 Remember 的会话级授权命中；新：工作区内非受保护写天然免确认），
// 且执行成功（err=false）—— 正是本用例要"带到暂停点去"的那条已产出结果。
func TestFix_MultiToolConfirm_FirstToolAllowed_KeepsItsResult(t *testing.T) {
	t.Chdir(t.TempDir())

	blocks := []session.ContentBlock{
		{Type: "tool_use", ID: "call_a", Name: "write_file", Input: `{"path":"a.txt","content":"AAA"}`},
		{Type: "tool_use", ID: "call_b", Name: "write_file", Input: `{"path":".git/config","content":"BBB"}`},
	}
	m := newMultiToolModel(t, blocks)
	// 预授权 call_a（会话级允许）⇒ 它在 index=0 免确认、直接执行并产出结果，
	// index=1 的 call_b 才触发暂停。此时「已产出结果」必须随暂停点带走。
	m.toolService.Policy().Remember("write_file", blocks[0].Input, true)

	out, pausedAt := driveConfirmsByProductionPath(t, m, m.executeTools()())

	if len(pausedAt) != 1 || pausedAt[0] != 1 {
		t.Fatalf("预期仅在 index=1 暂停一次（call_a 已授权免确认），实际暂停于 %v", pausedAt)
	}
	res, ok := out.(toolResultMsg)
	if !ok {
		t.Fatalf("确认结束后应返回 toolResultMsg，实际 %T", out)
	}
	ids := resultIDs(res)
	t.Logf("最终 results（%d 条）：%v", len(res.results), ids)

	if len(res.results) != 2 {
		t.Fatalf("【缺陷复现】首个已执行工具（call_a）的结果被丢弃，仅剩 %d 条：%v"+
			"\n—— executeToolsDirect 的暂停点未携带 priorResults", len(res.results), ids)
	}
	if ids[0] != "call_a(err=false)" {
		t.Fatalf("已执行工具的结果未前置回结果序列：%v", ids)
	}
}
