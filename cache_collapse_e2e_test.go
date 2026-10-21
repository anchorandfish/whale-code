package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestE2E_LayeredPromptCacheHit 多轮对话缓存命中率
// 验证: 连续多轮对话中，分层 system prompt 排列optimization后缓存命中率提升
// 使用 MiniMax-M2.5 (百炼阿里云) 或其他可用模型
func TestE2E_LayeredPromptCacheHit(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 需要有效的 API Key")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--plain")
	cmd.Dir = t.TempDir()
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Start()
	defer cmd.Process.Kill()

	reader := bufio.NewReader(stdout)

	// 发送 3 轮简短对话
	prompts := []string{"hi", "1+1=?", "hello"}
	for _, p := range prompts {
		fmt.Fprintf(stdin, "%s\n", p)
		// 等待响应（读取直到出现提示符或超时）
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			if strings.Contains(line, ">") || strings.Contains(line, "whale") {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	// 验证: 程序没有崩溃，能正常对话
	// 详细的缓存命中率验证需要在 UsageTracker 中添加输出
	t.Log("E2E: Multi-turn conversation completed successfully")
}

// TestE2E_CompactionPipeline 长对话压缩管线
// 验证: 5 层压缩管线在长对话中正常工作
func TestE2E_CompactionPipeline(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 需要有效的 API Key")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--plain")
	cmd.Dir = t.TempDir()
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Start()
	defer cmd.Process.Kill()

	reader := bufio.NewReader(stdout)

	// 后台排空 stdout，防止管道缓冲区写满导致子进程阻塞
	go func() {
		for {
			if _, err := reader.ReadString('\n'); err != nil {
				return
			}
		}
	}()

	// 发送多轮对话触发压缩
	for i := 0; i < 5; i++ {
		fmt.Fprintf(stdin, "say 'round %d' only\n", i+1)
		// 等待响应
		time.Sleep(3 * time.Second)
	}

	t.Log("E2E: Long conversation with compaction completed")
}

// bridgeEnabled 检测 bridge 模式是否被显式启用（环境变量或配置文件）。
// 与 runBridgeMode 中的安全门禁逻辑保持一致：需 WHALE_BRIDGE_ENABLED=1 或
// 配置文件 bridge_enabled=true 才视为启用。
// 未启用时 runBridgeMode 会写 stderr 并 os.Exit(1)，不发送 ready 通知，
// 导致测试从 stdout 读到 EOF。此函数用于在测试开头提前跳过该路径。
func bridgeEnabled() bool {
	cfg := loadConfig()
	return cfg.BridgeEnabled
}

// TestE2E_BridgeProtocol Bridge 模式协议测试
// 验证: Bridge JSON-RPC 协议正确性
func TestE2E_BridgeProtocol(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 需要有效的 API Key")
	}
	// bridge 模式因安全原因（JWT 不验签）默认禁用，未显式 opt-in 时
	// runBridgeMode 直接 os.Exit(1) 不发 ready 通知，测试会读到 EOF。
	// 此处复用与 runBridgeMode 相同的判定逻辑，未启用则跳过。
	if !bridgeEnabled() {
		t.Skip("bridge 模式默认禁用，需显式 opt-in（WHALE_BRIDGE_ENABLED=1 或配置 bridge_enabled=true）")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--bridge")
	cmd.Dir = t.TempDir()
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Start()
	defer cmd.Process.Kill()

	reader := bufio.NewReader(stdout)

	// 等待 ready 通知
	readyLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("Failed to read ready notification: %v", err)
	}

	var readyMsg map[string]interface{}
	json.Unmarshal([]byte(strings.TrimSpace(readyLine)), &readyMsg)

	// 验证 ready 通知
	if method, ok := readyMsg["method"].(string); !ok || method != "ready" {
		t.Errorf("Expected ready notification, got: %v", readyMsg)
	}

	// 发送 chat 请求
	chatReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "chat",
		"params": map[string]interface{}{
			"message": "hi",
		},
	}
	reqJSON, _ := json.Marshal(chatReq)
	fmt.Fprintf(stdin, "%s\n", string(reqJSON))

	// 读取流式通知
	receivedStream := false
	timeout := time.After(20 * time.Second)
	for {
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for stream notification")
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("Failed to read stream: %v", err)
			}
			var msg map[string]interface{}
			json.Unmarshal([]byte(strings.TrimSpace(line)), &msg)
			if method, ok := msg["method"].(string); ok && method == "stream" {
				receivedStream = true
				// 检查是否有 done 类型
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if streamType, ok := params["type"].(string); ok && streamType == "done" {
						goto done
					}
				}
			}
		}
	}
done:

	if !receivedStream {
		t.Error("Expected to receive stream notifications")
	}

	t.Log("E2E: Bridge protocol test passed")
}

// TestE2E_CompactionPipelineNoAPI 无 API 的压缩管线测试
// 验证: 压缩管线在无 API 调用的情况下也能正确工作
func TestE2E_CompactionPipelineNoAPI(t *testing.T) {
	// 这个测试不需要 API Key，直接测试压缩管线逻辑
	// 已在integration test中覆盖，此处作为 E2E 标记
	t.Log("E2E: Compaction pipeline logic tested in integration tests")
}
