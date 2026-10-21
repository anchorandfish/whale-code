// Package command — /upgrade 命令unit test
package command

import (
	"strings"
	"testing"
)

// TestUpgradeCommandRegistered 测试 /upgrade 命令注册
func TestUpgradeCommandRegistered(t *testing.T) {
	r := NewRegistry()

	cmd, ok := r.Get("upgrade")
	if !ok {
		t.Fatal("upgrade 命令应已注册")
	}

	if cmd.Name != "upgrade" {
		t.Errorf("Name = %q, want %q", cmd.Name, "upgrade")
	}

	if cmd.Category != "Core" {
		t.Errorf("Category = %q, want %q", cmd.Category, "Core")
	}

	if cmd.Description == "" {
		t.Error("Description should not be empty")
	}

	if cmd.Handler == nil {
		t.Fatal("Handler 不should be nil")
	}

	if cmd.Usage == "" {
		t.Error("Usage should not be empty")
	}
}

// TestUpgradeDefaultCheck 测试无参数时默认执行 check
func TestUpgradeDefaultCheck(t *testing.T) {
	r := NewRegistry()
	r.Version = "0.2.0"

	cmd, _ := r.Get("upgrade")
	output, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if output == "" {
		t.Fatal("输出should not be empty")
	}

	if !strings.Contains(output, "Whale Code v") {
		t.Errorf("输出should contain版本号前缀, actual: %s", output)
	}
}

// TestUpgradeCheckSubCommand 测试 check 子命令
func TestUpgradeCheckSubCommand(t *testing.T) {
	r := NewRegistry()
	r.Version = "0.1.0"

	cmd, _ := r.Get("upgrade")
	output, err := cmd.Handler("check")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if output == "" {
		t.Fatal("输出should not be empty")
	}

	if !strings.Contains(output, "Whale Code v") {
		t.Errorf("输出should contain版本号, actual: %s", output)
	}
}

// TestUpgradeVersionDisplay 测试版本号显示
func TestUpgradeVersionDisplay(t *testing.T) {
	r := NewRegistry()
	r.Version = "1.5.0"

	cmd, _ := r.Get("upgrade")
	output, _ := cmd.Handler("check")

	if !strings.Contains(output, "1.5.0") {
		t.Errorf("输出should contain版本号 1.5.0, actual: %s", output)
	}
}

// TestUpgradeDefaultVersion 测试未设置版本号时的default value
func TestUpgradeDefaultVersion(t *testing.T) {
	r := NewRegistry()

	cmd, _ := r.Get("upgrade")
	output, _ := cmd.Handler("check")

	if !strings.Contains(output, "0.1.0") {
		t.Errorf("默认版本号应为 0.1.0, actual: %s", output)
	}
}

// TestUpgradeInstallSubCommand 测试 install 子命令
func TestUpgradeInstallSubCommand(t *testing.T) {
	r := NewRegistry()
	r.Version = "0.2.0"

	cmd, _ := r.Get("upgrade")
	output, err := cmd.Handler("install")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if output == "" {
		t.Fatal("输出should not be empty")
	}

	if !strings.Contains(output, "安装") && !strings.Contains(output, "install") {
		t.Errorf("输出should contain安装指引, actual: %s", output)
	}

	if !strings.Contains(output, "0.2.0") {
		t.Errorf("输出should contain版本号, actual: %s", output)
	}
}

// TestUpgradeInstallPlatformInfo 测试 install 包含平台信息
func TestUpgradeInstallPlatformInfo(t *testing.T) {
	r := NewRegistry()

	cmd, _ := r.Get("upgrade")
	output, _ := cmd.Handler("install")

	if !strings.Contains(output, "go build") && !strings.Contains(output, "go install") {
		t.Errorf("输出should contain构建命令, actual: %s", output)
	}
}

// TestUpgradeInvalidSubCommand 测试无效子命令
func TestUpgradeInvalidSubCommand(t *testing.T) {
	r := NewRegistry()

	cmd, _ := r.Get("upgrade")
	output, err := cmd.Handler("invalid")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "Usage") {
		t.Errorf("输出should contain用法提示, actual: %s", output)
	}

	if !strings.Contains(output, "check") {
		t.Errorf("输出should contain check 选项, actual: %s", output)
	}
}

// TestCompareVersions 测试版本号比较
func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1   string
		v2   string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"v0.1.0", "0.1.0", 0},
		{"0.1.0", "v0.1.0", 0},
		{"0.1.0", "0.2.0", -1},
		{"0.2.0", "0.1.0", 1},
		{"1.0.0", "0.9.9", 1},
		{"0.9.0", "1.0.0", -1},
		{"0.0.1", "0.0.2", -1},
		{"0.10.0", "0.2.0", 1},
		{"0.1.0", "0.2.0-beta", -1},
		{"v1.2.3", "v2.0.0", -1},
		{"2.0.0", "2.0.0", 0},
		{"V0.1.0", "v0.1.0", 0},
	}

	for _, tt := range tests {
		t.Run(tt.v1+"_vs_"+tt.v2, func(t *testing.T) {
			got := compareVersions(tt.v1, tt.v2)
			if got != tt.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
			}
		})
	}
}

// TestParseVersion 测试版本号解析
func TestParseVersion(t *testing.T) {
	tests := []struct {
		input    string
		wantNums []int
		wantPre  string
	}{
		{"0.1.0", []int{0, 1, 0}, ""},
		{"v1.2.3", []int{1, 2, 3}, ""},
		{"V2.0.0", []int{2, 0, 0}, ""},
		{"1.2.3-beta", []int{1, 2, 3}, "beta"},
		{"1.2", []int{1, 2}, ""},
		{"1", []int{1}, ""},
		{"invalid", []int{0}, ""},
	}

	for _, tt := range tests {
		gotNums, gotPre := parseVersion(tt.input)
		if len(gotNums) != len(tt.wantNums) {
			t.Errorf("parseVersion(%q) len = %d, want %d", tt.input, len(gotNums), len(tt.wantNums))
			continue
		}
		for i := range gotNums {
			if gotNums[i] != tt.wantNums[i] {
				t.Errorf("parseVersion(%q)[%d] = %d, want %d", tt.input, i, gotNums[i], tt.wantNums[i])
			}
		}
		if gotPre != tt.wantPre {
			t.Errorf("parseVersion(%q) pre = %q, want %q", tt.input, gotPre, tt.wantPre)
		}
	}
}

// TestGetVersion 测试版本号获取
func TestGetVersion(t *testing.T) {
	r := NewRegistry()

	if v := r.getVersion(); v != "0.1.0" {
		t.Errorf("默认版本号 = %q, want %q", v, "0.1.0")
	}

	r.Version = "2.0.0"
	if v := r.getVersion(); v != "2.0.0" {
		t.Errorf("自定义版本号 = %q, want %q", v, "2.0.0")
	}
}

// TestUpgradeCheckErrorHandling 测试网络错误时降级处理
func TestUpgradeCheckErrorHandling(t *testing.T) {
	r := NewRegistry()
	r.Version = "0.1.0"

	cmd, _ := r.Get("upgrade")
	output, err := cmd.Handler("check")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if output == "" {
		t.Fatal("输出should not be empty")
	}

	if !strings.Contains(output, "Whale Code v") {
		t.Errorf("输出should contain版本号, actual: %s", output)
	}

	// 网络错误时should contain版本号（降级处理）
	if !strings.Contains(output, "0.1.0") {
		t.Errorf("降级输出should contain版本号, actual: %s", output)
	}
}
