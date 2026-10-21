package tui

import (
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/anchorandfish/whale-code/internal/command"
)

// —— 面 2「接线语义双实现」行为判别器 ————————————————————————————
//
// 背景（2026-09-20 机械枚举出的事实面）：
//
//	NewModel（lifecycle.go:123，**按值返回**）在函数体内把 44 个命令回调注册为
//	`m.cmdRegistry.OnXxx = func(...){ ... m ... }`，闭包**捕获值局部 m**
//	（`m := Model{...}`，lifecycle.go:266）。调用方（`Update` 的返回值 / tea.Program）
//	拿到的是 m 的副本，此后状态更新都落在副本上 ⇒ 这 44 个闭包体内的 m 永远停在
//	NewModel 结束时刻（见 domains/TUI按值复制陷阱.md）。
//
//	迄今未炸的原因：handleCommand（app.go:2785）在 `cmd.Handler(args)` 前有一道
//	`if !isTUICommandHandledLocally(name)` 阀门；本地列表（app.go:2736）内的命令
//	只走本地实现（用活模型）。实测该列表 97 项与同一分发函数 `switch name` 的 97 个
//	case **完全同集合** ⇒ 不存在"先跑回调再跑本地分支"的双执行。
//
//	plain/bridge 路径不走 NewModel（走 NewRunnerCore），它们自己用 `runner.handleXxx`
//	（指针接收者，安全）注册；其中 OnContext 被 runner_core.go:429 以指针方法值覆盖。
//
// 本文件的判别器把"恰好成立"变成"被判据钉住"：
//
//	① TestTUICommandDispatch...：
// 把 registry 上所有 On* 回调换成**记录调用**的探针，再走生产分发入口。
// 不变量 = 本地列表内的命令**不得**执行 registry 回调；
// 本地实现主动读回调的命令（白名单）必须执行 —— 双向断言，防漏也防表过时。
//
//	② TestContextCommandReadsLiveStateNotCapturedCopy：
// /context 必须反映**活模型**的 inputTokens。曾经还断言"直接调 OnContext 回调
// 必读到旧副本"作为值捕获前提的必要性证据；该回调接线已随 家族收口删除
// （2026-10-11，/context 唯一活面 = 本地 switch），② 随之退役 —— 重接线防护由
// AST 门（value_capture_ast_gate_test.go）与本文件的面 2 探针判别器承担。
//
// 为什么探针记录"调用"而不是"返回值哨兵"：本地实现主动读回调的命令里，
// handleSkillPrompt（commands_mcp.go:157）/ handleSecurityReview（commands_misc.go:682）
// 把回调返回值**注入成用户消息**，自身只返回固定标记（`__SKILL_PROMPT__` /
// `"🛡️ Security review started"`）⇒ 返回值哨兵根本不会出现在命令输出里。
// 首次实现用"输出含哨兵"判定，这 5 条命令被误报为"白名单虚报"，正是本注释的由来。

// callbackProbe 记录「哪些 registry 回调被真正执行过」。
type callbackProbe struct {
	mu    sync.Mutex
	calls []string
}

func (p *callbackProbe) record(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, name)
}

// mark 返回当前调用计数，供调用方在执行一段代码前后做差值。
func (p *callbackProbe) mark() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *callbackProbe) since(n int) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n >= len(p.calls) {
		return nil
	}
	out := make([]string, len(p.calls)-n)
	copy(out, p.calls[n:])
	return out
}

