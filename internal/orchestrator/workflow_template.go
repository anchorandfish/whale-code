// workflow_template.go — H10 Phase 2 模块 1 工作流模板 schema + Loader + Validator（编排层 §6.1/§6.2/§6.3）。
// REF-外部-LangGraph@官方文档+2026-07-15访问-graph 定义 + 节点 schema；借鉴逻辑：Stage schema 化 + depends_on；落地：StageTpl struct + JSON schema
// REF-外部-同类CLI@2026-03-31快照-Prompt 模板化；借鉴逻辑：prompt 字段引用文件；落地：StageTpl.Prompt 字段
// schema 完整定义；落地：WorkflowTemplate/StageTpl/ReviewTpl/Variable/TemplateMeta struct
// /020-审查 1 轮硬上限；落地：ReviewTpl.MaxRound=1（ValidateTemplate 校验）
// ；落地：ReviewTpl.NoPlan=true（review Stage 推荐）
// ；落地：Version 语义版本 + Metadata.ContentHash sha256 去重
// SNAPSHOT: pkg=internal/orchestrator; type=WorkflowTemplate; fields={Schema,Name,Version,Desc,Stages,Variables,Metadata}; StageTpl={ID,Type,Prompt,OutputSchema,DependsOn,UserConfirm,Review,SubtasksFrom,Isolation,OnFail,AdvanceGuard}; ReviewTpl.MaxRound=1(); NoPlan=true(,review); store=.whale/workflows/<name>.template.json; priority=project>global; path_traversal=Name含/\\拒绝

package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WorkflowTemplate 工作流模板（编排层 §6.2 完整定义）。
// schema 化的 Stage 序列，支持 /workflow run 加载与 /workflow save-as 保存。
type WorkflowTemplate struct {
	Schema    string       `json:"schema"`    // 模板 schema 版本，如 "v1"
	Name      string       `json:"name"`      // 模板名（唯一，不含路径分隔符）
	Version   string       `json:"version"`   // 语义版本，如 "1.0.0"
	Desc      string       `json:"desc"`      // 模板描述
	Stages    []StageTpl   `json:"stages"`    // Stage 定义序列（不可为空）
	Variables []Variable   `json:"variables"` // 模板变量（用户填）
	Metadata  TemplateMeta `json:"metadata"`  // 元数据
}

// StageTpl Stage 模板（编排层 §6.2）。
type StageTpl struct {
	ID           string     `json:"id"`                      // clarify/plan/contract/implement/review/test
	Type         string     `json:"type"`                    // interactive/agent/auto
	Prompt       string     `json:"prompt"`                  // prompt 文件路径或内联
	OutputSchema string     `json:"output_schema"`           // 输出 struct 名
	DependsOn    []string   `json:"depends_on"`              // 依赖的 Stage ID
	UserConfirm  bool       `json:"user_confirm"`            // 是否需要用户确认
	Review       *ReviewTpl `json:"review,omitempty"`        // 审查配置（review Stage）
	SubtasksFrom string     `json:"subtasks_from,omitempty"` // 子任务来源
	Isolation    string     `json:"isolation,omitempty"`     // 隔离方式（Phase 3）
	OnFail       string     `json:"on_fail"`                 // retry_once/interrupt/abort
	AdvanceGuard string     `json:"advance_guard"`           // 流转条件校验函数名
}

// ReviewTpl 审查配置（编排层 §6.2）。
// MaxRound 必须 = 1（/020 硬上限，ValidateTemplate 校验）。
// NoPlan=true 时审查不喂方案。
type ReviewTpl struct {
	Type         string `json:"type"`          // independent
	Dimensions   int    `json:"dimensions"`    // 审查维度数（3/7）
	NoPlan       bool   `json:"no_plan"`       // 是否不喂方案
	MaxRound     int    `json:"max_round"`     // 必须 = 1（/020）
	DiffProvider bool   `json:"diff_provider"` // 是否用不同 provider（Phase 3）
}

// Variable 模板变量（编排层 §6.2）。
type Variable struct {
	Name     string `json:"name"`
	Desc     string `json:"desc"`
	Required bool   `json:"required"`
	Default  string `json:"default,omitempty"`
}

// TemplateMeta 模板元数据（编排层 §6.2 + §6.3 版本管理）。
type TemplateMeta struct {
	Author      string   `json:"author"`
	CreatedAt   int64    `json:"created_at"`
	Tags        []string `json:"tags"`         // 如 ["stable","experiment-a"]
	Benchmarks  []string `json:"benchmarks"`   // 对标来源（官方源）
	ContentHash string   `json:"content_hash"` // 模板内容 sha256（去重用）
}

// TemplateSchemaVersion 当前模板 schema 版本（编排层 §6.2）。
const TemplateSchemaVersion = "v1"

