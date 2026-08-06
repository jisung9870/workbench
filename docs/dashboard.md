# Local Dashboard

The Dashboard is a client of the Workbench core. It does not become a daemon or
a second state owner and does not parse browser-side copies of registry files.

The embedded web surface has two product routes:

| Route | Purpose |
|---|---|
| `/` | Operational Dashboard for projects, Agent tasks, worktrees, Git state, and Doctor |
| `/guide` | Searchable, offline product documentation shipped with the current binary |

`/docs` is an alias for `/guide`. Both pages share the same System, Light, and
Dark theme control.

## Start and stop

Run the Dashboard in the foreground during interactive use or development:

```bash
wb dashboard
wb dashboard --open browser
wb dashboard --open cmux
wb dashboard --open none --port 0
```

The default `auto` target uses the cmux browser on macOS when cmux is available
and otherwise asks the platform default browser to open the URL. An opener
failure prints the loopback URL for manual use while the server remains active.
`Ctrl-C` or `SIGTERM` shuts down the HTTP server and closes its listener.

Run the same Dashboard as a managed background server when it must survive the
launching terminal:

```bash
wb server start
wb server start --open browser --port 0
wb server status
wb server status --json
wb server stop
```

Background start defaults to `--open none` and an OS-assigned port. The selected
URL is returned by `start` and `status`. Runtime metadata is stored in
`${XDG_STATE_HOME:-~/.local/state}/workbench/server.json` on Unix/WSL or
`%LOCALAPPDATA%\workbench\server.json` on native Windows. Server output is
appended to the adjacent `server.log`.

The state file is mode 0600 and contains a random instance ID and shutdown
token. `status` requires the loopback management endpoint to match the recorded
instance and PID. `stop` sends the private token to that endpoint and waits for
graceful shutdown; it never kills a process based only on a possibly reused PID.
An unreachable but still-live registered PID is reported as `unavailable`
instead of being overwritten or killed.

`--port 0` asks the operating system for an available port. A fixed port must be
between 0 and 65535. Binding is always `127.0.0.1`; a wildcard or externally
reachable address is not configurable.

## Information model

`GET /api/v1/snapshot` returns one schema-v1 Workbench envelope containing:

- projects from the project store;
- reconciled Agent task records;
- Git-verified linked worktrees;
- per-project branch and porcelain change summaries;
- the complete Doctor report and non-fatal collection warnings.

The browser refreshes this snapshot every 15 seconds and on demand. Project,
Agent, worktree, change, and capability state stays read-only in the UI.

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
| `open_project` | `project_id`, optional `backend` | WSL can combine a tmux session with a Windows Terminal surface; shell remains interactive-only |
| `start_agent` | `project_id`, `agent_kind`, optional `backend` | detached tmux/cmux/Windows Terminal runtime |
| `jump_agent` | `task_id` | registered active task only |
| `stop_agent` | `task_id` | registered ownership revalidation; UI confirmation |
| `clear_agent_history` | `project_id`, exact `task_ids` | confirmed terminal records only; stale-set rejection, cross-process lock, and registry backup |

On WSL, `auto` respects `prefer_current_tmux` as a session preference. The
Dashboard process validates tmux and Windows Terminal availability and first
ensures the exact project-ID session through the shared session manager. New
sessions receive Workbench project ID/path ownership metadata; an existing legacy
session is reused without implicit adoption, while incomplete or mismatched
metadata is rejected. It then launches `wt.exe -> wsl.exe --exec tmux
new-session -A` as an argument array. The server does not attach to tmux itself
and never switches a client based on an inherited `TMUX` environment variable.
Closing the Windows Terminal tab leaves the tmux session available for the next
Open. Shell-backed project open is refused because
an interactive child would block the Dashboard request and has no attachment target.
There is no arbitrary command, path, prompt, test command, or force-delete
field. Terminal Agent records are separated from active tasks and cannot invoke
Jump or Stop. The initial Run tests control is disabled until a registered
workflow contract exists.

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
- background server shutdown requires a separate random token stored only in
  the mode-0600 server state and sent to the loopback management endpoint.

The token is runtime-only. It is never written to Workbench state, logs, or a
committed asset.

## Verification

Handler tests cover the versioned envelope, action token and origin checks,
unknown-field rejection, Dashboard/Guide routes and embedded assets, loopback
binding, and listener shutdown. Node tests exercise theme defaulting,
persistence, invalid values, and unavailable localStorage. Fake executors verify
browser/cmux command arrays and Git status arguments. The UI JavaScript is
syntax-checked without starting a browser, and the release verification includes
a browser smoke for theme switching, Guide search, and responsive navigation.
