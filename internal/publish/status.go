package publish

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Jenish-1235/dsbox/internal/config"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

// DefaultHost is the GitLab host the developer's projects live on.
const DefaultHost = "gitlab.com"

// Mirror is one GitLab project's push mirror to GitHub or, with Kind
// KindCopy, a GitHub project's copy on GitLab.
type Mirror struct {
	Kind string `json:"kind,omitempty"`
	// Project is the GitLab project path, e.g. "alex/shop"; Name is its last part.
	Project   string `json:"project"`
	Name      string `json:"name"`
	GitLabURL string `json:"gitlab_url"`
	// GitHub is "owner/repo".
	GitHub string `json:"github"`
	// Status is GitLab's update_status: finished, failed, started, ….
	Status        string    `json:"status"`
	LastUpdateAt  time.Time `json:"last_update_at,omitzero"`
	LastSuccessAt time.Time `json:"last_success_at,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
}

// Failed reports whether the mirror's last update failed (see failing), or
// the copy isn't the same as GitHub.
func (m Mirror) Failed() bool {
	if m.Kind == KindCopy {
		return m.Status != statusInSync
	}
	return failing(m.Status, m.LastError)
}

func newMirror(p glProject, m remoteMirror, github string) Mirror {
	return Mirror{
		Project:       p.PathWithNS,
		Name:          p.Path,
		GitLabURL:     p.WebURL,
		GitHub:        github,
		Status:        m.UpdateStatus,
		LastUpdateAt:  cmp.Or(m.LastUpdateAt, m.LastUpdateStartedAt), // unset while to_retry
		LastSuccessAt: m.LastSuccessfulUpdateAt,
		LastError:     cleanError(m.LastError),
	}
}

// Status is the last check of every mirror, cached in
// ~/.devbox/state/mirrors.json so the shell can show failures without
// touching the network.
type Status struct {
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// Error is why the last check couldn't finish; Mirrors are then from the
	// check before.
	Error   string   `json:"error,omitempty"`
	Mirrors []Mirror `json:"mirrors"`
}

// LoadStatus reads the cached status. ok is false when there is none yet.
func LoadStatus(path string) (s Status, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, fmt.Errorf("read mirror status: %w", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, false, fmt.Errorf("parse mirror status %s: %w", path, err)
	}
	return s, true, nil
}

// Save writes the status atomically.
func (s Status) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode mirror status: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state folder: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write mirror status: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("save mirror status: %w", err)
	}
	return nil
}

// For keeps what the forge has: copies on GitLab for github, mirrors to
// GitHub for gitlab. A forge switch leaves the other kind in the cache.
func (s Status) For(forge string) Status {
	copies := forge != config.ForgeGitLab
	out := s
	out.Mirrors = nil
	for _, m := range s.Mirrors {
		if (m.Kind == KindCopy) == copies {
			out.Mirrors = append(out.Mirrors, m)
		}
	}
	return out
}

// upsert replaces the entry for m's project and target, or adds it.
func (s *Status) upsert(m Mirror) {
	i := slices.IndexFunc(s.Mirrors, func(o Mirror) bool {
		return o.Kind == m.Kind && o.Project == m.Project && o.GitHub == m.GitHub
	})
	if i >= 0 {
		s.Mirrors[i] = m
		return
	}
	s.Mirrors = append(s.Mirrors, m)
	sortMirrors(s.Mirrors)
}

func sortMirrors(ms []Mirror) {
	slices.SortFunc(ms, func(a, b Mirror) int {
		if c := strings.Compare(a.Project, b.Project); c != 0 {
			return c
		}
		return strings.Compare(a.GitHub, b.GitHub)
	})
}

// Check asks GitLab for the push mirrors to github.com on the developer's projects. host
// "" means DefaultHost; prev is the last status, whose mirrors are always
// checked again, group projects included.
//
// gitlab.com allows a burst of 100 API requests per user, fewer than one per
// project for someone with many, so only likely mirrors are asked about: a
// project whose name matches one of the developer's GitHub repos, whose GitHub repo says
// "Mirror of <its GitLab URL>" (ds publish writes that), or that prev lists.
// Without gh, only prev's mirrors are checked, and Status.Error says so.
// With no GitHub repos (or no gh) and no mirrors seen before there's nothing
// to check: GitLab isn't asked, and the status is simply empty.
func Check(ctx context.Context, run Runner, host string, prev Status) (Status, error) {
	if host == "" {
		host = DefaultHost
	}
	st := Status{}
	repos, err := gitHub{run: run}.repos(ctx)
	switch {
	case len(repos) == 0 && len(prev.Mirrors) == 0:
		return Status{CheckedAt: time.Now().UTC()}, nil
	case err != nil:
		st.Error = "only checked the mirrors seen before: " + firstLine(err.Error())
	}
	g := gitLab{run: run, host: host}
	projects, err := g.ownedProjects(ctx)
	if err != nil {
		return Status{}, err
	}
	if projects, err = addSeen(ctx, g, projects, prev); err != nil {
		return Status{}, err
	}
	var (
		mu   sync.Mutex
		out  []Mirror
		errs []error
		wg   sync.WaitGroup
		sem  = make(chan struct{}, 4)
	)
	for _, p := range candidates(projects, repos, prev) {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			ms, err := g.mirrors(ctx, p.ID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// Mirrors need Maintainer; a project the developer can't see them on has none of the developer's.
				if !strings.Contains(err.Error(), "(HTTP 403)") {
					errs = append(errs, err)
				}
				return
			}
			for _, m := range ms {
				if full, ok := githubTarget(m.URL); ok {
					out = append(out, newMirror(p, m, full))
				}
			}
		})
	}
	wg.Wait()
	switch len(errs) {
	case 0:
	case 1:
		return Status{}, errs[0]
	default:
		return Status{}, fmt.Errorf("%w (and %d more errors)", errs[0], len(errs)-1)
	}
	sortMirrors(out)
	st.CheckedAt, st.Mirrors = time.Now().UTC(), out
	return st, nil
}

// addSeen adds the projects of prev's mirrors that projects lacks: ds
// publish mirrors group projects too, and only their own are listed. A project
// that's gone, hidden or archived is dropped.
func addSeen(ctx context.Context, g gitLab, projects []glProject, prev Status) ([]glProject, error) {
	have := map[string]bool{"": true}
	for _, p := range projects {
		have[p.PathWithNS] = true
	}
	for _, m := range prev.Mirrors {
		if have[m.Project] {
			continue
		}
		have[m.Project] = true
		p, err := g.project(ctx, m.Project)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !p.Archived {
			projects = append(projects, p)
		}
	}
	return projects, nil
}

// candidates are the projects that may mirror to GitHub (see Check).
func candidates(projects []glProject, repos []ghListed, prev Status) []glProject {
	names, described, known := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, r := range repos {
		names[strings.ToLower(r.Name)] = true
		if rest, ok := strings.CutPrefix(r.Description, "Mirror of "); ok {
			u, _, _ := strings.Cut(rest, ";")
			described[strings.TrimSpace(u)] = true
		}
	}
	for _, m := range prev.Mirrors {
		known[m.Project] = true
	}
	var out []glProject
	for _, p := range projects {
		if names[strings.ToLower(p.Path)] || described[p.WebURL] || known[p.PathWithNS] {
			out = append(out, p)
		}
	}
	return out
}

// Notices turns the cached status into short lines for the start of a
// shell: one per failing mirror, or one in all when more than two fail.
func Notices(s Status, now time.Time) []string {
	var failed []Mirror
	for _, m := range s.Mirrors {
		if m.Failed() {
			failed = append(failed, m)
		}
	}
	if len(failed) > 2 {
		if failed[0].Kind == KindCopy {
			return []string{fmt.Sprintf("%d GitLab copies differ from GitHub → ds mirrors", len(failed))}
		}
		return []string{fmt.Sprintf("%d mirrors → GitHub failing → ds mirrors", len(failed))}
	}
	var out []string
	for _, m := range failed {
		since := ""
		if !m.LastSuccessAt.IsZero() {
			since = " since " + short(now.Sub(m.LastSuccessAt))
		}
		if m.Kind == KindCopy {
			out = append(out, fmt.Sprintf("GitLab copy of %s differs from GitHub%s → ds mirrors", m.Name, since))
			continue
		}
		out = append(out, fmt.Sprintf("mirror %s → GitHub failing%s → ds mirrors", m.Name, since))
	}
	return out
}

// Report prints one line per mirror, failures with a hint.
func Report(out ui.Printer, s Status, now time.Time) {
	if s.Error != "" {
		out.Warn("%s", s.Error)
		out.Hint("gh auth status shows whether you're logged in to GitHub")
	}
	if len(s.Mirrors) == 0 {
		out.Text("No GitHub mirrors yet.")
		out.Hint("ds publish --github in a project adds one")
		return
	}
	name, target := 0, 0
	for _, m := range s.Mirrors {
		name, target = max(name, len(m.Name)), max(target, len("github.com/"+m.GitHub))
	}
	for _, m := range s.Mirrors {
		line := fmt.Sprintf("%-*s → %-*s  ", name, m.Name, target, "github.com/"+m.GitHub)
		switch {
		case m.Status == statusFinished:
			out.OK("%s%s", line, out.Dim("synced "+ago(now, m.LastUpdateAt)))
		case m.Failed():
			since := "failing"
			if !m.LastSuccessAt.IsZero() {
				since += " since " + short(now.Sub(m.LastSuccessAt))
			}
			if !m.LastUpdateAt.IsZero() {
				since += ", last try " + ago(now, m.LastUpdateAt)
			}
			out.Fail("%s%s", line, since)
			if m.LastError != "" {
				out.Text("    %s", out.Dim(m.LastError))
			}
			out.Hint("%s", Hint(m))
		case m.Status == "" || m.Status == "none":
			out.Warn("%snot synced yet", line)
		default:
			out.Warn("%s%s", line, m.Status)
			if m.LastError != "" {
				out.Text("    %s", out.Dim(m.LastError))
			}
		}
	}
}

// Hint says what to do about a failing mirror, from GitLab's error.
func Hint(m Mirror) string {
	e := strings.ToLower(m.LastError)
	retry := "ds publish " + m.Name + " --github"
	switch {
	case containsAny(e, "authentication failed", "invalid username or", "could not read username",
		"bad credentials", "403", "401", "permission denied", "write access to repository not granted"):
		return "GitHub token rejected: gh auth login, then " + retry
	case containsAny(e, "repository not found", "not found"):
		return "GitHub repo missing: " + retry + " creates it again"
	case containsAny(e, "protected branch", "rejected", "non-fast-forward", "pre-receive hook"):
		return "GitHub refused the push (branch rules?); push to GitLab only, then " + retry
	}
	return retry + " retries; the error above says why it failed"
}

func containsAny(s string, subs ...string) bool {
	return slices.ContainsFunc(subs, func(sub string) bool { return strings.Contains(s, sub) })
}

// ago is "3h ago", or "never" for a zero time.
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return short(now.Sub(t)) + " ago"
}

// short is a duration as one rounded unit: 45s → 1m, 5h, 3d.
func short(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(1, int(d.Round(time.Minute)/time.Minute)))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

// cleanError shortens GitLab's mirror error to what GitHub said, e.g.
//
//	13:get remote references: … stderr: "remote: Invalid username or token. …\nfatal: …"
//
// becomes "remote: Invalid username or token. …".
func cleanError(s string) string {
	s = firstLine(strings.ReplaceAll(s, `\n`, "\n"))
	if _, rest, ok := strings.Cut(s, `stderr: "`); ok && strings.TrimSpace(rest) != "" {
		s = rest
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), `"`))
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
