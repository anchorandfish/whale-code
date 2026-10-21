# Whale Code Makefile
# Usage:
#   make              build CLI only
#   make all          build CLI + VS Code extension
#   make extension    build VS Code extension only
#   make clean        clean + rebuild
#   make test         run all tests
#   make test-cli     run CLI tests only
#   make test-ext     run extension tests only
#   make test-race    run CLI tests with race detector (requires gcc/cgo)
#   make package      package VS Code extension as .vsix
#   make release      build all platform binaries with size optimization
#   make release-hardened  E-1: build hardened binaries (PIE+RELRO on Linux, -s -w on Windows)
#   make help         show help

.PHONY: build all clean test test-cli test-ext test-race test-release-core test-full extension package release release-hardened help

BINARY = whale
BINARY_EXE = whale.exe
VSCODE_DIR = vscode-extension
# 版本号的唯一真源是 internal/version.Version（见 internal/version/version.go）。
# 默认不注入任何版本 —— 让源码里的值原样生效；只有显式传 VERSION=… 才覆盖：
#   make build VERSION="$(git describe --tags)"
# 曾经这里写 `VERSION ?= dev`，结果 `make build` 会把版本号钉成 "dev"：
# 源码是 0.1.0、构建产物却是 dev，正是 「同一事实必须同源」要防的漂移。
VERSION ?=
VERSION_FLAG = $(if $(VERSION),-X $(VERSION_PKG).Version=$(VERSION))
# 二进制 size 优化（-s 去符号表，-w 去 DWARF 调试信息，-trimpath 去编译机路径）
# 三者组合可减小约 30% 二进制体积
#
# 可复现构建（）：Go 默认把两样「与代码行为无关」的东西编进二进制 ——
#   ① build id = 源码内容哈希；② buildvcs 标记 = git 提交号 + 工作树「脏」标记。
# 后果：只改一个注释字符，二进制 md5 也会变 ⇒ 手测期间「改注释」会被误当成「重建过」。
# 服法：-buildid= 清掉①、-buildvcs=false 清掉② ⇒ 只改注释时二进制字节相同；
#   改代码时仍然变（判别性保留）。实测与复现见
#   内部工作回执。
VERSION_PKG = github.com/anchorandfish/whale-code/internal/version
LDFLAGS = $(VERSION_FLAG) -s -w -buildid=
TRIMPATH = -trimpath
VCSFLAG = -buildvcs=false
GOFLAGS_BASE = -ldflags "$(LDFLAGS)" $(TRIMPATH) $(VCSFLAG)

# E-1 二进制加固选项（桶 X6）
# Windows: -s -w（Go 默认已启用 DEP/NXCOMPAT，无 PIE）
# Linux: -buildmode=pie + RELRO (-Wl,-z,relro,-z,now) + BIND_NOW
# macOS: -buildmode=pie（Go 默认 PIE，无需额外 extldflags）
LDFLAGS_LINUX_HARDENED = $(VERSION_FLAG) -s -w -buildid= -extldflags='-Wl,-z,relro,-z,now'
LDFLAGS_WINDOWS_HARDENED = $(VERSION_FLAG) -s -w -buildid=
LDFLAGS_DARWIN_HARDENED = $(VERSION_FLAG) -s -w -buildid=

# 默认只构建 CLI：不要让人跑一次 `make` 就去 npm install 前端依赖。
.DEFAULT_GOAL := build

# 构建目标永远是 phony：文件目标没有源码依赖，`make build` 在源码改了之后
# 会安静地 "up to date" 什么都不做 —— 那正是「二进制是旧的却以为重建过」的来源。
build:
	CGO_ENABLED=0 go build $(GOFLAGS_BASE) -o $(BINARY_EXE) .
	cp -f $(BINARY_EXE) $(BINARY)

all: build extension

extension:
	cd $(VSCODE_DIR) && npm install && npm run compile

package: extension
	cd $(VSCODE_DIR) && npx vsce package

