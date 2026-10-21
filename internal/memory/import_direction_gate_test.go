// 移交单 · 依赖方向 —— **import 方向 AST 机械门**。
//
// 被守卫的不变量：
//
//	internal/memory 包（含生产码与测试）的**任何** .go 文件都**不得** import
//	`github.com/anchorandfish/whale-code/internal/orchestrator`（高层编排层）。
//
// 为什么必须有它（根因，不是"某行写错"）：
//
//	低层（memory，H11/N27）反向 import 高层（orchestrator，H10）是依赖方向倒置：
//	消费者自持了记录格式的版本常量/字段名副本，生产者改 schema 语义时无任何强制力
//	（drift 探测器在生产中恒不响）。本次修复把记录类型/读取契约下沉到零依赖叶子包
//	internal/orchrecord，反向边消失 —— 但「消失」本身需要一道机械门防复发：
//	将来任何人再往 memory 里加回 `import .../orchestrator`，Go 编译器**不会**报错
//	（它合法），只会让反向依赖悄然复活。只有结构面（AST）能抓住。
//
// ⚠️ 已知边界（必须写下来，否则下一个人会高估它）：
// - 只扫 internal/memory **一个目录**。本门守护的是「memory → orchestrator」这一条
// 已知的反向边；orchestrator → memory 的正向边不在本门职责内。
// - 本用例**运行期自读磁盘**（go/parser + 相对路径）⇒ 按本仓铁律，
// `-overlay` 对它**物理无效**：判别性只能靠「受控写回 + 无条件还原 + md5 校验」
// 证明（见移交单 回执）。
// - 扫描范围含 `*_test.go`：测试里 import 高层同样是反向依赖的复发形态
// （本仓 compact_test.go 曾 import orchestrator）。
package memory

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// memoryForbiddenOrchestratorImport 是反向依赖的唯一判定字符串。
// 若将来包路径改名，须同步更新本常量与本门头注释。
const memoryForbiddenOrchestratorImport = "github.com/anchorandfish/whale-code/internal/orchestrator"

// TestMemoryImportDirectionGate_NoOrchestratorImport 是本单的结构核心。
//
// 扫描 internal/memory 目录下所有 .go 文件（生产 + 测试）的 import 块，
// 断言没有任何文件 import orchestrator。任一命中即红，并点名文件与行号。
func TestMemoryImportDirectionGate_NoOrchestratorImport(t *testing.T) {
	dir := memoryPackageDir(t)
	offenders := scanForbiddenImports(t, dir, memoryForbiddenOrchestratorImport)

	if len(offenders) > 0 {
		var sb strings.Builder
		for _, o := range offenders {
			sb.WriteString(o)
			sb.WriteString("\n")
		}
		t.Fatalf("internal/memory 反向 import 高层 orchestrator（依赖方向倒置， 复发）：\n%s"+
			"  ⇒ 记录类型/读取契约已下沉到 internal/orchrecord，请改 import 该叶子包，"+
			"不得重新 import orchestrator。",
			sb.String())
	}
}

// memoryPackageDir 定位 internal/memory 包目录（相对测试工作目录 = 包目录）。
func memoryPackageDir(t *testing.T) string {
	t.Helper()
	// 测试工作目录即包目录（go test 的 CWD = 包目录），返回 "."。
	// 用 os.Getwd 兜底并断言包内存在 compact.go，防 CWD 意外漂移。
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wd, "compact.go")); err != nil {
		t.Fatalf("未能在 %s 找到 compact.go —— 测试工作目录不在 memory 包目录", wd)
	}
	return wd
}

// scanForbiddenImports 返回目录下所有 import 了 forbidden 的 .go 文件与行号，形如 "file.go:12"。
func scanForbiddenImports(t *testing.T, dir, forbidden string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录 %s 失败: %v", dir, err)
	}

	var offenders []string
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", path, err)
		}
		for _, imp := range file.Imports {
			// import path 是字符串字面量，去掉两侧引号
			p := strings.Trim(imp.Path.Value, `"`)
			if p == forbidden {
				offenders = append(offenders,
					path+"："+fset.Position(imp.Pos()).String())
			}
		}
	}
	sort.Strings(offenders)
	return offenders
}
