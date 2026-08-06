# Environment registry and wenv migration

Workbench owns environment metadata in
`${XDG_CONFIG_HOME:-~/.config}/workbench/environments.toml` on Unix/WSL and
`%APPDATA%\workbench\environments.toml` on native Windows. The registry is
schema version 1, is written with mode `0600`, and is replaced through the same
atomic-write boundary as the project registry. Before replacing an existing
registry, Workbench stores a mode-`0600` recovery copy in the state
`backups/` directory.

## Commands

```text
wb env list [--json]
wb env show <id> [--json]
wb env add <id> [--aws-profile <value>] [--aws-region <value>]
                [--kube-context <value>] [--kube-namespace <value>]
                [--set KEY=VALUE]...
                [--secret KEY=sec://service/field]... [--json]
wb env remove <id> [--json]
wb env health <id> [--json]
wb env export <id> [--resolve-secrets] [--json]
wb env migrate check|apply [--source <wenv.d>] [--json]
```

All JSON forms use the standard schema-v1 Workbench envelope. An environment
contains `id`, optional AWS and kube fields, an `exports` object, and a
`secrets` object whose values are references such as `sec://github/token`.
Secret reference names are metadata; resolved values never appear in list,
show, health, migration, or JSON output. Migration
responses contain the resolved source directory, `can_apply`, counts, and one
item per preset with `ready`, `existing`, `unsupported`, or `conflict` status.
`check` is read-only even when every item is ready.

`wb env health <id>` checks each reference and reports only `available`,
`missing`, or `store_unavailable`. A missing store or invalid/wrong identity is
reported as unavailable without exposing identity material or decryption
diagnostics.

By default, `wb env export <id>` emits only POSIX-compatible, single-quoted
export lines for `AWS_PROFILE`, `AWS_REGION`, and ordinary `exports`. It warns
when secret references remain unresolved. `--resolve-secrets` decrypts them in
memory and adds them to the same sorted shell output. This flag cannot be used
with `--json` and refuses a terminal stdout; use it only through command
substitution or a pipe:

```bash
eval "$(wb env export dev --resolve-secrets)"
```

Keys are validated as shell variable names. A secret key cannot collide with
an ordinary export or a typed AWS/kube field. Kube fields are returned as
`pending_mutations` in JSON and produce a warning in text mode; this slice does
not call `kubectl` or claim that context was changed.

## Safe wenv migration

The default source is `${BINBOX_WENV_DIR}` when set, otherwise the sibling
binbox config directory `${XDG_CONFIG_HOME:-~/.config}/binbox/wenv.d`.
Workbench reads regular, non-hidden files only and uses a purpose-built parser.
It does not start Bash, `source` a preset, or use `eval`.

The accepted subset is:

```bash
AWS_PROFILE=dev
AWS_REGION=ap-northeast-2
KUBE_CONTEXT=local-cluster
KUBE_NAMESPACE=tools
EXPORTS=(FEATURE=on "MESSAGE=hello world")
```

Blank lines, full-line comments, scalar trailing comments, single/double
quotes, and multiline `EXPORTS` are accepted. Unknown keys, duplicate keys,
command substitution, variable expansion, pipelines, redirection, globbing,
and arbitrary shell statements are reported as `unsupported`. Reserved fields
inside `EXPORTS` are rejected so they cannot silently override typed fields.

Apply first parses every source and compares every ID against the current
registry. If any item is unsupported or conflicts, no file is written. A
second plan is calculated immediately before mutation to detect source or
registry changes. Identical existing records are idempotently skipped; all
ready records are committed in one registry replacement.

## Project and workflow integration

A project can store an optional default `environment_id`. Typed workflow runs
use that default unless `--environment <id>` overrides it or
`--no-environment` explicitly disables it. Immediately before starting the
subprocess, the detached worker reloads the selected environment, preserves
the inherited process environment, and overlays `AWS_PROFILE`, `AWS_REGION`,
and ordinary `exports`. Secret references are resolved only when the run was
launched with `--resolve-secrets`; missing or invalid references and
NUL-containing values fail before project code starts. Workflow state and API
responses retain only the environment ID and the `resolve_secrets` request
intent, never resolved values or reference details.

Kubernetes context/namespace mutation, environment or credential expiry
policy, and dedicated Dashboard environment/secret controls remain deferred.
Ordinary `exports` remain plaintext configuration and must not contain secret
values.
