package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/metrics"
	"github.com/anchorandfish/whale-code/internal/util"
)

// ============================================================
// M08-02 Web 工具重试机制
// 复用 ai.BackoffStrategy（C10 复用承诺， 已落地）
// ============================================================

// 默认 Web 工具重试配置常量（对标同类 CLI 自动重试 + 退避模式）
const (
	defaultWebMaxRetries = 3                      // 最大重试次数（对标同类 CLI 默认 3 次）
	defaultWebBaseDelay  = 500 * time.Millisecond // 初始退避延迟
	defaultWebMaxDelay   = 5 * time.Second        // 最大退避延迟（封顶）
)

// defaultWebBackoff 返回 Web 工具默认 BackoffStrategy 实例
// 复用 ai.NewExponentialBackoff（C10 统一接口，）
// 参数：base=500ms / max=5s / factor=2.0 / jitter=0.2
func defaultWebBackoff() ai.BackoffStrategy {
	return ai.NewExponentialBackoff(
		defaultWebBaseDelay,
		defaultWebMaxDelay,
		2.0,
		0.2,
		nil, // 用 math/rand 全局源
	)
}

// fetchWithRetry 在 BackoffStrategy 控制下重试 HTTP 请求
// - 仅对网络临时错误（net.Error.Timeout）、5xx、429 重试
// - 4xx 立即失败（不重试）
// - 支持解析 Retry-After header（429/503 响应可能携带，优先使用服务端指示延迟）
// - ctx 取消时立即返回 ctx.Err()
// - backoff 为 nil 时用 defaultWebBackoff()
// - maxRetries < 0 时按 0 处理（不重试）
//
// 对标同类 CLI 自动重试 + 退避（@commit 待回填 sha，已验证模式）
func fetchWithRetry(ctx context.Context, client *http.Client, req *http.Request,
	backoff ai.BackoffStrategy, maxRetries int) (*http.Response, error) {
	if backoff == nil {
		backoff = defaultWebBackoff()
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, err := client.Do(req)
		// 成功或不可重试的 4xx：直接返回
		if err == nil && resp.StatusCode < 500 && resp.StatusCode != 429 {
			return resp, nil
		}
		// 收集错误信息（OPT-01：lastErr 含 status code）
		status := 0
		var retryAfter time.Duration
		if resp != nil {
			status = resp.StatusCode
			// 解析 Retry-After header（OPT-02）
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
		}
		lastErr = fmt.Errorf("attempt %d failed: status=%d err=%v", attempt+1, status, err)
		// 不可重试错误：立即失败
		if !isWebRetryableError(err, status) {
			return nil, lastErr
		}
		// 已达最大重试次数：返回最后错误
		if attempt >= maxRetries {
			return nil, lastErr
		}
		// 计算延迟：优先用 Retry-After，否则用 backoff
		var delay time.Duration
		if retryAfter > 0 {
			delay = retryAfter
		} else {
			delay = backoff.NextDelay(attempt + 1)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}

// isWebRetryableError 判断错误是否可重试
// 仅对网络临时错误（net.Error.Timeout）、5xx、429 重试；4xx 立即失败
// 对标同类 CLI 重试策略：4xx 视为永久失败不重试
func isWebRetryableError(err error, status int) bool {
	if status == 429 || status >= 500 {
		return true
	}
	if status >= 400 {
		return false // 4xx 不重试
	}
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// parseRetryAfter 解析 Retry-After header
// 支持两种格式：
// - 秒数（"30"）：直接返回 30s
// - HTTP 日期（"Wed, 21 Oct 2026 07:28:00 GMT"）：返回距离当前的剩余时间
//
// 解析失败或非正值返回 0（调用方降级到 backoff 计算）
func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	// 尝试解析为秒数
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	// 尝试解析为 HTTP 日期
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// WebFetchInput WebFetch toolinput
type WebFetchInput struct {
	URL     string `json:"url" desc:"Web page URL"`
	Timeout int    `json:"timeout" desc:"Timeout in seconds"`
}

// validateWebURL checks if a URL is safe to fetch (basic structural validation only).
// DNS-level SSRF protection is enforced at the transport layer (DialContext) to prevent
// DNS rebinding attacks — validateWebURL does NOT resolve DNS to avoid TOCTOU race conditions.
func (e *Executor) validateWebURL(rawURL string) error {
	u, err := parseURLStrict(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http and https protocols are allowed, got: %s", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL host is empty")
	}
	if e.AllowLocalhostForTest {
		return nil
	}
	// Validate hostname format (basic structural check, DNS-level SSRF enforcement is in DialContext)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		// IPv6 literal
		ip := net.ParseIP(host[1 : len(host)-1])
		if ip == nil {
			return fmt.Errorf("invalid IPv6 address: %s", host)
		}
		if isIPBlocked(ip) {
			log.Warn("security: SSRF blocked", "type", "ipv6", "host", host)
			if metrics.Default != nil {
				metrics.Default.RecordSecurityEvent("ssrf")
			}
			metrics.RecordSecurityEventPrometheus("ssrf")
			return fmt.Errorf("access to restricted IP address is not allowed: %s", host)
		}
	} else if net.ParseIP(host) != nil {
		// IPv4 literal — check IP directly since no DNS resolution needed
		ip := net.ParseIP(host)
		if isIPBlocked(ip) {
			log.Warn("security: SSRF blocked", "type", "ipv4", "host", host)
			if metrics.Default != nil {
				metrics.Default.RecordSecurityEvent("ssrf")
			}
			metrics.RecordSecurityEventPrometheus("ssrf")
			return fmt.Errorf("access to restricted IP address is not allowed: %s", host)
		}
	}
	// For hostnames, DNS resolution is deferred to DialContext to prevent rebinding
	return nil
}

func isIPBlocked(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

func newSSRFSafeDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:  10 * time.Second,
		Resolver: nil,
	}
}

