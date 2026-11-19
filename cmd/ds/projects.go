package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"charm.land/huh/v2"

	"github.com/Jenish-1235/dsbox/internal/devproj"
	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/publish"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

// isProjectCommand reports whether cmd is a ds subcommand that runs the developer's projects.
func isProjectCommand(cmd string) bool {
	switch cmd {
	case "ls", "list", "up", "start", "stop", "logs", "log", "attach", "url", "new", "templates", "restore":
		return true
	}
	return false
}

// runProject handles a project subcommand such as ds up or ds new.
func runProject(ctx context.Context, l layout.Layout, cmd string, rest []string, stdout io.Writer) error {
	d := devproj.Dev{S: devproj.LoadSettings(l), Out: ui.New(os.Stdout), W: stdout}
	flags, pos := splitFlags(rest)
	arg := ""
	if len(pos) > 0 {
		arg = pos[0]
	}
	switch cmd {
	case "ls", "list":
		return listProjects(ctx, d, flags["--brief"])
	case "up", "start":
		p, err := d.Resolve(arg)
		if err != nil {
			return err
		}
		return d.Up(ctx, p)
	case "stop":
		p, err := d.Resolve(arg)
		if err != nil {
			return err
		}
		return d.Stop(ctx, p)
	case "logs", "log":
		p, err := d.Resolve(arg)
		if err != nil {
			return err
		}
		return projectLogs(ctx, d, p, flags["-f"] || flags["--follow"])
	case "attach":
		p, err := d.Resolve(arg)
		if err != nil {
			return err
		}
		return attach(d, p)
	case "url":
		p, err := d.Resolve(arg)
		if err != nil {
			return err
		}
		u, ok := d.URL(p)
		if !ok {
			d.Out.Fail("%s has no port yet", p.Name)
			d.Out.Hint("ds up %s gives it one", p.Name)
			return errReported
		}
		fmt.Fprintln(stdout, u)
		return nil
	case "new":
		if len(pos) >= 2 {
			return d.New(ctx, pos[0], pos[1])
		}
		if !interactive() {
			if len(pos) == 1 {
				d.Out.Fail("Name the project: ds new %s <name>", pos[0])
				return errReported
			}
			return listTemplates(d, stdout)
		}
		tmpl, name, err := pickNew(ctx, d, pos)
		if err != nil {
			return err
		}
		return d.New(ctx, tmpl, name)
	case "templates":
		return listTemplates(d, stdout)
	case "restore":
		// Run at boot by the devbox-restore user service; quiet when all is running.
		return d.Restore(ctx)
	}
	return fmt.Errorf("unknown project command %q", cmd)
}

// splitFlags separates -x/--xyz flags from positional arguments.
func splitFlags(args []string) (map[string]bool, []string) {
	flags := map[string]bool{}
	var pos []string
	for _, a := range args {
		if len(a) > 1 && a[0] == '-' {
			flags[a] = true
		} else {
			pos = append(pos, a)
		}
	}
	return flags, pos
}

// listProjects prints projects. --brief is read at every shell start: only
// running projects, "<name>\t<url>" per line, and nothing at all when none run.
func listProjects(ctx context.Context, d devproj.Dev, brief bool) error {
	rows := d.List(ctx, brief)
	if brief {
		for _, r := range rows {
			fmt.Fprintf(d.W, "%s\t%s\n", r.Name, r.URL)
		}
		return nil
	}
	if len(rows) == 0 {
		d.Out.Text("No projects in %s yet.", d.S.Layout.Pretty(d.S.ProjectsDir))
		d.Out.Hint("ds new <template> <name> creates one; ds templates lists them")
		return nil
	}
	width := 0
	for _, r := range rows {
		width = max(width, len(r.Name))
	}
	for _, r := range rows {
		if r.Running {
			fmt.Fprintf(d.W, "● %-*s  %s\n", width, r.Name, r.URL)
		} else {
			fmt.Fprintf(d.W, "%s\n", d.Out.Dim(fmt.Sprintf("○ %-*s  stopped", width, r.Name)))
		}
	}
	return nil
}

