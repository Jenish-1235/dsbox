package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jenish-1235/dsbox/internal/artifact"
	"github.com/Jenish-1235/dsbox/internal/devproj"
	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

// newArtifactCmd is ds artifact on a temp devbox with apps domain "devbox",
// run from inside the project folder "shop".
func newArtifactCmd(t *testing.T) (artifactCmd, *bytes.Buffer, string) {
	t.Helper()
	root := t.TempDir()
	l := layout.Layout{Home: root, Root: filepath.Join(root, ".devbox")}
	shop := filepath.Join(root, "projects", "Shop_App", "docs")
	if err := os.MkdirAll(shop, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(shop)
	var stdout bytes.Buffer
	return artifactCmd{
		S:     devproj.Settings{Layout: l, ProjectsDir: filepath.Join(root, "projects"), Host: "devbox", AppsDomain: "devbox", AppsTLS: "internal"},
		Store: artifact.Store{Dir: l.Artifacts()},
		Out:   ui.Plain(io.Discard), W: &stdout,
	}, &stdout, shop
}

func TestArtifactSubmitCommand(t *testing.T) {
	c, stdout, dir := newArtifactCmd(t)
	file := filepath.Join(dir, "Design Doc.html")
	if err := os.WriteFile(file, []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The project comes from the folder, the slug from the file name.
	if err := c.run([]string{"submit", file, "--title", "Design doc"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "https://artifacts.devbox/shop-app/design-doc" {
		t.Errorf("submit printed %q", got)
	}
	stdout.Reset()
	if err := c.run([]string{"submit", "--project=blog", "--slug", "x", file, "--title=T"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "https://artifacts.devbox/blog/x" {
		t.Errorf("submit with options printed %q", got)
	}
	stdout.Reset()
	if err := c.run([]string{"url", "design-doc"}); err != nil || strings.TrimSpace(stdout.String()) != "https://artifacts.devbox/shop-app/design-doc" {
		t.Errorf("url design-doc = %q, %v", stdout.String(), err)
	}
	if err := c.run([]string{"rm", "blog/x"}); err != nil {
		t.Fatal(err)
	}
	if all, _ := c.Store.List(); len(all) != 1 || all[0].ID() != "shop-app/design-doc" {
		t.Errorf("after rm: %+v", all)
	}
	if err := c.run([]string{"rm", "blog/x"}); !isReported(err) {
		t.Errorf("rm of a missing artifact = %v, want a reported error", err)
	}
}

func TestArtifactSubmitRefuses(t *testing.T) {
	c, _, dir := newArtifactCmd(t)
	md := filepath.Join(dir, "notes.md")
	html := filepath.Join(dir, "notes.html")
	for _, f := range []string{md, html} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"submit", md, "--title", "T"},                           // not HTML
		{"submit", html},                                         // no title
		{"submit", html, "--title", "T", "--project", "../etc"},  // a path
		{"submit", html, "--title", "T", "--slug", "../../evil"}, // a path
		{"submit", html, "--title"},                              // no value
		{"submit", html, "--title", "T", "--nope", "x"},          // unknown option
		{"url", "../x"}, {"rm", "shop/../../x"}, {"frobnicate"},
	} {
		if err := c.run(args); err == nil {
			t.Errorf("ds artifact %v succeeded, want refused", args)
		}
	}
	if all, _ := c.Store.List(); len(all) != 0 {
		t.Errorf("refused submits were stored: %+v", all)
	}
}
