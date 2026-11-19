// Package config reads and checks the machine settings in
// /etc/devbox/devbox.env, which provision.sh installs from the repo's
// config/devbox.env.
//
// The format is plain KEY=value lines with # comments. Values are kept plain
// (no quotes, spaces or $) so that systemd's EnvironmentFile= and the shell's
// "set -a; . file" read exactly the same thing.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Setting names.
const (
	Host        = "DEVBOX_HOST"
	User        = "DEVBOX_USER"
	ProjectsDir = "DEVBOX_PROJECTS_DIR"
	Domain      = "DEVBOX_DOMAIN"
	AppsDomain  = "DEVBOX_APPS_DOMAIN"
	AppsTLS     = "DEVBOX_APPS_TLS"
	PortMin     = "DEVBOX_PORT_MIN"
	PortMax     = "DEVBOX_PORT_MAX"
	DozzlePort  = "DEVBOX_DOZZLE_PORT"
	Channel     = "DEVBOX_CHANNEL"
	Forge       = "DEVBOX_FORGE"
)

// Fix is how to install the repo's settings on the machine.
const Fix = "sudo ~/.devbox/src/provision/provision.sh --only config"

// Keys returns every known setting, in the order ds config shows them.
func Keys() []string {
	return []string{Host, User, ProjectsDir, Domain, AppsDomain, AppsTLS, PortMin, PortMax, DozzlePort, Channel, Forge}
}

// AppsTLS values: off serves project links over http only; internal also
// serves https with certificates from Caddy's own local CA.
const (
	TLSOff      = "off"
	TLSInternal = "internal"
)

// Forge values: where ds publish puts a project that has no origin yet.
// github pushes straight to a GitHub repo; gitlab pushes to a GitLab project
// and can mirror it to GitHub.
const (
	ForgeGitHub = "github"
	ForgeGitLab = "gitlab"
)

// optional settings may be empty. An empty AppsTLS means off, and an empty
// Forge means github, as on machines set up before those settings existed.
func optional(key string) bool {
	return key == Domain || key == AppsDomain || key == User || key == ProjectsDir || key == AppsTLS || key == Forge
}

func lower(r rune) bool { return r >= 'a' && r <= 'z' }
func upper(r rune) bool { return r >= 'A' && r <= 'Z' }
func digit(r rune) bool { return r >= '0' && r <= '9' }

// validKey: UPPER_CASE letters, digits and _, not starting with a digit.
func validKey(k string) bool {
	return k != "" && !digit(rune(k[0])) && !strings.ContainsFunc(k, func(r rune) bool {
		return !upper(r) && !digit(r) && r != '_'
	})
}

// plainValue: nothing the shell or systemd would read differently.
func plainValue(v string) bool {
	return !strings.ContainsFunc(v, func(r rune) bool {
		return !lower(r) && !upper(r) && !digit(r) && !strings.ContainsRune("._/:@,+=-", r)
	})
}

// validLabel: one DNS label or host name, lowercase.
func validLabel(l string) bool {
	return len(l) >= 1 && len(l) <= 63 && l[0] != '-' && l[len(l)-1] != '-' &&
		!strings.ContainsFunc(l, func(r rune) bool { return !lower(r) && !digit(r) && r != '-' })
}

// validUser: a Linux user name.
func validUser(u string) bool {
	return len(u) >= 1 && len(u) <= 32 && (lower(rune(u[0])) || u[0] == '_') &&
		!strings.ContainsFunc(u, func(r rune) bool { return !lower(r) && !digit(r) && r != '_' && r != '-' })
}

// Entry is one KEY=value line.
type Entry struct {
	Key   string
	Value string
	Line  int
}

