package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jenish-1235/dsbox/internal/component"
	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/update"
)

func TestJudgeUpdateChecks(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return fmt.Sprintf("@%d", now.Add(-d).Unix()) }
	timer := func(lastTrigger string) map[string]string {
		return map[string]string{"LoadState": "loaded", "UnitFileState": "enabled", "LastTriggerUSec": lastTrigger}
	}
	ok := map[string]string{"Result": "success"}
	fresh := update.Status{CheckedAt: now.Add(-time.Hour)}
	tests := []struct {
		name       string
		timer      map[string]string
		service    map[string]string
		st         update.Status
		wantLevel  Level
		wantDetail string
	}{
		{"healthy", timer(at(3 * time.Hour)), ok, fresh, OK, "daily, last ran 3h ago"},
		{"not installed", map[string]string{"LoadState": "not-found"}, ok, fresh, Warn, "the daily timer isn't installed"},
		{"disabled", map[string]string{"LoadState": "loaded", "UnitFileState": "disabled"}, ok, fresh, Warn, "the daily timer is off"},
		{"last run failed", timer(at(3 * time.Hour)), map[string]string{"Result": "exit-code"}, fresh, Warn, "the last check failed (exit-code)"},
		{"never ran", timer("n/a"), ok, update.Status{}, Warn, "hasn't run yet"},
		{"fetch failing", timer(at(3 * time.Hour)), ok, update.Status{CheckedAt: fresh.CheckedAt, FetchError: "no route"}, Warn, "can't reach the repo: no route"},
		{"stale", timer(at(5 * 24 * time.Hour)), ok, update.Status{CheckedAt: now.Add(-5 * 24 * time.Hour)}, Warn, "last ran 5 days ago"},
		// ds status rewrites updates.json, so it being recent says nothing about the timer.
		{"timer stopped firing, file kept recent by ds status", timer(at(5 * 24 * time.Hour)), ok, fresh, Warn, "last ran 5 days ago"},
		{"started by hand since the timer last fired", timer(at(5 * 24 * time.Hour)),
			map[string]string{"Result": "success", "ExecMainStartTimestamp": at(2 * time.Hour)}, fresh, OK, "daily, last ran 2h ago"},
		{"started by hand, timer never fired", timer("n/a"),
			map[string]string{"Result": "success", "ExecMainStartTimestamp": at(time.Hour)}, fresh, OK, "daily, last ran 1h ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := judgeUpdateChecks(tt.timer, tt.service, tt.st, now)
			if got.Level != tt.wantLevel || got.Detail != tt.wantDetail {
				t.Errorf("judgeUpdateChecks() = %v %q, want %v %q", got.Level, got.Detail, tt.wantLevel, tt.wantDetail)
			}
			if got.Level != OK && got.Fix == "" {
				t.Error("judgeUpdateChecks() gave no fix for a warning")
			}
		})
	}
}

func TestLogins(t *testing.T) {
	tests := []struct {
		name     string
		forge    string
		mirrors  bool
		signedIn bool
		want     []string // "name level detail" per login
	}{
		{"github signed in", "github", false, true, []string{"github (gh) 0 signed in"}},
		{"github signed out", "github", false, false, []string{"github (gh) 1 signed out"}},
		{"github ignores old mirrors", "github", true, false, []string{"github (gh) 1 signed out"}},
		{"gitlab without mirrors", "gitlab", false, false, []string{
			"gitlab (glab) 1 signed out",
			"github (gh) 0 signed out; only needed for GitHub mirrors (ds publish --github)",
		}},
		{"gitlab with mirrors", "gitlab", true, false, []string{"gitlab (glab) 1 signed out", "github (gh) 1 signed out"}},
		{"gitlab signed in", "gitlab", false, true, []string{"gitlab (glab) 0 signed in", "github (gh) 0 signed in"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, lg := range logins(tt.forge, tt.mirrors) {
				r := judgeLogin(lg, tt.signedIn)
				got = append(got, fmt.Sprintf("%s %d %s", r.Name, r.Level, r.Detail))
				if r.Level == Warn && r.Fix != lg.fix {
					t.Errorf("%s: Fix = %q, want %q", r.Name, r.Fix, lg.fix)
				}
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("logins(%q, %v) judged %v = %q, want %q", tt.forge, tt.mirrors, tt.signedIn, got, tt.want)
			}
		})
	}
}

