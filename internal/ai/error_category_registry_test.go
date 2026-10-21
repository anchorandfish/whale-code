// 移交单 · ErrorID→Category 显式归属 —— **正向跑期守卫**。
//
// 与 failureKind 族守卫的分工（★ 别把这两类守卫当同一件事）：
//
//	本文件 = **归属守卫** —— 新增 ErrorID 时问"它登记了吗、非 default 归属是什么"。
//	error_category_ast_gate_test.go = **结构面守卫** —— 扫 errors.go 常量声明，
// 抓"声明了常量但两侧都忘登记"（本文件抓不到的一种失败模式，见下）。
//
// ⚠️ 本文件**抓不到**的一种失败模式（必须写下来）：新增常量却**两侧都忘登记**
// ⇒ AllErrorIDs() 与 errorCategorySpecs 互相一致 ⇒ 本文件全绿，而该新 ErrorID 在
// categoryFromID 里静默继承 CategoryFatal ⇒ IsRetryable() 恒 false。
// 抓它的是 error_category_ast_gate_test.go（结构面，扫常量声明）。

package ai

import "testing"

// TestErrorCategoryRegistry_EveryIDExplicitlyClassified 双向比对两份清单。
//
// 为什么需要「双向」：单看一个方向漏得掉另一半 ——
// 只查 "declared ⊆ specs" 会漏掉"登记了一个不存在的 ID"（改名后没同步），
// 只查 "specs ⊆ declared" 会漏掉"新常量没登记"。
func TestErrorCategoryRegistry_EveryIDExplicitlyClassified(t *testing.T) {
	declared := make(map[ErrorID]bool, len(AllErrorIDs()))
	for _, id := range AllErrorIDs() {
		if id == "" {
			t.Errorf("AllErrorIDs() 里出现了零值：%q", id)
			continue
		}
		if declared[id] {
			t.Errorf("AllErrorIDs() 里重复声明了 %q", id)
		}
		declared[id] = true
		if _, ok := errorCategorySpecs[id]; !ok {
			t.Errorf("ErrorID %q 未在 errorCategorySpecs 登记 —— 新增 ErrorID 必须显式声明 "+
				"Category，不得继承 default(=CategoryFatal)（default 无法区分「显式 Fatal」与「忘记登记」）", id)
		}
	}
	for id := range errorCategorySpecs {
		if !declared[id] {
			t.Errorf("errorCategorySpecs 里的 %q 未出现在 AllErrorIDs() —— 两份清单已漂移", id)
		}
	}
	if len(errorCategorySpecs) != len(AllErrorIDs()) {
		t.Errorf("两份清单长度不一致：errorCategorySpecs=%d AllErrorIDs()=%d",
			len(errorCategorySpecs), len(AllErrorIDs()))
	}
}

// TestErrorCategoryRegistry_UnknownIDIsFatalByDefault 把"未登记 ⇒ CategoryFatal 兜底"钉住。
//
// 兜底 CategoryFatal 是**必要的 fail-closed**：categoryFromID 可能被传入任意外来 ErrorID
// （如 GetErrorID 从 APIError 提取的未知值），此时宁可判不可重试（Fatal）也不该误判可重试。
// 这与「新增**常量**漏登记」是两回事 —— 前者由本测试钉住兜底语义，后者由 AST 门抓。
func TestErrorCategoryRegistry_UnknownIDIsFatalByDefault(t *testing.T) {
	for _, id := range []ErrorID{"", "nonexistent_error_id"} {
		if got := categoryFromID(id); got != CategoryFatal {
			t.Errorf("categoryFromID(%q) = %v，期望 CategoryFatal（未登记须 fail-closed 兜底）", id, got)
		}
	}
}
