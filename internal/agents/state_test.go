package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
)

func TestStateStorePersistsAndValidatesTransitions(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(config.Paths{StateDir: root, AgentsFile: filepath.Join(root, "agents.json"), BackupsDir: filepath.Join(root, "backups")})
	now := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	task := Task{
		ID: "task-1", ProjectID: "alpha", AgentKind: "codex", Backend: backend.Tmux,
		State: Starting, StateSource: SourceRegistry, CWD: root, StartedAt: now, LastEventAt: now,
	}
	if _, err := store.Create(task); err != nil {
		t.Fatal(err)
	}
	running, backup, err := store.Update(task.ID, func(current *Task) error {
		current.State = Running
		current.BackendRef = "tmux:%12"
		current.BackendDetails = map[string]string{"pane": "%12", "session": "alpha"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if running.State != Running || running.BackendRef != "tmux:%12" || backup == "" {
		t.Fatalf("unexpected update: task=%#v backup=%q", running, backup)
	}
	if _, _, err := store.Update(task.ID, func(current *Task) error {
		current.State = Starting
		return nil
	}); err == nil || !strings.Contains(err.Error(), "invalid agent task transition") {
		t.Fatalf("terminal rollback was accepted: %v", err)
	}
	loaded, found, err := store.Show(task.ID)
	if err != nil || !found || loaded.State != Running {
		t.Fatalf("stored task was corrupted: %#v found=%v err=%v", loaded, found, err)
	}
}

func TestStateStoreRejectsUnmarkedScrapeAndUnknownFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agents.json")
	store := NewStateStore(config.Paths{AgentsFile: path, BackupsDir: filepath.Join(root, "backups")})
	now := time.Now().UTC()
	_, err := store.Create(Task{
		ID: "task-not-legacy", ProjectID: "alpha", AgentKind: "claude", Backend: backend.Tmux,
		BackendRef: "tmux:%1", State: Running, StateSource: SourceScrape, CWD: root,
		StartedAt: now, LastEventAt: now,
	})
	if err == nil || !strings.Contains(err.Error(), "legacy: id") {
		t.Fatalf("unmarked scrape task was accepted: %v", err)
	}
	contents := `{"schema_version":1,"tasks":[],"unexpected":true}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown registry field was accepted: %v", err)
	}
}
