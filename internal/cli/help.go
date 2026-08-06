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

type helpOption struct {
	Syntax      string
	Description string
}

type helpTopic struct {
	Path        string
	Summary     string
	Usage       string
	Description string
	Commands    []helpCommand
	Options     []helpOption
	Examples    []string
}

var topLevelCommands = []helpCommand{
	{"projects", "Register and inspect projects"},
	{"open", "Open a project in a selected terminal backend"},
	{"sessions", "Inspect and control live tmux sessions"},
	{"worktrees", "Create and manage safe Git worktrees"},
	{"agents", "Start and control registered Agent tasks"},
	{"server", "Run the Dashboard as a background service"},
	{"dashboard", "Run the Dashboard in the foreground"},
	{"doctor", "Check configuration and runtime capabilities"},
	{"config", "Validate Workbench configuration"},
	{"migrate", "Import projects from legacy configuration"},
	{"compatibility", "Record legacy client compatibility use"},
	{"completion", "Generate shell completion scripts"},
	{"help", "Show help for a command"},
}

var helpTopics = buildHelpTopics()

func buildHelpTopics() map[string]helpTopic {
	topics := map[string]helpTopic{}
	addGroup := func(path, summary string, commands ...helpCommand) {
		topics[path] = helpTopic{Path: path, Summary: summary, Usage: "wb " + path + " <command> [options]", Commands: commands}
	}
	addLeaf := func(path, summary, usage, description string, options []helpOption, examples ...string) {
		topics[path] = helpTopic{Path: path, Summary: summary, Usage: usage, Description: description, Options: options, Examples: examples}
	}

	addGroup("projects", "Register and inspect projects",
		helpCommand{"list", "List registered projects"}, helpCommand{"show", "Show one project"},
		helpCommand{"add", "Register a project directory"}, helpCommand{"remove", "Remove only the registry entry"})
	addLeaf("projects list", "List registered projects", "wb projects list [--json]",
		"Reads the project registry without changing it.", jsonOption(), "wb projects list", "wb projects list --json")
	addLeaf("projects show", "Show one project", "wb projects show <id> [--json]",
		"Shows the canonical path, profile, backend, and editor stored for a project.", jsonOption(), "wb projects show alpha")
	addLeaf("projects add", "Register a project directory", "wb projects add <path> [--id <id>] [--profile <profile>]",
		"Canonicalizes the directory and rejects duplicate IDs or paths. It never creates or modifies the repository.",
		[]helpOption{{"--id <id>", "Use a stable project ID instead of deriving one"}, {"--profile <profile>", "Assign a Workbench profile"}},
		"wb projects add ~/src/alpha --id alpha")
	addLeaf("projects remove", "Remove a project registry entry", "wb projects remove <id>",
		"Removes only Workbench registry state. The repository directory is always preserved.", nil, "wb projects remove alpha")

	addLeaf("open", "Open a registered project",
		"wb open <project-id> [--backend <backend>] [--window <target>] [--terminal-mode <mode>]",
		"Selects a backend, then opens the project. An explicit unavailable backend fails instead of silently changing behavior.",
		[]helpOption{{"--backend <backend>", "auto, cmux, windows-terminal, tmux, or shell"}, {"--window <target>", "Windows Terminal: last, new, ID, or name"}, {"--terminal-mode <mode>", "tab, split-auto, split-horizontal, or split-vertical"}},
		"wb open alpha", "wb open alpha --backend tmux")

	addGroup("sessions", "Inspect and control live tmux sessions",
		helpCommand{"list", "List live sessions and ownership"}, helpCommand{"show", "Show one live session"},
		helpCommand{"jump", "Attach or switch to a session"}, helpCommand{"adopt", "Explicitly adopt a matching legacy session"},
		helpCommand{"stop", "Stop a verified managed session"})
	addLeaf("sessions list", "List live tmux sessions", "wb sessions list [--json]",
		"Classifies each session as managed, legacy, or foreign and reports clients, windows, and paths.", jsonOption())
	addLeaf("sessions show", "Show one live tmux session", "wb sessions show <session-name> [--json]",
		"Reads live tmux ownership metadata without mutating the session.", jsonOption())
	addLeaf("sessions jump", "Attach or switch to a tmux session", "wb sessions jump <session-name>",
		"Switches the current tmux client, or attaches the current terminal when outside tmux.", nil)
	addLeaf("sessions adopt", "Adopt a matching legacy tmux session", "wb sessions adopt <project-id> [--json]",
		"Writes ownership metadata only when the exact session name and first pane path match the registered project.", jsonOption())
	addLeaf("sessions stop", "Stop a managed tmux session", "wb sessions stop <project-id> [--json]",
		"Revalidates project ID, canonical path, and ownership metadata immediately before stopping the session.", jsonOption())

	addGroup("worktrees", "Create and manage safe Git worktrees",
		helpCommand{"list", "List project worktrees"}, helpCommand{"create", "Create a managed linked worktree"},
		helpCommand{"remove", "Safely remove a managed worktree"})
	addLeaf("worktrees list", "List project worktrees", "wb worktrees list <project-id> [--json]",
		"Combines Git porcelain state with Workbench ownership records.", jsonOption())
	addLeaf("worktrees create", "Create a managed linked worktree", "wb worktrees create <project-id> <branch> [--base <ref>]",
		"Creates a deterministic linked worktree without forcing branch operations.", []helpOption{{"--base <ref>", "Create the branch from this Git reference"}})
	addLeaf("worktrees remove", "Safely remove a managed worktree", "wb worktrees remove <worktree-id> [--delete-branch]",
		"Rejects external, locked, or dirty worktrees. Branch deletion requires exact confirmation and never uses force.",
		[]helpOption{{"--delete-branch", "Also safely delete the branch with git branch -d"}})

	addGroup("agents", "Start and control registered Agent tasks",
		helpCommand{"list", "List Agent tasks"}, helpCommand{"show", "Show one Agent task"},
		helpCommand{"start", "Start a Codex or Claude task"}, helpCommand{"jump", "Jump to a verified active task"},
		helpCommand{"stop", "Stop a verified active task"})
	addLeaf("agents list", "List Agent tasks", "wb agents list [--project <id>] [--json]",
		"Lists registry-backed tasks and reconciles their runtime state.",
		[]helpOption{{"--project <id>", "Filter by project ID"}, {"--json", "Emit one schema-v1 JSON envelope"}})
	addLeaf("agents show", "Show one Agent task", "wb agents show <task-id> [--json]",
		"Shows lifecycle state, backend identity, working directory, and timestamps.", jsonOption())
	addLeaf("agents start", "Start a registered Agent task",
		"wb agents start <project-id> --agent codex|claude [--worktree <id>] [--backend <backend>]",
		"Starts only the fixed Codex or Claude executable and records backend ownership before returning.",
		[]helpOption{{"--agent <kind>", "Required: codex or claude"}, {"--worktree <id>", "Run in a registered managed worktree"}, {"--backend <backend>", "Override automatic backend selection"}})
	addLeaf("agents jump", "Jump to a verified Agent task", "wb agents jump <task-id>",
		"Revalidates the registered backend reference before switching or attaching.", nil)
	addLeaf("agents stop", "Stop a verified Agent task", "wb agents stop <task-id>",
		"Stops only a runtime object whose ownership still matches the registered task.", nil)

	addGroup("server", "Run the Dashboard as a background service",
		helpCommand{"start", "Start a managed background server"}, helpCommand{"status", "Inspect the managed server"},
		helpCommand{"stop", "Gracefully stop the managed server"})
	addLeaf("server start", "Start the Dashboard in the background",
		"wb server start [--open <target>] [--port <port>] [--json]",
		"Starts a detached loopback server, records private instance metadata, and returns after readiness is verified.",
		[]helpOption{{"--open <target>", "auto, cmux, browser, or none; default none"}, {"--port <port>", "Loopback port 0-65535; default 0"}, {"--json", "Emit one schema-v1 JSON envelope"}},
		"wb server start", "wb server start --open browser")
	addLeaf("server status", "Inspect the managed background server", "wb server status [--json]",
		"Validates the recorded PID and private loopback instance identity before reporting status.", jsonOption())
	addLeaf("server stop", "Gracefully stop the managed background server", "wb server stop [--json]",
		"Uses the private loopback control token and never kills a process based only on a reused PID.", jsonOption())

	addLeaf("dashboard", "Run the Dashboard in the foreground", "wb dashboard [--open <target>] [--port <port>]",
		"Runs the loopback Dashboard until Ctrl-C or SIGTERM.",
		[]helpOption{{"--open <target>", "auto, cmux, browser, or none"}, {"--port <port>", "Loopback port 0-65535; default 0"}})
	addLeaf("doctor", "Check Workbench capabilities", "wb doctor [--profile <name>] [--json] [--strict]",
		"Checks configuration, state, platform tools, and backend availability with recovery guidance.",
		[]helpOption{{"--profile <name>", "Check a specific profile"}, {"--json", "Emit one schema-v1 JSON envelope"}, {"--strict", "Treat unavailable optional capabilities as failure"}})
	addGroup("config", "Validate Workbench configuration", helpCommand{"validate", "Validate config, profile, and project registry"})
	addLeaf("config validate", "Validate Workbench configuration", "wb config validate",
		"Strictly validates schemas, unknown fields, profiles, and the project registry.", nil)

	migrateOptions := []helpOption{{"--check", "Preview without writing"}, {"--apply", "Apply the reviewed migration"}, {"--file <path>", "Override the legacy source file"}, {"--profile <profile>", "Assign imported projects to a profile"}}
	addLeaf("migrate", "Import projects from tmux-sessionizer",
		"wb migrate [sessionizer] --check|--apply [--file <path>] [--profile <profile>]",
		"Plans or applies a migration from the legacy directory list. Check mode never writes.", migrateOptions,
		"wb migrate --check", "wb migrate sessionizer --apply")
	addLeaf("migrate sessionizer", "Import projects from tmux-sessionizer",
		"wb migrate sessionizer --check|--apply [--file <path>] [--profile <profile>]",
		"Uses the explicit tmux-sessionizer source type.", migrateOptions)

	addGroup("compatibility", "Record legacy client compatibility use",
		helpCommand{"observe", "Record one allowlisted compatibility observation"})
	addLeaf("compatibility observe", "Record compatibility use",
		"wb compatibility observe --client <client> --feature <feature> --source <source>",
		"Records only externally allowlisted client, feature, and source tuples.",
		[]helpOption{{"--client <client>", "Legacy client name"}, {"--feature <feature>", "Observed feature"}, {"--source <source>", "Compatibility source"}})

	addGroup("completion", "Generate shell completion scripts", helpCommand{"zsh", "Generate zsh completion for wb"})
	addLeaf("completion zsh", "Generate zsh completion", "wb completion zsh",
		"Writes a zsh completion function to stdout. make install installs it and registers it from ~/.zshrc.", nil,
		"wb completion zsh > ~/.local/share/zsh/site-functions/_wb")
	return topics
}

