package bridge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/metrics"
)

// TokenPrefixes is the list of known JWT token prefixes to strip before decoding
var TokenPrefixes = []string{"sk-ant-si-", "sk-bailian-", "Bearer "}

// JwtPayload represents the decoded payload part of a JWT token
type JwtPayload struct {
	Sub    string `json:"sub,omitempty"`
	Iss    string `json:"iss,omitempty"`
	Aud    string `json:"aud,omitempty"`
	Exp    int64  `json:"exp,omitempty"`
	Iat    int64  `json:"iat,omitempty"`
	Nbf    int64  `json:"nbf,omitempty"`
	Jti    string `json:"jti,omitempty"`
	Scope  string `json:"scope,omitempty"`
	Region string `json:"region,omitempty"`
}

// DecodeJWTPayload 仅解码 JWT payload，不验签。
// ⚠️ Deprecated: 此函数不验证签名，存在安全风险。新代码必须用 VerifyJWT。
// 仅供调试使用，不可用于鉴权：任何人可伪造 JWT payload，不可作为信任依据（B-1 修复）。
// 保留仅为向后兼容（现有 TokenManager 旧构造函数过渡期使用）。
// DecodeJWTExpiry 仅用于 exp 提取调度（非鉴权），保留不验签（v2 R3）。
// Auto-strips known prefixes like "sk-ant-si-", "sk-bailian-", "Bearer " before decoding
// Returns decoded JwtPayload pointer, returns error if token format is invalid
func DecodeJWTPayload(tokenString string) (*JwtPayload, error) {
	// 1. Strip known prefixes
	raw := StripTokenPrefix(tokenString)

	// 2. Split by ".", take the second segment (payload)
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
	}

	// 3. Base64URL decode payload
	payloadBytes, err := base64urlDecode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("base64 decode failed: %w", err)
	}

	// 4. JSON deserialization
	var payload JwtPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("json unmarshal failed: %w", err)
	}

	return &payload, nil
}

// DecodeJWTExpiry extracts expiration timestamp from JWT (Unix seconds)
// Returns exp claim value, returns error if no exp or token is invalid
func DecodeJWTExpiry(tokenString string) (int64, error) {
	payload, err := DecodeJWTPayload(tokenString)
	if err != nil {
		return 0, fmt.Errorf("decode JWT payload failed: %w", err)
	}
	if payload.Exp == 0 {
		return 0, fmt.Errorf("token has no exp claim")
	}
	return payload.Exp, nil
}

// StripTokenPrefix strips known prefixes from JWT token
// Supported prefixes: "sk-ant-si-", "sk-bailian-", "Bearer "
// Returns as-is if no matching prefix
func StripTokenPrefix(token string) string {
	for _, prefix := range TokenPrefixes {
		if strings.HasPrefix(token, prefix) {
			return strings.TrimPrefix(token, prefix)
		}
	}
	return token
}

// MaskToken masks Token for desensitization, only showing first 4 and last 4 characters
// Returns "***" when length is less than 9
func MaskToken(token string) string {
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "***" + token[len(token)-4:]
}

// base64urlDecode performs Base64URL decoding, auto-padding
// Replaces URL-safe characters (- and _) back to standard characters, and pads = based on length
func base64urlDecode(s string) ([]byte, error) {
	// Base64URL may lack padding, needs to be padded
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.StdEncoding.DecodeString(s)
}

// base64urlEncode performs Base64URL encoding, removing padding
// Replaces standard characters (+ and /) with URL-safe characters, and removes trailing =
func base64urlEncode(data []byte) string {
	s := base64.StdEncoding.EncodeToString(data)
	s = strings.ReplaceAll(s, "+", "-")
	s = strings.ReplaceAll(s, "/", "_")
	return strings.TrimRight(s, "=")
}

// VerifyOptions 是 VerifyJWT 的可选参数集（P0-1/P0-2，）。
type VerifyOptions struct {
	Secret           string             // HMAC-SHA256 密钥（来源委托 H3）
	ExpectedIssuer   string             // 期望的 iss claim（空则不校验）
	ExpectedAudience string             // 期望的 aud claim（空则不校验）
	ClockSkewSeconds int64              // 时钟偏移窗口（秒），对标同类 CLI --ws-max-clock-skew-seconds，默认 30
	JtiCache         *JtiCachePersisted // jti 去重 cache（P0-11: 改为持久化版，nil 则不校验，仅验签+claim）
}

