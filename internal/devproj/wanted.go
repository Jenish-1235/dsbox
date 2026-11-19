package devproj

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Wanted is the projects the developer started with ds up and hasn't stopped with
// ds stop, by name, with their folders. It's saved in
// ~/.devbox/state/running.json so ds restore can start them after a reboot.
type Wanted map[string]string

// WantedFile is where the wanted projects are saved.
func (s Settings) WantedFile() string { return filepath.Join(s.Layout.StateDir(), "running.json") }

// LoadWanted reads the wanted projects; a missing file is an empty map.
func LoadWanted(path string) (Wanted, error) {
	w := Wanted{}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return w, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read running projects: %w", err)
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("read running projects %s: %w", path, err)
	}
	return w, nil
}

// Save writes the wanted projects atomically.
func (w Wanted) Save(path string) error {
	if err := saveJSON(path, w); err != nil {
		return fmt.Errorf("save running projects: %w", err)
	}
	return nil
}

// PlanRestore returns the wanted projects that aren't running and still have
// their folder, in name order, and the names whose folder is gone.
func PlanRestore(w Wanted, running map[string]bool, isDir func(string) bool) (start []Project, gone []string) {
	for name, dir := range w {
		switch {
		case running[name]:
		case !isDir(dir):
			gone = append(gone, name)
		default:
			start = append(start, Project{Name: name, Dir: dir})
		}
	}
	slices.SortFunc(start, func(a, b Project) int { return strings.Compare(a.Name, b.Name) })
	slices.Sort(gone)
	return start, gone
}

// want records p as wanted; ds up calls it once the server is starting.
func (d Dev) want(p Project) {
	d.editWanted(func(w Wanted) bool {
		if w[p.Name] == p.Dir {
			return false
		}
		w[p.Name] = p.Dir
		return true
	})
}

// unwant forgets name; ds stop calls it.
func (d Dev) unwant(name string) {
	d.editWanted(func(w Wanted) bool {
		if _, ok := w[name]; !ok {
			return false
		}
		delete(w, name)
		return true
	})
}

// editWanted applies change and saves when it reports a change. Failing to
// remember a project only matters after a reboot, so it's a warning.
func (d Dev) editWanted(change func(Wanted) bool) {
	path := d.S.WantedFile()
	w, err := LoadWanted(path)
	if err != nil {
		d.Out.Warn("Couldn't read which projects to start after a reboot: %v", err)
		return
	}
	if !change(w) {
		return
	}
	if err := w.Save(path); err != nil {
		d.Out.Warn("Couldn't remember which projects to start after a reboot: %v", err)
	}
}

// Restore starts every wanted project that isn't running, as ds up would.
// It prints nothing when there's nothing to do. Projects whose folder is gone
// are forgotten. It returns ErrReported when a project didn't start.
func (d Dev) Restore(ctx context.Context) error {
	w, err := LoadWanted(d.S.WantedFile())
	if err != nil {
		return err
	}
	start, gone := PlanRestore(w, Running(ctx), func(dir string) bool {
		fi, err := os.Stat(dir)
		return err == nil && fi.IsDir()
	})
	for _, name := range gone {
		d.Out.Warn("Not starting %s: %s is gone", name, d.S.Layout.Pretty(w[name]))
		d.unwant(name)
	}
	failed := 0
	for _, p := range start {
		if err := d.Up(ctx, p); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !errors.Is(err, ErrReported) {
				d.Out.Fail("%s: %v", p.Name, err)
			}
			failed++
		}
	}
	if failed > 0 {
		return ErrReported
	}
	return nil
}

// saveJSON writes v as indented JSON to path atomically, creating its folder.
func saveJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
