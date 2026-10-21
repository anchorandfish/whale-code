// Package tools — LoopScheduler 增强测试
// 覆盖：持久化、首次立即触发、单次任务、MaxFires、Jitter、pause/resume 命令
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testFilePath 返回临时目录下的持久化文件路径
func testFilePath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "loop_tasks.json")
}

// waitForFire 等待触发，带超时
func waitForFire(t *testing.T, fired chan string, timeout time.Duration) string {
	select {
	case id := <-fired:
		return id
	case <-time.After(timeout):
		t.Fatal("触发超时")
		return ""
	}
}

// ===== 1. 间隔解析测试 =====

// TestIntervalParsing 测试间隔字符串解析
func TestIntervalParsing(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		hasError bool
	}{
		{"30s", 30 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"2h", 2 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"60s", 60 * time.Second, false},
		{"90m", 90 * time.Minute, false},
		{"3h", 3 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		// 无效格式
		{"", 0, true},
		{"abc", 0, true},
		{"5x", 0, true},
		{"m5", 0, true},
		{"5", 0, true},
		{"0s", 0, true},
		{"0m", 0, true},
		{"-1s", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseInterval(tt.input)
			if tt.hasError {
				if err == nil {
					t.Errorf("parseInterval(%q) expected返回错误，但得到 %v", tt.input, result)
				}
			} else {
				if err != nil {
					t.Errorf("parseInterval(%q) 不expected返回错误: %v", tt.input, err)
				}
				if result != tt.expected {
					t.Errorf("parseInterval(%q) = %v, expected %v", tt.input, result, tt.expected)
				}
			}
		})
	}
}

// ===== 2. 基础任务创建测试 =====

// TestLoopTaskCreation 测试循环任务创建
func TestLoopTaskCreation(t *testing.T) {
	var fired atomic.Int32
	ls := NewLoopScheduler(func(taskID, prompt string) {
		fired.Add(1)
	}, "")

	task, err := ls.Create("5m", "检查部署状态")
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	if task.ID == "" {
		t.Error("任务 ID should not be empty")
	}
	if task.Interval != 5*time.Minute {
		t.Errorf("任务间隔 = %v, expected %v", task.Interval, 5*time.Minute)
	}
	if task.IntervalStr != "5m" {
		t.Errorf("任务间隔字符串 = %q, expected %q", task.IntervalStr, "5m")
	}
	if task.Prompt != "检查部署状态" {
		t.Errorf("任务提示词 = %q, expected %q", task.Prompt, "检查部署状态")
	}
	if task.Status != "active" {
		t.Errorf("任务状态 = %q, expected %q", task.Status, "active")
	}
	if task.FireCount != 1 { // 默认立即触发
		t.Errorf("任务触发次数 = %d, expected 1（立即触发）", task.FireCount)
	}
	if task.MaxAge != defaultMaxAge {
		t.Errorf("任务最大存活时间 = %v, expected %v", task.MaxAge, defaultMaxAge)
	}
	if !task.Recurring {
		t.Error("默认应为循环任务")
	}

	// 验证间隔过短会rejected
	_, err = ls.Create("10s", "太短了")
	if err == nil {
		t.Error("间隔小于 30 秒应该返回错误")
	}

	// 验证空提示词会rejected
	_, err = ls.Create("5m", "")
	if err == nil {
		t.Error("空提示词应该返回错误")
	}
}

// ===== 3. 任务列表测试 =====

// TestLoopTaskList 测试列出循环任务
func TestLoopTaskList(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	// 空列表
	tasks := ls.List()
	if len(tasks) != 0 {
		t.Errorf("空调度器应该返回空列表，得到 %d 项", len(tasks))
	}

	// 创建多个任务
	ls.Create("5m", "任务1")
	ls.Create("2h", "任务2")
	ls.Create("1d", "任务3")

	tasks = ls.List()
	if len(tasks) != 3 {
		t.Errorf("列表长度 = %d, expected 3", len(tasks))
	}

	// 取消一个任务
	task := tasks[0]
	ls.Cancel(task.ID)

	tasks = ls.List()
	if len(tasks) != 2 {
		t.Errorf("取消后列表长度 = %d, expected 2", len(tasks))
	}

	// ListAll 应该包含已取消的
	allTasks := ls.ListAll()
	if len(allTasks) != 3 {
		t.Errorf("ListAll 长度 = %d, expected 3", len(allTasks))
	}
}

