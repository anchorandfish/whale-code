package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// defaultMaxFrameLength 是 stdio 单帧（一条 newline-delimited JSON-RPC 消息）的字节数上限。
// 对标同类 CLI stderr 64MB / 消息大小限制，防止恶意 server 通过超大帧触发
// OOM（10MB 足以容纳任何合法 MCP 消息）。
const defaultMaxFrameLength = 10 * 1024 * 1024 // 10MB

// StdioTransport is an stdin/stdout pipe-based MCP transport implementation
// uses newline-delimited JSON frames for JSON-RPC message transport
type StdioTransport struct {
	cmd            *exec.Cmd                    // Child process command
	stdin          io.WriteCloser               // Child process stdin
	stdout         *bufio.Reader                // Child process stdout
	pending        map[string]chan *rpcResponse // Pending request mapping (Send 路径)；key=string(RawID)，向后兼容客户端 number ID
	mu             sync.RWMutex                 // Protects pending map + requestHandler + notifHandler
	notifHandler   NotificationHandler          // Notification callback
	requestHandler RequestHandler               // Server-side 请求回调（第3轮修复：致命漏洞 #1）
	closed         bool                         // Whether closed
	closeMu        sync.Mutex                   // Protects close operation
	maxFrameLength int                          // 单帧字节数上限（M4，防 OOM）
	// 流式请求路由表（SendStream 路径）
	streamPending map[string]chan StreamFrame // 流式请求 ID → channel 映射；key=string(RawID)
	streamMu      sync.RWMutex                // 保护 streamPending map
}

