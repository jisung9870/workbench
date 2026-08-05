# Backend contract

Slice 2B uses a small capability-oriented contract rather than a plugin or RPC
framework.

```text
Name()                         stable backend name
Detect(ctx, open-request)      availability, version, capabilities, reason
OpenProject(ctx, request)      command result and backend reference
```

Agent runtimes add narrow `Launch`, `Alive`, `Jump`, and `Stop` operations.
Unsupported operations return a capability error rather than a misleading
no-op. Session operations remain a later slice.

## Selection

Selection order for `auto` is:

1. project `default_backend`
2. active profile `default_backend`
3. current tmux client when profile `prefer_current_tmux` is true (default)
4. profile `backend_priority` when configured
5. otherwise Windows Terminal on native Windows
6. otherwise cmux capability on macOS when not in SSH
7. otherwise tmux in tmux, SSH, or WSL
8. shell

`backend_priority` accepts an ordered, duplicate-free list of concrete
backends (`cmux`, `windows-terminal`, `tmux`, `shell`). It does not accept
`auto`. cmux remains excluded from SSH auto-selection even when listed. Setting
`prefer_current_tmux = false` allows the configured list to choose cmux before
tmux while running inside a tmux pane. Unavailable entries are skipped before
the built-in platform fallback is evaluated; the list is a preference, not an
allowlist.

Explicit `--backend` bypasses this order. An unavailable explicit backend does
not launch a fallback automatically. Auto mode may skip a backend only during
preflight; after a launch command begins, failure is returned without opening a
second terminal or workspace.

## Process safety

- Commands and arguments are separate values; project/profile text is never
  interpolated into a shell command.
- Project paths are canonicalized again immediately before launch.
- cmux and Windows Terminal launch commands have a 15-second timeout; version
  probes have a 2-second timeout.
- Interactive shell and tmux attachment inherit stdin/stdout/stderr and are not
  killed by an arbitrary timeout.
- Captured stdout, stderr, exit code, command array, and a backend-specific
  reference are retained on failure.
- Agent launch callbacks persist backend ownership immediately after the child
  process, tmux pane, cmux workspace, or Windows Terminal launch is created.
- tmux and cmux revalidate their exact registered target before a destructive
  stop. Shell PIDs and Windows Terminal launch-only references are never used
  for guessed termination.

References currently use `shell:<project-id>`, `tmux:<project-id>`,
`cmux:<project-id>`, and `windows-terminal:<project-id>`.

## Windows and WSL

Native projects use `wt.exe ... --startingDirectory <native-path>`. WSL opens
use `wt.exe --window <target> (new-tab|split-pane) ... wsl.exe -d <distro>
--cd <wsl-path>`. A native project may
target WSL only when both `windows_wsl.distro` and `windows_wsl.wsl_path` are
present. Windows Terminal settings are read as JSONC when available so a missing
configured profile name or GUID can fall back during preflight with recovery
guidance. WSL distro precedence is project overlay, active profile, then
`WSL_DISTRO_NAME`; WSL detection refuses a launch if all three are absent.

Window and tab/pane selection are typed values rather than arbitrary command
fragments. `last` and `new` are the default-window targets; a configured ID/name
is also accepted. Modes map to `new-tab`, `split-pane`, `split-pane
--horizontal`, or `split-pane --vertical`. Per-invocation `--window` and
`--terminal-mode` options are valid only when the selected backend is Windows
Terminal.
