package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// mustReadRepoFile 读取仓库内文件（路径相对仓库根），用于下面两条**结构面**判据。
// 相对包目录（internal/tui）上两级即仓库根；这里故意读磁盘源码 —— 两条判据验的是
// "入口接线传了什么 / 还有没有第二处计数"，只能对源码文本断言。
func mustReadRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	return string(b)
}

// 本文件钉住 C1 切口② 的修复：**折叠提示里的 "/tool <N>" 必须能取回它自己那条工具结果**。
//
// 修复前的形态：折叠提示写死「输入 /tool 查看完整输出」，而无参 /tool 恒返回**最近一条**
// ⇒ 用户照第 1 个折叠块的提示输入，拿到的是最后一条 —— 提示指东、返回打西。
//
// ★ 判据落在**用户可见现象**上：从 resume 后的显示行里**提取**提示中的序号，
// 再照着这个序号调用 /tool，断言取回的正是该块自己的输出。
// 不是"断言代码里传了 Seq"（接线断言，恒绿），而是"照提示操作能拿到对的东西"。

var toolHintSeqRe = regexp.MustCompile(`/tool (\d+)`)

// foldedOut 造一条必折叠（> head+tail 行）且内容可区分的输出。
func foldedOut(tag string) string { return strings.Repeat(tag+"\n", 20) }

// hintSeqsInOrder 按显示行顺序提取折叠提示里的 "/tool <N>" 序号。
func hintSeqsInOrder(lines []string) []int {
	var out []int
	for _, l := range lines {
		for _, m := range toolHintSeqRe.FindAllStringSubmatch(l, -1) {
			n, _ := strconv.Atoi(m[1])
			out = append(out, n)
		}
	}
	return out
}

// TestToolResultSeqHintPointsToItsOwnResult 端到端同源判据（resume 派生 → 显示 → /tool）。
func TestToolResultSeqHintPointsToItsOwnResult(t *testing.T) {
	m := NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
	m.reassignServices() // 修复按值返回后的悬空服务别名（值副本陷阱）

	sess := session.NewSession()
	sess.SetMessages([]session.Message{
		session.UserText("go"),
		session.ToolResult("id-1", "bash", foldedOut("OUT_ONE"), false),
		session.ToolResult("id-2", "read_file", foldedOut("OUT_TWO"), false),
		session.ToolResult("id-3", "write_file", foldedOut("OUT_THREE"), false),
	})
	m.SessionModel.SetCurrentSession(sess)

	// 生产入口：resume 派生显示行（折叠提示就产自这里）
	// 会话 id 必须进程内唯一：TestMain 让整个测试进程共享一个 HOME ⇒ 共享锁目录，
	// 占用闸门会把「本进程上一次的持有」判成别的活进程 ⇒ 2s 超时后整体拒绝装载。
	// 唯一 id 的生成与原因统一收在 uniqueTestSessionID（tui_regression_test.go），此处不再各写一份。
	if err := m.applyResumedSession(uniqueTestSessionID(t), sess, "Restored"); err != nil {
		t.Fatalf("resume 装载失败：%v", err)
	}

	seqs := hintSeqsInOrder(m.SessionModel.Messages())
	if len(seqs) != 3 {
		t.Fatalf("应有 3 条折叠提示且各带序号；got=%v（全显示行=%q）",
			seqs, truncateRunes(strings.Join(m.SessionModel.Messages(), " | "), 400))
	}
	for i, s := range seqs {
		if s != i+1 {
			t.Fatalf("第 %d 条折叠块的提示序号 = %d，应为 %d（序号必须与会话顺序一致）", i+1, s, i+1)
		}
	}

	// ★ 核心：照提示里的序号输入 /tool，取回的必须是**它自己那条**
	want := []string{"OUT_ONE", "OUT_TWO", "OUT_THREE"}
	for i, s := range seqs {
		got := m.handleToolCommand(strconv.Itoa(s))
		if !strings.Contains(got, want[i]) {
			t.Fatalf("照提示输入 /tool %d：应取回 %s；got=%q",
				s, want[i], truncateRunes(got, 200))
		}
		// 反向半边：不得混入其它条目的内容（否则是"拿回全部"或"拿错但恰好含目标串"）
		for j, other := range want {
			if j == i {
				continue
			}
			if strings.Contains(got, other) {
				t.Fatalf("/tool %d 的输出混入了第 %d 条内容（%s）；got=%q",
					s, j+1, other, truncateRunes(got, 200))
			}
		}
	}
}

// TestToolResultSeqPlainDoesNotPointToMissingCommand plain 入口没有 /tool ⇒ 不得指路。
// 与 tools 包的 ToolFoldHint 判据互补：这边验的是**入口接线**（谁传了什么 Command）。
func TestToolResultSeqPlainDoesNotPointToMissingCommand(t *testing.T) {
	// 结构面：plain.go 里不得出现 "/tool" 提示的拼装（该入口无 case "tool"）。
	// 用源码级断言代替行为断言，因为 --plain 的输出走 fmt.Printf、不便在单测里捕获。
	src := mustReadRepoFile(t, "internal/tui/plain.go")
	if strings.Contains(src, `ToolFoldHint{Command: "/tool"`) {
		t.Fatalf("plain.go 不得传 Command=\"/tool\"：该入口没有 /tool 命令，提示会指向不存在的命令")
	}
	if !strings.Contains(src, "ToolFoldHint{}") {
		t.Fatalf("plain.go 应显式传空 ToolFoldHint（不指路），未找到")
	}
}

// TestToolResultSeqSSOTIsSingleSource 结构面：序号的遍历计数只允许有一处。
// 两处各数一遍 = 同一语义两份实现 ⇒ 迟早漂移回"提示指东、返回打西"。
func TestToolResultSeqSSOTIsSingleSource(t *testing.T) {
	src := mustReadRepoFile(t, "internal/tui/format.go")
	// format.go（/tool 的实现）不得再就地遍历 session.RoleTool 计数
	if strings.Contains(src, "for _, b := range msg.Blocks {") &&
		strings.Contains(src, "type toolEntry struct") {
		t.Fatalf("format.go 仍在就地收集工具结果：序号必须走 SSOT toolResultSeq")
	}
	if !strings.Contains(src, "newToolResultSeq(msgs)") {
		t.Fatalf("format.go 应改用 SSOT newToolResultSeq")
	}
}
