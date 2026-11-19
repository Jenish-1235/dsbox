package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Jenish-1235/dsbox/internal/layout"
)

// maxSubjects caps how many new commit subjects the status keeps.
const maxSubjects = 10

// Status is the result of the last check for updates, cached in
// ~/.devbox/state/updates.json so the shell and ds can show it without
// touching the network.
type Status struct {
	// CheckedAt is when the check ran.
	CheckedAt time.Time `json:"checked_at"`
	// Target is what the channel points at: "main" or a release tag.
	Target       string `json:"target"`
	TargetCommit string `json:"target_commit"`
	// Head is the commit that was checked out when the check ran.
	Head string `json:"head"`
	// NewCommits is how many commits Target has that Head doesn't.
	NewCommits int `json:"new_commits"`
	// Subjects are the newest new commits' subjects, at most maxSubjects.
	Subjects []string `json:"subjects,omitempty"`
	// FetchError is why fetching failed, if it did; the comparison then used
	// what was fetched before.
	FetchError string `json:"fetch_error,omitempty"`
	// FetchedAt is the last time fetching worked.
	FetchedAt time.Time `json:"fetched_at,omitzero"`
}

// LoadStatus reads the cached status. ok is false when there is none yet.
func LoadStatus(path string) (s Status, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, fmt.Errorf("read update status: %w", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, false, fmt.Errorf("parse update status %s: %w", path, err)
	}
	return s, true, nil
}

// Save writes the status atomically.
func (s Status) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update status: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state folder: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write update status: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("save update status: %w", err)
	}
	return nil
}

// CheckForUpdates compares HEAD with what the channel points at, saves the
// result, and returns it. With fetchTimeout > 0 it fetches first; a failed
// fetch is recorded in the status, not returned as an error. Without fetching
// it keeps the last fetch's outcome, so an earlier failure still shows.
func CheckForUpdates(ctx context.Context, l layout.Layout, fetchTimeout time.Duration) (Status, error) {
	src := l.Src()
	path := l.UpdatesFile()
	prev, _, _ := LoadStatus(path) // a broken cache is simply replaced
	s := Status{CheckedAt: time.Now().UTC(), FetchedAt: prev.FetchedAt}
	if fetchTimeout > 0 {
		fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
		_, err := git(fctx, src, "fetch", "--quiet", "--tags", "--prune", "origin")
		cancel()
		if err != nil {
			s.FetchError = firstLine(err.Error())
		} else {
			s.FetchedAt = s.CheckedAt
		}
	} else {
		s.FetchError = prev.FetchError
	}
	target, label, err := Resolve(ctx, src, "")
	if err != nil {
		return Status{}, err
	}
	head, err := git(ctx, src, "rev-parse", "HEAD")
	if err != nil {
		return Status{}, err
	}
	s.Target, s.TargetCommit, s.Head = label, target, head
	if head != target {
		count, err := git(ctx, src, "rev-list", "--count", head+".."+target)
		if err != nil {
			return Status{}, err
		}
		if s.NewCommits, err = strconv.Atoi(count); err != nil {
			return Status{}, fmt.Errorf("count new commits: %w", err)
		}
		if s.NewCommits > 0 {
			subjects, err := git(ctx, src, "log", "--no-decorate", "--format=%s",
				"--max-count="+strconv.Itoa(maxSubjects), head+".."+target)
			if err != nil {
				return Status{}, err
			}
			s.Subjects = strings.Split(subjects, "\n")
		}
	}
	return s, s.Save(path)
}

// Fresh reports whether s can stand in for a new check: it is from the last
// day, its fetch worked, and head is still what was checked out then.
func (s Status) Fresh(head string, now time.Time) bool {
	return s.FetchError == "" && s.Head == head && now.Sub(s.CheckedAt) < 24*time.Hour
}

// Notice is one line for the start of a shell.
type Notice struct {
	// Update is true for "an update is ready", false for a warning.
	Update bool
	Text   string
}

// Notices turns the cached status into at most two short lines. current is
// the commit ds last moved to (state.Current; may be empty); a status taken
// at another commit is out of date and says nothing.
func Notices(s Status, current string, now time.Time) []Notice {
	if s.CheckedAt.IsZero() {
		return nil
	}
	var out []Notice
	if s.NewCommits > 0 && (current == "" || current == s.Head) {
		changes := "1 change"
		if s.NewCommits > 1 {
			changes = fmt.Sprintf("%d changes", s.NewCommits)
		}
		what := "devbox update"
		if s.Target != "main" {
			what = "devbox " + s.Target
		}
		out = append(out, Notice{Update: true, Text: fmt.Sprintf("%s available (%s) → ds update", what, changes)})
	}
	const stale = 3 * 24 * time.Hour
	switch {
	case s.FetchError != "" && (s.FetchedAt.IsZero() || now.Sub(s.FetchedAt) > stale):
		since := ""
		if !s.FetchedAt.IsZero() {
			since = " since " + s.FetchedAt.Local().Format("2 Jan")
		}
		out = append(out, Notice{Text: "devbox can't reach its repo to check for updates" + since + " → ds doctor"})
	case now.Sub(s.CheckedAt) > stale:
		out = append(out, Notice{Text: "devbox hasn't checked for updates since " + s.CheckedAt.Local().Format("2 Jan") + " → ds doctor"})
	}
	return out
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
