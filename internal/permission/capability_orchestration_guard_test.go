package permission

import "testing"

// 守卫：编排族（orchestrationTools）的成员资格 —— 防「影子判据漂移」。
//
// 背景：`permission.IsOrchestrationTool` 是**审批免问**的判据（见
// internal/tools/pipeline.go 的 PermissionStage 出口 (b)）。免问意味着"不再有人工
// 闸门"，因此这个集合的**准入条件必须被机械钉住**，否则将来一次"顺手加一行"就会
// 把有本机副作用的工具变成免审执行 —— 那是本仓库最不能接受的漂移方向。
//
// 两条不变量（对应 capability.go 里 orchestrationTools 的三条入选判据）：
//
//	① 每个成员的能力桶**必须恰为 [Meta]** —— 即"无本机披露、无持久化落盘"。
//	   若某工具具备 Exec/Edit/Network/Mcp，它就有自己的副作用面，必须留在审批内。
//	② 排除清单**不得**出现在成员里 —— 它们各自违反了入选判据第 2/3 条。
//
// 判别性（A/B，实测）：把 "write_file" 或 "bash" 加进 orchestrationTools ⇒ ①立即红并点名；
// 把 "taskcreate" 加进去 ⇒ ②立即红。反向：磁盘原状 ⇒ 全绿。
func TestGuard_OrchestrationToolsAreMetaOnly(t *testing.T) {
	members := OrchestrationTools()
	if len(members) == 0 {
		t.Fatal("编排族为空：免问出口 (b) 会变成死代码，本守卫失去判别力")
	}

	for _, name := range members {
		caps := CapabilitiesOf(name)
		if len(caps) != 1 || caps[0] != CapMeta {
			t.Errorf("编排族成员 %q 的能力桶必须恰为 [Meta]，实际 %v。\n"+
				"它有本机副作用面 ⇒ 不得免问审批（见 capability.go 的 orchestrationTools 判据）。",
				name, caps)
		}
	}
}

// TestGuard_OrchestrationExclusionsStayOut 钉住"看起来像编排族、实际不满足判据"的工具。
//
// 这些是最容易被误加进免问清单的候选，逐个写明**为什么不行**。
func TestGuard_OrchestrationExclusionsStayOut(t *testing.T) {
	excluded := map[string]string{
		"taskcreate": "入库描述为 \"Executes a command in the background\" —— 它真的在本机执行命令（判据 3）",
		"crondelete": "改持久任务表 —— 持久化控制面写入（判据 2）",
		// schedule_cron 与 crondelete 同源理由；若注册表里存在同名工具，本条一并钉住。
		"schedule_cron":      "改持久任务表 —— 持久化控制面写入（判据 2）",
		"bash":               "在本机执行任意命令（Exec 桶，判据 3）",
		"monitor":            "在本机执行任意命令（Exec 桶，判据 3）",
		"write_file":         "写本机文件（Edit 桶，判据 1）",
		"apply_unified_diff": "写本机文件（Edit 桶，判据 1）",
	}

	for name, why := range excluded {
		if IsOrchestrationTool(name) {
			t.Errorf("%q 不得进入编排族：%s", name, why)
		}
	}
}

// TestGuard_OrchestrationUnknownToolIsNotExempt 钉住 fail-closed 方向。
//
// 未登记的工具一律**不得**被免问 —— 否则"改个名字/写错大小写"就可能绕过审批。
// 大小写与首尾空白不敏感是刻意保留的（与 CapabilitiesOf 的规范化一致），
// 但它不得把未知工具变成已知。
func TestGuard_OrchestrationUnknownToolIsNotExempt(t *testing.T) {
	for _, name := range []string{"", "   ", "zz_unknown_tool", "taskcreate_x", "fork"} {
		if IsOrchestrationTool(name) {
			t.Errorf("%q 未登记于编排族，必须 fail-closed 返回 false（保持原有确认行为）", name)
		}
	}

	// 反向：已登记成员经大小写/空白规范化后仍须命中（防把 TrimSpace 误改成
	// 精确匹配 —— 那会让 TUI 传入的大小写变体静默退化为"每次弹窗"）。
	for _, name := range []string{"Task", "TASK", "  task  ", "Agent", "Skill", "TaskStop"} {
		if !IsOrchestrationTool(name) {
			t.Errorf("IsOrchestrationTool(%q) = false，want true（大小写/空白不敏感）", name)
		}
	}
}