func TestSystemdTime(t *testing.T) {
	want := time.Date(2026, 11, 18, 3, 12, 0, 0, time.UTC)
	tests := []struct {
		in   string
		want time.Time
	}{
		{"Wed 2026-11-18 03:12:00 UTC", want},
		{"Wed 2026-11-18 08:42:00 +0530", want},
		{fmt.Sprintf("@%d", want.Unix()), want},
		{"n/a", time.Time{}},
		{"", time.Time{}},
		{"@soon", time.Time{}},
	}
	for _, tt := range tests {
		if got := systemdTime(tt.in); !got.Equal(tt.want) {
			t.Errorf("systemdTime(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestProperties(t *testing.T) {
	got := properties("LoadState=loaded\nUnitFileState=enabled\n")
	if got["LoadState"] != "loaded" || got["UnitFileState"] != "enabled" || len(got) != 2 {
		t.Errorf("properties() = %v", got)
	}
}

func TestJudgeTailscale(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	expiring := `{"BackendState":"Running","Self":{"KeyExpiry":"2026-11-21T12:00:00Z"}}`
	tests := []struct {
		name      string
		out       string
		err       error
		host      string
		wantLevel Level
		wantFix   string
	}{
		{"down on the devbox", "", errors.New("exit status 1"), "box2", Fail, "sudo tailscale up --ssh --hostname=box2"},
		{"logged out on the devbox", `{"BackendState":"NeedsLogin"}`, nil, "box2", Fail, "sudo tailscale up --ssh --hostname=box2"},
		{"down on a Mac: not renamed", "", errors.New("exit status 1"), "", Fail, "tailscale up"},
		{"key expiring on the devbox", expiring, nil, "box2", Fail, "Tailscale admin console → Machines → box2 → Disable key expiry"},
		{"key expiring on a Mac", expiring, nil, "", Fail, "Tailscale admin console → Machines → this machine → Disable key expiry"},
		{"healthy", `{"BackendState":"Running","Self":{}}`, nil, "box2", OK, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := judgeTailscale(tt.out, tt.err, tt.host, now)
			if got.Level != tt.wantLevel || got.Fix != tt.wantFix {
				t.Errorf("judgeTailscale() = %v fix %q, want %v fix %q", got.Level, got.Fix, tt.wantLevel, tt.wantFix)
			}
		})
	}
}

func TestStateResult(t *testing.T) {
	home := t.TempDir()
	l := layout.Layout{Home: home, Root: filepath.Join(home, ".devbox")}
	if got := stateResult(l); got.Level != OK {
		t.Errorf("stateResult() with no ds.json = %v %q, want OK (a fresh machine)", got.Level, got.Detail)
	}
	if err := os.MkdirAll(filepath.Dir(l.StateFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.StateFile(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := stateResult(l)
	if got.Level != Fail || !strings.Contains(got.Detail, "parse state") || !strings.Contains(got.Fix, "~/.devbox/state/ds.json") {
		t.Errorf("stateResult() with a broken ds.json = %v %q fix %q, want a failure naming the file", got.Level, got.Detail, got.Fix)
	}
}

func TestComponentSection(t *testing.T) {
	home := t.TempDir()
	l := layout.Layout{Home: home, Root: filepath.Join(home, ".devbox")}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rc"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".rc"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := component.Component{Name: "t", Dir: dir,
		Files: []component.File{{Src: "rc", Dest: "~/.rc"}},
		Checks: []component.Check{
			{Name: "sees the user bus", Run: `[ "$XDG_RUNTIME_DIR" = /run/user/7 ]`, Fix: "fix env"},
			{Name: "hangs", Run: "sleep 30", Fix: "fix hang"},
		}}
	env := append(os.Environ(), "XDG_RUNTIME_DIR=/run/user/7")
	defer func(d time.Duration) { checkTimeout = d }(checkTimeout)
	checkTimeout = 200 * time.Millisecond

	byName := func(s Section) map[string]Result {
		m := map[string]Result{}
		for _, r := range s.Results {
			m[r.Name] = r
		}
		return m
	}
	start := time.Now()
	got := byName(componentSection(t.Context(), l, c, map[string]string{}, env))
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("componentSection() took %v; a hung check should time out", took)
	}
	if r := got["sees the user bus"]; r.Level != OK {
		t.Errorf("check run without the given env: %+v", r)
	}
	if r := got["hangs"]; r.Level != Warn || !strings.Contains(r.Detail, "no answer within") || r.Fix != "fix hang" {
		t.Errorf("hung check = %+v, want a timeout warning with its fix", r)
	}
	if r, ok := got["~/.rc"]; !ok || r.Level != Warn {
		t.Errorf("file result = %+v (present %v), want a warning", r, ok)
	}

	// With ds.json unreadable, edited and outdated files can't be told apart,
	// so files aren't judged at all.
	if r, ok := byName(componentSection(t.Context(), l, c, nil, env))["~/.rc"]; ok {
		t.Errorf("unknown state still judged the file: %+v", r)
	}
}
