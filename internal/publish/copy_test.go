package publish

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Jenish-1235/dsbox/internal/config"
)

// newCopyHarness publishes with DEVBOX_FORGE=github for a developer logged
// in to GitLab, who answers yes when asked about a copy there.
func newCopyHarness(t *testing.T) (*harness, *[]string) {
	t.Helper()
	h := newHarness(t)
	h.pb.Forge = config.ForgeGitHub
	var asked []string
	h.pb.Ask = func(_ context.Context, name, to string) (bool, error) {
		asked = append(asked, name+" "+to)
		return true, nil
	}
	return h, &asked
}

func TestGitHubPublishCopyToGitLab(t *testing.T) {
	h, asked := newCopyHarness(t)
	h.f.branches = []string{"feature", "main"} // feature arrives first
	if err := h.publish(t, AskMirror); err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	if strings.Join(*asked, "|") != "shop GitLab" {
		t.Errorf("asked %q, want once about a copy of shop on GitLab", *asked)
	}
	p, ok := h.f.projects["alex/shop"]
	if !ok || p.Description != copyDescription("alex-gh/shop") || p.DefaultBranch != "main" {
		t.Fatalf("GitLab project = %+v, %v; want alex/shop described as a copy, default branch main\n%s", p, ok, h.out)
	}
	if got := h.f.glBranches["alex/shop"]; got["main"] != "sha-main" || got["feature"] != "sha-feature" {
		t.Errorf("GitLab branches = %v, want every local branch", got)
	}
	want := []string{"https://github.com/alex-gh/shop.git", "https://gitlab.com/alex/shop.git"}
	if strings.Join(h.f.pushURLs, " ") != strings.Join(want, " ") {
		t.Errorf("push URLs = %q, want GitHub first, then GitLab", h.f.pushURLs)
	}
	for _, s := range []string{"Created GitLab project alex/shop (private)", "GitHub  https://github.com/alex-gh/shop", "GitLab  https://gitlab.com/alex/shop"} {
		if !strings.Contains(h.out.String(), s) {
			t.Errorf("output lacks %q:\n%s", s, h.out)
		}
	}
	st, _, err := LoadStatus(h.status)
	if err != nil || len(st.Mirrors) != 1 || st.Mirrors[0].Kind != KindCopy || st.Mirrors[0].Failed() {
		t.Errorf("status = %+v, %v; want one copy, in sync", st, err)
	}

	// A rerun pushes to both through origin, without asking again.
	h.f.branches = []string{"dev", "feature", "main"}
	if err := h.publishFlags(t, Flags{}); err != nil {
		t.Fatalf("second Publish() = %v\n%s", err, h.out)
	}
	if len(*asked) != 1 || len(h.f.pushURLs) != 2 {
		t.Errorf("asked %q, push URLs %q; want no new question or push URL", *asked, h.f.pushURLs)
	}
	if h.f.ghBranches["alex-gh/shop"]["dev"] == "" || h.f.glBranches["alex/shop"]["dev"] == "" {
		t.Errorf("new branch missing: GitHub %v, GitLab %v", h.f.ghBranches["alex-gh/shop"], h.f.glBranches["alex/shop"])
	}
	if !strings.Contains(h.out.String(), "to alex-gh/shop and gitlab.com/alex/shop") {
		t.Errorf("second run doesn't say it pushed to both:\n%s", h.out)
	}
}

func TestGitHubPublishCopyFlags(t *testing.T) {
	// --gitlab copies without asking; --no-gitlab neither asks nor copies.
	h, _ := newCopyHarness(t)
	h.pb.Ask = nil
	if err := h.publishFlags(t, Flags{Choice: WithMirror, To: config.ForgeGitLab}); err != nil {
		t.Fatalf("Publish(--gitlab) = %v\n%s", err, h.out)
	}
	if _, ok := h.f.projects["alex/shop"]; !ok || len(h.f.pushURLs) != 2 {
		t.Errorf("--gitlab made no copy: %v", h.f.calls)
	}

	h, asked := newCopyHarness(t)
	if err := h.publishFlags(t, Flags{Choice: WithoutMirror, To: config.ForgeGitLab}); err != nil {
		t.Fatalf("Publish(--no-gitlab) = %v\n%s", err, h.out)
	}
	if len(*asked) != 0 || len(h.f.projects) != 0 || h.f.ran("glab") != 0 {
		t.Errorf("--no-gitlab asked %q or touched GitLab: %v", *asked, h.f.calls)
	}
}

func TestGitHubPublishCopyReusesEmptyProject(t *testing.T) {
	h, _ := newCopyHarness(t)
	h.f.addProject("alex/shop")
	if err := h.publish(t, AskMirror); err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	if h.f.projects["alex/shop"].Description != copyDescription("alex-gh/shop") || len(h.f.pushURLs) != 2 {
		t.Errorf("didn't take over the empty project: %+v, push URLs %q", h.f.projects["alex/shop"], h.f.pushURLs)
	}
	if !strings.Contains(h.out.String(), "GitLab project alex/shop exists and is empty, using it") {
		t.Errorf("output doesn't say it reused the project:\n%s", h.out)
	}
}

