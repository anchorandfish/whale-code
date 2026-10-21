package onboarding

import (
	"bufio"
	"context"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/doctor"
	"golang.org/x/term"
)

// sentinelFileName 哨兵文件名（位于 ~/.whale/ 下），标记 Onboarding 已完成。
// 对标同类 CLI hasCompletedOnboarding 配置字段，whale-code 用独立文件做双保险。
const sentinelFileName = ".onboarded"

// sentinelPath 返回指定 homeDir 下哨兵文件的完整路径。
// 路径示例：~/.whale/.onboarded
func sentinelPath(homeDir string) string {
	return filepath.Join(whalehome.Dir(), sentinelFileName)
}

// Onboarding 是引导器主结构，聚合配置、doctor 与工作目录。
// 对标同类 CLI Onboarding 组件的状态机容器。
type Onboarding struct {
	// cfg 只读引用 whale-code 配置（可为 nil，步骤 4 内部构造新 Config 写入）。
	cfg *config.Config
	// doctor N17 诊断器（可为 nil，步骤 1 安全跳过）。
	doctor *doctor.Doctor
	// workDir 当前工作目录。
	workDir string
	// homeDir 用户主目录（哨兵文件与 settings.json 写入位置）。
	homeDir string
	// state 跨步骤共享状态，Run 时初始化。
	state *OnboardingState
	// stdinReader 标准输入读取器，默认 os.Stdin；测试时可注入 mock。
	stdinReader io.Reader
	// stdinIsTerminal 标记 stdin 是否为终端（决定用 term.ReadPassword 还是 bufio 读取）。
	stdinIsTerminal bool
	// stdinBuf 共享的 bufio.Reader，避免多个步骤各自创建 bufio.Reader 导致读缓冲区数据丢失。
	stdinBuf *bufio.Reader
}

// NewOnboarding 创建引导器实例（生产环境入口，使用 os.Stdin）。
// cfg 可为 nil（首次运行无配置时）；doc 可为 nil（N17 未初始化时步骤 1 跳过）；
// workDir 当前工作目录；homeDir 用户主目录。
func NewOnboarding(cfg *config.Config, doc *doctor.Doctor, workDir, homeDir string) *Onboarding {
	return NewOnboardingWithStdin(cfg, doc, workDir, homeDir, os.Stdin)
}

// NewOnboardingWithStdin 创建引导器实例（支持注入自定义 stdin，用于测试）。
// stdinReader 用于步骤 2 选项读取和步骤 3 API key 输入；
// 当 stdin 是终端时使用 term.ReadPassword（不回显），否则用 bufio 读取（测试场景）。
func NewOnboardingWithStdin(cfg *config.Config, doc *doctor.Doctor, workDir, homeDir string, stdinReader io.Reader) *Onboarding {
	isTerminal := false
	if f, ok := stdinReader.(*os.File); ok {
		isTerminal = term.IsTerminal(int(f.Fd()))
	}
	return &Onboarding{
		cfg:             cfg,
		doctor:          doc,
		workDir:         workDir,
		homeDir:         homeDir,
		stdinReader:     stdinReader,
		stdinIsTerminal: isTerminal,
		stdinBuf:        bufio.NewReader(stdinReader),
		state: &OnboardingState{
			WorkDir: workDir,
			HomeDir: homeDir,
		},
	}
}

// readPassword 读取密码，终端模式下用 term.ReadPassword 不回显，非终端模式用 bufio 读取（测试场景）。
// 封装 stdin 读取逻辑，使步骤 3 可测试。非终端模式复用共享的 stdinBuf，避免多 bufio.Reader 导致数据丢失。
func (o *Onboarding) readPassword() (string, error) {
	if o.stdinIsTerminal {
		if f, ok := o.stdinReader.(*os.File); ok {
			bytes, err := term.ReadPassword(int(f.Fd()))
			if err != nil {
				return "", err
			}
			return string(bytes), nil
		}
	}
	// 非终端模式（测试注入 mock stdin），复用共享的 bufio.Reader 读取一行
	line, err := o.stdinBuf.ReadString('\n')
	if err != nil {
		return "", err
	}
	return line, nil
}

// stdinBufio 返回共享的 bufio.Reader，用于步骤 2 等普通文本输入。
// 复用同一个 bufio.Reader 实例，避免各步骤各自创建导致读缓冲区数据丢失。
func (o *Onboarding) stdinBufio() *bufio.Reader {
	return o.stdinBuf
}

