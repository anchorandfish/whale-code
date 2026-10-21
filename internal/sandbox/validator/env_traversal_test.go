// Package validator — M14-04 环境变量泄漏 + M14-05 路径穿越检测单元测试。
package validator

import (
	"testing"
)

// TestCheckEnvVarLeak_DumpCommands 验证 env var 转储命令被检测。
func TestCheckEnvVarLeak_DumpCommands(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool // true = 应命中（Passed=false）
	}{
		// env 转储
		{"env_standalone", "env", true},
		{"env_pipe", "env | grep API", true},
		{"env_redirect", "env > /tmp/env.txt", true},
		{"env_semicolon", "env ; ls", true},
		{"env_and", "env && ls", true},
		{"env_at_end", "ls && env", true},
		{"env_background", "env &", true},

		// printenv
		{"printenv_no_arg", "printenv", true},
		{"printenv_with_arg", "printenv HOME", true},
		{"printenv_pipe", "printenv | grep API", true},

		// set 转储
		{"set_standalone", "set", true},
		{"set_pipe", "set | grep API", true},
		{"set_redirect", "set > /tmp/vars.txt", true},

		// declare -p / typeset
		{"declare_p", "declare -p", true},
		{"declare_p_with_var", "declare -p PATH", true},
		{"typeset_p", "typeset -p", true},

		// export -p
		{"export_p", "export -p", true},

		// /proc/self/environ
		{"proc_self_environ", "cat /proc/self/environ", true},
		{"proc_self_environ_redir", "cat /proc/self/environ > /tmp/leak", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := checkEnvVarLeak(tc.cmd)
			if tc.want && result.Passed {
				t.Errorf("checkEnvVarLeak(%q) should detect env leak, got Passed=true", tc.cmd)
			}
			if tc.want && result.CheckID != "ENV-LEAK" {
				t.Errorf("checkEnvVarLeak(%q) CheckID=%q, want ENV-LEAK", tc.cmd, result.CheckID)
			}
		})
	}
}

// TestCheckEnvVarLeak_LegitimateUsage 验证合法 env 用法不被误拦。
func TestCheckEnvVarLeak_LegitimateUsage(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"env_set_var", "env FOO=bar ls"},
		{"env_unset_var", "env -u FOO ls"},
		{"env_clear_env", "env -i ls"},
		{"env_set_multiple", "env FOO=bar BAR=baz cmd"},
		{"set_option_e", "set -e"},
		{"set_option_pipefail", "set -o pipefail"},
		{"set_option_nounset", "set -u"},
		{"export_var", "export FOO=bar"},
		{"declare_var", "declare FOO=bar"},
		{"normal_command", "ls -la /tmp"},
		{"grep_env_file", "grep API_KEY .env"}, // .env 是文件名，不是 env 命令
		{"vim_environ_file", "vim .environment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := checkEnvVarLeak(tc.cmd)
			if !result.Passed {
				t.Errorf("checkEnvVarLeak(%q) should not flag legitimate usage, got CheckID=%s Message=%s",
					tc.cmd, result.CheckID, result.Message)
			}
		})
	}
}

// TestCheckPathTraversal_SensitiveSystemPaths 验证敏感系统路径穿越被检测（Critical）。
func TestCheckPathTraversal_SensitiveSystemPaths(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"etc_passwd", "cat ../../../etc/passwd"},
		{"etc_shadow", "cat ../../etc/shadow"},
		{"usr_bin", "ls ../../../usr/bin"},
		{"root_ssh", "cat ../../root/.ssh/id_rsa"},
		{"var_log", "tail ../../../var/log/auth.log"},
		{"proc_self", "cat ../../proc/self/environ"},
		{"boot_grub", "ls ../../boot/grub"},
		{"windows_system32", "cat ..\\..\\..\\Windows\\System32\\config\\SAM"},
		{"windows_backslash", "type ..\\..\\..\\Windows\\System32\\drivers\\etc\\hosts"},
		{"deep_traversal_etc", "cat ../../../../../../etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := checkPathTraversal(tc.cmd)
			if result.Passed {
				t.Errorf("checkPathTraversal(%q) should detect, got Passed=true", tc.cmd)
			}
			if result.CheckID != "PATH-TRAVERSAL" {
				t.Errorf("checkPathTraversal(%q) CheckID=%q, want PATH-TRAVERSAL", tc.cmd, result.CheckID)
			}
			if result.Severity != SeverityCritical {
				t.Errorf("checkPathTraversal(%q) Severity=%v, want Critical for system path", tc.cmd, result.Severity)
			}
		})
	}
}

