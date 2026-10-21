#!/usr/bin/env bash
# fmt-check.sh —— 按「构建图」裁剪范围后再跑 gofmt。
#
# 为什么不能直接 `gofmt -l .`（2026-09-13 实测）：
#   gofmt **不认** `_` 前缀目录，也**不认** `//go:build ignore` —— 只有 go 工具认。
#   直接扫 `.` 会把 docs/ vendor/ third_party/ 下的归档与第三方代码一起算进结果
#   （实测 `gofmt -l ./internal/ ./cmd/ .` 命中 575 个文件，其中 9 个在 docs/ 下），
#   让 CI 的格式门因为「非本仓源码」而变红 —— 门就失去可操作性。
#
# 做法：用 `go list` 取出**构建图内**的每个 .go（GoFiles / TestGoFiles / XTestGoFiles），
#      只对这批文件跑 gofmt。范围与 go 工具语义同源：不多也不少。
#
# ★ 2026-09-20 三处收口（每一处都会让债务在门里「隐形」，都是本仓实测出来的）：
#   ① 范围必须跨平台。`go list` 只列**宿主**平台的文件 ⇒ CI 在 ubuntu 上永远看不见
#      `*_windows.go` / `*_darwin.go`：单 GOOS 口径实测 1241 个文件，漏掉 **6 个未格式化的
#      本仓源码**（如 `internal/sandbox/os_sandbox_windows.go`）—— 它们从未被这道门看过。
#   ② 范围必须含带 tag 的文件。`//go:build integration` 的文件在默认口径下不出现（实测漏 2 个）。
#      ⇒ 取 GOOS{linux,darwin,windows} × tags{默认, integration} 的并集（实测 1277 个文件）。
#      仓库内除 integration 外没有其它自定义 tag（2026-09-20 全仓清点）。
#   ③ `--fix` 必须收敛到不动点。本仓的文档注释让 gofmt **单次不收敛**（实测 internal/ai/stream.go
#      要连跑 2 轮才到不动点）⇒ 只跑一遍 `gofmt -w` 会留下「看着修过了」的残余 —— 历史欠账
#      清不干净的原因就在这里。修（--fix）与检（默认）收在**同一个脚本**里，避免命令双实现。
#
# 用法：
#   cd whale-code && ./scripts/fmt-check.sh          # 检查，有未格式化则 exit 1
#   cd whale-code && ./scripts/fmt-check.sh --list   # 只列，不判退出码
#   cd whale-code && ./scripts/fmt-check.sh --fix    # 跑到不动点，再复检（有残余仍 exit 1）
#
# 注意：仓库里**既有**的未格式化/CRLF 文件属历史欠账。
#      2026-09-20 已清零：宿主口径 250 个 ＋ 跨平台 6 个 ＋ tag 口径 2 个，共 258 个文件已格式化；
#      唯一未动的是 `internal/session/uuid.go`（当时正被另一会话写入，按一文件一写者跳过）。
#      本脚本只负责让门「范围正确、修法可收敛」，不负责替历史欠账背书。
set -euo pipefail

cd "$(dirname "$0")/.."

# 范围 = 三平台 × 默认与 integration tag 的并集（见头注 ① ②）
collect_files() {
  local goos tags
  for goos in linux darwin windows; do
    for tags in "" "integration"; do
      if [ -n "$tags" ]; then
        GOOS="$goos" go list -tags "$tags" -f '{{$d := .Dir}}{{range .GoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .TestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}' ./...
      else
        GOOS="$goos" go list -f '{{$d := .Dir}}{{range .GoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .TestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}' ./...
      fi
    done
  done | sort -u
}

files=$(collect_files)

count=$(printf '%s\n' "$files" | grep -c . || true)
echo "[fmt-check] 构建图内 Go 文件（linux/darwin/windows × 默认+integration 并集）: $count 个"

if [ "${1:-}" = "--fix" ]; then
  rounds=0
  for _ in 1 2 3 4 5 6; do
    pending=$(gofmt -l $files || true)
    if [ -z "$pending" ]; then
      break
    fi
    printf '%s\n' "$pending" | while IFS= read -r f; do
      gofmt -w "$f"
    done
    rounds=$((rounds + 1))
  done
  echo "[fmt-check] --fix 收敛用轮数: $rounds"
fi

unformatted=$(gofmt -l $files || true)
if [ -z "$unformatted" ]; then
  echo "[fmt-check] OK：全部已格式化"
  exit 0
fi

n=$(printf '%s\n' "$unformatted" | grep -c . || true)
echo "[fmt-check] 未格式化 $n 个："
printf '%s\n' "$unformatted"

if [ "${1:-}" = "--list" ]; then
  exit 0
fi
exit 1
