package dashboard

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jisung9870/workbench/internal/output"
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
	handler, err := NewHandler(&fakeService{snapshot: Snapshot{Platform: "linux", Profile: "personal"}}, "secret")
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
