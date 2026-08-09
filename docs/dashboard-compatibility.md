# Dashboard compatibility baseline

> Status: S0 evidence and frontend change map; no runtime UI change
>
> Evidence date: 2026-08-10
>
> Root baseline: `63e22fabafcce4adf7f2429a5038749c2ad40519`
>
> Workbench baseline: `10347a9d169a2cb7fa69dd03e174437cb47d192e`

This report maps the current embedded Dashboard to the six target product
areas in [the Dashboard specification](../../plan/DASHBOARD-SPEC.md). It is an
inventory, not an API or UI implementation contract. **Current** below means
observable in the cited checkout; **planned** means gated by
[the implementation roadmap](../../plan/IMPLEMENTATION-ROADMAP.md). In
particular, example v2 routes and fields in the product specification are not
accepted backend fields.

## Evidence and interpretation

The current baseline was read from:

- [the current Dashboard contract](dashboard.md),
  [`internal/dashboard/dashboard.go`](../internal/dashboard/dashboard.go), and
  [`internal/cli/dashboard.go`](../internal/cli/dashboard.go) for HTTP, snapshot,
  action, and backend-selection behavior;
- [`assets/index.html`](../internal/dashboard/assets/index.html),
  [`assets/app.js`](../internal/dashboard/assets/app.js), and
  [`assets/style.css`](../internal/dashboard/assets/style.css) for rendered DOM,
  client state, responsive behavior, focus, and sensitive-value boundaries;
- [`dashboard_test.go`](../internal/dashboard/dashboard_test.go),
  [`cli/dashboard_test.go`](../internal/cli/dashboard_test.go), and the
  [`testdata`](../internal/dashboard/testdata) Node tests for executable evidence;
- [the target architecture](../../plan/ARCHITECTURE.md) and roadmap for ownership,
  stage gates, non-goals, and current/planned language.

The existing [backend adapter contract](backend-contract.md) and the concurrently
produced [Core contract baseline](core-contract-baseline.md) were also reviewed.
The former documents backend selection/process safety; the latter confirms the
15-action executable baseline, direct service composition, asymmetric partial
snapshot behavior, sensitive-path inventory, S1–S3 boundaries, and the same
request-size inconsistency found here. Neither is an accepted S2/S3 wire schema.

## Current route and deep-link inventory

The handler serves the same embedded HTML template with a route-specific body
class. CSS hides and reveals sections; every operational route fetches the same
`GET /api/v1/snapshot` and refreshes it every 15 seconds.

| Current URL | Current visible purpose | Target area | Compatibility decision |
|---|---|---|---|
| `/` | Overview metrics, category links, verified attention, work locations, and tool health | Today | Keep `/` as the canonical Today entry. Add `/today` only as an alias after the Today capability is available; until then it may render a clearly planned/unavailable shell, not invented data. |
| `/projects` | Project list/workspace, Context, Tasks/history, worktrees, changes, workflows, and task detail | Projects | Preserve the path. Incrementally replace registry-oriented panels only when their Core query/action gates exist. |
| `/activity` | Overview plus bounded activity events, tmux sessions, scheduler, and unregistered Tasks | Runs & Agents | Preserve as a server-side alias or redirect for at least one release after `/runs` exists. The compatibility path must keep query/fragment data and must not imply current tmux Tasks are Orca Agents. |
| `/settings` | Active profile editor, Tools, and Secret metadata/write forms | Integrations plus System & Recovery | Do not guess one redirect target while the page has two owners. During the split, keep `/settings` as a compatibility index that links to capability-gated subsections; profile/recovery belongs to `/system`, adapter account/context/scope belongs to `/integrations`. |
| `/system` | Overview, tmux/scheduler sidebar, Tools, and Workbench Doctor | System & Recovery | Preserve the path and expand the label/content only as S1 evidence becomes available. |
| `/guide`, `/guide/` | Embedded searchable documentation | Help entry outside the six primary areas | Preserve both forms and offline assets. |
| `/docs`, `/docs/` | Exact Guide alias | Help entry outside the six primary areas | Preserve as a compatibility alias. |
| `/inbox` | Not served (404) | Inbox | Planned S2 read-only shell; never treat 404 as an empty Inbox. |
| `/today` | Not served (404) | Today | Planned alias, gated as above. |
| `/runs` | Not served (404) | Runs & Agents | Planned S3 surface; `/activity` remains compatible during migration. |
| `/integrations` | Not served (404) | Integrations | Planned shell. S1–S3 may show local capability status only; external-provider UI is S5+. |

