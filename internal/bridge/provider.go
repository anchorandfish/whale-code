package bridge

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// Authentication-related error codes
const (
	ErrInvalidToken       = -32001 // Invalid Token
	ErrTokenExpired       = -32002 // Token expired
	ErrRefreshFailed      = -32003 // Token refresh failed
	ErrRefreshUnavailable = -32004 // Refresh service unavailable
	ErrAuthRequired       = -32005 // Authentication required
	ErrBridgeReconnecting = -32006 // T1-P1-A1（SNAPSHOT-5，）：重连中，IDE 可重试
)

// MapErrorCodeToErrorID 将 bridge 错误码映射到 N6 的 ErrorID。
// T1-P1-A1：ErrBridgeReconnecting(-32006) 映射到 ErrBridgeReconnect（与重连失败同 ID，便于上层统一处理）。
func MapErrorCodeToErrorID(code int) ai.ErrorID {
	switch code {
	case ErrInvalidToken, ErrTokenExpired, ErrAuthRequired:
		return ai.ErrBridgeJWT
	case ErrRefreshFailed, ErrRefreshUnavailable, ErrBridgeReconnecting:
		return ai.ErrBridgeReconnect
	default:
		return ai.ErrBridgeJWT
	}
}

// TokenStatusNotification is used to notify Token status via JSON-RPC
type TokenStatusNotification struct {
	IsValid      bool   `json:"isValid"`                // Whether Token is valid
	ExpiresIn    int64  `json:"expiresIn"`              // Seconds until expiration, -1 means no expiration
	State        string `json:"state"`                  // State: "none", "valid", "expiring_soon", "expired"
	RefreshState string `json:"refreshState,omitempty"` // Refresh state: "idle", "scheduled", "refreshing", "failed"
	Source       string `json:"source,omitempty"`       // Token source
}

// AuthProviderConfig defines AuthProvider configuration
type AuthProviderConfig struct {
	SchedulerConfig RefreshSchedulerConfig // Refresh scheduler config
}

// DefaultAuthProviderConfig returns default AuthProvider configuration
func DefaultAuthProviderConfig() AuthProviderConfig {
	return AuthProviderConfig{
		SchedulerConfig: DefaultRefreshSchedulerConfig(),
	}
}

// AuthProvider coordinates JWT decoding, refresh scheduling, and Token storage
// Provides Token setting, status retrieval, and refresh functionality
type AuthProvider struct {
	mu           sync.Mutex
	config       AuthProviderConfig
	store        *TokenStore
	scheduler    *RefreshScheduler
	tokenMgr     *TokenManager     // 委托验签（v2 R1，nil 时回退 DecodeJWTPayload）
	reconnectMgr *ReconnectManager // 401/403 重连管理（v2 R5/R6，nil 时不触发 reconnect）
	// T1-P1-A1 新增字段（SNAPSHOT-4，）
	onReconnectComplete OnReconnectCompleteCallback // IDE 通知回调（SetToken 异步触发）
}

// NewAuthProvider creates a new authentication provider
// config is the configuration parameters, including refresh scheduler config
func NewAuthProvider(config AuthProviderConfig) *AuthProvider {
	p := &AuthProvider{
		config: config,
		store:  NewTokenStore(),
	}

	// Create scheduler, refresh callback points to AuthProvider internal refresh method
	p.scheduler = NewRefreshScheduler(config.SchedulerConfig, p.refreshTokenInternal)

	return p
}

// SetTokenManager 注入 TokenManager 用于委托验签（v2 R1）。
// P0-13 修复：传 nil 时 SetToken 不再回退到 DecodeJWTPayload（fail-closed），而是返回 ErrInvalidToken。
func (p *AuthProvider) SetTokenManager(tm *TokenManager) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokenMgr = tm
}

