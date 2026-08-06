package tmux

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/projects"
)

type fakeExecutor struct {
	requests []backend.ProcessRequest
	lookErr  error
	results  []backend.ProcessResult
	errors   []error
}

func (executor *fakeExecutor) LookPath(name string) (string, error) {
	return "/usr/bin/" + name, executor.lookErr
}

func (executor *fakeExecutor) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	executor.requests = append(executor.requests, request)
	if len(executor.results) > 0 {
		result := executor.results[0]
		executor.results = executor.results[1:]
		var err error
		if len(executor.errors) > 0 {
			err = executor.errors[0]
			executor.errors = executor.errors[1:]
		}
		return result, err
	}
	result := backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}
	if len(request.Args) > 0 && request.Args[0] == "has-session" {
		result.ExitCode = 1
		return result, errors.New("session missing")
	}
	return result, nil
}

func TestSnapshotParsesStableTmuxHierarchy(t *testing.T) {
	sep := snapshotSeparator
	output := "$1" + sep + "zeta" + sep + "0" + sep + "@4" + sep + "2" + sep + "api" + sep + "0" + sep + "%8" + sep + "1" + sep + "0" + sep + "900" + sep + "/repo/api" + sep + "nvim\n" +
		"$0" + sep + "alpha" + sep + "1" + sep + "@2" + sep + "0" + sep + "main" + sep + "1" + sep + "%3" + sep + "0" + sep + "1" + sep + "700" + sep + "/repo" + sep + "codex\n"
	executor := &fakeExecutor{results: []backend.ProcessResult{{Stdout: output}}}
	snapshot := New(executor, func(string) string { return "" }).Snapshot(context.Background())
	if !snapshot.Available || len(snapshot.Sessions) != 2 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	alpha := snapshot.Sessions[0]
	if alpha.ID != "$0" || alpha.Name != "alpha" || !alpha.Attached || alpha.Windows[0].ID != "@2" || alpha.Windows[0].Panes[0].ID != "%3" {
		t.Fatalf("stable identifiers were not preserved: %#v", alpha)
	}
	pane := alpha.Windows[0].Panes[0]
	if pane.CurrentCommand != "codex" || pane.CurrentPath != "/repo" || pane.PID != 700 {
		t.Fatalf("pane metadata mismatch: %#v", pane)
	}
}

func TestSnapshotHandlesInterleavedRowsForSameWindow(t *testing.T) {
	sep := snapshotSeparator
	row := func(windowID, windowIndex, paneID, paneIndex string) string {
		return "$0" + sep + "alpha" + sep + "1" + sep + windowID + sep + windowIndex + sep + "window" + sep + "0" + sep + paneID + sep + paneIndex + sep + "0" + sep + "700" + sep + "/repo" + sep + "zsh"
	}
	output := strings.Join([]string{row("@2", "0", "%3", "0"), row("@3", "1", "%4", "0"), row("@2", "0", "%5", "1")}, "\n")
	snapshot := New(&fakeExecutor{results: []backend.ProcessResult{{Stdout: output}}}, nil).Snapshot(context.Background())
	if !snapshot.Available || len(snapshot.Sessions) != 1 || len(snapshot.Sessions[0].Windows) != 2 {
		t.Fatalf("unexpected interleaved snapshot: %#v", snapshot)
	}
	first := snapshot.Sessions[0].Windows[0]
	if first.ID != "@2" || len(first.Panes) != 2 || first.Panes[0].ID != "%3" || first.Panes[1].ID != "%5" {
		t.Fatalf("interleaved panes were attached to a stale window: %#v", first)
	}
}

func TestSnapshotTreatsMissingTmuxAndServerAsOptionalUnavailable(t *testing.T) {
	missing := New(&fakeExecutor{lookErr: errors.New("missing")}, nil).Snapshot(context.Background())
	if missing.Available || missing.Reason == "" || missing.Sessions == nil {
		t.Fatalf("missing tmux was not optional unavailable: %#v", missing)
	}
	server := New(&fakeExecutor{results: []backend.ProcessResult{{Stderr: "no server running"}}, errors: []error{errors.New("exit 1")}}, nil).Snapshot(context.Background())
	if server.Available || server.Reason != "no server running" {
		t.Fatalf("missing server was not optional unavailable: %#v", server)
	}
}

