package devproj

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Jenish-1235/dsbox/internal/sysexec"
)

// Template is a project template dev new can start from.
type Template struct {
	Name    string
	Dir     string
	Summary string
	// ADLC templates are created with ADLC's own `just new`.
	ADLC bool
}

// ADLCDir is the clone of the ADLC repo, whose templates come first.
func (s Settings) ADLCDir() string { return filepath.Join(s.Layout.Root, "adlc") }

// EmptyTemplate is the built-in choice that creates a project with only git,
// a README and ADLC's agent rules and skills: no app code, nothing to start.
const EmptyTemplate = "empty"

// summary describes the ADLC templates, which have no README of their own.
func summary(name string) string {
	switch name {
	case "nextjs":
		return "Next.js web app (App Router, TypeScript)"
	case "react-vite":
		return "React single-page app with Vite (TypeScript)"
	case "ddd-golang":
		return "Go HTTP service with domain-driven layers"
	case "python-fastapi":
		return "FastAPI service (Python, uv)"
	case EmptyTemplate:
		return "Empty project: git plus ADLC agent rules and skills, no code"
	}
	return ""
}

// hidden templates aren't ready to start from yet.
func hidden(name string) bool { return name == "event-driven-golang" }

// Templates lists the ADLC templates, then their own in ~/.devbox/templates.
// A name in both places is the ADLC one.
func (s Settings) Templates() []Template {
	var out []Template
	seen := map[string]bool{}
	for _, src := range []struct {
		dir  string
		adlc bool
	}{{filepath.Join(s.ADLCDir(), "templates"), true}, {s.Layout.Templates(), false}} {
		entries, _ := os.ReadDir(src.dir)
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || seen[e.Name()] || hidden(e.Name()) || e.Name() == EmptyTemplate {
				continue
			}
			seen[e.Name()] = true
			dir := filepath.Join(src.dir, e.Name())
			out = append(out, Template{Name: e.Name(), Dir: dir, Summary: orDefault(readmeSummary(dir), summary(e.Name())), ADLC: src.adlc})
		}
	}
	return append(out, Template{Name: EmptyTemplate, Summary: summary(EmptyTemplate)})
}

// newEmpty creates a project with git, a README, a .gitignore and ADLC's agent
// rules and skills (via ADLC's `just install`). There's nothing to start yet.
func (d Dev) newEmpty(ctx context.Context, name, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", d.S.Layout.Pretty(dest), err)
	}
	readme := fmt.Sprintf("# %s\n", name)
	if err := os.WriteFile(filepath.Join(dest, "README.md"), []byte(readme), 0o644); err != nil {
		return fmt.Errorf("write README.md: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".gitignore"), []byte(".env\n.env*.local\nnode_modules/\n"), 0o644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	if _, err := sysexec.Output(ctx, dest, "git", "init", "--quiet", "-b", "main"); err != nil {
		return fmt.Errorf("start git repo: %w", err)
	}
	justfile := filepath.Join(d.S.ADLCDir(), "justfile")
	if _, err := os.Stat(justfile); err == nil {
		if err := sysexec.Run(ctx, d.S.ADLCDir(), d.W, os.Stderr, "just", "--justfile", justfile, "install", dest); err != nil {
			return fmt.Errorf("add ADLC agent rules: %w", err)
		}
	} else {
		d.Out.Warn("ADLC isn't installed at %s; the project has no agent rules yet", d.S.Layout.Pretty(d.S.ADLCDir()))
	}
	if _, err := sysexec.Output(ctx, dest, "git", "add", "-A"); err != nil {
		return fmt.Errorf("stage files: %w", err)
	}
	if _, err := sysexec.Output(ctx, dest, "git", "commit", "--quiet", "-m", "Start "+name+": empty project with ADLC agent rules"); err != nil {
		return fmt.Errorf("make first commit: %w", err)
	}
	d.Out.OK("Created %s (empty: git, README, ADLC agent rules and skills)", d.S.Layout.Pretty(dest))
	d.Out.Hint("cd %s, then build it with claude; once it has a start command (a justfile dev recipe or .devrc), ds up runs it", d.S.Layout.Pretty(dest))
	return nil
}

// readmeSummary is the first non-empty line of README.md, without "#"s.
func readmeSummary(dir string) string {
	f, err := os.Open(filepath.Join(dir, "README.md"))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }() // read-only
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(strings.TrimLeft(sc.Text(), "# ")); line != "" {
			return line
		}
	}
	return ""
}

