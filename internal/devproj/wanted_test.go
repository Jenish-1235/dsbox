package devproj

import (
	"bytes"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

func TestWantedLoadSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "running.json")
	w, err := LoadWanted(path)
	if err != nil || len(w) != 0 {
		t.Fatalf("LoadWanted(missing) = %v, %v, want empty, nil", w, err)
	}
	w["shop"] = "/home/h/projects/shop"
	if err := w.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadWanted(path)
	if err != nil || !maps.Equal(got, w) {
		t.Errorf("LoadWanted() = %v, %v, want %v", got, err, w)
	}
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWanted(path); err == nil {
		t.Error("LoadWanted(broken JSON) = nil error")
	}
}

func TestPlanRestore(t *testing.T) {
	w := Wanted{"shop": "/p/shop", "api": "/p/api", "blog": "/p/blog", "old": "/p/old"}
	running := map[string]bool{"blog": true, "other": true}
	dirs := map[string]bool{"/p/shop": true, "/p/api": true, "/p/blog": true}
	start, gone := PlanRestore(w, running, func(d string) bool { return dirs[d] })
	want := []Project{{Name: "api", Dir: "/p/api"}, {Name: "shop", Dir: "/p/shop"}}
	if !slices.Equal(start, want) {
		t.Errorf("PlanRestore() start = %v, want %v", start, want)
	}
	if !slices.Equal(gone, []string{"old"}) {
		t.Errorf("PlanRestore() gone = %v, want [old]", gone)
	}

	start, gone = PlanRestore(Wanted{}, running, func(string) bool { return true })
	if len(start) != 0 || len(gone) != 0 {
		t.Errorf("PlanRestore(nothing wanted) = %v, %v, want nothing", start, gone)
	}
}

func testDev(t *testing.T) (Dev, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	l := layout.Layout{Home: root, Root: filepath.Join(root, ".devbox")}
	var out bytes.Buffer
	return Dev{S: Settings{Layout: l, ProjectsDir: filepath.Join(root, "projects")}, Out: ui.Plain(&out), W: io.Discard}, &out
}

func TestWantUnwant(t *testing.T) {
	d, out := testDev(t)
	d.want(Project{Name: "shop", Dir: "/p/shop"})
	d.want(Project{Name: "api", Dir: "/p/api"})
	d.unwant("api")
	d.unwant("never-started")
	got, err := LoadWanted(d.S.WantedFile())
	if err != nil || !maps.Equal(got, Wanted{"shop": "/p/shop"}) {
		t.Errorf("after want shop, api; unwant api: %v, %v, want only shop", got, err)
	}
	if out.Len() != 0 {
		t.Errorf("want/unwant printed %q, want nothing", out)
	}
}

func TestStopForgetsProject(t *testing.T) {
	d, _ := testDev(t)
	t.Setenv("TMUX_TMPDIR", t.TempDir()) // a tmux server of our own: nothing runs
	d.want(Project{Name: "shop", Dir: "/p/shop"})
	if err := d.Stop(t.Context(), Project{Name: "shop", Dir: "/p/shop"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadWanted(d.S.WantedFile()); len(got) != 0 {
		t.Errorf("after ds stop, running.json = %v, want empty", got)
	}
}

func TestRestoreQuietAndForgetsGone(t *testing.T) {
	d, out := testDev(t)
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	if err := d.Restore(t.Context()); err != nil || out.Len() != 0 {
		t.Fatalf("Restore() with nothing wanted = %v, printed %q; want nil and quiet", err, out)
	}
	d.want(Project{Name: "old", Dir: filepath.Join(t.TempDir(), "deleted")})
	if err := d.Restore(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Not starting old") {
		t.Errorf("Restore() printed %q, want a note about old", out)
	}
	if got, _ := LoadWanted(d.S.WantedFile()); len(got) != 0 {
		t.Errorf("after restore, running.json = %v, want old forgotten", got)
	}
}