// Parse reads the env format: KEY=value lines, # comments and blank lines.
// Nothing is expanded. It reports every malformed line, with its number.
func Parse(data []byte) ([]Entry, error) {
	var entries []Entry
	var errs []error
	seen := map[string]int{}
	for i, raw := range strings.Split(string(data), "\n") {
		n := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("line %d: expected KEY=value, got %q", n, line))
			continue
		case strings.HasPrefix(key, "export "):
			errs = append(errs, fmt.Errorf("line %d: drop the \"export\"; write %s=value", n, strings.TrimSpace(strings.TrimPrefix(key, "export "))))
			continue
		case !validKey(key):
			errs = append(errs, fmt.Errorf("line %d: %q isn't a valid name; use UPPER_CASE letters, digits and _", n, key))
			continue
		case !plainValue(value):
			errs = append(errs, fmt.Errorf("line %d: the value of %s must be plain: no quotes, spaces, $, ~ or comments after it", n, key))
			continue
		}
		if first, dup := seen[key]; dup {
			errs = append(errs, fmt.Errorf("line %d: %s is already set on line %d", n, key, first))
			continue
		}
		seen[key] = n
		entries = append(entries, Entry{Key: key, Value: value, Line: n})
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return entries, nil
}

// Settings are the machine settings, typed.
type Settings struct {
	Host        string
	User        string
	ProjectsDir string
	Domain      string
	AppsDomain  string
	AppsTLS     string
	PortMin     int
	PortMax     int
	DozzlePort  int
	Channel     string
	Forge       string
}

// Problem is a setting that is missing, unknown or invalid. Line is 0 when the
// setting isn't in the file.
type Problem struct {
	Key  string
	Line int
	Msg  string
}

func (p Problem) String() string {
	if p.Line == 0 {
		return p.Key + ": " + p.Msg
	}
	return fmt.Sprintf("%s (line %d): %s", p.Key, p.Line, p.Msg)
}

// Validate checks entries and returns the settings it could read, plus every
// problem found. No problems means the settings are complete and valid.
func Validate(entries []Entry) (Settings, []Problem) {
	var s Settings
	var probs []Problem
	byKey := map[string]Entry{}
	for _, e := range entries {
		if !slices.Contains(Keys(), e.Key) {
			probs = append(probs, Problem{e.Key, e.Line, "unknown setting (a typo?)"})
			continue
		}
		byKey[e.Key] = e
	}
	bad := func(e Entry, format string, a ...any) {
		probs = append(probs, Problem{e.Key, e.Line, fmt.Sprintf(format, a...)})
	}
	port := func(e Entry, lowest int) int {
		n, err := strconv.Atoi(e.Value)
		if err != nil || n < lowest || n > 65535 {
			bad(e, "must be a port number from %d to 65535, not %q", lowest, e.Value)
			return 0
		}
		return n
	}
	for _, key := range Keys() {
		e, ok := byKey[key]
		if !ok || e.Value == "" {
			if !optional(key) {
				probs = append(probs, Problem{Key: key, Line: e.Line, Msg: "missing; it needs a value"})
			}
			continue
		}
		switch key {
		case Host:
			if !validLabel(e.Value) {
				bad(e, "must be a host name: lowercase letters, digits and -")
			}
			s.Host = e.Value
		case User:
			if !validUser(e.Value) {
				bad(e, "must be a Linux user name")
			}
			s.User = e.Value
		case ProjectsDir:
			if !path.IsAbs(e.Value) || path.Clean(e.Value) != e.Value {
				bad(e, "must be an absolute path such as /home/<user>/projects")
			}
			s.ProjectsDir = e.Value
		case Domain:
			if !validDomain(e.Value) {
				bad(e, "%q isn't a domain name such as dev.example.com", e.Value)
			}
			s.Domain = e.Value
		case AppsDomain:
			// A single word (e.g. devbox) is an internal name served over Tailscale.
			if !validDomain(e.Value) && !validLabel(e.Value) {
				bad(e, "%q isn't a name such as devbox or apps.example.com", e.Value)
			}
			s.AppsDomain = e.Value
		case AppsTLS:
			if e.Value != TLSOff && e.Value != TLSInternal {
				bad(e, "must be off or internal, not %q", e.Value)
			}
			s.AppsTLS = e.Value
		case PortMin:
			s.PortMin = port(e, 1024)
		case PortMax:
			s.PortMax = port(e, 1024)
		case DozzlePort:
			s.DozzlePort = port(e, 1)
		case Channel:
			if e.Value != "main" && e.Value != "release" {
				bad(e, "must be main or release, not %q", e.Value)
			}
			s.Channel = e.Value
		case Forge:
			if e.Value != ForgeGitHub && e.Value != ForgeGitLab {
				bad(e, "must be github or gitlab, not %q", e.Value)
			}
			s.Forge = e.Value
		}
	}
	if s.PortMin != 0 && s.PortMax != 0 && s.PortMin >= s.PortMax {
		bad(byKey[PortMin], "must be below %s (%d)", PortMax, s.PortMax)
	}
	if s.DozzlePort != 0 && s.PortMin != 0 && s.PortMax != 0 && s.DozzlePort >= s.PortMin && s.DozzlePort <= s.PortMax {
		bad(byKey[DozzlePort], "is inside the app port range %d–%d; pick a port outside it", s.PortMin, s.PortMax)
	}
	// File order reads best; settings missing from the file go last.
	slices.SortStableFunc(probs, func(a, b Problem) int {
		switch {
		case a.Line == b.Line:
			return 0
		case a.Line == 0:
			return 1
		case b.Line == 0:
			return -1
		}
		return a.Line - b.Line
	})
	return s, probs
}

