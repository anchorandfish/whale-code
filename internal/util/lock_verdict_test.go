package util

import "testing"

// F11 L2/L4 判别器（声明侧）。
//
// 判别器纪律：每条都必须能通过注入反例打红。本族的反例注入 =
// 把 `MaySteal()` 改成"Unknown 也可夺"（例如 `return v != LockVerdictHeld`）
// ⇒ 下面 `TestLockVerdict_UnknownNeverMaySteal` 与
// `TestLockVerdict_ZeroValueIsUnknownAndNotStealable` **必须红**。

// TestLockVerdict_ZeroValueIsUnknownAndNotStealable 钉住「零值 = 未知 = 不夺」。
//
// 为什么这条是结构性的（不是风格）：调用方漏赋值 / 忘记检查时，Go 给出零值；
// 若零值恰好是 `Stale`，则「忘记判定」会静默变成「夺锁」⇒ 与 F11 L1
// （误夺锁 ⇒ 数据丢失）同向。收敛成零值 Unknown 后，该错误在**类型层面**落在
// 安全侧，与 `platform.BoundaryUnknown` 同一手法。
func TestLockVerdict_ZeroValueIsUnknownAndNotStealable(t *testing.T) {
	var v LockVerdict // 故意不赋值：零值必须落在"未知"

	if v != LockVerdictUnknown {
		t.Errorf("LockVerdict 零值 = %v(%d)，必须是 LockVerdictUnknown —— "+
			"否则「忘记赋值」会静默变成「可夺锁」", v, int(v))
	}
	if v.MaySteal() {
		t.Errorf("零值（未知）MaySteal() = true，必须为 false：" +
			"未知态夺锁 = 误夺锁 ⇒ 与 F11 L1 数据丢失同向")
	}
}

// TestLockVerdict_UnknownNeverMaySteal 是上一条的显式表：三值中只有 Stale 放行。
//
// 判据**不得**写成"锁能被夺"（那不是语义正确），必须直接断言判定值 → 放行结论。
func TestLockVerdict_UnknownNeverMaySteal(t *testing.T) {
	cases := []struct {
		name string
		v    LockVerdict
		want bool
	}{
		{"Unknown（损坏 / 不可判定）", LockVerdictUnknown, false},
		{"Held（仍被有效持有）", LockVerdictHeld, false},
		{"Stale（陈旧可夺）", LockVerdictStale, true},
	}
	for _, tc := range cases {
		if got := tc.v.MaySteal(); got != tc.want {
			t.Errorf("%s: MaySteal() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestLockDirPerm_IsOwnerOnly 钉住 F11 L2 的取值：锁目录权限**只属于持有者**。
//
// 判别性：改为 0755（或任何含 group/other 位的值）本用例即红。
// 该常量是"锁目录权限"的唯一声明 —— 修复前同族 4 处各写各的（0700 与 0755 并存），
// 而 `MkdirAll` 对已存在目录是空操作 ⇒ 磁盘上的真实权限取决于**谁先创建**，
// 单看任一调用点无法推断。
func TestLockDirPerm_IsOwnerOnly(t *testing.T) {
	if LockDirPerm != 0o700 {
		t.Errorf("LockDirPerm = %o, want 0700", LockDirPerm)
	}
	if g := LockDirPerm.Perm() & 0o077; g != 0 {
		t.Errorf("LockDirPerm 含 group/other 位（%03o），锁目录不应被同机其他用户访问", g)
	}
}
