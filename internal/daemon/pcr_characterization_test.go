// Package daemon — PCR 域 characterization 测试（快照步骤）。
//
// 本轮只钉"当前行为"，不改任何生产代码。每一条断言都必须对 whale-code 当前实现
// 为 GREEN，否则视为未钉住（违反 AUDIT_ROADMAP §5 规则 1）。
//
// 覆盖的不变量：I1（单一强制点 / 这里的"现状"基线）、I2（默认拒绝）、
// I5/I6（快照隔离，不别名内部切片）、I7（Status 不泄露密钥）。
//
// 命名前缀 PCR_ 与审计三件套（PCR_STATUS / PCR_HANDOFF / PCR_TEST_GUIDE）对应。
// 进入 Fix 阶段时，需要翻转的断言（如 metrics 空 token 直通）会刻意改为 FAIL
// 并补新测试，以证明生产真实路径行为已变。
package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// I2 · 默认拒绝：IPCServer 鉴权重置点（文件轮询传输）
// ---------------------------------------------------------------------------

func TestPCR_IPCAuthenticateRequest_FailClosed(t *testing.T) {
	// 空 token（初始化异常/被绕过）= 拒绝所有请求（fail-closed）。
	empty := &IPCServer{authToken: ""}
	if empty.authenticateRequest(JSONRPCRequest{Token: "whatever"}) {
		t.Fatal("空 authToken 应拒绝一切请求（I2 fail-closed）")
	}

	// 正常 token：匹配放行，不匹配拒绝。
	srv := &IPCServer{authToken: "abc"}
	if !srv.authenticateRequest(JSONRPCRequest{Token: "abc"}) {
		t.Fatal("正确 token 应放行")
	}
	if srv.authenticateRequest(JSONRPCRequest{Token: "wrong"}) {
		t.Fatal("错误 token 应拒绝")
	}
	if srv.authenticateRequest(JSONRPCRequest{Token: ""}) {
		t.Fatal("缺失 token 应拒绝")
	}
}

// ---------------------------------------------------------------------------
// I2 · 默认拒绝：UDSServer 鉴权重置点（Unix 域套接字传输）
// 注意：此实现与 IPCServer.authenticateRequest 是【独立】的第二份副本（I1 关切点）。
// ---------------------------------------------------------------------------

func TestPCR_UDSAuthenticate_FailClosed(t *testing.T) {
	empty := &UDSServer{cfg: UDSConfig{AuthToken: ""}}
	if empty.authenticate(JSONRPCRequest{Token: "whatever"}) {
		t.Fatal("空 AuthToken 应拒绝一切请求（I2 fail-closed）")
	}

	srv := &UDSServer{cfg: UDSConfig{AuthToken: "abc"}}
	if !srv.authenticate(JSONRPCRequest{Token: "abc"}) {
		t.Fatal("正确 token 应放行")
	}
	if srv.authenticate(JSONRPCRequest{Token: "wrong"}) {
		t.Fatal("错误 token 应拒绝")
	}
}

// ---------------------------------------------------------------------------
// I2 · 默认拒绝：dispatch 未知方法必须报错，而非放行（fail-closed 分支）
// ---------------------------------------------------------------------------

func TestPCR_HandleRequest_UnknownMethodDenied(t *testing.T) {
	ipc := &IPCServer{daemon: &Daemon{}}
	resp := ipc.handleRequest(JSONRPCRequest{Method: "totally_unknown_method", ID: 7})
	if resp.Error == nil {
		t.Fatal("未知方法应返回 error（I2 fail-closed），实际放行")
	}
	if resp.Error.Code != -32601 {
		t.Fatalf("未知方法错误码应为 -32601，实际 %d", resp.Error.Code)
	}
	if resp.Result != nil {
		t.Fatal("未知方法不应返回 Result")
	}
}

// ---------------------------------------------------------------------------
// I5/I6 · 快照隔离：TaskQueue 的"快照"返回值是值拷贝，不别名内部切片
// 这是 daemon 快照机制的核心（task.go:188 ClaimNext 注释 "Returning a snapshot
// prevents callers from retaining pointers into the queue's backing slice"）。
// ---------------------------------------------------------------------------

func TestPCR_TaskQueueSnapshotNoAliasing(t *testing.T) {
	q := &TaskQueue{
		tasks: []Task{
			{ID: "t1", Status: TaskPending, Result: "original"},
		},
	}

	// ClaimNext 返回指向【局部值拷贝】的指针，而非内部切片元素的别名。
	// 注意：ClaimNext 内部会把 Status 置为 TaskRunning、Result 清空，但不动 ID。
	// 因此用 ID（ClaimNext 不触碰的字段）来证明"调用方改写不影响内部状态"。
	claimed := q.ClaimNext()
	if claimed == nil {
		t.Fatal("ClaimNext 应返回被认领的任务")
	}
	claimed.ID = "MUTATED_BY_CALLER"

	if q.tasks[0].ID != "t1" {
		t.Fatalf("ClaimNext 返回的快照被别名污染：内部 ID=%q（应为 t1）", q.tasks[0].ID)
	}
	if q.tasks[0].Status != TaskRunning {
		t.Fatalf("ClaimNext 应已把内部状态置为 TaskRunning，实际 %v", q.tasks[0].Status)
	}

	// List 返回切片拷贝；外部改写元素 ID 不应污染内部切片。
	lst := q.List()
	if len(lst) != 1 {
		t.Fatalf("List 应返回 1 个任务，实际 %d", len(lst))
	}
	lst[0].ID = "MUTATED_LIST"
	if q.tasks[0].ID != "t1" {
		t.Fatalf("List 返回的快照被别名污染：内部 ID=%q（应为 t1）", q.tasks[0].ID)
	}
}

// ---------------------------------------------------------------------------
// I7 · 密钥不泄露：DaemonStatus 不应包含 token / secret 字段
// （daemon 的快照对外暴露面，必须可序列化且不携带凭证）
// ---------------------------------------------------------------------------

func TestPCR_StatusExposesNoSecretFields(t *testing.T) {
	d := &Daemon{}
	st := d.Status()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("Status 序列化失败：%v", err)
	}
	low := strings.ToLower(string(b))
	for _, bad := range []string{"token", "secret", "auth"} {
		if strings.Contains(low, bad) {
			t.Fatalf("DaemonStatus 序列化包含敏感字段 %q：%s", bad, string(b))
		}
	}
}

// ---------------------------------------------------------------------------
// I2 / I8 基线表征（Gap 候选，待 Human gate 拍板）：
// metricsServer.withAuth 在 authToken 为空时【无条件放行】next（无鉴权），
// 依赖 startMetricsServer 已强制 127.0.0.1 绑定作为补偿控制。
// 此测试钉住"当前即如此"——若后续决定"空 token 也必须拒绝"，本条会翻转。
// ---------------------------------------------------------------------------

func TestPCR_MetricsWithAuth_EmptyTokenBypasses(t *testing.T) {
	d := &Daemon{}
	called := false
	h := d.withAuth("", func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !called {
		t.Fatal("空 token 时 withAuth 应直接放行 handler（当前基线：无鉴权）")
	}
}
