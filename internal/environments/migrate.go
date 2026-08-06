package environments

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type MigrationStatus string

const (
	MigrationReady       MigrationStatus = "ready"
	MigrationExisting    MigrationStatus = "existing"
	MigrationUnsupported MigrationStatus = "unsupported"
	MigrationConflict    MigrationStatus = "conflict"
)

type MigrationItem struct {
	Source      string          `json:"source"`
	ID          string          `json:"id"`
	Status      MigrationStatus `json:"status"`
	Environment *Environment    `json:"-"`
	Issues      []string        `json:"issues"`
}

type MigrationPlan struct {
	SourceDir string          `json:"source_dir"`
	CanApply  bool            `json:"can_apply"`
	Items     []MigrationItem `json:"items"`
	Ready     int             `json:"ready"`
	Existing  int             `json:"existing"`
	Blocked   int             `json:"blocked"`
}

func PlanWenv(sourceDir string, store *Store) (MigrationPlan, error) {
	absolute, err := filepath.Abs(sourceDir)
	if err != nil {
		return MigrationPlan{}, fmt.Errorf("resolve wenv source: %w", err)
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return MigrationPlan{}, fmt.Errorf("read wenv source %s: %w", absolute, err)
	}
	registry, err := store.Load()
	if err != nil {
		return MigrationPlan{}, err
	}
	existing := map[string]Environment{}
	for _, environment := range registry.Environments {
		existing[environment.ID] = environment
	}
	plan := MigrationPlan{SourceDir: absolute, CanApply: true, Items: []MigrationItem{}}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		item := MigrationItem{Source: filepath.Join(absolute, entry.Name()), ID: entry.Name(), Issues: []string{}}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			item.Status = MigrationUnsupported
			if infoErr != nil {
				item.Issues = append(item.Issues, infoErr.Error())
			} else {
				item.Issues = append(item.Issues, "preset is not a regular file")
			}
			plan.Items = append(plan.Items, item)
			plan.Blocked++
			plan.CanApply = false
			continue
		}
		if !ValidID(item.ID) {
			item.Status = MigrationUnsupported
			item.Issues = append(item.Issues, "filename is not a valid Workbench environment id")
			plan.Items = append(plan.Items, item)
			plan.Blocked++
			plan.CanApply = false
			continue
		}
		file, openErr := os.Open(item.Source)
		if openErr != nil {
			item.Status = MigrationUnsupported
			item.Issues = append(item.Issues, openErr.Error())
			plan.Items = append(plan.Items, item)
			plan.Blocked++
			plan.CanApply = false
			continue
		}
		environment, parseErr := ParseWenv(item.ID, file)
		closeErr := file.Close()
		if parseErr != nil || closeErr != nil {
			item.Status = MigrationUnsupported
			if parseErr != nil {
				item.Issues = append(item.Issues, parseErr.Error())
			}
			if closeErr != nil {
				item.Issues = append(item.Issues, closeErr.Error())
			}
			plan.Items = append(plan.Items, item)
			plan.Blocked++
			plan.CanApply = false
			continue
		}
		item.Environment = &environment
		if current, found := existing[item.ID]; found {
			if Equal(current, environment) {
				item.Status = MigrationExisting
				plan.Existing++
			} else {
				item.Status = MigrationConflict
				item.Issues = append(item.Issues, "environment id already exists with different values")
				plan.Blocked++
				plan.CanApply = false
			}
		} else {
			item.Status = MigrationReady
			plan.Ready++
		}
		plan.Items = append(plan.Items, item)
	}
	sort.Slice(plan.Items, func(i, j int) bool { return plan.Items[i].ID < plan.Items[j].ID })
	return plan, nil
}

func ApplyWenv(plan MigrationPlan, store *Store) (string, error) {
	if !plan.CanApply {
		return "", &ConflictError{Message: "wenv migration is blocked by unsupported or conflicting presets"}
	}
	ready := make([]Environment, 0, plan.Ready)
	for _, item := range plan.Items {
		if item.Status == MigrationReady && item.Environment != nil {
			ready = append(ready, *item.Environment)
		}
	}
	if len(ready) == 0 {
		return "", nil
	}
	// Re-plan immediately before mutation so a concurrent registry change cannot
	// silently overwrite a newly introduced ID.
	fresh, err := PlanWenv(plan.SourceDir, store)
	if err != nil {
		return "", err
	}
	freshReady := make(map[string]Environment, fresh.Ready)
	for _, item := range fresh.Items {
		if item.Status == MigrationReady && item.Environment != nil {
			freshReady[item.ID] = *item.Environment
		}
	}
	sourceUnchanged := fresh.CanApply && len(freshReady) == len(ready)
	for _, environment := range ready {
		candidate, found := freshReady[environment.ID]
		if !found || !Equal(environment, candidate) {
			sourceUnchanged = false
			break
		}
	}
	if !sourceUnchanged {
		return "", &ConflictError{Message: "wenv source or environment registry changed after migration check"}
	}
	return store.AddMany(ready)
}

