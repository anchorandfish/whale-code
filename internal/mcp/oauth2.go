package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// oauthHTTPClient 是 OAuth2 metadata 发现专用的带超时 HTTP client（30s）。
// 替代 http.DefaultClient（无超时），防止恶意 server 让请求挂起（M-1 修复）。
var oauthHTTPClient = &http.Client{Timeout: 30 * time.Second}

// OAuth2Config is the OAuth 2.0 Authorization Code Grant configuration
//
// / M17-04：扩展 RFC 8414 OAuth2 Server Metadata 字段（[datatracker.ietf.org/doc/html/rfc8414](https://datatracker.ietf.org/doc/html/rfc8414) @2018-04 §2）。
// 新增字段全部 omitempty，向后兼容现有配置文件。
type OAuth2Config struct {
	AuthorizationURL string   `json:"authorization_url"`       // Authorization endpoint URL
	TokenURL         string   `json:"token_url"`               // Token endpoint URL
	ClientID         string   `json:"client_id"`               // Client ID
	ClientSecret     string   `json:"client_secret,omitempty"` // Client secret (optional)
	Scopes           []string `json:"scopes,omitempty"`        // Authorization scopes
	codeVerifier     string   // PKCE code_verifier（运行时生成，不 JSON 序列化）
	ResourceMetadata string   `json:"resource_metadata,omitempty"` // RFC 9728 protected resource metadata URL
	// / M17-04 SNAPSHOT：RFC 8414 OAuth2 Server Metadata 字段
	Issuer                        string   `json:"issuer,omitempty"`                           // RFC 8414 §2 issuer（metadata 发布方 URL，强制校验防钓鱼）
	JWKSURL                       string   `json:"jwks_uri,omitempty"`                         // RFC 8414 §2 jwks_uri（JSON Web Key Set URL，用于 ID token 验签）
	ScopesSupported               []string `json:"scopes_supported,omitempty"`                 // RFC 8414 §2 scopes_supported（server 支持的 scope 列表）
	ResponseTypesSupported        []string `json:"response_types_supported,omitempty"`         // RFC 8414 §2 response_types_supported（如 ["code"]）
	GrantTypesSupported           []string `json:"grant_types_supported,omitempty"`            // RFC 8414 §2 grant_types_supported（如 ["authorization_code","refresh_token"]）
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported,omitempty"` // RFC 8414 §2 + RFC 7636 code_challenge_methods_supported（如 ["S256"]）
	RegistrationURL               string   `json:"registration_endpoint,omitempty"`            // RFC 8414 §2 registration_endpoint（动态客户端注册 URL）
	RevocationURL                 string   `json:"revocation_endpoint,omitempty"`              // RFC 8414 §2 revocation_endpoint（token 撤销 URL）
	IntrospectionURL              string   `json:"introspection_endpoint,omitempty"`           // RFC 8414 §2 introspection_endpoint（token 内省 URL）
}

// OAuth2Token is an OAuth 2.0 token
type OAuth2Token struct {
	AccessToken  string    `json:"access_token"`            // Access token
	RefreshToken string    `json:"refresh_token,omitempty"` // Refresh token (optional)
	TokenType    string    `json:"token_type"`              // Token type (usually Bearer)
	ExpiresAt    time.Time `json:"expires_at"`              // Expiration time
}

// IsExpired checks if the token is expired
// Considers 30-second safety margin, tokens about to expire are also treated as expired
func (t *OAuth2Token) IsExpired() bool {
	if t.ExpiresAt.IsZero() {
		return true
	}
	return time.Now().After(t.ExpiresAt.Add(-30 * time.Second))
}

// HasRefreshToken checks if there is a refresh token
func (t *OAuth2Token) HasRefreshToken() bool {
	return t.RefreshToken != ""
}

// Validate validates whether the OAuth2 configuration is valid
func (c *OAuth2Config) Validate() error {
	if c.AuthorizationURL == "" {
		return fmt.Errorf("authorization_url cannot be empty")
	}
	if c.TokenURL == "" {
		return fmt.Errorf("token_url cannot be empty")
	}
	if c.ClientID == "" {
		return fmt.Errorf("client_id cannot be empty")
	}
	return nil
}

