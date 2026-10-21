package validator

import "testing"

// === 判别器：fd 复制（2>&1 / >&2）不是写文件重定向 ===
//
// 背景：`go test ./... 2>&1`（2>&1 在命令末尾）
// 被 DANG-REDIR-AST 硬拒，且用户点「允许」也照样拒 ⇒ 直接废掉 T-3。
//
// 根因（本文件断言其行为面）：
// 1. tokenizer 把 `2>&1` 整体吞成一个 Redirect token（无 target token）；
// 2. parseRedirect 把**下一个** token 当 target；`2>&1` 在末尾时取到 EOF ⇒ target="";
// 3. isDangerousWriteTarget("") ⇒ true（空值兜底）；
// 4. checkDangerousRedirectAST 只判「Op 含 >」，不区分 `>&`（fd 复制）与 `>`（写文件）。
//
// 语义依据（POSIX "Duplicating File Descriptors" / bash 手册 "Redirection"）：
// `[n]>&m`（m 为数字）是把 fd m 复制到 fd n，**不打开、不创建、不写任何文件**。
//
// ★ 边界由本机 bash 实测钉死：
// - `2>&1` / `>&1` → fd 复制，不落盘；
// - `>&/tmp/f` → **会创建文件**（bash 视 `>&word` 等价 `&>word`）；
// - `2>& /tmp/f` → bash 报 "ambiguous redirect"，不落盘。
// ⇒ 只有 `>&` 后紧跟**非空数字串**才算 fd 复制；否则一律按「真写」处理（fail-closed）。
//
// 全部断言从**生产真实入口** `NewValidator().Run()` 进入（不构造 AST、不绕守卫）。

// violationIDs 返回一次 Run 命中的全部检查项 ID（仅 Passed=false 的项）。
func violationIDs(cmd string) []string {
	var ids []string
	for _, r := range NewValidator().Run(cmd) {
		if !r.Passed {
			ids = append(ids, r.CheckID)
		}
	}
	return ids
}

// hasAny 判断 ids 中是否命中 want 中任意一项。
func hasAny(ids []string, want ...string) bool {
	for _, id := range ids {
		for _, w := range want {
			if id == w {
				return true
			}
		}
	}
	return false
}

// TestW016_FDDupNotDangerous：fd 复制形态**不得**被判为危险写重定向。
//
// 这是 的本体：修复前这些用例红。
func TestW016_FDDupNotDangerous(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{" 原始实例：2>&1 在末尾", "go test ./... -count=1 2>&1"},
		{"2>&1 在末尾（build）", "go build ./... 2>&1"},
		{"2>&1 后接分号（历史偶然放行）", "go build ./... 2>&1; echo rc=$?"},
		{"合法写 + 末尾 2>&1", "echo hi > /tmp/out.txt 2>&1"},
		{"无数字前缀的 fd 复制 >&2", "cmd >&2"},
		{"fd 复制到 /dev/null（安全 sink）", "cmd 2>&1 1>/dev/null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids := violationIDs(tc.cmd)
			if hasAny(ids, "DANG-REDIR-AST", "DANG-REDIR") {
				t.Errorf("cmd %q 被误判为危险写重定向（ 未修复），命中 %v", tc.cmd, ids)
			}
		})
	}
}

// TestW016_RealWriteStillBlocked：真写敏感路径**必须**仍被拦（不动安全语义）。
func TestW016_RealWriteStillBlocked(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"普通写敏感路径", "echo x > /etc/passwd"},
		{"fd 写敏感路径", "echo x 2> /etc/passwd"},
		{"追加写敏感路径", "echo x >> /etc/passwd"},
		{"无空格写敏感路径", "echo x 2>/etc/passwd"},
		{"&> 写敏感路径", "echo x &> /etc/passwd"},
		// ★ 潜伏漏洞：修前 parseRedirect 把 `>` 当成 `2>&1` 的 target 而吞掉，
		// 真写重定向丢失 ⇒ 只有字符串层兜住；修后 AST 层也应命中（纵深防御）。
		{"fd 复制 + 真写敏感路径（原被吞）", "echo x 2>&1 > /etc/passwd"},
		// ★ 边界：`>&/path`（非数字）bash 视作 `&>/path`，**会真建文件** ⇒ 必须拦。
		{"无数字前缀 >& 直接写文件", "echo x >&/etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids := violationIDs(tc.cmd)
			if !hasAny(ids, "DANG-REDIR-AST", "DANG-REDIR") {
				t.Errorf("cmd %q 未被拦（安全语义被削弱！），命中 %v", tc.cmd, ids)
			}
		})
	}
}

