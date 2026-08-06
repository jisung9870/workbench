package sessions

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/projects"
)

type fakeSession struct {
	attached int
	windows  int
	start    string
	options  map[string]string
}

type fakeExecutor struct {
	sessions map[string]*fakeSession
	calls    []backend.ProcessRequest
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{sessions: map[string]*fakeSession{}}
}

func (executor *fakeExecutor) LookPath(name string) (string, error) {
	if name != "tmux" {
		return "", fmt.Errorf("missing %s", name)
	}
	return "/usr/bin/tmux", nil
}

func (executor *fakeExecutor) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	executor.calls = append(executor.calls, request)
	result := backend.ProcessResult{Command: append([]string{request.Name}, request.Args...), ExitCode: 0}
	if len(request.Args) == 0 {
		return result, nil
	}
	switch request.Args[0] {
	case "list-sessions":
		names := make([]string, 0, len(executor.sessions))
		for name := range executor.sessions {
			names = append(names, name)
		}
		sort.Strings(names)
		rows := make([]string, 0, len(names))
		for _, name := range names {
			session := executor.sessions[name]
			rows = append(rows, fmt.Sprintf("%s\t%d\t%d", name, session.attached, session.windows))
		}
		result.Stdout = strings.Join(rows, "\n")
		if len(rows) > 0 {
			result.Stdout += "\n"
		}
	case "display-message":
		name := fakeTargetName(request.Args[3])
		session, found := executor.sessions[name]
		if !found {
			result.ExitCode = 1
			return result, errors.New("missing session")
		}
		result.Stdout = fmt.Sprintf("%d\t%d\n", session.attached, session.windows)
	case "show-options":
		name := fakeTargetName(request.Args[3])
		session, found := executor.sessions[name]
		if !found {
			result.ExitCode = 1
			return result, errors.New("missing session")
		}
		result.Stdout = session.options[request.Args[4]] + "\n"
	case "list-panes":
		name := fakeTargetName(request.Args[2])
		session, found := executor.sessions[name]
		if !found {
			result.ExitCode = 1
			return result, errors.New("missing session")
		}
		result.Stdout = session.start + "\n"
	case "has-session":
		name := fakeTargetName(request.Args[2])
		if _, found := executor.sessions[name]; !found {
			result.ExitCode = 1
			return result, errors.New("missing session")
		}
	case "new-session":
		name := argumentAfter(request.Args, "-s")
		start := argumentAfter(request.Args, "-c")
		executor.sessions[name] = &fakeSession{windows: 1, start: start, options: map[string]string{}}
	case "set-option":
		name := fakeTargetName(request.Args[2])
		session, found := executor.sessions[name]
		if !found {
			result.ExitCode = 1
			return result, errors.New("missing session")
		}
		session.options[request.Args[3]] = request.Args[4]
	case "kill-session":
		name := fakeTargetName(request.Args[2])
		if _, found := executor.sessions[name]; !found {
			result.ExitCode = 1
			return result, errors.New("missing session")
		}
		delete(executor.sessions, name)
	case "attach-session", "switch-client":
	default:
		return result, fmt.Errorf("unexpected tmux command: %v", request.Args)
	}
	return result, nil
}

func argumentAfter(args []string, option string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == option {
			return args[index+1]
		}
	}
	return ""
}

func fakeTargetName(target string) string {
	return strings.TrimSuffix(strings.TrimPrefix(target, "="), ":")
}

func testProject(t *testing.T) projects.Project {
	t.Helper()
	return projects.Project{ID: "alpha", Path: t.TempDir()}
}