// BuildAuthorizationURL generates the OAuth2 authorization URL with PKCE (S256).
// FACT-MCP-020: 生成 code_verifier（43-128 字符随机串），计算 code_challenge = base64url(sha256(code_verifier))，
// 设置 code_challenge_method=S256，防止授权码拦截攻击。
// code_verifier 存储在 OAuth2Config.codeVerifier 中，供 token 交换时使用。
// Returns the complete authorization URL, state parameter, and possible error.
func (c *OAuth2Config) BuildAuthorizationURL(redirectURI string) (string, string, error) {
	if err := c.Validate(); err != nil {
		return "", "", err
	}

	state, err := generateState()
	if err != nil {
		return "", "", fmt.Errorf("generate state: %w", err)
	}

	// FACT-MCP-020: PKCE — 生成 code_verifier 并计算 code_challenge (S256)
	codeVerifier, err := generateCodeVerifier()
	if err != nil {
		return "", "", fmt.Errorf("generate PKCE code_verifier: %w", err)
	}
	c.codeVerifier = codeVerifier
	codeChallenge := computeCodeChallengeS256(codeVerifier)

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", c.ClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("state", state)
	// PKCE 参数
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")

	if len(c.Scopes) > 0 {
		params.Set("scope", strings.Join(c.Scopes, " "))
	}

	authURL, err := url.Parse(c.AuthorizationURL)
	if err != nil {
		return "", "", fmt.Errorf("parse authorization URL: %w", err)
	}
	authURL.RawQuery = params.Encode()

	return authURL.String(), state, nil
}

// generateCodeVerifier 生成符合 RFC 7636 的 PKCE code_verifier。
// 长度 43-128 字符，使用 unreserved 字符集（A-Z/a-z/0-9/-/./_/~）。
// 使用 crypto/rand 安全随机生成，防止可预测性攻击。
func generateCodeVerifier() (string, error) {
	// 生成 32 字节随机数据，base64url 编码后为 43 字符（最小合法长度）
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// computeCodeChallengeS256 按 S256 方法计算 code_challenge。
// code_challenge = base64url(sha256(code_verifier))，去除尾部填充。
func computeCodeChallengeS256(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// CodeVerifier 返回当前 PKCE code_verifier（供 token 交换时使用）。
// BuildAuthorizationURL 调用后有效；之前返回空串。
func (c *OAuth2Config) CodeVerifier() string {
	return c.codeVerifier
}

// generateState generates a random state parameter
func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ParseWWWAuthenticate parses the WWW-Authenticate response header
// Extracts OAuth2 configuration info from Bearer challenge.
// FACT-MCP-021: 支持 RFC 9728 resource_metadata 字段，当仅有 resource_metadata 而无
// authorization_url 时，通过 discoverMetadata 从该 URL 发现 endpoint。
func ParseWWWAuthenticate(header string) (*OAuth2Config, error) {
	if header == "" {
		return nil, fmt.Errorf("empty WWW-Authenticate header")
	}

	// Check if it is Bearer type
	if !strings.HasPrefix(header, "Bearer ") {
		return nil, fmt.Errorf("non-Bearer WWW-Authenticate header")
	}

	params := strings.TrimPrefix(header, "Bearer ")
	config := &OAuth2Config{}

	// Parse key="value" format parameters
	for _, part := range strings.Split(params, ",") {
		part = strings.TrimSpace(part)
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}

		key := strings.TrimSpace(kv[0])
		value := strings.Trim(strings.TrimSpace(kv[1]), "\"")

		switch key {
		case "authorization_url":
			config.AuthorizationURL = value
		case "token_url":
			config.TokenURL = value
		case "client_id":
			config.ClientID = value
		case "resource_metadata":
			// FACT-MCP-021: RFC 9728 protected resource metadata URL
			config.ResourceMetadata = value
		}
	}

	// FACT-MCP-021: 如果仅有 resource_metadata 而无 authorization_url，发现 endpoint
	if config.AuthorizationURL == "" && config.ResourceMetadata != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		discovered, err := discoverMetadata(ctx, config.ResourceMetadata)
		if err == nil && discovered != nil {
			if config.AuthorizationURL == "" {
				config.AuthorizationURL = discovered.AuthorizationURL
			}
			if config.TokenURL == "" {
				config.TokenURL = discovered.TokenURL
			}
		}
		// discoverMetadata 失败时不返回 error，允许调用方使用 ResourceMetadata 自行发现
	}

	if config.AuthorizationURL == "" {
		return nil, fmt.Errorf("missing authorization_url parameter")
	}

	return config, nil
}

