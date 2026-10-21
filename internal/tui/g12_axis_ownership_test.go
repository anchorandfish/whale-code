package tui

// g12_axis_ownership_test.go — G12：**轴归属**（一轴一写者 + 单轴命令的自我声明）。
//
// # 被修的缺陷
//
//	`/sandbox on|off` **跨轴偷写审批档**：
// on → `SetMode(read-only)`
// off → `SetModeByName(cfg.PermissionMode)`（默认 workspace-write）
//	（`commands_plugin.go:162,170` 与 `runner_core.go:2287,2293` 两处同形）
//
// 三个叠加的毛病：
//
//	① 取值来源错：`cfg.PermissionMode` 是**本次启动的初始档**，不是"上一档"；
// `/permissions` 的档位切换**根本不落盘**（只改内存 Policy），所以它从定义上
// 就不可能是"上一档"。这是**取值来源本身**错了，不只是丢状态。
//	② 不可逆：danger 用户 `/sandbox on`→read-only→`/sandbox off`→workspace-write，
// 原 danger **永久丢失**，没有任何反向命令能还原。
//	③ fail-open（本文件判据二）：read-only 用户敲 `/sandbox off`，写入被**放开**
// ——命令名承诺"减少限制"，实际在**另一个轴上扩权**。
//
// 对照同类 CLI：TUI 的斜杠命令表里**没有**单翻沙箱的命令，只有 `Permissions`
// 三预设（``）；CLI 层的 `-s` / `-a`
// 是两个**互不写入**的独立 flag（`utils/cli/src/shared_options.rs:39-40,45-49`）。
// 两轴同关只有逃生舱 `--dangerously-bypass-approvals-and-sandbox`（名字把两轴都写出来）。
// ⇒ 判据：**改轴的命令不得顺手写另一个轴**；要跨轴，名字必须声明写的是哪两轴。
//
// # 生产入口（不手工构造前提）
//
// - Model 侧：`Model.handleCommand("sandbox …")` → `handleSandbox`
// - runnerCore 侧：`runnerCore.handleSandbox("…")`，装配复用 `newTestRunnerCore()`
//
// # ★ 判别性（before 必须精确打红）
//
//	「/sandbox on|off 前后 `Policy().Mode()` 不变」在**当前实现下必然红**
//	（off 把它拨回 workspace-write、on 把它拨成 read-only），改后必绿。
//
// # ⚠️ 本文件里的 AST 守卫是「运行期读磁盘」型判别器
//
// `go/ast` 守卫在**运行时** `os.Open` 生产源码（不是编译期输入）⇒ `-overlay`
// 对它们**物理无效**（技能 production-path-tdd R4-8）。本文件注释里标明这一点，
// 是为了让后来做 A/B 的人不去用 overlay 跑它们、然后得出"守卫不可判别"的错误结论。
// 对这两个守卫做 before 态取证，必须走「受控写回 + 无条件还原 + md5 校验」。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// ────────────────── 判据一：单轴命令不得写另一个轴 ──────────────────

// TestG12_SandboxOffDoesNotTouchApprovalAxis_ModelPath 判据一（Model 侧）。
//
// 从 danger 档出发才能暴露"档位被顺手拨走"。改后：/sandbox off 只翻能力轴，
// 审批档必须**仍是 danger-full-access**。
func TestG12_SandboxOffDoesNotTouchApprovalAxis_ModelPath(t *testing.T) {
	m := g10Model(t)

	if !m.toolService.Policy().SetModeByName("danger-full-access") {
		t.Fatal("前置失败：danger-full-access 应可解析")
	}
	if got := m.toolService.Policy().Mode(); got != permission.LevelDangerFullAccess {
		t.Fatalf("前置失败：起点应是 danger-full-access，实际 %s", got)
	}

	out := m.handleCommand("sandbox off")

	if got := m.toolService.Policy().Mode(); got != permission.LevelDangerFullAccess {
		t.Errorf("G12：/sandbox off 只写能力轴，审批轴必须原封不动。"+
			"实际档位被拨成 %s（旧实现回落到 cfg.PermissionMode=workspace-write，"+
			"且原 danger 档永久丢失、无反向命令可还原）。输出=%q", got, out)
	}
}

