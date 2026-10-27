# Whale Code

> 🐋 给个人用户用的本地 AI 编码助手。住在终端里，跑在你自己的电脑上。

[English](README.md) | **简体中文**

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://golang.org)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)](#安全)

Whale Code 是面向个人开发者的编码智能体：你用自然语言描述任务，它读你的代码、改文件、执行命令 —— 全程在你设定的权限规则之内。

- **为个人使用而生** —— 不用部署服务器、不用注册账号、没有团队配置。一个二进制加一个终端就能跑。
- **本地运行** —— 会话、配置都存在你机器上（`~/.whale`），没有我们的服务器，无遥测。联网只为了调用你配置的 AI 服务商（以及你自己添加的扩展，如 MCP 服务器）。

## 安装

**1. 预编译二进制** —— macOS / Linux，一行装好：

```bash
# macOS（Apple Silicon 芯片）
curl -fL -o whale https://github.com/anchorandfish/whale-code/releases/latest/download/whale-darwin-arm64

# 其他平台：同一个地址，换文件名
#   macOS Intel      whale-darwin-amd64
#   Linux x86_64     whale-linux-amd64
#   Linux arm64      whale-linux-arm64
#   Windows x86_64   whale-windows-amd64.exe   （存成 whale.exe）

chmod +x whale && ./whale --version
```

也可以到 [最新发布](https://github.com/anchorandfish/whale-code/releases/latest) 页面手动挑文件。

> **macOS —— 仅当你用浏览器下载时**：二进制目前没有代码签名，Gatekeeper 会拦下并提示
> *"无法打开，因为无法验证开发者"*。执行一次 `xattr -d com.apple.quarantine ./whale` 即可放行，
> 或者在「系统设置 → 隐私与安全性」里点「仍要打开」。上面那条 `curl` 命令根本不会带上这个标记。

**2. 用 Go 安装**（1.26+）：

```bash
go install github.com/anchorandfish/whale-code@latest
# Go 按导入路径末段命名，所以装出来叫 `whale-code`：
mv "$(go env GOPATH)/bin/whale-code" "$(go env GOPATH)/bin/whale"   # 可选
```

**3. 从源码构建**：

```bash
git clone https://github.com/anchorandfish/whale-code.git
cd whale-code
CGO_ENABLED=0 go build -o whale .
```

三种方式得到的都是单个静态二进制，无运行期依赖 —— 名字是 `whale`；如果你走 `go install`
且没改上面那条 `mv`，则叫 `whale-code`。

## 配置

1. 给它一个服务商的 API key：

   ```bash
   export ANTHROPIC_API_KEY=sk-ant-...   # 或 OPENAI_API_KEY=sk-...
   ```

   这样导出后 key 不落盘，每次启动读取。如果选择在 onboarding 里粘贴，它会被明文存进
   `~/.whale/settings.json`（权限 `0600`，仅你自己可读）。

2. 启动 `./whale` —— 首次运行会引导你选择服务商和模型，并写入 `~/.whale/settings.json`。

3. 之后就在 `~/.whale/settings.json` 里配置，常用字段：

   ```json
   {
     "model": "claude-sonnet-4-20250514",
     "permission_mode": "workspace-write",
     "auto_compact": true
   }
   ```

   也可以在会话中途管理模型：`/model` 查看当前、`/model <name>` 切换、`/model add <name>` 添加。

4. 自检：

   ```bash
   ./whale doctor        # 环境与配置诊断
   ./whale --check-api   # 测试 API 连通性
   ```

从其他工具迁来？`./whale migrate --from codex|claude-code` 可导入已有配置。

状态目录是 `~/.whale/`。要换位置就设 `WHALE_HOME` —— 该目录必须已存在（先 `mkdir -p`）。

## 启动

```bash
./whale
```

然后直接说要做什么就行。常用参数：`-p "任务"` 预填提示词、`-c` 恢复上次会话 —— 完整列表见 `./whale --help`。

## 安全

- **三档权限模式** —— `read-only`、`workspace-write`、`danger-full-access`，由你决定智能体可以不经询问做多少事。
- **破坏性命令永远要确认** —— `rm -rf`、强制推送这类操作，无论哪一档都会先问你。拒绝规则永远优先于允许规则。
- **AI 服务商会看到你发给它的内容** —— 你让它处理哪个文件，那个文件的内容就会随请求发给模型服务商。除此之外没有其他对外流量。
- **操作系统级沙箱，按平台如实说明**：

| 平台 | 沙箱覆盖 |
|---|---|
| macOS | 完整 —— 通过 Seatbelt 限制文件系统写入 |
| Linux | 通过 namespaces 做进程/网络隔离；内核 ≥ 5.13 时经 landlock 限制写入 |
| Windows | 仅有进程隔离（Job Object）；文件系统写限制尚未实现 |

## 文档

- [快速上手](docs/GETTING_STARTED.zh-CN.md) —— 更详细的构建、配置、运行说明

## 许可

[MIT](LICENSE)