// ===== 4. 取消测试 =====

// TestLoopTaskCancel test cancelled循环任务
func TestLoopTaskCancel(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	task, _ := ls.Create("5m", "test cancelled")

	err := ls.Cancel(task.ID)
	if err != nil {
		t.Errorf("取消任务失败: %v", err)
	}

	got, ok := ls.Get(task.ID)
	if !ok {
		t.Error("任务应该仍然存在")
	}
	if got.Status != "cancelled" {
		t.Errorf("任务状态 = %q, expected %q", got.Status, "cancelled")
	}

	// 重复取消应该返回错误
	err = ls.Cancel(task.ID)
	if err == nil {
		t.Error("重复取消应该返回错误")
	}

	// 取消不存在的任务应该返回错误
	err = ls.Cancel("nonexistent")
	if err == nil {
		t.Error("取消不存在的任务应该返回错误")
	}
}

// ===== 5. 暂停/恢复测试 =====

// TestLoopTaskPauseResume 测试暂停和恢复循环任务
func TestLoopTaskPauseResume(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	task, _ := ls.Create("5m", "测试暂停")

	// 暂停
	err := ls.Pause(task.ID)
	if err != nil {
		t.Errorf("暂停任务失败: %v", err)
	}
	got, _ := ls.Get(task.ID)
	if got.Status != "paused" {
		t.Errorf("任务状态 = %q, expected %q", got.Status, "paused")
	}

	// 恢复
	err = ls.Resume(task.ID)
	if err != nil {
		t.Errorf("恢复任务失败: %v", err)
	}
	got, _ = ls.Get(task.ID)
	if got.Status != "active" {
		t.Errorf("任务状态 = %q, expected %q", got.Status, "active")
	}

	// 暂停已取消的任务应该失败
	ls.Cancel(task.ID)
	err = ls.Pause(task.ID)
	if err == nil {
		t.Error("暂停已取消的任务应该返回错误")
	}

	// 恢复活跃的任务应该失败
	task2, _ := ls.Create("5m", "test recovery活跃")
	err = ls.Resume(task2.ID)
	if err == nil {
		t.Error("恢复活跃的任务应该返回错误")
	}
}

// ===== 6. 启动/停止测试 =====

// TestLoopSchedulerStartStop 测试调度器启动和停止
func TestLoopSchedulerStartStop(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	ls.Start()
	if !ls.started {
		t.Error("调度器应该已启动")
	}

	ls.Start() // 重复启动应该无操作

	ls.Stop()
	if ls.started {
		t.Error("调度器应该已停止")
	}

	ls.Stop() // 重复停止应该无操作
}

// ===== 7. 触发测试 =====

// TestLoopSchedulerFiring 测试调度器触发回调
func TestLoopSchedulerFiring(t *testing.T) {
	var mu sync.Mutex
	var firedTasks []string
	var firedPrompts []string

	ls := NewLoopScheduler(func(taskID, prompt string) {
		mu.Lock()
		firedTasks = append(firedTasks, taskID)
		firedPrompts = append(firedPrompts, prompt)
		mu.Unlock()
	}, "")

	// 创建一个间隔为 1 秒的任务（覆盖为测试用）
	task, _ := ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr:     "30s",
		Prompt:          "测试触发",
		Recurring:       true,
		FireImmediately: false, // 不立即触发，让调度器触发
	})
	ls.mu.Lock()
	task.Interval = 1 * time.Second
	task.jitterOff = 0 // 测试时去掉抖动
	ls.mu.Unlock()

	ls.Start()
	defer ls.Stop()

	time.Sleep(2500 * time.Millisecond)

	mu.Lock()
	if len(firedTasks) == 0 {
		t.Error("任务应该已被触发")
	}
	if len(firedPrompts) == 0 || firedPrompts[0] != "测试触发" {
		t.Errorf("触发提示词 = %v, expected [测试触发]", firedPrompts)
	}
	mu.Unlock()

	got, _ := ls.Get(task.ID)
	if got.FireCount < 1 {
		t.Errorf("触发次数 = %d, expected >= 1", got.FireCount)
	}
	if got.LastFiredAt == nil {
		t.Error("LastFiredAt 不should be nil")
	}
}

