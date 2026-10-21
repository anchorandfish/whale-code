package tui

// —— 面 1「接线面」机械门 —— NewModel 内值捕获闭包 × 本副本字段访问基线 ————————
//
// 背景（2026-09-20 机械实测，见 内部工作回执 §10）：
//
//	NewModel（lifecycle.go:123）**按值返回**，函数体内 `m := Model{...}`（:266）是值局部。
//	该函数体内 44 处 `m.cmdRegistry.OnXxx = func(...){...}` 全部**捕获那个值局部**。
//	于是回调体里的 `m` 永远是 NewModel 结束时刻的快照。
//
//	全包 94 个 `X.<Reg>.On<Name> = ...` 注册点里，**只有这 44 处**的根变量是值局部；
//	其余 50 处的根是 `runner` / `rc` / `core` / `c` / `r` / 指针接收者 `m` ⇒ 跨副本共享同一对象。
//
//	「读快照」这件事本身不一定错——只有当被读字段**在构造后会被改**时才错。
//	所以本门只钉住一个可判定的结构面：
//
// ① 每个值捕获闭包**传递可达**的「副本字段访问」必须逐条登记在基线里（未登记即红）。
// ② 基线每条必须写明**为什么安全**（缺理由即红）—— 防止基线变成"什么都放行"。
// ③ 不变量：`workDir` / `homeDir` 全包**零写点**（6 个活线闭包的安全性就压在这上面）。
//
// ★ 与 §10 的审计工具**同源**：本文件既跑门（go test）也打印全表（go test -v）。
// 不另立第二份实现，避免"门的判据"和"审计的判据"分叉。
//
// 判别力（before 必须红）：在任一当前未登记的闭包里加一次 `_ = m.remoteMode`
// ⇒ ① 红；把 `m.workDir = "x"` 加到任一处 ⇒ ③ 红。两者都实测过（见 §10.4）。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ——————————————————————————————————————————————————————————————
// 类型层
// ——————————————————————————————————————————————————————————————

type vcKind int

const (
	vcShared  vcKind = iota // 指针 / 接口 / map / chan / func / 切片头 ⇒ 跨副本共享
	vcValue                 // 基本类型 / 结构体 / 数组 ⇒ 按值复制
	vcUnknown               // 无法在语法层定（跨包类型且包源码不可达）
)

func (k vcKind) String() string {
	switch k {
	case vcShared:
		return "共享"
	case vcValue:
		return "按值"
	default:
		return "待判"
	}
}

type vcAnalyzer struct {
	fset      *token.FileSet
	files     map[string]*ast.File
	fileNames []string
	ifaces    map[string]bool
	structs   map[string]map[string]vcField
	model     map[string]vcField
	methods   map[string]*vcMethod
	aliasPath map[string]string
	modRoot   string
	extCache  map[string]vcKind
	typeExpr  map[string]ast.Expr
}

type vcField struct {
	Owner string
	Kind  vcKind
	Type  string
}

type vcMethod struct {
	Name    string
	PtrRecv bool
	Body    *ast.BlockStmt
	Line    int
}

func vcExprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + vcExprString(t.X)
	case *ast.SelectorExpr:
		return vcExprString(t.X) + "." + t.Sel.Name
	case *ast.MapType:
		return "map[" + vcExprString(t.Key) + "]" + vcExprString(t.Value)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + vcExprString(t.Elt)
		}
		return "[...]" + vcExprString(t.Elt)
	case *ast.ChanType:
		return "chan " + vcExprString(t.Value)
	case *ast.FuncType:
		return "func(...)"
	case *ast.InterfaceType:
		return "interface{...}"
	case *ast.BasicLit:
		return t.Value
	}
	return fmt.Sprintf("%T", e)
}

var vcBasicValueTypes = map[string]bool{
	"string": true, "bool": true, "int": true, "int8": true, "int16": true,
	"int32": true, "int64": true, "uint": true, "uint8": true, "uint16": true,
	"uint32": true, "uint64": true, "uintptr": true, "float32": true,
	"float64": true, "complex64": true, "complex128": true, "byte": true, "rune": true,
}

func (a *vcAnalyzer) kindOf(expr ast.Expr) (vcKind, string) {
	k, tp := a.kindOfLiteral(expr)
	if k == vcUnknown {
		if id, ok := expr.(*ast.Ident); ok {
			if u, ok := a.typeExpr[id.Name]; ok && u != expr {
				return a.kindOf(u)
			}
		}
	}
	return k, tp
}

