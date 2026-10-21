package monitor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewManager_DefaultMaxConcurrency 验证 NewManager 默认 maxConcurrency=10
func TestNewManager_DefaultMaxConcurrency(t *testing.T) {
	m := NewManager()
	if m.MaxConcurrency() != DefaultMaxConcurrency {
		t.Errorf("MaxConcurrency() = %d, want %d", m.MaxConcurrency(), DefaultMaxConcurrency)
	}
	if m.sem == nil {
		t.Fatal("sem field is nil after NewManager()")
	}
	if cap(m.sem) != DefaultMaxConcurrency {
		t.Errorf("sem capacity = %d, want %d", cap(m.sem), DefaultMaxConcurrency)
	}
}

// TestNewManager_WithMaxConcurrency 验证 WithMaxConcurrency(n) 正常设置
func TestNewManager_WithMaxConcurrency(t *testing.T) {
	m := NewManager(WithMaxConcurrency(5))
	if m.MaxConcurrency() != 5 {
		t.Errorf("MaxConcurrency() = %d, want 5", m.MaxConcurrency())
	}
	if cap(m.sem) != 5 {
		t.Errorf("sem capacity = %d, want 5", cap(m.sem))
	}
}

// TestNewManager_WithMaxConcurrency_Guard 验证 WithMaxConcurrency(0)/(-1) 守卫为 minMaxConcurrency
func TestNewManager_WithMaxConcurrency_Guard(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want int
	}{
		{"zero", 0, minMaxConcurrency},
		{"negative", -1, minMaxConcurrency},
		{"negative_large", -100, minMaxConcurrency},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager(WithMaxConcurrency(tt.n))
			if m.MaxConcurrency() != tt.want {
				t.Errorf("WithMaxConcurrency(%d) = %d, want %d", tt.n, m.MaxConcurrency(), tt.want)
			}
			if cap(m.sem) != tt.want {
				t.Errorf("sem capacity = %d, want %d", cap(m.sem), tt.want)
			}
		})
	}
}

// TestNewManager_SemaphoreNotNil 验证 NewManager 后 sem 字段非 nil（优化 5）
func TestNewManager_SemaphoreNotNil(t *testing.T) {
	m := NewManager()
	if m.sem == nil {
		t.Fatal("sem field is nil after NewManager()")
	}

	m2 := NewManager(WithMaxConcurrency(3))
	if m2.sem == nil {
		t.Fatal("sem field is nil after NewManager(WithMaxConcurrency(3))")
	}
	if cap(m2.sem) != 3 {
		t.Errorf("sem capacity = %d, want 3", cap(m2.sem))
	}
}

// TestManager_TryStart_RejectsWhenFull 验证 TryStart 在并发满时返回 ErrConcurrencyLimit
func TestManager_TryStart_RejectsWhenFull(t *testing.T) {
	m := NewManager(WithMaxConcurrency(2))

	// 用 TryAcquire 占满 2 个令牌
	if err := m.TryAcquire(); err != nil {
		t.Fatalf("TryAcquire 1 failed: %v", err)
	}
	if err := m.TryAcquire(); err != nil {
		t.Fatalf("TryAcquire 2 failed: %v", err)
	}

	// 第 3 次 TryStart 应返回 ErrConcurrencyLimit
	_, err := m.TryStart("echo hello", "test", "")
	if !errors.Is(err, ErrConcurrencyLimit) {
		t.Errorf("TryStart when full err = %v, want ErrConcurrencyLimit", err)
	}

	// 释放 1 个令牌后 TryStart 应成功
	m.Release()
	proc, err := m.TryStart("echo hello", "test", "")
	if err != nil {
		t.Errorf("TryStart after Release err = %v, want nil", err)
	}
	if proc == nil {
		t.Error("TryStart returned nil proc after Release")
	}

	// 等待进程结束，归还令牌
	if _, done := proc.WaitForDone(10 * time.Second); !done {
		t.Error("process did not finish in time")
	}
}

// TestManager_Start_BlocksWhenFull 验证 Start 在并发满时阻塞等待
func TestManager_Start_BlocksWhenFull(t *testing.T) {
	m := NewManager(WithMaxConcurrency(1))

	// 占满 1 个令牌
	if err := m.TryAcquire(); err != nil {
		t.Fatalf("TryAcquire failed: %v", err)
	}

	// Start 应阻塞，用 goroutine 检测
	started := atomic.Int32{}
	go func() {
		proc, err := m.Start("echo hello", "test", "")
		if err != nil {
			t.Errorf("Start after block err = %v", err)
			return
		}
		started.Store(1)
		if _, done := proc.WaitForDone(10 * time.Second); !done {
			t.Error("process did not finish in time")
		}
	}()

	// 等待 200ms，确认 Start 仍在阻塞
	time.Sleep(200 * time.Millisecond)
	if started.Load() != 0 {
		t.Fatal("Start did not block when semaphore full")
	}

	// 释放令牌，Start 应继续
	m.Release()

	// 等待 goroutine 完成
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if started.Load() == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if started.Load() != 1 {
		t.Fatal("Start did not proceed after Release")
	}
}

