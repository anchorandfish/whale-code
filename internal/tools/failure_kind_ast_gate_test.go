// 移交单 · 安全拒绝种类唯一真相源 —— **AST 机械门**（第 1 轮 · O-T）。
//
// 被守卫的不变量：
//
//	tools.go 里**声明**的每一个 `X FailureKind = "..."` 常量，
//	都必须在 `failureKindSpecs` 里**显式登记**（Terminal + SecurityDenial 两轴）。
//
// 为什么必须有它（根因，不是"某行写错"）：
//
//	Go 的**单测无法枚举常量**。若某人新增
//	`FailureKindNetworkDenied FailureKind = "network_denied"` 却**两侧都忘了改**，
//	则 `failure_kind_registry_test.go` 的两份清单（AllFailureKinds 与 failureKindSpecs）
//	**互相一致** ⇒ **全绿**，而该新种类在安全账本上静默漏记
//	（下游 `tui/stream.go:1177` 的 `roundDeniedKind != ""` 判据对它恒 false）。
//	只有**结构面（AST）**能抓住"声明了但没登记"。
//
// ⚠️ 已知边界（必须写下来，否则下一个人会高估它）：
// - 只扫 **tools.go 一个文件**。若将来 FailureKind 常量被挪到别的文件，
// 本门必须同步扩面（否则静默失效）。
// - 只看**显式声明 `FailureKind` 类型**的常量，外加一条**命名约定兜底**
// （名字以 `FailureKind` 开头且值为字符串字面量）—— 覆盖 `X = "..."` 省略类型的写法。
// - 本用例**运行期自读磁盘**（go/parser + nil src + 相对路径）
// ⇒ 按本仓铁律，`-overlay` 对它**物理无效**：判别性只能靠
// 「受控写回 + 无条件还原 + md5 校验」证明（实测见移交单 §5 回执）。

package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// TestFailureKindASTGate_EveryDeclaredKindIsRegistered 是本单的结构核心。
//
// 两侧都从 AST 取，**不依赖任何运行期符号**（`failureKindSpecs` / `AllFailureKinds`
// 在本用例里都只是**字符串**，不是被引用的符号）——
// 这正是它能对"两侧都忘登记"这一失败模式保持灵敏的原因。
//
// 判别性（before 必红；after 后逐条注入反例实测）：
//
//	④ 只往 tools.go 加常量、不动两张清单 ⇒ 本门必红，报错须列出未登记的名字。
func TestFailureKindASTGate_EveryDeclaredKindIsRegistered(t *testing.T) {
	declared := failureKindDeclaredConsts(t)
	registered, found := failureKindRegisteredSpecs(t)

	if !found {
		t.Fatalf("未能在 tools.go 里找到 `var failureKindSpecs` 的 map 复合字面量 —— " +
			"语义表的形态被改动了，本门已失去判别对象（这是**门失效**，不是「没登记」）。" +
			"若确实要改形态，请同步更新本门与其头注释声明的边界。")
	}
	if len(declared) == 0 {
		t.Fatalf("未能在 tools.go 里扫到任何 `FailureKind` 常量 —— 扫描器失灵或常量已搬家，本门已失去判别对象")
	}

	var missing, drifted []string
	for name := range declared {
		if !registered[name] {
			missing = append(missing, name)
		}
	}
	for name := range registered {
		if !declared[name] {
			drifted = append(drifted, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(drifted)

	if len(missing) > 0 {
		t.Errorf("failureKindSpecs **漏登记**了已声明的 FailureKind 常量：%s\n"+
			"  ⇒ 该种类在「终局轴」与「安全账本轴」上都拿不到显式语义（静默继承默认）。\n"+
			"  ⇒ 新增 Kind 必须在 failureKindSpecs 里显式声明 Terminal 与 SecurityDenial 两轴，"+
			"**不得**靠默认值（默认值无法区分「显式 false」与「忘记登记」）。",
			strings.Join(missing, ", "))
	}
	if len(drifted) > 0 {
		t.Errorf("failureKindSpecs 里的 %s 在 tools.go 里**找不到对应的常量声明**（两份清单已漂移）：\n"+
			"  ⇒ 要么是改名后没同步，要么是登记了一个不存在的种类。",
			strings.Join(drifted, ", "))
	}
}

// failureKindDeclaredConsts 返回 tools.go 里**声明为 FailureKind** 的常量名集合。
//
// 判定规则（两条并集，避免只靠一种写法）：
// 1. const 块里 `Type` 为标识符 `FailureKind` 的 ValueSpec 的名字；
// 2. const 块里名字以 `FailureKind` 开头、且值为字符串字面量的 ValueSpec 的名字
// —— 兜住 `FailureKindXxx = "xxx"`（省略类型）的写法。
func failureKindDeclaredConsts(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	// ★ 读盘型：相对路径 + nil src ⇒ 解析的是磁盘上的 tools.go
	// （测试工作目录即包目录，见 d002 / g6 两个同包先例）。
	file, err := parser.ParseFile(fset, failureKindSourceFile, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", failureKindSourceFile, err)
	}

	out := map[string]bool{}
	for _, d := range file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			typed := false
			if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "FailureKind" {
				typed = true
			}
			if !typed && !failureKindLooksLikeKindConst(vs) {
				continue
			}
			for _, n := range vs.Names {
				if n.Name != "" && n.Name != "_" {
					out[n.Name] = true
				}
			}
		}
	}
	return out
}

// failureKindLooksLikeKindConst 是命名约定兜底：名字前缀 `FailureKind` ＋ 值是字符串字面量。
func failureKindLooksLikeKindConst(vs *ast.ValueSpec) bool {
	if len(vs.Values) == 0 {
		return false
	}
	for _, n := range vs.Names {
		if !strings.HasPrefix(n.Name, "FailureKind") {
			return false
		}
	}
	for _, v := range vs.Values {
		if _, ok := v.(*ast.BasicLit); !ok || v.(*ast.BasicLit).Kind != token.STRING {
			return false
		}
	}
	return true
}

// failureKindRegisteredSpecs 返回 `var failureKindSpecs = map[FailureKind]failureKindSpec{...}`
// 里**已登记的 Key 名**集合。第二个返回值报告"是否找到了该 map 字面量"。
//
// 只取 map 复合字面量的**直接** KeyValueExpr 键 —— 值里嵌的
// `{Terminal: true, SecurityDenial: true}` 是 KeyValueExpr 但**不是** map 的键，必须排除。
func failureKindRegisteredSpecs(t *testing.T) (map[string]bool, bool) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, failureKindSourceFile, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", failureKindSourceFile, err)
	}

	out := map[string]bool{}
	found := false

	for _, d := range file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			named := false
			for _, n := range vs.Names {
				if n.Name == failureKindSpecsVarName {
					named = true
				}
			}
			if !named {
				continue
			}
			for _, v := range vs.Values {
				cl, ok := v.(*ast.CompositeLit)
				if !ok {
					continue
				}
				if _, isMap := cl.Type.(*ast.MapType); !isMap {
					continue
				}
				found = true
				for _, elt := range cl.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if id, ok := kv.Key.(*ast.Ident); ok && id.Name != "" && id.Name != "_" {
						out[id.Name] = true
					}
				}
			}
		}
	}
	return out, found
}

const (
	// failureKindSourceFile 是语义表的声明处（本门只扫这一个文件，见头注释「已知边界」）。
	failureKindSourceFile = "tools.go"
	// failureKindSpecsVarName 是语义表的变量名 —— 门与被守卫对象之间的**契约名**。
	failureKindSpecsVarName = "failureKindSpecs"
)