// TestW016_EmptyTargetStillFailClosed：空/无法解析的写目标必须仍 fail-closed。
//
// 这是被 误伤掩盖的**正确**行为，修 时不得连带放松。
func TestW016_EmptyTargetStillFailClosed(t *testing.T) {
	ids := violationIDs("echo x >")
	if !hasAny(ids, "DANG-REDIR-AST") {
		t.Errorf("末尾悬空 `>` 应 fail-closed 判危险，命中 %v", ids)
	}
}

// TestW016_RedirectNodeCarriesFDDup：结构化事实 —— fd 复制必须在 AST 层被标记，
// 而不是靠下游各消费点重新嗅探 op 字符串（避免"同一语义多处实现"）。
func TestW016_RedirectNodeCarriesFDDup(t *testing.T) {
	root := NewASTBuilder(NewTokenizer("go test ./... 2>&1").Tokenize()).Build()
	var got *RedirectNode
	WalkAST(root, func(n *CommandNode) bool {
		for i := range n.Redirects {
			if n.Redirects[i].Op == "2>&1" {
				got = &n.Redirects[i]
			}
		}
		return true
	})
	if got == nil {
		t.Fatal("未找到 2>&1 重定向节点")
	}
	if !got.FDDup {
		t.Errorf("2>&1 应标记 FDDup=true，实际 Op=%q Target=%q FDDup=%v", got.Op, got.Target, got.FDDup)
	}
	if got.Target != "1" {
		t.Errorf("fd 复制的 target 应为被复制的 fd \"1\"，实际 %q", got.Target)
	}

	// 无数字前缀形态 `>&2` 也应被 tokenizer 收敛为同一个自包含 fd 复制节点。
	root2 := NewASTBuilder(NewTokenizer("cmd >&2").Tokenize()).Build()
	var got2 *RedirectNode
	WalkAST(root2, func(n *CommandNode) bool {
		for i := range n.Redirects {
			if n.Redirects[i].Op == ">&2" {
				got2 = &n.Redirects[i]
			}
		}
		return true
	})
	if got2 == nil {
		t.Fatal("未找到 >&2 重定向节点（tokenizer 未把 >&N 收敛为自包含 token）")
	}
	if !got2.FDDup || got2.Target != "2" {
		t.Errorf(">&2 应 FDDup=true Target=\"2\"，实际 Op=%q Target=%q FDDup=%v", got2.Op, got2.Target, got2.FDDup)
	}
}

// TestW016_FDDupNoBogusWriteTarget：BashWriteTargets 不得把 fd 复制报成写目标，
// 也不得因它把整条命令标成 unresolved（否则审批层会退化成"永远询问"）。
func TestW016_FDDupNoBogusWriteTarget(t *testing.T) {
	targets, unresolved := BashWriteTargets("go test ./... -count=1 2>&1")
	if len(targets) != 0 {
		t.Errorf("2>&1 不应产生写目标，实际 %v", targets)
	}
	if unresolved {
		t.Errorf("2>&1 不应把命令标为 unresolved（否则审批层会多余询问）")
	}

	// 分号不应被当成写目标（修前 target 误取下一个 token ";"）。
	targets2, _ := BashWriteTargets("go build ./... 2>&1; echo rc=$?")
	for _, tg := range targets2 {
		if tg == ";" {
			t.Errorf("分号不应被当作写目标，实际 %v", targets2)
		}
	}
}

// TestW016_RealWriteStillExtracted：BashWriteTargets 对真写仍然提取（不得因修 fd 复制而漏报）。
func TestW016_RealWriteStillExtracted(t *testing.T) {
	targets, unresolved := BashWriteTargets("echo x > /etc/passwd 2>&1")
	found := false
	for _, tg := range targets {
		if tg == "/etc/passwd" {
			found = true
		}
	}
	if !found {
		t.Errorf("应提取出真写目标 /etc/passwd，实际 targets=%v unresolved=%v", targets, unresolved)
	}
}