Current deep links stop at routes. There is no URL representation for selected
project, task, session, worktree, activity item, context, or panel. `projectId`
and `taskId` live only in JavaScript memory; load selects the first project,
navigation/reload loses task selection, and an overview work-location click
opens task detail without navigating to the owning Project route. There is also
no history handling. A future deep-link grammar must therefore be supplied by
the accepted Core identity contract; this report does not invent query names or
ID fields.

### Current non-page HTTP routes

| Route | Method/current role | Compatibility requirement |
|---|---|---|
| `/api/v1/snapshot` | `GET`; complete Dashboard snapshot in the repository schema-v1 envelope | Preserve through S1–S3. A compatibility adapter may source it from accepted services later; a frontend must not treat it as a target v2 model. |
| `/api/v1/actions` | `POST`; runtime-token/same-origin typed action union | Preserve security, exact v1 inputs, stable errors, and unknown/trailing-field rejection during migration. |
| `/api/v1/server` | `GET`; background server control/status wrapper | Server/runtime-owned, not a product area or browser state owner. Preserve current control semantics. |
| `/api/v1/server/stop` | Token-protected `POST`; background server shutdown | System runtime control only; do not confuse it with a target Core action or expose it as install/repair. |
| `/assets/app.js`, `/assets/theme.js`, `/assets/guide.js`, `/assets/style.css`, `/assets/dashboard-overview-light.jpg` | `GET/HEAD`; embedded offline assets | Preserve embedding, CSP/offline behavior, content types, and no remote dependency. New frontend modules require explicit handler/embed coverage. |

Other paths currently return 404. Page/Guide/assets accept only `GET`/`HEAD`,
the snapshot only `GET`, and actions only `POST`; method behavior remains part
of the compatibility regression suite.

## Current action inventory and target ownership

All 15 executable action identifiers are retained as compatibility inputs until
an accepted S3 mapping exists. The current public table lists 14 because it
omits tested `update_secret`; executable code/tests are the baseline until the
documentation discrepancy is resolved. “Hold” means the endpoint and its
current security checks remain regression-covered, but the control must not be
newly promoted onto a target screen merely because it exists today.

| Current v1 action | Current boundary/evidence | Single target owner | S1–S3 compatibility treatment |
|---|---|---|---|
| `open_project` | Project ID and optional typed backend; canonical project/backend preflight; Dashboard supports cmux, Windows Terminal, or WSL tmux via Windows Terminal, never interactive shell | Projects | Preserve from `/projects`; candidate mapping to planned `resume_project`/`open_work_location` only after the Core mapping is accepted. |
| `attach_session` | Exact session name; non-interactive Dashboard attach requires server inside tmux | Runs & Agents | Hold on compatibility Activity only; terminal fallback remains explicit. |
| `adopt_session` | Registered project and verified legacy tmux session; browser confirmation | Runs & Agents | Hold. It conflicts with the planned human-only tmux/Orca ownership direction and requires a later migration decision. |
| `stop_session` | Re-reads managed ownership before exact tmux stop; browser confirmation | Runs & Agents | Hold; do not expose on the new Runs surface without a phase/ownership gate. |
| `update_environment` | Typed sub-operations: metadata, export set/remove, Secret-reference set/remove, expiry set/clear; atomic store calls | Integrations | Preserve only in the compatibility Settings surface until Context/adapter ownership and S2 schema are accepted. Do not translate it into browser-authored Core fields. |
| `update_secret` | Typed set/remove; service and field metadata rendered; value is write-only and cleared from the input before request | Integrations | Preserve compatibility behavior; raw credentials never move into target snapshots/DOM. Owner split with System remains an open backend/planner decision. |
| `update_profile` | Complete active schema-v1 profile replacement, validation, backup, atomic save | System & Recovery | Preserve in compatibility Settings; planned S1 System cards are read/evidence-first and must not gain install/repair execution. |
| `start_agent` | Registered project, typed Agent kind/backend; detached tmux/cmux/Windows Terminal; shell refused | Runs & Agents | Hold and do not promote. Planned Orca-controlled start is S8, not S1–S3. |
| `jump_agent` | Registered active Agent task and backend ownership | Runs & Agents | Preserve for compatibility records; planned provider jump is S4B and must use fresh Orca identity. |
| `stop_agent` | Registered ownership revalidation | Runs & Agents | Hold; planned early Runs & Agents has no stop/remove. |
| `jump_task` | Managed task, workflow task, or freshly re-observed stable tmux pane | Runs & Agents | Preserve compatibility deep action; planned S3/S4 mapping remains schema-gated. |
| `stop_task` | Managed Agent task only; workflow and observed tmux Tasks are rejected with stable errors | Runs & Agents | Hold; never infer ownership from an observed card. |
| `clear_agent_history` | Project plus exact terminal task ID set; stale-set rejection, lock, and backup; browser confirmation | Runs & Agents | Hold; do not equate bounded registry deletion with review/receipt retention. |
| `jump_pane` | Stable tmux pane ID; jumps the client that launched Dashboard | Runs & Agents | Preserve only as legacy tmux compatibility. It is not evidence of an Orca Agent. |
| `run_workflow` | Project plus compiled workflow ID only; confirmation; no args/path/prompt/env input | Runs & Agents | Hold; S3 review may display existing results, but controlled execution is not automatically accepted. |

