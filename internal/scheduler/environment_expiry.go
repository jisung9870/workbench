package scheduler

import (
	"context"
	"time"

	"github.com/jisung9870/workbench/internal/environments"
)

const EnvironmentExpiryJobID = "environment-expiry-scan"

type EnvironmentLister interface {
	List() ([]environments.Environment, error)
}

type EnvironmentExpiryJob struct {
	store    EnvironmentLister
	interval time.Duration
}

func NewEnvironmentExpiryJob(store EnvironmentLister, interval time.Duration) *EnvironmentExpiryJob {
	return &EnvironmentExpiryJob{store: store, interval: interval}
}

func (job *EnvironmentExpiryJob) ID() string              { return EnvironmentExpiryJobID }
func (job *EnvironmentExpiryJob) Interval() time.Duration { return job.interval }

func (job *EnvironmentExpiryJob) Run(_ context.Context, now time.Time) (Details, error) {
	items, err := job.store.List()
	if err != nil {
		return nil, err
	}
	details := Details{"total": len(items), "permanent": 0, "active": 0, "expiring": 0, "expired": 0}
	for _, item := range items {
		details[string(environments.ExpiryAt(item, now).Status)]++
	}
	return details, nil
}
