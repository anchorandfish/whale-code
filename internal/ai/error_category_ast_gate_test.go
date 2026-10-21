// 移交单 · ErrorID→Category 显式归属 —— **AST 机械门**。
//
// 被守卫的不变量：
//
//	errors.go 里**声明**的每一个 `X ErrorID = "..."` 常量，
//	都必须在 `errorCategorySpecs` 里**显式登记**（非 default 归属）。
//
// 为什么必须有它（根因，不是"某行写错"）：
//
//	Go 的**单测无法枚举常量**。若某人新增
//	`ErrAIApiTimeout ErrorID = "ai_api_timeout"` 却**两侧都忘了改**，
//	则 error_category_registry_test.go 的两份清单（AllErrorIDs 与 errorCategorySpecs）
//	**互相一致** ⇒ **全绿**，而该新 ErrorID 在 categoryFromID 里静默继承 CategoryFatal
//	（下游 `IsRetryable()` 对它恒 false ⇒ 该类瞬时故障永久不重试）。
//	只有**结构面（AST）**能抓住"声明了但没登记"。
//
// ⚠️ 已知边界（必须写下来，否则下一个人会高估它）：
// - 只扫 **errors.go 一个文件**。若将来 ErrorID 常量被挪到别的文件，
// 本门必须同步扩面（否则静默失效）。
// - 只看**显式声明 `ErrorID` 类型**的常量，外加一条**命名约定兜底**
// （名字以 `Err` 开头且值为字符串字面量）—— 覆盖 `X = "..."` 省略类型的写法。
// - 本用例**运行期自读磁盘**（go/parser + nil src + 相对路径）
// ⇒ 按本仓铁律，`-overlay` 对它**物理无效**：判别性只能靠
// 「受控写回 + 无条件还原 + md5 校验」证明（实测见回执）。

package ai

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// TestErrorCategoryASTGate_EveryDeclaredIDIsRegistered 是本单的结构核心。
//
// 两侧都从 AST 取，**不依赖任何运行期符号**（`errorCategorySpecs` / `AllErrorIDs`
// 在本用例里都只是**字符串**，不是被引用的符号）——
// 这正是它能对"两侧都忘登记"这一失败模式保持灵敏的原因。
func TestErrorCategoryASTGate_EveryDeclaredIDIsRegistered(t *testing.T) {
	declared := errorIDDeclaredConsts(t)
	registered, found := errorIDRegisteredSpecs(t)

	if !found {
		t.Fatalf("未能在 errors.go 里找到 `var errorCategorySpecs` 的 map 复合字面量 —— " +
			"语义表的形态被改动了，本门已失去判别对象（这是**门失效**，不是「没登记」）。" +
			"若确实要改形态，请同步更新本门与其头注释声明的边界。")
	}
	if len(declared) == 0 {
		t.Fatalf("未能在 errors.go 里扫到任何 `ErrorID` 常量 —— 扫描器失灵或常量已搬家，本门已失去判别对象")
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
		t.Errorf("errorCategorySpecs **漏登记**了已声明的 ErrorID 常量：%s\n"+
			"  ⇒ 该 ErrorID 在 categoryFromID 里静默继承 CategoryFatal（IsRetryable() 恒 false）。\n"+
			"  ⇒ 新增 ErrorID 必须在 errorCategorySpecs 里显式声明 Category，"+
			"**不得**靠 default 兜底（default 无法区分「显式 Fatal」与「忘记登记」）。",
			strings.Join(missing, ", "))
	}
	if len(drifted) > 0 {
		t.Errorf("errorCategorySpecs 里的 %s 在 errors.go 里**找不到对应的常量声明**（两份清单已漂移）：\n"+
			"  ⇒ 要么是改名后没同步，要么是登记了一个不存在的 ID。",
			strings.Join(drifted, ", "))
	}
}

// errorIDDeclaredConsts 返回 errors.go 里**声明为 ErrorID** 的常量名集合。
//
// 判定规则（两条并集，避免只靠一种写法）：
// 1. const 块里 `Type` 为标识符 `ErrorID` 的 ValueSpec 的名字；
// 2. const 块里名字以 `Err` 开头、且值为字符串字面量的 ValueSpec 的名字
// —— 兜住 `ErrXxx = "xxx"`（省略类型）的写法。
func errorIDDeclaredConsts(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	// ★ 读盘型：相对路径 + nil src ⇒ 解析的是磁盘上的 errors.go
	// （测试工作目录即包目录）。
	file, err := parser.ParseFile(fset, errorCategorySourceFile, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", errorCategorySourceFile, err)
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
			if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "ErrorID" {
				typed = true
			}
			if !typed && !errorIDLooksLikeIDConst(vs) {
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

// errorIDLooksLikeIDConst 是命名约定兜底：名字前缀 `Err` ＋ 值是字符串字面量。
func errorIDLooksLikeIDConst(vs *ast.ValueSpec) bool {
	if len(vs.Values) == 0 {
		return false
	}
	for _, n := range vs.Names {
		if !strings.HasPrefix(n.Name, "Err") {
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

// errorIDRegisteredSpecs 返回 `var errorCategorySpecs = map[ErrorID]Category{...}`
// 里**已登记的 Key 名**集合。第二个返回值报告"是否找到了该 map 字面量"。
//
// 只取 map 复合字面量的**直接** KeyValueExpr 键。
func errorIDRegisteredSpecs(t *testing.T) (map[string]bool, bool) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, errorCategorySourceFile, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", errorCategorySourceFile, err)
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
				if n.Name == errorCategorySpecsVarName {
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
	// errorCategorySourceFile 是语义表的声明处（本门只扫这一个文件，见头注释「已知边界」）。
	errorCategorySourceFile = "errors.go"
	// errorCategorySpecsVarName 是语义表的变量名 —— 门与被守卫对象之间的**契约名**。
	errorCategorySpecsVarName = "errorCategorySpecs"
)
