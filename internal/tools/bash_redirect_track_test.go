package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/filehistory"
	"github.com/anchorandfish/whale-code/internal/testutil"
)

// bash_redirect_track_test.go — 迭代③「bash 写重定向入卡」接线判据。
//
// 为什么必须有这一条（R1/R2，与 turn_diff_wiring_test.go 同一道理）：
// 重定向目标的 track 由 **bash handler 自己**在执行前后写进 filehistory
// （bash.go 生产入口，executor.go:1087 注册）。单测直接调 Track/TrackAfter 只能证明
// "聚合口径对"，证不了生产那条线真的通 —— 提取器没接上、TrackAfter 漏调、
// msgId 拿错，任何一处断线都会让"bash 写的文件"从卡片上消失，而单测全绿。
//
// ⇒ 本判据从生产工具入口走：真实 bash handler → 它自己调 Track/TrackAfter。
//
// 判别性（overlay 变体，读数见回执）：
// - 摘掉 bash.go 的 TrackAfter 调用 ⇒ TestBashRedirect_TargetsTrackedAsTurnChange 红
// （重定向目标从净变更里消失）；
// - 把 BashWriteTargets 换成恒返回空 ⇒ 同上红；
// - 摘掉 dev-sink 过滤（validator.isDevSink 恒 false）⇒ TestBashRedirect_DevSink 红。
//
// 边界（拍板时的诚实声明，勿当 bug）：run_in_background 的 bash 走 TaskMgr
// （task.go 独立执行链）、Monitor 是另一个工具 —— 两者本轮均不入 track（漏报方向，
// 由卡片提示行兜底），已在总账登记。

// bashThroughTool 经真实工具入口跑一次 bash（Track/TrackAfter 由 handler 自己调）。
// 命令 exit≠0 ≠ 工具失败：重定向在 shell 拉起进程时就已发生（`>` 先截断再执行），
// 所以这里不断言 IsError；但**沙箱拒绝**（命令根本没跑）必须排除，
// 否则断言建立在"没执行"的前提上（R2）。
func bashThroughTool(t *testing.T, e *Executor, command, callID string) {
	t.Helper()
	res := e.Execute(context.Background(), Call{
		ID:    callID,
		Name:  "bash",
		Input: mustMarshal(map[string]string{"command": command}),
	})
	if res.IsError && strings.Contains(res.Output, "sandbox denied") {
		t.Fatalf("bash 被沙箱拒绝，判据前提不成立（命令未执行）：%s", res.Output)
	}
}

// `>` 与 `>` 覆盖混合的一条命令 ⇒ 两个目标都进净变更：新建标 IsNew、
// 覆盖带轮首基线；且 BashRan 旗标为 true（卡片提示行的前提）。
func TestBashRedirect_TargetsTrackedAsTurnChange(t *testing.T) {
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
	workDir := t.TempDir()
	e := NewExecutor(NewRegistry(), workDir)
	if e.FileHistory == nil {
		t.Fatal("前置失败：FileHistory 未初始化（降级态）⇒ 本判据不可执行")
	}
	const msgID = "user-msg-bash-1" // 生产中由 sendInput 设置（stream.go:300）
	e.SetCurrentMessageId(msgID)

	cover := filepath.Join(workDir, "cover.txt")
	if err := os.WriteFile(cover, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}

	bashThroughTool(t, e, "printf 'new-file' > brand_new.txt && printf 'newer\\n' > cover.txt", "call-b1")

	changes, err := e.FileHistory.TurnChanges(msgID)
	if err != nil {
		t.Fatalf("TurnChanges: %v", err)
	}
	byPath := make(map[string]filehistory.TurnChange, len(changes))
	for _, c := range changes {
		byPath[c.Path] = c
	}
	if len(changes) != 2 {
		t.Fatalf("两个重定向目标都应进净变更，实际 %d 条：%+v", len(changes), changes)
	}

	// NewFileHistory 会 EvalSymlinks（macOS 上 /var/… ⇒ /private/var/…），
	// 期望路径必须用同一解析形态，否则是断言自身的路径形态错，不是接线错。
	resolvedWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}

	newFile := filepath.Join(resolvedWorkDir, "brand_new.txt")
	got, ok := byPath[newFile]
	if !ok {
		t.Fatalf("新建目标 brand_new.txt 未入账，实际：%+v", changes)
	}
	if !got.IsNew {
		t.Errorf("新建目标应标 IsNew：%+v", got)
	}
	if got.After != "new-file" {
		t.Errorf("新建目标终态应为重定向写入的内容，实际 %q", got.After)
	}

	gotCover, ok := byPath[filepath.Join(resolvedWorkDir, "cover.txt")]
	if !ok {
		t.Fatalf("覆盖目标 cover.txt 未入账，实际：%+v", changes)
	}
	if gotCover.Before != "old\n" || gotCover.After != "newer\n" {
		t.Errorf("覆盖目标应带轮首基线（old\\n → newer\\n），实际 %q → %q", gotCover.Before, gotCover.After)
	}
}

// `> /dev/null`（无害 sink）与 `2>&1`（fd 复制，不落盘）都不得进净变更 ——
// 这是"多报"防线：把没写文件的东西列上卡片就是误报。
func TestBashRedirect_DevSinkAndFdDupNotTracked(t *testing.T) {
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
	workDir := t.TempDir()
	e := NewExecutor(NewRegistry(), workDir)
	const msgID = "user-msg-bash-2"
	e.SetCurrentMessageId(msgID)

	bashThroughTool(t, e, "echo x > /dev/null 2>&1; echo y >&2", "call-b2")

	changes, err := e.FileHistory.TurnChanges(msgID)
	if err != nil {
		t.Fatalf("TurnChanges: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("/dev/null 与 fd 复制不落盘，不得出现在净变更里；实际 %d 条：%+v", len(changes), changes)
	}
}

// 变量路径（`> "$VAR/x"`）提不出静态目标 ⇒ **不猜、不编造**：
// 净变更里不得出现任何猜测路径（宁漏报、由提示行兜底，不做"多报"）。
func TestBashRedirect_UnresolvedNotGuessed(t *testing.T) {
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
	workDir := t.TempDir()
	e := NewExecutor(NewRegistry(), workDir)
	const msgID = "user-msg-bash-3"
	e.SetCurrentMessageId(msgID)

	bashThroughTool(t, e, "echo x > \"$W044_UNSET_VAR/zz_should_never_appear.txt\"", "call-b3")

	changes, err := e.FileHistory.TurnChanges(msgID)
	if err != nil {
		t.Fatalf("TurnChanges: %v", err)
	}
	for _, c := range changes {
		if strings.Contains(c.Path, "zz_should_never_appear") {
			t.Fatalf("变量路径不得被猜测成具体文件入账：%+v", c)
		}
	}
	// 变量展开为空 ⇒ shell 写 /zz_... 失败，工作区内不会有真文件；即使 shell 起了，
	// 也不得有任何目标被记成"已提交变更"。
	if len(changes) != 0 {
		t.Logf("（信息）未解析目标轮的净变更：%+v", changes)
	}
}
