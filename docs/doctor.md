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
Agent and worktree registries, Git, and the shell backend. tmux, binbox,
Neovim, Codex, and Claude are optional. cmux is skipped outside macOS; Windows
Terminal is skipped outside native Windows and WSL.

Registered project paths are reported individually as optional capabilities.
This makes a stale machine-specific path visible without making every other
registered project or core state unusable.

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
