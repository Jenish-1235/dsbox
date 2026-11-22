package web

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jenish-1235/dsbox/internal/artifact"
	"github.com/Jenish-1235/dsbox/internal/devproj"
	"github.com/Jenish-1235/dsbox/internal/dockui"
	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

// fakeDocker serves a tiny Docker Engine API on a unix socket: one running
// postgres in project "shop" and one nginx (not a database).
func fakeDocker(t *testing.T) *dockui.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = io.WriteString(w, `[
 {"Id":"pg1","Names":["/shop-db-1"],"Image":"postgres:18","State":"running","Status":"Up 1 minute",
  "Ports":[{"IP":"0.0.0.0","PrivatePort":5432,"PublicPort":5433,"Type":"tcp"}],
  "Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"db"}},
 {"Id":"web1","Names":["/shop-web-1"],"Image":"nginx:alpine","State":"running","Status":"Up 1 minute",
  "Ports":[],"Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"web"}}]`)
		case strings.HasSuffix(r.URL.Path, "/containers/pg1/json"):
			_, _ = io.WriteString(w, `{"Config":{"Env":["POSTGRES_USER=app","POSTGRES_PASSWORD=secret","POSTGRES_DB=shop"],"Tty":false},"State":{"StartedAt":"2026-11-18T10:00:00Z"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return dockui.NewClient(sock)
}

func newServer(t *testing.T) Server {
	t.Helper()
	// An empty tmux socket folder: the page must not see the machine's real projects.
	// Inside tmux, $TMUX points tmux at the developer's own server; clear it.
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	root := t.TempDir()
	l := layout.Layout{Home: root, Root: filepath.Join(root, ".devbox")}
	d := devproj.Dev{
		S:   devproj.Settings{Layout: l, ProjectsDir: filepath.Join(root, "projects"), Host: "devbox", AppsDomain: "devbox"},
		Out: ui.Plain(io.Discard), W: io.Discard,
	}
	return Server{Dev: d, Docker: fakeDocker(t), Artifacts: artifact.Store{Dir: l.Artifacts()}}
}

func get(t *testing.T, s Server, url string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec.Code, rec.Body.String()
}

func TestDatabasesPage(t *testing.T) {
	code, body := get(t, newServer(t), "http://dbs.devbox/")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{"<h2>shop</h2>", "db", "postgres://app:secret@devbox:5433/shop"} {
		if !strings.Contains(body, want) {
			t.Errorf("dbs page is missing %q", want)
		}
	}
	if strings.Contains(body, "nginx") {
		t.Error("dbs page lists nginx, which isn't a database")
	}
}

func TestHomePage(t *testing.T) {
	code, body := get(t, newServer(t), "http://devbox/")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{"<h1>devbox</h1>", "No projects yet", `<a href="http://dbs.devbox">1 running</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("home page is missing %q", want)
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	Server{}.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://dbs.devbox/", strings.NewReader("")))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

func TestRootCertificate(t *testing.T) {
	s := newServer(t)
	if code, _ := get(t, s, "http://devbox/root.crt"); code != http.StatusNotFound {
		t.Errorf("GET /root.crt before Caddy made its CA = %d, want 404", code)
	}
	pem := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	if err := os.MkdirAll(filepath.Dir(s.Dev.S.CAFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Dev.S.CAFile(), []byte(pem), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://devbox/root.crt", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != pem {
		t.Errorf("GET /root.crt = %d %q, want 200 and the certificate", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-x509-ca-cert" {
		t.Errorf("Content-Type = %q, want application/x-x509-ca-cert", ct)
	}
}

func TestHomePageTLS(t *testing.T) {
	s := newServer(t)
	s.Dev.S.AppsTLS = "internal"
	_, body := get(t, s, "http://devbox/")
	for _, want := range []string{`<a href="/root.crt">root.crt</a>`, `<a href="https://dbs.devbox">1 running</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("home page with TLS is missing %q", want)
		}
	}
	if _, body := get(t, newServer(t), "http://devbox/"); strings.Contains(body, "root.crt") {
		t.Error("home page without TLS mentions root.crt")
	}
}
