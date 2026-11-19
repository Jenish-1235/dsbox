package update

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestCheckForUpdates(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	v1 := f.git(f.l.Src(), "rev-parse", "HEAD")

	st, err := CheckForUpdates(ctx, f.l, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.NewCommits != 0 || st.Head != v1 || st.TargetCommit != v1 || st.Target != "main" || st.FetchError != "" || st.FetchedAt.IsZero() {
		t.Errorf("CheckForUpdates() on the latest = %+v, want 0 new on main and a fetch time", st)
	}

	for i := range 12 {
		f.commit(fmt.Sprintf("v%d", i+2), fmt.Sprintf("change %d", i+1))
	}
	st, err = CheckForUpdates(ctx, f.l, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.NewCommits != 12 || len(st.Subjects) != maxSubjects || st.Subjects[0] != "change 12" {
		t.Errorf("CheckForUpdates() = %d new, subjects %q; want 12 new, the newest %d subjects", st.NewCommits, st.Subjects, maxSubjects)
	}
	saved, ok, err := LoadStatus(f.l.UpdatesFile())
	if err != nil || !ok || saved.NewCommits != 12 || saved.TargetCommit != st.TargetCommit {
		t.Errorf("LoadStatus() = %+v, %v, %v; want the saved status", saved, ok, err)
	}

	// Updating refreshes the cache, so the shell stops announcing it.
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if saved, _, _ = LoadStatus(f.l.UpdatesFile()); saved.NewCommits != 0 || saved.Head != saved.TargetCommit {
		t.Errorf("after Update, status = %+v, want 0 new", saved)
	}
}

func TestCheckForUpdatesRecordsFetchFailure(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	first, err := CheckForUpdates(ctx, f.l, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.git(f.l.Src(), "remote", "set-url", "origin", f.origin+"-missing")
	st, err := CheckForUpdates(ctx, f.l, time.Minute)
	if err != nil {
		t.Fatalf("CheckForUpdates() with a broken remote = %v, want the failure recorded instead", err)
	}
	if st.FetchError == "" || !st.FetchedAt.Equal(first.FetchedAt) || st.Target != "main" {
		t.Errorf("status = %+v, want a fetch error, the earlier fetch time %v, and a comparison", st, first.FetchedAt)
	}
}

// A rollback doesn't fetch, so it keeps a fetch error the last check found;
// an update fetches, so a successful one clears it.
func TestFetchErrorSurvivesRollbackNotUpdate(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatal(err)
	}
	f.commit("v2", "second")
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatal(err)
	}

	f.git(f.l.Src(), "remote", "set-url", "origin", f.origin+"-missing")
	failed, err := CheckForUpdates(ctx, f.l, time.Minute)
	if err != nil || failed.FetchError == "" {
		t.Fatalf("CheckForUpdates() with a broken remote = %+v, %v; want a fetch error", failed, err)
	}
	if err := f.u.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	st, _, _ := LoadStatus(f.l.UpdatesFile())
	if st.FetchError != failed.FetchError || !st.FetchedAt.Equal(failed.FetchedAt) {
		t.Errorf("after Rollback, status = %+v; want the fetch error %q and fetch time %v kept", st, failed.FetchError, failed.FetchedAt)
	}
	if st.Fresh(st.Head, time.Now()) {
		t.Error("after Rollback, the status counts as fresh despite the fetch error")
	}

	f.git(f.l.Src(), "remote", "set-url", "origin", f.origin)
	if err := f.u.Update(ctx, ""); err != nil {
		t.Fatal(err)
	}
	st, _, _ = LoadStatus(f.l.UpdatesFile())
	if st.FetchError != "" || !st.FetchedAt.After(failed.FetchedAt) {
		t.Errorf("after Update, status = %+v; want no fetch error and a new fetch time", st)
	}
}

func TestFresh(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	base := Status{CheckedAt: now.Add(-2 * time.Hour), Head: "abc"}
	failed := base
	failed.FetchError = "no network"
	tests := []struct {
		name string
		s    Status
		head string
		now  time.Time
		want bool
	}{
		{"recent", base, "abc", now, true},
		{"a day old", base, "abc", now.Add(23 * time.Hour), false},
		{"moved since", base, "def", now, false},
		{"fetch failed", failed, "abc", now, false},
	}
	for _, tt := range tests {
		if got := tt.s.Fresh(tt.head, tt.now); got != tt.want {
			t.Errorf("%s: Fresh() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestNotices(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Hour)
	tests := []struct {
		name    string
		s       Status
		current string
		want    []Notice
	}{
		{"never checked", Status{}, "", nil},
		{"up to date", Status{CheckedAt: recent, Head: "a", Target: "main"}, "", nil},
		{"update on main", Status{CheckedAt: recent, Head: "a", Target: "main", NewCommits: 3}, "a",
			[]Notice{{Update: true, Text: "devbox update available (3 changes) → ds update"}}},
		{"one change", Status{CheckedAt: recent, Head: "a", Target: "main", NewCommits: 1}, "",
			[]Notice{{Update: true, Text: "devbox update available (1 change) → ds update"}}},
		{"release", Status{CheckedAt: recent, Head: "a", Target: "v1.2.0", NewCommits: 4}, "",
			[]Notice{{Update: true, Text: "devbox v1.2.0 available (4 changes) → ds update"}}},
		{"cache from another commit", Status{CheckedAt: recent, Head: "a", Target: "main", NewCommits: 3}, "b", nil},
		{"recent fetch failure is quiet", Status{CheckedAt: recent, Head: "a", Target: "main", FetchError: "x", FetchedAt: now.Add(-24 * time.Hour)}, "", nil},
		{"fetch failing for days", Status{CheckedAt: recent, Head: "a", Target: "main", NewCommits: 2, FetchError: "x", FetchedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)}, "",
			[]Notice{
				{Update: true, Text: "devbox update available (2 changes) → ds update"},
				{Text: "devbox can't reach its repo to check for updates since 10 Nov → ds doctor"},
			}},
		{"never fetched", Status{CheckedAt: recent, Head: "a", Target: "main", FetchError: "x"}, "",
			[]Notice{{Text: "devbox can't reach its repo to check for updates → ds doctor"}}},
		{"timer stopped", Status{CheckedAt: time.Date(2026, 11, 12, 12, 0, 0, 0, time.Local), Head: "a", Target: "main"}, "",
			[]Notice{{Text: "devbox hasn't checked for updates since 12 Nov → ds doctor"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Notices(tt.s, tt.current, now); !slices.Equal(got, tt.want) {
				t.Errorf("Notices() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
