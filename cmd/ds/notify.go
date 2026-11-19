package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/Jenish-1235/dsbox/internal/inbox"
	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/notify"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

const (
	// pushTimeout bounds every push: hooks must never hold Claude Code up.
	pushTimeout = 5 * time.Second
	// pushEvery is the most often one project pushes from a hook.
	pushEvery = 30 * time.Second
)

// runNotify handles ds notify: setup, off, the Claude Code hooks (hook, bell)
// and sending a message by hand. Anything that isn't a subcommand on its own
// is the message.
func runNotify(ctx context.Context, l layout.Layout, args []string, stdin io.Reader, stdout io.Writer, out ui.Printer) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	// "ds notify off to lunch" must not turn pushes off.
	if len(args) > 1 && slices.Contains([]string{"setup", "off", "bell", "hook"}, sub) {
		return fmt.Errorf("ds notify %s takes no arguments; to send a message starting with %q, quote it: ds notify %s", sub, sub, shellQuote(strings.Join(args, " ")))
	}
	switch sub {
	case "":
		return notifyStatus(l, out)
	case "setup":
		return notifySetup(ctx, l, out)
	case "off":
		if err := os.Remove(l.NotifyTopicFile()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("turn phone notifications off: %w", err)
		}
		out.OK("Phone notifications off. ds notify setup turns them on again (with a new topic).")
		return nil
	case "bell":
		// Claude Code hook: it prints this sequence to the terminal for us.
		_, err := stdout.Write(notify.Bell())
		return err
	case "hook":
		// Claude Code hook: never fails, never prints. Problems go to a log.
		if err := notifyHook(ctx, l, stdin); err != nil {
			logNotify(l, err)
		}
		return nil
	}
	return notifyByHand(ctx, l, strings.Join(args, " "), out)
}

func notifyStatus(l layout.Layout, out ui.Printer) error {
	topic, ok, err := notify.LoadTopic(l.NotifyTopicFile())
	switch {
	case err != nil:
		return err
	case ok:
		out.OK("Phone notifications on · topic %s", topic)
		out.Hint("ds notify setup shows how to subscribe; ds notify off stops them")
	default:
		out.Text("Phone notifications are off.")
		out.Hint("ds notify setup")
	}
	return nil
}

func notifySetup(ctx context.Context, l layout.Layout, out ui.Printer) error {
	topic, created, err := notify.EnsureTopic(l.NotifyTopicFile())
	if err != nil {
		return err
	}
	server := ntfyServer()
	link := strings.TrimSuffix(server, "/") + "/" + topic

	out.Title("Phone notifications")
	if created {
		out.Text("%s", out.Dim("New private topic saved in "+l.Pretty(l.NotifyTopicFile())))
	}
	out.Text("1. Install ntfy on your phone (free, no account):")
	out.Text("     Android  https://play.google.com/store/apps/details?id=io.heckel.ntfy")
	out.Text("     iPhone   https://apps.apple.com/app/ntfy/id1625396347")
	out.Text("2. In the app tap +, enter this topic and Subscribe:")
	out.Text("     %s", topic)
	if server != notify.DefaultServer {
		out.Text("     server: %s (use another server)", server)
	}
	if lines, err := qrLines(link); err == nil {
		out.Text("   or scan this to open it on the phone:")
		for _, line := range lines {
			out.Text("     %s", line)
		}
	}

	sctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	msg := notify.Message{Title: "devbox", Body: "Test: phone notifications work", Tags: []string{"tada"}}
	if err := (notify.Client{Server: server}).Send(sctx, topic, msg); err != nil {
		out.Fail("Couldn't send a test push: %v", err)
		out.Hint("check the internet connection, then ds notify setup again")
		return errReported
	}
	out.OK("3. Test push sent; it arrives within seconds once you're subscribed.")
	out.Text("Claude Code now pushes when it's done or needs you, at most once per 30s per project.")
	out.Text("%s", out.Dim("Keep the topic private: anyone who has it can read the pushes. ds notify off stops them."))
	return nil
}

