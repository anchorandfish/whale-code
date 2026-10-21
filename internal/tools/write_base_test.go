package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// write_file 的「盲覆写」必须改成「有条件覆写」。
//
// 根因层：edit_file / apply_unified_diff 都以**盘上现值**为基准（内容锚定），
// 唯独 write_file 以**模型记忆**为基准 —— 它是唯一能凭几轮前的旧认知整篇盖掉
// 他人写入的工具。修法 = 服务端自动登记「读基准」（read_file 时登记盘上内容
// hash，不靠模型传参 —— 拆掉 T6 那套「三重 opt-in」死防线），write_file 对
// 已登记且已存在的文件自动比对，不一致 ⇒ 结构化拒绝（FailureKindPatchConflict，
// Terminal=false ⇒ 模型重读后重写即可自愈）。
//
// 判别器（生产入口 = readFileHandler / writeFileHandler，与 executor.go:1100-1101
// 注册的生产 handler 同一构造）：
// 主用例：读 A → 外部改写 B → 写 C ⇒ 必须拒绝 + Kind=patch_conflict + 盘上仍是 B。
// 修复前：写成功、B 被盖 ⇒ 红。
// 反向对照（防过度拦截）：
// a) 未读过就写 ⇒ 放行（现状语义；bash/编辑器读的文件登记不到，诚实边界）；
// b) 读 A → 写 B（无外部改动）⇒ 放行；
// c) 读 A → 写 B → 再写 C ⇒ 放行（成功写入后基准随写更新）；
// d) 读 A → edit_file A→A2 → 写 A2 ⇒ 放行（edit 成功后基准更新，不误伤 edit→write 连招）。

func TestW072_WriteFile_RejectsStaleBase(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(NewRegistry(), dir)

	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("v1"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// ① 模型读文件（服务端登记读基准）
	readJSON, _ := json.Marshal(ReadFileInput{Path: p})
	if _, err := e.readFileHandler()(context.Background(), readJSON); err != nil {
		t.Fatalf("read_file: %v", err)
	}

	// ② 外部改写（另一进程 / 编辑器，不经 whale）
	if err := os.WriteFile(p, []byte("external edit"), 0644); err != nil {
		t.Fatalf("external modify: %v", err)
	}

	// ③ 模型凭旧认知整篇覆写 ⇒ 必须拒绝
	writeJSON, _ := json.Marshal(WriteFileInput{Path: p, Content: "stale overwrite"})
	_, err := e.writeFileHandler()(context.Background(), writeJSON)
	if err == nil {
		t.Fatal(": write_file 在「本会话读过、此后被外部改写」时未拒绝 ⇒ 盲覆写" +
			"（模型凭旧记忆整篇盖掉他人写入）")
	}
	var tee *ToolExecError
	if !errors.As(err, &tee) || tee.Kind != FailureKindPatchConflict {
		t.Errorf(": 拒绝必须走结构化通道（Kind=patch_conflict，可自愈、不终局），实得: %v", err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "external edit" {
		t.Fatalf(": 外部修改被覆盖。\n  期望: external edit\n  实得: %q", string(data))
	}
}

func TestW072_WriteFile_ReverseControls(t *testing.T) {
	t.Run("unregistered path is allowed", func(t *testing.T) {
		dir := t.TempDir()
		e := NewExecutor(NewRegistry(), dir)
		p := filepath.Join(dir, "new.txt")
		writeJSON, _ := json.Marshal(WriteFileInput{Path: p, Content: "fresh"})
		if _, err := e.writeFileHandler()(context.Background(), writeJSON); err != nil {
			t.Fatalf(" 反例 a：未读就写（新建）应放行: %v", err)
		}
	})

	t.Run("fresh base is allowed", func(t *testing.T) {
		dir := t.TempDir()
		e := NewExecutor(NewRegistry(), dir)
		p := filepath.Join(dir, "f.txt")
		os.WriteFile(p, []byte("v1"), 0644)
		rj, _ := json.Marshal(ReadFileInput{Path: p})
		if _, err := e.readFileHandler()(context.Background(), rj); err != nil {
			t.Fatalf("read: %v", err)
		}
		wj, _ := json.Marshal(WriteFileInput{Path: p, Content: "v2"})
		if _, err := e.writeFileHandler()(context.Background(), wj); err != nil {
			t.Fatalf(" 反例 b：读后写、无外部改动应放行: %v", err)
		}
	})

	t.Run("base follows successful write", func(t *testing.T) {
		dir := t.TempDir()
		e := NewExecutor(NewRegistry(), dir)
		p := filepath.Join(dir, "f.txt")
		os.WriteFile(p, []byte("v1"), 0644)
		rj, _ := json.Marshal(ReadFileInput{Path: p})
		e.readFileHandler()(context.Background(), rj)
		w1, _ := json.Marshal(WriteFileInput{Path: p, Content: "v2"})
		if _, err := e.writeFileHandler()(context.Background(), w1); err != nil {
			t.Fatalf("first write: %v", err)
		}
		w2, _ := json.Marshal(WriteFileInput{Path: p, Content: "v3"})
		if _, err := e.writeFileHandler()(context.Background(), w2); err != nil {
			t.Fatalf(" 反例 c：成功写入后基准应随写更新，连续写不应被误拒: %v", err)
		}
	})

	t.Run("base follows successful edit", func(t *testing.T) {
		dir := t.TempDir()
		e := NewExecutor(NewRegistry(), dir)
		p := filepath.Join(dir, "f.txt")
		os.WriteFile(p, []byte("hello world"), 0644)
		rj, _ := json.Marshal(ReadFileInput{Path: p})
		e.readFileHandler()(context.Background(), rj)
		ej, _ := json.Marshal(EditFileInput{Path: p, OldText: "world", NewText: "there"})
		if _, err := e.editFileHandler()(context.Background(), ej); err != nil {
			t.Fatalf("edit: %v", err)
		}
		// 模型基于编辑后的内容（hello there）整篇重写 —— 基准若未随 edit 更新，这里会误拒
		wj, _ := json.Marshal(WriteFileInput{Path: p, Content: "hello there!"})
		if _, err := e.writeFileHandler()(context.Background(), wj); err != nil {
			t.Fatalf(" 反例 d：edit 成功后基准应更新，edit→write 连招不应被误拒: %v", err)
		}
	})
}