// TestLoopSchedulerPausedTaskNotFired 测试暂停的任务不会被触发
func TestLoopSchedulerPausedTaskNotFired(t *testing.T) {
	var fired atomic.Int32

	ls := NewLoopScheduler(func(taskID, prompt string) {
		fired.Add(1)
	}, "")

	task, _ := ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr:     "30s",
		Prompt:          "暂停测试",
		Recurring:       true,
		FireImmediately: false,
	})
	ls.mu.Lock()
	task.Interval = 1 * time.Second
	task.jitterOff = 0
	ls.mu.Unlock()

	ls.Pause(task.ID)

	ls.Start()
	defer ls.Stop()

	time.Sleep(2500 * time.Millisecond)

	if fired.Load() > 0 {
		t.Error("暂停的任务不应该被触发")
	}
}

// ===== 8. 过期测试 =====

// TestMaxAgeExpiry 测试任务自动过期
func TestMaxAgeExpiry(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	task, _ := ls.Create("5m", "过期测试")

	ls.mu.Lock()
	task.MaxAge = 1 * time.Nanosecond
	task.CreatedAt = time.Now().Add(-2 * time.Second)
	ls.mu.Unlock()

	count := ls.CleanExpired()
	if count < 1 {
		t.Errorf("应该清理至少 1 个过期任务，actual清理 %d 个", count)
	}

	got, _ := ls.Get(task.ID)
	if got.Status != "cancelled" {
		t.Errorf("过期任务状态 = %q, expected %q", got.Status, "cancelled")
	}
}

// ===== 9. 并发测试 =====

// TestConcurrentAccess 测试并发访问线程安全
func TestConcurrentAccess(t *testing.T) {
	var fired atomic.Int32
	ls := NewLoopScheduler(func(taskID, prompt string) {
		fired.Add(1)
	}, "")

	ls.Start()
	defer ls.Stop()

	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ls.Create("30s", "并发任务")
		}(i)
	}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ls.List()
		}()
	}

	wg.Wait()

	tasks := ls.List()
	if len(tasks) != 50 {
		t.Errorf("并发创建后任务数 = %d, expected 50", len(tasks))
	}

	for _, task := range tasks {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			ls.Cancel(id)
		}(task.ID)
	}

	wg.Wait()

	remaining := ls.List()
	if len(remaining) != 0 {
		t.Errorf("全部取消后剩余任务数 = %d, expected 0", len(remaining))
	}
}

// ===== 10. 格式化测试 =====

// TestFormatList 测试格式化任务列表
func TestFormatList(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	result := ls.FormatList()
	// 文案已改为「英文主行 + 缩进中文辅行」，全等比较会恒假（测试照样绿但不再拦）⇒ 改 Contains，意图不变。
	if !strings.Contains(result, "没有活跃的循环任务") {
		t.Errorf("空列表格式化结果 = %q, expected 含 %q", result, "没有活跃的循环任务")
	}

	ls.Create("5m", "检查状态")
	result = ls.FormatList()
	if result == "" {
		t.Error("格式化结果should not be empty")
	}
	if !contains(result, "检查状态") {
		t.Errorf("格式化结果should contain提示词 %q", "检查状态")
	}
	if !contains(result, "5m") {
		t.Errorf("格式化结果should contain间隔 %q", "5m")
	}
}

