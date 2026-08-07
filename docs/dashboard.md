# Local Dashboard

The Dashboard is a client of the Workbench core. It is not a second state owner
and does not parse browser-side copies of registry files. It may run in the
foreground or as an explicitly started background process.

The embedded web surface has a concise Overview, four operational category routes, and the Guide:

| Route | Purpose |
|---|---|
| `/` | At-a-glance summary and entry points to each operational category |
| `/projects` | Project actions, Tasks, contexts, worktrees, Git state, and workflows |
| `/activity` | Activity history, live tmux sessions, scheduler state, and task locations |
| `/settings` | Active Profile and metadata-only Secret management |
| `/system` | Workbench Doctor, tool health, and runtime status |
| `/guide` | Searchable, offline product documentation shipped with the current binary |

`/docs` is an alias for `/guide`. Both pages share the same System, Light, and
Dark theme control.

The left rail is intentionally bounded to the viewport. Long project lists
scroll inside the rail, while tmux, scheduler, and unregistered-task details
stay collapsed until requested. Category pages keep their primary detail in the
main content area.

## Start and stop

```bash
wb dashboard
wb dashboard --open browser
wb dashboard --open cmux
wb dashboard --open none --port 0
wb server start --open browser
wb server status
wb server stop
```

The default `auto` target uses the cmux browser on macOS when cmux is available
and otherwise asks the platform default browser to open the URL. An opener
failure prints the loopback URL for manual use while the server remains active.
`Ctrl-C` or `SIGTERM` shuts down the HTTP server and closes its listener.
`wb server start` writes a mode-0600 runtime record and log in the Workbench
state directory, verifies readiness through an authenticated loopback probe,
and returns. `status` rejects stale or PID-reused records; `stop` uses the
runtime-only control token for graceful shutdown.

The background server also hosts a local scheduler. `environment-expiry-scan`
and `activity-scan` run immediately and then once per minute. The expiry job
only derives permanent/active/expiring/expired counts. The activity job records
Agent, workflow, and Environment expiry state transitions in a mode-0600,
atomic, 200-event local history. It never records command output, Secret or
environment values, or filesystem paths. Job failure is isolated from HTTP
serving and is visible in `wb server status --json` and the Dashboard Scheduler
panel. Foreground `wb dashboard` reads existing activity history but reports the
scheduler as unavailable.

`--port 0` asks the operating system for an available port. A fixed port must be
between 0 and 65535. Binding is always `127.0.0.1`; a wildcard or externally
reachable address is not configurable.

## Information model

`GET /api/v1/snapshot` returns one schema-v1 Workbench envelope containing:

- projects from the project store;
- a metadata-only `contexts` projection with registry availability, environment metadata, project links, export key names,
  and secret-reference availability status;
- a metadata-only Secret catalog with typed store, explicit replacement, and
  remove actions;
- the active Profile's backend, editor, tmux preference, and Windows Terminal
  settings;
- a live tmux session/window/pane hierarchy using stable tmux IDs and explicit
  managed, legacy, or foreign ownership;
- scheduler availability, job status, last/next run, and expiry counts;
- bounded metadata-only activity events, newest first;
- reconciled managed Agent records and snapshot-only observed tmux Tasks;
- Git-verified linked worktrees;
- per-project branch and porcelain change summaries;
- the complete Doctor report and non-fatal collection warnings;
- an operations overview with typed counts, evidence-backed attention, active
  work locations, and normalized `bb doctor --json` tool health.

The browser refreshes this snapshot every 15 seconds and on demand. Each Task
keeps its provenance, ownership, confidence, evidence, and allowed actions.
Observed Tasks are projected from the current tmux foreground command and are
never written to the Agent registry. Their exit result is `unknown`; Workbench
does not infer success or failure after the command disappears.

The overview labels a tmux session detached only when tmux reports no attached
clients. Worktrees become stale attention only from Git's `prunable` state or a
verified managed-registry drift. Missing project paths and unavailable core
state come from Doctor capabilities. No age threshold or browser-side guess is
used. Active work locations preserve whether the underlying Task can currently
jump.

Binbox health is optional. The server invokes only the fixed argument array
`bb doctor --json`, accepts its schema-v1 capability contract, and strips the
reported executable paths. Missing, failed, or malformed providers remain
visible as unavailable health while the snapshot itself succeeds.

The Profile panel replaces the complete active schema-v1 profile through one
typed action. Workbench validates backend names and ordering, editor text,
tmux preference, and Windows Terminal values before creating a backup and
atomically replacing the profile file. The Tools panel remains observational:
it shows normalized status and recovery guidance but never executes install or
repair commands.

For the selected project, the Context panel shows its default environment ID,
AWS profile/region, Kubernetes context/namespace metadata, ordinary export key
names, and each secret variable's availability status. It distinguishes an
unavailable registry, an unlinked project, a missing linked environment, and
unhealthy secret-reference states. Typed forms can replace metadata, add or
remove ordinary exports, add or remove Secret references, and set or clear
expiry. Existing ordinary values and raw `sec://` references are never rendered;
replacement values are write-only request fields. Secret plaintext and
identity/store paths are neither accepted nor returned.

## Appearance

The **Theme** selector provides three options:

