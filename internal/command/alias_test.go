// alias_test.go — 命令别名测试（M09-02 改造，）
package command

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestAliasStore_Expand_Simple 测试简单别名展开
func TestAliasStore_Expand_Simple(t *testing.T) {
	a := NewAliasStore("")
	_ = a.Add("cp", "commit-push-pr", "", "")

	result := a.Expand("/cp")
	if result != "/commit-push-pr" {
		t.Errorf("expected /commit-push-pr, got %s", result)
	}
}

// TestAliasStore_Expand_Args 测试 $1 参数替换
func TestAliasStore_Expand_Args(t *testing.T) {
	a := NewAliasStore("")
	_ = a.Add("cp", "commit-push-pr", "$1 --draft", "")

	result := a.Expand("/cp feature-x")
	expected := "/commit-push-pr feature-x --draft"
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

// TestAliasStore_Expand_Star 测试 $* 全参数替换
func TestAliasStore_Expand_Star(t *testing.T) {
	a := NewAliasStore("")
	_ = a.Add("run", "workflow run", "$*", "")

	result := a.Expand("/run ci --input hello")
	expected := "/workflow run ci --input hello"
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

// TestAliasStore_Expand_MissingArg 测试 $1 未提供替换为空（OPT-03 shell 语义）
func TestAliasStore_Expand_MissingArg(t *testing.T) {
	a := NewAliasStore("")
	_ = a.Add("cp", "commit-push-pr", "$1 --draft", "")

	// 不带参数 → $1 替换为空
	result := a.Expand("/cp")
	expected := "/commit-push-pr  --draft"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestAliasStore_Expand_NotAlias 测试非别名原样返回
func TestAliasStore_Expand_NotAlias(t *testing.T) {
	a := NewAliasStore("")
	_ = a.Add("cp", "commit-push-pr", "", "")

	result := a.Expand("/help")
	if result != "/help" {
		t.Errorf("expected /help unchanged, got %s", result)
	}
}

// TestAliasStore_Expand_EmptyInput 测试空输入
func TestAliasStore_Expand_EmptyInput(t *testing.T) {
	a := NewAliasStore("")
	result := a.Expand("")
	if result != "" {
		t.Errorf("expected empty string unchanged, got %s", result)
	}
}

// TestAliasStore_Add_List_Remove 测试 CRUD
func TestAliasStore_Add_List_Remove(t *testing.T) {
	a := NewAliasStore("")

	_ = a.Add("cp", "commit-push-pr", "", "")
	_ = a.Add("run", "workflow run", "$*", "")
	_ = a.Add("up", "upgrade", "", "")

	aliases := a.List()
	if len(aliases) != 3 {
		t.Fatalf("expected 3 aliases, got %d", len(aliases))
	}
	// List 按名称排序
	if aliases[0].Name != "cp" {
		t.Errorf("expected first alias cp, got %s", aliases[0].Name)
	}
	if aliases[1].Name != "run" {
		t.Errorf("expected second alias run, got %s", aliases[1].Name)
	}
	if aliases[2].Name != "up" {
		t.Errorf("expected third alias up, got %s", aliases[2].Name)
	}

	// Remove
	if err := a.Remove("run"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	aliases = a.List()
	if len(aliases) != 2 {
		t.Fatalf("expected 2 aliases after remove, got %d", len(aliases))
	}

	// Remove 不存在的别名应报错
	if err := a.Remove("nonexistent"); err == nil {
		t.Error("expected error removing nonexistent alias, got nil")
	}
}

// TestAliasStore_Reload 测试 Reloadable 接口
func TestAliasStore_Reload(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "aliases.json")

	// 先写一个文件
	a1 := NewAliasStore(path)
	_ = a1.Add("cp", "commit-push-pr", "", "")

	// 创建新的 store（内存为空），通过 Reload 加载文件
	a2 := NewAliasStore("")
	if err := a2.Reload(path); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	aliases := a2.List()
	if len(aliases) != 1 {
		t.Fatalf("expected 1 alias after Reload, got %d", len(aliases))
	}
	if aliases[0].Name != "cp" {
		t.Errorf("expected alias cp, got %s", aliases[0].Name)
	}
}

// TestAliasStore_Reload_FileNotFound 测试 Reload 文件不存在清空内存
func TestAliasStore_Reload_FileNotFound(t *testing.T) {
	a := NewAliasStore("")
	_ = a.Add("cp", "commit-push-pr", "", "")

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nonexistent.json")
	// Reload 不存在的文件应清空内存
	if err := a.Reload(path); err != nil {
		t.Fatalf("Reload nonexistent file should not error, got %v", err)
	}
	aliases := a.List()
	if len(aliases) != 0 {
		t.Errorf("expected 0 aliases after Reload nonexistent, got %d", len(aliases))
	}
}

// TestAliasStore_CaseInsensitive 测试别名大小写不敏感
func TestAliasStore_CaseInsensitive(t *testing.T) {
	a := NewAliasStore("")
	_ = a.Add("CP", "commit-push-pr", "", "")

	// 用小写 /cp 也应能展开
	result := a.Expand("/cp")
	if result != "/commit-push-pr" {
		t.Errorf("expected /commit-push-pr (case insensitive), got %s", result)
	}

	// 用大写 /CP 也应能展开
	result = a.Expand("/CP")
	if result != "/commit-push-pr" {
		t.Errorf("expected /commit-push-pr (case insensitive), got %s", result)
	}
}

// TestAliasStore_Concurrent 测试并发安全（race 检测）
func TestAliasStore_Concurrent(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "aliases.json")
	a := NewAliasStore(path)

	var wg sync.WaitGroup
	// 并发 Add
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = a.Add("alias", "target", "", "")
		}(i)
	}
	// 并发 List
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.List()
		}()
	}
	// 并发 Expand
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.Expand("/alias")
		}()
	}
	wg.Wait()
}

// TestAliasStore_Persistence 测试持久化（写入文件后重新加载）
func TestAliasStore_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "aliases.json")

	a1 := NewAliasStore(path)
	_ = a1.Add("cp", "commit-push-pr", "$1 --draft", "commit push pr workflow")

	// 验证文件已写入
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected aliases file to exist, got %v", err)
	}

	// 重新加载
	a2 := NewAliasStore(path)
	aliases := a2.List()
	if len(aliases) != 1 {
		t.Fatalf("expected 1 alias after reload from file, got %d", len(aliases))
	}
	if aliases[0].Name != "cp" {
		t.Errorf("expected alias cp, got %s", aliases[0].Name)
	}
	if aliases[0].Args != "$1 --draft" {
		t.Errorf("expected args $1 --draft, got %s", aliases[0].Args)
	}
}

// TestSubstituteArgs_MultiPlaceholders 测试多个占位符
func TestSubstituteArgs_MultiPlaceholders(t *testing.T) {
	result := substituteArgs("$1 $2 $3", []string{"a", "b", "c"})
	if result != "a b c" {
		t.Errorf("expected 'a b c', got %s", result)
	}
}

// TestSubstituteArgs_OutOfRange 测试 $N 超出范围替换为空
func TestSubstituteArgs_OutOfRange(t *testing.T) {
	result := substituteArgs("$1 $5", []string{"a"})
	expected := "a "
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestSplitArgs 测试参数切分
func TestSplitArgs(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"a b c", 3},
		{"  a   b  ", 2},
	}
	for _, tt := range tests {
		result := splitArgs(tt.input)
		if len(result) != tt.want {
			t.Errorf("splitArgs(%q) = %v (len %d), want len %d", tt.input, result, len(result), tt.want)
		}
	}
}