// discoverMetadata 从 RFC 9728 / RFC 8414 metadata URL 发现 OAuth2 endpoint（入口函数）。
// HTTP GET metadata URL，解析 JSON 获取 authorization_servers / authorization_endpoint / token_endpoint。
// 如果仅有 authorization_servers，递归发现第一个 authorization server 的 endpoint。
// 使用 30 秒超时（通过传入 ctx 控制），防止恶意 server 让请求挂起。
//
// / M17-04：扩展解析 RFC 8414 完整字段（issuer / jwks_uri / scopes_supported 等）。
// 限制响应大小 1MB（防恶意 server 返回超大 JSON 拖垮客户端， 优化 3 吸纳）。
//
// M-2 修复：内部委托 discoverMetadataDepth，默认最大递归深度 3，
// 防止恶意 server 构造循环引用导致栈溢出。
func discoverMetadata(ctx context.Context, metadataURL string) (*OAuth2Config, error) {
	return discoverMetadataDepth(ctx, metadataURL, 3)
}

// discoverMetadataDepth 从 metadata URL 发现 OAuth2 endpoint，带递归深度限制（M-2 修复）。
// maxDepth 控制递归跟随 authorization_servers 的最大次数，每次递归自减，超限返回 error。
// 其他行为与 discoverMetadata 相同：30s 超时 / 1MB 响应限制 / RFC 8414 完整字段解析。
func discoverMetadataDepth(ctx context.Context, metadataURL string, maxDepth int) (*OAuth2Config, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create metadata request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata fetch failed with status %d", resp.StatusCode)
	}

	// 优化 3：限制响应大小 1MB（防资源耗尽攻击）
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read metadata body: %w", err)
	}

	// / M17-04：解析 RFC 8414 完整字段
	var meta struct {
		// RFC 9728 / 现有字段
		AuthorizationServers  []string `json:"authorization_servers"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		// RFC 8414 §2 完整字段
		Issuer                        string   `json:"issuer"`
		JWKSURI                       string   `json:"jwks_uri"`
		ScopesSupported               []string `json:"scopes_supported"`
		ResponseTypesSupported        []string `json:"response_types_supported"`
		GrantTypesSupported           []string `json:"grant_types_supported"`
		CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
		RegistrationEndpoint          string   `json:"registration_endpoint"`
		RevocationEndpoint            string   `json:"revocation_endpoint"`
		IntrospectionEndpoint         string   `json:"introspection_endpoint"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("parse metadata JSON: %w", err)
	}

	config := &OAuth2Config{}
	if meta.AuthorizationEndpoint != "" {
		config.AuthorizationURL = meta.AuthorizationEndpoint
	}
	if meta.TokenEndpoint != "" {
		config.TokenURL = meta.TokenEndpoint
	}
	// / M17-04：填充 RFC 8414 字段
	config.Issuer = meta.Issuer
	config.JWKSURL = meta.JWKSURI
	config.ScopesSupported = meta.ScopesSupported
	config.ResponseTypesSupported = meta.ResponseTypesSupported
	config.GrantTypesSupported = meta.GrantTypesSupported
	config.CodeChallengeMethodsSupported = meta.CodeChallengeMethodsSupported
	config.RegistrationURL = meta.RegistrationEndpoint
	config.RevocationURL = meta.RevocationEndpoint
	config.IntrospectionURL = meta.IntrospectionEndpoint

	// RFC 8414: 如果仅有 authorization_servers，递归发现第一个 authorization server
	// M-2 修复：检查递归深度，防止恶意 server 构造循环引用导致栈溢出
	if config.AuthorizationURL == "" && len(meta.AuthorizationServers) > 0 {
		if maxDepth <= 0 {
			return nil, fmt.Errorf("metadata discovery depth exceeded (possible circular reference)")
		}
		subConfig, err := discoverMetadataDepth(ctx, meta.AuthorizationServers[0], maxDepth-1)
		if err == nil && subConfig != nil {
			config.AuthorizationURL = subConfig.AuthorizationURL
			config.TokenURL = subConfig.TokenURL
			// 子配置的 RFC 8414 字段优先（更精确）
			if subConfig.Issuer != "" {
				config.Issuer = subConfig.Issuer
			}
			if subConfig.JWKSURL != "" {
				config.JWKSURL = subConfig.JWKSURL
			}
		}
	}

	return config, nil
}

