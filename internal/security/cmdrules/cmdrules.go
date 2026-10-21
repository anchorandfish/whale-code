// Package cmdrules 是「危险命令静态规则」的**唯一真相源**。
//
// 结构性定位（叶子包）：
//
//	本包**只依赖标准库**，不含任何 whale 内部依赖 ⇒ 任何层都可以安全 import 它，
//	不存在循环依赖；也不需要任何「初始化 / 依赖注入」。
//
// 为什么需要它（收敛背景）：
//
//		「这条命令危不危险」这一语义，此前在仓库里有**两份各写各的实现**：
//
//	  - permission.AnalyzeBashCommand 的 dangerousPatterns（Layer 3 风险分级，权威份）
//
//	  - memory.basicDangerCheck 的自建字符串黑名单（H2 未注入时的降级份）
//	    两份的模式表不同、匹配方式不同，于是同一批输入会给出不同答案（实测 3 处分歧）。
//	    memory 侧还因此承担了一个**子串匹配的误报**（`echo shutdown` 被判危险）。
//
//	    ⇒ 本包把「模式表 + 匹配方式」抽成单一实现；各消费方只保留**策略**
//	    （命中之后是 Deny / Ask / 拒注册，由各自决定），不再各自维护数据。
//
// 消费方（改动后）：
//
//	permission.AnalyzeBashCommand —— 危险档判定（风险分级）
//	permission.Policy —— 审批护栏（guardrailFires）+ 危险命令不入记忆
//	memory.basicDangerCheck —— probe 注册的硬拒绝门
//	sandbox/validator —— 复用经典 fork 炸弹正则（唯一声明）
//
// 消费方（-R1 · 2026-09-20 收口追加）：
//
//	tools.DangerousGuardStage —— 危险档的**管线承载体**（强制确认，早于所有免问出口）
//	tools.DestructiveCheckStage —— 破坏性档确认闸（名单改为消费本包，不再手抄）
//	tools/bash.AnalyzeSemantics —— 命令语义的「破坏性」判定（名单改为消费本包）
//	tools/powershell —— 破坏性 cmdlet 正则（名单改为消费本包）
//
// 两档的关系（**不要合并**）：
//
//	危险档（Dangerous）= 不可逆 / 灾难级 ⇒ 永远不得免问（管线强制确认）
//	破坏性档（Destructive）= 有副作用需确认，但可被沙箱边界兜住 ⇒ 命中即在确认闸提问
//	两者是**不同的语义**，不是同一档的严格度差异：`rm -rf /` 属危险档，
//	`rm build/` 属破坏性档。名单因此**分别声明**（confirmCommandNames / dangerousPatterns）。
//
// 两类匹配方式（都必要，不可互相替代）：
//
//	phrase 子串匹配：语义本身即危险（`rm -rf /`），出现即命中 —— 但**必须落在
//
// 它所属命令的段内**（见 Pattern.Heads）。裸子串匹配会误伤把危险词
// 当**字符串参数**用的合法命令（`grep -rn "rm -rf /" docs/`、
// `echo "never run rm -rf /"`）—— （2026-10-06 语料扫描实测）。
// 注：远程直通 shell 一族已改为 regex（见其声明处注释）—— 字面短语在真实形态下不命中。
//
//	command 命令位匹配：**单个命令名**（`shutdown`）。必须限定「它被当作命令执行」，
//
// 否则 `grep halt /var/log/x`、`echo shutdown`、`cat reboot.md` 这类
// 把关键字当**参数**用的合法命令会被误判。词边界（\b）不够 ——
// `\bshutdown\b` 仍会命中 `rm shutdown.txt`（`.` 是词边界）。
//
//	regex 正则：需要容忍书写变体（fork 炸弹、带 URL 的 `curl ... | sh`）。
//
// 带 Heads 的正则同样按命令位门控（`grep "curl | sh"` 不命中）。
//
// ★ 的另一半（安全方向，不可省）：按命令位门控后，**引号内容会被执行**的形态
// 必须**下探递归**再判一次 —— `sh -c "rm -rf /"`、`echo $(rm -rf /)`、`xargs rm -rf /`。
// 否则消误报的同时会把真危险放进来。见 executionPayloads。
package cmdrules

import (
	"regexp"
	"strings"
)

// Kind 规则的类型，决定匹配方式。
type Kind string

const (
	// KindPhrase 短语：按（小写）子串匹配，出现即命中。
	KindPhrase Kind = "phrase"
	// KindCommand 命令名：仅当出现在「命令位」时命中（见 commandHeads）。
	KindCommand Kind = "command"
	// KindRegex 正则：需要容忍空白/书写变体的模式（如 fork 炸弹）。
	KindRegex Kind = "regex"
)

// Pattern 一条危险命令规则。
//
// ID 是**稳定标识**：判据、日志、审计都应引用 ID，而不是 Value 或 Note 的文案
// （R4：可判定的结构化事实不得由散文关键词断言）。
//
// ★ ：Note / NoteZH 按全仓双语形态 SSOT 拆分 ——
// Note 是**英文主行**（可独立读懂，不得含汉字），NoteZH 是**中文辅行**（缩进另起一行，
// 由展示层用 util.Bi 拼接）。本包是 stdlib-only 叶子包，不 import internal/util
// ⇒ 只存数据，不存形态；展示层（tools/pipeline）负责拼。
type Pattern struct {
	ID    string
	Kind  Kind
	Value string
	// Note 英文主行理由（确认弹窗的 Risk 行主文本）。
	Note string
	// NoteZH 中文辅行理由（缩进显示；为空则只有英文主行）。
	NoteZH string
	// Heads 指定本规则**归属的命令位**（可多个）：短语/正则只在「段首命令名命中
	// Heads 之一」的段内匹配（见 segHeadIn）。空 = 保留旧的「全句子串」语义
	// （仅用于 `> /dev/sd` 这类**语法级**规则，它不属于任何单一命令名）。
	//
	// ★ ：没有这一层门控时，`grep -rn "rm -rf /" docs/` 会被
	// 判为危险命令（用户只是**搜索**了危险词）。命令位匹配的原语本就存在
	// （commandSegments/commandHeads， 已把破坏性档收口到它），危险档的
	// 短语表此前**没有跟上** —— 同一语义两套精度的典型形态。
	Heads []string
}

