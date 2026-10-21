package hook

import (
	"os"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/sandbox/validator"
	"github.com/anchorandfish/whale-code/internal/security"
)

// TestMain 初始化 security.Checker，避免测试中 "Checker 未初始化" 错误。
func TestMain(m *testing.M) {
	workDir, _ := os.Getwd()
	s := sandbox.NewSandbox(workDir)
	v := validator.NewValidator()
	p := permission.NewPolicy(permission.LevelDangerFullAccess)
	security.Init(s, v, p)
	os.Exit(m.Run())
}
