package permission

// read_effect_view_test.go — （2026-10-06 用户拍板 ①）：
// 让 deny 的**匹配面**识别「只读效果」。
//
// 缺口：`Read(*)` 的成员判定此前只有「静态工具名 → 能力桶」一条路（capability.go），
// 而 `bash` 静态登记为 `Exec` ⇒ 永不入读家族 ⇒ `deny ["Read(*)"]` 管不住
// `bash cat SECRET.md`。 又把"bash 一律弹框"这个**偶然闸门**拆掉（只读即免问）
// ⇒ 该绕过从"有人工闸门"变成"静默"，缺口变宽（总账）。
//
// ★ 根因定位：这是**匹配范围**问题，不是优先级问题 —— deny 的裁决点
// （pipeline.go 的 rule_gate）本就在所有免问出口之前，deny 一旦匹配上就天然压过它们。
// 所以修复不是"让 deny 更优先"，而是"让 deny **匹配得上**这一次调用"。
//
// 本文件钉住修复后的契约（三条，缺一即错）：
// 1. `Read(*)` **覆盖**「命令可判定为全只读」的命令载体调用（bash/monitor）；
// 2. `Read(*)` **不覆盖** 写 / 执行 / 混淆读（`sh -c 'cat X'`）—— 效果视图 fail-closed；
// 3. 效果视图**只作用于规则匹配面**：`toolCapabilities` 一字不改 ⇒
// `CapabilityToolsOf(CapRead)` 与只读子代理的派生工具面**不含**命令载体（防提权），
// 且 `Edit(*)` / `Exec(*)` 等**不**获得"按效果"判定（不做无根据扩张）。

import (
	"strconv"
	"testing"
)

func cmdInput(cmd string) string {
	return `{"command":` + strconv.Quote(cmd) + `}`
}

// TestW025_ReadDenyCoversReadOnlyBash 契约 1：效果视图把「全只读命令」接进读家族。
func TestW025_ReadDenyCoversReadOnlyBash(t *testing.T) {
	reads := []string{
		"cat SECRET.md",
		"ls -la",
		"head -20 README.md",
		"cat a && echo done",
		"cat a | head -5",
		"sleep 0 && echo final_check",
	}
	for _, cmd := range reads {
		if !familyHit(t, "Read(*)", "bash", cmdInput(cmd)) {
			t.Errorf("Read(*) 应覆盖「全只读」的 bash 调用 %q（ 修复目标），实际未覆盖", cmd)
		}
	}

	// monitor 同为命令载体（CapExec 派生，capability.go）—— 同一判据必须同样生效。
	if !familyHit(t, "Read(*)", "monitor", cmdInput("tail -n 5 log.txt")) {
		t.Error("Read(*) 应同样覆盖「全只读」的 monitor 调用（命令载体判据由 CapExec 派生，不分工具）")
	}
}

// TestW025_ReadDenyDoesNotCoverNonReadBash 契约 2：不可判定为只读的一律落回 Exec。
func TestW025_ReadDenyDoesNotCoverNonReadBash(t *testing.T) {
	nonReads := []string{
		"mkdir -p x",            // 写
		"rm -f x",               // 删
		"echo hi > /tmp/f",      // 重定向写
		"cat a > /tmp/f",        // 读 + 重定向写
		"cat a && mkdir -p b",   // 复合：有一段是写
		"sh -c 'cat SECRET.md'", // 混淆读：命令名不在白名单 ⇒ fail-closed（已知残余，见回执）
		"xargs cat",             // wrapper 不剥 ⇒ fail-closed
		"sudo cat SECRET.md",    // wrapper 不剥 ⇒ fail-closed
	}
	for _, cmd := range nonReads {
		if familyHit(t, "Read(*)", "bash", cmdInput(cmd)) {
			t.Errorf("Read(*) 不得覆盖 %q：它不能被判定为「全只读」，按 fail-closed 必须落回 Exec 语义", cmd)
		}
	}
}

// TestW025_EffectViewDoesNotTouchCapabilityBuckets 契约 3（**防提权**，本修复最重要的护栏）。
//
// 读家族还被 tui/agent_guard.go 用来定义**只读子代理的工具面**。
// 若图省事把 bash 塞进 toolCapabilities 的 CapRead 桶，只读角色就会**拿到 bash**
// —— 那是提权，比原来的缺口更糟。本用例把这条退化形态钉死。
func TestW025_EffectViewDoesNotTouchCapabilityBuckets(t *testing.T) {
	for _, tool := range CapabilityToolsOf(CapRead) {
		if IsCommandBearingTool(tool) {
			t.Fatalf("读能力桶不得含命令载体 %q —— 效果视图只应作用于规则匹配面。"+
				"进了桶 ⇒ 只读子代理的工具面会拿到命令执行能力 = 提权（比原缺口更糟）", tool)
		}
	}
	caps := CapabilitiesOf("bash")
	if len(caps) != 1 || caps[0] != CapExec {
		t.Fatalf("bash 的静态能力桶必须仍是 [Exec]（效果视图不得改静态分桶），实际 %v", caps)
	}
}

// TestW025_EffectViewIsReadOnly 契约 3 续：效果视图只对 CapRead 生效，不向外扩张。
func TestW025_EffectViewIsReadOnly(t *testing.T) {
	// 只读 bash 不得被 Edit(*) 命中 —— 否则会凭空禁掉与"写"无关的调用。
	if familyHit(t, "Edit(*)", "bash", cmdInput("cat SECRET.md")) {
		t.Error("Edit(*) 不得因「命令只读」而命中 bash：效果视图只对 CapRead 生效")
	}
	// Exec(*) 走静态桶，本就命中 bash —— 效果视图不得改变这一结果（有/无只读都命中）。
	if !familyHit(t, "Exec(*)", "bash", cmdInput("cat SECRET.md")) {
		t.Error("Exec(*) 必须命中 bash（静态桶），效果视图不得使其丢失")
	}
	if !familyHit(t, "Exec(*)", "bash", cmdInput("mkdir -p x")) {
		t.Error("Exec(*) 必须命中 bash（静态桶），与命令是否只读无关")
	}
}

// TestW025_NegationFollowsEffectView 取反语义须与新视图一致（否则 !(Read(*)) 会自相矛盾）。
func TestW025_NegationFollowsEffectView(t *testing.T) {
	if familyHit(t, "!(Read(*))", "bash", cmdInput("cat SECRET.md")) {
		t.Error("!(Read(*)) 不应命中「全只读」的 bash —— 它现在计入读家族")
	}
	if !familyHit(t, "!(Read(*))", "bash", cmdInput("mkdir -p x")) {
		t.Error("!(Read(*)) 应命中非只读 bash —— 它不在读家族内")
	}
}
