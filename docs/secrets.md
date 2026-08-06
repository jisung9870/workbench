# Local secrets store

`wb secrets` stores personal development tokens in a Workbench-owned, local
age v1 file. It is not a network vault and has no daemon or external service.
The implementation pins `filippo.io/age` v1.3.1 and uses one unencrypted
X25519 identity so the encrypted store remains compatible with binbox `sec`.

## Commands

```text
wb secrets init [--json]
wb secrets list [service] [--json]
wb secrets set <service> <field> [--replace] [--json]
wb secrets get <service> [field]
wb secrets remove <service> [field] [--yes] [--json]
wb secrets migrate check|apply [--json]
```

`set` reads the value from stdin. When stdin is a terminal, input is hidden.
The value is never accepted as an argument. Existing fields are not silently
overwritten. Use explicit `--replace` for token rotation; replacement still
creates a ciphertext backup before the new value is installed. Multiline values
are preserved.

`remove` asks for `y/N` confirmation on an interactive terminal. Scripts and
other non-interactive callers must state intent with `--yes`.

`list` exposes names only. JSON responses use the normal schema-v1 Workbench
envelope and contain paths, service names, field names, counts, health flags,
and backup paths only. `get` has no JSON mode; its stdout is the plaintext
contract, with diagnostics kept on stderr.

## Files and security boundary

Unix and WSL paths are:

```text
${XDG_CONFIG_HOME:-~/.config}/workbench/age.key
${XDG_CONFIG_HOME:-~/.config}/workbench/secrets.json.age
${XDG_STATE_HOME:-~/.local/state}/workbench/backups/secrets.json.age-*
```

Native Windows uses `%APPDATA%\workbench` for the identity and store and
`%LOCALAPPDATA%\workbench\backups` for backups. Unix directories are mode
`0700`; identity, store, and backups are mode `0600`. Writes create a
same-directory encrypted temporary file, flush it, rename it, and flush the
directory. Backups contain ciphertext only. Decrypted JSON and individual
values remain in process memory and are never written to a temporary file.
Mutations also hold an OS file lock, so separate Workbench processes and
terminal sessions cannot overwrite each other's read-modify-write updates.
If post-write decryption validation fails, Workbench atomically restores the
previous ciphertext backup before reporting the failure.

The encrypted plaintext deliberately retains the binbox-compatible legacy
shape:

```json
{"service":{"field":"value"}}
```

Encryption closes the age writer before installation. Reads consume the
decryption stream completely so authentication is checked, then validate the
entire JSON shape and every service/field name. Identity and store must be
regular files. On Unix, permissive file modes are rejected by both normal reads
and migration.

Back up `age.key` separately and never commit it. Losing it makes the encrypted
store unrecoverable. Anyone holding it and the encrypted store can read every
value.

## Binbox migration

The default read-only source is:

```text
${XDG_CONFIG_HOME:-~/.config}/binbox/age.key
${XDG_CONFIG_HOME:-~/.config}/binbox/secrets.json.age
```

`BINBOX_AGE_KEY` and `BINBOX_SECRETS_FILE` override those paths. `check`
validates that both are regular mode-`0600` files, the identity is exactly one
unencrypted X25519 identity, the store fully decrypts and authenticates, and
the complete JSON schema and names are valid. It reports metadata only and
changes no file.

`apply` repeats every check, refuses a destination conflict, stages and flushes
both destination files, verifies the copied store with the copied identity,
then installs them. It never deletes or edits the legacy source. This leaves a
transition period with two copies of the same private identity and ciphertext;
after exercising Workbench, retire the legacy copy manually according to your
own backup policy.

## Deliberately deferred

The first slice does not implement passphrase-encrypted identities, ASCII
armor, clipboard copy, whole-store editor integration, environment
export/injection, Dashboard rendering, or project/Task attachment. These are
separate features because each expands the plaintext exposure boundary.
