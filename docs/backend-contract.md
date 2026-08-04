# Backend contract

Slice 2B uses a small capability-oriented contract rather than a plugin or RPC
framework.

```text
Name()                         stable backend name
Detect(ctx, open-request)      availability, version, capabilities, reason
OpenProject(ctx, request)      command result and backend reference
```

Later session and agent slices should add narrow optional interfaces for
`list_sessions`, `launch_agent`, `jump`, `stop`, and `health`; adapters must not
claim unsupported capabilities or implement misleading no-op methods.

## Selection

Selection order for `auto` is:

1. project `default_backend`
2. active profile `default_backend`
3. Windows Terminal on native Windows
4. cmux capability on macOS when not in SSH
5. tmux when already in tmux, SSH, or WSL
6. shell

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

References currently use `shell:<project-id>`, `tmux:<project-id>`,
`cmux:<project-id>`, and `windows-terminal:<project-id>`.

## Windows and WSL

Native projects use `wt.exe ... --startingDirectory <native-path>`. WSL opens
use `wt.exe ... wsl.exe [-d <distro>] --cd <wsl-path>`. A native project may
target WSL only when both `windows_wsl.distro` and `windows_wsl.wsl_path` are
present. Windows Terminal settings are read as JSONC when available so a missing
configured profile can fall back during preflight with recovery guidance.
