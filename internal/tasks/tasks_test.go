package tasks

import (
	"testing"
	"time"

	tmuxadapter "github.com/jisung9870/workbench/adapters/tmux"
	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/workflows"
)

func TestClassifyUsesExplicitForegroundCommand(t *testing.T) {
	for _, command := range []string{"codex", "/opt/bin/claude", "OMC", "omx"} {
		if _, ok := Classify(command); !ok {
			t.Fatalf("expected %q to be classified", command)
		}
	}
	for _, command := range []string{"zsh", "node", "terraform", "codex-helper"} {
		if kind, ok := Classify(command); ok {
			t.Fatalf("unexpected classification %q as %q", command, kind)
		}
	}
}

func TestWorkflowRunProjectsAsManagedNonStoppableTask(t *testing.T) {
	now := time.Now().UTC()
	items := ProjectWithWorkflows(nil, []workflows.Result{{ID: "run-1", WorkflowID: workflows.SecurityScan, ProjectID: "alpha", Status: workflows.Running, PaneID: "%8", SessionName: "alpha", StartedAt: now, FinishedAt: now}}, tmuxadapter.Snapshot{Available: true}, nil, now)
	task, found := Find(items, "run-1")
	if !found || task.Provenance != ProvenanceManaged || task.Ownership != OwnershipManaged || !task.CanJump || task.CanStop || task.RuntimeLocation.PaneID != "%8" {
		t.Fatalf("workflow task contract mismatch: %#v", task)
	}
}

func TestProjectMergesManagedAndObservedWithoutDuplicatePane(t *testing.T) {
	now := time.Date(2026, 8, 6, 2, 0, 0, 0, time.UTC)
	managed := []agents.Task{{
		ID: "task-1", ProjectID: "alpha", AgentKind: "codex", Backend: backend.Tmux,
		BackendDetails: map[string]string{"pane": "%1"}, State: agents.Running,
		StateSource: agents.SourceRegistry, CWD: "/repo", StartedAt: now, LastEventAt: now,
	}}
	snapshot := tmuxadapter.Snapshot{Available: true, Sessions: []tmuxadapter.Session{{ID: "$1", Name: "alpha", Windows: []tmuxadapter.Window{{ID: "@1", Panes: []tmuxadapter.Pane{
		{ID: "%1", CurrentPath: "/repo", CurrentCommand: "codex"},
		{ID: "%2", CurrentPath: "/repo/sub", CurrentCommand: "claude", PID: 22},
	}}}}}}
	items := Project(managed, snapshot, []projects.Project{{ID: "alpha", Path: "/repo"}}, now)
	if len(items) != 2 {
		t.Fatalf("got %d tasks, want managed and observed: %#v", len(items), items)
	}
	managedTask, _ := Find(items, "task-1")
	if managedTask.Provenance != ProvenanceManaged || !managedTask.CanStop || managedTask.Confidence != ConfidenceAuthoritative {
		t.Fatalf("managed authority was lost: %#v", managedTask)
	}
	observed, found := Find(items, "tmux:%2")
	if !found || observed.Ownership != OwnershipObserved || observed.CanStop || !observed.CanJump || observed.ExitResult != "unknown" || observed.ProjectID != "alpha" {
		t.Fatalf("observed contract mismatch: %#v", observed)
	}
	if observed.RuntimeLocation.PaneID != "%2" || len(observed.Evidence) != 2 {
		t.Fatalf("observed evidence missing: %#v", observed)
	}
}

func TestProjectTreatsTmuxUnavailableAsOptional(t *testing.T) {
	items := Project(nil, tmuxadapter.Snapshot{Available: false, Reason: "no server", Sessions: []tmuxadapter.Session{}}, nil, time.Now())
	if items == nil || len(items) != 0 {
		t.Fatalf("unexpected tasks for unavailable tmux: %#v", items)
	}
}
