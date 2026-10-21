// cron_extended_test.go — M08-03 Cron 7 字段扩展测试
// 测试覆盖：5/6/7 字段解析、L/W/LW/# 修饰符、闰年判断、向后兼容
package tools

import (
	"fmt"
	"testing"
	"time"
)

// TestParseCronSchedule_5Fields 5 字段解析（向后兼容）
func TestParseCronSchedule_5Fields(t *testing.T) {
	sched, err := ParseCronSchedule("0 9 * * 1-5")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if sched.FieldCount != Cron5Fields {
		t.Errorf("expected FieldCount=5, got %d", sched.FieldCount)
	}
	if sched.Minute != "0" || sched.Hour != "9" || sched.Day != "*" || sched.Month != "*" || sched.Weekday != "1-5" {
		t.Errorf("fields mismatch: %+v", sched)
	}
	if sched.Second != "" || sched.Year != "" {
		t.Errorf("5 fields mode: Second/Year should be empty, got Second=%q Year=%q", sched.Second, sched.Year)
	}
}

// TestParseCronSchedule_6Fields 6 字段解析（秒级精度）
func TestParseCronSchedule_6Fields(t *testing.T) {
	sched, err := ParseCronSchedule("30 0 9 * * 1-5")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if sched.FieldCount != Cron6Fields {
		t.Errorf("expected FieldCount=6, got %d", sched.FieldCount)
	}
	if sched.Second != "30" || sched.Minute != "0" || sched.Hour != "9" {
		t.Errorf("fields mismatch: %+v", sched)
	}
}

// TestParseCronSchedule_7Fields 7 字段解析（年字段）
func TestParseCronSchedule_7Fields(t *testing.T) {
	sched, err := ParseCronSchedule("0 0 9 * * 1-5 2026")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if sched.FieldCount != Cron7Fields {
		t.Errorf("expected FieldCount=7, got %d", sched.FieldCount)
	}
	if sched.Second != "0" || sched.Year != "2026" {
		t.Errorf("fields mismatch: %+v", sched)
	}
}

// TestParseCronSchedule_InvalidFieldCount 4/8 字段拒绝
func TestParseCronSchedule_InvalidFieldCount(t *testing.T) {
	tests := []string{
		"0 9 * *",                  // 4 字段
		"0 0 9 * * 1-5 2026 extra", // 8 字段
		"",                         // 0 字段
	}
	for _, input := range tests {
		_, err := ParseCronSchedule(input)
		if err == nil {
			t.Errorf("expected error for %q, got nil", input)
		}
	}
}

