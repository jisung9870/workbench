package activity

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jisung9870/workbench/internal/config"
)

func TestStoreEmitsInitialAndChangedStatesWithoutDuplicates(t *testing.T) {
	root := t.TempDir()
	store := NewStore(config.Paths{StateDir: root, ActivityFile: filepath.Join(root, "activity.json")})
	now := time.Date(2026, 8, 7, 1, 2, 3, 0, time.UTC)
	initial := Observation{Key: "agent:a", Kind: "agent_state", Severity: "info", Title: "Agent a running", ResourceID: "a", ProjectID: "p", State: "running", OccurredAt: now, EmitInitial: true}

	if emitted, err := store.Observe([]Observation{initial}); err != nil || emitted != 1 {
		t.Fatalf("initial emitted=%d err=%v", emitted, err)
	}
	if emitted, err := store.Observe([]Observation{initial}); err != nil || emitted != 0 {
		t.Fatalf("duplicate emitted=%d err=%v", emitted, err)
	}
	completed := initial
	completed.State = "completed"
	completed.Title = "Agent a completed"
	completed.OccurredAt = now.Add(time.Minute)
	if emitted, err := store.Observe([]Observation{completed}); err != nil || emitted != 1 {
		t.Fatalf("transition emitted=%d err=%v", emitted, err)
	}
	events, err := store.List()
	if err != nil || len(events) != 2 || events[0].State != "completed" || events[1].State != "running" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if events[0].ProjectID != "p" || events[0].OccurredAt.Location() != time.UTC {
		t.Fatalf("unsafe or non-normalized event=%#v", events[0])
	}
}

func TestStoreBaselinesQuietObservationsAndBoundsHistory(t *testing.T) {
	root := t.TempDir()
	store := NewStore(config.Paths{StateDir: root})
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	quiet := Observation{Key: "environment:dev", Kind: "environment_expiry", Severity: "info", Title: "Environment dev active", ResourceID: "dev", State: "active", OccurredAt: now}
	if emitted, err := store.Observe([]Observation{quiet}); err != nil || emitted != 0 {
		t.Fatalf("baseline emitted=%d err=%v", emitted, err)
	}
	observations := make([]Observation, 0, HistoryLimit+5)
	for index := 0; index < HistoryLimit+5; index++ {
		observations = append(observations, Observation{Key: "task:" + time.Unix(int64(index), 0).String(), Kind: "agent_state", Severity: "info", Title: "task", ResourceID: "task", State: "completed", OccurredAt: now.Add(time.Duration(index) * time.Second), EmitInitial: true})
	}
	if _, err := store.Observe(observations); err != nil {
		t.Fatal(err)
	}
	events, err := store.List()
	if err != nil || len(events) != HistoryLimit || !events[0].OccurredAt.Equal(now.Add((HistoryLimit+4)*time.Second)) {
		t.Fatalf("bounded events=%d first=%v err=%v", len(events), events[0].OccurredAt, err)
	}
	info, err := os.Stat(filepath.Join(root, "activity.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("activity file mode=%v err=%v", info.Mode().Perm(), err)
	}
}
