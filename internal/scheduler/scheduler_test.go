package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jisung9870/workbench/internal/activity"
	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/environments"
	"github.com/jisung9870/workbench/internal/workflows"
)

type fakeJob struct {
	id       string
	interval time.Duration
	calls    int
	err      error
}

type fakeAgentActivityLister struct{ items []agents.Task }

func (lister fakeAgentActivityLister) List(string) ([]agents.Task, error) { return lister.items, nil }

type fakeWorkflowActivityLister struct{ items []workflows.Result }

func (lister fakeWorkflowActivityLister) List(string) ([]workflows.Result, error) {
	return lister.items, nil
}

type fakeActivityRecorder struct{ observations []activity.Observation }

func (recorder *fakeActivityRecorder) Observe(observations []activity.Observation) (int, error) {
	recorder.observations = append([]activity.Observation(nil), observations...)
	return len(observations), nil
}

func (job *fakeJob) ID() string              { return job.id }
func (job *fakeJob) Interval() time.Duration { return job.interval }
func (job *fakeJob) Run(context.Context, time.Time) (Details, error) {
	job.calls++
	return Details{"calls": job.calls}, job.err
}

type fakeEnvironmentLister struct {
	items []environments.Environment
	err   error
}

func (lister fakeEnvironmentLister) List() ([]environments.Environment, error) {
	return lister.items, lister.err
}

func TestRunnerRunsDueJobsWithoutOverlapAndRecordsState(t *testing.T) {
	job := &fakeJob{id: "scan", interval: time.Minute}
	runner, err := New(job)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	runner.RunDue(context.Background(), now)
	runner.RunDue(context.Background(), now.Add(30*time.Second))
	if job.calls != 1 {
		t.Fatalf("job calls=%d", job.calls)
	}
	snapshot := runner.Snapshot()
	if !snapshot.Available || len(snapshot.Jobs) != 1 || snapshot.Jobs[0].Status != "succeeded" || snapshot.Jobs[0].Details["calls"] != 1 || !snapshot.Jobs[0].NextRunAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	job.err = errors.New("scan failed")
	runner.RunDue(context.Background(), now.Add(time.Minute))
	if snapshot = runner.Snapshot(); snapshot.Jobs[0].Status != "failed" || snapshot.Jobs[0].Error != "scan failed" {
		t.Fatalf("failed snapshot=%#v", snapshot)
	}
}

func TestEnvironmentExpiryJobCountsDerivedStates(t *testing.T) {
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	active := now.Add(48 * time.Hour)
	expiring := now.Add(time.Hour)
	expired := now.Add(-time.Second)
	job := NewEnvironmentExpiryJob(fakeEnvironmentLister{items: []environments.Environment{
		{ID: "permanent"}, {ID: "active", ExpiresAt: &active}, {ID: "expiring", ExpiresAt: &expiring}, {ID: "expired", ExpiresAt: &expired},
	}}, time.Minute)
	details, err := job.Run(context.Background(), now)
	if err != nil || details["total"] != 4 || details["permanent"] != 1 || details["active"] != 1 || details["expiring"] != 1 || details["expired"] != 1 {
		t.Fatalf("details=%#v err=%v", details, err)
	}
}

func TestActivityScanJobBuildsMetadataOnlyObservations(t *testing.T) {
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)
	recorder := &fakeActivityRecorder{}
	job := NewActivityScanJob(
		fakeAgentActivityLister{items: []agents.Task{{ID: "agent-1", ProjectID: "project", AgentKind: "codex", State: agents.Failed, LastEventAt: now}}},
		fakeWorkflowActivityLister{items: []workflows.Result{{ID: "run-1", WorkflowID: workflows.ProjectTest, ProjectID: "project", Status: workflows.Succeeded, FinishedAt: now}}},
		fakeEnvironmentLister{items: []environments.Environment{{ID: "dev", ExpiresAt: &expiresAt}}},
		recorder,
		time.Minute,
	)
	details, err := job.Run(context.Background(), now)
	if err != nil || details["observed"] != 3 || details["emitted"] != 3 {
		t.Fatalf("details=%#v err=%v", details, err)
	}
	if len(recorder.observations) != 3 {
		t.Fatalf("observations=%#v", recorder.observations)
	}
	if recorder.observations[0].Severity != "error" || recorder.observations[0].ProjectID != "project" || recorder.observations[0].State != "failed" {
		t.Fatalf("agent observation=%#v", recorder.observations[0])
	}
	if recorder.observations[2].Severity != "warning" || !recorder.observations[2].EmitInitial || recorder.observations[2].State != "expiring" {
		t.Fatalf("environment observation=%#v", recorder.observations[2])
	}
}