// TestValidateCronSchedule_5FieldsCompat 5 字段向后兼容
func TestValidateCronSchedule_5FieldsCompat(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"0 9 * * 1-5", false},
		{"*/15 * * * *", false},
		{"0 0 1 1 *", false},
		{"60 9 * * 1-5", true}, // 分钟越界
		{"0 25 * * 1-5", true}, // 小时越界
		{"0 9 * * 8", true},    // 周越界
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			err := ValidateCronSchedule(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCronSchedule(%q) err=%v, wantErr=%v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// TestValidateCronSchedule_LModifier L 修饰符验证
func TestValidateCronSchedule_LModifier(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"0 9 L * *", false},  // day=L 合法
		{"0 9 1L * *", false}, // 1L 不合法但语法允许（实际匹配为 false）
		{"0 9 0L * *", true},  // 0L 越界
		{"0 9 32L * *", true}, // 32L 越界
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			err := ValidateCronSchedule(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCronSchedule(%q) err=%v, wantErr=%v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// TestValidateCronSchedule_WModifier W 修饰符验证
func TestValidateCronSchedule_WModifier(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"0 9 15W * *", false}, // 15W 合法
		{"0 9 1W * *", false},  // 1W 合法
		{"0 9 0W * *", true},   // 0W 越界
		{"0 9 32W * *", true},  // 32W 越界
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			err := ValidateCronSchedule(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCronSchedule(%q) err=%v, wantErr=%v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// TestValidateCronSchedule_LWModifier LW 修饰符验证
func TestValidateCronSchedule_LWModifier(t *testing.T) {
	if err := ValidateCronSchedule("0 9 LW * *"); err != nil {
		t.Errorf("LW modifier should be valid, got: %v", err)
	}
}

// TestValidateCronSchedule_HashModifier # 修饰符验证
func TestValidateCronSchedule_HashModifier(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"0 9 * * 5#3", false}, // 5#3 合法（第三个周五）
		{"0 9 * * 1#1", false}, // 1#1 合法（第一个周一）
		{"0 9 * * 8#3", true},  // 周越界
		{"0 9 * * 5#6", true},  // nth 越界（>5）
		{"0 9 * * 5#0", true},  // nth 越界（<1）
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			err := ValidateCronSchedule(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCronSchedule(%q) err=%v, wantErr=%v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// TestValidateCronSchedule_QuestionMark ? 与 * 等价
func TestValidateCronSchedule_QuestionMark(t *testing.T) {
	if err := ValidateCronSchedule("0 9 * * ?"); err != nil {
		t.Errorf("? should be valid (equivalent to *), got: %v", err)
	}
}

// TestValidateCronSchedule_6Fields 6 字段验证
func TestValidateCronSchedule_6Fields(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"30 0 9 * * 1-5", false},
		{"60 0 9 * * 1-5", true},  // 秒越界
		{"30 60 9 * * 1-5", true}, // 分越界
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			err := ValidateCronSchedule(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCronSchedule(%q) err=%v, wantErr=%v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// TestValidateCronSchedule_7Fields 7 字段验证
func TestValidateCronSchedule_7Fields(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"0 0 9 * * 1-5 2026", false},
		{"0 0 9 * * 1-5 1969", true}, // 年越界（<1970）
		{"0 0 9 * * 1-5 2101", true}, // 年越界（>2100）
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			err := ValidateCronSchedule(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCronSchedule(%q) err=%v, wantErr=%v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// TestShouldFire_5FieldsCompat 5 字段 shouldFire 向后兼容
func TestShouldFire_5FieldsCompat(t *testing.T) {
	cm := NewCronManager()
	// 2026-07-21 是周二（weekday=2）
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.Local)

	tests := []struct {
		schedule string
		want     bool
	}{
		{"0 9 * * *", true},   // 每天 9:00
		{"0 9 * * 2", true},   // 周二 9:00
		{"0 9 * * 1-5", true}, // 工作日 9:00
		{"0 10 * * *", false}, // 10:00 不匹配
		{"0 9 * * 1", false},  // 周一不匹配
		{"0 9 * * 6", false},  // 周六不匹配
	}
	for _, tt := range tests {
		t.Run(tt.schedule, func(t *testing.T) {
			got := cm.shouldFire(tt.schedule, now)
			if got != tt.want {
				t.Errorf("shouldFire(%q, %v) = %v, want %v", tt.schedule, now, got, tt.want)
			}
		})
	}
}

// TestShouldFire_6FieldsSecond 6 字段秒级精度
func TestShouldFire_6FieldsSecond(t *testing.T) {
	cm := NewCronManager()
	// 2026-07-21 09:00:30
	now := time.Date(2026, 7, 21, 9, 0, 30, 0, time.Local)

	tests := []struct {
		schedule string
		want     bool
	}{
		{"30 0 9 * * *", true},   // 9:00:30
		{"0 0 9 * * *", false},   // 9:00:00 不匹配
		{"*/30 0 9 * * *", true}, // 每 30 秒
	}
	for _, tt := range tests {
		t.Run(tt.schedule, func(t *testing.T) {
			got := cm.shouldFire(tt.schedule, now)
			if got != tt.want {
				t.Errorf("shouldFire(%q, %v) = %v, want %v", tt.schedule, now, got, tt.want)
			}
		})
	}
}

// TestShouldFire_7FieldsYear 7 字段年字段
func TestShouldFire_7FieldsYear(t *testing.T) {
	cm := NewCronManager()
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.Local)

	tests := []struct {
		schedule string
		want     bool
	}{
		{"0 0 9 * * 1-5 2026", true},  // 2026 年
		{"0 0 9 * * 1-5 2025", false}, // 2025 年不匹配
		{"0 0 9 * * 1-5 2027", false}, // 2027 年不匹配
	}
	for _, tt := range tests {
		t.Run(tt.schedule, func(t *testing.T) {
			got := cm.shouldFire(tt.schedule, now)
			if got != tt.want {
				t.Errorf("shouldFire(%q, %v) = %v, want %v", tt.schedule, now, got, tt.want)
			}
		})
	}
}

// TestShouldFire_LModifier day=L 月末匹配
func TestShouldFire_LModifier(t *testing.T) {
	cm := NewCronManager()
	// 2026-07-31 是 7 月最后一天
	lastDay := time.Date(2026, 7, 31, 9, 0, 0, 0, time.Local)
	// 2026-07-21 不是月末
	notLastDay := time.Date(2026, 7, 21, 9, 0, 0, 0, time.Local)

	if !cm.shouldFire("0 9 L * *", lastDay) {
		t.Error("day=L should fire on last day of month")
	}
	if cm.shouldFire("0 9 L * *", notLastDay) {
		t.Error("day=L should NOT fire on non-last day")
	}
}

// TestShouldFire_WeekdayL 5L 最后一个周 X
func TestShouldFire_WeekdayL(t *testing.T) {
	cm := NewCronManager()
	// 2026-07-31 是周五，且是 7 月最后一个周五
	lastFriday := time.Date(2026, 7, 31, 9, 0, 0, 0, time.Local)
	// 2026-07-24 是周五，但不是最后一个
	notLastFriday := time.Date(2026, 7, 24, 9, 0, 0, 0, time.Local)

	// weekday=5L 表示最后一个周五（cron 中 5=周五）
	if !cm.shouldFire("0 9 * * 5L", lastFriday) {
		t.Error("5L should fire on last Friday of month")
	}
	if cm.shouldFire("0 9 * * 5L", notLastFriday) {
		t.Error("5L should NOT fire on non-last Friday")
	}
}

// TestShouldFire_WModifier 15W 最近工作日
func TestShouldFire_WModifier(t *testing.T) {
	cm := NewCronManager()
	// 2026-07-15 是周三（工作日），15W 直接匹配
	weekday15 := time.Date(2026, 7, 15, 9, 0, 0, 0, time.Local)
	// 2026-07-16 不匹配 15W
	not15 := time.Date(2026, 7, 16, 9, 0, 0, 0, time.Local)

	if !cm.shouldFire("0 9 15W * *", weekday15) {
		t.Error("15W should fire when 15th is a weekday")
	}
	if cm.shouldFire("0 9 15W * *", not15) {
		t.Error("15W should NOT fire on 16th")
	}
}

// TestShouldFire_WModifierWeekend 15W 周末偏移
func TestShouldFire_WModifierWeekend(t *testing.T) {
	cm := NewCronManager()
	// 2026-08-15 是周六，15W 应匹配 14（周五）
	// nearestWeekday(15, 2026, August) = 14
	sat15 := time.Date(2026, 8, 14, 9, 0, 0, 0, time.Local)

	if !cm.shouldFire("0 9 15W * *", sat15) {
		t.Error("15W should fire on Friday 14th when 15th is Saturday")
	}
}

// TestShouldFire_LWModifier LW 月末最近工作日
func TestShouldFire_LWModifier(t *testing.T) {
	cm := NewCronManager()
	// 2026-07-31 是周五（月末且工作日）→ LW 直接匹配
	lastDayWeekday := time.Date(2026, 7, 31, 9, 0, 0, 0, time.Local)
	if !cm.shouldFire("0 9 LW * *", lastDayWeekday) {
		t.Error("LW should fire when last day is weekday")
	}

	// 2026-05-31 是周日，月末最近工作日 = 29（周五）
	nearestWeekdayLast := time.Date(2026, 5, 29, 9, 0, 0, 0, time.Local)
	if !cm.shouldFire("0 9 LW * *", nearestWeekdayLast) {
		t.Error("LW should fire on nearest weekday to month end")
	}
}

// TestShouldFire_HashModifier 5#3 第三个周五
func TestShouldFire_HashModifier(t *testing.T) {
	cm := NewCronManager()
	// 2026-07-17 是 7 月第三个周五
	thirdFriday := time.Date(2026, 7, 17, 9, 0, 0, 0, time.Local)
	// 2026-07-10 是第二个周五
	secondFriday := time.Date(2026, 7, 10, 9, 0, 0, 0, time.Local)

	if !cm.shouldFire("0 9 * * 5#3", thirdFriday) {
		t.Error("5#3 should fire on 3rd Friday")
	}
	if cm.shouldFire("0 9 * * 5#3", secondFriday) {
		t.Error("5#3 should NOT fire on 2nd Friday")
	}
}

// TestLastDayOfMonth_LeapYear 闰年 2 月 29 天
func TestLastDayOfMonth_LeapYear(t *testing.T) {
	tests := []struct {
		year  int
		month time.Month
		want  int
	}{
		{2024, time.February, 29}, // 闰年
		{2026, time.February, 28}, // 平年
		{2000, time.February, 29}, // 世纪闰年
		{1900, time.February, 28}, // 世纪非闰年
		{2026, time.January, 31},
		{2026, time.April, 30},
		{2026, time.December, 31},
	}
	for _, tt := range tests {
		name := string(rune('A' + int(tt.month)))
		if tt.year < 2000 && tt.month == time.February {
			name = "1900_Feb"
		}
		t.Run(name, func(t *testing.T) {
			got := lastDayOfMonth(tt.year, tt.month)
			if got != tt.want {
				t.Errorf("lastDayOfMonth(%d, %v) = %d, want %d", tt.year, tt.month, got, tt.want)
			}
		})
	}
}

// TestNearestWeekday 最近工作日计算
func TestNearestWeekday(t *testing.T) {
	tests := []struct {
		day     int
		year    int
		month   time.Month
		wantDay int
	}{
		// 2026-07-15 是周三（工作日），最近工作日 = 15
		{15, 2026, time.July, 15},
		// 2026-08-15 是周六，最近工作日 = 14（周五）
		{15, 2026, time.August, 14},
		// 2026-08-16 是周日，最近工作日 = 17（周一）
		{16, 2026, time.August, 17},
		// 2026-05-31 是周日（月末），+1 跨月 → 取上月最后一个周五 = 29
		{31, 2026, time.May, 29},
		// 2026-01-31 是周六（月末），-1 = 30（周五，月内）
		{31, 2026, time.January, 30},
		// 2026-02-01 是周日，+1 = 2（周一，月内）
		{1, 2026, time.February, 2},
		// 2026-08-01 是周六，-1 跨月 → 取下周一 = 3
		{1, 2026, time.August, 3},
	}
	for _, tt := range tests {
		// 用 day 作为子测试名（避免重名）
		name := fmt.Sprintf("%d-%d-%d", tt.year, int(tt.month), tt.day)
		t.Run(name, func(t *testing.T) {
			got := nearestWeekday(tt.day, tt.year, tt.month)
			if got != tt.wantDay {
				t.Errorf("nearestWeekday(%d, %d, %v) = %d, want %d",
					tt.day, tt.year, tt.month, got, tt.wantDay)
			}
		})
	}
}

// TestIsLastWeekdayInMonth 判断本月最后一个指定 weekday
func TestIsLastWeekdayInMonth(t *testing.T) {
	// 2026-07-31 是 7 月最后一个周五
	lastFriday := time.Date(2026, 7, 31, 9, 0, 0, 0, time.Local)
	if !isLastWeekdayInMonth(lastFriday, 5) { // 5=周五
		t.Error("2026-07-31 should be last Friday of July 2026")
	}
	// 2026-07-24 不是最后一个周五
	notLastFriday := time.Date(2026, 7, 24, 9, 0, 0, 0, time.Local)
	if isLastWeekdayInMonth(notLastFriday, 5) {
		t.Error("2026-07-24 should NOT be last Friday of July 2026")
	}
}

// TestNthWeekdayInMonth 判断本月第 N 个指定 weekday
func TestNthWeekdayInMonth(t *testing.T) {
	// 2026-07-17 是 7 月第三个周五
	thirdFriday := time.Date(2026, 7, 17, 9, 0, 0, 0, time.Local)
	if !nthWeekdayInMonth(thirdFriday, 5, 3) { // 5=周五, 3=第三个
		t.Error("2026-07-17 should be 3rd Friday of July 2026")
	}
	// 2026-07-10 是第二个周五
	if !nthWeekdayInMonth(time.Date(2026, 7, 10, 9, 0, 0, 0, time.Local), 5, 2) {
		t.Error("2026-07-10 should be 2nd Friday of July 2026")
	}
	// 第 5 个周五通常不存在
	if nthWeekdayInMonth(thirdFriday, 5, 5) {
		t.Error("5th Friday of July 2026 should not exist")
	}
}

// TestShouldFire_QuestionMark ? 与 * 等价
func TestShouldFire_QuestionMark(t *testing.T) {
	cm := NewCronManager()
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.Local)
	// ? 与 * 等价
	if !cm.shouldFire("0 9 ? ? ?", now) {
		t.Error("? should match like *")
	}
}

// TestShouldFire_InvalidSchedule 非法表达式不匹配
func TestShouldFire_InvalidSchedule(t *testing.T) {
	cm := NewCronManager()
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.Local)
	// 4 字段非法
	if cm.shouldFire("0 9 * *", now) {
		t.Error("invalid schedule should not fire")
	}
}

// TestCronManager_Create_ExtendedFormat 创建任务支持扩展格式
func TestCronManager_Create_ExtendedFormat(t *testing.T) {
	cm := NewCronManager()
	tests := []struct {
		schedule string
		wantErr  bool
	}{
		{"0 9 * * 1-5", false},        // 5 字段
		{"30 0 9 * * 1-5", false},     // 6 字段
		{"0 0 9 * * 1-5 2026", false}, // 7 字段
		{"0 9 L * *", false},          // L 修饰符
		{"0 9 15W * *", false},        // W 修饰符
		{"0 9 LW * *", false},         // LW 修饰符
		{"0 9 * * 5#3", false},        // # 修饰符
	}
	for _, tt := range tests {
		t.Run(tt.schedule, func(t *testing.T) {
			job, err := cm.Create(tt.schedule, "test", "desc")
			if (err != nil) != tt.wantErr {
				t.Errorf("Create(%q) err=%v, wantErr=%v", tt.schedule, err, tt.wantErr)
			}
			if err == nil && job == nil {
				t.Error("expected non-nil job on success")
			}
		})
	}
}
