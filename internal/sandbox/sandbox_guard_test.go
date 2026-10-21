// Package sandbox — 防护守卫测试（Guard Tests）。
//
// 与常规单测的区别：本文件回答三个"防护是否真实"的问题（B2-1/B4-2 对抗式审查）：
//  1. 沙箱是否单一入口无旁路？（静态扫描生产代码中的 NewManager/newPlatformSandbox
//     旁路调用）
//  2. 生产配置三条路径（TUI/plain/bridge）是否统一？
//  3. 审计降级链是否防篡改？
//
// 内核级攻击用例（绕开 Go validator 的真实子进程）见
// sandbox_guard_attack_darwin_test.go（//go:build darwin）。
package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// ===== 1. 单一入口无旁路（静态扫描） =====

// TestGuard_NoSandboxBypassInProductionCode 扫描整个模块的非测试 Go 源码，
// 断言 sandbox.Manager 的创建只发生在 internal/sandbox 包内
// （生产唯一入口 NewProductionSandboxManager → NewManager）。
//
// 防护缺口背景（P1-SEC 修复记录）：修复前 plain/bridge 路径完全绕过沙箱。
// 本测试防止未来任何 cmd/ internal/executor/ 代码再次旁路。
func TestGuard_NoSandboxBypassInProductionCode(t *testing.T) {
	pkgDir := currentSourceDir(t)
	moduleRoot := filepath.Dir(filepath.Dir(pkgDir)) // internal/sandbox → module root

	// 仅匹配包限定调用（sandbox.NewManager）——其他包（keybindings/approval/
	// monitor/skill/plugin/hook/goal）有各自同名 NewManager，不算沙箱旁路。
	// newPlatformSandbox 为小写未导出，包外不可能调用，无需扫描。
	bypassPatterns := []*regexp.Regexp{
		regexp.MustCompile(`sandbox\.NewManager\s*\(`),
		regexp.MustCompile(`sandbox\.NewWindowsSandbox\s*\(`),
		// 注：sandbox.NewAuditLog 不在扫描范围——它只是审计日志实例，
		// 不创建沙箱隔离（executor.go/daemon worker.go 的调用合法）。
	}

	var violations []string
	werr := filepath.Walk(moduleRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过不可读路径
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") && name != "." && name != ".." {
				return filepath.SkipDir // .git 等
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// internal/sandbox 包内（含子包）允许创建 Manager —— 单一事实源
		if strings.Contains(path, filepath.Join("internal", "sandbox")) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(moduleRoot, path)
		for _, pat := range bypassPatterns {
			if pat.Match(data) {
				violations = append(violations,
					fmt.Sprintf("%s: 匹配 %s（旁路创建沙箱 Manager）", rel, pat.String()))
			}
		}
		return nil
	})
	if werr != nil {
		t.Fatalf("walk module root %s: %v", moduleRoot, werr)
	}
	if len(violations) > 0 {
		t.Errorf("发现 %d 处沙箱旁路（单一入口违例）：\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// TestGuard_ProductionManagerUniform 验证生产工厂多次实例化（模拟 TUI/plain/bridge
// 三条执行路径各自创建）配置完全一致：级别 Strict + 平台实现一致 + 无静默降级。
func TestGuard_ProductionManagerUniform(t *testing.T) {
	ws := t.TempDir()
	ctx := context.Background()

	var levels []SandboxLevel
	var effective []SandboxLevel
	var platforms []string

	for i := 0; i < 3; i++ { // 模拟三条 runner 路径独立创建
		mgr := NewProductionSandboxManager(ws)
		levels = append(levels, mgr.Level())

		sc, cleanup, err := mgr.Prepare(ctx, SandboxLevelStrict, ws)
		if err != nil {
			t.Fatalf("路径 %d Prepare: %v", i, err)
		}
		effective = append(effective, sc.EffectiveLevel())
		if sc.OSSandbox() == nil {
			t.Errorf("路径 %d: OSSandbox 为 nil（降级为纯应用层，非预期）", i)
		} else {
			platforms = append(platforms, sc.OSSandbox().Platform())
		}
		if cleanup != nil {
			cleanup()
		}
	}

	for i := 1; i < 3; i++ {
		if levels[i] != levels[0] {
			t.Errorf("路径 %d Level=%s 与路径 0 Level=%s 不一致（配置漂移）",
				i, levels[i], levels[0])
		}
		if effective[i] != effective[0] {
			t.Errorf("路径 %d EffectiveLevel=%s 与路径 0=%s 不一致",
				i, effective[i], effective[0])
		}
		if platforms[i] != platforms[0] {
			t.Errorf("路径 %d Platform=%s 与路径 0=%s 不一致（平台实现漂移）",
				i, platforms[i], platforms[0])
		}
	}
	if levels[0] != SandboxLevelStrict {
		t.Errorf("生产默认 Level=%s, want strict", levels[0])
	}
	t.Logf("三路径统一：level=%s effective=%s platform=%s", levels[0], effective[0], platforms[0])
}

// ===== 2. 审计链防篡改 =====

// TestGuard_AuditTamperDetection 验证审计日志哈希链：
// 正常追加 → VerifyIntegrity 通过；篡改/追加伪造记录 → VerifyIntegrity 报错。
func TestGuard_AuditTamperDetection(t *testing.T) {
	dir := t.TempDir()
	al := &AuditLog{logDir: dir, prevHash: computeHash([]byte(genesisSeed))}

	al.LogDegradation(SandboxLevelIsolated, SandboxLevelStrict, "guard-test-1")
	al.LogDegradation(SandboxLevelStrict, SandboxLevelRelaxed, "guard-test-2")
	al.Close()

	if err := al.VerifyIntegrity(); err != nil {
		t.Fatalf("未篡改时 VerifyIntegrity 应通过: %v", err)
	}

	// 篡改：向当前日志文件追加一条伪造记录（prev_hash 无效 → 链断裂）
	logPath := al.currentPath
	if logPath == "" {
		t.Fatal("currentPath 为空，测试无法继续")
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("打开审计文件: %v", err)
	}
	_, _ = f.WriteString(`{"timestamp":"2099-01-01T00:00:00Z","event":"VIOLATION","prev_hash":"forged"}` + "\n")
	_ = f.Close()

	if err := al.VerifyIntegrity(); err == nil {
		t.Error("篡改后 VerifyIntegrity 应报错（哈希链断裂），却返回 nil（防篡改失效）")
	} else {
		t.Logf("篡改检测成功: %v", err)
	}
}

// ===== helpers =====

func currentSourceDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}
