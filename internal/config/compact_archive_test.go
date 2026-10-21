package config

import "testing"

// TestCompactArchiveDefaultTrue 结果断言：compact_archive 缺省为 true
// （归档式压缩默认开启——压缩前先归档是安全网，不是可选项的默认关闭项）。
func TestCompactArchiveDefaultTrue(t *testing.T) {
	if !DefaultConfig().CompactArchive {
		t.Fatal("DefaultConfig().CompactArchive 应为 true（归档默认开启）")
	}
}

// TestCompactArchiveMergeOverride 结果断言：settings.json 显式
// compact_archive=false 才能关闭（对齐 auto_compact 覆盖语义）。
func TestCompactArchiveMergeOverride(t *testing.T) {
	c := DefaultConfig()
	mergeInto(&c, map[string]interface{}{"compact_archive": false})
	if c.CompactArchive {
		t.Fatal("settings.json compact_archive=false 应关闭归档")
	}
	// 显式 true 不改变默认
	c2 := DefaultConfig()
	mergeInto(&c2, map[string]interface{}{"compact_archive": true})
	if !c2.CompactArchive {
		t.Fatal("compact_archive=true 应保持开启")
	}
	// 字段缺失保持默认（向后兼容：存量 settings.json 无此键）
	c3 := DefaultConfig()
	mergeInto(&c3, map[string]interface{}{"vim_mode": true})
	if !c3.CompactArchive {
		t.Fatal("settings.json 缺失 compact_archive 键时应保持默认 true")
	}
}