// dangerousPatterns 危险（Deny 档）规则表 —— 本表是**唯一**声明处。
//
// 表的来源是两份历史实现的**并集**（收敛时逐条核对）：
// - permission.AnalyzeBashCommand（权威份，19 条）
// - memory.basicDangerCheck（降级份，17 条）
//
// 并集带来的**行为收紧**（已在回执中如实登记）：
// - fork 炸弹、shutdown / reboot / halt、del /f /s /q、裸 mkfs
// 由原来的「Destructive → 需确认」升级为「Dangerous → 直接拒绝」。
// - 同批改动**消除**了降级份原有的误报：`echo shutdown` 之类不再命中。
var dangerousPatterns = []Pattern{
	// —— 递归/强行删除根或家目录（含 -fr 顺序变体、$HOME 变体）——
	{ID: "rm-rf-root", Kind: KindPhrase, Value: "rm -rf /", Heads: []string{"rm"}, Note: "Recursively delete the root path (also covers rm -rf /<any subpath>)", NoteZH: "递归删除根路径（同时覆盖 rm -rf /<任意子路径>）"},
	{ID: "rm-rf-root-glob", Kind: KindPhrase, Value: "rm -rf /*", Heads: []string{"rm"}, Note: "Recursively delete everything under root (kept: overlaps the previous rule, preserved for audit)", NoteZH: "递归删除根下全部（保留：与上一条重叠，历史表逐条保留以便审计）"},
	{ID: "rm-fr-root", Kind: KindPhrase, Value: "rm -fr /", Heads: []string{"rm"}, Note: "-fr order variant", NoteZH: "-fr 顺序变体"},
	{ID: "rm-fr-root-glob", Kind: KindPhrase, Value: "rm -fr /*", Heads: []string{"rm"}, Note: "-fr order variant (kept: overlaps the previous rule)", NoteZH: "-fr 顺序变体（保留：与上一条重叠）"},
	{ID: "rm-rf-home-tilde", Kind: KindPhrase, Value: "rm -rf ~", Heads: []string{"rm"}, Note: "Delete the home directory (~ variant)", NoteZH: "删除家目录（~ 变体）"},
	{ID: "rm-fr-home-tilde", Kind: KindPhrase, Value: "rm -fr ~", Heads: []string{"rm"}, Note: "Delete the home directory (-fr ~ variant)", NoteZH: "删除家目录（-fr ~ 变体）"},
	{ID: "rm-rf-home-env", Kind: KindPhrase, Value: "rm -rf $home", Heads: []string{"rm"}, Note: "Delete the home directory ($HOME variant; lowercased before matching)", NoteZH: "删除家目录（$HOME 变体；匹配前已小写）"},
	{ID: "rm-fr-home-env", Kind: KindPhrase, Value: "rm -fr $home", Heads: []string{"rm"}, Note: "Delete the home directory (-fr $HOME variant)", NoteZH: "删除家目录（-fr $HOME 变体）"},

	// —— 抹盘 / 覆盖块设备 ——
	{ID: "format-drive", Kind: KindPhrase, Value: "format c:", Heads: []string{"format"}, Note: "Format the system drive", NoteZH: "格式化系统盘"},
	{ID: "dd-read-disk", Kind: KindPhrase, Value: "dd if=", Heads: []string{"dd"}, Note: "Read from an arbitrary device/file and write out (dd is double-edged)", NoteZH: "从任意设备/文件读取并写出（dd 双刃）"},
	{ID: "dd-write-dev", Kind: KindPhrase, Value: "dd of=/dev", Heads: []string{"dd"}, Note: "Write directly to a block device", NoteZH: "直接写块设备"},
	// ★ 保留裸子串语义：这是**重定向语法**，不属于任何单一命令名
	// （`echo x > /dev/sda` 的命令位是 echo）。残留边界：`echo "> /dev/sda"` 仍会命中。
	{ID: "redirect-dev-block", Kind: KindPhrase, Value: "> /dev/sd", Note: "Redirect overwrite of a block device (syntax-level, no command position)", NoteZH: "重定向覆盖块设备（语法级，无命令位）"},
	{ID: "mkfs-any", Kind: KindPhrase, Value: "mkfs.", Heads: []string{"mkfs"}, Note: "Invoke mkfs.<fstype> (covers mkfs.ext4 etc.)", NoteZH: "调用 mkfs.<fstype>（覆盖 mkfs.ext4 等）"},

	// —— 远程脚本直通 shell（不可审计的供应链风险）——
	//
	// ★ -R1：这四条原为**子串短语**（`curl | sh`），实测发现它们
	// 在**真实形态下永不命中** —— `curl https://get.example.com | sh` 里 `curl` 与 `|`
	// 之间隔着 URL，字面 `curl | sh` 不是它的子串。而项目文档（ci_permission_stage2_test.go
	// 头注）一直把 `curl ... | sh` 列为"只有权限层护栏能拦"的样本 ⇒ 文档与事实相反。
	// 现改为正则：容忍中间的命令行参数。**规则 ID 与目标 shell 集合保持不变**
	// （仍只覆盖 sh / bash —— 不顺手扩到 zsh/dash 之外，也不扩到 `| sudo bash` 形态）。
	//
	// ⚠️ 历史边界（· 2026-10-06 已关闭）：改前 `echo curl x | sh` 这类"把 curl 当参数"
	// 的形态也会命中。现在本族与短语族统一加**命令位归属**（Heads）⇒
	// 仅当段首命令确为 curl/wget 时才求值，`grep 'curl | sh' docs/` 不再误报；
	// 经 shell 包装的形态（`bash -c "curl x | sh"`）由下探递归兜住。
	{ID: "curl-pipe-sh", Kind: KindRegex, Value: curlPipeShellSource, Heads: []string{"curl", "wget"}, Note: "Script fetched by curl piped straight to sh (tolerates intermediate arguments)", NoteZH: "curl 取回的脚本直通 sh（容忍中间参数）"},
	{ID: "curl-pipe-bash", Kind: KindRegex, Value: curlPipeBashSource, Heads: []string{"curl", "wget"}, Note: "Script fetched by curl piped straight to bash (tolerates intermediate arguments)", NoteZH: "curl 取回的脚本直通 bash（容忍中间参数）"},
	{ID: "wget-pipe-sh", Kind: KindRegex, Value: wgetPipeShellSource, Heads: []string{"curl", "wget"}, Note: "Script fetched by wget piped straight to sh (tolerates intermediate arguments)", NoteZH: "wget 取回的脚本直通 sh（容忍中间参数）"},
	{ID: "wget-pipe-bash", Kind: KindRegex, Value: wgetPipeBashSource, Heads: []string{"curl", "wget"}, Note: "Script fetched by wget piped straight to bash (tolerates intermediate arguments)", NoteZH: "wget 取回的脚本直通 bash（容忍中间参数）"},

	// —— 强制改写共享历史 ——
	{ID: "git-push-force-long", Kind: KindPhrase, Value: "git push --force", Heads: []string{"git"}, Note: "Force push (rewrites remote history)", NoteZH: "强制推送（改写远端历史）"},
	{ID: "git-push-force-short", Kind: KindPhrase, Value: "git push -f", Heads: []string{"git"}, Note: "Force push (short option)", NoteZH: "强制推送（短选项）"},

	// —— 删除（含 Windows 语义）——
	{ID: "del-force-recursive", Kind: KindPhrase, Value: "del /f /s /q", Heads: []string{"del"}, Note: "Windows forced recursive silent delete", NoteZH: "Windows 强制递归静默删除"},

	// —— fork 炸弹（正则：容忍空格变体）——
	{ID: "fork-bomb-classic", Kind: KindRegex, Value: forkBombClassicSource, Note: "Classic :(){ :|:& };: and its whitespace variants", NoteZH: "经典 :(){ :|:& };: 及其空格变体"},
}

// dangerousCommands 必须以「命令位」匹配的危险命令名。
//
// 用命令位而非子串/词边界，是为了**不误报把关键字当参数用的合法命令**
// （`grep halt x` / `echo shutdown` / `rm shutdown.txt` 都不应命中）。
var dangerousCommands = []Pattern{
	{ID: "shutdown", Kind: KindCommand, Value: "shutdown", Note: "Shut down the machine", NoteZH: "关机"},
	{ID: "reboot", Kind: KindCommand, Value: "reboot", Note: "Reboot the machine", NoteZH: "重启"},
	{ID: "halt", Kind: KindCommand, Value: "halt", Note: "Halt the machine", NoteZH: "停机"},
	{ID: "mkfs-bare", Kind: KindCommand, Value: "mkfs", Note: "Bare mkfs (mkfs.<fstype> is covered by a phrase rule)", NoteZH: "裸 mkfs（mkfs.<fstype> 由 phrase 规则覆盖）"},
}

// confirmCommandNames 「破坏性档·确认闸」命令名 —— 本档的**唯一声明处**。
//
// 语义：命中即「需要用户确认」（`DestructiveCheckStage` / `AnalyzeBashCommand` 的 Destructive）。
// 这一档刻意**只收重度不可逆**的名字 —— 它不含 chmod/chown：
// 后两者改的是元数据、可被沙箱边界兜住且可逆，放进确认闸只会制造弹框疲劳
// （`chmod +x build.sh` 这种在任务循环里高频出现）。
//
// 历史背景（为什么要有 `ConfirmCommandNames` 与 `DestructiveCommandNames` 之分）：
// 收敛前同一语义有三份各写各的名单 ——
//
//	permission.AnalyzeBashCommand 侧 {rm,rmdir,mv,chmod,chown}
//	tools/bash 的 destructiveCommands {rm,rmdir,mv,shred,mkfs}
//	tools/pipeline 的 destructiveBashCommands {rm,rmdir,mv,shred,mkfs,dd,fdisk,parted}
//
// 三份的交集只有 rm/rmdir/mv/mkfs。差异并非设计，而是历史漂移；
// 现按「用途分层」重新声明为两个**派生视图**（下面两个导出函数），各自只有一份数据。
var confirmCommandNames = []string{"rm", "rmdir", "mv", "shred", "mkfs", "dd", "fdisk", "parted"}

