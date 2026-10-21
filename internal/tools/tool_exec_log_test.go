package tools

// C9（S13-G3）· 工具执行必须留下**可审计级别**的日志（I9/I12 可复现性）。
//
// 移交单：内部工作回执
// 决策项：C9（§2.3 族C）
//
// 缺陷面（修改前）：`executor.go` 的 executeDirect 只 `whalelog.Debug("[perf] tool: execute done", …)`，
// 而生产默认级别是 Info（internal/log/log.go:19 `level = slog.LevelInfo`）⇒ 默认配置下
// **工具执行零留痕**，"谁在什么时候执行了什么工具、成功还是失败"无法从 whale.log 还原。
//
// 判别性（A/B）：把 executor.go 里该行单点回退为 `whalelog.Debug(...)`（overlay 反向重建）⇒
// JSON handler（Level=Info）在 Enabled 阶段就丢弃该记录，日志文件里该行**消失** ⇒ 本条打红。
//
// ⚠️ 观测面刻意选**磁盘日志文件**（生产真实出口）而不是内存断言：
// 断言"调用了一个 Info 函数"属于接线断言假绿 —— 真正要证明的是"默认级别下它真的落进日志"。

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	whalelog "github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/testutil"
)

func TestGT_C9_ToolExecutionLoggedAtAuditableLevel(t *testing.T) {
	// 生产默认形态：不设 WHALE_DEBUG、不调 SetLevel(Debug)。
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
	whalelog.SetLevel(slog.LevelInfo) // log.go:19 的默认值，显式写死以防被同包其它用例改过

	logPath := filepath.Join(t.TempDir(), "whale.log")
	if err := whalelog.InitFileLogger(logPath, 10, 2, 1); err != nil {
		t.Fatalf("InitFileLogger: %v", err)
	}
	t.Cleanup(func() {
		whalelog.CloseFileLogger()
		whalelog.SetLevel(slog.LevelInfo) // 重建 handler，别让后续用例写进已关闭的 sink
	})

	ws := t.TempDir()
	target := filepath.Join(ws, "c9.txt")
	if err := os.WriteFile(target, []byte("C9_MARKER"), 0o644); err != nil {
		t.Fatalf("准备夹具失败: %v", err)
	}

	e := gtExecutorAtProductionEntry(t, ws)
	e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelWorkspaceWrite), ws)

	res := e.ExecuteWithConfirm(context.Background(), Call{
		ID:       "gt-c9",
		Name:     "read_file",
		Input:    `{"path":"` + target + `"}`,
		Approved: true,
	})
	if res.Result == nil || res.Result.IsError || !strings.Contains(res.Result.Output, "C9_MARKER") {
		t.Fatalf("前提失效：工具没有真正执行到 executeDirect —— 本条会变成空测。\n"+
			"proceed=%v rejection=%q failure_kind=%q\nresult=%+v\nstages=%+v",
			res.Proceed, res.Rejection, res.FailureKind, res.Result, res.StageLogs)
	}

	whalelog.CloseFileLogger() // flush：关闭 sink 后再读文件

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", logPath, err)
	}

	type logRec struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}

	var (
		found    bool
		allLines []string
	)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		allLines = append(allLines, line)
		if !strings.Contains(line, "tool: execute done") {
			continue
		}
		var rec logRec
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("该行不是 JSON 日志（%v）：%s", err, line)
		}
		found = true
		if !strings.EqualFold(rec.Level, "INFO") {
			t.Fatalf("C9：工具执行留痕级别 = %q，期望 INFO（可审计级别；"+
				"DEBUG 在默认级别下被过滤 ⇒ 等价于零留痕）", rec.Level)
		}
	}
	if !found {
		t.Fatalf("C9 回归：默认级别（Info）下工具执行**零留痕**（I12 不成立）。\n日志内容=\n%s",
			strings.Join(allLines, "\n"))
	}
}