// ===== 11. Get 测试 =====

// TestGetTask 测试获取单个任务
func TestGetTask(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	_, ok := ls.Get("nonexistent")
	if ok {
		t.Error("获取不存在的任务应该返回 false")
	}

	task, _ := ls.Create("5m", "获取测试")
	got, ok := ls.Get(task.ID)
	if !ok {
		t.Error("获取存在的任务应该返回 true")
	}
	if got.Prompt != "获取测试" {
		t.Errorf("获取的任务提示词 = %q, expected %q", got.Prompt, "获取测试")
	}
}

// ===== 12. CleanExpired 测试 =====

// TestCleanExpired test cleanup过期任务
func TestCleanExpired(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	task1, _ := ls.Create("5m", "过期任务1")
	task2, _ := ls.Create("5m", "正常任务")

	ls.mu.Lock()
	task1.MaxAge = 1 * time.Nanosecond
	task1.CreatedAt = time.Now().Add(-2 * time.Second)
	ls.mu.Unlock()

	count := ls.CleanExpired()
	if count < 1 {
		t.Errorf("应该清理至少 1 个过期任务，actual %d", count)
	}

	got, ok := ls.Get(task2.ID)
	if !ok || got.Status != "active" {
		t.Error("未过期的任务应该仍然活跃")
	}
}

// ========================================
// 新增测试：持久化、立即触发、单次、MaxFires、Jitter
// ========================================

// ===== 13. 持久化测试 =====

// TestPersistenceSave 测试创建后文件写入
func TestPersistenceSave(t *testing.T) {
	fp := testFilePath(t)
	ls := NewLoopScheduler(func(taskID, prompt string) {}, fp)

	ls.Create("5m", "持久化测试")

	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("持久化文件不存在: %v", err)
	}

	var store loopStore
	if err := json.Unmarshal(data, &store); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}

	if len(store.Tasks) != 1 {
		t.Errorf("持久化任务数 = %d, expected 1", len(store.Tasks))
	}
	if store.Tasks[0].Prompt != "持久化测试" {
		t.Errorf("持久化提示词 = %q, expected %q", store.Tasks[0].Prompt, "持久化测试")
	}
}

// TestPersistenceLoad 测试从文件加载任务
func TestPersistenceLoad(t *testing.T) {
	fp := testFilePath(t)

	// 先创建并保存
	ls1 := NewLoopScheduler(func(taskID, prompt string) {}, fp)
	ls1.Create("5m", "加载测试")
	ls1.Create("2h", "第二个任务")

	// 重新加载
	ls2 := NewLoopScheduler(func(taskID, prompt string) {}, fp)
	tasks := ls2.List()
	if len(tasks) != 2 {
		t.Errorf("加载后任务数 = %d, expected 2", len(tasks))
	}
}

// TestPersistenceLoadExpired 测试加载时跳过已过期任务
func TestPersistenceLoadExpired(t *testing.T) {
	fp := testFilePath(t)

	ls1 := NewLoopScheduler(func(taskID, prompt string) {}, fp)
	task, _ := ls1.Create("5m", "过期任务")
	// 手动修改文件使任务过期
	ls1.mu.Lock()
	task.MaxAge = 1 * time.Nanosecond
	task.CreatedAt = time.Now().Add(-2 * time.Second)
	ls1.mu.Unlock()
	ls1.saveToFile()

	// 重新加载
	ls2 := NewLoopScheduler(func(taskID, prompt string) {}, fp)
	tasks := ls2.List()
	if len(tasks) != 0 {
		t.Errorf("过期任务不应被加载，得到 %d 个", len(tasks))
	}
}

