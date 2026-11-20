// Package doctor checks the machine and explains how to fix what's wrong.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jenish-1235/dsbox/internal/component"
	"github.com/Jenish-1235/dsbox/internal/config"
	"github.com/Jenish-1235/dsbox/internal/devproj"
	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/notify"
	"github.com/Jenish-1235/dsbox/internal/publish"
	"github.com/Jenish-1235/dsbox/internal/state"
	"github.com/Jenish-1235/dsbox/internal/sysexec"
	"github.com/Jenish-1235/dsbox/internal/update"
)

// Level is how serious a result is.
type Level int

const (
	// OK means healthy.
	OK Level = iota
	// Warn means it works but needs attention.
	Warn
	// Fail means it's broken.
	Fail
)

// Result is one check's outcome.
type Result struct {
	Name   string
	Level  Level
	Detail string
	Fix    string
}

// Section groups results under a heading.
type Section struct {
	Title   string
	Results []Result
}

// Counts returns how many warnings and failures sections contain.
func Counts(sections []Section) (warn, fail int) {
	for _, s := range sections {
		for _, r := range s.Results {
			switch r.Level {
			case Warn:
				warn++
			case Fail:
				fail++
			}
		}
	}
	return warn, fail
}

// Run runs every check.
func Run(ctx context.Context, l layout.Layout) []Section {
	sections := []Section{devboxSection(ctx, l)}
	comps, err := component.LoadAll(l.Components())
	if err != nil {
		sections = append(sections, Section{Title: "Components", Results: []Result{{
			Name: "component definitions", Level: Fail, Detail: err.Error(),
			Fix: "run ds update, or check ~/.devbox/src",
		}}})
	} else {
		// Without the state, an edited file and an outdated one look the
		// same, so files aren't judged; the Devbox section says why.
		var written map[string]string
		if st, err := state.Load(l.StateFile()); err == nil {
			written = st.Files
		}
		env := sysexec.UserBusEnv()
		for _, c := range comps {
			if !c.Supports(runtime.GOOS) {
				continue
			}
			sections = append(sections, componentSection(ctx, l, c, written, env))
		}
	}
	return append(sections, systemSection(ctx, l), servicesSection(ctx, l))
}

// stateResult checks that ds can read what it has applied (ds.json); update,
// rollback and diff all need it.
func stateResult(l layout.Layout) Result {
	if _, err := state.Load(l.StateFile()); err != nil {
		return Result{Name: "applied state", Level: Fail, Detail: err.Error(),
			Fix: "fix or remove " + l.Pretty(l.StateFile()) + "; ds update then rebuilds it, keeping a backup of any file it replaces"}
	}
	return Result{Name: "applied state", Level: OK, Detail: l.Pretty(l.StateFile())}
}

func devboxSection(ctx context.Context, l layout.Layout) Section {
	s := Section{Title: "Devbox"}
	var missing []string
	for _, dir := range l.Dirs() {
		if _, err := os.Stat(dir); err != nil {
			missing = append(missing, l.Pretty(dir))
		}
	}
	if len(missing) > 0 {
		s.Results = append(s.Results, Result{Name: "folders", Level: Warn,
			Detail: "missing " + strings.Join(missing, ", "), Fix: "ds doctor --fix"})
	} else {
		s.Results = append(s.Results, Result{Name: "folders", Level: OK, Detail: l.Pretty(l.Root)})
	}

	src := l.Src()
	switch status, err := sysexec.Output(ctx, src, "git", "status", "--porcelain"); {
	case err != nil:
		s.Results = append(s.Results, Result{Name: "source", Level: Fail,
			Detail: "no git clone at " + l.Pretty(src), Fix: "run install.sh"})
	case status != "":
		s.Results = append(s.Results, Result{Name: "source", Level: Warn,
			Detail: "changed by hand; ds update will refuse to run",
			Fix:    "cd " + l.Pretty(src) + " && git status"})
	default:
		head, _ := sysexec.Output(ctx, src, "git", "log", "-1", "--format=%h %cs")
		s.Results = append(s.Results, Result{Name: "source", Level: OK, Detail: head})
	}

	bin := filepath.Join(l.Bin(), "ds")
	onPath := slices.Contains(filepath.SplitList(os.Getenv("PATH")), l.Bin())
	if onPath {
		s.Results = append(s.Results, Result{Name: "on PATH", Level: OK, Detail: l.Pretty(bin)})
	} else {
		s.Results = append(s.Results, Result{Name: "on PATH", Level: Warn,
			Detail: l.Pretty(l.Bin()) + " isn't on PATH", Fix: "ds update (installs ~/.zshenv), then open a new terminal"})
	}
	s.Results = append(s.Results, stateResult(l))
	// /etc/devbox only exists on the devbox itself, not on a Mac used to work on this repo.
	if runtime.GOOS == "linux" {
		s.Results = append(s.Results, machineConfigResult(l))
	}
	return s
}