func (a *vcAnalyzer) kindOfLiteral(expr ast.Expr) (vcKind, string) {
	switch t := expr.(type) {
	case *ast.StarExpr, *ast.MapType, *ast.ChanType, *ast.FuncType, *ast.InterfaceType:
		return vcShared, vcExprString(expr)
	case *ast.ArrayType:
		if t.Len == nil {
			return vcShared, vcExprString(expr)
		}
		return vcValue, vcExprString(expr)
	case *ast.StructType:
		return vcValue, "struct{...}"
	case *ast.Ident:
		if a.ifaces[t.Name] {
			return vcShared, t.Name + "(iface)"
		}
		if vcBasicValueTypes[t.Name] {
			return vcValue, t.Name
		}
		if t.Name == "error" {
			return vcShared, "error"
		}
		if _, ok := a.structs[t.Name]; ok {
			return vcValue, t.Name + "(struct)"
		}
		return vcUnknown, t.Name
	case *ast.SelectorExpr:
		if k, ok := vcKnownExternalKinds[vcExprString(expr)]; ok {
			return k, vcExprString(expr)
		}
		return a.resolveExternal(vcExprString(t.X), t.Sel.Name)
	}
	return vcUnknown, vcExprString(expr)
}

// resolveExternal 读被导入包自己的源码定类型，而不是猜。
func (a *vcAnalyzer) resolveExternal(alias, name string) (vcKind, string) {
	key := alias + "." + name
	if k, ok := a.extCache[key]; ok {
		return k, key
	}
	a.extCache[key] = vcUnknown
	path, ok := a.aliasPath[alias]
	if !ok || a.modRoot == "" {
		return vcUnknown, key
	}
	mp := vcModulePath(a.modRoot)
	if mp == "" || !strings.HasPrefix(path, mp) {
		return vcUnknown, key
	}
	dir := filepath.Join(a.modRoot, strings.TrimPrefix(strings.TrimPrefix(path, mp), "/"))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return vcUnknown, key
	}
	sub := &vcAnalyzer{
		fset: token.NewFileSet(), ifaces: map[string]bool{}, structs: map[string]map[string]vcField{},
		typeExpr: map[string]ast.Expr{}, aliasPath: a.aliasPath, modRoot: a.modRoot,
		extCache: a.extCache,
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(sub.fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			continue
		}
		sub.collectTypes(f)
		if _, ok := sub.structs[name]; ok {
			sub.collectTypeExprs(f)
		}
	}
	if _, ok := sub.ifaces[name]; ok {
		a.extCache[key] = vcShared
		return vcShared, key
	}
	if _, ok := sub.structs[name]; ok {
		a.extCache[key] = vcValue
		return vcValue, key
	}
	return vcUnknown, key
}

// vcKnownExternalKinds：模块**外**的类型（源码不在本仓，解析不到）——只登记逐条核对过的。
// 未登记的模块外类型一律落 vcUnknown（fail-closed，会要求登记基线理由）。
var vcKnownExternalKinds = map[string]vcKind{
	// bubbletea：`type Cmd func() Msg` 是函数值 ⇒ 副本共享同一闭包。
	"tea.Cmd": vcShared,
}

func vcModulePath(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(ln, "module "))
		}
	}
	return ""
}

func vcFindModRoot(dir string) string {
	d := dir
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		nd := filepath.Dir(d)
		if nd == d {
			break
		}
		d = nd
	}
	return ""
}

func (a *vcAnalyzer) collectTypes(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		switch t := ts.Type.(type) {
		case *ast.InterfaceType:
			a.ifaces[ts.Name.Name] = true
		case *ast.StructType:
			m := map[string]vcField{}
			for _, fld := range t.Fields.List {
				k, tp := a.kindOf(fld.Type)
				for _, nm := range fld.Names {
					m[nm.Name] = vcField{Owner: ts.Name.Name, Kind: k, Type: tp}
				}
				if len(fld.Names) == 0 {
					m["<embed:"+vcExprString(fld.Type)+">"] = vcField{Owner: ts.Name.Name, Kind: k, Type: tp}
				}
			}
			a.structs[ts.Name.Name] = m
		}
		return true
	})
}

func (a *vcAnalyzer) collectTypeExprs(f *ast.File) {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, sp := range gd.Specs {
			if ts, ok := sp.(*ast.TypeSpec); ok {
				a.typeExpr[ts.Name.Name] = ts.Type
			}
		}
	}
}

