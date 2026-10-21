package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/util"
)

// 双语文案守卫（git / worktree / monitor 工具结果面）。
//
// 形态 SSOT = internal/util/bilingual.go（英文主行 + 3 空格缩进中文辅行）。
// 分工原则（照 internal/util/bilingual_test.go:18-20）：**字符集同源**
// （复用 util.HasHan / util.AssertChineseOnlyOnIndentedLines）、**判据逻辑独立**
// （本文件自带 AST 遍历与扫描循环，不调用生产侧断言）。

var b2cFiles = []string{"git.go", "worktree.go", "monitor.go"}

// b2cUnwrappedHanEmit 返回 src 中「把中文当用户可见消息**直接发射**、且未经
// util.Bi / util.BiSame 包装」的位置。
//
// 覆盖两类发射点：
// 1. fmt.Errorf / fmt.Fprintf / fmt.Fprintln / fmt.Fprint / errors.New 的**直接实参**里
// 含汉字的字面量，且该字面量不在嵌套的 util.Bi / util.BiSame 子树内；
// 2. `return "<含汉字字面量>"`（裸返回中文）。
//
// ★ 有意**不**把 fmt.Sprintf 列入发射点：Sprintf 只是构造字符串，真正发射发生在
// return / Errorf。worktree.go 的整句式双语需要先拼 zh 片段（如 `(分支 %s)`）再交给
// util.Bi —— 那是**合法**的中间构造，不是发射。
//
// 抽成吃源码字节的纯函数 ⇒ 判别性自证可在内存内用合成源码完成（零磁盘写回），
// 规避 ：overlay 只改编译期输入，对「运行期自读磁盘」的判别器物理无效。
func b2cUnwrappedHanEmit(src []byte) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		return []string{"PARSE_ERROR: " + err.Error()}
	}

	// 1) 收集所有 util.Bi / util.BiSame 调用子树内的字面量位置（= 已包装）。
	wrapped := map[token.Pos]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "util" || (sel.Sel.Name != "Bi" && sel.Sel.Name != "BiSame") {
			return true
		}
		ast.Inspect(call, func(m ast.Node) bool {
			if lit, ok := m.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				wrapped[lit.Pos()] = true
			}
			return true
		})
		return true
	})

	var out []string
	add := func(pos token.Pos, val string) {
		out = append(out, fmt.Sprintf("line %d: %s", fset.Position(pos).Line, val))
	}

	// 2) 找违规发射点。
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			sel, ok := v.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			isEmit := false
			switch pkg.Name {
			case "fmt":
				switch sel.Sel.Name {
				case "Errorf", "Fprintf", "Fprintln", "Fprint":
					isEmit = true
				}
			case "errors":
				if sel.Sel.Name == "New" {
					isEmit = true
				}
			}
			if !isEmit {
				return true
			}
			for _, a := range v.Args {
				if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING &&
					util.HasHan(lit.Value) && !wrapped[lit.Pos()] {
					add(lit.Pos(), lit.Value)
				}
			}
		case *ast.ReturnStmt:
			// 「(en, zh) 对生产者」是**设计形态**（见 pipeline.go checkDestructive
			// 上方注释）：帮助函数返回两个裸字符串（[0]=英文主行、[1]=中文辅行），
			// 调用点在两侧外面各包一层框架文案后交给 util.Bi —— 预拼串无法再拆。
			// 该形态**不算**违规：消费侧由 rule 1（util.Bi 子树内的字面量视为已包装）兜住。
			// 若把 pair return 也判红 ⇒ 会把合法形态误判（本门第一版即踩此坑）。
			if isBilingualPairReturn(v.Results) {
				return true
			}
			for _, r := range v.Results {
				if lit, ok := r.(*ast.BasicLit); ok && lit.Kind == token.STRING && util.HasHan(lit.Value) {
					add(lit.Pos(), lit.Value)
				}
			}
		}
		return true
	})
	return out
}

// isBilingualPairReturn 判定一组返回值是否为「(en, zh) 双语对」形态：
// 恰两个结果、均为字符串字面量、[1] 含汉字且 [0] 不含汉字。
//
// 这是**契约级**的形态判定（不是宽松）：[0] 必须是英文主行、[1] 才是中文辅行；
// `return "中文", nil`（单侧）或 `return "中文", "中文"`（两侧皆中）都**不**匹配 ⇒ 仍判违规。
func isBilingualPairReturn(results []ast.Expr) bool {
	if len(results) != 2 {
		return false
	}
	en, ok1 := results[0].(*ast.BasicLit)
	zh, ok2 := results[1].(*ast.BasicLit)
	if !ok1 || !ok2 || en.Kind != token.STRING || zh.Kind != token.STRING {
		return false
	}
	return util.HasHan(zh.Value) && !util.HasHan(en.Value)
}

