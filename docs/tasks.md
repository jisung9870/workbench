# Unified Tasks

Workbench exposes managed and observed work through one snapshot model without
giving them the same authority.

| Field | Managed | Observed |
|---|---|---|
| Provenance | `managed` | `observed` |
| State source | registry | tmux snapshot |
| Ownership | `managed` | `unmanaged` |
| Confidence | `authoritative` | `inferred` |
| Persistence | Agent registry | none |
| Jump | active backend reference | live stable pane after revalidation |
| Stop | ownership-verified runtime | unavailable |
| Exit result | recorded exit code when known | `unknown` |

The observed classifier currently recognizes only exact foreground command
names reported by `pane_current_command`: `codex`, `claude`, `omc`, and `omx`.
Shell history, pane titles, prompts, and arbitrary process guesses are not used.
When a command returns to the shell or its pane disappears, it leaves the next
snapshot. Workbench does not convert that absence into succeeded, failed, or
stopped state because tmux does not provide the exit result.

An active managed tmux pane is deduplicated by its registered stable pane ID.
The registry remains authoritative for lifecycle and stop permission. The
projection can later add explicit tool classifiers such as Terraform or Trivy
without changing this ownership boundary.