func vcNewAnalyzer(dir string) (*vcAnalyzer, error) {
	a := &vcAnalyzer{
		fset: token.NewFileSet(), files: map[string]*ast.File{},
		ifaces: map[string]bool{}, structs: map[string]map[string]vcField{},
		methods: map[string]*vcMethod{}, aliasPath: map[string]string{},
		extCache: map[string]vcKind{}, typeExpr: map[string]ast.Expr{},
		modRoot: vcFindModRoot(dir),
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(a.fset, filepath.Join(dir, e.Name()), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		a.files[e.Name()] = f
		a.fileNames = append(a.fileNames, e.Name())
	}
	sort.Strings(a.fileNames)
	for _, f := range a.files {
		for _, im := range f.Imports {
			p, err := strconv.Unquote(im.Path.Value)
			if err != nil {
				continue
			}
			alias := ""
			if im.Name != nil {
				alias = im.Name.Name
			}
			if alias == "" || alias == "_" || alias == "." {
				alias = filepath.Base(p)
			}
			a.aliasPath[alias] = p
		}
	}
	// ★ 两趟：先登记**全部** interface（否则字段类型解析依赖 map 遍历顺序 ⇒ 随机错判），
	// 再解析 struct 字段。单趟会因「接口还没登记就解析用到它的字段」而把共享字段误判成按值。
	for _, fn := range a.fileNames {
		f := a.files[fn]
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, sp := range gd.Specs {
				if ts, ok := sp.(*ast.TypeSpec); ok {
					if _, isIface := ts.Type.(*ast.InterfaceType); isIface {
						a.ifaces[ts.Name.Name] = true
					}
					a.typeExpr[ts.Name.Name] = ts.Type
				}
			}
		}
	}
	for _, f := range a.files {
		a.collectTypes(f)
	}
	// Model 字段视图（展开嵌入值结构体的提升字段）
	a.model = map[string]vcField{}
	seen := map[string]bool{}
	var expand func(string, int)
	expand = func(tn string, d int) {
		if d > 4 || seen[tn] {
			return
		}
		seen[tn] = true
		for nm, info := range a.structs[tn] {
			if strings.HasPrefix(nm, "<embed:") {
				et := strings.TrimSuffix(strings.TrimPrefix(nm, "<embed:"), ">")
				if info.Kind != vcShared {
					expand(et, d+1)
					for sn, si := range a.structs[et] {
						if strings.HasPrefix(sn, "<embed:") {
							continue
						}
						if _, ok := a.model[sn]; !ok {
							a.model[sn] = si
						}
					}
				}
				continue
			}
			if _, ok := a.model[nm]; !ok {
				a.model[nm] = info
			}
		}
	}
	expand("Model", 0)
	// Model 的 (m) / (*m) 方法
	for _, fn := range a.fileNames {
		for _, d := range a.files[fn].Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil || len(fd.Recv.List) == 0 {
				continue
			}
			recv := fd.Recv.List[0].Type
			ptr := false
			if st, ok := recv.(*ast.StarExpr); ok {
				ptr, recv = true, st.X
			}
			if id, ok := recv.(*ast.Ident); !ok || id.Name != "Model" {
				continue
			}
			a.methods[fd.Name.Name] = &vcMethod{
				Name: fd.Name.Name, PtrRecv: ptr, Body: fd.Body,
				Line: a.fset.Position(fd.Pos()).Line,
			}
		}
	}
	return a, nil
}

// ——————————————————————————————————————————————————————————————
// 访问收集与传递可达
// ——————————————————————————————————————————————————————————————

type vcAccess struct {
	Field string
	Write bool
}

func vcCollectAccess(root ast.Node, capture string) []vcAccess {
	writes := map[ast.Node]bool{}
	ast.Inspect(root, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.AssignStmt:
			for _, l := range t.Lhs {
				writes[l] = true
			}
		case *ast.IncDecStmt:
			writes[t.X] = true
		case *ast.UnaryExpr:
			if t.Op == token.AND {
				writes[t.X] = true
			}
		}
		return true
	})
	var out []vcAccess
	f := func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false // 内层闭包单独收集（它们同样捕获副本）
		}
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		_, w := writes[sel]
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == capture {
			out = append(out, vcAccess{Field: sel.Sel.Name, Write: w})
			return true
		}
		if inner, ok := sel.X.(*ast.SelectorExpr); ok {
			if id, ok := inner.X.(*ast.Ident); ok && id.Name == capture {
				out = append(out, vcAccess{Field: inner.Sel.Name + "." + sel.Sel.Name, Write: w})
			}
		}
		return true
	}
	if blk, ok := root.(*ast.BlockStmt); ok {
		for _, s := range blk.List {
			ast.Inspect(s, f)
		}
	} else {
		ast.Inspect(root, f)
	}
	return out
}

