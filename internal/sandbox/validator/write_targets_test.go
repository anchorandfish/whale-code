package validator

import "testing"

// TestBashWriteTargets 是 T2 的输入面判别器：写目标必须从**语法**里提取，
// 且无法静态归约的成分必须被显式报成 unresolved（而不是悄悄丢掉目标 ⇒ 被误判成"没有写入"）。
//
// 判别性：反例注入 ＝ 让 `$`/`$(` 的目标被当作普通路径返回且 unresolved=false
// ⇒ 下面"变量/命令替换 ⇒ unresolved"两行红。
func TestBashWriteTargets(t *testing.T) {
	cases := []struct {
		name       string
		command    string
		wantAny    []string // 必须出现的目标（子集断言）
		wantEmpty  bool
		unresolved bool
	}{
		{
			name:    "绝对路径写目标",
			command: "echo x > /etc/whale_i22_probe",
			wantAny: []string{"/etc/whale_i22_probe"},
		},
		{
			name:    "相对路径写目标",
			command: "echo x > ./i23_inside.txt",
			wantAny: []string{"./i23_inside.txt"},
		},
		{
			name:    "追加写目标",
			command: "echo x >> ./log.txt",
			wantAny: []string{"./log.txt"},
		},
		{
			name:       "变量目标不可归约",
			command:    `echo x > "$OUTDIR/i24.txt"`,
			unresolved: true,
		},
		{
			name:       "命令替换目标不可归约",
			command:    `echo x > "$(mktemp)/i24.txt"`,
			unresolved: true,
		},
		{
			name:       "裸变量目标不可归约",
			command:    `echo x > $OUTDIR/i24.txt`,
			unresolved: true,
		},
		{
			name:      "无害 sink 不算写目标",
			command:   "echo x 2>/dev/null",
			wantEmpty: true,
		},
		{
			name:      "无写入的命令没有目标",
			command:   "ls -la /tmp",
			wantEmpty: true,
		},
		{
			name:      "空命令",
			command:   "   ",
			wantEmpty: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targets, unresolved := BashWriteTargets(tc.command)
			if unresolved != tc.unresolved {
				t.Fatalf("unresolved = %v, want %v (targets=%v)", unresolved, tc.unresolved, targets)
			}
			if tc.wantEmpty && len(targets) != 0 {
				t.Fatalf("期望无写目标，实际 = %v", targets)
			}
			for _, w := range tc.wantAny {
				found := false
				for _, got := range targets {
					if got == w {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("写目标缺 %q；实际 = %v", w, targets)
				}
			}
		})
	}
}

// TestBashWriteTargets_UnresolvedIsConservative 钉住"报不准就报 unresolved"
// 的方向：解析失败必须 unresolved（宁可多问一次，不可静默放行）。
func TestBashWriteTargets_UnresolvedIsConservative(t *testing.T) {
	if _, unresolved := BashWriteTargets("echo x > '"); !unresolved {
		t.Fatal("引号不闭合的命令必须报 unresolved（保守侧）")
	}
}
