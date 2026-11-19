package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"charm.land/huh/v2"

	"github.com/Jenish-1235/dsbox/internal/config"
	"github.com/Jenish-1235/dsbox/internal/devproj"
	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/publish"
	"github.com/Jenish-1235/dsbox/internal/sysexec"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

// runPublish is ds publish [name] [--github|--no-github]. The flags only
// apply to the gitlab forge; publish.Publisher says so for github.
func runPublish(ctx context.Context, l layout.Layout, args []string, out ui.Printer) error {
	flags, pos := splitFlags(args)
	choice := publish.AskGitHub
	for f := range flags {
		switch f {
		case "--github":
			choice = publish.WithGitHub
		case "--no-github":
			choice = publish.WithoutGitHub
		default:
			return fmt.Errorf("unknown flag %s (ds publish [name] [--github|--no-github])", f)
		}
	}
	if flags["--github"] && flags["--no-github"] {
		return errors.New("choose one of --github and --no-github")
	}
	arg := ""
	if len(pos) > 0 {
		arg = pos[0]
	}
	d := devproj.Dev{S: devproj.LoadSettings(l), Out: out, W: os.Stdout}
	p, err := d.Resolve(arg)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(p.Dir); err != nil || !fi.IsDir() {
		out.Fail("No project %s in %s", arg, l.Pretty(d.S.ProjectsDir))
		out.Hint("ds lists your projects")
		return errReported
	}
	pb := publish.Publisher{Run: publish.Exec{}, Out: out, Forge: d.S.Forge, StatusFile: l.MirrorsFile()}
	if interactive() {
		pb.Ask = askMirror
	}
	return pb.Publish(ctx, publish.Project{Name: p.Name, Dir: p.Dir}, choice)
}

// askMirror asks whether to mirror to GitHub; the default is no.
func askMirror(ctx context.Context, name string) (bool, error) {
	yes := false
	err := huh.NewForm(huh.NewGroup(huh.NewConfirm().
		Title("Mirror to GitHub? (y/N)").
		Description("A private, read-only copy of " + name + " on GitHub, kept in sync by GitLab").
		Affirmative("Yes").
		Negative("No").
		Value(&yes))).RunWithContext(ctx)
	if errors.Is(err, huh.ErrUserAborted) {
		return false, errReported
	}
	if err != nil {
		return false, fmt.Errorf("ask about GitHub: %w", err)
	}
	return yes, nil
}

// mirroring reports whether GitHub mirrors apply here: only GitLab projects
// have them, so only the gitlab forge checks and reports them.
func mirroring(l layout.Layout) bool {
	return devproj.LoadSettings(l).Forge == config.ForgeGitLab
}

// runMirrors is ds mirrors: every GitHub mirror, checked now.
func runMirrors(ctx context.Context, l layout.Layout, out ui.Printer) error {
	if !mirroring(l) {
		out.Text("No mirrors to check: %s is github, and mirrors are for GitLab projects.", config.Forge)
		return nil
	}
	prev, _, _ := publish.LoadStatus(l.MirrorsFile()) // a broken cache is simply replaced
	st, err := publish.Check(ctx, publish.Exec{}, "", prev)
	if err != nil {
		out.Fail("Can't list mirrors: %v", err)
		out.Hint("glab auth status shows whether you're logged in to GitLab")
		return errReported
	}
	if err := st.Save(l.MirrorsFile()); err != nil {
		out.Warn("Couldn't save the mirror status: %v", err)
	}
	publish.Report(out, st, time.Now())
	return nil
}

// checkMirrors is ds check-mirrors, run daily by devbox-check-updates.service:
// it saves every mirror's state for ds notices.
func checkMirrors(ctx context.Context, l layout.Layout, out ui.Printer) error {
	if !mirroring(l) {
		out.OK("%s is github; no mirrors to check", config.Forge)
		return nil
	}
	if !sysexec.Exists("glab") {
		out.OK("glab isn't installed; no mirrors to check")
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	prev, _, _ := publish.LoadStatus(l.MirrorsFile()) // a broken cache is simply replaced
	st, err := publish.Check(ctx, publish.Exec{}, "", prev)
	if err != nil {
		// Keep the last known mirrors; say why this check didn't finish.
		prev.CheckedAt, prev.Error = time.Now().UTC(), err.Error()
		if serr := prev.Save(l.MirrorsFile()); serr != nil {
			return errors.Join(err, serr)
		}
		return fmt.Errorf("check mirrors: %w", err)
	}
	if err := st.Save(l.MirrorsFile()); err != nil {
		return err
	}
	if st.Error != "" {
		out.Warn("%s", st.Error)
	}
	failed := 0
	for _, m := range st.Mirrors {
		if m.Failed() {
			failed++
		}
	}
	switch {
	case failed > 0:
		out.Fail("%d of %d mirrors failing → ds mirrors", failed, len(st.Mirrors))
	case len(st.Mirrors) == 0:
		out.OK("No GitHub mirrors to check")
	default:
		out.OK("%d mirrors checked, none failing", len(st.Mirrors))
	}
	return nil
}

// mirrorNotices prints a line per failing mirror from the cached check. With
// the github forge nothing checks mirrors, so an old cache says nothing.
func mirrorNotices(l layout.Layout, out ui.Printer) {
	st, ok, err := publish.LoadStatus(l.MirrorsFile())
	if !ok || err != nil || !mirroring(l) {
		return
	}
	for _, n := range publish.Notices(st, time.Now()) {
		out.Warn("%s", n)
	}
}
