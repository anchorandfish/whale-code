package tools

// ============================================================================
// S2′ 结构门（机械门）
// ============================================================================
//
// 这四条不测"行为对不对"，而测"结构上还会不会再退回去"——都是本次裁定里
// 容易被后来者无声推翻、且推翻后**判据会变成假绿**的点。
//
// 装在 go test 里，所以 CI 每次都跑；不靠人记得跑脚本。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"strings"
	"testing"
)

func parsePackageSource(t *testing.T, path string) (*token.FileSet, *ast.File) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
	return fset, f
}

func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// TestGate_DoWebSearchTransportMustHonorTestFlag
//
// doWebSearch 的 transport 实参不得是字面 false。
//
// 为什么它是门：判据必须用 httptest 在**离线**下走生产入口，而 httptest 监听 127.0.0.1；
// 一旦有人"为了安全"把它改回 createSSRFSafeTransport(false)，
// 所有离线夹具会被自身 SSRF 拦掉 ⇒ 判据整体失效（同族：声明了却不可判别）。
// 生产安全边界由 AllowLocalhostForTest 的**零值 false** 保证，不靠这一行硬编码。
func TestGate_DoWebSearchTransportMustHonorTestFlag(t *testing.T) {
	_, f := parsePackageSource(t, "web.go")

	checked := 0
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "doWebSearch" {
			return true
		}
		ast.Inspect(fn, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok || calleeName(call) != "createSSRFSafeTransport" {
				return true
			}
			checked++
			if len(call.Args) != 1 {
				t.Fatalf("createSSRFSafeTransport 期望 1 个实参，实际 %d", len(call.Args))
			}
			if id, isIdent := call.Args[0].(*ast.Ident); isIdent && id.Name == "false" {
				t.Fatalf("doWebSearch 的 transport 被硬编码成 false：离线判据会被自身 SSRF 拦掉（判据物理无效）。" +
					"生产零值已保证不放行回环/内网，请改回 e.AllowLocalhostForTest。")
			}
			return true
		})
		return true
	})

	if checked != 1 {
		t.Fatalf("期望 doWebSearch 内恰好 1 处 createSSRFSafeTransport 调用，实际 %d", checked)
	}
}

// searchResponsePathFuncs 是本门的作用域：检索的**响应侧**函数。
// 显式列举而不是"整个包"：WebFetch 的 content[:30000]、通用 truncateStr 的 s[:maxLen]
// 都是合法切分，把它们算进来只会制造噪音（第一版就误报了 3 处）。
// 新增响应侧函数时把它加进来。
var searchResponsePathFuncs = map[string]bool{
	"webSearchHandler":    true,
	"doWebSearch":         true,
	"parseSearchResults":  true,
	"sniffSearchResults":  true,
	"formatSearchResults": true,
}

// TestGate_NoRawBodySliceInReturn
//
// 检索响应侧函数的返回值里不得出现对原始响应的**切片直传**。
//
// 为什么：改造前 parseSearchResults 有 3 处 `return string(body[:min(len(body), 2000)])` ——
// 它把 HTML 登录页、`{"code":401,...}` 原样以"成功"身份交给模型（/ 同族幻觉入口）。
// 注意：doWebSearch 里给 401 报文截 200B **不属于**本门（那是错误归因，不是返回成功值，
// 且已过 redactSearchSecrets）；本门只盯 ReturnStmt。
// 嵌套函数字面量会被跳过（`return func(...) Handler {...}` 不是直传）；
// handler 闭包的行为由夹具（I-html/J-errenv/…）从生产入口覆盖。
func TestGate_NoRawBodySliceInReturn(t *testing.T) {
	for _, file := range []string{"web.go", "web_search_contract.go"} {
		fset, f := parsePackageSource(t, file)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !searchResponsePathFuncs[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if _, isFuncLit := n.(*ast.FuncLit); isFuncLit {
					return false
				}
				ret, isReturn := n.(*ast.ReturnStmt)
				if !isReturn {
					return true
				}
				for _, res := range ret.Results {
					hasSlice := false
					ast.Inspect(res, func(m ast.Node) bool {
						if _, isSlice := m.(*ast.SliceExpr); isSlice {
							hasSlice = true
						}
						return true
					})
					if hasSlice {
						t.Errorf("%s:%d （函数 %s）检索响应侧不得把原始响应按切片直接返回"+
							"（原文直通 ⇒ 把 HTML 汤/错误信封当检索结果）。唯一出口应是 parseSearchResults。",
							file, fset.Position(ret.Pos()).Line, fn.Name.Name)
					}
				}
				return true
			})
		}
	}
}