// TestG12_SandboxOnDoesNotTouchApprovalAxis_ModelPath 判据一（on 方向）。
func TestG12_SandboxOnDoesNotTouchApprovalAxis_ModelPath(t *testing.T) {
	m := g10Model(t)

	if !m.toolService.Policy().SetModeByName("workspace-write") {
		t.Fatal("前置失败：workspace-write 应可解析")
	}
	out := m.handleCommand("sandbox on")

	if got := m.toolService.Policy().Mode(); got != permission.LevelWorkspaceWrite {
		t.Errorf("G12：/sandbox on 只写能力轴，审批轴必须原封不动（旧实现把它拨成 read-only）；"+
			"实际 %s。输出=%q", got, out)
	}
}

// TestG12_SandboxOffDoesNotEscalateFromReadOnly 判据二（fail-open 守卫）。
//
// 这是三个毛病里最重的一个：read-only 用户（写操作被拦）敲 `/sandbox off`
// ——命令名承诺的是"减少限制"，旧实现却在**审批轴上扩权**（read-only →
// cfg 默认 workspace-write），写入被放开。这不是"降级"，是**提权**。
func TestG12_SandboxOffDoesNotEscalateFromReadOnly(t *testing.T) {
	m := g10Model(t)

	if !m.toolService.Policy().SetModeByName("read-only") {
		t.Fatal("前置失败：read-only 应可解析")
	}
	m.handleCommand("sandbox off")

	if got := m.toolService.Policy().Mode(); got != permission.LevelReadOnly {
		t.Errorf("G12 fail-open：/sandbox off 把 read-only 提权成了 %s —— "+
			"关沙箱不得顺手放开审批轴。这一条在旧实现下必红。", got)
	}
}

// TestG12_SandboxOffDoesNotTouchApprovalAxis_RunnerCorePath 判据一（runnerCore 侧）。
//
// **两处同形必须同改**：只改 Model 侧 = 半修（plain/bridge 路径照旧越界）。
// 本判据正交于 Model 侧判据 —— 单点回退必打红。
func TestG12_SandboxOffDoesNotTouchApprovalAxis_RunnerCorePath(t *testing.T) {
	c := newTestRunnerCore()
	if c.Policy == nil || c.Executor == nil {
		t.Fatal("前置失败：newTestRunnerCore 未装配 Policy/Executor")
	}
	if !c.Policy.SetModeByName("danger-full-access") {
		t.Fatal("前置失败：danger-full-access 应可解析")
	}

	out := c.handleSandbox("off")

	if got := c.Policy.Mode(); got != permission.LevelDangerFullAccess {
		t.Errorf("G12：runnerCore 的 /sandbox off 必须与 Model 侧同形同改（只改一侧 = 半修）；"+
			"实际档位 %s。输出=%q", got, out)
	}
}

// ────────────────── 判据三：单轴命令必须自报轴归属 ──────────────────

// TestG12_SandboxOutputDeclaresAxisOwnership 判据三：输出的**可读性**契约。
//
// 用户明确要求：`/sandbox` 与 `/permissions` 极易被当成同一件事，
// 所以单轴命令的输出必须**显式声明它没写另一个轴**。
//
// 反向守卫：A1 的既有文案（`Sandbox OFF` + T1 降级告警）必须仍在 —— 加新句不得吃掉旧句。
func TestG12_SandboxOutputDeclaresAxisOwnership(t *testing.T) {
	m := g10Model(t)

	off := m.handleCommand("sandbox off")
	if !strings.Contains(off, "capability axis only") {
		t.Errorf("G12：/sandbox 必须显式声明它只写能力轴（用户要求：区分单轴归属）；got %q", off)
	}
	if !strings.Contains(off, "approval axis") {
		t.Errorf("G12：/sandbox 的轴归属声明必须点名它**不碰**审批轴；got %q", off)
	}
	if !strings.Contains(off, "Sandbox OFF") {
		t.Errorf("G12 反例守卫：A1 的 `Sandbox OFF` 报告必须保留；got %q", off)
	}
	if !strings.Contains(off, "permission layer") {
		t.Errorf("G12 反例守卫：A1 的 T1 降级告警必须保留；got %q", off)
	}

	on := m.handleCommand("sandbox on")
	if !strings.Contains(on, "capability axis only") || !strings.Contains(on, "approval axis") {
		t.Errorf("G12：/sandbox on 同样必须自报轴归属；got %q", on)
	}
	if !strings.Contains(on, "manager=") {
		t.Errorf("G12 反例守卫：/sandbox on 仍必须报两套门状态；got %q", on)
	}
}

