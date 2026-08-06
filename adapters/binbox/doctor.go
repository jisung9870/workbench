package binbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
)

const schemaVersion = 1

type Status string

const (
	Available   Status = "available"
	Unavailable Status = "unavailable"
	Skipped     Status = "skipped"
)

type Capability struct {
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	Status      Status `json:"status"`
	Description string `json:"description"`
	Reason      string `json:"reason,omitempty"`
	Recovery    string `json:"recovery,omitempty"`
}

type Summary struct {
	Available           int `json:"available"`
	UnavailableCore     int `json:"unavailable_core"`
	UnavailableOptional int `json:"unavailable_optional"`
	Skipped             int `json:"skipped"`
}

type Report struct {
	Provider     string       `json:"provider"`
	Available    bool         `json:"available"`
	Reason       string       `json:"reason,omitempty"`
	Capabilities []Capability `json:"capabilities"`
	Summary      Summary      `json:"summary"`
}

type Adapter struct {
	executor backend.Executor
}

func New(executor backend.Executor) *Adapter { return &Adapter{executor: executor} }

func (adapter *Adapter) Doctor(ctx context.Context) Report {
	report := unavailableReport("bb doctor is unavailable")
	command, err := adapter.executor.LookPath("bb")
	if err != nil {
		report.Reason = "bb executable was not found"
		return report
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, runErr := adapter.executor.Run(probeCtx, backend.ProcessRequest{Name: command, Args: []string{"doctor", "--json"}})
	parsed, parseErr := parseReport(result.Stdout)
	if parseErr == nil {
		return parsed
	}
	if runErr != nil {
		report.Reason = fmt.Sprintf("bb doctor --json failed with exit code %d", result.ExitCode)
		return report
	}
	report.Reason = "bb doctor --json returned an incompatible response"
	return report
}

type envelope struct {
	SchemaVersion int             `json:"schema_version"`
	OK            bool            `json:"ok"`
	Data          json.RawMessage `json:"data"`
	Error         *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type doctorData struct {
	Capabilities []struct {
		Name        string  `json:"name"`
		Scope       string  `json:"scope"`
		Description string  `json:"description"`
		Available   bool    `json:"available"`
		Recovery    *string `json:"recovery"`
	} `json:"capabilities"`
}

func parseReport(contents string) (Report, error) {
	decoder := json.NewDecoder(strings.NewReader(contents))
	var source envelope
	if err := decoder.Decode(&source); err != nil {
		return Report{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Report{}, errors.New("doctor response contains trailing JSON")
	}
	if source.SchemaVersion != schemaVersion || len(source.Data) == 0 || string(source.Data) == "null" {
		return Report{}, errors.New("unsupported doctor schema")
	}
	var data doctorData
	if err := json.Unmarshal(source.Data, &data); err != nil {
		return Report{}, err
	}
	if data.Capabilities == nil {
		return Report{}, errors.New("doctor capabilities are missing")
	}
	report := Report{Provider: "binbox", Available: true, Capabilities: []Capability{}}
	seen := map[string]struct{}{}
	for _, item := range data.Capabilities {
		if strings.TrimSpace(item.Name) == "" || (item.Scope != "core" && item.Scope != "optional") {
			return Report{}, errors.New("doctor capability has invalid identity or scope")
		}
		if _, exists := seen[item.Name]; exists {
			return Report{}, errors.New("doctor capability names are not unique")
		}
		seen[item.Name] = struct{}{}
		capability := Capability{Name: item.Name, Scope: item.Scope, Description: item.Description, Status: Available}
		if item.Available {
			report.Summary.Available++
		} else {
			capability.Status = Unavailable
			capability.Reason = "reported unavailable by bb doctor"
			if item.Recovery != nil {
				capability.Recovery = *item.Recovery
			}
			if item.Scope == "core" {
				report.Summary.UnavailableCore++
			} else {
				report.Summary.UnavailableOptional++
			}
		}
		report.Capabilities = append(report.Capabilities, capability)
	}
	if !source.OK && source.Error == nil {
		return Report{}, errors.New("failed doctor response has no error")
	}
	if source.OK && source.Error != nil {
		return Report{}, errors.New("successful doctor response contains an error")
	}
	if source.Error != nil {
		report.Reason = source.Error.Message
	}
	return report, nil
}

func unavailableReport(reason string) Report {
	return Report{Provider: "binbox", Available: false, Reason: reason, Capabilities: []Capability{}}
}