func jsonOption() []helpOption {
	return []helpOption{{"--json", "Emit one schema-v1 JSON envelope"}}
}

func handleHelp(args []string, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 {
		writeTopLevelHelp(stdout)
		return true, ExitOK
	}
	if args[0] == "help" {
		return true, writeHelpTopic(args[1:], stdout, stderr)
	}
	for index, argument := range args {
		if argument == "--help" || argument == "-h" || argument == "help" {
			return true, writeHelpTopic(resolveHelpPath(args[:index]), stdout, stderr)
		}
	}
	return false, ExitOK
}

func resolveHelpPath(args []string) []string {
	for end := len(args); end > 0; end-- {
		candidate := args[:end]
		if _, found := helpTopics[strings.Join(candidate, " ")]; found {
			return candidate
		}
	}
	return args
}

func writeHelpTopic(path []string, stdout, stderr io.Writer) int {
	if len(path) == 0 {
		writeTopLevelHelp(stdout)
		return ExitOK
	}
	key := strings.Join(path, " ")
	topic, found := helpTopics[key]
	if !found {
		fmt.Fprintf(stderr, "wb: no help topic for %q\n", key)
		return ExitArgument
	}
	writeTopic(stdout, topic)
	return ExitOK
}

func writeTopLevelHelp(writer io.Writer) {
	fmt.Fprintln(writer, "wb - local workbench control plane")
	fmt.Fprintln(writer, "\nUsage:\n  wb <command> [arguments]")
	writeCommands(writer, topLevelCommands)
	fmt.Fprintln(writer, "\nOptions:\n  -h, --help  Show help")
	fmt.Fprintln(writer, "\nRun 'wb <command> --help' for details about a command.")
}

