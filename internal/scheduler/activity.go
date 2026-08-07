package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/jisung9870/workbench/internal/activity"
	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/environments"
	"github.com/jisung9870/workbench/internal/workflows"
)

const ActivityScanJobID = "activity-scan"

type AgentActivityLister interface {
	List(string) ([]agents.Task, error)
}

type WorkflowActivityLister interface {
	List(string) ([]workflows.Result, error)
}

type ActivityRecorder interface {
	Observe([]activity.Observation) (int, error)
}

type ActivityScanJob struct {
	agents       AgentActivityLister
	workflows    WorkflowActivityLister
	environments EnvironmentLister
	recorder     ActivityRecorder
	interval     time.Duration
}

func NewActivityScanJob(agentStore AgentActivityLister, workflowStore WorkflowActivityLister, environmentStore EnvironmentLister, recorder ActivityRecorder, interval time.Duration) *ActivityScanJob {
	return &ActivityScanJob{agents: agentStore, workflows: workflowStore, environments: environmentStore, recorder: recorder, interval: interval}
}

func (job *ActivityScanJob) ID() string              { return ActivityScanJobID }
func (job *ActivityScanJob) Interval() time.Duration { return job.interval }

func (job *ActivityScanJob) Run(_ context.Context, now time.Time) (Details, error) {
	agentItems, err := job.agents.List("")
	if err != nil {
		return nil, fmt.Errorf("list agent activity: %w", err)
	}
	workflowItems, err := job.workflows.List("")
	if err != nil {
		return nil, fmt.Errorf("list workflow activity: %w", err)
	}
	environmentItems, err := job.environments.List()
	if err != nil {
		return nil, fmt.Errorf("list environment activity: %w", err)
	}
	observations := make([]activity.Observation, 0, len(agentItems)+len(workflowItems)+len(environmentItems))
	for _, task := range agentItems {
		occurredAt := task.LastEventAt
		if occurredAt.IsZero() {
			occurredAt = task.StartedAt
		}
		observations = append(observations, activity.Observation{
			Key: "agent:" + task.ID, Kind: "agent_state", Severity: agentSeverity(task.State),
			Title: fmt.Sprintf("Agent %s %s", task.AgentKind, task.State), ResourceID: task.ID,
			ProjectID: task.ProjectID, State: string(task.State), OccurredAt: occurredAt, EmitInitial: true,
		})
	}
	for _, result := range workflowItems {
		occurredAt := result.FinishedAt
		if occurredAt.IsZero() {
			occurredAt = result.StartedAt
		}
		observations = append(observations, activity.Observation{
			Key: "workflow:" + result.ID, Kind: "workflow_result", Severity: workflowSeverity(result.Status),
			Title: fmt.Sprintf("Workflow %s %s", result.WorkflowID, result.Status), ResourceID: result.ID,
			ProjectID: result.ProjectID, State: string(result.Status), OccurredAt: occurredAt, EmitInitial: true,
		})
	}
	for _, environment := range environmentItems {
		expiry := environments.ExpiryAt(environment, now)
		severity := "info"
		emitInitial := false
		if expiry.Status == environments.ExpiryExpiring {
			severity, emitInitial = "warning", true
		} else if expiry.Status == environments.ExpiryExpired {
			severity, emitInitial = "error", true
		}
		observations = append(observations, activity.Observation{
			Key: "environment:" + environment.ID, Kind: "environment_expiry", Severity: severity,
			Title: fmt.Sprintf("Environment %s %s", environment.ID, expiry.Status), ResourceID: environment.ID,
			State: string(expiry.Status), OccurredAt: now, EmitInitial: emitInitial,
		})
	}
	emitted, err := job.recorder.Observe(observations)
	if err != nil {
		return nil, err
	}
	return Details{"observed": len(observations), "emitted": emitted, "agents": len(agentItems), "workflows": len(workflowItems), "environments": len(environmentItems)}, nil
}

func agentSeverity(state agents.State) string {
	switch state {
	case agents.Failed:
		return "error"
	case agents.Waiting, agents.Stopped:
		return "warning"
	default:
		return "info"
	}
}

func workflowSeverity(status workflows.Status) string {
	switch status {
	case workflows.Failed, workflows.TimedOut:
		return "error"
	case workflows.Cancelled:
		return "warning"
	default:
		return "info"
	}
}
