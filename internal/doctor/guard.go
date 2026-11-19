package doctor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jenish-1235/dsbox/internal/layout"
)

// httpsResult says whether Caddy's local CA exists, which https links need.
func httpsResult(l layout.Layout, caFile string) Result {
	if _, err := os.Stat(caFile); err != nil {
		return Result{Name: "https certificates", Level: Warn, Detail: "Caddy hasn't made its CA yet",
			Fix: "ds update (rewrites the routes), then journalctl -u devbox-web -n 20"}
	}
	return Result{Name: "https certificates", Level: OK, Detail: "devbox CA at " + l.Pretty(caFile) + " (ds cert)"}
}

const memguardFix = "sudo ~/.devbox/src/provision/provision.sh --only memguard"

// memoryGuardResult checks that earlyoom runs and reports what it stopped in
// the last 24 hours.
func memoryGuardResult(ctx context.Context) Result {
	if exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "earlyoom").Run() != nil {
		return Result{Name: "memory guard", Level: Warn, Detail: "earlyoom isn't running", Fix: memguardFix}
	}
	cmd := exec.CommandContext(ctx, "journalctl", "-u", "earlyoom", "--since", "24 hours ago", "-o", "short-iso", "--no-pager")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	// Without the systemd-journal group, journalctl shows only their own logs
	// and says so on stderr.
	readable := err == nil && !strings.Contains(stderr.String(), "not seeing messages")
	return judgeMemoryGuard(readable, parseKills(stdout.String()), time.Now())
}

func judgeMemoryGuard(readable bool, kills []oomKill, now time.Time) Result {
	r := Result{Name: "memory guard", Level: OK}
	switch {
	case !readable:
		r.Detail = "earlyoom on; can't read its log"
		r.Fix = memguardFix + " (adds you to systemd-journal), then log in again"
	case len(kills) == 0:
		r.Detail = "earlyoom on, nothing stopped in 24h"
	default:
		last := kills[len(kills)-1]
		r.Level = Warn
		r.Detail = fmt.Sprintf("earlyoom stopped %s (pid %d) %s ago to free memory", last.Process, last.PID, short(now.Sub(last.At)))
		if n := len(kills) - 1; n > 0 {
			r.Detail += fmt.Sprintf(", and %d more in 24h", n)
		}
		r.Fix = "ds restore starts stopped projects again; journalctl -u earlyoom --since -24h shows what used the memory"
	}
	return r
}

func short(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

// parseStamp reads a short-iso timestamp; systemd 255 writes the offset as
// +05:30, older versions as +0530.
func parseStamp(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// oomKill is one process earlyoom signalled.
type oomKill struct {
	At      time.Time
	PID     int
	Process string
}

// parseKills reads journalctl -o short-iso output of earlyoom for lines like
//
//	2026-11-19T10:02:03+0000 devbox earlyoom[612]: sending SIGTERM to process 4242 uid 1000 "node": badness 950, VmRSS 6120 MiB
//
// in time order, once per process (a SIGKILL after a SIGTERM isn't counted again).
func parseKills(journal string) []oomKill {
	var kills []oomKill
	for line := range strings.Lines(journal) {
		stamp, _, _ := strings.Cut(line, " ")
		_, rest, ok := strings.Cut(line, "sending SIG")
		if !ok {
			continue
		}
		_, rest, ok = strings.Cut(rest, " to process ")
		if !ok {
			continue
		}
		pidText, rest, _ := strings.Cut(rest, " ")
		pid, err := strconv.Atoi(pidText)
		if err != nil {
			continue
		}
		_, rest, ok = strings.Cut(rest, `"`)
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, `"`)
		if slices.ContainsFunc(kills, func(k oomKill) bool { return k.PID == pid }) {
			continue
		}
		kills = append(kills, oomKill{At: parseStamp(stamp), PID: pid, Process: name})
	}
	return kills
}
