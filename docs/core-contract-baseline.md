# Workbench core contract baseline

> Status: S0 evidence baseline; no v2 schema or Orca mutation is implemented here.
> Evidence date: 2026-08-10 checkout, branch `orca/work`.
> Target sources: [architecture](../../plan/ARCHITECTURE.md), [Dashboard specification](../../plan/DASHBOARD-SPEC.md), and [roadmap](../../plan/IMPLEMENTATION-ROADMAP.md).
> Current public contract: [Local Dashboard](dashboard.md).

## 1. Reading rule and scope

This inventory separates current code from planned ownership. “Current” means directly evidenced by the Go implementation and tests in this checkout. “Planned” means a gate in the target documents and must not be advertised as available. The v2 route and field examples remain non-normative until the S2 schema and S3 application-service contracts are accepted.

This document freezes the S0 backend baseline and maps S1–S3 work. It does not define target v2 fields, implement a Markdown schema or SQLite projection, add an Orca adapter, transfer runtime state, or authorize provider/Orca mutation.

## 2. Current API and service ownership

### 2.1 HTTP surface

The current Dashboard handler owns a small schema-v1 HTTP boundary:

| Surface | Current contract | Current owner/evidence |
|---|---|---|
| Page routes | `GET/HEAD /`, `/projects`, `/activity`, `/settings`, `/system`; `GET/HEAD /guide` and `/docs` aliases | [`internal/dashboard/dashboard.go`](../internal/dashboard/dashboard.go), `Handler.ServeHTTP` and `dashboardPage` |
| Snapshot | `GET /api/v1/snapshot`; returns one unversioned Go `Snapshot` value inside the repository-wide schema-v1 output envelope | [`internal/dashboard/dashboard.go`](../internal/dashboard/dashboard.go), `Snapshot` and `serveSnapshot`; assembled by [`internal/cli/dashboard.go`](../internal/cli/dashboard.go), `dashboardService.Snapshot` |
| Actions | `POST /api/v1/actions`; a single `ActionRequest` union with one `action` string and allowlisted typed fields | `ActionRequest`, `serveAction`, and `dashboardService.Execute` in the same files |
| Background control | `GET /api/v1/server` and token-protected `POST /api/v1/server/stop` | [`internal/cli/server.go`](../internal/cli/server.go), `serverControlHandler` |

There is no current `/api/v2/*`, `ActionPlan`, `ActionRun`, plan hash, expected revision, idempotency key, portable receipt reference, or common application-service envelope. The HTTP handler depends on a narrow `dashboard.Service`, but its production implementation lives in `internal/cli` and directly composes project, environment, Secret, Agent, workflow, worktree, tmux, Git, Doctor, scheduler, and backend packages. CLI commands often use the same package managers, but CLI and Dashboard do not yet enter through one client-neutral query/command service. This is the central S3 parity gap.

### 2.2 Snapshot composition

`dashboardService.Snapshot` fresh-reads and combines:

- project TOML and active profile configuration;
- metadata-only environment and Secret projections;
- Agent JSON registry reconciled against its runtime;
- bounded workflow and activity JSON history;
- tmux session/window/pane observation and inferred unmanaged Tasks;
- Git-verified registered worktrees and per-project porcelain summaries;
- Doctor, backend/tool capability, overview, and scheduler snapshots.

The aggregation is rebuildable as a response, but its inputs have different authorities. A collection failure is not uniformly partial: project or Agent listing fails the whole snapshot, while environment, workflow history, worktree, workflow catalog, activity, tmux, and tool failures are generally represented as warnings or unavailable subtrees. There is no snapshot/data revision for stale-write detection.

The response currently includes `AgentRegistryPath`, project/worktree paths, task `CWD` and runtime locations, and diagnostic details on some action failures. S2/S3 redaction review must decide which paths are necessary for a first-party local client; target v2 must not copy these fields by default merely for compatibility.

## 3. Current state and runtime authority

### 3.1 Persisted state classification

