package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ===== probeStateDirWritable 行为判据 =====

func TestProbeStateDirWritable_OK(t *testing.T) {
	dir := t.TempDir()
	if err := probeStateDirWritable(dir); err != nil {
		t.Fatalf("可写目录探针失败: %v", err)
	}
	// 探针必须零残留（写→改名→删全链路）
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("探针留下残留: %v", entries)
	}
}

func TestProbeStateDirWritable_ReadOnlyDir_FailsAtWrite(t *testing.T) {
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })

	err := probeStateDirWritable(ro)
	if err == nil {
		t.Fatal("只读目录探针应失败（write 步），got nil")
	}
	if !strings.Contains(err.Error(), "write") {
		t.Fatalf("失败应发生在 write 步，got: %v", err)
	}
}

// TestProbeStateDirWritable_ReadOnlyDir_FailsAtRename 模拟 的真实形态：
// write 能成功、rename 被拒 ⇒ 必须红在 rename 步。
// （用"预占改名目标文件"模拟 rename 失败——私有名，不碰真实文件。）
func TestProbeStateDirWritable_FailsAtRename(t *testing.T) {
	dir := t.TempDir()
	// 探针目标名 = .whale-doctor-probe-<pid>.tmp.renamed；把它预占成一个目录，
	// rename 进目录会失败（EISDIR/ENOTDIR），write 步仍正常。
	target := filepath.Join(dir, fmt.Sprintf(".whale-doctor-probe-%d.tmp.renamed", os.Getpid()))
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(target) })

	err := probeStateDirWritable(dir)
	if err == nil {
		t.Fatal("rename 被占位时探针应失败，got nil")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Fatalf("失败应发生在 rename 步（ 形态），got: %v", err)
	}
}

// ===== checkStateDirWritable（doctor 接线面）=====

func TestCheckStateDirWritable_ReadOnlyDir_WarnsWithWhaleHomeGuidance(t *testing.T) {
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })

	t.Setenv("WHALE_HOME", ro)
	doc := &Doctor{}
	results := doc.checkStateDirWritable()
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Severity != SeverityWarn {
		t.Fatalf("不可写状态目录应为 Warn，got %v（把无声失败变可见是本检查的全部意义）", r.Severity)
	}
	if !strings.Contains(r.FixSuggest, "WHALE_HOME") {
		t.Fatalf("FixSuggest 必须给出 WHALE_HOME 搬家指引，got: %q", r.FixSuggest)
	}
	if !strings.Contains(r.Message, ro) {
		t.Fatalf("Message 必须带状态目录路径，got: %q", r.Message)
	}
}

func TestCheckStateDirWritable_OKDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WHALE_HOME", dir)
	doc := &Doctor{}
	results := doc.checkStateDirWritable()
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Severity != SeverityOK {
		t.Fatalf("可写目录应为 OK，got %v: %s", r.Severity, r.Message)
	}
	if !strings.Contains(r.Message, "WHALE_HOME") {
		t.Fatalf("经 WHALE_HOME 搬家后 OK 消息应标注来源，got: %q", r.Message)
	}
}