// TestPersistenceLoadResetLastFiredAt 测试加载时重置 LastFiredAt
func TestPersistenceLoadResetLastFiredAt(t *testing.T) {
	fp := testFilePath(t)

	ls1 := NewLoopScheduler(func(taskID, prompt string) {}, fp)
	task, _ := ls1.Create("5m", "重置测试")
	// 立即触发已设置 LastFiredAt
	if task.LastFiredAt == nil {
		t.Error("立即触发后 LastFiredAt 不should be nil")
	}
	ls1.saveToFile()

	// 重新加载
	ls2 := NewLoopScheduler(func(taskID, prompt string) {}, fp)
	got, ok := ls2.Get(task.ID)
	if !ok {
		t.Fatal("任务应该存在")
	}
	if got.LastFiredAt != nil {
		t.Error("加载后 LastFiredAt 应被重置为 nil，防止补触发")
	}
}

// TestPersistenceSaveOnCancel test cancelled后文件更新
func TestPersistenceSaveOnCancel(t *testing.T) {
	fp := testFilePath(t)
	ls := NewLoopScheduler(func(taskID, prompt string) {}, fp)

	task, _ := ls.Create("5m", "取消持久化")
	ls.Cancel(task.ID)

	data, _ := os.ReadFile(fp)
	var store loopStore
	json.Unmarshal(data, &store)
	if store.Tasks[0].Status != "cancelled" {
		t.Errorf("取消后状态 = %q, expected cancelled", store.Tasks[0].Status)
	}
}

// TestPersistenceSaveOnPause 测试暂停后文件更新
func TestPersistenceSaveOnPause(t *testing.T) {
	fp := testFilePath(t)
	ls := NewLoopScheduler(func(taskID, prompt string) {}, fp)

	task, _ := ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr: "5m", Prompt: "暂停持久化",
		Recurring: true, FireImmediately: false,
	})
	ls.Pause(task.ID)

	data, _ := os.ReadFile(fp)
	var store loopStore
	json.Unmarshal(data, &store)
	if store.Tasks[0].Status != "paused" {
		t.Errorf("暂停后状态 = %q, expected paused", store.Tasks[0].Status)
	}
}

// TestPersistenceSaveOnResume test recovery后文件更新
func TestPersistenceSaveOnResume(t *testing.T) {
	fp := testFilePath(t)
	ls := NewLoopScheduler(func(taskID, prompt string) {}, fp)

	task, _ := ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr: "5m", Prompt: "恢复持久化",
		Recurring: true, FireImmediately: false,
	})
	ls.Pause(task.ID)
	ls.Resume(task.ID)

	data, _ := os.ReadFile(fp)
	var store loopStore
	json.Unmarshal(data, &store)
	if store.Tasks[0].Status != "active" {
		t.Errorf("恢复后状态 = %q, expected active", store.Tasks[0].Status)
	}
}

// TestPersistenceCorruptedFile 测试损坏文件不崩溃
func TestPersistenceCorruptedFile(t *testing.T) {
	fp := testFilePath(t)
	os.WriteFile(fp, []byte("{invalid json}"), 0644)

	ls := NewLoopScheduler(func(taskID, prompt string) {}, fp)
	tasks := ls.List()
	if len(tasks) != 0 {
		t.Error("损坏文件should return empty列表")
	}
}

// TestPersistenceMissingFile 测试文件不存在时正常初始化
func TestPersistenceMissingFile(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "nonexistent", "loop_tasks.json")
	ls := NewLoopScheduler(func(taskID, prompt string) {}, fp)
	tasks := ls.List()
	if len(tasks) != 0 {
		t.Error("不存在的文件should return empty列表")
	}
}

// TestPersistenceNextID 测试 nextID 从文件恢复后继续递增
func TestPersistenceNextID(t *testing.T) {
	fp := testFilePath(t)

	ls1 := NewLoopScheduler(func(taskID, prompt string) {}, fp)
	_, _ = ls1.Create("5m", "任务1")
	task2, _ := ls1.Create("5m", "任务2")

	ls2 := NewLoopScheduler(func(taskID, prompt string) {}, fp)
	task3, _ := ls2.Create("5m", "任务3")

	if task3.ID <= task2.ID {
		t.Errorf("新任务 ID %q should be greater than旧最大 ID %q", task3.ID, task2.ID)
	}
}