// vcDirectField 把 `m.<X>` 归一成 Model 自身字段名。
// `m.SessionModel.requestSnapshot` → ("requestSnapshot", true)；`m.registry.Specs` → ("", false)
func vcDirectField(sel string) (string, bool) {
	i := strings.Index(sel, ".")
	if i < 0 {
		return sel, true
	}
	switch sel[:i] {
	case "SessionModel", "UIModel", "AIModel", "ToolModel":
		return sel[i+1:], true
	}
	return "", false
}

func (a *vcAnalyzer) resolveMethod(sel string) (*vcMethod, bool) {
	base := sel
	if i := strings.Index(sel, "."); i >= 0 {
		base = sel[i+1:]
		if !strings.HasPrefix(sel, "SessionModel.") && !strings.HasPrefix(sel, "UIModel.") &&
			!strings.HasPrefix(sel, "AIModel.") && !strings.HasPrefix(sel, "ToolModel.") {
			return nil, false
		}
	}
	m, ok := a.methods[base]
	return m, ok
}

type vcTouch struct {
	Field string
	Write bool
}

func (a *vcAnalyzer) reach(root ast.Node, capture string, stack map[string]bool) []vcTouch {
	var out []vcTouch
	for _, ac := range vcCollectAccess(root, capture) {
		if name, ok := vcDirectField(ac.Field); ok {
			if fi, has := a.model[name]; has {
				// 写：**任何**字段都丢（写的即便是接口/指针字段，丢的也是那个字段头）
				// 读：只有「按值/待判」才可能读到陈旧副本
				if ac.Write || fi.Kind != vcShared {
					out = append(out, vcTouch{Field: ac.Field, Write: ac.Write})
				}
				continue
			}
		}
		m, ok := a.resolveMethod(ac.Field)
		if !ok || stack[m.Name] {
			continue
		}
		stack[m.Name] = true
		out = append(out, a.reach(m.Body, "m", stack)...)
		delete(stack, m.Name)
	}
	return out
}

// ——————————————————————————————————————————————————————————————
// 闭包枚举
// ——————————————————————————————————————————————————————————————

type vcClosure struct {
	Line   int
	Assign string // 形如 m.cmdRegistry.OnContext / m.goalManager
	Ord    int    // 同一 Assign 下的第几个闭包（源序）
	Nested int    // >0 = 内层嵌套闭包
	Body   *ast.BlockStmt
}

func vcFindClosures(a *vcAnalyzer, file, fnName string) []vcClosure {
	var out []vcClosure
	f := a.files[file]
	if f == nil {
		return nil
	}
	seq := map[string]int{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fnName || fd.Body == nil {
			continue
		}
		for _, st := range fd.Body.List {
			lhs := ""
			if as, ok := st.(*ast.AssignStmt); ok && len(as.Lhs) == 1 {
				lhs = vcExprString(as.Lhs[0])
			}
			var visit func(n ast.Node, depth int)
			visit = func(n ast.Node, depth int) {
				ast.Inspect(n, func(c ast.Node) bool {
					fl, ok := c.(*ast.FuncLit)
					if !ok {
						return true
					}
					ord := seq[lhs]
					seq[lhs]++
					out = append(out, vcClosure{
						Line: a.fset.Position(fl.Pos()).Line, Assign: lhs, Ord: ord,
						Nested: depth, Body: fl.Body,
					})
					visit(fl.Body, depth+1)
					return false
				})
			}
			visit(st, 0)
		}
	}
	return out
}

// ——————————————————————————————————————————————————————————————
// 基线
// ——————————————————————————————————————————————————————————————

// vcBaselineReason 每条基线必须给理由 —— 防止基线退化成"什么都放行"。
type vcBaselineEntry struct {
	Key    string // 闭包键：`<Assign>` 或 `<Assign>#<ord>`
	Reason string // 为什么当前不构成缺陷
}

