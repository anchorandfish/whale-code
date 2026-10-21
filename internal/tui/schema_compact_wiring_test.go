package tui

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// 本文件钉住 P0-2 的**接线**（不是纯函数本身 —— 那在 internal/tools/schema_compact_test.go）。
//
// 为什么接线需要单独的判据：模型可见的 tools 数组有**两处**构造点
// （runnerCore.buildToolDefs 覆盖 PlainRunner/Bridge；Model.buildToolDefs 覆盖 TUI），
// 它们历史上就漂移过（见 stream.go:977 的"三模式一致"注释）。
// 只改一处 = 假修复：另一个模式照样把超大 MCP schema 原样发出去。

// TestBuildToolDefs_CompactsOversizedMCPSchema 是端到端判据：从**生产入口** buildToolDefs 进入。
// 注册一个超大 MCP 工具 ⇒ 模型侧 schema 必须有界，且 registry 本体不受污染。
func TestBuildToolDefs_CompactsOversizedMCPSchema(t *testing.T) {
	runner, _ := newTestBridgeRunner()

	huge := map[string]interface{}{
		"type":        "object",
		"description": strings.Repeat("x", 6000),
		"properties": map[string]interface{}{
			"q": map[string]interface{}{"type": "string", "description": strings.Repeat("y", 2000)},
		},
		"required": []string{"q"},
	}
	if len(mustMarshal(t, huge)) <= tools.MaxModelSchemaBytes {
		t.Fatalf("测试前提失效：构造的 MCP schema 未超预算")
	}
	if err := runner.Registry.RegisterMCP(tools.Spec{
		Name:        "mcp_huge_tool",
		Description: "oversized MCP tool",
		InputSchema: huge,
	}); err != nil {
		t.Fatalf("RegisterMCP: %v", err)
	}

	defs := runner.buildToolDefs()
	idx := -1
	for i := range defs {
		if defs[i].Name == "mcp_huge_tool" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("buildToolDefs 未包含注册的 MCP 工具（注册或筛选逻辑变了？）")
	}

	// ① 模型侧：有界
	modelSide := mustMarshal(t, defs[idx].InputSchema)
	if len(modelSide) > tools.MaxModelSchemaBytes {
		t.Errorf("模型侧 schema 未被压到预算内：%d > %d", len(modelSide), tools.MaxModelSchemaBytes)
	}

	// ② 本体：原样（SchemaValidateStage 用的就是这份，被压坏 ⇒ 模型按残缺 schema 传参会被拒）
	back, ok := runner.Registry.Get("mcp_huge_tool")
	if !ok {
		t.Fatal("registry 里找不到 MCP 工具了")
	}
	localSide := mustMarshal(t, back.InputSchema)
	if len(localSide) <= tools.MaxModelSchemaBytes {
		t.Errorf("registry 本体被就地压缩了（应保持原样）：%d 字节", len(localSide))
	}
}

func mustMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestBuildToolDefsGate_BothSitesProjectSchema 是**双实现机械门**（复发坑）。
// 两处 buildToolDefs（runner_core.go / stream.go）都必须经过 tools.CompactSchemaForModel。
// 只覆盖一侧 ⇒ 另一侧（某一运行模式）静默绕过预算 ⇒ 本门必红。
//
// ★ 读盘型 AST 门：相对路径 + nil src ⇒ 解析磁盘源码（测试工作目录即包目录）。
func TestBuildToolDefsGate_BothSitesProjectSchema(t *testing.T) {
	sites := []string{"runner_core.go", "stream.go"}
	fset := token.NewFileSet()

	for _, path := range sites {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", path, err)
		}
		sawDecl, projected := false, false
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Name.Name != "buildToolDefs" || fd.Body == nil {
				continue
			}
			sawDecl = true
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "CompactSchemaForModel" {
					return true
				}
				projected = true
				return false
			})
		}
		if !sawDecl {
			t.Errorf("%s 里找不到 buildToolDefs 函数 —— 门的判别对象搬家了，"+
				"本门已失效（请同步更新本门，而不是忽略它）", path)
			continue
		}
		if !projected {
			t.Errorf("%s 的 buildToolDefs 未经过 tools.CompactSchemaForModel：\n"+
				"  ⇒ 该运行模式会把外部 MCP 的无上限 schema 原样发给模型（P0-2 失效）。\n"+
				"  ⇒ 两处构造点必须共用同一投影，任一漏改都算假修复。", path)
		}
	}
}