// TestW083B2c_TouchedFilesExist 前置自检：被判文件必须真在，否则遍历零命中 = 假绿。
func TestW083B2c_TouchedFilesExist(t *testing.T) {
	for _, f := range b2cFiles {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("被守卫的源文件读不到（%s）: %v", f, err)
		}
		if len(src) == 0 {
			t.Fatalf("被守卫的源文件为空（%s）", f)
		}
	}
}

// TestW083B2c_NoUnwrappedHanEmit 结构面：三个文件里不得有「直接发射未包装中文」的写法。
func TestW083B2c_NoUnwrappedHanEmit(t *testing.T) {
	for _, fn := range b2cFiles {
		src, err := os.ReadFile(fn)
		if err != nil {
			t.Fatalf("读 %s: %v", fn, err)
		}
		for _, v := range b2cUnwrappedHanEmit(src) {
			t.Errorf("%s: 中文被直接发射且未经 util.Bi/BiSame 包装: %s", fn, v)
		}
	}
}

// TestW083B2c_EmitGateCatchesInjection 判别性自证：注入「直接发射中文」必须被抓、
// 合规写法不得被误判。
func TestW083B2c_EmitGateCatchesInjection(t *testing.T) {
	bad := []byte("package p\n\nimport (\n\t\"errors\"\n\t\"fmt\"\n)\n\n" +
		"func f() (string, error) {\n" +
		"\tif true {\n\t\treturn \"\", fmt.Errorf(\"中文错误：直接发射\")\n\t}\n" +
		"\tif false {\n\t\treturn \"裸返回中文\", nil\n\t}\n" +
		"\treturn \"\", errors.New(\"另一个中文错误\")\n" +
		"}\n")
	if got := b2cUnwrappedHanEmit(bad); len(got) != 3 {
		t.Errorf("发射门应抓到 3 处（Errorf / 裸 return / errors.New），实得 %d: %v", len(got), got)
	}

	good := []byte("package p\n\nimport (\n\t\"fmt\"\n\n\t\"github.com/anchorandfish/whale-code/internal/util\"\n)\n\n" +
		"func f() (string, error) {\n\treturn \"\", fmt.Errorf(\"%s\", util.Bi(\"english error\", \"中文错误\"))\n}\n\n" +
		"func g() string { return util.Bi(\"ok\", \"好\") }\n")
	if got := b2cUnwrappedHanEmit(good); len(got) != 0 {
		t.Errorf("发射门误判了合规写法（判据太宽）: %v", got)
	}
}

// TestW083B2c_PairReturnPattern 专项判别「(en, zh) 双语对生产者」形态：
// 合法形态不得被误判，单侧/双中文的伪形态必须仍被抓。
func TestW083B2c_PairReturnPattern(t *testing.T) {
	// ① 合法：恰 2 个字符串字面量、[0] 英文、[1] 中文 ⇒ 不判违规
	ok := []byte("package p\n\n" +
		"func pair() (string, string) {\n" +
		"\treturn \"exit_worktree with action=remove (deletes worktree directory and branch)\",\n" +
		"\t\t\"exit_worktree action=remove（删除 worktree 目录与分支）\"\n" +
		"}\n")
	if got := b2cUnwrappedHanEmit(ok); len(got) != 0 {
		t.Errorf("(en, zh) 对形态被误判为违规（判据太宽）: %v", got)
	}

	// ② 伪形态：单侧（return \"中文\", nil）⇒ 必须仍被抓
	oneSided := []byte("package p\n\nfunc f() (string, error) {\n\treturn \"裸返回中文\", nil\n}\n")
	if got := b2cUnwrappedHanEmit(oneSided); len(got) == 0 {
		t.Error("单侧裸返回中文必须被抓（否则判据形同虚设）")
	}

	// ③ 伪形态：两侧皆中文（[0] 不是英文主行）⇒ 必须仍被抓
	bothHan := []byte("package p\n\nfunc f() (string, string) {\n\treturn \"中文一\", \"中文二\"\n}\n")
	if got := b2cUnwrappedHanEmit(bothHan); len(got) == 0 {
		t.Error("两侧皆中文的伪对必须被抓（否则 pair 识别太宽松）")
	}
}

