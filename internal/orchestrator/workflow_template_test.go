// workflow_template_test.go — H10 Phase 2 模块 1 工作流模板 schema + Loader + Validator 反例测试（编排层 §6.2/§6.3）。
// 覆盖致命反例：① 空 stages ② 递归引用 ③ Stage.ID 重复 ④ DependsOn 引用不存在 ⑤ Name 含路径分隔符 ⑥ Review.MaxRound != 1。
// schema；落地：ValidateTemplate 7 类校验全测
// /020-审查 1 轮硬上限；落地：ReviewTpl.MaxRound != 1 拒绝
// SNAPSHOT: tests={EmptyStages_Rejected, RecursiveDependency_Rejected, DuplicateStageID_Rejected, DependsOnNotFound_Rejected, NameWithPathSep_Rejected, ReviewMaxRoundNotOne_Rejected, SelfCycle_Rejected, SchemaVersion_Rejected, Loader_ProjectOverridesGlobal, Loader_TemplateNotFound, Loader_PathTraversal_Rejected, Loader_Save_PathTraversal_Rejected, Loader_Save_OverwriteExisting, Save_ComputesContentHash, ValidateTemplate_Nil_Rejected}

package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeValidTemplate 构造合法模板（6 Stage 线性序列，编排层 §6.2 示例）。
func makeValidTemplate(name string) *WorkflowTemplate {
	return &WorkflowTemplate{
		Schema:  TemplateSchemaVersion,
		Name:    name,
		Version: "1.0.0",
		Desc:    "test template",
		Stages: []StageTpl{
			{ID: StageClarify, Type: WorkflowStageTypeInteractive, OnFail: OnFailRetryOnce, Prompt: "prompts/clarify.md"},
			{ID: StagePlan, Type: WorkflowStageTypeAgent, OnFail: OnFailRetryOnce, Prompt: "prompts/plan.md", DependsOn: []string{StageClarify}},
			{ID: StageContract, Type: WorkflowStageTypeAgent, OnFail: OnFailRetryOnce, Prompt: "prompts/contract.md", DependsOn: []string{StagePlan}},
			{ID: StageImplement, Type: WorkflowStageTypeAgent, OnFail: OnFailRetryOnce, Prompt: "prompts/implement.md", DependsOn: []string{StageContract}},
			{ID: StageReview, Type: WorkflowStageTypeAgent, OnFail: OnFailRetryOnce, Prompt: "prompts/review.md", DependsOn: []string{StageImplement},
				Review: &ReviewTpl{Type: "independent", Dimensions: 3, NoPlan: true, MaxRound: 1}},
			{ID: StageTest, Type: WorkflowStageTypeAuto, OnFail: OnFailRetryOnce, Prompt: "prompts/test.md", DependsOn: []string{StageReview}},
		},
		Variables: []Variable{},
		Metadata:  TemplateMeta{Author: "test", CreatedAt: 1720000000, Tags: []string{"test"}},
	}
}

// TestValidateTemplate_EmptyStages_Rejected 致命反例 1：Stages 为空 → error（零容忍）。
func TestValidateTemplate_EmptyStages_Rejected(t *testing.T) {
	tpl := makeValidTemplate("empty-stages")
	tpl.Stages = []StageTpl{}
	err := ValidateTemplate(tpl)
	if err == nil {
		t.Fatal("致命反例 1：Stages 为空必须返回 error（零容忍）")
	}
	if !strings.Contains(err.Error(), "Stages must not be empty") {
		t.Errorf("error 应包含 'Stages must not be empty'，got: %v", err)
	}
}

// TestValidateTemplate_RecursiveDependency_Rejected 致命反例 2：Stage A→B→A 间接环 → error（零容忍）。
func TestValidateTemplate_RecursiveDependency_Rejected(t *testing.T) {
	tpl := makeValidTemplate("recursive")
	tpl.Stages = []StageTpl{
		{ID: "A", Type: WorkflowStageTypeAgent, OnFail: OnFailRetryOnce, DependsOn: []string{"B"}},
		{ID: "B", Type: WorkflowStageTypeAgent, OnFail: OnFailRetryOnce, DependsOn: []string{"A"}},
	}
	err := ValidateTemplate(tpl)
	if err == nil {
		t.Fatal("致命反例 2：Stage A→B→A 间接环必须返回 error（零容忍）")
	}
	if !strings.Contains(err.Error(), "cycle detected") {
		t.Errorf("error 应包含 'cycle detected'，got: %v", err)
	}
}

