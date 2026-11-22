// Package artifact keeps the HTML artifacts that agents and the developer
// submit (design docs, reports, dashboards), for https://artifacts.<apps domain>.
//
// They live in ~/.devbox/artifacts:
//
//	index.json              title and times of every artifact
//	<project>/<slug>.html   the artifact itself
//
// Only ds writes here, directly on disk as the developer: the web pages only
// read, so nothing on the network can add or delete an artifact.
package artifact

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// MaxSize is the largest artifact ds accepts: 20 MiB.
const MaxSize = 20 << 20

// Sandbox is the Content-Security-Policy for a raw artifact. Without
// allow-same-origin the artifact runs in an opaque origin, so its scripts can't
// read the hub or any other devbox page; CDN scripts still load.
const Sandbox = "sandbox allow-scripts allow-popups allow-forms"

// Errors callers can tell apart.
var (
	ErrNotFound = errors.New("no such artifact")
	ErrTooLarge = fmt.Errorf("artifact is larger than %d MB", MaxSize>>20)
)

// Artifact is one submitted HTML file.
type Artifact struct {
	Project string    `json:"project"`
	Slug    string    `json:"slug"`
	Title   string    `json:"title"`
	Size    int64     `json:"size"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// ID is "<project>/<slug>", how commands and links name the artifact.
func (a Artifact) ID() string { return a.Project + "/" + a.Slug }

// Store is the artifacts folder. Now is the clock (time.Now when nil).
type Store struct {
	Dir string
	Now func() time.Time
}

// ValidName reports whether s can be a project or slug: lowercase letters,
// digits and single dashes, like project names. It can't hold a path.
func ValidName(s string) bool {
	if s == "" || len(s) > 80 || s[0] == '-' || s[len(s)-1] == '-' || strings.Contains(s, "--") {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// CleanName turns a file or folder name into a valid name, or "" when nothing
// usable is left: "Design Doc.html" → "design-doc".
func CleanName(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 80 {
		out = strings.TrimRight(out[:80], "-")
	}
	return out
}

// ParseID splits "<project>/<slug>" and checks both parts.
func ParseID(id string) (project, slug string, err error) {
	project, slug, ok := strings.Cut(id, "/")
	if !ok || !ValidName(project) || !ValidName(slug) {
		return "", "", fmt.Errorf("%q isn't <project>/<slug> (lowercase letters, digits and -)", id)
	}
	return project, slug, nil
}

func check(project, slug string) error {
	if !ValidName(project) {
		return fmt.Errorf("project %q: use lowercase letters, digits and - (e.g. %q)", project, CleanName(project))
	}
	if !ValidName(slug) {
		return fmt.Errorf("slug %q: use lowercase letters, digits and - (e.g. %q)", slug, CleanName(slug))
	}
	return nil
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Store) indexFile() string { return filepath.Join(s.Dir, "index.json") }

// Path is where the artifact's HTML is on disk.
func (s Store) Path(project, slug string) string {
	return filepath.Join(s.Dir, project, slug+".html")
}

// Submit saves the HTML from r as project/slug, replacing any artifact of that
// name (it keeps its created time; updated becomes now). It refuses more than
// MaxSize bytes. replaced says whether one was there before.
func (s Store) Submit(project, slug, title string, r io.Reader) (a Artifact, replaced bool, err error) {
	if err := check(project, slug); err != nil {
		return Artifact{}, false, err
	}
	title = cleanTitle(title)
	if title == "" {
		return Artifact{}, false, errors.New("give the artifact a title")
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return Artifact{}, false, fmt.Errorf("read artifact: %w", err)
	}
	if len(data) > MaxSize {
		return Artifact{}, false, ErrTooLarge
	}
	err = s.locked(syscall.LOCK_EX, func() error {
		idx, err := s.load()
		if err != nil {
			return err
		}
		now := s.now()
		a = Artifact{Project: project, Slug: slug, Title: title, Size: int64(len(data)), Created: now, Updated: now}
		i := slices.IndexFunc(idx, func(o Artifact) bool { return o.Project == project && o.Slug == slug })
		if i >= 0 {
			replaced = true
			a.Created = idx[i].Created
			idx[i] = a
		} else {
			idx = append(idx, a)
		}
		if err := os.MkdirAll(filepath.Join(s.Dir, project), 0o700); err != nil {
			return fmt.Errorf("create artifact folder: %w", err)
		}
		if err := writeAtomic(s.Path(project, slug), data); err != nil {
			return err
		}
		return s.save(idx)
	})
	return a, replaced, err
}

// List returns every artifact, most recently updated first. No folder yet
// means none.
func (s Store) List() ([]Artifact, error) {
	var idx []Artifact
	err := s.locked(syscall.LOCK_SH, func() error {
		var err error
		idx, err = s.load()
		return err
	})
	slices.SortFunc(idx, func(a, b Artifact) int {
		if c := b.Updated.Compare(a.Updated); c != 0 {
			return c
		}
		return cmp.Compare(a.ID(), b.ID())
	})
	return idx, err
}

// Get returns one artifact's details, or ErrNotFound.
func (s Store) Get(project, slug string) (Artifact, error) {
	if check(project, slug) != nil {
		return Artifact{}, ErrNotFound
	}
	all, err := s.List()
	if err != nil {
		return Artifact{}, err
	}
	for _, a := range all {
		if a.Project == project && a.Slug == slug {
			return a, nil
		}
	}
	return Artifact{}, ErrNotFound
}

// Open returns the artifact's details and its HTML, or ErrNotFound.
func (s Store) Open(project, slug string) (Artifact, *os.File, error) {
	a, err := s.Get(project, slug)
	if err != nil {
		return Artifact{}, nil, err
	}
	f, err := os.Open(s.Path(project, slug))
	if errors.Is(err, fs.ErrNotExist) {
		return Artifact{}, nil, ErrNotFound
	}
	if err != nil {
		return Artifact{}, nil, fmt.Errorf("open artifact: %w", err)
	}
	return a, f, nil
}

// Remove deletes project/slug, or returns ErrNotFound.
func (s Store) Remove(project, slug string) error {
	if err := check(project, slug); err != nil {
		return err
	}
	return s.locked(syscall.LOCK_EX, func() error {
		idx, err := s.load()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(idx, func(o Artifact) bool { return o.Project == project && o.Slug == slug })
		if i < 0 {
			return ErrNotFound
		}
		if err := s.save(slices.Delete(idx, i, i+1)); err != nil {
			return err
		}
		if err := os.Remove(s.Path(project, slug)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove artifact: %w", err)
		}
		_ = os.Remove(filepath.Join(s.Dir, project)) // only succeeds once the folder is empty
		return nil
	})
}

// locked runs fn holding a lock on the folder: shared to read, exclusive to
// change, so a ds command and the web pages never see a half-written index.
// Reading a folder that doesn't exist yet needs no lock.
func (s Store) locked(how int, fn func() error) error {
	if how == syscall.LOCK_EX {
		if err := os.MkdirAll(s.Dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", s.Dir, err)
		}
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if errors.Is(err, fs.ErrNotExist) && how == syscall.LOCK_SH {
		return fn()
	}
	if err != nil {
		return fmt.Errorf("lock artifacts: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		return fmt.Errorf("lock artifacts: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

func (s Store) load() ([]Artifact, error) {
	data, err := os.ReadFile(s.indexFile())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read artifact index: %w", err)
	}
	var idx []Artifact
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("read artifact index %s: %w", s.indexFile(), err)
	}
	// Drop entries that could point outside the folder (a hand-edited index).
	return slices.DeleteFunc(idx, func(a Artifact) bool { return check(a.Project, a.Slug) != nil }), nil
}

func (s Store) save(idx []Artifact) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("write artifact index: %w", err)
	}
	return writeAtomic(s.indexFile(), append(data, '\n'))
}

// writeAtomic replaces path with data, so readers see the old or the new
// file, never part of one.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

// cleanTitle keeps titles to one printable line of at most 200 characters, so
// they can't move the cursor or recolour a terminal in ds artifact ls.
func cleanTitle(t string) string {
	t = strings.Join(strings.FieldsFunc(t, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }), " ")
	if r := []rune(t); len(r) > 200 {
		t = string(r[:200])
	}
	return t
}

// Ago says how long before now t was, briefly: "just now", "5m ago", "3d ago",
// then the date.
func Ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Local().Format("2 Jan 2006")
}
