# tmux session observer

Workbench observes tmux without taking ownership of its lifecycle. Every list
or Dashboard refresh executes a new read-only `tmux list-panes` query; no daemon
or Workbench session registry is created.

```bash
wb sessions list
wb sessions list --json
wb sessions jump %12
```

The snapshot preserves tmux's stable session (`$…`), window (`@…`), and pane
(`%…`) identifiers alongside display indexes and names. Pane metadata is limited
to the current path, foreground command, pane PID, active state, and hierarchy.
It does not read scrollback, environment variables, command arguments, or file
contents.

`wb sessions jump` accepts only a stable pane identifier matching `%<number>`.
Inside tmux it switches the current client directly to that pane. Outside tmux,
the CLI uses an interactive `attach-session` targeted at the pane. The Dashboard
uses the same typed identifier but never launches an interactive child, so its
jump action is available only when `wb dashboard` was started from tmux.

tmux is optional. If the executable is missing or no server is running,
`wb sessions list` still exits successfully and reports `available: false` with
a reason. Project, Agent, worktree, Doctor, and terminal workflows continue.
