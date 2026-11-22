package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Jenish-1235/dsbox/internal/devproj"
	"github.com/Jenish-1235/dsbox/internal/layout"
)

func TestCheckNewNameShowsProjectsDir(t *testing.T) {
	home, elsewhere := t.TempDir(), t.TempDir()
	for _, tt := range []struct{ projects, want string }{
		{filepath.Join(home, "projects"), "~/projects/shop already exists"},
		{filepath.Join(elsewhere, "work"), filepath.Join(elsewhere, "work", "shop") + " already exists"},
	} {
		if err := os.MkdirAll(filepath.Join(tt.projects, "shop"), 0o755); err != nil {
			t.Fatal(err)
		}
		d := devproj.Dev{S: devproj.Settings{Layout: layout.Layout{Home: home}, ProjectsDir: tt.projects}}
		if err := checkNewName(d, "shop"); err == nil || err.Error() != tt.want {
			t.Errorf("checkNewName(shop) in %s = %v, want %q", tt.projects, err, tt.want)
		}
		if err := checkNewName(d, "cart"); err != nil {
			t.Errorf("checkNewName(cart) = %v, want nil", err)
		}
		for _, reserved := range []string{"dbs", "artifacts"} {
			if err := checkNewName(d, reserved); err == nil {
				t.Errorf("checkNewName(%s) = nil, want an error", reserved)
			}
		}
	}
}
