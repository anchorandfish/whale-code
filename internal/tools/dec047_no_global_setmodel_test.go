package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestDEC047_NoGlobalSetModelInProduction 是**结构面防复发门**：
// internal/tools 的生产代码（非 _test.go）里不得出现任何 `.SetModel(` 调用。
//
// 为什么需要这条门：
//
//	AIClient.SetModel 是**进程级全局改写**。tools 层是"多个子 agent 并发执行"的地方，
//	在这里改全局 model，必然出现「A 设 X → B 读到 X 也设 Y → A 恢复旧值 → B 恢复 Y
//	⇒ 全局永久停在 Y」的污染，且**没有任何报错**。
//	模型是请求级参数，应经 ai.Request.Model 传递；全局字段只保存缺省值。
//
// 淘汰的两种旧写法（本门抓的就是它们）：
// 1. agent_task.go runMultiRound：SetModel(input.Model) + defer SetModel(original)
// 2. web.go skillHandler ：SetModel(result.Model) + defer SetModel(orig)
// （后者更糟：位于 AI 调用**之后**，从未生效 —— 死代码。）
//
// ⚠️ 本门是「命中即红」型读盘门（R4-8b）：
// 判别性验证方式 = 在包内临时放一个调用 `.SetModel(` 的私有探针文件，本门必须变红。
func TestDEC047_NoGlobalSetModelInProduction(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("列举源文件失败: %v", err)
	}

	type hit struct {
		file string
		line int
	}
	var hits []hit
	scanned := 0

	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", f, err)
		}
		scanned++

		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel == nil || sel.Sel.Name != "SetModel" {
				return true
			}
			hits = append(hits, hit{file: f, line: fset.Position(call.Pos()).Line})
			return true
		})
	}

	// 前置自检：必须真的扫到生产文件，否则本门静默恒绿（R4-7 的"断言目标被解析到"）。
	if scanned == 0 {
		t.Fatal("未扫描到任何生产 .go 文件 ⇒ 本门失效（恒绿），检查工作目录或文件名规则")
	}
	t.Logf("已扫描 %d 个生产源文件", scanned)

	if len(hits) == 0 {
		return
	}

	var b strings.Builder
	b.WriteString("在 tools 生产代码中发现了进程级全局 model 改写（ 禁止）：\n")
	for _, h := range hits {
		b.WriteString("  ")
		b.WriteString(h.file)
		b.WriteString(":")
		b.WriteString(itoa(h.line))
		b.WriteString("  .SetModel(...)\n")
	}
	b.WriteString("\n为什么禁止：多个子 agent 并发时会互相覆盖，且全局值可能永久停在某个子任务的模型上。\n")
	b.WriteString("正确做法：把模型写进 ai.Request.Model（AgentContext.ModelOverride 已提供该通道）。\n")
	t.Fatal(b.String())
}

// itoa 避免为一个数字引入 strconv 的额外 import 噪声（保持本文件依赖最小）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