// ===== 14. 首次立即触发测试 =====

// TestFireImmediately 测试立即触发
func TestFireImmediately(t *testing.T) {
	fired := make(chan string, 1)
	ls := NewLoopScheduler(func(taskID, prompt string) {
		select {
		case fired <- taskID:
		default:
		}
	}, "")

	task, err := ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr:     "5m",
		Prompt:          "立即触发测试",
		Recurring:       true,
		FireImmediately: true,
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 验证 onFire 被调用
	select {
	case id := <-fired:
		if id != task.ID {
			t.Errorf("触发的任务 ID = %q, expected %q", id, task.ID)
		}
	case <-time.After(time.Second):
		t.Error("立即触发未执行")
	}

	// 验证 FireCount
	if task.FireCount != 1 {
		t.Errorf("FireCount = %d, expected 1", task.FireCount)
	}
}

// TestFireDelayed 测试延迟触发
func TestFireDelayed(t *testing.T) {
	var fired atomic.Int32
	ls := NewLoopScheduler(func(taskID, prompt string) {
		fired.Add(1)
	}, "")

	ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr:     "5m",
		Prompt:          "延迟触发测试",
		Recurring:       true,
		FireImmediately: false,
	})

	if fired.Load() != 0 {
		t.Error("延迟模式下不应立即触发")
	}
}

// TestFireImmediatelyCount 测试立即触发后 FireCount
func TestFireImmediatelyCount(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	task, _ := ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr:     "5m",
		Prompt:          "计数测试",
		Recurring:       true,
		FireImmediately: true,
	})

	if task.FireCount != 1 {
		t.Errorf("立即触发后 FireCount = %d, expected 1", task.FireCount)
	}
	if task.LastFiredAt == nil {
		t.Error("立即触发后 LastFiredAt 不should be nil")
	}
}

// ===== 15. 单次任务测试 =====

// TestOneShotTask 测试单次任务触发后完成
func TestOneShotTask(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	task, _ := ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr:     "30s",
		Prompt:          "单次测试",
		Recurring:       false,
		FireImmediately: true,
	})

	if task.Status != "completed" {
		t.Errorf("单次任务触发后状态 = %q, expected completed", task.Status)
	}
}

// TestOneShotNotReFired 测试已完成的单次任务不再触发
func TestOneShotNotReFired(t *testing.T) {
	var fired atomic.Int32
	ls := NewLoopScheduler(func(taskID, prompt string) {
		fired.Add(1)
	}, "")

	// 创建单次任务，不立即触发，让调度器触发
	task, _ := ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr:     "30s",
		Prompt:          "单次不重复",
		Recurring:       false,
		FireImmediately: false,
	})

	// 覆盖间隔为 1 秒测试
	ls.mu.Lock()
	task.Interval = 1 * time.Second
	task.jitterOff = 0
	ls.mu.Unlock()

	ls.Start()
	defer ls.Stop()

	// 等待足够时间让调度器触发（应该只触发1次，然后变为 completed）
	time.Sleep(3500 * time.Millisecond)

	// fired 应该只有 1（调度器触发的那次），completed 后不再触发
	if fired.Load() != 1 {
		t.Errorf("单次任务触发次数 = %d, expected 1", fired.Load())
	}

	// 验证状态为 completed
	got, _ := ls.Get(task.ID)
	if got.Status != "completed" {
		t.Errorf("单次任务状态 = %q, expected completed", got.Status)
	}
}

// TestOneShotInList 测试已完成任务不在 List 中
func TestOneShotInList(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr:     "30s",
		Prompt:          "单次列表",
		Recurring:       false,
		FireImmediately: true,
	})

	tasks := ls.List()
	if len(tasks) != 0 {
		t.Errorf("已完成任务不应在 List 中，得到 %d 个", len(tasks))
	}

	allTasks := ls.ListAll()
	if len(allTasks) != 1 {
		t.Errorf("ListAll should contain已完成任务，得到 %d 个", len(allTasks))
	}
}

