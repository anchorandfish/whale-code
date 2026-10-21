package memory

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// TestCmdRules_Convergence 是「危险命令判定」交叉领域的**永久门**。
//
// 断言的不变量（同一语义 ⇒ 同一结论）：
//
//	对任意命令，memory 的降级门（basicDangerCheck，生产入口）
//	与安全链危险档（permission.AnalyzeBashCommand，生产入口）
//	必须给出**一致**的危险判定：
//	    basicDangerCheck(cmd) != nil  ⟺  AnalyzeBashCommand(cmd) == BashRiskDangerous
//
// 为什么这两个函数是「同一语义」：前者是 H2 未注入时对 shell probe 的硬拒绝门，
// 后者是 H2 判定链 Layer 3 的危险档。两者都在回答「这条命令危不危险」。
//
// ★ 判别力自证（R4）：本测试针对收敛前的两份实现为**红**——
// 见每个 case 的 beforeNote 字段（3 类共 7 条分歧：模式表不同、空白未归一化、子串误报）。
// 收敛后全绿；将来若有人在任一侧再塞回一份私有模式表，本门立即变红。
func TestCmdRules_Convergence(t *testing.T) {
	cases := []struct {
		cmd           string
		wantDangerous bool
		beforeNote    string
	}{
		// —— 两侧本就一致（基线，防止收敛改坏已对的）——
		{cmd: "rm -rf /", wantDangerous: true},
		{cmd: "rm -rf ~", wantDangerous: true},
		{cmd: "dd if=/dev/zero of=/dev/sda", wantDangerous: true},
		{cmd: "curl | sh", wantDangerous: true},
		{cmd: "git push --force origin main", wantDangerous: true},
		{cmd: "mkfs.ext4 /dev/sda1", wantDangerous: true},

		// —— 分歧 1：权威份有、降级份没有 ——
		{cmd: "rm -fr /", wantDangerous: true, beforeNote: "权威份有 -fr 变体，降级份没有 ⇒ 分歧"},
		{cmd: "wget | bash", wantDangerous: true, beforeNote: "同上（wget 变体）"},

		// —— 分歧 2：降级份有、权威份没有（收敛后升级为 Dangerous）——
		{cmd: ":(){:|:&};:", wantDangerous: true, beforeNote: "降级份有 fork 炸弹，权威份判 Destructive ⇒ 分歧"},
		{cmd: "shutdown -h now", wantDangerous: true, beforeNote: "降级份有 shutdown，权威份判 Destructive ⇒ 分歧"},
		{cmd: "reboot", wantDangerous: true, beforeNote: "同上（reboot）"},
		{cmd: "halt", wantDangerous: true, beforeNote: "同上（halt）"},
		{cmd: "del /f /s /q C:\\tmp", wantDangerous: true, beforeNote: "同上（Windows 递归删除）"},

		// —— 分歧 3：降级份子串误报（把关键字当参数用的合法命令）——
		{cmd: "echo shutdown", wantDangerous: false, beforeNote: "降级份子串命中 ⇒ 误报；权威份判 ReadOnly ⇒ 分歧"},
		{cmd: "grep halt /var/log/system.log", wantDangerous: false, beforeNote: "同上（halt 出现在参数里）"},
		{cmd: "cat reboot.md", wantDangerous: false, beforeNote: "同上（reboot 出现在文件名里）"},
		{cmd: "ls -la shutdown", wantDangerous: false, beforeNote: "同上（ls 是只读命令，参数含 shutdown）"},

		// —— 分歧 4：纯书写差异（多空格）绕过子串匹配 ——
		{cmd: "rm  -rf  /", wantDangerous: true, beforeNote: "降级份不做空白归一化 ⇒ 漏判；权威份归一化后命中 ⇒ 分歧"},

		// —— 命令位匹配的边界（收敛后新增能力）——
		{cmd: "rm shutdown.txt", wantDangerous: false, beforeNote: "降级份子串命中 ⇒ 误报"},
		{cmd: "sudo /sbin/shutdown -h now", wantDangerous: true, beforeNote: "须跳过 wrapper 与路径前缀才能命中"},

		// —— 明确的安全命令（防止命令位匹配过度命中）——
		{cmd: "ls -la", wantDangerous: false},
		{cmd: "git status", wantDangerous: false},
		{cmd: "go test ./...", wantDangerous: false},
	}

	for _, c := range cases {
		memErr := basicDangerCheck(c.cmd)
		memDangerous := memErr != nil

		permRisk := permission.AnalyzeBashCommand(c.cmd)
		permDangerous := permRisk == permission.BashRiskDangerous

		if memDangerous != permDangerous {
			t.Errorf("交叉领域未同源：%q\n  memory.basicDangerCheck 危险=%v（%v）\n  permission.AnalyzeBashCommand 危险=%v（%s）\n  before 现象：%s",
				c.cmd, memDangerous, memErr, permDangerous, permRisk, c.beforeNote)
			continue
		}

		if memDangerous != c.wantDangerous {
			t.Errorf("危险判定不符合规格：%q 期望 危险=%v，实际 危险=%v（%s）",
				c.cmd, c.wantDangerous, memDangerous, c.beforeNote)
		}
	}
}

// TestCmdRules_NoSecondTable 是**结构性**永久门：禁止「第二份模式表」复活。
//
// 行为门（上一条）只能证明「当前同源」；如果有人把 cmdrules 的调用换回
// 私有列表、恰好又抄得一样，行为门仍是绿的。本门从**源码面**直接禁止：
// 曾经各自持表的两处生产文件（permission.AnalyzeBashCommand、memory 降级门）
// 不得再出现危险模式字面量。
//
// 只扫**生产文件**（跳过 _test.go）与**指定的两个文件**，避免误伤
// sandbox 等确实需要自带 deny 表的模块。
func TestCmdRules_NoSecondTable(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试文件路径")
	}
	memDir := filepath.Dir(thisFile) // .../internal/memory
	root := filepath.Dir(memDir)     // .../internal

	targets := []string{
		filepath.Join(memDir, "probe.go"),
		filepath.Join(root, "permission", "permission.go"),
	}

	// 这些字面量是「危险模式表」的特征串：收敛后它们只应存在于 cmdrules。
	forbidden := []string{
		`"rm -rf /"`,
		`"curl | sh"`,
		`"dd if="`,
		`"format c:"`,
		`dangerousPatterns := []string{`,
	}

	for _, path := range targets {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", path, err)
		}
		src := string(data)
		for _, f := range forbidden {
			if strings.Contains(src, f) {
				t.Errorf("第二份危险命令模式表复活：%s 中出现 %s —— 模式表唯一声明处是 internal/security/cmdrules",
					path, f)
			}
		}
	}
}