func machineConfigResult(l layout.Layout) Result {
	r := config.Inspect(l.MachineConfig(), l.RepoConfig())
	res := Result{Name: "machine settings", Fix: config.Fix}
	switch {
	case r.Missing:
		res.Level, res.Detail = Warn, l.MachineConfig()+" doesn't exist yet"
	case r.Err != nil:
		res.Level, res.Detail, res.Fix = Fail, "can't be read", "ds config shows what's wrong"
	case len(r.Problems) > 0:
		res.Level, res.Detail, res.Fix = Fail, fmt.Sprintf("%d invalid", len(r.Problems)), "ds config shows what's wrong"
	case r.RepoErr != nil:
		res.Level, res.Detail, res.Fix = Warn, "can't compare with the repo: "+r.RepoErr.Error(), ""
	case r.Differs:
		res.Level, res.Detail = Warn, "differ from the repo's config/devbox.env (ds config)"
	default:
		res.Level, res.Detail = OK, l.MachineConfig()+", same as the repo"
	}
	return res
}

// componentSection checks c's commands, files and checks. written is what ds
// last wrote (from ds.json); nil means it's unknown, and files aren't judged.
// Checks run with env, which should include the user bus (systemctl --user).
func componentSection(ctx context.Context, l layout.Layout, c component.Component, written map[string]string, env []string) Section {
	s := Section{Title: "Component: " + c.Name}
	var missing, installs, manual []string
	for _, r := range c.Requires {
		if sysexec.Exists(r.Cmd) {
			continue
		}
		missing = append(missing, r.Cmd)
		if r.Install != "" {
			installs = append(installs, r.Install)
		} else {
			manual = append(manual, r.Cmd)
		}
	}
	switch {
	case len(c.Requires) == 0:
	case len(missing) > 0:
		var fixes []string
		if len(installs) > 0 {
			fixes = append(fixes, strings.Join(installs, " && "))
		}
		if len(manual) > 0 {
			fixes = append(fixes, "install "+strings.Join(manual, ", ")+" (see provision steps)")
		}
		s.Results = append(s.Results, Result{Name: "commands", Level: Fail,
			Detail: "missing " + strings.Join(missing, ", "), Fix: strings.Join(fixes, "; then ")})
	default:
		s.Results = append(s.Results, Result{Name: "commands", Level: OK,
			Detail: fmt.Sprintf("all %d present", len(c.Requires))})
	}

	var actions []component.Action
	if written != nil {
		var err error
		if actions, err = c.Plan(l.Expand, written); err != nil {
			s.Results = append(s.Results, Result{Name: "files", Level: Fail, Detail: err.Error()})
		}
	}
	for _, a := range actions {
		r := Result{Name: l.Pretty(a.Dest), Detail: a.Status.String()}
		switch a.Status {
		case component.Same:
			r.Level = OK
		case component.Missing, component.Outdated:
			r.Level, r.Fix = Warn, "ds update"
		case component.Edited:
			r.Level, r.Fix = Warn, "ds diff to see changes; ds update replaces it (keeping a backup)"
		}
		s.Results = append(s.Results, r)
	}

	for _, chk := range c.Checks {
		s.Results = append(s.Results, runCheck(ctx, chk, env))
	}
	return s
}

