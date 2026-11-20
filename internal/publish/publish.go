package publish

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Jenish-1235/dsbox/internal/config"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

// Project is the folder to publish; Name becomes the repo name when there's
// no origin yet.
type Project struct {
	Name string
	Dir  string
}

// MirrorChoice says whether to keep a copy of the project on the other
// forge: a GitLab project's mirror on GitHub (kept by GitLab), or a GitHub
// project's copy on GitLab (kept by git push).
type MirrorChoice int

const (
	// AskMirror asks when Publisher.Ask is set, and otherwise doesn't copy.
	AskMirror MirrorChoice = iota
	// WithMirror copies without asking (--github, --gitlab).
	WithMirror
	// WithoutMirror never copies (--no-github, --no-gitlab).
	WithoutMirror
)

// Flags are ds publish's flags: Choice, and To, the forge the flag names
// (config.ForgeGitHub for --github and --no-github, config.ForgeGitLab for
// --gitlab and --no-gitlab; "" without a flag).
type Flags struct {
	Choice MirrorChoice
	To     string
}

// Publisher puts projects on GitHub or GitLab, with an optional copy on the
// other one. The zero value of every field but Run and Out is usable.
type Publisher struct {
	Run Runner
	Out ui.Printer
	// Forge is where a project with no origin goes: config.ForgeGitHub
	// ("" too) or config.ForgeGitLab.
	Forge string
	// Host is the GitLab host; "" means DefaultHost.
	Host string
	// Ask asks whether to keep a copy of the project name on the forge to
	// ("GitHub" or "GitLab"). nil means there's no one to ask.
	Ask func(ctx context.Context, name, to string) (bool, error)
	// StatusFile, when set, gets the mirror's result (see Status).
	StatusFile string
	// SyncWait is how long to wait for the first sync (90s); PollEvery how
	// often to look (3s).
	SyncWait  time.Duration
	PollEvery time.Duration
}

// mirrorDescription is the GitHub repo description; reruns recognise their
// own repo by it.
func mirrorDescription(gitlabURL string) string {
	return "Mirror of " + gitlabURL + "; push to GitLab, not here"
}

// Publish pushes p to its origin on GitLab or GitHub. Without an origin it
// creates a private repo on the configured forge first. A project on the
// configured forge can also get a copy on the other one, if chosen: GitLab
// mirrors to GitHub, and a GitHub project's git push also goes to GitLab.
// Running it again reuses what exists.
func (pb Publisher) Publish(ctx context.Context, p Project, f Flags) error {
	forge := cmp.Or(pb.Forge, config.ForgeGitHub)
	switch {
	case forge != config.ForgeGitHub && forge != config.ForgeGitLab:
		pb.Out.Fail("%s is %q; it must be github or gitlab", config.Forge, forge)
		pb.Out.Hint("fix config/devbox.env in the repo, then %s", config.Fix)
		return ErrReported
	case f.To != "" && f.To == forge:
		pb.Out.Fail("--%s and --no-%s only apply when %s=%s", f.To, f.To, config.Forge, otherForge(f.To))
		pb.Out.Hint("here it's %s: ds publish pushes to %s itself; --%s adds a copy on %s",
			forge, forgeName(forge), otherForge(forge), forgeName(otherForge(forge)))
		return ErrReported
	}
	host := cmp.Or(pb.Host, DefaultHost)
	git := func(args ...string) (string, error) { return pb.Run.Output(ctx, p.Dir, nil, "git", args...) }

	if _, err := git("rev-parse", "--git-dir"); err != nil {
		pb.Out.Fail("%s isn't a git repository", p.Dir)
		pb.Out.Hint("git init -b main && git add -A && git commit -m 'First commit', then ds publish")
		return ErrReported
	}
	if _, err := git("rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		pb.Out.Fail("Nothing committed in %s yet", p.Name)
		pb.Out.Hint("git add -A && git commit -m 'First commit', then ds publish")
		return ErrReported
	}
	remotes, err := git("remote")
	if err != nil {
		return fmt.Errorf("list git remotes: %w", err)
	}
	raw := ""
	if hasLine(remotes, "origin") {
		if raw, err = git("remote", "get-url", "origin"); err != nil {
			return fmt.Errorf("read origin: %w", err)
		}
	}

	o := classifyOrigin(raw, host)
	if o.kind == otherOrigin {
		pb.Out.Fail("origin is %s, which is on neither %s nor %s", raw, host, gitHubHost)
		pb.Out.Hint("ds publish pushes to GitLab or GitHub; to move: git remote rename origin old-origin, then ds publish")
		return ErrReported
	}
	dest := config.ForgeGitLab
	if o.kind == gitHubOrigin || (o.kind == noOrigin && forge == config.ForgeGitHub) {
		dest = config.ForgeGitHub
	}
	choice := f.Choice
	switch {
	case f.To != "" && f.To == dest:
		verb := "mirrors" // --github: GitLab mirrors to GitHub
		if f.To == config.ForgeGitLab {
			verb = "copies"
		}
		pb.Out.Fail("origin is already on %s (%s); --%s %s a %s project", forgeName(dest), o.path, f.To, verb, forgeName(otherForge(dest)))
		pb.Out.Hint("ds publish pushes to it")
		return ErrReported
	case dest != forge:
		// A project on the other forge: push, never copy.
		choice = WithoutMirror
	}
	if dest == config.ForgeGitHub {
		return pb.publishGitHub(ctx, p, o, host, choice)
	}
	return pb.publishGitLab(ctx, p, o, host, choice)
}

