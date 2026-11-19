package doctor

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jenish-1235/dsbox/internal/layout"
)

const earlyoomLog = `2026-11-19T08:00:00+0000 devbox earlyoom[612]: earlyoom 1.7
2026-11-19T10:02:03+0000 devbox earlyoom[612]: low memory! at or below SIGTERM limits: mem  4.00%, swap 10.00%
2026-11-19T10:02:03+0000 devbox earlyoom[612]: sending SIGTERM to process 4242 uid 1000 "next-server (v1": badness 950, VmRSS 6120 MiB
2026-11-19T10:02:04+0000 devbox earlyoom[612]: sending SIGKILL to process 4242 uid 1000 "next-server (v1": badness 950, VmRSS 6120 MiB
2026-11-19T17:00:00+05:30 devbox earlyoom[612]: sending SIGTERM to process 5151 uid 1000 "node": badness 900, VmRSS 4000 MiB
2026-11-19T11:30:01+0000 devbox earlyoom[612]: process exited after 0.4 seconds
`

func TestParseKills(t *testing.T) {
	got := parseKills(earlyoomLog)
	want := []oomKill{
		{At: time.Date(2026, 11, 19, 10, 2, 3, 0, time.UTC), PID: 4242, Process: "next-server (v1"},
		{At: time.Date(2026, 11, 19, 11, 30, 0, 0, time.UTC), PID: 5151, Process: "node"},
	}
	if !slices.EqualFunc(got, want, func(a, b oomKill) bool {
		return a.At.Equal(b.At) && a.PID == b.PID && a.Process == b.Process
	}) {
		t.Errorf("parseKills() = %+v, want %+v", got, want)
	}
	if got := parseKills("-- No entries --\n"); len(got) != 0 {
		t.Errorf("parseKills(no entries) = %+v, want none", got)
	}
}

func TestJudgeMemoryGuard(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		readable   bool
		log        string
		wantLevel  Level
		wantDetail string
	}{
		{"quiet", true, "", OK, "earlyoom on, nothing stopped in 24h"},
		{"stopped two", true, earlyoomLog, Warn, "earlyoom stopped node (pid 5151) 30m ago to free memory, and 1 more in 24h"},
		{"log unreadable", false, "", OK, "earlyoom on; can't read its log"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := judgeMemoryGuard(tt.readable, parseKills(tt.log), now)
			if got.Level != tt.wantLevel || got.Detail != tt.wantDetail {
				t.Errorf("judgeMemoryGuard() = %v %q, want %v %q", got.Level, got.Detail, tt.wantLevel, tt.wantDetail)
			}
			if got.Level != OK && got.Fix == "" {
				t.Error("judgeMemoryGuard() gave no fix for a warning")
			}
		})
	}
}

func TestHTTPSResult(t *testing.T) {
	home := t.TempDir()
	l := layout.Layout{Home: home}
	if r := httpsResult(l, filepath.Join(home, "missing.crt")); r.Level != Warn {
		t.Errorf("httpsResult(missing CA) = %v, want Warn", r.Level)
	}
	if r := httpsResult(l, home); r.Level != OK || !strings.Contains(r.Detail, "ds cert") {
		t.Errorf("httpsResult(existing CA) = %v %q, want OK mentioning ds cert", r.Level, r.Detail)
	}
}