// createSSRFSafeTransport creates an HTTP transport with SSRF protection.
// DNS resolution and IP blocking are performed in a single DialContext call to prevent
// DNS rebinding attacks (TOCTOU race conditions between DNS resolution and connection).
func createSSRFSafeTransport(allowLocalhost bool) *http.Transport {
	dialer := newSSRFSafeDialer()
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if allowLocalhost {
				return dialer.DialContext(ctx, network, addr)
			}
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid address: %w", err)
			}
			// Use LookupIPAddr to get all resolved IPs in a single DNS query,
			// then check all of them before connecting to any — prevents DNS rebinding
			ipAddrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve host %q: %w", host, err)
			}
			for _, ia := range ipAddrs {
				if isIPBlocked(ia.IP) {
					log.Warn("security: SSRF blocked", "type", "dns", "ip", ia.IP)
					if metrics.Default != nil {
						metrics.Default.RecordSecurityEvent("ssrf")
					}
					metrics.RecordSecurityEventPrometheus("ssrf")
					return nil, fmt.Errorf("access to restricted address is not allowed: %s", ia.IP)
				}
			}
			// Connect to the first resolved IP directly (bypassing further DNS lookups)
			return dialer.DialContext(ctx, network, net.JoinHostPort(ipAddrs[0].IP.String(), port))
		},
	}
}

// parseURLStrict parses URL and rejects URLs without a host
func parseURLStrict(rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("URL must have a host")
	}
	return u, nil
}

// WebSearchInput WebSearch toolinput
type WebSearchInput struct {
	Query string `json:"query" desc:"Search keywords"`
}

// webFetchHandler fetches web page content
func (e *Executor) webFetchHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input WebFetchInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		if input.URL == "" {
			return "", fmt.Errorf("URL cannot be empty")
		}

		if err := e.validateWebURL(input.URL); err != nil {
			return "", fmt.Errorf("URL validation failed: %w", err)
		}

		timeout := 30 * time.Second
		if input.Timeout > 0 {
			timeout = time.Duration(input.Timeout) * time.Second
			if timeout > 120*time.Second {
				timeout = 120 * time.Second
			}
		}
		client := &http.Client{
			Timeout:   timeout,
			Transport: createSSRFSafeTransport(e.AllowLocalhostForTest),
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, input.URL, nil)
		if err != nil {
			return "", fmt.Errorf("createrequestfailed: %w", err)
		}
		req.Header.Set("User-Agent", "WhaleCode/0.1.0")

		// M08-02 改造新增：复用 BackoffStrategy 进行重试（C10 复用承诺，）
		// 仅对网络临时错误、5xx、429 重试；4xx 立即失败
		resp, err := fetchWithRetry(ctx, client, req, defaultWebBackoff(), defaultWebMaxRetries)
		if err != nil {
			return "", fmt.Errorf("requestfailed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		if err != nil {
			return "", fmt.Errorf("readresponsefailed: %w", err)
		}

		content := string(body)
		content = stripHTMLTags(content)

		// ★ S4：上限读 GetToolLimits（**上限的唯一出口**），不再写字面量。
		// 改造前此处硬编码 30000，而 limits.go 声明 50000 ⇒ 同一事实两个来源（B2 第（2）条实测）。
		// 执行点**刻意留在 web 层**：提到 executor 出口会改变 bash/read_file 等全部工具的返回长度，
		// 属跨域行为变更，已单独立项挂 （§11.2 范围警告）。
		content = capWebResult("WebFetch", content)

		return content, nil
	}
}