// otherForge is gitlab for github and github for gitlab.
func otherForge(forge string) string {
	if forge == config.ForgeGitLab {
		return config.ForgeGitHub
	}
	return config.ForgeGitLab
}

// forgeName is how messages name a forge.
func forgeName(forge string) string {
	if forge == config.ForgeGitLab {
		return "GitLab"
	}
	return "GitHub"
}

// publishGitHub pushes to the GitHub repo origin points at or, with no
// origin, to <gh user>/<name>, created private when it doesn't exist. It then
// adds a copy on GitLab if choice (or the developer's answer) says so.
func (pb Publisher) publishGitHub(ctx context.Context, p Project, o origin, host string, choice MirrorChoice) error {
	hub := gitHub{run: pb.Run}
	user, err := hub.login(ctx)
	if err != nil {
		pb.Out.Fail("Can't use GitHub: %v", err)
		pb.Out.Hint("gh auth login, then ds publish")
		return ErrReported
	}
	full, empty := o.path, false
	if o.kind == noOrigin {
		full = user + "/" + p.Name
		if empty, err = pb.ensureRepo(ctx, hub, full); err != nil {
			return err
		}
		if _, err := pb.Run.Output(ctx, p.Dir, nil, "git", "remote", "add", "origin", hub.cloneURL(ctx, full)); err != nil {
			return fmt.Errorf("add origin: %w", err)
		}
	} else if _, err := hub.repo(ctx, full); errors.Is(err, ErrNotFound) {
		pb.Out.Fail("origin points at %s/%s, which doesn't exist or you can't see", gitHubHost, full)
		pb.Out.Hint("check gh auth status, or git remote remove origin and ds publish creates a new repo")
		return ErrReported
	} else if err != nil {
		return err
	}

	// A copy on GitLab is a second push URL on origin, so this push updates
	// it too.
	copyPath := pb.gitLabCopy(ctx, p.Dir, host)
	where := full
	if copyPath != "" {
		where += " and " + host + "/" + copyPath
	}
	branches, err := pb.push(ctx, p.Dir, where)
	if err != nil {
		return err
	}
	// An empty repo takes whichever branch arrives first as its default.
	if _, hasMain := branches["main"]; empty && hasMain {
		if now, err := hub.repo(ctx, full); err == nil && now.DefaultBranch != "main" {
			if err := hub.setDefaultBranch(ctx, full, "main"); err != nil {
				pb.Out.Warn("Couldn't make main the default branch: %v", err)
			}
		}
	}
	if copyPath != "" {
		pb.record(copyStatus(host, copyPath, full))
		pb.Out.Text("GitHub  https://%s/%s", gitHubHost, full)
		pb.Out.Text("GitLab  https://%s/%s  %s", host, copyPath, pb.Out.Dim("(copy; git push updates both)"))
		if choice == WithoutMirror {
			pb.Out.Hint("already copied to GitLab; git remote set-url --delete --push origin %s stops it", copyPath)
		}
		return nil
	}
	return pb.offerCopy(ctx, p, full, host, choice, branches)
}

