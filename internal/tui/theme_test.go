// Package tui - theme manager tests ( M10-05)
//
// 验证 ThemeManager 的主题热替换 + 并发安全。
package tui

import (
	"sync"
	"testing"
)

// TestThemeManager_SetTheme 验证切换主题后 Renderer 返回新 renderer。
func TestThemeManager_SetTheme(t *testing.T) {
	tm := newThemeManager("dark")
	if tm.Current() != "dark" {
		t.Errorf("初始主题应为 dark，实际 %q", tm.Current())
	}
	darkRenderer := tm.Renderer()

	if err := tm.SetTheme("light"); err != nil {
		t.Fatalf("SetTheme(light) 失败: %v", err)
	}
	if tm.Current() != "light" {
		t.Errorf("切换后主题应为 light，实际 %q", tm.Current())
	}
	lightRenderer := tm.Renderer()
	if lightRenderer == darkRenderer {
		t.Error("切换后 renderer 应为新实例")
	}
	if lightRenderer.theme != "light" {
		t.Errorf("新 renderer 主题应为 light，实际 %q", lightRenderer.theme)
	}
}

// TestThemeManager_MonoMapping 验证 "mono" 映射到 "notty"。
func TestThemeManager_MonoMapping(t *testing.T) {
	tm := newThemeManager("dark")
	if err := tm.SetTheme("mono"); err != nil {
		t.Fatalf("SetTheme(mono) 失败: %v", err)
	}
	if tm.Current() != "notty" {
		t.Errorf("mono 应映射到 notty，实际 %q", tm.Current())
	}
}

// TestThemeManager_InvalidTheme 验证非法主题返回 error，保留旧 renderer。
func TestThemeManager_InvalidTheme(t *testing.T) {
	tm := newThemeManager("dark")
	originalRenderer := tm.Renderer()

	// glamour.WithStandardStyle 接受 "dark"/"light"/"notty"/"pink"
	// 实际上 glamour 对未知主题可能不报错（fallback dark），这里仅测试空字符串
	err := tm.SetTheme("")
	if err == nil {
		t.Error("空主题应返回 error")
	}
	// 旧 renderer 应保留
	if tm.Renderer() != originalRenderer {
		t.Error("非法主题时旧 renderer 应保留")
	}
	if tm.Current() != "dark" {
		t.Errorf("非法主题时旧主题名应保留，实际 %q", tm.Current())
	}
}

// TestThemeManager_ConcurrentSafe 验证并发 SetTheme + Renderer 不崩溃（-race）。
//
// 对标 PrefetchBuffer 异步预渲染路径的竞态防护。
func TestThemeManager_ConcurrentSafe(t *testing.T) {
	tm := newThemeManager("dark")
	themes := []string{"dark", "light", "notty", "pink"}
	var wg sync.WaitGroup

	// 5 个 goroutine 并发 SetTheme
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			theme := themes[idx%len(themes)]
			_ = tm.SetTheme(theme)
		}(i)
	}

	// 5 个 goroutine 并发 Renderer
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := tm.Renderer()
			if r != nil {
				_, _ = r.Render("# test")
			}
		}()
	}

	wg.Wait()
	// 不崩溃即通过（-race 检测竞态）
}

// TestThemeManager_RendererNotNil 验证 dark 主题初始化后 renderer 非 nil。
func TestThemeManager_RendererNotNil(t *testing.T) {
	tm := newThemeManager("dark")
	r := tm.Renderer()
	if r == nil {
		t.Error("dark 主题 renderer 不应为 nil")
	}
}

// TestThemeManager_InitWithEmpty 验证空主题降级 dark。
func TestThemeManager_InitWithEmpty(t *testing.T) {
	tm := newThemeManager("")
	if tm.Current() != "dark" {
		t.Errorf("空主题应降级 dark，实际 %q", tm.Current())
	}
}

// TestThemeManager_InitWithMono 验证初始 mono 映射 notty。
func TestThemeManager_InitWithMono(t *testing.T) {
	tm := newThemeManager("mono")
	if tm.Current() != "notty" {
		t.Errorf("初始 mono 应映射 notty，实际 %q", tm.Current())
	}
}
