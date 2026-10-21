// 移交单 · 安全拒绝种类唯一真相源 —— **正向语义守卫**（第 1 轮 · O-T）。
//
// 与既有 fixtures 的分工（★ 别把这两类守卫当同一件事）：
//
//	apply_diff_prod_test.go:466 `if len(AllFailureKinds()) != 3 { t.Errorf }`
// = **计数**守卫 —— 把 `3` 改成 `4` 即绿，它**不问**"这个新种类在两个轴上各是什么"。
//	tui/g9_rejection_facts_test.go:107
// = **反向**锁（`patch_conflict` **不得**入安全账本）—— 只锁了"不该进的别进"，
// 对"该进的必须进"这条**正向**方向**无任何断言**。
//	本文件
// = **归属**守卫 —— 新增种类时问"它登记了吗、两条轴各是什么"。
//
// 判别性（before 必红 / after 后逐条注入反例实测，见移交单 §5 回执）：
//
//	① 从 failureKindSpecs 删掉任一 entry ⇒ 红（「未登记」）
//	② 往 AllFailureKinds() 加一个 specs 没有的名字 ⇒ 红（「未登记」）
//	③ 往 failureKindSpecs 加一个 AllFailureKinds() 没有的名字 ⇒ 红（「已漂移」）
//
// ⚠️ 本文件**抓不到**的一种失败模式（必须写下来）：新增常量却**两侧都忘登记**
// ⇒ 两张清单互相一致 ⇒ 本文件全绿。抓它的是 `failure_kind_ast_gate_test.go`（结构面）。

package tools

import "testing"

// TestFailureKindRegistry_EveryKindExplicitlyClassified 双向比对两张清单。
//
// 为什么需要「双向」：单看一个方向漏得掉另一半 ——
// 只查 "declared ⊆ specs" 会漏掉"登记了一个不存在的种类"（改名后没同步），
// 只查 "specs ⊆ declared" 会漏掉"新常量没登记"。
func TestFailureKindRegistry_EveryKindExplicitlyClassified(t *testing.T) {
	declared := make(map[FailureKind]bool, len(AllFailureKinds()))
	for _, k := range AllFailureKinds() {
		if k == "" {
			t.Errorf("AllFailureKinds() 里出现了零值：%q", k)
			continue
		}
		if declared[k] {
			t.Errorf("AllFailureKinds() 里重复声明了 %q", k)
		}
		declared[k] = true
		if _, ok := failureKindSpecs[k]; !ok {
			t.Errorf("FailureKind %q 未在 failureKindSpecs 登记 —— 新增 Kind 必须显式声明 "+
				"Terminal 与 SecurityDenial 两轴，不得继承默认（默认值无法区分"+
				"「显式 false」与「忘记登记」）", k)
		}
	}
	for k := range failureKindSpecs {
		if !declared[k] {
			t.Errorf("failureKindSpecs 里的 %q 未出现在 AllFailureKinds() —— 两份清单已漂移", k)
		}
	}
	if len(failureKindSpecs) != len(AllFailureKinds()) {
		t.Errorf("两份清单长度不一致：failureKindSpecs=%d AllFailureKinds()=%d",
			len(failureKindSpecs), len(AllFailureKinds()))
	}
}

// TestFailureKindRegistry_UnknownKindIsSafeByDefault 把"未登记 ⇒ 安全侧"钉住。
//
// 两个轴都必须是 false，且理由不同：
// - Terminal=false ⇒ 未登记的种类**不得**掐掉用户回合（fail-safe：
// 宁可让一个本该终结的失败多试一轮，也不能因为"没登记"就终结用户）；
// - SecurityDenial=false ⇒ 任意外来字符串**不得**污染安全账本
// （账本是 R4 的权威判据来源，写进去即等于断言"发生了安全拒绝"）。
//
// ★ 与既有断言的诚实关系：Terminal 轴这两条**与 `apply_diff_prod_test.go:453-458`
// 重叠**（那里已断言零值 / `nonexistent_kind` 非终局）；本用例新增的有效覆盖
// 是 **SecurityDenial 轴**（既有处无任何断言）。保留重叠是**有意的冗余**：
// 两轴一旦被合并回单轴 map，两处会同时红，信号更响。
func TestFailureKindRegistry_UnknownKindIsSafeByDefault(t *testing.T) {
	for _, k := range []FailureKind{"", "nonexistent_kind"} {
		if k.Terminal() {
			t.Errorf("%q 不得默认获得终局权（会掐掉用户回合）", k)
		}
		if k.IsSecurityDenial() {
			t.Errorf("%q 不得默认被算作安全拒绝（否则任意外来字符串都会污染安全账本，使 R4 判据失真）", k)
		}
	}
}