// VerifyJWT 验证 JWT 签名并校验 claim（iss/aud/exp/nbf/jti）（P0-1/P0-2，）。
// 算法：HMAC-SHA256，密钥由调用方传入（密钥来源委托 H3）。
// 校验顺序：1) 格式校验 → 2) 签名验证 → 3) payload 解码 → 4) exp 过期 → 5) nbf 未生效 → 6) iss/aud 匹配 → 7) jti 去重。
// 任一失败返回带 N6 ErrBridgeJWT 的错误（02 §2.3.2 契约）。
func VerifyJWT(tokenString string, opts VerifyOptions) (*JwtPayload, error) {
	raw := StripTokenPrefix(tokenString)
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, ai.WithID(fmt.Errorf("invalid jwt format: expected 3 parts, got %d", len(parts)), ai.ErrBridgeJWT)
	}
	// 1. 签名验证（HMAC-SHA256）
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(opts.Secret))
	mac.Write([]byte(signingInput))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expectedSig), []byte(parts[2])) {
		return nil, ai.WithID(fmt.Errorf("jwt signature mismatch"), ai.ErrBridgeJWT)
	}
	// 2. payload 解码
	payloadBytes, err := base64urlDecode(parts[1])
	if err != nil {
		return nil, ai.WithID(fmt.Errorf("decode payload failed: %w", err), ai.ErrBridgeJWT)
	}
	var payload JwtPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, ai.WithID(fmt.Errorf("unmarshal payload failed: %w", err), ai.ErrBridgeJWT)
	}
	// 3. exp 过期校验（带时钟偏移窗口）
	// B5-P1-4 修复：无 exp claim 的 JWT 必须拒绝（fail-closed），不再被永久接受。
	// 原实现 payload.Exp == 0 时跳过过期校验，导致无 exp 的 JWT 永久有效且不触发刷新。
	now := time.Now().Unix()
	if payload.Exp == 0 {
		// §4.6 安全事件监控：JWT 无 exp 拦截记录
		log.Warn("security: JWT rejected, missing exp claim (exp=0)")
		if metrics.Default != nil {
			metrics.Default.RecordSecurityEvent("jwt")
		}
		metrics.RecordSecurityEventPrometheus("jwt")
		return nil, ai.WithID(fmt.Errorf("jwt missing exp claim (exp=0 rejected)"), ai.ErrBridgeJWT)
	}
	if now > payload.Exp+opts.ClockSkewSeconds {
		return nil, ai.WithID(fmt.Errorf("jwt expired at %d, now %d", payload.Exp, now), ai.ErrBridgeJWT)
	}
	// 4. nbf 未生效校验（带时钟偏移窗口）
	if payload.Nbf > 0 && now+opts.ClockSkewSeconds < payload.Nbf {
		return nil, ai.WithID(fmt.Errorf("jwt not valid before %d, now %d", payload.Nbf, now), ai.ErrBridgeJWT)
	}
	// 5. iss/aud 校验
	if opts.ExpectedIssuer != "" && payload.Iss != opts.ExpectedIssuer {
		return nil, ai.WithID(fmt.Errorf("jwt iss mismatch: expected %s, got %s", opts.ExpectedIssuer, payload.Iss), ai.ErrBridgeJWT)
	}
	if opts.ExpectedAudience != "" && payload.Aud != opts.ExpectedAudience {
		return nil, ai.WithID(fmt.Errorf("jwt aud mismatch: expected %s, got %s", opts.ExpectedAudience, payload.Aud), ai.ErrBridgeJWT)
	}
	// 6. jti 去重（委托 JtiCache）— P0-11: 改用 IsReplayedAndSave 持久化新 jti 到磁盘
	if payload.Jti != "" && opts.JtiCache != nil {
		if opts.JtiCache.IsReplayedAndSave(payload.Jti, payload.Exp) {
			return nil, ai.WithID(fmt.Errorf("jwt jti replay detected: %s", payload.Jti), ai.ErrBridgeJWT)
		}
	}
	return &payload, nil
}

// TokenManager manages JWT tokens with automatic refresh before expiration.
// 当通过 NewTokenManagerWithVerify 构造时，SetToken 会调 VerifyJWT 验签（P0-1/P0-2）；
// 当通过 NewTokenManager（deprecated）构造时，SetToken 回退到 DecodeJWTPayload（向后兼容）。
type TokenManager struct {
	mu            sync.Mutex
	token         string
	payload       *JwtPayload
	refreshToken  func() (string, error)
	onRefreshed   func(newToken string)
	timer         *time.Timer
	verifyOpts    VerifyOptions // 验签选项（Secret 为空时回退到 DecodeJWTPayload）
	verifyEnabled bool          // 是否启用验签（NewTokenManagerWithVerify 设置为 true）
}

// TokenManagerOptions 是 NewTokenManagerWithVerify 的可选参数集（v2 R1 新增）。
type TokenManagerOptions struct {
	Secret           string // HMAC-SHA256 密钥（来源委托 H3）
	ExpectedIssuer   string // 期望的 iss claim
	ExpectedAudience string // 期望的 aud claim
	ClockSkewSeconds int64  // 时钟偏移窗口（秒）
}