// TemplateLoader 模板加载器，扫描项目级 + 全局级目录并按优先级合并（编排层 §6.1）。
// 优先级：projectDir（.whale/workflows/）覆盖 globalDir（~/.whale/workflows/）同名模板。
type TemplateLoader struct {
	projectDir string // .whale/workflows/（项目级，高优先）
	globalDir  string // ~/.whale/workflows/（全局级，低优先）
}

// NewTemplateLoader 构造模板加载器（编排层 §6.1）。
// projectDir 为项目级目录（.whale/workflows/），globalDir 为全局级目录（~/.whale/workflows/）。
func NewTemplateLoader(projectDir, globalDir string) *TemplateLoader {
	return &TemplateLoader{
		projectDir: projectDir,
		globalDir:  globalDir,
	}
}

// Load 按模板名加载，项目级覆盖全局级（编排层 §6.1 优先级）。
// name 必须先过路径穿越校验（含 `..` 或 `/` 或 `\` 拒绝）。
func (l *TemplateLoader) Load(name string) (*WorkflowTemplate, error) {
	if err := validateTemplateName(name); err != nil {
		return nil, err
	}

	// 先查项目级
	if l.projectDir != "" {
		tpl, err := loadTemplateFile(filepath.Join(l.projectDir, name+".template.json"))
		if err == nil {
			return tpl, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("template_loader: load project %s: %w", name, err)
		}
	}

	// 再查全局级
	if l.globalDir != "" {
		tpl, err := loadTemplateFile(filepath.Join(l.globalDir, name+".template.json"))
		if err == nil {
			return tpl, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("template_loader: load global %s: %w", name, err)
		}
	}

	return nil, fmt.Errorf("template_loader: template %q not found in project or global", name)
}