The current action transport serializes browser requests through one promise
queue, posts same-origin JSON with the runtime token, shows a transient notice,
and reloads the entire snapshot after success. It has no preview/apply split,
action ID, plan hash, expected revision, receipt reference, reconciliation UI,
or per-control pending state. Those are planned S3 capabilities and their field
names remain backend-owned.

## Current data and DOM sensitivity boundaries

### Boundaries already enforced

- The server listens only on loopback. Mutations require a random per-process
  token and same-origin check; JSON rejects unknown fields and trailing values;
  responses set CSP, frame denial, no-referrer, no-sniff, no-store, and no CORS.
- The browser does not parse registry files. It renders the server snapshot and
  HTML-escapes values inserted through `innerHTML`.
- Context projection includes environment metadata, export **key names**, and
  Secret-reference variable/status only. Tests reject reconstructable Secret
  service, field, raw `sec://` reference, and plaintext values.
- The Secret catalog renders service/field names, never stored values. A new
  plaintext value exists in the password input and request payload only; the
  input is cleared before the action call. This is a current compatibility
  exception, not permission to place credentials in future view models.
- Safe workflow results omit captured command output. Tool health strips
  executable paths, and activity is designed as metadata-only bounded history.
- Arbitrary command, path, prompt, argv, environment, or force/delete fields are
  not accepted by the action schema.

### Sensitive values currently rendered by design

These values are not Secret plaintext, but they are locally sensitive and must
be deliberately allowlisted/redacted in S2/S3 fixtures:

- canonical project paths and project IDs;
- worktree branch/path (path in a title attribute), changed filenames, Agent
  registry path, task IDs, task `cwd`, backend/pane/session/window IDs, tmux pane
  current paths and foreground command names;
- AWS profile/region, Kubernetes context/namespace, environment IDs, export key
  names, Secret variable names and availability; and
- workflow IDs/status/timestamps, activity resource/project IDs, Doctor/tool
  reasons, and recovery text.

The current snapshot can also contain non-fatal `warnings`, but `app.js` does
not render them. Backend review confirms collection behavior is asymmetric:
project or Agent listing can fail the whole snapshot, while environment,
workflow history/catalog, worktree, activity, tmux, and tool failures generally
survive as warnings or unavailable subtrees. Future common state components
must not blindly dump warning or error detail into the DOM: server error
messages/details can contain command diagnostics in current action failures, so
the backend redaction contract and a frontend allowlist are both required.

### Contract inconsistency to resolve

`dashboard.md` states a 16 KiB action-body limit, while the current handler sets
`maxActionBody` to 16 MiB plus 64 KiB. This report does not choose a normative
value. Backend/security owners must reconcile code, documentation, and a boundary
test before S1 compatibility acceptance.

## Loading, empty, error, and refresh state inventory

