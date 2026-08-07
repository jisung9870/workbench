package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	binboxadapter "github.com/jisung9870/workbench/adapters/binbox"
	tmuxadapter "github.com/jisung9870/workbench/adapters/tmux"
	"github.com/jisung9870/workbench/internal/activity"
	"github.com/jisung9870/workbench/internal/agents"
	"github.com/jisung9870/workbench/internal/backend"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/doctor"
	"github.com/jisung9870/workbench/internal/environments"
	"github.com/jisung9870/workbench/internal/output"
	"github.com/jisung9870/workbench/internal/overview"
	"github.com/jisung9870/workbench/internal/projects"
	"github.com/jisung9870/workbench/internal/scheduler"
	"github.com/jisung9870/workbench/internal/secrets"
	"github.com/jisung9870/workbench/internal/tasks"
	"github.com/jisung9870/workbench/internal/workflows"
	"github.com/jisung9870/workbench/internal/worktrees"
)

//go:embed assets/*
var assets embed.FS

const maxActionBody = (16 << 20) + (64 << 10)

type ChangeSummary = overview.ChangeSummary

type AgentTask struct {
	agents.Task
	Lifecycle string `json:"lifecycle"`
}

type WorkflowRun struct {
	ID              string           `json:"id"`
	WorkflowID      string           `json:"workflow_id"`
	ProjectID       string           `json:"project_id"`
	Status          workflows.Status `json:"status"`
	ExitCode        *int             `json:"exit_code,omitempty"`
	StartedAt       time.Time        `json:"started_at"`
	FinishedAt      time.Time        `json:"finished_at"`
	DurationMillis  int64            `json:"duration_millis"`
	OutputTruncated bool             `json:"output_truncated"`
	PaneID          string           `json:"pane_id,omitempty"`
	SessionName     string           `json:"session_name,omitempty"`
	EnvironmentID   string           `json:"environment_id,omitempty"`
	ResolveSecrets  bool             `json:"resolve_secrets"`
}

type ContextSecretReference struct {
	Variable string `json:"variable"`
	Status   string `json:"status"`
}

type ContextEnvironment struct {
	ID               string                   `json:"id"`
	AWSProfile       string                   `json:"aws_profile,omitempty"`
	AWSRegion        string                   `json:"aws_region,omitempty"`
	KubeContext      string                   `json:"kube_context,omitempty"`
	KubeNamespace    string                   `json:"kube_namespace,omitempty"`
	ExportKeys       []string                 `json:"export_keys"`
	ProjectIDs       []string                 `json:"project_ids"`
	SecretReferences []ContextSecretReference `json:"secret_references"`
	Expiry           environments.Expiry      `json:"expiry"`
}

type ContextSummary struct {
	Environments      int `json:"environments"`
	LinkedProjects    int `json:"linked_projects"`
	SecretReferences  int `json:"secret_references"`
	Available         int `json:"available"`
	Missing           int `json:"missing"`
	StoreUnavailable  int `json:"store_unavailable"`
	InvalidReferences int `json:"invalid_references"`
	Permanent         int `json:"permanent"`
	Active            int `json:"active"`
	Expiring          int `json:"expiring"`
	Expired           int `json:"expired"`
}

type Contexts struct {
	RegistryAvailable bool                 `json:"registry_available"`
	Reason            string               `json:"reason,omitempty"`
	Summary           ContextSummary       `json:"summary"`
	Environments      []ContextEnvironment `json:"environments"`
}

type SecretCatalog struct {
	Available bool            `json:"available"`
	Reason    string          `json:"reason,omitempty"`
	Entries   []secrets.Entry `json:"entries"`
}

type ProfileSettings struct {
	Available bool           `json:"available"`
	Reason    string         `json:"reason,omitempty"`
	Name      string         `json:"name,omitempty"`
	Values    config.Profile `json:"values"`
}

func SafeWorkflowRun(result workflows.Result) WorkflowRun {
	return WorkflowRun{ID: result.ID, WorkflowID: result.WorkflowID, ProjectID: result.ProjectID, Status: result.Status, ExitCode: result.ExitCode, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt, DurationMillis: result.DurationMillis, OutputTruncated: result.OutputTruncated, PaneID: result.PaneID, SessionName: result.SessionName, EnvironmentID: result.EnvironmentID, ResolveSecrets: result.ResolveSecrets}
}