// TestG12_PermissionsOutputDeclaresAxisOwnership 判据三（审批轴侧）。
//
// `/permissions` 的输出同样必须说清"不含沙箱"——否则它看起来就是"总的权限开关"，
// 而实际上它完全不碰 OS 隔离。
func TestG12_PermissionsOutputDeclaresAxisOwnership(t *testing.T) {
	m := g10Model(t)

	status := m.handleCommand("permissions status")
	if !strings.Contains(status, "approval axis only") {
		t.Errorf("G12：/permissions 必须显式声明它只写审批轴（不含沙箱）；got %q", status)
	}
	if !strings.Contains(status, "sandbox") {
		t.Errorf("G12：/permissions 的轴归属声明必须点名沙箱轴不受影响；got %q", status)
	}

	switched := m.handleCommand("permissions read-only")
	if !strings.Contains(switched, "approval axis only") {
		t.Errorf("G12：切换档位后的回执也必须自报轴归属；got %q", switched)
	}
}

// ────────────────── 判据四：AST 结构面守卫（ratchet） ──────────────────

// g12PolicyWriteSites 用 AST 扫描**生产**（非 _test.go）源码，返回
//
//	sites —— "文件.go:(接收者).函数名" → 该函数体内「审批轴写入点」的个数
//	parsed —— 被解析到的全部函数名集合（用于**前置断言**：证明守卫没变瞎）
//
// 只认对 `Policy` 的写入：`m.toolService.Policy().SetMode*(…)`、
// `c.Policy.SetMode*(…)`、参数 `policy.SetMode*(…)`。
// 刻意**不**匹配 `keybindingsMgr.SetMode("vim")` / `kbMgr.SetMode(...)`
// ——那是 vim 键位表，与权限轴无关（同名 token 不同语义，不排除会误命中）。
//
// ★ 为什么必须同时返回 parsed：写入点表里"没有某个函数"有两种成因——
// ① 它真的不写了（期望）；② 它被改名/搬走，守卫变瞎（危险）。
// 只查 sites 分不出这两种，会把"守卫失效"读成"修复成功"（R4-7 的恒绿陷阱）。
//
// ★ 这是**运行期读磁盘**型判别器：`-overlay` 对它无效（见本文件头注释）。
func g12PolicyWriteSites(t *testing.T) (map[string]int, map[string]bool) {
	t.Helper()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob 生产源码失败：%v", err)
	}
	fset := token.NewFileSet()
	sites := map[string]int{}
	parsed := map[string]bool{}
	scanned := 0

	for _, path := range files {
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		src, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败：%v", base, err)
		}
		scanned++
		for _, decl := range src.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			name := base + ":" + g12FuncName(fd)
			parsed[name] = true
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if !g12IsPolicyModeWriter(sel.Sel.Name) {
					return true
				}
				if !g12IsPolicyExpr(sel.X) {
					return true
				}
				sites[name]++
				return true
			})
		}
	}

	if scanned == 0 {
		t.Fatal("前置失败：一个生产 .go 都没扫到 —— 守卫会静默恒绿")
	}
	return sites, parsed
}

func g12IsPolicyModeWriter(sel string) bool {
	switch sel {
	case "SetMode", "SetModeByName", "SetModeCycle":
		return true
	default:
		return false
	}
}

