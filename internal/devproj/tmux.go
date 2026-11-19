package devproj

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Jenish-1235/dsbox/internal/sysexec"
)

// Session is the tmux session that holds one window per running project.
const Session = "dev"

// Target is the tmux target for a project's window, matched exactly.
func Target(name string) string { return "=" + Session + ":=" + name }

// Running returns the names of the windows in the dev session. No tmux server
// or no session means nothing is running.
func Running(ctx context.Context) map[string]bool {
	out, err := exec.CommandContext(ctx, "tmux", "list-windows", "-t", "="+Session, "-F", "#{window_name}").Output()
	running := map[string]bool{}
	if err != nil {
		return running
	}
	for line := range strings.Lines(string(out)) {
		if name := strings.TrimSpace(line); name != "" {
			running[name] = true
		}
	}
	return running
}

// startWindow opens a window for name in the dev session (creating the session
// if needed), running script in dir with env, and pipes its output to logFile.
func startWindow(ctx context.Context, name, dir, script, logFile string, env []string) error {
	var envArgs []string
	for _, kv := range env {
		envArgs = append(envArgs, "-e", kv)
	}
	var args []string
	if exec.CommandContext(ctx, "tmux", "has-session", "-t", "="+Session).Run() == nil {
		args = append([]string{"new-window", "-d", "-t", "=" + Session + ":", "-n", name, "-c", dir}, envArgs...)
	} else {
		args = append([]string{"new-session", "-d", "-s", Session, "-n", name, "-c", dir}, envArgs...)
	}
	// The short sleep lets pipe-pane attach before the server prints anything.
	args = append(args, "sleep 0.5; "+script)
	if _, err := sysexec.Output(ctx, "", "tmux", args...); err != nil {
		return fmt.Errorf("start tmux window %s: %w", name, err)
	}
	if _, err := sysexec.Output(ctx, "", "tmux", "pipe-pane", "-o", "-t", Target(name), "cat >> "+shellQuote(logFile)); err != nil {
		return fmt.Errorf("capture output of %s: %w", name, err)
	}
	return nil
}

func interrupt(ctx context.Context, name string) error {
	if _, err := sysexec.Output(ctx, "", "tmux", "send-keys", "-t", Target(name), "C-c"); err != nil {
		return fmt.Errorf("stop %s: %w", name, err)
	}
	return nil
}

func killWindow(ctx context.Context, name string) error {
	if _, err := sysexec.Output(ctx, "", "tmux", "kill-window", "-t", Target(name)); err != nil {
		return fmt.Errorf("close window %s: %w", name, err)
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
