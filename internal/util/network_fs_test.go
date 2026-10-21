package util

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestIsNetworkFS_Injects 验证 IsNetworkFS 的可注入判定（§5.4 降级测试的唯一办法）。
func TestIsNetworkFS_Injects(t *testing.T) {
	orig := networkFSProbe
	defer func() { networkFSProbe = orig }()

	networkFSProbe = func(path string) (bool, string, error) {
		return true, "nfs", nil
	}
	network, fstype, err := IsNetworkFS("/x")
	if err != nil || !network || fstype != "nfs" {
		t.Fatalf("IsNetworkFS = (%v, %q, %v), want (true, nfs, nil)", network, fstype, err)
	}

	networkFSProbe = func(path string) (bool, string, error) {
		return false, "apfs", nil
	}
	network, fstype, err = IsNetworkFS("/x")
	if err != nil || network || fstype != "apfs" {
		t.Fatalf("IsNetworkFS = (%v, %q, %v), want (false, apfs, nil)", network, fstype, err)
	}

	// 检测失败（err != nil）必须由调用方按 fail-safe 处理。
	networkFSProbe = func(path string) (bool, string, error) {
		return false, "", errors.New("statfs failed")
	}
	_, _, err = IsNetworkFS("/x")
	if err == nil {
		t.Fatalf("检测失败应返回 error，供调用方 fail-safe 回退")
	}
}

// TestEffectiveSessionLockTimeout 归一化：≤0 ⇒ 默认 2s，杜绝把 0 传给库（0=无限阻塞）。
func TestEffectiveSessionLockTimeout(t *testing.T) {
	if got := EffectiveSessionLockTimeout(); got != defaultSessionLockTimeout {
		t.Fatalf("default = %v, want %v", got, defaultSessionLockTimeout)
	}
	orig := SessionLockTimeout
	defer func() { SessionLockTimeout = orig }()

	SessionLockTimeout = 0
	if got := EffectiveSessionLockTimeout(); got != defaultSessionLockTimeout {
		t.Fatalf("SessionLockTimeout=0 归一化后 = %v, want 默认 %v", got, defaultSessionLockTimeout)
	}
	SessionLockTimeout = 5 * time.Second
	if got := EffectiveSessionLockTimeout(); got != 5*time.Second {
		t.Fatalf("SessionLockTimeout=5s 归一化后 = %v, want 5s", got)
	}
}

// TestTryAcquireFileLockTimeout_LocksAndConflicts 超时获取锁：成功持有、冲突超时。
func TestTryAcquireFileLockTimeout_LocksAndConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.lock")

	// 成功获取。
	first, err := TryAcquireFileLockTimeout(path, 1*time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Release()

	// 第二个应超时（被 first 持有），且是 LockTimeoutError。
	start := time.Now()
	_, err = TryAcquireFileLockTimeout(path, 200*time.Millisecond)
	if err == nil {
		t.Fatalf("second acquire should time out while first holds the lock")
	}
	var tErr *LockTimeoutError
	if !errors.As(err, &tErr) {
		t.Fatalf("second acquire err = %v, want *LockTimeoutError", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took %v, want ~200ms bounded", elapsed)
	}

	// 释放后能再次获取。
	first.Release()
	second, err := TryAcquireFileLockTimeout(path, 1*time.Second)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	second.Release()
}

// TestTryAcquireFileLockTimeout_RefusesZero 拒绝 0 超时（0 在库侧是无限阻塞，必须拒绝而非静默阻塞）。
func TestTryAcquireFileLockTimeout_RefusesZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.lock")
	if _, err := TryAcquireFileLockTimeout(path, 0); err == nil {
		t.Fatalf("timeout=0 必须被拒绝（0=无限阻塞），不得静默阻塞")
	}
}
