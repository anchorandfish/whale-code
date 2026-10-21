#!/usr/bin/env bash
# scripts/build.sh — E-1 二进制加固跨平台构建脚本（桶 X6）
#
# 用法：
#   bash scripts/build.sh [version]        # 构建当前平台加固版
#   bash scripts/build.sh [version] all    # 构建所有平台加固版
#
# 加固选项：
#   - Linux:   -buildmode=pie + -extldflags='-Wl,-z,relro,-z,now'（PIE + RELRO + BIND_NOW）
#   - Windows: -buildmode=pie + -s -w（Go 默认 DEP/NXCOMPAT，无 RELRO）
#   - macOS:   -buildmode=pie + -s -w（Go 默认 PIE）
#   公共：     -trimpath（移除构建路径，对标 codex release binary）
#
# 对标：codex release binary 启用 PIE + RELRO（REF-外部 codex @commit:待验证）
#
# 注意：
#   - Linux RELRO 需要 CGO_ENABLED=1（extldflags 走外部链接器）
#   - Windows/macOS 用 CGO_ENABLED=0（无 extldflags）
#   - 输出到 dist/ 目录

set -euo pipefail

# 解析参数
# 版本号默认不注入：让 internal/version.Version 的源码值原样生效（唯一真源）。
# 只有显式传参才覆盖：./scripts/build.sh v0.2.0
# 曾默认 "dev"，会让不带参数构建出的二进制自称 dev，与源码里的 0.1.0 打架。
VERSION="${1:-}"
TARGET="${2:-current}"

# 切换到项目根（脚本所在目录的上级）
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

# 创建输出目录
mkdir -p dist

# 公共 ldflags（去符号表 + 去 DWARF + 按需注入版本号）
# 版本号注入 internal/version.Version（唯一真源；main 包没有 version 变量）
VERSION_FLAG=""
[ -n "${VERSION}" ] && VERSION_FLAG="-X github.com/anchorandfish/whale-code/internal/version.Version=${VERSION}"
LDFLAGS_COMMON="${VERSION_FLAG} -s -w"

# buildHardened 构建 Linux 加固二进制（PIE + RELRO + BIND_NOW）
# 参数：$1=GOOS, $2=GOARCH, $3=输出文件名
buildHardened() {
    local goos="$1"
    local goarch="$2"
    local output="$3"

    echo ">>> Building hardened binary: ${goos}/${goarch} -> ${output}"

    case "${goos}" in
        linux)
            # Linux: PIE + RELRO + BIND_NOW（需要 CGO 走外部链接器）
            CGO_ENABLED=1 GOOS="${goos}" GOARCH="${goarch}" \
                go build -buildmode=pie \
                -ldflags "${LDFLAGS_COMMON} -extldflags='-Wl,-z,relro,-z,now'" \
                -trimpath \
                -o "dist/${output}" .
            ;;
        windows)
            # Windows: PIE + -s -w（Go 默认 DEP/NXCOMPAT，无 RELRO）
            CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
                go build -buildmode=pie \
                -ldflags "${LDFLAGS_COMMON}" \
                -trimpath \
                -o "dist/${output}" .
            ;;
        darwin)
            # macOS: PIE（Go 默认）+ -s -w
            CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
                go build -buildmode=pie \
                -ldflags "${LDFLAGS_COMMON}" \
                -trimpath \
                -o "dist/${output}" .
            ;;
        *)
            echo "ERROR: unsupported GOOS: ${goos}" >&2
            return 1
            ;;
    esac

    echo "    OK: dist/${output}"
}

# 根据目标构建
case "${TARGET}" in
    current)
        # 仅构建当前平台
        CURRENT_OS="$(go env GOOS)"
        CURRENT_ARCH="$(go env GOARCH)"
        case "${CURRENT_OS}" in
            windows) EXT=".exe" ;;
            *)       EXT="" ;;
        esac
        buildHardened "${CURRENT_OS}" "${CURRENT_ARCH}" "whale-${CURRENT_OS}-${CURRENT_ARCH}${EXT}"
        ;;
    all)
        # 构建所有平台
        buildHardened "windows" "amd64" "whale-windows-amd64.exe"
        buildHardened "linux"   "amd64" "whale-linux-amd64"
        buildHardened "darwin"  "amd64" "whale-darwin-amd64"
        buildHardened "darwin"  "arm64" "whale-darwin-arm64"
        ;;
    *)
        echo "ERROR: unknown target '${TARGET}' (use 'current' or 'all')" >&2
        exit 1
        ;;
esac

echo ""
echo "Hardened binaries built in dist/:"
ls -lh dist/