// checkTimeout bounds each component check, so one stuck command (a hung
// user bus, say) can't stall doctor or ds status.
var checkTimeout = 10 * time.Second

func runCheck(ctx context.Context, chk component.Check, env []string) Result {
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	switch {
	case sysexec.ShellEnv(cctx, env, chk.Run):
		return Result{Name: chk.Name, Level: OK}
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		return Result{Name: chk.Name, Level: Warn, Detail: "no answer within " + checkTimeout.String(), Fix: chk.Fix}
	default:
		return Result{Name: chk.Name, Level: Warn, Detail: "not yet", Fix: chk.Fix}
	}
}

func systemSection(ctx context.Context, l layout.Layout) Section {
	s := Section{Title: "System"}
	for _, path := range []string{"/", l.Home} {
		used, err := diskUsed(ctx, path)
		if err != nil {
			continue
		}
		r := Result{Name: "disk " + l.Pretty(path), Level: OK, Detail: fmt.Sprintf("%d%% used", used)}
		switch {
		case used >= 95:
			r.Level, r.Fix = Fail, "free space now: docker system df, ncdu "+path
		case used >= 85:
			r.Level, r.Fix = Warn, "check what's big: docker system df, ncdu "+path
		}
		s.Results = append(s.Results, r)
	}
	if data, err := os.ReadFile("/proc/swaps"); err == nil {
		if strings.Count(strings.TrimSpace(string(data)), "\n") == 0 {
			s.Results = append(s.Results, Result{Name: "swap", Level: Warn, Detail: "none", Fix: "see provision steps: 16 GB /swapfile"})
		} else {
			s.Results = append(s.Results, Result{Name: "swap", Level: OK, Detail: "on"})
		}
	}
	if data, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches"); err == nil {
		n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if n < 524288 {
			s.Results = append(s.Results, Result{Name: "file watchers", Level: Warn,
				Detail: fmt.Sprintf("limit %d is low for dev servers", n), Fix: "see provision steps: /etc/sysctl.d/90-devbox.conf"})
		} else {
			s.Results = append(s.Results, Result{Name: "file watchers", Level: OK, Detail: fmt.Sprintf("limit %d", n)})
		}
	}
	if runtime.GOOS == "linux" {
		s.Results = append(s.Results, memoryGuardResult(ctx))
	}
	if _, err := os.Stat("/var/run/reboot-required"); err == nil {
		s.Results = append(s.Results, Result{Name: "reboot", Level: Warn,
			Detail: "updates need a restart", Fix: "sudo reboot (dev servers stop; files are safe)"})
	}
	return s
}

func servicesSection(ctx context.Context, l layout.Layout) Section {
	s := Section{Title: "Services and logins"}
	quick, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if v, err := sysexec.Output(quick, "", "docker", "info", "--format", "{{.ServerVersion}}"); err == nil {
		s.Results = append(s.Results, Result{Name: "docker", Level: OK, Detail: "running " + v})
	} else {
		s.Results = append(s.Results, Result{Name: "docker", Level: Fail, Detail: "not reachable",
			Fix: "sudo systemctl start docker; make sure you're in the docker group (id)"})
	}

	// User systemd timers, Caddy and DEVBOX_HOST only apply on the devbox,
	// not on a Mac.
	if runtime.GOOS != "linux" {
		s.Results = append(s.Results, tailscaleResult(quick, ""))
	} else {
		ds := devproj.LoadSettings(l)
		s.Results = append(s.Results, tailscaleResult(quick, ds.Host), updateChecksResult(quick, l))
		if ds.TLS() {
			s.Results = append(s.Results, httpsResult(l, ds.CAFile()))
		}
	}

	s.Results = append(s.Results, phonePushResult(l))

	forge := devproj.LoadSettings(l).Forge
	mirrors := false
	if st, _, err := publish.LoadStatus(l.MirrorsFile()); err == nil {
		mirrors = len(st.For(forge).Mirrors) > 0
	}
	for _, lg := range logins(forge, mirrors) {
		s.Results = append(s.Results, judgeLogin(lg, sysexec.Shell(quick, lg.cmd+" >/dev/null 2>&1")))
	}
	return s
}

