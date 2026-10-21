// .go — 机械门：命令判定**只准有一处**判据。
//
// # 本门要防的事
//
// 的根因不是"少了一个分支"，而是「**这段输入是不是命令**」这个语义
// 被写成了裸 `strings.HasPrefix(input, "/")`，并在 8 处各写一遍（无 SSOT）。
// 只把其中几处改掉、留下几处，就是**假修复** —— 症状会以"某个 UI 还是老行为"的形态复发。
//
// 因此本门断言：`internal/tui/**` 与 `internal/typeahead/**` 里
// **不得再出现 `HasPrefix(<任意>, "/")` 形式的命令判定**，除 `n105PendingWiring` 显式登记的存量外。
//
// # 判别性（本门必须能被打红）
//
// 往被扫目录里任一**非 `_test.go`** 文件插一行 `strings.HasPrefix(x, "/")`
// ⇒ 本测试必须 FAIL。★ 因为本门**读磁盘**，`-overlay` 对它**物理无效**，
// A/B 只能走真实写回（或在副本目录上跑，见下），跑完无条件还原。
//
// 为此本门支持 `WHALE_N105_GATE_BASE` 环境变量改写扫描基址（默认 `..` = 仓库根）：
//
//	WHALE_N105_GATE_BASE=/private/tmp/<probe> go test ./internal/ -run TestN105_NoNewSlashHasPrefixCopies
//
// 这样可以在**副本目录**上注入反例自证本门能红，而不必往真实工作树里写探针文件
// （纪律：探针不得写真实用户文件；tui 树可能正被另一会话使用）。
package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// n105SlashPredicatePattern 匹配"把 `/` 当命令判据"的裸写法。
//
// 刻意**宽**（只要一行里同时出现 `HasPrefix(` 与 `"/"` 就算命中）：
// 本门只在 `internal/tui` 与 `internal/typeahead` 两个目录里跑，
// 这两处出现该组合只能是命令判定。将来若确有一处**合法的路径判定**，
// 应当显式登记进下面的表（= "白名单例外须显式登记"），而不是把门改窄。
var n105SlashPredicatePattern = regexp.MustCompile(`HasPrefix\s*\(.*"/"`)

// n105PendingWiring 是「尚未接线到 command.IsCommandInput」的存量副本台账（按文件计数）。
//
// ★ 这是一张**只准递减**的临时账：接线上线后必须清空。
// ★ 往此表**新增**条目 = 新增一处旧判据副本 ⇒ 那就是本门要拦的事，
//
//	须先走登记（并说明为什么不能走 SSOT），不得为了"让门变绿"而加行。
//
// 实测基线（2026-09-20 09:2x，Grep 工具全仓核对）：
//
//	bridge.go:459（判命令）· bridge.go:523（取反，记 FirstUserInput）
//	stream.go:204（判命令）· stream.go:299（取反）
//	plain.go:222（判命令）· plain.go:297（取反）· plain.go:1854（取反）
//	typeahead.go:399（补全弹窗触发，TrimLeftFunc 后判）
//
// ★ **2026-09-20 批 C：8 处接线已完成 ⇒ 台账清空为 `{}`.**
// 逐处落点（改动后）：
//
//	stream.go（`command.IsCommandInput(rawInput)` 判定 ＋ `!…` 取反，传 rawInput 保逃逸语义）
//	bridge.go（`command.IsCommandInput(message)` ＋ `!…`）
//	plain.go （`command.IsCommandInput(line)` ＋ `!…` ×2）
//	typeahead.go（`command.IsCommandInput(before)`，不再 TrimLeft 后判）
//
// ⇒ 本表**从此为空**。留空它（而非删变量）是为了保留"显式登记例外"的机制与这个注释位；
// 任何人都**不得**为了"让门变绿"而重新加行 —— 加行 = 新增一处旧判据副本，正是本门要拦的事。
//
// ★ 2026-10-19 登记一条**合法路径判定**豁免（门的失败信息自述的②号通道，非命令判定副本）：
// - internal/tui/render.go: FileLinkURI 里 `strings.HasPrefix(p, "/")` —— p 是**转义后的文件路径**，
// 判的是「绝对 POSIX 路径带前导斜杠 ⇒ file:/// 拼接前去重」（⑧ 修正 file://// 四斜杠），
// 与命令输入判定无关。匹配模式对 `HasPrefix(…, "/")` 无法区分语义 ⇒ 按协议登记而非改窄模式。
var n105PendingWiring = map[string]int{
	"internal/tui/render.go": 1,
}