// New creates project name from template tmpl, commits it, runs `just setup`
// when there is one, and starts it.
func (d Dev) New(ctx context.Context, tmpl, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("%q isn't a project name; use lowercase letters, digits and - (e.g. %s)", name, orDefault(Name(name), "my-app"))
	}
	i := slices.IndexFunc(d.S.Templates(), func(t Template) bool { return t.Name == tmpl })
	if i < 0 {
		d.Out.Fail("No template called %q", tmpl)
		d.Out.Hint("ds templates lists them")
		return ErrReported
	}
	t := d.S.Templates()[i]
	dest := filepath.Join(d.S.ProjectsDir, name)
	if _, err := os.Stat(dest); err == nil {
		d.Out.Fail("%s already exists", d.S.Layout.Pretty(dest))
		d.Out.Hint("pick another name, or start it with: ds up %s", name)
		return ErrReported
	}
	if err := os.MkdirAll(d.S.ProjectsDir, 0o755); err != nil {
		return fmt.Errorf("create projects folder: %w", err)
	}
	d.Out.Title("Creating %s from %s", name, tmpl)
	if err := d.create(ctx, t, name, dest); err != nil {
		// dest didn't exist before: remove what was made of it, so trying
		// again doesn't find a broken project that "already exists".
		if rmErr := os.RemoveAll(dest); rmErr != nil {
			d.Out.Warn("Couldn't remove the unfinished %s: %v", d.S.Layout.Pretty(dest), rmErr)
		}
		return err
	}
	if tmpl == EmptyTemplate {
		return nil
	}
	d.Out.OK("Created %s", d.S.Layout.Pretty(dest))
	if HasRecipe(dest, "setup") {
		d.Out.Title("Running just setup")
		if err := sysexec.Run(ctx, dest, d.W, os.Stderr, "just", "setup"); err != nil {
			d.Out.Fail("just setup failed in %s", d.S.Layout.Pretty(dest))
			d.Out.Hint("fix it, then: cd %s && just setup && ds up", d.S.Layout.Pretty(dest))
			return ErrReported
		}
	}
	return d.Up(ctx, Project{Name: name, Dir: dest})
}

// create makes dest from template t, up to and including the first commit.
func (d Dev) create(ctx context.Context, t Template, name, dest string) error {
	if t.Name == EmptyTemplate {
		return d.newEmpty(ctx, name, dest)
	}
	if t.ADLC {
		justfile := filepath.Join(d.S.ADLCDir(), "justfile")
		if err := sysexec.Run(ctx, d.S.ADLCDir(), d.W, os.Stderr, "just", "--justfile", justfile, "new", t.Name, dest); err != nil {
			return fmt.Errorf("create %s with ADLC: %w", name, err)
		}
	} else {
		if err := copyTree(t.Dir, dest); err != nil {
			return fmt.Errorf("copy template %s: %w", t.Name, err)
		}
		if _, err := sysexec.Output(ctx, dest, "git", "init", "--quiet", "-b", "main"); err != nil {
			return fmt.Errorf("start git repo: %w", err)
		}
	}
	if err := Rename(dest, t.Name, name); err != nil {
		return err
	}
	if _, err := sysexec.Output(ctx, dest, "git", "add", "-A"); err != nil {
		return fmt.Errorf("stage files: %w", err)
	}
	msg := fmt.Sprintf("Start %s from the %s template", name, t.Name)
	if err := sysexec.Run(ctx, dest, d.W, os.Stderr, "git", "commit", "--quiet", "-m", msg); err != nil {
		d.Out.Warn("The first commit's checks failed (see above); committing without them")
		d.Out.Hint("fix what they report, then commit again; the checks run on every commit")
		if _, err := sysexec.Output(ctx, dest, "git", "commit", "--quiet", "--no-verify", "-m", msg); err != nil {
			return fmt.Errorf("make first commit: %w", err)
		}
	}
	return nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Rename puts the project name where the template name was: package.json
// "name", the go.mod module, the pyproject.toml name (and the project's entry
// in uv.lock, which uv otherwise rejects as stale), and the page title in
// src/app/layout.tsx and index.html.
func Rename(dir, from, to string) error {
	q := regexp.QuoteMeta(from)
	// title matches the template name on a line that mentions a title.
	title := regexp.MustCompile(`(?mi)^(.*title.*?)` + q + `()(.*)$`)
	pyName := regexp.MustCompile(`(?m)^(name\s*=\s*")` + q + `(")`)
	edits := []struct {
		file string
		re   *regexp.Regexp
	}{
		{"package.json", regexp.MustCompile(`("name"\s*:\s*")` + q + `(")`)},
		{"pyproject.toml", pyName},
		{"uv.lock", pyName},
		{"go.mod", regexp.MustCompile(`(?m)^(module\s+(?:\S*/)?)` + q + `()(\s*)$`)},
		{"src/app/layout.tsx", title},
		{"index.html", title},
	}
	for _, e := range edits {
		path := filepath.Join(dir, e.file)
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("rename project in %s: %w", e.file, err)
		}
		out := e.re.ReplaceAll(data, []byte("${1}"+to+"${2}${3}"))
		if string(out) == string(data) {
			continue
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return fmt.Errorf("rename project in %s: %w", e.file, err)
		}
	}
	return nil
}

// copyTree copies src to dst, skipping node_modules and .git.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && (e.Name() == "node_modules" || e.Name() == ".git") {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := e.Info()
		if err != nil {
			return err
		}
		switch {
		case e.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, info.Mode().Perm())
		}
	})
}