// notifyHook handles a Claude Code Notification or Stop hook: it pushes to
// the phone when a topic is set up and the project hasn't pushed recently.
func notifyHook(ctx context.Context, l layout.Layout, stdin io.Reader) error {
	in, err := notify.ParseHook(stdin)
	if err != nil {
		return err
	}
	topic, ok, err := notify.LoadTopic(l.NotifyTopicFile())
	if err != nil || !ok {
		return err // off unless ds notify setup ran
	}
	project := notify.Project(os.Getenv("CLAUDE_PROJECT_DIR"), in.Cwd)
	msg, ok := in.Push(project)
	if !ok {
		return nil
	}
	limit := notify.Limiter{Dir: l.NotifyStateDir(), Every: pushEvery}
	if allowed, err := limit.Allow(project, time.Now()); err != nil || !allowed {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	return notify.Client{Server: ntfyServer()}.Send(sctx, topic, msg)
}

// notifyByHand sends message, titled with the current folder's name. It only
// warns on failure, so it's safe at the end of scripts and in hooks.
func notifyByHand(ctx context.Context, l layout.Layout, message string, out ui.Printer) error {
	topic, ok, err := notify.LoadTopic(l.NotifyTopicFile())
	switch {
	case err != nil:
		out.Warn("%v", err)
		return nil
	case !ok:
		out.Warn("Phone notifications are off → ds notify setup")
		return nil
	}
	cwd, _ := os.Getwd()
	sctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	msg := notify.Message{Title: notify.Project("", cwd), Body: message}
	if err := (notify.Client{Server: ntfyServer()}).Send(sctx, topic, msg); err != nil {
		out.Warn("%v", err)
	}
	return nil
}

// ntfyServer is ntfy.sh unless DEVBOX_NTFY_SERVER names another (self-hosted) one.
func ntfyServer() string {
	if s := os.Getenv("DEVBOX_NTFY_SERVER"); s != "" {
		return s
	}
	return notify.DefaultServer
}

// logNotify appends a hook failure to ~/.devbox/logs/notify.log, best effort.
func logNotify(l layout.Layout, err error) {
	if mkErr := os.MkdirAll(l.Logs(), 0o755); mkErr != nil {
		return
	}
	f, openErr := os.OpenFile(filepath.Join(l.Logs(), "notify.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if openErr != nil {
		return
	}
	// Best-effort log for a hook that must never fail Claude.
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s %v\n", time.Now().Format(time.RFC3339), err)
}

// qrLines draws text as a QR code with half-block characters, light modules
// in the foreground colour, for dark terminals.
func qrLines(text string) ([]string, error) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return nil, fmt.Errorf("encode QR: %w", err)
	}
	const quiet = 2 // light border, in modules
	n := code.Size + 2*quiet
	light := func(x, y int) bool { return y < n && !code.Black(x-quiet, y-quiet) }
	var lines []string
	for y := 0; y < n; y += 2 {
		var b strings.Builder
		for x := range n {
			switch top, bottom := light(x, y), light(x, y+1); {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		lines = append(lines, b.String())
	}
	return lines, nil
}

// runInbox lists the screenshots uploaded from the laptop, newest first,
// after removing those older than a week.
func runInbox(l layout.Layout, out ui.Printer) error {
	if _, err := inbox.Clean(l.Inbox(), inbox.Keep, time.Now()); err != nil {
		out.Warn("Couldn't remove old screenshots: %v", err)
	}
	files, err := inbox.List(l.Inbox())
	if err != nil {
		return err
	}
	if len(files) == 0 {
		out.Text("No screenshots in %s.", l.Pretty(l.Inbox()))
		out.Hint("on the laptop: copy a screenshot, press Ctrl+Alt+V, then paste the path into Claude")
		return nil
	}
	out.Title("Screenshots from the laptop, newest first (kept 7 days)")
	now := time.Now()
	for _, f := range files {
		out.Text("  %s  %s", out.Dim(fmt.Sprintf("%-8s", age(now.Sub(f.ModTime)))), f.Path)
	}
	return nil
}

func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// shellQuote quotes s for a POSIX shell, so a suggested command can be pasted
// as is ($ and backticks stay literal).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