// SetReconnectManager 注入 ReconnectManager 用于 401/403 重连（v2 R5/R6）。
// 传 nil 时 SetToken 不触发 reconnect（向后兼容）。
// B2 修复（方案 A）：自动将 reconnectMgr 的 refreshToken 回调委托到 scheduler 的刷新逻辑，
// 避免调用方遗漏注入导致 ReconnectOnAuthError nil panic。
// T1-P1-A1：同时注入统一 BackoffStrategy（C10 冲突解决）+ onReconnectComplete 回调委托。
func (p *AuthProvider) SetReconnectManager(rm *ReconnectManager) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reconnectMgr = rm
	if rm == nil {
		return
	}
	// B2 修复：注入 refresh 回调，委托 scheduler 的 refreshToken（与定时刷新共用同一逻辑）
	rm.SetRefreshToken(func() (string, error) {
		return p.scheduler.refreshToken()
	})
	// T1-P1-A1（优化 6）：注入统一 BackoffStrategy（基于 rm.config 构造 ExponentialBackoff）
	bs := ai.NewExponentialBackoff(
		rm.config.BaseDelay,
		rm.config.MaxDelay,
		rm.config.Factor,
		rm.config.JitterFactor,
		nil, // rng=nil 用 math/rand 全局源
	)
	rm.SetBackoffStrategy(bs)
	// T1-P1-A1（优化 4）：注入 onReconnectComplete 回调委托（若 AuthProvider 已注入则转发）
	if p.onReconnectComplete != nil {
		rm.SetOnReconnectComplete(p.onReconnectComplete)
	}
}

// SetOnReconnectComplete 注入重连完成回调（T1-P1-A1 新增）。
// cb 会被转发到 reconnectMgr（若已注入）；未注入 reconnectMgr 时存储待 SetReconnectManager 时转发。
// 由 TUI bridge.go 调用，用于发 reconnectStatus JSON-RPC 通知给 IDE。
func (p *AuthProvider) SetOnReconnectComplete(cb OnReconnectCompleteCallback) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onReconnectComplete = cb
	if p.reconnectMgr != nil {
		p.reconnectMgr.SetOnReconnectComplete(cb)
	}
}

// SetToken sets Token and schedules automatic refresh
// token is the JWT token string, source is the Token source identifier
// Returns Token status notification and possible error
// v2 R1：保留原签名，内部委托 tokenMgr.SetToken 验签（若已注入）。
// v2 R5/R6：401/403 认证错误触发 reconnect（若 reconnectMgr 已注入），reconnect 不持 p.mu。
// T1-P1-A1：401/403 触发时改为异步重连（候选 B），SetToken 立即返回 ErrBridgeReconnecting（不阻塞调用方）。
// 重连成功后由 goroutine 内部调 tokenMgr.SetToken + storeAndNotify + scheduler.ScheduleOrFallback；
// 重连完成通过 onReconnectComplete 回调通知 IDE（吸纳审查优化 3）。
func (p *AuthProvider) SetToken(token string, source string) (*TokenStatusNotification, error) {
	if token == "" {
		return nil, formatError(ErrInvalidToken, "token is empty")
	}

	// 读取 tokenMgr/reconnectMgr（持锁快照，随后释放以便 tokenMgr.SetToken 获取自己的锁）
	p.mu.Lock()
	tokenMgr := p.tokenMgr
	reconnectMgr := p.reconnectMgr
	p.mu.Unlock()

	// 验签阶段
	var payload *JwtPayload
	if tokenMgr != nil {
		// 委托 TokenManager 验签（tokenMgr.SetToken 持自己的 mu，不与 p.mu 死锁）
		if err := tokenMgr.SetToken(token); err != nil {
			// 认证类错误 + reconnect 可用 → 异步触发重连（T1-P1-A1，吸纳审查优化 2/3）
			if isAuthError(err) && reconnectMgr != nil {
				// TryStartAsyncReconnect 原子检查 inflight：已 inflight 则 fail-fast
				ctx, ok := reconnectMgr.TryStartAsyncReconnect()
				if !ok {
					// 已有重连在跑，立即返回 ErrBridgeReconnecting（fail-fast，防 401 风暴）
					return nil, formatError(ErrBridgeReconnecting, "reconnect already in progress, please retry later")
				}
				// 启动异步重连 goroutine（不阻塞调用方）
				go p.runAsyncReconnect(ctx, reconnectMgr, tokenMgr, source)
				// 立即返回 ErrBridgeReconnecting（调用方零阻塞，IDE 可重试）
				return nil, formatError(ErrBridgeReconnecting, "reconnect started, please retry later")
			}
			return nil, err
		}
		// 验签成功，取 payload 存 store（tokenMgr 已验签，此处仅取 payload）
		payload, _ = DecodeJWTPayload(token)
	} else {
		// P0-13 修复：fail-closed — tokenMgr 未注入时拒绝 token（原实现回退 DecodeJWTPayload 不验签为 fail-open）
		// 生产路径下 tokenMgr 必须由 NewBridgeRunner 注入；未注入说明配置异常，拒绝放行。
		return nil, formatError(ErrInvalidToken, "tokenMgr not injected: fail-closed 拒绝不验签的 token (P0-13)")
	}

	// 存储 + 通知（持 p.mu）
	p.mu.Lock()
	defer p.mu.Unlock()
	p.storeAndNotify(token, payload, source)
	return p.buildStatusNotification(source), nil
}