// validDomain accepts lowercase DNS names with at least two labels.
func validDomain(d string) bool {
	if len(d) > 253 {
		return false
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !validLabel(l) {
			return false
		}
	}
	_, err := strconv.Atoi(labels[len(labels)-1])
	return err != nil // the top-level label can't be all digits
}

// Report is what ds config and ds doctor show about the machine file.
type Report struct {
	// Missing means the machine file doesn't exist yet.
	Missing bool
	// Err is a read error or the syntax errors in the machine file.
	Err      error
	Entries  []Entry
	Settings Settings
	Problems []Problem
	// Differs means the machine file isn't byte-for-byte the repo's copy;
	// Changes lists the settings that differ ("comments only" when none do).
	Differs bool
	Changes []string
	// RepoErr is a problem reading or parsing the repo's copy.
	RepoErr error
}

// OK reports whether the machine file exists, is valid and matches the repo.
func (r Report) OK() bool {
	return !r.Missing && r.Err == nil && len(r.Problems) == 0 && !r.Differs && r.RepoErr == nil
}

// Inspect reads the machine file at machine, validates it, and compares it
// with the repo's copy at repo.
func Inspect(machine, repo string) Report {
	var r Report
	data, err := os.ReadFile(machine)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.Missing = true
	case err != nil:
		r.Err = fmt.Errorf("read %s: %w", machine, err)
	default:
		if r.Entries, err = Parse(data); err != nil {
			r.Err = err
		} else {
			r.Settings, r.Problems = Validate(r.Entries)
		}
	}
	want, err := os.ReadFile(repo)
	if err != nil {
		r.RepoErr = fmt.Errorf("read %s: %w", repo, err)
		return r
	}
	if r.Missing || bytes.Equal(data, want) {
		return r
	}
	r.Differs = true
	wantEntries, err := Parse(want)
	if err != nil {
		r.RepoErr = fmt.Errorf("parse %s: %w", repo, err)
		return r
	}
	if r.Err == nil {
		r.Changes = Changes(r.Entries, wantEntries)
	}
	return r
}

// Changes describes how have differs from want, one line per setting.
func Changes(have, want []Entry) []string {
	haveMap, wantMap := map[string]string{}, map[string]string{}
	var keys []string
	for _, e := range have {
		haveMap[e.Key] = e.Value
		keys = append(keys, e.Key)
	}
	for _, e := range want {
		wantMap[e.Key] = e.Value
		if _, ok := haveMap[e.Key]; !ok {
			keys = append(keys, e.Key)
		}
	}
	var out []string
	for _, k := range keys {
		h, inHave := haveMap[k]
		w, inWant := wantMap[k]
		switch {
		case !inWant:
			out = append(out, fmt.Sprintf("%s=%s isn't in the repo", k, h))
		case !inHave:
			out = append(out, fmt.Sprintf("%s is missing (repo: %s)", k, w))
		case h != w:
			out = append(out, fmt.Sprintf("%s is %q here, %q in the repo", k, h, w))
		}
	}
	if len(out) == 0 {
		out = append(out, "comments or layout only")
	}
	return out
}
