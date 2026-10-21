//go:build race

package tui

// raceEnabled 标记本测试二进制是否带 race 插桩。
// 用途：耗时对比类探针在 -race 下失真（插桩开销远大于被测路径），
// 这类探针必须显式跳过，而不是靠放宽阈值蒙过去。
const raceEnabled = true
