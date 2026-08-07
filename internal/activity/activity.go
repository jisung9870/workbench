package activity

import "time"

const (
	SchemaVersion = 1
	HistoryLimit  = 200
)

type Event struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Severity   string    `json:"severity"`
	Title      string    `json:"title"`
	ResourceID string    `json:"resource_id"`
	ProjectID  string    `json:"project_id,omitempty"`
	State      string    `json:"state"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Observation is deliberately metadata-only. Raw command output, environment
// values, secret values, and filesystem paths do not belong in activity history.
type Observation struct {
	Key         string
	Kind        string
	Severity    string
	Title       string
	ResourceID  string
	ProjectID   string
	State       string
	OccurredAt  time.Time
	EmitInitial bool
}

type Registry struct {
	SchemaVersion int               `json:"schema_version"`
	Events        []Event           `json:"events"`
	States        map[string]string `json:"states"`
}
