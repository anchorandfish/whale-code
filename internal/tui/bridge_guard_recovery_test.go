package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/prompt"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// recoveryFakeModel 与 scripts/fakemodel 同一 SSE 方言（OpenAI tool_calls 增量
// + [DONE] flush）。恢复闭环 E2E 用它注入确定性循环：每次请求都返回
// bash true 工具调用，驱动 BridgeRunner 走完整"模型调用→工具执行→记账→
// 下一次模型调用前闸门"生产循环。
type recoveryFakeModel struct {
	calls atomic.Int64
}

func (f *recoveryFakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := f.calls.Add(1)
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	sse := func(data string) {
		fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	sse(fmt.Sprintf(`{"choices":[{"delta":{"role":"assistant","content":"第 %d 轮执行 true。"}}]}`, n))
	sce, _ := json.Marshal(map[string]interface{}{
		"choices": []map[string]interface{}{{
			"delta": map[string]interface{}{
				"tool_calls": []map[string]interface{}{{
					"index": 0, "id": "call_bash", "type": "function",
					"function": map[string]interface{}{"name": "bash", "arguments": `{"command":"true"}`},
				}},
			},
		}},
	})
	sse(string(sce))
	sse(`{"usage":{"prompt_tokens":100,"completion_tokens":20}}`)
	sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	sse("[DONE]")
}

// newRecoveryBridgeRunner 构造带真实 guard（MaxTurnsPerRequest=maxTurns）与
// 指向 fake server 的真实 SimpleClient 的 BridgeRunner（生产构造口径）。
func newRecoveryBridgeRunner(t *testing.T, srvURL string, maxTurns int) (*BridgeRunner, *bytes.Buffer) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Model = "recovery-model"
	cfg.MaxTurnsPerRequest = maxTurns
	cfg.PermissionMode = config.PermWorkspaceWrite

	client := ai.NewSimpleClient("recovery-model", "test-key", srvURL, ai.ProviderOpenAI)
	reg := ai.NewModelRegistry()
	reg.Add(&ai.ModelConfig{
		Name:            "recovery-model",
		Provider:        ai.ProviderOpenAI,
		BaseURL:         srvURL,
		APIKey:          "test-key",
		ContextWindow:   200000,
		MaxOutputTokens: 16384,
	})
	estimator := ai.NewTokenEstimator()
	toolRegistry := tools.NewRegistry()

	var buf bytes.Buffer
	homeDir, _ := os.UserHomeDir()
	sessionStore, _ := session.NewStore(homeDir + "/.whale/sessions")

	runner := &BridgeRunner{
		runnerCore: &runnerCore{
			Session:         session.NewSession(),
			Config:          &cfg,
			AIClient:        client,
			Registry:        toolRegistry,
			Executor:        tools.NewExecutor(toolRegistry, t.TempDir()),
			CmdRegistry:     newTestCmdRegistry(),
			Policy:          permission.NewPolicy(permission.LevelWorkspaceWrite),
			Budget:          ctxpkg.DefaultBudget(cfg.ContextWindow),
			Compactor:       ctxpkg.NewCompactor(estimator),
			AICompactor:     ctxpkg.NewAICompactor(client, estimator),
			Estimator:       estimator,
			Store:           sessionStore,
			Fallback:        ai.NewFallbackTracker("recovery-model", ""),
			MagicDocs:       config.NewMagicDocs(""),
			MemoryStore:     nil,
			MemoryIndex:     nil,
			MemSelector:     config.NewMemorySelector(nil, nil, client),
			MemExtractor:    config.NewAutoMemoryExtractor(nil),
			UsageTracker:    ai.NewUsageTracker(t.TempDir() + "/usage.json"),
			ModelName:       "recovery-model",
			ModelRegistry:   reg,
			AvailableModels: reg.Names(),
			WorkDir:         t.TempDir(),
		},
		encoder:         json.NewEncoder(&buf),
		continueSession: false,
		guard:           newAgentGuard(&cfg), // 生产构造：MaxTurnsPerRequest 传播
	}
	runner.Executor.InitPipelineWithHooks(runner.Policy, t.TempDir())
	runner.PromptBuilder = prompt.NewBuilder(runner.WorkDir, runner.ModelName, runner.MagicDocs, runner.MemSelector, runner.Executor, 100*1024)
	return runner, &buf
}

// 恢复闭环（防护验证缺失的半边）：停机 → 用户"继续" → guard 归零 →
// 新预算段正常发起模型调用。全链路走 BridgeRunner 生产方法
// （handleChat → streamAIRequest → executeAndContinue → 闸门停机 →
// renderGuardStop → handleChat("继续")），无测试专用旁路。
func TestBridgeGuardRecoveryLoop(t *testing.T) {
	fake := &recoveryFakeModel{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	runner, buf := newRecoveryBridgeRunner(t, srv.URL, 1)

	// 第一段：MaxTurns=1，1 轮后停机交回控制权
	id1 := 1
	runner.handleChat("执行任务", &id1, nil)

	out1 := buf.String()
	if !strings.Contains(out1, "已暂停") {
		t.Fatalf("第一段应输出停机快照（已暂停），got:\n%s", out1)
	}
	if !strings.Contains(out1, "轮次达到上限 1") {
		t.Fatalf("停机原因应为轮次达到上限 1，got:\n%s", out1)
	}
	if fake.calls.Load() != 1 {
		t.Fatalf("第一段恰好 1 次模型调用, got %d", fake.calls.Load())
	}
	if runner.guard == nil || runner.guard.TotalTurns() != 1 {
		t.Fatalf("停机时 guard TotalTurns 应为 1, got %v", runner.guard.TotalTurns())
	}

	buf.Reset()

	// 第二段：用户回复"继续" → 新预算段：guard 归零、模型被再次调用
	id2 := 2
	runner.handleChat("继续", &id2, nil)

	if fake.calls.Load() != 2 {
		t.Fatalf("「继续」后必须发起新的模型调用（防护归零、新预算段）, 模型调用数 = %d", fake.calls.Load())
	}
	if runner.guard.TotalTurns() != 1 {
		t.Fatalf("新预算段应重新计 1 轮（归零后累计）, TotalTurns = %d", runner.guard.TotalTurns())
	}
	out2 := buf.String()
	if !strings.Contains(out2, "第 2 轮") {
		t.Fatalf("第二段应有第 2 轮模型文本输出（新预算段正常跑）:\n%s", out2)
	}
	// 停机收尾：不发 error 通知（停机非错误）
	for _, line := range strings.Split(out2, "\n") {
		if strings.Contains(line, `"type":"error"`) || strings.Contains(line, `"type": "error"`) {
			t.Fatalf("停机不应产生 error 通知:\n%s", out2)
		}
	}
}
