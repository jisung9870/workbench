package clipboard

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
)

type command struct {
	name string
	args []string
}

func Write(value []byte) error {
	selected, err := writeCommand()
	if err != nil {
		return err
	}
	cmd := exec.Command(selected.name, selected.args...)
	cmd.Stdin = bytes.NewReader(value)
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write clipboard with %s: %w", selected.name, err)
	}
	return nil
}

func Read() ([]byte, error) {
	selected, err := readCommand()
	if err != nil {
		return nil, err
	}
	output, err := exec.Command(selected.name, selected.args...).Output()
	if err != nil {
		return nil, fmt.Errorf("read clipboard with %s: %w", selected.name, err)
	}
	return output, nil
}

func writeCommand() (command, error) {
	if runtime.GOOS == "darwin" {
		return available(command{name: "pbcopy"})
	}
	if runtime.GOOS == "windows" {
		return available(command{name: "powershell.exe", args: []string{"-NoProfile", "-NonInteractive", "-Command", "$input | Set-Clipboard"}})
	}
	return firstAvailable(
		command{name: "wl-copy"},
		command{name: "xclip", args: []string{"-selection", "clipboard"}},
		command{name: "clip.exe"},
	)
}

func readCommand() (command, error) {
	if runtime.GOOS == "darwin" {
		return available(command{name: "pbpaste"})
	}
	if runtime.GOOS == "windows" {
		return available(command{name: "powershell.exe", args: []string{"-NoProfile", "-NonInteractive", "-Command", "Get-Clipboard -Raw"}})
	}
	return firstAvailable(
		command{name: "wl-paste", args: []string{"-n"}},
		command{name: "xclip", args: []string{"-selection", "clipboard", "-o"}},
		command{name: "powershell.exe", args: []string{"-NoProfile", "-NonInteractive", "-Command", "Get-Clipboard -Raw"}},
	)
}

func firstAvailable(commands ...command) (command, error) {
	for _, candidate := range commands {
		if _, err := exec.LookPath(candidate.name); err == nil {
			return candidate, nil
		}
	}
	return command{}, errors.New("no supported clipboard command found (pbcopy/pbpaste, wl-copy/wl-paste, xclip, or Windows clipboard tools)")
}

func available(candidate command) (command, error) {
	if _, err := exec.LookPath(candidate.name); err != nil {
		return command{}, fmt.Errorf("clipboard command %s is unavailable", candidate.name)
	}
	return candidate, nil
}
