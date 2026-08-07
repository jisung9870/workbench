# Workbench continuation plan

Updated: 2026-08-07

Status: **Ready to continue — Activity Center phase 1 is implemented and verified**

Canonical continuation branch: `codex/dashboard-activity-center`

## Outcome and direction

Workbench is the personal development-environment operations console. It owns
canonical project, Environment, Secret, session, Agent, workflow, profile, and
operational-event state. tmux remains the primary workspace, LazyVim remains the
lightweight editor, and binbox remains the provider of focused domain tools.
The Dashboard is a readable, keyboard-friendly client of Workbench rather than
a second state owner.

The next implementation should finish Activity Center usability before adding
new control surfaces. This keeps the original problem—finding running, waiting,
completed, and failed work—at the front of the roadmap.

## Implemented baseline

| Area | Status | Current contract |
| --- | --- | --- |
| Projects and worktrees | Core complete | Canonical registries, Git-verified lifecycle, CLI management |
| tmux sessions | Complete | Managed/adopted/foreign ownership, attach/jump/stop with identity revalidation |
| Agent tasks | Complete | Codex/Claude start, list, jump, stop, bounded terminal history |
| Observed tasks | Complete | Snapshot-only tmux projection; unmanaged tasks are never stopped by Workbench |
| Environments | Core complete | Migration, metadata, exports, Secret references, expiry policy and Dashboard editing |
| Secrets | Complete | Embedded age library, encrypted local store, CLI edit/copy with conditional clipboard clearing, metadata-only Dashboard writes |
| Workflows | Complete | Fixed allowlist, detached tmux execution, bounded metadata-only history |
| Background server | Complete | Loopback-only lifecycle and one-minute scheduler |
| Profile and tool health | Current scope complete | Typed profile editing; `bb doctor --json` status and recovery remain read-only |
| Activity Center phase 1 | Complete | Agent/workflow/Environment expiry transitions, deduplication, atomic mode-0600 state, 200-event retention, Dashboard timeline |

The branch includes the integrated commit chain from the terminal operations
console through managed sessions, expiry scheduling, Environment/Secret/Profile
Dashboard management, and Activity Center. Use
`git log --oneline origin/main..HEAD` for the exact current chain.

## Fixed product and security boundaries

- Workbench stays local-first and single-user. No external Vault migration is planned.
- The Dashboard binds only to loopback and accepts only typed, same-origin-token actions.
- Do not add an arbitrary command, executable, argument, path, prompt, environment value, or Secret-reading field to the Dashboard API.
- Activity and workflow history must never contain terminal output, scrollback, Secret values, environment values, or filesystem paths.
- Workbench must not duplicate tmux or LazyVim. It should improve observability and management around them.
- binbox domain tools may be surfaced only through versioned typed provider contracts; Dashboard code must not reconstruct shell commands from recovery text.

## Recommended remaining work

### P0 — Activity Center phase 2: filtering and acknowledgement

Decision: implement next.

1. Add client-side filters for severity, event kind, project, and time order.
2. Add a canonical acknowledgement cursor or acknowledged event set to the
   activity store; it must survive Dashboard and server restarts.
3. Add a typed `ack_activity` Dashboard action. It should accept exact event IDs
   or an explicit cutoff, reject unknown/stale selections, and never clear event
   history as a side effect.
4. Expose total and unacknowledged counts in the snapshot and Dashboard header.
5. Add CLI read/ack commands only if they reuse the same store contract; do not
   create a second CLI-only acknowledgement model.

Acceptance criteria:

- Repeated scheduler scans do not create duplicate events or reset acknowledgement.
- Filter changes require no server mutation or page reload.
- Acknowledgement remains correct after server restart and 200-event pruning.
- Unknown fields and stale event IDs return typed errors.
- Unit, Dashboard contract, race, and cross-platform build checks pass.

### P1 — Local notification delivery

Decision: implement after P0 so notification state and acknowledgement share one
event identity model.

1. Add a provider interface for macOS, Linux/WSL, and native Windows local notifications.
2. Add profile policy with an explicit default of `off`; supported levels should
   be `error`, `warning_error`, and `all`.
3. Notify only newly emitted transitions. Initial import of historical Agent or
   workflow records must not trigger a notification storm.
4. Persist delivered-event evidence so server restarts do not resend events.
5. Treat provider unavailability as visible degraded health, not as a failure of
   activity persistence or HTTP serving.

Acceptance criteria:

- Notification titles contain only the metadata already permitted in `activity.Event`.
- Delivery is deduplicated across scans and restarts.
- Unsupported platforms and disabled policy are explicit non-error states.
- Provider tests do not invoke a real desktop notification service.

### P2 — Global Environment and project-context management

Decision: recommended after monitoring is usable.

