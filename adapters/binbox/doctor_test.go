package binbox

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jisung9870/workbench/internal/backend"
)

type fakeExecutor struct {
	path    string
	lookErr error
	result  backend.ProcessResult
	runErr  error
	request backend.ProcessRequest
}

func (executor *fakeExecutor) LookPath(string) (string, error) {
	return executor.path, executor.lookErr
}
func (executor *fakeExecutor) Run(_ context.Context, request backend.ProcessRequest) (backend.ProcessResult, error) {
	executor.request = request
	return executor.result, executor.runErr
}

func TestDoctorNormalizesOfficialJSONWithoutExposingPaths(t *testing.T) {
	executor := &fakeExecutor{path: "/opt/bin/bb", result: backend.ProcessResult{Stdout: `{"schema_version":1,"ok":false,"data":{"capabilities":[{"name":"git","scope":"core","description":"gx","available":true,"path":"/secret/tool/path","recovery":null},{"name":"terraform","scope":"optional","description":"tfx","available":false,"path":null,"recovery":"install terraform"}]},"warnings":[],"error":{"code":"CORE_DEPENDENCY_MISSING","message":"required dependency missing","details":{}}}`}}
	report := New(executor).Doctor(context.Background())
	if !report.Available || len(report.Capabilities) != 2 || report.Summary.Available != 1 || report.Summary.UnavailableOptional != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
	if report.Capabilities[1].Reason == "" || report.Capabilities[1].Recovery != "install terraform" {
		t.Fatalf("unavailable capability was not normalized: %#v", report.Capabilities[1])
	}
	if !reflect.DeepEqual(executor.request.Args, []string{"doctor", "--json"}) {
		t.Fatalf("unexpected command arguments: %#v", executor.request)
	}
}

func TestDoctorTreatsMissingFailureAndMalformedAsOptionalUnavailable(t *testing.T) {
	tests := []struct {
		name     string
		executor *fakeExecutor
	}{
		{name: "missing", executor: &fakeExecutor{lookErr: errors.New("missing")}},
		{name: "failed", executor: &fakeExecutor{path: "bb", result: backend.ProcessResult{ExitCode: 7, Stderr: "token=secret"}, runErr: errors.New("exit 7")}},
		{name: "malformed", executor: &fakeExecutor{path: "bb", result: backend.ProcessResult{Stdout: `{"schema_version":2}`}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := New(test.executor).Doctor(context.Background())
			if report.Available || report.Reason == "" || len(report.Capabilities) != 0 {
				t.Fatalf("failure was not optional unavailable: %#v", report)
			}
			if report.Reason == "token=secret" {
				t.Fatal("stderr leaked into health reason")
			}
		})
	}
}

func TestDoctorAcceptsNonzeroCommandWhenOfficialEnvelopeIsValid(t *testing.T) {
	executor := &fakeExecutor{path: "bb", result: backend.ProcessResult{ExitCode: 1, Stdout: `{"schema_version":1,"ok":false,"data":{"capabilities":[{"name":"docker","scope":"core","description":"dx","available":false,"path":null,"recovery":"install docker"}]},"warnings":[],"error":{"code":"CORE_DEPENDENCY_MISSING","message":"required dependency missing","details":{}}}`}, runErr: errors.New("exit 1")}
	report := New(executor).Doctor(context.Background())
	if !report.Available || report.Summary.UnavailableCore != 1 {
		t.Fatalf("valid diagnostic payload was discarded: %#v", report)
	}
}