The target class shown below is a migration hypothesis, not a claim that current stores already satisfy the target durability protocol.

| Current store/fact | Current authority and durability | Target class/disposition | Rebuildable now? |
|---|---|---|---|
| `config.toml`, profile TOML | Active profile selection and backend/editor policy; atomic mode-0600 replacement, profile backup | C3 portable declaration | No; authoritative config |
| `projects.toml` | Project ID/path/context/backend declarations; canonical path validation, backup, atomic replacement | C3 until S2 Project Markdown decision | No |
| `environments.toml` | Environment metadata, ordinary export values, `sec://` references and expiry; backup + atomic write | C3; values need explicit sensitivity review | No |
| `age.key`, `secrets.json.age` | Secret identity and encrypted vault; dedicated store and ciphertext backup | Secret boundary, never C1–O3 | No |
| `worktrees.json` | Workbench-managed worktree registry; fresh Git facts are separately queried; backup + atomic write | Transitional operational declaration; migrate runtime authority to Orca at S4B+, not S1–S3 | Not completely: Git can discover facts but not Workbench ownership intent |
| `agents.json` | Current Workbench Agent lifecycle registry; records starting/running/terminal transitions, backend reference, command/CWD; cross-process lock, backup + atomic write | Transitional current authority only; Orca owns target lifecycle | No; runtime observation cannot reconstruct approvals or transitions |
| `workflows.json` | Workbench workflow run registry/history, capped at 50 with five backups; can include bounded output though Dashboard projection omits it | Candidate C2/O1 evidence plus derived run view; must be split before calling SQLite rebuildable | No; cap can discard history |
| `activity.json` | Scheduler-derived transition history and last observed states, capped at 200; atomic mode-0600 write, no backup | O3 attention/history projection | Partly; old bounded events are not reproducible |
| `server.json`, `server.log` | Runtime reservation/control token and local process log; identity/PID revalidated, mode 0600 | Ephemeral runtime/diagnostic state, not canonical | Yes for service operation; history is not canonical |
| live Git/tmux/backend/Doctor/tool facts | Provider-owned, observed at snapshot time | O2 provider facts | Yes by fresh observation |
| HTTP snapshot/overview/unified Task list/scheduler counts | In-memory aggregation/derivation | O3 | Yes from available inputs |

No SQLite database, canonical Markdown object store, portable receipt journal, projection rebuild/import/export, or O1 replay protocol exists in the current Go service.

### 3.2 Runtime ownership today versus target

Today Workbench selects shell/tmux/cmux/Windows Terminal backends, ensures/adopts/stops managed tmux sessions, launches and stops Codex/Claude Agent tasks, launches allowlisted workflows, and infers unmanaged Agent-like tasks from tmux foreground commands. The target architecture deliberately changes this split:

- Orca becomes the sole owner of worktree, terminal, Agent, and Run/Task/Dispatch runtime lifecycle.
- Workbench retains long-lived personal objects and stores only opaque Orca references, observed time/confidence, and result pointers.
- tmux becomes human-only partitioning inside an Orca worktree; its foreground command is never promoted to Orca Agent authority.
- The target product has one workspace surface, Orca, on WSL and macOS. Windows Terminal, planned iTerm2,
  and cmux receive no new product integration; existing adapters remain compatibility history until a gated deprecation.

Therefore current Workbench Agent/session mutations are compatibility behavior, not evidence of target ownership. S1–S3 must preserve their v1 safety and deep links while avoiding new dependencies on this ownership model. Orca read/open/jump is gated at S4B; controlled launch is S8; stop/remove has no approved target stage in the current roadmap.

## 4. Current security and compatibility invariants

These invariants are current regression requirements:

- Listen only on `127.0.0.1`; no configurable wildcard binding.
- Use a random 32-byte per-process Dashboard action token in `X-Workbench-Token`; background stop has a separate runtime-only control token.
- When `Origin` is present, require exact HTTP scheme and request host; do not enable CORS.
- Require `application/json`, reject unknown fields and trailing JSON, and apply a bounded body reader.
- Preserve CSP, frame denial, no-referrer, no-sniff, and no-store headers; assets remain embedded and offline.
- Use action-specific field validation and fixed argument arrays. Never add arbitrary shell, path, prompt, argv, environment, force, or delete bypass fields.
- Render environment data through an allowlist: metadata, export key names, project links, expiry, and normalized Secret-reference status. Never return ordinary values, raw `sec://` references, Secret plaintext, identity path, or vault path.
- Separate managed and observed Tasks. Re-read tmux before an observed jump; observed stop fails with `TASK_UNMANAGED`. Revalidate managed ownership before stop/adopt/history prune.
- Back up then atomically replace current registries where supported; preserve surviving assets and backup details on known partial Agent/workflow paths.
- Keep `/activity`, the schema-v1 snapshot/action endpoints, current stable error codes, and current route deep links through the compatibility window. `/docs` remains a Guide alias.

One documentation/code discrepancy is now explicit: [`docs/dashboard.md`](dashboard.md) lists 14 action IDs but `dashboardService.Execute` also implements `update_secret`. Compatibility tests exercise it, so the executable baseline is 15 actions. The public table should not be silently treated as exhaustive until a later documentation owner resolves the discrepancy.

The code constant `maxActionBody` is approximately 16 MiB plus 64 KiB, while the public document says 16 KiB. Tests prove a bound and typed decoding, not the documented 16 KiB limit. S1 must resolve this by evidence before claiming the smaller limit; this S0 task does not change code.

## 5. Complete current Dashboard action migration map

Risk uses the Dashboard specification's `read|write|destructive` vocabulary. “Current owner” names the executable owner today; “canonical target owner” names the approved end-state owner.