type Snapshot struct {
	GeneratedAt       time.Time                `json:"generated_at"`
	Platform          string                   `json:"platform"`
	Profile           string                   `json:"profile"`
	AgentRegistryPath string                   `json:"agent_registry_path"`
	Projects          []projects.Project       `json:"projects"`
	Agents            []AgentTask              `json:"agents"`
	Tasks             []tasks.Task             `json:"tasks"`
	Worktrees         []worktrees.Item         `json:"worktrees"`
	Changes           []ChangeSummary          `json:"changes"`
	Doctor            doctor.Report            `json:"doctor"`
	Warnings          []string                 `json:"warnings"`
	Tmux              tmuxadapter.Snapshot     `json:"tmux"`
	Overview          overview.Summary         `json:"overview"`
	ToolHealth        binboxadapter.Report     `json:"tool_health"`
	Workflows         []workflows.Availability `json:"workflows"`
	WorkflowHistory   []WorkflowRun            `json:"workflow_history"`
	Contexts          Contexts                 `json:"contexts"`
	Scheduler         scheduler.Snapshot       `json:"scheduler"`
	Secrets           SecretCatalog            `json:"secrets"`
	ProfileSettings   ProfileSettings          `json:"profile_settings"`
	Activity          []activity.Event         `json:"activity"`
}

type ActionRequest struct {
	Action      string               `json:"action"`
	ProjectID   string               `json:"project_id,omitempty"`
	TaskID      string               `json:"task_id,omitempty"`
	TaskIDs     []string             `json:"task_ids,omitempty"`
	AgentKind   string               `json:"agent_kind,omitempty"`
	Backend     string               `json:"backend,omitempty"`
	PaneID      string               `json:"pane_id,omitempty"`
	WorkflowID  string               `json:"workflow_id,omitempty"`
	SessionName string               `json:"session_name,omitempty"`
	Environment *EnvironmentMutation `json:"environment,omitempty"`
	Secret      *SecretMutation      `json:"secret,omitempty"`
	Profile     *ProfileMutation     `json:"profile,omitempty"`
}

