package update

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/state"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

// hookFixture is a source tree with one component whose after_apply appends
// to a log, or fails while the file named by fail exists.
type hookFixture struct {
	t        *testing.T
	l        layout.Layout
	log      string
	fail     string
	compFile string
}

func newHookFixture(t *testing.T) *hookFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	h := &hookFixture{
		t:    t,
		l:    layout.Layout{Home: home, Root: filepath.Join(home, ".devbox")},
		log:  filepath.Join(root, "hook.log"),
		fail: filepath.Join(root, "fail"),
	}
	dir := filepath.Join(h.l.Components(), "demo")
	h.compFile = filepath.Join(dir, "files", "unit")
	yaml := "name: demo\nfiles:\n  - { src: files/unit, dest: ~/.config/demo.unit }\nafter_apply:\n" +
		"  - test ! -e " + h.fail + " || { echo no user bus >&2; exit 1; }\n" +
		"  - echo reloaded >> " + h.log + "\n"
	h.write(filepath.Join(dir, "component.yaml"), yaml)
	h.write(h.compFile, "v1")
	return h
}

func (h *hookFixture) write(path, content string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *hookFixture) apply() string {
	h.t.Helper()
	var buf strings.Builder
	if err := Apply(h.t.Context(), h.l, ui.Plain(&buf)); err != nil {
		h.t.Fatalf("Apply() = %v, want nil (hook failures are warnings)", err)
	}
	return buf.String()
}

func (h *hookFixture) runs() int {
	data, _ := os.ReadFile(h.log)
	return strings.Count(string(data), "reloaded")
}

func (h *hookFixture) pending() []string {
	h.t.Helper()
	st, err := state.Load(h.l.StateFile())
	if err != nil {
		h.t.Fatal(err)
	}
	return st.PendingAfterApply
}

func TestAfterApplyRunsOnlyWhenFilesChange(t *testing.T) {
	h := newHookFixture(t)

	h.apply()
	if got := h.runs(); got != 1 {
		t.Fatalf("after installing the file, hook ran %d times, want 1", got)
	}
	h.apply()
	if got := h.runs(); got != 1 {
		t.Errorf("with nothing changed, hook ran %d times in total, want still 1", got)
	}
	h.write(h.compFile, "v2")
	h.apply()
	if got := h.runs(); got != 2 {
		t.Errorf("after the file changed, hook ran %d times in total, want 2", got)
	}
}

func TestFailedAfterApplyWarnsAndRetries(t *testing.T) {
	h := newHookFixture(t)
	h.write(h.fail, "")

	out := h.apply()
	if !strings.Contains(out, "demo: couldn't finish setting up") || !strings.Contains(out, "no user bus") {
		t.Errorf("Apply() output = %q, want a warning with the hook's error", out)
	}
	if got := h.pending(); !slices.Equal(got, []string{"demo"}) {
		t.Errorf("pending = %q, want [demo]", got)
	}
	if data, _ := os.ReadFile(filepath.Join(h.l.Home, ".config", "demo.unit")); string(data) != "v1" {
		t.Errorf("file = %q, want it installed despite the hook failing", data)
	}

	// Nothing changed, but the hook is pending: it runs again and now succeeds.
	if err := os.Remove(h.fail); err != nil {
		t.Fatal(err)
	}
	h.apply()
	if got := h.runs(); got != 1 {
		t.Errorf("retry: hook ran %d times, want 1", got)
	}
	if got := h.pending(); len(got) != 0 {
		t.Errorf("pending = %q after a successful retry, want none", got)
	}
}

func TestApplyDropsPendingForRemovedComponents(t *testing.T) {
	h := newHookFixture(t)
	st := state.State{Files: map[string]string{}, PendingAfterApply: []string{"gone"}}
	if err := st.Save(h.l.StateFile()); err != nil {
		t.Fatal(err)
	}
	h.apply()
	if got := h.pending(); len(got) != 0 {
		t.Errorf("pending = %q, want the removed component dropped", got)
	}
}

func TestFailedApplyKeepsPendingHooks(t *testing.T) {
	h := newHookFixture(t)
	h.write(h.fail, "")
	// "e-broken" sorts after "demo" and can't be planned: a folder is in the
	// way of its file.
	broken := filepath.Join(h.l.Components(), "e-broken")
	h.write(filepath.Join(broken, "component.yaml"), "name: e-broken\nfiles:\n  - { src: files/x, dest: ~/.broken }\n")
	h.write(filepath.Join(broken, "files", "x"), "x")
	h.write(filepath.Join(h.l.Home, ".broken", "in-the-way"), "")
	// "later" sorts after "e-broken" and was pending from an earlier run.
	later := filepath.Join(h.l.Components(), "later")
	h.write(filepath.Join(later, "component.yaml"), "name: later\nfiles:\n  - { src: files/x, dest: ~/.later }\nafter_apply:\n  - echo later >> "+h.log+"\n")
	h.write(filepath.Join(later, "files", "x"), "x")
	st := state.State{Files: map[string]string{}, PendingAfterApply: []string{"later"}}
	if err := st.Save(h.l.StateFile()); err != nil {
		t.Fatal(err)
	}

	if err := Apply(t.Context(), h.l, ui.Plain(io.Discard)); err == nil {
		t.Fatal("Apply() = nil error, want the plan error")
	}
	if got := h.pending(); !slices.Equal(got, []string{"demo", "later"}) {
		t.Errorf("pending = %q after a failed apply, want [demo later]", got)
	}

	// With the causes fixed, the demo hook runs although its file is unchanged.
	for _, p := range []string{h.fail, broken} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}
	h.apply()
	if got := h.runs(); got != 1 {
		t.Errorf("retry: demo hook ran %d times, want 1", got)
	}
	if got := h.pending(); len(got) != 0 {
		t.Errorf("pending = %q after a successful retry, want none", got)
	}
}
