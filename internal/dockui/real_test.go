package dockui

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// TestRealDocker runs against a real Docker daemon when DOCKUI_REAL names a
// running postgres container started with POSTGRES_PASSWORD=x and -p 5499:5432:
//
//	DOCKUI_REAL=dtui-pg go test -run Real ./internal/dockui
func TestRealDocker(t *testing.T) {
	name := os.Getenv("DOCKUI_REAL")
	if name == "" {
		t.Skip("DOCKUI_REAL not set")
	}
	c := NewClient(SocketPath())
	ctx := t.Context()
	cs, err := c.Containers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pg Container
	for _, ct := range cs {
		if ct.Name() == name {
			pg = ct
		}
	}
	if pg.ID == "" {
		t.Fatalf("no container %s", name)
	}
	d, err := c.Inspect(ctx, pg.ID)
	if err != nil {
		t.Fatal(err)
	}
	lines := Connect(pg.Image, d.Env, Addresses(pg.Ports, "devbox"))
	t.Logf("connect: %+v", lines)
	if want := "postgres://postgres:x@devbox:5499/postgres"; lines[0].CopyValue() != want {
		t.Errorf("connect = %q, want %q", lines[0].CopyValue(), want)
	}

	r, err := c.Logs(ctx, pg.ID, 5, false, d.TTY)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		t.Logf("log: %s", sc.Text())
		if strings.ContainsRune(sc.Text(), 0) || strings.ContainsRune(sc.Text(), 1) {
			t.Errorf("log line has frame bytes: %q", sc.Text())
		}
	}
	_ = r.Close()
	if n == 0 || n > 5 {
		t.Errorf("logs: %d lines, want 1..5", n)
	}

	if err := c.Restart(ctx, pg.ID); err != nil {
		t.Fatal(err)
	}
	du, err := c.DiskUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("disk: %d images, %d volumes, %d cache entries", len(du.Images), len(du.Volumes), len(du.BuildCache))
}
