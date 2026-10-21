package bridge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// signJWT 用 HMAC-SHA256 签名生成 JWT 字符串（测试辅助函数）。
func signJWT(payload JwtPayload, secret string) string {
	header := `{"alg":"HS256","typ":"JWT"}`
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(header))
	payloadBytes, _ := json.Marshal(payload)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signingInput := headerB64 + "." + payloadB64
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig
}

// signJWTWithWrongSecret 用错误密钥签名（测试伪造签名反例）。
func signJWTWithWrongSecret(payload JwtPayload) string {
	return signJWT(payload, "wrong-secret")
}

// TestVerifyJWT_ValidToken 验证合法 JWT 验签通过。
func TestVerifyJWT_ValidToken(t *testing.T) {
	secret := "test-secret"
	payload := JwtPayload{
		Sub: "user1",
		Iss: "test-issuer",
		Aud: "test-audience",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
		Iat: time.Now().Unix(),
		Jti: "jti-valid-001",
	}
	token := signJWT(payload, secret)

	// P0-11: 测试用空路径 NewJtiCachePersisted("") 降级为无持久化（向后兼容）
	cache := NewJtiCachePersisted("")
	opts := VerifyOptions{
		Secret:           secret,
		ExpectedIssuer:   "test-issuer",
		ExpectedAudience: "test-audience",
		ClockSkewSeconds: 30,
		JtiCache:         cache,
	}
	result, err := VerifyJWT(token, opts)
	if err != nil {
		t.Fatalf("valid token should pass, got error: %v", err)
	}
	if result.Sub != "user1" {
		t.Errorf("payload Sub = %s, want user1", result.Sub)
	}
}

// TestVerifyJWT_ForgedSignature 验证伪造签名 JWT 返回 ErrBridgeJWT（P0-1 反例）。
func TestVerifyJWT_ForgedSignature(t *testing.T) {
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}
	token := signJWTWithWrongSecret(payload)

	opts := VerifyOptions{
		Secret:           "correct-secret",
		ClockSkewSeconds: 30,
	}
	_, err := VerifyJWT(token, opts)
	if err == nil {
		t.Fatal("forged signature should fail")
	}
	if id := ai.GetErrorID(err); id != ai.ErrBridgeJWT {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeJWT)
	}
}

// TestVerifyJWT_Expired 验证过期 JWT 返回 ErrBridgeJWT（P0-2 反例）。
func TestVerifyJWT_Expired(t *testing.T) {
	secret := "test-secret"
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(-2 * time.Hour).Unix(), // 2小时前过期
	}
	token := signJWT(payload, secret)

	opts := VerifyOptions{
		Secret:           secret,
		ClockSkewSeconds: 30, // 30秒偏移窗口，仍过期
	}
	_, err := VerifyJWT(token, opts)
	if err == nil {
		t.Fatal("expired token should fail")
	}
	if id := ai.GetErrorID(err); id != ai.ErrBridgeJWT {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeJWT)
	}
}

// TestVerifyJWT_NotYetValid 验证未生效 JWT（nbf 在未来）返回 ErrBridgeJWT。
func TestVerifyJWT_NotYetValid(t *testing.T) {
	secret := "test-secret"
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
		Nbf: time.Now().Add(1 * time.Hour).Unix(), // 1小时后生效
	}
	token := signJWT(payload, secret)

	opts := VerifyOptions{
		Secret:           secret,
		ClockSkewSeconds: 30,
	}
	_, err := VerifyJWT(token, opts)
	if err == nil {
		t.Fatal("not-yet-valid token should fail")
	}
	if id := ai.GetErrorID(err); id != ai.ErrBridgeJWT {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeJWT)
	}
}

// TestVerifyJWT_IssMismatch 验证 iss 不匹配返回 ErrBridgeJWT。
func TestVerifyJWT_IssMismatch(t *testing.T) {
	secret := "test-secret"
	payload := JwtPayload{
		Sub: "user1",
		Iss: "actual-issuer",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}
	token := signJWT(payload, secret)

	opts := VerifyOptions{
		Secret:         secret,
		ExpectedIssuer: "expected-issuer",
	}
	_, err := VerifyJWT(token, opts)
	if err == nil {
		t.Fatal("iss mismatch should fail")
	}
	if id := ai.GetErrorID(err); id != ai.ErrBridgeJWT {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeJWT)
	}
}

// TestVerifyJWT_AudMismatch 验证 aud 不匹配返回 ErrBridgeJWT。
func TestVerifyJWT_AudMismatch(t *testing.T) {
	secret := "test-secret"
	payload := JwtPayload{
		Sub: "user1",
		Aud: "actual-audience",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}
	token := signJWT(payload, secret)

	opts := VerifyOptions{
		Secret:           secret,
		ExpectedAudience: "expected-audience",
	}
	_, err := VerifyJWT(token, opts)
	if err == nil {
		t.Fatal("aud mismatch should fail")
	}
	if id := ai.GetErrorID(err); id != ai.ErrBridgeJWT {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeJWT)
	}
}

