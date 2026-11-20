package publish

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Jenish-1235/dsbox/internal/config"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

func TestClassifyOrigin(t *testing.T) {
	tests := []struct {
		raw      string
		kind     originKind
		wantPath string
	}{
		{"", noOrigin, ""},
		{"https://gitlab.com/alex/shop.git", gitLabOrigin, "alex/shop"},
		{"https://gitlab.com/alex/shop", gitLabOrigin, "alex/shop"},
		{"https://oauth2:tok@gitlab.com/group/sub/shop.git", gitLabOrigin, "group/sub/shop"},
		{"git@gitlab.com:alex/shop.git", gitLabOrigin, "alex/shop"},
		{"ssh://git@GitLab.com:22/alex/shop.git", gitLabOrigin, "alex/shop"},
		{"https://github.com/alex/shop.git", gitHubOrigin, "alex/shop"},
		{"git@github.com:alex/shop.git", gitHubOrigin, "alex/shop"},
		{"ssh://git@GitHub.com/alex/shop", gitHubOrigin, "alex/shop"},
		{"https://github.com/alex/shop/extra.git", otherOrigin, ""},
		{"https://github.com/shop", otherOrigin, ""},
		{"https://gitlab.example.com/alex/shop.git", otherOrigin, ""},
		{"https://gitlab.com/shop", otherOrigin, ""},
		{"/srv/git/shop.git", otherOrigin, ""},
		{"../shop", otherOrigin, ""},
	}
	for _, tt := range tests {
		got := classifyOrigin(tt.raw, "gitlab.com")
		if got.kind != tt.kind || got.path != tt.wantPath {
			t.Errorf("classifyOrigin(%q) = %v %q, want %v %q", tt.raw, got.kind, got.path, tt.kind, tt.wantPath)
		}
	}
}

func TestMirrorURL(t *testing.T) {
	got := mirrorURL("alex-gh", "gho_a/b@c", "alex-gh/shop")
	want := "https://alex-gh:gho_a%2Fb%40c@github.com/alex-gh/shop.git"
	if got != want {
		t.Errorf("mirrorURL() = %q, want %q", got, want)
	}
	for _, raw := range []string{got, "https://*****:*****@github.com/alex-gh/shop.git"} {
		if full, ok := githubTarget(raw); !ok || full != "alex-gh/shop" {
			t.Errorf("githubTarget(%q) = %q, %v; want alex-gh/shop", raw, full, ok)
		}
	}
	if _, ok := githubTarget("https://gitlab.com/alex/shop.git"); ok {
		t.Error("githubTarget(gitlab URL) = ok, want not a GitHub mirror")
	}
}

type harness struct {
	f      *fake
	out    *bytes.Buffer
	status string
	pb     Publisher
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{f: newFake(), out: &bytes.Buffer{}, status: filepath.Join(t.TempDir(), "mirrors.json")}
	h.pb = Publisher{Run: h.f, Out: ui.Plain(h.out), Forge: config.ForgeGitLab, StatusFile: h.status, PollEvery: time.Millisecond, SyncWait: time.Second}
	return h
}

// publish runs ds publish with choice as --github or --no-github (none for AskMirror).
func (h *harness) publish(t *testing.T, choice MirrorChoice) error {
	t.Helper()
	f := Flags{Choice: choice}
	if choice != AskMirror {
		f.To = config.ForgeGitHub
	}
	return h.publishFlags(t, f)
}

func (h *harness) publishFlags(t *testing.T, f Flags) error {
	t.Helper()
	return h.pb.Publish(t.Context(), Project{Name: "shop", Dir: t.TempDir()}, f)
}