// valueCaptureLocalReaders 是「本地实现会主动读 registry 回调」的命令集合。
//
// 这些命令在 isTUICommandHandledLocally 列表内（因此 handleCommand 不调
// cmd.Handler），但本地实现自己读 `m.cmdRegistry.OnXxx` 再调用 —— 所以探针下
// 它们**必然**触发回调。来源（机械枚举 cmdRegistry.OnXxx 的读取点，排除赋值）：
//
//	commands_mcp.go:23,688 OnMcp -> mcp
//	commands_mcp.go:161 OnSkillExecute -> skillify/simplify/verify/batch
//	commands_misc.go:700 OnSkillExecute -> security-review
//	commands_misc.go:205,816 OnEnv -> env
//	commands_misc.go:861 OnOutputStyle -> output-style
//	commands_misc.go:893 OnTerminalSetup -> terminal-setup
//	commands_misc.go:922 OnMigrate -> migrate
//	commands_task.go:156 OnDaemon -> tasks
//
// 这是必须与读取点同步的白名单：新增"本地读回调"的命令时，断言会变红，
// 逼迫作者显式确认（设计意图）。
var valueCaptureLocalReaders = map[string]bool{
	"mcp":             true,
	"skillify":        true,
	"simplify":        true,
	"verify":          true,
	"batch":           true,
	"security-review": true,
	"env":             true,
	"output-style":    true,
	"terminal-setup":  true,
	"migrate":         true,
	"tasks":           true,
}

// probeRegistryCallbacks 把 reg 上**已注册（非 nil）**的 `On*` 函数字段替换为
// "记录调用"的探针，返回 (替换的字段名, 原本为 nil 的字段名)。
//
// registry 是 *command.Registry（指针，跨副本共享），因此替换对活模型与
// 闭包内捕获的 m 同时生效 —— 任何代码路径执行回调，探针都会记录。
//
// ★ 为什么必须跳过 nil 字段（本测试首个版本踩过）：生产路径下 TUI **不注册**
// 某些回调（如 OnSecurityCheck —— 全仓只有测试给它赋值）。这类字段为 nil 时，
// 消费方是 `if r.OnSecurityCheck != nil { ... }` ⇒ 分支被跳过、回调不执行。
// 若探针把 nil 填成非 nil，就会**凭空造出一条执行路径**，把 /fast（本地 case
// 会显式委托 cmd.Handler，见 app.go:2810）误报成"执行了 registry 回调"。
// 保留 nil 语义才能忠实反映真实路径。
func probeRegistryCallbacks(t *testing.T, reg *command.Registry, probe *callbackProbe) (probed, nilFields []string) {
	t.Helper()
	probeStruct(t, reflect.ValueOf(reg).Elem(), probe, &probed, &nilFields)
	sort.Strings(probed)
	sort.Strings(nilFields)
	return probed, nilFields
}

// probeStruct 递归替换结构体（含匿名嵌入）上所有非 nil 的 `On*` 函数字段。
//
// 为什么必须递归：`On*` 回调并不直接挂在 Registry 上，而是挂在它嵌入的
// HandlerCallbacks 结构体里（command.go:230 `type Registry struct { ...
// HandlerCallbacks }`）。只扫顶层字段会一个都找不到 —— 首次实现漏了这层，
// 测试立刻以"夹具失效"报错，未静默通过。
func probeStruct(t *testing.T, v reflect.Value, probe *callbackProbe, probed, nilFields *[]string) {
	t.Helper()
	rt := v.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		fv := v.Field(i)
		if f.Anonymous && fv.Kind() == reflect.Struct {
			probeStruct(t, fv, probe, probed, nilFields)
			continue
		}
		if !strings.HasPrefix(f.Name, "On") || f.Type.Kind() != reflect.Func {
			continue
		}
		if !fv.CanSet() {
			t.Fatalf("registry 字段 %s 不可设置 —— 夹具前提失效", f.Name)
		}
		if fv.IsNil() {
			*nilFields = append(*nilFields, f.Name)
			continue
		}
		name := f.Name
		fnType := f.Type
		fv.Set(reflect.MakeFunc(fnType, func(_ []reflect.Value) []reflect.Value {
			probe.record(name)
			return zeroOuts(fnType)
		}))
		*probed = append(*probed, name)
	}
}

func zeroOuts(fnType reflect.Type) []reflect.Value {
	out := make([]reflect.Value, fnType.NumOut())
	for i := range out {
		out[i] = reflect.Zero(fnType.Out(i))
	}
	return out
}

// callWithoutPanic 执行 f 并吞掉 panic（个别命令在裸测试 Model 上可能缺前置）。
func callWithoutPanic(f func() string) string {
	defer func() { _ = recover() }()
	return f()
}

