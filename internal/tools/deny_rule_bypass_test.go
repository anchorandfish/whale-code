package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件记录 2026-09-13 手测暴露的第二个问题（用户 23:26 的"6 个 explore 子代理"用例）：
// 子代理报告 "read_file 被拒绝，我将使用替代工具来获取内容 → 已成功读取"。
//
// 根因：`read_file(*)` 是**按工具名**匹配的规则（rules.go matchesTool），不是按能力/路径。
// 因此"禁止读文件"这一**用户意图**无法用该规则表达 —— 同一个文件内容仍可通过
// 其它未被 deny 的只读工具（grep_search / glob_search / LspDocumentSymbols …）静默读到。
// （bash 是写档，会被确认框拦住，属另一类"有人工闸门"的路径，见用例末尾。）
//
// 本用例断言这个缺口**当前确实存在**（红 = 现象成立）。它是"用户意图 vs 规则语义"
// 的落差，修复方向需要用户拍板（见交付说明），故本文件先固定现象、不做行为改动。
func TestDenyRule_ReadFileOnly_IsBypassableByOtherReadTools(t *testing.T) {
	workDir := t.TempDir()
	secret := filepath.Join(workDir, "SECRET.md")
	const marker = "TOP_SECRET_MARKER_9f3a"
	if err := os.WriteFile(secret, []byte("# "+marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["bash(rm:*)","read_file(*)"]},"priorityMode":"posix"}`)
	ctx := context.Background()

	// 1) read_file 命中 deny 规则 → 拒绝（这是用户看到的行为）
	readInput, _ := json.Marshal(map[string]any{"path": secret, "limit": 1})
	r1 := exec.ExecuteWithConfirm(ctx, Call{ID: "c-1", Name: "read_file", Input: string(readInput)})
	if r1.Proceed {
		t.Fatalf("前提失效：read_file 未被 deny 规则拦住，本用例无法说明问题")
	}
	t.Logf("read_file   -> Proceed=%v  %s", r1.Proceed, r1.Rejection)

	// 2) grep_search 读同一目录 → **放行**，且能拿到文件内容
	grepInput, _ := json.Marshal(map[string]any{
		"pattern": marker, "path": workDir, "output_mode": "content",
	})
	r2 := exec.ExecuteWithConfirm(ctx, Call{ID: "c-2", Name: "grep_search", Input: string(grepInput)})
	if !r2.Proceed || r2.Result == nil {
		t.Fatalf("grep_search 应被放行（预期缺口成立），实际 Proceed=%v Rejection=%q", r2.Proceed, r2.Rejection)
	}
	if !strings.Contains(r2.Result.Output, marker) {
		t.Fatalf("grep_search 未返回被保护文件内容，缺口不成立；output=%q", r2.Result.Output)
	}
	t.Logf("grep_search -> Proceed=%v NeedConfirm=%v 输出含被保护内容=%v",
		r2.Proceed, r2.NeedConfirm, strings.Contains(r2.Result.Output, marker))

	// 3) bash（本用例的 deny 只覆盖 `bash(rm:*)` 与 `read_file(*)` —— 都是**工具名**作用域，
	// 故不覆盖 bash 的读）。
	//
	// ★★ 2026-10-06 之后**缺口变宽**（如实登记，勿当噪音）：
	// `cat SECRET.md` 是**只读**命令 ⇒ 被只读免问出口 (e)（`cmdrules.AllSegmentsReadOnly`）放行
	// ⇒ 它现在与 `grep_search` 一样是**静默**的 —— 修复前它至少还会弹一次确认框
	//（"写档 ⇒ 需人工确认"这个**偶然**的闸门，见本文件头部注释对两类缺口的区分）。
	// 该闸门消失的原因不是 deny 规则变了，而是 bash 的确认面**只读即免问**。
	//
	// ★ 已拍板（2026-10-06 用户选 ①「让 deny 的匹配面识别只读效果」）：
	// 现在写成 **`Read(*)`** 就能覆盖 `bash cat SECRET.md`（见下方 TestW025_ReadCapability_*）。
	// 本用例**保持不变**：它用的是 `read_file(*)`，属**工具名作用域** —— 这正是要对照的
	// "换条路读同一个文件"缺口，也说明"用对规则形态（能力别名）"才是修复的关键。
	catInput, _ := json.Marshal(map[string]any{"command": "cat SECRET.md"})
	r3 := exec.ExecuteWithConfirm(ctx, Call{ID: "c-3", Name: "bash", Input: string(catInput)})
	if r3.NeedConfirm {
		t.Fatalf(" 之后 bash 只读读文件不再请求确认（缺口在确认层也消失了）。\n"+
			"  若这里恢复 NeedConfirm=true，说明只读免问出口 (e) 被改动/移除 ⇒ 请同步本断言与  的登记。\n"+
			"  Proceed=%v Rejection=%q", r3.Proceed, r3.Rejection)
	}
	t.Logf("bash cat    -> Proceed=%v NeedConfirm=%v（★  后与 grep_search 同为**静默**）",
		r3.Proceed, r3.NeedConfirm)

	// 3') **判别锚**：同一 deny 配置与同一 Executor 下，**非只读**的 bash 命令仍必须确认。
	// 没有这一条，本用例无法区分「只读免问生效」与「确认层整体失效」——
	// 后者会让 3) 以同样的形态通过（假判别）。
	writeInput, _ := json.Marshal(map[string]any{"command": "mkdir -p " + filepath.Join(workDir, "d3")})
	r4 := exec.ExecuteWithConfirm(ctx, Call{ID: "c-4", Name: "bash", Input: string(writeInput)})
	if !r4.NeedConfirm {
		t.Fatalf("非只读 bash 命令仍必须请求确认（否则 3) 的断言不构成判别）: Proceed=%v", r4.Proceed)
	}
	t.Logf("bash mkdir  -> NeedConfirm=%v（非只读仍有闸门 ⇒ 3) 的结论可归因到只读免问）", r4.NeedConfirm)
}

// 对照：read_file 的 deny 规则本身是生效的（deny 优先级最高，不受模式影响，
// 也不被 allow 规则覆盖）—— 用于排除"规则根本没生效"这一解释。
func TestDenyRule_ReadFileAlwaysWins(t *testing.T) {
	workDir := t.TempDir()
	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["read_file(*)"],"allow":["read_file(*)"]},"priorityMode":"posix"}`)

	input := fmt.Sprintf(`{"path":%q,"limit":1}`, filepath.Join(workDir, "x.md"))
	res := exec.ExecuteWithConfirm(context.Background(), Call{ID: "c", Name: "read_file", Input: input})
	if res.Proceed {
		t.Fatal("deny 必须压过 allow（对齐业内既有实现：deny 绝对生效），实际被放行")
	}
	t.Logf("deny+allow 同时存在 -> %s", res.Rejection)
}