// DiscoverAuthorizationServer 通过 issuer URL 发现 RFC 8414 OAuth2 Server Metadata（/ M17-04）。
//
// 端点：{issuer}/.well-known/oauth-authorization-server（RFC 8414 §3.1）
// 强制 HTTPS：http 直接返回错误（防中间人攻击）
// 强制 issuer 校验：metadata.issuer 必须 == issuerURL（防钓鱼式 metadata 攻击）
//
// 对标 RFC 8414（[datatracker.ietf.org/doc/html/rfc8414](https://datatracker.ietf.org/doc/html/rfc8414) @2018-04）：
// - §3.1：端点路径 .well-known/oauth-authorization-server
// - §2：metadata 字段 issuer / jwks_uri / scopes_supported 等完整定义
// - §3.1：issuer 必须 HTTPS（除 localhost 测试外）
//
// 降级策略：
// - HTTP URL → 返回错误（强制 HTTPS）
// - metadata.issuer != issuerURL → 返回 ErrIssuerMismatch（防钓鱼）
// - HTTP 请求失败 → 返回原始错误（调用方降级到现有 OAuth2Config）
// - 响应非 JSON / JSON 解析失败 → 返回解析错误
func DiscoverAuthorizationServer(ctx context.Context, issuerURL string) (*OAuth2Config, error) {
	if issuerURL == "" {
		return nil, fmt.Errorf("issuer URL cannot be empty")
	}
	// 强制 HTTPS（防中间人，RFC 8414 §3.1 要求）
	if !strings.HasPrefix(issuerURL, "https://") {
		// 例外：localhost 测试允许 http
		if !strings.HasPrefix(issuerURL, "http://localhost") && !strings.HasPrefix(issuerURL, "http://127.0.0.1") {
			return nil, fmt.Errorf("issuer URL must be HTTPS (got %s)", issuerURL)
		}
	}
	// 构造 .well-known 端点（RFC 8414 §3.1）
	metadataURL := strings.TrimSuffix(issuerURL, "/") + "/.well-known/oauth-authorization-server"

	config, err := discoverMetadata(ctx, metadataURL)
	if err != nil {
		return nil, fmt.Errorf("discover authorization server metadata: %w", err)
	}

	// 强制 issuer 校验（防钓鱼式 metadata 攻击）
	// RFC 8414 §2 要求 metadata.issuer 必须 == 请求的 issuer URL
	if config.Issuer != "" && config.Issuer != issuerURL {
		return nil, fmt.Errorf("%w: expected %s, got %s", ErrIssuerMismatch, issuerURL, config.Issuer)
	}
	// 如果 metadata 未返回 issuer 字段，记录但不阻塞（向后兼容旧 server）
	// 调用方应警惕未声明 issuer 的 metadata

	return config, nil
}

// DetectOAuth2FromResponse detects OAuth2 requirements from HTTP response
// When server returns 401 status code with WWW-Authenticate header, extract OAuth2 config
func DetectOAuth2FromResponse(resp *http.Response) (*OAuth2Config, error) {
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, fmt.Errorf("status code is not 401: %d", resp.StatusCode)
	}

	authHeader := resp.Header.Get("Www-Authenticate")
	if authHeader == "" {
		authHeader = resp.Header.Get("WWW-Authenticate")
	}

	if authHeader == "" {
		return nil, fmt.Errorf("401 response missing WWW-Authenticate header")
	}

	return ParseWWWAuthenticate(authHeader)
}

// OAuth2TokenPath returns the storage path of the token file
func OAuth2TokenPath(baseDir, serverName string) string {
	return filepath.Join(baseDir, "oauth", serverName+".json")
}