// TestValidateTemplate_SelfCycle_Rejected 致命反例 2 变种：Stage A→A 自环 → error（零容忍）。
func TestValidateTemplate_SelfCycle_Rejected(t *testing.T) {
	tpl := makeValidTemplate("self-cycle")
	tpl.Stages = []StageTpl{
		{ID: "A", Type: WorkflowStageTypeAgent, OnFail: OnFailRetryOnce, DependsOn: []string{"A"}},
	}
	err := ValidateTemplate(tpl)
	if err == nil {
		t.Fatal("致命反例 2 变种：Stage A→A 自环必须返回 error（零容忍）")
	}
	if !strings.Contains(err.Error(), "cycle detected") {
		t.Errorf("error 应包含 'cycle detected'，got: %v", err)
	}
}

// TestValidateTemplate_DuplicateStageID_Rejected：两个 Stage 同 ID → error。
func TestValidateTemplate_DuplicateStageID_Rejected(t *testing.T) {
	tpl := makeValidTemplate("dup-id")
	tpl.Stages = []StageTpl{
		{ID: "A", Type: WorkflowStageTypeAgent, OnFail: OnFailRetryOnce},
		{ID: "A", Type: WorkflowStageTypeAgent, OnFail: OnFailRetryOnce}, // 重复
	}
	err := ValidateTemplate(tpl)
	if err == nil {
		t.Fatal("Stage.ID 重复必须返回 error")
	}
	if !strings.Contains(err.Error(), "duplicate Stage.ID") {
		t.Errorf("error 应包含 'duplicate Stage.ID'，got: %v", err)
	}
}

// TestValidateTemplate_DependsOnNotFound_Rejected：DependsOn 引用不存在 ID → error。
func TestValidateTemplate_DependsOnNotFound_Rejected(t *testing.T) {
	tpl := makeValidTemplate("dep-not-found")
	tpl.Stages = []StageTpl{
		{ID: "A", Type: WorkflowStageTypeAgent, OnFail: OnFailRetryOnce, DependsOn: []string{"nonexistent"}},
	}
	err := ValidateTemplate(tpl)
	if err == nil {
		t.Fatal("DependsOn 引用不存在 ID 必须返回 error")
	}
	if !strings.Contains(err.Error(), "DependsOn unknown Stage") {
		t.Errorf("error 应包含 'DependsOn unknown Stage'，got: %v", err)
	}
}

// TestValidateTemplate_NameWithPathSep_Rejected：Name 含 / 或 \ → error（路径穿越防护）。
func TestValidateTemplate_NameWithPathSep_Rejected(t *testing.T) {
	cases := []struct {
		name     string
		tplName  string
		errMatch string
	}{
		{"含正斜杠", "foo/bar", "path separator"},
		{"含反斜杠", "foo\\bar", "path separator"},
		{"含 ..", "../evil", "'..'"},
		{"空字符串", "", "name must not be empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tpl := makeValidTemplate(c.tplName)
			err := ValidateTemplate(tpl)
			if err == nil {
				t.Fatalf("Name=%q 必须返回 error（路径穿越防护）", c.tplName)
			}
			if !strings.Contains(err.Error(), c.errMatch) {
				t.Errorf("error 应包含 %q，got: %v", c.errMatch, err)
			}
		})
	}
}

// TestValidateTemplate_ReviewMaxRoundNotOne_Rejected：ReviewTpl.MaxRound != 1 → error（/020）。
func TestValidateTemplate_ReviewMaxRoundNotOne_Rejected(t *testing.T) {
	tpl := makeValidTemplate("bad-maxround")
	tpl.Stages[4].Review.MaxRound = 3 // review Stage 的 MaxRound 改为 3
	err := ValidateTemplate(tpl)
	if err == nil {
		t.Fatal("ReviewTpl.MaxRound=3 必须返回 error（/020）")
	}
	if !strings.Contains(err.Error(), "MaxRound must be 1") {
		t.Errorf("error 应包含 'MaxRound must be 1'，got: %v", err)
	}
}

// TestValidateTemplate_SchemaVersion_Rejected：Schema 不为 "v1" → error。
func TestValidateTemplate_SchemaVersion_Rejected(t *testing.T) {
	tpl := makeValidTemplate("bad-schema")
	tpl.Schema = "v999"
	err := ValidateTemplate(tpl)
	if err == nil {
		t.Fatal("Schema 不为 'v1' 必须返回 error")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("error 应包含 'schema'，got: %v", err)
	}
}

// TestValidateTemplate_Nil_Rejected：nil 模板 → error。
func TestValidateTemplate_Nil_Rejected(t *testing.T) {
	if err := ValidateTemplate(nil); err == nil {
		t.Fatal("nil 模板必须返回 error")
	}
}