// 端到端复现用户 2026-09-13 23:26 的观察：在**只读子代理**（explore）里，
// read_file 被 deny 规则挡住，但子代理可改用 grep_search 拿到同一文件的内容。
// 子代理的只读工具集（agent.go readOnlyToolSet）里 grep_search 是允许项，
// 而 deny 规则只写了 read_file —— 于是"禁止读文件"的意图被子代理绕开。
func TestDenyRule_SubAgentExplore_BypassesViaGrepSearch(t *testing.T) {
	workDir := t.TempDir()
	const marker = "TOP_SECRET_MARKER_9f3a"
	if err := os.WriteFile(filepath.Join(workDir, "SECRET.md"), []byte("# "+marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["bash(rm:*)","read_file(*)"]},"priorityMode":"posix"}`)

	agent := NewAgentContext(exec, "总结被保护文件的内容", "读取受保护文件", workDir, 1)
	agent.AgentType = AgentTypeExplore // 只读子代理（与用户 23:26 用例一致）
	ctx := context.Background()

	// 1) 子代理用 read_file → 被 deny 规则拦住（子代理会改为找替代工具）
	readInput, _ := json.Marshal(map[string]any{"path": filepath.Join(workDir, "SECRET.md"), "limit": 1})
	if _, err := agent.executeToolCall(ctx, "read_file", readInput); err == nil {
		t.Fatal("前提失效：子代理的 read_file 未被拦住")
	} else {
		t.Logf("explore agent read_file -> 被拦: %v", err)
	}

	// 2) 子代理改用 grep_search → 成功拿到被保护文件内容
	grepInput, _ := json.Marshal(map[string]any{
		"pattern": marker, "path": workDir, "output_mode": "content",
	})
	out, err := agent.executeToolCall(ctx, "grep_search", grepInput)
	if err != nil {
		t.Fatalf("grep_search 应能绕过 read_file 的 deny 规则，实际 err=%v", err)
	}
	if !strings.Contains(out, marker) {
		t.Fatalf("grep_search 未返回被保护内容，缺口不成立；out=%q", out)
	}
	t.Logf("explore agent grep_search -> 绕过成功，输出含被保护内容=true")
}

// ---------------------------------------------------------------------------
// 2026-09-14 修复（用户拍板 ②「deny 扩到能力级」）：`Read(*)` 家族别名。
//
// 上面两个用例固定的是**修复前**的缺口（单工具名规则挡不住同能力工具）。
// 下面用同一个生产入口，断言换用 `Read(*)` 后缺口闭合，且**不越界**。
// 判别力：把 capability.go 的 toolCapabilities 里 read 家族的条目摘掉，本组会立刻变红。
//
// ★ 续修（2026-10-06 用户拍板 ①）：能力别名此前只按**静态工具名**判成员，
// 而 `bash` 静态归 Exec ⇒ 永不入读家族 ⇒ `Read(*)` 管不住 `bash cat SECRET.md`。
// 现补一层**按实际效果**的成员判定（命令可判定为全只读 ⇒ 计入读家族），
// 仍**不改**静态分桶。判别力：摘掉 capability.go 的 `capabilityCoversCall`
// 效果分支，TestW025_* 立刻变红。
// ---------------------------------------------------------------------------

// TestReadCapabilityFamily_ClosesReadBypass 生产路径：Read(*) 同时拦住读取侧。
func TestReadCapabilityFamily_ClosesReadBypass(t *testing.T) {
	workDir := t.TempDir()
	secret := filepath.Join(workDir, "SECRET.md")
	const marker = "TOP_SECRET_MARKER_9f3a"
	if err := os.WriteFile(secret, []byte("# "+marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["Read(*)"]},"priorityMode":"posix"}`)
	ctx := context.Background()

	// 读取侧：三个工具都必须被同一条规则挡住（含修复前可静默绕过的 grep_search）。
	readCalls := []struct {
		name  string
		input string
	}{
		{"read_file", fmt.Sprintf(`{"path":%q,"limit":1}`, secret)},
		{"grep_search", fmt.Sprintf(`{"pattern":%q,"path":%q,"output_mode":"content"}`, marker, workDir)},
		{"glob_search", fmt.Sprintf(`{"pattern":"**/*.md","path":%q}`, workDir)},
	}
	for i, c := range readCalls {
		res := exec.ExecuteWithConfirm(ctx, Call{ID: fmt.Sprintf("r-%d", i), Name: c.name, Input: c.input})
		if res.Proceed {
			t.Errorf("Read(*) 应拦住 %s，实际被放行", c.name)
			continue
		}
		if res.NeedConfirm {
			t.Errorf("%s 应被规则前置裁决直接拒绝（不再弹确认框），实际 NeedConfirm=true", c.name)
		}
		if !strings.Contains(res.Rejection, "deny rule") {
			t.Errorf("%s 的拒绝原因应来自 deny 规则，实际 %q", c.name, res.Rejection)
		}
		t.Logf("%-12s -> 被 Read(*) 拦住: %s", c.name, res.Rejection)
	}
}