// riskOnlyCommandNames 只参与**风险分级**（`AnalyzeBashCommand`）的权限类命令名。
//
// 为什么不进确认闸：见 confirmCommandNames 注释（可逆 + 沙箱可兜 + 高频）。
// 但它们确实会改动文件元数据，风险分级必须知道它们。
var riskOnlyCommandNames = []string{"chmod", "chown"}

// destructiveCmdlets PowerShell 破坏性 cmdlet —— 本档的**唯一声明处**。
//
// 此前 `tools/pipeline.destructivePSCmdlets` 与 `tools/powershell.destructiveCmdletPattern`
// 各写一份**逐字相同**的 9 项清单；现由两处共同消费本切片
// （powershell 侧据它编译正则，pipeline 侧据它建集合）。
//
// ⚠️ 未验证边界：PowerShell 侧至今**没有真机手测**（只在 Linux/macOS 上跑单测），
// 故本轮只做「单一声明」，不夹带任何行为变更。
var destructiveCmdlets = []string{
	"Remove-Item", "Format-Volume", "Clear-Content", "Clear-Item",
	"Stop-Process", "Stop-Service", "Stop-Computer", "Restart-Computer",
	"Dismount-VHD",
}

// destructivePhrases 破坏性短语（子串匹配）。
var destructivePhrases = []string{
	"git push", "git commit", "git merge", "git rebase",
	"docker rm", "docker rmi", "docker system prune",
	"npm publish", "pip upload",
	"remove-item", "move-item", "set-acl",
}

// readOnlyCommands 只读档的**命令名**（按「命令位」匹配，同 dangerousCommands 的理由）。
//
// ★ P-015：本档此前是**一整张裸子串表**，且含裸 `"ls"` ⇒
// `strings.Contains` 会把任何含 `ls` 两个字母的命令判成只读。实测命中（修前）：
//
//	`go test ./internal/tools/` ⇒ 只读（路径含 `tools`）—— 而 `go test` 会执行仓内任意代码
//	`go test ./...` ⇒ destructive（不含 ls）—— **分界只取决于路径里有没有 ls**
//	`make -C tools` / `docker build -t tools .` / `false` / `lsblk` ⇒ 全部只读
//
// 只读档的消费面 = 「免确认」＋「沙箱在强制档下命中免问出口 (c)」＋
// 「审批记忆按可执行名合并的前置条件」⇒ **误报方向是放宽（危险）**，
// 故单 token 命令名一律按**命令位**匹配（与 `dangerousCommands` 同一纪律）。
//
// ⚠️ 判据方向：只读档**误报才危险**，漏报只是多问一次 ⇒ 本表刻意取窄：宁多问，不放过。
var readOnlyCommands = []string{
	"ls", "pwd", "whoami", "hostname", "uname",
	"cat", "head", "tail", "less", "more",
	"grep", "find", "which", "where", "whereis",
	"echo", "sleep", "seq",
	"get-childitem", "get-content", "get-process", "get-service",
	"write-output", "test-path",
}

// readOnlyPhrases 只读档的**多词形态**（命令 + 子命令；按「段首前缀」匹配，**不是裸子串**）。
//
// 注：`sleep` / `seq` 本身无副作用，但组合命令（`sleep 60 && rm x`）仍会被
// 破坏性/危险档先命中 —— 那两档的检查顺序在前（`AnalyzeBashCommand` 分级顺序 2/3/4 先于 5）。
//
// ⚠️ 已知残留（本批**未**处理，已登记台账 P-016）：`git branch -D x` / `git tag -d v1` /
// `git remote remove o` 这类「只读动词 + 破坏性开关」仍会被判只读。
// 它属**子命令粒度**问题，与本批修的「裸子串」并不同源，故不在本批扩大改动面。
var readOnlyPhrases = []string{
	"git status", "git log", "git diff", "git branch",
	"git remote", "git tag", "git stash list",
	"node --version", "python --version", "go version",
	"cargo --version", "rustc --version",
}

// DangerousPatterns 返回危险档规则表的副本（调用方不得依赖其可写性）。
func DangerousPatterns() []Pattern {
	out := make([]Pattern, len(dangerousPatterns))
	copy(out, dangerousPatterns)
	return out
}

// DestructiveCommandNames 返回「风险分级」用的破坏性命令名 = 确认闸集合 ∪ 权限类集合。
//
// 消费方：`permission.AnalyzeBashCommand`（决定 BashRiskDestructive）。
// 注意它**宽于**确认闸（多 chmod/chown）—— 需要"要不要弹框"的消费方请用 ConfirmCommandNames。
func DestructiveCommandNames() []string {
	out := make([]string, 0, len(confirmCommandNames)+len(riskOnlyCommandNames))
	out = append(out, confirmCommandNames...)
	out = append(out, riskOnlyCommandNames...)
	return out
}

// ConfirmCommandNames 返回「破坏性档·确认闸」命令名（重度不可逆，命中需用户确认）。
//
// 消费方：`tools.pipeline` 的确认闸、`tools/bash` 的命令语义。
// 返回副本，调用方不得依赖其可写性。
func ConfirmCommandNames() []string { return append([]string(nil), confirmCommandNames...) }

// DestructiveCmdlets 返回 PowerShell 破坏性 cmdlet 名单（唯一声明）。
// 返回副本，调用方不得依赖其可写性。
func DestructiveCmdlets() []string { return append([]string(nil), destructiveCmdlets...) }

// DestructivePhrases 返回破坏性档短语（子串匹配）。
func DestructivePhrases() []string { return append([]string(nil), destructivePhrases...) }

// ReadOnlyCommands 返回只读档命令名（按命令位匹配）。返回副本，调用方不得依赖其可写性。
func ReadOnlyCommands() []string { return append([]string(nil), readOnlyCommands...) }

// ReadOnlyPhrases 返回只读档多词形态（按段首前缀匹配）。返回副本。
func ReadOnlyPhrases() []string { return append([]string(nil), readOnlyPhrases...) }

// MatchReadOnly 判定命令是否为**只读**（不改文件系统、无副作用）。
//
// 匹配规则（与危险档同源的纪律：**必须落在命令位**）：
//
//	单 token 命令名：段首词 == 表项 （`ls -la` ✓ / `lsblk` ✗ / `…/tools/` ✗）
//	多词形态： 段首以「表项 + 空格」开头（`git status -s` ✓ / `xgit status` ✗）
//
// 段切分与前置剥离（`KEY=VALUE`、`sudo` 等 wrappers）复用 commandSegments ——
// 与 commandHeads（危险档的命令位）**同一份实现**，避免两套口径各自漂移。
//
// ★ P-015：修前本档是裸子串匹配，`"ls"` 会命中 `tools`/`false`/`lsblk`。见 readOnlyCommands 注释。
func MatchReadOnly(cmd string) bool {
	norm := Normalize(cmd)
	if norm == "" {
		return false
	}
	lower := strings.ToLower(norm)
	for _, rest := range commandSegments(lower) {
		for _, name := range readOnlyCommands {
			if rest == name || strings.HasPrefix(rest, name+" ") {
				return true
			}
		}
		for _, p := range readOnlyPhrases {
			if rest == p || strings.HasPrefix(rest, p+" ") {
				return true
			}
		}
	}
	return false
}

