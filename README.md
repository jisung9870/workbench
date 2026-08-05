# workbench

`wb` is the headless control plane for project, session, worktree, and agent
state shared by the dev environment clients.

## Baseline

- Toolchain: Go 1.25.12
- Targets: Linux (`amd64`, `arm64`), macOS (`amd64`, `arm64`), and Windows
  (`amd64`, `arm64`)
- Runtime: one CLI process; no daemon
- Data contract: schema version 1

The initial implementation uses the Go standard library for CLI parsing, JSON,
filesystem, and process behavior. `github.com/BurntSushi/toml` v1.4.0 is the
only runtime dependency because TOML is not part of the standard library. It is
small, stable, supports strict unknown-field detection, and reports parser
locations needed by `wb config validate`.

Remote repository creation and publishing are intentionally deferred. The
module path reserves the planned GitHub location.

## Implemented commands

```text
wb projects list [--json]
wb projects show <id> [--json]
wb projects add <path> [--id <id>] [--profile <profile>]
wb projects remove <id>
wb open <id> [--backend auto|cmux|windows-terminal|tmux|shell]
wb worktrees list <project-id> [--json]
wb worktrees create <project-id> <branch> [--base <ref>]
wb worktrees remove <worktree-id> [--delete-branch]
wb agents list [--project <id>] [--json]
wb agents show <task-id> [--json]
wb agents start <project-id> --agent codex|claude [--worktree <id>] [--backend <backend>]
wb agents jump <task-id>
wb agents stop <task-id>
wb doctor [--profile <name>] [--json] [--strict]
wb dashboard [--open auto|cmux|browser|none] [--port <0-65535>]
wb config validate
wb migrate [sessionizer] --check|--apply [--file <path>] [--profile <profile>]
```

`projects add` resolves the path to a canonical absolute directory and rejects
duplicate IDs or paths. `projects remove` edits only the registry; it never
deletes the repository. JSON reads emit exactly one schema-v1 envelope on
stdout, with diagnostics reserved for stderr.

## Files

Unix and WSL use:

```text
${XDG_CONFIG_HOME:-~/.config}/workbench/config.toml
${XDG_CONFIG_HOME:-~/.config}/workbench/projects.toml
${XDG_CONFIG_HOME:-~/.config}/workbench/profiles/*.toml
${XDG_STATE_HOME:-~/.local/state}/workbench/backups/
${XDG_STATE_HOME:-~/.local/state}/workbench/worktrees.json
${XDG_STATE_HOME:-~/.local/state}/workbench/agents.json
```

Native Windows uses `%APPDATA%\workbench` for configuration and
`%LOCALAPPDATA%\workbench` for state. Native Windows and WSL intentionally do
not share state files.

All registry changes create a state-directory backup when an earlier registry
exists, write a same-directory temporary file, flush it, rename it, and validate
the resulting TOML. File replacement uses Go's `os.Rename`; Go documents the
replacement as non-atomic on non-Unix platforms, so the pre-write backup is the
Windows recovery boundary.

## Configuration

`config.toml` is optional; the defaults are equivalent to:

```toml
schema_version = 1
active_profile = "personal"
```

Profile files support `schema_version`, `default_backend`,
`prefer_current_tmux`, `backend_priority`, `editor`,
`windows_terminal_profile`, `windows_terminal_distro`,
`windows_terminal_window`, and `windows_terminal_mode`.
Unknown TOML fields, unsupported schema versions, and parser errors fail
`wb config validate` instead of being silently ignored.

The schema-v1 registry is written in this shape (normally through
`wb projects add`):

```toml
schema_version = 1

[[projects]]
id = "terraform-lab"
name = "terraform-lab"
path = "/home/me/projects/terraform-lab"
repo_root = "/home/me/projects/terraform-lab"
default_backend = "auto"
editor = "nvim"
tags = ["terraform"]
profile = "personal"
```

An explicitly supplied `--id` is the portable identity when project directory
names differ across machines. Without it, `wb` derives a readable lowercase ID
from the directory basename and rejects collisions.

An optional active profile can select a backend and machine-local Windows
Terminal launch preferences:

```toml
schema_version = 1
default_backend = "auto"
prefer_current_tmux = true
backend_priority = ["cmux", "tmux", "shell"]
editor = "nvim"
windows_terminal_profile = "Ubuntu-24.04"
windows_terminal_distro = "Ubuntu-24.04"
windows_terminal_window = "last"
windows_terminal_mode = "tab"
```

The repository includes the same macOS-oriented starting point at
`examples/profiles/personal.toml`. Copy it to
`${XDG_CONFIG_HOME:-~/.config}/workbench/profiles/personal.toml` and adjust the
ordered list for a machine when needed.

`windows_terminal_profile` accepts the installed profile name or GUID. Window
values are `last`, `new`, or a window ID/name. Modes are `tab`, `split-auto`,
`split-horizontal`, and `split-vertical`. The distro setting is used before
`WSL_DISTRO_NAME`; an explicit project `windows_wsl.distro` overlay wins over
both. See [Windows Terminal and WSL](docs/windows-terminal.md).

## Opening a project

`--backend` always wins. With `auto`, `wb` first tries the project's
`default_backend` and the active profile default. When `prefer_current_tmux` is
true (the default), an existing tmux client is preserved before any other auto
backend is considered. A non-empty `backend_priority` then controls the
remaining concrete backend order; `auto` and duplicate entries are rejected.
Unavailable priority entries fall through to the built-in order: native Windows
Terminal, cmux on macOS outside SSH, tmux in tmux/SSH/WSL environments, and
finally the user's shell. An explicitly requested unavailable backend exits
with code 3 and lists usable alternatives.