func TestJumpUsesValidatedStablePaneIdentifier(t *testing.T) {
	executor := &fakeExecutor{results: []backend.ProcessResult{{Stdout: "%7\x1falpha\n"}, {}}}
	adapter := New(executor, func(key string) string {
		if key == "TMUX" {
			return "/tmp/tmux,1,0"
		}
		return ""
	})
	if err := adapter.Jump(context.Background(), "%7", false); err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 2 || !reflect.DeepEqual(executor.requests[1].Args, []string{"switch-client", "-t", "%7"}) {
		t.Fatalf("unexpected jump requests: %#v", executor.requests)
	}
	if err := adapter.Jump(context.Background(), "alpha; rm -rf /", false); err == nil {
		t.Fatal("free-form pane target was accepted")
	}
}

func TestJumpOutsideTmuxRequiresExplicitInteractiveAttach(t *testing.T) {
	executor := &fakeExecutor{results: []backend.ProcessResult{{Stdout: "%9\x1falpha\n"}}}
	if err := New(executor, func(string) string { return "" }).Jump(context.Background(), "%9", false); err == nil {
		t.Fatal("non-interactive outside-tmux jump was accepted")
	}
}

func TestJumpOutsideTmuxAttachesExactSessionThenSelectsPane(t *testing.T) {
	executor := &fakeExecutor{results: []backend.ProcessResult{{Stdout: "%9\x1fproject name\n"}, {}}}
	if err := New(executor, func(string) string { return "" }).Jump(context.Background(), "%9", true); err != nil {
		t.Fatal(err)
	}
	want := backend.ProcessRequest{Name: "/usr/bin/tmux", Args: []string{"attach-session", "-t", "=project name", ";", "select-pane", "-t", "%9"}, Interactive: true}
	if len(executor.requests) != 2 || !reflect.DeepEqual(executor.requests[1], want) {
		t.Fatalf("outside attach did not preserve exact argv: %#v", executor.requests)
	}
}

func TestJumpRejectsUnverifiedSessionName(t *testing.T) {
	executor := &fakeExecutor{results: []backend.ProcessResult{{Stdout: "%9\x1falpha\nother"}}}
	if err := New(executor, func(string) string { return "" }).Jump(context.Background(), "%9", true); err == nil {
		t.Fatal("newline-bearing session verification was accepted")
	}
	if len(executor.requests) != 1 {
		t.Fatalf("attach ran after failed verification: %#v", executor.requests)
	}
}

func TestInsideTmuxCreatesMissingSessionThenSwitches(t *testing.T) {
	executor := &fakeExecutor{}
	adapter := New(executor, func(key string) string {
		if key == "TMUX" {
			return "/tmp/tmux,1,0"
		}
		return ""
	})
	path := t.TempDir()
	canonicalPath, err := projects.CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.OpenProject(context.Background(), backend.OpenRequest{Project: projects.Project{ID: "alpha", Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 3 {
		t.Fatalf("unexpected calls: %#v", executor.requests)
	}
	wantCreate := []string{"new-session", "-d", "-s", "alpha", "-c", canonicalPath}
	if !reflect.DeepEqual(executor.requests[1].Args, wantCreate) {
		t.Fatalf("unexpected create args: %v", executor.requests[1].Args)
	}
	if !reflect.DeepEqual(executor.requests[2].Args, []string{"switch-client", "-t", "=alpha"}) {
		t.Fatalf("unexpected switch args: %v", executor.requests[2].Args)
	}
	if result.Reference != "tmux:alpha" {
		t.Fatalf("unexpected reference: %s", result.Reference)
	}
}

func TestOutsideTmuxUsesAttachOrCreateArgumentArray(t *testing.T) {
	executor := &fakeExecutor{}
	adapter := New(executor, func(string) string { return "" })
	path := t.TempDir()
	canonicalPath, err := projects.CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.OpenProject(context.Background(), backend.OpenRequest{Project: projects.Project{ID: "alpha", Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"new-session", "-A", "-s", "alpha", "-c", canonicalPath}
	if len(executor.requests) != 1 || !reflect.DeepEqual(executor.requests[0].Args, want) || !executor.requests[0].Interactive {
		t.Fatalf("unexpected process request: %#v", executor.requests)
	}
}