// CommandRegions 返回命令串中每个命令段在「剥掉前置环境赋值与包装命令」之后的剩余文本（小写）。
//
// ★ 同源补漏：本函数是**导出的**命令位原语，供 `internal/sandbox`
// 的 deny 判定与 `commandHeads`（危险档）/ `MatchReadOnly`（只读档）**共用同一份
// 段切分 + 前置剥离**。导出而非让 sandbox 自写一份，理由与 P-015 相同：
// 同一语义两份实现必然漂移（本仓已多次吃过： / / P-015）。
//
// 为什么是「剩余文本」而不是「命令名」：deny 表 / 只读短语表里存在**多词形态**
// （`cat /tmp/secret` / `git status`），消费方需要按「段首前缀」匹配，故返回文本。
//
// ⚠️ 边界（与 commandHeads 逐条相同，见该函数注释）：不处理引号内的分隔符、
// 命令替换 `$(...)`、xargs / find -exec 后的次级命令。
// **需要「保守下探」的消费方应在此之上自行叠加** —— 方向差异是刻意的：
// 拦（deny）下探只会多拦、是安全的；放行（只读档）下探会**放松**，
// 故 `wrappers` 表刻意**不含** sh/bash/xargs（见 sandbox.denyProbeRegions 注释）。
func CommandRegions(cmd string) []string {
	return commandSegments(strings.ToLower(Normalize(cmd)))
}

// MatchRiskDestructive 判定命令是否命中「风险分级·破坏性命令名」，**按命令位**匹配。
//
// 集合 = confirmCommandNames ∪ riskOnlyCommandNames（= DestructiveCommandNames，
// **宽于**确认闸：多 chmod/chown —— 风险分级需要知道可逆但仍改元数据的命令）。
//
// ★ 同源补漏：消费方 `permission.AnalyzeBashCommand` 的第 3 步
// 此前用本包**之外**的 `wordBoundaryMatch`（整串词边界）遍历这张表 ⇒
// `grep -rn rm .` / `grep -rn chmod Makefile` / `ls -la | grep dd` 被判
// 「破坏性 ⇒ 需确认」—— 而它们的命令位是 grep（只读）。
// 同一理由本包注释已写明（见第 44-47 行）：词边界 `\b` 不够，`\bshutdown\b`
// 仍会命中 `rm shutdown.txt`。故收口到命令位，与 MatchDestructive / MatchReadOnly 同源。
func MatchRiskDestructive(cmd string) bool {
	norm := Normalize(cmd)
	if norm == "" {
		return false
	}
	lower := strings.ToLower(norm)
	for _, head := range commandHeads(lower) {
		for _, name := range confirmCommandNames {
			if head == name {
				return true
			}
		}
		for _, name := range riskOnlyCommandNames {
			if head == name {
				return true
			}
		}
	}
	return false
}

// forkBombClassicSource 经典 fork 炸弹的函数定义部分（唯一声明）。
// 注释里的说明与 validator 侧历史注释一致：
// 容忍任意空格变化；仅匹配函数定义部分（`}` 之前），不依赖尾部 `;:` 调用。
const forkBombClassicSource = `:\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&\s*\}`

// 远程脚本直通 shell 的四个模式源（-R1）—— 容忍 `curl`/`wget` 与 `|` 之间
// 的命令行参数（真实形态必然带 URL）。判定串已小写化，故无需 (?i)。
//
// 边界刻意收窄：`[^\n|]*` 只跳过管道符之前的内容（不会跨过 `|`），
// 目标 shell 用 `\b` 界定（`bash` 不会命中 `bashful`）。
const (
	curlPipeShellSource = `\bcurl\b[^\n|]*\|\s*sh\b`
	curlPipeBashSource  = `\bcurl\b[^\n|]*\|\s*bash\b`
	wgetPipeShellSource = `\bwget\b[^\n|]*\|\s*sh\b`
	wgetPipeBashSource  = `\bwget\b[^\n|]*\|\s*bash\b`
)

// ForkBombClassicRe 返回经典 fork 炸弹正则（编译一次，全局复用）。
// sandbox/validator 的字符串匹配层复用本正则，避免同一模式两处声明。
var ForkBombClassicRe = regexp.MustCompile(forkBombClassicSource)

