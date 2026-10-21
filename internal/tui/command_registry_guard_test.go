package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"testing"

	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/memory"
)

// command_registry_guard_test.go — C-5 守卫门（发布1版分派清单块 C · 2026-10-10）
//
// 目标：TUI 侧「命令声明面」与 internal/command 注册表双向对齐，防止两个漂移方向：
//
//   正向（假命令/死 case）：handleCommand 的 switch case 名（及 isTUICommandHandledLocally
//     的名单）不在注册表 ⇒ 永远走不到 —— handleCommand 入口先 cmdRegistry.Get(name)，
//     未注册直接回 "Unknown"（app.go handleCommand 首个 if）。case 体成死代码，
//     用户敲该命令永远 Unknown。实测案例（本门首跑即红）：mark/unmark/tag ——
//     P0-2 注释自称「保留诚实降级提示」，但因从未注册，提示从未展示过。
//
//   反向（死命令）：注册表中可见命令（非 Hidden 非 Stub），既无 TUI case 又无
//     Handler ⇒ 无任何执行路径（有 Handler 则走 handleCommand 的通用 cmd.Handler 通道）。
//
// 判别性自证（R4-8b 正向注入，不改磁盘）：本门读磁盘源码（go/parser 运行期解析），
// -overlay 物理无效 ⇒ 判别性改用「合成源码注入」：向纯函数 parseSwitchCasesInFunc
// 注入一段含未注册 case 名的合成 handleCommand，断言能被抓出。真实文件的解析正确性
// 由「解析数 ≥ 40」结构自检兜底（解析失败/空集 = 门静默失真，必须红）。

// parseSwitchCasesInFunc 解析 src 中指定函数体内 `switch name` 的全部 case 字符串字面量。
// 多名 case（case "a", "b"）逐个展开；非字符串字面量的 case 表达式忽略（当前不存在）。
// 找不到目标函数或目标 switch 时返回 error —— 门不许在「没读到」时静默通过。
func parseSwitchCasesInFunc(src, funcName string) (map[string]bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, funcName+".go", src, 0)
	if err != nil {
		return nil, err
	}
	cases := map[string]bool{}
	foundFunc := false
	foundSwitch := false
	ast.Inspect(file, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		if fd.Name.Name != funcName {
			return true
		}
		foundFunc = true
		ast.Inspect(fd.Body, func(b ast.Node) bool {
			ss, ok := b.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			ident, ok := ss.Tag.(*ast.Ident)
			if !ok || ident.Name != "name" {
				return true
			}
			foundSwitch = true
			for _, stmt := range ss.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range cc.List {
					bl, ok := expr.(*ast.BasicLit)
					if !ok {
						continue
					}
					// BasicLit.Value 自带引号，去掉后取字面值（go/constant 更稳，这里值形态受控）
					v := bl.Value
					if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
						v = v[1 : len(v)-1]
					}
					cases[v] = true
				}
			}
			return true
		})
		return false
	})
	if !foundFunc {
		return nil, errFuncNotFound(funcName)
	}
	if !foundSwitch {
		return nil, errSwitchNotFound(funcName)
	}
	return cases, nil
}

type guardError string

func (e guardError) Error() string { return string(e) }

func errFuncNotFound(fn string) error   { return guardError("guard: function not found: " + fn) }
func errSwitchNotFound(fn string) error { return guardError("guard: `switch name` not found in " + fn) }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// registryWithMemoryCommands 构造与生产一致的注册表（lifecycle.go:361 NewRegistry +
// memory_wiring.go:146 memory.RegisterCommands）。
func registryWithMemoryCommands(t *testing.T) *command.Registry {
	t.Helper()
	reg := command.NewRegistry()
	if err := memory.RegisterCommands(reg); err != nil {
		t.Fatalf("memory.RegisterCommands failed: %v", err)
	}
	return reg
}

// TestTUICommandDeclarations_AlignWithRegistry 双向对齐主门。
func TestTUICommandDeclarations_AlignWithRegistry(t *testing.T) {
	src, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatalf("读不到 app.go —— 门失真（必须红，不许静默绿）: %v", err)
	}
	cases, err := parseSwitchCasesInFunc(string(src), "handleCommand")
	if err != nil {
		t.Fatalf("解析 handleCommand 失败 —— 门失真: %v", err)
	}
	localNames, err := parseSwitchCasesInFunc(string(src), "isTUICommandHandledLocally")
	if err != nil {
		t.Fatalf("解析 isTUICommandHandledLocally 失败 —— 门失真: %v", err)
	}
	// 结构自检：解析数过低 = 源码结构变了或解析器静默失效，不许绿。
	if len(cases) < 40 {
		t.Fatalf("handleCommand 仅解析出 %d 个 case（预期 ≥40）—— 解析器可能静默失真", len(cases))
	}
	if len(localNames) < 40 {
		t.Fatalf("isTUICommandHandledLocally 仅解析出 %d 个名字（预期 ≥40）—— 解析器可能静默失真", len(localNames))
	}

	reg := registryWithMemoryCommands(t)
	registered := map[string]bool{}
	for _, c := range reg.All() {
		registered[c.Name] = true
	}

	// 正向：TUI 声明面（switch case + 本地处理名单）⊆ 注册表。
	var fake []string
	for _, name := range sortedKeys(cases) {
		if !registered[name] {
			fake = append(fake, "case "+name)
		}
	}
	for _, name := range sortedKeys(localNames) {
		if !registered[name] {
			fake = append(fake, "localList "+name)
		}
	}
	if len(fake) > 0 {
		t.Fatalf("TUI 声明了注册表里不存在的命令（handleCommand 先 Get() ⇒ 永远 Unknown，case/名单为死代码）:\n  %v\n修复方向：注册该命令，或删掉 TUI 侧声明", fake)
	}

	// 反向：注册表可见命令必须有执行路径（TUI case 或非 nil Handler）。
	var dead []string
	for _, c := range reg.All() {
		if c.Hidden || c.Stub {
			continue
		}
		if !cases[c.Name] && c.Handler == nil {
			dead = append(dead, c.Name)
		}
	}
	if len(dead) > 0 {
		t.Fatalf("注册表可见命令无任何执行路径（无 TUI case 且 Handler 为 nil）: %v", dead)
	}
}

// TestCommandCaseGuard_Discriminability 判别性自证：注入未注册 case 名必须能被抓出。
func TestCommandCaseGuard_Discriminability(t *testing.T) {
	synthetic := `package tui

func (m *Model) handleCommand(input string) string {
	switch name {
	case "zz-unregistered-probe", "compact":
		return ""
	}
	return ""
}
`
	cases, err := parseSwitchCasesInFunc(synthetic, "handleCommand")
	if err != nil {
		t.Fatalf("合成源码解析失败: %v", err)
	}
	if !cases["zz-unregistered-probe"] || !cases["compact"] {
		t.Fatalf("解析器没抓全注入的 case 名（got %v）—— 判别器失效", sortedKeys(cases))
	}
	reg := registryWithMemoryCommands(t)
	if _, ok := reg.Get("zz-unregistered-probe"); ok {
		t.Fatal("探针名意外已注册 —— 换个名字重做本测试")
	}
	// 主门逻辑判定：该 case 会被正向检查打红。
	if registered := func() bool {
		_, ok := reg.Get("zz-unregistered-probe")
		return ok
	}(); registered {
		t.Fatal("unreachable")
	}
	// 双重断言：注入名不在注册表 ⇒ 主门正向检查必然对它报 fake。
}
