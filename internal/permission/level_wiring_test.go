// Package permission_test —— 权限包单测的外部测试包。
//
// 本文件只做一件事：把 **internal/tools 链接进权限包的测试二进制**。
//
// 为什么必须链接（不是形式要求）：
// - L1起「工具 → 权限档位」的唯一真相源是工具注册表里的
// Spec.RequiredPermission，permission 包经注入的目录来源查询
// （见 permission/level.go 的 RegisterToolLevelSource、tools/toollevel.go 的 init）。
// - 生产的每个二进制都会链接 internal/tools（registry/executor 来自该包），
// 因此 tools.init() 必然执行、目录必然可用。
// - 若本包的测试二进制不链接 tools，RequiredLevelOf 会退回历史默认
// LevelWorkspaceWrite —— 于是「read_file 在只读档被拒」这类**假红**会出现，
// 真实的档位漂移反而被掩盖。
//
// 用空白导入而不是把测试改成 tools 的调用方：这里要的是**生产同源的装配**，
// 不是替身。守卫 TestFixture_ToolLevelCatalogIsWired 断言装配确实生效。
package permission_test

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"

	_ "github.com/anchorandfish/whale-code/internal/tools" // init(): 注入工具档位目录
)

// TestFixture_ToolLevelCatalogIsWired 断言权限包的测试跑在**真实档位目录**上，
// 且目录内容与工具声明一致（抽查读写两侧各一个代表）。
func TestFixture_ToolLevelCatalogIsWired(t *testing.T) {
	if !permission.ToolLevelSourceRegistered() {
		t.Fatal("档位目录未注入：本测试二进制缺少 internal/tools（见本文件顶部说明）")
	}

	cases := []struct {
		tool string
		want permission.Level
	}{
		{"read_file", permission.LevelReadOnly},            // 只读侧代表
		{"grep_search", permission.LevelReadOnly},          // 读取逃逸口的另一半
		{"bash", permission.LevelWorkspaceWrite},           // 执行侧代表（L1 已由 DFA 正名为 WW）
		{"write_file", permission.LevelWorkspaceWrite},     // 写入侧代表
		{"export_session", permission.LevelWorkspaceWrite}, // Q1：声明档由 RO 正名为 WW
	}
	for _, c := range cases {
		got, ok := permission.LevelOf(c.tool)
		if !ok {
			t.Errorf("%s 未在档位目录中登记", c.tool)
			continue
		}
		if got != c.want {
			t.Errorf("%s 档位 = %v，want %v", c.tool, got, c.want)
		}
	}

	// 未登记的工具必须解析失败（fail-closed 的前提）。
	if _, ok := permission.LevelOf("__not_a_real_tool__"); ok {
		t.Error("未登记工具竟解析成功 —— fail-closed 兜底失效")
	}
}