```bash
wb open terraform-lab
wb open terraform-lab --backend tmux
wb open terraform-lab --backend windows-terminal
wb open terraform-lab --backend windows-terminal --window new
wb open terraform-lab --backend windows-terminal --window last --terminal-mode split-vertical
```

The shell adapter starts the configured interactive shell in the project
directory. The tmux adapter creates or reuses the exact project-ID session. The
cmux adapter invokes `cmux <project-path>` only on macOS. The Windows Terminal
adapter distinguishes native Windows from WSL and never infers a WSL path from
a Windows path. Native-to-WSL projects require an explicit registry overlay:

```toml
[projects.windows_wsl]
distro = "Ubuntu-24.04"
wsl_path = "/home/me/projects/terraform-lab"
```

External processes are invoked as argument arrays. Launch timeout, exit code,
stdout, stderr, command arguments, and backend reference are retained in the
result or error. After cancellation, non-interactive capture closes inherited
pipes after a bounded grace period so a detached descendant cannot hold the CLI
open indefinitely; this is a pipe-lifetime guarantee, not portable process-tree
ownership. Interactive shell/tmux sessions intentionally have no launch timeout
because their lifetime is controlled by the user.

## Worktrees

Workbench creates linked worktrees outside the main repository at:

```text
<repo-parent>/.worktrees/<project-id>/<stable-worktree-id>
```

The ID is deterministically derived from the stable project ID and branch, for
example `wt-alpha-feature-api-1a2b3c4d`. Git porcelain remains authoritative;
`worktrees.json` records stable IDs only for worktrees created by `wb`.
Externally created worktrees are visible as `managed=false` but cannot be
removed through Workbench.

```bash
wb worktrees create alpha feature/api --base main
wb worktrees list alpha --json
wb worktrees remove wt-alpha-feature-api-1a2b3c4d
```

Creation rejects a branch already checked out in any worktree. Removal rereads
`git worktree list --porcelain -z`, verifies the registered path and branch,
rejects locked or dirty worktrees, and uses `git worktree remove` without
`--force`. The branch is preserved by default. `--delete-branch` additionally
requires typing the exact branch name and uses safe `git branch -d`; an unmerged
branch therefore remains rather than being forced away.

## Sessionizer migration

The default source is the sibling legacy file
`${XDG_CONFIG_HOME:-~/.config}/tmux-sessionizer/dirs`. Parent entries import
their non-hidden depth-one directories; `=path` entries import that path
directly.

Always inspect the diff first:

```bash
wb migrate sessionizer --check
wb migrate sessionizer --apply
```

`--check` performs no writes. `--apply` backs up the legacy source before
writing the project registry; reruns skip canonical paths already registered.

## Agent tasks

`wb agents start` accepts only the fixed `codex` and `claude` executable names.
It writes a schema-v1 `starting` task before launching anything and records the
backend reference as soon as a process, tmux pane, cmux workspace, or Windows
Terminal tab launch is created. Registry-derived tasks always use
`state_source=registry`; legacy pane observations remain separate as
`legacy:*` / `state_source=scrape` records.

```bash
wb agents start alpha --agent codex --backend tmux
wb agents start alpha --agent claude --worktree wt-alpha-feature-api-1a2b3c4d
wb agents list --project alpha --json
wb agents jump task-19c...
wb agents stop task-19c...
```

Tmux panes carry an exact `@workbench_task_id`, which is reread before jump or
stop. A cmux task owns a newly created workspace and its reference must still
appear in `cmux list-workspaces --json` before selection or closure. Attached
shell processes and Windows Terminal tabs do not expose a safely reconnectable
identity, so `jump` and `stop` return capability exit code 3 instead of guessing
a PID or tab. See [docs/agents.md](docs/agents.md) for the state and ownership
contract.

## Doctor and capabilities

`wb doctor` checks the Workbench configuration and project/Agent/worktree state
schemas, required Git and shell capabilities, optional terminal backends,
binbox, Neovim, Codex, and Claude Code.

```bash
wb doctor
wb doctor --json
wb doctor --profile work --strict
```

The default mode fails only when a core capability is unavailable. Missing
optional tools remain visible as warnings. `--strict` also fails on optional
misses, which is useful for a fully provisioned machine check. JSON failures
retain the complete capability report in `data` instead of discarding the
successfully collected checks.

cmux is `disabled/skipped` outside macOS. Windows Terminal is
`disabled/skipped` outside native Windows and WSL. A platform-disabled backend
does not become an optional warning or strict-mode failure. See
[docs/doctor.md](docs/doctor.md) for the schema and recovery contract.

## Local Dashboard

`wb dashboard` serves an embedded responsive UI and versioned API on an
ephemeral loopback port. It remains a foreground process and releases the
listener when interrupted.

```bash
wb dashboard
wb dashboard --open browser
wb dashboard --open cmux
wb dashboard --open none --port 0
```

The Dashboard shows registered projects and Agent tasks, linked worktrees, Git
change summaries, and Doctor capabilities. Mutations are limited to typed
project-open and Agent start/jump/stop actions. Cross-origin requests and action
requests without the per-process token are rejected; no arbitrary shell command
field is exposed. See [docs/dashboard.md](docs/dashboard.md).

## Development

Install the current source build into the user environment:

```bash
make install
```

The default destination is `~/.local/bin/wb`. Override it when needed:

```bash
make install WB_INSTALL_DIR="$HOME/bin"
```

```bash
go test ./...
go vet ./...
go build ./cmd/wb
```