// n105ScannedDirs 是命令判定的全部已知落点目录。
// ★ 若将来在别处（daemon / remote / ssh / plugin）发现第 9 处命令入口，
//
//	必须把该目录加进来 —— 否则门对它完全盲（这正是"第 9 处"的来源）。
var n105ScannedDirs = []string{"internal/tui", "internal/typeahead"}

func TestN105_NoNewSlashHasPrefixCopies(t *testing.T) {
	base := ".."
	if v := os.Getenv("WHALE_N105_GATE_BASE"); v != "" {
		base = v
	}

	hits := map[string][]string{} // relPath -> 命中行（含行号）
	for _, dir := range n105ScannedDirs {
		root := filepath.Join(base, dir)
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil // 目录不存在（如探针副本里只放了一个子目录）⇒ 不算失败
				}
				return err
			}
			name := info.Name()
			if info.IsDir() {
				if name == "testdata" || strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil // 测试文件可以用旧写法做 A/B，不属产品面
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			rel, _ := filepath.Rel(base, path)
			for i, line := range strings.Split(string(data), "\n") {
				if n105SlashPredicatePattern.MatchString(line) {
					hits[rel] = append(hits[rel], formatN105Hit(i+1, line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("扫描 %s 失败：%v", root, err)
		}
	}

	// 自检：门必须真的扫到了东西。若所有被扫目录都读不到，
	// 本门会在"零命中"下安然全绿 —— 那就是假绿。
	if err := n105AssertGateIsLive(t, base); err != nil {
		t.Fatal(err)
	}

	var offenders []string
	for rel, lines := range hits {
		if len(lines) > n105PendingWiring[rel] {
			sort.Strings(lines)
			offenders = append(offenders, rel+" 命中 "+strconv.Itoa(len(lines))+" 处（台账允许 "+
				strconv.Itoa(n105PendingWiring[rel])+" 处）：\n    "+strings.Join(lines, "\n    "))
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("发现未经登记的命令判定副本（：判定必须走 command.IsCommandInput 单一入口）：\n\n  %s\n\n"+
			"处理方式二选一：① 改走 command.IsCommandInput（推荐）；② 若确属**合法的路径判定**，"+
			"在 n105PendingWiring 显式登记并写明理由。\n"+
			"★ 不得把本门的匹配规则改窄来绕过。", strings.Join(offenders, "\n\n  "))
	}

	// 台账卫生：已接线完的文件应把条目删掉，否则台账会一直挂着假账。
	for rel, allowed := range n105PendingWiring {
		if n := len(hits[rel]); n < allowed {
			t.Logf("提示：%s 的副本数已降到 %d（台账记的是 %d）⇒ 接线完成后请把该条从 n105PendingWiring 删除",
				rel, n, allowed)
		}
	}
	t.Logf("已扫 %v，命中总数 = %d（台账允许总数 = %d）", n105ScannedDirs, n105TotalHits(hits), n105TotalAllowed())
}

// n105AssertGateIsLive 断言本门确实读到了被扫目录里的生产文件。
// 没有这一步，"扫描路径写错" 与 "代码已干净" 会给出同一个绿 —— 典型假绿。
func n105AssertGateIsLive(t *testing.T, base string) error {
	t.Helper()
	found := 0
	for _, dir := range n105ScannedDirs {
		entries, err := os.ReadDir(filepath.Join(base, dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
				found++
			}
		}
	}
	if found == 0 {
		return fmt.Errorf("门在 %s 下没读到任何被扫生产文件 ⇒ 本门会以'零命中'安然全绿（假绿）"+
			"：先确认 WHALE_N105_GATE_BASE / n105ScannedDirs 指向正确", base)
	}
	t.Logf("门自检：在 %s 下读到 %d 个被扫生产文件", base, found)
	return nil
}

func formatN105Hit(lineNo int, line string) string {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) > 120 {
		trimmed = trimmed[:120] + "…"
	}
	return strconv.Itoa(lineNo) + ": " + trimmed
}

func n105TotalHits(hits map[string][]string) int {
	n := 0
	for _, v := range hits {
		n += len(v)
	}
	return n
}

func n105TotalAllowed() int {
	n := 0
	for _, v := range n105PendingWiring {
		n += v
	}
	return n
}

// n105SSOTCallPattern 匹配"调用 SSOT 谓词"的写法（提交侧 / 补全侧都算）。
// ★ 两条导出谓词共用同一实现，本门**不限定**走哪一条 —— 它只禁"裸 HasPrefix"。
var n105SSOTCallPattern = regexp.MustCompile(`command\.IsCommand(?:In|CompletionIn)put\s*\(`)

// n105ExpectedSSOTCalls 是 8 处调用点**接线后**的期望计数（按文件）。
//
// 这张表是 n105PendingWiring 的**镜像**：
// - 前者（反向）说"目录里还剩几处裸判据" ⇒ 接线完成后须**恒为 0**；
// - 本表（正向）说"每个文件必须有几处走 SSOT" ⇒ 须**恒等于**此。
//
// 两张表同时成立，才同时排除"漏接线"与"留旧副本"。
// ★ 只留反向是可绕过的：把整条判定**删掉**也能"零命中"全绿。正向表正是堵这个口子。
var n105ExpectedSSOTCalls = map[string]int{
	"internal/tui/stream.go":          2,
	"internal/tui/bridge.go":          2,
	"internal/tui/plain.go":           3,
	"internal/typeahead/typeahead.go": 1,
}

// TestN105_AllSitesWiredToSSOT 是"接线已完成"的**永久判别器**（正向，与反向门配对）。
//
// 判别性：把任一处的 `command.IsCommandInput(...)` 改回裸 `strings.HasPrefix(...)`
// ⇒ 本测试立即 FAIL（该文件计数少 1）；反向门同时会报"多出一处裸判据"。
// —— 同一改动两面同时红，这才是可判别的接线判据。
func TestN105_AllSitesWiredToSSOT(t *testing.T) {
	base := ".."
	if v := os.Getenv("WHALE_N105_GATE_BASE"); v != "" {
		base = v
	}

	got := map[string]int{}
	for _, dir := range n105ScannedDirs {
		root := filepath.Join(base, dir)
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if nm := info.Name(); nm == "testdata" || strings.HasPrefix(nm, ".") || strings.HasPrefix(nm, "_") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(info.Name(), ".go") || strings.HasSuffix(info.Name(), "_test.go") {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			rel, _ := filepath.Rel(base, path)
			for _, line := range strings.Split(string(data), "\n") {
				// 注释与文档行不算：说明文字里常出现谓词名，但不构成调用点。
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				got[rel] += len(n105SSOTCallPattern.FindAllString(line, -1))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("扫描 %s 失败：%v", root, err)
		}
	}

	var problems []string
	for rel, want := range n105ExpectedSSOTCalls {
		if got[rel] != want {
			problems = append(problems, fmt.Sprintf("%s：走 SSOT 的调用点 = %d，期望 %d", rel, got[rel], want))
		}
	}
	for rel, n := range got {
		if _, ok := n105ExpectedSSOTCalls[rel]; !ok && n > 0 {
			problems = append(problems, fmt.Sprintf("%s：多出 %d 处 SSOT 调用（未被 n105ExpectedSSOTCalls 登记）", rel, n))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf(" 接线不完整或不一致：\n\n  %s\n\n"+
			"接线后每处判定都必须显式走 command.IsCommandInput / command.IsCommandCompletionInput；"+
			"若确要新增合法调用点，请同步更新 n105ExpectedSSOTCalls 并写明理由。", strings.Join(problems, "\n  "))
	}
	t.Logf("8 处调用点全部走 SSOT 谓词，逐文件计数 = %v", n105ExpectedSSOTCalls)
}