// webSearchConfigHint 是"未配置凭据"时的可执行指引。
//
// ⚠️ 维护约束（不是文案偏好）：**不得出现 SearXNG / self-hosted**。
// doWebSearch 的搜索路径对**回环与内网**地址一律拦截（isIPBlocked 覆盖
// Loopback/Private/LinkLocal），自建 SearXNG 的典型部署恰是 127.0.0.1 或内网 ⇒ 该建议不可执行。
// 有一道有意为红的门盯着这个事实：TestWebSearch_NoAPIKey_HintMustNotRecommendUnreachableOption。
// （SEARCH_API_FORMAT 的合法取值里确实有 searxng，但那份清单只在**报错文案**里出现，
//
//	不放进取指引 —— 指引只列本机可达的方案。）
const webSearchConfigHint = `WebSearch requires API configuration.
Query: %s

Set these environment variables:
  SEARCH_API_KEY     - API key for search engine (not needed when SEARCH_API_FORMAT=none-auth formats)
  SEARCH_API_FORMAT  - Request/response contract: bocha | qianfan | google | brave
  SEARCH_API_URL     - Endpoint URL (optional; every format has a built-in default)
  SEARCH_ENGINE_ID   - Custom search engine ID (Google CSE only)

Reachable options:
  - Bocha Web Search: https://open.bochaai.com/                        (SEARCH_API_FORMAT=bocha)
  - Baidu Qianfan AI Search: https://cloud.baidu.com/doc/qianfan-api/s/Wmbq4z7e5   (SEARCH_API_FORMAT=qianfan)

Free option:
  - Google Programmable Search: https://programmablesearchengine.google.com/`

// webSearchHandler search the internet
func (e *Executor) webSearchHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input WebSearchInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.Query == "" {
			return "", fmt.Errorf("query is required")
		}

		// B4 裁定 ②③：请求方法 / 请求体模板 / 鉴权形态全部来自**同一个契约结构体**，
		// 鉴权显式配置、不做 host 推断；配置写错 ⇒ 硬错（fail-closed），不退化成"猜一个形态"。
		contract, err := resolveSearchContract(os.Getenv)
		if err != nil {
			return "", err
		}

		apiKey := os.Getenv(searchAPIKeyEnv)

		// 止血（fail-closed）：不得把"我没有这个能力"编码成一次成功的结果。
		// 旧行为 return (配置提示文本, nil)：经 executor.go:730-769 映射为 IsError=false，
		// 模型会把配置提示读成"检索成功但没内容"，进而用自己的记忆补答案（幻觉入口）。
		// 现行为 err != nil ⇒ Result{IsError:true}，失败在工具层可见、可归因。
		//
		// ★ requiresAPIKey()：鉴权形态为 none 的契约本就不需要 key（改造前是无条件要求，
		// 迫使这类用户伪造一个假 key）。有鉴权契约仍等价于改造前的"无 Key 即失败"。
		if contract.requiresAPIKey() && apiKey == "" {
			return "", fmt.Errorf(webSearchConfigHint, input.Query)
		}

		results, err := e.doWebSearch(ctx, contract, apiKey, input.Query)
		if err != nil {
			// 同一类软失败：上游检索报错同样不得伪装成成功结果（"Search failed: ..." + nil）。
			return "", fmt.Errorf("Search failed: %v", err)
		}

		// ★ S4（裁定 D=①）：总长上限（8000 字符）与条数上限（≤5）同属返回契约。
		// 单点执行：doWebSearch → parseSearchResults 已是唯一出口，截断只在这里做一次。
		return capWebResult("WebSearch", results), nil
	}
}