// TestW083B2c_EnglishMainLinesPresent 正向：改造后的英文主行字面量必须在。
// （挡「悄悄退回中文为主」这类回归 —— 与结构门互补，各挡一种回归。）
func TestW083B2c_EnglishMainLinesPresent(t *testing.T) {
	want := map[string][]string{
		"git.go": {
			"Not a Git repository: the current directory is not inside a Git work tree",
			"Working tree clean, nothing to commit",
			"Added to staging area: %s (%s)",
			"Commit succeeded: %s",
			"Current branch: %s",
			"Created and switched to branch: %s (%s)",
			"Switched to branch: %s (%s)",
		},
		"worktree.go": {
			"Already in a worktree session; exit the current worktree first",
			"Worktree name validation failed",
			"Worktree name cannot contain path separators",
			"Not in a worktree session; nothing to exit",
			"Cannot determine worktree status (%v); set discard_changes=true to force removal",
			"removing it will permanently discard this work.",
			"Created worktree at %s%s. Session switched to the worktree. Use exit_worktree to exit.",
			"Exited worktree. Work kept at %s%s. Session returned to %s.",
			"Exited and removed worktree (%s).",
			"action cannot be empty",
			"action must be",
		},
		"monitor.go": {
			"command cannot be empty",
			"monitor manager is not initialized",
			"Blocked: %s.",
		},
	}
	for fn, subs := range want {
		src, err := os.ReadFile(fn)
		if err != nil {
			t.Fatalf("读 %s: %v", fn, err)
		}
		for _, sub := range subs {
			if !strings.Contains(string(src), sub) {
				t.Errorf("%s: 英文主行字面量缺失（%q）—— 疑似退回「中文为主」", fn, sub)
			}
		}
	}
}

// assertB2cBilingual 行为面断言：真实输出过生产形态门（含汉字的行必须缩进）且英文主行在。
func assertB2cBilingual(t *testing.T, name, out, wantEN string) {
	t.Helper()
	if v := util.AssertChineseOnlyOnIndentedLines(out); v != "" {
		t.Errorf("[%s] 输出有未缩进的中文行（形态门不过）: %q\n完整输出:\n%s", name, v, out)
	}
	if !strings.Contains(out, wantEN) {
		t.Errorf("[%s] 英文主行缺失（%q）\n完整输出:\n%s", name, wantEN, out)
	}
}

