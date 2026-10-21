package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOAuth2Token_IsExpired 测试 OAuth2 token过期检测
func TestOAuth2Token_IsExpired(t *testing.T) {
	t.Run("未过期的token", func(t *testing.T) {
		token := OAuth2Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			ExpiresAt:   time.Now().Add(1 * time.Hour),
		}
		if token.IsExpired() {
			t.Error("未过期的token不应报告为已过期")
		}
	})

	t.Run("已过期的token", func(t *testing.T) {
		token := OAuth2Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			ExpiresAt:   time.Now().Add(-1 * time.Hour),
		}
		if !token.IsExpired() {
			t.Error("已过期的token应报告为已过期")
		}
	})

	t.Run("即将过期的token（30秒内）", func(t *testing.T) {
		token := OAuth2Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			ExpiresAt:   time.Now().Add(10 * time.Second),
		}
		if !token.IsExpired() {
			t.Error("即将过期的token（30秒内）应报告为已过期")
		}
	})

	t.Run("零值expiration time的token", func(t *testing.T) {
		token := OAuth2Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
		}
		if !token.IsExpired() {
			t.Error("零值expiration time的token应报告为已过期")
		}
	})
}

// TestOAuth2Token_HasRefreshToken 测试refresh token检测
func TestOAuth2Token_HasRefreshToken(t *testing.T) {
	t.Run("有refresh token", func(t *testing.T) {
		token := OAuth2Token{
			AccessToken:  "test-token",
			RefreshToken: "refresh-token",
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
		}
		if !token.HasRefreshToken() {
			t.Error("有refresh token时should return true")
		}
	})

	t.Run("无refresh token", func(t *testing.T) {
		token := OAuth2Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			ExpiresAt:   time.Now().Add(1 * time.Hour),
		}
		if token.HasRefreshToken() {
			t.Error("无refresh token时should return false")
		}
	})
}

// TestParseWWWAuthenticate 测试 WWW-Authenticate 头解析
func TestParseWWWAuthenticate(t *testing.T) {
	tests := []struct {
		name         string
		header       string
		wantAuthURL  string
		wantTokenURL string
		wantClientID string
		wantErr      bool
	}{
		{
			name:         "标准 Bearer 头",
			header:       `Bearer authorization_url="https://auth.example.com/authorize", token_url="https://auth.example.com/token", client_id="test-client"`,
			wantAuthURL:  "https://auth.example.com/authorize",
			wantTokenURL: "https://auth.example.com/token",
			wantClientID: "test-client",
		},
		{
			name:    "缺少 authorization_url",
			header:  `Bearer token_url="https://auth.example.com/token", client_id="test-client"`,
			wantErr: true,
		},
		{
			name:    "空头",
			header:  "",
			wantErr: true,
		},
		{
			name:    "非 Bearer 头",
			header:  `Basic realm="test"`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := ParseWWWAuthenticate(tt.header)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseWWWAuthenticate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if config.AuthorizationURL != tt.wantAuthURL {
				t.Errorf("AuthorizationURL = %s, expected %s", config.AuthorizationURL, tt.wantAuthURL)
			}
			if config.TokenURL != tt.wantTokenURL {
				t.Errorf("TokenURL = %s, expected %s", config.TokenURL, tt.wantTokenURL)
			}
			if config.ClientID != tt.wantClientID {
				t.Errorf("ClientID = %s, expected %s", config.ClientID, tt.wantClientID)
			}
		})
	}
}

// TestOAuth2Config_AuthorizationURL 测试授权 URL 生成
func TestOAuth2Config_AuthorizationURL(t *testing.T) {
	config := OAuth2Config{
		AuthorizationURL: "https://auth.example.com/authorize",
		TokenURL:         "https://auth.example.com/token",
		ClientID:         "test-client",
		Scopes:           []string{"mcp", "profile"},
	}

	t.Run("生成带 state 的授权 URL", func(t *testing.T) {
		url, state, err := config.BuildAuthorizationURL("https://localhost:9090/callback")
		if err != nil {
			t.Fatalf("AuthorizationURL 返回错误: %v", err)
		}

		if !strings.Contains(url, "response_type=code") {
			t.Error("授权 URL should contain response_type=code")
		}
		if !strings.Contains(url, "client_id=test-client") {
			t.Error("授权 URL should contain client_id=test-client")
		}
		if !strings.Contains(url, "redirect_uri=") {
			t.Error("授权 URL should contain redirect_uri")
		}
		if !strings.Contains(url, "scope=mcp+profile") {
			t.Error("授权 URL should contain scope=mcp+profile")
		}
		if !strings.Contains(url, "state=") {
			t.Error("授权 URL should contain state")
		}
		if state == "" {
			t.Error("state should not be empty")
		}
	})

	t.Run("state 每次不同", func(t *testing.T) {
		_, state1, _ := config.BuildAuthorizationURL("https://localhost:9090/callback")
		_, state2, _ := config.BuildAuthorizationURL("https://localhost:9090/callback")
		if state1 == state2 {
			t.Error("每次生成的 state 应不同")
		}
	})
}