// NewStdioTransport creates stdio-based MCP transport
// Starts child process and communicates via stdin/stdout pipe using JSON-RPC.
// 进程组创建委托 sandbox.StartCommandWithProcessGroup（C11：引用 sandbox helper）。
//
// （2026-10-10，对齐 同类 CLI）：env 构造按 level 三态（详见函数体内注释）——
// **默认 Disabled = 去沙箱**（不 Prepare、不 Restrict），但仍剥密钥；显式
// relaxed/strict/isolated 才走 sandbox.Manager.Prepare 的沙箱路径。
func NewStdioTransport(ctx context.Context, command string, args []string, env map[string]string, sbMgr sandbox.Manager, level sandbox.SandboxLevel) (*StdioTransport, error) {
	// ⑤ 修复（2026-09-10，本轮新发现，须 review）：子进程生命周期与「创建期请求 ctx」解耦。
	//
	// 请求 ctx 仍约束握手等待（Client.initialize → Send 用 ctx 超时），但进程本身
	// 由 StdioTransport.Close()（KillProcessGroup）回收 —— 不能用 exec.CommandContext
	// 把进程绑定到调用方的短生命周期 ctx。
	//
	// 历史缺陷（实测）：tools.getMCPClientForWorkDir 在 NewClient 返回时 `defer cancel()`
	// → exec.CommandContext 的 watchCtx 立即 kill 刚启动的 MCP server →
	// DiscoverAndRegisterMCPTools 与 McpCallTool 全部静默失败（"transport closed"），
	// 即 MCP 工具在 macOS/Linux 上根本不可用。
	cmd := exec.Command(command, args...)

	// env 构造三态（，2026-10-10，对齐 同类 CLI）：
	//
	//	① level == Disabled（**现在的默认**）⇒ 去沙箱：不 Prepare、不 Restrict，
	// 但仍"只剥不缩"（剥密钥、保留宿主其余）—— 联网型 server 开箱可用；
	//	② level != Disabled 且 sbMgr != nil ⇒ 沙箱路径：Prepare 产出最小 env
	// ＋ 捕获 OSSandbox 供启动前 Restrict（显式 opt-in 的逃生舱）；
	//	③ level != Disabled 且 sbMgr == nil ⇒ 既有契约：宿主完整 env
	// （生产侧 mcpSandboxManager 永不返回 nil，故永不命中；由
	// mcp_char_test.go 钉住此形态）。
	//
	// ★ 关键：密钥过滤**不再依附于沙箱**。①③的差别只在"是否用白名单收窄"，
	// 两者的密钥剥离都由 sandbox 包的 DenyList 保证（单一事实源）。
	var envList []string
	var osSb sandbox.OSSandbox // P0-1：捕获 OS 沙箱实例，供启动前 Restrict
	switch {
	case level == sandbox.SandboxLevelDisabled:
		envList = sandbox.BuildEnv(sandbox.UnsandboxedEnvConfig())
	case sbMgr != nil:
		workDir, err := os.Getwd()
		if err != nil || workDir == "" {
			workDir = "."
		}
		sandboxCtx, cleanup, err := sbMgr.Prepare(ctx, level, workDir)
		if err == nil {
			defer cleanup()
			envList = sandboxCtx.Env
			osSb = sandboxCtx.OSSandbox() // P0-1：捕获 OS 沙箱实例
		} else {
			// Prepare 失败：**不再回退到宿主完整 env**（那会泄漏密钥 —— 修此 fail-open），
			// 降级为"只剥不缩"：保住可用性，同时不泄漏密钥。
			envList = sandbox.BuildEnv(sandbox.UnsandboxedEnvConfig())
		}
	default:
		envList = os.Environ()
	}
	for k, v := range env {
		envList = append(envList, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = envList

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	cmd.Stderr = os.Stderr

	// P0-1：启动前应用 OS 级隔离（同 bash.go，fail-closed 防绕过）。
	if osSb != nil {
		if restrictErr := osSb.Restrict(cmd); restrictErr != nil {
			return nil, fmt.Errorf("sandbox: OS restrict failed (server not started for safety): %w", restrictErr)
		}
	}
	// C11: 引用 sandbox helper 启动子进程并放入新进程组
	// （Unix: Setpgid；Windows: CREATE_NEW_PROCESS_GROUP），Close 时用
	// sandbox.KillProcessGroup 终止整个进程组，移除自实现的 taskkill。
	if err := sandbox.StartCommandWithProcessGroup(cmd); err != nil {
		return nil, fmt.Errorf("start server: %w", err)
	}

	t := &StdioTransport{
		cmd:            cmd,
		stdin:          stdinPipe,
		stdout:         bufio.NewReader(stdoutPipe),
		pending:        make(map[string]chan *rpcResponse),
		maxFrameLength: defaultMaxFrameLength,
		streamPending:  make(map[string]chan StreamFrame),
	}

	go t.readLoop()

	return t, nil
}

// writeMessage 以 MCP stdio 规范要求的形式写出一帧：单行 JSON + '\n'。
//
// 依据（MCP spec 2024-11-05 §Transports / stdio）：
//
//	"Messages are delimited by newlines, and MUST NOT contain embedded newlines."
//
// 依据（同业）：同类实现按行读写。
//
// 不变量：json.Marshal 的输出不含裸换行（字符串内的 '\n' 会被转义成字面 `\n` 两个字节），
// 因此 msg 体内不可能出现分隔符 —— 无需再做转义，也无需长度前缀。
// 该不变量是本实现的正确性前提，由 TestFrame_JSONMarshalHasNoRawNewline 守卫。
//
// 集中为单一写点的理由：5 个写点必须字节级一致，任何一处漏改 ⇒ 该路径静默挂死
// （且是「初始化正常、只有流式/通知失败」这类最难查的形态）。
func (t *StdioTransport) writeMessage(msg []byte) error {
	buf := make([]byte, 0, len(msg)+1)
	buf = append(buf, msg...)
	buf = append(buf, '\n')
	_, err := t.stdin.Write(buf)
	return err
}

// parseFrame 解析一帧 newline-delimited JSON（line 已去掉行尾分隔符与首尾空白）。
//
// 提为包级纯函数（而非方法）的理由：帧解析是纯逻辑，与 exec.Cmd / stdin / stdout 无关；
// 独立成纯函数后，「改回 Content-Length 帧格式」这类回归能被打红，而不再依赖反射查字段名
// —— 旧的 bug_test.go 反射守卫 TestBug_StdioNoContentLengthLimit（只查字段名、不查行为）正是典型的假绿守卫：
// 把实现回退成 Content-Length，它仍然绿；现由行为断言 TestGuard_StdioFrameLengthLimit 取代。
//
// 返回值语义与 readLoop 原控制流逐一一对应：
// - ok=true → 解析成功，msg 有效
// - ok=false, fatal=false → 该帧应被跳过（空行 / 非法 JSON），调用方 continue
// - ok=false, fatal=true → 帧超长（> maxFrameLength），调用方 return（终止连接）
//
// M4 语义迁移：旧实现把上限挂在 Content-Length 头的数值上；改行分隔后挂在行长上 ——
// 两者都约束「一帧的字节数」，语义等价。
func parseFrame(line string, maxFrameLength int) (msg readLoopMsg, ok bool, fatal bool) {
	if len(line) == 0 {
		return readLoopMsg{}, false, false
	}
	if len(line) > maxFrameLength {
		return readLoopMsg{}, false, true
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return readLoopMsg{}, false, false
	}
	return msg, true, false
}

// Send sends JSON-RPC request and waits for response
// Sends request via stdin pipe, waits for corresponding ID response via pending map
func (t *StdioTransport) Send(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ch := make(chan *rpcResponse, 1)
	idKey := strconv.FormatInt(id, 10)
	t.mu.Lock()
	t.pending[idKey] = ch
	t.mu.Unlock()

	if err := t.writeMessage(reqJSON); err != nil {
		t.mu.Lock()
		delete(t.pending, idKey)
		t.mu.Unlock()
		return nil, fmt.Errorf("write: %w", err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("transport closed")
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, idKey)
		t.mu.Unlock()
		return nil, ctx.Err()
	}
}

// SendNotification sends JSON-RPC notification (no ID, no response expected)
func (t *StdioTransport) SendNotification(ctx context.Context, method string, params interface{}) error {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	return t.writeMessage(reqJSON)
}

// SetNotificationHandler sets notification callback
// Called when notification message is received from server
// 并发安全：通过 mu 锁保护 notifHandler 字段，避免与 readLoop 并发读竞态（建议优化 #5）
func (t *StdioTransport) SetNotificationHandler(handler NotificationHandler) {
	t.mu.Lock()
	t.notifHandler = handler
	t.mu.Unlock()
}

// SetRequestHandler 注册 server-side 请求回调（第3轮修复：新引入致命漏洞 #1）
// 对标 SetNotificationHandler 模式：handler=nil 清除回调
// readLoop 收到 id + method 同时存在的消息时调用此 handler
//
// 并发安全：通过 mu 锁保护 requestHandler 字段，避免与 readLoop 并发读竞态（建议优化 #2）
func (t *StdioTransport) SetRequestHandler(handler RequestHandler) {
	t.mu.Lock()
	t.requestHandler = handler
	t.mu.Unlock()
}

// SendResponse 发送 JSON-RPC success response（第2轮修复：致命漏洞 #1 前置条件）
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样写入响应消息的 id 字段
// awaitElicitationResponse goroutine 通过此方法异步发送响应
func (t *StdioTransport) SendResponse(reqID json.RawMessage, result interface{}) error {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return fmt.Errorf("transport is closed")
	}
	t.closeMu.Unlock()
	t.sendSuccessResponse(reqID, result)
	return nil
}

// Close 关闭传输连接，执行三阶段进程清理（M5/C11）：
//
//	Stage 1: stdin Close（EOF 信号）— 优雅关闭，让 server 自行退出
//	Stage 2: 优雅终止信号（Unix: SIGTERM 进程组）+ grace period（5s）
//	Stage 3: 强制终止整个进程组 — sandbox.KillProcessGroup（C11 引用 sandbox helper）
//
// 三阶段确保子进程不孤儿：stdin EOF → SIGTERM → SIGKILL 进程组。
// Windows 缺乏优雅组终止信号，Stage 2 为 no-op，由 Stage 3 的 taskkill /T /F 兜底。
func (t *StdioTransport) Close() error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true

	// Stage 1: stdin Close（EOF 信号）— 优雅关闭
	if t.stdin != nil {
		t.stdin.Close()
	}

	// Stage 2 + 3: 仅当子进程存在时执行
	if t.cmd != nil && t.cmd.Process != nil {
		// Stage 2: 优雅终止信号（Unix: SIGTERM 进程组；Windows: no-op）+ grace period
		_ = signalProcessGroupGraceful(t.cmd)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- t.cmd.Wait()
		}()

		select {
		case <-done:
			// 进程在 grace period 内优雅退出，无需 Stage 3
		case <-ctx.Done():
			// Stage 3: grace period 超时，强制终止整个进程组
			// C11: 引用 sandbox.KillProcessGroup（Unix: kill -PGID SIGKILL；Windows: taskkill /T /F）
			_ = sandbox.KillProcessGroup(t.cmd)
			<-done // 等待 cmd.Wait 返回，回收僵尸进程
		}
	}

	// 清理 pending 请求，通知所有等待方 transport 已关闭
	t.mu.Lock()
	for id, ch := range t.pending {
		close(ch)
		delete(t.pending, id)
	}
	t.mu.Unlock()

	return nil
}

