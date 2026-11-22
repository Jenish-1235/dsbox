package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func withArtifacts(t *testing.T) Server {
	t.Helper()
	s := newServer(t)
	for _, a := range []struct{ project, slug, title, body string }{
		{"shop", "design", "Checkout <design>", "<script>alert(1)</script><h1>design</h1>"},
		{"blog", "traffic", "Traffic report", "<p>traffic</p>"},
	} {
		if _, _, err := s.Artifacts.Submit(a.project, a.slug, a.title, strings.NewReader(a.body)); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestArtifactRawIsSandboxed(t *testing.T) {
	s := withArtifacts(t)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://artifacts.devbox/shop/design/raw", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "<script>alert(1)</script><h1>design</h1>" {
		t.Fatalf("raw = %d %q", rec.Code, rec.Body.String())
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if csp != "sandbox allow-scripts allow-popups allow-forms" {
		t.Errorf("Content-Security-Policy = %q, want the sandbox without allow-same-origin", csp)
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Error("raw artifacts must not get allow-same-origin")
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
}

func TestArtifactViewer(t *testing.T) {
	code, body := get(t, withArtifacts(t), "http://artifacts.devbox/shop/design")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{
		`<iframe src="/shop/design/raw" sandbox="allow-scripts allow-popups allow-forms"`,
		"Checkout &lt;design&gt;", `href="/shop/design/raw" target="_blank"`, `href="/"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("viewer is missing %q", want)
		}
	}
	if strings.Contains(body, "alert(1)") {
		t.Error("the viewer page must not inline the artifact")
	}
}

func TestArtifactsPage(t *testing.T) {
	code, body := get(t, withArtifacts(t), "http://artifacts.devbox/?project=blog")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{`<h1>Artifacts</h1>`, `href="/blog/traffic"`, `href="/shop/design"`, `Checkout &lt;design&gt;`,
		`<option value="blog" selected>blog (1)</option>`, `id="q"`} {
		if !strings.Contains(body, want) {
			t.Errorf("artifacts page is missing %q", want)
		}
	}
	// Newest first: blog/traffic was submitted last.
	if strings.Index(body, "/blog/traffic") > strings.Index(body, "/shop/design") {
		t.Error("artifacts page should list the newest first")
	}
	if strings.Contains(body, "<design>") {
		t.Error("titles must be escaped")
	}
}

func TestArtifactsEmptyAndMissing(t *testing.T) {
	s := newServer(t)
	if _, body := get(t, s, "http://artifacts.devbox/"); !strings.Contains(body, "No artifacts yet") {
		t.Error("empty hub should say how to submit one")
	}
	for _, u := range []string{
		"http://artifacts.devbox/shop/nope", "http://artifacts.devbox/shop/nope/raw",
		"http://artifacts.devbox/../index.json", "http://artifacts.devbox/shop/../../x/raw", "http://artifacts.devbox/a/b/c/d",
	} {
		if code, _ := get(t, s, u); code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", u, code)
		}
	}
}

func TestArtifactsReadOnly(t *testing.T) {
	s := withArtifacts(t)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(m, "http://artifacts.devbox/shop/design/raw", strings.NewReader("x")))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", m, rec.Code)
		}
	}
	if all, _ := s.Artifacts.List(); len(all) != 2 {
		t.Errorf("artifacts changed over HTTP: %+v", all)
	}
}

func TestHomePageLinksArtifacts(t *testing.T) {
	_, body := get(t, withArtifacts(t), "http://devbox/")
	if !strings.Contains(body, `<a href="http://artifacts.devbox">2 submitted</a>`) {
		t.Error("home page should link the artifact hub")
	}
}
