package bridge

import (
	stderrors "errors"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// TestNewBridgeError_JWTCode 验证 -32001/-32002/-32005 映射到 ErrBridgeJWT。
func TestNewBridgeError_JWTCode(t *testing.T) {
	cases := []int{ErrInvalidToken, ErrTokenExpired, ErrAuthRequired}
	for _, code := range cases {
		err := NewBridgeError(code, "test")
		if err.ID != ai.ErrBridgeJWT {
			t.Errorf("code %d: expected ID %s, got %s", code, ai.ErrBridgeJWT, err.ID)
		}
		if err.Code != code {
			t.Errorf("code %d: Code field mismatch", code)
		}
	}
}

// TestNewBridgeError_ReconnectCode 验证 -32003/-32004 映射到 ErrBridgeReconnect。
func TestNewBridgeError_ReconnectCode(t *testing.T) {
	cases := []int{ErrRefreshFailed, ErrRefreshUnavailable}
	for _, code := range cases {
		err := NewBridgeError(code, "test")
		if err.ID != ai.ErrBridgeReconnect {
			t.Errorf("code %d: expected ID %s, got %s", code, ai.ErrBridgeReconnect, err.ID)
		}
	}
}

// TestNewBridgeError_DefaultCode 验证未知错误码兜底到 ErrBridgeJWT。
func TestNewBridgeError_DefaultCode(t *testing.T) {
	err := NewBridgeError(-99999, "unknown")
	if err.ID != ai.ErrBridgeJWT {
		t.Errorf("unknown code: expected fallback ID %s, got %s", ai.ErrBridgeJWT, err.ID)
	}
}

// TestBridgeError_ErrorIDMethod 验证 BridgeError.ErrorID() 方法返回正确的 ErrorID（v2 R2）。
func TestBridgeError_ErrorIDMethod(t *testing.T) {
	err := NewBridgeError(ErrRefreshFailed, "refresh failed")
	if err.ErrorID() != ai.ErrBridgeReconnect {
		t.Errorf("ErrorID() = %s, want %s", err.ErrorID(), ai.ErrBridgeReconnect)
	}
}

// TestBridgeError_ErrorFormat 验证 Error() 字符串包含错误码和 ErrorID。
func TestBridgeError_ErrorFormat(t *testing.T) {
	err := NewBridgeError(ErrInvalidToken, "invalid")
	msg := err.Error()
	if msg == "" {
		t.Error("Error() returned empty string")
	}
	if !contains(msg, "32001") {
		t.Errorf("Error() should contain code 32001, got: %s", msg)
	}
}

// TestErrorsAs_ExtractBridgeError 验证 errors.As 能从 formatError 结果中提取 *BridgeError。
func TestErrorsAs_ExtractBridgeError(t *testing.T) {
	err := formatError(ErrRefreshFailed, "refresh failed")
	var bridgeErr *BridgeError
	if !stderrors.As(err, &bridgeErr) {
		t.Fatal("errors.As failed to extract *BridgeError")
	}
	if bridgeErr.Code != ErrRefreshFailed {
		t.Errorf("extracted Code = %d, want %d", bridgeErr.Code, ErrRefreshFailed)
	}
	if bridgeErr.ID != ai.ErrBridgeReconnect {
		t.Errorf("extracted ID = %s, want %s", bridgeErr.ID, ai.ErrBridgeReconnect)
	}
}

// TestFormatError_GetErrorID 验证 ai.GetErrorID 能从 formatError 结果提取 ErrorID（v2 R2）。
// formatError 使用 ai.WithID 双层包装：外层 *ai.Error 供 GetErrorID 提取，
// 内层 *BridgeError 供 errors.As 提取。
func TestFormatError_GetErrorID(t *testing.T) {
	// -32003 → ErrBridgeReconnect
	err := formatError(ErrRefreshFailed, "refresh failed")
	if id := ai.GetErrorID(err); id != ai.ErrBridgeReconnect {
		t.Errorf("GetErrorID(formatError(-32003)) = %s, want %s", id, ai.ErrBridgeReconnect)
	}

	// -32001 → ErrBridgeJWT
	err2 := formatError(ErrInvalidToken, "invalid token")
	if id := ai.GetErrorID(err2); id != ai.ErrBridgeJWT {
		t.Errorf("GetErrorID(formatError(-32001)) = %s, want %s", id, ai.ErrBridgeJWT)
	}

	// -32005 → ErrBridgeJWT
	err3 := formatError(ErrAuthRequired, "auth required")
	if id := ai.GetErrorID(err3); id != ai.ErrBridgeJWT {
		t.Errorf("GetErrorID(formatError(-32005)) = %s, want %s", id, ai.ErrBridgeJWT)
	}
}

// TestIsAuthError 验证 isAuthError 对各种错误的判断（v2 R5 a/d）。
func TestIsAuthError(t *testing.T) {
	// *BridgeError 认证类错误 → true
	for _, code := range []int{ErrInvalidToken, ErrTokenExpired, ErrAuthRequired, ErrRefreshFailed, ErrRefreshUnavailable} {
		err := NewBridgeError(code, "test")
		if !isAuthError(err) {
			t.Errorf("isAuthError(code %d) = false, want true", code)
		}
	}
	// 非 BridgeError 错误 → false
	plainErr := stderrors.New("plain error")
	if isAuthError(plainErr) {
		t.Error("isAuthError(plain error) = true, want false")
	}
	// nil → false
	if isAuthError(nil) {
		t.Error("isAuthError(nil) = true, want false")
	}
}

// contains 检查字符串是否包含子串。
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(s[:len(substr)] == substr) || contains(s[1:], substr))
}