// g12IsPolicyExpr 判断调用接收者是不是「权限策略」对象。
func g12IsPolicyExpr(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		// c.Policy.SetMode*(…)
		return v.Sel.Name == "Policy"
	case *ast.Ident:
		// applySecurityPreset(policy *permission.Policy, …) 里的 policy.SetMode*(…)
		return v.Name == "policy"
	case *ast.CallExpr:
		// m.toolService.Policy().SetMode*(…)
		if inner, ok := v.Fun.(*ast.SelectorExpr); ok {
			return inner.Sel.Name == "Policy"
		}
	}
	return false
}

func g12FuncName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	typeExpr := fd.Recv.List[0].Type
	if star, ok := typeExpr.(*ast.StarExpr); ok {
		typeExpr = star.X
	}
	if id, ok := typeExpr.(*ast.Ident); ok {
		return "(" + id.Name + ")." + fd.Name.Name
	}
	return fd.Name.Name
}

// TestG12_NoApprovalWriteInsideSandboxHandlers 判据四（durable invariant）：
// **两个 `handleSandbox` 函数体内不得有任何审批轴写入点**。
//
// 这条比"输出文案"稳：文案可以被改掉（骗过守卫），调用点不能。
func TestG12_NoApprovalWriteInsideSandboxHandlers(t *testing.T) {
	sites, parsed := g12PolicyWriteSites(t)

	// R4-7：必须先证明目标函数**确实被解析到**，否则函数改名会让本守卫静默恒绿。
	// 注意按 `parsed`（解析到的函数）而不是 `sites`（写入点）计数 ——
	// 修复后 handleSandbox 里**本来就不该有写入点**，拿 sites 计数会恒红。
	const modelSB = "commands_plugin.go:(Model).handleSandbox"
	const runnerSB = "runner_core.go:(runnerCore).handleSandbox"
	for _, key := range []string{modelSB, runnerSB} {
		if !parsed[key] {
			t.Fatalf("前置失败：AST 未解析到 %s —— 函数被改名/搬走会让本守卫"+
				"静默失真；实际解析到的 handleSandbox 有：%v", key, g12KeysWith(parsed, "handleSandbox"))
		}
	}

	var offenders []string
	for key, n := range sites {
		if strings.Contains(key, "handleSandbox") && n > 0 {
			offenders = append(offenders, key)
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("G12：/sandbox 是一轴一写者的受害者，其函数体内不得写审批轴；"+
			"仍在写的位置：%v", offenders)
	}
}

// g12KeysWith 返回集合里含指定子串的键（仅用于失败信息，便于定位守卫变瞎的原因）。
func g12KeysWith(set map[string]bool, substr string) []string {
	var out []string
	for k := range set {
		if strings.Contains(k, substr) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TestG12_ApprovalAxisWriterWhitelist 判据四（ratchet）：审批轴的**写入点白名单**。
//
// 冻结「哪些函数允许写审批轴、各写几处」。新增写入面必须显式改这张表
// —— 这就是"刻意复审"的入口；偷偷加一个跨轴写会立刻打红。
//
// 允许的写入面及其理由：
//
//	app.go:(Model).Update —— /permissions 选择器的结果落地
//	commands_config.go:(Model).handlePermissionsCommand —— /permissions <mode>（TUI 侧）
//	runner_core.go:(runnerCore).handlePermissions —— /permissions <mode>（--plain/bridge 侧）
//	plain.go:(PlainRunner).RunCI —— CI 启动装配（--ci-permission）
//	security_preset.go:applySecurityPreset —— 双轴预设（名字声明写两轴）
//
// ⚠️ 计数一并冻结：在**已白名单的函数里**偷偷加第二处写入同样会红。
//
// 变更记录（G12b，2026-09-16）：`plain.go:(PlainRunner).initDispatchHandlers`
// （1 处）**移出**本表，改为 `runner_core.go:(runnerCore).handlePermissions`（1 处）。
// 理由：修复前 `--plain` 在 dispatch 闭包里**重新实现**了一遍 `/permissions`，
// 真机探针实证它 (a) 无轴归属文案、(b) 不认 `status`。把实现收进 runnerCore
// 与 TUI 侧共用之后，写入点自然从闭包迁移到共享方法——写入面**总数不变（净 0）**，
// 只是换了归属函数。ratchet 正确地把这次迁移逼成了显式复审。
func TestG12_ApprovalAxisWriterWhitelist(t *testing.T) {
	want := map[string]int{
		"app.go:(Model).Update":                               1,
		"commands_config.go:(Model).handlePermissionsCommand": 1,
		"runner_core.go:(runnerCore).handlePermissions":       1,
		"plain.go:(PlainRunner).RunCI":                        2,
		"security_preset.go:applySecurityPreset":              1,
	}

	got, _ := g12PolicyWriteSites(t)

	var missing, extra []string
	for k := range want {
		if _, ok := got[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k, n := range got {
		if w, ok := want[k]; !ok {
			extra = append(extra, k)
			continue
		} else if w != n {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("G12 ratchet：白名单里的写入面**消失了**：%v —— "+
			"若是有意移除（如删掉 /sandbox 的跨轴写），请一并更新本表并说明理由", missing)
	}
	if len(extra) > 0 {
		t.Errorf("G12 ratchet：出现了白名单之外的审批轴写入面：%v —— "+
			"若是正当的新写入面，请更新本表并写明它声明写哪些轴（I2）；"+
			"若是跨轴偷写，请改回去。全量清单=%v", extra, got)
	}
}

// ────────────────── 判据五：状态栏两轴常驻可见 ──────────────────

// TestG12_StatusBarReportsBothAxes 判据五：footer 必须**同时**报两轴。
//
// 两轴模型只有在两轴都看得见时才诚实：只报 `Permission:` 会让用户以为
// "权限"已经把沙箱包含在内（这正是用户提出的误解点）。
func TestG12_StatusBarReportsBothAxes(t *testing.T) {
	m := g10Model(t)
	m.ready = true
	m.width = 120
	m.height = 40
	m.recalcViewportHeight()

	status := m.renderStatusBar()
	if !strings.Contains(status, "Permission:") {
		t.Fatalf("前置失败：footer 仍应报审批轴；got %q", status)
	}
	if !strings.Contains(status, "Sandbox:") {
		t.Errorf("G12：footer 必须**常驻**报出能力轴，否则用户只能靠按需命令查、"+
			"很容易把 `Permission:` 当成包含沙箱；got %q", status)
	}

	// ★ ：`Sandbox:` 后的词由两态变**三态** ——
	// 用户开了**且本机真在强制** ⇒ `on`；用户开了但本机**装不上** ⇒ `degraded`
	//（`sandboxAxisBadge` 现在读 `sandbox.EnforcementActive`）。
	// 因此本判据不得写死 `on`（那是**主机事实**，不是 code 不变式）——
	// 改为与强制事实对照，使其在"能装 / 不能装"两个世界都成立且可判别。
	exec := m.toolService.Executor()
	if sandbox.EnforcementActive(exec.SandboxMgr) {
		if !strings.Contains(status, "Sandbox: on") {
			t.Errorf("G12：本机可强制，footer 应报 on；got %q", status)
		}
	} else {
		if !strings.Contains(status, "Sandbox: degraded") {
			t.Errorf("G12：本机不可强制（EnforcementActive=false），footer 不得报 on，应报 degraded；got %q", status)
		}
		if strings.Contains(status, "Sandbox: on") {
			t.Errorf("G12 反例守卫：本机不可强制却报 on ⇒ **谎报隔离**；got %q", status)
		}
	}

	m.handleCommand("sandbox off")
	statusOff := m.renderStatusBar()
	if !strings.Contains(statusOff, "Sandbox: off") {
		t.Errorf("G12：关闭后 footer 必须如实报 off（不得沿用旧值）；got %q", statusOff)
	}
	if strings.Contains(statusOff, "Sandbox: on") {
		t.Errorf("G12 反例守卫：关闭后不得仍报 on；got %q", statusOff)
	}
}
