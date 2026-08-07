package cli

import (
	"fmt"
	"io"
	"strings"
)

type helpCommand struct {
	Name    string
	Summary string
}

type helpTopic struct {
	Path     string
	Summary  string
	Usage    string
	Commands []helpCommand
}

var topLevelCommands = []helpCommand{
	{Name: "projects", Summary: "Register projects and connect environments"},
	{Name: "env", Summary: "Manage runtime environment metadata"},
	{Name: "secrets", Summary: "Manage the encrypted local Secret store"},
	{Name: "open", Summary: "Open a project in a terminal backend"},
	{Name: "sessions", Summary: "Observe and manage tmux sessions"},
	{Name: "tasks", Summary: "Inspect managed and observed tasks"},
	{Name: "agents", Summary: "Start and control Agent tasks"},
	{Name: "workflows", Summary: "Run allowlisted detached workflows"},
	{Name: "worktrees", Summary: "Manage safe Git worktrees"},
	{Name: "overview", Summary: "Summarize the operations console"},
	{Name: "server", Summary: "Run the Dashboard in the background"},
	{Name: "dashboard", Summary: "Run the Dashboard in the foreground"},
	{Name: "doctor", Summary: "Check configuration and capabilities"},
	{Name: "config", Summary: "Validate Workbench configuration"},
	{Name: "migrate", Summary: "Import legacy project configuration"},
	{Name: "compatibility", Summary: "Record compatibility observations"},
	{Name: "completion", Summary: "Generate shell completion"},
	{Name: "help", Summary: "Show command help"},
}

var helpTopics = map[string]helpTopic{
	"projects":      {Path: "projects", Summary: "Register projects and connect environments", Usage: "wb projects <command> [options]", Commands: []helpCommand{{"list", "List projects"}, {"show", "Show a project"}, {"add", "Register a project"}, {"set-environment", "Connect or clear an Environment"}, {"remove", "Remove only the registry entry"}}},
	"env":           {Path: "env", Summary: "Manage runtime environment metadata", Usage: "wb env <command> [options]", Commands: []helpCommand{{"list", "List Environments"}, {"show", "Show an Environment"}, {"add", "Create or replace metadata"}, {"remove", "Remove an Environment"}, {"health", "Check references"}, {"export", "Emit shell exports"}, {"migrate", "Migrate wenv.d presets"}}},
	"secrets":       {Path: "secrets", Summary: "Manage the encrypted local Secret store", Usage: "wb secrets <command> [options]", Commands: []helpCommand{{"init", "Initialize the store"}, {"list", "List names without values"}, {"set", "Store a value from stdin or hidden TTY"}, {"get", "Read a value to stdout"}, {"remove", "Remove a service or field"}, {"migrate", "Migrate the legacy sec store"}}},
	"sessions":      {Path: "sessions", Summary: "Observe and manage tmux sessions", Usage: "wb sessions <command> [options]", Commands: []helpCommand{{"list", "List the session/window/pane hierarchy"}, {"show", "Show one session and ownership"}, {"jump", "Jump to an exact pane ID"}, {"attach", "Attach or switch to a session"}, {"adopt", "Adopt a matching legacy project session"}, {"stop", "Stop a verified managed project session"}}},
	"tasks":         {Path: "tasks", Summary: "Inspect managed and observed tasks", Usage: "wb tasks <command> [options]", Commands: []helpCommand{{"list", "List unified tasks"}, {"show", "Show one task"}, {"jump", "Jump to a task"}, {"stop", "Stop an owned task"}}},
	"agents":        {Path: "agents", Summary: "Start and control Agent tasks", Usage: "wb agents <command> [options]", Commands: []helpCommand{{"list", "List Agent records"}, {"show", "Show one Agent"}, {"start", "Start Codex or Claude"}, {"jump", "Jump to an Agent"}, {"stop", "Stop an owned Agent"}}},
	"workflows":     {Path: "workflows", Summary: "Run allowlisted detached workflows", Usage: "wb workflows <command> [options]", Commands: []helpCommand{{"catalog", "List applicable workflows"}, {"run", "Start an allowlisted workflow"}, {"history", "List metadata-only history"}, {"show", "Show a workflow run"}}},
	"worktrees":     {Path: "worktrees", Summary: "Manage safe Git worktrees", Usage: "wb worktrees <command> [options]", Commands: []helpCommand{{"list", "List worktrees"}, {"create", "Create a managed worktree"}, {"remove", "Remove a verified managed worktree"}}},
	"server":        {Path: "server", Summary: "Run the Dashboard in the background", Usage: "wb server <command> [options]", Commands: []helpCommand{{"start", "Start a managed background server"}, {"status", "Inspect the managed server"}, {"stop", "Gracefully stop the managed server"}}},
	"config":        {Path: "config", Summary: "Validate Workbench configuration", Usage: "wb config validate", Commands: []helpCommand{{"validate", "Validate schemas and registries"}}},
	"compatibility": {Path: "compatibility", Summary: "Record compatibility observations", Usage: "wb compatibility observe [options]", Commands: []helpCommand{{"observe", "Record one allowlisted observation"}}},
	"completion":    {Path: "completion", Summary: "Generate shell completion", Usage: "wb completion zsh", Commands: []helpCommand{{"zsh", "Print zsh completion to stdout"}}},
}