func TestGitHubPublishCopyRefusesOtherProject(t *testing.T) {
	for name, setup := range map[string]func(*glProject){
		"has commits":         func(p *glProject) { p.Empty = false },
		"copy of another one": func(p *glProject) { p.Description = copyDescription("someone/shop") },
	} {
		h, _ := newCopyHarness(t)
		setup(h.f.addProject("alex/shop"))
		err := h.publish(t, AskMirror)
		if !errors.Is(err, ErrReported) {
			t.Fatalf("%s: Publish() = %v, want ErrReported\n%s", name, err, h.out)
		}
		if len(h.f.pushURLs) != 0 || h.f.glBranches["alex/shop"] != nil {
			t.Errorf("%s: pushed to the other project or added it to origin: %q", name, h.f.pushURLs)
		}
		if !strings.Contains(h.out.String(), "gitlab.com/alex/shop already exists and isn't a copy of github.com/alex-gh/shop") {
			t.Errorf("%s: output doesn't explain:\n%s", name, h.out)
		}
		// GitHub got the push; only the copy was refused.
		if h.f.ghBranches["alex-gh/shop"]["main"] != "sha-main" {
			t.Errorf("%s: GitHub wasn't pushed to", name)
		}
	}
}

func TestPublishCopyFlagChecks(t *testing.T) {
	// --gitlab with the gitlab forge.
	h := newHarness(t)
	if err := h.publishFlags(t, Flags{Choice: WithMirror, To: config.ForgeGitLab}); !errors.Is(err, ErrReported) {
		t.Fatalf("Publish(--gitlab, gitlab forge) = %v, want ErrReported", err)
	}
	if len(h.f.calls) != 0 || !strings.Contains(h.out.String(), "--gitlab and --no-gitlab only apply when DEVBOX_FORGE=github") {
		t.Errorf("ran commands or didn't explain: %v\n%s", h.f.calls, h.out)
	}

	// --gitlab for a project already on GitLab.
	h, _ = newCopyHarness(t)
	h.f.origin = "git@gitlab.com:alex/shop.git"
	h.f.addProject("alex/shop")
	if err := h.publishFlags(t, Flags{Choice: WithMirror, To: config.ForgeGitLab}); !errors.Is(err, ErrReported) {
		t.Fatalf("Publish(--gitlab, GitLab origin) = %v, want ErrReported", err)
	}
	if h.f.ran("git push") != 0 || !strings.Contains(h.out.String(), "origin is already on GitLab (alex/shop); --gitlab copies a GitHub project") {
		t.Errorf("pushed, or didn't explain:\n%s", h.out)
	}
}

func TestCheckCopies(t *testing.T) {
	h, _ := newCopyHarness(t)
	h.f.addProject("alex/other") // not a copy
	if err := h.publish(t, AskMirror); err != nil {
		t.Fatalf("Publish() = %v\n%s", err, h.out)
	}
	st, err := CheckCopies(t.Context(), h.f, "", Status{})
	if err != nil || len(st.Mirrors) != 1 {
		t.Fatalf("CheckCopies() = %+v, %v; want the one copy", st, err)
	}
	if m := st.Mirrors[0]; m.Kind != KindCopy || m.Project != "alex/shop" || m.GitHub != "alex-gh/shop" || m.Failed() {
		t.Errorf("copy = %+v, want alex/shop in sync with alex-gh/shop", m)
	}

	// A PR merged on GitHub: the copy is behind until the next git push.
	h.f.ghBranches["alex-gh/shop"]["main"] = "sha-merged"
	synced := st.Mirrors[0].LastSuccessAt
	st, err = CheckCopies(t.Context(), h.f, "", st)
	if err != nil || len(st.Mirrors) != 1 {
		t.Fatalf("CheckCopies() = %+v, %v", st, err)
	}
	m := st.Mirrors[0]
	if !m.Failed() || m.LastError != "main differs from GitHub" || !m.LastSuccessAt.Equal(synced) {
		t.Errorf("copy = %+v; want out of sync on main, last in sync kept", m)
	}
	if n := Notices(st, synced.Add(3*time.Hour)); len(n) != 1 || n[0] != "GitLab copy of shop differs from GitHub since 3h → ds mirrors" {
		t.Errorf("Notices() = %q", n)
	}
	if len(st.For(config.ForgeGitLab).Mirrors) != 0 || len(st.For(config.ForgeGitHub).Mirrors) != 1 {
		t.Errorf("For() doesn't keep copies to the github forge")
	}

	// Not logged in to GitLab: no copies, no error, no API calls.
	f := newFake()
	f.glSignedOut = true
	st, err = CheckCopies(t.Context(), f, "", Status{})
	if err != nil || len(st.Mirrors) != 0 || f.ran("glab api") != 0 {
		t.Errorf("CheckCopies() signed out = %+v, %v, %d API calls", st, err, f.ran("glab api"))
	}
}
