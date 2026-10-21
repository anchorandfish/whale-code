package permission

// security_preset_test.go — G13 预设层的**结构性**判据（I2 命名空间 + I4 双轴合取）。
//
// 这两个不变量都是"结构性"的，适合在 permission 包内直接判（不依赖 UI）：
//
//	I2：预设 id 必须与单轴档名（LookupModeName 认得的 token）不相交。
//	I4：MatchSecurityPreset 必须对两轴取合取，故两轴 6 态里只有 3 态有预设。
//
// # 判别性
//
//   - I2 守卫：把任一预设 id 改成 "workspace-write" ⇒ 必红。
//   - I4 守卫：把 MatchSecurityPreset 改成"只比 Level" ⇒ 必红
//     （danger-full-access + 沙箱 ON 会被误判成 unrestricted）。
//   - 覆盖守卫：预设表若被扩到覆盖全部 6 态，则 Custom 态不存在 ⇒ 必红
//     （这条是 I4 的"非空谈"证据：它证明混合态真实存在）。

import "testing"

// TestSecurityPresetIDsDoNotCollideWithModeNames 是 I2 的机器校验。
//
// 单轴 token 的唯一真相源是 LookupModeName。若预设 id 落在它的值域里，
// 同一个词在两个入口就指向不同事物：
//
//	/permissions workspace-write     → 只设审批档
//	/security-preset workspace-write → 两轴一起
//
// 这比"名字只说一轴却写两轴"更难排查 —— 至少后者 token 是不同的。
// 同类 CLI 用 `:` 前缀把两层命名空间强行错开
// （protocol/src/models.rs:396,399,402 vs approval-presets/src/lib.rs:31,41,51）。
func TestSecurityPresetIDsDoNotCollideWithModeNames(t *testing.T) {
	presets := BuiltinSecurityPresets()
	if len(presets) == 0 {
		t.Fatal("前置失败：预设表为空 —— 本守卫会静默恒绿")
	}
	for _, p := range presets {
		if _, known := LookupModeName(p.ID); known {
			t.Errorf("I2 违规：预设 id %q 与单轴档名同名（LookupModeName 认得它）。"+
				"于是 /permissions %s（只改审批轴）与 /security-preset %s（改两轴）"+
				"指向不同事物。请给预设另起一个不与档名碰撞的词。", p.ID, p.ID, p.ID)
		}
		if p.ID == "" {
			t.Error("I2 违规：预设 id 为空")
		}
	}
}

// TestMatchSecurityPresetRequiresBothAxes 是 I4 的核心判据。
//
// `danger-full-access + 沙箱 ON` 这个组合**不属于任何预设**：
// unrestricted 要求审批放开**且**沙箱关闭。若只比审批档，它会被判成
// unrestricted —— 用户看到 "Current: unrestricted" 而沙箱其实开着，
// 预设名开始骗人（F2 那类"报告与事实不符"换到预设层复发）。
func TestMatchSecurityPresetRequiresBothAxes(t *testing.T) {
	// ① 混合态必须**不匹配**（→ 上层显示 Custom）。
	if p, ok := MatchSecurityPreset(LevelDangerFullAccess, true); ok {
		t.Errorf("I4 违规：danger-full-access + 沙箱 ON 被判成了预设 %q —— "+
			"这个组合两轴不一致（沙箱其实开着）。必须返回 ok=false 让调用方显示 Custom。", p.ID)
	}
	if p, ok := MatchSecurityPreset(LevelReadOnly, false); ok {
		t.Errorf("I4 违规：read-only + 沙箱 OFF 被判成了预设 %q（沙箱其实关着）", p.ID)
	}
	if p, ok := MatchSecurityPreset(LevelWorkspaceWrite, false); ok {
		t.Errorf("I4 违规：workspace-write + 沙箱 OFF 被判成了预设 %q（沙箱其实关着）", p.ID)
	}

	// ② 反向守卫：**真属于**预设的组合不得被判成 Custom（防"一律返回 false"式修复）。
	for _, want := range BuiltinSecurityPresets() {
		got, ok := MatchSecurityPreset(want.Level, want.SandboxOn)
		if !ok || got.ID != want.ID {
			t.Errorf("反向守卫：%s（approval=%s sandbox=%v）应匹配到预设 %q，got (%q, %v)",
				want.ID, want.Level, want.SandboxOn, want.ID, got.ID, ok)
		}
	}
}

// TestPresetsCoverOnlyHalfOfTheStateSpace 证明 I4 不是空谈：
// 两轴 3 档 × 2 态 = 6 个状态，而预设只有 3 条 ⇒ 必然存在 3 个 Custom 态。
//
// 这条同时是**反过度设计**守卫：若有人把预设扩到覆盖全部 6 态，
// Custom 分支就成了死代码，I4 也随之失效 —— 那时应该删掉 I4 而不是留着假守卫。
func TestPresetsCoverOnlyHalfOfTheStateSpace(t *testing.T) {
	levels := []Level{LevelReadOnly, LevelWorkspaceWrite, LevelDangerFullAccess}
	covered, uncovered := 0, 0
	for _, l := range levels {
		for _, on := range []bool{true, false} {
			if _, ok := MatchSecurityPreset(l, on); ok {
				covered++
			} else {
				uncovered++
			}
		}
	}
	if covered != 3 || uncovered != 3 {
		t.Errorf("两轴共 6 态，预设应覆盖 3 态、留 3 态给 Custom；"+
			"实际 covered=%d uncovered=%d —— 若有意改变覆盖，请同步更新 I4 与预设层的 Custom 渲染",
			covered, uncovered)
	}
}

// TestBuiltinSecurityPresetsCoverTheStartupDefault 是**接线事实**守卫：
// 启动装配（`internal/tui/lifecycle.go:511` 开 legacy 门 + `:520` 装 Manager，
// 默认审批档 workspace-write）恰好落在 `standard` 上。
//
// 若不成立，用户一进来看到的 "Current" 就是 Custom —— 那说明预设表与启动默认
// 脱节了（而启动路径**不是**预设写的，两者只能靠这条断言对齐）。
func TestBuiltinSecurityPresetsCoverTheStartupDefault(t *testing.T) {
	startupLevel := LevelWorkspaceWrite // config.PermWorkspaceWrite（config/config.go:26）
	if p, ok := MatchSecurityPreset(startupLevel, true); !ok {
		t.Errorf("启动默认（workspace-write + 沙箱 ON）必须落在某个预设上，否则用户开局就是 Custom")
	} else if p.ID != "standard" {
		t.Errorf("启动默认应落在 standard，实际 %q", p.ID)
	}
}

// TestLookupSecurityPresetRejectsUnknown 覆盖 I2 的**用户可见后果**：
// 拿单轴档名当预设名必须被拒（否则 `/security-preset read-only` 会被误解为
// "把审批档设成 read-only"，实际它会写两轴）。
func TestLookupSecurityPresetRejectsUnknown(t *testing.T) {
	for _, name := range []string{"read-only", "workspace-write", "danger-full-access", "auto", "plan", "nope", ""} {
		if p, ok := LookupSecurityPreset(name); ok {
			t.Errorf("I2：%q 不得是合法预设 id（got %q）", name, p.ID)
		}
	}
	// 反向守卫：真正的预设 id 必须能查到。
	for _, want := range BuiltinSecurityPresets() {
		got, ok := LookupSecurityPreset(want.ID)
		if !ok || got.ID != want.ID {
			t.Errorf("预设 %q 应能查到，got (%q, %v)", want.ID, got.ID, ok)
		}
	}
}
