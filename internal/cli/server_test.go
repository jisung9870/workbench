package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/output"
)

func TestServerRejectsInvalidCommandsAndOptions(t *testing.T) {
	paths := config.Paths{StateDir: t.TempDir()}
	for _, args := range [][]string{
		{},
		{"launch"},
		{"start", "--port", "65536"},
		{"start", "--open", "external"},
		{"status", "extra"},
		{"stop", "extra"},
		{"_serve"},
	} {
		err := runServer(args, paths, io.Discard, io.Discard)
		if err == nil || err.ExitCode != ExitArgument {
			t.Fatalf("expected argument error for %v, got %#v", args, err)
		}
	}
}

func TestServerStateReservationIsExclusiveAndIdentityBound(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "server.json")
	state := testServerState(root)
	if err := reserveServerState(path, state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("server state mode=%v", info.Mode().Perm())
	}
	if err := reserveServerState(path, state); !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate reservation was not rejected: %v", err)
	}
	loaded, found, err := loadServerState(path)
	if err != nil || !found || loaded.InstanceID != state.InstanceID {
		t.Fatalf("unexpected loaded state: %#v found=%v err=%v", loaded, found, err)
	}
	if err := removeServerState(path, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("mismatched identity removed state: %v", err)
	}
	if err := removeServerState(path, state.InstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("matching identity did not remove state: %v", err)
	}
}

func TestServerControlHandlerRequiresToken(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	handler := &serverControlHandler{
		next:   http.NotFoundHandler(),
		probe:  serverProbe{SchemaVersion: 1, InstanceID: strings.Repeat("a", 64), PID: 42, URL: "http://127.0.0.1:1234/"},
		token:  strings.Repeat("b", 64),
		cancel: func() { cancelled <- struct{}{} },
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/server", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("probe failed: %d %s", response.Code, response.Body.String())
	}
	probe := serverProbe{}
	if err := json.Unmarshal(response.Body.Bytes(), &probe); err != nil || probe.InstanceID != handler.probe.InstanceID {
		t.Fatalf("unexpected probe: %#v err=%v", probe, err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/server/stop", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("unauthorized stop returned %d", response.Code)
	}
	select {
	case <-cancelled:
		t.Fatal("unauthorized request stopped server")
	default:
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/server/stop", nil)
	request.Header.Set("X-Workbench-Server-Token", handler.token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("authorized stop returned %d", response.Code)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("authorized request did not stop server")
	}
}

func TestServerServeStatusAndStopLifecycle(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{StateDir: root}
	state := testServerState(root)
	statePath := serverStatePath(paths)
	if err := reserveServerState(statePath, state); err != nil {
		t.Fatal(err)
	}
	t.Setenv(serverInstanceEnv, state.InstanceID)
	t.Setenv(serverControlEnv, state.ControlToken)
	done := make(chan *commandError, 1)
	go func() { done <- runServerServe([]string{"--port", "0"}, paths, io.Discard, io.Discard) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case serveErr := <-done:
			t.Fatalf("server exited before becoming ready: %v", serveErr)
		default:
		}
		current, found, err := loadServerState(statePath)
		if err != nil {
			t.Fatal(err)
		}
		if found && current.Status == serverStatusRunning && inspectServer(context.Background(), statePath, current).Status == serverStatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}

	var statusOutput bytes.Buffer
	if err := runServerStatus([]string{"--json"}, paths, &statusOutput); err != nil {
		t.Fatal(err)
	}
	envelope := output.Envelope{}
	if err := json.Unmarshal(statusOutput.Bytes(), &envelope); err != nil || !envelope.OK || !bytes.Contains(statusOutput.Bytes(), []byte(`"status":"running"`)) || !bytes.Contains(statusOutput.Bytes(), []byte(`"environment-expiry-scan"`)) {
		t.Fatalf("unexpected status output: %s err=%v", statusOutput.String(), err)
	}

	var stopOutput bytes.Buffer
	if err := runServerStop(nil, paths, &stopOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stopOutput.String(), "status: stopped") {
		t.Fatalf("unexpected stop output: %s", stopOutput.String())
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit after stop")
	}
	if _, found, err := loadServerState(statePath); err != nil || found {
		t.Fatalf("server state remained after stop: found=%v err=%v", found, err)
	}
}

func testServerState(root string) serverState {
	return serverState{
		SchemaVersion: serverSchemaVersion,
		Status:        serverStatusStarting,
		InstanceID:    strings.Repeat("a", 64),
		ControlToken:  strings.Repeat("b", 64),
		StartedAt:     time.Now().UTC(),
		LogPath:       filepath.Join(root, "server.log"),
	}
}
