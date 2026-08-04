package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
)

type Worktree struct {
	Path        string
	Head        string
	Branch      string
	Detached    bool
	Bare        bool
	Locked      bool
	LockReason  string
	Prunable    bool
	PruneReason string
}

type CommandError struct {
	Operation string
	Result    backend.ProcessResult
	Cause     error
}

func (err *CommandError) Error() string {
	message := strings.TrimSpace(err.Result.Stderr)
	if message == "" && err.Cause != nil {
		message = err.Cause.Error()
	}
	return fmt.Sprintf("git %s failed (exit %d): %s", err.Operation, err.Result.ExitCode, message)
}

func (err *CommandError) Unwrap() error { return err.Cause }

type Client struct {
	executor backend.Executor
}

func New(executor backend.Executor) *Client { return &Client{executor: executor} }

func (client *Client) Detect() error {
	_, err := client.executor.LookPath("git")
	if err != nil {
		return fmt.Errorf("git executable was not found: %w", err)
	}
	return nil
}

func (client *Client) TopLevel(ctx context.Context, repository string) (string, error) {
	result, err := client.run(ctx, 10*time.Second, "top-level", repository, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(result.Stdout)
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve git top-level: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func (client *Client) ListWorktrees(ctx context.Context, repository string) ([]Worktree, error) {
	result, err := client.run(ctx, 10*time.Second, "worktree list", repository, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return parsePorcelain(result.Stdout)
}

func (client *Client) ValidateBranch(ctx context.Context, repository, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	_, err := client.run(ctx, 10*time.Second, "check branch", repository, "check-ref-format", "--branch", branch)
	return err
}

func (client *Client) BranchExists(ctx context.Context, repository, branch string) (bool, error) {
	command, err := client.executor.LookPath("git")
	if err != nil {
		return false, err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, runErr := client.executor.Run(checkCtx, backend.ProcessRequest{Dir: repository, Name: command, Args: []string{"show-ref", "--verify", "--quiet", "refs/heads/" + branch}})
	if runErr == nil {
		return true, nil
	}
	if result.ExitCode == 1 {
		return false, nil
	}
	return false, &CommandError{Operation: "show-ref", Result: result, Cause: runErr}
}

func (client *Client) ValidateBase(ctx context.Context, repository, base string) error {
	if base == "" || strings.HasPrefix(base, "-") {
		return fmt.Errorf("invalid base ref %q", base)
	}
	_, err := client.run(ctx, 10*time.Second, "verify base", repository, "rev-parse", "--verify", base+"^{commit}")
	return err
}

func (client *Client) AddWorktree(ctx context.Context, repository, path, branch, base string, branchExists bool) (backend.ProcessResult, error) {
	args := []string{"worktree", "add"}
	if branchExists {
		args = append(args, path, branch)
	} else {
		args = append(args, "-b", branch, path, base)
	}
	return client.run(ctx, 2*time.Minute, "worktree add", repository, args...)
}

func (client *Client) Dirty(ctx context.Context, path string) (bool, error) {
	result, err := client.run(ctx, 15*time.Second, "status", path, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return false, err
	}
	return result.Stdout != "", nil
}

func (client *Client) RemoveWorktree(ctx context.Context, repository, path string) (backend.ProcessResult, error) {
	return client.run(ctx, 2*time.Minute, "worktree remove", repository, "worktree", "remove", path)
}

func (client *Client) DeleteBranch(ctx context.Context, repository, branch string) (backend.ProcessResult, error) {
	return client.run(ctx, 30*time.Second, "branch delete", repository, "branch", "-d", "--", branch)
}

func (client *Client) run(ctx context.Context, timeout time.Duration, operation, directory string, args ...string) (backend.ProcessResult, error) {
	command, err := client.executor.LookPath("git")
	if err != nil {
		return backend.ProcessResult{ExitCode: -1}, fmt.Errorf("git executable was not found: %w", err)
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, runErr := client.executor.Run(commandCtx, backend.ProcessRequest{Dir: directory, Name: command, Args: args})
	if runErr != nil {
		return result, &CommandError{Operation: operation, Result: result, Cause: runErr}
	}
	return result, nil
}

func parsePorcelain(output string) ([]Worktree, error) {
	records := strings.Split(output, "\x00\x00")
	worktrees := []Worktree{}
	for _, record := range records {
		if record == "" {
			continue
		}
		worktree := Worktree{}
		for _, field := range strings.Split(record, "\x00") {
			key, value, found := strings.Cut(field, " ")
			if !found {
				key = field
				value = ""
			}
			switch key {
			case "worktree":
				worktree.Path = value
			case "HEAD":
				worktree.Head = value
			case "branch":
				worktree.Branch = strings.TrimPrefix(value, "refs/heads/")
			case "detached":
				worktree.Detached = true
			case "bare":
				worktree.Bare = true
			case "locked":
				worktree.Locked = true
				worktree.LockReason = value
			case "prunable":
				worktree.Prunable = true
				worktree.PruneReason = value
			}
		}
		if worktree.Path == "" {
			return nil, fmt.Errorf("invalid git worktree porcelain record: missing path")
		}
		worktrees = append(worktrees, worktree)
	}
	return worktrees, nil
}
