# Agent task registry and ownership

`wb agents` is the authoritative registry for Workbench-launched Codex and
Claude Code tasks. It does not infer normal task state from terminal titles,
process names, or UI text.

## Commands

```text
wb agents list [--project <id>] [--json]
wb agents show <task-id> [--json]
wb agents start <project-id> --agent codex|claude [--worktree <id>] [--backend <backend>]
wb agents jump <task-id>
wb agents stop <task-id>
```

An optional worktree must be owned by Workbench and must still match current
Git porcelain. Externally created, drifted, or prunable worktrees cannot be
used as a registered agent launch target.

## State contract

`agents.json` uses schema version 1. A launch is stored before backend mutation:

```text
starting -> running -> waiting | idle -> completed
                    \-> failed | stopped
```

The implementation also permits a running, waiting, or idle task to return to
another active state before reaching a terminal state. Terminal states cannot
transition back to active. Launch failures remain queryable as `failed`, even
when the backend never produced a reference.

On Unix and WSL the authoritative registry is
`${XDG_STATE_HOME:-~/.local/state}/workbench/agents.json`; on native Windows it
is `%LOCALAPPDATA%\workbench\agents.json`. The Dashboard snapshot reports the
resolved path as `agent_registry_path` and shows it in Task history.

The Dashboard counts and lists only active states (`starting`, `running`,
`waiting`, and `idle`) in Active Agents. Terminal states (`completed`,
`stopped`, and `failed`) remain available under the collapsed Task history
disclosure. Jump and Stop are disabled for terminal records. Clear history
sends the exact terminal task IDs shown at confirmation time and removes only
those records from the selected project. A changed or stale set is rejected;
active and newly completed records are preserved. The previous registry is
copied to the Workbench `backups/` directory before the atomic write, and the
recovery path is shown in the success notice. All Agent registry mutations use
a per-registry process lock plus an OS advisory file lock (`flock` on supported
Unix platforms and `LockFileEx` on Windows), which the OS releases when the
owner process exits.

Workbench tasks use `state_source=registry`. Compatibility observations from
the legacy binbox pane scraper are valid only with an ID prefixed by `legacy:`
and `state_source=scrape`; they are never granted Workbench stop authority.

## Backend ownership

| Backend | Registered reference | Jump | Stop safety |
|---|---|---|---|
| tmux | exact pane plus session | focuses the pane | pane option `@workbench_task_id` must equal the task ID |
| cmux | dedicated new workspace | selects the workspace | exact workspace reference must still be listed by cmux |
| shell | attached process PID for diagnostics | unavailable | refused because PID identity cannot be safely revalidated |
| Windows Terminal | launch-only task reference | unavailable | refused because `wt.exe` exposes no stable tab ownership here |

The cmux runtime uses its documented CLI/socket surface: create/list/select/
close workspace and send input to a specific surface. cmux defaults to allowing
socket access only from cmux-managed processes, so a launch outside that trust
boundary is reported unavailable rather than weakening the user's access mode.
See the [cmux CLI reference](https://cmux.com/docs/api).

For cmux, the API necessarily accepts terminal input as text. Workbench builds
only a fixed `cd` plus fixed allowlisted agent command and single-quotes the
canonical path and resolved executable; arbitrary prompts or shell fragments
are not accepted by this command.

## Failure and recovery

Registry files are strict JSON. Existing state is backed up before atomic
replacement. If the backend launch succeeds but the final state update fails,
the command returns exit 5 and includes the surviving task/backend information.

`stop` first requires a registered active task and a non-empty backend
reference. It then invokes backend-specific ownership validation. An unknown
task ID, a mismatched tmux pane marker, or a missing cmux workspace never falls
back to process-name, session-name, or PID guessing.

cmux launch behavior is covered with a mocked CLI contract in cross-platform
tests. A real macOS cmux smoke test remains required before declaring the macOS
runtime operational on a release machine.
