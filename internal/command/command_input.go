package command

import "strings"

// · 「这段输入是不是命令」的单一权威判定（SSOT）。
//
// # 背景（真机踩中）
//
// 旧判据是裸 `strings.HasPrefix(input, "/")`，并在 8 处各写一遍（4 处判"是命令"、
// 4 处取反判"是真用户输入"）。它把「文件系统路径」与「命令」两个命名空间混成一个判据 ——
// 而以 `/` 开头的绝对路径在两个命名空间里都是合法表达。于是
//
//	/private/tmp//hello.txt，里面写hello
//
// 整串被当作命令名去查注册表（`，` 是中文逗号，不是分隔符，所以整串没有空白可切），
// 必然查不到 ⇒ 报 `Unknown: …`，且输入被吞掉、用户需重打。
//
// # 判据
//
// 本函数实现的是「命令命名空间」判据，不是「是否以 `/` 开头」：
//
//	① 命令必须以 `/` 为**第一个字符**：前导空白 ⇒ 一律转义为普通文本（此处刻意不 TrimSpace）
//	② 命令名不得为空（光一个 `/`）
//	③ 命令名不得含路径分隔符 `/`（路径形状 ⇒ 不是命令）
//	④ 命令名必须落在白名单字符集内（见 isCommandNameShaped）
//
// # 已裁决不做的两层（2026-09-20 用户拍板）
//
// - ⛔ **不做磁盘兜底**（不 `os.Stat("/"+name)`）：解析层不得依赖文件系统状态
// —— 引入磁盘 IO、把"文件系统存在性"耦进命令判定，并带来沙箱权限耦合风险。
// 代价：`/tmp`、`/private` 这类「名字里不含 `/`」的**单段绝对路径**仍会被判为命令 ⇒ **已知限制**（见下）。
//
// # 已知限制（正式记账，勿当作已修）
//
//	`/tmp`（裸目录名 + 紧跟空格/中文标点）仍判为命令并报 Unknown: /tmp。
//	`/tmp/x.txt`（名字里含 `/`）**已被 ③ 挡住**，不受影响。
//	待用户反馈积累到一定量后，再单开迭代引入磁盘判断。
//
// # 调用纪律
//
// **所有调用点（判定侧与取反侧）都必须走本函数**，不得再自行 `HasPrefix(input, "/")`。
// 该纪律由机械门 `.go` 强制。
//
// ★ **补全侧例外（但不是"绕开 SSOT"）**：`internal/typeahead` 问的是"该不该弹命令候选"，
// 与"提交会不会走命令调度"是**两个问题** ⇒ 它必须用同文件的 `IsCommandCompletionInput`
// （同一实现、仅 ② 取值相反），**不得**改回本地 `HasPrefix`。
// 机械门对两处一并覆盖：它只禁"裸 HasPrefix"，不限定走哪一条导出谓词。
//
// 完整依据、对标证据与 8 处调用点清单见工作区文档
// `债务清剿_TUI斜杠命令判定_同类问题与收口方案_2026-09-20.md`。
func IsCommandInput(input string) bool {
	// ★ name=="" 时**不是**命令：光一个 `/` 没有可执行的命令名（提交侧语义）。
	// 与补全侧的差别**仅此一处** —— 见 IsCommandCompletionInput。
	return commandInputVerdict(input, false)
}

// IsCommandCompletionInput 是「此刻该不该弹**命令候选**」的判据。
//
// ★ 它与 IsCommandInput **不是同一个问题**，不可互相替代（2026-09-20 批 C 实测发现）：
//
// - IsCommandInput 答「**提交**这串会不会走命令调度」⇒ 裸 `/` 没有命令名 ⇒ **false**。
// - 本函数答「**该不该弹命令候选**」⇒ 用户正打 `/` 就是要看全部命令 ⇒ **必须 true**。
//
// 只坍缩成一条会让「输入 `/` 看命令列表」这个基本 UX 消失
// （实测：typeahead 的 `TestExtractTokenWithTriggerSlash/empty_command` 立刻变红）。
// 两条谓词共用同一段实现（`commandInputVerdict`），差异被参数化为**一处** boolean，
// 而不是再抄一份判据本体 —— 这是"同一语义唯一声明"在**一对相邻问题**上的形态。
func IsCommandCompletionInput(input string) bool {
	// ★ name=="" 时**是**补全输入：与提交侧相反。
	return commandInputVerdict(input, true)
}

// commandInputVerdict 是上面两条谓词的**唯一实现**（规则 ①③④ 相同，仅 ② 按参数取值）。
//
//	① !HasPrefix(input, "/") ⇒ false ← 刻意不 TrimSpace：前导空白 = 正式逃逸语义
//	② name == "" ⇒ emptyNameIsInput（提交侧 false / 补全侧 true）
//	③ name 含 `/` 或 `\` ⇒ false ← 路径形状
//	④ !isCommandNameShaped ⇒ false ← 白名单
func commandInputVerdict(input string, emptyNameIsInput bool) bool {
	// ① 前导空白是**正式逃逸语义**：`/permissions` 是命令，` /permissions` 是普通文本。
	// 刻意不做 TrimSpace —— 否则"某个调用点恰好 trim 了"会变成隐式契约，
	// 而实测三处调用点对前导空白的处理本就互不一致（详见文档 §2.3）。
	if !strings.HasPrefix(input, "/") {
		return false
	}

	// 复用 Parse，保证「判定用哪一段做判据」与「执行时取哪个命令名」是同一套切名规则
	// （多词命令如 `session list` 由 Parse 内的 knownPrefixes 吃掉，不会在此处分叉）。
	name, _ := Parse(input)

	// ② 光一个 `/`（或 `/` 后全是空白）：提交侧不是命令；补全侧是补全入口。
	if name == "" {
		return emptyNameIsInput
	}

	// ③ 路径形状 ⇒ 不是命令。
	// ★ ④ 的白名单其实也覆盖了它，这里显式保留：一是与对标实现的判据逐条对应，
	// 二是将来若放宽 ④ 也不会连带漏掉这条。
	if strings.ContainsAny(name, `/\`) {
		return false
	}

	return isCommandNameShaped(name)
}

// isCommandNameShaped 报告 name 是否"长得像命令名"。
//
// 白名单 = 字母 / 数字 / `:` / `_` / `-` / 空格。
// 空格仅供 Parse 切出的**多词命令**使用（`session list`、`checkpoint save`、`daemon status` …
// —— 本仓确有这类注册名，所以空格必须在白名单内，不能收得更紧）。
//
// ★ 刻意**不含 `.`**：`.` 正是文件名 / 相对路径的标志。含 `.`、`/`、CJK、其它标点的
// token 一律判为"文件路径或其它输入"，交给模型按普通消息处理。
//
// 安全性依据（实测，非推断）：`internal/command` 全部已注册 `Name` 均满足本白名单；
// 动态命令（skill）名已被 `internal/skill` 归一化为 `[a-zA-Z0-9_-]`。
// 该不变量由 `TestN105_WhitelistCoversEveryRegisteredCommand` 守卫 ——
// 将来若有人注册含 `.` 的命令名，那条守卫会红，而不是被这里静默吞掉。
func isCommandNameShaped(name string) bool {
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == ':' || r == '_' || r == '-' || r == ' ':
		default:
			return false
		}
	}
	return true
}