// TestVerifyJWT_JtiReplay 验证 jti 重放返回 ErrBridgeJWT（P0-3 反例）。
func TestVerifyJWT_JtiReplay(t *testing.T) {
	secret := "test-secret"
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
		Jti: "jti-replay-001",
	}
	token := signJWT(payload, secret)

	// P0-11: 测试用空路径 NewJtiCachePersisted("") 降级为无持久化（向后兼容）
	cache := NewJtiCachePersisted("")
	opts := VerifyOptions{
		Secret:   secret,
		JtiCache: cache,
	}

	// 首次验签通过
	_, err := VerifyJWT(token, opts)
	if err != nil {
		t.Fatalf("first use should pass, got: %v", err)
	}

	// 第二次重放应失败
	_, err = VerifyJWT(token, opts)
	if err == nil {
		t.Fatal("replayed jti should fail")
	}
	if id := ai.GetErrorID(err); id != ai.ErrBridgeJWT {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeJWT)
	}
}

// TestVerifyJWT_InvalidFormat 验证格式错误返回 ErrBridgeJWT。
func TestVerifyJWT_InvalidFormat(t *testing.T) {
	opts := VerifyOptions{Secret: "test"}
	_, err := VerifyJWT("not.a.valid.jwt.format", opts)
	if err == nil {
		t.Fatal("invalid format should fail")
	}
	if id := ai.GetErrorID(err); id != ai.ErrBridgeJWT {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeJWT)
	}
}

// TestVerifyJWT_ClockSkewTolerance 验证时钟偏移窗口内的过期 token 仍通过。
func TestVerifyJWT_ClockSkewTolerance(t *testing.T) {
	secret := "test-secret"
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(-10 * time.Second).Unix(), // 10秒前过期
	}
	token := signJWT(payload, secret)

	opts := VerifyOptions{
		Secret:           secret,
		ClockSkewSeconds: 30, // 30秒偏移窗口，10秒 < 30秒，应通过
	}
	_, err := VerifyJWT(token, opts)
	if err != nil {
		t.Errorf("token within clock skew should pass, got: %v", err)
	}
}

// TestVerifyJWT_EmptyOptionalFields 验证 iss/aud 为空时不校验（向后兼容）。
func TestVerifyJWT_EmptyOptionalFields(t *testing.T) {
	secret := "test-secret"
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
		Iss: "any-issuer",
		Aud: "any-audience",
	}
	token := signJWT(payload, secret)

	// ExpectedIssuer/ExpectedAudience 为空 → 不校验
	opts := VerifyOptions{
		Secret:           secret,
		ClockSkewSeconds: 30,
	}
	_, err := VerifyJWT(token, opts)
	if err != nil {
		t.Errorf("empty optional fields should not cause failure: %v", err)
	}
}

// TestTokenManager_SetToken_VerifyEnabled 验证 NewTokenManagerWithVerify 构造的 TokenManager 验签。
func TestTokenManager_SetToken_VerifyEnabled(t *testing.T) {
	secret := "test-secret"
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}
	token := signJWT(payload, secret)

	tm := NewTokenManagerWithVerify(nil, TokenManagerOptions{
		Secret:           secret,
		ClockSkewSeconds: 30,
	})
	if err := tm.SetToken(token); err != nil {
		t.Errorf("valid token should pass verification: %v", err)
	}
	if tm.GetToken() != token {
		t.Error("GetToken() should return the set token")
	}
	tm.Stop()
}

// TestTokenManager_SetToken_VerifyFailed 验证验签失败返回错误。
func TestTokenManager_SetToken_VerifyFailed(t *testing.T) {
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}
	token := signJWT(payload, "wrong-secret")

	tm := NewTokenManagerWithVerify(nil, TokenManagerOptions{
		Secret:           "correct-secret",
		ClockSkewSeconds: 30,
	})
	err := tm.SetToken(token)
	if err == nil {
		t.Fatal("forged token should fail verification")
	}
	tm.Stop()
}

// TestTokenManager_SetToken_LegacyMode 验证 NewTokenManager（deprecated）不验签（向后兼容）。
func TestTokenManager_SetToken_LegacyMode(t *testing.T) {
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}
	token := signJWT(payload, "any-secret")

	tm := NewTokenManager(nil)
	// 旧构造函数不验签，任何格式正确的 JWT 都能通过
	if err := tm.SetToken(token); err != nil {
		t.Errorf("legacy mode should not verify signature: %v", err)
	}
	tm.Stop()
}

