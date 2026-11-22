package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Jenish-1235/dsbox/internal/artifact"
	"github.com/Jenish-1235/dsbox/internal/devproj"
	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

const artifactUsage = `ds artifact submit <file.html> --title "…" [--project <name>] [--slug <name>]
ds artifact ls
ds artifact url <project>/<slug>
ds artifact rm <project>/<slug>`

// artifactCmd is ds artifact: the settings for links, the store, and where to print.
type artifactCmd struct {
	S     devproj.Settings
	Store artifact.Store
	Out   ui.Printer
	W     io.Writer
}

func runArtifact(l layout.Layout, args []string, stdout io.Writer, out ui.Printer) error {
	c := artifactCmd{S: devproj.LoadSettings(l), Store: artifact.Store{Dir: l.Artifacts()}, Out: out, W: stdout}
	return c.run(args)
}

func (c artifactCmd) run(args []string) error {
	sub, rest := "", []string(nil)
	if len(args) > 0 {
		sub, rest = args[0], args[1:]
	}
	opts, pos, err := parseOptions(rest, "--title", "--project", "--slug")
	if err != nil {
		return err
	}
	switch sub {
	case "submit", "add":
		if len(pos) != 1 {
			return c.usage("give one HTML file")
		}
		return c.submit(pos[0], opts)
	case "ls", "list", "":
		return c.list()
	case "url":
		if len(pos) != 1 {
			return c.usage("give the artifact as <project>/<slug>")
		}
		a, err := c.get(pos[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(c.W, c.link(a))
		return nil
	case "rm", "remove":
		if len(pos) != 1 {
			return c.usage("give the artifact as <project>/<slug>")
		}
		project, slug, err := c.id(pos[0])
		if err != nil {
			return err
		}
		if err := c.Store.Remove(project, slug); errors.Is(err, artifact.ErrNotFound) {
			return c.notFound(project + "/" + slug)
		} else if err != nil {
			return err
		}
		c.Out.OK("Removed %s/%s", project, slug)
		return nil
	}
	return c.usage(fmt.Sprintf("unknown command %q", sub))
}

func (c artifactCmd) usage(problem string) error {
	c.Out.Fail("%s", problem)
	c.Out.Text("%s", artifactUsage)
	return errReported
}

func (c artifactCmd) submit(file string, opts map[string]string) error {
	if ext := strings.ToLower(filepath.Ext(file)); ext != ".html" && ext != ".htm" {
		c.Out.Fail("%s isn't an HTML file", file)
		c.Out.Hint("build the artifact as one self-contained .html file (inline CSS and JS, or pinned CDN links)")
		return errReported
	}
	title := strings.TrimSpace(opts["--title"])
	if title == "" {
		return c.usage(`give it a title: --title "…"`)
	}
	project := opts["--project"]
	if project == "" {
		// The project you're in, as ds up names it.
		p, err := devproj.Dev{S: c.S}.Resolve("")
		if err != nil || !artifact.ValidName(p.Name) {
			c.Out.Fail("Can't tell which project this is for")
			c.Out.Hint("name it: --project <name>")
			return errReported
		}
		project = p.Name
	}
	slug := opts["--slug"]
	if slug == "" {
		slug = artifact.CleanName(strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)))
	}
	f, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("open %s: %w", file, err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > artifact.MaxSize {
		return fmt.Errorf("%s: %w", file, artifact.ErrTooLarge)
	}
	a, replaced, err := c.Store.Submit(project, slug, title, f)
	if err != nil {
		return err
	}
	verb := "Submitted"
	if replaced {
		verb = "Replaced"
	}
	c.Out.OK("%s %s · %s", verb, a.ID(), size(a.Size))
	fmt.Fprintln(c.W, c.link(a))
	return nil
}

func (c artifactCmd) list() error {
	all, err := c.Store.List()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		c.Out.Text("No artifacts yet.")
		c.Out.Hint(`ds artifact submit <file.html> --title "…" adds one`)
		return nil
	}
	width := 0
	for _, a := range all {
		width = max(width, len(a.ID()))
	}
	now := time.Now()
	for _, a := range all {
		fmt.Fprintf(c.W, "%-*s  %s  %s\n", width, a.ID(), c.Out.Dim(fmt.Sprintf("%-9s", artifact.Ago(a.Updated, now))), a.Title)
	}
	if hub := c.S.ArtifactsURL(); hub != "" {
		c.Out.Hint("all of them: %s", hub)
	}
	return nil
}

// id reads "<project>/<slug>", or a bare slug in the project you're in.
func (c artifactCmd) id(arg string) (string, string, error) {
	if !strings.Contains(arg, "/") {
		if p, err := (devproj.Dev{S: c.S}).Resolve(""); err == nil && artifact.ValidName(p.Name) {
			arg = p.Name + "/" + arg
		}
	}
	project, slug, err := artifact.ParseID(arg)
	if err != nil {
		c.Out.Fail("%v", err)
		c.Out.Hint("ds artifact ls lists them")
		return "", "", errReported
	}
	return project, slug, nil
}

func (c artifactCmd) get(arg string) (artifact.Artifact, error) {
	project, slug, err := c.id(arg)
	if err != nil {
		return artifact.Artifact{}, err
	}
	a, err := c.Store.Get(project, slug)
	if errors.Is(err, artifact.ErrNotFound) {
		return a, c.notFound(project + "/" + slug)
	}
	return a, err
}

func (c artifactCmd) notFound(id string) error {
	c.Out.Fail("No artifact %s", id)
	c.Out.Hint("ds artifact ls lists them")
	return errReported
}

// link is the artifact's page on the hub, or its file when the devbox has no
// apps domain (and so no pages).
func (c artifactCmd) link(a artifact.Artifact) string {
	if hub := c.S.ArtifactsURL(); hub != "" {
		return hub + "/" + a.ID()
	}
	return c.Store.Path(a.Project, a.Slug)
}

// parseOptions reads --name value and --name=value for the names given,
// anywhere among the arguments; everything else is positional.
func parseOptions(args []string, names ...string) (map[string]string, []string, error) {
	opts := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		name, value, hasValue := strings.Cut(a, "=")
		if !slices.Contains(names, name) {
			return nil, nil, fmt.Errorf("unknown option %s", name)
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("%s needs a value", name)
			}
			i++
			value = args[i]
		}
		opts[name] = value
	}
	return opts, pos, nil
}

func size(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
