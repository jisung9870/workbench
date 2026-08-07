package dashboard

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	binboxadapter "github.com/jisung9870/workbench/adapters/binbox"
	"github.com/jisung9870/workbench/internal/output"
	"github.com/jisung9870/workbench/internal/overview"
	"github.com/jisung9870/workbench/internal/tasks"
	"github.com/jisung9870/workbench/internal/workflows"
)

type fakeService struct {
	snapshot Snapshot
	actions  atomic.Int32
}

func (service *fakeService) Snapshot(context.Context) (Snapshot, error) { return service.snapshot, nil }
func (service *fakeService) Execute(_ context.Context, request ActionRequest) (ActionResult, error) {
	service.actions.Add(1)
	return ActionResult{Message: request.Action}, nil
}

func TestHandlerServesVersionedSnapshotWithSecurityHeaders(t *testing.T) {
	handler, err := NewHandler(&fakeService{snapshot: Snapshot{Platform: "linux", Profile: "personal", Agents: []AgentTask{{Lifecycle: "terminal"}}, Tasks: []tasks.Task{{ID: "tmux:%3", Kind: "codex", Provenance: "observed", Ownership: "unmanaged", Confidence: "inferred", StateSource: "tmux", Lifecycle: "running", ExitResult: "unknown"}}, Overview: overview.Summary{Counts: overview.Counts{ActiveObservedTasks: 1}, Attention: []overview.Attention{}, WorkLocations: []overview.WorkLocation{{TaskID: "tmux:%3", CanJump: true}}, ToolHealth: binboxadapter.Report{Provider: "binbox", Available: false, Reason: "bb executable was not found", Capabilities: []binboxadapter.Capability{}}}, Contexts: Contexts{RegistryAvailable: true, Environments: []ContextEnvironment{{ID: "dev", ExportKeys: []string{"FEATURE"}, ProjectIDs: []string{"alpha"}, SecretReferences: []ContextSecretReference{{Variable: "TOKEN", Status: "available"}}}}}}}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("security headers are missing or CORS was enabled: %#v", response.Header())
	}
	var envelope output.Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.SchemaVersion != 1 {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
	if !strings.Contains(response.Body.String(), `"lifecycle":"terminal"`) {
		t.Fatalf("snapshot omitted server-owned lifecycle classification: %s", response.Body.String())
	}
	for _, field := range []string{`"provenance":"observed"`, `"ownership":"unmanaged"`, `"confidence":"inferred"`, `"exit_result":"unknown"`} {
		if !strings.Contains(response.Body.String(), field) {
			t.Fatalf("snapshot omitted observed Task field %s: %s", field, response.Body.String())
		}
	}
	for _, field := range []string{`"active_observed_tasks":1`, `"work_locations"`, `"tool_health"`, `"provider":"binbox"`} {
		if !strings.Contains(response.Body.String(), field) {
			t.Fatalf("snapshot omitted overview field %s: %s", field, response.Body.String())
		}
	}
	for _, field := range []string{`"contexts"`, `"registry_available":true`, `"export_keys":["FEATURE"]`, `"variable":"TOKEN"`, `"status":"available"`} {
		if !strings.Contains(response.Body.String(), field) {
			t.Fatalf("snapshot omitted contexts field %s: %s", field, response.Body.String())
		}
	}
	for _, forbidden := range []string{`"service":`, `"field":`, `sec://`} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("snapshot contexts exposed reconstructable reference metadata %s: %s", forbidden, response.Body.String())
		}
	}
}