// noSecret fails when the token shows up anywhere but a request body.
func (h *harness) noSecret(t *testing.T, err error) {
	t.Helper()
	tok := h.f.token
	if strings.Contains(h.out.String(), tok) {
		t.Errorf("output contains the token:\n%s", h.out)
	}
	if err != nil && strings.Contains(err.Error(), tok) {
		t.Errorf("error contains the token: %v", err)
	}
	for _, c := range h.f.calls {
		if strings.Contains(c.String(), tok) {
			t.Errorf("command line contains the token: %s", c)
		}
	}
}

func TestPublishNewProjectWithMirror(t *testing.T) {
	h := newHarness(t)
	err := h.publish(t, WithMirror)
	h.noSecret(t, err)
	if err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	p := h.f.projects["alex/shop"]
	if p == nil {
		t.Fatalf("no GitLab project alex/shop; output:\n%s", h.out)
	}
	if h.f.origin != p.HTTPURL {
		t.Errorf("origin = %q, want %q", h.f.origin, p.HTTPURL)
	}
	if p.DefaultBranch != "main" {
		t.Errorf("default branch = %q, want main", p.DefaultBranch)
	}
	if n := h.f.ran("git push --quiet -u origin --all"); n != 1 {
		t.Errorf("pushed branches %d times, want 1", n)
	}
	repo, ok := h.f.repos["alex-gh/shop"]
	if !ok || repo.Description != "Mirror of https://gitlab.com/alex/shop; push to GitLab, not here" {
		t.Errorf("GitHub repo = %+v, %v; want alex-gh/shop with the mirror description", repo, ok)
	}
	ms := h.f.mirrors[p.ID]
	if len(ms) != 1 || ms[0].UpdateStatus != statusFinished {
		t.Fatalf("mirrors = %+v, want one, synced", ms)
	}
	// The token went to GitLab in the body, with every branch and forced pushes.
	var body string
	for _, c := range h.f.calls {
		if strings.Contains(c.String(), "--method POST projects/") && strings.HasSuffix(c.String(), "remote_mirrors --input -") {
			body = c.stdin
		}
	}
	for _, want := range []string{`"url":"https://alex-gh:fake-gh-token-for-tests@github.com/alex-gh/shop.git"`, `"keep_divergent_refs":false`, `"only_protected_branches":false`, `"enabled":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("mirror request body %s lacks %s", body, want)
		}
	}
	for _, want := range []string{"GitLab  https://gitlab.com/alex/shop", "GitHub  https://github.com/alex-gh/shop", "Mirror synced"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
	st, ok, err := LoadStatus(h.status)
	if err != nil || !ok || len(st.Mirrors) != 1 || st.Mirrors[0].Status != statusFinished || st.Mirrors[0].GitHub != "alex-gh/shop" {
		t.Errorf("LoadStatus() = %+v, %v, %v; want the synced mirror", st, ok, err)
	}
}

func TestPublishTwiceReuses(t *testing.T) {
	h := newHarness(t)
	if err := h.publish(t, WithMirror); err != nil {
		t.Fatalf("first Publish() = %v\n%s", err, h.out)
	}
	h.out.Reset()
	if err := h.publish(t, WithMirror); err != nil {
		t.Fatalf("second Publish() = %v\n%s", err, h.out)
	}
	if len(h.f.projects) != 1 {
		t.Errorf("GitLab projects = %d, want 1", len(h.f.projects))
	}
	for _, prefix := range []string{"glab api --hostname gitlab.com --method POST projects --input", "gh repo create", "git remote add"} {
		if n := h.f.ran(prefix); n != 1 {
			t.Errorf("%q ran %d times over two publishes, want 1", prefix, n)
		}
	}
	if ms := h.f.mirrors[h.f.projects["alex/shop"].ID]; len(ms) != 1 {
		t.Errorf("mirrors = %d, want 1", len(ms))
	}
	if out := h.out.String(); !strings.Contains(out, "already mirrors") || !strings.Contains(out, "GitHub is up to date") {
		t.Errorf("second run output doesn't say it reused the mirror and GitHub is current:\n%s", out)
	}
	if n := h.f.ran("glab api --hostname gitlab.com --method POST projects/101/remote_mirrors/102/sync"); n != 1 {
		t.Errorf("synced %d times, want 1: the rerun has nothing new for GitHub", n)
	}

	// A new commit makes the next run sync again.
	h.f.branches = append(h.f.branches, "feature")
	h.out.Reset()
	if err := h.publish(t, WithMirror); err != nil {
		t.Fatalf("third Publish() = %v\n%s", err, h.out)
	}
	if !strings.Contains(h.out.String(), "Mirror synced") || h.f.ghBranches["alex-gh/shop"]["feature"] != "sha-feature" {
		t.Errorf("new branch didn't reach GitHub:\n%s", h.out)
	}
}

func TestPublishReusesLeftoverProject(t *testing.T) {
	// An earlier run created the project but stopped before adding origin.
	h := newHarness(t)
	h.f.addProject("alex/shop")
	if err := h.publish(t, WithoutMirror); err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	if len(h.f.projects) != 1 || h.f.ran("glab api --hostname gitlab.com --method POST projects") != 0 {
		t.Errorf("created a second project; projects = %d", len(h.f.projects))
	}
	if h.f.origin != "https://gitlab.com/alex/shop.git" {
		t.Errorf("origin = %q, want the existing project", h.f.origin)
	}
	if !strings.Contains(h.out.String(), "GitLab project alex/shop exists and is empty, using it") {
		t.Errorf("output doesn't say it reused the empty project:\n%s", h.out)
	}
}

func TestPublishRefusesProjectWithCommits(t *testing.T) {
	// Another project of the developer's already has the name.
	h := newHarness(t)
	h.f.addProject("alex/shop").Empty = false
	err := h.publish(t, WithMirror)
	if !errors.Is(err, ErrReported) {
		t.Fatalf("Publish() = %v, want ErrReported\n%s", err, h.out)
	}
	if h.f.origin != "" || h.f.ran("git push") != 0 || h.f.ran("gh") != 0 {
		t.Errorf("set origin to %q or pushed into a project with commits: %v", h.f.origin, h.f.calls)
	}
	for _, want := range []string{
		"GitLab project alex/shop already exists and has commits",
		"if it is this project: git remote add origin https://gitlab.com/alex/shop.git, then ds publish",
	} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
}

func TestPublishReplacesFailedMirror(t *testing.T) {
	h := newHarness(t)
	h.f.origin = "git@gitlab.com:alex/shop.git"
	p := h.f.addProject("alex/shop")
	h.f.repos["alex-gh/shop"] = ghRepo{Description: mirrorDescription(p.WebURL)}
	h.f.mirrors[p.ID] = []remoteMirror{
		{ID: 7, URL: "https://*****:*****@github.com/alex-gh/shop.git", UpdateStatus: statusFailed, LastError: "Authentication failed"},
		{ID: 8, URL: "https://*****:*****@example.org/other.git", UpdateStatus: statusFailed},
	}
	// GitHub already has the commits, from before the token broke.
	h.f.ghBranches["alex-gh/shop"] = h.f.heads()
	err := h.publish(t, WithMirror)
	h.noSecret(t, err)
	if err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	if h.f.ran("glab api --hostname gitlab.com --method DELETE projects/101/remote_mirrors/7") != 1 {
		t.Error("the failed mirror wasn't deleted")
	}
	ms := h.f.mirrors[p.ID]
	if len(ms) != 2 || ms[0].ID != 8 || ms[1].UpdateStatus != statusFinished {
		t.Errorf("mirrors = %+v, want the other one kept and a new synced one", ms)
	}
	if h.f.ran("gh repo create") != 0 || h.f.ran("git remote add") != 0 {
		t.Error("recreated the GitHub repo or origin")
	}
	// The new token must be tried, even though GitHub looked current.
	if out := h.out.String(); !strings.Contains(out, "Mirror synced") || strings.Contains(out, "GitHub is up to date") {
		t.Errorf("output doesn't show the new mirror syncing:\n%s", out)
	}
}

func TestPublishFreshMirrorNotTriedYet(t *testing.T) {
	h := newHarness(t)
	h.f.syncResult = "" // GitLab defers the sync
	h.f.repos["alex-gh/shop"] = ghRepo{Description: mirrorDescription("https://gitlab.com/alex/shop")}
	h.f.ghBranches["alex-gh/shop"] = h.f.heads()
	h.pb.SyncWait = 20 * time.Millisecond
	if err := h.publish(t, WithMirror); err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	if !strings.Contains(h.out.String(), "GitLab hasn't tried the new mirror yet") {
		t.Errorf("output doesn't say the mirror is untried:\n%s", h.out)
	}
}

func TestPublishOtherOrigin(t *testing.T) {
	for _, forge := range []string{config.ForgeGitLab, config.ForgeGitHub} {
		t.Run(forge, func(t *testing.T) {
			h := newHarness(t)
			h.pb.Forge = forge
			h.f.origin = "git@bitbucket.org:someone/shop.git"
			err := h.publish(t, AskMirror)
			if !errors.Is(err, ErrReported) {
				t.Fatalf("Publish() = %v, want ErrReported", err)
			}
			if h.f.ran("git push") != 0 || h.f.ran("glab") != 0 || h.f.ran("gh") != 0 {
				t.Errorf("did something despite a foreign origin: %v", h.f.calls)
			}
			if !strings.Contains(h.out.String(), "origin is git@bitbucket.org:someone/shop.git, which is on neither gitlab.com nor github.com") {
				t.Errorf("output doesn't explain:\n%s", h.out)
			}
		})
	}
}

func TestPublishGitLabForgeGitHubOrigin(t *testing.T) {
	// A project already on GitHub is pushed there, not moved or mirrored.
	h := newHarness(t)
	h.f.origin = "git@github.com:someone/shop.git"
	h.f.repos["someone/shop"] = ghRepo{}
	h.pb.Ask = func(context.Context, string, string) (bool, error) {
		t.Error("asked about a mirror for a project on GitHub")
		return true, nil
	}
	if err := h.publish(t, AskMirror); err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	if h.f.ran("glab") != 0 || h.f.ran("gh repo create") != 0 || h.f.ghBranches["someone/shop"]["main"] != "sha-main" {
		t.Errorf("didn't just push to GitHub: %v", h.f.calls)
	}

	// --github has nothing to mirror.
	h = newHarness(t)
	h.f.origin = "git@github.com:someone/shop.git"
	h.f.repos["someone/shop"] = ghRepo{}
	if err := h.publish(t, WithMirror); !errors.Is(err, ErrReported) {
		t.Fatalf("Publish(--github) = %v, want ErrReported", err)
	}
	if h.f.ran("git push") != 0 || !strings.Contains(h.out.String(), "origin is already on GitHub (someone/shop); --github mirrors a GitLab project") {
		t.Errorf("pushed, or didn't explain:\n%s", h.out)
	}
}

func TestPublishNothingCommitted(t *testing.T) {
	h := newHarness(t)
	h.f.noCommit = true
	if err := h.publish(t, WithoutMirror); !errors.Is(err, ErrReported) {
		t.Fatalf("Publish() = %v, want ErrReported", err)
	}
	if len(h.f.projects) != 0 {
		t.Error("created a GitLab project for an empty repo")
	}
}

func TestPublishRefusesForeignGitHubRepo(t *testing.T) {
	h := newHarness(t)
	h.f.repos["alex-gh/shop"] = ghRepo{Description: "my real shop"}
	err := h.publish(t, WithMirror)
	if !errors.Is(err, ErrReported) {
		t.Fatalf("Publish() = %v, want ErrReported", err)
	}
	if n := len(h.f.mirrors[h.f.projects["alex/shop"].ID]); n != 0 {
		t.Errorf("added %d mirrors onto a repo that isn't a mirror", n)
	}
}

func TestPublishExistingUndescribedGitHubRepo(t *testing.T) {
	// GitHub's size lags pushes, so the branches decide whether it's empty.
	tests := []struct {
		name      string
		branches  map[string]string
		answer409 bool
		reuse     bool
	}{
		{"with branches", map[string]string{"main": "sha-theirs"}, false, false},
		{"no branches", nil, false, true},
		{"empty answers 409", nil, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.f.repos["alex-gh/shop"] = ghRepo{}
			if tt.branches != nil {
				h.f.ghBranches["alex-gh/shop"] = tt.branches
			}
			h.f.ghEmpty409 = tt.answer409
			err := h.publish(t, WithMirror)
			mirrors := len(h.f.mirrors[h.f.projects["alex/shop"].ID])
			desc := h.f.repos["alex-gh/shop"].Description
			if !tt.reuse {
				if !errors.Is(err, ErrReported) {
					t.Fatalf("Publish() = %v, want ErrReported\n%s", err, h.out)
				}
				if mirrors != 0 || desc != "" || h.f.ghBranches["alex-gh/shop"]["main"] != "sha-theirs" {
					t.Errorf("touched a repo with commits: %d mirrors, description %q, branches %v", mirrors, desc, h.f.ghBranches["alex-gh/shop"])
				}
				if !strings.Contains(h.out.String(), "github.com/alex-gh/shop already exists and isn't a mirror of alex/shop") {
					t.Errorf("output doesn't explain:\n%s", h.out)
				}
				return
			}
			if err != nil {
				t.Fatalf("Publish() = %v\n%s", err, h.out)
			}
			if mirrors != 1 || desc != mirrorDescription("https://gitlab.com/alex/shop") {
				t.Errorf("%d mirrors, description %q; want the empty repo taken over", mirrors, desc)
			}
			if !strings.Contains(h.out.String(), "GitHub repo alex-gh/shop exists and is empty, using it") {
				t.Errorf("output doesn't say it reused the empty repo:\n%s", h.out)
			}
		})
	}
}

func TestPublishWithoutTerminalDoesntMirror(t *testing.T) {
	h := newHarness(t) // Ask is nil: no terminal
	if err := h.publish(t, AskMirror); err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	if h.f.ran("gh") != 0 {
		t.Error("touched GitHub without being asked to")
	}
	if !strings.Contains(h.out.String(), "ds publish --github") {
		t.Errorf("output doesn't say how to mirror later:\n%s", h.out)
	}
}

func TestPublishAsks(t *testing.T) {
	h := newHarness(t)
	asked := ""
	h.pb.Ask = func(_ context.Context, name, to string) (bool, error) {
		if to != "GitHub" {
			t.Errorf("asked about a copy on %s, want GitHub", to)
		}
		asked = name
		return false, nil
	}
	if err := h.publish(t, AskMirror); err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	if asked != "shop" || h.f.ran("gh") != 0 {
		t.Errorf("asked %q and ran gh %d times; want asked about shop and no GitHub", asked, h.f.ran("gh"))
	}
	if strings.Contains(h.out.String(), "--github adds") {
		t.Errorf("hinted at --github after the developer said no:\n%s", h.out)
	}
}

func TestPublishRedactsTokenInErrors(t *testing.T) {
	h := newHarness(t)
	h.f.echoBody = true
	err := h.publish(t, WithMirror)
	if err == nil {
		t.Fatal("Publish() = nil, want the mirror error")
	}
	h.noSecret(t, err)
	if !strings.Contains(err.Error(), "url is blocked") {
		t.Errorf("Publish() = %v, want GitLab's reason kept", err)
	}
}

func TestPublishReportsFailedSync(t *testing.T) {
	h := newHarness(t)
	h.f.syncResult, h.f.syncError = statusFailed, "13:Authentication failed for 'https://github.com/alex-gh/shop.git/'"
	err := h.publish(t, WithMirror)
	if !errors.Is(err, ErrReported) {
		t.Fatalf("Publish() = %v, want ErrReported", err)
	}
	if !strings.Contains(h.out.String(), "GitHub token rejected: gh auth login, then ds publish shop --github") {
		t.Errorf("output lacks the hint:\n%s", h.out)
	}
	st, _, _ := LoadStatus(h.status)
	if got := Notices(st, time.Now()); len(got) != 1 || got[0] != "mirror shop → GitHub failing → ds mirrors" {
		t.Errorf("Notices() = %q, want the failing mirror", got)
	}
}

func TestCheck(t *testing.T) {
	f := newFake()
	shop, blog, other, theirs := f.addProject("alex/shop"), f.addProject("alex/blog"), f.addProject("alex/other"), f.addProject("team/theirs")
	f.mirrors[shop.ID] = []remoteMirror{{ID: 1, URL: "https://*****:*****@github.com/alex-gh/shop.git", UpdateStatus: statusFinished}}
	f.mirrors[blog.ID] = []remoteMirror{{ID: 2, URL: "https://*****:*****@github.com/alex-gh/blog.git", UpdateStatus: statusFailed, LastError: "boom\nmore"}}
	f.mirrors[other.ID] = []remoteMirror{{ID: 3, URL: "https://*****:*****@bitbucket.org/x/other.git", UpdateStatus: statusFinished}}
	f.forbidden[theirs.ID] = true
	quiet := f.addProject("alex/quiet") // no GitHub repo of that name: never asked about
	f.mirrors[quiet.ID] = []remoteMirror{{ID: 4, URL: "https://*****:*****@github.com/alex-gh/renamed.git"}}
	f.repos["alex-gh/shop"] = ghRepo{}
	f.repos["alex-gh/web-blog"] = ghRepo{Description: mirrorDescription(blog.WebURL)}
	f.repos["alex-gh/other"] = ghRepo{}
	f.repos["alex-gh/theirs"] = ghRepo{}
	f.limited = 1 // the first call is rate-limited and retried

	st, err := Check(t.Context(), f, "", Status{})
	if err != nil {
		t.Fatalf("Check() = %v", err)
	}
	if len(st.Mirrors) != 2 || st.Mirrors[0].Project != "alex/blog" || st.Mirrors[1].Project != "alex/shop" {
		t.Fatalf("Check() mirrors = %+v, want blog and shop, sorted", st.Mirrors)
	}
	if b := st.Mirrors[0]; !b.Failed() || b.LastError != "boom" || b.GitHub != "alex-gh/blog" || b.GitLabURL != "https://gitlab.com/alex/blog" {
		t.Errorf("blog = %+v, want failed with the first error line", b)
	}
	if st.CheckedAt.IsZero() || st.Error != "" {
		t.Errorf("CheckedAt = %v, Error = %q; want a time and no error", st.CheckedAt, st.Error)
	}
	if n := f.ran("glab api --hostname gitlab.com --method GET projects/" + strconv.Itoa(quiet.ID)); n != 0 {
		t.Errorf("asked about a project with no matching GitHub repo %d times, want 0", n)
	}

	// A mirror seen before is always checked; without gh, only those are.
	f.ghBroken = true
	st, err = Check(t.Context(), f, "", Status{Mirrors: []Mirror{{Project: "alex/quiet"}}})
	if err != nil {
		t.Fatalf("Check() without gh = %v", err)
	}
	if len(st.Mirrors) != 1 || st.Mirrors[0].GitHub != "alex-gh/renamed" || !strings.Contains(st.Error, "only checked the mirrors seen before") {
		t.Errorf("Check() without gh = %+v, want only quiet's mirror and a note", st)
	}
}

func TestCheckGroupProjects(t *testing.T) {
	// ds publish mirrors group projects too; they aren't the developer's, so only the
	// ones seen before are checked.
	f := newFake()
	shop, theirs, old := f.addProject("acme/shop"), f.addProject("team/theirs"), f.addProject("acme/old")
	f.mirrors[shop.ID] = []remoteMirror{{ID: 1, URL: "https://*****:*****@github.com/alex-gh/shop.git", UpdateStatus: statusFailed, LastError: "boom"}}
	f.forbidden[theirs.ID] = true
	old.Archived = true
	f.mirrors[old.ID] = []remoteMirror{{ID: 2, URL: "https://*****:*****@github.com/alex-gh/old.git", UpdateStatus: statusFinished}}
	prev := Status{Mirrors: []Mirror{
		{Project: "acme/shop", GitHub: "alex-gh/shop"},
		{Project: "acme/shop", GitHub: "alex-gh/shop-copy"},
		{Project: "team/theirs", GitHub: "alex-gh/theirs"},
		{Project: "acme/old", GitHub: "alex-gh/old"},
		{Project: "acme/gone", GitHub: "alex-gh/gone"},
	}}

	st, err := Check(t.Context(), f, "", prev)
	if err != nil {
		t.Fatalf("Check() = %v", err)
	}
	if len(st.Mirrors) != 1 || st.Mirrors[0].Project != "acme/shop" || !st.Mirrors[0].Failed() || st.Mirrors[0].Name != "shop" {
		t.Errorf("Check() mirrors = %+v, want acme/shop's failing mirror only", st.Mirrors)
	}
	if n := f.ran("glab api --hostname gitlab.com --method GET projects/acme%2Fshop"); n != 1 {
		t.Errorf("looked up acme/shop %d times, want 1", n)
	}
}

func TestRetryAfter(t *testing.T) {
	tests := []struct {
		msg  string
		want time.Duration
		ok   bool
	}{
		{"glab: HTTP 429\nglab: rate limited by x, retry after 44s Rate limit exceeded", 45 * time.Second, true},
		{"glab: HTTP 429\nglab: rate limited, retry after 600s", time.Minute, true},
		{"glab: HTTP 429", 30 * time.Second, true},
		{"glab: 404 Not Found (HTTP 404)", 0, false},
	}
	for _, tt := range tests {
		if got, ok := retryAfter(tt.msg); got != tt.want || ok != tt.ok {
			t.Errorf("retryAfter(%q) = %v, %v; want %v, %v", tt.msg, got, ok, tt.want, tt.ok)
		}
	}
}

func TestNotices(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	ok := Mirror{Name: "ok", Status: statusFinished}
	shop := Mirror{Name: "shop", Status: statusFailed, LastSuccessAt: now.Add(-50 * time.Hour)}
	blog := Mirror{Name: "blog", Status: statusFailed}
	tests := []struct {
		mirrors []Mirror
		want    []string
	}{
		{nil, nil},
		{[]Mirror{ok}, nil},
		{[]Mirror{ok, shop}, []string{"mirror shop → GitHub failing since 2d → ds mirrors"}},
		{[]Mirror{shop, blog}, []string{"mirror shop → GitHub failing since 2d → ds mirrors", "mirror blog → GitHub failing → ds mirrors"}},
		{[]Mirror{shop, blog, shop}, []string{"3 mirrors → GitHub failing → ds mirrors"}},
	}
	for _, tt := range tests {
		got := Notices(Status{Mirrors: tt.mirrors}, now)
		if strings.Join(got, "|") != strings.Join(tt.want, "|") {
			t.Errorf("Notices(%v) = %q, want %q", tt.mirrors, got, tt.want)
		}
	}
}

func TestStatusRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "mirrors.json")
	if _, ok, err := LoadStatus(path); ok || err != nil {
		t.Fatalf("LoadStatus(missing) = %v, %v; want not ok, no error", ok, err)
	}
	s := Status{CheckedAt: time.Now().UTC().Truncate(time.Second), Mirrors: []Mirror{{Project: "alex/shop", Name: "shop", GitHub: "alex-gh/shop", Status: statusFailed}}}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LoadStatus(path)
	if err != nil || !ok || !got.CheckedAt.Equal(s.CheckedAt) || len(got.Mirrors) != 1 || !got.Mirrors[0].Failed() {
		t.Errorf("LoadStatus() = %+v, %v, %v; want what was saved", got, ok, err)
	}
}

func TestReport(t *testing.T) {
	now := time.Now()
	var buf bytes.Buffer
	Report(ui.Plain(&buf), Status{Mirrors: []Mirror{
		{Name: "blog", GitHub: "alex-gh/blog", Status: statusFailed, LastUpdateAt: now.Add(-3 * time.Hour), LastSuccessAt: now.Add(-72 * time.Hour), LastError: "remote: Repository not found."},
		{Name: "shop", GitHub: "alex-gh/shop", Status: statusFinished, LastUpdateAt: now.Add(-5 * time.Minute)},
	}}, now)
	for _, want := range []string{
		"✗ blog → github.com/alex-gh/blog  failing since 3d, last try 3h ago",
		"GitHub repo missing: ds publish blog --github creates it again",
		"✓ shop → github.com/alex-gh/shop  synced 5m ago",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("Report() lacks %q:\n%s", want, buf.String())
		}
	}
}

func TestCleanError(t *testing.T) {
	// As GitLab reported a rejected token on a real mirror.
	raw := `13:get remote references: create git ls-remote: exit status 128, stderr: "remote: Invalid username or token. Password authentication is not supported for Git operations.\nfatal: Authentication failed for 'https://github.com/x/y.git/'\n".`
	want := "remote: Invalid username or token. Password authentication is not supported for Git operations."
	if got := cleanError(raw); got != want {
		t.Errorf("cleanError() = %q, want %q", got, want)
	}
	if got := Hint(Mirror{Name: "shop", LastError: cleanError(raw)}); !strings.HasPrefix(got, "GitHub token rejected") {
		t.Errorf("Hint(rejected token) = %q, want the token hint", got)
	}
	if got := cleanError(""); got != "" {
		t.Errorf("cleanError(\"\") = %q, want empty", got)
	}
}

func TestFailing(t *testing.T) {
	tests := []struct {
		status, err string
		want        bool
	}{
		{statusFinished, "", false},
		{statusFailed, "", true},
		{statusRetry, "remote: Invalid username or token.", true},
		{statusRetry, "", false},
		{"started", "", false},
	}
	for _, tt := range tests {
		if got := (Mirror{Status: tt.status, LastError: tt.err}).Failed(); got != tt.want {
			t.Errorf("Mirror{%s, %q}.Failed() = %v, want %v", tt.status, tt.err, got, tt.want)
		}
	}
}

func TestHint(t *testing.T) {
	tests := map[string]string{
		"13:Authentication failed for 'https://github.com/x/y.git/'":        "GitHub token rejected: gh auth login, then ds publish shop --github",
		"remote: Invalid username or password.":                             "GitHub token rejected: gh auth login, then ds publish shop --github",
		"remote: Repository not found.":                                     "GitHub repo missing: ds publish shop --github creates it again",
		"! [remote rejected] main -> main (protected branch hook declined)": "GitHub refused the push (branch rules?); push to GitLab only, then ds publish shop --github",
		"something odd": "ds publish shop --github retries; the error above says why it failed",
	}
	for msg, want := range tests {
		if got := Hint(Mirror{Name: "shop", LastError: msg}); got != want {
			t.Errorf("Hint(%q) = %q, want %q", msg, got, want)
		}
	}
}