// readLoopMsg 是 readLoop 解析消息用的内部结构
// 第3轮修复（致命漏洞 #2）：ID 字段从 int64 改为 json.RawMessage（命名 RawID）
// 对标同类 CLI RequestId enum（String(String) | Integer(i64)；REF-外部-同类 CLI-RequestId）
// 保留原始 JSON 字节，无损支持 string/number 双类型
type readLoopMsg struct {
	RawID  json.RawMessage `json:"id"` // 原始 JSON 字节的请求 ID（string/number/null）
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

// hasID 判断消息是否含 id 字段（区分请求/响应 vs 通知）
// 第3轮修复（致命漏洞 #1）：JSON-RPC 2.0 规范：
// - Request：含 id + method + params
// - Notification：含 method + params，无 id
// - Response：含 id + result 或 error
// json.RawMessage 在 JSON 中无 id 字段时为 nil（len==0），有 id 字段时为非空字节
// 建议优化 #3：处理 "id": null 边界（null 解析为 4 字节 "null"，不应视为有效 id）
func (m *readLoopMsg) hasID() bool {
	return len(m.RawID) > 0 && string(m.RawID) != "null"
}

// parseIDToInt64 将 json.RawMessage 解析为 int64（向后兼容现有 rpcResponse.ID / StreamFrame.ID int64 字段）
// 第3轮修复（致命漏洞 #2）：transport 层 msg.ID 改为 RawID，但 rpcResponse/StreamFrame 结构保留 int64
// 此 helper 用于桥接：number ID 直接解析；string ID 解析失败返回 0（向后兼容客户端发送的 number ID）
// 注意：client 发送请求时 ID 用 int64（c.serverID.Add(1)），所以响应的 RawID 一定是 number，
// 此 helper 在客户端响应路径上不会丢失信息。server-side 请求路径用 RawID 原样回传，不经过此 helper
func parseIDToInt64(rawID json.RawMessage) int64 {
	if len(rawID) == 0 {
		return 0
	}
	var id int64
	if err := json.Unmarshal(rawID, &id); err != nil {
		// string ID 解析失败，返回 0（仅在混合场景下可能出现，常规 client 发送 number ID 不会触发）
		return 0
	}
	return id
}

// readLoop continuously reads JSON-RPC responses and notifications from stdout
// Parses newline-delimited JSON frames, dispatches response to pending map or notification handler
//
// 同时支持 SendStream 路径——匹配 ID 的响应优先路由到 streamPending，
// 未在 streamPending 中的响应走原 pending 路径；notifications/progress 通知
// 路由到所有活跃 streamPending channel（stdio 单连接多路复用）。
//
// 第3轮修复（致命漏洞 #1/#2）：readLoop 改为三路径分发
// - 路径 1：id != nil && method != "" → 请求（调用 requestHandler，识别 errElicitationPending 哨兵不发响应）
// - 路径 2：id != nil && method == "" → 响应（路由到 pending/streamPending channel）
// - 路径 3：id == nil && method != "" → 通知（转发给 notifHandler）
// 分发优先级：请求 > 响应 > 通知（避免 server-side 请求被误判为响应）
func (t *StdioTransport) readLoop() {
	defer func() {
		t.mu.Lock()
		for id, ch := range t.pending {
			close(ch)
			delete(t.pending, id)
		}
		t.mu.Unlock()
		// 清理 streamPending
		t.streamMu.Lock()
		for id, ch := range t.streamPending {
			close(ch)
			delete(t.streamPending, id)
		}
		t.streamMu.Unlock()
	}()

	for {
		line, err := t.stdout.ReadString('\n')
		if err != nil {
			return
		}

		line = strings.TrimSpace(line)

		// 帧解析提为纯函数 parseFrame（见下方定义）：
		// 空行 / 非法 JSON ⇒ 跳过该行（continue）；
		// 超长帧 ⇒ 终止连接（return，fail-safe：流已不可恢复）。
		msg, ok, fatal := parseFrame(line, t.maxFrameLength)
		if fatal {
			return
		}
		if !ok {
			continue
		}

		// === 第3轮修复：三路径分发 ===
		if msg.hasID() && msg.Method != "" {
			// 路径 1：server-side 请求（id + method 同时存在）
			t.handleServerSideRequest(msg)
		} else if msg.hasID() {
			// 路径 2：响应（id 存在，method 为空）
			t.handleResponse(msg)
		} else if msg.Method != "" {
			// 路径 3：通知（id 不存在，method 存在）
			t.handleNotification(msg)
		}
	}
}

// handleServerSideRequest 处理 server-side JSON-RPC 请求（第3轮修复：致命漏洞 #1）
// 调用 requestHandler 处理请求，按返回值契约决定是否发送响应
//
// 第3轮修复（致命漏洞 #1）：readLoop 不再遗漏 server-side 请求路径
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样回传
// 建议优化 #2：读取 requestHandler 时加 RLock 保护，避免与 SetRequestHandler 并发写竞态
//
// 对标同类 CLI rmcp 库：rmcp runtime 自动路由 server-side 请求到 handlers，
// whale-code 显式调用 requestHandler，等价但更可控
func (t *StdioTransport) handleServerSideRequest(msg readLoopMsg) {
	// 建议优化 #2：加 RLock 保护 requestHandler 读取
	t.mu.RLock()
	handler := t.requestHandler
	t.mu.RUnlock()
	if handler == nil {
		// fail-safe：无 requestHandler 时发送 method not found error response
		// 对标 JSON-RPC 2.0 规范 §"Error object" code -32601
		t.sendErrorResponse(msg.RawID, &RPCError{
			Code:    -32601,
			Message: "method not found",
		})
		return
	}
	// 调用 requestHandler（即 Client.dispatchRequest）
	result, err := handler(msg.Method, msg.Params, msg.RawID)
	if errors.Is(err, errElicitationPending) {
		// 哨兵：不发响应，由 awaitElicitationResponse goroutine 异步发送
		// 对标第2轮致命漏洞 #1 修复的 readLoop 调用方契约
		return
	}
	if err != nil {
		// handler 返回错误：发送 error response
		t.sendErrorResponse(msg.RawID, &RPCError{
			Code:    -32603,
			Message: err.Error(),
		})
		return
	}
	if result != nil {
		// handler 返回结果：发送 success response
		t.sendSuccessResponse(msg.RawID, result)
	}
}

// handleResponse 处理 JSON-RPC 响应（原 readLoop 的 msg.ID > 0 路径）
// 第3轮修复（致命漏洞 #2）：ID 索引从 int64 改为 string(RawID)
// 客户端发送请求时 ID 用 int64，register pending 时转 strconv.FormatInt(id, 10) 作为 map key
// 响应到达时用 string(RawID) 查找 pending channel
func (t *StdioTransport) handleResponse(msg readLoopMsg) {
	idKey := string(msg.RawID)
	// 优先检查 streamPending（SendStream 路径）
	t.streamMu.RLock()
	streamCh, streamOk := t.streamPending[idKey]
	t.streamMu.RUnlock()
	if streamOk {
		frame := StreamFrame{
			ID:     parseIDToInt64(msg.RawID), // 兼容现有 StreamFrame.ID int64 字段
			Result: msg.Result,
			Error:  msg.Error,
		}
		streamCh <- frame
		t.streamMu.Lock()
		delete(t.streamPending, idKey)
		close(streamCh)
		t.streamMu.Unlock()
		return
	}
	// 原 Send 路径
	resp := &rpcResponse{
		ID:     parseIDToInt64(msg.RawID), // 兼容现有 rpcResponse.ID int64 字段
		Result: msg.Result,
		Error:  msg.Error,
	}
	t.mu.RLock()
	ch, ok := t.pending[idKey]
	t.mu.RUnlock()
	if ok {
		ch <- resp
		t.mu.Lock()
		delete(t.pending, idKey)
		t.mu.Unlock()
	}
}

// handleNotification 处理 JSON-RPC 通知（原 readLoop 的 msg.Method != "" 路径）
// 原逻辑保留，仅参数类型从匿名 struct 改为 readLoopMsg
func (t *StdioTransport) handleNotification(msg readLoopMsg) {
	// notifications/progress 路由到所有活跃 streamPending
	// （stdio 单连接，多并发流式调用时进度通知广播到所有活跃 channel）
	if msg.Method == "notifications/progress" {
		frame := StreamFrame{
			Method:   msg.Method,
			Params:   msg.Params,
			Progress: ParseProgressInfo(msg.Params),
		}
		t.streamMu.RLock()
		for _, ch := range t.streamPending {
			select {
			case ch <- frame:
			default:
				// channel 满，跳过（背压由 Client 层 BackpressurePipe 处理）
			}
		}
		t.streamMu.RUnlock()
	}
	// 转发给外部通知回调
	t.mu.RLock()
	handler := t.notifHandler
	t.mu.RUnlock()
	if handler != nil {
		handler(msg.Method, msg.Params)
	}
}

// sendSuccessResponse 发送 JSON-RPC success response（第3轮修复：致命漏洞 #1 前置条件）
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样写入响应消息的 id 字段
// 对标同类 CLI rmcp 库基于 make_sender 返回值自动发送 response 的机制
func (t *StdioTransport) sendSuccessResponse(reqID json.RawMessage, result interface{}) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(reqID), // 原样回传，无损支持 string/number 双类型
		"result":  result,
	}
	respJSON, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_ = t.writeMessage(respJSON)
}

