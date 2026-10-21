# Getting Started

**English** | [简体中文](GETTING_STARTED.zh-CN.md)

A first-time guide: build it, configure it, then use it to work on code.

## Build

Requires [Go 1.25+](https://go.dev/dl/).

```bash
git clone <this repository>
cd whale-code
CGO_ENABLED=0 go build -o whale .
```

The result is a single static binary `whale` with no runtime dependencies
(third-party sources are vendored, so the build works offline).

## Configure

**Keep API keys in environment variables** (never written to disk):

```bash
export ANTHROPIC_API_KEY=sk-ant-...   # or OPENAI_API_KEY=sk-...
```

**Everything else lives in `~/.whale/settings.json`**. The first run of
`./whale` walks you through creating it; afterwards just edit the file.
Common fields:

| Field | Meaning |
|---|---|
| `model` | which model to use |
| `fallback_model` | backup model used if the primary is unavailable |
| `permission_mode` | `read-only` / `workspace-write` / `danger-full-access` |
| `auto_compact` | automatically compact long conversations |

**Add or switch models mid-session** without touching the file:

```text
/model              show current model
/model <name>       switch to a model
/model add <name>   add a model (supports --base-url / --provider)
```

**Coming from another tool**: `./whale migrate --from codex|claude-code`
imports your existing configuration.

**Sanity check**:

```bash
./whale doctor        # environment and config diagnostics
./whale --check-api   # test API connectivity
```

State lives in `~/.whale/`. To move it, set `WHALE_HOME` — the directory
must already exist (`mkdir -p` it first).

## Start

```bash
./whale
```

Then just type what you want done. Useful flags: `-p "task"` pre-fills the
prompt, `-c` resumes the last session; the full list is in `./whale --help`.

## Everyday commands

Inside a session, just state the goal — "change function X to do Y",
"fix this error", "add tests for this module". It reads code, edits files,
and runs commands on its own, always within your permission rules.
Below are the common commands grouped by scenario (implemented ones only;
type `/help` in-session for the full list).

**When something went wrong:**

| Command | When to use |
|---|---|
| `/undo` | revert the most recent file changes |
| `/redo` | re-apply what you just undid |
| `/diff [--stat]` | see exactly what the AI changed |
| `/clear --confirm` | wipe the conversation and start fresh |

**Context and status:**

| Command | When to use |
|---|---|
| `/context` | see what's currently in context |
| `/compact` | conversation got long / slow — compact it |
| `/usage` | check token usage |
| `/status` | full current status |

**Sessions:**

| Command | When to use |
|---|---|
| `/sessions` | list all sessions |
| `/resume [id]` | resume a session |
| `/name <n>` | name a session so you can find it later |
| `/copy last` | copy the last reply to the clipboard |
| `/export --format markdown` | export the whole session to a file |

**Adjust settings mid-session:**

| Command | When to use |
|---|---|
| `/model <name>` | try a different model |
| `/models` | list available models |
| `/config` | view and change configuration |
| `/permissions` | adjust the permission mode |
| `/sandbox on\|off` | toggle the sandbox |
| `/security-preset safe\|standard\|unrestricted` | one-shot security preset (approval + sandbox) |
| `/memory` | project memory management (list/edit/add…) |
| `/skills` · `/skill trust <n>` | browse skills / manage skill trust |
| `/mcp list\|add\|remove\|restart` | manage MCP servers |

**Fallback:**

| Command | When to use |
|---|---|
| `/help` | forgot a command? type this |
| `/doctor` | run diagnostics without leaving the session |

## Sessions and safety

- Sessions live under `~/.whale/` with a single-writer model: opening the
  same session twice is rejected; use `--fork-session` for a branch copy.
- Dangerous/destructive commands always require explicit confirmation,
  regardless of permission mode.
- Shell and tool subprocesses run inside an OS-level sandbox; writes
  outside the workspace are blocked (full support on macOS; Linux needs
  kernel ≥ 5.13; Windows currently provides process isolation only).
  Rejected commands explain what was blocked and why.