// TestReadCapabilityFamily_DoesNotBlockNonRead 边界（防过度扩张）：
// 同一条 Read(*) 不得把**写入侧**当作读取拦下 —— 那会让规则产生与"读"无关的副作用。
//
// ★ 变更：本用例原先把 `bash cat SECRET.md` 列为"不得被拦"的样本
// （彼时 bash 不在读家族，"人工闸门"承接）。用户拍板 ① 后，**全只读**的 bash 调用
// **计入**读家族 ⇒ bash 拆成两条独立用例（只读应拦 / 写与混淆读不得拦），
// 见下方 TestW025_ReadCapability_*。
func TestReadCapabilityFamily_DoesNotBlockNonRead(t *testing.T) {
	workDir := t.TempDir()
	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["Read(*)"],"allow":["write_file(*)"]},"priorityMode":"posix"}`)

	target := filepath.Join(workDir, "out.md")
	res := exec.ExecuteWithConfirm(context.Background(), Call{
		ID: "w-1", Name: "write_file",
		Input: fmt.Sprintf(`{"path":%q,"content":"hi"}`, target),
	})
	if strings.Contains(res.Rejection, "deny rule") {
		t.Fatalf("write_file 不应被 Read(*) 拦下（能力边界越界），实际 %q", res.Rejection)
	}
	t.Logf("write_file under Read(*) -> Proceed=%v NeedConfirm=%v Rejection=%q",
		res.Proceed, res.NeedConfirm, res.Rejection)
}

// TestW025_ReadCapability_CoversReadOnlyBash 修复目标（生产入口）：
// `Read(*)` 现在**覆盖**「全只读」的 bash 调用 ⇒ 从"换条路静默读"变"规则前置拒绝"。
func TestW025_ReadCapability_CoversReadOnlyBash(t *testing.T) {
	workDir := t.TempDir()
	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["Read(*)"]},"priorityMode":"posix"}`)

	cat := exec.ExecuteWithConfirm(context.Background(), Call{
		ID: "b-1", Name: "bash", Input: `{"command":"cat SECRET.md"}`,
	})
	if cat.Proceed {
		t.Fatalf("Read(*) 应拦住「全只读」的 bash（cat SECRET.md），实际被放行")
	}
	if cat.NeedConfirm {
		t.Errorf("应被规则**前置裁决**直接拒绝（不再弹确认框），实际 NeedConfirm=true")
	}
	if !strings.Contains(cat.Rejection, "deny rule") {
		t.Errorf("拒绝原因应来自 deny 规则，实际 %q", cat.Rejection)
	}
	t.Logf("bash cat under Read(*) -> %s", cat.Rejection)
}