// doWebSearch 按契约执行一次检索（方法 / 请求体 / 查询参数 / 鉴权全部取自契约结构体）。
//
// B3a-P1-1 修复（保留）：SSRF 防护由 createSSRFSafeTransport 独立负责，
// 防止 SEARCH_API_URL 被利用访问内网/云元数据端点（如 169.254.169.254）。
//
// ★ 2026-09-18 S2′：transport 的 allowLocalhost **由硬编码 false 改为 e.AllowLocalhostForTest**。
// 动机不是放开边界，而是判据可用性：判据必须能用 httptest 在**离线**下走生产入口，
// 硬编码 false 会把每个离线用例拦成 SKIP ⇒ 判据物理无效（同族：声明了却不可判别）。
// 生产路径上 AllowLocalhostForTest 恒为零值 false（只有测试代码置 true），安全边界不变；
// 且 isIPBlocked 对 Loopback/Private/LinkLocal 的判断完全没动，SSRF 防护与结构契约校验互不耦合。
func (e *Executor) doWebSearch(ctx context.Context, contract searchAPIContract, apiKey, query string) (string, error) {
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: createSSRFSafeTransport(e.AllowLocalhostForTest),
	}

	req, err := contract.buildRequest(ctx, apiKey, query)
	if err != nil {
		return "", fmt.Errorf("build search request (%s): %w", contract.Name, err)
	}

	// M08-02 改造新增：复用 BackoffStrategy 进行重试（C10 复用承诺，）
	resp, err := fetchWithRetry(ctx, client, req, defaultWebBackoff(), defaultWebMaxRetries)
	if err != nil {
		// ★ 必须脱敏：net/http 的客户端错误自带**完整 URL**（`Get "https://…/v1?key=SECRET": dial tcp…`），
		// 而 query-key 形态下 key 就在 query 里。只脱"上游响应体"是不够的。
		return "", fmt.Errorf("request failed: %s", redactSearchSecrets(err.Error()))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		safeBody := redactSearchSecrets(string(body[:min(len(body), 200)]))
		// 止血（fail-closed，）：与"无 Key"同类的第三处软失败。
		// 触达路径已核实：fetchWithRetry（web.go:72-74）对 status<500 && !=429 原样返回 resp，
		// 故 401/403（Key 配错/过期，最常见场景）必然落到本分支。
		// 旧行为 return (文本, nil) ⇒ executor.go:730-769 映射为 IsError=false ⇒
		// 模型把 "API returned status 401" 读成"检索成功"，进而用自己的记忆补答案（幻觉入口）。
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, safeBody)
	}

	// S2′：响应侧统一结构契约（引擎无关）。
	// ★ 改造前此处及 parseSearchResults 内有 3 处 `return string(body[:2000]), nil` 原文直通 ——
	// 那正是"把 HTML 汤 / 错误信封当检索结果"的幻觉入口，本次已全部删除（单出口）。
	return parseSearchResults(body, contract.Endpoint)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// legacyAgentInput 是旧 Agent tool 的入参类型（仅用于旧 agentHandler 兼容）。
// N1 引入了符合 02 §2.1.1 契约的新 AgentInput（Prompt/Tools/Model/ParentCtx），
// 此处保留旧类型仅供 legacy agentHandler 解析 agent_type/description 字段使用。
type legacyAgentInput struct {
	Prompt      string `json:"prompt" desc:"Task description for the sub-agent"`
	Description string `json:"description,omitempty" desc:"Brief task description (3-5 words)"`
	AgentType   string `json:"agent_type,omitempty" desc:"Sub-agent type: coder|plan|explore|verification|fork"`
}

// TodoWriteInput 是 TodoWrite tool 的入参类型（N16 提取为命名结构体，供 BuildSchemaFromStruct 使用）。
type TodoWriteInput struct {
	Todos []TodoItem `json:"todos" desc:"Todo list items"`
}

// TodoItem 是单个 todo 项。
type TodoItem struct {
	ID       string `json:"id" desc:"Todo item ID"`
	Content  string `json:"content" desc:"Todo item content"`
	Status   string `json:"status" desc:"Todo status: pending|in_progress|completed"`
	Priority string `json:"priority,omitempty" desc:"Todo priority: high|medium|low"`
}

// SkillInput 是 Skill tool 的入参类型（N16 提取为命名结构体，供 BuildSchemaFromStruct 使用）。
type SkillInput struct {
	Name   string `json:"name" desc:"Skill name"`
	Prompt string `json:"prompt,omitempty" desc:"Skill prompt"`
}

// topLevelDelegateLimit 计算主会话在当前权限档下**可执行**的工具面，作为顶层委派的上限。
//
// 「委派不扩权」（L2/I2）的根：子 agent 工具面 ⊆ 父会话工具面。主会话是信任根，但它
// 自身也受当前权限档约束（read-only 档下写工具不可执行）—— 若不算这一层，
// 一个 read-only 会话仍能委派出手握 bash/write_file 的 coder 子 agent。
//
// 返回 nil 表示无法计算（Policy 未接线 / 无注册表）→ 视为不限，
// 由 Policy 在**子 agent 每次工具调用**时兜底（executeToolCall → ExecuteWithPipeline）。
// 即：这里是纵深防御的一层，不是唯一防线。
func topLevelDelegateLimit(e *Executor) map[string]bool {
	if e == nil || e.registry == nil || e.Pipeline == nil {
		return nil
	}
	policy := e.Pipeline.Policy()
	if policy == nil {
		return nil
	}
	names := e.registry.List()
	out := make(map[string]bool, len(names))
	for _, name := range names {
		// 输入无关性：只有 Agent 定级依赖 input，其余工具用空对象即可；
		// 空 agent_type 在权限层被解析为默认角色（= explore，只读），是保守取值。
		if allowed, _ := policy.Check(name, "{}"); allowed {
			out[name] = true
		}
	}
	return out
}

