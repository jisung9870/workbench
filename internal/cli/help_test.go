package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestHierarchicalHelp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative-path-that-help-must-ignore")
	tests := []struct {
		name       string
		args       []string
		contains   []string
		notContain []string
	}{
		{
			name:       "top level",
			args:       []string{"--help"},
			contains:   []string{"Usage:\n  wb <command> [arguments]", "projects         Register and inspect projects", "server           Run the Dashboard as a background service"},
			notContain: []string{"wb projects list [--json]", "--open <target>"},
		},
		{
			name:       "group",
			args:       []string{"projects", "--help"},
			contains:   []string{"wb projects <command> [options]", "add              Register a project directory", "wb projects <command> --help"},
			notContain: []string{"--id <id>"},
		},
		{
			name:     "leaf",
			args:     []string{"server", "start", "--help"},
			contains: []string{"wb server start [--open <target>] [--port <port>] [--json]", "--open <target>", "default none"},
		},
		{
			name:     "help alias",
			args:     []string{"help", "sessions", "adopt"},
			contains: []string{"wb sessions adopt <project-id> [--json]", "exact session name and first pane path"},
		},
		{
			name:     "help after positional",
			args:     []string{"projects", "show", "alpha", "--help"},
			contains: []string{"wb projects show <id> [--json]"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(test.args, &stdout, &stderr); code != ExitOK {
				t.Fatalf("help failed: code=%d stderr=%s", code, stderr.String())
			}
			for _, value := range test.contains {
				if !strings.Contains(stdout.String(), value) {
					t.Fatalf("help missing %q:\n%s", value, stdout.String())
				}
			}
			for _, value := range test.notContain {
				if strings.Contains(stdout.String(), value) {
					t.Fatalf("help unexpectedly contains %q:\n%s", value, stdout.String())
				}
			}
		})
	}
}

func TestNoArgumentsShowsTopLevelHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != ExitOK {
		t.Fatalf("no-argument help returned %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "wb <command> [arguments]") || stderr.Len() != 0 {
		t.Fatalf("unexpected no-argument help: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestUnknownHelpTopicFailsCleanly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help", "unknown"}, &stdout, &stderr); code != ExitArgument {
		t.Fatalf("unknown help topic returned %d", code)
	}
	if !strings.Contains(stderr.String(), "no help topic") || stdout.Len() != 0 {
		t.Fatalf("unexpected unknown help output: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestZshCompletionIncludesDocumentedCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"completion", "zsh"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("completion failed: code=%d stderr=%s", code, stderr.String())
	}
	script := stdout.String()
	if !strings.HasPrefix(script, "#compdef wb\n") {
		t.Fatalf("completion header missing: %s", script)
	}
	for _, command := range topLevelCommands {
		if !strings.Contains(script, "'"+command.Name+":") {
			t.Errorf("completion missing top-level command %q", command.Name)
		}
	}
	for _, value := range []string{"_wb_project_ids", "_wb_session_names", "_wb_agent_ids", "--terminal-mode", "--delete-branch"} {
		if !strings.Contains(script, value) {
			t.Errorf("completion missing %q", value)
		}
	}
}

func TestCompletionRejectsUnsupportedShell(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"completion", "bash"}, &stdout, &stderr); code != ExitArgument {
		t.Fatalf("unsupported completion returned %d", code)
	}
	if !strings.Contains(stderr.String(), "wb completion zsh") {
		t.Fatalf("missing completion recovery: %s", stderr.String())
	}
}
