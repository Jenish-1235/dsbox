// Command ds is the one devbox command: it runs the developer's projects (new, up, ls,
// logs, stop) and keeps the machine set up (update, doctor, rollback).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/Jenish-1235/dsbox/internal/component"
	"github.com/Jenish-1235/dsbox/internal/config"
	"github.com/Jenish-1235/dsbox/internal/devproj"
	"github.com/Jenish-1235/dsbox/internal/doctor"
	"github.com/Jenish-1235/dsbox/internal/inbox"
	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/state"
	"github.com/Jenish-1235/dsbox/internal/sysexec"
	"github.com/Jenish-1235/dsbox/internal/ui"
	"github.com/Jenish-1235/dsbox/internal/update"
)

// errReported means the problem was already printed; exit 1 quietly.
var errReported = errors.New("reported")

const usage = `ds runs your projects and keeps your devbox set up.

Projects:
  ds                        list projects and their links (same as ds ls)
  ds new                    pick a template (or empty) and a name, then start it
  ds new <template> <name>  the same, without questions
  ds templates              list the templates
  ds up [name]              start a project and print its link
  ds stop [name]            stop it
  ds logs [name] [-f]       show its output (-f: keep following)
  ds attach [name]          open its window (Ctrl-b d leaves it running)
  ds url [name]             print its link
  ds restore                start the projects that ran before a reboot (runs at boot)
  ds docker                 containers by project: logs, shell, ports, disk (ds d)

GitHub or GitLab (DEVBOX_FORGE in ds config; github when unset):
  ds publish [name]         push to origin; with none, first create a private repo
                            on the forge, then ask about a copy on the other one
  ds publish --github       gitlab: GitLab mirrors it to GitHub, no question (--no-github: don't)
  ds publish --gitlab       github: git push also updates a GitLab copy, no question (--no-gitlab: don't)
  ds mirrors                each GitHub mirror or GitLab copy, and whether it's in sync

Claude Code:
  ds notify setup           phone pushes when Claude is done or needs you (ntfy)
  ds notify [off|<message>] show whether they're on, stop them, or send one
  ds inbox                  screenshots sent from the laptop (Ctrl+Alt+V), newest first

Machine:
  ds status                 version, updates and health in one line each
  ds doctor [--fix]         check everything and explain how to fix problems
  ds update [ref]           get the latest devbox and apply it
  ds rollback               undo the last update
  ds diff                   how your files differ from the managed versions
  ds config                 the machine settings (/etc/devbox/devbox.env)
  ds cert                   trust the devbox's certificate on a laptop, for https links
  ds version                the installed version
  ds cheat                  everything you can do on the devbox (also at http://devbox)

Without a name, ds uses the project you're in. Each project keeps its port.
To choose how a project starts, put the command in a .devrc file in it, e.g.
  pnpm dev --port $PORT --host 0.0.0.0
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		if !isReported(err) {
			fmt.Fprintln(os.Stderr, "ds:", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	l, err := layout.FromEnv()
	if err != nil {
		return err
	}
	out := ui.New(os.Stdout)
	cmd, rest := "", []string(nil)
	if len(args) > 0 {
		cmd, rest = args[0], args[1:]
	}
	if isProjectCommand(cmd) {
		return runProject(ctx, l, cmd, rest, stdout)
	}
	switch cmd {
	case "":
		// The everyday view: the developer's projects, then any devbox notice (local, fast).
		if err := runProject(ctx, l, "ls", nil, stdout); err != nil {
			return err
		}
		notices(l, out)
		return nil
	case "docker", "d":
		return runDocker(ctx, l, rest, stdout)
	case "notify":
		return runNotify(ctx, l, rest, os.Stdin, stdout, out)
	case "inbox":
		return runInbox(l, out)
	case "status":
		return summary(ctx, l, out)
	case "update":
		ref := ""
		if len(rest) > 0 {
			ref = rest[0]
		}
		return update.New(l, out).Update(ctx, ref)
	case "rollback":
		return update.New(l, out).Rollback(ctx)
	case "apply":
		if err := update.Apply(ctx, l, out); err != nil {
			return err
		}
		// Refresh project names and the http://devbox page (cheatsheet changes).
		if err := devproj.LoadSettings(l).SyncAllRoutes(); err != nil {
			out.Warn("Couldn't update project names: %v", err)
		}
		return nil
	case "serve":
		// Run by the devbox-serve user service; Caddy forwards http://devbox here.
		return serve(ctx, l)
	case "cheat", "cheatsheet":
		data, err := os.ReadFile(devproj.LoadSettings(l).CheatsheetFile())
		if err != nil {
			return fmt.Errorf("read the cheatsheet (ds update installs it): %w", err)
		}
		_, err = stdout.Write(data)
		return err
	case "doctor":
		fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
		fix := fs.Bool("fix", false, "apply components and create missing folders")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return runDoctor(ctx, l, out, *fix)
	case "diff":
		return diff(ctx, l, out)
	case "config":
		return showConfig(l, out)
	case "cert":
		return cert(l, out, stdout)
	case "notices":
		// Called at every interactive shell start: local files only, never fails.
		notices(l, out)
		return nil
	case "check-updates":
		// Run daily by the devbox-check-updates systemd user timer.
		return checkUpdates(ctx, l, out)
	case "publish":
		return runPublish(ctx, l, rest, out)
	case "mirrors":
		return runMirrors(ctx, l, out)
	case "check-mirrors":
		// Run daily by the devbox-check-updates systemd user timer.
		return checkMirrors(ctx, l, out)
	case "version":
		return version(l, out)
	case "help", "-h", "--help":
		_, err := io.WriteString(stdout, usage)
		return err
	}
	_, _ = io.WriteString(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", cmd)
}

func summary(ctx context.Context, l layout.Layout, out ui.Printer) error {
	if err := version(l, out); err != nil {
		return err
	}
	// Use the daily check's result when it's recent; otherwise check now.
	head, _ := sysexec.Output(ctx, l.Src(), "git", "rev-parse", "HEAD")
	st, ok, _ := update.LoadStatus(l.UpdatesFile())
	if !ok || !st.Fresh(head, time.Now()) {
		if fresh, err := update.CheckForUpdates(ctx, l, 8*time.Second); err == nil {
			st, ok = fresh, true
		}
	}
	if ok && st.NewCommits > 0 {
		out.Warn("Update available on %s (%d new) → ds update", st.Target, st.NewCommits)
	}
	warn, fail := doctor.Counts(doctor.Run(ctx, l))
	switch {
	case fail > 0:
		out.Fail("%d problems, %d warnings → ds doctor", fail, warn)
	case warn > 0:
		out.Warn("%d warnings → ds doctor", warn)
	default:
		out.OK("Everything healthy")
	}
	return nil
}

func notices(l layout.Layout, out ui.Printer) {
	if st, ok, err := update.LoadStatus(l.UpdatesFile()); ok && err == nil {
		applied, _ := state.Load(l.StateFile()) // on error Current is empty, which is fine
		for _, n := range update.Notices(st, applied.Current, time.Now()) {
			if n.Update {
				out.Up("%s", n.Text)
			} else {
				out.Warn("%s", n.Text)
			}
		}
	}
	mirrorNotices(l, out)
}

func checkUpdates(ctx context.Context, l layout.Layout, out ui.Printer) error {
	// The daily timer also clears out week-old screenshots from the laptop.
	if n, err := inbox.Clean(l.Inbox(), inbox.Keep, time.Now()); err != nil {
		out.Warn("Couldn't remove old screenshots: %v", err)
	} else if n > 0 {
		out.OK("Removed %d old screenshot(s) from %s (kept a week)", n, l.Pretty(l.Inbox()))
	}
	st, err := update.CheckForUpdates(ctx, l, 60*time.Second)
	if err != nil {
		return fmt.Errorf("check for updates: %w", err)
	}
	if st.FetchError != "" {
		out.Warn("Couldn't fetch, compared with what was fetched before: %s", st.FetchError)
	}
	if st.NewCommits > 0 {
		out.Up("%d new on %s → ds update", st.NewCommits, st.Target)
	} else {
		out.OK("Up to date with %s", st.Target)
	}
	return nil
}

func runDoctor(ctx context.Context, l layout.Layout, out ui.Printer, fix bool) error {
	if fix {
		if err := update.Apply(ctx, l, out); err != nil {
			return err
		}
		out.Text("")
	}
	sections := doctor.Run(ctx, l)
	for _, s := range sections {
		out.Title("%s", s.Title)
		for _, r := range s.Results {
			line := r.Name
			if r.Detail != "" {
				line += out.Dim(" · " + r.Detail)
			}
			switch r.Level {
			case doctor.OK:
				out.OK("%s", line)
			case doctor.Warn:
				out.Warn("%s", line)
			case doctor.Fail:
				out.Fail("%s", line)
			}
			if r.Level != doctor.OK && r.Fix != "" {
				out.Hint("%s", r.Fix)
			}
		}
		out.Text("")
	}
	warn, fail := doctor.Counts(sections)
	switch {
	case fail > 0:
		out.Fail("%d problems, %d warnings", fail, warn)
		return errReported
	case warn > 0:
		out.Warn("%d warnings", warn)
	default:
		out.OK("Everything healthy")
	}
	return nil
}

func diff(ctx context.Context, l layout.Layout, out ui.Printer) error {
	comps, err := component.LoadAll(l.Components())
	if err != nil {
		return err
	}
	st, err := state.Load(l.StateFile())
	if err != nil {
		return err
	}
	shown := 0
	for _, c := range comps {
		if !c.Supports(runtime.GOOS) {
			continue
		}
		actions, err := c.Plan(l.Expand, st.Files)
		if err != nil {
			return err
		}
		for _, a := range actions {
			switch a.Status {
			case component.Same:
				continue
			case component.Missing:
				out.Warn("%s: %s doesn't exist yet → ds update", c.Name, l.Pretty(a.Dest))
				shown++
				continue
			}
			out.Title("%s: %s (%s)", c.Name, l.Pretty(a.Dest), a.Status)
			// git diff exits 1 when files differ; that's the expected case here.
			_ = sysexec.Run(ctx, "", os.Stdout, os.Stderr, "git", "--no-pager", "diff", "--no-index", "--color=auto", a.Dest, a.Src)
			shown++
		}
	}
	if shown == 0 {
		out.OK("Your files match the managed versions")
	}
	return nil
}

func showConfig(l layout.Layout, out ui.Printer) error {
	machine, repo := l.MachineConfig(), l.Pretty(l.RepoConfig())
	r := config.Inspect(machine, l.RepoConfig())
	switch {
	case r.Missing:
		out.Fail("%s doesn't exist yet", machine)
		out.Hint("%s", config.Fix)
		return errReported
	case r.Err != nil:
		out.Fail("%s has errors:", machine)
		for line := range strings.Lines(r.Err.Error()) {
			out.Text("    %s", strings.TrimSpace(line))
		}
		out.Hint("fix config/devbox.env in the repo, then %s", config.Fix)
		return errReported
	}

	out.Title("Machine settings")
	out.Text("%s", out.Dim(machine))
	values := map[string]string{}
	for _, e := range r.Entries {
		values[e.Key] = e.Value
	}
	// The developer's own settings override the machine's, and the shell loads
	// both, so the environment normally agrees with the result.
	user := map[string]string{}
	if data, err := os.ReadFile(l.UserConfig()); err == nil {
		if entries, err := config.Parse(data); err == nil {
			for _, e := range entries {
				user[e.Key] = e.Value
			}
		}
	}
	for _, key := range config.Keys() {
		value, note := values[key], ""
		if u, ok := user[key]; ok {
			value, note = u, fmt.Sprintf(" · from %s", l.Pretty(l.UserConfig()))
		}
		// Say so when something else (e.g. ~/.zshrc.local) overrides it.
		if env, ok := os.LookupEnv(key); ok && env != value {
			value, note = env, fmt.Sprintf(" · from your environment; the settings say %q", value)
		}
		if value == "" {
			value = out.Dim("(not set)")
		}
		out.Text("  %-20s %s%s", key, value, out.Dim(note))
	}
	out.Text("")

	for _, p := range r.Problems {
		out.Fail("%s", p)
	}
	switch {
	case len(r.Problems) > 0 && r.Differs:
		out.Hint("%s", config.Fix)
	case len(r.Problems) > 0:
		out.Hint("fix config/devbox.env in the repo, then %s", config.Fix)
	default:
		out.OK("Settings are valid")
	}
	switch {
	case r.RepoErr != nil:
		out.Warn("Can't compare with the repo: %v", r.RepoErr)
	case r.Differs:
		out.Warn("Differs from %s:", repo)
		for _, c := range r.Changes {
			out.Item("%s", c)
		}
		out.Hint("%s", config.Fix)
	default:
		out.OK("Same as %s", repo)
	}
	if len(r.Problems) > 0 {
		return errReported
	}
	return nil
}

func version(l layout.Layout, out ui.Printer) error {
	rev, at, modified := "unknown", "", false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.time":
				at = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if modified {
		rev += " (with local changes)"
	}
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		at = t.Local().Format("2 Jan 2006 15:04")
	}
	desc, _ := sysexec.Output(context.Background(), l.Src(), "git", "describe", "--tags", "--always")
	out.Title("devbox %s", strings.TrimSpace(desc))
	out.Text("%s", out.Dim(fmt.Sprintf("ds built from %s %s", rev, at)))
	return nil
}
