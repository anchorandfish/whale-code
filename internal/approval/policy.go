// Package approval 提供工具调用审批的交互层（C19 三层权限模型第三层）。
//
// 该包实现 on-failure/on-request/never 等审批策略，配合 permission.Policy（规则层）
// 与 Tool.CheckPermissions（声明层）共同构成完整的权限模型。
package approval

import "fmt"

// ApprovalPolicy 审批策略（对标同类 CLI AskForApproval）。
type ApprovalPolicy int

const (
	// PolicyUntrusted 高频审批：总是询问用户（除非已缓存允许）。
	PolicyUntrusted ApprovalPolicy = 0
	// PolicyOnFailure 失败时审批：先放行执行，失败后由调用方再次调用 CheckWithResult 决定是否询问。
	PolicyOnFailure ApprovalPolicy = 1
	// PolicyOnRequest 按需审批：仅危险工具询问用户，其余直接放行。
	PolicyOnRequest ApprovalPolicy = 2
	// PolicyNever 从不审批：仅依赖沙箱限制，不再询问用户。
	PolicyNever ApprovalPolicy = 3
)

// String 返回审批策略的可读字符串表示。
func (p ApprovalPolicy) String() string {
	switch p {
	case PolicyUntrusted:
		return "untrusted"
	case PolicyOnFailure:
		return "on-failure"
	case PolicyOnRequest:
		return "on-request"
	case PolicyNever:
		return "never"
	}
	return "unknown"
}

// ParsePolicy 从字符串解析审批策略，支持 "untrusted"/"on-failure"/"on-request"/"never"。
// 解析失败时返回错误，零值为 PolicyUntrusted。
func ParsePolicy(s string) (ApprovalPolicy, error) {
	switch s {
	case "untrusted":
		return PolicyUntrusted, nil
	case "on-failure":
		return PolicyOnFailure, nil
	case "on-request":
		return PolicyOnRequest, nil
	case "never":
		return PolicyNever, nil
	}
	return PolicyUntrusted, fmt.Errorf("unknown approval policy: %s", s)
}
