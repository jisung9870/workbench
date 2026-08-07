package scheduler

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

type Details map[string]int

type Job interface {
	ID() string
	Interval() time.Duration
	Run(context.Context, time.Time) (Details, error)
}

type JobSnapshot struct {
	ID                 string    `json:"id"`
	Status             string    `json:"status"`
	LastRunAt          time.Time `json:"last_run_at,omitempty"`
	NextRunAt          time.Time `json:"next_run_at,omitempty"`
	LastDurationMillis int64     `json:"last_duration_millis,omitempty"`
	Error              string    `json:"error,omitempty"`
	Details            Details   `json:"details,omitempty"`
}

type Snapshot struct {
	Available bool          `json:"available"`
	Running   bool          `json:"running"`
	Reason    string        `json:"reason,omitempty"`
	Jobs      []JobSnapshot `json:"jobs"`
}

type Runner struct {
	mu      sync.RWMutex
	jobs    []Job
	states  map[string]JobSnapshot
	running bool
}

func New(jobs ...Job) (*Runner, error) {
	states := make(map[string]JobSnapshot, len(jobs))
	seen := map[string]struct{}{}
	for _, job := range jobs {
		if job == nil || job.ID() == "" || job.Interval() <= 0 {
			return nil, errors.New("scheduler jobs require an ID and positive interval")
		}
		if _, exists := seen[job.ID()]; exists {
			return nil, errors.New("scheduler job IDs must be unique")
		}
		seen[job.ID()] = struct{}{}
		states[job.ID()] = JobSnapshot{ID: job.ID(), Status: "idle", Details: Details{}}
	}
	return &Runner{jobs: append([]Job(nil), jobs...), states: states}, nil
}

func Unavailable(reason string) Snapshot {
	return Snapshot{Available: false, Running: false, Reason: reason, Jobs: []JobSnapshot{}}
}

func (runner *Runner) Snapshot() Snapshot {
	runner.mu.RLock()
	defer runner.mu.RUnlock()
	jobs := make([]JobSnapshot, 0, len(runner.states))
	for _, state := range runner.states {
		state.Details = cloneDetails(state.Details)
		jobs = append(jobs, state)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	return Snapshot{Available: true, Running: runner.running, Jobs: jobs}
}

func (runner *Runner) Run(ctx context.Context) {
	runner.mu.Lock()
	if runner.running {
		runner.mu.Unlock()
		return
	}
	runner.running = true
	runner.mu.Unlock()
	defer func() {
		runner.mu.Lock()
		runner.running = false
		runner.mu.Unlock()
	}()

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-timer.C:
			runner.RunDue(ctx, now.UTC())
			timer.Reset(runner.nextDelay(time.Now().UTC()))
		}
	}
}

func (runner *Runner) RunDue(ctx context.Context, now time.Time) {
	for _, job := range runner.jobs {
		runner.mu.RLock()
		state := runner.states[job.ID()]
		due := state.NextRunAt.IsZero() || !now.Before(state.NextRunAt)
		runner.mu.RUnlock()
		if !due {
			continue
		}

		runner.mu.Lock()
		state.Status = "running"
		state.Error = ""
		runner.states[job.ID()] = state
		runner.mu.Unlock()

		started := time.Now()
		details, err := job.Run(ctx, now)
		finished := time.Now()
		state = JobSnapshot{
			ID: job.ID(), Status: "succeeded", LastRunAt: now,
			NextRunAt: now.Add(job.Interval()), LastDurationMillis: finished.Sub(started).Milliseconds(),
			Details: cloneDetails(details),
		}
		if err != nil {
			state.Status = "failed"
			state.Error = err.Error()
		}
		runner.mu.Lock()
		runner.states[job.ID()] = state
		runner.mu.Unlock()
	}
}

func (runner *Runner) nextDelay(now time.Time) time.Duration {
	runner.mu.RLock()
	defer runner.mu.RUnlock()
	var earliest time.Time
	for _, state := range runner.states {
		if state.NextRunAt.IsZero() {
			return 0
		}
		if earliest.IsZero() || state.NextRunAt.Before(earliest) {
			earliest = state.NextRunAt
		}
	}
	if earliest.IsZero() {
		return time.Hour
	}
	delay := earliest.Sub(now)
	if delay < 0 {
		return 0
	}
	return delay
}

func cloneDetails(details Details) Details {
	if details == nil {
		return Details{}
	}
	cloned := make(Details, len(details))
	for key, value := range details {
		cloned[key] = value
	}
	return cloned
}
