package overview

import (
	"testing"

	binboxadapter "github.com/jisung9870/workbench/adapters/binbox"
	tmuxadapter "github.com/jisung9870/workbench/adapters/tmux"
	"github.com/jisung9870/workbench/internal/doctor"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/tasks"
	"github.com/jisung9870/workbench/internal/worktrees"
)

func TestBuildCountsOnlyGroundedStateAndCreatesWorkLocations(t *testing.T) {
	input := Input{
		Projects: []projects.Project{{ID: "alpha"}},
		Tmux: tmuxadapter.Snapshot{Available: true, Sessions: []tmuxadapter.Session{
			{ID: "$1", Name: "attached", Attached: true}, {ID: "$2", Name: "detached", Attached: false},
		}},
		Tasks: []tasks.Task{
			{ID: "managed", Kind: "codex", Provenance: tasks.ProvenanceManaged, Lifecycle: "running", ProjectID: "alpha", CWD: "/repo", CanJump: true},
			{ID: "tmux:%2", Kind: "claude", Provenance: tasks.ProvenanceObserved, Lifecycle: "running", CWD: "/tmp", CanJump: true, RuntimeLocation: tasks.RuntimeLocation{SessionName: "detached", WindowIndex: 3, PaneID: "%2"}},
			{ID: "history", Provenance: tasks.ProvenanceManaged, Lifecycle: "failed"},
		},
		Worktrees: []worktrees.Item{{ID: "dirty", Dirty: true}, {ID: "drift", ProjectID: "alpha", Drifted: true}},
		Tools:     binboxadapter.Report{Provider: "binbox", Available: true, Capabilities: []binboxadapter.Capability{}},
	}
	summary := Build(input)
	if summary.Counts.Projects != 1 || summary.Counts.TmuxSessions != 2 || summary.Counts.DetachedSessions != 1 || summary.Counts.ActiveManagedTasks != 1 || summary.Counts.ActiveObservedTasks != 1 || summary.Counts.DirtyWorktrees != 1 {
		t.Fatalf("unexpected counts: %#v", summary.Counts)
	}
	if len(summary.WorkLocations) != 2 || summary.WorkLocations[1].PaneID != "%2" || !summary.WorkLocations[1].CanJump {
		t.Fatalf("work locations were not projected: %#v", summary.WorkLocations)
	}
	if !hasAttention(summary, "tmux-session:$2", "detached_session") || !hasAttention(summary, "worktree:drift", "stale") {
		t.Fatalf("grounded attention missing: %#v", summary.Attention)
	}
}

func TestBuildReportsOptionalProviderAndVerifiedUnavailableState(t *testing.T) {
	summary := Build(Input{
		Tmux:    tmuxadapter.Snapshot{Available: false, Reason: "no server"},
		Changes: []ChangeSummary{{ProjectID: "alpha", Unavailable: "not a repository"}},
		Doctor: doctor.Report{Summary: doctor.Summary{UnavailableCore: 1}, Capabilities: []doctor.Capability{
			{Name: "config", Scope: doctor.Core, Status: doctor.Unavailable, Reason: "invalid TOML", Recovery: "fix config"},
			{Name: "project:alpha", Scope: doctor.Optional, Status: doctor.Unavailable, Reason: "path missing", Recovery: "update path"},
		}},
		Tools: binboxadapter.Report{Provider: "binbox", Available: false, Reason: "bb executable was not found", Capabilities: []binboxadapter.Capability{}},
	})
	for _, expected := range []struct{ id, kind string }{{"tmux:unavailable", "unavailable"}, {"changes:alpha", "unavailable"}, {"doctor:config", "unavailable"}, {"doctor:project:alpha", "stale"}, {"tool-health:binbox", "unavailable"}} {
		if !hasAttention(summary, expected.id, expected.kind) {
			t.Fatalf("attention %s/%s missing: %#v", expected.id, expected.kind, summary.Attention)
		}
	}
}

func hasAttention(summary Summary, id, kind string) bool {
	for _, item := range summary.Attention {
		if item.ID == id && item.Kind == kind {
			return true
		}
	}
	return false
}