| State | Current behavior | Gap against target common states |
|---|---|---|
| Initial loading | Static placeholders say `loading`, `checking`, or zero while the full snapshot is fetched. Navigation and Guide work. | No area-level skeleton/state name, no `aria-busy`, and controls may look actionable before data exists. |
| Refreshing | Manual and 15-second refresh call the same `load`; old DOM remains until replacement. | No refreshing label/progress/generated time, no stale badge, and mutation controls are not disabled as a group. |
| Empty | Explicit copy exists for no projects, no tasks/history, worktrees, changes, workflows, events, scheduler jobs, Secrets, tools, and contexts. | Empty copy is ad hoc rather than a reusable state with cause and one safe next step; Inbox has no route/state. |
| Local unavailable | tmux, scheduler, Context registry, Secret store, Profile, tool provider, Git change read, and some task actions can show unavailable/reason states. | No shared semantic state/name or fallback slot; optional and core failures can look alike. |
| Snapshot failure | A five-second polite notice says `Snapshot failed`; the prior DOM remains if one existed. | Prior content is not marked stale or timestamped; initial failure leaves misleading placeholders. No persistent retry/recovery block. |
| Action success | Polite five-second notice, then full snapshot reload. | No action/receipt reference, focus return, or explicit fresh snapshot indicator. |
| Action failure | Five-second notice using the server message. | Error uses `role=status`, not alert/error summary; stable code, survivor, recovery, and safe retry are not rendered. |
| Partial | Workflow service can return `PARTIAL_RESULT` details. | Client discards structured details and shows only message; surviving run/backup and recovery are invisible. |
| Stale/retryable/blocked | Some labels use `warning`, `review`, missing, expired, stopped, or unavailable. | No normalized stale, retryable, blocked, permission-denied, offline, or last-success/attempt treatment. |
| Unknown/observed | Task cards/detail preserve provenance, ownership, confidence, source, observed time, exit `unknown`, and disable unavailable actions. | Strongest current seam, but periodic rerender/focus and missing accessible selection semantics remain. |

## Accessibility baseline and gaps

### Current positives

- A skip link targets `<main>`, global navigation has a name and active links
  receive `aria-current="page"`; `header`, `nav`, `main`, `section`, and two
  labelled `aside` landmarks are present.
- Native buttons, forms, `details`/`summary`, labels, select controls, and browser
  confirmation dialogs provide a usable keyboard baseline. CSS defines visible
  focus treatment, reduced motion, system/light/dark themes, and wrapping for
  many long values.
- The notice and several changing panels are polite live regions. Decorative
  activity markers are hidden from accessibility APIs.

### Required gaps

- `/settings` has no visible `h1`; heading levels and one-`h1` behavior need
  route-by-route tests rather than relying on CSS-hidden shared markup.
- Project/task buttons use only CSS `active`/`selected`; they do not expose
  `aria-current`, `aria-pressed`, or `aria-selected` semantics. Task selection
  does not move or announce focus to detail.
- `render()` replaces project/task/control DOM every 15 seconds. A focused
  descendant can disappear, violating the target requirement that refresh and
  resize preserve focus/reading position. Action completion has no deterministic
  focus return.
- A failure is announced through polite `role=status` and disappears after five
  seconds. Forms have no programmatically associated field errors or focusable
  error summary.
- Dynamically updated containers do not use consistent `aria-busy`; counts and
  status changes may be too noisy or completely unannounced depending on panel.
- Small controls are common (for example 4–8 px vertical padding and 9 px text in
  session/Secret actions). There is no verified 44×44 CSS px narrow-screen
  target or 16 px body/form-text floor.
- Ellipsized project, row, pane, and Secret-reference labels sometimes preserve
  a full `title`, sometimes not. Full accessible names/copy behavior for long
  IDs and paths is not systematic.
- No automated landmark/name/heading, contrast, high-contrast, 200% zoom, focus
  order, focus retention, or screen-reader announcement test exists.

## Responsive baseline and required viewport checks

Current CSS changes the dashboard at 1050 px and 720 px (the Guide also uses
1180 px and 780 px). The target spec uses wide `>=1180`, compact `720–1179`, and
narrow `<720`, so the existing dashboard does not yet share one explicit
breakpoint contract.

| Viewport | Current expected layout | Unverified risks and required test |
|---|---|---|
| 1280 px | Three columns for Projects (260 px sidebar, flexible content with 480 px minimum, 300 px detail); route-specific Overview/Settings use bounded single content. | Screenshot plus DOM overflow assertion; primary actions/risk labels visible with detail open; long path/ID, 200% zoom, and no body horizontal overflow. |
| 768 px | 1050 rule produces 220 px sidebar + main, with detail moved below both; two-column internal grids collapse. Top navigation remains horizontally scrollable. | Keyboard traversal across nav/sidebar/main/detail, discoverability without hover, explicit detail close/return plan, focus retention on refresh/resize, overflow with long localized/status text. |
| 360 px | 720 rule makes the layout block; sidebar precedes content, project list becomes horizontal scroll, panels/cards/forms become one column, and topbar wraps. | Target semantic order is not met (`page title/status -> primary action -> filters -> content -> detail`); verify 44 px targets, 16 px text, no body overflow, in-viewport confirmations, accessible horizontal navigation, and no content/focus loss. |

