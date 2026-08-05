package agents

import (
	"bufio"
	"os"
	"os/exec"
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

func TestStateStorePrunesOnlySelectedProjectTerminalHistory(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(config.Paths{StateDir: root, AgentsFile: filepath.Join(root, "agents.json"), BackupsDir: filepath.Join(root, "backups")})
	now := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	for _, task := range []Task{
		{ID: "task-active", ProjectID: "alpha", AgentKind: "codex", Backend: backend.Tmux, BackendRef: "tmux:%1", State: Running, StateSource: SourceRegistry, CWD: root, StartedAt: now, LastEventAt: now},
		{ID: "task-stopped", ProjectID: "alpha", AgentKind: "codex", Backend: backend.Tmux, BackendRef: "tmux:%2", State: Stopped, StateSource: SourceRegistry, CWD: root, StartedAt: now, LastEventAt: now},
		{ID: "task-failed", ProjectID: "alpha", AgentKind: "claude", Backend: backend.Tmux, State: Failed, StateSource: SourceRegistry, CWD: root, StartedAt: now, LastEventAt: now},
		{ID: "task-newer", ProjectID: "alpha", AgentKind: "claude", Backend: backend.Tmux, BackendRef: "tmux:%4", State: Completed, StateSource: SourceRegistry, CWD: root, StartedAt: now, LastEventAt: now},
		{ID: "task-other", ProjectID: "beta", AgentKind: "codex", Backend: backend.Tmux, BackendRef: "tmux:%3", State: Completed, StateSource: SourceRegistry, CWD: root, StartedAt: now, LastEventAt: now},
	} {
		if _, err := store.Create(task); err != nil {
			t.Fatal(err)
		}
	}

	removed, backup, err := store.PruneTerminal("alpha", []string{"task-stopped", "task-failed"})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 || backup == "" {
		t.Fatalf("unexpected prune result: removed=%d backup=%q", removed, backup)
	}
	backupContents, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(backupContents), "task-stopped") || !strings.Contains(string(backupContents), "task-failed") {
		t.Fatalf("backup does not contain removed history: %s", backupContents)
	}
	tasks, err := store.List("")
	if err != nil {
		t.Fatal(err)
	}
	remaining := map[string]bool{}
	for _, task := range tasks {
		remaining[task.ID] = true
	}
	if len(tasks) != 3 || !remaining["task-active"] || !remaining["task-newer"] || !remaining["task-other"] {
		t.Fatalf("unexpected remaining tasks: %#v", tasks)
	}
	if _, found, err := store.Show("task-other"); err != nil || !found {
		t.Fatalf("another project's history was removed: found=%v err=%v", found, err)
	}
}

func TestStateStorePruneRequiresProjectAndSkipsEmptyHistory(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(config.Paths{StateDir: root, AgentsFile: filepath.Join(root, "agents.json"), BackupsDir: filepath.Join(root, "backups")})
	if _, _, err := store.PruneTerminal("", []string{"task-stopped"}); err == nil {
		t.Fatal("empty project ID was accepted")
	}
	if _, _, err := store.PruneTerminal("alpha", nil); err == nil {
		t.Fatal("empty task ID list was accepted")
	}
}

func TestStateStoresForSameRegistryShareProcessLock(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{StateDir: root, AgentsFile: filepath.Join(root, "agents.json"), BackupsDir: filepath.Join(root, "backups")}
	first := NewStateStore(paths)
	second := NewStateStore(paths)
	if first.mu != second.mu {
		t.Fatal("state stores for the same registry do not share a process lock")
	}
}

func TestStateStorePruneRejectsChangedHistory(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(config.Paths{StateDir: root, AgentsFile: filepath.Join(root, "agents.json"), BackupsDir: filepath.Join(root, "backups")})
	now := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	if _, err := store.Create(Task{ID: "task-stopped", ProjectID: "alpha", AgentKind: "codex", Backend: backend.Tmux, BackendRef: "tmux:%2", State: Stopped, StateSource: SourceRegistry, CWD: root, StartedAt: now, LastEventAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PruneTerminal("alpha", []string{"task-stopped", "task-missing"}); err == nil || !strings.Contains(err.Error(), "history changed") {
		t.Fatalf("changed history was accepted: %v", err)
	}
	if _, found, err := store.Show("task-stopped"); err != nil || !found {
		t.Fatalf("failed prune changed registry: found=%v err=%v", found, err)
	}
}

func TestRegistryLockSerializesProcessesAndReleasesOnExit(t *testing.T) {
	if lockPath := os.Getenv("WORKBENCH_TEST_AGENT_LOCK_PATH"); lockPath != "" {
		release, err := acquireRegistryFileLock(lockPath)
		if err != nil {
			os.Exit(2)
		}
		_ = release
		_, _ = os.Stdout.WriteString("locked\n")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		os.Exit(0)
	}

	lockPath := filepath.Join(t.TempDir(), "agents.json.lock")
	command := exec.Command(os.Args[0], "-test.run=^TestRegistryLockSerializesProcessesAndReleasesOnExit$")
	command.Env = append(os.Environ(), "WORKBENCH_TEST_AGENT_LOCK_PATH="+lockPath)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		t.Fatalf("lock helper did not acquire lock: %q err=%v", scanner.Text(), scanner.Err())
	}

	type lockResult struct {
		release func() error
		err     error
	}
	acquired := make(chan lockResult, 1)
	go func() {
		release, err := acquireRegistryFileLock(lockPath)
		acquired <- lockResult{release: release, err: err}
	}()
	select {
	case result := <-acquired:
		if result.release != nil {
			_ = result.release()
		}
		t.Fatalf("second process did not block on registry lock: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := stdin.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-acquired:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if err := result.release(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registry lock was not released when the owner process exited")
	}
}
