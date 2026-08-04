package output

import (
	"encoding/json"
	"io"
)

const SchemaVersion = 1

type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

type Envelope struct {
	SchemaVersion int      `json:"schema_version"`
	OK            bool     `json:"ok"`
	Data          any      `json:"data"`
	Warnings      []string `json:"warnings"`
	Error         *Error   `json:"error"`
}

func Write(w io.Writer, data any, warnings []string) error {
	return WriteResult(w, true, data, warnings, nil)
}

func WriteResult(w io.Writer, ok bool, data any, warnings []string, resultError *Error) error {
	if warnings == nil {
		warnings = []string{}
	}
	return json.NewEncoder(w).Encode(Envelope{
		SchemaVersion: SchemaVersion,
		OK:            ok,
		Data:          data,
		Warnings:      warnings,
		Error:         resultError,
	})
}

func WriteError(w io.Writer, code, message string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	return WriteResult(w, false, nil, []string{}, &Error{
		Code:    code,
		Message: message,
		Details: details,
	})
}
