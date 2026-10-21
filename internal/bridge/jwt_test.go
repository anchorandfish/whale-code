package bridge

import (
	"encoding/json"
	"testing"
	"time"
)

// createTestJWT creates a test JWT token
// payload 为要编码的 payload 对象，返回完整的 JWT 字符串
func createTestJWT(payload map[string]interface{}) string {
	// Header: {"alg":"HS256","typ":"JWT"}
	header := map[string]interface{}{
		"alg": "HS256",
		"typ": "JWT",
	}
	headerBytes, _ := json.Marshal(header)
	payloadBytes, _ := json.Marshal(payload)

	headerPart := base64urlEncode(headerBytes)
	payloadPart := base64urlEncode(payloadBytes)
	// 签名部分使用假值，因为我们不验证签名
	signaturePart := base64urlEncode([]byte("fake-signature"))

	return headerPart + "." + payloadPart + "." + signaturePart
}

// createTestJWTWithExpiry 创建带有指定 exp claim 的测试 JWT
// exp 为 Unix 时间戳
func createTestJWTWithExpiry(exp int64) string {
	return createTestJWT(map[string]interface{}{
		"sub":   "test-user",
		"iss":   "test-issuer",
		"aud":   "test-audience",
		"exp":   exp,
		"iat":   time.Now().Unix(),
		"scope": "read write",
	})
}

// createTestJWTExpiringIn 创建在指定时长后过期的测试 JWT
// duration 为距离过期的时长
func createTestJWTExpiringIn(duration time.Duration) string {
	return createTestJWTWithExpiry(time.Now().Add(duration).Unix())
}

// TestDecodeJWTPayload_ValidToken 测试解码有效的 JWT token
func TestDecodeJWTPayload_ValidToken(t *testing.T) {
	token := createTestJWT(map[string]interface{}{
		"sub":    "user123",
		"iss":    "my-issuer",
		"aud":    "my-audience",
		"exp":    1735689600,
		"iat":    1735686000,
		"nbf":    1735686000,
		"jti":    "unique-id-123",
		"scope":  "read write",
		"region": "us-west-2",
	})

	payload, err := DecodeJWTPayload(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if payload.Sub != "user123" {
		t.Errorf("expected sub=user123, got %s", payload.Sub)
	}
	if payload.Iss != "my-issuer" {
		t.Errorf("expected iss=my-issuer, got %s", payload.Iss)
	}
	if payload.Aud != "my-audience" {
		t.Errorf("expected aud=my-audience, got %s", payload.Aud)
	}
	if payload.Exp != 1735689600 {
		t.Errorf("expected exp=1735689600, got %d", payload.Exp)
	}
	if payload.Iat != 1735686000 {
		t.Errorf("expected iat=1735686000, got %d", payload.Iat)
	}
	if payload.Nbf != 1735686000 {
		t.Errorf("expected nbf=1735686000, got %d", payload.Nbf)
	}
	if payload.Jti != "unique-id-123" {
		t.Errorf("expected jti=unique-id-123, got %s", payload.Jti)
	}
	if payload.Scope != "read write" {
		t.Errorf("expected scope='read write', got %s", payload.Scope)
	}
	if payload.Region != "us-west-2" {
		t.Errorf("expected region=us-west-2, got %s", payload.Region)
	}
}

// TestDecodeJWTPayload_ExpiredToken 测试解码已过期的 JWT token
// 过期的 token 仍然可以解码（不验证签名），只是 exp 已经过去
func TestDecodeJWTPayload_ExpiredToken(t *testing.T) {
	token := createTestJWTWithExpiry(time.Now().Add(-1 * time.Hour).Unix())

	payload, err := DecodeJWTPayload(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if payload.Exp == 0 {
		t.Error("expected exp to be set")
	}
	// expiration time应该在过去
	if time.Unix(payload.Exp, 0).After(time.Now()) {
		t.Error("expected exp to be in the past")
	}
}

// TestDecodeJWTPayload_MalformedToken 测试解码格式错误的 token
func TestDecodeJWTPayload_MalformedToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"empty string", ""},
		{"single part", "onlyonepart"},
		{"two parts", "part1.part2"},
		{"four parts", "a.b.c.d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeJWTPayload(tt.token)
			if err == nil {
				t.Error("expected error for malformed token")
			}
		})
	}
}

