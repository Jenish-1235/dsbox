package component

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAcceptsShortAndLongRequirements(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	writeFile(t, filepath.Join(dir, "files", "rc"), "x")
	writeFile(t, filepath.Join(dir, "component.yaml"), `name: demo
requires:
  - git
  - { cmd: just, install: mise use -g just }
files:
  - { src: files/rc, dest: ~/.rc }
`)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Requirement{{Cmd: "git"}, {Cmd: "just", Install: "mise use -g just"}}
	if len(c.Requires) != 2 || c.Requires[0] != want[0] || c.Requires[1] != want[1] {
		t.Errorf("Requires = %+v, want %+v", c.Requires, want)
	}
}

func TestRepoComponentsLoad(t *testing.T) {
	comps, err := LoadAll("../../components")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range comps {
		if c.Name != "claude" {
			continue
		}
		want := "~/.devbox/bin/devbox-claude-hooks"
		if len(c.AfterApply) != 2 || c.AfterApply[1] != want {
			t.Errorf("claude after_apply = %q, want it to end with %q", c.AfterApply, want)
		}
		return
	}
	t.Error("no claude component")
}

// The claude component's first after_apply adds the devbox guide's import to
// the developer's CLAUDE.md on a line of its own, once, whatever the file held before.
func TestClaudeImportLine(t *testing.T) {
	c, err := Load("../../components/claude")
	if err != nil {
		t.Fatal(err)
	}
	const imp = "@~/.claude/devbox.md\n"
	cases := map[string]struct {
		before *string // nil: no CLAUDE.md yet
		want   string
	}{
		"no file":             {nil, imp},
		"empty file":          {ptr(""), imp},
		"ends with newline":   {ptr("- Prefer tabs\n"), "- Prefer tabs\n" + imp},
		"no trailing newline": {ptr("- Prefer tabs"), "- Prefer tabs\n" + imp},
		"already imported":    {ptr("x\n" + imp + "y"), "x\n" + imp + "y"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".claude", "CLAUDE.md")
			if tc.before != nil {
				writeFile(t, path, *tc.before)
			}
			only := Component{Name: c.Name, Dir: c.Dir, AfterApply: c.AfterApply[:1]}
			for range 2 { // a rerun changes nothing
				if _, err := only.RunAfterApply(t.Context(), []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("CLAUDE.md = %q, want %q", got, tc.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }

func TestLoadRejectsMismatchedName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	writeFile(t, filepath.Join(dir, "component.yaml"), "name: other\n")
	if _, err := Load(dir); err == nil {
		t.Error("Load() = nil error, want name mismatch error")
	}
}

func TestLoadRejectsMissingSourceFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	writeFile(t, filepath.Join(dir, "component.yaml"), "name: demo\nfiles:\n  - { src: files/nope, dest: ~/.nope }\n")
	if _, err := Load(dir); err == nil {
		t.Error("Load() = nil error, want missing file error")
	}
}

func TestPlanStatuses(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dir := filepath.Join(root, "demo")
	writeFile(t, filepath.Join(dir, "files", "rc"), "new")
	c := Component{Name: "demo", Dir: dir, Files: []File{{Src: "files/rc", Dest: "~/.rc"}}}
	expand := func(p string) string { return filepath.Join(home, p[2:]) }
	dest := filepath.Join(home, ".rc")

	tests := []struct {
		name    string
		onDisk  string // "" means absent
		written string // what ds last wrote, "" means never
		want    Status
	}{
		{"absent", "", "", Missing},
		{"identical", "new", "", Same},
		{"ds wrote the old version", "old", "old", Outdated},
		{"edited after ds wrote it", "mine", "old", Edited},
		{"never written by ds", "mine", "", Edited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.RemoveAll(home)
			written := map[string]string{}
			if tt.onDisk != "" {
				writeFile(t, dest, tt.onDisk)
			}
			if tt.written != "" {
				src := filepath.Join(root, "written")
				writeFile(t, src, tt.written)
				h, err := hashFile(src)
				if err != nil {
					t.Fatal(err)
				}
				written[dest] = h
			}
			actions, err := c.Plan(expand, written)
			if err != nil {
				t.Fatal(err)
			}
			if got := actions[0].Status; got != tt.want {
				t.Errorf("Plan() status = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWriteCreatesParentsAndReplacesAtomically(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, src, "content")
	dest := filepath.Join(root, "a", "b", "dest")
	if err := (Action{Src: src, Dest: dest}).Write(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "content" {
		t.Errorf("dest = %q, want %q", got, "content")
	}
	if _, err := os.Stat(dest + ".ds-tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}

func TestLoadAfterApplyAndOS(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	writeFile(t, filepath.Join(dir, "component.yaml"), `name: demo
os: linux
after_apply:
  - systemctl --user daemon-reload
  - systemctl --user enable --now demo.timer
`)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.AfterApply) != 2 || c.AfterApply[1] != "systemctl --user enable --now demo.timer" {
		t.Errorf("AfterApply = %q", c.AfterApply)
	}
	if !c.Supports("linux") || c.Supports("darwin") {
		t.Errorf("Supports(linux, darwin) = %v, %v, want true, false", c.Supports("linux"), c.Supports("darwin"))
	}
	if !(Component{}).Supports("darwin") {
		t.Error("a component without os should support every OS")
	}
}

func TestLoadRejectsBadOSAndEmptyHook(t *testing.T) {
	for name, yaml := range map[string]string{
		"unknown os": "name: demo\nos: windows\n",
		"empty hook": "name: demo\nafter_apply:\n  - '  '\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "demo")
			writeFile(t, filepath.Join(dir, "component.yaml"), yaml)
			if _, err := Load(dir); err == nil {
				t.Error("Load() = nil error, want a validation error")
			}
		})
	}
}

func TestRunAfterApply(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	c := Component{Name: "demo", Dir: dir, AfterApply: []string{
		"echo one >> " + log,
		"echo \"$DEMO_VAR\" >> " + log,
		"echo boom >&2; exit 3",
		"echo never >> " + log,
	}}
	ran, err := c.RunAfterApply(t.Context(), []string{"DEMO_VAR=two", "PATH=" + os.Getenv("PATH")})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("RunAfterApply() error = %v, want exit status 3 with its output", err)
	}
	if len(ran) != 2 {
		t.Errorf("RunAfterApply() ran %q, want the first two", ran)
	}
	got, _ := os.ReadFile(log)
	if string(got) != "one\ntwo\n" {
		t.Errorf("log = %q, want commands run in order with env, stopping at the failure", got)
	}
}
