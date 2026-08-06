package overview

import (
	"fmt"
	"sort"
	"strings"

	binboxadapter "github.com/jisung9870/workbench/adapters/binbox"
	tmuxadapter "github.com/jisung9870/workbench/adapters/tmux"
	"github.com/jisung9870/workbench/internal/doctor"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/tasks"
	"github.com/jisung9870/workbench/internal/worktrees"
)

type Counts struct {
	Projects              int `json:"projects"`
	TmuxSessions          int `json:"tmux_sessions"`
	AttachedSessions      int `json:"attached_sessions"`
	DetachedSessions      int `json:"detached_sessions"`
	ActiveManagedTasks    int `json:"active_managed_tasks"`
	ActiveObservedTasks   int `json:"active_observed_tasks"`
	Worktrees             int `json:"worktrees"`
	DirtyWorktrees        int `json:"dirty_worktrees"`
	UnavailableCoreChecks int `json:"unavailable_core_checks"`
}

type Attention struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	Reason    string `json:"reason"`
	Recovery  string `json:"recovery,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	PaneID    string `json:"pane_id,omitempty"`
}

type WorkLocation struct {
	TaskID      string `json:"task_id"`
	Kind        string `json:"kind"`
	Provenance  string `json:"provenance"`
	ProjectID   string `json:"project_id,omitempty"`
	Path        string `json:"path,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	WindowIndex int    `json:"window_index,omitempty"`
	PaneID      string `json:"pane_id,omitempty"`
	CanJump     bool   `json:"can_jump"`
}

type ChangeSummary struct {
	ProjectID    string   `json:"project_id"`
	Branch       string   `json:"branch"`
	Dirty        bool     `json:"dirty"`
	Changed      int      `json:"changed"`
	ChangedFiles []string `json:"changed_files"`
	Unavailable  string   `json:"unavailable,omitempty"`
}

type Summary struct {
	Counts        Counts               `json:"counts"`
	Attention     []Attention          `json:"attention"`
	WorkLocations []WorkLocation       `json:"work_locations"`
	ToolHealth    binboxadapter.Report `json:"tool_health"`
}

type Input struct {
	Projects  []projects.Project
	Tasks     []tasks.Task
	Tmux      tmuxadapter.Snapshot
	Worktrees []worktrees.Item
	Changes   []ChangeSummary
	Doctor    doctor.Report
	Tools     binboxadapter.Report
}