// Run 执行 4 步骤 Onboarding 流程，返回是否完成。
// 对标同类 CLI Onboarding.tsx 的 steps 数组 + goToNextStep 状态机推进。
// 任一步骤失败即终止并返回 error；全部成功则返回 (true, nil)。
//
// D1-1：Run 入口先渲染差异化首屏（突出本地优先/H12/Windows 三大卖点），
// 首屏仅在 IsCompleted=false 时由调用方触发 Run（main.go:719），
// 因此 Run 内无需重复判定。首屏渲染 fail-open：panic 时跳过首屏直接进步骤 1。
func (o *Onboarding) Run(ctx context.Context) (bool, error) {
	// D1-1：渲染差异化首屏（fail-open，panic 时静默跳过，不阻塞 4 步骤主流程）
	func() {
		defer func() {
			_ = recover() // 任何 panic 静默吞掉，跳过首屏继续 4 步骤
		}()
		if welcome := o.renderDifferentiatedWelcome(); welcome != "" {
			fmt.Println(welcome)
		}
	}()

	// 渲染欢迎头部
	fmt.Print(renderHeader())

	steps := []Step{
		{ID: StepEnvironment, Name: "Environment Check", NameZH: "环境检测", Run: o.RunStepEnvironment},
		{ID: StepProvider, Name: "Provider Selection", NameZH: "Provider 选择", Run: o.RunStepProvider},
		{ID: StepAPIKey, Name: "API Key Input", NameZH: "API Key 输入", Run: o.RunStepAPIKey},
		{ID: StepConfig, Name: "Config Generation", NameZH: "配置生成", Run: o.RunStepConfig},
	}

	for i, step := range steps {
		fmt.Println(renderStepProgress(i+1, step.Name, step.NameZH))
		if err := step.Run(o.state); err != nil {
			return false, fmt.Errorf("step %s failed: %w", step.Name, err)
		}
	}
	return true, nil
}

// IsCompleted 检测是否已完成 Onboarding（哨兵文件 + settings.json 双保险 + API key 兜底）。
// 任一标记存在即视为完成，避免单点失败导致重复引导。
// 3 层检测：1) 哨兵文件 2) settings.json Onboarded 字段 3) settings.json 已有 API key（兼容性兜底）。
// 对标同类 CLI !config.hasCompletedOnboarding 的触发条件判断。
func IsCompleted(homeDir string) bool {
	// 1. 哨兵文件存在性检测
	if _, err := os.Stat(sentinelPath(homeDir)); err == nil {
		return true
	}
	// 2. settings.json 中 Onboarded 字段检测
	loader := config.NewLoader(homeDir, "")
	cfg, err := loader.Load()
	if err != nil {
		return false
	}
	if cfg.Onboarded {
		return true
	}
	// 3. 兼容性兜底：settings.json 存在且已有 API key 的用户视为已配置
	// （改进方案 §1.9 第 3 层，适配实际 Config 结构：cfg.APIKey 顶层字段）
	if cfg.APIKey != "" {
		return true
	}
	return false
}

// MarkComplete 标记 Onboarding 完成（写哨兵文件 + 更新 settings.json Onboarded 字段）。
// 哨兵文件权限 0600（仅用户可读写），内容为完成时间戳。
// 对标同类 CLI completeOnboarding 写入 hasCompletedOnboarding: true。
func MarkComplete(homeDir string, cfg *config.Config) error {
	// 1. 写哨兵文件 ~/.whale/.onboarded
	sentinel := sentinelPath(homeDir)
	if err := os.MkdirAll(filepath.Dir(sentinel), 0700); err != nil {
		return fmt.Errorf("create sentinel dir: %w", err)
	}
	if err := os.WriteFile(sentinel, []byte(time.Now().Format(time.RFC3339)), 0600); err != nil {
		return fmt.Errorf("write sentinel: %w", err)
	}

	// 2. 更新 settings.json 中 Onboarded 字段（若 cfg 非空）
	if cfg != nil {
		cfg.Onboarded = true
		loader := config.NewLoader(homeDir, "")
		if err := loader.Save(cfg); err != nil {
			return fmt.Errorf("save settings.json: %w", err)
		}
	}
	return nil
}

// Reset 重置 Onboarding 状态（删除哨兵文件，允许重跑）。
// 注意：不修改 settings.json 中的 Onboarded 字段（仅删哨兵文件即可触发重跑，
// IsCompleted 双保险中哨兵文件优先检测）。
// 对标同类 CLI 重置 onboarding 的能力（同类 CLI 无此功能，whale-code 增强）。
func Reset(homeDir string) error {
	sentinel := sentinelPath(homeDir)
	if err := os.Remove(sentinel); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove sentinel: %w", err)
	}
	return nil
}

// MaybeMarkExistingUser 检测已有用户（settings.json 存在 + 环境变量 API key 已配置）并自动标记完成。
// 缓解改进方案 §3.3 兼容性风险：避免已配置好 API key 的现有用户被强制重跑 Onboarding。
// 返回 true 表示已自动标记完成（调用方应跳过 Onboarding）。
func MaybeMarkExistingUser(homeDir string) bool {
	// settings.json 已存在
	settingsPath := filepath.Join(whalehome.Dir(), "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		return false // 无 settings.json，是真正的新用户
	}
	// 环境变量 API key 已配置
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("OPENAI_API_KEY") != "" {
		// 读现有 settings.json，标记 Onboarded=true，写哨兵文件
		loader := config.NewLoader(homeDir, "")
		cfg, err := loader.Load()
		if err != nil {
			return false
		}
		if !cfg.Onboarded {
			cfg.Onboarded = true
			_ = loader.Save(cfg)
		}
		_ = MarkComplete(homeDir, cfg)
		return true
	}
	return false
}

// State 返回当前 OnboardingState（测试用，允许断言步骤副作用）。
func (o *Onboarding) State() *OnboardingState {
	return o.state
}
