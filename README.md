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
