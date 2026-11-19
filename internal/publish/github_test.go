package publish

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Jenish-1235/dsbox/internal/config"
)

// newGitHubHarness publishes with DEVBOX_FORGE=github.
func newGitHubHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.pb.Forge = config.ForgeGitHub
	h.pb.Ask = func(context.Context, string) (bool, error) {
		t.Error("asked about a GitHub mirror with the github forge")
		return false, nil
	}
	return h
}

func TestGitHubPublishNewRepo(t *testing.T) {
	h := newGitHubHarness(t)
	h.f.branches = []string{"feature", "main"} // feature arrives first
	if err := h.publish(t, AskGitHub); err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	repo, ok := h.f.repos["alex-gh/shop"]
	if !ok {
		t.Fatalf("no GitHub repo alex-gh/shop; output:\n%s", h.out)
	}
	if h.f.ran("gh repo create alex-gh/shop --private") != 1 || repo.Description != "" {
		t.Errorf("repo = %+v; want one private repo with no mirror description: %v", repo, h.f.calls)
	}
	if h.f.origin != "https://github.com/alex-gh/shop.git" {
		t.Errorf("origin = %q, want the new repo", h.f.origin)
	}
	if h.f.ran("git push --quiet -u origin --all") != 1 || h.f.ran("git push --quiet origin --tags") != 1 {
		t.Errorf("didn't push branches and tags once each: %v", h.f.calls)
	}
	if got := h.f.ghBranches["alex-gh/shop"]; got["main"] != "sha-main" || got["feature"] != "sha-feature" {
		t.Errorf("GitHub branches = %v, want every local branch", got)
	}
	if repo.DefaultBranch != "main" {
		t.Errorf("default branch = %q, want main", repo.DefaultBranch)
	}
	if h.f.ran("glab") != 0 {
		t.Errorf("ran glab with the github forge: %v", h.f.calls)
	}
	for _, want := range []string{"Created GitHub repo alex-gh/shop (private)", "Pushed 2 branches, 0 tags to alex-gh/shop", "GitHub  https://github.com/alex-gh/shop"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
	if strings.Contains(h.out.String(), "--github") {
		t.Errorf("output mentions mirroring:\n%s", h.out)
	}
}

func TestGitHubPublishSSH(t *testing.T) {
	h := newGitHubHarness(t)
	h.f.ghProtocol = "ssh"
	if err := h.publish(t, AskGitHub); err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	if h.f.origin != "git@github.com:alex-gh/shop.git" {
		t.Errorf("origin = %q, want the ssh URL gh is set to use", h.f.origin)
	}
}

func TestGitHubPublishReusesEmptyRepo(t *testing.T) {
	// An earlier run created the repo but stopped before adding origin.
	for _, answer409 := range []bool{false, true} {
		h := newGitHubHarness(t)
		h.f.repos["alex-gh/shop"] = ghRepo{}
		h.f.ghEmpty409 = answer409
		if err := h.publish(t, AskGitHub); err != nil {
			t.Fatalf("Publish() (409: %v) = %v\n%s", answer409, err, h.out)
		}
		if h.f.ran("gh repo create") != 0 || h.f.origin != "https://github.com/alex-gh/shop.git" {
			t.Errorf("409 %v: created a repo or origin = %q, want the existing repo", answer409, h.f.origin)
		}
		if !strings.Contains(h.out.String(), "GitHub repo alex-gh/shop exists and is empty, using it") {
			t.Errorf("409 %v: output doesn't say it reused the empty repo:\n%s", answer409, h.out)
		}
	}
}

func TestGitHubPublishRefusesRepoWithCommits(t *testing.T) {
	// Another repo of the developer's already has the name.
	h := newGitHubHarness(t)
	h.f.repos["alex-gh/shop"] = ghRepo{}
	h.f.ghBranches["alex-gh/shop"] = map[string]string{"main": "sha-theirs"}
	err := h.publish(t, AskGitHub)
	if !errors.Is(err, ErrReported) {
		t.Fatalf("Publish() = %v, want ErrReported\n%s", err, h.out)
	}
	if h.f.origin != "" || h.f.ran("git push") != 0 || h.f.ghBranches["alex-gh/shop"]["main"] != "sha-theirs" {
		t.Errorf("set origin to %q or pushed into a repo with commits: %v", h.f.origin, h.f.calls)
	}
	for _, want := range []string{
		"GitHub repo alex-gh/shop already exists and has commits",
		"if it is this project: git remote add origin https://github.com/alex-gh/shop.git, then ds publish",
	} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
}

func TestGitHubPublishExistingOrigin(t *testing.T) {
	// Any GitHub repo the developer can push to, not only one under the developer's login.
	h := newGitHubHarness(t)
	h.f.origin = "git@github.com:acme/shop.git"
	h.f.repos["acme/shop"] = ghRepo{DefaultBranch: "main"}
	if err := h.publish(t, AskGitHub); err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	if h.f.ran("gh repo create") != 0 || h.f.ran("git remote add") != 0 || h.f.ran("gh repo edit") != 0 {
		t.Errorf("changed the repo or origin: %v", h.f.calls)
	}
	if h.f.ghBranches["acme/shop"]["main"] != "sha-main" || !strings.Contains(h.out.String(), "GitHub  https://github.com/acme/shop") {
		t.Errorf("didn't push to acme/shop:\n%s", h.out)
	}
}

func TestGitHubPublishMissingOrigin(t *testing.T) {
	h := newGitHubHarness(t)
	h.f.origin = "https://github.com/alex-gh/gone.git"
	if err := h.publish(t, AskGitHub); !errors.Is(err, ErrReported) {
		t.Fatalf("Publish() = %v, want ErrReported", err)
	}
	if h.f.ran("git push") != 0 || !strings.Contains(h.out.String(), "origin points at github.com/alex-gh/gone, which doesn't exist or you can't see") {
		t.Errorf("pushed, or didn't explain:\n%s", h.out)
	}
}

func TestGitHubPublishLoggedOut(t *testing.T) {
	h := newGitHubHarness(t)
	h.f.ghBroken = true
	if err := h.publish(t, AskGitHub); !errors.Is(err, ErrReported) {
		t.Fatalf("Publish() = %v, want ErrReported", err)
	}
	if h.f.ran("git push") != 0 || h.f.ran("git remote add") != 0 || !strings.Contains(h.out.String(), "gh auth login, then ds publish") {
		t.Errorf("went on without a GitHub login, or didn't say how to log in:\n%s", h.out)
	}
}

func TestGitHubPublishGitLabOrigin(t *testing.T) {
	// A project already on GitLab is pushed there, never mirrored.
	h := newGitHubHarness(t)
	h.f.origin = "git@gitlab.com:alex/shop.git"
	h.f.addProject("alex/shop")
	if err := h.publish(t, AskGitHub); err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	if h.f.ran("git push --quiet -u origin --all") != 1 || h.f.ran("gh") != 0 || h.f.ran("glab api --hostname gitlab.com --method POST") != 0 {
		t.Errorf("didn't just push to GitLab: %v", h.f.calls)
	}
	if out := h.out.String(); !strings.Contains(out, "GitLab  https://gitlab.com/alex/shop") || strings.Contains(out, "--github") {
		t.Errorf("output should show the GitLab project and not offer a mirror:\n%s", out)
	}
}

func TestGitHubPublishRejectsMirrorFlags(t *testing.T) {
	for _, choice := range []GitHubChoice{WithGitHub, WithoutGitHub} {
		h := newGitHubHarness(t)
		if err := h.publish(t, choice); !errors.Is(err, ErrReported) {
			t.Fatalf("Publish(%v) = %v, want ErrReported", choice, err)
		}
		if len(h.f.calls) != 0 {
			t.Errorf("Publish(%v) ran commands: %v", choice, h.f.calls)
		}
		if !strings.Contains(h.out.String(), "--github and --no-github only apply when DEVBOX_FORGE=gitlab") {
			t.Errorf("Publish(%v) output doesn't explain:\n%s", choice, h.out)
		}
	}
}

func TestPublishUnknownForge(t *testing.T) {
	h := newHarness(t)
	h.pb.Forge = "bitbucket"
	if err := h.publish(t, AskGitHub); !errors.Is(err, ErrReported) {
		t.Fatalf("Publish() = %v, want ErrReported", err)
	}
	if len(h.f.calls) != 0 || !strings.Contains(h.out.String(), `DEVBOX_FORGE is "bitbucket"; it must be github or gitlab`) {
		t.Errorf("ran commands or didn't explain: %v\n%s", h.f.calls, h.out)
	}
}

func TestPublishDefaultForgeIsGitHub(t *testing.T) {
	h := newHarness(t)
	h.pb.Forge = ""
	if err := h.publish(t, AskGitHub); err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	if _, ok := h.f.repos["alex-gh/shop"]; !ok || len(h.f.projects) != 0 {
		t.Errorf("repos %v, GitLab projects %d; want a GitHub repo only", h.f.repos, len(h.f.projects))
	}
}

func TestCheckNothingToCheck(t *testing.T) {
	// No GitHub repos (or no gh) and no mirrors seen before: GitLab isn't asked.
	for _, broken := range []bool{false, true} {
		f := newFake()
		f.ghBroken = broken
		f.addProject("alex/shop")
		st, err := Check(t.Context(), f, "", Status{})
		if err != nil {
			t.Fatalf("Check() (gh broken: %v) = %v", broken, err)
		}
		if len(st.Mirrors) != 0 || st.Error != "" || st.CheckedAt.IsZero() {
			t.Errorf("Check() (gh broken: %v) = %+v, want an empty, checked status with no error", broken, st)
		}
		if n := f.ran("glab"); n != 0 {
			t.Errorf("Check() (gh broken: %v) ran glab %d times, want 0", broken, n)
		}
	}
}