The in-repository automated suite does not launch a browser. Go handler/service
tests and Node `vm` tests cover routes/assets, security, typed actions, theme,
Guide behavior, and Context escaping/state distinctions. The documentation
mentions a release browser smoke, but there is no checked-in executable harness
or result covering 360/768/1280, zoom, focus, landmarks, or overflow.

## Incremental frontend seams

These seams preserve the embedded/no-build-system deployment and avoid a second
state owner:

1. **Route/view descriptor:** centralize route label, active navigation, visible
   regions, compatibility alias, and capability state. Server aliases remain a
   Go/backend concern; frontend descriptors must not synthesize data.
2. **Snapshot adapter:** isolate the current schema-v1 snapshot-to-view mapping.
   A future accepted Core envelope gets a separate adapter and fixtures; do not
   spread `data.foo || fallback` field guesses across render functions.
3. **Common state renderer:** one escaped component contract for loading,
   refreshing, empty, stale, partial, retryable, blocked, unknown, unavailable,
   offline, permission denied, success, and failure, with timestamps, recovery,
   live-region policy, and capability-gated action slots.
4. **Selection/deep-link controller:** keep selected IDs, URL synchronization,
   history, focus return, and revalidation separate from project/task markup.
   Adopt only IDs and URL parameters accepted by the backend contract.
5. **Action adapter:** retain the v1 serialized queue and exact payloads behind a
   compatibility adapter. Add preview/apply/reconcile only from the accepted S3
   application service and expose stable error/outcome fields without dumping
   arbitrary details.
6. **Safe render primitives:** continue escaping HTML and add explicit renderers
   for local-sensitive identifiers, redacted diagnostics, timestamps, status
   text, full accessible names, and copyable truncated values.
7. **Focus/responsive controller:** keyed incremental DOM updates or focus
   capture/restore around refresh, route-level `h1`/landmark tests, a closable
   compact detail pattern, and narrow semantic DOM order.

No framework migration is required to create these seams. Splitting pure
functions into additional embedded JavaScript modules is preferable to a
client-side registry or browser-owned domain cache.

## S1–S3 frontend change map

Concrete API fields remain intentionally absent. Each frontend wave starts from
accepted capabilities/fixtures rather than parallel schema invention.

| Stage | Frontend-owned change | Required fixture/check | Dependency and file-owner boundary |
|---|---|---|---|
| S1 | Add the six-area navigation shell, `/settings` compatibility index behavior, System & Recovery evidence cards, common unavailable/loading/error shell, and exact recovery-command presentation; preserve Guide and v1 controls without newly promoting destructive actions. | Current v1 route/action regression; capability available/unavailable/expired and macOS experimental fixtures; keyboard access to native recovery command; 360/768/1280 screenshots/overflow; no install/repair invocation. | Planner owns wording/acceptance docs. Root/backend own profile, manifest, capability, and route alias data/handlers. Frontend owns embedded markup/styles/render modules and frontend-only fixtures; do not edit backend service/schema files concurrently. |
| S2 | Add read-only Today, Inbox, and Project skeletons; projection freshness/rebuild status; canonical-file/provenance presentation; distinct empty/stale/partial/parse-error/unavailable states. Keep v1 compatibility adapter and deep links. | Fixture matrix for clean/empty/stale/partial/corrupt/unavailable, malicious strings, personal/work context, and Secret sentinels; DOM must contain zero Secret plaintext/raw auth refs; Node pure-render checks plus browser accessibility/responsive checks. | Starts only after accepted canonical schema/query fixtures. Backend owns Markdown/O1/SQLite/query schema and redaction; frontend consumes checked fixtures and does not parse files or coin fields. |
| S3 | Implement Today/Inbox/Project/Runs closed-loop UI on the common Core action service; action preview/approval, stale-plan rejection, terminal-required exact CLI handoff, partial survivors/recovery, route/deep-link/focus compatibility. | Shared CLI/Dashboard parity corpus for plan/outcome/error/receipt; v1 endpoint/security/deep-link regression; refresh/server failure; keyboard-only, landmarks/names, focus retention, 200% zoom, and 360/768/1280 no-loss/no-overflow. | Starts after S2 and accepted `ActionPlan`/`ActionRun`. Backend owns action identity/hash/revision/reconcile and stable errors. Frontend owns interaction, rendering, and browser harness. Any unavoidable shared Go handler/test edit is scheduled serially, never as concurrent ownership. |

