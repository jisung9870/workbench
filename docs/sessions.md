# Managed tmux sessions

Workbench observes every tmux session and takes ownership only of project
sessions it creates or explicitly adopts. Ownership is stored in tmux user
options rather than a parallel Workbench session registry.

```bash
wb sessions list
wb sessions list --json
wb sessions show alpha
wb sessions attach alpha
wb sessions adopt alpha
wb sessions stop alpha
wb sessions jump %12
```

Sessions are classified as `managed`, `legacy`, or `foreign`. Managed sessions
contain a complete Workbench marker, project ID, and canonical project path.
Legacy sessions have none of those fields. Foreign sessions contain incomplete
or mismatched metadata and are never changed automatically.

Project open, Agent launch, and workflow launch create or reuse the exact
project-ID session. New ownership is committed last, after the project ID and
path options are written. Existing legacy sessions remain legacy until
`wb sessions adopt <project-id>` verifies that the session name and first pane
start path match the registered project. `stop` re-reads and verifies ownership
before killing the exact session target.

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

`wb sessions attach <name>` switches the current tmux client or interactively
attaches from the CLI. Dashboard Attach never starts a blocking interactive
child, so it succeeds only when the Dashboard server itself runs inside tmux.

tmux is optional. If the executable is missing or no server is running,
`wb sessions list` still exits successfully and reports `available: false` with
a reason. Project, Agent, worktree, Doctor, and terminal workflows continue.
