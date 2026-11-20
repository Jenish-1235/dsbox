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

// runPublish is ds publish [name] [--github|--no-github|--gitlab|--no-gitlab].
// --github adds a GitHub mirror to a GitLab project (the gitlab forge),
// --gitlab a GitLab copy to a GitHub project (the github forge);
// publish.Publisher says when a flag doesn't fit.
func runPublish(ctx context.Context, l layout.Layout, args []string, out ui.Printer) error {
	flags, pos := splitFlags(args)
	if len(flags) > 1 {
		return errors.New("choose one of --github, --no-github, --gitlab and --no-gitlab")
	}
	var pf publish.Flags
	for f := range flags {
		switch f {
		case "--github":
			pf = publish.Flags{Choice: publish.WithMirror, To: config.ForgeGitHub}
		case "--no-github":
			pf = publish.Flags{Choice: publish.WithoutMirror, To: config.ForgeGitHub}
		case "--gitlab":
			pf = publish.Flags{Choice: publish.WithMirror, To: config.ForgeGitLab}
		case "--no-gitlab":
			pf = publish.Flags{Choice: publish.WithoutMirror, To: config.ForgeGitLab}
		default:
			return fmt.Errorf("unknown flag %s (ds publish [name] [--github|--no-github|--gitlab|--no-gitlab])", f)
		}
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
	return pb.Publish(ctx, publish.Project{Name: p.Name, Dir: p.Dir}, pf)
}

// askMirror asks whether to keep a copy of name on to (GitHub or GitLab);
// the default is no.
func askMirror(ctx context.Context, name, to string) (bool, error) {
	title, desc := "Mirror to GitHub? (y/N)", "A private, read-only copy of "+name+" on GitHub, kept in sync by GitLab"
	if to == "GitLab" {
		title, desc = "Copy to GitLab? (y/N)", "A private copy of "+name+" on GitLab; git push from here updates GitHub and GitLab"
	}
	yes := false
	err := huh.NewForm(huh.NewGroup(huh.NewConfirm().
		Title(title).
		Description(desc).
		Affirmative("Yes").
		Negative("No").
		Value(&yes))).RunWithContext(ctx)
	if errors.Is(err, huh.ErrUserAborted) {
		return false, errReported
	}
	if err != nil {
		return false, fmt.Errorf("ask about %s: %w", to, err)
	}
	return yes, nil
}

// copies reports whether the forge is github, where projects get copies on
// GitLab; with gitlab they get mirrors on GitHub.
func copies(l layout.Layout) bool {
	return devproj.LoadSettings(l).Forge != config.ForgeGitLab
}

// checkAll checks the forge's kind: copies on GitLab or mirrors to GitHub.
func checkAll(ctx context.Context, l layout.Layout, prev publish.Status) (publish.Status, error) {
	if copies(l) {
		return publish.CheckCopies(ctx, publish.Exec{}, "", prev)
	}
	return publish.Check(ctx, publish.Exec{}, "", prev)
}

// runMirrors is ds mirrors: every GitHub mirror (gitlab forge) or GitLab
// copy (github forge), checked now.
func runMirrors(ctx context.Context, l layout.Layout, out ui.Printer) error {
	prev, _, _ := publish.LoadStatus(l.MirrorsFile()) // a broken cache is simply replaced
	st, err := checkAll(ctx, l, prev)
	if err != nil {
		out.Fail("Can't list mirrors: %v", err)
		out.Hint("glab auth status shows whether you're logged in to GitLab")
		return errReported
	}
	if err := st.Save(l.MirrorsFile()); err != nil {
		out.Warn("Couldn't save the mirror status: %v", err)
	}
	if copies(l) {
		publish.ReportCopies(out, st)
		return nil
	}
	publish.Report(out, st, time.Now())
	return nil
}

// checkMirrors is ds check-mirrors, run daily by devbox-check-updates.service:
// it saves every mirror's (or copy's) state for ds notices.
func checkMirrors(ctx context.Context, l layout.Layout, out ui.Printer) error {
	if !sysexec.Exists("glab") {
		out.OK("glab isn't installed; no mirrors or copies to check")
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	prev, _, _ := publish.LoadStatus(l.MirrorsFile()) // a broken cache is simply replaced
	st, err := checkAll(ctx, l, prev)
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
	what := "mirrors"
	if copies(l) {
		what = "GitLab copies"
	}
	switch {
	case failed > 0:
		out.Fail("%d of %d %s need attention → ds mirrors", failed, len(st.Mirrors), what)
	case len(st.Mirrors) == 0:
		out.OK("No %s to check", what)
	default:
		out.OK("%d %s checked, all fine", len(st.Mirrors), what)
	}
	return nil
}

// mirrorNotices prints a line per failing mirror or copy from the cached
// check, of the kind the forge uses.
func mirrorNotices(l layout.Layout, out ui.Printer) {
	st, ok, err := publish.LoadStatus(l.MirrorsFile())
	if !ok || err != nil {
		return
	}
	st = st.For(devproj.LoadSettings(l).Forge)
	for _, n := range publish.Notices(st, time.Now()) {
		out.Warn("%s", n)
	}
}