// TestManager_StartFailure_ReleasesSemaphore 验证进程结束/启动失败后令牌必定归还（优化 4）。
//
// ⚠️ 前提更正（2026-09-22 · P-014 A1 **触发暴露**，A/B 实测：修前 PASS / 修后 FAIL）：
// 本用例原来的注释假设「security 层 Allow 这种命令」且「失败发生在 cmd.Start」—— 两点实测都不成立：
//
//	· `"/nonexistent/binary/…"` 被判**破坏性档（Ask）**，修前 `Start` 在**取令牌之前**就返回错误
//
// ⇒ 本用例从未真的取过令牌 ⇒ 断言恒真，属**空转通过**（它声称要验的那条路径根本没跑到）；
//
//	· `buildAndStartProcess` 用 `sh -c <command>` 启动 ⇒ `cmd.Start` **成功**，内层命令以 127 退出。
//
// P-014 后 Ask 不再拦截 ⇒ 本用例**第一次真的走到**令牌管理路径 ⇒ 断言必须等进程结束
// （令牌在 `waitProcess` 的 defer 里归还，见 buildAndStartProcess 注释；
// `WaitForDone` 在状态变更后即返回，与 defer 归还之间还有一个窗口 ⇒ 轮询，同
// TestManager_StopAll_ReleasesAllSemaphore 的既有写法）。
func TestManager_StartFailure_ReleasesSemaphore(t *testing.T) {
	m := NewManager(WithMaxConcurrency(1))

	proc, err := m.Start("/nonexistent/binary/that/should/not/exist", "fail test", "")
	if err != nil {
		// 仍可能存在「未取令牌即失败」的路径（如 security 硬拒绝）—— 此时令牌自然可用。
		if err := m.TryAcquire(); err != nil {
			t.Errorf("Start 未取令牌却失败（err=%v），而 TryAcquire 仍失败 = %v", err, err)
		} else {
			m.Release()
		}
		return
	}
	if proc == nil {
		t.Fatal("Start 既未报错也未返回进程（不可判）")
	}

	if _, done := proc.WaitForDone(10 * time.Second); !done {
		t.Fatal("进程未在限期内结束")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := m.TryAcquire(); err == nil {
			m.Release() // 归还，避免影响后续用例
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("进程结束后令牌未归还（优化 4 的回归）")
}

// TestManager_StopAll_ReleasesAllSemaphore 验证 StopAll 后 TryAcquire 全部成功
func TestManager_StopAll_ReleasesAllSemaphore(t *testing.T) {
	m := NewManager(WithMaxConcurrency(3))

	// 启动 3 个长运行进程
	procs := make([]*Process, 0, 3)
	for i := 0; i < 3; i++ {
		proc, err := m.Start("sleep 60", "long", "")
		if err != nil {
			t.Fatalf("Start %d failed: %v", i, err)
		}
		procs = append(procs, proc)
	}

	// 此时令牌已满
	if err := m.TryAcquire(); !errors.Is(err, ErrConcurrencyLimit) {
		t.Errorf("TryAcquire when full err = %v, want ErrConcurrencyLimit", err)
	}

	// StopAll 应让所有 waitProcess 退出并归还令牌
	m.StopAll()

	// 等待所有进程结束（status 不再为 running）
	for _, proc := range procs {
		if _, done := proc.WaitForDone(10 * time.Second); !done {
			t.Errorf("process %s did not stop in time", proc.ID)
		}
	}

	// 令牌应全部归还。注意：WaitForDone 在状态变更后返回，
	// 但 waitProcess 的 defer { <-m.sem } 在状态变更之后才执行，
	// 因此需要轮询等待令牌释放（defer 执行窗口）
	deadline := time.Now().Add(5 * time.Second)
	acquired := 0
	for time.Now().Before(deadline) && acquired < 3 {
		if err := m.TryAcquire(); err == nil {
			acquired++
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if acquired != 3 {
		t.Errorf("after StopAll acquired %d tokens, want 3", acquired)
	}
	// 归还所有令牌
	for i := 0; i < acquired; i++ {
		m.Release()
	}
}

// TestManager_MaxConcurrency_1 边界测试：上限 1，串行化进程
func TestManager_MaxConcurrency_1(t *testing.T) {
	m := NewManager(WithMaxConcurrency(1))
	if m.MaxConcurrency() != 1 {
		t.Fatalf("MaxConcurrency = %d, want 1", m.MaxConcurrency())
	}

	// 启动一个短进程
	proc, err := m.Start("echo hello", "test", "")
	if err != nil {
		t.Fatalf("Start 1 failed: %v", err)
	}
	if _, done := proc.WaitForDone(10 * time.Second); !done {
		t.Fatal("process 1 did not finish in time")
	}

	// 令牌应已归还，可启动第二个
	proc2, err := m.Start("echo world", "test", "")
	if err != nil {
		t.Fatalf("Start 2 failed: %v", err)
	}
	if _, done := proc2.WaitForDone(10 * time.Second); !done {
		t.Fatal("process 2 did not finish in time")
	}
}

// TestManager_MaxConcurrency_0_Guard 边界测试：WithMaxConcurrency(0) 守卫为 1
func TestManager_MaxConcurrency_0_Guard(t *testing.T) {
	m := NewManager(WithMaxConcurrency(0))
	if m.MaxConcurrency() != minMaxConcurrency {
		t.Errorf("MaxConcurrency = %d, want %d", m.MaxConcurrency(), minMaxConcurrency)
	}
	if cap(m.sem) != minMaxConcurrency {
		t.Errorf("sem capacity = %d, want %d", cap(m.sem), minMaxConcurrency)
	}
}

// TestManager_MaxConcurrency_Negative_Guard 边界测试：WithMaxConcurrency(-1) 守卫为 1
func TestManager_MaxConcurrency_Negative_Guard(t *testing.T) {
	m := NewManager(WithMaxConcurrency(-1))
	if m.MaxConcurrency() != minMaxConcurrency {
		t.Errorf("MaxConcurrency = %d, want %d", m.MaxConcurrency(), minMaxConcurrency)
	}
}

// TestManager_Acquire_TryAcquire_Release 验证独立 API 工作流程（C17 复用测试）
func TestManager_Acquire_TryAcquire_Release(t *testing.T) {
	m := NewManager(WithMaxConcurrency(2))

	// TryAcquire 应成功 2 次
	if err := m.TryAcquire(); err != nil {
		t.Errorf("TryAcquire 1 err = %v", err)
	}
	if err := m.TryAcquire(); err != nil {
		t.Errorf("TryAcquire 2 err = %v", err)
	}

	// 第 3 次 TryAcquire 应失败
	if err := m.TryAcquire(); !errors.Is(err, ErrConcurrencyLimit) {
		t.Errorf("TryAcquire 3 err = %v, want ErrConcurrencyLimit", err)
	}

	// Release 1 个，TryAcquire 应成功
	m.Release()
	if err := m.TryAcquire(); err != nil {
		t.Errorf("TryAcquire after Release err = %v", err)
	}

	// 清理
	m.Release()
	m.Release()
}

// TestManager_Acquire_CtxCancel 验证 Acquire 在 ctx 取消时返回 ctx.Err()
func TestManager_Acquire_CtxCancel(t *testing.T) {
	m := NewManager(WithMaxConcurrency(1))

	// 占满令牌
	if err := m.TryAcquire(); err != nil {
		t.Fatalf("TryAcquire failed: %v", err)
	}

	// Acquire 应阻塞，ctx 取消后返回 ctx.Err()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := m.Acquire(ctx)
	if err == nil {
		t.Error("Acquire err = nil, want ctx.Err()")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Acquire err = %v, want context.DeadlineExceeded", err)
	}

	// 清理
	m.Release()
}

// TestManager_Doctor_SimulateConcurrentCheck 模拟 Doctor 并发检测场景（C17 复用测试）
// 模拟 Doctor 在 P2-G7 实施时，用 Acquire/TryAcquire/Release 限制并发检测数
func TestManager_Doctor_SimulateConcurrentCheck(t *testing.T) {
	const maxConcurrency = 3
	m := NewManager(WithMaxConcurrency(maxConcurrency))

	const totalChecks = 10
	var wg sync.WaitGroup
	var activeMax int32
	var activeNow int32

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for i := 0; i < totalChecks; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// 模拟 Doctor 用 Acquire 限制并发
			if err := m.Acquire(ctx); err != nil {
				t.Errorf("Doctor check %d Acquire err = %v", idx, err)
				return
			}
			defer m.Release()

			// 更新并发计数
			now := atomic.AddInt32(&activeNow, 1)
			for {
				max := atomic.LoadInt32(&activeMax)
				if now <= max || atomic.CompareAndSwapInt32(&activeMax, max, now) {
					break
				}
			}
			defer atomic.AddInt32(&activeNow, -1)

			// 模拟检测耗时
			time.Sleep(50 * time.Millisecond)
		}(i)
	}

	wg.Wait()

	// 验证并发数从未超过 maxConcurrency
	if activeMax > int32(maxConcurrency) {
		t.Errorf("max concurrent checks = %d, want <= %d", activeMax, maxConcurrency)
	}
}

// TestManager_Race_ConcurrentStart 并发 Start + Release（需 -race）
// 验证信号量在并发场景下无 race
func TestManager_Race_ConcurrentStart(t *testing.T) {
	m := NewManager(WithMaxConcurrency(4))

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			proc, err := m.Start("echo hello", "race test", "")
			if err != nil {
				t.Errorf("goroutine %d Start err = %v", idx, err)
				return
			}
			// 等待进程结束（令牌由 waitProcess 归还）
			if _, done := proc.WaitForDone(15 * time.Second); !done {
				t.Errorf("goroutine %d process did not finish", idx)
			}
		}(i)
	}

	wg.Wait()
}