func ParseWenv(id string, reader io.Reader) (Environment, error) {
	environment := Environment{ID: id, Exports: map[string]string{}}
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(reader)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "EXPORTS") {
			if _, duplicate := seen["EXPORTS"]; duplicate {
				return Environment{}, fmt.Errorf("line %d: duplicate EXPORTS assignment", lineNumber)
			}
			seen["EXPORTS"] = struct{}{}
			assignment, consumed, err := collectArray(line, scanner, &lineNumber)
			if err != nil {
				return Environment{}, fmt.Errorf("line %d: %w", lineNumber, err)
			}
			_ = consumed
			values, err := parseExports(assignment)
			if err != nil {
				return Environment{}, fmt.Errorf("line %d: %w", lineNumber, err)
			}
			for key, value := range values {
				if ReservedKey(key) {
					return Environment{}, fmt.Errorf("line %d: EXPORTS key %q must use its dedicated assignment", lineNumber, key)
				}
				environment.Exports[key] = value
			}
			continue
		}
		key, raw, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || !allowedPresetKey(key) {
			return Environment{}, fmt.Errorf("line %d: unsupported shell syntax or key", lineNumber)
		}
		if _, duplicate := seen[key]; duplicate {
			return Environment{}, fmt.Errorf("line %d: duplicate %s assignment", lineNumber, key)
		}
		seen[key] = struct{}{}
		value, err := parseScalar(stripShellComment(strings.TrimSpace(raw)))
		if err != nil {
			return Environment{}, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		switch key {
		case "AWS_PROFILE":
			environment.AWSProfile = value
		case "AWS_REGION":
			environment.AWSRegion = value
		case "KUBE_CONTEXT":
			environment.KubeContext = value
		case "KUBE_NAMESPACE":
			environment.KubeNamespace = value
		}
	}
	if err := scanner.Err(); err != nil {
		return Environment{}, fmt.Errorf("read preset: %w", err)
	}
	if err := ValidateRegistry(Registry{SchemaVersion: SchemaVersion, Environments: []Environment{environment}}); err != nil {
		return Environment{}, err
	}
	return environment, nil
}

func collectArray(first string, scanner *bufio.Scanner, lineNumber *int) (string, int, error) {
	text := first
	for !hasClosingParen(text) {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", 0, err
			}
			return "", 0, errors.New("unterminated EXPORTS array")
		}
		*lineNumber++
		text += "\n" + strings.TrimSpace(scanner.Text())
	}
	return text, *lineNumber, nil
}

func hasClosingParen(value string) bool {
	quote := rune(0)
	escaped := false
	for _, character := range value {
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && character == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == ')' {
			return true
		}
	}
	return false
}

func parseExports(assignment string) (map[string]string, error) {
	key, raw, found := strings.Cut(assignment, "=")
	if !found || strings.TrimSpace(key) != "EXPORTS" {
		return nil, errors.New("unsupported EXPORTS assignment")
	}
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "(") {
		return nil, errors.New("EXPORTS must be a Bash array")
	}
	end, err := matchingClose(raw)
	if err != nil {
		return nil, err
	}
	if trailing := strings.TrimSpace(raw[end+1:]); trailing != "" && !strings.HasPrefix(trailing, "#") {
		return nil, errors.New("unsupported syntax after EXPORTS array")
	}
	tokens, err := shellWords(raw[1:end])
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for index, token := range tokens {
		key, value, found := strings.Cut(token, "=")
		if !found {
			return nil, fmt.Errorf("invalid EXPORTS item at index %d: expected KEY=VALUE", index)
		}
		if !ValidVariableName(key) {
			return nil, fmt.Errorf("invalid EXPORTS item at index %d: key is invalid", index)
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("duplicate EXPORTS key %q", key)
		}
		result[key] = value
	}
	return result, nil
}

func matchingClose(raw string) (int, error) {
	quote := byte(0)
	escaped := false
	for i := 1; i < len(raw); i++ {
		character := raw[i]
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && character == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == ')' {
			return i, nil
		}
	}
	return 0, errors.New("unterminated EXPORTS array")
}

func shellWords(value string) ([]string, error) {
	words := []string{}
	var word strings.Builder
	quote := rune(0)
	escaped := false
	started := false
	flush := func() {
		if started {
			words = append(words, word.String())
			word.Reset()
			started = false
		}
	}
	for _, character := range value {
		if escaped {
			word.WriteRune(character)
			escaped = false
			started = true
			continue
		}
		if quote == '"' && character == '\\' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if quote == '"' && (character == '$' || character == '`') {
				return nil, fmt.Errorf("unsupported shell expansion %q", character)
			}
			if character == quote {
				quote = 0
			} else {
				word.WriteRune(character)
			}
			started = true
			continue
		}
		switch character {
		case '\'', '"':
			quote = character
			started = true
		case ' ', '\t', '\r', '\n':
			flush()
		case ';', '|', '&', '<', '>', '$', '`', '(', ')', '*', '?', '[', ']', '~', '{', '}':
			return nil, fmt.Errorf("unsupported shell metacharacter %q", character)
		case '\\':
			escaped = true
			started = true
		default:
			word.WriteRune(character)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, errors.New("unterminated quote or escape")
	}
	flush()
	return words, nil
}

func parseScalar(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	words, err := shellWords(raw)
	if err != nil {
		return "", err
	}
	if len(words) != 1 {
		return "", errors.New("scalar assignment must contain exactly one value")
	}
	return words[0], nil
}

func stripShellComment(value string) string {
	quote := rune(0)
	escaped := false
	for index, character := range value {
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && character == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == '#' && (index == 0 || value[index-1] == ' ' || value[index-1] == '\t') {
			return strings.TrimSpace(value[:index])
		}
	}
	return value
}

func allowedPresetKey(key string) bool {
	return key == "AWS_PROFILE" || key == "AWS_REGION" || key == "KUBE_CONTEXT" || key == "KUBE_NAMESPACE"
}

func Equal(left, right Environment) bool {
	if left.ID != right.ID || left.AWSProfile != right.AWSProfile || left.AWSRegion != right.AWSRegion || left.KubeContext != right.KubeContext || left.KubeNamespace != right.KubeNamespace || len(left.Exports) != len(right.Exports) {
		return false
	}
	for key, value := range left.Exports {
		if right.Exports[key] != value {
			return false
		}
	}
	return true
}