// TestTUICommandDispatchNeverFallsThroughToRegistryCallbacks 是面 2 的核心判别器。
//
// 双向断言：
// 1. 本地列表内、不在白名单的命令：执行期间**不得**触发任何 registry 回调
// ⇒ 本地实现确实是唯一实现。
// 2. 白名单里的命令：**必须**触发回调 ⇒ 白名单没有虚报（防"表里写过时了"）。
func TestTUICommandDispatchNeverFallsThroughToRegistryCallbacks(t *testing.T) {
	m := newTUIRegressionModel(t)
	if m.cmdRegistry == nil {
		t.Fatal("夹具失效：cmdRegistry 为 nil")
	}
	probe := &callbackProbe{}
	probed, nilFields := probeRegistryCallbacks(t, m.cmdRegistry, probe)
	if len(probed) == 0 {
		t.Fatal("夹具失效：registry 上没有任何非 nil 的 On* 回调字段被探针化")
	}
	t.Logf("探针化回调 %d 个；生产路径下未注册（nil，消费方会跳过）的 %d 个：%v",
		len(probed), len(nilFields), nilFields)

	executed := 0
	hitCount := 0
	for _, c := range m.cmdRegistry.All() {
		name := c.Name
		if !isTUICommandHandledLocally(name) {
			continue // 非本地命令：设计上就走 registry 回调，不在本不变量范围
		}
		before := probe.mark()
		_ = callWithoutPanic(func() string { return m.handleCommand("/" + name) })
		got := probe.since(before)
		executed++

		if len(got) > 0 {
			hitCount++
			if !valueCaptureLocalReaders[name] {
				t.Errorf("命令 /%s 在本地列表内，却执行了 registry 回调 %v —— "+
					"「本地实现是唯一实现」这一不变量被破坏（回调捕获的是 NewModel 的值副本，读不到活状态）",
					name, got)
			}
			continue
		}
		if valueCaptureLocalReaders[name] {
			t.Errorf("命令 /%s 在 valueCaptureLocalReaders 白名单里，但执行期间没有触发任何回调 —— "+
				"该命令已不再主动读回调，请从白名单移除（防表过时）", name)
		}
	}

	if executed == 0 {
		t.Fatal("夹具失效：没有任何本地命令被执行（isTUICommandHandledLocally 全 false？）")
	}
	t.Logf("探针化回调 %d 个；执行本地命令 %d 个；触发回调的命令 %d 个（白名单 %d 项）",
		len(probed), executed, hitCount, len(valueCaptureLocalReaders))
}

// TestContextCommandReadsLiveStateNotCapturedCopy 是面 2 的行为判别器。
//
// 机械枚举（含穿透 service 接口与一层方法调用）显示：值捕获回调中**只有**
// OnContext 曾依赖**可变**快照（handleContextInfo 读 requestSnapshot 值字段，并经
// sessionService 读 inputTokens/outputTokens/requestCount/compactCount —— 这些
// 都落在该 m 自己的 SessionModel 上）。其余回调读的是共享指针或不可变值
// （workDir/homeDir 构造后不再赋值）。
//
// 本测试固化：/context 走生产分发入口读的是**活模型**的 inputTokens（本地实现）。
// 原第②半（"直接执行 OnContext 回调必读到旧副本"）已随该回调接线删除而退役
// （家族收口，2026-10-11）—— 重接线防护 = AST 门 + 面 2 探针判别器，
// 见文件头注释 ②。
func TestContextCommandReadsLiveStateNotCapturedCopy(t *testing.T) {
	m := newTUIRegressionModel(t)

	const liveInput = 987654
	// 模拟"一轮请求之后"：把可辨识值写进**活模型**的值字段。
	m.SessionModel.inputTokens = liveInput
	m.reassignServices() // 保证活模型的 sessionService 指向自身嵌入体

	out := m.handleCommand("/context")
	if !strings.Contains(out, "987,654") {
		t.Fatalf("/context 未反映活模型的 inputTokens=%d —— 分发可能落回了值捕获回调。输出：\n%s",
			liveInput, out)
	}
}