Suggested frontend-only files for later implementation are new render/fixture
modules under `internal/dashboard/assets/` and `internal/dashboard/testdata/`.
Existing `dashboard.go`, `internal/cli/dashboard.go`, and their service tests stay
backend-owned. Route alias changes necessarily touch the Go handler and should
be delivered by the backend owner against a frontend-authored route matrix (or
scheduled as an explicitly serialized integration task). This keeps the
planner/backend/frontend simultaneous edit overlap at zero.

## Fixture and check inventory

### Keep as compatibility regression

- Go: route/assets and schema-v1 envelope, loopback/security headers, same-origin
  token, content type, unknown/trailing fields, typed nested actions, stable IDs,
  ownership revalidation, backup/stale-set behavior, redacted workflow results,
  optional-provider snapshot survival, and argument-array backend selection.
- Node: theme default/persistence/storage failure, Guide search/Escape/current
  section, Context unavailable/unlinked/missing/unhealthy states, allowlisted
  metadata, HTML escaping, and Secret sentinel exclusion.
- Static: `node --check` for embedded scripts, template/assets embed coverage,
  local-link validation, and `git diff --check`.

### Add before S1/S2/S3 acceptance

- One versioned fixture family per accepted Core envelope with success and every
  common state, plus malformed/unknown/trailing fields and missing capability.
- Security fixtures containing Secret plaintext, `sec://` references, raw auth
  refs, prompts, transcripts, command diagnostics, user home paths, HTML/event
  attributes, bidi/long Unicode, and personal/work cross-context mismatches.
- Action fixtures for duplicate clicks, pending refresh, stale preview,
  permission denial, timeout/reconcile, partial survivor, retryable read,
  terminal handoff, action failure, and post-success focus/receipt navigation.
- A real browser harness that records 360/768/1280 and light/dark/system,
  asserts body `scrollWidth <= clientWidth`, checks 200% zoom/reflow, keyboard
  order and focus survival, one `h1`, landmarks/names/active navigation, live
  region behavior, reduced motion, minimum targets, and no console/network error.
- Shared parity fixtures consumed by CLI and Dashboard adapters; frontend asserts
  only accepted fields and exact stable outcomes, never provider-specific extras.

## Uncertainties requiring owner decisions

1. Reconcile the action request size limit (documented 16 KiB versus implemented
   16 MiB + 64 KiB).
2. Decide the duration and redirect-versus-rendered-alias behavior for `/activity`
   and `/settings`, including query/fragment preservation.
3. Assign the long-term UI owner for local credential availability and profile
   settings across Integrations versus System & Recovery; do not duplicate them.
4. Define the accepted stable identity/deep-link grammar for Project, Inbox item,
   Run, and provider-owned WorkLocation/Agent references.
5. Define which current action error details are safe to render and which require
   server-side redaction before S3 structured recovery UI.
6. Confirm whether S1 verified-manifest/recovery evidence arrives in the v1
   compatibility snapshot or a new capability query; frontend must not infer it
   from current Doctor/Profile fields.

These questions were communicated through the active Orca dispatch. Backend's
concurrent baseline independently confirms items 1, 4, and 5 remain decisions
for their named gates rather than frontend-owned schema choices. Until they are
resolved, compatibility behavior stays explicit and planned UI remains
disabled/unavailable rather than speculative.

## Non-goals

- No runtime HTML, CSS, JavaScript, Go handler, API, schema, or route was changed
  by this S0 report.
- No target v2 field, cursor, URL parameter, action payload, or provider object
  is defined here.
- No browser-side Markdown/registry parsing, new state store, service worker,
  external asset, analytics, or remote Dashboard exposure.
- No arbitrary shell/argv/path/prompt/environment action and no automatic
  install, repair, fallback double-launch, write retry, stop, delete, or history
  clearing on a new target screen.
- No claim that Orca integration, iTerm2 support, GitHub/Slack adapters,
  SQLite projection, off-device restore, or S3 parity currently exists.