func projectLogs(ctx context.Context, d devproj.Dev, p devproj.Project, follow bool) error {
	file := d.S.LogFile(p.Name)
	if _, err := os.Stat(file); err != nil {
		d.Out.Fail("No log for %s yet", p.Name)
		d.Out.Hint("ds up %s starts it and writes %s", p.Name, d.S.Layout.Pretty(file))
		return errReported
	}
	args := []string{"-n", "100"}
	if follow {
		args = append(args, "-F")
	}
	cmd := exec.CommandContext(ctx, "tail", append(args, file)...)
	cmd.Stdout, cmd.Stderr = d.W, os.Stderr
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("show log %s: %w", file, err)
	}
	return nil
}

func attach(d devproj.Dev, p devproj.Project) error {
	if !devproj.Running(context.Background())[p.Name] {
		d.Out.Fail("%s isn't running", p.Name)
		d.Out.Hint("ds up %s starts it", p.Name)
		return errReported
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("find tmux: %w", err)
	}
	args := []string{"tmux", "attach-session", "-t", devproj.Target(p.Name)}
	if os.Getenv("TMUX") != "" {
		args = []string{"tmux", "switch-client", "-t", devproj.Target(p.Name)}
	}
	if err := syscall.Exec(tmux, args, os.Environ()); err != nil {
		return fmt.Errorf("attach to %s: %w", p.Name, err)
	}
	return nil
}

func listTemplates(d devproj.Dev, w io.Writer) error {
	ts := d.S.Templates()
	if len(ts) == 0 {
		d.Out.Text("No templates yet.")
		d.Out.Hint("templates come from %s/templates and %s", d.S.Layout.Pretty(d.S.ADLCDir()), d.S.Layout.Pretty(d.S.Layout.Templates()))
		return nil
	}
	width := 0
	for _, t := range ts {
		width = max(width, len(t.Name))
	}
	d.Out.Title("Templates (ds new <template> <name>):")
	for _, t := range ts {
		fmt.Fprintf(w, "  %-*s  %s\n", width, t.Name, d.Out.Dim(t.Summary))
	}
	return nil
}

// interactive reports whether ds can ask questions: stdin and stdout are a terminal.
func interactive() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		fi, err := f.Stat()
		if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

// pickNew asks for the template (unless given) and the project name.
func pickNew(ctx context.Context, d devproj.Dev, pos []string) (string, string, error) {
	ts := d.S.Templates()
	width := 0
	for _, t := range ts {
		width = max(width, len(t.Name))
	}
	options := make([]huh.Option[string], 0, len(ts))
	for _, t := range ts {
		options = append(options, huh.NewOption(fmt.Sprintf("%-*s  %s", width, t.Name, t.Summary), t.Name))
	}
	tmpl, name := "", ""
	if len(pos) == 1 {
		tmpl = pos[0]
	}
	var fields []huh.Field
	if tmpl == "" {
		fields = append(fields, huh.NewSelect[string]().Title("Template").Options(options...).Value(&tmpl))
	}
	fields = append(fields, huh.NewInput().
		Title("Project name").
		Description("lowercase letters, digits and -; it becomes http://<name>."+orHost(d)).
		Placeholder("my-app").
		Validate(func(s string) error { return checkNewName(d, s) }).
		Value(&name))
	if err := huh.NewForm(huh.NewGroup(fields...)).RunWithContext(ctx); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", "", errReported
		}
		return "", "", fmt.Errorf("ask for the project: %w", err)
	}
	return tmpl, name, nil
}

// checkNewName says why s can't be the name of a new project, if it can't.
func checkNewName(d devproj.Dev, s string) error {
	if !devproj.ValidName(s) {
		return errors.New("use lowercase letters, digits and - (e.g. my-app)")
	}
	dest := filepath.Join(d.S.ProjectsDir, s)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists", d.S.Layout.Pretty(dest))
	}
	return nil
}

// orHost is the apps domain for the name hint, or the host when names are off.
func orHost(d devproj.Dev) string {
	if d.S.AppsDomain != "" {
		return d.S.AppsDomain
	}
	return d.S.Host
}

// isReported reports whether err was already printed for the user.
func isReported(err error) bool {
	return errors.Is(err, errReported) || errors.Is(err, devproj.ErrReported) || errors.Is(err, publish.ErrReported)
}
