# Whale Code

**English** | [简体中文](README.zh-CN.md)

> 🐋 A local AI coding assistant for personal use. Lives in your terminal, runs on your machine.

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25.12-00ADD8?logo=go&logoColor=white)](https://golang.org)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)](#safety)

Whale Code is a coding agent for individual developers: you describe a task in natural language, and it reads your code, edits files, and runs commands — under permission rules that you control.

- **For personal use** — no server to deploy, no account, no team setup. One binary and your terminal.
- **Runs locally** — sessions, config, and checkpoints live on your machine (`~/.whale`). No telemetry, no server of ours. The only network traffic is calls to the AI provider you configure (plus extensions you add yourself, like MCP servers).

## Install

Pick one:

```bash
# 1. Download a prebuilt binary (macOS / Linux / Windows) from the Releases page

# 2. Install with Go (1.25+)
go install github.com/anchorandfish/whale-code@latest

# 3. Build from source
git clone https://github.com/anchorandfish/whale-code.git
cd whale-code
CGO_ENABLED=0 go build -o whale .
```

The result is a single static binary `whale` with no runtime dependencies.

## Configure

1. Set an API key for your provider (kept in an env var, never written to a file):

   ```bash
   export ANTHROPIC_API_KEY=sk-ant-...   # or OPENAI_API_KEY=sk-...
   ```

2. Start `./whale` — onboarding walks you through provider and model, and writes them into `~/.whale/settings.json`.

3. From then on, configure in `~/.whale/settings.json` — the main fields:

   ```json
   {
     "model": "claude-sonnet-4-20250514",
     "permission_mode": "workspace-write",
     "auto_compact": true
   }
   ```

   Or manage models mid-session: `/model` shows the current one, `/model <name>` switches, `/model add <name>` adds one.

4. Verify:

   ```bash
   ./whale doctor        # environment and config diagnostics
   ./whale --check-api   # test API connectivity
   ```

Coming from another tool? `./whale migrate --from codex|claude-code` imports your existing configuration.

State lives in `~/.whale/`. To use a different location, set `WHALE_HOME` — the directory must already exist (`mkdir -p` it first).

## Start

```bash
./whale
```

Then just type what you want done. Useful flags: `-p "task"` pre-fills a prompt, `-c` resumes the last session — full list in `./whale --help`.

## Safety

- **Three permission modes** — `read-only`, `workspace-write`, `danger-full-access`. You choose how much the agent may do without asking.
- **Destructive commands always ask** — `rm -rf`, force-push, and the like require confirmation in every mode. Deny rules always win over allow rules.
- **The AI provider sees what you send it** — when you ask it to work on a file, that file's content is included in the request to the model provider. Apart from that, no traffic leaves your machine.
- **OS-level sandbox, per platform** (honest version):

| Platform | Sandbox coverage |
|---|---|
| macOS | Full — filesystem write restriction via Seatbelt |
| Linux | Process/network isolation via namespaces; write restriction via landlock on kernels ≥ 5.13 |
| Windows | Process isolation only (Job Object); filesystem write restriction not implemented yet |

## Documentation

- [Getting Started](docs/GETTING_STARTED.md) — build, configure, and run in detail

## License

[MIT](LICENSE)
