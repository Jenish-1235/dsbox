package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jenish-1235/dsbox/internal/layout"
)

func TestDockerDumpUsesConfiguredHost(t *testing.T) {
	dir, err := os.MkdirTemp("", "dk") // short: unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"Id":"aaaaaaaaaaaaaaaa","Names":["/shop-db-1"],"Image":"postgres:18","State":"running","Status":"Up 2 hours",
		  "Ports":[{"IP":"0.0.0.0","PrivatePort":5432,"PublicPort":5433,"Type":"tcp"}],
		  "Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"db"}}]`)
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "unix://"+sock)
	t.Setenv("DEVBOX_HOST", "mybox")

	root := t.TempDir()
	l := layout.Layout{Home: root, Root: filepath.Join(root, ".devbox"), Etc: filepath.Join(root, "etc")}
	var out strings.Builder
	if err := runDocker(t.Context(), l, []string{"--dump", "--width", "120"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "mybox:5433→5432") || strings.Contains(out.String(), "devbox:5433") {
		t.Errorf("dump doesn't use DEVBOX_HOST:\n%s", out.String())
	}
}
