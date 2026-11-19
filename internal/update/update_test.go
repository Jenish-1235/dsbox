package update

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/state"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

// fixture is an "origin" repo with one component, cloned into a fake ~/.devbox/src.
type fixture struct {
	t      *testing.T
	origin string
	l      layout.Layout
	u      Updater
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"DEVBOX_CHANNEL": "",
	} {
		t.Setenv(k, v)
	}
	root := t.TempDir()
	f := &fixture{t: t, origin: filepath.Join(root, "origin")}
	f.git(root, "init", "--quiet", "-b", "main", f.origin)
	f.commit("v1", "first")

	home := filepath.Join(root, "home")
	f.l = layout.Layout{Home: home, Root: filepath.Join(home, ".devbox")}
	if err := f.l.Ensure(); err != nil {
		t.Fatal(err)
	}
	f.git(root, "clone", "--quiet", f.origin, f.l.Src())

	f.u = Updater{
		Layout: f.l,
		Out:    ui.Plain(io.Discard),
		Build: func(ctx context.Context, src, _, dst string) error {
			head, err := exec.CommandContext(ctx, "git", "-C", src, "rev-parse", "HEAD").Output()
			if err != nil {
				return err
			}
			return os.WriteFile(dst, head, 0o755) // fake binary: the commit it was "built" from
		},
		Reapply: func(ctx context.Context) error { return Apply(ctx, f.l, ui.Plain(io.Discard)) },
		Revision: func(path string) string {
			data, _ := os.ReadFile(path)
			return strings.TrimSpace(string(data))
		},
	}
	return f
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.CommandContext(f.t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes the demo component with greeting content and commits it to origin.
func (f *fixture) commit(content, msg string) string {
	f.t.Helper()
	dir := filepath.Join(f.origin, "components", "demo")
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	for _, cmd := range []string{"ds", "dev"} {
		main := filepath.Join(f.origin, "cmd", cmd, "main.go")
		if err := os.MkdirAll(filepath.Dir(main), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(main, []byte("package main\n"), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	yaml := "name: demo\nfiles:\n  - { src: files/greeting, dest: ~/.greeting }\n"
	if err := os.WriteFile(filepath.Join(dir, "component.yaml"), []byte(yaml), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "files", "greeting"), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(f.origin, "add", "-A")
	f.git(f.origin, "commit", "--quiet", "-m", msg)
	return f.git(f.origin, "rev-parse", "HEAD")
}

func (f *fixture) greeting() string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.l.Home, ".greeting"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func (f *fixture) binary() string {
	f.t.Helper()
	return f.command("ds")
}

// command returns the commit the installed command name was "built" from.
func (f *fixture) command(name string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.l.Bin(), name))
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func TestUpdateRollbackCycle(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	// Already current: just applies.
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatalf("first Update: %v", err)
	}
	if got := f.greeting(); got != "v1" {
		t.Fatalf("after first update greeting = %q, want v1", got)
	}

	// New commit: moves, builds, applies.
	v1 := f.git(f.l.Src(), "rev-parse", "HEAD")
	v2 := f.commit("v2", "second")
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatalf("Update to v2: %v", err)
	}
	if got := f.greeting(); got != "v2" {
		t.Errorf("greeting = %q, want v2", got)
	}
	if got := f.binary(); got != v2 {
		t.Errorf("ds built from %q, want %q", got, v2)
	}
	if got := f.command("dev"); got != v2 {
		t.Errorf("dev built from %q, want %q", got, v2)
	}
	st, err := state.Load(f.l.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != v2 || st.Previous != v1 {
		t.Errorf("state = current %s previous %s, want %s %s", st.Current, st.Previous, v2, v1)
	}

	// A hand edit is backed up before v3 replaces it.
	if err := os.WriteFile(filepath.Join(f.l.Home, ".greeting"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.commit("v3", "third")
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatalf("Update to v3: %v", err)
	}
	if got := f.greeting(); got != "v3" {
		t.Errorf("greeting = %q, want v3", got)
	}
	backups, err := filepath.Glob(filepath.Join(f.l.Backups(), "*", ".greeting"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v (err %v), want exactly one", backups, err)
	}
	if data, _ := os.ReadFile(backups[0]); string(data) != "mine" {
		t.Errorf("backup = %q, want %q", data, "mine")
	}

	// Rollback returns to v2, files and binary.
	if err := f.u.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := f.greeting(); got != "v2" {
		t.Errorf("after rollback greeting = %q, want v2", got)
	}
	if got := f.binary(); got != v2 {
		t.Errorf("after rollback ds from %q, want %q", got, v2)
	}
	if got := f.command("dev"); got != v2 {
		t.Errorf("after rollback dev from %q, want %q", got, v2)
	}
	// Rolling forward again swaps the v3 binaries back in.
	if err := f.u.Rollback(ctx); err != nil {
		t.Fatalf("second Rollback: %v", err)
	}
	if ds, dev := f.binary(), f.command("dev"); ds != dev || ds == v2 {
		t.Errorf("after roll-forward ds from %q, dev from %q; want both from v3", ds, dev)
	}
}

func TestUpdateRefusesLocalChanges(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.l.Src(), "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.u.Update(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Errorf("Update() error = %v, want local changes error", err)
	}
}

func TestFailedBuildStaysOnCurrentVersion(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatal(err)
	}
	v1 := f.git(f.l.Src(), "rev-parse", "HEAD")
	f.commit("v2", "second")
	// ds builds, dev doesn't: nothing is installed, not even ds.
	build := f.u.Build
	f.u.Build = func(ctx context.Context, src, name, dst string) error {
		if name == "dev" {
			return os.ErrInvalid
		}
		return build(ctx, src, name, dst)
	}
	if err := f.u.Update(ctx, ""); err == nil {
		t.Fatal("Update() = nil error, want build failure")
	}
	if head := f.git(f.l.Src(), "rev-parse", "HEAD"); head != v1 {
		t.Errorf("HEAD = %s after failed build, want %s", head, v1)
	}
	if got := f.greeting(); got != "v1" {
		t.Errorf("greeting = %q after failed build, want v1", got)
	}
	// The fixture's first update was already current, so nothing was built yet.
	if _, err := os.Stat(filepath.Join(f.l.Bin(), "ds")); err == nil {
		t.Error("ds was installed although dev failed to build")
	}
	if left, _ := filepath.Glob(filepath.Join(f.l.Bin(), "*.new")); len(left) > 0 {
		t.Errorf("leftover builds after failure: %v", left)
	}
}

func TestRollbackWithNoHistory(t *testing.T) {
	f := newFixture(t)
	if err := f.u.Rollback(t.Context()); err == nil {
		t.Error("Rollback() = nil error, want nothing to roll back")
	}
}

func TestInterruptedBuildGoesBack(t *testing.T) {
	f := newFixture(t)
	if err := f.u.Update(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	v1 := f.git(f.l.Src(), "rev-parse", "HEAD")
	v2 := f.commit("v2", "second")
	// Ctrl-C during the build of dev cancels ctx; going back must still work.
	ctx, cancel := context.WithCancel(t.Context())
	build := f.u.Build
	f.u.Build = func(bctx context.Context, src, name, dst string) error {
		if name == "dev" {
			cancel()
			return bctx.Err()
		}
		return build(bctx, src, name, dst)
	}
	if err := f.u.Update(ctx, ""); err == nil {
		t.Fatal("Update() = nil error, want the interrupted build's error")
	}
	if head := f.git(f.l.Src(), "rev-parse", "HEAD"); head != v1 {
		t.Errorf("HEAD = %s after an interrupted build, want %s", head, v1)
	}

	f.u.Build = build
	if err := f.u.Update(t.Context(), ""); err != nil {
		t.Fatalf("Update after the interruption: %v", err)
	}
	if got := f.binary(); got != v2 {
		t.Errorf("ds built from %q, want %q", got, v2)
	}
}

func TestUpdateRebuildsWhenSourceIsAheadOfBinaries(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	v2 := f.commit("v2", "second")
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatal(err)
	}
	// An older ds left HEAD on v3 with the v2 binaries and state.
	v3 := f.commit("v3", "third")
	f.git(f.l.Src(), "fetch", "--quiet", "origin")
	f.git(f.l.Src(), "checkout", "--quiet", "--detach", v3)

	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if got := f.binary(); got != v3 {
		t.Errorf("ds built from %q, want %q", got, v3)
	}
	if got := f.greeting(); got != "v3" {
		t.Errorf("greeting = %q, want v3", got)
	}
	st, err := state.Load(f.l.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != v3 || st.Previous != v2 {
		t.Errorf("state = current %s previous %s, want %s %s", st.Current, st.Previous, v3, v2)
	}
}

func TestUpdateWithBrokenStateKeepsSource(t *testing.T) {
	f := newFixture(t)
	v1 := f.git(f.l.Src(), "rev-parse", "HEAD")
	f.commit("v2", "second")
	if err := os.WriteFile(f.l.StateFile(), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.u.Update(t.Context(), ""); err == nil {
		t.Fatal("Update() = nil error, want the state error")
	}
	if head := f.git(f.l.Src(), "rev-parse", "HEAD"); head != v1 {
		t.Errorf("HEAD = %s with a broken state file, want %s", head, v1)
	}
}

// install.sh rebuilds from whatever is checked out without recording it in
// state; the commit stamped into the binary is what counts.
func TestUpdateAfterInstallScriptRebuild(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.commit("v1.1", "applied")
	if err := f.u.Update(ctx, ""); err != nil { // state.Current = v1.1
		t.Fatal(err)
	}
	v2 := f.commit("v2", "second")
	f.git(f.l.Src(), "fetch", "--quiet", "origin")
	f.git(f.l.Src(), "checkout", "--quiet", "--detach", v2)
	for _, name := range []string{"ds", "dev"} {
		if err := f.u.Build(ctx, f.l.Src(), name, filepath.Join(f.l.Bin(), name)); err != nil {
			t.Fatal(err)
		}
	}
	v3 := f.commit("v3", "third")
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(f.l.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != v3 || st.Previous != v2 {
		t.Errorf("state = current %s previous %s, want %s %s", st.Current, st.Previous, v3, v2)
	}
	if err := f.u.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if head, bin := f.git(f.l.Src(), "rev-parse", "HEAD"), f.binary(); head != v2 || bin != v2 {
		t.Errorf("after rollback HEAD = %s, ds built from %s; want both %s", head, bin, v2)
	}
}

// Ctrl-C while a rollback builds a command with no kept binary goes back.
func TestInterruptedRollbackBuildGoesBack(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatal(err)
	}
	// Two real updates, so ds and dev both have kept previous binaries.
	f.commit("v2", "second")
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatal(err)
	}
	f.commit("v3", "third")
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatal(err)
	}
	v2 := f.git(f.l.Src(), "rev-parse", "HEAD") // the version to stay on
	if err := os.Remove(f.l.PreviousCommand("dev")); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	build := f.u.Build
	f.u.Build = func(bctx context.Context, _, _, _ string) error {
		cancel()
		return bctx.Err()
	}
	if err := f.u.Rollback(cctx); err == nil {
		t.Fatal("Rollback() = nil error, want the interrupted build's error")
	}
	f.u.Build = build
	if head, bin := f.git(f.l.Src(), "rev-parse", "HEAD"), f.binary(); head != v2 || bin != v2 {
		t.Errorf("after interrupted rollback HEAD = %s, ds built from %s; want both %s", head, bin, v2)
	}
	st, err := state.Load(f.l.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != v2 {
		t.Errorf("state.Current = %s, want %s (unchanged)", st.Current, v2)
	}
}