| Current action | Current owner | Canonical target owner / target area | Risk | Status | Migration gate |
|---|---|---|---|---|---|
| `open_project` | Workbench backend registry + managed-session service; selected tmux/cmux/Windows Terminal adapter | Orca open/jump, Projects/Today; manual break-glass on unavailability | write (runtime launch) | Current v1 compatibility; target planned | Freeze v1 in S0–S3; S4B capability/version, canonical repo/path/branch revalidation and break-glass acceptance before Orca path |
| `attach_session` | tmux via Workbench session manager | Human tmux partition / terminal handoff, Projects or Runs | read/jump with runtime attach side effect | Current, only from Dashboard process already in tmux | Preserve CLI handoff; no target browser emulation. Revisit after S4B human-partition link contract |
| `adopt_session` | Workbench session manager after registered name and canonical start-path verification | Human tmux explicit link, not Agent ownership | destructive (ownership grant) | Current compatibility only | Do not re-expose in new IA until explicit S4B migration fixture proves link identity and rollback |
| `stop_session` | Workbench session manager after complete managed ownership reread | tmux human partition; Orca must not be inferred owner | destructive | Current compatibility only | Keep behind current confirmation; no S1–S3 target action. Separate post-S4B ownership/stop decision gate required |
| `update_environment` | Workbench environment TOML store | Workbench Core C3/context policy; Integrations/Projects | write | Current typed sub-operations | S2 classification/schema + cross-context negative fixtures; S3 shared plan/revision/checkpoint/receipt before new IA mutation |
| `update_profile` | Workbench config/profile store, active profile only | Workbench Core C3; System & Recovery | write | Current full replacement | S1 recovery/manifest surface is read/handoff; any mutation migration waits for S3 shared plan and stale revision rejection |
| `update_secret` | Workbench encrypted Secret store | Dedicated Secret/vault boundary; Integrations/System | write (sensitive) | Current in code/tests, absent from public action table | Preserve metadata-only response; S2 zero-leak fixtures and S3 foreground plan/revision required before re-exposure; never place value in journal/snapshot |
| `run_workflow` | Workbench allowlisted workflow manager, often tmux-backed; bounded registry/history | Workbench application service for recipe plan/receipt; runtime execution owner depends on recipe, Runs & Agents | write (execution) | Current compatibility | S3 ActionPlan/ActionRun and terminal-required handoff. Any Agent/worktree launch recipe additionally waits for S8 Orca launch gate |
| `start_agent` | Workbench Agent manager + tmux/cmux/Windows Terminal runtime | Orca sole Agent runtime owner, Runs & Agents | write (execution/launch) | Current compatibility; target forbidden in S1–S4B | Do not migrate as mutation in S1–S3. S8 only after S4B + S7C, with policy, idempotency and orphan-worker acceptance |
| `jump_agent` | Workbench Agent registry and backend runtime | Orca fresh-read open/jump using opaque ref, Runs & Agents/Projects | read/jump | Current compatibility | S4B read/open/jump gate; stale handle must be rediscovered and tmux inference excluded |
| `stop_agent` | Workbench Agent registry + backend runtime after ownership revalidation | Orca runtime, if a later explicit stop contract is accepted | destructive | Current compatibility; no approved target action | Do not add to target IA. Separate future decision gate after Orca ownership, fencing, receipt and recovery proof |
| `jump_task` | Dispatcher: workflow registry, fresh observed tmux, or Workbench Agent manager | Workbench Run result pointer or Orca/human-tmux provider jump, Runs & Agents | read/jump | Current polymorphic compatibility | S3 stable Workbench object namespaces; S4B for Orca refs. Preserve fresh observed tmux check and never merge Workbench/Orca Task IDs |
| `stop_task` | Workbench Agent manager only; workflow stop unavailable; observed tmux refused | Provider runtime owner; no generic cross-provider stop | destructive | Current managed-only compatibility | Do not make generic target action. Future provider-specific ownership gate; observed and workflow refusal remains fail-closed |
| `clear_agent_history` | Workbench Agent state store with exact terminal set, lock, stale-set rejection and backup | Workbench receipt retention policy; Runtime remains Orca-owned | destructive | Current compatibility | S2 C2/O1 retention classification and export/restore proof, then S3 plan hash/checkpoint/receipt. Never prune canonical receipt through cache UI |
| `jump_pane` | tmux adapter using stable pane ID; launcher-client context | Human tmux partition, Projects/Runs | read/jump | Current | Preserve as compatibility/handoff; S4B explicit Core link and fresh tmux evidence before new IA exposure |

No current action should be mechanically renamed into a v2 action. Each row enters the new IA only after its owner and gate are satisfied; unavailable actions remain visibly unavailable or produce an exact CLI handoff.

## 6. Gaps to S1–S3 and backend change map

### S1 — trust foundation and recovery shell

Backend scope:

- Define a read-only `SystemRecoveryQuery` boundary over existing Doctor/profile/backend/server facts plus root-owned verified manifest/checkpoint inputs. The root repository remains owner of profile selection, provisioning manifest, and aggregate restore entrypoint.
- Add portable inventory and backup/verify/restore-dry-run contracts only through established CLI ownership; Dashboard displays capability tier, manifest freshness, last checkpoint, and exact recovery command. It must not execute install/repair.
- Preserve current v1 handler/security behavior and current store backups. Resolve the body-limit documentation mismatch and inventory sensitive path fields.
- Supply fixtures for missing/expired manifest, unsupported macOS Orca, unavailable Orca/Workbench, corrupt current registry, dirty child preservation, and terminal-required handoff.

Gate: root S1 smoke/restore evidence must be accepted. S1 does not introduce the personal Markdown model, SQLite, v2 fields, or Orca mutation.

### S2 — canonical read core and projection prototype

Backend scope:

- Introduce package boundaries, not frozen wire fields: `CanonicalRepository`, `OperationalJournal`, `Projection`, `CoreQueryService`, and classification/redaction policy.
- Prototype Markdown parse/validate/stable-ID/link/rename/migrate plus portable C2 receipt/O1 checkpoint replay. Projection deletion must lose no C1–C3, C2 receipt, or O1 state.
- Make SQLite O2/O3-only by construction unless an O1 record can replay from the durable journal. Add rebuild/export/import/query-equivalence instrumentation.
- Serve read-only Today/Inbox/Project/System models through the query service; keep v1 snapshot as a compatibility adapter rather than letting new handlers parse stores directly.
- Keep target field names provisional until corruption, rename, concurrent edit, export/import, and zero-Secret fixtures pass.

Gate: clean rebuild and clean import preserve 100% stable relations; Secret sentinel appears in zero Markdown, database, search, log, snapshot, and DOM artifacts; personal/work cross-context write fails closed.

### S3 — shared application service and first closed loop

Backend scope:

- Add client-neutral `QueryService` and `CommandService` boundaries used by both CLI adapters and Dashboard adapters. HTTP/CLI translate only; domain validation, policy, execution, reconcile, and receipts live below them.
- Implement the accepted ActionPlan/ActionRun concepts with stable action identity, exact target/context/account, risk, expected revision, plan hash, approval/checkpoint, reconcile status, survivor, recovery hint, and receipt reference. Names here are concepts, not this document freezing Go/JSON fields.
- Wrap compatible v1 actions through adapters incrementally. Keep current endpoints/routes/error codes until measured compatibility removal; reject stale plans before side effects and never auto-retry an unknown write.
- Implement capture → classify → Today/Next → resume → review/receipt for Workbench canonical objects. Terminal-interactive actions receive the same plan/action ID and an exact `wb` command handoff.

Gate: CLI and Dashboard fixtures produce zero mismatch in plan hash, state transition, error/outcome, and receipt; every partial result names survivors and next action; v1 security/deep-link tests remain green.

### File-owner map

| Area | Suggested backend-owned files | Coordination constraint |
|---|---|---|
| Baseline/RFC | `docs/core-contract-baseline.md` now; later focused docs under `docs/` | Planner retains `plan/DASHBOARD-SPEC.md`; PM retains architecture/roadmap |
| Core model/journal/projection | New packages under `internal/core`, `internal/journal`, `internal/projection` after accepted design | Backend owns schema/migration; frontend consumes fixtures, does not independently freeze fields |
| Application service | New `internal/application` plus CLI/HTTP adapters | Extract domain behavior from `internal/cli/dashboard.go` without duplicating it in handlers |
| v1 compatibility | `internal/dashboard`, `internal/cli/dashboard.go`, their tests | Backend and frontend schedule non-overlapping files; embedded assets remain frontend-owned |
| S1 root manifest/profile | Root setup files and root tests | Root/planner owner; Workbench reads an agreed contract and does not rewrite root provisioning |

## 7. Proposed application-service boundaries

These are responsibility boundaries for S2/S3 design, deliberately not concrete v2 schemas:

1. `CanonicalRepository`: load/stage/validate/commit human/LLM objects and portable declarations; expose stable ID/revision and canonical file reference.
2. `OperationalJournal`: append durable action intent, approval/checkpoint, state transitions, reconcile outcome, survivors, and recovery before projecting them.
3. `Projection`: transactional rebuildable index/cache with source revision, observed time, freshness and rebuild report; never becomes sole C1–C3/O1 authority.
4. `ProviderReader`: fresh Git/file/Orca-later observations with capability, provenance, cursor/opaque reference and confidence; read never grants mutation ownership.
5. `QueryService`: client-neutral Today/Inbox/Project/Run/Integration/System views and common partial/stale/unavailable semantics.
6. `CommandService`: validate → plan → approve/handoff → execute → reconcile → receipt. It invokes explicit typed domain ports and enforces context, account, revision, ownership and idempotency.
7. Compatibility adapters: map current CLI invocations and `/api/v1/*` requests/results to accepted services without changing legacy error/deep-link behavior prematurely.

Dependency direction is adapters → application service → core policies/ports → stores/providers. HTTP and CLI must not independently parse Markdown, query SQLite, or infer provider authority.