// agentHandler sub-agent handler with tool calling support and type-based routing
// resolves agent_type parameter and passes to AgentContext, implementing typed sub-agent scheduling
// Fork type takes asynchronous path: background goroutine execution, returns task ID immediately
func (e *Executor) agentHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input legacyAgentInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input failed: %w", err)
		}
		if input.Prompt == "" {
			return "", fmt.Errorf("prompt is required")
		}

		agentCtx := NewAgentContext(e, input.Prompt, input.Description, e.WorkDir(), 1)

		// 解析调用方**声明**的子代理角色（L2 · 声明即真相）。
		//
		// 契约：缺省/空白 → DefaultAgentType（explore，委派默认不扩权）；
		// 合法值 → 归一（大小写/空白不敏感）；非法值 → fail-closed 硬报错。
		// 解析器唯一（ParseAgentType），与嵌套委派、Spawn 共用，避免三份 switch 漂移。
		at, ok := ParseAgentType(input.AgentType)
		if !ok {
			return "", invalidAgentTypeError(input.AgentType)
		}
		agentCtx.AgentType = at

		// Fork type takes asynchronous path
		if agentCtx.AgentType == AgentTypeFork {
			return e.handleForkAgent(ctx, agentCtx)
		}

		// ---- S4-F6：顶层非 fork Agent 委派收敛到 AgentTaskMgr.Spawn ----
		// 修复前：agentCtx.Run(ctx) 直连，绕过 AgentTaskManager → maxSpawn /
		// maxConcurrent / 深度守卫只覆盖嵌套子 agent（handleNestedAgentCall），
		// 不覆盖顶层并行发子 agent（对齐业内既有实现 max_threads 的意图）。
		// 修复后：与嵌套/Task 共用同一单入口 Spawn，广度守卫对顶层也生效。
		// AgentTaskMgr 未接线（单测/未初始化）→ fail-closed，绝不无界递归。
		if e.AgentTaskMgr == nil {
			return "", fmt.Errorf("Agent requires AgentTaskManager (fail-closed): Executor.AgentTaskMgr is nil")
		}

		// parentDepth=0：顶层是根的第一个子任务（Spawn 内 depth 变 1）。
		parentCtx := ctx
		topInput := AgentInput{
			Prompt:    input.Prompt,
			AgentType: string(agentCtx.AgentType), // L2：声明值直达 Spawn，不再被 prompt 关键字重猜
			// L2：委派不扩权 —— 主会话在当前权限档下**可执行**的工具面即委派上限。
			ParentToolSet: topLevelDelegateLimit(e),
			ParentCtx:     &parentCtx,
		}
		out, err := e.AgentTaskMgr.Spawn(ctx, topInput, "", 0)
		if err != nil {
			errMsg := fmt.Sprintf("Sub-agent error: %v", err)
			if out.TaskID != "" {
				errMsg = fmt.Sprintf("Sub-agent %s error: %v", out.TaskID, err)
			}
			return errMsg, nil
		}
		if out.Result == "" {
			return fmt.Sprintf("Sub-agent %s completed (no output)", out.TaskID), nil
		}
		return out.Result, nil
	}
}

