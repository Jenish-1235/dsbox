package artifact

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newStore is a store in a temp folder with a clock the test moves.
func newStore(t *testing.T) (Store, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	return Store{Dir: filepath.Join(t.TempDir(), "artifacts"), Now: func() time.Time { return now }}, &now
}

func submit(t *testing.T, s Store, project, slug, title, body string) Artifact {
	t.Helper()
	a, _, err := s.Submit(project, slug, title, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Submit(%s/%s): %v", project, slug, err)
	}
	return a
}

func TestSubmitAndList(t *testing.T) {
	s, now := newStore(t)
	if all, err := s.List(); err != nil || len(all) != 0 {
		t.Fatalf("List() on a new store = %v, %v; want nothing", all, err)
	}
	submit(t, s, "shop", "design", "Checkout design", "<h1>a</h1>")
	*now = now.Add(time.Hour)
	submit(t, s, "blog", "report", "Traffic\x1b[31m report\n", "<p>b</p>")

	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID() != "blog/report" || all[1].ID() != "shop/design" {
		t.Fatalf("List() = %+v, want blog/report then shop/design (newest first)", all)
	}
	if all[0].Title != "Traffic [31m report" {
		t.Errorf("title = %q, want control characters removed", all[0].Title)
	}
	data, err := os.ReadFile(s.Path("shop", "design"))
	if err != nil || string(data) != "<h1>a</h1>" {
		t.Errorf("file = %q, %v", data, err)
	}
	if fi, err := os.Stat(s.Dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("artifacts folder mode = %v, %v; want 0700", fi.Mode().Perm(), err)
	}
}

func TestSubmitReplaces(t *testing.T) {
	s, now := newStore(t)
	first := submit(t, s, "shop", "design", "Old", "v1")
	*now = now.Add(time.Hour)
	submit(t, s, "shop", "other", "Other", "x")
	*now = now.Add(time.Hour)
	a, replaced, err := s.Submit("shop", "design", "New", strings.NewReader("v2"))
	if err != nil || !replaced {
		t.Fatalf("Submit again = %v, replaced %v", err, replaced)
	}
	if !a.Created.Equal(first.Created) || !a.Updated.Equal(*now) {
		t.Errorf("created %v updated %v; want created kept and updated = %v", a.Created, a.Updated, *now)
	}
	all, _ := s.List()
	if len(all) != 2 || all[0].ID() != "shop/design" || all[0].Title != "New" {
		t.Fatalf("List() = %+v, want the replaced artifact first, once", all)
	}
	_, f, err := s.Open("shop", "design")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if data, _ := io.ReadAll(f); string(data) != "v2" {
		t.Errorf("content = %q, want v2", data)
	}
}

func TestRemove(t *testing.T) {
	s, _ := newStore(t)
	submit(t, s, "shop", "design", "D", "x")
	if err := s.Remove("shop", "design"); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.List(); len(all) != 0 {
		t.Errorf("List() after Remove = %+v", all)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "shop")); !os.IsNotExist(err) {
		t.Errorf("empty project folder kept: %v", err)
	}
	if err := s.Remove("shop", "design"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Remove twice = %v, want ErrNotFound", err)
	}
	if _, _, err := s.Open("shop", "design"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open after Remove = %v, want ErrNotFound", err)
	}
}

func TestRejectsPaths(t *testing.T) {
	s, _ := newStore(t)
	outside := filepath.Join(filepath.Dir(s.Dir), "evil.html")
	for _, tt := range []struct{ project, slug string }{
		{"..", "x"}, {"shop", ".."}, {"../shop", "x"}, {"shop", "../../evil"}, {"shop", "a/b"},
		{"/etc", "x"}, {"shop", ""}, {"", "x"}, {"Shop", "x"}, {"shop", "x.html"}, {"shop", "-x"},
	} {
		if _, _, err := s.Submit(tt.project, tt.slug, "T", strings.NewReader("x")); err == nil {
			t.Errorf("Submit(%q, %q) succeeded, want refused", tt.project, tt.slug)
		}
		if err := s.Remove(tt.project, tt.slug); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Remove(%q, %q) = %v, want refused", tt.project, tt.slug, err)
		}
		if _, _, err := s.Open(tt.project, tt.slug); !errors.Is(err, ErrNotFound) {
			t.Errorf("Open(%q, %q) = %v, want ErrNotFound", tt.project, tt.slug, err)
		}
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Error("a file was written outside the artifacts folder")
	}
	for _, id := range []string{"shop", "shop/a/b", "../x", "shop/..", "/x"} {
		if _, _, err := ParseID(id); err == nil {
			t.Errorf("ParseID(%q) succeeded", id)
		}
	}
}

func TestIgnoresBadIndexEntries(t *testing.T) {
	s, _ := newStore(t)
	submit(t, s, "shop", "design", "D", "x")
	idx := `[{"project":"..","slug":"etc","title":"bad"},{"project":"shop","slug":"design","title":"D"}]`
	if err := os.WriteFile(s.indexFile(), []byte(idx), 0o600); err != nil {
		t.Fatal(err)
	}
	all, err := s.List()
	if err != nil || len(all) != 1 || all[0].ID() != "shop/design" {
		t.Errorf("List() = %+v, %v; want only shop/design", all, err)
	}
}

func TestSizeCap(t *testing.T) {
	s, _ := newStore(t)
	big := bytes.Repeat([]byte("a"), MaxSize+1)
	if _, _, err := s.Submit("shop", "big", "Big", bytes.NewReader(big)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Submit(%d bytes) = %v, want ErrTooLarge", len(big), err)
	}
	if all, _ := s.List(); len(all) != 0 {
		t.Errorf("a refused artifact was listed: %+v", all)
	}
	if _, _, err := s.Submit("shop", "max", "Max", bytes.NewReader(big[:MaxSize])); err != nil {
		t.Errorf("Submit(MaxSize bytes) = %v, want accepted", err)
	}
}

func TestSubmitNeedsTitle(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.Submit("shop", "x", " \n ", strings.NewReader("x")); err == nil {
		t.Error("Submit without a title succeeded")
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"Design Doc": "design-doc", "../../etc": "etc", "a__b--c": "a-b-c", "--": "", "Ünïcode": "n-code",
	} {
		if got := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
		if got := CleanName(in); got != "" && !ValidName(got) {
			t.Errorf("CleanName(%q) = %q, which isn't valid", in, got)
		}
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		10 * time.Second: "just now", 5 * time.Minute: "5m ago", 3 * time.Hour: "3h ago", 50 * time.Hour: "2d ago",
	} {
		if got := Ago(now.Add(-d), now); got != want {
			t.Errorf("Ago(%v) = %q, want %q", d, got, want)
		}
	}
}
