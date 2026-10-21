#!/usr/bin/env bash
# scripts/govulncheck.sh — E-2 本地 govulncheck 包装脚本（桶 X6）
#
# 用法：
#   bash scripts/govulncheck.sh             # 扫描 ./...
#   bash scripts/govulncheck.sh ./internal/...  # 扫描指定包
#
# 功能：
#   1. 检测 govulncheck 是否已安装，未安装则提示安装命令
#   2. 运行 govulncheck 全量漏洞扫描
#   3. 运行 go mod verify 验证依赖完整性
#   4. 输出人类可读的扫描报告
#
# 对标：codex CI 启用 govulncheck；本脚本提供本地等价能力
# 与 .github/workflows/security.yml 行为一致

set -euo pipefail

# 切换到项目根
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

# checkGovulncheck 检查 govulncheck 是否已安装，未安装则提示
checkGovulncheck() {
    if ! command -v govulncheck >/dev/null 2>&1; then
        echo "ERROR: govulncheck not installed." >&2
        echo "  Install with: go install golang.org/x/vuln/cmd/govulncheck@latest" >&2
        echo "  Then ensure \$GOPATH/bin is in your PATH." >&2
        exit 1
    fi
}

# runVerify 运行 go mod verify 验证依赖完整性
runVerify() {
    echo "=== go mod verify ==="
    if ! go mod verify; then
        echo "ERROR: go mod verify failed — dependencies may be tampered" >&2
        exit 1
    fi
    echo "OK: dependencies verified"
    echo ""
}

# runGovulncheck 运行 govulncheck，参数为扫描目标（默认 ./...）
runGovulncheck() {
    local target="${1:-./...}"
    echo "=== govulncheck ${target} ==="
    # -show=verbose 显示详细调用路径
    # 发现漏洞时 govulncheck 返回非零退出码
    if ! govulncheck -show=verbose "${target}"; then
        echo ""
        echo "ERROR: govulncheck found vulnerabilities." >&2
        echo "  Fix by:" >&2
        echo "    1. Update vulnerable dependency: go get -u <module>" >&2
        echo "    2. Tidy: go mod tidy" >&2
        echo "    3. Re-run: bash scripts/govulncheck.sh" >&2
        exit 1
    fi
    echo "OK: no vulnerabilities found"
}

# 主流程
TARGET="${1:-./...}"
checkGovulncheck
runVerify
runGovulncheck "${TARGET}"