func TestEnsureCreatesManagedSessionAndSetsManagedFlagLast(t *testing.T) {
	executor := newFakeExecutor()
	project := testProject(t)
	item, created, err := NewManager(executor, nil).Ensure(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if !created || !item.Managed || item.Ownership != Managed || item.ProjectID != project.ID {
		t.Fatalf("unexpected ensured session: %#v created=%v", item, created)
	}
	options := []string{}
	for _, call := range executor.calls {
		if len(call.Args) > 0 && call.Args[0] == "set-option" {
			options = append(options, call.Args[3])
		}
	}
	want := []string{ProjectIDOption, ProjectPathOption, ManagedOption}
	if !reflect.DeepEqual(options, want) {
		t.Fatalf("ownership metadata order is unsafe: got=%v want=%v", options, want)
	}
}

func TestEnsurePreservesLegacySessionWithoutAdopting(t *testing.T) {
	executor := newFakeExecutor()
	project := testProject(t)
	executor.sessions[project.ID] = &fakeSession{windows: 1, start: project.Path, options: map[string]string{}}
	item, created, err := NewManager(executor, nil).Ensure(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if created || item.Ownership != Legacy || item.Managed {
		t.Fatalf("legacy session changed during ensure: %#v created=%v", item, created)
	}
	for _, call := range executor.calls {
		if len(call.Args) > 0 && call.Args[0] == "set-option" {
			t.Fatal("legacy session was adopted implicitly")
		}
	}
}

func TestAdoptRequiresMatchingStartPath(t *testing.T) {
	executor := newFakeExecutor()
	project := testProject(t)
	executor.sessions[project.ID] = &fakeSession{windows: 1, start: project.Path, options: map[string]string{}}
	item, changed, err := NewManager(executor, nil).Adopt(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !item.Managed || item.ProjectPath == "" {
		t.Fatalf("legacy session was not adopted: %#v changed=%v", item, changed)
	}
}

func TestAdoptRejectsDifferentStartPathWithoutMutation(t *testing.T) {
	executor := newFakeExecutor()
	project := testProject(t)
	executor.sessions[project.ID] = &fakeSession{windows: 1, start: t.TempDir(), options: map[string]string{}}
	_, _, err := NewManager(executor, nil).Adopt(context.Background(), project)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("path mismatch was not a conflict: %v", err)
	}
	if len(executor.sessions[project.ID].options) != 0 {
		t.Fatal("path mismatch changed ownership metadata")
	}
}

func TestStopRejectsLegacyAndKillsOnlyMatchingManagedSession(t *testing.T) {
	project := testProject(t)
	t.Run("legacy", func(t *testing.T) {
		executor := newFakeExecutor()
		executor.sessions[project.ID] = &fakeSession{windows: 1, start: project.Path, options: map[string]string{}}
		_, err := NewManager(executor, nil).Stop(context.Background(), project)
		var conflict *ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("legacy stop was not rejected: %v", err)
		}
		if _, found := executor.sessions[project.ID]; !found {
			t.Fatal("legacy session was killed")
		}
	})
	t.Run("managed", func(t *testing.T) {
		executor := newFakeExecutor()
		executor.sessions[project.ID] = &fakeSession{
			windows: 1, start: project.Path,
			options: map[string]string{ManagedOption: "1", ProjectIDOption: project.ID, ProjectPathOption: project.Path},
		}
		item, err := NewManager(executor, nil).Stop(context.Background(), project)
		if err != nil {
			t.Fatal(err)
		}
		if !item.Managed {
			t.Fatalf("unexpected stopped item: %#v", item)
		}
		if _, found := executor.sessions[project.ID]; found {
			t.Fatal("managed session still exists")
		}
	})
}

func TestStopRejectsManagedMetadataForDifferentPath(t *testing.T) {
	executor := newFakeExecutor()
	project := testProject(t)
	executor.sessions[project.ID] = &fakeSession{
		windows: 1, start: project.Path,
		options: map[string]string{ManagedOption: "1", ProjectIDOption: project.ID, ProjectPathOption: t.TempDir()},
	}
	_, err := NewManager(executor, nil).Stop(context.Background(), project)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("ownership mismatch was not rejected: %v", err)
	}
	if _, found := executor.sessions[project.ID]; !found {
		t.Fatal("mismatched managed session was killed")
	}
}

func TestJumpUsesCurrentClientOrAttachesOutsideTmux(t *testing.T) {
	tests := []struct {
		name string
		tmux string
		want string
	}{
		{name: "inside", tmux: "/tmp/tmux,1,0", want: "switch-client"},
		{name: "outside", want: "attach-session"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := newFakeExecutor()
			executor.sessions["alpha"] = &fakeSession{windows: 1, start: "/tmp", options: map[string]string{}}
			manager := NewManager(executor, func(key string) string {
				if key == "TMUX" {
					return test.tmux
				}
				return ""
			})
			if _, err := manager.Jump(context.Background(), "alpha"); err != nil {
				t.Fatal(err)
			}
			last := executor.calls[len(executor.calls)-1]
			if last.Args[0] != test.want || !last.Interactive {
				t.Fatalf("unexpected jump request: %#v", last)
			}
		})
	}
}

func TestListClassifiesAndSortsOwnership(t *testing.T) {
	executor := newFakeExecutor()
	executor.sessions["legacy"] = &fakeSession{windows: 1, start: "/tmp", options: map[string]string{}}
	executor.sessions["managed"] = &fakeSession{windows: 2, start: "/tmp", options: map[string]string{
		ManagedOption: "1", ProjectIDOption: "managed", ProjectPathOption: "/tmp",
	}}
	executor.sessions["foreign"] = &fakeSession{windows: 1, start: "/tmp", options: map[string]string{
		ProjectIDOption: "foreign",
	}}
	items, warnings, err := NewManager(executor, nil).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(items) != 3 {
		t.Fatalf("unexpected list: %#v warnings=%v", items, warnings)
	}
	if items[0].Name != "foreign" || items[0].Ownership != Foreign ||
		items[1].Name != "legacy" || items[1].Ownership != Legacy ||
		items[2].Name != "managed" || items[2].Ownership != Managed {
		t.Fatalf("unexpected ownership ordering: %#v", items)
	}
}