type EnvironmentMutation struct {
	ID            string `json:"id"`
	Operation     string `json:"operation"`
	AWSProfile    string `json:"aws_profile,omitempty"`
	AWSRegion     string `json:"aws_region,omitempty"`
	KubeContext   string `json:"kube_context,omitempty"`
	KubeNamespace string `json:"kube_namespace,omitempty"`
	Variable      string `json:"variable,omitempty"`
	Value         string `json:"value,omitempty"`
	Reference     string `json:"reference,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

type SecretMutation struct {
	Operation string `json:"operation"`
	Service   string `json:"service"`
	Field     string `json:"field"`
	Value     string `json:"value,omitempty"`
	Replace   bool   `json:"replace,omitempty"`
}

type ProfileMutation struct {
	DefaultBackend         string   `json:"default_backend"`
	PreferCurrentTmux      bool     `json:"prefer_current_tmux"`
	BackendPriority        []string `json:"backend_priority"`
	Editor                 string   `json:"editor"`
	WindowsTerminalProfile string   `json:"windows_terminal_profile"`
	WindowsTerminalDistro  string   `json:"windows_terminal_distro"`
	WindowsTerminalWindow  string   `json:"windows_terminal_window"`
	WindowsTerminalMode    string   `json:"windows_terminal_mode"`
}

type ActionResult struct {
	Message     string       `json:"message"`
	Output      string       `json:"output,omitempty"`
	WorkflowRun *WorkflowRun `json:"workflow_run,omitempty"`
	Session     backend.Name `json:"session,omitempty"`
	Surface     backend.Name `json:"surface,omitempty"`
}

type Service interface {
	Snapshot(context.Context) (Snapshot, error)
	Execute(context.Context, ActionRequest) (ActionResult, error)
}

type ActionError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (err *ActionError) Error() string { return err.Message }

type Handler struct {
	service Service
	token   string
	index   *template.Template
	guide   *template.Template
}

func NewToken() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate dashboard action token: %w", err)
	}
	return hex.EncodeToString(random), nil
}

func NewHandler(service Service, token string) (*Handler, error) {
	if service == nil || token == "" {
		return nil, errors.New("dashboard service and action token are required")
	}
	contents, err := assets.ReadFile("assets/index.html")
	if err != nil {
		return nil, err
	}
	index, err := template.New("index").Parse(string(contents))
	if err != nil {
		return nil, fmt.Errorf("parse dashboard index: %w", err)
	}
	guideContents, err := assets.ReadFile("assets/guide.html")
	if err != nil {
		return nil, err
	}
	guide, err := template.New("guide").Parse(string(guideContents))
	if err != nil {
		return nil, fmt.Errorf("parse dashboard guide: %w", err)
	}
	return &Handler{service: service, token: token, index: index, guide: guide}, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(writer)
	switch request.URL.Path {
	case "/", "/projects", "/activity", "/settings", "/system":
		handler.serveIndex(writer, request, dashboardPage(request.URL.Path))
	case "/guide", "/guide/", "/docs", "/docs/":
		handler.serveGuide(writer, request)
	case "/assets/app.js":
		handler.serveAsset(writer, request, "assets/app.js", "text/javascript; charset=utf-8")
	case "/assets/theme.js":
		handler.serveAsset(writer, request, "assets/theme.js", "text/javascript; charset=utf-8")
	case "/assets/guide.js":
		handler.serveAsset(writer, request, "assets/guide.js", "text/javascript; charset=utf-8")
	case "/assets/dashboard-overview-light.jpg":
		handler.serveAsset(writer, request, "assets/dashboard-overview-light.jpg", "image/jpeg")
	case "/assets/style.css":
		handler.serveAsset(writer, request, "assets/style.css", "text/css; charset=utf-8")
	case "/api/v1/snapshot":
		handler.serveSnapshot(writer, request)
	case "/api/v1/actions":
		handler.serveAction(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (handler *Handler) serveGuide(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if request.Method == http.MethodHead {
		return
	}
	if err := handler.guide.Execute(writer, nil); err != nil {
		http.Error(writer, "render dashboard guide", http.StatusInternalServerError)
	}
}

func dashboardPage(path string) string {
	switch path {
	case "/projects":
		return "projects"
	case "/activity":
		return "activity"
	case "/settings":
		return "settings"
	case "/system":
		return "system"
	default:
		return "dashboard"
	}
}

func (handler *Handler) serveIndex(writer http.ResponseWriter, request *http.Request, page string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if request.Method == http.MethodHead {
		return
	}
	if err := handler.index.Execute(writer, map[string]string{"Token": handler.token, "Page": page}); err != nil {
		http.Error(writer, "render dashboard", http.StatusInternalServerError)
	}
}

func (handler *Handler) serveAsset(writer http.ResponseWriter, request *http.Request, name, contentType string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	contents, err := assets.ReadFile(name)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "no-cache")
	if request.Method == http.MethodGet {
		_, _ = writer.Write(contents)
	}
}

func (handler *Handler) serveSnapshot(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	snapshot, err := handler.service.Snapshot(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "SNAPSHOT_FAILED", err.Error(), nil)
		return
	}
	writeResult(writer, http.StatusOK, snapshot)
}

func (handler *Handler) serveAction(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	if !sameOrigin(request) || subtle.ConstantTimeCompare([]byte(request.Header.Get("X-Workbench-Token")), []byte(handler.token)) != 1 {
		writeError(writer, http.StatusForbidden, "ACTION_FORBIDDEN", "dashboard action authorization failed", nil)
		return
	}
	if mediaType := strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]); mediaType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, "INVALID_CONTENT_TYPE", "dashboard actions require application/json", nil)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxActionBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var action ActionRequest
	if err := decoder.Decode(&action); err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_ACTION", err.Error(), nil)
		return
	}
	if err := ensureEOF(decoder); err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_ACTION", err.Error(), nil)
		return
	}
	result, err := handler.service.Execute(request.Context(), action)
	if err != nil {
		var actionErr *ActionError
		if errors.As(err, &actionErr) {
			writeError(writer, actionErr.Status, actionErr.Code, actionErr.Message, actionErr.Details)
			return
		}
		writeError(writer, http.StatusInternalServerError, "ACTION_FAILED", err.Error(), nil)
		return
	}
	writeResult(writer, http.StatusOK, result)
}

func Listen(port int) (net.Listener, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("dashboard port must be between 0 and 65535")
	}
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

func URL(listener net.Listener) string {
	return "http://" + listener.Addr().String() + "/"
}

func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		serveErr := <-result
		if shutdownErr != nil {
			return shutdownErr
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}

func setSecurityHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; script-src 'self'; style-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
}

func sameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Scheme == "http" && parsed.Host == request.Host
}

func methodNotAllowed(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	writeError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed", nil)
}

func writeResult(writer http.ResponseWriter, status int, data any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = output.Write(writer, data, nil)
}

func writeError(writer http.ResponseWriter, status int, code, message string, details map[string]any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = output.WriteError(writer, code, message, details)
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values are not allowed")
}
