package permission

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ============================================================================
// —— 「whale 自身」的识别声明
// ============================================================================
//
// 背景（生产入口实测，见 `债务清剿_I10_dogfooding_自改源码_F-P_2026-09-19.md`）：
// dogfooding 场景下（whale 在自己的源码仓库里运行）两条审批免问出口同时失效 ——
//
//	出口 (a) 工作区内写免问：前提「没逃逸可写根」字面成立，但**未逃逸的地方正是被保护对象**；
//	出口 (c) 沙箱真强制免问：沙箱可写根 = 同一棵树 ⇒「边界内」成立，但**边界圈住的就是 whale 自己**。
//
// ⇒ 两条出口都以「工作区/沙箱边界」为信任边界，而自指时该边界与被保护对象重合。
// 后果：agent（或注入了 prompt injection 的文件内容）可零确认地改 `internal/permission/*.go`
// 并 `go build -o whale .` 重建，下一次启动即运行被削弱的二进制。
//
// 用户拍板：**方案 A** —— 把「whale 自身源码树」识别为受保护对象，
// **单一声明、所有入口继承**；废弃方案 B（只在出口 (a)/(c) 加自指例外）——
// B 只堵两个出口，新入口不会自动继承，正是 B2 正在收的「多入口各写各的」病。
//
// 本文件就是那**一份声明**。消费方只有一处（`internal/tools` 的 PermissionStage 免问闸），
// 因为它排在**所有**免问出口之前 ⇒ 一个载体覆盖全部入口；不新增第二套 `isSelfPath` 判定。
//
// ============================================================================
// ★ 口径补充拍板（2026-09-20 05:2x）：**P3 —— 按工具可达面分流**
// ============================================================================
//
// 方案 A 落地后实测出一处偏差（探针实测见第十批回执 §9）：判定来源 2（**二进制住在自身树里**）
// **不看 workDir 与自身树的关系** ⇒ 用开发件（本仓构建的二进制 `whale` 正是此形态）
// 跑**任何**工作区都会被判自指 ⇒ 在别人的项目里每次写文件都要确认。
// 方向安全，但把「只保护自身」变成了「所有工作区都吵」，且与本项守卫自述的
// 「不得波及其他任何工作区」直接冲突。三个口径待裁：
//
//	P1 现状：exe 在树内 ⇒ 所有工作区皆自指（安全但吵）；
//	P2 只看**目标/工作区**是否落在自身树（安静，但**放开** `bash` 从别处
// `cd` 进树再 `go build -o whale .` 这一洞）；
//	P3 按**工具可达面**分流：任意命令类保留 P1，目标受限类用 P2。
//
// 用户选定 **P3**。本文件因此提供**两个谓词**（仍是「一份声明」——分流只发生在载体里）：
//
//	IsSelfSourceTree(workDir) —— P1：工作区**或**运行中的二进制落在自身树。
// 用于「可达面无法由入参界定」的工具（`bash`/`Monitor`）。
//	PathInSelfSourceTree(target) —— P2：**只看目标路径**，不看二进制在哪、不看工作区。
// 用于「效果只经由声明目标路径发生」的写类工具。
//
// 为什么 P3 不是「换一个判据」而是「按工具的可达面选判据」：`bash` 入参只有一个字符串，
// 里面可以 `cd`、可以 `$(...)`、可以调别的东西 —— **入参界定不了它的可达面**，
// 所以只能用最保守的 P1；而 `write_file` 的目标就在入参里、且已被 CustomValidateStage
// 逐个认证过，它的可达面**可静态判定**，用 P2 既准且不吵。这正是「判据要与问题的
// 可达面匹配」而非「一刀切」。
//
// ★ 分流**不新增工具名单**：两个类的边界都是**既有 SSOT**，见
// `internal/tools/pipeline.go` 的 `selfTargetedCall`（`isWriteTool` +
// `permission.CanModifyLocalState` 的 `CapabilitiesOf`）。
// 结构面守卫：`TestGuard_N077_P3_CommandClassNeverUsesTargetReach`。

// SelfModulePath 是 whale 自己的 Go 模块路径 —— 「这是 whale 自身源码树」的**唯一判据**。
//
// 为何用模块路径而不是别的信号（逐条排除）：
// - 二进制所在目录：`go build -o /tmp/x` 后二进制可以在任何地方；且安装版与源码树分离。
// - 目录名/文件清单：可被重命名、可被复制，属"名单"型判据（本仓已有 4 份工具清单漂移的教训）。
// - 模块路径是**构建期的自证身份**：只有 whale 自己的 go.mod 会这么写；
// 判据是**精确相等**（`whale-fork-of-x` 这类前缀相近的第三方模块不得被误保护）。
const SelfModulePath = "github.com/anchorandfish/whale-code"