// vcGateBaseline：2026-09-20 机械枚举的**现状快照**。
// 新增条目 = 新引入一处「值捕获闭包触达副本字段」⇒ 门红。
// 修理（条目消失）⇒ 门只警告，不红 —— 免得修 bug 反被门挡住。
var vcGateBaseline = []vcBaselineEntry{
	// 家族收口：OnContext / OnNotifyDismiss / OnTaskCommand 三条
	// 死角基线已随死闭包接线删除而移除（原 OnRemote 条目同批，见下）。
	// 三条命令均在 isTUICommandHandledLocally 表内 ⇒ TUI 内 cmd.Handler 永不触发，
	// NewModel 里的值捕获闭包是死代码；唯一活面 = 本地 switch 直调 m.handleXxx，
	// plain/bridge 由 runner_core.go 在各自 registry 实例上独立接线。
	// ★ 若有人在 NewModel 重新接回任一条值捕获闭包，会作为「新引入条目」被本门打红（§①）。
	{Key: "m.cmdRegistry.OnDaemon",
		Reason: "★ 活线（`/daemon *` 走分发）。handleDaemon（commands_mcp.go:445）写 daemonControl ⇒ 活线上的写丢失；当前被 NewModel:758 已赋值挡住（见 §8.6 潜伏项）。"},
	{Key: "m.cmdRegistry.OnHooks",
		Reason: "活线，只读 workDir（string）。安全性压在「workDir 全包零写点」这条不变量上 ⇒ 由 TestValueCaptureWorkDirHomeDirWriteFree 守。"},
	{Key: "m.cmdRegistry.OnMcp",
		Reason: "活线，只读 workDir（string）。同上。"},
	{Key: "m.cmdRegistry.OnDoctor",
		Reason: "活线，只读 homeDir / workDir（string）。同上。"},
	{Key: "m.cmdRegistry.OnOnboarding",
		Reason: "活线，只读 homeDir（string）。同上。"},
	{Key: "m.cmdRegistry.OnInit",
		Reason: "活线，只读 workDir（string）。同上。"},
	{Key: "m.goalManager",
		Reason: "有意模式：闭包体内经 reassignServices（service.go:7）写 4 个 service 接口字段，使其指向**本副本**自己的嵌入体。这是「让副本自洽」的既有惯例，不是漏写；真收敛手段在 P2-1 重构（docs/future/P2-1-model-struct-refactor.md）。"},
}

// vcKey 闭包 → 基线键。同一 Assign 下有多个闭包时用 `#ord` 区分。
func vcKey(c vcClosure, dup bool) string {
	if dup {
		return fmt.Sprintf("%s#%d", c.Assign, c.Ord)
	}
	return c.Assign
}

func vcTouchLabel(t vcTouch) string {
	if t.Write {
		return t.Field + "✎写"
	}
	return t.Field
}

// ——————————————————————————————————————————————————————————————
// 门
// ——————————————————————————————————————————————————————————————

func vcAnalyze(t *testing.T) (*vcAnalyzer, []vcClosure, map[string][]vcTouch) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	a, err := vcNewAnalyzer(dir)
	if err != nil {
		t.Fatalf("解析 %s 下生产 .go 失败: %v", dir, err)
	}
	closures := vcFindClosures(a, "lifecycle.go", "NewModel")
	if len(closures) < 50 {
		t.Fatalf("NewModel 内闭包只找到 %d 个（预期 ~56）—— 解析可能失效，门不可信", len(closures))
	}
	// 逐闭包求触达；只有「同一 Assign 下有多个闭包命中」时才用 #ord 区分，
	// 否则键就是 Assign 本身（源码里挪动同语句内其它闭包不会误红）。
	touches := map[*vcClosure][]vcTouch{}
	byAssign := map[string]int{}
	for i := range closures {
		c := &closures[i]
		ts := a.reach(c.Body, "m", map[string]bool{})
		if len(ts) == 0 {
			continue
		}
		touches[c] = vcUnique(ts)
		byAssign[c.Assign]++
	}
	got := map[string][]vcTouch{}
	for c, ts := range touches {
		got[vcKey(*c, byAssign[c.Assign] > 1)] = ts
	}
	return a, closures, got
}

func vcUnique(in []vcTouch) []vcTouch {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Field != in[j].Field {
			return in[i].Field < in[j].Field
		}
		return !in[i].Write && in[j].Write
	})
	var out []vcTouch
	for _, t := range in {
		dup := false
		for _, o := range out {
			if o.Field == t.Field && o.Write == t.Write {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, t)
		}
	}
	return out
}

