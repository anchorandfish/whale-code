package config

// 批 5 守卫：MemoryStore 错误面英文单侧。
//
// 为什么 error 不双语：error 字符串同时流向 ① 用户展示（/memory 命令失败提示）
// ② 日志与上报（S11 结构化失败通道、stderr）。单面字符串服务两种读者 ⇒
// 按 「两种读者」最严侧取英文（Go error 惯例同）；双语多行 error 嵌进
// %w 包装链会在任意位置断行，破坏机器可读性。批 3 /daemon、批 4 /orchestrate
// 的 error 均为英文单侧 —— 同一先例。

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// TestW083B5_MemoryStoreErrors_EnglishOnly 走真实 MemoryStore 入口钉错误面无汉字。
func TestW083B5_MemoryStoreErrors_EnglishOnly(t *testing.T) {
	base := t.TempDir()
	store, err := NewMemoryStore(base, filepath.Join(base, "work"))
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	// ① Write 空 Name → 校验错误。
	err = store.Write(MemoryEntryV2{Name: "  "})
	if err == nil {
		t.Fatal("空 Name 应报错")
	}
	if util.HasHan(err.Error()) {
		t.Errorf("Write 空名错误含汉字：%q（error 面英文单侧）", err.Error())
	}
	if !strings.Contains(err.Error(), "memory name") {
		t.Errorf("Write 空名错误应含英文主句，实际 %q", err.Error())
	}

	// ② Read 不存在文件。
	if _, err := store.Read("no-such-file"); err == nil {
		t.Fatal("读不存在文件应报错")
	} else if util.HasHan(err.Error()) {
		t.Errorf("Read 错误含汉字：%q", err.Error())
	}

	// ③ Delete 不存在文件。
	if err := store.Delete("no-such-file"); err == nil {
		t.Fatal("删不存在文件应报错")
	} else if util.HasHan(err.Error()) {
		t.Errorf("Delete 错误含汉字：%q", err.Error())
	}

	// ④ EditInEditor 不存在文件（返回单 error）。
	if err := store.EditInEditor("no-such-file"); err == nil {
		t.Fatal("编辑不存在文件应报错")
	} else if util.HasHan(err.Error()) {
		t.Errorf("EditInEditor 错误含汉字：%q", err.Error())
	}
}

// TestW083B5_MemoryFreshnessText_EnglishOnly 钉 freshness 标签英文单侧
// （标签进 /memory list 的单行数据行，容不下辅行）。
func TestW083B5_MemoryFreshnessText_EnglishOnly(t *testing.T) {
	// 时间原点无关化：直接对三个档位抽样（负 age 不会出现，用足够老的 mtime）。
	now := time.Now()
	for _, mtime := range []time.Time{
		now.Add(-2 * 24 * time.Hour),  // 天档
		now.Add(-10 * 24 * time.Hour), // 周档
		now.Add(-60 * 24 * time.Hour), // 月档
	} {
		if got := MemoryFreshnessText(mtime); util.HasHan(got) {
			t.Errorf("freshness 标签含汉字 %q：单行容器 ⇒ 必须英文单侧", got)
		}
	}
}