// ensureRepo finds full on GitHub (left empty by an earlier run) or creates
// it, private. It reports whether the repo is empty, which it always is when
// it returns no error: a repo of that name with branches may be another
// project entirely, so it's left alone.
func (pb Publisher) ensureRepo(ctx context.Context, hub gitHub, full string) (bool, error) {
	_, err := hub.repo(ctx, full)
	switch {
	case errors.Is(err, ErrNotFound):
		if err := hub.createRepo(ctx, full); err != nil {
			return false, err
		}
		pb.Out.OK("Created GitHub repo %s (private)", full)
		return true, nil
	case err != nil:
		return false, err
	}
	// The repo's size lags pushes by up to an hour; its branches don't.
	have, err := hub.branches(ctx, full)
	if err != nil {
		return false, err
	}
	if len(have) > 0 {
		pb.Out.Fail("GitHub repo %s already exists and has commits", full)
		pb.Out.Hint("if it is this project: git remote add origin %s, then ds publish; if not, rename or delete it on GitHub, or set origin to a repo with another name",
			hub.cloneURL(ctx, full))
		return false, ErrReported
	}
	pb.Out.OK("GitHub repo %s exists and is empty, using it", full)
	return true, nil
}

// publishGitLab pushes to the GitLab project origin points at or, with no
// origin, to <glab user>/<name>, created private when it doesn't exist. It
// then mirrors the project to GitHub if choice (or the developer's answer) says so.
func (pb Publisher) publishGitLab(ctx context.Context, p Project, o origin, host string, choice MirrorChoice) error {
	gl := gitLab{run: pb.Run, host: host}
	var (
		proj    glProject
		created bool
		err     error
	)
	switch o.kind {
	case gitLabOrigin:
		proj, err = gl.project(ctx, o.path)
		if errors.Is(err, ErrNotFound) {
			pb.Out.Fail("origin points at %s/%s, which doesn't exist or you can't see", host, o.path)
			pb.Out.Hint("check glab auth status, or git remote remove origin and ds publish creates a new project")
			return ErrReported
		}
		if err != nil {
			return err
		}
	case noOrigin:
		if proj, created, err = pb.ensureProject(ctx, gl, p.Name); err != nil {
			return err
		}
		if _, err := pb.Run.Output(ctx, p.Dir, nil, "git", "remote", "add", "origin", pb.cloneURL(ctx, host, proj)); err != nil {
			return fmt.Errorf("add origin: %w", err)
		}
	}

	branches, err := pb.push(ctx, p.Dir, proj.PathWithNS)
	if err != nil {
		return err
	}
	// A new project takes whichever branch arrives first as its default.
	if _, hasMain := branches["main"]; created && hasMain {
		if now, err := gl.project(ctx, proj.PathWithNS); err == nil && now.DefaultBranch != "main" {
			if err := gl.setDefaultBranch(ctx, proj.ID, "main"); err != nil {
				pb.Out.Warn("Couldn't make main the default branch: %v", err)
			}
		}
	}

	mirror, asked, err := pb.decide(ctx, p.Name, "GitHub", choice)
	if err != nil {
		return err
	}
	if !mirror {
		pb.Out.Text("GitLab  %s", proj.WebURL)
		if !asked && choice == AskMirror {
			pb.Out.Hint("not mirrored to GitHub; ds publish --github adds the mirror")
		}
		return nil
	}
	ghURL, err := pb.mirror(ctx, gl, proj, branches)
	pb.Out.Text("GitLab  %s", proj.WebURL)
	if ghURL != "" {
		pb.Out.Text("GitHub  %s  %s", ghURL, pb.Out.Dim("(read-only mirror)"))
	}
	return err
}

// push pushes every branch and tag in dir to origin, which is where (for
// messages), and returns the branches pushed (name → commit).
func (pb Publisher) push(ctx context.Context, dir, where string) (map[string]string, error) {
	git := func(args ...string) (string, error) { return pb.Run.Output(ctx, dir, nil, "git", args...) }
	heads, err := git("for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads")
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}
	branches := parseHeads(heads)
	tags, err := git("for-each-ref", "--format=%(refname:short)", "refs/tags")
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	if _, err := git("push", "--quiet", "-u", "origin", "--all"); err != nil {
		return nil, fmt.Errorf("push branches to %s: %w", where, err)
	}
	if _, err := git("push", "--quiet", "origin", "--tags"); err != nil {
		return nil, fmt.Errorf("push tags to %s: %w", where, err)
	}
	pb.Out.OK("Pushed %s, %s to %s", count(len(branches), "branch", "branches"), count(len(lines(tags)), "tag", "tags"), where)
	return branches, nil
}