// TestValueCaptureASTGate ①新引入的值捕获触达即红 ②基线缺理由即红 ③基线过期只告警。
func TestValueCaptureASTGate(t *testing.T) {
	a, closures, got := vcAnalyze(t)

	t.Logf("NewModel 内闭包 %d 个；其中触达副本字段的 %d 个", len(closures), len(got))

	// ② 基线自检
	baseline := map[string]vcBaselineEntry{}
	for _, b := range vcGateBaseline {
		if strings.TrimSpace(b.Reason) == "" {
			t.Errorf("基线 %q 没有写明理由 —— 基线不得退化成「什么都放行」", b.Key)
		}
		if _, dup := baseline[b.Key]; dup {
			t.Errorf("基线键重复：%q", b.Key)
		}
		baseline[b.Key] = b
	}

	// ① 新增即红
	var newKeys []string
	for k := range got {
		if _, ok := baseline[k]; !ok {
			newKeys = append(newKeys, k)
		}
	}
	sort.Strings(newKeys)
	for _, k := range newKeys {
		var labels []string
		for _, tt := range got[k] {
			kn := "?"
			if d, ok := vcDirectField(tt.Field); ok {
				if fi, ok := a.model[d]; ok {
					kn = fi.Kind.String()
				}
			}
			labels = append(labels, vcTouchLabel(tt)+"("+kn+")")
		}
		t.Errorf("新增「值捕获闭包触达副本字段」：%s → [%s]\n"+
			"  NewModel 按值返回，闭包里的 m 是 NewModel 结束时刻的快照。\n"+
			"  读按值字段 ⇒ 陈旧值；写任何字段 ⇒ 写丢失。\n"+
			"  正确做法：闭包只经**跨副本共享的指针/接口字段**取状态，或把该命令留在本地表内由活模型处理。\n"+
			"  若确认无害，请在 vcGateBaseline 里登记并写明理由。",
			k, strings.Join(labels, ", "))
	}

	// ③ 基线过期只告警
	var stale []string
	for k := range baseline {
		if _, ok := got[k]; !ok {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Logf("提示：基线里 %d 条已不再命中（可能是被修掉了），可收窄 vcGateBaseline：%v", len(stale), stale)
	}

	// -v 时打印全表（审计与门同源：一份实现两种用法）
	if testing.Verbose() {
		var all []string
		for k := range got {
			all = append(all, k)
		}
		sort.Strings(all)
		for _, k := range all {
			var labels []string
			for _, tt := range got[k] {
				labels = append(labels, vcTouchLabel(tt))
			}
			t.Logf("  %-40s %s", k, strings.Join(labels, ", "))
		}
	}
}

// TestValueCaptureWorkDirHomeDirWriteFree 不变量：NewModel 内 6 个活线闭包的
// 「只读 workDir/homeDir 因此安全」这个前提，压在「这两个字段构造后不被改」上。
// 一旦全包出现 `*.workDir = ...` / `*.homeDir = ...` 的写点，本门红。
func TestValueCaptureWorkDirHomeDirWriteFree(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	a, err := vcNewAnalyzer(dir)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	guarded := []string{"workDir", "homeDir"}
	var violations []string
	for _, fn := range a.fileNames {
		ast.Inspect(a.files[fn], func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, l := range as.Lhs {
				sel, ok := l.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				for _, g := range guarded {
					if sel.Sel.Name != g {
						continue
					}
					// 只关心写入 **Model** 的这一份（其它结构体的同名字段无关）
					if root, ok := sel.X.(*ast.Ident); ok && root.Name == "m" {
						violations = append(violations, fmt.Sprintf("%s:%d  %s = %s",
							fn, a.fset.Position(as.Pos()).Line, vcExprString(l), vcExprString(as.Rhs[min(i, len(as.Rhs)-1)])))
					}
				}
			}
			return true
		})
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("不变量被打破：Model.%v 出现写点 —— 6 个活线闭包（OnHooks/OnMcp/OnDoctor/OnOnboarding/OnInit）"+
			"原本靠「这两个字段构造后不变」保证读到正确值：\n  %s\n"+
			"  修法二选一：① 改成指针字段；② 把这些闭包改成绑定指针接收者的方法值（对齐全包另外 50 处注册点）。",
			guarded, strings.Join(violations, "\n  "))
	}
}