// Normalize 做安全检查前的命令归一化：去掉首尾空白 + 把**行内**连续空白压缩为单个空格。
//
// 目的：抵御 `rm -rf /`（多空格）这类**纯书写差异**绕过子串匹配。
// 归一化只用于检查，调用方传入的原命令不受影响。
//
// ★ 残余①：**换行不在归一化范围内**。
//
// 修前这里把 `\n` 与 `\t`/`\r`/`' '` 同类折叠 ⇒ **两条命令被并成一段** ⇒ 所有
// 「按命令位判定」的消费方（`MatchDangerous` / `MatchRiskDestructive` / `MatchReadOnly` /
// `AllSegmentsReadOnly` / `CommandRegions`）都只看**第一段的段首词**。实测两个方向：
// - 只读面（免问）：`cat a\nmkdir b` 被判「全段只读」⇒ 免问出口**静默放行** mkdir；
// - 危险面（deny）：`# 注释\nrm -rf /tmp/x` 段首词成 `#` ⇒ 危险档**漏判**。
// （`cat a\nrm -rf /tmp/x` 同理 —— 见 newline_segment_test.go 同源面判据。）
//
// 为什么必须保留：归一化的职责是消**书写差异**；而 `\n` 是 shell 的**命令分隔符**，
// 它改变命令的**语义**（一条 vs 两条）。把它当空白折掉，就是把**结构性事实**降级成
// **书写噪声** —— 与本批主题（拿代理顶替真值）同源。行内空白仍折叠，故
// `rm -rf /` 的防御不受影响（判据 TestW025_WhitespacePaddingDefense_StillHolds 钉住）。
//
// 附：CRLF 先归成 LF；**连续换行折叠成一个分隔符**（空命令是 shell 的 no-op，
// 丢弃不改变语义，却能让 `a\n\nb` 不产生空段而误判「非全段只读」）。
func Normalize(cmd string) string {
	cmd = strings.ReplaceAll(cmd, "\r\n", "\n")
	lines := strings.Split(cmd, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if ln = collapseInlineSpaces(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}

// collapseInlineSpaces 是 Normalize 的**行内**归一化：去首尾空白 + 连续空白压成单空格。
// 它**不**处理 `\n` —— 换行由调用方（Normalize）作为分隔符保留。
func collapseInlineSpaces(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, ch := range s {
		if ch == '\t' || ch == '\r' || ch == ' ' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(ch)
		prevSpace = false
	}
	return b.String()
}

// MatchDangerous 判定命令是否命中危险档规则。
// 命中时返回命中的规则（可据 ID 做机器判定），否则 ok == false。
//
// 匹配顺序：短语 → 正则 → 命令位 → **执行载荷下探**。顺序只影响「返回哪一条命中」，
// 不影响「是否危险」这一布尔结论（各规则集合互不包含语义）。
//
// ★ ：短语/正则规则先在**它归属的命令位**上过一道门控
// （Pattern.Heads），再对「引号内容会被再次执行」的载荷递归下探。
// 前者消误报（`grep "rm -rf /"`）、后者防放松（`sh -c "rm -rf /"`）—— 两半缺一不可。
func MatchDangerous(cmd string) (Pattern, bool) {
	return matchDangerousDepth(cmd, 0)
}

// maxDangerDepth 下探深度上限（防构造嵌套命令导致栈/耗时放大）。
const maxDangerDepth = 3

func matchDangerousDepth(cmd string, depth int) (Pattern, bool) {
	norm := Normalize(cmd)
	if norm == "" {
		return Pattern{}, false
	}
	lower := strings.ToLower(norm)
	segs := commandSegments(lower)

	for _, p := range dangerousPatterns {
		switch p.Kind {
		case KindPhrase:
			if len(p.Heads) == 0 {
				// 语法级规则（无命令位归属）：保留旧的裸子串语义。
				if strings.Contains(lower, p.Value) {
					return p, true
				}
				continue
			}
			for _, seg := range segs {
				if segHeadIn(seg, p.Heads) && strings.Contains(seg, p.Value) {
					return p, true
				}
			}
		case KindRegex:
			re, ok := compiledRegex(p.ID)
			if !ok {
				continue
			}
			if len(p.Heads) == 0 {
				if re.MatchString(lower) {
					return p, true
				}
				continue
			}
			// 正则本身跨段（`curl … | sh` 的 `|` 前后是两段）⇒ 先按命令位门控，
			// 再对整串求值。门控保证匹配起点那条命令确实是 curl/wget。
			for _, seg := range segs {
				if segHeadIn(seg, p.Heads) && re.MatchString(lower) {
					return p, true
				}
			}
		}
	}

	if heads := commandHeads(lower); len(heads) > 0 {
		for _, p := range dangerousCommands {
			for _, h := range heads {
				if h == p.Value {
					return p, true
				}
			}
		}
	}

	// 执行载荷下探：引号/替换里的内容会被 shell 再次执行 ⇒ 必须递归判定一次。
	if depth < maxDangerDepth {
		for _, payload := range executionPayloads(lower) {
			if p, ok := matchDangerousDepth(payload, depth+1); ok {
				return p, true
			}
		}
	}

	return Pattern{}, false
}

// segHeadIn 判断命令段 seg 的**段首命令名**是否命中 heads 之一。
//
// 段文本已由 commandSegments 剥掉前置环境赋值与包装命令（`KEY=VALUE`、`sudo` 等），
// 故直接取首 token 比对即可。允许「首 token 以 head 加点号开头」的形态
// （`mkfs.ext4` ← head `mkfs`），与 dangerousCommands 对 mkfs 的处理一致。
func segHeadIn(seg string, heads []string) bool {
	token := seg
	if i := strings.IndexByte(seg, ' '); i >= 0 {
		token = seg[:i]
	}
	if token == "" {
		return false
	}
	for _, h := range heads {
		if token == h || strings.HasPrefix(token, h+".") {
			return true
		}
	}
	return false
}

// executionWrapperHeads 会把其参数**再次执行**的命令名 —— 下探用。
//
// ⚠️ 方向差异是刻意的（与 commandSegments 注释第 310-312 行同源）：
// **危险档下探只会多拦、是安全的**；只读档下探会放松，故那边刻意不含这些。
var executionWrapperHeads = []string{"sh", "bash", "zsh", "dash", "ksh", "ash"}

// executionPayloads 抽出命令串里**会被再次执行**的载荷文本。
//
// 覆盖形态（保守方向，宁多拦不放过）：
//
//	$( … ) / 反引号 命令替换（任意上下文都会执行）
//	sh -c "…" / bash -c '…' shell 包装的脚本参数
//	eval … eval 的剩余参数
//	xargs … 被 xargs 执行的后继命令
//	find … -exec … 被 -exec 执行的后继命令
//
// ★ 为什么不是「剥掉所有引号」：那样会把 `sh -c "rm -rf /"`（引号内**会被执行**）
// 一并放掉，反而放松安全语义。这里只把「**执行型上下文**里的引号内容」当成载荷，
// 其余引号（`grep "rm -rf /"`）仍视作惰性字符串。
func executionPayloads(lower string) []string {
	var out []string

	// ① 命令替换与反引号（任意上下文都执行）
	out = append(out, substitutionPayloads(lower)...)

	// ② 执行型命令后面紧跟的引号内容（`sh -c "…"` / `eval "…"`）
	out = append(out, quotedAfterExecutionHead(lower)...)

	// ③ 段级的后继命令（xargs / find -exec / 无引号的 shell -c）
	//
	// ⚠️ 段切分**不识别引号内分隔符**（commandHeads 的已知边界）⇒ `bash -c "curl x | sh"`
	// 会被切成两段。因此这一路只作为②的补充（②在原始串上扫描，不受切段影响）。
	for _, seg := range commandSegments(lower) {
		if segHeadIn(seg, executionWrapperHeads) {
			if p := shellCArg(seg); p != "" {
				out = append(out, p)
			}
			continue
		}
		if seg == "eval" || strings.HasPrefix(seg, "eval ") {
			out = append(out, strings.TrimPrefix(seg, "eval"))
			continue
		}
		if seg == "xargs" || strings.HasPrefix(seg, "xargs ") {
			out = append(out, stripXargsOptions(strings.TrimPrefix(seg, "xargs")))
			continue
		}
		if i := strings.Index(seg, "-exec "); i >= 0 {
			out = append(out, seg[i+len("-exec "):])
		}
	}
	return out
}

// ExecutionPayloads 返回命令串里**会被再次执行**的载荷文本 —— **本仓唯一声明处**。
//
// 为什么必须只有一个声明：“（危险档下探）与 “（替换体递归校验）
// 都要回答"这条命令里有哪些内容会被 shell 换个地方再执行一次"。两边各写一份抽取
// 逻辑，就会像 `dangerousPatterns` / `basicDangerCheck` 那样**各自漂移**（实测过 3 处分歧）。
// ⇒ 抽取归本包（叶子包，任何层可 import），判定策略归各消费方。
//
// 覆盖（保守方向，宁多拦不放过）：`$( … )` / `<( … )` / `>( … )` / `=( … )`（zsh）/ 反引号，
// 以及执行型包装后的内容 —— `sh -c "…"`、`eval …`、`xargs …`、`find … -exec …`。
//
// 边界（如实登记，方向为多拦）：不做引号感知 —— `echo '$(date)'`（单引号内**不会执行**）
// 也会被抽出并判定。因为判定只看内层良莠，良性内层仍会放行；危险内层被拦属过度保守，
// 与本包既有的「段切分不认引号内分隔符」同类。
func ExecutionPayloads(cmd string) []string {
	norm := Normalize(cmd)
	if norm == "" {
		return nil
	}
	return executionPayloads(strings.ToLower(norm))
}

// UnbalancedSubstitution 报告命令串里是否存在**取不出配对**的执行型替换。
//
// 与 ExecutionPayloads 配对使用：抽取结果为空 **且** 本函数为真 ⇒ 那不是"没有替换"，
// 而是"有替换但解析不出来"。调用方此时必须 **fail-closed**（拒）——
// 把"解析失败"当成"没问题"是 同族的错法。
func UnbalancedSubstitution(cmd string) bool {
	norm := Normalize(cmd)
	if norm == "" {
		return false
	}
	lower := strings.ToLower(norm)
	// 反引号：奇数个 ⇒ 必有一个未闭合
	if strings.Count(lower, "`")%2 == 1 {
		return true
	}
	for i := 0; i < len(lower); i++ {
		open := substitutionOpenParen(lower, i)
		if open < 0 {
			continue
		}
		if _, _, ok := parenBody(lower, open); !ok {
			return true
		}
	}
	return false
}

// IsSimpleCommand 报告命令是否为「单一简单命令」——
// 恰好一段、无命令/进程替换、无反引号、无重定向。
//
// ★ （2026-10-06 用户拍板「允许该命令前缀」）的**结构面判据**。
// 「允许前缀」天然是一条**放宽**规则（一旦成立，后续同头调用不再询问），
// 放宽规则的边界必须由**结构**划定，而不是"再扫一遍危不危险"：
// - `sleep 5 && rm -rf x` 的头部也是 `sleep`，但它**不是**"以 sleep 开头的简单调用"，
// 而是一串调用 ⇒ 复合/含替换/含重定向的命令**根本进不了前缀范围**。
//
// 于是「复合命令不被前缀豁免」是结构性保证，不依赖任何二次危险扫描。
//
// 判据与其消费方（`permission`）共用本包的段切分与载荷抽取原语，避免"两处各写一遍"。
func IsSimpleCommand(cmd string) bool {
	norm := Normalize(cmd)
	if norm == "" {
		return false
	}
	lower := strings.ToLower(norm)
	// ① 恰好一段（无 && || ; | & 换行）
	if len(splitCommandSegments(lower)) != 1 {
		return false
	}
	// ② 无执行型替换 / 反引号，且不存在"取不出配对"的替换（后者必须 fail-closed）
	if UnbalancedSubstitution(cmd) {
		return false
	}
	if len(ExecutionPayloads(cmd)) > 0 {
		return false
	}
	// ③ 无重定向（前缀豁免不覆盖"写入面会变"的调用）
	if strings.ContainsAny(lower, "<>") {
		return false
	}
	return true
}

// CommandHead 返回「单一简单命令」的**前缀头词**：归一化后的第一个 token
// （小写、去路径 basename）；非单一简单命令一律返回 ""。
//
// ★ 与 commandHeads（命令位原语）的**刻意差异**：本函数**不剥离**前置环境赋值
// 与权限 wrapper —— 前缀的语义是「以相同字面文本开头」。若剥掉 wrapper，
// `sudo sleep 5` 与 `sleep 5` 会塌成同一个前缀 ⇒ 用户批准 `sleep` 就等于顺带
// 批准了 `sudo`（提权）。放宽规则必须收窄，故此处保守保留字面 token。
// 同理 `FOO=1 seq 1 9` 的头词是 `foo=1`，不与 `seq` 交叉匹配。
func CommandHead(cmd string) string {
	if !IsSimpleCommand(cmd) {
		return ""
	}
	return headWord(Normalize(cmd))
}

// headWord 取一段命令文本的**可执行名**：首 token → 去目录 → 小写。
//
// 与 `CommandHead` 的关系：后者 = 「先判 IsSimpleCommand，再取首 token」；本函数是
// 纯粹的取词原语（不含"是不是简单命令"的判断），供 `CommandHead` 与
// `AllSegmentsReadOnly` 共用 —— 避免"两处各写一遍去路径/小写"（那类漂移）。
//
// ⚠️ 刻意**不**剥前置环境赋值与 wrapper（`FOO=1 ls` / `sudo ls` 的首 token 就是
// `foo=1` / `sudo`）：本函数只服务**放宽**面（前缀豁免 / 只读免问），
// 剥 wrapper 会让 `sudo ls` 冒充 `ls` ⇒ 方向相反。需要剥离语义的消费方
// 请用 `commandSegments`（危险档 / 只读档的命令位口径）。
func headWord(seg string) string {
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return ""
	}
	head := strings.ToLower(fields[0])
	if idx := strings.LastIndex(head, "/"); idx >= 0 {
		head = head[idx+1:]
	}
	return head
}

// alwaysReadOnlyExecutables 是「**任意参数下都只读**」的可执行名白名单 —— 本仓唯一声明处。
//
// 成员判据（新增成员须同时补测试）：任意参数下都只读 —— 不写文件系统、不派生写操作、
// 不起 pager/编辑器、不执行子命令。
//
// 显式排除「名字像只读但参数可写/可执行」的常见陷阱：
// - find（-delete / -fprint / -exec）、less|more（`!cmd`、`s file`）、
// - sort|uniq（-o）、sed|awk（-i / 脚本内写）、git（diff --output 等）、
// - tee、xargs、env（可执行任意命令）、vim|nano 等编辑器。
//
// 业内对照（同类 CLI）：`shell-command/src/command_safety/is_safe_command.rs` 走的是
// 「先按命令名进名单、再逐条排除危险开关」（`:114-131` 排除 `find -delete/-exec`、
// `:135-154` 排除 `rg --pre`）。本仓方向相反但等价：**参数可写的命令根本不进名单** ——
// 名单越窄，越不需要第二套"危险开关"判据（判据越少越不容易漂移）。
//
// ★ 2026-10-06收口：本表原在 `internal/permission`（名
// `scopeCollapsibleExecutables`，服务"审批记忆按可执行名合并授权"）。 需要**同一份**
// "任意参数下都只读"的知识来做**免问**判据 ⇒ 移到命令规则的叶子包，permission 改为消费
// `IsAlwaysReadOnlyExecutable`。这与 `CommandRegions` / `ExecutionPayloads` 的导出理由同款：
// 同一语义只留一处声明。
//
// ★ 与 `readOnlyCommands` 的关系（**两个不同消费面，必须两份**）：
// - `readOnlyCommands` = 「**风险分级**用的只读档」：**宽**（含 find/less/git 这些"通常只读"），
// 误报方向是**放宽**（把危险命令判成只读），故按命令位匹配；
// - 本表 = 「**免问**用的严格档」：**窄**（只留任意参数下都只读的），任一误入即静默放行。
// ⇒ 两者的方向都是"宁可多问"，但**严格度不同**，不能合并。
//
// 说明：即便某条只读命令被免问，**deny 规则与 OS 沙箱仍然生效**
// （pipeline.go：免问出口只抑制交互确认，权限检查照常执行），故爆炸半径有界。
var alwaysReadOnlyExecutables = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "grep": true,
	"which": true, "where": true, "whereis": true,
	"echo": true, "pwd": true, "whoami": true, "hostname": true,
	"uname": true, "sleep": true, "seq": true,
}

