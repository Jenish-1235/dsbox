package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// gitLab calls the GitLab API through glab, which holds the developer's login.
type gitLab struct {
	run  Runner
	host string
}

// glProject is the part of a GitLab project ds uses.
type glProject struct {
	ID            int    `json:"id"`
	Path          string `json:"path"`
	PathWithNS    string `json:"path_with_namespace"`
	WebURL        string `json:"web_url"`
	HTTPURL       string `json:"http_url_to_repo"`
	SSHURL        string `json:"ssh_url_to_repo"`
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
	// Empty is true while the project has no commits.
	Empty bool `json:"empty_repo"`
}

// remoteMirror is a GitLab push mirror. GitLab masks the credentials in URL.
type remoteMirror struct {
	ID                     int       `json:"id"`
	Enabled                bool      `json:"enabled"`
	URL                    string    `json:"url"`
	UpdateStatus           string    `json:"update_status"`
	LastUpdateAt           time.Time `json:"last_update_at"`
	LastUpdateStartedAt    time.Time `json:"last_update_started_at"`
	LastSuccessfulUpdateAt time.Time `json:"last_successful_update_at"`
	LastError              string    `json:"last_error"`
}

// Mirror update states GitLab reports.
const (
	statusFinished = "finished"
	statusFailed   = "failed"
	// statusRetry follows a failed update that GitLab will try again; after
	// a few tries (minutes apart) it becomes statusFailed.
	statusRetry = "to_retry"
)

// failing reports whether the mirror's last update failed, including one
// GitLab is still retrying (an auth error sits in to_retry for minutes).
func failing(status, lastError string) bool {
	return status == statusFailed || (status == statusRetry && lastError != "")
}

func (m remoteMirror) failing() bool { return failing(m.UpdateStatus, m.LastError) }

// api calls endpoint with method; body (if any) goes as JSON on stdin.
func (g gitLab) api(ctx context.Context, method, endpoint string, body, out any) error {
	args := []string{"api", "--hostname", g.host, "--method", method, endpoint}
	var stdin []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s: %w", method, endpoint, err)
		}
		stdin = data
		args = append(args, "--input", "-")
	}
	res, err := g.output(ctx, stdin, args...)
	if err != nil {
		return apiError("gitlab "+method+" "+endpoint, err)
	}
	if out == nil || res == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(res), out); err != nil {
		return fmt.Errorf("parse gitlab %s %s: %w", method, endpoint, err)
	}
	return nil
}

// output runs glab, and once more after the wait GitLab asks for when it
// rate-limits (gitlab.com allows a burst of 100 requests per user).
func (g gitLab) output(ctx context.Context, stdin []byte, args ...string) (string, error) {
	res, err := g.run.Output(ctx, "", stdin, "glab", args...)
	if err == nil {
		return res, nil
	}
	wait, ok := retryAfter(err.Error())
	if !ok {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", err
	case <-time.After(wait):
	}
	return g.run.Output(ctx, "", stdin, "glab", args...)
}

// retryAfter reads glab's "HTTP 429 … retry after 44s", capped at a minute.
func retryAfter(msg string) (time.Duration, bool) {
	if !strings.Contains(msg, "HTTP 429") {
		return 0, false
	}
	_, rest, ok := strings.Cut(msg, "retry after ")
	if !ok {
		return 30 * time.Second, true
	}
	secs, _, _ := strings.Cut(rest, "s")
	n, err := strconv.Atoi(secs)
	if err != nil {
		return 30 * time.Second, true
	}
	return min(time.Duration(n+1)*time.Second, time.Minute), true
}

func (g gitLab) user(ctx context.Context) (string, error) {
	var u struct {
		Username string `json:"username"`
	}
	if err := g.api(ctx, "GET", "user", nil, &u); err != nil {
		return "", err
	}
	if u.Username == "" {
		return "", errors.New("gitlab user: no username")
	}
	return u.Username, nil
}

func (g gitLab) project(ctx context.Context, path string) (glProject, error) {
	var p glProject
	err := g.api(ctx, "GET", "projects/"+url.QueryEscape(path), nil, &p)
	return p, err
}

// createProject creates a private project in their own namespace.
func (g gitLab) createProject(ctx context.Context, name string) (glProject, error) {
	var p glProject
	err := g.api(ctx, "POST", "projects", map[string]any{
		"name":       name,
		"path":       name,
		"visibility": "private",
	}, &p)
	return p, err
}