// List 列出所有可用模板，按 Name 排序，项目级覆盖全局级同名模板（编排层 §6.1）。
func (l *TemplateLoader) List() ([]WorkflowTemplate, error) {
	merged := make(map[string]WorkflowTemplate)

	// 先扫全局级（低优先，被覆盖）
	if l.globalDir != "" {
		globals, err := scanTemplateDir(l.globalDir)
		if err != nil {
			return nil, fmt.Errorf("template_loader: scan global: %w", err)
		}
		for _, t := range globals {
			merged[t.Name] = t
		}
	}

	// 再扫项目级（高优先，覆盖同名）
	if l.projectDir != "" {
		projects, err := scanTemplateDir(l.projectDir)
		if err != nil {
			return nil, fmt.Errorf("template_loader: scan project: %w", err)
		}
		for _, t := range projects {
			merged[t.Name] = t
		}
	}

	result := make([]WorkflowTemplate, 0, len(merged))
	for _, t := range merged {
		result = append(result, t)
	}
	// 按 Name 排序（稳定输出）
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// Save 保存模板到项目级目录，计算 ContentHash 并写入（编排层 §6.1/§6.3）。
// 保存前必须 ValidateTemplate 通过，且 Name 必须过路径穿越校验。
func (l *TemplateLoader) Save(tpl *WorkflowTemplate) error {
	if tpl == nil {
		return fmt.Errorf("template_loader: template must not be nil")
	}
	if err := ValidateTemplate(tpl); err != nil {
		return fmt.Errorf("template_loader: ValidateTemplate: %w", err)
	}
	if err := validateTemplateName(tpl.Name); err != nil {
		return err
	}

	if l.projectDir == "" {
		return fmt.Errorf("template_loader: projectDir must not be empty for Save")
	}

	// 计算内容 sha256（去除 Metadata.ContentHash 字段后 hash，保证 hash 稳定）
	hash, err := computeContentHash(tpl)
	if err != nil {
		return fmt.Errorf("template_loader: compute hash: %w", err)
	}
	tpl.Metadata.ContentHash = hash

	// 确保目录存在
	if err := os.MkdirAll(l.projectDir, 0o755); err != nil {
		return fmt.Errorf("template_loader: mkdir %s: %w", l.projectDir, err)
	}

	path := filepath.Join(l.projectDir, tpl.Name+".template.json")
	data, err := json.MarshalIndent(tpl, "", "  ")
	if err != nil {
		return fmt.Errorf("template_loader: marshal: %w", err)
	}

	// 原子写入：先写 .tmp 再 rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("template_loader: write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("template_loader: rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// ValidateTemplate 校验模板 schema 合法性（编排层 §6.2，致命反例防护）。
// 致命反例：① Stages 为空 ② Stage.ID 重复 ③ DependsOn 引用不存在 ID ④ DependsOn 形成环（DFS 环检测）⑤ Schema 不为 "v1" ⑥ Name 为空或含路径分隔符 ⑦ ReviewTpl.MaxRound != 1（/020）。
func ValidateTemplate(tpl *WorkflowTemplate) error {
	if tpl == nil {
		return fmt.Errorf("template: must not be nil")
	}

	// ⑤ Schema 版本校验
	if tpl.Schema != TemplateSchemaVersion {
		return fmt.Errorf("template: schema %q must be %q", tpl.Schema, TemplateSchemaVersion)
	}

	// ⑥ Name 校验（非空 + 路径穿越防护）
	if err := validateTemplateName(tpl.Name); err != nil {
		return fmt.Errorf("template: invalid name: %w", err)
	}

	// ① Stages 非空（致命反例 1，零容忍）
	if len(tpl.Stages) == 0 {
		return fmt.Errorf("template: Stages must not be empty (致命反例)")
	}

	// ② Stage.ID 唯一性
	idSet := make(map[string]bool)
	for _, s := range tpl.Stages {
		if s.ID == "" {
			return fmt.Errorf("template: Stage.ID must not be empty")
		}
		if idSet[s.ID] {
			return fmt.Errorf("template: duplicate Stage.ID %q", s.ID)
		}
		idSet[s.ID] = true
	}

	// ③ DependsOn 引用必须存在
	for _, s := range tpl.Stages {
		for _, dep := range s.DependsOn {
			if !idSet[dep] {
				return fmt.Errorf("template: Stage %q DependsOn unknown Stage %q", s.ID, dep)
			}
		}
	}

	// ④ DependsOn 环检测（DFS，致命反例 2，零容忍）
	if err := detectStageCycle(tpl.Stages); err != nil {
		return err
	}

	// ⑦ ReviewTpl.MaxRound 必须 = 1（/020 硬约束）
	for _, s := range tpl.Stages {
		if s.Review != nil {
			if s.Review.MaxRound != 1 {
				return fmt.Errorf("template: Stage %q Review.MaxRound must be 1, got %d", s.ID, s.Review.MaxRound)
			}
		}
	}

	return nil
}

// validateTemplateName 校验模板名不含路径穿越字符（防注入，编排层 §6.2 安全要求）。
// 规则：非空 + 不含 .. + 不含 / + 不含 \ 。
func validateTemplateName(name string) error {
	if name == "" {
		return fmt.Errorf("template: name must not be empty")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("template: name contains '..': %s", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("template: name contains path separator: %s", name)
	}
	return nil
}

// detectStageCycle DFS 环检测（编排层 §6.2 致命反例 2）。
// 覆盖自环（A→A）+ 间接环（A→B→A）。
func detectStageCycle(stages []StageTpl) error {
	// 构造邻接表
	adj := make(map[string][]string)
	for _, s := range stages {
		adj[s.ID] = s.DependsOn
	}

	// 三色 DFS：0=未访问，1=访问中（在栈中），2=已完成
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int)
	for _, s := range stages {
		color[s.ID] = white
	}

	var dfs func(node string, path []string) error
	dfs = func(node string, path []string) error {
		color[node] = gray
		path = append(path, node)
		for _, dep := range adj[node] {
			switch color[dep] {
			case gray:
				// 发现环
				return fmt.Errorf("template: cycle detected: %s → %s", strings.Join(path, " → "), dep)
			case white:
				if err := dfs(dep, path); err != nil {
					return err
				}
			}
		}
		color[node] = black
		return nil
	}

	for _, s := range stages {
		if color[s.ID] == white {
			if err := dfs(s.ID, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadTemplateFile 从单文件加载模板（编排层 §6.1）。
func loadTemplateFile(path string) (*WorkflowTemplate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tpl WorkflowTemplate
	if err := json.Unmarshal(data, &tpl); err != nil {
		return nil, fmt.Errorf("template_loader: unmarshal %s: %w", path, err)
	}
	return &tpl, nil
}

// scanTemplateDir 扫描目录下所有 *.template.json 文件并返回模板列表（编排层 §6.1）。
func scanTemplateDir(dir string) ([]WorkflowTemplate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var result []WorkflowTemplate
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".template.json") {
			continue
		}
		tpl, err := loadTemplateFile(filepath.Join(dir, name))
		if err != nil {
			// 跳过无法解析的文件（容错）
			continue
		}
		result = append(result, *tpl)
	}
	return result, nil
}

// computeContentHash 计算模板内容 sha256（去除 Metadata.ContentHash 字段后 hash，编排层 §6.3）。
// 去除 ContentHash 是为了：① hash 稳定（不因 hash 字段变化导致 hash 漂移）② 内容变化才能反映到 hash。
func computeContentHash(tpl *WorkflowTemplate) (string, error) {
	// 深拷贝并清空 ContentHash
	clone := *tpl
	clone.Metadata.ContentHash = ""

	data, err := json.Marshal(clone)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