// handleForkAgent handles Fork type sub-agent start
// modeled after a comparable CLI asynchronous Agent lifecycle: fire-and-forget mode
// returns task ID and state immediately, sub-agent executes in background goroutine
func (e *Executor) handleForkAgent(ctx context.Context, agentCtx *AgentContext) (string, error) {
	forkManager := e.getOrCreateForkManager()

	// recursive guard: check if already in a Fork sub-agent
	if agentCtx.IsForkChild {
		return "", fmt.Errorf("fork is not available inside a forked worker")
	}

	// set Fork-specific parameters
	agentCtx.IsForkChild = false
	agentCtx.MaxRounds = ForkMaxTurns
	agentCtx.MaxOutputTokens = ForkMaxOutputTokens
	// （2026-10-10 拍板）：fork 不设寿命闹钟（Timeout=0 ⇒ RunForkAsync/Run 走无超时分支）。
	// 对齐 同类 CLI（agent job 默认无 runtime 上限）/ 同类 CLI（后台任务无 timeout 字段）；
	// 防失控靠 MaxRounds=200 轮数闸 + 编排层 token 总闸。取消 = owner（Executor.Close 收杀）。
	agentCtx.Timeout = 0
	agentCtx.ForkManager = forkManager

	// inherit parent agent's system prompt (cache sharing optimization)
	// modeled after a comparable CLI Fork Agent: sub-agent inherits parent rendered system prompt, producing byte-identical prefix for cache hit
	if e.RenderedSystemPrompt != nil && agentCtx.ParentSysPrompt == "" {
		agentCtx.ParentSysPrompt = e.RenderedSystemPrompt.Content
	}

	task, err := agentCtx.RunForkAsync(ctx, forkManager)
	if err != nil {
		return "", fmt.Errorf("failed to start fork agent: %w", err)
	}

	// ★ ：本条回执里的下一步必须是**模型真能走的**。
	// 修复前写的是 "Use the task ID to check status later." —— 而全仓**没有任何工具**
	// 能按 fork task_id 查状态/结果（`ForkManager.GetTask` 生产零调用点）⇒ 死路指引，
	// 子 agent 的结论谁也拿不到。现在点名 ForkStatus（三态可读）。
	return fmt.Sprintf("Fork agent launched in background.\nTask ID: %s\nStatus: running\nUse the ForkStatus tool with this task ID to read its status and result.", task.ID), nil
}

// forkManagerInstance is the global Fork manager instance (lazy initialization)
var (
	forkManagerInstance *ForkManager
	forkManagerOnce     sync.Once
)

// getOrCreateForkManager gets or creates a Fork manager
func (e *Executor) getOrCreateForkManager() *ForkManager {
	forkManagerOnce.Do(func() {
		forkManagerInstance = NewForkManager()
	})
	return forkManagerInstance
}

// webTruncationMarker 是 web 系工具触达上限时的显式标记。
//
// 标记本身是契约的一部分：没有标记的截断会让模型把"被砍掉的内容"当成"页面就到此为止"，
// 于是基于不完整信息下结论（与 「声明了上限却看不出截断」同族）。判据 T13 断言它必须出现。
const webTruncationMarker = "\n... (content truncated)"

// capWebResult 是 web 系工具结果上限的**唯一执行点**（S4）。
//
// 上限取自 GetToolLimits(name).MaxResultChars —— 与工具描述（webToolConstraints）、
// Spec.Limits 同一出口，因此"声明 = 执行 = 文档"是结构保证而非人工同步（B2）。
//
// 语义与改造前 webFetchHandler 的内联截断**逐字等价**（字节切片 + 同一标记），
// 故 30000 时生产行为零变化；env 覆盖（WHALE_WEBFETCH_MAX_RESULT_CHARS）只影响上限取值。
func capWebResult(toolName, content string) string {
	maxChars := GetToolLimits(toolName).MaxResultChars
	if maxChars <= 0 || len(content) <= maxChars {
		return content
	}
	return content[:maxChars] + webTruncationMarker
}

// truncateStr truncate string
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// todoWriteHandler todo write handler with persistence
func (e *Executor) todoWriteHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input TodoWriteInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input failed: %w", err)
		}

		// 收口：Dir() 返回空（home 不可得）⇒ 报错，行为同前。
		if whalehome.Dir() == "" {
			return "", fmt.Errorf("failed to get home directory")
		}
		todoFile := filepath.Join(whalehome.Dir(), "todos.json")

		var existing []map[string]string
		if data, readErr := os.ReadFile(todoFile); readErr == nil {
			json.Unmarshal(data, &existing)
		}

		todoMap := make(map[string]map[string]string)
		for _, t := range existing {
			if id, ok := t["id"]; ok {
				todoMap[id] = t
			}
		}

		for _, t := range input.Todos {
			todoMap[t.ID] = map[string]string{
				"id":       t.ID,
				"content":  t.Content,
				"status":   t.Status,
				"priority": t.Priority,
			}
		}

		var result []map[string]string
		for _, t := range todoMap {
			if t["status"] != "completed" {
				result = append(result, t)
			}
		}

		if err := os.MkdirAll(filepath.Dir(todoFile), 0755); err != nil {
			return "", fmt.Errorf("failed to create todo directory: %w", err)
		}
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to marshal todo data: %w", err)
		}
		// Atomic write using util.AtomicWriteFile (CreateTemp + rename) to prevent data corruption
		if err := util.AtomicWriteFile(todoFile, data, 0644); err != nil {
			return "", fmt.Errorf("failed to write todo file: %w", err)
		}

		active := 0
		for _, t := range result {
			if t["status"] == "in_progress" {
				active++
			}
		}
		return fmt.Sprintf("Todo list updated: %d items (%d active)", len(result), active), nil
	}
}