// sendErrorResponse 发送 JSON-RPC error response（第3轮修复：致命漏洞 #1 前置条件）
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样写入响应消息的 id 字段
// 对标 JSON-RPC 2.0 规范 §"Error object"（REF-外部-MCP-Spec-2025-06-18-RequestId）
func (t *StdioTransport) sendErrorResponse(reqID json.RawMessage, rpcErr *RPCError) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(reqID), // 原样回传，无损支持 string/number 双类型
		"error":   rpcErr,
	}
	respJSON, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_ = t.writeMessage(respJSON)
}

// SendStream 发送 JSON-RPC 请求并返回流式响应 channel（/ T1-P1-A3）。
//
// stdio 传输的流式语义（对标 MCP 规范 2025-06-18 §"Transports"，访问日期 2026-07-21）：
// - 复用现有 readLoop，无需启动新 goroutine
// - 请求通过 stdin pipe 发送（newline-delimited JSON，见 writeMessage）
// - 响应 + notifications/progress 通过 readLoop 路由到 streamPending[id] channel
// - channel 在 readLoop 收到匹配 ID 的最终响应后关闭
//
// goroutine 生命周期：
// - ctx 取消 → 启动清理 goroutine 关闭 channel + 清理 streamPending
// - readLoop 收到最终响应 → 关闭 channel + 清理 streamPending
// - readLoop 退出（连接断开）→ defer 清理所有 streamPending
func (t *StdioTransport) SendStream(ctx context.Context, id int64, method string, params interface{}) (<-chan StreamFrame, error) {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return nil, fmt.Errorf("transport is closed")
	}
	t.closeMu.Unlock()

	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ch := make(chan StreamFrame, 16)
	idKey := strconv.FormatInt(id, 10)
	t.streamMu.Lock()
	t.streamPending[idKey] = ch
	t.streamMu.Unlock()

	if err := t.writeMessage(reqJSON); err != nil {
		t.streamMu.Lock()
		delete(t.streamPending, idKey)
		close(ch)
		t.streamMu.Unlock()
		return nil, fmt.Errorf("write: %w", err)
	}

	// 启动 goroutine 监听 ctx 取消，清理 streamPending
	go func() {
		<-ctx.Done()
		t.streamMu.Lock()
		if streamCh, ok := t.streamPending[idKey]; ok {
			delete(t.streamPending, idKey)
			close(streamCh)
		}
		t.streamMu.Unlock()
	}()

	return ch, nil
}
