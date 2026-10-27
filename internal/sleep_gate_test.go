// sleep_gate_test.go — 机械门：测试里的 `time.Sleep` 只许减少，不许新增。
//
// # 为什么要有这道门
//
// `time.Sleep(X)` + 断言，等于断言「这台机器能在 X 内把这件事做完」。
// 它与被测功能无关：机器忙就红、机器闲就绿。2026-10-27 实测：
// `internal/daemon` 一条用例用 3s 等子进程起来，空闲 30/30 绿，
// 5 个包并发编译时启动延迟从 0.38s 涨到 1.45–3.96s ⇒ 约 20% 概率红，
// 最后靠 CI「失败包重试一次」兜着 —— **重试是把症状按住，病还在**。
//
// 单点修复不解决问题：全仓还有两百来处，谁都能再写一处。
// 真正让它不再复发的东西**不是**「记得别写」，而是**写不进去**。
//
// # 门怎么工作（棘轮 / ratchet）
//
// 基线文件 `internal/testdata/sleep_baseline.golden` 逐文件记录**当前**的 `time.Sleep` 处数。
// 判定是**双向严格相等**：
//
//	某文件实际处数 > 基线 ⇒ 红：新增了「等秒数」判据
//	某文件实际处数 < 基线 ⇒ 红：基线可以缩小，请同步更新（否则空出来的额度会被悄悄用掉）
//	基线里有、实际已无 ⇒ 红：基线过期
//	实际有、基线里没有 ⇒ 红：新文件引入（默认零容忍）
//
// 双向相等是刻意的：只做单向（actual ≤ baseline）等于给每个文件留了一个**长期额度**，
// 修掉 3 处后别人还能再写回 3 处而门不响。基线必须**始终等于现状**，任何变动都得在 diff 里露出来。
//
// 唯一出口（会在 diff 里留下可见痕迹，供 review 质询）：
//
//	WHALE_UPDATE_SLEEP_BASELINE=1 go test ./internal/ -run TestGate_TimeSleepRatchet
//
// # 判别性（本门必须能被打红）
//
// 往任意 `*_test.go` 里加一处 `time.Sleep` ⇒ 本测试必须 FAIL。
// 若加了仍然绿，说明扫描失效（目录没走到 / AST 判定写错），必须当场修门而不是改基线。
// ★ 通道：本门是**读盘型**（`parser.ParseFile` 读磁盘），`-overlay` 对它**物理无效**
// ⇒ 验证判别性只能真的落一个探针文件，跑完删掉（见文件尾注释的做法记录）。
//
// # 为什么是 AST 而不是正则
//
// 正则会把**注释和字符串**里的 `time.Sleep` 也算进去（本仓 `internal/daemon/bug_test.go`
// 的 Skip 文案里就写着 `time.Sleep(100 * time.Millisecond)`），基线会因此虚高，
// 而且改个文案就要求改基线 —— 门开始制造噪音，就离被关掉不远了。
// AST 顺带还解决了 import 别名（`import clock "time"`）——别名照样能认出来。
package internal

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// sleepBaselinePath 是基线文件相对仓库根的路径。
//
// ★ 为什么后缀是 `.golden` 而不是 `.txt`：本仓 `.gitignore:24` 有全局 `*.txt`，
// 基线若叫 `.txt` 就**不会进仓库**（`git check-ignore` 实测命中）⇒ 干净 clone 后
// 这道门会以"基线缺失"直接判红（的 `vendor/modules.txt` 就是被这条规则吞掉的）。
// 换成 `.golden`（Go 社区 testdata 黄金文件的惯用后缀）从类型上就不与那条规则相交。
const sleepBaselinePath = "internal/testdata/sleep_baseline.golden"

// sleepBaselineHeader 是基线文件的说明头（更新时原样写回）。
const sleepBaselineHeader = `# 基线：测试里 time.Sleep 的处数（file<TAB>count），由 sleep_gate_test.go 维护。
#
# ★ 这个文件只许**变小**。新增一行 = 你在引入一条「等固定秒数」的判据，
#   请先读 internal/testutil/wait.go 顶部的说明，改用 testutil.Eventually*。
# 更新方式（必须与代码改动同一个提交，让 diff 里看得见）：
#   WHALE_UPDATE_SLEEP_BASELINE=1 go test ./internal/ -run TestGate_TimeSleepRatchet
`

