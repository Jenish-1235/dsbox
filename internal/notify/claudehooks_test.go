package notify

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// These tests run the claude component's settings.json merge script, which
// installs the hooks that call ds notify.
const hooksScript = "../../components/claude/files/devbox-claude-hooks"

const devboxMatcher = "permission_prompt|elicitation_dialog|elicitation_url_dialog|agent_needs_input"

func devboxHandlers() []any {
	return []any{
		map[string]any{"type": "command", "command": "~/.devbox/bin/ds notify bell", "timeout": 5.0},
		map[string]any{"type": "command", "command": "~/.devbox/bin/ds notify hook", "async": true},
	}
}

func runHooksScript(t *testing.T, settings string) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	out, err := exec.Command("bash", hooksScript, settings).CombinedOutput()
	if err != nil {
		t.Fatalf("devbox-claude-hooks: %v\n%s", err, out)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("%s isn't JSON: %v\n%s", path, err, data)
	}
	return v
}

func TestHooksScriptCreatesSettings(t *testing.T) {
	settings := filepath.Join(t.TempDir(), ".claude", "settings.json")
	runHooksScript(t, settings)

	got := readJSON(t, settings)
	want := map[string]any{"hooks": map[string]any{
		"Stop":         []any{map[string]any{"hooks": devboxHandlers()}},
		"Notification": []any{map[string]any{"matcher": devboxMatcher, "hooks": devboxHandlers()}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("settings = %v\nwant %v", got, want)
	}
}

func TestHooksScriptKeepsHerSettingsAndIsIdempotent(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	mine := `{
  "model": "opus",
  "permissions": {"allow": ["Bash(npm test)"]},
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "say done"}]}],
    "Notification": [{"matcher": "idle_prompt", "hooks": [{"type": "command", "command": "my-alert"}]}],
    "PostToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "prettier"}]}]
  }
}`
	if err := os.WriteFile(settings, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	runHooksScript(t, settings)
	first, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	runHooksScript(t, settings)
	second, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("second run changed the file:\n%s\n---\n%s", first, second)
	}

	got := readJSON(t, settings)
	if got["model"] != "opus" || !reflect.DeepEqual(got["permissions"], map[string]any{"allow": []any{"Bash(npm test)"}}) {
		t.Errorf("unrelated settings changed: %v", got)
	}
	hooks := got["hooks"].(map[string]any)
	wantStop := []any{
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "say done"}}},
		map[string]any{"hooks": devboxHandlers()},
	}
	if !reflect.DeepEqual(hooks["Stop"], wantStop) {
		t.Errorf("Stop = %v\nwant %v", hooks["Stop"], wantStop)
	}
	wantNotification := []any{
		map[string]any{"matcher": "idle_prompt", "hooks": []any{map[string]any{"type": "command", "command": "my-alert"}}},
		map[string]any{"matcher": devboxMatcher, "hooks": devboxHandlers()},
	}
	if !reflect.DeepEqual(hooks["Notification"], wantNotification) {
		t.Errorf("Notification = %v\nwant %v", hooks["Notification"], wantNotification)
	}
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Error("the developer's PostToolUse hook is gone")
	}
	assertMode(t, settings, 0o600)
}

func TestHooksScriptReplacesOlderDevboxHooks(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	old := `{"hooks": {"Stop": [
  {"hooks": [{"type": "command", "command": "~/.devbox/bin/ds notify old-way"}]},
  {"hooks": [{"type": "command", "command": "say done"}, {"type": "command", "command": "~/.devbox/bin/ds notify hook"}]}
]}}`
	if err := os.WriteFile(settings, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	runHooksScript(t, settings)

	stop := readJSON(t, settings)["hooks"].(map[string]any)["Stop"]
	want := []any{
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "say done"}}},
		map[string]any{"hooks": devboxHandlers()},
	}
	if !reflect.DeepEqual(stop, want) {
		t.Errorf("Stop = %v\nwant %v", stop, want)
	}
}

func TestHooksScriptLeavesBrokenSettingsAlone(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	broken := "{ \"model\": \"opus\", // comment\n}"
	if err := os.WriteFile(settings, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("bash", hooksScript, settings).Run(); err == nil {
		t.Fatal("devbox-claude-hooks succeeded on invalid JSON, want a failure")
	}
	if data, _ := os.ReadFile(settings); string(data) != broken {
		t.Errorf("settings changed to %q", data)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left temp files behind: %v", entries)
	}
}

func TestHooksScriptFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles-settings.json")
	if err := os.WriteFile(target, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	runHooksScript(t, link)

	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("settings.json is no longer a symlink (%v)", err)
	}
	if got := readJSON(t, target); got["theme"] != "dark" || got["hooks"] == nil {
		t.Errorf("linked file = %v, want theme kept and hooks added", got)
	}
}
