// baseline.go — 桶 6 开发基线 checklist 库加载器（地基层 §2.3 第三层）。
// 双层维护：whale-code 内置一份（随版本发布，保证下限）+ 项目级 .whale/baselines/ 可覆盖。
// 子任务 prompt 按任务类型自动注入对应 checklist。
// 库；落地：≤10 条硬规则/文件 + 双层维护 + 按任务类型注入
// SNAPSHOT: path=.whale/baselines/*.checklist.md; max_rules=10; format=# <name> checklist + numbered list; inject_by=task_type

package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BaselineLibrary checklist 库，管理 .whale/baselines/*.checklist.md 文件。
// 双层维护：项目级 .whale/baselines/ 覆盖内置默认（地基层 §2.3）。
type BaselineLibrary struct {
	dir      string            // .whale/baselines/ 目录绝对路径
	builtins map[string]string // 内置 checklist（任务类型 → 内容，保证下限）
}

// NewBaselineLibrary 创建 checklist 库，dir 为 .whale/baselines/ 目录。
func NewBaselineLibrary(dir string) *BaselineLibrary {
	return &BaselineLibrary{
		dir:      dir,
		builtins: defaultBuiltins(),
	}
}

// Load 按任务类型加载 checklist（双层：项目级覆盖内置，地基层 §2.3）。
// taskType 映射到 checklist 文件名（如 "bug-fix" → "error-handling.checklist.md"）。
func (b *BaselineLibrary) Load(taskType string) (string, error) {
	filename := taskTypeToChecklistFile(taskType)
	if filename == "" {
		return "", fmt.Errorf("baseline: no checklist mapping for task_type %q", taskType)
	}

	// 第一层：项目级 .whale/baselines/<filename>
	projectPath := filepath.Join(b.dir, filename)
	if buf, err := os.ReadFile(projectPath); err == nil {
		return string(buf), nil
	}

	// 第二层：内置默认
	content, ok := b.builtins[filename]
	if !ok {
		return "", fmt.Errorf("baseline: checklist %q not found (neither project-level nor builtin)", filename)
	}
	return content, nil
}

// LoadAll 加载所有内置 checklist（用于 /doctor 诊断展示）。
func (b *BaselineLibrary) LoadAll() map[string]string {
	result := make(map[string]string, len(b.builtins))
	for k, v := range b.builtins {
		result[k] = v
	}
	return result
}

// taskTypeToChecklistFile 任务类型 → checklist 文件名映射（地基层 §2.3）。
func taskTypeToChecklistFile(taskType string) string {
	switch taskType {
	case TaskTypeBugFix:
		return "error-handling.checklist.md"
	case TaskTypeImplement:
		return "concurrency.checklist.md"
	case TaskTypeDocGen:
		return "api-design.checklist.md"
	case TaskTypeReview:
		return "test-quality.checklist.md"
	case TaskTypePlan:
		return "cli-design.checklist.md"
	}
	return ""
}

// defaultBuiltins 返回内置 checklist 内容（保证下限，项目级 .whale/baselines/ 可覆盖）。
// 每个 checklist ≤10 条硬规则（地基层 §2.3）。
func defaultBuiltins() map[string]string {
	return map[string]string{
		"error-handling.checklist.md": `# error-handling checklist
1. 所有 error 必须 wrap（fmt.Errorf 用 %w，禁止 %v/%s 包裹 error）
2. 不允许 _ = foo() 丢弃 error（errcheck 强制）
3. 不允许返回 nil 但 err 非空（nilerr）
4. lib 层（internal/）禁止 panic，必须返回 error
5. error 变量命名用小写驼峰（如 errConn），不用 e/er
6. 自定义 error 类型实现 Unwrap() 方法
7. error 消息小写开头、无标点结尾（Go 惯例）
8. 对外 API error 用 sentinel 或 typed error，不用字符串比较
9. defer 关闭资源时检查 Close() 返回的 error
10. 测试必须覆盖至少 1 个 error path
`,
		"concurrency.checklist.md": `# concurrency checklist
1. 共享状态必须用 sync.Mutex 或 sync.RWMutex 保护
2. 不要在持锁状态下执行 I/O 或长时间操作
3. goroutine 启动前明确退出条件（context.Done 或 channel close）
4. 不要在 goroutine 中直接写外部变量（用 channel 或 mutex）
5. sync.WaitGroup.Add 必须在 goroutine 启动前调用
6. 避免闭包捕获循环变量（Go 1.22+ 已修复，但仍需注意）
7. map 并发读写必须用 sync.Map 或加锁
8. select 语句必须有 default 或超时分支（防死锁）
9. 不要用 time.Sleep 等待 goroutine（用 channel/Wg）
10. 并发测试必须用 -race 跑一遍
`,
		"test-quality.checklist.md": `# test-quality checklist
1. 每个公开函数至少 1 个反例测试（输入非法值/边界值）
2. 测试函数命名用 Test<类型>_<场景>（如 TestStore_ConcurrentAppend）
3. 并发测试必须用 t.Parallel() + sync.WaitGroup
4. 测试不依赖外部网络（用 mock 或 httptest）
5. 测试不依赖执行顺序（每个测试独立）
6. 使用 table-driven 测试覆盖多场景
7. 测试必须覆盖至少 1 个 error path
8. 不要在测试中用 time.Sleep 等待（用 channel/事件）
9. 测试清理用 t.Cleanup() 而非 defer（保证执行顺序）
10. 测试失败信息含上下文（got/expected + 输入值）
`,
		"api-design.checklist.md": `# api-design checklist
1. 公开符号必须有 godoc 注释（首字母大写函数/类型/常量）
2. 函数参数 ≤5 个，超过用 struct 封装
3. 返回值 ≤3 个，超过用 struct 封装
4. 不要用 bool 参数控制函数行为（用枚举或拆分函数）
5. 接口定义在消费方（非实现方），小接口（1-3 方法）
6. error 作为最后一个返回值
7. context.Context 作为第一个参数
8. 不要导出未实现的接口（Stub 字段标记）
9. 接口扩展走仲裁流程（02 §4），不私自声明接口归属
10. 破坏性变更必须 ADR 记录
`,
		"cli-design.checklist.md": `# cli-design checklist
1. 命令名小写中划线分隔（如 session-split，非 sessionSplit）
2. 子命令 ≤5 个，超过用分组（如 plugin install/uninstall/list）
3. 每个命令有 --help 输出（用法 + 示例）
4. 退出码：0=成功 / 1=通用错误 / 2=用法错误
5. 不要用 os.Exit 在 lib 层（由 cmd 层统一处理）
6. 输出格式支持 --json（便于脚本消费）
7. 长选项用 --flag，短选项用 -f（避免混淆）
8. 危险操作必须 --confirm 或 -y 跳过确认
9. 命令补全通过 commandCompletions 注册（02 §2.5.1）
10. 跨平台兼容（Windows 路径用 filepath.Join，不用 / 拼接）
`,
	}
}

// CountRules 统计 checklist 内容中的规则数（解析 numbered list）。
// 用于 baseline_test.go 验证 ≤10 条规则约束。
func CountRules(content string) int {
	lines := strings.Split(content, "\n")
	count := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 匹配 "数字. " 开头的行（如 "1. " / "10. "）
		if len(trimmed) < 3 {
			continue
		}
		if trimmed[0] >= '0' && trimmed[0] <= '9' {
			// 查找第一个空格或点
			dotIdx := strings.Index(trimmed, ". ")
			if dotIdx > 0 {
				count++
			}
		}
	}
	return count
}

// ValidateFormat 校验 checklist 格式（# <name> checklist 开头 + numbered list）。
func ValidateFormat(content string) error {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return fmt.Errorf("baseline: empty checklist")
	}
	// 第一行必须是 # <name> checklist
	first := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(first, "# ") || !strings.HasSuffix(first, "checklist") {
		return fmt.Errorf("baseline: first line must be '# <name> checklist', got %q", first)
	}
	// 必须至少 1 条 numbered list
	if CountRules(content) == 0 {
		return fmt.Errorf("baseline: no numbered rules found")
	}
	return nil
}
