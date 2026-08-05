# Doctor capability contract

`wb doctor` is a read-only health report for Workbench core state, local tools,
and terminal providers.

```text
wb doctor [--profile <name>] [--json] [--strict]
```

## Scope and status

Every capability has independent `scope` and `status` fields.

| Scope | Meaning |
|---|---|
| `core` | Workbench state or provider required for the implemented core workflow |
| `optional` | Useful client, Agent, or terminal provider whose absence does not break core |
| `disabled` | Provider does not apply to the current platform |

| Status | Meaning |
|---|---|
| `available` | Check passed or executable/backend was detected |
| `unavailable` | Check applies but failed; `reason` and `recovery` explain it |
| `skipped` | Platform-disabled check was deliberately not required |

Core checks currently cover settings/profile validation, schema-v1 project,
Agent, worktree, and compatibility state, Git, and the shell backend. tmux, binbox,
Neovim, Codex, and Claude are optional. cmux is skipped outside macOS; Windows
Terminal is skipped outside native Windows and WSL.

Registered project paths are reported individually as optional capabilities.
This makes a stale machine-specific path visible without making every other
registered project or core state unusable.

## Compatibility readiness

Two optional capabilities summarize the latest local source observation:

- `compatibility:nvim-projects`: primary `nvim/projects/workbench`; fallbacks
  `nvim/projects/binbox` and `nvim/projects/sessionizer`.
- `compatibility:agents`: primary `workbench/agents/registry`; fallback
  `binbox/agents/scrape`.

With no observations, the capability is `skipped`. A latest primary observation
is `available`; a latest fallback observation is `unavailable`, so default
doctor remains healthy but strict doctor is not removal-ready. Observations are
stored below the Workbench state directory as five allowlisted per-tuple JSON
files. Consumer-side recording is best effort and never changes the original
project or Agent command result.

The timestamps only show the most recently observed source on this machine.
They do not prove that every workflow or machine used the primary path, and a
wall-clock rollback can distort ordering. Never delete a fallback solely from
this capability; review a representative usage period and the relevant
regression tests first.

## JSON result

JSON mode uses the common schema-v1 envelope. `data` contains:

```json
{
  "platform": "windows-wsl",
  "profile": "personal",
  "capabilities": [
    {
      "name": "backend:cmux",
      "scope": "disabled",
      "status": "skipped",
      "available": false,
      "description": "terminal backend",
      "reason": "cmux is supported only on macOS"
    }
  ],
  "summary": {
    "available": 10,
    "unavailable_core": 0,
    "unavailable_optional": 2,
    "skipped": 1
  }
}
```

An unhealthy report keeps this data and sets envelope `ok=false`. Core failures
use `CORE_CAPABILITY_UNAVAILABLE`; strict optional failures use
`OPTIONAL_CAPABILITY_UNAVAILABLE`. The error details contain the exact missing
capability names.

## Exit behavior

- default: exit 0 when all core capabilities are ready;
- `--strict`: also requires every applicable optional capability;
- invalid arguments/profile syntax: exit 2;
- platform-disabled and skipped capabilities never cause failure.

Doctor does not install packages, repair files, change profiles, start terminal
sessions, or mutate registries. Recovery strings are guidance only.