// ===== 16. MaxFires 测试 =====

// TestMaxFiresLimit 测试最大触发次数限制
func TestMaxFiresLimit(t *testing.T) {
	var fired atomic.Int32
	ls := NewLoopScheduler(func(taskID, prompt string) {
		fired.Add(1)
	}, "")

	task, _ := ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr:     "30s",
		Prompt:          "限制3次",
		Recurring:       true,
		FireImmediately: true,
		MaxFires:        3,
	})

	// 立即触发1次
	if task.FireCount != 1 {
		t.Errorf("首次 FireCount = %d, expected 1", task.FireCount)
	}

	// 覆盖间隔为 1 秒
	ls.mu.Lock()
	task.Interval = 1 * time.Second
	task.jitterOff = 0
	ls.mu.Unlock()

	ls.Start()
	defer ls.Stop()

	// 等待足够时间让调度器再触发 2 次
	time.Sleep(3500 * time.Millisecond)

	got, _ := ls.Get(task.ID)
	if got.Status != "completed" {
		t.Errorf("达到 MaxFires 后状态 = %q, expected completed", got.Status)
	}
	if got.FireCount > 3 {
		t.Errorf("触发次数 = %d, 不应超过 3", got.FireCount)
	}
}

// TestMaxFiresZero 测试 MaxFires=0 无限制
func TestMaxFiresZero(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	task, _ := ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr:     "5m",
		Prompt:          "无限制",
		Recurring:       true,
		FireImmediately: true,
		MaxFires:        0,
	})

	if task.MaxFires != 0 {
		t.Error("MaxFires 应为 0")
	}
	if task.Status != "active" {
		t.Errorf("无限制任务状态 = %q, expected active", task.Status)
	}
}

// ===== 17. Jitter 测试 =====

// TestJitterRange 测试抖动在scope内
func TestJitterRange(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("loop_%d", i)
		offset := computeJitter(id, defaultJitterRange)
		if offset < -defaultJitterRange || offset > defaultJitterRange {
			t.Errorf("jitter 偏移 %v 超出scope ±%v (id=%s)", offset, defaultJitterRange, id)
		}
	}
}

// TestJitterDeterministic 测试同一 ID 的抖动确定性
func TestJitterDeterministic(t *testing.T) {
	offset1 := computeJitter("loop_42", defaultJitterRange)
	offset2 := computeJitter("loop_42", defaultJitterRange)
	if offset1 != offset2 {
		t.Errorf("同一 ID 的抖动应确定: %v != %v", offset1, offset2)
	}
}

// TestJitterDifferentIDs 测试不同 ID 的抖动不同
func TestJitterDifferentIDs(t *testing.T) {
	offset1 := computeJitter("loop_1", defaultJitterRange)
	offset2 := computeJitter("loop_2", defaultJitterRange)
	// 大概率不同（哈希分布）
	if offset1 == offset2 {
		// 不算错误，但值得注意
		t.Logf("不同 ID 产生了相同抖动: %v", offset1)
	}
}

// TestJitterZeroRange 测试零抖动scope
func TestJitterZeroRange(t *testing.T) {
	offset := computeJitter("loop_1", 0)
	if offset != 0 {
		t.Errorf("零抖动scopeshould return 0, 得到 %v", offset)
	}
}

// ===== 18. Cancel 已完成任务测试 =====

// TestCancelCompletedTask test cancelled已完成的任务
func TestCancelCompletedTask(t *testing.T) {
	ls := NewLoopScheduler(func(taskID, prompt string) {}, "")

	task, _ := ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr:     "30s",
		Prompt:          "已完成任务",
		Recurring:       false,
		FireImmediately: true,
	})

	err := ls.Cancel(task.ID)
	if err == nil {
		t.Error("取消已完成的任务应该返回错误")
	}
}

// ===== helper functions =====

// contains 检查字符串是否包含子串
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
