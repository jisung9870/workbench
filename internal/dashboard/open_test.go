package dashboard

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jisung9870/workbench/internal/backend"
)

type fakeExecutor struct {
	missing map[string]bool
	request backend.ProcessRequest
}

func (executor *fakeExecutor) LookPath(name string) (string, error) {
	if executor.missing[name] {
		return "", errors.New("missing")
	}
	return name, nil
}

func (executor *fakeExecutor) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	executor.request = request
	return backend.ProcessResult{ExitCode: 0}, nil
}

func TestOpenUsesTypedPlatformCommands(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		isWSL  bool
		target string
		miss   map[string]bool
		want   backend.ProcessRequest
	}{
		{name: "cmux", goos: "darwin", target: "cmux", want: backend.ProcessRequest{Name: "cmux", Args: []string{"browser", "open", "http://127.0.0.1:1/"}}},
		{name: "windows browser", goos: "windows", target: "browser", want: backend.ProcessRequest{Name: "rundll32", Args: []string{"url.dll,FileProtocolHandler", "http://127.0.0.1:1/"}}},
		{name: "WSL fallback browser", goos: "linux", isWSL: true, target: "browser", miss: map[string]bool{"wslview": true}, want: backend.ProcessRequest{Name: "cmd.exe", Args: []string{"/c", "start", "", "http://127.0.0.1:1/"}}},
		{name: "linux browser", goos: "linux", target: "browser", want: backend.ProcessRequest{Name: "xdg-open", Args: []string{"http://127.0.0.1:1/"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &fakeExecutor{missing: test.miss}
			if err := Open(context.Background(), executor, test.goos, test.isWSL, test.target, "http://127.0.0.1:1/"); err != nil {
				t.Fatal(err)
			}
			if executor.request.Name != test.want.Name || !reflect.DeepEqual(executor.request.Args, test.want.Args) {
				t.Fatalf("unexpected opener request: %#v", executor.request)
			}
		})
	}
}

func TestOpenNoneDoesNotResolveACommand(t *testing.T) {
	executor := &fakeExecutor{missing: map[string]bool{"cmux": true, "open": true, "xdg-open": true}}
	if err := Open(context.Background(), executor, "linux", false, "none", "http://127.0.0.1:1/"); err != nil {
		t.Fatal(err)
	}
	if executor.request.Name != "" {
		t.Fatalf("none target launched a command: %#v", executor.request)
	}
}