func (g gitLab) setDefaultBranch(ctx context.Context, id int, branch string) error {
	return g.api(ctx, "PUT", fmt.Sprintf("projects/%d", id), map[string]any{"default_branch": branch}, nil)
}

// ownedProjects lists the developer's unarchived projects, all pages.
func (g gitLab) ownedProjects(ctx context.Context) ([]glProject, error) {
	const endpoint = "projects?owned=true&archived=false&simple=true&per_page=100"
	res, err := g.output(ctx, nil, "api", "--hostname", g.host, "--paginate", endpoint)
	if err != nil {
		return nil, apiError("list gitlab projects", err)
	}
	var all []glProject
	if err := decodePages(res, &all); err != nil {
		return nil, fmt.Errorf("parse gitlab projects: %w", err)
	}
	return all, nil
}

// decodePages reads what glab or gh --paginate print: one JSON array per
// page, back to back.
func decodePages[T any](s string, all *[]T) error {
	dec := json.NewDecoder(strings.NewReader(s))
	for {
		var page []T
		err := dec.Decode(&page)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		*all = append(*all, page...)
	}
}

func (g gitLab) mirrors(ctx context.Context, projectID int) ([]remoteMirror, error) {
	var ms []remoteMirror
	err := g.api(ctx, "GET", fmt.Sprintf("projects/%d/remote_mirrors", projectID), nil, &ms)
	return ms, err
}

func (g gitLab) mirror(ctx context.Context, projectID, mirrorID int) (remoteMirror, error) {
	var m remoteMirror
	err := g.api(ctx, "GET", fmt.Sprintf("projects/%d/remote_mirrors/%d", projectID, mirrorID), nil, &m)
	return m, err
}

// mirrorSettings are the push mirror settings ds wants: every branch, and
// GitHub always made equal to GitLab.
func mirrorSettings() map[string]any {
	return map[string]any{"enabled": true, "only_protected_branches": false, "keep_divergent_refs": false}
}

// createMirror adds a push mirror to rawURL, which holds a token: callers
// must redact errors.
func (g gitLab) createMirror(ctx context.Context, projectID int, rawURL string) (remoteMirror, error) {
	body := mirrorSettings()
	body["url"] = rawURL
	var m remoteMirror
	err := g.api(ctx, "POST", fmt.Sprintf("projects/%d/remote_mirrors", projectID), body, &m)
	return m, err
}

func (g gitLab) updateMirror(ctx context.Context, projectID, mirrorID int) (remoteMirror, error) {
	var m remoteMirror
	err := g.api(ctx, "PUT", fmt.Sprintf("projects/%d/remote_mirrors/%d", projectID, mirrorID), mirrorSettings(), &m)
	return m, err
}

func (g gitLab) deleteMirror(ctx context.Context, projectID, mirrorID int) error {
	return g.api(ctx, "DELETE", fmt.Sprintf("projects/%d/remote_mirrors/%d", projectID, mirrorID), nil, nil)
}

func (g gitLab) syncMirror(ctx context.Context, projectID, mirrorID int) error {
	return g.api(ctx, "POST", fmt.Sprintf("projects/%d/remote_mirrors/%d/sync", projectID, mirrorID), nil, nil)
}

// gitHub calls GitHub through gh, which holds the developer's login.
type gitHub struct{ run Runner }