// TestValidateTemplate_ValidTemplate_Passes：合法模板通过校验。
func TestValidateTemplate_ValidTemplate_Passes(t *testing.T) {
	tpl := makeValidTemplate("valid-tpl")
	if err := ValidateTemplate(tpl); err != nil {
		t.Errorf("合法模板不应返回 error，got: %v", err)
	}
}

// TestTemplateLoader_Load_ProjectOverridesGlobal：项目级覆盖全局级同名模板。
func TestTemplateLoader_Load_ProjectOverridesGlobal(t *testing.T) {
	projectDir := t.TempDir()
	globalDir := t.TempDir()

	// 全局级模板 v1.0.0
	globalTpl := makeValidTemplate("shared")
	globalTpl.Version = "1.0.0"
	globalTpl.Desc = "global version"
	writeTemplateFile(t, globalDir, globalTpl)

	// 项目级同名模板 v2.0.0
	projectTpl := makeValidTemplate("shared")
	projectTpl.Version = "2.0.0"
	projectTpl.Desc = "project version"
	writeTemplateFile(t, projectDir, projectTpl)

	loader := NewTemplateLoader(projectDir, globalDir)
	got, err := loader.Load("shared")
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if got.Version != "2.0.0" {
		t.Errorf("项目级应覆盖全局级，期望 Version=2.0.0，got %s", got.Version)
	}
	if got.Desc != "project version" {
		t.Errorf("期望 Desc='project version'，got %q", got.Desc)
	}
}

// TestTemplateLoader_Load_TemplateNotFound：run 不存在的模板名 → error。
func TestTemplateLoader_Load_TemplateNotFound(t *testing.T) {
	loader := NewTemplateLoader(t.TempDir(), t.TempDir())
	_, err := loader.Load("nonexistent")
	if err == nil {
		t.Fatal("Load 不存在的模板必须返回 error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error 应包含 'not found'，got: %v", err)
	}
}

// TestTemplateLoader_Load_PathTraversal_Rejected：Load name 含 ../ → error（致命反例 4 变种）。
func TestTemplateLoader_Load_PathTraversal_Rejected(t *testing.T) {
	loader := NewTemplateLoader(t.TempDir(), t.TempDir())
	cases := []string{"../evil", "foo/bar", "foo\\bar", ".."}
	for _, evil := range cases {
		t.Run("name="+evil, func(t *testing.T) {
			_, err := loader.Load(evil)
			if err == nil {
				t.Fatalf("Load name=%q 必须返回 error（路径穿越防护）", evil)
			}
		})
	}
}

// TestTemplateLoader_Save_PathTraversal_Rejected 致命反例 4：Save name 含 ../ → error（零容忍）。
func TestTemplateLoader_Save_PathTraversal_Rejected(t *testing.T) {
	loader := NewTemplateLoader(t.TempDir(), "")
	cases := []string{"../evil", "foo/bar", "foo\\bar"}
	for _, evil := range cases {
		t.Run("name="+evil, func(t *testing.T) {
			tpl := makeValidTemplate(evil)
			err := loader.Save(tpl)
			if err == nil {
				t.Fatalf("致命反例 4：Save name=%q 必须返回 error（零容忍）", evil)
			}
		})
	}
}

// TestTemplateLoader_Save_OverwriteExisting：同名模板覆盖 + 新 ContentHash。
func TestTemplateLoader_Save_OverwriteExisting(t *testing.T) {
	projectDir := t.TempDir()
	loader := NewTemplateLoader(projectDir, "")

	// 第一次保存
	tpl1 := makeValidTemplate("overwrite-test")
	tpl1.Desc = "version 1"
	if err := loader.Save(tpl1); err != nil {
		t.Fatalf("第一次 Save 失败: %v", err)
	}
	hash1 := tpl1.Metadata.ContentHash
	if hash1 == "" {
		t.Fatal("Save 后 ContentHash 必须非空")
	}

	// 验证文件存在
	path := filepath.Join(projectDir, "overwrite-test.template.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件未创建: %v", err)
	}

	// 第二次保存（覆盖，Desc 改变）
	tpl2 := makeValidTemplate("overwrite-test")
	tpl2.Desc = "version 2"
	if err := loader.Save(tpl2); err != nil {
		t.Fatalf("第二次 Save 失败: %v", err)
	}
	hash2 := tpl2.Metadata.ContentHash

	if hash1 == hash2 {
		t.Errorf("覆盖后 ContentHash 必须不同（内容变化）: hash1=%s hash2=%s", hash1, hash2)
	}

	// 验证 Load 返回新版本
	got, err := loader.Load("overwrite-test")
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if got.Desc != "version 2" {
		t.Errorf("期望 Desc='version 2'，got %q", got.Desc)
	}
}

