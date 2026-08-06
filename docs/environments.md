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
                [--set KEY=VALUE]... [--json]
wb env remove <id> [--json]
wb env export <id> [--json]
wb env migrate check|apply [--source <wenv.d>] [--json]
```

All JSON forms use the standard schema-v1 Workbench envelope. An environment
contains `id`, optional AWS and kube fields, and an `exports` object. Migration
responses contain the resolved source directory, `can_apply`, counts, and one
item per preset with `ready`, `existing`, `unsupported`, or `conflict` status.
`check` is read-only even when every item is ready.

`wb env export <id>` emits only POSIX-compatible, single-quoted export lines
for `AWS_PROFILE`, `AWS_REGION`, and `exports`. Keys are validated as shell
variable names and output is sorted. Kube fields are returned as
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

## Deferred Phase 5 scope

This registry has no secret type, encryption, redaction, or secret-reference
resolution. Values in `exports` are persisted as ordinary plaintext
configuration, so secret values should not be added or migrated in this slice.
Secret references/injection, kube mutation, project or Task attachment, scoped
process launch, expiry policy, and Dashboard controls remain later Phase 5 work.