// runAsyncReconnect 在 goroutine 内执行异步重连 + 重连成功后存储/调度（T1-P1-A1 新增，吸纳审查优化 3）。
//
// 行为：
// - 调 reconnectMgr.ReconnectOnAuthErrorAsync(ctx) 获取结果 channel
// - 阻塞等待结果（goroutine 内阻塞不影响调用方）
// - 成功：调 tokenMgr.SetToken(newToken) 验签 + 持 p.mu 调 storeAndNotify + scheduler.ScheduleOrFallback
// - 失败：由 ReconnectManager 内部调 onReconnectComplete 回调通知 IDE
//
// 线程安全：不持 p.mu 进入（storeAndNotify 内部自获取 p.mu）。
// ctx 由 TryStartAsyncReconnect 创建，可被 reconnectMgr.Stop() 取消。
func (p *AuthProvider) runAsyncReconnect(ctx context.Context, reconnectMgr *ReconnectManager, tokenMgr *TokenManager, source string) {
	resultCh := reconnectMgr.ReconnectOnAuthErrorAsync(ctx)
	result, ok := <-resultCh
	if !ok {
		// channel 已关闭且无数据（不应发生，防御性）
		return
	}
	if result.Err != nil {
		// 重连失败：onComplete 回调已由 ReconnectManager 内部调用
		return
	}
	// 重连成功：用新 token 验签 + 存储 + 调度刷新（吸纳审查优化 3）
	newToken := result.NewToken
	if newToken == "" {
		return
	}
	if err := tokenMgr.SetToken(newToken); err != nil {
		// 桶 X3 病灶5 修复：新 token 验签失败时，通过 NotifyReconnectFailure 通知 IDE 真实失败状态。
		// 修复前：仅 return，但 finishAsyncReconnect(true, nil) 已被调用 → IDE 收到虚假 success。
		// 修复后：调用 NotifyReconnectFailure(err) 触发 onComplete(false, err)，IDE 收到失败通知。
		// 不调 finishAsyncReconnect（inflight 已被清理），仅触发回调覆盖之前的虚假 success。
		reconnectMgr.NotifyReconnectFailure(fmt.Errorf("new token verification failed: %w", err))
		return
	}
	// 验签成功：取 payload + 存储 + 调度刷新（持 p.mu）
	payload, _ := DecodeJWTPayload(newToken)
	p.mu.Lock()
	p.storeAndNotify(newToken, payload, source)
	p.mu.Unlock()
}