- `System` follows `prefers-color-scheme` and updates when the operating-system
  appearance changes;
- `Light` uses the warm-paper light palette regardless of the system setting;
- `Dark` uses the graphite dark palette regardless of the system setting.

The preference is shared by the Dashboard and Guide using the browser-local key
`workbench.dashboard.theme.v1`. It is not written to Workbench configuration or
state, sent to an API, or synchronized between browsers. If storage is blocked,
the selected theme still applies to the current page but is not persisted.

## Embedded Guide

The Guide follows the same information architecture used by mature operations
projects: overview and quickstart, architecture and concepts, task-oriented
guides, reference, operations, troubleshooting, and a glossary. It documents
only implemented behavior in the binary that serves it.

The current guide covers:

- the Workbench control-plane architecture and request flow;
- the roles of the CLI, Dashboard, LazyVim, cmux, binbox, and terminal backends;
- projects, profiles, backend selection, managed worktrees, and Agent lifecycle;
- Dashboard and Doctor operations, the security model, and fallback evidence;
- the CLI, configuration, local data, HTTP API, exit codes, and troubleshooting.

Product screenshots use isolated documentation fixtures so committed images do
not expose a user's project names, home directory, task identifiers, or local
state. Images are embedded in the binary and never loaded from a remote host.

Search filters whole sections as the user types. `Escape` clears the query.
Section navigation follows the currently visible heading, the layout collapses
for narrow screens, and print styles remove the navigation chrome.

## Typed actions

`POST /api/v1/actions` accepts only these action IDs and their typed fields:

| Action | Required fields | Boundary |
|---|---|---|
| `open_project` | `project_id`, optional `backend` | cmux, Windows Terminal, or WSL tmux-session/Windows-Terminal-surface combination |
| `attach_session` | `session_name` | exact session; non-interactive and therefore requires the server to run inside tmux |
| `adopt_session` | `project_id` | legacy session only after registered name and canonical start path verification |
| `stop_session` | `project_id` | re-reads complete Workbench ownership before killing the exact session |
| `update_environment` | typed `environment` mutation | metadata, export, Secret reference, or expiry operation only; no plaintext Secret field |
| `update_profile` | complete typed `profile` mutation | active profile only; validated atomic replacement with backup |
| `start_agent` | `project_id`, `agent_kind`, optional `backend` | detached tmux/cmux/Windows Terminal runtime |
| `jump_agent` | `task_id` | registered active task only |
| `stop_agent` | `task_id` | registered ownership revalidation; UI confirmation |
| `jump_task` | `task_id` | managed Task or currently re-observed `tmux:%<pane>` Task |
| `stop_task` | `task_id` | managed Task only; observed Tasks return `TASK_UNMANAGED` |
| `clear_agent_history` | `project_id`, exact `task_ids` | confirmed terminal records only; stale-set rejection, cross-process lock, and registry backup |
| `jump_pane` | `pane_id` | stable `%<number>` tmux pane ID only; switches the client that launched Dashboard |
| `run_workflow` | `project_id`, `workflow_id` | compiled allowlist only; canonical registry path and fixed executable/argv |

Shell-backed project open and Agent launch are refused because an interactive
child would block the Dashboard request and has no browser attachment target.
The observer itself never creates sessions or writes a parallel registry;
project, Agent, workflow, and explicit session actions use the managed-session
service. A missing tmux executable or server is reported as optional unavailable
snapshot data. A Dashboard pane or session attach is refused when the Dashboard
process is not running inside tmux; use `wb sessions jump <pane-id>` or
`wb sessions attach <name>` from a terminal instead.
There is no arbitrary command, path, prompt, argument, environment, or force-delete
field. Terminal Agent records are separated from active tasks and cannot invoke
Jump or Stop. Observed Tasks can Jump only after a fresh tmux snapshot and
stable-pane verification; Stop is visibly unavailable. Project workflows are
limited to the catalog documented in [workflows.md](workflows.md), confirmed in
the UI, and recorded in bounded local history.

## Browser security

- every action requires a random per-process token embedded in the same-origin
  page and sent in `X-Workbench-Token`;
- an `Origin` header, when present, must match the request host;
- request JSON has a 16 KiB limit and rejects unknown fields or trailing values;
- responses do not enable CORS and use a restrictive Content Security Policy,
  frame denial, no-referrer, no-sniff, and no-store headers;
- Guide, theme, CSS, and JavaScript assets are embedded in the binary and make
  no remote font, image, analytics, or other network requests;
- project and task values are passed to backends as argument arrays.
- Context rendering is an allowlist: only environment metadata, export key
  names, and normalized secret status are inserted into the DOM.

The token is runtime-only. It is never written to Workbench state, logs, or a
committed asset.

## Verification

Handler tests cover the versioned envelope, action token and origin checks,
unknown-field rejection, Dashboard/Guide routes and embedded assets, loopback
binding, and listener shutdown. Node tests exercise theme defaulting,
persistence, invalid values, and unavailable localStorage. Fake executors verify
browser/cmux command arrays, tmux snapshot parsing and stable-ID jumps, and Git status arguments. The UI JavaScript is
syntax-checked without starting a browser, and the release verification includes
a browser smoke for theme switching, Guide search, and responsive navigation.
