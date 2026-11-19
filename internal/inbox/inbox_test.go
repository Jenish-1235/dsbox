package inbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func put(t *testing.T, dir, name string, mod time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func names(files []File) []string {
	var out []string
	for _, f := range files {
		out = append(out, filepath.Base(f.Path))
	}
	return out
}

func TestListNewestFirst(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	put(t, dir, "shot-a.png", now.Add(-time.Hour))
	put(t, dir, "shot-b.png", now)
	put(t, dir, "shot-c.png", now.Add(-48*time.Hour))
	put(t, dir, ".upload.tmp", now)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	files, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(files), []string{"shot-b.png", "shot-a.png", "shot-c.png"}; !slices.Equal(got, want) {
		t.Errorf("List() = %v, want %v", got, want)
	}
}

func TestListMissingInbox(t *testing.T) {
	files, err := List(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(files) != 0 {
		t.Fatalf("List(missing) = %v, %v; want empty, no error", files, err)
	}
}

func TestCleanRemovesOnlyOldFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	put(t, dir, "old.png", now.Add(-8*24*time.Hour))
	put(t, dir, "week.png", now.Add(-Keep+time.Minute))
	put(t, dir, "new.png", now)

	n, err := Clean(dir, Keep, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Clean() removed %d, want 1", n)
	}
	files, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(files), []string{"new.png", "week.png"}; !slices.Equal(got, want) {
		t.Errorf("after Clean, inbox = %v, want %v", got, want)
	}

	if n, err := Clean(filepath.Join(dir, "nope"), Keep, now); n != 0 || err != nil {
		t.Errorf("Clean(missing) = %d, %v; want 0, nil", n, err)
	}
}