// ghRepo is the part of a GitHub repo ds uses.
type ghRepo struct {
	HTMLURL       string `json:"html_url"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
}

func (h gitHub) login(ctx context.Context) (string, error) {
	out, err := h.run.Output(ctx, "", nil, "gh", "api", "user", "--jq", ".login")
	if err != nil {
		return "", fmt.Errorf("find your GitHub user: %w", err)
	}
	if out == "" {
		return "", errors.New("find your GitHub user: gh returned no login")
	}
	return out, nil
}

// ghListed is a repo in the developer's GitHub repo list.
type ghListed struct {
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
}

// repos lists the GitHub repos the developer owns or reaches through an organisation.
func (h gitHub) repos(ctx context.Context) ([]ghListed, error) {
	out, err := h.run.Output(ctx, "", nil, "gh", "api", "--paginate", "user/repos?affiliation=owner,organization_member&per_page=100")
	if err != nil {
		return nil, fmt.Errorf("list your GitHub repos: %w", err)
	}
	var all []ghListed
	if err := decodePages(out, &all); err != nil {
		return nil, fmt.Errorf("parse your GitHub repos: %w", err)
	}
	return all, nil
}

func (h gitHub) repo(ctx context.Context, full string) (ghRepo, error) {
	out, err := h.run.Output(ctx, "", nil, "gh", "api", "repos/"+full)
	if err != nil {
		return ghRepo{}, apiError("github repo "+full, err)
	}
	var r ghRepo
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return ghRepo{}, fmt.Errorf("parse github repo %s: %w", full, err)
	}
	return r, nil
}

// createRepo creates a private repo for a project that lives on GitHub.
func (h gitHub) createRepo(ctx context.Context, full string) error {
	if _, err := h.run.Output(ctx, "", nil, "gh", "repo", "create", full, "--private"); err != nil {
		return fmt.Errorf("create github repo %s: %w", full, err)
	}
	return nil
}

// createMirrorRepo creates a private repo for a GitLab mirror: described as
// one, with issues and wiki off.
func (h gitHub) createMirrorRepo(ctx context.Context, full, description string) error {
	_, err := h.run.Output(ctx, "", nil, "gh", "repo", "create", full, "--private",
		"--description", description, "--disable-issues", "--disable-wiki")
	if err != nil {
		return fmt.Errorf("create github repo %s: %w", full, err)
	}
	return nil
}

func (h gitHub) setDefaultBranch(ctx context.Context, full, branch string) error {
	if _, err := h.run.Output(ctx, "", nil, "gh", "repo", "edit", full, "--default-branch", branch); err != nil {
		return fmt.Errorf("set the default branch of github repo %s: %w", full, err)
	}
	return nil
}

// cloneURL is full's URL in the protocol gh is set to use.
func (h gitHub) cloneURL(ctx context.Context, full string) string {
	if proto, _ := h.run.Output(ctx, "", nil, "gh", "config", "get", "git_protocol", "--host", gitHubHost); proto == "ssh" {
		return "git@" + gitHubHost + ":" + full + ".git"
	}
	return "https://" + gitHubHost + "/" + full + ".git"
}

func (h gitHub) setDescription(ctx context.Context, full, description string) error {
	if _, err := h.run.Output(ctx, "", nil, "gh", "repo", "edit", full, "--description", description); err != nil {
		return fmt.Errorf("describe github repo %s: %w", full, err)
	}
	return nil
}

// branches lists full's branches (name → commit); an empty repo has none.
func (h gitHub) branches(ctx context.Context, full string) (map[string]string, error) {
	out, err := h.run.Output(ctx, "", nil, "gh", "api", "--paginate", "repos/"+full+"/branches?per_page=100")
	if err != nil {
		// GitHub may answer 409 "Git Repository is empty" instead of [].
		if strings.Contains(err.Error(), "(HTTP 409)") {
			return map[string]string{}, nil
		}
		return nil, apiError("github branches of "+full, err)
	}
	var got []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := decodePages(out, &got); err != nil {
		return nil, fmt.Errorf("parse github branches of %s: %w", full, err)
	}
	have := map[string]string{}
	for _, b := range got {
		have[b.Name] = b.Commit.SHA
	}
	return have, nil
}

// hasBranches reports whether full has every branch in want (name → commit)
// at that commit. Any error counts as no.
func (h gitHub) hasBranches(ctx context.Context, full string, want map[string]string) bool {
	if len(want) == 0 {
		return false
	}
	have, err := h.branches(ctx, full)
	if err != nil {
		return false
	}
	for name, sha := range want {
		if have[name] != sha {
			return false
		}
	}
	return true
}

// token is the developer's gh login's token. It must only ever go into a request body.
func (h gitHub) token(ctx context.Context) (string, error) {
	out, err := h.run.Output(ctx, "", nil, "gh", "auth", "token", "--hostname", "github.com")
	if err != nil {
		return "", fmt.Errorf("read your gh token: %w", err)
	}
	if out == "" {
		return "", errors.New("read your gh token: gh returned none")
	}
	return out, nil
}

// mirrorURL is the push URL GitLab uses: https with the developer's GitHub user and token.
func mirrorURL(user, token, full string) string {
	u := url.URL{Scheme: "https", User: url.UserPassword(user, token), Host: "github.com", Path: "/" + full + ".git"}
	return u.String()
}

// githubTarget returns "owner/repo" when a mirror URL (masked or not) points
// at github.com.
func githubTarget(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", false
	}
	full := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if strings.Count(full, "/") != 1 {
		return "", false
	}
	return full, true
}