// login is a forge CLI whose sign-in ds publish uses.
type login struct {
	name, cmd, fix string
	// optional is why the login may be missing; "" means it's needed.
	optional string
}

// logins are the sign-ins the forge (DEVBOX_FORGE) needs: gh for github,
// plus glab while a GitLab copy exists; glab for gitlab, plus gh while a
// GitHub mirror exists (mirrors says whether one does). Without one, the
// other login is only needed to add one.
func logins(forge string, mirrors bool) []login {
	gh := login{name: "github (gh)", cmd: "gh auth status", fix: "gh auth login"}
	glab := login{name: "gitlab (glab)", cmd: "glab auth status", fix: "glab auth login"}
	if forge != config.ForgeGitLab {
		if !mirrors {
			glab.optional = "only needed for GitLab copies (ds publish --gitlab)"
		}
		return []login{gh, glab}
	}
	if !mirrors {
		gh.optional = "only needed for GitHub mirrors (ds publish --github)"
	}
	return []login{glab, gh}
}

// judgeLogin warns about a needed login that's signed out; an optional one
// signed out is fine.
func judgeLogin(lg login, signedIn bool) Result {
	switch {
	case signedIn:
		return Result{Name: lg.name, Level: OK, Detail: "signed in"}
	case lg.optional != "":
		return Result{Name: lg.name, Level: OK, Detail: "signed out; " + lg.optional}
	}
	return Result{Name: lg.name, Level: Warn, Detail: "signed out", Fix: lg.fix}
}

// phonePushResult says whether Claude Code's phone pushes are on. They're
// opt-in, so off is fine, not a warning.
func phonePushResult(l layout.Layout) Result {
	r := Result{Name: "phone notifications", Level: OK}
	switch _, ok, err := notify.LoadTopic(l.NotifyTopicFile()); {
	case err != nil:
		r.Level, r.Detail, r.Fix = Warn, err.Error(), "ds notify off && ds notify setup"
	case ok:
		r.Detail = "on (ntfy); ds notify setup shows how to subscribe"
	default:
		r.Detail = "off; ds notify setup turns them on"
	}
	return r
}

const updateUnit = "devbox-check-updates"

// updateChecksResult checks the daily update check: its timer is enabled, its
// last run worked, and it ran recently.
func updateChecksResult(ctx context.Context, l layout.Layout) Result {
	timer, err := sysexec.UserSystemctl(ctx, "show", updateUnit+".timer", "--property=LoadState,UnitFileState,LastTriggerUSec")
	if err != nil {
		return Result{Name: "update checks", Level: Warn, Detail: "can't reach your user systemd",
			Fix: "sudo ~/.devbox/src/provision/provision.sh --only linger, then log in again"}
	}
	service, _ := sysexec.UserSystemctl(ctx, "show", updateUnit+".service", "--property=Result,ExecMainStartTimestamp")
	st, _, _ := update.LoadStatus(l.UpdatesFile()) // only its fetch error is used; a broken file is replaced by the next run
	return judgeUpdateChecks(properties(timer), properties(service), st, time.Now())
}