// SaveOAuth2Token saves OAuth2 token to file with cross-process lockfile.
// FACT-MCP-023: 写入前创建 lockfile（.token.lock），用 O_CREATE|O_EXCL 实现跨进程互斥，
// 防止多进程并发刷新 token 导致 token 失效或文件损坏。
// lockfile 持久存在作为标记（下次写入时先清理旧 lockfile 再重建）。
// Token is stored in baseDir/oauth/serverName.json
func SaveOAuth2Token(baseDir, serverName string, token *OAuth2Token) error {
	oauthDir := filepath.Join(baseDir, "oauth")
	if err := os.MkdirAll(oauthDir, 0700); err != nil {
		return fmt.Errorf("create OAuth directory: %w", err)
	}

	lockPath := filepath.Join(oauthDir, ".token.lock")

	// FACT-MCP-023: 跨进程互斥 — O_CREATE|O_EXCL 创建 lockfile
	// 若 lockfile 已存在（上次写入遗留），先清理再重建
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		// lockfile 已存在，清理后重试
		_ = os.Remove(lockPath)
		lockFile, err = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("acquire lockfile: %w", err)
		}
	}
	// 写入 lockfile 元数据（记录持有者 PID 和时间，便于调试）
	_, _ = lockFile.WriteString(fmt.Sprintf("pid=%d time=%s\n", os.Getpid(), time.Now().Format(time.RFC3339)))
	_ = lockFile.Close()

	tokenPath := OAuth2TokenPath(baseDir, serverName)
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize token: %w", err)
	}

	if err := os.WriteFile(tokenPath, data, 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}

	// 保留 lockfile 作为标记（下次写入时清理重建），不删除
	return nil
}

// LoadOAuth2Token loads OAuth2 token from file
func LoadOAuth2Token(baseDir, serverName string) (*OAuth2Token, error) {
	tokenPath := OAuth2TokenPath(baseDir, serverName)
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("read token file: %w", err)
	}

	var token OAuth2Token
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("parse token file: %w", err)
	}

	return &token, nil
}

// DeleteOAuth2Token deletes the OAuth2 token file
func DeleteOAuth2Token(baseDir, serverName string) error {
	tokenPath := OAuth2TokenPath(baseDir, serverName)
	return os.Remove(tokenPath)
}

// TokenManager manages OAuth2 token lifecycle with thread-safe refresh.
// FACT-MCP-022: 内置 httpClient 字段，所有 HTTP 请求设置 30s 超时，防止恶意 server 让请求挂起。
type TokenManager struct {
	config     *OAuth2Config
	token      *OAuth2Token
	mu         sync.Mutex
	refreshing bool
	httpClient *http.Client // FACT-MCP-022: 带超时的 HTTP client（30s）
}

// NewTokenManager creates a token manager.
// FACT-MCP-022: 初始化 httpClient 并设置 30s 超时，替代 http.DefaultClient（无超时）。
func NewTokenManager(config *OAuth2Config, token *OAuth2Token) *TokenManager {
	return &TokenManager{
		config:     config,
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// GetToken returns the current token, refreshing if necessary
func (m *TokenManager) GetToken() (*OAuth2Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token != nil && !m.token.IsExpired() {
		return m.token, nil
	}
	if m.refreshing {
		return nil, fmt.Errorf("token refresh already in progress")
	}
	if m.token != nil && m.token.HasRefreshToken() && m.config != nil {
		m.refreshing = true
		newToken, err := m.refreshToken()
		m.refreshing = false
		if err != nil {
			return m.token, fmt.Errorf("token refresh failed: %w", err)
		}
		m.token = newToken
		return m.token, nil
	}
	return m.token, fmt.Errorf("token expired and no refresh mechanism available")
}

// refreshToken performs the OAuth2 token refresh using the refresh_token grant
func (m *TokenManager) refreshToken() (*OAuth2Token, error) {
	if m.token == nil || !m.token.HasRefreshToken() || m.config == nil {
		return nil, fmt.Errorf("no refresh token or config available")
	}

	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", m.token.RefreshToken)
	data.Set("client_id", m.config.ClientID)
	if m.config.ClientSecret != "" {
		data.Set("client_secret", m.config.ClientSecret)
	}

	resp, err := m.httpClient.PostForm(m.config.TokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed with status %d", resp.StatusCode)
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token,omitempty"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}

	newToken := &OAuth2Token{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		TokenType:    result.TokenType,
	}
	if result.RefreshToken == "" {
		newToken.RefreshToken = m.token.RefreshToken
	}
	if result.ExpiresIn > 0 {
		newToken.ExpiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	}

	return newToken, nil
}

// InvalidateToken clears the cached token
func (m *TokenManager) InvalidateToken() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = nil
}