// NewTokenManager creates a new TokenManager with the given refresh function.
// ⚠️ Deprecated: 不带验签配置，新代码应调 NewTokenManagerWithVerify。
// 保留向后兼容：构造的 TokenManager.SetToken 回退到 DecodeJWTPayload（不验签）。
func NewTokenManager(refreshFn func() (string, error)) *TokenManager {
	return &TokenManager{
		refreshToken: refreshFn,
	}
}

// defaultJtiCachePath 返回 jti_cache.json 默认路径（~/.whale/bridge/jti_cache.json）。
// P0-11: persistPath 从默认路径获取（HOME 不可用时返回空字符串，降级为无持久化）。
func defaultJtiCachePath() string {
	// 收口：Dir() 返回空（home 不可得）⇒ 降级为无持久化，行为同前。
	if whalehome.Dir() == "" {
		return ""
	}
	return filepath.Join(whalehome.Dir(), "bridge", "jti_cache.json")
}

// NewTokenManagerWithVerify 是带验签配置的构造函数（v2 R1 新增）。
// 密钥来源：调用方通过 H3 secret.SafeEnv 获取环境变量，传入构造函数。
// 构造后 SetToken/RefreshToken 会调 VerifyJWT 验签（HMAC-SHA256 + claim 校验 + jti 去重）。
// P0-11 修复：JtiCache 改用持久化版（NewJtiCachePersisted），重启后仍能检测重放。
func NewTokenManagerWithVerify(refreshFn func() (string, error), opts TokenManagerOptions) *TokenManager {
	// P0-11: 使用持久化版 JtiCache 替代内存版 NewJtiCache()
	persistPath := defaultJtiCachePath()
	jtiCache := NewJtiCachePersisted(persistPath)
	// 加载历史 jti（失败不阻断，降级为空 cache）
	_ = jtiCache.Load()
	tm := &TokenManager{
		refreshToken:  refreshFn,
		verifyEnabled: true,
		verifyOpts: VerifyOptions{
			Secret:           opts.Secret,
			ExpectedIssuer:   opts.ExpectedIssuer,
			ExpectedAudience: opts.ExpectedAudience,
			ClockSkewSeconds: opts.ClockSkewSeconds,
			JtiCache:         jtiCache,
		},
	}
	return tm
}

// SetToken 设置并验证 JWT token（鉴权路径改调 VerifyJWT，v2 R1）。
// 保留原签名 SetToken(token string) error（不引入 source 参数，通知属 AuthProvider）。
// 当 verifyEnabled=true 时调 VerifyJWT 验签；否则回退 DecodeJWTPayload（向后兼容）。
// 验签失败返回带 N6 ErrBridgeJWT 的错误（02 §2.3.2 契约）。
func (m *TokenManager) SetToken(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var payload *JwtPayload
	var err error
	if m.verifyEnabled {
		payload, err = VerifyJWT(token, m.verifyOpts)
	} else {
		payload, err = DecodeJWTPayload(token)
		if err != nil {
			return fmt.Errorf("invalid token: %w", err)
		}
	}
	if err != nil {
		return err
	}

	m.token = token
	m.payload = payload
	m.scheduleRefresh()
	return nil
}

// GetToken returns the current token string
func (m *TokenManager) GetToken() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token
}

// RefreshToken 刷新 token 后调 SetToken 验签（v2 R1/R5）。
// 刷新回调 refreshToken() 返回新 token，经 SetToken 验签+存储。
// ⚠️ v2 R5：用新 token 验签，非重验旧 token。
func (m *TokenManager) RefreshToken() error {
	m.mu.Lock()
	if m.refreshToken == nil {
		m.mu.Unlock()
		return fmt.Errorf("no refresh function configured")
	}
	refreshFn := m.refreshToken
	m.mu.Unlock()

	newToken, err := refreshFn()
	if err != nil {
		return fmt.Errorf("refresh failed: %w", err)
	}

	// 用新 token 调 SetToken 验签+存储（R5：非重验旧 token）
	if err := m.SetToken(newToken); err != nil {
		return err
	}

	m.mu.Lock()
	onRefreshed := m.onRefreshed
	m.mu.Unlock()

	if onRefreshed != nil {
		onRefreshed(newToken)
	}
	return nil
}

// SetOnRefreshed sets the callback invoked after successful token refresh
func (m *TokenManager) SetOnRefreshed(fn func(string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onRefreshed = fn
}

// Stop cancels the scheduled refresh timer
func (m *TokenManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
}

// scheduleRefresh schedules automatic refresh 5 minutes before token expiration
func (m *TokenManager) scheduleRefresh() {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	if m.payload == nil || m.payload.Exp == 0 {
		return
	}

	expiresAt := time.Unix(m.payload.Exp, 0)
	refreshAt := expiresAt.Add(-5 * time.Minute)
	now := time.Now()

	var delay time.Duration
	if now.Before(refreshAt) {
		delay = time.Until(refreshAt)
	} else {
		delay = 0
	}

	m.timer = time.AfterFunc(delay, func() {
		m.RefreshToken()
	})
}
