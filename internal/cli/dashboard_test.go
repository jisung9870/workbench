package cli

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/dashboard"
	"github.com/jisung9870/workbench/internal/projects"
)

func TestDashboardRejectsInvalidOptionsBeforeListening(t *testing.T) {
	for _, args := range [][]string{
		{"dashboard", "--open", "external"},
		{"dashboard", "--port", "65536"},
		{"dashboard", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != ExitArgument {
			t.Fatalf("expected argument error for %v, got %d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestDashboardServiceRejectsUnknownAction(t *testing.T) {
	_, err := (&dashboardService{}).Execute(context.Background(), dashboard.ActionRequest{Action: "shell", ProjectID: "alpha"})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != "INVALID_ACTION" {
		t.Fatalf("unexpected action error: %v", err)
	}
}

type changeExecutor struct {
	result backend.ProcessResult
	args   []string
}

func (executor *changeExecutor) LookPath(string) (string, error) { return "git", nil }
func (executor *changeExecutor) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	executor.args = request.Args
	return executor.result, nil
}

func TestProjectChangesUsesGitArgumentArrayAndParsesPorcelain(t *testing.T) {
	executor := &changeExecutor{result: backend.ProcessResult{Stdout: "## feature/dashboard...origin/feature/dashboard\n M internal/dashboard.go\nR  old.txt -> new.txt\n?? notes.md\n"}}
	summary := projectChanges(context.Background(), executor, projects.Project{ID: "alpha", RepoRoot: "/repo with spaces"})
	wantArgs := []string{"-C", "/repo with spaces", "status", "--porcelain=v1", "--branch", "--untracked-files=normal"}
	if !reflect.DeepEqual(executor.args, wantArgs) {
		t.Fatalf("git arguments were not preserved: %v", executor.args)
	}
	if summary.Branch != "feature/dashboard" || !summary.Dirty || summary.Changed != 3 {
		t.Fatalf("unexpected change summary: %#v", summary)
	}
	if got := strings.Join(summary.ChangedFiles, ","); got != "internal/dashboard.go,new.txt,notes.md" {
		t.Fatalf("unexpected changed files: %s", got)
	}
}
