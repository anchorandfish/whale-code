// merge_layer.go — H11/N27 Phase B B2 跨 scope 合并层。
// ；落地：Markdown body 是自由散文（Content string yaml:"-"），无字段语义，"同名字段覆盖"无法落地，改为整文件覆盖
// REF-外部-同类CLI@2026-03-31快照-文件级 precedence；借鉴逻辑：同类 CLI 项目级 memory 覆盖用户级 memory（整文件覆盖，非字段级）；落地：H11/N27 project scope 覆盖 user scope（整文件覆盖）
// ；落地：H11/N27 新增 retrieval 子层，scope 分类是 H11/N27 责任段
// SNAPSHOT: MergeLayer={userStore+projectStore; ListMerged=按Name去重+project覆盖user(整文件覆盖, 非字段级); 归属=H11/N27新增retrieval子层(02§4仲裁, )}

package memory

import "fmt"

// MergeLayer 跨 scope 合并层，retrieval 预处理。
// 对齐 v0.4 §5.2 + 候选 4 红队档案：整文件覆盖（非字段级）。
// 归属：H11/N27 新增 retrieval 子层（02 §4 仲裁，scope 分类是 H11/N27 责任段）。
//
// 设计依据：
// - Markdown body 是自由散文（MemoryEntry.Content string yaml:"-"），无字段语义
// - "同名字段覆盖"无法落地（body 不是结构化字段），改为整文件覆盖
// - 借鉴同类 CLI 项目级 memory 覆盖用户级 memory（@2026-03-31 快照文件级 precedence）
type MergeLayer struct {
	userStore    *MemoryStore // _user/ scope
	projectStore *MemoryStore // project scope
}

// NewMergeLayer 构造 MergeLayer，传入两个已初始化的 MemoryStore。
// userStore 应为 NewMemoryStoreForScope(baseDir, "user", false) 创建。
// projectStore 应为 NewMemoryStoreForProject(baseDir, workDir) 创建。
func NewMergeLayer(userStore, projectStore *MemoryStore) *MergeLayer {
	return &MergeLayer{
		userStore:    userStore,
		projectStore: projectStore,
	}
}

// ListMerged 合并两 scope 清单，整文件覆盖（project > user）。
// 按 sanitized Name 去重，project scope 同名时整体不注入 user 版本，仅注入 project 版本。
//
// 合并策略：
// 1. 先填 user scope 全部 entry
// 2. 再填 project scope entry（同名时整体覆盖 user 版本，包括 Description/Content/Verification 等所有字段）
// 3. 返回合并后的切片（不保证排序，调用方按需排序）
//
// 错误处理：单 scope List 失败不阻塞另一 scope 返回（降级为空切片），仅当两 scope 都失败时返回 error。
// 部分失败时返回的 entries 仍可使用，error 含失败 scope 信息。
func (m *MergeLayer) ListMerged() ([]*MemoryEntry, error) {
	merged := make(map[string]*MemoryEntry)

	// 1. 先填 user scope
	var userFailed, projectFailed error
	if m.userStore != nil {
		userEntries, err := m.userStore.List()
		if err != nil {
			userFailed = err
		} else {
			for _, e := range userEntries {
				merged[e.Name] = e
			}
		}
	}

	// 2. 再填 project scope（同名时整文件覆盖 user 版本）
	if m.projectStore != nil {
		projectEntries, err := m.projectStore.List()
		if err != nil {
			projectFailed = err
		} else {
			for _, e := range projectEntries {
				// 整文件覆盖：直接替换 user 版本（包括 Description/Content/Verification 等所有字段）
				merged[e.Name] = e
			}
		}
	}

	// 3. 全部失败时返回 error
	if userFailed != nil && projectFailed != nil {
		return nil, &MergeError{
			UserErr:    userFailed,
			ProjectErr: projectFailed,
		}
	}

	// 4. 收集 map → 切片
	result := make([]*MemoryEntry, 0, len(merged))
	for _, e := range merged {
		result = append(result, e)
	}
	return result, nil
}

// MergeError 跨 scope 合并错误，记录两 scope 各自的失败原因。
// 调用方可通过 UserErr/ProjectErr 判断哪个 scope 失败。
type MergeError struct {
	UserErr    error
	ProjectErr error
}

// Error 实现 error 接口。
func (e *MergeError) Error() string {
	return fmt.Sprintf("merge layer error: user=%v, project=%v", e.UserErr, e.ProjectErr)
}

// Unwrap 支持 errors.Is/As 链式查找。
func (e *MergeError) Unwrap() error {
	if e.UserErr != nil {
		return e.UserErr
	}
	return e.ProjectErr
}
