package publish

import (
	"net/url"
	"strings"
)

// originKind says what a repo's origin remote points at.
type originKind int

const (
	noOrigin originKind = iota
	gitLabOrigin
	gitHubOrigin
	otherOrigin
)

// gitHubHost is where GitHub repos live.
const gitHubHost = "github.com"

// origin is a parsed origin remote.
type origin struct {
	kind originKind
	// path is the project path on GitLab, e.g. "group/sub/shop", or the
	// repo on GitHub, "owner/shop" (not set for noOrigin and otherOrigin).
	path string
}

// classifyOrigin sorts the origin URL raw into none, a project on the GitLab
// host, a repo on github.com, or something else. It understands https,
// ssh:// and scp-like (git@host:path) URLs.
func classifyOrigin(raw, host string) origin {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return origin{kind: noOrigin}
	}
	h, path, ok := splitRemote(raw)
	if !ok {
		return origin{kind: otherOrigin}
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	switch {
	case strings.EqualFold(h, host) && strings.Contains(path, "/"):
		return origin{kind: gitLabOrigin, path: path}
	case strings.EqualFold(h, gitHubHost) && strings.Count(path, "/") == 1:
		return origin{kind: gitHubOrigin, path: path}
	}
	return origin{kind: otherOrigin}
}

// splitRemote returns the host (without port) and path of a git remote URL.
func splitRemote(raw string) (host, path string, ok bool) {
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "", "", false
		}
		return u.Hostname(), u.Path, true
	}
	// scp-like: [user@]host:path. A colon after a slash is a local path.
	before, after, found := strings.Cut(raw, ":")
	if !found || strings.Contains(before, "/") || after == "" {
		return "", "", false
	}
	if _, h, ok := strings.Cut(before, "@"); ok {
		before = h
	}
	return before, after, before != ""
}