// TestOAuth2TokenStorage 测试 OAuth2 token存储
func TestOAuth2TokenStorage(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("保存和加载token", func(t *testing.T) {
		token := OAuth2Token{
			AccessToken:  "access-token-123",
			RefreshToken: "refresh-token-456",
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
		}

		// 保存token
		err := SaveOAuth2Token(tmpDir, "test-server", &token)
		if err != nil {
			t.Fatalf("SaveOAuth2Token 失败: %v", err)
		}

		// 验证文件存在
		tokenPath := filepath.Join(tmpDir, "oauth", "test-server.json")
		if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
			t.Errorf("token文件应存在于 %s", tokenPath)
		}

		// 加载token
		loaded, err := LoadOAuth2Token(tmpDir, "test-server")
		if err != nil {
			t.Fatalf("LoadOAuth2Token 失败: %v", err)
		}

		if loaded.AccessToken != token.AccessToken {
			t.Errorf("AccessToken = %s, expected %s", loaded.AccessToken, token.AccessToken)
		}
		if loaded.RefreshToken != token.RefreshToken {
			t.Errorf("RefreshToken = %s, expected %s", loaded.RefreshToken, token.RefreshToken)
		}
		if loaded.TokenType != token.TokenType {
			t.Errorf("TokenType = %s, expected %s", loaded.TokenType, token.TokenType)
		}
	})

	t.Run("加载不存在的token", func(t *testing.T) {
		_, err := LoadOAuth2Token(tmpDir, "nonexistent")
		if err == nil {
			t.Error("加载不存在的tokenshould return error")
		}
	})

	t.Run("删除token", func(t *testing.T) {
		token := OAuth2Token{
			AccessToken: "to-delete",
			TokenType:   "Bearer",
			ExpiresAt:   time.Now().Add(1 * time.Hour),
		}

		SaveOAuth2Token(tmpDir, "delete-me", &token)
		err := DeleteOAuth2Token(tmpDir, "delete-me")
		if err != nil {
			t.Fatalf("DeleteOAuth2Token 失败: %v", err)
		}

		_, err = LoadOAuth2Token(tmpDir, "delete-me")
		if err == nil {
			t.Error("删除post-loadingshould return error")
		}
	})
}

// TestOAuth2Config_Serialization 测试 OAuth2Config 序列化
func TestOAuth2Config_Serialization(t *testing.T) {
	config := OAuth2Config{
		AuthorizationURL: "https://auth.example.com/authorize",
		TokenURL:         "https://auth.example.com/token",
		ClientID:         "test-client",
		ClientSecret:     "test-secret",
		Scopes:           []string{"mcp", "profile"},
	}

	// 序列化
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("serialization failed: %v", err)
	}

	// 反序列化
	var decoded OAuth2Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}

	if decoded.AuthorizationURL != config.AuthorizationURL {
		t.Errorf("AuthorizationURL = %s, expected %s", decoded.AuthorizationURL, config.AuthorizationURL)
	}
	if decoded.ClientID != config.ClientID {
		t.Errorf("ClientID = %s, expected %s", decoded.ClientID, config.ClientID)
	}
	if len(decoded.Scopes) != len(config.Scopes) {
		t.Errorf("Scopes 数量 = %d, expected %d", len(decoded.Scopes), len(config.Scopes))
	}
}

