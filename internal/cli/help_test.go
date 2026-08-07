package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpCoversCurrentCommandGroups(t *testing.T) {
	for _, test := range []struct {
		args []string
		want []string
	}{
		{args: nil, want: []string{"wb <command>", "env", "secrets", "sessions", "workflows", "server"}},
		{args: []string{"sessions", "--help"}, want: []string{"attach", "adopt", "stop"}},
		{args: []string{"help", "workflows", "run"}, want: []string{"wb workflows run <workflow-id>", "--project"}},
		{args: []string{"secrets", "set", "--help"}, want: []string{"wb secrets set", "--replace"}},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(test.args, &stdout, &stderr); code != ExitOK {
			t.Fatalf("Run(%v) exit=%d stderr=%s", test.args, code, stderr.String())
		}
		for _, want := range test.want {
			if !strings.Contains(stdout.String(), want) {
				t.Fatalf("Run(%v) help missing %q:\n%s", test.args, want, stdout.String())
			}
		}
	}
}

func TestUnknownHelpTopicIsArgumentError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help", "missing"}, &stdout, &stderr); code != ExitArgument {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), `no help topic for "missing"`) {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestZshCompletionIncludesCurrentCommandsWithoutShellMutation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"completion", "zsh"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"#compdef wb", "env:Manage", "secrets:Manage", "sessions attach", "workflows:Run", "server:Run"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("completion missing %q", want)
		}
	}
	if strings.Contains(stdout.String(), ".zshrc") {
		t.Fatal("completion output must not modify or mention .zshrc")
	}
}