// IsAlwaysReadOnlyExecutable 报告某个**可执行名**是否属于「任意参数下都只读」白名单。
//
// 消费方：`permission.commandScopeKey`（审批记忆按可执行名合并的前置条件）与
// `AllSegmentsReadOnly`（只读免问出口）。
func IsAlwaysReadOnlyExecutable(name string) bool {
	return alwaysReadOnlyExecutables[strings.ToLower(name)]
}

// AllSegmentsReadOnly 报告命令是否为「**每一段**都是只读命令」——全段口径。
//
// ★ （2026-10-06 用户拍板）的**结构面判据**：ww 档下 gate ① 按"**工具**是否只读"
// 定级（bash 能写 ⇒ 条条问），与具体命令无关 ⇒ `ls -la` / `sleep 70 && echo x` 都弹框。
// 本判据把"具体命令确实只读"这一事实接回免问路径。
//
// 为什么**不能**复用 `MatchReadOnly`：它是**任一段**命中即只读（本文件 `:304-317`）⇒
// `weirdcmd && echo hi` 会被判只读 ⇒ 拿它免问就是**静默放行** `weirdcmd`。
// 免问是**放宽**方向，必须全段成立。
//
// 判据（逐条，任一不满足即 false = 保持询问）：
// 1. 非空；
// 2. **无取不出配对的替换**（`UnbalancedSubstitution`）—— "解析失败"≠"没问题"（同族）；
// 3. **无执行型载荷**（`ExecutionPayloads`）—— 载荷内容不在本判据可见范围内，不能宣称只读；
// 4. **无重定向**（`<` `>`）—— 重定向即写入面；
// 5. 按 shell 分隔符切段后，**每一段**的首 token（去路径/小写）都在
// `alwaysReadOnlyExecutables` 内，且**不存在空段**（`ls &&` 这类残留分隔符 ⇒ fail-closed）。
//
// 已知边界（如实登记，方向均为"多问一次"，不是漏放）：
// - 引号内的分隔符不识别 ⇒ `echo "a && b"` 会被切段后判否（过度保守）；
// - 不剥离 wrapper / 前置赋值 ⇒ `sudo ls`、`FOO=1 ls` 判否（放宽面取其窄，见 `headWord`）；
// - `cd x && ls` 判否（`cd` 未进白名单）—— 是否收 `cd` 属**白名单成员**决策，
// 须按上面的成员判据单独论证，不在本轮顺手加。
func AllSegmentsReadOnly(cmd string) bool {
	norm := Normalize(cmd)
	if norm == "" {
		return false
	}
	lower := strings.ToLower(norm)
	if UnbalancedSubstitution(cmd) {
		return false
	}
	if len(ExecutionPayloads(cmd)) > 0 {
		return false
	}
	if strings.ContainsAny(lower, "<>") {
		return false
	}
	segs := splitCommandSegments(lower)
	if len(segs) == 0 {
		return false
	}
	for _, seg := range segs {
		head := headWord(seg)
		if head == "" || !alwaysReadOnlyExecutables[head] {
			return false
		}
	}
	return true
}

