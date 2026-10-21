package command

import (
	"reflect"
	"testing"
)

// TestEffortCommand_Removed C 批 C1判据：`/effort` 整条命令已删除。
//
// ★ 为什么必须有这条：它此前是 Hidden 且**未接线**（OnSetEffort 全仓零赋值 ⇒ 敲了只回
// "not wired"），属于"做不到的命令"。删掉最诚实，但要留判据挡住"顺手加回来"——
// 要作为新功能回归，必须先接 `OnSetEffort` 等回调 + 映射厂商原生参数
// （见 内部工作回执 §C1）。
func TestEffortCommand_Removed(t *testing.T) {
	r := NewRegistry()

	if _, ok := r.Get("effort"); ok {
		t.Error("/effort 应从注册表中删除（C1 已删），但 Get(\"effort\") 仍命中")
	}

	// 声明面：补全不得再给出 effort
	for _, c := range r.Completion("/eff") {
		if c == "effort" {
			t.Error("/effort 已删除，补全不应再给出 effort")
		}
	}
}

// TestRegistry_HasNoEffortCallbacks 结构面锁：`/effort` 专属的 4 个回调一并删除。
// 留着回调字段 = 留着一个"看起来还能接线的插口"，会诱导下一个人以为 effort 仍是活功能。
func TestRegistry_HasNoEffortCallbacks(t *testing.T) {
	rt := reflect.TypeOf(Registry{})
	for _, name := range []string{"OnGetEffort", "OnSetEffort", "OnGetCurrentModel", "OnGetEffortLevels"} {
		if _, ok := rt.FieldByName(name); ok {
			t.Errorf("Registry 不应再有 %s 回调（C1 已删）", name)
		}
	}
}
