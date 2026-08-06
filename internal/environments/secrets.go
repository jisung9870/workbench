package environments

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	workbenchsecrets "github.com/jisung9870/workbench/internal/secrets"
)

type SecretReference struct {
	Service string `json:"service"`
	Field   string `json:"field"`
}

type SecretReferenceStatus struct {
	Variable  string `json:"variable"`
	Reference string `json:"reference"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type SecretGetter interface {
	Get(service, field string) ([]byte, string, error)
}

func ParseSecretReference(value string) (SecretReference, error) {
	const prefix = "sec://"
	if !strings.HasPrefix(value, prefix) {
		return SecretReference{}, errors.New("reference must use sec://<service>/<field>")
	}
	path := strings.TrimPrefix(value, prefix)
	service, field, found := strings.Cut(path, "/")
	if !found || strings.Contains(field, "/") || !workbenchsecrets.ValidName(service) || !workbenchsecrets.ValidName(field) {
		return SecretReference{}, errors.New("reference must use sec://<service>/<field> with valid names")
	}
	return SecretReference{Service: service, Field: field}, nil
}

func PendingSecretReferences(environment Environment) []SecretReferenceStatus {
	statuses := make([]SecretReferenceStatus, 0, len(environment.Secrets))
	for variable, reference := range environment.Secrets {
		statuses = append(statuses, SecretReferenceStatus{Variable: variable, Reference: reference, Reason: "not_resolved"})
	}
	sortSecretStatuses(statuses)
	return statuses
}

func CheckSecretReferences(environment Environment, getter SecretGetter) []SecretReferenceStatus {
	_, statuses, _ := resolveSecretReferences(environment, getter, false)
	return statuses
}

// ResolveSecretReferences resolves values for a future subprocess environment.
// Callers own the returned byte slices and must call ZeroResolvedSecrets.
func ResolveSecretReferences(environment Environment, getter SecretGetter) (map[string][]byte, []SecretReferenceStatus, error) {
	return resolveSecretReferences(environment, getter, true)
}

func resolveSecretReferences(environment Environment, getter SecretGetter, retain bool) (map[string][]byte, []SecretReferenceStatus, error) {
	resolved := make(map[string][]byte, len(environment.Secrets))
	statuses := make([]SecretReferenceStatus, 0, len(environment.Secrets))
	failed := false
	for variable, referenceValue := range environment.Secrets {
		status := SecretReferenceStatus{Variable: variable, Reference: referenceValue}
		reference, err := ParseSecretReference(referenceValue)
		if err != nil {
			status.Reason = "invalid_reference"
			failed = true
			statuses = append(statuses, status)
			continue
		}
		value, _, err := getter.Get(reference.Service, reference.Field)
		if err != nil {
			zeroBytes(value)
			var notFound *workbenchsecrets.NotFoundError
			if errors.As(err, &notFound) {
				status.Reason = "missing"
			} else {
				status.Reason = "store_unavailable"
			}
			failed = true
			statuses = append(statuses, status)
			continue
		}
		status.Available = true
		statuses = append(statuses, status)
		if retain {
			resolved[variable] = value
		} else {
			zeroBytes(value)
		}
	}
	sortSecretStatuses(statuses)
	if failed {
		ZeroResolvedSecrets(resolved)
		return nil, statuses, errors.New("one or more secret references are unavailable")
	}
	return resolved, statuses, nil
}

func ZeroResolvedSecrets(values map[string][]byte) {
	for key, value := range values {
		zeroBytes(value)
		delete(values, key)
	}
}

func WriteShellExports(writer io.Writer, environment Environment, resolved map[string][]byte) error {
	values := ExportValues(environment)
	for key, value := range values {
		if strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("value for variable %q contains NUL and cannot be represented in a shell environment", key)
		}
	}
	for key, value := range resolved {
		if containsNUL(value) {
			return fmt.Errorf("value for variable %q contains NUL and cannot be represented in a shell environment", key)
		}
	}
	keys := make([]string, 0, len(values)+len(resolved))
	for key := range values {
		keys = append(keys, key)
	}
	for key := range resolved {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var output []byte
	defer func() { zeroBytes(output) }()
	for _, key := range keys {
		output = append(output, "export "...)
		output = append(output, key...)
		output = append(output, '=')
		if secret, ok := resolved[key]; ok {
			output = appendShellQuoted(output, secret)
		} else {
			output = appendShellQuoted(output, []byte(values[key]))
		}
		output = append(output, '\n')
	}
	written, err := writer.Write(output)
	if err != nil {
		return fmt.Errorf("write shell exports: %w", err)
	}
	if written != len(output) {
		return io.ErrShortWrite
	}
	return nil
}

func appendShellQuoted(destination, value []byte) []byte {
	destination = append(destination, '\'')
	for _, character := range value {
		if character == '\'' {
			destination = append(destination, '\'', '\\', '\'', '\'')
		} else {
			destination = append(destination, character)
		}
	}
	return append(destination, '\'')
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func containsNUL(value []byte) bool {
	for _, character := range value {
		if character == 0 {
			return true
		}
	}
	return false
}

func sortSecretStatuses(statuses []SecretReferenceStatus) {
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Variable < statuses[j].Variable })
}
