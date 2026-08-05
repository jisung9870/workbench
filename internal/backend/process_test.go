package backend

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOSExecutorPreservesOutputAndExitCode(t *testing.T) {
	request := ProcessRequest{}
	if runtime.GOOS == "windows" {
		request = ProcessRequest{Name: "cmd.exe", Args: []string{"/d", "/s", "/c", "echo stdout & echo stderr 1>&2 & exit /b 7"}}
	} else {
		request = ProcessRequest{Name: "sh", Args: []string{"-c", "printf stdout; printf stderr >&2; exit 7"}}
	}
	result, err := (&OSExecutor{}).Run(context.Background(), request)
	if err == nil {
		t.Fatal("expected non-zero process error")
	}
	if result.ExitCode != 7 || !strings.Contains(result.Stdout, "stdout") || !strings.Contains(result.Stderr, "stderr") {
		t.Fatalf("process details were lost: %#v", result)
	}
}

func TestOSExecutorReportsStartedPID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	startedPID := 0
	result, err := (&OSExecutor{}).Run(context.Background(), ProcessRequest{
		Name: "sh", Args: []string{"-c", "exit 0"},
		Started: func(pid int) error {
			startedPID = pid
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if startedPID <= 0 || result.PID != startedPID {
		t.Fatalf("started PID was not preserved: callback=%d result=%#v", startedPID, result)
	}
}

func TestOSExecutorBoundsInheritedPipesAfterContextTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell and process fixture")
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	result, err := (&OSExecutor{}).Run(ctx, ProcessRequest{
		Name: "sh",
		Args: []string{"-c", `sleep 5 & child=$!; printf '%s' "$child" >"$1"; wait`, "sh", pidFile},
	})
	duration := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline identity was lost: result=%#v err=%v", result, err)
	}
	if duration > time.Second {
		t.Fatalf("executor waited for inherited pipes: %s", duration)
	}
	stopFixtureChild(t, pidFile)
}

func TestOSExecutorBoundsInheritedPipesAfterStartedCallbackFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell and process fixture")
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	callbackErr := errors.New("recording started process failed")

	started := time.Now()
	result, err := (&OSExecutor{}).Run(context.Background(), ProcessRequest{
		Name: "sh",
		Args: []string{"-c", `sleep 5 & child=$!; printf '%s' "$child" >"$1"; wait`, "sh", pidFile},
		Started: func(int) error {
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if _, statErr := os.Stat(pidFile); statErr == nil {
					return callbackErr
				}
				time.Sleep(10 * time.Millisecond)
			}
			return errors.New("fixture child did not start")
		},
	})
	duration := time.Since(started)
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error was lost: result=%#v err=%v", result, err)
	}
	if duration > time.Second {
		t.Fatalf("executor waited for inherited pipes after callback failure: %s", duration)
	}
	stopFixtureChild(t, pidFile)
}

func stopFixtureChild(t *testing.T, pidFile string) {
	t.Helper()
	contents, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read fixture child PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid fixture child PID %q: %v", contents, err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find fixture child: %v", err)
	}
	if err := process.Kill(); err != nil && !strings.Contains(err.Error(), "process already finished") {
		t.Fatalf("kill fixture child %d: %v", pid, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("kill", "-0", strconv.Itoa(pid)).Run() != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture child %d remained after cleanup", pid)
}
