// competitor_name_gate_test.go — 机械门：产品源码里**不得**出现竞品名。
//
// # 用户裁定
//
//	「我不允许在任何显示和注释里出现别的产品的名称，注释可能宽泛一些，竞品不能出现。」
//
// 范围 = `whale-code/` 目录内的 Go 源码（显示串 + 注释）。工作区里的审查/交接文档**不在**范围
// （那些是审计的对标证据链，用户明确要求保留）。`vendor/` 是第三方代码，不动。
//
// # 唯一的例外：`migrate` 功能区
//
// `whale migrate --from <x>` 的**用途本身**就是从竞品导入配置 ⇒ 它的 flag 值、目录名
// （`~/.同类 CLI` / `~/.同类 CLI`）与报错文案里必然出现竞品名，那些名字是**功能主体**而不是宣传对标。
// 用户明确裁定保留。因此：
//
//	internal/migrate/** → 整目录豁免
//	其它位置的「功能性出现」 → 逐行白名单（见 functionalAllowlist）
//
// # 判别性（本门必须能被打红）
//
// 往任意非豁免文件的**注释或字符串**里插一个竞品名 ⇒ 本测试必须 FAIL。
// 若插入后仍然绿，说明门失效（例如正则写错、遍历漏了目录），必须当场修门而不是改断言。
//
// # 为什么门用「拼接」构造竞品名
//
// 本文件自身也在被扫描范围内。若把竞品名写成字面量，本门会**自己把自己打红**；
// 而把自己加进排除名单又会留下「守门人不受同一规则约束」的口子。
// 拼接构造让本文件字面上不含竞品名 ⇒ 无需豁免，规则对门自身同样生效。
package internal

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// competitorNamePattern 由拼接片段构造 —— 见文件头「为什么门用拼接构造竞品名」。
var competitorNamePattern = regexp.MustCompile(`(?i)\b(?:` + strings.Join([]string{
	"Co" + "dex",
	"Claude " + "Code",
	"claude-" + "code",
	"Co" + "pilot",
	"Wind" + "surf",
	"Ai" + "der",
	"De" + "vin",
}, "|") + `)\b`)

// functionalAllowlist 是「名字即功能主体」的逐行白名单。
//
// ★ 只匹配**具体形态**，不是「只要提到 migrate 就放行」—— 后者会让白名单退化成通用逃生门，
// 门就失去意义。每一条都对应一个真实的功能性标识符形态。
var functionalAllowlist = []*regexp.Regexp{
	// --from <x>：flag 名之后就是产品标识本身
	regexp.MustCompile(`(?i)--from\b`),
	// 该 flag 的两个取值分支
	regexp.MustCompile(`(?i)case\s*"(?:Co` + `dex|claude-` + `code)"`),
	// 迁移实现里的标识符（名字直接编码了来源产品）
	regexp.MustCompile(`(?i)(?:MigrateFrom|detect|parse)(?:Co` + `dex|Claude)`),
	// 被探测的配置目录常量
	regexp.MustCompile(`(?i)~\s*/\s*\.(?:Co` + `dex|claude)`),
	// 编辑器可执行文件的**安全白名单**（internal/tui/render.go 的 allowedEditors）：
	// 这里出现的是「编辑器 CLI 名」——用户可能把 $EDITOR 设成它，删掉会让这些用户无法启动编辑器。
	// 它不是在宣传竞品，是在枚举「允许被 exec 的名字」。
	regexp.MustCompile(`(?i)"(?:Cu` + `rsor|wind` + `surf)"\s*:\s*true`),
	// 约定文件名（internal/config/magic_docs.go 的 patterns 表）：
	// 这些是**要被读取的文件名**，名字即功能主体，改了就找不到文件。
	regexp.MustCompile(`(?i)\.github/copi` + `lot-instructions\.md`),
}

// exemptDirs 是整目录豁免。
//
// - vendor：第三方代码，不属本仓产品面，也不该被本仓规则改。
// - internal/migrate：migrate 功能区（见文件头，用户裁定保留）。
// - docs：审计归档与设计文档。用户裁定「文档保留」——它们是**对标的证据链**
// （含 file:line 引用），清掉会破坏可追溯性。docs 下的 .go 是归档快照，不进构建。
var exemptDirs = []string{
	"vendor",
	"internal/migrate",
	"docs",
}

func TestGate_NoCompetitorProductNames(t *testing.T) {
	root := ".." // 测试的 CWD 是 internal/，仓库根在其上一层

	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "vendor" || base == "node_modules" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(os.PathSeparator)))
		for _, ex := range exemptDirs {
			if strings.HasPrefix(rel, ex+"/") {
				return nil
			}
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !competitorNamePattern.MatchString(line) {
				continue
			}
			if allowedFunctionalLine(line) {
				continue
			}
			offenders = append(offenders, formatOffender(rel, i+1, line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历源码失败: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("产品源码里出现竞品名 %d 处（显示串与注释都不允许）：\n%s\n\n"+
			"修法：改写为中性表述（如 `对标同类 CLI` / `对齐业内既有实现`）。\n"+
			"若该处是「名字即功能主体」（如 migrate 的 --from 取值），请把**具体形态**加进 "+
			"functionalAllowlist，不要放宽成本文件这一层。",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

// allowedFunctionalLine 报告该行是否为「功能性出现」。
func allowedFunctionalLine(line string) bool {
	for _, re := range functionalAllowlist {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// formatOffender 给出带「显示面 / 注释面」判定的定位信息。
func formatOffender(rel string, lineNo int, line string) string {
	kind := "显示面（字符串/标识符）"
	if isCommentHit(line) {
		kind = "注释面"
	}
	return "  " + rel + ":" + strconv.Itoa(lineNo) + "  [" + kind + "]  " + strings.TrimSpace(line)
}

// isCommentHit 判断命中是否落在 `//` 之后（粗略但足够：用于给人看的分类，不用于判绿）。
func isCommentHit(line string) bool {
	idx := competitorNamePattern.FindStringIndex(line)
	comment := commentStart(line)
	return comment >= 0 && idx != nil && idx[0] > comment
}

// commentStart 返回行内 `//` 的下标（跳过字符串与反引号内的 `/`）。
func commentStart(line string) int {
	inStr, inRaw, esc := false, false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inStr:
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		case inRaw:
			if c == '`' {
				inRaw = false
			}
		default:
			switch c {
			case '"':
				inStr = true
			case '`':
				inRaw = true
			case '/':
				if i+1 < len(line) && line[i+1] == '/' {
					return i
				}
			}
		}
	}
	return -1
}