// AskUserOption AskUser 工具的选项结构（:517）。
// Phase 6 S2 新增 DefaultIdx 字段（SNAPSHOT）：默认选中项索引（0-based）。
// T3 S1 新增 Description 字段（SNAPSHOT）：选项详细说明（≤80 字符）。
type AskUserOption struct {
	Label       string `json:"label"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"` // T3 S1 新增（SNAPSHOT）
}

// AskUserResult 是 AskUser 工具的返回结果（含选项信息供 TUI 层渲染）。
// Phase 6 S2 新增字段（SNAPSHOT）：DefaultIdx + Category。
// T3 S1 新增字段（SNAPSHOT）：AllowFreeText + Context。
// - Question：问题文本（≤500 字符，超长已截断）
// - Options：选项列表（allow_free_text=false 时必须非空）
// - DefaultIdx：默认选中项索引（0-based，-1 表示无默认）
// - Category：提问分类（clarify/plan-confirm/cost-warning/scope-confirm/dialogue-clarify）
// - AllowFreeText：是否允许「自由文本**取代**选择」（即纯开放式提问：true 时 options 可空）
//
//	★ 2026-09-23 · 语义**收窄**：它**不**控制客户端追加的 Other（「None of the above」）。
//
// 有 options 时 TUI **总是**追加该逃生项（对齐 同类 CLI 服务端强制 is_other=true、
// 同类 CLI 的无条件追加），否则该字段默认 false 会把「以上都不是」整个关掉。
//
// - Context：上下文元数据（trace_id/task_id 等）
type AskUserResult struct {
	Question      string          `json:"question"`
	Options       []AskUserOption `json:"options,omitempty"`
	DefaultIdx    int             `json:"default_idx,omitempty"`     // Phase 6 S2 新增（SNAPSHOT）
	Category      string          `json:"category,omitempty"`        // Phase 6 S2 新增（SNAPSHOT）
	AllowFreeText bool            `json:"allow_free_text,omitempty"` // T3 S1 新增（SNAPSHOT）
	Context       map[string]any  `json:"context,omitempty"`         // T3 S1 新增（SNAPSHOT）
}

// askUserHandler 是 AskUser 工具的执行处理器（:528）。
// 解析输入字段后做 4 项校验：
// 1. question 非空（REF-降级策略：返回 error）
// 2. question 超长（>500 字符）截断（REF-降级策略）
// 3. category 非法值降级为 dialogue-clarify（REF-降级策略-优化5）
// 4. default_idx 越界降级为 -1（REF-降级策略-优化1）
// 5. allow_free_text=false 时 options 必须非空（REF-降级策略）
// 校验通过后返回 AskUserResult JSON 给 TUI 渲染 PendingChoice。
func (e *Executor) askUserHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Question      string          `json:"question"`
			Options       []AskUserOption `json:"options"`
			DefaultIdx    int             `json:"default_idx"`     // Phase 6 S2 新增
			Category      string          `json:"category"`        // Phase 6 S2 新增
			AllowFreeText bool            `json:"allow_free_text"` // T3 S1 新增
			Context       map[string]any  `json:"context"`         // T3 S1 新增
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}

		// 校验 question 非空（REF-降级策略）
		if strings.TrimSpace(input.Question) == "" {
			return "", fmt.Errorf("question is required")
		}
		// 截断超长 question（>500 字符）
		if len(input.Question) > 500 {
			input.Question = input.Question[:500] + "..."
		}

		// 校验 category（REF-降级策略-优化5：非法值降级为 dialogue-clarify）
		validCategories := map[string]bool{
			"clarify":          true,
			"plan-confirm":     true,
			"cost-warning":     true,
			"scope-confirm":    true,
			"dialogue-clarify": true,
		}
		if !validCategories[input.Category] {
			input.Category = "dialogue-clarify"
		}

		// 校验 default_idx 越界（REF-降级策略-优化1：越界降级为 -1）
		if len(input.Options) > 0 {
			if input.DefaultIdx < 0 || input.DefaultIdx >= len(input.Options) {
				input.DefaultIdx = -1
			}
		} else {
			input.DefaultIdx = -1
		}

		// 校验 allow_free_text=false 时 options 必须非空
		if !input.AllowFreeText && len(input.Options) == 0 {
			return "", fmt.Errorf("options is required when allow_free_text=false")
		}

		// 校验「提供了 options 时必须 2-3 个」（对齐 同类 CLI request_user_input 的互斥选项约束）。
		// 逼模型先想清楚「到底有几条路」，而非丢一个开放式/单选项问题把认知负担转嫁给用户。
		// 兼容边界：allow_free_text=true 且 options 空 = 纯开放式提问，仍保留（编排器专用），不在此约束内。
		if len(input.Options) > 0 && (len(input.Options) < 2 || len(input.Options) > 3) {
			return "", fmt.Errorf("options must contain 2-3 mutually exclusive choices")
		}

		result := AskUserResult{
			Question:      input.Question,
			Options:       input.Options,
			DefaultIdx:    input.DefaultIdx,
			Category:      input.Category,
			AllowFreeText: input.AllowFreeText,
			Context:       input.Context,
		}
		resultJSON, _ := json.Marshal(result)
		return string(resultJSON), nil
	}
}

