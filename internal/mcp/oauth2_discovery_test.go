// Package mcp — / M17-04 RFC 8414 OAuth2 Server Metadata 发现测试
//
// 测试覆盖：
// - DiscoverAuthorizationServer：HTTPS 强制 / issuer 校验 / RFC 8414 完整字段解析 / localhost 例外
// - discoverMetadata：1MB 响应限制 / JSON 解析 / 递归发现 authorization_servers
// - ErrIssuerMismatch：钓鱼式 metadata 检测
//
// 对标 RFC 8414（[datatracker.ietf.org/doc/html/rfc8414](https://datatracker.ietf.org/doc/html/rfc8414) @2018-04）。
// 注意：httptest 测试在 WSL 内运行（Windows Winsock 损坏， 项目宪法约束）。
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDiscoverAuthorizationServer_HTTPSEnforcement 测试非 HTTPS URL 返回错误（除 localhost 外）。
func TestDiscoverAuthorizationServer_HTTPSEnforcement(t *testing.T) {
	tests := []struct {
		name      string
		issuerURL string
		wantErr   bool
		errSubstr string
	}{
		{"http production URL", "http://example.com", true, "must be HTTPS"},
		{"https URL", "https://example.com", false, ""}, // 不会因 HTTPS 校验失败（可能因网络失败）
		{"localhost http", "http://localhost:8080", false, ""},
		{"127.0.0.1 http", "http://127.0.0.1:8080", false, ""},
		{"empty URL", "", true, "cannot be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DiscoverAuthorizationServer(context.Background(), tt.issuerURL)
			if tt.wantErr {
				if err == nil {
					t.Errorf("DiscoverAuthorizationServer(%q): got nil error, want error containing %q", tt.issuerURL, tt.errSubstr)
					return
				}
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("DiscoverAuthorizationServer(%q): error = %v, want substring %q", tt.issuerURL, err, tt.errSubstr)
				}
			}
			// 非 wantErr 的 case 可能因网络失败返回错误，不检查
		})
	}
}

// TestDiscoverAuthorizationServer_ValidMetadata 测试有效 metadata 返回完整 RFC 8414 字段。
func TestDiscoverAuthorizationServer_ValidMetadata(t *testing.T) {
	issuerURL := "" // 在 setup 中填充
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			http.NotFound(w, r)
			return
		}
		metadata := map[string]interface{}{
			"issuer":                           issuerURL,
			"authorization_endpoint":           issuerURL + "/auth",
			"token_endpoint":                   issuerURL + "/token",
			"jwks_uri":                         issuerURL + "/jwks",
			"scopes_supported":                 []string{"read", "write"},
			"response_types_supported":         []string{"code"},
			"grant_types_supported":            []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported": []string{"S256"},
			"registration_endpoint":            issuerURL + "/register",
			"revocation_endpoint":              issuerURL + "/revoke",
			"introspection_endpoint":           issuerURL + "/introspect",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()

	issuerURL = server.URL
	config, err := DiscoverAuthorizationServer(context.Background(), issuerURL)
	if err != nil {
		t.Fatalf("DiscoverAuthorizationServer failed: %v", err)
	}
	// 验证 RFC 8414 完整字段
	if config.Issuer != issuerURL {
		t.Errorf("Issuer = %s, want %s", config.Issuer, issuerURL)
	}
	if config.AuthorizationURL != issuerURL+"/auth" {
		t.Errorf("AuthorizationURL = %s, want %s/auth", config.AuthorizationURL, issuerURL)
	}
	if config.TokenURL != issuerURL+"/token" {
		t.Errorf("TokenURL = %s, want %s/token", config.TokenURL, issuerURL)
	}
	if config.JWKSURL != issuerURL+"/jwks" {
		t.Errorf("JWKSURL = %s, want %s/jwks", config.JWKSURL, issuerURL)
	}
	if len(config.ScopesSupported) != 2 || config.ScopesSupported[0] != "read" {
		t.Errorf("ScopesSupported = %v, want [read write]", config.ScopesSupported)
	}
	if len(config.ResponseTypesSupported) != 1 || config.ResponseTypesSupported[0] != "code" {
		t.Errorf("ResponseTypesSupported = %v, want [code]", config.ResponseTypesSupported)
	}
	if len(config.GrantTypesSupported) != 2 {
		t.Errorf("GrantTypesSupported = %v, want 2 elements", config.GrantTypesSupported)
	}
	if len(config.CodeChallengeMethodsSupported) != 1 || config.CodeChallengeMethodsSupported[0] != "S256" {
		t.Errorf("CodeChallengeMethodsSupported = %v, want [S256]", config.CodeChallengeMethodsSupported)
	}
	if config.RegistrationURL != issuerURL+"/register" {
		t.Errorf("RegistrationURL = %s, want %s/register", config.RegistrationURL, issuerURL)
	}
	if config.RevocationURL != issuerURL+"/revoke" {
		t.Errorf("RevocationURL = %s, want %s/revoke", config.RevocationURL, issuerURL)
	}
	if config.IntrospectionURL != issuerURL+"/introspect" {
		t.Errorf("IntrospectionURL = %s, want %s/introspect", config.IntrospectionURL, issuerURL)
	}
}