// ensureProject finds name in the developer's namespace (left empty by an earlier run)
// or creates it, private. A project of that name with commits may be another
// project entirely, so it's left alone.
func (pb Publisher) ensureProject(ctx context.Context, gl gitLab, name string) (glProject, bool, error) {
	user, err := gl.user(ctx)
	if err != nil {
		return glProject{}, false, err
	}
	proj, err := gl.project(ctx, user+"/"+name)
	if err == nil {
		if !proj.Empty {
			pb.Out.Fail("GitLab project %s already exists and has commits", proj.PathWithNS)
			pb.Out.Hint("if it is this project: git remote add origin %s, then ds publish; if not, rename or delete it on GitLab, or set origin to a project with another name",
				pb.cloneURL(ctx, gl.host, proj))
			return glProject{}, false, ErrReported
		}
		pb.Out.OK("GitLab project %s exists and is empty, using it", proj.PathWithNS)
		return proj, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return glProject{}, false, err
	}
	if proj, err = gl.createProject(ctx, name, ""); err != nil {
		return glProject{}, false, err
	}
	pb.Out.OK("Created GitLab project %s (private)", proj.PathWithNS)
	return proj, true, nil
}

// cloneURL is proj's URL in the protocol glab is set to use.
func (pb Publisher) cloneURL(ctx context.Context, host string, proj glProject) string {
	if proto, _ := pb.Run.Output(ctx, "", nil, "glab", "config", "get", "git_protocol", "--host", host); proto == "ssh" {
		return proj.SSHURL
	}
	return proj.HTTPURL
}

// decide applies the flag, or asks about a copy on to ("GitHub" or
// "GitLab"). asked is true when the developer was asked.
func (pb Publisher) decide(ctx context.Context, name, to string, choice MirrorChoice) (mirror, asked bool, err error) {
	switch {
	case choice == WithMirror:
		return true, false, nil
	case choice == WithoutMirror || pb.Ask == nil:
		return false, false, nil
	}
	yes, err := pb.Ask(ctx, name, to)
	if err != nil {
		return false, true, err
	}
	return yes, true, nil
}

// mirror makes sure GitHub has a private repo and GitLab pushes to it, then
// syncs and waits for the result. It returns the GitHub URL once the repo
// exists.
func (pb Publisher) mirror(ctx context.Context, gl gitLab, proj glProject, branches map[string]string) (string, error) {
	hub := gitHub{run: pb.Run}
	user, err := hub.login(ctx)
	if err != nil {
		pb.Out.Fail("Can't use GitHub: %v", err)
		pb.Out.Hint("gh auth login, then ds publish --github")
		return "", ErrReported
	}
	full := user + "/" + proj.Path
	ghURL := "https://github.com/" + full
	desc := mirrorDescription(proj.WebURL)
	refuse := func() (string, error) {
		pb.Out.Fail("github.com/%s already exists and isn't a mirror of %s", full, proj.PathWithNS)
		pb.Out.Hint("the mirror would overwrite it; rename or delete it on GitHub, then ds publish --github")
		return "", ErrReported
	}
	repo, err := hub.repo(ctx, full)
	switch {
	case errors.Is(err, ErrNotFound):
		if err := hub.createMirrorRepo(ctx, full, desc); err != nil {
			return "", err
		}
		pb.Out.OK("Created GitHub repo %s (private)", full)
	case err != nil:
		return "", err
	case strings.HasPrefix(repo.Description, "Mirror of "+proj.WebURL):
		pb.Out.OK("GitHub repo %s exists, using it", full)
	case repo.Description != "":
		return refuse()
	default:
		// The repo's size lags pushes by up to an hour; its branches don't.
		have, err := hub.branches(ctx, full)
		if err != nil {
			return "", err
		}
		if len(have) > 0 {
			return refuse()
		}
		if err := hub.setDescription(ctx, full, desc); err != nil {
			return "", err
		}
		pb.Out.OK("GitHub repo %s exists and is empty, using it", full)
	}

	token, err := hub.token(ctx)
	if err != nil {
		pb.Out.Fail("Can't read your gh token: %v", err)
		pb.Out.Hint("gh auth login, then ds publish --github")
		return ghURL, ErrReported
	}
	m, fresh, err := pb.ensureMirror(ctx, gl, proj, user, token, full)
	if err != nil {
		return ghURL, redact(err, token)
	}
	return ghURL, redact(pb.sync(ctx, gl, hub, proj, m, fresh, full, branches), token)
}

