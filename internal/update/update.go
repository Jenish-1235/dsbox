// Package update moves the machine to a version of dsbox: it pulls the
// source, rebuilds ds and dev, and applies every component. It also rolls back.
package update

import (
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/Jenish-1235/dsbox/internal/backup"
	"github.com/Jenish-1235/dsbox/internal/component"
	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/state"
	"github.com/Jenish-1235/dsbox/internal/sysexec"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

// ErrDirty means someone changed files inside ~/.devbox/src by hand.
var ErrDirty = errors.New("local changes in the devbox source")

// Updater runs updates and rollbacks.
type Updater struct {
	Layout layout.Layout
	Out    ui.Printer
	// Build compiles the command in src/cmd/<name> (ds, dev, …) into dst.
	// Tests replace it.
	Build func(ctx context.Context, src, name, dst string) error
	// Reapply applies components with the ds binary now in Bin. Tests replace it.
	Reapply func(ctx context.Context) error
	// Revision returns the commit the binary at path was built from, or ""
	// when it can't tell. Nil means unknown. Tests replace it.
	Revision func(path string) string
}

// New returns an Updater that builds with Go and reapplies by running the new ds.
func New(l layout.Layout, out ui.Printer) Updater {
	return Updater{
		Layout: l,
		Out:    out,
		Build:  buildCommand,
		Reapply: func(ctx context.Context) error {
			return sysexec.Run(ctx, "", os.Stdout, os.Stderr, filepath.Join(l.Bin(), "ds"), "apply")
		},
		Revision: binaryRevision,
	}
}

// binaryRevision reads the commit Go stamped into the binary at path when it
// was built from the source checkout.
func binaryRevision(path string) string {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}

// Update fetches, moves to ref (a branch, tag or commit; empty means the
// channel default), rebuilds ds if the version changed, and applies components.
func (u Updater) Update(ctx context.Context, ref string) error {
	src := u.Layout.Src()
	if err := u.checkClean(ctx); err != nil {
		return err
	}
	u.Out.Title("Checking for updates")
	fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, err := git(fetchCtx, src, "fetch", "--quiet", "--tags", "--prune", "origin"); err != nil {
		return fmt.Errorf("fetch updates: %w", err)
	}
	target, label, err := Resolve(ctx, src, ref)
	if err != nil {
		return err
	}
	st, err := state.Load(u.Layout.StateFile())
	if err != nil {
		return err
	}
	head, err := git(ctx, src, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	from := u.builtFrom(ctx, st, head, target)
	if from == target {
		u.Out.OK("Already on the latest version (%s, %s)", label, short(head))
	} else {
		if head == target {
			u.Out.Text("ds was built from %s, not the checked-out %s; rebuilding", short(from), short(head))
		}
		changes, err := git(ctx, src, "log", "--no-decorate", "--format=%s", from+".."+target)
		if err != nil {
			return err
		}
		if changes == "" {
			u.Out.Text("Switching to %s (%s)", label, short(target))
		} else {
			u.Out.Text("New in %s:", label)
			for line := range strings.Lines(changes) {
				u.Out.Item("%s", strings.TrimSpace(line))
			}
		}
		if err := u.moveTo(ctx, st, from, target); err != nil {
			return err
		}
		u.Out.OK("Updated %s → %s", short(from), short(target))
	}
	u.refreshStatus(ctx, true)
	return u.Reapply(ctx)
}

// Rollback returns to the version before the last update or rollback.
func (u Updater) Rollback(ctx context.Context) error {
	st, err := state.Load(u.Layout.StateFile())
	if err != nil {
		return err
	}
	if st.Previous == "" {
		return errors.New("nothing to roll back to yet")
	}
	if err := u.checkClean(ctx); err != nil {
		return err
	}
	head, err := git(ctx, u.Layout.Src(), "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	from := u.builtFrom(ctx, st, head, st.Previous)
	u.Out.Title("Rolling back %s → %s", short(from), short(st.Previous))
	if err := u.moveTo(ctx, st, from, st.Previous); err != nil {
		return err
	}
	u.refreshStatus(ctx, false)
	return u.Reapply(ctx)
}

// refreshStatus updates the cached update status after moving, so shell
// notices don't announce an update that's already applied. It doesn't fetch;
// fetched says the caller just fetched successfully, which clears an earlier
// fetch error (a rollback doesn't fetch, so one still stands). On failure the
// cache is left alone; the next daily check replaces it.
func (u Updater) refreshStatus(ctx context.Context, fetched bool) {
	s, err := CheckForUpdates(ctx, u.Layout, 0)
	if err != nil || !fetched {
		return
	}
	s.FetchError, s.FetchedAt = "", s.CheckedAt
	_ = s.Save(u.Layout.UpdatesFile())
}

// builtFrom returns the commit the installed binaries were built from: the one
// stamped into bin/ds when it can be read (install.sh rebuilds from whatever is
// checked out without recording it). Otherwise it is head, unless an
// interrupted update left HEAD on another commit than the one state records as
// applied; then it is the recorded one.
func (u Updater) builtFrom(ctx context.Context, st state.State, head, target string) string {
	if u.Revision != nil {
		if rev := u.Revision(filepath.Join(u.Layout.Bin(), "ds")); rev != "" && u.isCommit(ctx, rev) {
			return rev
		}
	}
	c := st.Current
	if c == "" || c == head || c == target || !u.isCommit(ctx, c) {
		return head
	}
	return c
}

func (u Updater) isCommit(ctx context.Context, rev string) bool {
	_, err := git(ctx, u.Layout.Src(), "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	return err == nil
}

// revertTimeout bounds the checkout that undoes a failed move.
const revertTimeout = 30 * time.Second

// moveTo checks out to, installs every command (ds, dev, …) built from it
// (reusing the kept previous binaries when going back to it), and records the
// move in st. from is the commit the installed binaries were built from; if
// installing fails, the source goes back to it, so the source and binaries
// stay at the same version.
func (u Updater) moveTo(ctx context.Context, st state.State, from, to string) error {
	src := u.Layout.Src()
	if _, err := git(ctx, src, "checkout", "--quiet", "--detach", to); err != nil {
		// An interrupted checkout can leave the tree half switched.
		return u.revert(ctx, from, fmt.Errorf("check out %s: %w", short(to), err))
	}
	cmds := Commands(src)
	if to == st.Previous && exists(u.Layout.PreviousBinary()) {
		// Going back: swap the kept binaries in, keep the current ones for
		// roll-forward. A command with no kept binary is built.
		var swapped []string
		unswap := func(err error) error {
			for _, done := range slices.Backward(swapped) {
				_ = swap(filepath.Join(u.Layout.Bin(), done), u.Layout.PreviousCommand(done))
			}
			return u.revert(ctx, from, err)
		}
		for _, name := range cmds {
			bin, prev := filepath.Join(u.Layout.Bin(), name), u.Layout.PreviousCommand(name)
			if exists(bin) && exists(prev) {
				if err := swap(bin, prev); err != nil {
					return unswap(err)
				}
				swapped = append(swapped, name)
				continue
			}
			if err := u.Build(ctx, src, name, bin); err != nil {
				if ctx.Err() != nil {
					// Interrupted: go back rather than record a half-done move.
					return unswap(fmt.Errorf("build %s at %s: %w", name, short(to), err))
				}
				u.Out.Warn("couldn't build %s at %s: %v", name, short(to), err)
			}
		}
	} else if err := u.install(ctx, src, cmds); err != nil {
		return u.revert(ctx, from, fmt.Errorf("build at %s: %w", short(to), err))
	}
	st.Previous, st.Current, st.UpdatedAt = from, to, time.Now().UTC()
	return st.Save(u.Layout.StateFile())
}

// revert checks out from again after a failed move and returns err. It runs
// even when ctx is cancelled (Ctrl-C during the build), because leaving HEAD
// on the new commit with the old binaries would skew the two.
func (u Updater) revert(ctx context.Context, from string, err error) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revertTimeout)
	defer cancel()
	if _, gerr := git(rctx, u.Layout.Src(), "checkout", "--quiet", "--detach", from); gerr != nil {
		return errors.Join(err, fmt.Errorf("go back to %s: %w", short(from), gerr))
	}
	return fmt.Errorf("%w (stayed on %s)", err, short(from))
}

// install builds every command, and only when all of them build, keeps the
// current binaries as the previous ones and moves the new ones in. On an
// error the installed binaries are left as they were.
func (u Updater) install(ctx context.Context, src string, cmds []string) error {
	bin := u.Layout.Bin()
	defer func() {
		for _, name := range cmds {
			_ = os.Remove(filepath.Join(bin, name+".new"))
		}
	}()
	for _, name := range cmds {
		if err := u.Build(ctx, src, name, filepath.Join(bin, name+".new")); err != nil {
			return fmt.Errorf("build %s: %w", name, err)
		}
	}
	var undo []func()
	fail := func(err error) error {
		for _, f := range slices.Backward(undo) {
			f()
		}
		return err
	}
	for _, name := range cmds {
		cur, prev := filepath.Join(bin, name), u.Layout.PreviousCommand(name)
		if exists(cur) {
			if err := os.Rename(cur, prev); err != nil {
				return fail(fmt.Errorf("keep previous %s: %w", name, err))
			}
			undo = append(undo, func() { _ = os.Rename(prev, cur) })
		} else {
			undo = append(undo, func() { _ = os.Remove(cur) })
		}
		if err := os.Rename(cur+".new", cur); err != nil {
			return fail(fmt.Errorf("install new %s: %w", name, err))
		}
	}
	return nil
}

// Commands lists the commands in src/cmd, ds first. A source without a cmd
// folder still has ds.
func Commands(src string) []string {
	cmds := []string{"ds"}
	entries, _ := os.ReadDir(filepath.Join(src, "cmd"))
	for _, e := range entries {
		if e.IsDir() && e.Name() != "ds" && !strings.HasPrefix(e.Name(), ".") {
			cmds = append(cmds, e.Name())
		}
	}
	return cmds
}

// Apply syncs every component's files, backing up anything edited by hand,
// then runs the after_apply commands of components whose files changed.
func Apply(ctx context.Context, l layout.Layout, out ui.Printer) error {
	if err := l.Ensure(); err != nil {
		return err
	}
	comps, err := component.LoadAll(l.Components())
	if err != nil {
		return err
	}
	st, err := state.Load(l.StateFile())
	if err != nil {
		return err
	}
	store := backup.New(l.Backups(), time.Now())
	changed, applied := 0, 0
	var pending []string
	out.Title("Applying components")
	for i, c := range comps {
		if !c.Supports(runtime.GOOS) {
			continue
		}
		applied++
		wasPending := slices.Contains(st.PendingAfterApply, c.Name)
		written, err := applyFiles(c, l, st.Files, store, out)
		changed += written
		needsHook := len(c.AfterApply) > 0 && (written > 0 || wasPending)
		if err != nil {
			// Keep every hook that still has to run, so the next apply runs
			// it: this component's if it changed files, and those not reached.
			if needsHook {
				pending = append(pending, c.Name)
			}
			for _, rest := range comps[i+1:] {
				if slices.Contains(st.PendingAfterApply, rest.Name) {
					pending = append(pending, rest.Name)
				}
			}
			st.PendingAfterApply = pending
			if serr := st.Save(l.StateFile()); serr != nil {
				return errors.Join(err, serr)
			}
			return err
		}
		if !needsHook {
			continue
		}
		if !runAfterApply(ctx, c, out) {
			pending = append(pending, c.Name)
		}
		changed++
	}
	// Only components still in the repo can be pending.
	st.PendingAfterApply = pending
	if err := st.Save(l.StateFile()); err != nil {
		return err
	}
	if changed == 0 {
		out.OK("All %d components up to date", applied)
	}
	return nil
}

// applyFiles syncs c's files, recording what it wrote in files, and returns
// how many it wrote, also when it stops at an error.
func applyFiles(c component.Component, l layout.Layout, files map[string]string, store backup.Store, out ui.Printer) (int, error) {
	actions, err := c.Plan(l.Expand, files)
	if err != nil {
		return 0, fmt.Errorf("plan %s: %w", c.Name, err)
	}
	written := 0
	for _, a := range actions {
		if a.Status == component.Same {
			files[a.Dest] = a.Want
			continue
		}
		if a.Status == component.Edited {
			rel, err := filepath.Rel(l.Home, a.Dest)
			if err != nil || strings.HasPrefix(rel, "..") {
				rel = strings.TrimPrefix(a.Dest, string(filepath.Separator))
			}
			saved, err := store.Save(a.Dest, rel)
			if err != nil {
				return written, err
			}
			out.Warn("%s: %s was edited by hand, saved a copy to %s", c.Name, l.Pretty(a.Dest), l.Pretty(saved))
			out.Hint("put personal tweaks in the .local file next to it so updates never touch them")
		}
		if err := a.Write(); err != nil {
			return written, err
		}
		files[a.Dest] = a.Want
		written++
		out.OK("%s: %s %s", c.Name, verb(a.Status), l.Pretty(a.Dest))
	}
	return written, nil
}

// runAfterApply runs c's after_apply commands and reports how it went. A
// failure is a warning, not an error: the files are in place, and the next
// apply retries.
func runAfterApply(ctx context.Context, c component.Component, out ui.Printer) bool {
	ran, err := c.RunAfterApply(ctx, sysexec.UserBusEnv())
	for _, cmd := range ran {
		out.OK("%s: ran %s", c.Name, cmd)
	}
	if err == nil {
		return true
	}
	out.Warn("%s: couldn't finish setting up: %v", c.Name, err)
	if runtime.GOOS == "linux" && !sysexec.HasUserBus() {
		out.Hint("your user systemd isn't reachable from this session: sudo ~/.devbox/src/provision/provision.sh --only linger, then ds doctor --fix")
	} else {
		out.Hint("ds doctor --fix tries again")
	}
	return false
}

// Resolve turns ref into a commit. An empty ref follows DEVBOX_CHANNEL:
// "release" means the newest v* tag, anything else (default) means main.
func Resolve(ctx context.Context, src, ref string) (commit, label string, err error) {
	if ref == "" {
		if os.Getenv("DEVBOX_CHANNEL") == "release" {
			tags, err := git(ctx, src, "tag", "--list", "v*", "--sort=-v:refname")
			if err != nil {
				return "", "", err
			}
			first, _, _ := strings.Cut(tags, "\n")
			if first == "" {
				return "", "", errors.New("no release tags yet; tag one or set DEVBOX_CHANNEL=main")
			}
			ref = first
		} else {
			ref = "main"
		}
	}
	for _, candidate := range []string{"origin/" + ref, ref} {
		commit, err := git(ctx, src, "rev-parse", "--verify", "--quiet", candidate+"^{commit}")
		if err == nil && commit != "" {
			return commit, ref, nil
		}
	}
	return "", "", fmt.Errorf("unknown version %q: not a branch, tag or commit", ref)
}

func (u Updater) checkClean(ctx context.Context) error {
	src := u.Layout.Src()
	if !exists(filepath.Join(src, ".git")) {
		return fmt.Errorf("no devbox source at %s; run install.sh first", src)
	}
	status, err := git(ctx, src, "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("%w at %s; commit or discard them first:\n%s", ErrDirty, src, status)
	}
	return nil
}

func buildCommand(ctx context.Context, src, name, dst string) error {
	goName, args := goCommand()
	args = append(args, "build", "-trimpath", "-o", dst, "./cmd/"+name)
	cmd := exec.CommandContext(ctx, goName, args...)
	cmd.Dir = src
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build %s: %w", name, err)
	}
	return nil
}

// goCommand finds Go: on PATH, or through mise when PATH lacks mise's shims.
func goCommand() (string, []string) {
	if sysexec.Exists("go") {
		return "go", nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		if mise := filepath.Join(home, ".local", "bin", "mise"); exists(mise) {
			return mise, []string{"exec", "--", "go"}
		}
	}
	return "go", nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return sysexec.Output(ctx, dir, "git", args...)
}

func swap(a, b string) error {
	tmp := a + ".swap"
	if err := os.Rename(a, tmp); err != nil {
		return fmt.Errorf("swap binaries: %w", err)
	}
	if err := os.Rename(b, a); err != nil {
		return fmt.Errorf("swap binaries: %w", err)
	}
	if err := os.Rename(tmp, b); err != nil {
		return fmt.Errorf("swap binaries: %w", err)
	}
	return nil
}

func verb(s component.Status) string {
	switch s {
	case component.Missing:
		return "installed"
	case component.Outdated:
		return "updated"
	case component.Edited:
		return "replaced"
	}
	return "kept"
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