// storeAndNotify 存储 token 并调度刷新（必须在持有 p.mu 时调用）。
func (p *AuthProvider) storeAndNotify(token string, payload *JwtPayload, source string) {
	info := &TokenInfo{
		RawToken:   token,
		Payload:    payload,
		ObtainedAt: time.Now(),
		Source:     source,
	}
	if payload != nil && payload.Exp > 0 {
		info.ExpiresAt = time.Unix(payload.Exp, 0)
	}
	p.store.Set(info)
	p.scheduler.ScheduleOrFallback(token)
}

// GetStatus gets the current Token status notification
// Returns Token validity, expiration time, refresh status and other info
func (p *AuthProvider) GetStatus() *TokenStatusNotification {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buildStatusNotification("")
}

// RefreshToken manually triggers Token refresh
// Returns refreshed Token status notification and possible error
func (p *AuthProvider) RefreshToken() (*TokenStatusNotification, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	info := p.store.Get()
	if info == nil {
		return nil, formatError(ErrAuthRequired, "no token available")
	}

	// Execute refresh (using refresh function configured in scheduler)
	newToken, err := p.scheduler.refreshToken()
	if err != nil {
		return nil, formatError(ErrRefreshFailed, fmt.Sprintf("refresh failed: %v", err))
	}

	// Decode new Token
	payload, err := DecodeJWTPayload(newToken)
	if err != nil {
		return nil, formatError(ErrInvalidToken, fmt.Sprintf("invalid refreshed token: %v", err))
	}

	// Update storage
	newInfo := &TokenInfo{
		RawToken:   newToken,
		Payload:    payload,
		ObtainedAt: time.Now(),
		Source:     info.Source,
	}
	if payload.Exp > 0 {
		newInfo.ExpiresAt = time.Unix(payload.Exp, 0)
	}
	p.store.Set(newInfo)

	// Schedule next refresh
	p.scheduler.ScheduleOrFallback(newToken)

	return p.buildStatusNotification(info.Source), nil
}

// refreshTokenInternal is the internal Token refresh implementation
// Triggered by scheduler callback, current implementation returns error indicating refresh service unavailable
// Should be replaced with specific refresh logic in actual use
func (p *AuthProvider) refreshTokenInternal() (string, error) {
	// Default implementation: returns error indicating refresh service unavailable
	// In actual use, custom refresh logic can be set via SetRefreshFunction
	return "", fmt.Errorf("refresh not configured")
}