// TestW083B2c_GitHandlersBilingual 行为面：git 工具六个 handler 的真实结果。
func TestW083B2c_GitHandlersBilingual(t *testing.T) {
	// ① 非 Git 仓库
	nonRepo := t.TempDir()
	e := NewExecutor(NewRegistry(), nonRepo)
	res := e.Execute(context.Background(), Call{ID: "1", Name: "git_status", Input: `{}`})
	assertB2cBilingual(t, "git_status(non-repo)", res.Output, "Not a Git repository")

	// ② 干净仓库
	repo := t.TempDir()
	initGitRepo(t, repo)
	createInitialCommit(t, repo)
	e2 := NewExecutor(NewRegistry(), repo)

	res = e2.Execute(context.Background(), Call{ID: "2", Name: "git_status", Input: `{}`})
	assertB2cBilingual(t, "git_status(clean)", res.Output, "Working tree clean, nothing to commit")

	// ③ git_branch current
	res = e2.Execute(context.Background(), Call{ID: "3", Name: "git_branch", Input: `{"action":"current"}`})
	assertB2cBilingual(t, "git_branch(current)", res.Output, "Current branch: ")

	// ④ git_add（先造一处真实变更，否则 git add 无内容可暂存）
	if err := os.WriteFile(filepath.Join(repo, "b2c.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	res = e2.Execute(context.Background(), Call{ID: "4", Name: "git_add", Input: `{"files":["b2c.txt"]}`})
	assertB2cBilingual(t, "git_add", res.Output, "Added to staging area: ")

	// ⑤ git_commit
	res = e2.Execute(context.Background(), Call{ID: "5", Name: "git_commit", Input: `{"message":"b2c"}`})
	assertB2cBilingual(t, "git_commit", res.Output, "Commit succeeded: ")
}

// TestW083B2c_WorktreeOutputsBilingual 行为面：worktree 管理器/处理器的真实错误输出。
func TestW083B2c_WorktreeOutputsBilingual(t *testing.T) {
	// ① 非 Git 仓库 → enter 错误
	nonRepo := t.TempDir()
	wm := NewWorktreeManager(nonRepo)
	if _, err := wm.EnterWorktree(context.Background(), "x", ""); err == nil {
		t.Fatal("非 Git 仓库 enter 应报错")
	} else {
		assertB2cBilingual(t, "EnterWorktree(non-repo)", err.Error(), "Not a Git repository")
	}

	// ② 不在 worktree 会话 → exit 错误
	if _, _, err := wm.ExitWorktree(context.Background(), "keep", false); err == nil {
		t.Fatal("不在会话时 exit 应报错")
	} else {
		assertB2cBilingual(t, "ExitWorktree(no-session)", err.Error(), "Not in a worktree session")
	}

	// ③ 真实 Git 仓库 + 非法名称 → 名称校验错误
	repo := t.TempDir()
	initGitRepo(t, repo)
	wm2 := NewWorktreeManager(repo)
	if _, err := wm2.EnterWorktree(context.Background(), "bad name!", ""); err == nil {
		t.Fatal("非法 worktree 名称应报错")
	} else {
		assertB2cBilingual(t, "EnterWorktree(bad-name)", err.Error(), "Worktree name validation failed")
	}

	// ④ exit 的 action 校验（在 handler 层，先于 session 检查）
	eh := (&Executor{handlers: make(map[string]Handler)}).exitWorktreeHandler(repo)
	if _, err := eh(context.Background(), json.RawMessage(`{"action":""}`)); err == nil {
		t.Fatal("空 action 应报错")
	} else {
		assertB2cBilingual(t, "exit_worktree(empty-action)", err.Error(), "action cannot be empty")
	}
	if _, err := eh(context.Background(), json.RawMessage(`{"action":"bogus"}`)); err == nil {
		t.Fatal("非法 action 应报错")
	} else {
		assertB2cBilingual(t, "exit_worktree(bad-action)", err.Error(), "action must be")
	}

	// ⑤ 完整 enter → exit(keep) 成功路径
	repo2 := t.TempDir()
	initGitRepoForIntegration(t, repo2)
	e2 := NewExecutor(NewRegistry(), repo2)
	ctx := context.Background()
	res := e2.Execute(ctx, Call{ID: "e1", Name: "enter_worktree", Input: `{"name":"b2c-keep"}`})
	assertB2cBilingual(t, "enter_worktree(ok)", res.Output, "Created worktree at ")
	res = e2.Execute(ctx, Call{ID: "e2", Name: "exit_worktree", Input: `{"action":"keep"}`})
	assertB2cBilingual(t, "exit_worktree(keep)", res.Output, "Exited worktree. Work kept at ")

	// ⑥ 完整 enter → exit(remove) 成功路径（无变更 ⇒ 直接删）
	res = e2.Execute(ctx, Call{ID: "e3", Name: "enter_worktree", Input: `{"name":"b2c-rm"}`})
	res = e2.Execute(ctx, Call{ID: "e4", Name: "exit_worktree", Input: `{"action":"remove"}`})
	assertB2cBilingual(t, "exit_worktree(remove)", res.Output, "Exited and removed worktree (")
}

// TestW083B2c_MonitorOutputsBilingual 行为面：monitor 工具的真实错误输出。
func TestW083B2c_MonitorOutputsBilingual(t *testing.T) {
	workDir := t.TempDir()
	e := &Executor{
		handlers:   make(map[string]Handler),
		MonitorMgr: monitor.NewManager(),
	}
	handler := e.monitorHandler(workDir)

	// ① 空命令
	in, _ := json.Marshal(MonitorInput{Command: ""})
	if _, err := handler(context.Background(), in); err == nil {
		t.Fatal("空命令应报错")
	} else {
		assertB2cBilingual(t, "monitor(empty-command)", err.Error(), "command cannot be empty")
	}

	// ② 超长 sleep 被阻止
	in, _ = json.Marshal(MonitorInput{Command: "sleep 5"})
	if _, err := handler(context.Background(), in); err == nil {
		t.Fatal("sleep 5 应被阻止")
	} else {
		assertB2cBilingual(t, "monitor(blocked-sleep)", err.Error(), "Blocked: ")
	}
}
