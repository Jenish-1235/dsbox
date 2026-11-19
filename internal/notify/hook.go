package notify

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
)

// HookInput is the part of Claude Code's hook input (JSON on stdin) that
// decides whether to push. See https://code.claude.com/docs/en/hooks.
type HookInput struct {
	Event            string            `json:"hook_event_name"`
	NotificationType string            `json:"notification_type"`
	Cwd              string            `json:"cwd"`
	BackgroundTasks  []json.RawMessage `json:"background_tasks"`
}

// ParseHook reads a hook's JSON input.
func ParseHook(r io.Reader) (HookInput, error) {
	var in HookInput
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&in); err != nil {
		return HookInput{}, fmt.Errorf("read hook input: %w", err)
	}
	return in, nil
}

// Push is the phone push for this hook event in project, and false when the
// event shouldn't send one. The text is fixed per event: Claude's own
// notification message and last reply are never forwarded.
func (in HookInput) Push(project string) (Message, bool) {
	switch in.Event {
	case "Stop":
		// Background work will wake Claude again, so it isn't done yet.
		if len(in.BackgroundTasks) > 0 {
			return Message{}, false
		}
		return Message{Title: project, Body: "Claude is done", Tags: []string{"white_check_mark"}}, true
	case "Notification":
		switch in.NotificationType {
		case "permission_prompt":
			return Message{Title: project, Body: "Claude needs your permission", Tags: []string{"raising_hand"}}, true
		case "elicitation_dialog", "elicitation_url_dialog", "agent_needs_input":
			return Message{Title: project, Body: "Claude is waiting for you", Tags: []string{"raising_hand"}}, true
		}
	}
	// idle_prompt comes a minute after Stop already pushed; the rest
	// (auth_success, ...) aren't worth a buzz.
	return Message{}, false
}

// Project names the project a hook ran for: the folder Claude Code was
// started in (CLAUDE_PROJECT_DIR), else the hook's working directory.
func Project(projectDir, cwd string) string {
	for _, dir := range []string{projectDir, cwd} {
		if dir == "" {
			continue
		}
		if name := filepath.Base(filepath.Clean(dir)); name != "/" && name != "." {
			return name
		}
	}
	return "devbox"
}

// Bell is a hook's JSON output asking Claude Code to ring the terminal bell.
// Hooks can't write to /dev/tty, so Claude Code emits it on their behalf; in
// tmux the bell then reaches the laptop's terminal.
func Bell() []byte {
	out, _ := json.Marshal(struct {
		TerminalSequence string `json:"terminalSequence"`
	}{"\a"})
	return append(out, '\n')
}
