package tools

// ── B-1 判据：Monitor 的 tool_result 是给**模型**读的 ────────────────────────
//
// 缺陷（2026-09-30 真机 I-7.1 实测）：tool_result 第 4 行硬编码
// "输出将实时显示，进程结束时自动通知" —— 这是**机制承诺**，不是事实。
// tool_result 的读者是模型，于是模型把它逐字复述进回答，与工具卡片 100% 重复。
// 证据：whale-code/内部工作回执:41-45
// （工具卡片第 4 行 与 模型回答第 2 句 **逐字相同**）
//
// 本测试两条判据各自独立、**两个方向都断言**（缺一即假绿）：
// A 不得含机制承诺句（"输出将实时显示" / "自动通知"）；
// B 必须含查看/停止入口（只删不给出路 = 半修：用户问"怎么停"时模型无话可说）。
//
// before 自检（R4-7）：改动前 internal/tools/monitor.go:113 含承诺句、且**无**停止路径
// ⇒ 判据 A、B 在 before 态**均红**。两个方向都可判别，不是单向装饰。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/monitor"
)

// b1MonitorResult 走生产同层入口（executor.go:1114 注册的就是
// e.monitorHandler(workDir)），返回一次成功启动的 tool_result 正文。
func b1MonitorResult(t *testing.T) string {
	t.Helper()
	e := &Executor{
		handlers:   make(map[string]Handler),
		MonitorMgr: monitor.NewManager(),
	}
	// 夹具纪律：本测试会起真实后台进程 ⇒ 必须配对生产退出收尾（StopAll 幂等）
	t.Cleanup(e.MonitorMgr.StopAll)

	handler := e.monitorHandler(t.TempDir())
	input, err := json.Marshal(MonitorInput{Command: "echo b1-probe", Description: "b1 probe"})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	out, err := handler(context.Background(), input)
	if err != nil {
		t.Fatalf("monitor handler: %v", err)
	}
	return out
}

// TestB1_MonitorResult_NoMechanismPromise 判据 A：tool_result 不得含机制承诺句。
func TestB1_MonitorResult_NoMechanismPromise(t *testing.T) {
	out := b1MonitorResult(t)

	// 正向留痕（必先满足）：这确实是"启动成功"的结果，不是错误路径的返回。
	// 否则"不含承诺句"会被"提前失败"蒙对（本仓 R3-③：负向断言易被更早的短路蒙对）。
	if !strings.Contains(out, "监控已启动") {
		t.Fatalf("不是启动成功的结果 ⇒ 判据 A 会被提前失败蒙对；got=%q", out)
	}
	if !strings.Contains(out, "b1 probe") {
		t.Fatalf("描述未透传（tool_result 不完整）；got=%q", out)
	}

	for _, banned := range []string{"输出将实时显示", "自动通知"} {
		if strings.Contains(out, banned) {
			t.Errorf("tool_result 含机制承诺句 %q ⇒ 模型会逐字复述、与工具卡片重复；got=%q", banned, out)
		}
	}
}

// TestB1_MonitorResult_PointsToStopPath 判据 B：tool_result 必须给出查看/停止入口。
//
// 为什么必配这条：停止后台进程的唯一出口是 `/monitors stop <id>`，而该命令的注册项
// 被 Hidden（command/commands_core.go:60）⇒ 用户在 /help 里看不到。
// 若 tool_result 也不提，则"怎么停"在**模型侧与用户侧同时不可发现**。
func TestB1_MonitorResult_PointsToStopPath(t *testing.T) {
	out := b1MonitorResult(t)

	if !strings.Contains(out, "/monitors") {
		t.Errorf("tool_result 未给出查看入口 /monitors；got=%q", out)
	}
	if !strings.Contains(out, "/monitors stop") {
		t.Errorf("tool_result 未给出停止入口 /monitors stop <id> ⇒ 用户问'怎么停'时模型无话可说；got=%q", out)
	}
}

// TestB1_MonitorSpec_DescriptionIsTargetState 判据 C：模型可见的 Spec 描述要与目标态一致。
//
// 落点取证（R4-9：先证明改的是**生效面**）：
//
//	tools.go 的 []Spec 表 → registerBuiltinTools → Registry.Get → 模型可见工具面。
//
// 因此本判据从**生产构造函数** NewRegistry() 出发断言入口产物，
// 而不是直接读表 —— 把表恢复成旧文案它会红。
//
// ★ 目标态依赖 A 会话（internal/tui 的 A-2：输出不再逐行进对话流）：
//
//	A 未合入时，本描述**暂时超前于实现**（输出此刻仍会实时刷进对话流）。
//	这是 B 单与 A 单的已知顺序依赖，已在回执里显式登记。
func TestB1_MonitorSpec_DescriptionIsTargetState(t *testing.T) {
	r := NewRegistry()
	spec, ok := r.Get("Monitor")
	if !ok {
		t.Fatal("Registry.Get(\"Monitor\") 未命中 ⇒ 落点找错，本判据无效")
	}

	// 判据 C-1：不得再对模型承诺"逐行实时显示"
	if strings.Contains(spec.Description, "displayed in real-time") {
		t.Errorf("模型可见描述仍承诺逐行实时显示（与 A 的目标态相悖）；got=%q", spec.Description)
	}
	// 判据 C-2：必须指明输出有界 + 模型**真能走**的主动查看路径。
	//
	// ★ 已翻转（2026-10-13，）：原期望是 "/monitors"。但 `/monitors` 是**人类斜杠
	// 命令、模型调不到** —— 这一点 早已确证（internal/tools/monitor.go:30-34 逐字：
	// "两者都是人类斜杠命令，模型调不到"），当时只修了 tool_result、**Spec 漏改**。
	// 模型面唯一可走的主动查看入口是 MonitorTail（现可带 wait_ms 等进程结束）。
	// 断言方向不变（"必须给出主动查看入口"），只把过时的入口名换成正确的那个。
	for _, want := range []string{"MonitorTail"} {
		if !strings.Contains(spec.Description, want) {
			t.Errorf("模型可见描述未给出 %s 入口；got=%q", want, spec.Description)
		}
	}
	// 反向半边（R9）：只断言"含新句"会把"新旧并存"读成"换对了" ⇒ 必须断言旧句已去。
	if strings.Contains(spec.Description, "auto-notifies on process exit. Used for monitoring") {
		t.Errorf("旧描述仍在（新旧并存 = 没换掉）；got=%q", spec.Description)
	}
}