// skillHandler skill 调用处理器
// 调 ExecuteWithMeta 取 Prompt 返回；**不改写**全局 AIClient（模型注入已于 删除）。
func (e *Executor) skillHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input SkillInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("parse input: %w", err)
		}
		if input.Name == "" {
			return "", fmt.Errorf("skill name cannot be empty")
		}

		if e.SkillMgr == nil {
			return "skill manager not initialized", nil
		}

		result, err := e.SkillMgr.ExecuteWithMeta(input.Name, input.Prompt)
		if err != nil {
			return fmt.Sprintf("skill execute failed: %v", err), nil
		}

		// B2（S19-H-B，2026-09-15 · O-T 轮）：此处原有「把 skill 声明的 allowed-tools 写进
		// 共享 Pipeline.Policy、defer 恢复」的注入，已删除。理由：
		// · SetAllowedTools 是「免问表」（permission.go:609 auto-approve），不是能力白名单；
		// · 原实现是**整体覆盖**而非取交集 ⇒ 外部 skill 写 `allowed-tools: [bash, write]`
		// 等于把这两个工具**加进免问表**（放宽，不是收窄）；
		// · skill 正文与该元数据同源同文件（project/plugin = 外部制品，git clone 即可进入）
		// ⇒ 违反 I8（外部代码 ⊥ 权限）与 I10（不自提权）。
		// 同类先例：agent_task.go:292 已以同一理由移除 SetAllowedTools(intersect(...))。
		// 守卫：gt_b2_skill_allowed_tools_test.go（结构面判别器 + 范围守卫）。
		// skill.SkillResult.AllowedTools 字段本身保留（公开结构，tui 在用），本处只停用注入。

		// 此处原有「若 skill 指定 model，则 AIClient.SetModel(result.Model)
		// + defer 恢复」的注入，已删除。两个理由：
		// · **它从未生效**：位置在 ExecuteWithMeta **之后**，而两者之间没有任何 AI 调用
		// ⇒ 改完全局后立刻 return，defer 当场把值改回去，窗口内零请求。
		// （看起来"接线了"，实际是死代码 —— 比缺失更难发现。）
		// · 即使位置对，也不该用全局改写：多个 skill / 子 agent 并发会互相污染
		// （同 agent_task.go 的原因，见）。
		// skill 的 model 仍经 SkillResult.Model 返回；若将来需要让它影响请求，
		// 应在**调用 AI 之前**把它作为 ai.Request.Model 传入，而不是改写全局 client。
		return result.Prompt, nil
	}
}

// stripHTMLTags is a simple HTML tag remover
func stripHTMLTags(s string) string {
	var result strings.Builder
	inTag := false
	inScript := false

	lower := strings.ToLower(s)
	i := 0
	for i < len(s) {
		if !inScript && !inTag {
			if strings.HasPrefix(lower[i:], "<script") {
				inScript = true
				closeIdx := strings.Index(lower[i:], "</script>")
				if closeIdx >= 0 {
					i = i + closeIdx + len("</script>")
					inScript = false
					continue
				}
				i++
				continue
			}
			if strings.HasPrefix(lower[i:], "<style") {
				inScript = true
				closeIdx := strings.Index(lower[i:], "</style>")
				if closeIdx >= 0 {
					i = i + closeIdx + len("</style>")
					inScript = false
					continue
				}
				i++
				continue
			}
		}
		if inScript {
			i++
			continue
		}
		if s[i] == '<' {
			inTag = true
			i++
			continue
		}
		if s[i] == '>' && inTag {
			inTag = false
			result.WriteByte(' ')
			i++
			continue
		}
		if !inTag {
			result.WriteByte(s[i])
		}
		i++
	}

	return strings.TrimSpace(result.String())
}