func TestHandlerServesDashboardGuideAndThemeAssets(t *testing.T) {
	handler, err := NewHandler(&fakeService{}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path        string
		contentType string
		contains    []string
	}{
		{path: "/", contentType: "text/html", contains: []string{`id="theme-select"`, `href="/guide"`, `/assets/theme.js`, `id="overview-heading"`, `id="overview-attention"`, `id="overview-locations"`, `id="overview-tools"`, `id="tmux-sessions"`, `id="tmux-availability"`, `id="scheduler-status"`, `id="scheduler-jobs"`, `id="unregistered-tasks"`, `id="contexts-heading"`, `id="context-status"`, `id="contexts"`, `id="agent-history"`, `id="agent-registry-path"`, `id="clear-agent-history"`, `id="workflows"`, `id="workflow-history"`, `id="task-terminal-note"`, `data-task-action="jump_task"`, `data-task-action="stop_task"`}},
		{path: "/guide", contentType: "text/html", contains: []string{`id="guide-search"`, `id="architecture"`, `id="cli-reference"`, `id="troubleshooting"`, `/assets/dashboard-overview-light.jpg`, `alt="Workbench Dashboard 화면 구성`}},
		{path: "/assets/app.js", contentType: "text/javascript", contains: []string{`"starting", "running", "waiting", "idle"`, `task.provenance`, `renderOverview`, `renderContexts`, `renderScheduler`, `renderSecrets`, `renderProfileSettings`, `renderTools`, `update_profile`, `update_secret`, `form.elements.value.value = ""`, `registry_available`, `secret_references`, `export_keys`, `work_locations`, `tool_health`, `clear_agent_history`, `agent_registry_path`, `workflow_id`, `run_workflow`, `data-workflow-id`, `task-terminal-note`, `attach_session`, `adopt_session`, `stop_session`, `session.ownership`}},
		{path: "/assets/theme.js", contentType: "text/javascript", contains: []string{"workbench.dashboard.theme.v1", "localStorage"}},
		{path: "/assets/guide.js", contentType: "text/javascript", contains: []string{"guide-search", "IntersectionObserver"}},
		{path: "/assets/dashboard-overview-light.jpg", contentType: "image/jpeg"},
		{path: "/assets/style.css", contentType: "text/css", contains: []string{"prefers-color-scheme: light", `data-theme="light"`, "[hidden] { display: none !important; }"}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), test.contentType) {
				t.Fatalf("unexpected response: status=%d content-type=%q", response.Code, response.Header().Get("Content-Type"))
			}
			if response.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("security headers missing")
			}
			for _, fragment := range test.contains {
				if !strings.Contains(response.Body.String(), fragment) {
					t.Fatalf("response %s is missing %q", test.path, fragment)
				}
			}
		})
	}
}

func TestHandlerServesEmbeddedDashboardScreenshot(t *testing.T) {
	handler, err := NewHandler(&fakeService{}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/dashboard-overview-light.jpg", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); !strings.Contains(got, "image/jpeg") {
		t.Fatalf("Content-Type = %q, want image/jpeg", got)
	}
	body := response.Body.Bytes()
	if len(body) < 2 || body[0] != 0xff || body[1] != 0xd8 {
		t.Fatalf("embedded screenshot is not a JPEG: size=%d", len(body))
	}
}

func TestDashboardJavaScriptBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	command := exec.Command(node, "--test", "testdata/theme_test.mjs", "testdata/guide_test.mjs", "testdata/contexts_test.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("dashboard JavaScript tests failed: %v\n%s", err, output)
	}
}

