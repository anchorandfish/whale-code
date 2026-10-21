package tui

import "testing"

// ============================================================================
// 请求内审批复用 · 作用域与键
// ============================================================================
//
// 判据的生产实现（`approvalReuseKey` / `requestsApprovalReuse` / `noteRequestApproval`）
// 在 tool_exec.go。管线**行为面**的判别器在 internal/tools/approval_reuse_test.go
// （生产入口 `InitPipelineWithHooks` + `ExecuteWithConfirm`）。
//
// 本文件补的是管线层测不到的那一半：**作用域边界**与**键**。
// 管线只收到一个 bool（ReusedApproval），无从知道"这条批准属于哪次请求" ——
// 那次判断完全发生在 TUI 的这一层，所以必须在这里单独钉住。

const (
	arName  = "bash"
	arInput = `{"command":"ls -la"}`
)

// newApprovalReuseModel 经**生产构造器**取一个 Model。
//
// 必须走 NewModel：复用正确性**依赖**它在 lifecycle.go:354 里对 reqApprovals 的
// make —— 见 TestGuard_ApprovalReuse_ProductionModelInitializesTable。
func newApprovalReuseModel(t *testing.T) Model {
	t.Helper()
	return NewModel(newTestConfig(), newTestClient("test-model"), newTestRegistry())
}

// ---------------------------------------------------------------------------
// 主判别器：作用域 = 一次用户请求
// ---------------------------------------------------------------------------

// TestFix_ApprovalReuse_RequestScopeIsTheBoundary 钉住用户 2026-09-20 拍板的作用域：
// **一次用户请求内**（不是一条消息、也不是整个会话）。
//
// 用户原话的口径取舍：
// - 比「仅同一条消息」宽 —— 否则修不了实测形态 ②（模型跨两条消息又发一次同样的 `ls -la`，
// 但仍在**同一个**用户请求内）；
// - 比「整个会话免问」窄 —— 下一次用户请求必须重新问。
func TestFix_ApprovalReuse_RequestScopeIsTheBoundary(t *testing.T) {
	m := newApprovalReuseModel(t)

	const req1 = "1789871930921108000"
	const req2 = "1789871930921108999" // 下一次用户请求（新的 RequestID）

	m.noteRequestApproval(arName, arInput, req1)

	// 每个用例**显式**给出被查询的 (工具名, 入参, 请求 id) —— 上一版把入参写死在
	// 循环外，"参数不同"那格其实查了同一把键（测试自身的 bug，已由本表钉住）。
	cases := []struct {
		name  string
		qTool string
		qIn   string
		qID   string
		want  bool
		why   string
	}{
		{
			name: "同请求·同工具同参数", qTool: arName, qIn: arInput, qID: req1, want: true,
			why: "这正是要修的缺陷：同请求内逐字相同 ⇒ 复用批准、不再问第二次",
		},
		{
			name: "★新请求·同工具同参数", qTool: arName, qIn: arInput, qID: req2, want: false,
			why: "作用域边界：下一次用户请求必须重新问（不是整会话免问）",
		},
		{
			name: "同请求·参数不同", qTool: arName, qIn: `{"command":"ls -la "}`, qID: req1, want: false,
			why: "只复用**逐字相同**的调用 —— 参数不同就是另一次副作用",
		},
		{
			name: "同请求·工具不同", qTool: "write_file", qIn: arInput, qID: req1, want: false,
			why: "工具名必须参与判据：换个工具、入参逐字相同也不得复用",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := m.requestsApprovalReuse(c.qTool, c.qIn, c.qID); got != c.want {
				t.Fatalf("requestsApprovalReuse(%q, %q, %q) = %v, want %v（%s）",
					c.qTool, c.qIn, c.qID, got, c.want, c.why)
			}
		})
	}
}

// TestGuard_ApprovalReuse_EmptyRequestIDFailsClosed 钉住 fail-closed 分支：
// RequestID 为空（Plain / Bridge 适配器恒返回 ""）时**恒不复用**。
//
// 为什么重要：那种入口没有"用户请求"这个边界 ⇒ 若按"空 id 也匹配"处理，
// 复用会退化成**跨请求免问**。宁多问，不可漏问。
func TestGuard_ApprovalReuse_EmptyRequestIDFailsClosed(t *testing.T) {
	m := newApprovalReuseModel(t)

	m.noteRequestApproval(arName, arInput, "")
	if m.requestsApprovalReuse(arName, arInput, "") {
		t.Fatal("RequestID 为空时必须恒不复用（该入口没有「用户请求」边界，不得跨请求免问）")
	}

	// 反向：即便表里已有一条**非空**请求的批准，空 id 查询也不得命中。
	m.noteRequestApproval(arName, arInput, "req-real")
	if m.requestsApprovalReuse(arName, arInput, "") {
		t.Fatal("空 RequestID 不得命中任何已有批准")
	}
}

