package backend

import (
	"context"
	"runtime"
	"strings"
	"testing"
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