// TestOAuth2Token_Serialization 测试 OAuth2Token 序列化
func TestOAuth2Token_Serialization(t *testing.T) {
	token := OAuth2Token{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		TokenType:    "Bearer",
		ExpiresAt:    time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(token)
	if err != nil {
		t.Fatalf("serialization failed: %v", err)
	}

	var decoded OAuth2Token
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}

	if decoded.AccessToken != token.AccessToken {
		t.Errorf("AccessToken = %s, expected %s", decoded.AccessToken, token.AccessToken)
	}
}

// TestDetectOAuth2FromResponse 测试从 HTTP 响应检测 OAuth2 requirement
func TestDetectOAuth2FromResponse(t *testing.T) {
	t.Run("401 带 WWW-Authenticate 头", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header: http.Header{
				"Www-Authenticate": []string{`Bearer authorization_url="https://auth.example.com/authorize", token_url="https://auth.example.com/token", client_id="test-client"`},
			},
		}

		config, err := DetectOAuth2FromResponse(resp)
		if err != nil {
			t.Fatalf("DetectOAuth2FromResponse 失败: %v", err)
		}
		if config == nil {
			t.Fatal("should return OAuth2Config")
		}
		if config.AuthorizationURL != "https://auth.example.com/authorize" {
			t.Errorf("AuthorizationURL = %s, expected https://auth.example.com/authorize", config.AuthorizationURL)
		}
	})

	t.Run("401 无 WWW-Authenticate 头", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{},
		}

		config, err := DetectOAuth2FromResponse(resp)
		if err == nil {
			t.Error("无 WWW-Authenticate 头should return error")
		}
		if config != nil {
			t.Error("should return nil config")
		}
	})

	t.Run("非 401 状态码", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
		}

		config, err := DetectOAuth2FromResponse(resp)
		if err == nil {
			t.Error("非 401 状态码should return error")
		}
		if config != nil {
			t.Error("should return nil config")
		}
	})
}

// TestOAuth2TokenPath 测试token文件路径生成
func TestOAuth2TokenPath(t *testing.T) {
	path := OAuth2TokenPath("/home/user/.whale", "my-server")

	expected := filepath.Join("/home/user/.whale", "oauth", "my-server.json")
	if path != expected {
		t.Errorf("OAuth2TokenPath = %s, expected %s", path, expected)
	}
}

// TestOAuth2Config_Validate 测试 OAuth2Config 验证
func TestOAuth2Config_Validate(t *testing.T) {
	t.Run("有效配置", func(t *testing.T) {
		config := OAuth2Config{
			AuthorizationURL: "https://auth.example.com/authorize",
			TokenURL:         "https://auth.example.com/token",
			ClientID:         "test-client",
		}
		if err := config.Validate(); err != nil {
			t.Errorf("有效配置不should return error: %v", err)
		}
	})

	t.Run("缺少 AuthorizationURL", func(t *testing.T) {
		config := OAuth2Config{
			TokenURL: "https://auth.example.com/token",
			ClientID: "test-client",
		}
		if err := config.Validate(); err == nil {
			t.Error("缺少 AuthorizationURL should return error")
		}
	})

	t.Run("缺少 TokenURL", func(t *testing.T) {
		config := OAuth2Config{
			AuthorizationURL: "https://auth.example.com/authorize",
			ClientID:         "test-client",
		}
		if err := config.Validate(); err == nil {
			t.Error("缺少 TokenURL should return error")
		}
	})

	t.Run("缺少 ClientID", func(t *testing.T) {
		config := OAuth2Config{
			AuthorizationURL: "https://auth.example.com/authorize",
			TokenURL:         "https://auth.example.com/token",
		}
		if err := config.Validate(); err == nil {
			t.Error("缺少 ClientID should return error")
		}
	})
}

// TestOAuth2Config_AuthorizationURL_NoScopes 测试无 scope 的授权 URL
func TestOAuth2Config_AuthorizationURL_NoScopes(t *testing.T) {
	config := OAuth2Config{
		AuthorizationURL: "https://auth.example.com/authorize",
		TokenURL:         "https://auth.example.com/token",
		ClientID:         "test-client",
	}

	url, _, err := config.BuildAuthorizationURL("https://localhost:9090/callback")
	if err != nil {
		t.Fatalf("AuthorizationURL 返回错误: %v", err)
	}

	if strings.Contains(url, "scope=") {
		t.Error("无 scope 时授权 URL should not contain scope 参数")
	}

	_ = fmt.Sprintf("URL: %s", url)
}
