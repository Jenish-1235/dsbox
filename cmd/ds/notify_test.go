package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/notify"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

func TestQRLines(t *testing.T) {
	lines, err := qrLines("https://ntfy.sh/devbox-abcdefghijklmnopqrstuvwx")
	if err != nil {
		t.Fatal(err)
	}
	// Version 3 is 29 modules, plus a 2-module border: 33 wide, 17 lines.
	if len(lines) != 17 {
		t.Fatalf("qrLines() has %d lines, want 17", len(lines))
	}
	for i, line := range lines {
		if n := utf8.RuneCountInString(line); n != 33 {
			t.Errorf("line %d is %d wide, want 33", i, n)
		}
	}
	if want := strings.Repeat("█", 33); lines[0] != want {
		t.Errorf("first line = %q, want the light border", lines[0])
	}
}

func TestNotifyArgs(t *testing.T) {
	var (
		mu   sync.Mutex
		sent []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(body))
		mu.Unlock()
	}))
	defer srv.Close()
	t.Setenv("DEVBOX_NTFY_SERVER", srv.URL)

	root := t.TempDir()
	l := layout.Layout{Home: root, Root: filepath.Join(root, ".devbox")}
	if err := notify.SaveTopic(l.NotifyTopicFile(), notify.NewTopic()); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		return runNotify(t.Context(), l, args, strings.NewReader(""), io.Discard, ui.Plain(io.Discard))
	}

	// A subcommand followed by more words is refused, and changes nothing.
	for _, args := range [][]string{{"off", "to", "lunch"}, {"setup", "done"}, {"hook", "x"}, {"bell", "x"}} {
		if err := run(args...); err == nil || !strings.Contains(err.Error(), "quote it") {
			t.Errorf("ds notify %s = %v, want an error saying to quote the message", strings.Join(args, " "), err)
		}
	}
	if _, err := os.Stat(l.NotifyTopicFile()); err != nil {
		t.Fatalf("topic file gone after refused commands: %v", err)
	}
	// The suggested command is safe to paste: $ and ' stay literal.
	if err := run("off", "it's", "$5"); err == nil || !strings.Contains(err.Error(), `ds notify 'off it'\''s $5'`) {
		t.Errorf("suggested command not shell-quoted: %v", err)
	}

	// Anything else is the message, however many words.
	for _, args := range [][]string{{"offline", "build", "done"}, {"off to lunch"}} {
		if err := run(args...); err != nil {
			t.Errorf("ds notify %q = %v", args, err)
		}
	}
	mu.Lock()
	got := strings.Join(sent, "|")
	mu.Unlock()
	if want := "offline build done|off to lunch"; got != want {
		t.Errorf("pushes sent = %q, want %q", got, want)
	}

	if err := run("off"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.NotifyTopicFile()); !os.IsNotExist(err) {
		t.Errorf("ds notify off left the topic file: %v", err)
	}
}