// TestCheckPathTraversal_DeepNonSystem 验证非系统路径的深度穿越被标记（Medium）。
func TestCheckPathTraversal_DeepNonSystem(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"deep_project", "cat ../../../project/src/main.go"},
		{"deep_sibling", "ls ../../sibling/dir"},
		{"deep_workspace", "cd ../../../workspace/other"},
		{"windows_deep", "type ..\\..\\..\\project\\file.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := checkPathTraversal(tc.cmd)
			if result.Passed {
				t.Errorf("checkPathTraversal(%q) should detect deep traversal, got Passed=true", tc.cmd)
			}
			if result.Severity != SeverityMedium {
				t.Errorf("checkPathTraversal(%q) Severity=%v, want Medium for non-system path", tc.cmd, result.Severity)
			}
		})
	}
}

// TestCheckPathTraversal_LegitimateSingleLevel 验证单段 ../ 不被误拦。
func TestCheckPathTraversal_LegitimateSingleLevel(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"cd_parent", "cd .."},
		{"cd_sibling", "cd ../sibling"},
		{"ls_parent", "ls .."},
		{"cat_parent_file", "cat ../README.md"},
		{"cd_up_one", "cd ../project"},
		{"windows_cd_parent", "cd .."},
		{"normal_path", "cat /tmp/file.txt"},
		{"relative_path", "cat src/main.go"},
		{"no_traversal", "ls -la"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := checkPathTraversal(tc.cmd)
			if !result.Passed {
				t.Errorf("checkPathTraversal(%q) should not flag legitimate single-level, got CheckID=%s Message=%s",
					tc.cmd, result.CheckID, result.Message)
			}
		})
	}
}

// TestCheckEnvVarLeak_Integration 集成测试：通过 Validator.Run 验证 ENV-LEAK CheckID 出现。
func TestCheckEnvVarLeak_Integration(t *testing.T) {
	v := NewValidatorWithoutAST()
	cmd := "env | grep -i api"
	results := v.Run(cmd)
	found := false
	for _, r := range results {
		if r.CheckID == "ENV-LEAK" && !r.Passed {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Validator.Run(%q) should return ENV-LEAK result, got %v", cmd, results)
	}
}

// TestCheckPathTraversal_Integration 集成测试：通过 Validator.Run 验证 PATH-TRAVERSAL CheckID 出现。
func TestCheckPathTraversal_Integration(t *testing.T) {
	v := NewValidatorWithoutAST()
	cmd := "cat ../../../etc/passwd"
	results := v.Run(cmd)
	found := false
	for _, r := range results {
		if r.CheckID == "PATH-TRAVERSAL" && !r.Passed {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Validator.Run(%q) should return PATH-TRAVERSAL result, got %v", cmd, results)
	}
}

// TestCheckEnvVarLeak_SecurityIntegration 通过 security.CheckCommand 端到端验证。
//
// 注：本测试位于 validator 包，仅验证 CheckID；端到端 Deny 决策在 security 包验证。
func TestCheckEnvVarLeak_HasBlocking(t *testing.T) {
	v := NewValidatorWithoutAST()
	cmd := "printenv HOME"
	results := v.Run(cmd)
	if !HasBlocking(results) {
		t.Errorf("HasBlocking should be true for env leak (High severity), got results: %v", results)
	}
}

// TestCheckPathTraversal_HasBlocking 验证 Critical 穿越触发 HasBlocking。
func TestCheckPathTraversal_HasBlocking(t *testing.T) {
	v := NewValidatorWithoutAST()
	cmd := "cat ../../../etc/passwd"
	results := v.Run(cmd)
	if !HasBlocking(results) {
		t.Errorf("HasBlocking should be true for system path traversal (Critical), got results: %v", results)
	}
}
