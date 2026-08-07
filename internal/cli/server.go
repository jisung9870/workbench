package cli

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jisung9870/workbench/internal/activity"
	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/dashboard"
	"github.com/jisung9870/workbench/internal/environments"
	"github.com/jisung9870/workbench/internal/output"
	"github.com/jisung9870/workbench/internal/scheduler"
	"github.com/jisung9870/workbench/internal/storage"
	"github.com/jisung9870/workbench/internal/workflows"
)

const (
	serverSchemaVersion  = 1
	serverStatusStarting = "starting"
	serverStatusRunning  = "running"
	serverStatusStopped  = "stopped"
	serverStatusUnknown  = "unavailable"

	serverInstanceEnv = "WORKBENCH_SERVER_INSTANCE"
	serverControlEnv  = "WORKBENCH_SERVER_CONTROL_TOKEN"
)

var (
	serverExecutable   = os.Executable
	serverStartTimeout = 5 * time.Second
	serverStopTimeout  = 5 * time.Second
	serverHTTPTimeout  = 750 * time.Millisecond
	serverHTTPClient   = &http.Client{
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

type serverState struct {
	SchemaVersion int       `json:"schema_version"`
	Status        string    `json:"status"`
	InstanceID    string    `json:"instance_id"`
	ControlToken  string    `json:"control_token"`
	PID           int       `json:"pid,omitempty"`
	URL           string    `json:"url,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	LogPath       string    `json:"log_path"`
}

type serverStatusView struct {
	Status    string             `json:"status"`
	PID       int                `json:"pid,omitempty"`
	URL       string             `json:"url,omitempty"`
	StartedAt time.Time          `json:"started_at,omitempty"`
	StateFile string             `json:"state_file"`
	LogFile   string             `json:"log_file,omitempty"`
	Scheduler scheduler.Snapshot `json:"scheduler"`
}

type serverProbe struct {
	SchemaVersion int                `json:"schema_version"`
	InstanceID    string             `json:"instance_id"`
	PID           int                `json:"pid"`
	URL           string             `json:"url"`
	StartedAt     time.Time          `json:"started_at"`
	Scheduler     scheduler.Snapshot `json:"scheduler"`
}

type serverControlHandler struct {
	next      http.Handler
	probe     serverProbe
	token     string
	cancel    context.CancelFunc
	scheduler schedulerRuntime
}

func runServer(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	if len(args) == 0 {
		return invalid("server subcommand is required (expected start, stop, or status)")
	}
	switch args[0] {
	case "start":
		return runServerStart(args[1:], paths, stdout, stderr)
	case "stop":
		return runServerStop(args[1:], paths, stdout)
	case "status":
		return runServerStatus(args[1:], paths, stdout)
	case "_serve":
		return runServerServe(args[1:], paths, stdout, stderr)
	default:
		return invalid("unknown server subcommand %q", args[0])
	}
}

func runServerStart(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	positionals, options, parseErr := parseOptions(args, map[string]bool{"--open": true, "--port": true, "--json": false})
	if parseErr != nil || len(positionals) != 0 {
		return invalid("usage: wb server start [--open auto|cmux|browser|none] [--port <0-65535>] [--json]")
	}
	openTarget := options["--open"]
	if openTarget == "" {
		openTarget = "none"
	}
	if !validDashboardOpenTarget(openTarget) {
		return invalid("invalid server open target %q", openTarget)
	}
	port, err := parseDashboardPort(options["--port"])
	if err != nil {
		return invalid("%s", err)
	}

	statePath := serverStatePath(paths)
	if existing, found, loadErr := loadServerState(statePath); loadErr != nil {
		return generalError(loadErr)
	} else if found {
		view := inspectServer(context.Background(), statePath, existing)
		if view.Status != serverStatusStopped {
			return &commandError{ExitCode: ExitConflict, Code: "SERVER_ALREADY_RUNNING", Message: fmt.Sprintf("Workbench server is %s", view.Status), Details: map[string]any{"pid": view.PID, "url": view.URL, "state_file": statePath}}
		}
		if err := removeServerState(statePath, existing.InstanceID); err != nil {
			return generalError(err)
		}
	}

	instanceID, err := dashboard.NewToken()
	if err != nil {
		return generalError(err)
	}
	controlToken, err := dashboard.NewToken()
	if err != nil {
		return generalError(err)
	}
	state := serverState{
		SchemaVersion: serverSchemaVersion,
		Status:        serverStatusStarting,
		InstanceID:    instanceID,
		ControlToken:  controlToken,
		StartedAt:     time.Now().UTC(),
		LogPath:       filepath.Join(paths.StateDir, "server.log"),
	}
	if err := reserveServerState(statePath, state); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return &commandError{ExitCode: ExitConflict, Code: "SERVER_ALREADY_RUNNING", Message: "Workbench server start is already in progress", Details: map[string]any{"state_file": statePath}}
		}
		return generalError(err)
	}
	cleanupReservation := func() { _ = removeServerState(statePath, instanceID) }

	executable, err := serverExecutable()
	if err != nil {
		cleanupReservation()
		return generalError(fmt.Errorf("resolve wb executable: %w", err))
	}
	logFile, err := os.OpenFile(state.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		cleanupReservation()
		return generalError(fmt.Errorf("open server log: %w", err))
	}
	command := exec.Command(executable, "server", "_serve", "--port", strconv.Itoa(port))
	command.Env = append(os.Environ(), serverInstanceEnv+"="+instanceID, serverControlEnv+"="+controlToken)
	command.Stdout = logFile
	command.Stderr = logFile
	configureBackgroundCommand(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		cleanupReservation()
		return generalError(fmt.Errorf("start Workbench server: %w", err))
	}
	childPID := command.Process.Pid
	_ = command.Process.Release()
	_ = logFile.Close()

	deadline := time.Now().Add(serverStartTimeout)
	for time.Now().Before(deadline) {
		current, found, loadErr := loadServerState(statePath)
		if loadErr != nil {
			return generalError(loadErr)
		}
		if found && current.InstanceID == instanceID {
			view := inspectServer(context.Background(), statePath, current)
			if view.Status == serverStatusRunning {
				warnings := []string{}
				if openTarget != "none" {
					executor := &backend.OSExecutor{Stdout: stdout, Stderr: stderr}
					environment := backend.CurrentEnvironment()
					if openErr := dashboard.Open(context.Background(), executor, environment.GOOS, environment.IsWSL(), openTarget, current.URL); openErr != nil {
						warnings = append(warnings, fmt.Sprintf("open dashboard: %v", openErr))
					}
				}
				return writeServerStatus(stdout, stderr, options, view, warnings)
			}
		}
		alive, aliveErr := processAlive(childPID)
		if aliveErr == nil && !alive {
			cleanupReservation()
			return &commandError{ExitCode: ExitGeneral, Code: "SERVER_START_FAILED", Message: fmt.Sprintf("Workbench server exited before becoming ready; inspect %s", state.LogPath), Details: map[string]any{"log_file": state.LogPath}}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return &commandError{ExitCode: ExitPartial, Code: "SERVER_START_TIMEOUT", Message: fmt.Sprintf("Workbench server process %d started but did not become ready; inspect %s", childPID, state.LogPath), Details: map[string]any{"pid": childPID, "state_file": statePath, "log_file": state.LogPath}}
}

func runServerStatus(args []string, paths config.Paths, stdout io.Writer) *commandError {
	positionals, options, parseErr := parseOptions(args, map[string]bool{"--json": false})
	if parseErr != nil || len(positionals) != 0 {
		return invalid("usage: wb server status [--json]")
	}
	statePath := serverStatePath(paths)
	state, found, err := loadServerState(statePath)
	if err != nil {
		return generalError(err)
	}
	if !found {
		return writeServerStatus(stdout, io.Discard, options, serverStatusView{Status: serverStatusStopped, StateFile: statePath}, nil)
	}
	return writeServerStatus(stdout, io.Discard, options, inspectServer(context.Background(), statePath, state), nil)
}

func runServerStop(args []string, paths config.Paths, stdout io.Writer) *commandError {
	positionals, options, parseErr := parseOptions(args, map[string]bool{"--json": false})
	if parseErr != nil || len(positionals) != 0 {
		return invalid("usage: wb server stop [--json]")
	}
	statePath := serverStatePath(paths)
	state, found, err := loadServerState(statePath)
	if err != nil {
		return generalError(err)
	}
	if !found {
		return writeServerStatus(stdout, io.Discard, options, serverStatusView{Status: serverStatusStopped, StateFile: statePath}, nil)
	}

	deadline := time.Now().Add(serverStopTimeout)
	view := inspectServer(context.Background(), statePath, state)
	for view.Status == serverStatusStarting && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		current, currentFound, loadErr := loadServerState(statePath)
		if loadErr != nil {
			return generalError(loadErr)
		}
		if !currentFound {
			return writeServerStatus(stdout, io.Discard, options, serverStatusView{Status: serverStatusStopped, StateFile: statePath}, nil)
		}
		state = current
		view = inspectServer(context.Background(), statePath, state)
	}
	if view.Status == serverStatusStopped {
		if err := removeServerState(statePath, state.InstanceID); err != nil {
			return generalError(err)
		}
		return writeServerStatus(stdout, io.Discard, options, serverStatusView{Status: serverStatusStopped, StateFile: statePath, LogFile: state.LogPath}, nil)
	}
	if view.Status != serverStatusRunning {
		return &commandError{ExitCode: ExitConflict, Code: "SERVER_UNAVAILABLE", Message: fmt.Sprintf("Workbench server process %d is registered but its management endpoint is unavailable", state.PID), Details: map[string]any{"pid": state.PID, "state_file": statePath, "log_file": state.LogPath}}
	}
	if err := requestServerStop(context.Background(), state); err != nil {
		return generalError(err)
	}
	deadline = time.Now().Add(serverStopTimeout)
	for time.Now().Before(deadline) {
		current, currentFound, loadErr := loadServerState(statePath)
		if loadErr != nil {
			return generalError(loadErr)
		}
		if !currentFound || current.InstanceID != state.InstanceID {
			return writeServerStatus(stdout, io.Discard, options, serverStatusView{Status: serverStatusStopped, StateFile: statePath, LogFile: state.LogPath}, nil)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return &commandError{ExitCode: ExitPartial, Code: "SERVER_STOP_TIMEOUT", Message: fmt.Sprintf("Workbench server process %d accepted shutdown but did not stop in time", state.PID), Details: map[string]any{"pid": state.PID, "state_file": statePath}}
}

func runServerServe(args []string, paths config.Paths, stdout, stderr io.Writer) *commandError {
	positionals, options, parseErr := parseOptions(args, map[string]bool{"--port": true})
	if parseErr != nil || len(positionals) != 0 || os.Getenv(serverInstanceEnv) == "" || os.Getenv(serverControlEnv) == "" {
		return invalid("server _serve is an internal command")
	}
	port, err := parseDashboardPort(options["--port"])
	if err != nil {
		return invalid("%s", err)
	}
	statePath := serverStatePath(paths)
	state, found, err := loadServerState(statePath)
	if err != nil {
		return generalError(err)
	}
	if !found || state.Status != serverStatusStarting || state.InstanceID != os.Getenv(serverInstanceEnv) || subtle.ConstantTimeCompare([]byte(state.ControlToken), []byte(os.Getenv(serverControlEnv))) != 1 {
		return &commandError{ExitCode: ExitConflict, Code: "SERVER_RESERVATION_INVALID", Message: "Workbench server reservation is missing or does not match this process"}
	}

	listener, err := dashboard.Listen(port)
	if err != nil {
		_ = removeServerState(statePath, state.InstanceID)
		return generalError(fmt.Errorf("listen for Workbench server: %w", err))
	}
	token, err := dashboard.NewToken()
	if err != nil {
		_ = listener.Close()
		_ = removeServerState(statePath, state.InstanceID)
		return generalError(err)
	}
	environmentStore := environments.NewStore(paths)
	runner, err := scheduler.New(
		scheduler.NewEnvironmentExpiryJob(environmentStore, time.Minute),
		scheduler.NewActivityScanJob(agents.NewStateStore(paths), workflows.NewStore(paths), environmentStore, activity.NewStore(paths), time.Minute),
	)
	if err != nil {
		_ = listener.Close()
		_ = removeServerState(statePath, state.InstanceID)
		return generalError(err)
	}
	ctx, cancel := serverSignalContext(context.Background())
	defer cancel()
	handler, err := dashboard.NewHandler(&dashboardService{paths: paths, scheduler: runner}, token)
	if err != nil {
		_ = listener.Close()
		_ = removeServerState(statePath, state.InstanceID)
		return generalError(err)
	}
	state.Status = serverStatusRunning
	state.PID = os.Getpid()
	state.URL = dashboard.URL(listener)
	if err := updateServerState(statePath, state.InstanceID, state); err != nil {
		_ = listener.Close()
		return generalError(err)
	}
	defer func() { _ = removeServerState(statePath, state.InstanceID) }()
	probe := serverProbe{SchemaVersion: serverSchemaVersion, InstanceID: state.InstanceID, PID: state.PID, URL: state.URL, StartedAt: state.StartedAt}
	managed := &serverControlHandler{next: handler, probe: probe, token: state.ControlToken, cancel: cancel, scheduler: runner}
	go runner.Run(ctx)
	fmt.Fprintf(stdout, "Workbench server started pid=%d url=%s\n", state.PID, state.URL)
	if err := dashboard.Serve(ctx, listener, managed); err != nil {
		fmt.Fprintf(stderr, "Workbench server failed: %v\n", err)
		return generalError(fmt.Errorf("serve Workbench server: %w", err))
	}
	fmt.Fprintf(stdout, "Workbench server stopped pid=%d\n", state.PID)
	return nil
}

func (handler *serverControlHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	switch request.URL.Path {
	case "/api/v1/server":
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		probe := handler.probe
		if handler.scheduler != nil {
			probe.Scheduler = handler.scheduler.Snapshot()
		}
		_ = json.NewEncoder(writer).Encode(probe)
	case "/api/v1/server/stop":
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if subtle.ConstantTimeCompare([]byte(request.Header.Get("X-Workbench-Server-Token")), []byte(handler.token)) != 1 {
			http.Error(writer, "server control authorization failed", http.StatusForbidden)
			return
		}
		writer.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(writer, "stopping\n")
		go handler.cancel()
	default:
		handler.next.ServeHTTP(writer, request)
	}
}

func parseDashboardPort(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 0 || port > 65535 {
		return 0, errors.New("server port must be between 0 and 65535")
	}
	return port, nil
}

func validDashboardOpenTarget(value string) bool {
	switch value {
	case "auto", "cmux", "browser", "none":
		return true
	default:
		return false
	}
}

func serverStatePath(paths config.Paths) string { return filepath.Join(paths.StateDir, "server.json") }

func reserveServerState(path string, state serverState) error {
	if err := validateServerState(state); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create server state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode server state: %w", err)
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write server state: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("flush server state: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close server state: %w", err)
	}
	return nil
}

func updateServerState(path, instanceID string, state serverState) error {
	current, found, err := loadServerState(path)
	if err != nil {
		return err
	}
	if !found || current.InstanceID != instanceID {
		return errors.New("server state reservation changed before update")
	}
	if err := validateServerState(state); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode server state: %w", err)
	}
	return storage.WriteAtomic(path, append(data, '\n'))
}

func loadServerState(path string) (serverState, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return serverState{}, false, nil
		}
		return serverState{}, false, fmt.Errorf("open server state: %w", err)
	}
	defer file.Close()
	state := serverState{}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return serverState{}, false, fmt.Errorf("decode server state: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values are not allowed")
		}
		return serverState{}, false, fmt.Errorf("decode server state: %w", err)
	}
	if err := validateServerState(state); err != nil {
		return serverState{}, false, fmt.Errorf("validate server state: %w", err)
	}
	return state, true, nil
}

func validateServerState(state serverState) error {
	if state.SchemaVersion != serverSchemaVersion {
		return fmt.Errorf("unsupported server schema_version %d", state.SchemaVersion)
	}
	if state.Status != serverStatusStarting && state.Status != serverStatusRunning {
		return fmt.Errorf("invalid server status %q", state.Status)
	}
	if len(state.InstanceID) != 64 || len(state.ControlToken) != 64 || state.StartedAt.IsZero() || !filepath.IsAbs(state.LogPath) {
		return errors.New("server state has invalid identity, timestamp, or log path")
	}
	if state.PID < 0 {
		return errors.New("server state has an invalid pid")
	}
	if state.Status == serverStatusRunning {
		parsed, err := url.Parse(state.URL)
		if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" || state.PID == 0 {
			return errors.New("running server state has an invalid loopback URL or pid")
		}
	}
	return nil
}

func removeServerState(path, instanceID string) error {
	state, found, err := loadServerState(path)
	if err != nil {
		return err
	}
	if !found || state.InstanceID != instanceID {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove server state: %w", err)
	}
	return nil
}

func inspectServer(ctx context.Context, statePath string, state serverState) serverStatusView {
	view := serverStatusView{Status: state.Status, PID: state.PID, URL: state.URL, StartedAt: state.StartedAt, StateFile: statePath, LogFile: state.LogPath, Scheduler: scheduler.Unavailable("server scheduler is unavailable")}
	if state.Status == serverStatusRunning {
		probe, err := probeServer(ctx, state)
		if err == nil && probe.InstanceID == state.InstanceID && probe.PID == state.PID {
			view.Status = serverStatusRunning
			view.Scheduler = probe.Scheduler
			return view
		}
	}
	if state.PID == 0 {
		if state.Status == serverStatusStarting && time.Since(state.StartedAt) < serverStartTimeout*2 {
			view.Status = serverStatusStarting
		} else {
			view.Status = serverStatusStopped
		}
		return view
	}
	alive, err := processAlive(state.PID)
	if err != nil {
		view.Status = serverStatusUnknown
	} else if alive {
		if state.Status == serverStatusStarting {
			view.Status = serverStatusStarting
		} else {
			view.Status = serverStatusUnknown
		}
	} else {
		view.Status = serverStatusStopped
	}
	return view
}

func probeServer(ctx context.Context, state serverState) (serverProbe, error) {
	endpoint, err := resolveServerEndpoint(state.URL, "api/v1/server")
	if err != nil {
		return serverProbe{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, serverHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return serverProbe{}, err
	}
	response, err := serverHTTPClient.Do(request)
	if err != nil {
		return serverProbe{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return serverProbe{}, fmt.Errorf("server probe returned HTTP %d", response.StatusCode)
	}
	probe := serverProbe{}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<10))
	if err := decoder.Decode(&probe); err != nil {
		return serverProbe{}, err
	}
	if probe.SchemaVersion != serverSchemaVersion {
		return serverProbe{}, fmt.Errorf("unsupported server probe schema_version %d", probe.SchemaVersion)
	}
	return probe, nil
}

func requestServerStop(ctx context.Context, state serverState) error {
	endpoint, err := resolveServerEndpoint(state.URL, "api/v1/server/stop")
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, serverHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, strings.NewReader(""))
	if err != nil {
		return err
	}
	request.Header.Set("X-Workbench-Server-Token", state.ControlToken)
	response, err := serverHTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("request Workbench server shutdown: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("Workbench server rejected shutdown with HTTP %d", response.StatusCode)
	}
	return nil
}

func resolveServerEndpoint(baseURL, relative string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(&url.URL{Path: relative}).String(), nil
}

func writeServerStatus(stdout, stderr io.Writer, options map[string]string, view serverStatusView, warnings []string) *commandError {
	if view.Scheduler.Jobs == nil {
		view.Scheduler = scheduler.Unavailable("server is not running")
	}
	if _, jsonMode := options["--json"]; jsonMode {
		if err := output.Write(stdout, map[string]any{"server": view}, warnings); err != nil {
			return generalError(err)
		}
		return nil
	}
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "warning: %s\n", warning)
	}
	fmt.Fprintf(stdout, "status: %s\n", view.Status)
	if view.PID != 0 {
		fmt.Fprintf(stdout, "pid: %d\n", view.PID)
	}
	if view.URL != "" {
		fmt.Fprintf(stdout, "url: %s\n", view.URL)
	}
	if !view.StartedAt.IsZero() {
		fmt.Fprintf(stdout, "started_at: %s\n", view.StartedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(stdout, "state_file: %s\n", view.StateFile)
	if view.LogFile != "" {
		fmt.Fprintf(stdout, "log_file: %s\n", view.LogFile)
	}
	if view.Scheduler.Available {
		status := "stopped"
		if view.Scheduler.Running {
			status = "running"
		}
		fmt.Fprintf(stdout, "scheduler: %s\n", status)
		for _, job := range view.Scheduler.Jobs {
			fmt.Fprintf(stdout, "scheduler_job: %s\t%s\t%s\n", job.ID, job.Status, job.NextRunAt.Format(time.RFC3339))
		}
	}
	return nil
}