// TestDecodeJWTPayload_PrefixStripping 测试解码时自动剥离前缀
func TestDecodeJWTPayload_PrefixStripping(t *testing.T) {
	baseToken := createTestJWT(map[string]interface{}{
		"sub": "prefix-test",
		"exp": time.Now().Add(1 * time.Hour).Unix(),
	})

	tests := []struct {
		name   string
		prefix string
	}{
		{"sk-ant-si prefix", "sk-ant-si-"},
		{"sk-bailian prefix", "sk-bailian-"},
		{"Bearer prefix", "Bearer "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefixedToken := tt.prefix + baseToken
			payload, err := DecodeJWTPayload(prefixedToken)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if payload.Sub != "prefix-test" {
				t.Errorf("expected sub=prefix-test, got %s", payload.Sub)
			}
		})
	}
}

// TestDecodeJWTPayload_EmptyPayload 测试解码空 payload 的 JWT
func TestDecodeJWTPayload_EmptyPayload(t *testing.T) {
	token := createTestJWT(map[string]interface{}{})

	payload, err := DecodeJWTPayload(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if payload.Sub != "" || payload.Exp != 0 {
		t.Error("expected empty payload fields")
	}
}

// TestDecodeJWTPayload_InvalidBase64 测试解码 Base64 无效的 payload
func TestDecodeJWTPayload_InvalidBase64(t *testing.T) {
	// 构造一个 payload 部分不是有效 base64 的 token
	token := "eyJhbGciOiJIUzI1NiJ9.!!!invalid-base64!!!.fake-signature"

	_, err := DecodeJWTPayload(token)
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

// TestDecodeJWTPayload_InvalidJSON 测试解码 payload 为无效 JSON 的 token
func TestDecodeJWTPayload_InvalidJSON(t *testing.T) {
	// 构造一个 payload 部分是有效 base64 但不是有效 JSON 的 token
	header := base64urlEncode([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64urlEncode([]byte("not-json-at-all"))
	signature := base64urlEncode([]byte("fake-signature"))
	token := header + "." + payload + "." + signature

	_, err := DecodeJWTPayload(token)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// TestDecodeJWTExpiry_ValidExp 测试从有效 token 中提取 exp
func TestDecodeJWTExpiry_ValidExp(t *testing.T) {
	expectedExp := time.Now().Add(1 * time.Hour).Unix()
	token := createTestJWTWithExpiry(expectedExp)

	exp, err := DecodeJWTExpiry(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exp != expectedExp {
		t.Errorf("expected exp=%d, got %d", expectedExp, exp)
	}
}

// TestDecodeJWTExpiry_MissingExp 测试从无 exp claim 的 token 中提取expiration time
func TestDecodeJWTExpiry_MissingExp(t *testing.T) {
	token := createTestJWT(map[string]interface{}{
		"sub": "no-exp-user",
	})

	_, err := DecodeJWTExpiry(token)
	if err == nil {
		t.Error("expected error for missing exp claim")
	}
}

// TestDecodeJWTExpiry_InvalidToken 测试从无效 token 中提取expiration time
func TestDecodeJWTExpiry_InvalidToken(t *testing.T) {
	_, err := DecodeJWTExpiry("not-a-jwt")
	if err == nil {
		t.Error("expected error for invalid token")
	}
}

// TestStripTokenPrefix 测试剥离已知前缀（表驱动测试）
func TestStripTokenPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"sk-ant-si prefix", "sk-ant-si-abc123", "abc123"},
		{"sk-bailian prefix", "sk-bailian-xyz789", "xyz789"},
		{"Bearer prefix", "Bearer token123", "token123"},
		{"no prefix", "plain-token", "plain-token"},
		{"empty string", "", ""},
		{"prefix only", "sk-ant-si-", ""},
		{"Bearer with space", "Bearer  double-space", " double-space"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripTokenPrefix(tt.input)
			if result != tt.expected {
				t.Errorf("StripTokenPrefix(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestMaskToken 测试 Token 脱敏处理（表驱动测试）
func TestMaskToken(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"long token", "abcdefghij1234567890", "abcd***7890"},
		{"exactly 9 chars", "abcdefghi", "abcd***fghi"},
		{"8 chars", "abcdefgh", "***"},
		{"4 chars", "abcd", "***"},
		{"1 char", "a", "***"},
		{"empty string", "", "***"},
		{"10 chars", "abcdefghij", "abcd***ghij"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MaskToken(tt.input)
			if result != tt.expected {
				t.Errorf("MaskToken(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
