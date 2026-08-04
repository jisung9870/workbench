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

## Slice 2A commands

```text
wb projects list [--json]
wb projects show <id> [--json]
wb projects add <path> [--id <id>] [--profile <profile>]
wb projects remove <id>
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

Profile files support `schema_version`, `default_backend`, and `editor`.
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

## Development

```bash
go test ./...
go vet ./...
go build ./cmd/wb
```