// TestGate_TimeSleepRatchet 守住「测试里不再新增 time.Sleep」这条不变量。
func TestGate_TimeSleepRatchet(t *testing.T) {
	root := repoRoot(t)
	actual, err := scanSleepCalls(root)
	if err != nil {
		t.Fatalf("扫描失败（不是「没找到 time.Sleep」，是「扫不动」）：%v", err)
	}

	if os.Getenv("WHALE_UPDATE_SLEEP_BASELINE") == "1" {
		if err := writeSleepBaseline(root, actual); err != nil {
			t.Fatalf("写基线失败：%v", err)
		}
		t.Logf("基线已更新：%d 个文件 / %d 处 time.Sleep", len(actual), totalOf(actual))
		return
	}

	baseline, err := readSleepBaseline(root)
	if err != nil {
		t.Fatalf("读基线失败：%v", err)
	}

	var problems []string
	paths := map[string]struct{}{}
	for p := range actual {
		paths[p] = struct{}{}
	}
	for p := range baseline {
		paths[p] = struct{}{}
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	for _, p := range sorted {
		got, inActual := actual[p]
		want, inBaseline := baseline[p]
		switch {
		case inActual && !inBaseline:
			problems = append(problems, fmt.Sprintf(
				"%s：新增 %d 处 time.Sleep（基线里没有这个文件）", p, got))
		case inActual && got > want:
			problems = append(problems, fmt.Sprintf(
				"%s：%d 处，基线 %d 处（**新增了 %d 处**）", p, got, want, got-want))
		case inActual && got < want:
			problems = append(problems, fmt.Sprintf(
				"%s：%d 处，基线 %d 处（已减少 %d 处 —— 请同步更新基线，保持基线=现状）",
				p, got, want, want-got))
		case !inActual && inBaseline:
			problems = append(problems, fmt.Sprintf(
				"%s：基线记了 %d 处，实际一处也没有（基线过期）", p, want))
		}
	}

	if len(problems) == 0 {
		t.Logf("time.Sleep 处数 %d（%d 个文件），与基线一致", totalOf(actual), len(actual))
		return
	}

	t.Fatalf("测试里的 time.Sleep 与基线不一致（%d 处差异）：\n  %s\n\n"+
		"★ 若是**新增**：请不要调大基线。等事件的写法见 internal/testutil/wait.go：\n"+
		"    testutil.Eventually(t, \"<什么事件>\", func() bool { ... })\n"+
		"  真正必须睡的地方（例如「制造时间差」）请在调用处写一行原因注释，再更新基线，\n"+
		"  让 review 能质询这个理由。\n"+
		"★ 若是**减少 / 删除**：更新基线即可（这一步是在收紧棘轮，是好事）：\n"+
		"    WHALE_UPDATE_SLEEP_BASELINE=1 go test ./internal/ -run TestGate_TimeSleepRatchet",
		len(problems), strings.Join(problems, "\n  "))
}

// scanSleepCalls 返回 relPath → `time.Sleep` 调用处数。
//
// 只统计**调用表达式**（`ast.CallExpr` + 包名解析），不统计注释/字符串里的字面量。
func scanSleepCalls(root string) (map[string]int, error) {
	counts := map[string]int{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := info.Name()
		if info.IsDir() {
			switch name {
			case "vendor", ".git", "node_modules", "testdata":
				return filepath.SkipDir
			}
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, "_test.go") {
			return nil
		}
		n, err := countSleepCallsInFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if n > 0 {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			counts[filepath.ToSlash(rel)] = n
		}
		return nil
	})
	return counts, err
}

// countSleepCallsInFile 解析单个文件并数 `time.Sleep(...)`。
//
// 包名按**该文件自己的 import 表**解析 ⇒ `import clock "time"` 里的
// `clock.Sleep(...)` 同样会被数到（否则门可被别名绕过）。
func countSleepCallsInFile(path string) (int, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return 0, err
	}

	timeNames := map[string]struct{}{}
	for _, imp := range file.Imports {
		importPath, unquoteErr := strconv.Unquote(imp.Path.Value)
		if unquoteErr != nil || importPath != "time" {
			continue
		}
		local := "time" // 未写别名时就是包名
		if imp.Name != nil {
			local = imp.Name.Name
		}
		timeNames[local] = struct{}{}
	}
	if len(timeNames) == 0 {
		return 0, nil
	}

	count := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "Sleep" {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, isTime := timeNames[ident.Name]; isTime {
			count++
		}
		return true
	})
	return count, nil
}

// readSleepBaseline 读基线；格式 `path<TAB>count`，`#` 开头为注释，空行忽略。
func readSleepBaseline(root string) (map[string]int, error) {
	path := filepath.Join(root, sleepBaselinePath)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 %s: %w（基线必须随仓库存在，不能缺失）", sleepBaselinePath, err)
	}
	defer f.Close()

	out := map[string]int{}
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d 格式应为 `path<TAB>count`，实际 %q", sleepBaselinePath, lineNo, line)
		}
		n, convErr := strconv.Atoi(fields[1])
		if convErr != nil || n <= 0 {
			return nil, fmt.Errorf("%s:%d 处数非法：%q", sleepBaselinePath, lineNo, fields[1])
		}
		out[fields[0]] = n
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// writeSleepBaseline 用当前实测处数重写基线（只由 WHALE_UPDATE_SLEEP_BASELINE=1 触发）。
func writeSleepBaseline(root string, counts map[string]int) error {
	path := filepath.Join(root, sleepBaselinePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(sleepBaselineHeader)
	b.WriteString("#\n# 合计：")
	b.WriteString(strconv.Itoa(totalOf(counts)))
	b.WriteString(" 处 / ")
	b.WriteString(strconv.Itoa(len(keys)))
	b.WriteString(" 个文件\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\t%d\n", k, counts[k])
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func totalOf(counts map[string]int) int {
	sum := 0
	for _, v := range counts {
		sum += v
	}
	return sum
}

// repoRoot 用本文件的路径定位仓库根（`internal/` 的上一级），
// 这样不依赖 `go test` 的工作目录（包目录 vs 模块根）。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位 sleep_gate_test.go 自身路径")
	}
	root := filepath.Dir(filepath.Dir(file))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("定位到的仓库根 %s 里没有 go.mod：%v", root, err)
	}
	return root
}
