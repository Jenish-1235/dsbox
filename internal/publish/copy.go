package publish

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Jenish-1235/dsbox/internal/ui"
)

// A GitHub project's copy on GitLab. gitlab.com's free plan can't pull from
// GitHub, so the copy is a second push URL on origin: every git push from
// the project updates GitHub and GitLab. The GitLab project's description
// names the GitHub repo, which is how reruns and checks recognise it.

// KindCopy marks a Mirror that is a GitHub project's copy on GitLab; the
// zero Kind is a GitLab project's push mirror to GitHub.
const KindCopy = "gitlab-copy"

// A copy's states: the same commits on both, different ones, or couldn't
// check (statusFailed).
const (
	statusInSync    = "in_sync"
	statusOutOfSync = "out_of_sync"
)

const copyPrefix = "Copy of https://" + gitHubHost + "/"

// copyDescription is the GitLab project's description for a copy of full.
func copyDescription(full string) string {
	return copyPrefix + full + "; push to GitHub, not here"
}

// copyTarget is the GitHub repo a copy's description names.
func copyTarget(description string) (string, bool) {
	rest, ok := strings.CutPrefix(description, copyPrefix)
	if !ok {
		return "", false
	}
	full, _, _ := strings.Cut(rest, ";")
	full = strings.TrimSpace(full)
	return full, strings.Count(full, "/") == 1
}

// gitLabCopy is the GitLab project origin also pushes to, "" when none.
func (pb Publisher) gitLabCopy(ctx context.Context, dir, host string) string {
	// git exits 1 when there's no push URL.
	urls, _ := pb.Run.Output(ctx, dir, nil, "git", "config", "--get-all", "remote.origin.pushurl")
	for _, u := range lines(urls) {
		if o := classifyOrigin(u, host); o.kind == gitLabOrigin {
			return o.path
		}
	}
	return ""
}

// offerCopy adds a copy on GitLab when choice says so, or when the developer
// says yes. Only someone logged in to GitLab is asked, so a GitHub-only
// developer never hears about it.
func (pb Publisher) offerCopy(ctx context.Context, p Project, full, host string, choice MirrorChoice, branches map[string]string) error {
	ghLine := func() { pb.Out.Text("GitHub  https://%s/%s", gitHubHost, full) }
	if choice == WithoutMirror || (choice == AskMirror && (pb.Ask == nil || !pb.signedInToGitLab(ctx, host))) {
		ghLine()
		return nil
	}
	if choice == AskMirror {
		yes, err := pb.Ask(ctx, p.Name, "GitLab")
		if err != nil {
			return err
		}
		if !yes {
			ghLine()
			return nil
		}
	}
	path, err := pb.copyToGitLab(ctx, p.Dir, full, host, branches)
	ghLine()
	if path != "" {
		pb.Out.Text("GitLab  https://%s/%s  %s", host, path, pb.Out.Dim("(copy; git push updates both)"))
	}
	return err
}

func (pb Publisher) signedInToGitLab(ctx context.Context, host string) bool {
	_, err := pb.Run.Output(ctx, "", nil, "glab", "auth", "status", "--hostname", host)
	return err == nil
}

// copyToGitLab finds or creates the private project <glab user>/<repo name>,
// pushes every branch and tag to it, and adds it as a push URL of origin. It
// returns the project's path once something was pushed there.
func (pb Publisher) copyToGitLab(ctx context.Context, dir, full, host string, branches map[string]string) (string, error) {
	gl := gitLab{run: pb.Run, host: host}
	user, err := gl.user(ctx)
	if err != nil {
		pb.Out.Fail("Can't use GitLab: %v", err)
		pb.Out.Hint("glab auth login, then ds publish --gitlab")
		return "", ErrReported
	}
	_, name, _ := strings.Cut(full, "/")
	desc := copyDescription(full)
	proj, err := gl.project(ctx, user+"/"+name)
	created := false
	switch target, isCopy := copyTarget(proj.Description); {
	case errors.Is(err, ErrNotFound):
		if proj, err = gl.createProject(ctx, name, desc); err != nil {
			return "", err
		}
		created = true
		pb.Out.OK("Created GitLab project %s (private)", proj.PathWithNS)
	case err != nil:
		return "", err
	case isCopy && strings.EqualFold(target, full):
		pb.Out.OK("GitLab project %s exists, using it", proj.PathWithNS)
	case proj.Description != "" || !proj.Empty:
		pb.Out.Fail("%s/%s already exists and isn't a copy of %s/%s", host, proj.PathWithNS, gitHubHost, full)
		pb.Out.Hint("pushing would mix two projects; rename or delete it on GitLab, then ds publish --gitlab")
		return "", ErrReported
	default:
		if err := gl.setDescription(ctx, proj.ID, desc); err != nil {
			return "", err
		}
		pb.Out.OK("GitLab project %s exists and is empty, using it", proj.PathWithNS)
	}

	git := func(args ...string) (string, error) { return pb.Run.Output(ctx, dir, nil, "git", args...) }
	url := pb.cloneURL(ctx, host, proj)
	if _, err := git("push", "--quiet", url, "--all"); err != nil {
		return "", fmt.Errorf("push branches to %s: %w", proj.PathWithNS, err)
	}
	if _, err := git("push", "--quiet", url, "--tags"); err != nil {
		return "", fmt.Errorf("push tags to %s: %w", proj.PathWithNS, err)
	}
	// A new project takes whichever branch arrives first as its default.
	if _, hasMain := branches["main"]; created && hasMain {
		if now, err := gl.project(ctx, proj.PathWithNS); err == nil && now.DefaultBranch != "main" {
			if err := gl.setDefaultBranch(ctx, proj.ID, "main"); err != nil {
				pb.Out.Warn("Couldn't make main the default branch: %v", err)
			}
		}
	}

	// With no push URL git pushes to the fetch URL; once one is added it
	// pushes only to the push URLs, so GitHub's goes in first.
	if have, _ := git("config", "--get-all", "remote.origin.pushurl"); len(lines(have)) == 0 {
		fetch, err := git("remote", "get-url", "origin")
		if err != nil {
			return proj.PathWithNS, fmt.Errorf("read origin: %w", err)
		}
		if _, err := git("remote", "set-url", "--add", "--push", "origin", fetch); err != nil {
			return proj.PathWithNS, fmt.Errorf("add a push URL to origin: %w", err)
		}
	}
	if _, err := git("remote", "set-url", "--add", "--push", "origin", url); err != nil {
		return proj.PathWithNS, fmt.Errorf("add a push URL to origin: %w", err)
	}
	pb.Out.OK("Pushed %s to %s; git push now updates GitHub and GitLab", count(len(branches), "branch", "branches"), proj.PathWithNS)
	pb.record(copyStatus(host, proj.PathWithNS, full))
	return proj.PathWithNS, nil
}