func Build(input Input) Summary {
	result := Summary{
		Counts:    Counts{Projects: len(input.Projects), Worktrees: len(input.Worktrees), UnavailableCoreChecks: input.Doctor.Summary.UnavailableCore + input.Tools.Summary.UnavailableCore},
		Attention: []Attention{}, WorkLocations: []WorkLocation{}, ToolHealth: input.Tools,
	}
	if input.Tmux.Available {
		result.Counts.TmuxSessions = len(input.Tmux.Sessions)
		for _, session := range input.Tmux.Sessions {
			if session.Attached {
				result.Counts.AttachedSessions++
				continue
			}
			result.Counts.DetachedSessions++
			result.Attention = append(result.Attention, Attention{
				ID: "tmux-session:" + session.ID, Kind: "detached_session", Severity: "info",
				Title: fmt.Sprintf("tmux session %s is detached", session.Name), Reason: "tmux reported no attached clients",
				Recovery: "jump to a pane in this session or attach with the existing tmux workflow",
			})
		}
	} else {
		result.Attention = append(result.Attention, Attention{
			ID: "tmux:unavailable", Kind: "unavailable", Severity: "warning", Title: "tmux observation unavailable",
			Reason: input.Tmux.Reason, Recovery: "start tmux or install it; Workbench remains optional",
		})
	}
	for _, task := range input.Tasks {
		if !activeLifecycle(task.Lifecycle) {
			continue
		}
		if task.Provenance == tasks.ProvenanceManaged {
			result.Counts.ActiveManagedTasks++
		} else if task.Provenance == tasks.ProvenanceObserved {
			result.Counts.ActiveObservedTasks++
		}
		result.WorkLocations = append(result.WorkLocations, WorkLocation{
			TaskID: task.ID, Kind: task.Kind, Provenance: task.Provenance, ProjectID: task.ProjectID,
			Path: task.CWD, SessionName: task.RuntimeLocation.SessionName, WindowIndex: task.RuntimeLocation.WindowIndex,
			PaneID: task.RuntimeLocation.PaneID, CanJump: task.CanJump,
		})
		if !task.CanJump {
			result.Attention = append(result.Attention, Attention{
				ID: "task-location:" + task.ID, Kind: "unavailable", Severity: "warning", Title: fmt.Sprintf("task %s cannot be resumed", task.ID),
				Reason: "the active task has no currently verified jump target", Recovery: "inspect the task backend from the terminal", TaskID: task.ID,
			})
		}
	}
	for _, item := range input.Worktrees {
		if item.Dirty {
			result.Counts.DirtyWorktrees++
		}
		switch {
		case item.Drifted:
			result.Attention = append(result.Attention, worktreeAttention(item, "stale", "managed worktree registry drift", "registered branch or repository no longer matches Git porcelain", "inspect the worktree and repair its Workbench registry before managed actions"))
		case item.Prunable:
			reason := item.PruneReason
			if reason == "" {
				reason = "Git reported the worktree as prunable"
			}
			result.Attention = append(result.Attention, worktreeAttention(item, "stale", "prunable worktree", reason, "review with git worktree list before pruning"))
		case item.Detached:
			result.Attention = append(result.Attention, worktreeAttention(item, "detached_worktree", "detached worktree", "Git reported a detached HEAD", "create or switch to a branch if this checkout should be retained"))
		}
	}
	for _, change := range input.Changes {
		if change.Unavailable != "" {
			result.Attention = append(result.Attention, Attention{ID: "changes:" + change.ProjectID, Kind: "unavailable", Severity: "warning", Title: "repository status unavailable", Reason: change.Unavailable, Recovery: "inspect the repository from its terminal", ProjectID: change.ProjectID})
		}
	}
	for _, capability := range input.Doctor.Capabilities {
		if capability.Status != doctor.Unavailable {
			continue
		}
		kind := "unavailable"
		if strings.HasPrefix(capability.Name, "project:") {
			kind = "stale"
		}
		if capability.Scope != doctor.Core && kind != "stale" {
			continue
		}
		projectID := ""
		if strings.HasPrefix(capability.Name, "project:") {
			projectID = strings.TrimPrefix(capability.Name, "project:")
		}
		result.Attention = append(result.Attention, Attention{ID: "doctor:" + capability.Name, Kind: kind, Severity: "warning", Title: capability.Name + " unavailable", Reason: capability.Reason, Recovery: capability.Recovery, ProjectID: projectID})
	}
	if !input.Tools.Available {
		result.Attention = append(result.Attention, Attention{ID: "tool-health:binbox", Kind: "unavailable", Severity: "warning", Title: "binbox health unavailable", Reason: input.Tools.Reason, Recovery: "install or link bb, then run bb doctor --json"})
	} else if input.Tools.Summary.UnavailableCore > 0 {
		result.Attention = append(result.Attention, Attention{ID: "tool-health:binbox-core", Kind: "unavailable", Severity: "warning", Title: "binbox core tools unavailable", Reason: fmt.Sprintf("bb doctor reported %d unavailable core tools", input.Tools.Summary.UnavailableCore), Recovery: "follow the per-tool recovery guidance"})
	}
	sort.SliceStable(result.Attention, func(i, j int) bool { return result.Attention[i].ID < result.Attention[j].ID })
	sort.SliceStable(result.WorkLocations, func(i, j int) bool { return result.WorkLocations[i].TaskID < result.WorkLocations[j].TaskID })
	return result
}

func activeLifecycle(lifecycle string) bool {
	switch lifecycle {
	case "active", "starting", "running", "waiting", "idle":
		return true
	default:
		return false
	}
}

func worktreeAttention(item worktrees.Item, kind, title, reason, recovery string) Attention {
	return Attention{ID: "worktree:" + item.ID, Kind: kind, Severity: "warning", Title: title, Reason: reason, Recovery: recovery, ProjectID: item.ProjectID}
}
