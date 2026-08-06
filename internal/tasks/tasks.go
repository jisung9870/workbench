package tasks

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	tmuxadapter "github.com/jisung9870/workbench/adapters/tmux"
	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/workflows"
)

const (
	ProvenanceManaged       = "managed"
	ProvenanceObserved      = "observed"
	OwnershipManaged        = "managed"
	OwnershipObserved       = "unmanaged"
	ConfidenceAuthoritative = "authoritative"
	ConfidenceInferred      = "inferred"
)

type Evidence struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

type RuntimeLocation struct {
	SessionID   string `json:"session_id,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	WindowID    string `json:"window_id,omitempty"`
	WindowIndex int    `json:"window_index,omitempty"`
	PaneID      string `json:"pane_id,omitempty"`
	PaneIndex   int    `json:"pane_index,omitempty"`
	CurrentPath string `json:"current_path,omitempty"`
	PID         int    `json:"pid,omitempty"`
}

type Task struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	Provenance      string          `json:"provenance"`
	StateSource     string          `json:"state_source"`
	Ownership       string          `json:"ownership"`
	Confidence      string          `json:"confidence"`
	Lifecycle       string          `json:"lifecycle"`
	ProjectID       string          `json:"project_id,omitempty"`
	EnvironmentID   string          `json:"environment_id,omitempty"`
	RuntimeLocation RuntimeLocation `json:"runtime_location,omitempty"`
	CWD             string          `json:"cwd,omitempty"`
	LastObservedAt  *time.Time      `json:"last_observed_at,omitempty"`
	ExitCode        *int            `json:"exit_code,omitempty"`
	ExitResult      string          `json:"exit_result,omitempty"`
	Evidence        []Evidence      `json:"evidence"`
	CanJump         bool            `json:"can_jump"`
	CanStop         bool            `json:"can_stop"`
	Managed         *agents.Task    `json:"managed,omitempty"`
}

// Classify recognizes only foreground commands that tmux reports directly.
// It is intentionally small; future tool kinds should be added as explicit
// classifier cases rather than inferred from pane titles or shell history.
func Classify(command string) (kind string, ok bool) {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	name = strings.TrimPrefix(name, "-")
	switch name {
	case "codex", "claude", "omc", "omx":
		return name, true
	default:
		return "", false
	}
}

func Project(managed []agents.Task, snapshot tmuxadapter.Snapshot, projectItems []projects.Project, observedAt time.Time) []Task {
	return ProjectWithWorkflows(managed, nil, snapshot, projectItems, observedAt)
}

func ProjectWithWorkflows(managed []agents.Task, workflowRuns []workflows.Result, snapshot tmuxadapter.Snapshot, projectItems []projects.Project, observedAt time.Time) []Task {
	result := make([]Task, 0, len(managed))
	ownedPanes := make(map[string]struct{}, len(managed))
	livePanes := map[string]struct{}{}
	if snapshot.Available {
		for _, session := range snapshot.Sessions {
			for _, window := range session.Windows {
				for _, pane := range window.Panes {
					if !pane.Dead {
						livePanes[pane.ID] = struct{}{}
					}
				}
			}
		}
	}
	for index := range managed {
		task := managed[index]
		paneID := task.BackendDetails["pane"]
		if paneID != "" && agents.IsActiveState(task.State) {
			ownedPanes[paneID] = struct{}{}
		}
		copy := task
		result = append(result, Task{
			ID: task.ID, Kind: task.AgentKind, Provenance: ProvenanceManaged,
			StateSource: task.StateSource, Ownership: OwnershipManaged,
			Confidence: ConfidenceAuthoritative, Lifecycle: string(task.State),
			ProjectID: task.ProjectID, CWD: task.CWD, ExitCode: task.ExitCode,
			ExitResult:      managedExitResult(task),
			RuntimeLocation: RuntimeLocation{SessionName: task.BackendDetails["session"], PaneID: paneID, CurrentPath: task.CWD, PID: task.PID},
			Evidence:        []Evidence{{Field: "registry_task_id", Value: task.ID}},
			CanJump:         agents.IsActiveState(task.State), CanStop: agents.IsActiveState(task.State), Managed: &copy,
		})
	}
	for _, run := range workflowRuns {
		active := run.Status == workflows.Pending || run.Status == workflows.Starting || run.Status == workflows.Running
		lifecycle := string(run.Status)
		canJump := active && run.PaneID != ""
		if snapshot.Available && canJump {
			if _, live := livePanes[run.PaneID]; !live {
				lifecycle, canJump = "orphaned", false
			}
		}
		result = append(result, Task{ID: run.ID, Kind: run.WorkflowID, Provenance: ProvenanceManaged, StateSource: "workflow_registry", Ownership: OwnershipManaged, Confidence: ConfidenceAuthoritative, Lifecycle: lifecycle, ProjectID: run.ProjectID, EnvironmentID: run.EnvironmentID, ExitCode: run.ExitCode, ExitResult: workflowExitResult(run), RuntimeLocation: RuntimeLocation{SessionName: run.SessionName, PaneID: run.PaneID}, Evidence: []Evidence{{Field: "workflow_id", Value: run.WorkflowID}, {Field: "stop_policy", Value: "return to terminal; Dashboard stop unavailable"}}, CanJump: canJump, CanStop: false})
		if active && run.PaneID != "" {
			ownedPanes[run.PaneID] = struct{}{}
		}
	}
	if !snapshot.Available {
		return result
	}
	observedAt = observedAt.UTC()
	for _, session := range snapshot.Sessions {
		for _, window := range session.Windows {
			for _, pane := range window.Panes {
				if _, owned := ownedPanes[pane.ID]; owned {
					continue
				}
				kind, ok := Classify(pane.CurrentCommand)
				if !ok {
					continue
				}
				result = append(result, Task{
					ID: "tmux:" + pane.ID, Kind: kind, Provenance: ProvenanceObserved,
					StateSource: "tmux", Ownership: OwnershipObserved,
					Confidence: ConfidenceInferred, Lifecycle: "running",
					ProjectID: projectForPath(pane.CurrentPath, projectItems), CWD: pane.CurrentPath,
					RuntimeLocation: RuntimeLocation{SessionID: session.ID, SessionName: session.Name, WindowID: window.ID, WindowIndex: window.Index, PaneID: pane.ID, PaneIndex: pane.Index, CurrentPath: pane.CurrentPath, PID: pane.PID},
					LastObservedAt:  &observedAt, ExitResult: "unknown",
					Evidence: []Evidence{{Field: "pane_current_command", Value: pane.CurrentCommand}, {Field: "pane_id", Value: pane.ID}},
					CanJump:  true, CanStop: false,
				})
			}
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Provenance != result[j].Provenance {
			return result[i].Provenance < result[j].Provenance
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func workflowExitResult(run workflows.Result) string {
	if run.ExitCode == nil {
		return "unknown"
	}
	if *run.ExitCode == 0 {
		return "succeeded"
	}
	return "failed"
}

func managedExitResult(task agents.Task) string {
	if task.ExitCode != nil {
		if *task.ExitCode == 0 {
			return "succeeded"
		}
		return "failed"
	}
	if task.State == agents.Stopped {
		return "stopped"
	}
	return "unknown"
}

func projectForPath(path string, items []projects.Project) string {
	clean := filepath.Clean(path)
	bestID, bestLength := "", 0
	for _, project := range items {
		root := filepath.Clean(project.Path)
		rel, err := filepath.Rel(root, clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(root) > bestLength {
			bestID, bestLength = project.ID, len(root)
		}
	}
	return bestID
}

func Find(items []Task, id string) (Task, bool) {
	for _, task := range items {
		if task.ID == id {
			return task, true
		}
	}
	return Task{}, false
}
