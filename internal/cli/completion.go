package cli

import (
	_ "embed"
	"fmt"
	"io"
)

//go:embed completions/_wb
var zshCompletion string

func handleCompletion(args []string, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 || args[0] != "completion" {
		return false, ExitOK
	}
	if len(args) == 2 && args[1] == "zsh" {
		fmt.Fprint(stdout, zshCompletion)
		return true, ExitOK
	}
	fmt.Fprintln(stderr, "wb: usage: wb completion zsh")
	return true, ExitArgument
}