1. Add a global Environment catalog separate from the selected-project Context panel.
2. Support typed Environment creation, removal, and project link/unlink with
   conflict checks and destructive confirmation.
3. Keep arbitrary project-path registration in the CLI unless a trusted local
   path-selection contract is designed; do not add a free-form Dashboard path field.
4. Add worktree create/remove only through the existing project ID, branch
   validation, dirty/locked checks, and exact confirmation boundaries.

Acceptance criteria:

- Dashboard mutations call the same stores and validations as the CLI.
- Removing a linked Environment or dirty worktree is refused with recovery guidance.
- Secret references remain metadata-only throughout snapshots and errors.

### P3 — Typed binbox tool operations

Decision: conditional. Keep the current Tools panel read-only until binbox
provides a versioned action contract.

1. Define provider capabilities as fixed action IDs with typed schemas and risk labels.
2. Start with high-frequency tools such as account switching and Terraform
   inspection; keep `sec` and `wenv` state ownership in Workbench.
3. Require confirmation for mutating actions and return structured results.
4. Never execute `recovery`, display text, or caller-supplied command strings.

Acceptance criteria:

- An unsupported or schema-incompatible binbox version disables actions safely.
- Every action has an allowlisted ID, validated input schema, risk label, and test.
- Tool failure cannot corrupt Workbench registries or stop the Dashboard server.

### P4 — Background-server operations and release hardening

Decision: implement after the core console workflow is stable.

- Optional user-level autostart integrations for launchd, systemd user units, and
  Windows Task Scheduler, with install/status/remove commands and no administrator requirement.
- Scheduler interval and notification health visibility; avoid configuration
  knobs until a real usage need is observed.
- Keyboard navigation, accessibility audit, responsive-browser checks, and a
  visual regression fixture for Activity Center and management forms.
- Upgrade/migration tests for every persisted schema before changing schema version 1.

## Dependency order

```text
Activity filtering + acknowledgement
  -> notification delivery
  -> global Environment/context management
  -> typed binbox provider actions
  -> autostart and release hardening
```

P0 precedes P1 because notification deduplication depends on durable event
identity and consumption state. P2 and P3 should stay separate: Workbench-owned
registries require direct typed mutations, while binbox actions require a
provider-version compatibility boundary.

## Risks and controls

| Risk | Likelihood | Impact | Detection | Control / rollback |
| --- | --- | --- | --- | --- |
| Activity schema change loses acknowledgement | Medium | High | Migration and restart tests | Add migration before writer change; old binary may ignore the activity file |
| Historical events resend notifications | Medium | Medium | Restart fixture with existing history | Persist delivered IDs; suppress initial-history delivery |
| Dashboard tool action becomes arbitrary shell execution | Low if contract is followed | High | Unknown-field and allowlist tests | Keep Tools read-only until typed provider schema exists; revert action commit |
| UI creates a second source of truth | Medium | High | Compare CLI and Dashboard store results | Reuse core stores; snapshot remains read model |
| New background feature breaks server availability | Low | High | Server lifecycle and scheduler failure tests | Isolate job/provider failure; HTTP serving remains available |

## Continuation procedure

```bash
git fetch origin
git switch codex/dashboard-activity-center
git pull --ff-only
git status --short --branch
git log --oneline origin/main..HEAD
go test ./...
go vet ./...
```

For a new phase, branch from the verified continuation branch, keep one phase in
one reviewable commit, and update this document's statuses and acceptance
evidence in the same commit. Do not amend or rewrite the integrated history.

Suggested next-session instruction:

> Read `docs/handoff.md`, verify the current branch, and implement P0 Activity
> Center filtering and acknowledgement. Preserve the fixed security boundaries,
> run the documented verification, then report changes without starting P1.

## Verification baseline

The Activity Center phase-1 branch was checked with:

- `git diff --check`
- `go vet ./...`
- `go test ./...`
- `go test -race ./...`
- macOS arm64 and Windows amd64 `go build ./cmd/wb`
- `node --test internal/dashboard/testdata/*.mjs` — 9 passed

The Dashboard assets and API contracts were tested, but the Activity Center was
not manually rendered in a real browser during this phase. Browser rendering,
keyboard navigation, and visual regression evidence remain explicit P4 work.

## Evidence and document quality record

Facts in this handoff come from the repository implementation, tests, and Git
history on 2026-08-07. P0–P4 are recommendations, not claims of completed work.
No external source was required. Markdown is used because the repository keeps
operational documentation under `docs/`; the embedded HTML Guide has already
been updated for implemented behavior, while future recommendations remain in
this engineering handoff.

Quality review: blocking rubric sections 1, 2, 3, and 7 pass. Perspective,
execution readiness, and structure score 2/2 each. HTML rendering is not
applicable to this Markdown-only repository handoff; Markdown links, headings,
and command blocks must be rechecked whenever this file changes.