func writeTopic(writer io.Writer, topic helpTopic) {
	fmt.Fprintf(writer, "wb %s - %s\n\nUsage:\n  %s\n", topic.Path, topic.Summary, topic.Usage)
	if topic.Description != "" {
		fmt.Fprintf(writer, "\n%s\n", topic.Description)
	}
	if len(topic.Commands) > 0 {
		writeCommands(writer, topic.Commands)
	}
	fmt.Fprintln(writer, "\nOptions:")
	for _, option := range topic.Options {
		fmt.Fprintf(writer, "  %-28s %s\n", option.Syntax, option.Description)
	}
	fmt.Fprintln(writer, "  -h, --help                  Show help")
	if len(topic.Examples) > 0 {
		fmt.Fprintln(writer, "\nExamples:")
		for _, example := range topic.Examples {
			fmt.Fprintf(writer, "  %s\n", example)
		}
	}
	if len(topic.Commands) > 0 {
		fmt.Fprintf(writer, "\nRun 'wb %s <command> --help' for command details.\n", topic.Path)
	}
}

func writeCommands(writer io.Writer, commands []helpCommand) {
	fmt.Fprintln(writer, "\nCommands:")
	for _, command := range commands {
		fmt.Fprintf(writer, "  %-16s %s\n", command.Name, command.Summary)
	}
}
