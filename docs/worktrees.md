# Worktree lifecycle and safety

`wb worktrees` treats Git's machine-readable porcelain output as the source of
truth. `${XDG_STATE_HOME:-~/.local/state}/workbench/worktrees.json` stores only
Workbench ownership, stable IDs, expected paths, branches, repository roots,
and creation timestamps.

## Create

```text
wb worktrees create <project-id> <branch> [--base <ref>]
```

The project `repo_root` must equal `git rev-parse --show-toplevel`. The branch
is validated with `git check-ref-format --branch`. If the branch already exists,
it is checked out directly and `--base` is rejected; otherwise Workbench
validates the base commit and creates the branch with `git worktree add -b`.

Before creation, Workbench rejects any porcelain record already using the same
branch. After creation, it rereads porcelain and records state only when the
path and branch match. A failure after Git successfully creates the worktree is
reported as exit 5 with the surviving path instead of silently deleting it.

## List

The main project worktree is omitted. Each linked worktree reports branch,
HEAD, path, dirty, locked, prunable, detached, managed, and drifted fields.
Externally created worktrees get a deterministic display ID and
`managed=false`; this makes them visible without granting deletion authority.

## Remove

Removal requires all of these checks immediately before mutation:

1. the ID exists in the Workbench state registry;
2. the current project repository matches the recorded repository;
3. the path is inside the managed `.worktrees/<project-id>` root;
4. current Git porcelain contains that exact path and expected branch;
5. the worktree is neither locked nor dirty.

Workbench then calls `git worktree remove` without force and verifies that the
path disappeared from porcelain before removing its state record. State writes
are atomic and retain the previous valid snapshot under `backups/`.

`--delete-branch` asks the user to type the exact branch name before any
mutation. Branch deletion happens last with `git branch -d`; if it fails because
the branch is unmerged, the completed worktree removal is reported as partial
and the branch remains recoverable.
