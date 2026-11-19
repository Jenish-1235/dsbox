// Package sysexec runs external commands with readable errors.
package sysexec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Output runs name in dir and returns its trimmed stdout. On failure the error
// includes the command and its stderr.
func Output(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
		}
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Run runs name in dir with its output streamed to stdout and stderr.
func Run(ctx context.Context, dir string, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// Shell runs script with bash and reports whether it exited 0.
func Shell(ctx context.Context, script string) bool {
	return ShellEnv(ctx, nil, script)
}

// ShellEnv is Shell with env as the script's environment; nil means this
// process's environment.
func ShellEnv(ctx context.Context, env []string, script string) bool {
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = env
	return cmd.Run() == nil
}

// Exists reports whether name is on PATH.
func Exists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// UserBusEnv returns the environment plus what systemctl --user needs to find
// the user's systemd: XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS. They are
// missing under sudo -u, cron and SSM; they're only added when unset and the
// user's runtime folder exists.
func UserBusEnv() []string {
	return userBusEnv(os.Environ(), RuntimeDir(), func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
}

// RuntimeDir is the current user's systemd runtime folder, /run/user/<uid>.
func RuntimeDir() string { return fmt.Sprintf("/run/user/%d", os.Getuid()) }

// HasUserBus reports whether the user's systemd bus socket exists, which
// needs a login session or lingering (loginctl enable-linger).
func HasUserBus() bool {
	_, err := os.Stat(filepath.Join(RuntimeDir(), "bus"))
	return err == nil
}

func userBusEnv(environ []string, runtimeDir string, exists func(string) bool) []string {
	env := slices.Clone(environ)
	has := func(key string) bool {
		return slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, key+"=") })
	}
	if !has("XDG_RUNTIME_DIR") && exists(runtimeDir) {
		env = append(env, "XDG_RUNTIME_DIR="+runtimeDir)
	}
	bus := filepath.Join(runtimeDir, "bus")
	if !has("DBUS_SESSION_BUS_ADDRESS") && exists(bus) {
		env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+bus)
	}
	return env
}

// UserSystemctl runs systemctl --user with UserBusEnv and returns its trimmed stdout.
func UserSystemctl(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...)
	cmd.Env = UserBusEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return strings.TrimSpace(stdout.String()), fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return strings.TrimSpace(stdout.String()), fmt.Errorf("systemctl --user %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}
