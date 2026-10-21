package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================================
// 搜索输出「路径自证」守卫 —— 缺陷 T-11（2026-10-06 真机）
//
// 现象（真机轨迹）：
// 模型 glob_search("**/t10.txt") 拿到的输出是 `Found 1 files:` + ` t10.txt`
// —— 裸相对名，既没有搜索根、也不是可解析路径。
// 于是模型**只能猜** `/private/tmp/whalecap/work/t10.txt`，且前面已因猜错
// `work/out/t10.txt` 白跑一轮 `read_file`（ENOENT）。
//
// 根因：formatGlobOutput / formatGrepOutput 用 filepath.Rel(searchDir, p)
// 把路径缩成相对名，同时**丢弃了 searchDir 本身**（search_engine.go）。
// 工具内部明明持有绝对路径，输出却不可还原 ⇒ 信息在输出面被丢掉。
//
// 判据设计（R4-7 / R10）：断言的是「输出**本身**是否自证根」与「条目是否
// 可直接喂给下游工具」这两件**用户可见**的事，而不是函数内部实现。
// before（丢弃根）⇒ ①`不 Contains(workDir)`、②条目非绝对路径 ⇒ 必红；
// after（补回根） ⇒ 全绿。
//
// ★ 注意：这两个用例**不要**改成断言"输出不含某串"。它们是**正向**断言
// （输出必须含根 / 条目必须可 stat），改弱即退化成恒绿。
// ============================================================================

// parseListedPaths 从 glob 输出里抽出条目路径。
// 输出形态：`Found N files:` 随后每行 ` <path>`。
func parseListedPaths(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Found ") || strings.TrimSpace(line) == "" {
			continue
		}
		paths = append(paths, strings.TrimSpace(line))
	}
	return paths
}

// TestGlobOutput_PathsAreSelfDescribing 是缺陷 T-11 的判别器。
func TestGlobOutput_PathsAreSelfDescribing(t *testing.T) {
	workDir := t.TempDir()
	// 放在**嵌套子目录**里：这样"裸文件名 t10.txt"与"可解析路径"必须可区分 ——
	// 若文件在根目录、且测试只看文件名，缺陷会被蒙对。
	sub := filepath.Join(workDir, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("建子目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "t10.txt"), []byte("value: alpha gamma"), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	e := NewExecutor(NewRegistry(), workDir)
	res := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "glob_search",
		Input: mustMarshal(map[string]string{"pattern": "**/t10.txt"}),
	})
	if res.IsError {
		t.Fatalf("glob_search 失败: %s", res.Output)
	}

	// ① 输出必须自证搜索根 —— 否则下游无法还原绝对路径。
	if !strings.Contains(res.Output, workDir) {
		t.Fatalf("glob 输出未包含搜索根 %q ⇒ 模型只能猜路径（缺陷 T-11）。实际输出:\n%s",
			workDir, res.Output)
	}

	// ② 每条条目都必须是「可直接喂给 read_file」的路径：绝对 + 真实存在。
	entries := parseListedPaths(res.Output)
	if len(entries) == 0 {
		t.Fatalf("未从输出解析到任何路径。实际输出:\n%s", res.Output)
	}
	for _, p := range entries {
		if !filepath.IsAbs(p) {
			t.Errorf("条目 %q 不是绝对路径 ⇒ 下游工具仍需猜根", p)
			continue
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("条目 %q 不可 stat（路径无法解析）: %v", p, err)
		}
	}
}

// TestGrepOutput_PathsAreSelfDescribing 是同一缺陷在 grep 面的同源守卫（R3⑤）。
func TestGrepOutput_PathsAreSelfDescribing(t *testing.T) {
	workDir := t.TempDir()
	sub := filepath.Join(workDir, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("建子目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "t10.txt"), []byte("value: alpha gamma"), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	e := NewExecutor(NewRegistry(), workDir)
	res := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "grep_search",
		Input: mustMarshal(map[string]string{"pattern": "alpha", "output_mode": "content"}),
	})
	if res.IsError {
		t.Fatalf("grep_search 失败: %s", res.Output)
	}

	if !strings.Contains(res.Output, workDir) {
		t.Fatalf("grep 输出未包含搜索根 %q ⇒ 模型只能猜路径（缺陷 T-11 同源面）。实际输出:\n%s",
			workDir, res.Output)
	}
}
