package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ============================================================================
// W-046a 「写→读行数闭环」判别器 ＋ W-046b 搜索输出单复数文案门
//
// 现象（真机 I-13.13 转录，同一输出块三个数字打架）：
// write_file 报 "File written: out/t13.txt (2 lines)"、diff 说 "+1 lines"、
// read_file 回读只有 1 行。内容是 "…t10.txt\n"（末尾带换行）。
//
// 根因：行数语义有两套独立实现 ——
// read_file 走 bufio.Scanner（编辑器语义：末尾换行不算新行）；
// write_file（file.go writeFileHandler）走 strings.Count(s,"\n")+1
// （wc -l 语义：末尾换行多算一行）⇒ 同一事实跨入口不同源（同族）。
//
// 判据设计：
// ① 闭环判据 = write_file **自己报的行数** 必须等于 随后 read_file **回读的行数**
// （比"断言某个常数"更本质：不管语义怎么拍板，同一文件同轮两个数字不许打架）。
// before（wc -l 语义）在末尾带换行的用例上 2≠1 ⇒ 必红；
// after（编辑器语义）⇒ 全绿。
// ② 单复数判据 = 正向断言单数形态（"Found 1 file:" / "Found 1 match in 1 file:"）
// ＋ 配对负向（不得含 "1 files"）。
// ③ 静态机械门 = 源码里不得再出现未守卫的 "%d files:" 复数格式串。
// ============================================================================

var w046ReportedLinesRe = regexp.MustCompile(`\((\d+) lines\)`)

// w046CountNumberedLines 统计 read_file 输出里带行号前缀（`%6d→`）的行数。
// 这是"该文件实际有多少行"的生产读数 —— read_file 走 Scanner，天然编辑器语义。
func w046CountNumberedLines(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if regexp.MustCompile(`^\s*\d+→`).MatchString(line) {
			n++
		}
	}
	return n
}

// TestW046_WriteFile_LineCountMatchesReadFile —— W-046a 主判别器（写→读闭环）。
// 走生产入口 Executor.Execute（与 search_path_anchor_test 同形态，R2）。
// 注：无尾换行的两个子用例 before 也绿（两种语义一致），仅作语义钉；
//
//	判别力由末尾带换行的两个子用例承担（before 必红）。
func TestW046_WriteFile_LineCountMatchesReadFile(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"single line with trailing newline (判别子用例)", "abc\n", 1},
		{"single line without trailing newline (钉语义，不判别)", "abc", 1},
		{"two lines with trailing newline (判别子用例)", "a\nb\n", 2},
		{"two lines without trailing newline (钉语义，不判别)", "a\nb", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			e := NewExecutor(NewRegistry(), workDir)

			res := e.Execute(context.Background(), Call{
				ID:   "w1",
				Name: "write_file",
				Input: mustMarshal(map[string]string{
					"path":    "w046_lc.txt",
					"content": tc.content,
				}),
			})
			if res.IsError {
				t.Fatalf("write_file 失败: %s", res.Output)
			}

			m := w046ReportedLinesRe.FindStringSubmatch(res.Output)
			if m == nil {
				t.Fatalf("write_file 输出无 \"(N lines)\" 行数报告，闭环无从谈起。实际输出:\n%s", res.Output)
			}
			reported, _ := strconv.Atoi(m[1])

			res2 := e.Execute(context.Background(), Call{
				ID:   "w2",
				Name: "read_file",
				Input: mustMarshal(map[string]string{
					"path": "w046_lc.txt",
				}),
			})
			if res2.IsError {
				t.Fatalf("read_file 失败: %s", res2.Output)
			}
			actual := w046CountNumberedLines(res2.Output)

			// 闭环断言（判别器主体）：同一文件、同一轮，两个数字必须一致。
			if reported != actual {
				t.Fatalf("W-046a 闭环破：write_file 报 %d 行，read_file 回读 %d 行（content=%q）。\n"+
					"write 输出: %s", reported, actual, tc.content, res.Output)
			}
			// 语义钉：编辑器语义（末尾换行不分新行）。
			if reported != tc.want {
				t.Fatalf("W-046a 语义漂移：报了 %d 行，编辑器语义应为 %d（content=%q）", reported, tc.want, tc.content)
			}
		})
	}
}

// TestW046_SearchOutput_SingularCounts —— W-046b 判别器（单复数文案，工具输出面）。
func TestW046_SearchOutput_SingularCounts(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "t10.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("写夹具失败: %v", err)
	}
	e := NewExecutor(NewRegistry(), workDir)

	t.Run("glob: 1 file 必须用单数", func(t *testing.T) {
		res := e.Execute(context.Background(), Call{
			ID:    "g1",
			Name:  "glob_search",
			Input: mustMarshal(map[string]string{"pattern": "**/t10.txt"}),
		})
		if res.IsError {
			t.Fatalf("glob_search 失败: %s", res.Output)
		}
		// 正向：单数形态（"没出现坏串"≠通过，必须正向命中）
		if !strings.Contains(res.Output, "Found 1 file:") {
			t.Fatalf("W-046b：glob 单文件应报 \"Found 1 file:\"。实际输出:\n%s", res.Output)
		}
		// 配对负向：不得出现复数误用
		if strings.Contains(res.Output, "1 files") {
			t.Fatalf("W-046b：出现 \"1 files\" 复数误用。实际输出:\n%s", res.Output)
		}
	})

	t.Run("grep: 1 match in 1 file 必须用单数", func(t *testing.T) {
		res := e.Execute(context.Background(), Call{
			ID:    "g2",
			Name:  "grep_search",
			Input: mustMarshal(map[string]string{"pattern": "alpha", "output_mode": "content"}),
		})
		if res.IsError {
			t.Fatalf("grep_search 失败: %s", res.Output)
		}
		if !strings.Contains(res.Output, "Found 1 match in 1 file:") {
			t.Fatalf("W-046b：grep 单匹配应报 \"Found 1 match in 1 file:\"。实际输出:\n%s", res.Output)
		}
		if strings.Contains(res.Output, "1 matches") || strings.Contains(res.Output, "1 files") {
			t.Fatalf("W-046b：出现复数误用。实际输出:\n%s", res.Output)
		}
	})
}

// TestW046_SearchCopyGate_NoUnguardedPluralFormat —— W-046b 静态机械门（防复发）。
// ⚠️ 本门运行期读磁盘源码（通道 B 特性）：它守的是"文案格式串不再以未守卫形态回潮"，
// 属结构面守卫，与上面的行为判据各管一段。改文案时请走 plural() 助手。
func TestW046_SearchCopyGate_NoUnguardedPluralFormat(t *testing.T) {
	src, err := os.ReadFile("search_engine.go")
	if err != nil {
		t.Fatalf("读源码失败（本门必须在包目录内跑）: %v", err)
	}
	for _, bad := range []string{
		`Found %d files:`,
		`Found %d matches in %d files:`,
	} {
		if strings.Contains(string(src), bad) {
			t.Fatalf("W-046b 机械门：search_engine.go 仍含未守卫复数格式串 %q —— 请改用 plural() 助手", bad)
		}
	}
}