// copyStatus is a copy that was just pushed to, so it matches GitHub.
func copyStatus(host, path, full string) Mirror {
	now := time.Now().UTC()
	return Mirror{
		Kind:          KindCopy,
		Project:       path,
		Name:          path[strings.LastIndex(path, "/")+1:],
		GitLabURL:     "https://" + host + "/" + path,
		GitHub:        full,
		Status:        statusInSync,
		LastUpdateAt:  now,
		LastSuccessAt: now,
	}
}

// CheckCopies finds the developer's GitLab copies of GitHub repos and
// compares their branches with GitHub's. host "" means DefaultHost; prev
// keeps when each copy was last in sync. Someone not logged in to GitLab has
// no copies, and that is not an error.
func CheckCopies(ctx context.Context, run Runner, host string, prev Status) (Status, error) {
	if host == "" {
		host = DefaultHost
	}
	now := time.Now().UTC()
	if !(Publisher{Run: run}).signedInToGitLab(ctx, host) {
		return Status{CheckedAt: now}, nil
	}
	g, hub := gitLab{run: run, host: host}, gitHub{run: run}
	projects, err := g.ownedProjects(ctx)
	if err != nil {
		return Status{}, err
	}
	lastSync := map[string]time.Time{}
	for _, m := range prev.Mirrors {
		if m.Kind == KindCopy {
			lastSync[m.Project] = m.LastSuccessAt
		}
	}
	var out []Mirror
	for _, p := range projects {
		full, ok := copyTarget(p.Description)
		if !ok {
			continue
		}
		m := Mirror{Kind: KindCopy, Project: p.PathWithNS, Name: p.Path, GitLabURL: p.WebURL, GitHub: full,
			LastUpdateAt: now, LastSuccessAt: lastSync[p.PathWithNS]}
		want, err := hub.branches(ctx, full)
		if err != nil {
			m.Status, m.LastError = statusFailed, firstLine(err.Error())
			out = append(out, m)
			continue
		}
		have, err := g.branches(ctx, p.ID)
		if err != nil {
			return Status{}, err
		}
		if diff := differing(want, have); len(diff) > 0 {
			verb := " differ from GitHub"
			if len(diff) == 1 {
				verb = " differs from GitHub"
			}
			m.Status, m.LastError = statusOutOfSync, strings.Join(diff, ", ")+verb
		} else {
			m.Status, m.LastSuccessAt = statusInSync, now
		}
		out = append(out, m)
	}
	sortMirrors(out)
	return Status{CheckedAt: now, Mirrors: out}, nil
}

// differing are the branches of want (name → commit) that have lacks or has
// at another commit, sorted.
func differing(want, have map[string]string) []string {
	var out []string
	for name, sha := range want {
		if have[name] != sha {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// ReportCopies prints one line per GitLab copy, problems with a hint.
func ReportCopies(out ui.Printer, s Status) {
	if len(s.Mirrors) == 0 {
		out.Text("No GitLab copies yet.")
		out.Hint("ds publish --gitlab in a GitHub project adds one")
		return
	}
	name, target := 0, 0
	for _, m := range s.Mirrors {
		name, target = max(name, len(m.Name)), max(target, len(strings.TrimPrefix(m.GitLabURL, "https://")))
	}
	for _, m := range s.Mirrors {
		line := fmt.Sprintf("%-*s → %-*s  ", name, m.Name, target, strings.TrimPrefix(m.GitLabURL, "https://"))
		switch m.Status {
		case statusInSync:
			out.OK("%s%s", line, out.Dim("same as github.com/"+m.GitHub))
		case statusOutOfSync:
			out.Fail("%s%s", line, m.LastError)
			out.Hint("in %s: git pull, then git push (it updates GitHub and GitLab)", m.Name)
		default:
			out.Fail("%scouldn't read github.com/%s", line, m.GitHub)
			if m.LastError != "" {
				out.Text("    %s", out.Dim(m.LastError))
			}
			out.Hint("gh auth status shows whether you're logged in to GitHub")
		}
	}
}
