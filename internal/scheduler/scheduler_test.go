package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jisung9870/workbench/internal/environments"
)

type fakeJob struct {
	id       string
	interval time.Duration
	calls    int
	err      error
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
