// Package session — interface_test.go 子接口合规性编译时检查（候选 A）。
package session

// 候选 A：验证 Store 实现满足 4 个子接口 + 组合接口（编译时检查）。
// 若 Store 缺少任一方法，编译时报错。对标 Go io 包接口合规检查模式。
var (
	_ SessionReader   = (*Store)(nil)
	_ SessionWriter   = (*Store)(nil)
	_ SessionLocker   = (*Store)(nil)
	_ SessionSearcher = (*Store)(nil)
	_ SessionStore    = (*Store)(nil)
)