// selfModifyOptOutEnv 是用户**在启动 whale 之前**显式声明的「允许改自身」开关。
//
// 为什么免问只能由用户单向给出：把"改自己"的免问做成 agent 可自开的开关，等于给 agent 提权，
// 正是 I10 要禁的形态。本变量必须在 whale 进程启动前存在于环境里 ——
// agent 的 bash 跑在子进程，**改不了父进程的环境**，故这条通道天然是用户侧单向的。
//
// 真值：1 / true / yes / on（不区分大小写）。默认关闭（零值即保护生效，安全侧默认）。
const selfModifyOptOutEnv = "WHALE_ALLOW_SELF_MODIFY"

var selfModifyOptOutWarnOnce sync.Once

// SelfModifyOptOutActive 报告用户是否在启动时显式关闭了自身源码保护。
func SelfModifyOptOutActive() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(selfModifyOptOutEnv))) {
	case "1", "true", "yes", "on":
		selfModifyOptOutWarnOnce.Do(func() {
			log.Printf("[permission] WARN: %s 已开启 —— 自指场景下对 whale 自身源码/二进制的写与重建"+
				"不再强制确认（用户启动时显式声明；对齐 I10「免问只能由用户单向给出」）", selfModifyOptOutEnv)
		})
		return true
	}
	return false
}

var (
	selfRootMu    sync.Mutex
	selfRootCache = map[string]string{} // 起始目录 → 所属 whale 模块根（"" = 不是）
)

// moduleRootFrom 从 startDir 起**向上**查找声明了 SelfModulePath 的 go.mod，返回其所在目录。
//
// 为什么要向上找而不是只看 startDir：工作区可能是模块根的子目录
// （`whale-code` 下几层的路径），只看一层会漏判 —— 而漏判方向是**洞**。
// 向上找到即停，最坏走到文件系统根（有缓存，每目录只查一次）。
func moduleRootFrom(startDir string) string {
	if strings.TrimSpace(startDir) == "" {
		return ""
	}
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	if fi, statErr := os.Stat(abs); statErr == nil && !fi.IsDir() {
		abs = filepath.Dir(abs)
	}

	selfRootMu.Lock()
	if cached, ok := selfRootCache[abs]; ok {
		selfRootMu.Unlock()
		return cached
	}
	selfRootMu.Unlock()

	root := ""
	for dir := abs; ; {
		if dir == "" {
			break
		}
		if goModDeclaresSelf(dir) {
			root = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	selfRootMu.Lock()
	selfRootCache[abs] = root
	selfRootMu.Unlock()
	return root
}

// goModDeclaresSelf 判断 dir/go.mod 的首条 module 声明是否等于 SelfModulePath。
func goModDeclaresSelf(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	// 只扫前若干行：module 声明必须在注释与其它指令之外的头部，
	// 且本函数在向上查找的热路径上被逐目录调用。
	lines := strings.Split(string(data), "\n")
	if len(lines) > 64 {
		lines = lines[:64]
	}
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "//") {
			continue
		}
		if rest, ok := strings.CutPrefix(ln, "module"); ok {
			path := strings.TrimSpace(strings.Trim(rest, `"`))
			return path == SelfModulePath
		}
	}
	return false
}

// IsSelfSourceTree 报告 workDir 是否落在 **whale 自身源码树**内 —— **P1 判据**
// （用于「可达面无法由入参界定」的任意命令类工具，见本文件头部的 P3 口径）。
//
// 两条独立来源（任一命中即为自身；都找不到 ⇒ 不是）：
// 1. 从 workDir 向上找到自证身份的 go.mod —— 覆盖 dogfooding 的正形态（工作区=模块根或其子目录）；
// 2. 从**正在运行的二进制**所在目录向上找 —— 覆盖"工作区是源码树的父目录"这一形态
// （此时写 `<父目录>/whale-code/internal/...` 仍是在改 whale 自己），
// 以及「用开发件跑一个完全无关的工作区」这一形态（保守：该二进制的可达面本可碰到自身树）。
//
// 用户启动时显式声明了免问（SelfModifyOptOutActive）⇒ 恒返回 false（保护关闭）。
//
// ★ 本函数是**纯判定**，不改变任何拒绝面：它的消费方只把"本来会免问"的调用改成"问"，
// 从不把"本来会拒绝"的调用改成"问"（后者是软化硬边界，见 -R1 的单向收窄）。
//
// ★ 来源 2 的「不看 workDir」是**有意保留**的（P3）：对任意命令类，二进制住在自身树里就
// 意味着它随时能改到自身树（`go build -o whale .` 只是其中一个形态），故此处从严。
// P3 把它的爆炸半径限制在任意命令类；目标受限类改用 PathInSelfSourceTree（不看二进制位置）。
func IsSelfSourceTree(workDir string) bool {
	return isSelfSourceTreeFrom(workDir, RunningExePath())
}