var leafUsage = map[string]string{
	"projects list": "wb projects list [--json]", "projects show": "wb projects show <id> [--json]", "projects add": "wb projects add <path> [--id <id>] [--profile <profile>] [--environment <id>]", "projects set-environment": "wb projects set-environment <project-id> <environment-id|none>", "projects remove": "wb projects remove <id>",
	"env list": "wb env list [--json]", "env show": "wb env show <id> [--json]", "env add": "wb env add <id> [metadata options] [--json]", "env remove": "wb env remove <id> [--json]", "env health": "wb env health <id> [--json]", "env export": "wb env export <id> [--resolve-secrets] [--json]", "env migrate": "wb env migrate check|apply [--source <wenv.d>] [--json]",
	"secrets init": "wb secrets init [--json]", "secrets list": "wb secrets list [service] [--json]", "secrets set": "wb secrets set <service> <field> [--replace] [--json]", "secrets get": "wb secrets get <service> [field]", "secrets remove": "wb secrets remove <service> [field] [--yes] [--json]", "secrets migrate": "wb secrets migrate check|apply [--json]",
	"open": "wb open <project-id> [--backend <backend>] [--session none|tmux] [terminal options]", "sessions list": "wb sessions list [--json]", "sessions show": "wb sessions show <session-name> [--json]", "sessions jump": "wb sessions jump <pane-id>", "sessions attach": "wb sessions attach <session-name>", "sessions adopt": "wb sessions adopt <project-id> [--json]", "sessions stop": "wb sessions stop <project-id> [--json]",
	"tasks list": "wb tasks list [--project <id>] [--json]", "tasks show": "wb tasks show <task-id> [--json]", "tasks jump": "wb tasks jump <task-id>", "tasks stop": "wb tasks stop <task-id>",
	"agents list": "wb agents list [--project <id>] [--json]", "agents show": "wb agents show <task-id> [--json]", "agents start": "wb agents start <project-id> --agent codex|claude [options]", "agents jump": "wb agents jump <task-id>", "agents stop": "wb agents stop <task-id>",
	"workflows catalog": "wb workflows catalog [--project <id>] [--json]", "workflows run": "wb workflows run <workflow-id> --project <id> [environment options] [--json]", "workflows history": "wb workflows history [--project <id>] [--json]", "workflows show": "wb workflows show <run-id> [--json]",
	"worktrees list": "wb worktrees list <project-id> [--json]", "worktrees create": "wb worktrees create <project-id> <branch> [--base <ref>]", "worktrees remove": "wb worktrees remove <worktree-id> [--delete-branch]",
	"overview": "wb overview [--json]", "server start": "wb server start [--open auto|cmux|browser|none] [--port <port>] [--json]", "server status": "wb server status [--json]", "server stop": "wb server stop [--json]", "dashboard": "wb dashboard [--open auto|cmux|browser|none] [--port <port>]", "doctor": "wb doctor [--profile <name>] [--json] [--strict]", "config validate": "wb config validate", "migrate": "wb migrate [sessionizer] --check|--apply [options]", "compatibility observe": "wb compatibility observe --client <client> --feature <feature> --source <source>", "completion zsh": "wb completion zsh",
}

func handleHelp(args []string, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 {
		writeTopLevelHelp(stdout)
		return true, ExitOK
	}
	if args[0] == "help" {
		return true, writeHelp(args[1:], stdout, stderr)
	}
	for index, argument := range args {
		if argument == "--help" || argument == "-h" || argument == "help" {
			return true, writeHelp(args[:index], stdout, stderr)
		}
	}
	return false, ExitOK
}

func writeHelp(path []string, stdout, stderr io.Writer) int {
	if len(path) == 0 {
		writeTopLevelHelp(stdout)
		return ExitOK
	}
	key := strings.Join(path, " ")
	if usage, found := leafUsage[key]; found {
		fmt.Fprintf(stdout, "wb %s\n\nUsage:\n  %s\n", key, usage)
		return ExitOK
	}
	if topic, found := helpTopics[key]; found {
		fmt.Fprintf(stdout, "wb %s - %s\n\nUsage:\n  %s\n", topic.Path, topic.Summary, topic.Usage)
		writeHelpCommands(stdout, topic.Commands)
		fmt.Fprintf(stdout, "\nRun 'wb %s <command> --help' for command details.\n", topic.Path)
		return ExitOK
	}
	fmt.Fprintf(stderr, "wb: no help topic for %q\n", key)
	return ExitArgument
}

func writeTopLevelHelp(writer io.Writer) {
	fprintln(writer, "wb - local workbench control plane")
	fprintln(writer, "\nUsage:\n  wb <command> [arguments]")
	writeHelpCommands(writer, topLevelCommands)
	fprintln(writer, "\nOptions:\n  -h, --help  Show help")
	fprintln(writer, "\nRun 'wb <command> --help' for command details.")
}

func writeHelpCommands(writer io.Writer, commands []helpCommand) {
	fprintln(writer, "\nCommands:")
	for _, command := range commands {
		fmt.Fprintf(writer, "  %-16s %s\n", command.Name, command.Summary)
	}
}

func fprintln(writer io.Writer, value string) { _, _ = fmt.Fprintln(writer, value) }