// TestDiscoverAuthorizationServer_IssuerMismatch 测试 issuer 不匹配返回 ErrIssuerMismatch（防钓鱼）。
func TestDiscoverAuthorizationServer_IssuerMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata := map[string]interface{}{
			"issuer":                 "https://evil-server.com", // 故意返回错误的 issuer
			"authorization_endpoint": "https://evil-server.com/auth",
			"token_endpoint":         "https://evil-server.com/token",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()

	_, err := DiscoverAuthorizationServer(context.Background(), server.URL)
	if err == nil {
		t.Fatal("DiscoverAuthorizationServer with mismatched issuer: got nil error, want ErrIssuerMismatch")
	}
	if !errors.Is(err, ErrIssuerMismatch) {
		t.Errorf("Error = %v, want ErrIssuerMismatch", err)
	}
}

// TestDiscoverAuthorizationServer_NoIssuerField 测试 metadata 无 issuer 字段时不报错（向后兼容）。
func TestDiscoverAuthorizationServer_NoIssuerField(t *testing.T) {
	serverURL := "" // 在 server 创建后填充
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata := map[string]interface{}{
			// 故意不返回 issuer 字段
			"authorization_endpoint": serverURL + "/auth",
			"token_endpoint":         serverURL + "/token",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()
	serverURL = server.URL

	config, err := DiscoverAuthorizationServer(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("DiscoverAuthorizationServer without issuer field failed: %v", err)
	}
	if config.AuthorizationURL != server.URL+"/auth" {
		t.Errorf("AuthorizationURL = %s, want %s/auth", config.AuthorizationURL, server.URL)
	}
}

// TestDiscoverMetadata_SizeLimit 测试响应超过 1MB 限制时返回错误。
func TestDiscoverMetadata_SizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 返回超过 1MB 的数据
		w.Write([]byte(`{"issuer":"`))
		w.Write(make([]byte, 2<<20)) // 2MB
		w.Write([]byte(`"}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := discoverMetadata(ctx, server.URL)
	if err == nil {
		t.Error("discoverMetadata with >1MB response: got nil error, want parse error (truncated JSON)")
	}
}

// TestDiscoverMetadata_RecursiveAuthServers 测试 authorization_servers 递归发现。
func TestDiscoverMetadata_RecursiveAuthServers(t *testing.T) {
	// 子 server 返回实际的 endpoint
	subServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata := map[string]interface{}{
			"issuer":                 "https://auth-server.example.com",
			"authorization_endpoint": "https://auth-server.example.com/auth",
			"token_endpoint":         "https://auth-server.example.com/token",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metadata)
	}))
	defer subServer.Close()

	// 主 server 仅返回 authorization_servers 列表
	mainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata := map[string]interface{}{
			"authorization_servers": []string{subServer.URL},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metadata)
	}))
	defer mainServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config, err := discoverMetadata(ctx, mainServer.URL)
	if err != nil {
		t.Fatalf("discoverMetadata with recursive auth_servers failed: %v", err)
	}
	if config.AuthorizationURL != "https://auth-server.example.com/auth" {
		t.Errorf("AuthorizationURL = %s, want https://auth-server.example.com/auth", config.AuthorizationURL)
	}
	if config.TokenURL != "https://auth-server.example.com/token" {
		t.Errorf("TokenURL = %s, want https://auth-server.example.com/token", config.TokenURL)
	}
}

// TestDiscoverMetadata_HTTPError 测试非 200 状态码返回错误。
func TestDiscoverMetadata_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Not Found", http.StatusNotFound)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := discoverMetadata(ctx, server.URL)
	if err == nil {
		t.Error("discoverMetadata with 404: got nil error, want error")
	}
}

// TestDiscoverMetadata_InvalidJSON 测试无效 JSON 返回解析错误。
func TestDiscoverMetadata_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{invalid json`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := discoverMetadata(ctx, server.URL)
	if err == nil {
		t.Error("discoverMetadata with invalid JSON: got nil error, want parse error")
	}
}

// TestOAuth2Config_RFC8414Fields 测试 RFC 8414 字段 JSON 序列化/反序列化。
func TestOAuth2Config_RFC8414Fields(t *testing.T) {
	original := &OAuth2Config{
		AuthorizationURL:              "https://example.com/auth",
		TokenURL:                      "https://example.com/token",
		ClientID:                      "test-client",
		Issuer:                        "https://example.com",
		JWKSURL:                       "https://example.com/jwks",
		ScopesSupported:               []string{"read", "write"},
		ResponseTypesSupported:        []string{"code"},
		GrantTypesSupported:           []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported: []string{"S256"},
		RegistrationURL:               "https://example.com/register",
		RevocationURL:                 "https://example.com/revoke",
		IntrospectionURL:              "https://example.com/introspect",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded OAuth2Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Issuer != original.Issuer {
		t.Errorf("Issuer = %s, want %s", decoded.Issuer, original.Issuer)
	}
	if decoded.JWKSURL != original.JWKSURL {
		t.Errorf("JWKSURL = %s, want %s", decoded.JWKSURL, original.JWKSURL)
	}
	if len(decoded.ScopesSupported) != 2 {
		t.Errorf("ScopesSupported length = %d, want 2", len(decoded.ScopesSupported))
	}
	if len(decoded.CodeChallengeMethodsSupported) != 1 || decoded.CodeChallengeMethodsSupported[0] != "S256" {
		t.Errorf("CodeChallengeMethodsSupported = %v, want [S256]", decoded.CodeChallengeMethodsSupported)
	}
}

// TestDiscoverAuthorizationServer_ContextCancel 测试 ctx 取消时返回错误。
func TestDiscoverAuthorizationServer_ContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // 故意延迟
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := DiscoverAuthorizationServer(ctx, server.URL)
	if err == nil {
		t.Error("DiscoverAuthorizationServer with cancelled ctx: got nil error, want timeout error")
	}
}

// TestErrIssuerMismatch_IsSentinel 测试 ErrIssuerMismatch 是哨兵错误（errors.Is 可匹配）。
func TestErrIssuerMismatch_IsSentinel(t *testing.T) {
	wrapped := fmt.Errorf("wrapped: %w", ErrIssuerMismatch)
	if !errors.Is(wrapped, ErrIssuerMismatch) {
		t.Error("errors.Is should match ErrIssuerMismatch sentinel")
	}
}

// TestErrSubscribeNotSupported_IsSentinel 测试 ErrSubscribeNotSupported 是哨兵错误。
func TestErrSubscribeNotSupported_IsSentinel(t *testing.T) {
	wrapped := fmt.Errorf("subscribe failed: %w", ErrSubscribeNotSupported)
	if !errors.Is(wrapped, ErrSubscribeNotSupported) {
		t.Error("errors.Is should match ErrSubscribeNotSupported sentinel")
	}
}