// TestTokenManager_RefreshToken_VerifyNewToken 验证刷新后用新 token 验签（v2 R5）。
func TestTokenManager_RefreshToken_VerifyNewToken(t *testing.T) {
	secret := "test-secret"
	newPayload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}
	newToken := signJWT(newPayload, secret)

	refreshCalled := false
	refreshFn := func() (string, error) {
		refreshCalled = true
		return newToken, nil
	}

	tm := NewTokenManagerWithVerify(refreshFn, TokenManagerOptions{
		Secret:           secret,
		ClockSkewSeconds: 30,
	})
	if err := tm.RefreshToken(); err != nil {
		t.Errorf("RefreshToken should succeed with valid new token: %v", err)
	}
	if !refreshCalled {
		t.Error("refresh function was not called")
	}
	if tm.GetToken() != newToken {
		t.Error("GetToken() should return the refreshed token")
	}
	tm.Stop()
}

// TestTokenManager_RefreshToken_RejectForgedNewToken 验证刷新返回伪造 token 时验签失败。
func TestTokenManager_RefreshToken_RejectForgedNewToken(t *testing.T) {
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}
	forgedToken := signJWT(payload, "wrong-secret")

	refreshFn := func() (string, error) {
		return forgedToken, nil
	}

	tm := NewTokenManagerWithVerify(refreshFn, TokenManagerOptions{
		Secret:           "correct-secret",
		ClockSkewSeconds: 30,
	})
	err := tm.RefreshToken()
	if err == nil {
		t.Fatal("RefreshToken with forged new token should fail verification")
	}
	if id := ai.GetErrorID(err); id != ai.ErrBridgeJWT {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeJWT)
	}
	tm.Stop()
}

// TestDecodeJWTPayload_Deprecated 验证 DecodeJWTPayload 仍可用（向后兼容）。
func TestDecodeJWTPayload_Deprecated(t *testing.T) {
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}
	token := signJWT(payload, "any-secret")

	result, err := DecodeJWTPayload(token)
	if err != nil {
		t.Fatalf("DecodeJWTPayload should still work: %v", err)
	}
	if result.Sub != "user1" {
		t.Errorf("Sub = %s, want user1", result.Sub)
	}
}

// TestVerifyJWT_BearerPrefix 验证 VerifyJWT 能处理 "Bearer " 前缀。
func TestVerifyJWT_BearerPrefix(t *testing.T) {
	secret := "test-secret"
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}
	token := "Bearer " + signJWT(payload, secret)

	opts := VerifyOptions{
		Secret:           secret,
		ClockSkewSeconds: 30,
	}
	_, err := VerifyJWT(token, opts)
	if err != nil {
		t.Errorf("Bearer prefix should be stripped: %v", err)
	}
}

// TestVerifyJWT_NoJtiCache 验证 JtiCache=nil 时不做重放检测。
func TestVerifyJWT_NoJtiCache(t *testing.T) {
	secret := "test-secret"
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
		Jti: "jti-no-cache",
	}
	token := signJWT(payload, secret)

	opts := VerifyOptions{
		Secret:           secret,
		ClockSkewSeconds: 30,
		JtiCache:         nil, // 不做重放检测
	}
	// 连续两次验签都应通过（无 cache 不检测重放）
	_, err := VerifyJWT(token, opts)
	if err != nil {
		t.Fatalf("first verify should pass: %v", err)
	}
	_, err = VerifyJWT(token, opts)
	if err != nil {
		t.Errorf("second verify should pass (no jti cache): %v", err)
	}
}

// TestVerifyJWT_NbfClockSkewTolerance 验证 nbf 在时钟偏移窗口内仍通过。
func TestVerifyJWT_NbfClockSkewTolerance(t *testing.T) {
	secret := "test-secret"
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
		Nbf: time.Now().Add(10 * time.Second).Unix(), // 10秒后生效
	}
	token := signJWT(payload, secret)

	opts := VerifyOptions{
		Secret:           secret,
		ClockSkewSeconds: 30, // 30秒偏移窗口，10秒 < 30秒，应通过
	}
	_, err := VerifyJWT(token, opts)
	if err != nil {
		t.Errorf("nbf within clock skew should pass: %v", err)
	}
}

// TestNewTokenManagerWithVerify_JtiCacheInitialized 验证构造函数初始化 JtiCache。
func TestNewTokenManagerWithVerify_JtiCacheInitialized(t *testing.T) {
	tm := NewTokenManagerWithVerify(nil, TokenManagerOptions{
		Secret:           "test",
		ClockSkewSeconds: 30,
	})
	if tm.verifyOpts.JtiCache == nil {
		t.Error("JtiCache should be initialized by NewTokenManagerWithVerify")
	}
	if !tm.verifyEnabled {
		t.Error("verifyEnabled should be true")
	}
	tm.Stop()
}

// fmt import guard (避免 unused import 在某些构建条件下报错)
var _ = fmt.Sprintf