// judgeUpdateChecks judges the update check from its systemd units. When it
// last ran comes from systemd, not updates.json: ds status, update and
// rollback rewrite that file too, so it stays recent even if the timer
// stopped firing.
func judgeUpdateChecks(timer, service map[string]string, st update.Status, now time.Time) Result {
	// The timer's last trigger survives reboots (Persistent=true); the
	// service's start time also covers a run started by hand.
	lastRun := systemdTime(timer["LastTriggerUSec"])
	if t := systemdTime(service["ExecMainStartTimestamp"]); t.After(lastRun) {
		lastRun = t
	}
	r := Result{Name: "update checks", Level: Warn}
	switch {
	case timer["LoadState"] != "loaded":
		r.Detail, r.Fix = "the daily timer isn't installed", "ds update"
	case timer["UnitFileState"] != "enabled":
		r.Detail, r.Fix = "the daily timer is off", "systemctl --user enable --now "+updateUnit+".timer"
	case service["Result"] != "" && service["Result"] != "success":
		r.Detail, r.Fix = "the last check failed ("+service["Result"]+")", "journalctl --user -u "+updateUnit+" -n 20"
	case lastRun.IsZero():
		r.Detail, r.Fix = "hasn't run yet", "systemctl --user start "+updateUnit+".service"
	case st.FetchError != "":
		r.Detail, r.Fix = "can't reach the repo: "+st.FetchError, "cd ~/.devbox/src && git fetch"
	case now.Sub(lastRun) > 48*time.Hour:
		r.Detail, r.Fix = "last ran "+ago(now.Sub(lastRun)), "journalctl --user -u "+updateUnit+" -n 20"
	default:
		r.Level, r.Detail = OK, "daily, last ran "+ago(now.Sub(lastRun))
	}
	return r
}

// systemdTime parses a timestamp from systemctl show, such as
// "Wed 2026-11-18 03:12:00 UTC" (in the local time zone) or "@1763435520".
// Never ("n/a", empty) or anything unreadable is the zero time.
func systemdTime(v string) time.Time {
	if secs, ok := strings.CutPrefix(v, "@"); ok {
		if n, err := strconv.ParseInt(secs, 10, 64); err == nil {
			return time.Unix(n, 0)
		}
		return time.Time{}
	}
	for _, format := range []string{"Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05 -0700"} {
		if t, err := time.ParseInLocation(format, v, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

// properties parses systemctl show output (Key=Value lines).
func properties(out string) map[string]string {
	props := map[string]string{}
	for line := range strings.Lines(out) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	return props
}

func ago(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

// tailscaleResult checks Tailscale is up. host is the devbox's tailnet name
// (DEVBOX_HOST); empty means this isn't the devbox, so fixes don't rename it.
func tailscaleResult(ctx context.Context, host string) Result {
	out, err := sysexec.Output(ctx, "", "tailscale", "status", "--json")
	return judgeTailscale(out, err, host, time.Now())
}

func judgeTailscale(out string, statusErr error, host string, now time.Time) Result {
	up, machine := "tailscale up", "this machine"
	if host != "" {
		up, machine = "sudo tailscale up --ssh --hostname="+host, host
	}
	if statusErr != nil {
		return Result{Name: "tailscale", Level: Fail, Detail: "not running", Fix: up}
	}
	var st struct {
		BackendState string
		Self         struct {
			KeyExpiry *time.Time
		}
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return Result{Name: "tailscale", Level: Warn, Detail: "couldn't read status"}
	}
	if st.BackendState != "Running" {
		return Result{Name: "tailscale", Level: Fail, Detail: strings.ToLower(st.BackendState), Fix: up}
	}
	if st.Self.KeyExpiry != nil && !st.Self.KeyExpiry.IsZero() {
		left := st.Self.KeyExpiry.Sub(now)
		level := Warn
		if left < 7*24*time.Hour {
			level = Fail
		}
		return Result{Name: "tailscale", Level: level,
			Detail: fmt.Sprintf("connected, but its key expires in %d days", int(left.Hours()/24)),
			Fix:    "Tailscale admin console → Machines → " + machine + " → Disable key expiry"}
	}
	return Result{Name: "tailscale", Level: OK, Detail: "connected, key never expires"}
}

// diskUsed returns the percentage of the filesystem holding path that's used.
func diskUsed(ctx context.Context, path string) (int, error) {
	out, err := sysexec.Output(ctx, "", "df", "-P", path)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("unexpected df output: %q", out)
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 5 {
		return 0, fmt.Errorf("unexpected df output: %q", out)
	}
	return strconv.Atoi(strings.TrimSuffix(fields[4], "%"))
}
