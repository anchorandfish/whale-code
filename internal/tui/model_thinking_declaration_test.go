package tui

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// tuiOfficialBaseURL 是**厂商官方端点**（A2 起能力判定带端点维度）。
// 判据里必须用它而不是中性占位符：非官方端点会让 thinking 一律降级为 Unknown，
// 「未声明 ⇒ Unknown」这类断言会因为端点原因成立 ⇒ 假绿。
const tuiOfficialBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

// B1：`/model add|edit --supports-thinking` 必须**真的接线**——
// 从命令入口一路走到 internal/ai 的声明层（下发的唯一判据）。
//
// 为什么单独测这一层：internal/ai 的单元测试证明了"声明生效"，
// 但**证明不了命令真的去登记了声明**。用户敲的命令与内部判定之间断了，
// 表现是"命令说成功了、行为没变"—— 本仓最贵的一类缺陷（假生效）。
//
// ★ 双实现覆盖：TUI（commands_model.go）与 plain（plain.go）各有
// 一套 add/edit/remove。这里走 plain 全三子命令；TUI 侧调的是**同一个**
// syncThinkingDeclaration / clearThinkingDeclaration（见两处调用点），
// 语义等价性由 TestSyncThinkingDeclaration_SharedSemantics 直接固化。
func TestPlainModelAdd_ThinkingDeclaration_WiresToAILayer(t *testing.T) {
	ai.ClearThinkingDeclarations()
	t.Cleanup(ai.ClearThinkingDeclarations)

	r := newTestPlainRunner()

	// 前置：未声明 ⇒ 未知（否则后续断言可能是"本来就这样"）。
	if got := ai.ThinkingSupportFor("kimi-k2", tuiOfficialBaseURL); got != ai.ThinkingCapUnknown {
		t.Fatalf("前置不成立：kimi-k2 未声明时应为 Unknown，实际 %v", got)
	}

	// ① add 带声明
	out := r.handleModelAdd("kimi-k2 --base-url https://dashscope.aliyuncs.com/compatible-mode/v1 --supports-thinking supported")
	if got := ai.ThinkingSupportFor("kimi-k2", tuiOfficialBaseURL); got != ai.ThinkingCapSupported {
		t.Errorf("handleModelAdd 后声明未生效：ThinkingSupportFor = %v（命令输出：%s）", got, out)
	}

	// ② 重加（不带 flag）必须**保留**已有声明 —— 否则用户每重加一次模型，
	// 声明就静默退化一次，而他看到的是"model updated"。
	_ = r.handleModelAdd("kimi-k2 --base-url https://dashscope.aliyuncs.com/compatible-mode/v1")
	if got := ai.ThinkingSupportFor("kimi-k2", tuiOfficialBaseURL); got != ai.ThinkingCapSupported {
		t.Errorf("重加模型后声明应保留，实际 %v（声明被静默抹掉）", got)
	}

	// ③ edit 改声明
	_ = r.handleModelEdit("kimi-k2 --supports-thinking unsupported")
	if got := ai.ThinkingSupportFor("kimi-k2", tuiOfficialBaseURL); got != ai.ThinkingCapUnsupported {
		t.Errorf("handleModelEdit 后应为 Unsupported，实际 %v", got)
	}

	// ④ remove 必须**撤销**声明：声明层是进程级的，不会被 Remove 连带清掉。
	// 不撤销的话，同名模型下次被 add 回来会带着一个用户已经删掉的声明。
	_ = r.handleModelRemove("kimi-k2")
	if got := ai.ThinkingSupportFor("kimi-k2", tuiOfficialBaseURL); got != ai.ThinkingCapUnknown {
		t.Errorf("handleModelRemove 后声明应被撤销，实际 %v", got)
	}
}

// TestSyncThinkingDeclaration_SharedSemantics 固化共享 helper 的语义。
// 它是 TUI 与 plain 两条入口的唯一实现 —— 改坏它等于两条入口同时坏，
// 所以这里单独钉住三种输入（非空 / 空 / 非法）与 nil 安全。
func TestSyncThinkingDeclaration_SharedSemantics(t *testing.T) {
	ai.ClearThinkingDeclarations()
	t.Cleanup(ai.ClearThinkingDeclarations)

	ai.RegisterThinkingDeclaration("m1", ai.ThinkingCapSupported)

	// 非空合法声明 ⇒ 覆盖（双向：也能把"支持"改成"不支持"）。
	syncThinkingDeclaration(&ai.ModelConfig{Name: "m1", ThinkingSupport: "unsupported"})
	if got := ai.ThinkingSupportFor("m1", tuiOfficialBaseURL); got != ai.ThinkingCapUnsupported {
		t.Errorf("非空声明应覆盖，实际 %v", got)
	}

	// 空串 ⇒ 撤销声明，回到能力表/前缀推断（这就是"未声明"）。
	syncThinkingDeclaration(&ai.ModelConfig{Name: "m1"})
	if got := ai.ThinkingSupportFor("m1", tuiOfficialBaseURL); got != ai.ThinkingCapUnknown {
		t.Errorf("空声明应撤销（回到推断），实际 %v", got)
	}

	// 非法值 ⇒ 也撤销（不留垃圾声明）。注意：非法值在命令层已被拒绝，
	// 但 helper 是公开语义的一部分，直接调用也必须安全。
	ai.RegisterThinkingDeclaration("m1", ai.ThinkingCapSupported)
	syncThinkingDeclaration(&ai.ModelConfig{Name: "m1", ThinkingSupport: "bogus"})
	if got := ai.ThinkingSupportFor("m1", tuiOfficialBaseURL); got != ai.ThinkingCapUnknown {
		t.Errorf("非法声明应撤销，实际 %v", got)
	}

	// nil 安全（不得 panic）。
	syncThinkingDeclaration(nil)
	clearThinkingDeclaration("m1")
	if got := ai.ThinkingSupportFor("m1", tuiOfficialBaseURL); got != ai.ThinkingCapUnknown {
		t.Errorf("clearThinkingDeclaration 后应为 Unknown，实际 %v", got)
	}
}
