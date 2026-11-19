// Package publish puts a project on GitHub or GitLab (DEVBOX_FORGE). A
// GitLab project can also be mirrored to GitHub: GitLab is then the source of
// truth, GitHub a read-only copy kept in sync by a GitLab push mirror. It
// also checks those mirrors and caches the result for the shell's notices.
package publish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrReported means the problem was already printed.
var ErrReported = errors.New("reported")

// ErrNotFound is a GitLab or GitHub API 404.
var ErrNotFound = errors.New("not found")

// Runner runs external commands (git, glab, gh). Secrets go in stdin, never
// in args, so they can't show up in errors or the process list.
type Runner interface {
	// Output runs name in dir with stdin and returns its trimmed stdout.
	Output(ctx context.Context, dir string, stdin []byte, name string, args ...string) (string, error)
}

// Exec runs commands for real.
type Exec struct{}

// Output runs name in dir. On failure the error includes the command and
// what it printed.
func (Exec) Output(ctx context.Context, dir string, stdin []byte, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if out := strings.TrimSpace(stdout.String()); out != "" && !strings.Contains(msg, out) {
			msg = strings.TrimSpace(msg + " " + out)
		}
		if msg != "" {
			return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
		}
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// apiError turns a failed glab api or gh api call into ErrNotFound for a 404.
func apiError(what string, err error) error {
	if strings.Contains(err.Error(), "(HTTP 404)") {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// redact removes secret from err's text.
func redact(err error, secret string) error {
	if err == nil || secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "***"))
}
