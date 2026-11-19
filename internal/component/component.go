// Package component loads component definitions and syncs their files.
//
// A component is a folder under components/ with a component.yaml that lists
// the commands it needs, the files it manages, and extra health checks.
package component

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Component is one part of the machine setup, such as shell or tmux.
type Component struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// OS limits the component to one operating system ("linux"); empty means any.
	OS       string        `yaml:"os"`
	Requires []Requirement `yaml:"requires"`
	Files    []File        `yaml:"files"`
	Checks   []Check       `yaml:"checks"`
	// AfterApply are bash commands run, in order, after applying changed any
	// of the component's files (e.g. reloading a service).
	AfterApply []string `yaml:"after_apply"`
	// Dir is the component's folder; File.Src is relative to it.
	Dir string `yaml:"-"`
}

// Requirement is a command that must be on PATH, with how to install it.
type Requirement struct {
	Cmd     string `yaml:"cmd"`
	Install string `yaml:"install"`
}

// UnmarshalYAML accepts either a bare command name or {cmd, install}.
func (r *Requirement) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		r.Cmd = node.Value
		return nil
	}
	type plain Requirement
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*r = Requirement(p)
	return nil
}

// File is a file the component installs: Src inside the component folder,
// Dest on the machine ("~/" is the user's home).
type File struct {
	Src  string `yaml:"src"`
	Dest string `yaml:"dest"`
}

// Check is a bash snippet that exits 0 when healthy, with a suggested fix.
type Check struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
	Fix  string `yaml:"fix"`
}

// LoadAll reads every component under dir, sorted by name.
func LoadAll(dir string) ([]Component, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read components: %w", err)
	}
	var comps []Component
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		comps = append(comps, c)
	}
	slices.SortFunc(comps, func(a, b Component) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	return comps, nil
}

// Load reads and validates one component folder.
func Load(dir string) (Component, error) {
	path := filepath.Join(dir, "component.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return Component{}, fmt.Errorf("read %s: %w", path, err)
	}
	var c Component
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Component{}, fmt.Errorf("parse %s: %w", path, err)
	}
	c.Dir = dir
	if c.Name != filepath.Base(dir) {
		return Component{}, fmt.Errorf("%s: name %q must match folder %q", path, c.Name, filepath.Base(dir))
	}
	for _, f := range c.Files {
		if f.Src == "" || f.Dest == "" {
			return Component{}, fmt.Errorf("%s: every file needs src and dest", path)
		}
		if _, err := os.Stat(filepath.Join(dir, f.Src)); err != nil {
			return Component{}, fmt.Errorf("%s: file %s: %w", path, f.Src, err)
		}
	}
	for _, r := range c.Requires {
		if r.Cmd == "" {
			return Component{}, fmt.Errorf("%s: a requirement has no cmd", path)
		}
	}
	if c.OS != "" && c.OS != "linux" && c.OS != "darwin" {
		return Component{}, fmt.Errorf("%s: os must be linux or darwin, not %q", path, c.OS)
	}
	for _, cmd := range c.AfterApply {
		if strings.TrimSpace(cmd) == "" {
			return Component{}, fmt.Errorf("%s: an after_apply command is empty", path)
		}
	}
	return c, nil
}

// Supports reports whether the component applies on goos (runtime.GOOS).
func (c Component) Supports(goos string) bool { return c.OS == "" || c.OS == goos }

// hookTimeout bounds each after_apply command, so a stuck one can't hang ds.
const hookTimeout = time.Minute

// RunAfterApply runs the after_apply commands in order with bash, in the
// component's folder and with env as the environment. It stops at the first
// failure and returns an error that includes the command's output. ran lists
// the commands that succeeded.
func (c Component) RunAfterApply(ctx context.Context, env []string) (ran []string, err error) {
	for _, script := range c.AfterApply {
		hctx, cancel := context.WithTimeout(ctx, hookTimeout)
		cmd := exec.CommandContext(hctx, "bash", "-c", script)
		cmd.Dir, cmd.Env = c.Dir, env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		cancel()
		if err != nil {
			if msg := strings.TrimSpace(out.String()); msg != "" {
				return ran, fmt.Errorf("%s: %w: %s", script, err, msg)
			}
			return ran, fmt.Errorf("%s: %w", script, err)
		}
		ran = append(ran, script)
	}
	return ran, nil
}

// Status is how a managed file on disk compares with the component's copy.
type Status int

const (
	// Same means the file on disk matches the component.
	Same Status = iota
	// Missing means the file doesn't exist yet.
	Missing
	// Outdated means the file is what ds last wrote, and the component has changed.
	Outdated
	// Edited means someone changed the file since ds last wrote it (or ds never
	// wrote it). Applying backs it up first.
	Edited
)

func (s Status) String() string {
	switch s {
	case Same:
		return "up to date"
	case Missing:
		return "missing"
	case Outdated:
		return "update available"
	case Edited:
		return "edited by hand"
	}
	return "unknown"
}

// Action is one managed file and what applying would do to it.
type Action struct {
	Component string
	Src       string // absolute path in the component folder
	Dest      string // absolute path on the machine
	Status    Status
	Want      string // sha256 of Src
}

// Plan compares each file with what's on disk. written maps a destination
// to the sha256 ds last wrote there.
func (c Component) Plan(expand func(string) string, written map[string]string) ([]Action, error) {
	actions := make([]Action, 0, len(c.Files))
	for _, f := range c.Files {
		src := filepath.Join(c.Dir, f.Src)
		dest := expand(f.Dest)
		want, err := hashFile(src)
		if err != nil {
			return nil, err
		}
		a := Action{Component: c.Name, Src: src, Dest: dest, Want: want}
		have, err := hashFile(dest)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			a.Status = Missing
		case err != nil:
			return nil, err
		case have == want:
			a.Status = Same
		case have == written[dest]:
			a.Status = Outdated
		default:
			a.Status = Edited
		}
		actions = append(actions, a)
	}
	return actions, nil
}

// Write installs the component's copy at Dest atomically, keeping Src's mode.
func (a Action) Write() error {
	data, err := os.ReadFile(a.Src)
	if err != nil {
		return fmt.Errorf("read %s: %w", a.Src, err)
	}
	info, err := os.Stat(a.Src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", a.Src, err)
	}
	if err := os.MkdirAll(filepath.Dir(a.Dest), 0o755); err != nil {
		return fmt.Errorf("create folder for %s: %w", a.Dest, err)
	}
	tmp := a.Dest + ".ds-tmp"
	if err := os.WriteFile(tmp, data, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write %s: %w", a.Dest, err)
	}
	if err := os.Rename(tmp, a.Dest); err != nil {
		return fmt.Errorf("install %s: %w", a.Dest, err)
	}
	return nil
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