// TestGuard_ApprovalReuse_NewRequestPurgesStaleEntries 钉住内存卫生：
// 在新请求下登记批准时，属于旧请求的条目被清掉。
//
// 注意：**判定本身不依赖清理**（requestsApprovalReuse 按 reqID 比对，过期条目天然不复用）。
// 本用例钉的是"别无限累积"，以及清掉后确实不再命中。
func TestGuard_ApprovalReuse_NewRequestPurgesStaleEntries(t *testing.T) {
	m := newApprovalReuseModel(t)

	m.noteRequestApproval(arName, arInput, "req-1")
	m.noteRequestApproval("write_file", `{"path":"a.go"}`, "req-1")
	if len(m.reqApprovals) != 2 {
		t.Fatalf("前置不成立：同请求内应累积 2 条，实际 %d 条", len(m.reqApprovals))
	}

	m.noteRequestApproval(arName, arInput, "req-2") // 新请求 ⇒ 清旧
	if len(m.reqApprovals) != 1 {
		t.Fatalf("新请求登记时应清掉旧请求的条目，实际剩 %d 条: %+v", len(m.reqApprovals), m.reqApprovals)
	}
	if m.requestsApprovalReuse("write_file", `{"path":"a.go"}`, "req-1") {
		t.Fatal("旧请求的条目已被清理，不得再命中")
	}
	if !m.requestsApprovalReuse(arName, arInput, "req-2") {
		t.Fatal("新请求登记的那条必须命中")
	}
}

// TestGuard_ApprovalReuse_KeyIsUnambiguous 钉住键的**无歧义性**：
// ("a","bc") 与 ("ab","c") 不得拼成同一个键。
//
// 这条不是吹毛求疵：模型完全可能发出 `bash` + `{"command":"ls"}` 与
// `bash{"command":"ls"}` 这样的组合；朴素拼接会静默把它们当成同一次调用 ⇒
// 对**不同**的副作用复用同一次批准。
func TestGuard_ApprovalReuse_KeyIsUnambiguous(t *testing.T) {
	if approvalReuseKey("a", "bc") == approvalReuseKey("ab", "c") {
		t.Fatal("键必须无歧义：(\"a\",\"bc\") 与 (\"ab\",\"c\") 不得碰撞（应含分隔符）")
	}

	m := newApprovalReuseModel(t)
	m.noteRequestApproval("a", "bc", "req-1")
	if m.requestsApprovalReuse("ab", "c", "req-1") {
		t.Fatal("拼接碰撞会让「另一个工具的另一次调用」复用本条批准")
	}
}

// ---------------------------------------------------------------------------
// 结构前提：字段形态与构造器接线
// ---------------------------------------------------------------------------

// TestGuard_ApprovalReuse_SurvivesModelValueCopy 钉住本字段**必须是引用类型**。
//
// 为什么这是一个真前提而不是实现细节：Model 在 bubbletea 下**按值复制**
// （见 app.go:636 的警告 —— 值字段的写入会落在"调用它的那份副本"上，
// 静默失效且完全不可见）。若有人把这个 map "简化"成 `reqApprovalsID string`，
// 复用会在 Update 的下一份副本里静默失效，且**不报错** —— 正是最难查的那类回归。
//
// 判别法：先按值复制，再对原副本写，然后从**副本**读。
func TestGuard_ApprovalReuse_SurvivesModelValueCopy(t *testing.T) {
	base := newApprovalReuseModel(t)
	if base.reqApprovals == nil {
		t.Fatal("前置不成立：生产构造器必须已 make（否则本用例的复制语义无意义）")
	}

	cp := base // ★ 按值复制 —— 模拟 bubbletea Update 的语义
	base.noteRequestApproval(arName, arInput, "req-1")

	if !cp.requestsApprovalReuse(arName, arInput, "req-1") {
		t.Fatal("内容修改必须跨 Model 副本可见（引用类型语义）——" +
			"若改成值字段，复用会在下一份副本里静默失效且不报错")
	}
}

// TestGuard_ApprovalReuse_ProductionModelInitializesTable 钉住**构造器接线**：
// NewModel 必须 make 这个表。
//
// 为什么不能靠"用到时懒初始化"：Model 按值返回（lifecycle.go:123 `func NewModel(...) Model`），
// 此后每份副本共享的是**同一个 map 实例**；但懒初始化执行的是
// `m.reqApprovals = make(...)` —— 那是一次**值字段写入**，只落在当前那份副本上，
// 别的副本仍然是 nil ⇒ 静默失效。⇒ make 必须发生在"只有一份"的时候，即构造器里。
func TestGuard_ApprovalReuse_ProductionModelInitializesTable(t *testing.T) {
	m := newApprovalReuseModel(t)
	if m.reqApprovals == nil {
		t.Fatal("NewModel 必须初始化 reqApprovals（懒初始化在按值复制的 Model 上不成立）")
	}
	// 空表 ⇒ 一律不复用（不是"空表全放行"）。
	if m.requestsApprovalReuse(arName, arInput, "req-1") {
		t.Fatal("空表不得命中任何复用")
	}
}
