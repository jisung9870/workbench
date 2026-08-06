# Typed workflows

Workbench exposes a small compiled catalog of apply-safe project workflows. A caller selects a registered project ID and a fixed workflow ID; callers cannot provide a command, executable, argument, working directory, prompt, environment value, or secret. Tests still execute repository-controlled code with the user's permissions, and Terraform plan writes a local plan file, so the Dashboard confirmation includes each action's risk label.

| Workflow ID | Detection | Fixed execution | Side effect boundary |
|---|---|---|---|
| `project.test` | root `tests/contract-test.sh` or `go.mod` | Bash root-only contract or `go test ./...` | executes repository-controlled test code; it may mutate local files |
| `security.scan` | `bb` and Trivy are installed | `bb tvx ci repo .` | read-only local repository scan; HIGH/CRITICAL findings fail the run |
| `terraform.plan` | root-level `.tf`, `bb`, and Terraform | `bb tfx plan -input=false -no-color` | may refresh providers/state and writes the local plan file; never applies it |

Detection is intentionally shallow. Workbench does not recursively choose a nested project or accept a browser-supplied path. Register a child repository separately when it needs its own workflow surface.

`terraform apply`, `terraform destroy`, state mutation, cache cleanup, force-stop operations, and secret plaintext operations are absent from the catalog.

## CLI

```bash
wb workflows catalog --project <project-id>
wb workflows run project.test --project <project-id>
wb workflows run project.test --project <project-id> --environment <environment-id>
wb workflows run project.test --project <project-id> --no-environment
wb workflows run project.test --project <project-id> --resolve-secrets
wb workflows history --project <project-id>
wb workflows show <run-id> # metadata only; output remains in the terminal pane
```

Every read command supports `--json`; `run` also supports `--json`. JSON uses the standard schema-v1 envelope.

## Detached terminal lifecycle

- The registered project path is loaded and canonicalized immediately before every run.
- A run uses the project's default `environment_id`, unless `--environment` overrides it or `--no-environment` explicitly disables it. `--resolve-secrets` is opt-in and requires an environment.
- `run` first persists `pending`, then creates or reuses the project's tmux session and starts a new `wf-<id>` window. After the stable pane reference is recorded the state becomes `starting` and the request returns; only the ownership-verified worker claim advances it to `running`.
- The tmux window starts the current Workbench executable with only `workflows worker <run-id>`. Before claiming, the worker verifies through a short bounded handshake that its `$TMUX_PANE` ownership metadata matches the run ID. It then atomically claims once, rejects replay, re-loads the allowlisted workflow and project, and canonicalizes the registry path again.
- Provider commands use an executable plus an argument array. The fixed tmux shell adapter quotes every internal worker argument; no caller command or path enters it.
- Each worker has a fixed timeout. Cancellation terminates the spawned process tree (Unix process group or Windows `taskkill /T`).
- Results persist only status, exit code when known, start/finish timestamps, duration, pane metadata, selected `environment_id`, the `resolve_secrets` request intent, and whether the in-memory capture was truncated. `resolve_secrets` does not claim that a process received secrets; worker-side validation can still fail before process start.
- Raw stdout/stderr is never serialized into history, backup, CLI JSON, or Dashboard responses. Environment and secret values are never added to the record.
- Ordinary environment-only stdout/stderr streams live to the tmux pane. Runs with secret injection use bounded capture and only emit output after exact known secret values are redacted, so chunk boundaries cannot expose those exact values. Ctrl-C or SIGTERM cancels the worker context and its process tree.
- The worker re-loads the selected environment and secrets immediately before process start. Missing or changed references and NUL-containing values fail without starting project code. AWS profile/region and ordinary exports are merged into the inherited process environment; Kubernetes context/namespace mutation remains out of scope.
- Redaction is an exact-value terminal safeguard, not a sandbox. Repository-controlled code can transform or encode a secret, write it to a file, or send it over the network; those channels are outside Workbench's enforcement boundary.
- Dashboard and CLI history expose metadata only. Workflow output exists only in the live tmux terminal pane.
- The newest 50 results are stored in the Workbench state directory. Updates use a mode-0600 atomic replacement and cross-process lock; workflow backups are pruned to the newest five.
- Missing tools and inapplicable project shapes are reported as `unavailable` or `skipped`; they are not presented as successful runs.

The Dashboard shows the catalog and applicability status for the selected registered project, enables only available workflow buttons, requires confirmation before starting one, and displays recent bounded results. Active workflow runs also appear as authoritative managed Tasks with a verified tmux pane jump. Stop remains unavailable: the UI directs the user back to the terminal instead of claiming safe cancellation ownership. Its existing loopback, same-origin token, JSON body limit, and unknown-field rejection apply unchanged.