// ensureMirror reuses a healthy mirror to full, replaces a failed one (its
// token may be stale), or adds one. It reports true for a new mirror, whose
// token hasn't been tried yet.
func (pb Publisher) ensureMirror(ctx context.Context, gl gitLab, proj glProject, user, token, full string) (remoteMirror, bool, error) {
	existing, err := gl.mirrors(ctx, proj.ID)
	if err != nil {
		return remoteMirror{}, false, err
	}
	for _, m := range existing {
		if t, ok := githubTarget(m.URL); !ok || !strings.EqualFold(t, full) {
			continue
		}
		if !m.failing() {
			updated, err := gl.updateMirror(ctx, proj.ID, m.ID)
			if err != nil {
				return remoteMirror{}, false, err
			}
			pb.Out.OK("GitLab already mirrors to GitHub, using it")
			return updated, false, nil
		}
		if err := gl.deleteMirror(ctx, proj.ID, m.ID); err != nil {
			return remoteMirror{}, false, err
		}
		created, err := gl.createMirror(ctx, proj.ID, mirrorURL(user, token, full))
		if err != nil {
			return remoteMirror{}, false, err
		}
		pb.Out.OK("Replaced the failing mirror, now with your current gh token")
		return created, true, nil
	}
	created, err := gl.createMirror(ctx, proj.ID, mirrorURL(user, token, full))
	if err != nil {
		return remoteMirror{}, false, err
	}
	pb.Out.OK("GitLab now mirrors every branch to GitHub")
	return created, true, nil
}

// sync asks GitLab to push to GitHub now and waits until GitHub's branches
// match branches (name → commit) or the mirror fails. GitLab may run the
// sync later than asked (it spaces syncs minutes apart), so the branches,
// not the mirror's timestamps, say when it's done. A fresh mirror must also
// finish one update, which proves its token works.
func (pb Publisher) sync(ctx context.Context, gl gitLab, hub gitHub, proj glProject, m remoteMirror, fresh bool, full string, branches map[string]string) error {
	if !fresh && hub.hasBranches(ctx, full, branches) {
		pb.record(newMirror(proj, m, full))
		pb.Out.OK("GitHub is up to date")
		return nil
	}
	if err := gl.syncMirror(ctx, proj.ID, m.ID); err != nil {
		pb.Out.Warn("GitLab didn't start a sync now: %v", err)
	}
	wait, every := cmp.Or(pb.SyncWait, 90*time.Second), cmp.Or(pb.PollEvery, 3*time.Second)
	start := time.Now()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		cur, err := gl.mirror(ctx, proj.ID, m.ID)
		if err != nil {
			return err
		}
		// A reused mirror wasn't failing, so a failure now is this sync's.
		if cur.failing() {
			pb.record(newMirror(proj, cur, full))
			pb.Out.Fail("Mirror sync failed: %s", cleanError(cur.LastError))
			pb.Out.Hint("%s", Hint(newMirror(proj, cur, full)))
			return ErrReported
		}
		if (!fresh || cur.UpdateStatus == statusFinished) && hub.hasBranches(ctx, full, branches) {
			pb.record(newMirror(proj, cur, full))
			pb.Out.OK("Mirror synced in %s", time.Since(start).Round(time.Second))
			return nil
		}
		if time.Since(start) >= wait {
			pb.record(newMirror(proj, cur, full))
			if fresh && hub.hasBranches(ctx, full, branches) {
				pb.Out.Warn("GitHub has every branch, but GitLab hasn't tried the new mirror yet")
				pb.Out.Hint("ds mirrors in a few minutes shows whether it works")
				return nil
			}
			pb.Out.Warn("GitHub isn't up to date after %s; GitLab will keep trying", wait)
			pb.Out.Hint("ds mirrors shows how it ends")
			return nil
		}
	}
}

// parseHeads reads "branch commit" lines from git for-each-ref.
func parseHeads(s string) map[string]string {
	heads := map[string]string{}
	for _, l := range lines(s) {
		if name, sha, ok := strings.Cut(strings.TrimSpace(l), " "); ok {
			heads[name] = sha
		}
	}
	return heads
}

// record saves m in the status file, so a fixed mirror stops the notice.
func (pb Publisher) record(m Mirror) {
	if pb.StatusFile == "" {
		return
	}
	s, _, err := LoadStatus(pb.StatusFile)
	if err != nil {
		s = Status{} // a broken cache is simply replaced
	}
	s.upsert(m)
	if err := s.Save(pb.StatusFile); err != nil {
		pb.Out.Warn("Couldn't save the mirror status: %v", err)
	}
}

func lines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(strings.TrimSpace(s), "\n")
}

func hasLine(s, want string) bool {
	for _, l := range lines(s) {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