// TestW025_ReadCapability_DoesNotBlockWriteOrObfuscatedBash 判别锚（防过度扩张）：
// 同一 deny 配置下，**不能被判定为全只读**的 bash 调用不得被当作读拒绝 ——
// 它们落回 Exec 语义（照常走确认/沙箱）。含用户点名的"已知残余"（混淆读）。
func TestW025_ReadCapability_DoesNotBlockWriteOrObfuscatedBash(t *testing.T) {
	workDir := t.TempDir()
	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["Read(*)"]},"priorityMode":"posix"}`)

	cases := []struct {
		id     string
		cmd    string
		reason string
	}{
		{"b-write", fmt.Sprintf("mkdir -p %s", filepath.Join(workDir, "d")), "写命令"},
		{"b-redir", fmt.Sprintf("cat x > %s", filepath.Join(workDir, "o")), "读+重定向写"},
		{"b-obfus", `sh -c 'cat SECRET.md'`, "混淆读（fail-closed 残余）"},
	}
	for _, c := range cases {
		in, _ := json.Marshal(map[string]any{"command": c.cmd})
		res := exec.ExecuteWithConfirm(context.Background(), Call{ID: c.id, Name: "bash", Input: string(in)})
		if strings.Contains(res.Rejection, "deny rule") {
			t.Errorf("%s 不应被 Read(*) 拒绝（它不能被判定为全只读），实际 %q", c.reason, res.Rejection)
		}
		t.Logf("%-8s (%-22s) -> Proceed=%v NeedConfirm=%v", c.id, c.reason, res.Proceed, res.NeedConfirm)
	}
}

// TestReadCapabilityFamily_SubAgentCannotBypass 端到端：
// 只读子代理里，Read(*) 让"换工具读"这条绕过路径也失效。
func TestReadCapabilityFamily_SubAgentCannotBypass(t *testing.T) {
	workDir := t.TempDir()
	const marker = "TOP_SECRET_MARKER_9f3a"
	if err := os.WriteFile(filepath.Join(workDir, "SECRET.md"), []byte("# "+marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["Read(*)"]},"priorityMode":"posix"}`)

	agent := NewAgentContext(exec, "总结被保护文件的内容", "读取受保护文件", workDir, 1)
	agent.AgentType = AgentTypeExplore
	ctx := context.Background()

	readInput, _ := json.Marshal(map[string]any{"path": filepath.Join(workDir, "SECRET.md"), "limit": 1})
	if _, err := agent.executeToolCall(ctx, "read_file", readInput); err == nil {
		t.Error("子代理 read_file 应被 Read(*) 拦住")
	}
	grepInput, _ := json.Marshal(map[string]any{"pattern": marker, "path": workDir, "output_mode": "content"})
	if out, err := agent.executeToolCall(ctx, "grep_search", grepInput); err == nil {
		t.Errorf("子代理 grep_search 也应被 Read(*) 拦住（绕过路径必须闭合），实际成功: %q", out)
	} else {
		t.Logf("explore agent grep_search -> 已拦住: %v", err)
	}
}