// SetRefreshFunction sets a custom Token refresh function
// fn is the refresh callback, returns new Token string or error
func (p *AuthProvider) SetRefreshFunction(fn func() (string, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scheduler.refreshToken = fn
}

// SetOnTokenUpdate sets the Token update success callback
// When scheduler successfully refreshes Token, this callback is called to notify new Token
func (p *AuthProvider) SetOnTokenUpdate(fn func(string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scheduler.SetOnTokenUpdate(fn)
}

// SetOnStateChange sets the authentication state change callback
// When scheduler state changes, this callback is called with state string
func (p *AuthProvider) SetOnStateChange(fn func(string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scheduler.SetOnStateChange(func(state SchedulerState) {
		// Convert SchedulerState to string state
		var stateStr string
		switch {
		case state.ConsecFailures > 0:
			stateStr = "refresh_failed"
		case state.IsScheduled:
			stateStr = "scheduled"
		default:
			stateStr = "idle"
		}
		fn(stateStr)
	})
}

// buildStatusNotification builds status notification based on current storage state (must be called while holding lock)
func (p *AuthProvider) buildStatusNotification(source string) *TokenStatusNotification {
	info := p.store.Get()
	schedulerState := p.scheduler.GetState()

	notif := &TokenStatusNotification{}

	// Set source
	if source != "" {
		notif.Source = source
	} else if info != nil {
		notif.Source = info.Source
	}

	// Set refresh state
	switch {
	case schedulerState.ConsecFailures > 0:
		notif.RefreshState = "failed"
	case schedulerState.IsScheduled:
		notif.RefreshState = "scheduled"
	default:
		notif.RefreshState = "idle"
	}

	// No Token
	if info == nil {
		notif.IsValid = false
		notif.ExpiresIn = -1
		notif.State = "none"
		return notif
	}

	// Token expired
	if info.IsExpired() {
		notif.IsValid = false
		notif.ExpiresIn = 0
		notif.State = "expired"
		return notif
	}

	// No expiration time
	if info.ExpiresAt.IsZero() {
		notif.IsValid = true
		notif.ExpiresIn = -1
		notif.State = "valid"
		return notif
	}

	// Calculate remaining seconds
	expiresIn := int64(time.Until(info.ExpiresAt).Seconds())
	if expiresIn < 0 {
		expiresIn = 0
	}

	// Determine if expiring soon (within 5 minutes)
	state := "valid"
	if info.IsExpiringSoon(5 * time.Minute) {
		state = "expiring_soon"
	}

	notif.IsValid = true
	notif.ExpiresIn = expiresIn
	notif.State = state
	return notif
}

// Provider represents a backend service provider with weight and latency tracking
type Provider struct {
	ID              string
	BaseWeight      int
	EffectiveWeight int
	CurrentWeight   int
	totalLatency    atomic.Int64
	requestCount    atomic.Int64
	latencyMu       sync.Mutex // protects avgLatency pair read for consistency
}

// RecordLatency records a request latency for dynamic weight adjustment
func (p *Provider) RecordLatency(d time.Duration) {
	p.totalLatency.Add(int64(d))
	p.requestCount.Add(1)
}

// AvgLatency returns the average response latency of the provider
// Uses mutex to ensure consistent pair-read of totalLatency and requestCount
func (p *Provider) AvgLatency() time.Duration {
	p.latencyMu.Lock()
	count := p.requestCount.Load()
	total := p.totalLatency.Load()
	p.latencyMu.Unlock()

	if count == 0 {
		return 0
	}
	return time.Duration(total / count)
}

// ProviderPool manages multiple providers with weighted round-robin scheduling
type ProviderPool struct {
	mu        sync.Mutex
	providers []*Provider
}

// NewProviderPool creates a new provider pool
func NewProviderPool() *ProviderPool {
	return &ProviderPool{}
}

// Add adds a provider to the pool
func (pp *ProviderPool) Add(provider *Provider) {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	pp.providers = append(pp.providers, provider)
}

// Remove removes a provider from the pool by ID
func (pp *ProviderPool) Remove(id string) {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	for i, p := range pp.providers {
		if p.ID == id {
			pp.providers = append(pp.providers[:i], pp.providers[i+1:]...)
			return
		}
	}
}

// Next selects the next provider using smooth weighted round-robin
func (pp *ProviderPool) Next() *Provider {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	if len(pp.providers) == 0 {
		return nil
	}

	var best *Provider
	totalWeight := 0

	for _, p := range pp.providers {
		p.CurrentWeight += p.EffectiveWeight
		totalWeight += p.EffectiveWeight
		if best == nil || p.CurrentWeight > best.CurrentWeight {
			best = p
		}
	}

	if best != nil {
		best.CurrentWeight -= totalWeight
	}
	return best
}

// AdjustWeights dynamically adjusts effective weights based on average latency
func (pp *ProviderPool) AdjustWeights() {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	if len(pp.providers) == 0 {
		return
	}

	var minLatency time.Duration
	for i, p := range pp.providers {
		lat := p.AvgLatency()
		if i == 0 || (lat > 0 && lat < minLatency) {
			minLatency = lat
		}
	}

	if minLatency == 0 {
		for _, p := range pp.providers {
			p.EffectiveWeight = p.BaseWeight
		}
		return
	}

	for _, p := range pp.providers {
		lat := p.AvgLatency()
		if lat == 0 {
			p.EffectiveWeight = p.BaseWeight
			continue
		}
		ratio := float64(minLatency) / float64(lat)
		adjusted := int(float64(p.BaseWeight) * ratio)
		if adjusted < 1 {
			adjusted = 1
		}
		p.EffectiveWeight = adjusted
	}
}