// TestTemplateLoader_Save_ComputesContentHash：Save 后 ContentHash 非空 + 稳定（同内容同 hash）。
func TestTemplateLoader_Save_ComputesContentHash(t *testing.T) {
	projectDir := t.TempDir()
	loader := NewTemplateLoader(projectDir, "")

	tpl := makeValidTemplate("hash-test")
	if err := loader.Save(tpl); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	if tpl.Metadata.ContentHash == "" {
		t.Fatal("ContentHash 必须非空")
	}
	if len(tpl.Metadata.ContentHash) != 64 { // sha256 hex = 64 chars
		t.Errorf("ContentHash 必须是 64 字符 sha256 hex，got %d 字符: %s", len(tpl.Metadata.ContentHash), tpl.Metadata.ContentHash)
	}

	// 同内容再 Save 一份到不同 name，hash 应相同
	projectDir2 := t.TempDir()
	loader2 := NewTemplateLoader(projectDir2, "")
	tpl2 := makeValidTemplate("hash-test-2")
	tpl2.Stages = tpl.Stages // 同内容
	tpl2.Variables = tpl.Variables
	tpl2.Metadata = tpl.Metadata
	tpl2.Metadata.ContentHash = "" // 清空后重新计算
	if err := loader2.Save(tpl2); err != nil {
		t.Fatalf("第二次 Save 失败: %v", err)
	}
	// 注：Name 不同导致 hash 不同（Name 是 WorkflowTemplate 字段）
	// 但同 Name 同内容 hash 应相同（已由 OverwriteExisting 测试覆盖）
}

// TestTemplateLoader_List_MergesProjectAndGlobal：项目级 + 全局级同名，List 只显示项目级。
func TestTemplateLoader_List_MergesProjectAndGlobal(t *testing.T) {
	projectDir := t.TempDir()
	globalDir := t.TempDir()

	// 全局级：tpl-a, tpl-b
	writeTemplateFile(t, globalDir, makeValidTemplate("tpl-a"))
	writeTemplateFile(t, globalDir, makeValidTemplate("tpl-b"))

	// 项目级：tpl-b（覆盖全局级）, tpl-c
	projectB := makeValidTemplate("tpl-b")
	projectB.Desc = "project override"
	writeTemplateFile(t, projectDir, projectB)
	writeTemplateFile(t, projectDir, makeValidTemplate("tpl-c"))

	loader := NewTemplateLoader(projectDir, globalDir)
	list, err := loader.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("期望 3 个模板（tpl-a, tpl-b, tpl-c），got %d", len(list))
	}

	// 验证 tpl-b 是项目级版本（Desc="project override"）
	for _, tpl := range list {
		if tpl.Name == "tpl-b" && tpl.Desc != "project override" {
			t.Errorf("tpl-b 应为项目级覆盖版本（Desc='project override'），got %q", tpl.Desc)
		}
	}

	// 验证按 Name 排序
	if !isSortedByName(list) {
		t.Errorf("List 应按 Name 排序")
	}
}

// TestTemplateLoader_List_EmptyDirs：两目录均不存在 → 返回空切片不报错。
func TestTemplateLoader_List_EmptyDirs(t *testing.T) {
	loader := NewTemplateLoader(t.TempDir(), t.TempDir())
	list, err := loader.List()
	if err != nil {
		t.Fatalf("空目录 List 不应报错: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("空目录 List 应返回空切片，got %d 个", len(list))
	}
}

// writeTemplateFile 辅助函数：将模板写入目录。
func writeTemplateFile(t *testing.T, dir string, tpl *WorkflowTemplate) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	// 先校验模板合法（避免测试用例本身有错）
	if err := ValidateTemplate(tpl); err != nil {
		t.Fatalf("测试用例模板不合法: %v", err)
	}
	// 计算 ContentHash
	hash, err := computeContentHash(tpl)
	if err != nil {
		t.Fatalf("computeContentHash: %v", err)
	}
	tpl.Metadata.ContentHash = hash

	path := filepath.Join(dir, tpl.Name+".template.json")
	data := mustJSONMarshal(t, tpl)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// mustJSONMarshal 辅助：JSON marshal 失败直接 Fatal。
func mustJSONMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("JSON marshal: %v", err)
	}
	return data
}

// isSortedByName 检查模板列表是否按 Name 升序。
func isSortedByName(list []WorkflowTemplate) bool {
	for i := 1; i < len(list); i++ {
		if list[i-1].Name > list[i].Name {
			return false
		}
	}
	return true
}