## 8. Required fixtures and validation commands

### Fixtures to preserve from current tests

- unknown top-level and nested fields; trailing JSON; wrong token/origin/content type/method;
- loopback bind, shutdown, runtime reservation identity, stale/PID-reused server record and separate stop token;
- metadata-only environment/Secret projection with sentinel values, broken identity/store, invalid reference, missing link, and corrupt environment registry partial snapshot;
- stable tmux pane re-observation before jump, observed stop refusal, managed session adoption/stop ownership, stale Agent-history set rejection;
- Git argument-array/porcelain parsing, optional tmux/tool unavailability, allowlisted workflow action and output omission;
- atomic write, mode 0600, backup, cross-process locks, invalid transitions, partial launch/persist outcomes.

### New S1–S3 fixtures

- S1: verified/expired/mixed-run manifest; dirty child; synthetic corrupt backup; Workbench/Orca unavailable manual break-glass recovery; unsupported macOS Orca capability.
- S2: valid/minimal, unknown-field, truncated/corrupt and concurrent Markdown; stable-ID rename/link; duplicate relation; C2/O1 replay; deleted/corrupt projection; export → clean import → rebuild; personal/work crossing; Secret/path/prompt sentinels.
- S3: identical CLI/HTTP plan/apply fixture; stale revision after preview; duplicate idempotency; timeout then reconcile; partial survivor; terminal-required handoff; server restart between plan and apply; legacy v1 action/deep-link regression.

Run from `workbench/`:

```bash
go test ./...
go vet ./...
go build ./cmd/wb
```

Focused baseline checks:

```bash
go test ./internal/dashboard ./internal/cli ./internal/agents ./internal/sessions ./internal/workflows ./internal/worktrees ./internal/environments ./internal/secrets ./internal/activity
go test ./internal/dashboard -run 'TestHandler|TestListen|TestServe|TestSafeWorkflow'
go test ./internal/cli -run 'TestDashboard|TestServer|TestProjectChanges'
```

Cross-platform CI currently runs the full test, vet, and build commands on Ubuntu, macOS, and Windows. Passing mocked unit/contract tests is not native WSL/macOS restore or terminal smoke evidence.

## 9. Uncertainties and decision gates

- Should area reads first be additive views in the schema-v1 snapshot or separate routes? Decide only after S2 query/revision fixtures; do not freeze the illustrative v2 routes now.
- Which current project/environment fields migrate to Markdown C1/C3, and which remain configuration? The S2 prototype owns the answer.
- Are workflow results important receipts, operational journal entries, or disposable history by workflow kind? The current 50-record cap is incompatible with treating all of them as durable receipts.
- Which filesystem paths are essential locally, and which must be replaced by stable refs/redacted display values? Perform threat/UX review before target envelope design.
- Does the body limit intentionally permit approximately 16 MiB, or should code match the documented 16 KiB? S1 compatibility/security review must choose and test one value.
- How is an explicit human tmux partition linked to an Orca worktree without granting Agent ownership? S4B, not S1–S3, owns the stable-link fixture.
- Windows Terminal, iTerm2, and cmux have no target ownership. Preserve current compatibility facts without adding new product integration; remove only after the Orca migration/deprecation gate.
- No current accepted gate authorizes target stop/remove for Orca Agent, worktree, or generic Task. Keep those actions absent rather than extrapolating from v1.

## 10. Explicit non-goals

- No v2 endpoint, JSON field, Markdown frontmatter, SQLite schema, or migration implementation.
- No Orca adapter, lifecycle mirror, launch, stop, remove, terminal scraping, or mutation.
- No GitHub/Slack connector, provider write, webhook, plugin SDK, or external credential expansion.
- No root setup/profile/manifest edit, Dashboard asset/navigation edit, or existing v1 behavior change.
- No conversion of current Agent/workflow/activity JSON into canonical receipts by assertion.
- No deletion of current registries, no migration of user data, no commit, and no push.