func TestHandlerRequiresSameOriginTokenForActions(t *testing.T) {
	service := &fakeService{}
	handler, err := NewHandler(service, "secret")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		token  string
		origin string
		want   int
	}{
		{name: "missing token", origin: "http://workbench.local", want: http.StatusForbidden},
		{name: "cross origin", token: "secret", origin: "http://attacker.invalid", want: http.StatusForbidden},
		{name: "authorized", token: "secret", origin: "http://workbench.local", want: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://workbench.local/api/v1/actions", strings.NewReader(`{"action":"open_project","project_id":"alpha"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", test.origin)
			request.Header.Set("X-Workbench-Token", test.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("unexpected status: got=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
	if service.actions.Load() != 1 {
		t.Fatalf("unauthorized action reached service: %d calls", service.actions.Load())
	}
}

func TestHandlerRejectsUnknownActionFields(t *testing.T) {
	service := &fakeService{}
	handler, err := NewHandler(service, "secret")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://workbench.local/api/v1/actions", strings.NewReader(`{"action":"open_project","command":"rm"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Workbench-Token", "secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || service.actions.Load() != 0 {
		t.Fatalf("unknown action field was accepted: status=%d calls=%d", response.Code, service.actions.Load())
	}
}

func TestHandlerAcceptsOnlyTypedWorkflowActionFields(t *testing.T) {
	service := &fakeService{}
	handler, err := NewHandler(service, "secret")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://workbench.local/api/v1/actions", strings.NewReader(`{"action":"run_workflow","project_id":"alpha","workflow_id":"project.test","args":["--unsafe"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Workbench-Token", "secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || service.actions.Load() != 0 {
		t.Fatalf("workflow args reached service: status=%d calls=%d", response.Code, service.actions.Load())
	}
}

func TestHandlerRejectsUnknownNestedEnvironmentFields(t *testing.T) {
	service := &fakeService{}
	handler, err := NewHandler(service, "secret")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"update_environment","environment":{"id":"dev","operation":"set_secret_reference","variable":"TOKEN","reference":"sec://service/token","plaintext":"must-not-be-accepted"}}`
	request := httptest.NewRequest(http.MethodPost, "http://workbench.local/api/v1/actions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Workbench-Token", "secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || service.actions.Load() != 0 {
		t.Fatalf("unknown nested field was accepted: status=%d calls=%d", response.Code, service.actions.Load())
	}
}

func TestHandlerRejectsUnknownNestedSecretFields(t *testing.T) {
	service := &fakeService{}
	handler, err := NewHandler(service, "secret")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"update_secret","secret":{"operation":"set","service":"github","field":"token","value":"secret","command":"must-not-be-accepted"}}`
	request := httptest.NewRequest(http.MethodPost, "http://workbench.local/api/v1/actions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Workbench-Token", "secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || service.actions.Load() != 0 {
		t.Fatalf("unknown nested Secret field was accepted: status=%d calls=%d", response.Code, service.actions.Load())
	}
}

func TestHandlerRejectsUnknownNestedProfileFields(t *testing.T) {
	service := &fakeService{}
	handler, err := NewHandler(service, "secret")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"update_profile","profile":{"default_backend":"auto","prefer_current_tmux":true,"backend_priority":[],"editor":"nvim","windows_terminal_profile":"","windows_terminal_distro":"","windows_terminal_window":"last","windows_terminal_mode":"tab","command":"must-not-be-accepted"}}`
	request := httptest.NewRequest(http.MethodPost, "http://workbench.local/api/v1/actions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Workbench-Token", "secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || service.actions.Load() != 0 {
		t.Fatalf("unknown nested profile field was accepted: status=%d calls=%d", response.Code, service.actions.Load())
	}
}

func TestSafeWorkflowRunOmitsCapturedOutput(t *testing.T) {
	result := workflows.Result{ID: "run-1", WorkflowID: workflows.ProjectTest, ProjectID: "alpha", Status: workflows.Succeeded, Output: "SECRET_VALUE", OutputTruncated: true}
	encoded, err := json.Marshal(SafeWorkflowRun(result))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SECRET_VALUE") || !strings.Contains(string(encoded), `"output_truncated":true`) {
		t.Fatalf("unsafe workflow response: %s", encoded)
	}
}

func TestListenUsesLoopback(t *testing.T) {
	listener, err := Listen(0)
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skip("sandbox blocks loopback sockets")
		}
		t.Fatal(err)
	}
	defer listener.Close()
	if !listener.Addr().(*net.TCPAddr).IP.IsLoopback() {
		t.Fatalf("dashboard listener is not loopback: %s", listener.Addr())
	}
}

func TestServeStopsAndReleasesListener(t *testing.T) {
	listener := newBlockingListener()
	handler, err := NewHandler(&fakeService{}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, handler) }()
	<-listener.accepted
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dashboard server did not stop")
	}
	select {
	case <-listener.closed:
	default:
		t.Fatal("dashboard listener remained open after shutdown")
	}
}

type blockingListener struct {
	accepted chan struct{}
	closed   chan struct{}
	accept   sync.Once
	close    sync.Once
}

func newBlockingListener() *blockingListener {
	return &blockingListener{accepted: make(chan struct{}), closed: make(chan struct{})}
}

func (listener *blockingListener) Accept() (net.Conn, error) {
	listener.accept.Do(func() { close(listener.accepted) })
	<-listener.closed
	return nil, net.ErrClosed
}

func (listener *blockingListener) Close() error {
	listener.close.Do(func() { close(listener.closed) })
	return nil
}

func (listener *blockingListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}
