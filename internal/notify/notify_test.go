package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewTopic(t *testing.T) {
	want := regexp.MustCompile(`^devbox-[a-z0-9]{24}$`)
	seen := map[string]bool{}
	for range 100 {
		topic := NewTopic()
		if !want.MatchString(topic) {
			t.Fatalf("NewTopic() = %q, want devbox- and 24 lowercase letters or digits", topic)
		}
		if seen[topic] {
			t.Fatalf("NewTopic() repeated %q", topic)
		}
		seen[topic] = true
	}
}

func TestEnsureTopicCreatesPrivateFileOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".config", "devbox", "ntfy-topic")

	if _, ok, err := LoadTopic(path); err != nil || ok {
		t.Fatalf("LoadTopic(missing) = ok %v, err %v; want off, no error", ok, err)
	}
	topic, created, err := EnsureTopic(path)
	if err != nil || !created {
		t.Fatalf("EnsureTopic() = %q, created %v, err %v", topic, created, err)
	}
	assertMode(t, path, 0o600)
	assertMode(t, filepath.Dir(path), 0o700)

	again, created, err := EnsureTopic(path)
	if err != nil || created || again != topic {
		t.Fatalf("EnsureTopic() again = %q, created %v, err %v; want %q, not created", again, created, err, topic)
	}
}

func TestSaveTopicTightensExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ntfy-topic")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveTopic(path, "devbox-abc"); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0o600)
	if got, ok, err := LoadTopic(path); err != nil || !ok || got != "devbox-abc" {
		t.Fatalf("LoadTopic() = %q, %v, %v; want devbox-abc", got, ok, err)
	}
}

func TestLoadTopicRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ntfy-topic")
	if err := os.WriteFile(path, []byte("not a/topic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadTopic(path); err == nil {
		t.Fatal("LoadTopic(invalid) = nil error, want an error")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("mode of %s = %v, want %v", path, got, want)
	}
}

func TestClientSend(t *testing.T) {
	type request struct {
		method, path, title, tags, body string
	}
	got := make(chan request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- request{r.Method, r.URL.Path, r.Header.Get("Title"), r.Header.Get("Tags"), string(body)}
	}))
	defer srv.Close()

	c := Client{Server: srv.URL, HTTP: srv.Client()}
	msg := Message{Title: "shop", Body: "Claude is done", Tags: []string{"white_check_mark"}}
	if err := c.Send(t.Context(), "devbox-abc", msg); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	want := request{"POST", "/devbox-abc", "shop", "white_check_mark", "Claude is done"}
	if r := <-got; r != want {
		t.Errorf("request = %+v, want %+v", r, want)
	}
}

func TestClientSendEncodesNonASCIITitle(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Title")
	}))
	defer srv.Close()

	if err := (Client{Server: srv.URL}).Send(t.Context(), "t", Message{Title: "café"}); err != nil {
		t.Fatal(err)
	}
	if title := <-got; title != "=?utf-8?q?caf=C3=A9?=" {
		t.Errorf("Title header = %q, want an RFC 2047 encoded word", title)
	}
}

func TestClientSendReportsServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "limit reached", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	err := Client{Server: srv.URL}.Send(t.Context(), "devbox-abc", Message{Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("Send() = %v, want an error naming 429", err)
	}
}

func TestClientSendStopsAtDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release // doesn't answer until the test is over
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := (Client{Server: srv.URL}).Send(ctx, "devbox-abc", Message{Body: "x"}); err == nil {
		t.Fatal("Send() = nil, want a timeout error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Send() took %v after a 50ms deadline", d)
	}
}

func TestClientRejectsBadTopic(t *testing.T) {
	if err := (Client{Server: "http://127.0.0.1:1"}).Send(t.Context(), "../x", Message{}); err == nil {
		t.Fatal("Send(bad topic) = nil, want an error")
	}
}

func TestLimiter(t *testing.T) {
	l := Limiter{Dir: filepath.Join(t.TempDir(), "notify"), Every: 30 * time.Second}
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	steps := []struct {
		key  string
		at   time.Duration
		want bool
	}{
		{"shop", 0, true},
		{"shop", 10 * time.Second, false},
		{"blog", 10 * time.Second, true}, // projects are limited separately
		{"shop", 29 * time.Second, false},
		{"shop", 30 * time.Second, true},
		{"shop", 31 * time.Second, false},
		{"../odd name", 0, true}, // stays inside Dir
	}
	for _, s := range steps {
		got, err := l.Allow(s.key, t0.Add(s.at))
		if err != nil {
			t.Fatal(err)
		}
		if got != s.want {
			t.Errorf("Allow(%q, +%v) = %v, want %v", s.key, s.at, got, s.want)
		}
	}
	entries, err := os.ReadDir(l.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("Dir has %d files, want 3 (one per project)", len(entries))
	}
}

func TestLimiterAllowsOneOfConcurrentCalls(t *testing.T) {
	l := Limiter{Dir: t.TempDir(), Every: 30 * time.Second}
	now := time.Now()
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			ok, err := l.Allow("shop", now)
			if err != nil {
				t.Error(err)
			}
			if ok {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if n := allowed.Load(); n != 1 {
		t.Errorf("%d of 20 concurrent calls allowed, want 1", n)
	}
}

func TestHookPush(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string // body, "" for no push
	}{
		{"stop", `{"hook_event_name":"Stop","last_assistant_message":"secret code"}`, "Claude is done"},
		{"stop with background work", `{"hook_event_name":"Stop","background_tasks":[{"id":"1"}]}`, ""},
		{"stop with none", `{"hook_event_name":"Stop","background_tasks":[]}`, "Claude is done"},
		{"permission", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`, "Claude needs your permission"},
		{"elicitation", `{"hook_event_name":"Notification","notification_type":"elicitation_dialog"}`, "Claude is waiting for you"},
		{"idle", `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, ""},
		{"auth", `{"hook_event_name":"Notification","notification_type":"auth_success"}`, ""},
		{"other event", `{"hook_event_name":"PreToolUse"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, err := ParseHook(strings.NewReader(tt.input))
			if err != nil {
				t.Fatal(err)
			}
			msg, ok := in.Push("shop")
			if got := map[bool]string{true: msg.Body}[ok]; got != tt.want {
				t.Fatalf("Push() = %q (ok %v), want %q", msg.Body, ok, tt.want)
			}
			if ok && msg.Title != "shop" {
				t.Errorf("Push().Title = %q, want the project name", msg.Title)
			}
		})
	}
}

func TestParseHookRejectsGarbage(t *testing.T) {
	if _, err := ParseHook(strings.NewReader("not json")); err == nil {
		t.Fatal("ParseHook(garbage) = nil error")
	}
}

func TestProject(t *testing.T) {
	tests := []struct{ projectDir, cwd, want string }{
		{"/home/alex/projects/shop", "/home/alex/projects/shop/src", "shop"},
		{"", "/home/alex/projects/blog/", "blog"},
		{"", "/", "devbox"},
		{"", "", "devbox"},
	}
	for _, tt := range tests {
		if got := Project(tt.projectDir, tt.cwd); got != tt.want {
			t.Errorf("Project(%q, %q) = %q, want %q", tt.projectDir, tt.cwd, got, tt.want)
		}
	}
}

func TestBell(t *testing.T) {
	if got, want := string(Bell()), `{"terminalSequence":"\u0007"}`+"\n"; got != want {
		t.Errorf("Bell() = %q, want %q", got, want)
	}
}
