# Local Dashboard

The Dashboard is a client of the Workbench core. It does not become a daemon or
a second state owner and does not parse browser-side copies of registry files.

## Start and stop

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

## Typed actions

`POST /api/v1/actions` accepts only these action IDs and their typed fields:

| Action | Required fields | Boundary |
|---|---|---|
| `open_project` | `project_id`, optional `backend` | cmux or Windows Terminal only |
| `start_agent` | `project_id`, `agent_kind`, optional `backend` | detached tmux/cmux/Windows Terminal runtime |
| `jump_agent` | `task_id` | registered active task only |
| `stop_agent` | `task_id` | registered ownership revalidation; UI confirmation |

Shell-backed project open and Agent launch are refused because an interactive
child would block the Dashboard request and has no browser attachment target.
There is no arbitrary command, path, prompt, test command, or force-delete
field. The initial Run tests control is disabled until a registered workflow
contract exists.

## Browser security

- every action requires a random per-process token embedded in the same-origin
  page and sent in `X-Workbench-Token`;
- an `Origin` header, when present, must match the request host;
- request JSON has a 16 KiB limit and rejects unknown fields or trailing values;
- responses do not enable CORS and use a restrictive Content Security Policy,
  frame denial, no-referrer, no-sniff, and no-store headers;
- project and task values are passed to backends as argument arrays.

The token is runtime-only. It is never written to Workbench state, logs, or a
committed asset.

## Verification

Handler tests cover the versioned envelope, action token and origin checks,
unknown-field rejection, loopback binding, and listener shutdown. Fake
executors verify browser/cmux command arrays and Git status arguments. The UI
JavaScript is syntax-checked without starting a browser. A final visual and
interactive action smoke remains a target-machine check.
