package permission

import (
	"os"
	"path/filepath"
	"testing"
)

// S9 Fix 目标测试（HG-A2 / G5）：命令类工具的记忆键按「危险语义等价作用域」归一化。
//
// 改动前：整段 input 作键 → 语义相同、文本不同的只读调用不命中（去重失效）。
// 改动后：只读类简单命令按可执行名合并 → 去重生效。
//
// 该文件在**未打补丁的磁盘源码**上应 FAIL（RED），打补丁后 PASS（GREEN）。
func TestFix_S9_ReadOnlyCommandDedupedByScope(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	p.Remember("bash", `{"command":"ls -la"}`, true)

	if !p.Has("bash", `{"command":"ls -la /tmp"}`) {
		t.Fatal("EXPECT: 同只读作用域（ls）应去重命中（HG-A2）; GOT: 未命中")
	}
	// 作用域身份：不同可执行名不得共享。
	if p.Has("bash", `{"command":"cat README.md"}`) {
		t.Fatal("GOT: 不同只读可执行名被合并; EXPECT: 不共享（scope=ls vs cat）")
	}
	// 非只读类不得被只读授权覆盖。
	if p.Has("bash", `{"command":"go build ./..."}`) {
		t.Fatal("GOT: 非只读命令被只读授权覆盖; EXPECT: 不命中")
	}
}

// S9 Fix 目标测试（HG-A2 / fail-closed）：归一化绝不把不同危险度命令归为一类。
func TestFix_S9_ScopeNeverMergesDangerous(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	p.Remember("bash", `{"command":"ls -la"}`, true) // 只读授权

	notCovered := []string{
		`{"command":"rm -rf /tmp/x"}`,        // destructive
		`{"command":"rm /tmp/a"}`,            // destructive
		`{"command":"find / -delete"}`,       // 名字只读、参数破坏（白名单外 + 开关守卫）
		`{"command":"ls -la && rm /tmp/x"}`,  // 组合命令
		`{"command":"ls -la > /etc/passwd"}`, // 重定向
		`{"command":"echo hi | tee f"}`,      // 管道
		`{"command":"/bin/su"}`,              // 白名单外
	}
	for _, in := range notCovered {
		if p.Has("bash", in) {
			t.Fatalf("GOT: 危险/组合命令被只读授权放行 → %s; EXPECT: 绝不合并（fail-closed）", in)
		}
	}
}

// S9 Fix 目标测试（HG-A2）：不同目标/参数的**危险**命令之间不得合并。
func TestFix_S9_DangerousCommandsNotCollapsed(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)

	p.Remember("bash", `{"command":"rm /tmp/a"}`, true)
	if p.Has("bash", `{"command":"rm /tmp/b"}`) {
		t.Fatal("GOT: 不同目标的 rm 被合并; EXPECT: 目标不同 → 不命中")
	}

	// 白名单外的只读名也不合并（find 的参数可破坏：-delete/-exec）。
	p.Remember("bash", `{"command":"find / -name a"}`, true)
	if p.Has("bash", `{"command":"find / -name b"}`) {
		t.Fatal("GOT: find 被按作用域合并; EXPECT: find 在白名单外 → 不合并")
	}
}

// S9 Fix 目标测试（delta E · 严格会话级）：审批记忆**不落盘** → 不跨重启存活。
//
// ⚠️ 本用例相对 Round2 的 G1 是**有意翻转**（不是回归）：
// G1 曾要求 `Remember` 落盘，使「允许本会话」跨重启存活（= 持久授权）。
// 2026-09-14 用户拍板「严格会话级」——落盘与选项文案「允许本会话」的字面语义不符 ⇒ 撤销。
// 若有人把 `Remember` 里的 `SaveMemory` 接回去，**本用例必须变红**（这就是它的判别力）。
//
// 与 TestChar_S9_SessionMemoryNotPersisted 的分工（两条互补，缺一不可）：
// 一是那条复刻"生产没调用 SaveMemory"的**现状**，**不经过审批入口**；
// 二是本条**从 Remember 进**，证明"即使走了审批入口，也不产生持久化副作用"。
func TestFix_S9_ApprovalDoesNotSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.json")

	p1 := NewPolicy(LevelWorkspaceWrite)
	p1.SetMemoryStorePath(path)
	p1.Remember("bash", `{"command":"ls -la"}`, true)

	// ① 直接证据：审批入口不得产生任何落盘副作用。
	if _, err := os.Stat(path); err == nil {
		t.Fatal("GOT: Remember 落盘了; EXPECT: 严格会话级 → 不得产生 permissions.json")
	}

	// ② 端到端证据：新 Policy（= 重启）无论如何都拿不到这条授权。
	p2 := NewPolicy(LevelWorkspaceWrite)
	p2.SetMemoryStorePath(path)
	_ = p2.LoadMemory()
	if p2.Has("bash", `{"command":"ls -la"}`) {
		t.Fatal("GOT: 记忆跨重启存活; EXPECT: 严格会话级 → 重启后为空")
	}
}

// S9 遗留不变量（delta E / I6）：**状态**仍是进程内的 —— 同一进程内授权必须仍然生效。
//
// 这条防的是"为了不落盘而把 Remember 整个做成 no-op"这种过度修正：
// 严格会话级的定义是「**不跨重启**」，不是「不生效」。
func TestFix_S9_ApprovalStillEffectiveInProcess(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	p.Remember("bash", `{"command":"ls -la"}`, true)

	if !p.Has("bash", `{"command":"ls -la"}`) {
		t.Fatal("GOT: 进程内也不生效; EXPECT: 严格会话级 = 不跨重启，进程内仍必须命中")
	}
}

// S9 Fix 目标测试（I6）：存储文件不存在 → LoadMemory 视为空记忆，不报错。
//
// 注：delta E 后 LoadMemory 是**休眠 API**（生产不再调用），本用例保留为
// 对该 API 自身契约的钉死 —— 将来若真引入"永久允许"档而复用它，容错语义不能丢。
func TestFix_S9_LoadMemoryMissingFileIsNotError(t *testing.T) {
	dir := t.TempDir()
	p := NewPolicy(LevelWorkspaceWrite)
	p.SetMemoryStorePath(filepath.Join(dir, "permissions.json"))
	if err := p.LoadMemory(); err != nil {
		t.Fatalf("EXPECT: 文件不存在 → nil（不再每次冷启动告警）; GOT: %v", err)
	}
}