// TestGate_SearchErrorPathsMustRedact
//
// doWebSearch 里两条会把上游文本带进错误的出口都必须经过 redactSearchSecrets：
//
//	① 网络错误：net/http 的客户端错误自带完整 URL，query-key 形态下 key 就在 query 里
//	② 非 200：截取 200B 上游报文回显（可能含上游回显的凭据）
func TestGate_SearchErrorPathsMustRedact(t *testing.T) {
	_, f := parsePackageSource(t, "web.go")

	count := 0
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "doWebSearch" {
			return true
		}
		ast.Inspect(fn, func(m ast.Node) bool {
			if call, isCall := m.(*ast.CallExpr); isCall && calleeName(call) == "redactSearchSecrets" {
				count++
			}
			return true
		})
		return true
	})

	if count < 2 {
		t.Fatalf("doWebSearch 的两条错误出口都应经 redactSearchSecrets（网络错误 / 非 200 报文），实际只找到 %d 处", count)
	}
}

// bodyTemplateContainsPlaceholder 递归检查请求体模板里是否用了指定占位符。
func bodyTemplateContainsPlaceholder(v any, want bodyPlaceholder) bool {
	switch tpl := v.(type) {
	case bodyPlaceholder:
		return tpl == want
	case map[string]any:
		for _, val := range tpl {
			if bodyTemplateContainsPlaceholder(val, want) {
				return true
			}
		}
	case []any:
		for _, val := range tpl {
			if bodyTemplateContainsPlaceholder(val, want) {
				return true
			}
		}
	}
	return false
}

// TestGate_SearchContractTableSelfCheck 契约表自身的完整性。
//
// 防的是"加了一行契约但忘了配请求形态" —— 那种半成品只会在真机上才暴露（而真机手测最贵）。
func TestGate_SearchContractTableSelfCheck(t *testing.T) {
	contracts := builtinSearchContracts()
	if len(contracts) == 0 {
		t.Fatal("契约表为空")
	}

	for name, c := range contracts {
		if c.Name != name {
			t.Errorf("契约 %s 的 Name 字段是 %q（两者必须一致，否则错误归因会指错引擎）", name, c.Name)
		}
		switch c.Method {
		case http.MethodGet:
			if len(c.QueryParams) == 0 && c.EngineIDParam == "" {
				t.Errorf("GET 契约 %s 未定义任何查询参数：查询词发不出去", name)
			}
			if c.Body != nil {
				t.Errorf("GET 契约 %s 不该有请求体模板", name)
			}
		case http.MethodPost:
			if len(c.Body) == 0 {
				t.Errorf("POST 契约 %s 没有请求体模板：等于在最需要它的引擎上发不出请求", name)
			} else if !bodyTemplateContainsPlaceholder(c.Body, bodyPlaceholderQuery) {
				t.Errorf("POST 契约 %s 的请求体模板没有 %s 占位符：查询词根本没发出去", name, bodyPlaceholderQuery)
			}
		default:
			t.Errorf("契约 %s 的请求方法 %q 非法", name, c.Method)
		}

		switch c.Auth.Mode {
		case searchAuthNone:
		case searchAuthQueryKey:
			if c.Auth.QueryParam == "" {
				t.Errorf("契约 %s 用 query-key 鉴权但没给参数名", name)
			}
		case searchAuthBearer, searchAuthHeaderToken:
			if len(c.Auth.Headers) == 0 {
				t.Errorf("契约 %s 用头部鉴权但没给头名", name)
			}
		default:
			t.Errorf("契约 %s 的鉴权形态 %q 非法（显式配置要求取值来自闭集）", name, c.Auth.Mode)
		}
	}

	// 用户点名的两类境内引擎必须在表内，且必须是 POST（官方文档要求）。
	for _, want := range []string{"bocha", "qianfan"} {
		c, ok := contracts[want]
		if !ok {
			t.Fatalf("契约表缺少 %s", want)
		}
		if c.Method != http.MethodPost {
			t.Errorf("%s 官方要求 POST，契约表里是 %s", want, c.Method)
		}
	}

	// Bing 不得作为可用引擎出现（2025-08-11 已退役，替代品不返回原始结果 JSON）。
	for name := range contracts {
		if strings.Contains(name, "bing") {
			t.Errorf("契约表出现 %s：Bing Search API 已于 2025-08-11 退役，"+
				"替代品 Grounding with Bing Search 不返回原始结果 JSON，不得写入可用承诺", name)
		}
	}
}