// quotedAfterExecutionHead 在**原始命令串**上扫描引号块，取「执行型上下文」里的内容。
//
// 判定：引号前的 token 序列里，存在一个恰为 `-c` 的开关，且其左侧最近的非开关 token
// 是 shell 包装名（`sh`/`bash`/…）；或紧邻 token 就是 `eval`。
// 反例（不得当载荷）：`gcc -c "rm -rf /"` —— `-c` 属于 gcc，不是 shell 包装。
func quotedAfterExecutionHead(lower string) []string {
	var out []string
	for i := 0; i < len(lower); i++ {
		q := lower[i]
		if q != '"' && q != '\'' {
			continue
		}
		// 找配对引号（双引号内支持反斜杠转义）
		end := -1
		for k := i + 1; k < len(lower); k++ {
			if q == '"' && lower[k] == '\\' {
				k++
				continue
			}
			if lower[k] == q {
				end = k
				break
			}
		}
		if end < 0 {
			continue
		}
		if isExecutionQuoteContext(lower[:i]) {
			out = append(out, lower[i+1:end])
		}
		i = end
	}
	return out
}

// isExecutionQuoteContext 判断「引号左侧的文本」是否属于会再次执行的上下文。
func isExecutionQuoteContext(prefix string) bool {
	tokens := strings.Fields(prefix)
	if len(tokens) == 0 {
		return false
	}
	if tokens[len(tokens)-1] == "eval" {
		return true
	}
	sawDashC := false
	for i := len(tokens) - 1; i >= 0; i-- {
		tok := tokens[i]
		if strings.HasPrefix(tok, "-") {
			if tok == "-c" {
				sawDashC = true
			}
			continue
		}
		if !sawDashC {
			return false
		}
		base := tok
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = base[idx+1:]
		}
		for _, w := range executionWrapperHeads {
			if base == w {
				return true
			}
		}
		return false
	}
	return false
}

// shellCArg 取 `sh -c "…"` 形态里的脚本参数内容（去引号）。无则返回空串。
func shellCArg(seg string) string {
	i := strings.Index(seg, "-c")
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(seg[i+2:])
	if rest == "" {
		return ""
	}
	switch rest[0] {
	case '"', '\'':
		q := rest[0]
		if j := strings.IndexByte(rest[1:], q); j >= 0 {
			return rest[1 : 1+j]
		}
		return rest[1:]
	}
	return rest
}

// stripXargsOptions 跳过 xargs 自身的选项（`-0` / `-I{}` / `-n1` 等），
// 返回其后继命令文本。只跳过以 `-` 开头的 token，避免把命令当成选项。
func stripXargsOptions(s string) string {
	fields := strings.Fields(s)
	for len(fields) > 0 && strings.HasPrefix(fields[0], "-") {
		fields = fields[1:]
	}
	return strings.Join(fields, " ")
}

// substitutionPayloads 抽出**执行型替换体**的内层文本（按括号配对，支持嵌套）。
//
// 覆盖四种会被 shell「换个地方再执行一次」的形态：
//
//	$( … ) 命令替换
//	<( … ) 进程替换（输入）
//	>( … ) 进程替换（输出）
//	` … ` 反引号命令替换
//	=( … ) zsh 进程替换（仅当 `=` 处于 token 起始，避免吃掉 `arr=(a b)` 的数组赋值）
//
// ⚠️ **不含** `${ … }` 与 `$[ … ]`：那是**展开**（expand），不是执行（execute）。
// 它们的保守处理属各消费方的**策略**，不属于本函数的职责 —— 混进来会让
// 「什么算载荷」这一事实失去边界。
func substitutionPayloads(lower string) []string {
	var out []string
	for i := 0; i < len(lower); i++ {
		if lower[i] == '`' {
			if j := strings.IndexByte(lower[i+1:], '`'); j >= 0 {
				out = append(out, lower[i+1:i+1+j])
				i += j + 1
			}
			continue
		}
		open := substitutionOpenParen(lower, i)
		if open < 0 {
			continue
		}
		if body, end, ok := parenBody(lower, open); ok {
			out = append(out, body)
			i = end
		}
	}
	return out
}

// substitutionOpenParen 判断 lower[i] 是否开启一个执行型替换，返回其 `(` 的下标；否则 -1。
//
// 为什么逐个形态判而不是统一「碰到 ( 」：`(` 还有子 shell / 函数定义等含义，
// 只有**跟着替换前缀**的那一个才是「内容会被执行」的。
func substitutionOpenParen(lower string, i int) int {
	if i+1 >= len(lower) || lower[i+1] != '(' {
		return -1
	}
	switch lower[i] {
	case '$':
		return i + 1
	case '<', '>':
		// 排除 `<<` / `>>`（heredoc / 追加重定向）：那两个是**文件**面，不是执行面。
		if i > 0 && lower[i-1] == lower[i] {
			return -1
		}
		return i + 1
	case '=':
		// zsh 进程替换 `=(cmd)`：`=` 必须处于 token 起始（`arr=(a b)` 是数组赋值，不算）。
		if i == 0 || lower[i-1] == ' ' || lower[i-1] == '\t' {
			return i + 1
		}
	}
	return -1
}

// parenBody 从 s[open]（须为 '('）起按深度配对，返回内层文本与配对 `)` 的下标。
func parenBody(s string, open int) (body string, end int, ok bool) {
	depth := 0
	for k := open; k < len(s); k++ {
		switch s[k] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : k], k, true
			}
		}
	}
	return "", 0, false
}

// IsDangerous 是 MatchDangerous 的布尔便捷形式。
func IsDangerous(cmd string) bool {
	_, ok := MatchDangerous(cmd)
	return ok
}

// MatchDestructive 判定命令是否命中**破坏性档·确认闸**。
//
// 与危险档的分工（两档的差异只在「免问机制能否吃掉它」）：
// - 危险档（MatchDangerous）：灾难级、不可逆 ⇒ **任何免问机制都不得吃掉**（F2/-R1 的裁定）；
// - 破坏性档（本函数）：重度不可逆但**沙箱边界可兜** ⇒ 沙箱生效且工作区内时可免问，
// 其余情况必须确认。名单见 confirmCommandNames（不含 chmod/chown，理由见该处注释）。
//
// ★ 本函数存在的理由（B2/A3，2026-09-20 实测）：此前「破坏性 ⇒ 必须确认」只有
// `tools.pipeline.checkBashDestructive` 一个消费方，而 `permission.Policy.Check`
// 不知道破坏性档 ⇒ `danger-full-access` 档下 `rm -f x` / `mv a b` 本地管线 **ask**、
// 其他 `Policy.Check` 消费方 **静默 allow**。声明搬到这里后，两侧消费同一个
// MatchConfirm，不再可能分叉。（历史：远端通道是当时的第二消费方，2026-10-11
// 砍除 /remote 后该消费方消失，本规则因管线 DestructiveCheckStage 仍在。）
func MatchDestructive(cmd string) (Pattern, bool) {
	norm := Normalize(cmd)
	if norm == "" {
		return Pattern{}, false
	}
	lower := strings.ToLower(norm)

	// PowerShell 破坏性 cmdlet：按子串匹配（`powershell -c "Remove-Item x"` 形态下命令位不可靠）。
	for _, c := range destructiveCmdlets {
		if strings.Contains(lower, strings.ToLower(c)) {
			return Pattern{ID: "ps-" + strings.ToLower(c), Kind: KindPhrase, Value: c, Note: "PowerShell destructive cmdlet", NoteZH: "PowerShell 破坏性 cmdlet"}, true
		}
	}

	for _, head := range commandHeads(lower) {
		for _, name := range confirmCommandNames {
			if head == name {
				return Pattern{ID: name, Kind: KindCommand, Value: name, Note: "Destructive-tier command name", NoteZH: "破坏性档命令名"}, true
			}
		}
		// 命令变体（mkfs.ext4 / mkfs.ntfs / mkfs.vfat）：按 "." 取基础名再匹配。
		if dot := strings.Index(head, "."); dot > 0 {
			base := head[:dot]
			for _, name := range confirmCommandNames {
				if base == name {
					return Pattern{ID: name, Kind: KindCommand, Value: head, Note: "Destructive-tier command variant", NoteZH: "破坏性档命令变体"}, true
				}
			}
		}
	}

	return Pattern{}, false
}

