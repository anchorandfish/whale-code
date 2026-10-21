# 快速上手

[English](GETTING_STARTED.md) | **简体中文**

一份面向第一次使用的手册：装好、配好、然后拿它改代码。

## 构建

需要 [Go 1.25+](https://go.dev/dl/)。

```bash
git clone <this repository>
cd whale-code
CGO_ENABLED=0 go build -o whale .
```

产物是单个静态二进制 `whale`，无运行期依赖（第三方源码已 vendored，构建不需要联网）。

## 配置

**API key 放环境变量**（不写入任何文件）：

```bash
export ANTHROPIC_API_KEY=sk-ant-...   # 或 OPENAI_API_KEY=sk-...
```

**其余配置都在 `~/.whale/settings.json`**。首次运行 `./whale` 时，引导程序会帮你生成它；之后直接编辑这个文件即可。常用字段：

| 字段 | 说明 |
|---|---|
| `model` | 使用的模型 |
| `fallback_model` | 主模型不可用时自动切换的备用模型 |
| `permission_mode` | `read-only` / `workspace-write` / `danger-full-access` |
| `auto_compact` | 对话过长时自动压缩上下文 |

**中途加模型、换模型**不用改文件，在会话里直接敲：

```text
/model              查看当前模型
/model <name>       切换到指定模型
/model add <name>   添加一个模型（支持 --base-url / --provider）
```

**从别的工具迁来**：`./whale migrate --from codex|claude-code` 可导入已有配置。

**自检**：

```bash
./whale doctor        # 环境与配置诊断
./whale --check-api   # 测试 API 连通性
```

状态目录是 `~/.whale/`。要换位置就设 `WHALE_HOME` —— 该目录必须已存在（先 `mkdir -p`）。

## 启动

```bash
./whale
```

然后直接说要做什么就行。常用参数：`-p "任务"` 预填提示词、`-c` 恢复上次会话，完整列表见 `./whale --help`。

## 常用命令

会话里直接说要求 —— "把 X 函数改成 Y"、"修一下这个报错"、"给这个模块加个测试"。它自己读代码、改文件、跑命令，每一步都在你的权限规则之内。下面是按场景分组的常用命令（只列已实现的；完整清单会话内敲 `/help`）。

**改错了怎么办：**

| 命令 | 什么时候用 |
|---|---|
| `/undo` | 改错了，撤销最近的文件改动 |
| `/redo` | 重做刚撤销的更改 |
| `/diff [--stat]` | 看这轮 AI 到底改了什么 |
| `/clear --confirm` | 清空对话重新开始 |

**上下文与状态：**

| 命令 | 什么时候用 |
|---|---|
| `/context` | 想看当前上下文里有什么 |
| `/compact` | 对话太长、响应变慢时压缩上下文 |
| `/usage` | 查看 token 用量 |
| `/status` | 查看当前完整状态 |

**会话管理：**

| 命令 | 什么时候用 |
|---|---|
| `/sessions` | 列出所有会话 |
| `/resume [id]` | 恢复某个会话 |
| `/name <n>` | 给会话起名，方便下次找 |
| `/copy last` | 把刚才的回复复制出去 |
| `/export --format markdown` | 把整个会话导出成文件 |

**会话内调整配置：**

| 命令 | 什么时候用 |
|---|---|
| `/model <name>` | 换个模型再试 |
| `/models` | 列出所有可用模型 |
| `/config` | 查看与修改配置 |
| `/permissions` | 调整权限档位 |
| `/sandbox on\|off` | 开关沙箱 |
| `/security-preset safe\|standard\|unrestricted` | 一键切安全档位（同时改审批与沙箱） |
| `/memory` | 项目记忆管理（list/edit/add…） |
| `/skills` · `/skill trust <n>` | 查看技能 / 管理技能信任 |
| `/mcp list\|add\|remove\|restart` | 管理 MCP 服务器 |

**兜底：**

| 命令 | 什么时候用 |
|---|---|
| `/help` | 忘了命令就敲它 |
| `/doctor` | 会话内直接跑诊断 |

## 会话与安全

- 会话存放在 `~/.whale/` 下，单写者模型：同一会话被打开两次会被拒绝；
  需要分支副本时用 `--fork-session`。
- 危险/破坏性命令无论哪种权限模式都必须显式确认。
- shell 与工具子进程运行在操作系统级沙箱内，工作区之外的写入会被拦截
  （macOS 全量支持；Linux 需内核 ≥ 5.13；Windows 暂只有进程隔离）。
  被拒绝的命令会说明拦了什么、为什么拦。