clean:
	go clean -cache
	rm -f $(BINARY) $(BINARY_EXE) $(BINARY_EXE)~
	rm -rf $(VSCODE_DIR)/node_modules $(VSCODE_DIR)/out

test: test-cli test-ext

test-cli:
	go test ./...

test-race:
	WHALE_TEST_PARALLELISM=$${WHALE_TEST_PARALLELISM:-2} ./scripts/test-full.sh --race

test-release-core:
	./scripts/test-release-core.sh

test-full:
	./scripts/test-full.sh

test-ext:
	cd $(VSCODE_DIR) && npm install && npm test

# release 构建所有平台二进制（含 size 优化 + 版本注入）
# 适合 GitHub Release 上传
release:
	@echo "Building release binaries with version=$(VERSION)..."
	mkdir -p dist
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" $(TRIMPATH) $(VCSFLAG) -o dist/whale-windows-amd64.exe .
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" $(TRIMPATH) $(VCSFLAG) -o dist/whale-linux-amd64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" $(TRIMPATH) $(VCSFLAG) -o dist/whale-darwin-amd64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" $(TRIMPATH) $(VCSFLAG) -o dist/whale-darwin-arm64 .
	@echo "Release binaries built in dist/:"
	@ls -lh dist/

# release-hardened E-1：构建加固二进制（桶 X6）
# Linux 启用 PIE + RELRO + BIND_NOW（release 二进制加固）
# Windows 启用 -s -w（Go 默认 DEP/NXCOMPAT，无 PIE）
# macOS 启用 PIE（Go 默认）+ -s -w
# 注意：Linux RELRO 需要 cgo（extldflags 走外部链接器），CGO_ENABLED=1
release-hardened:
	@echo "Building hardened release binaries with version=$(VERSION)..."
	mkdir -p dist
	@echo "[1/4] Windows amd64 (-s -w, Go 默认 DEP/NXCOMPAT)"
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -buildmode=pie -ldflags "$(LDFLAGS_WINDOWS_HARDENED)" -trimpath $(VCSFLAG) -o dist/whale-windows-amd64-hardened.exe .
	@echo "[2/4] Linux amd64 (-buildmode=pie + RELRO + BIND_NOW)"
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildmode=pie -ldflags "$(LDFLAGS_LINUX_HARDENED)" -trimpath $(VCSFLAG) -o dist/whale-linux-amd64-hardened .
	@echo "[3/4] macOS amd64 (-buildmode=pie + -s -w)"
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -buildmode=pie -ldflags "$(LDFLAGS_DARWIN_HARDENED)" -trimpath $(VCSFLAG) -o dist/whale-darwin-amd64-hardened .
	@echo "[4/4] macOS arm64 (-buildmode=pie + -s -w)"
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -buildmode=pie -ldflags "$(LDFLAGS_DARWIN_HARDENED)" -trimpath $(VCSFLAG) -o dist/whale-darwin-arm64-hardened .
	@echo "Hardened release binaries built in dist/:"
	@ls -lh dist/

help:
	@echo "Whale Code Build Commands:"
	@echo "  make            Build CLI only (whale + whale.exe)"
	@echo "  make all        Build CLI + VS Code extension"
	@echo "  make extension  Build VS Code extension only"
	@echo "  make package    Package VS Code extension as .vsix"
	@echo "  make clean      Clean cache, binaries, and extension artifacts"
	@echo "  make test       Run all tests (CLI + extension)"
	@echo "  make test-cli   Run CLI tests only"
	@echo "  make test-race  Run CLI tests with race detector (requires gcc/cgo)"
	@echo "  make test-release-core  Run the fixed v0.1 release core gate without live model calls"
	@echo "  make test-full  Run the complete deterministic Go test suite without live model calls"
	@echo "  make test-ext   Run extension tests only"
	@echo "  make release    Build all platform binaries (Windows/Linux/macOS amd64+arm64)"
	@echo "  make release-hardened  E-1: Build hardened binaries (PIE+RELRO on Linux)"
	@echo "  make help       Show this message"
	@echo ""
	@echo "Variables:"
	@echo "  VERSION=0.1.0   Set version string (default: dev)"