// PathInSelfSourceTree 报告某个**目标路径**是否落在 whale 自身源码树内 —— **P2 判据**
// （用于「效果只经由声明目标路径发生」的写类工具，见本文件头部的 P3 口径）。
//
// 与 IsSelfSourceTree 的分工（这就是 P3 的两半）：
// - 本函数**只看路径自己**（向上找自证身份的 go.mod）；不看运行中的二进制在哪，也不看工作区。
// 回答的问题是「写这个目标，就是在改 whale 自己吗」。
// - IsSelfSourceTree 还会看二进制位置，回答的是「这一次调用的可达面有没有可能碰到 whale 自己」。
//
// ★ 只在工具的效果**只经由声明的目标路径**发生时才用它。`bash`/`Monitor` 这类任意命令工具
// **不得**用 —— 它们可以从别处 `cd` 进自身树再重建，而入参里的 `cd` 是提取不出来的。
//
// 相对路径与不存在的路径都由 moduleRootFrom 归一化处理（Abs + 向上查找；文件路径取其目录）。
func PathInSelfSourceTree(target string) bool {
	if SelfModifyOptOutActive() {
		return false
	}
	return moduleRootFrom(target) != ""
}

// RunningExePath 返回当前可执行文件的路径（取不到则空串）。
//
// ★ 为什么是**函数变量**而不是直接调 `os.Executable()`：这是 P3 判定在**生产形态**下
// 唯一不可由测试构造的输入 —— 测试二进制的 exe 落在 go 构建的临时目录，而生产二进制
// （本仓构建的二进制 `whale`）**住在自身源码树里**。不把它做成可注入的输入，
// 「二进制住在树里」这一形态**永远测不到**（2026-09-20 实测踩到：同一个 probe 只换 exe
// 位置，判定结果从 false 变 true）。
//
// ★ 它是**输入**而不是**开关**：换掉它只改变"我们看到的 exe 在哪"，不放宽也不收紧
// 任何判定逻辑本身（判定逻辑仍在 isSelfSourceTreeFrom 里，一行未动）。
// 唯一写入方是测试（`internal/permission` 与 `internal/tools` 的 判别器）。
var RunningExePath = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

// isSelfSourceTreeFrom 是判定本体；生产入口 = `IsSelfSourceTree`（注入真实 exe 路径）。
//
// 抽出来只为**可判别**：语义、分支顺序与 `IsSelfSourceTree` 完全一致，无第二套判据。
func isSelfSourceTreeFrom(workDir, exePath string) bool {
	if SelfModifyOptOutActive() {
		return false
	}
	// 来源 1（主形态）：工作区自己就是（或位于）whale 源码树内 —— 覆盖 dogfooding 正形态。
	if moduleRootFrom(workDir) != "" {
		return true
	}
	// 来源 2：**二进制住在 whale 源码树里** —— 覆盖「工作区是源码树的父目录」形态
	// （此时写 `<父目录>/whale-code/internal/...` 仍是在改 whale 自己），
	// 以及「用开发件跑完全无关的工作区」形态。
	//
	// ★ 本来源**不看 workDir 与自身树的关系**，这是 **P3 有意保留**的从严面（不是漏洞）：
	// 它只服务「可达面无法由入参界定」的任意命令类（`bash`/`Monitor`）—— 这类工具从
	// 二进制所在的位置随时能改到自身树。目标受限类改用 `PathInSelfSourceTree`
	// （只看目标、不看二进制），P3 的爆炸半径就是靠这个分流控制的。
	if strings.TrimSpace(exePath) == "" {
		return false
	}
	return moduleRootFrom(exePath) != ""
}

// CanModifyLocalState 报告该工具**能否改动本机持久化状态**。
//
// 复用既有的能力表 `toolCapabilities`（唯一真相源），不新增工具名单：
// `CapEdit` = 新建/修改/删除持久化本机状态；`CapExec` = 在本机跑任意命令/进程。
// 这两类正是"能改到 whale 自身树"的那两类 —— 其余（Read/Network/Mcp/Meta）改不到。
//
// ★ 未登记工具 `CapabilitiesOf` 返回 nil ⇒ 本函数返回 **false**。
// 方向说明：这里返回 false 表示"不额外收紧"，而**不**表示放行 ——
// 未登记工具在 PermissionStage 里由 `RequiredLevelOf` 按最高档 fail-closed 处理
// （level.go:93-97），拒绝面不受本函数影响。
func CanModifyLocalState(tool string) bool {
	for _, c := range CapabilitiesOf(tool) {
		if c == CapEdit || c == CapExec {
			return true
		}
	}
	return false
}