// MatchConfirm 判定命令是否**必须取得用户确认**（危险档 ∪ 破坏性档）。
//
// 这是「该命令必须问用户」这一事实的**唯一声明处**：`permission.Policy` 的档位判定
// （`checkModeLocked` / `guardrailFires`）与 `tools` 侧的确认阶段都从这里取结论，
// 因此「本地管线」与「远端通道」不可能对同一条命令给出相反结论。
//
// ⚠️ 语义边界（别误用）：本函数回答的是「要不要问」，**不是**「能不能做」。
// 「能不能做」由 `Policy.Check` 的 allowed 位回答（见 tools/pipeline 的单向收窄原则）。
func MatchConfirm(cmd string) (Pattern, bool) {
	if p, ok := MatchDangerous(cmd); ok {
		return p, true
	}
	return MatchDestructive(cmd)
}

// IsConfirmRequired 是 MatchConfirm 的布尔便捷形式。
func IsConfirmRequired(cmd string) bool {
	_, ok := MatchConfirm(cmd)
	return ok
}

// compiledRegex 惰性编译并缓存 KindRegex 规则。
// 规则表是编译期常量，故这里无需加锁（一次性 sync.Once 语义）。
func compiledRegex(id string) (*regexp.Regexp, bool) {
	switch id {
	case "fork-bomb-classic":
		return ForkBombClassicRe, true
	case "curl-pipe-sh":
		return curlPipeShellRe, true
	case "curl-pipe-bash":
		return curlPipeBashRe, true
	case "wget-pipe-sh":
		return wgetPipeShellRe, true
	case "wget-pipe-bash":
		return wgetPipeBashRe, true
	default:
		return nil, false
	}
}

// 远程直通 shell 的正则（编译一次，全局复用）。见上面四个 Source 常量的注释。
var (
	curlPipeShellRe = regexp.MustCompile(curlPipeShellSource)
	curlPipeBashRe  = regexp.MustCompile(curlPipeBashSource)
	wgetPipeShellRe = regexp.MustCompile(wgetPipeShellSource)
	wgetPipeBashRe  = regexp.MustCompile(wgetPipeBashSource)
)

// sepTokens 命令分隔符（长的在前，避免 "&&" 被 "&" 先截断）。
var sepTokens = []string{"&&", "||", ";", "|", "&", "\n"}

// wrappers 提升权限/改变上下文的**前缀包装命令**：真正的命令在其后。
var wrappers = map[string]bool{
	"sudo": true, "doas": true, "env": true, "nohup": true, "time": true,
	"command": true, "exec": true, "setsid": true, "nice": true,
	"ionice": true, "stdbuf": true,
}

// commandHeads 抽取命令串中每个「命令位」的可执行名（小写、去路径）。
//
// 例：`sudo /sbin/shutdown -h now && echo done` → ["shutdown", "echo"]
//
// 实现要点（故意保持朴素，避免引入解析器的复杂度）：
// - 按 shell 分隔符切段；每段取首个词为命令名；
// - 跳过 `KEY=VALUE` 形式的前置环境赋值；
// - 跳过 wrappers 及其后续的 `-xxx` 选项；
// - 取路径 basename（`/sbin/shutdown` → `shutdown`）。
//
// ⚠️ 已知边界（有意不做，避免过度工程）：不处理引号内包含分隔符、
// 命令替换 `$(...)`、`xargs` 后的次级命令。这些场景下可能漏判，
// 但不会误判合法命令 —— 漏判方向与「保守」一致，且 Layer 1（validator AST）
// 与 OS 沙箱仍在后面兜底。
//
// ★ 补充：**危险档**（MatchDangerous）已在这条边界之上叠加
// `executionPayloads` 下探（`$(…)`/反引号/`sh -c`/`eval`/`xargs`/`find -exec`），
// 因为危险档的漏判方向不可接受。本函数（命令位原语）保持朴素不动 ——
// 需要下探的消费方自行叠加，方向差异是刻意的（见 CommandRegions 注释）。
func commandHeads(lower string) []string {
	rests := commandSegments(lower)
	heads := make([]string, 0, len(rests))
	for _, rest := range rests {
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		head := fields[0]
		if idx := strings.LastIndex(head, "/"); idx >= 0 {
			head = head[idx+1:]
		}
		if head != "" {
			heads = append(heads, head)
		}
	}
	return heads
}

// commandSegments 返回每个命令段在「剥掉前置环境赋值与权限包装」之后的剩余文本（小写）。
//
// ★ 与 commandHeads（危险档命令位）／MatchReadOnly（只读段首）**共用**本函数 ——
// 避免"两处各自实现一遍段切分 + 前缀剥离"（那正是 / 那类
// 「同一事实跨入口各写各的」的温床）。
func commandSegments(lower string) []string {
	segs := splitCommandSegments(lower)
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		if rest := stripLeadingPrefixes(seg); rest != "" {
			out = append(out, rest)
		}
	}
	return out
}

// wrapValueOptions wrapper 里「带独立值」的短选项（`sudo -u root` / `nice -n 10`）。
//
// ★ 同源补漏：不认这些选项时，剥离只跳「以 `-` 开头」的 token，
// 于是 `nice -n 10 rm x` 被剥成 `10 rm x` ⇒ 命令位判成 `10` ⇒ **漏判**（rm 才是命令位）。
// 同一形态实测：`env FOO=1 rm x`（wrapper 后还有赋值）、`sudo -u root rm x`。
var wrapValueOptions = map[string]map[string]bool{
	"sudo":   {"-u": true, "-g": true, "-p": true, "-C": true, "-h": true, "-r": true, "-t": true, "-U": true, "-D": true},
	"doas":   {"-u": true, "-C": true},
	"nice":   {"-n": true},
	"ionice": {"-n": true, "-c": true},
	"env":    {"-u": true, "-S": true, "-C": true},
	"time":   {"-f": true, "-o": true},
	"stdbuf": {"-i": true, "-o": true, "-e": true},
}

// stripLeadingPrefixes 剥掉段首的 `KEY=VALUE` 环境赋值与 wrappers（含其选项），
// 返回剩余文本（内部连续空白压成单空格）。无剩余时返回 ""。
//
// ★ 补漏：两个前缀可**交替出现**（`env FOO=1 rm x` / `sudo nice -n 5 rm x`），
// 故改为一轮循环交替剥离 —— 修复前是两个顺序循环（先赋值、再 wrapper），
// 只处理得了「赋值在前」一种顺序，`env FOO=1 rm x` 会被剥成 `FOO=1 rm x`
// ⇒ 命令位判成 `FOO=1` ⇒ 漏判。
func stripLeadingPrefixes(seg string) string {
	fields := strings.Fields(seg)
	i := 0
	for i < len(fields) {
		// 前置环境赋值：KEY=VALUE（KEY 必须是合法标识符，避免把 `a=b` 参数误当赋值）
		if isEnvAssignment(fields[i]) {
			i++
			continue
		}
		wrapper := fields[i]
		if !wrappers[wrapper] {
			break
		}
		i++
		// 包装命令自身与其选项；带独立值的选项（`sudo -u root`）连值一起跳过
		for i < len(fields) && strings.HasPrefix(fields[i], "-") {
			opt := fields[i]
			i++
			if wrapValueOptions[wrapper][opt] && i < len(fields) {
				i++
			}
		}
	}
	if i >= len(fields) {
		return ""
	}
	return strings.Join(fields[i:], " ")
}

// splitCommandSegments 按 shell 分隔符把命令串切成命令段。
func splitCommandSegments(lower string) []string {
	segs := []string{lower}
	for _, tok := range sepTokens {
		next := make([]string, 0, len(segs))
		for _, s := range segs {
			next = append(next, strings.Split(s, tok)...)
		}
		segs = next
	}
	return segs
}

// isEnvAssignment 判断 token 是否为前置环境赋值 KEY=VALUE。
func isEnvAssignment(tok string) bool {
	eq := strings.Index(tok, "=")
	if eq <= 0 {
		return false
	}
	for i, r := range tok[:eq] {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
