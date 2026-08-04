package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jisung9870/workbench/internal/backend"
)

func Open(ctx context.Context, executor backend.Executor, goos string, isWSL bool, target, dashboardURL string) error {
	if target == "none" {
		return nil
	}
	if target == "auto" {
		if goos == "darwin" {
			if _, err := executor.LookPath("cmux"); err == nil {
				target = "cmux"
			} else {
				target = "browser"
			}
		} else {
			target = "browser"
		}
	}
	name := ""
	args := []string{}
	switch target {
	case "cmux":
		name = "cmux"
		args = []string{"browser", "open", dashboardURL}
	case "browser":
		switch {
		case goos == "darwin":
			name, args = "open", []string{dashboardURL}
		case goos == "windows":
			name, args = "rundll32", []string{"url.dll,FileProtocolHandler", dashboardURL}
		case isWSL:
			if path, err := executor.LookPath("wslview"); err == nil {
				name, args = path, []string{dashboardURL}
			} else {
				name, args = "cmd.exe", []string{"/c", "start", "", dashboardURL}
			}
		default:
			name, args = "xdg-open", []string{dashboardURL}
		}
	default:
		return fmt.Errorf("invalid dashboard open target %q", target)
	}
	command, err := executor.LookPath(name)
	if err != nil {
		return fmt.Errorf("dashboard %s opener %q was not found", target, name)
	}
	openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := executor.Run(openCtx, backend.ProcessRequest{Name: command, Args: args})
	if err != nil {
		detail := strings.TrimSpace(result.Stderr)
		if detail != "" {
			return fmt.Errorf("open dashboard with %s: %s", target, detail)
		}
		return fmt.Errorf("open dashboard with %s: %w", target, err)
	}
	return nil
}
