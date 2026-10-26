package bridge

// 判据：终局通知必须**先于**结果交付。
//
// 背景（CI 实测暴露，非构造场景）：
// 新版 token 验签失败时，AuthProvider.runAsyncReconnect 要补发一条
// NotifyReconnectFailure(false, err)，用来覆盖"重连成功"那条通知，避免 IDE 停在假成功
// （X3 病灶5 的收尾）。但这两条通知分属两个 goroutine：
//   - 生产者（本包 ReconnectOnAuthErrorAsync 的 goroutine）发 finishAsyncReconnect(...)
//   - 消费者（provider.runAsyncReconnect）发 NotifyReconnectFailure(...)
// 修复前生产者先 `resultCh <- result`（buffered，不阻塞）再发通知，两者之间没有同步点
// ⇒ 补发的失败可能先到 ⇒ IDE 以最后一条为准 ⇒ 又显示"重连成功"。
//
// 本文件给出两道判据：
//  1. 结构门（确定性，AST）：任何 `resultCh <-` 交付之前，同一个 block 内必须先有
//     `finishAsyncReconnect(...)` 调用。这条门不依赖调度，改回旧顺序必红。
//  2. 行为判据（happens-before）：消费者拿到结果的那一刻，终局通知必须已经记录。
//     修复后由程序顺序保证（send happens-after 回调），恒绿；修复前则取决于调度，
//     慢消费者（要验签）往往让生产者先发通知 —— 这正是一条老用例只在 CI 偶发红的原因。
//     所以判别力由第 1 条承担，第 2 条负责盯住"交付边界"这个不变量本身。

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"
)

// w124SendToResultCh 判断语句是否是向 resultCh 交付结果的发送语句。
func w124SendToResultCh(stmt ast.Stmt) bool {
	send, ok := stmt.(*ast.SendStmt)
	if !ok {
		return false
	}
	ident, ok := send.Chan.(*ast.Ident)
	return ok && ident.Name == "resultCh"
}

// w124IsFinishCall 判断语句是否是 rm.finishAsyncReconnect(...) 调用语句。
func w124IsFinishCall(stmt ast.Stmt) bool {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := exprStmt.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "finishAsyncReconnect"
}

// TestW124_FinishNotificationPrecedesResultSend 结构门：交付结果前必须先发终局通知。
//
// 判别性：把 reconnect.go 里三处「通知 / 交付」的顺序改回旧样子，本用例立刻红。
// 为防止"没扫到东西也算绿"，同时断言命中数 > 0。
func TestW124_FinishNotificationPrecedesResultSend(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "reconnect.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 reconnect.go 失败（判据自身不能工作，拒绝出结论）: %v", err)
	}

	var sendCount, finishCount int
	var violations []string

	ast.Inspect(file, func(node ast.Node) bool {
		block, ok := node.(*ast.BlockStmt)
		if !ok {
			return true
		}
		notified := false
		for _, stmt := range block.List {
			if w124IsFinishCall(stmt) {
				notified = true
				finishCount++
			}
			if w124SendToResultCh(stmt) {
				sendCount++
				if !notified {
					pos := fset.Position(stmt.Pos())
					violations = append(violations, fmt.Sprintf("%s:%d", pos.Filename, pos.Line))
				}
			}
		}
		return true
	})

	// 防空转：一个都没扫到 = 判据失效（不是"干净"）
	if sendCount == 0 {
		t.Fatalf("在 reconnect.go 里没找到任何 resultCh 交付语句 —— 判据已失效，不能判绿")
	}
	if finishCount == 0 {
		t.Fatalf("在 reconnect.go 里没找到任何 finishAsyncReconnect 调用 —— 判据已失效，不能判绿")
	}
	if len(violations) > 0 {
		t.Fatalf("发现 %d 处「先交付结果、后发终局通知」（消费者补发的失败可能先到 ⇒ IDE 停在假成功）:\n  %v\n"+
			"修法：把 rm.finishAsyncReconnect(...) 挪到 resultCh <- result 之前。",
			len(violations), violations)
	}
}

// TestW124_TerminalNotificationPrecedesResultHandoff 行为判据：
// 消费者拿到结果的瞬间，终局通知必须已经发出；补发的失败必须是最后一条。
func TestW124_TerminalNotificationPrecedesResultHandoff(t *testing.T) {
	// refresh 成功返回一个用错密钥签名的 token（复现"验签失败"分支）
	wrongSecretToken := signJWT(JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}, "wrong-secret")

	cfg := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  1,
	}
	rm := NewReconnectManager(cfg, func() (string, error) { return wrongSecretToken, nil })
	defer rm.Stop()

	recorder := newCallbackRecorder()
	rm.SetOnReconnectComplete(recorder.AsCallback())

	resultCh := rm.ReconnectOnAuthErrorAsync(context.Background())
	result, ok := <-resultCh
	if !ok {
		t.Fatal("结果通道被关闭但没有结果")
	}
	if result.Err != nil {
		t.Fatalf("refresh 应成功，实际失败: %v", result.Err)
	}
	if result.NewToken == "" {
		t.Fatal("refresh 成功但结果里没有 token")
	}

	// ★ 不变量：交付发生在终局通知之后（程序顺序保证），此刻应当已经有一条 success 通知
	if got := len(recorder.Calls()); got < 1 {
		t.Fatalf("消费者拿到结果时终局通知还没发出（已记录 %d 条）⇒ 顺序不变量被破坏，"+
			"消费者补发的 failure 可能先到、IDE 停在假成功", got)
	}

	// 模拟消费者：验签失败 → 补发 failure，它必须是最后一条
	rm.NotifyReconnectFailure(errors.New("new token verification failed: signature mismatch"))
	calls := recorder.Calls()
	last := calls[len(calls)-1]
	if last.success {
		t.Fatal("最后一条通知必须是 success=false（否则 IDE 停在假成功）")
	}
	if last.err == nil {
		t.Fatal("最后一条通知必须带非 nil 的 err")
	}
}
