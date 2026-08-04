# Windows Terminal and WSL

Workbench treats Windows Terminal as a launch backend, not as its state owner.
Projects and Agent tasks remain in the Workbench registries; a launched tab or
pane receives a launch-only reference and cannot be guessed for jump or stop.

## Profile settings

The active profile may contain machine-local preferences:

```toml
schema_version = 1
default_backend = "auto"
windows_terminal_profile = "Ubuntu-24.04"
windows_terminal_distro = "Ubuntu-24.04"
windows_terminal_window = "last"
windows_terminal_mode = "tab"
```

`windows_terminal_profile` accepts an installed Windows Terminal profile name
or GUID. When settings JSONC is readable, Workbench checks the configured value
case-insensitively and reports visible profile names for recovery.

`windows_terminal_window` accepts `last`, `new`, or a window ID/name. The
default is `last`. `windows_terminal_mode` has four values:

| Value | Windows Terminal command |
|---|---|
| `tab` | `new-tab` |
| `split-auto` | `split-pane` |
| `split-horizontal` | `split-pane --horizontal` |
| `split-vertical` | `split-pane --vertical` |

For an existing window, a split opens beside the selected pane according to
Windows Terminal's pane rules. A missing named window may cause Windows Terminal
to create a window; use `last` when the intent is to target the latest window.

The CLI can override the last two settings for one project open:

```powershell
wb open terraform-lab --backend windows-terminal --window new
wb open terraform-lab --backend windows-terminal --window last --terminal-mode split-vertical
```

Workbench rejects these options if another backend is selected. It never
accepts a free-form Windows Terminal command fragment.

## Native and WSL paths

A native Windows project launches with `--startingDirectory` and retains its
native canonical path. A WSL launch uses `wsl.exe -d <distro> --cd <wsl-path>`.
Workbench does not infer a Linux path by rewriting a Windows path.

For a native registry entry that should open in WSL, add both values explicitly:

```toml
[projects.windows_wsl]
distro = "Ubuntu-24.04"
wsl_path = "/home/me/projects/terraform-lab"
```

Distro precedence is the project overlay, active profile
`windows_terminal_distro`, then `WSL_DISTRO_NAME`. On WSL, detection returns an
unavailable capability with recovery guidance when no distro is known.

## Verification and fallback

Use `wb doctor --json` to inspect Windows Terminal availability without opening
a window. Unit tests verify the exact argument arrays for new windows, tabs,
pane orientations, profile names/GUIDs, distro precedence, and native/WSL path
boundaries. A final interactive smoke must be run on the target Windows/WSL
machine because it creates visible terminal windows.

If Windows Terminal is unavailable, request `--backend tmux` inside WSL or
`--backend shell`. An explicitly requested unavailable backend exits without
opening a fallback terminal.

Official command references:

- [Windows Terminal command-line arguments](https://learn.microsoft.com/windows/terminal/command-line-arguments)
- [Basic commands for WSL](https://learn.microsoft.com/windows/wsl/basic-commands)
