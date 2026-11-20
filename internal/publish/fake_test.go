package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// call is one command the fake ran.
type call struct {
	name  string
	args  []string
	stdin string
}

func (c call) String() string { return c.name + " " + strings.Join(c.args, " ") }

// fake stands in for git, glab and gh: a tiny GitLab and GitHub in memory.
type fake struct {
	mu sync.Mutex

	user, ghUser, token string
	// origin is the repo's origin URL ("" = none); branches are local branches.
	origin   string
	branches []string
	noCommit bool

	nextID   int
	projects map[string]*glProject // by path_with_namespace
	mirrors  map[int][]remoteMirror
	repos    map[string]ghRepo // by owner/name
	// syncResult is what a sync leaves: finished or failed (with syncError).
	syncResult, syncError string
	// echoBody makes POST remote_mirrors fail and print the request, as an
	// API might.
	echoBody bool
	// forbidden project IDs answer remote_mirrors with 403.
	forbidden map[int]bool
	// ghBroken makes gh fail as when logged out; limited is how many glab
	// calls answer 429 next.
	ghBroken bool
	limited  int
	// ghBranches are each GitHub repo's branches (name → commit).
	ghBranches map[string]map[string]string
	// ghEmpty409 makes listing a repo with no branches answer 409, as GitHub
	// may for an empty repo.
	ghEmpty409 bool
	// ghProtocol is what gh config get git_protocol prints.
	ghProtocol string
	// pushURLs are origin's push URLs (git config remote.origin.pushurl).
	pushURLs []string
	// glBranches are each GitLab project's branches (name → commit), by path.
	glBranches map[string]map[string]string
	// glSignedOut makes glab auth status fail.
	glSignedOut bool

	calls []call
}

func newFake() *fake {
	return &fake{
		user: "alex", ghUser: "alex-gh", token: "fake-gh-token-for-tests",
		branches:   []string{"main"},
		nextID:     100,
		projects:   map[string]*glProject{},
		mirrors:    map[int][]remoteMirror{},
		repos:      map[string]ghRepo{},
		syncResult: statusFinished,
		forbidden:  map[int]bool{},
		ghBranches: map[string]map[string]string{},
		glBranches: map[string]map[string]string{},
		ghProtocol: "https",
	}
}

func (f *fake) addProject(path string) *glProject {
	f.nextID++
	_, name, _ := strings.Cut(path, "/")
	p := &glProject{
		ID: f.nextID, Path: name, PathWithNS: path,
		WebURL:  "https://gitlab.com/" + path,
		HTTPURL: "https://gitlab.com/" + path + ".git",
		SSHURL:  "git@gitlab.com:" + path + ".git",
		Empty:   true, // until something is pushed
	}
	f.projects[path] = p
	return p
}

// heads are the local branches and their commits.
func (f *fake) heads() map[string]string {
	heads := map[string]string{}
	for _, b := range f.branches {
		heads[b] = "sha-" + b
	}
	return heads
}

func (f *fake) ran(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c.String(), prefix) {
			n++
		}
	}
	return n
}

func notFound(what string) error {
	return fmt.Errorf("%s: exit status 1: {\"message\":\"404 Not Found\"} (HTTP 404)", what)
}

func (f *fake) Output(_ context.Context, _ string, stdin []byte, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{name: name, args: args, stdin: string(stdin)})
	switch name {
	case "git":
		return f.git(args)
	case "glab":
		return f.glab(args, stdin)
	case "gh":
		return f.gh(args)
	}
	return "", fmt.Errorf("fake: unexpected command %s", name)
}

func (f *fake) git(args []string) (string, error) {
	switch strings.Join(args[:min(2, len(args))], " ") {
	case "rev-parse --git-dir":
		return ".git", nil
	case "rev-parse --verify":
		if f.noCommit {
			return "", errors.New("git rev-parse: exit status 1")
		}
		return "abc123", nil
	case "remote":
		if f.origin == "" {
			return "", nil
		}
		return "origin", nil
	case "remote get-url":
		return f.origin, nil
	case "remote add":
		f.origin = args[3]
		return "", nil
	case "remote set-url":
		if len(args) == 6 && args[2] == "--add" && args[3] == "--push" && args[4] == "origin" {
			f.pushURLs = append(f.pushURLs, args[5])
			return "", nil
		}
	case "config --get-all":
		if len(f.pushURLs) == 0 {
			return "", errors.New("git config: exit status 1")
		}
		return strings.Join(f.pushURLs, "\n"), nil
	case "for-each-ref --format=%(refname:short) %(objectname)":
		var heads []string
		for b, sha := range f.heads() {
			heads = append(heads, b+" "+sha)
		}
		return strings.Join(heads, "\n"), nil
	case "for-each-ref --format=%(refname:short)":
		return "", nil // no tags
	case "push --quiet":
		target := args[2]
		if target == "-u" {
			target = args[3]
		}
		urls := []string{target}
		if target == "origin" {
			urls = f.pushURLs
			if len(urls) == 0 {
				urls = []string{f.origin}
			}
		}
		for _, u := range urls {
			if err := f.pushTo(u); err != nil {
				return "", err
			}
		}
		return "", nil
	}
	return "", fmt.Errorf("fake git: unexpected %q", args)
}

// pushTo receives the local branches at url, on the fake GitLab or GitHub.
func (f *fake) pushTo(url string) error {
	for path, p := range f.projects {
		if url == p.HTTPURL || url == p.SSHURL {
			p.Empty = false
			f.glBranches[path] = f.heads()
			if p.DefaultBranch == "" {
				p.DefaultBranch = f.branches[0]
			}
		}
	}
	if o := classifyOrigin(url, DefaultHost); o.kind == gitHubOrigin {
		if _, ok := f.repos[o.path]; !ok {
			return errors.New("git push: exit status 128: remote: Repository not found")
		}
		f.ghBranches[o.path] = f.heads()
		// An empty repo takes the first branch that arrives as its default.
		if r := f.repos[o.path]; r.DefaultBranch == "" {
			r.DefaultBranch = f.branches[0]
			f.repos[o.path] = r
		}
	}
	return nil
}

func (f *fake) glab(args []string, stdin []byte) (string, error) {
	if len(args) >= 2 && args[0] == "config" {
		return "https", nil
	}
	if len(args) >= 2 && args[0] == "auth" && args[1] == "status" {
		if f.glSignedOut {
			return "", errors.New("glab auth status: exit status 1: not logged in")
		}
		return "", nil
	}
	if f.limited > 0 {
		f.limited--
		return "", errors.New("glab api: exit status 1: glab: HTTP 429\nglab: rate limited by burst_traffic_per_user_free_plan, retry after 0s Rate limit exceeded")
	}
	method, endpoint, paginate := "GET", "", false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--hostname", "--input":
			i++
		case "--method":
			method = args[i+1]
			i++
		case "--paginate":
			paginate = true
		default:
			endpoint = args[i]
		}
	}
	var body map[string]any
	if len(stdin) > 0 {
		if err := json.Unmarshal(stdin, &body); err != nil {
			return "", err
		}
	}
	reply := func(v any) (string, error) {
		data, err := json.Marshal(v)
		return string(data), err
	}
	parts := strings.Split(endpoint, "/")
	switch {
	case endpoint == "user":
		return reply(map[string]string{"username": f.user})
	case strings.HasPrefix(endpoint, "projects?") && paginate:
		// Two pages, as glab --paginate prints them.
		var page1, page2 []glProject
		i := 0
		for path, p := range f.projects {
			if strings.Contains(endpoint, "owned=true") && !strings.HasPrefix(path, f.user+"/") {
				continue
			}
			if i%2 == 0 {
				page1 = append(page1, *p)
			} else {
				page2 = append(page2, *p)
			}
			i++
		}
		a, _ := json.Marshal(page1)
		b, _ := json.Marshal(page2)
		return string(a) + string(b), nil
	case endpoint == "projects" && method == "POST":
		p := f.addProject(f.user + "/" + body["path"].(string))
		if d, ok := body["description"].(string); ok {
			p.Description = d
		}
		return reply(p)
	case len(parts) == 4 && parts[2] == "repository" && strings.HasPrefix(parts[3], "branches"):
		type branch struct {
			Name   string `json:"name"`
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		list := []branch{}
		for _, p := range f.projects {
			if strconv.Itoa(p.ID) != parts[1] {
				continue
			}
			for name, sha := range f.glBranches[p.PathWithNS] {
				b := branch{Name: name}
				b.Commit.ID = sha
				list = append(list, b)
			}
		}
		return reply(list)
	case len(parts) == 2 && method == "GET":
		path, _ := url.QueryUnescape(parts[1])
		if p, ok := f.projects[path]; ok {
			return reply(p)
		}
		for _, p := range f.projects {
			if strconv.Itoa(p.ID) == parts[1] {
				return reply(p)
			}
		}
		return "", notFound("glab api " + endpoint)
	case len(parts) == 2 && method == "PUT":
		for _, p := range f.projects {
			if strconv.Itoa(p.ID) == parts[1] {
				if b, ok := body["default_branch"].(string); ok {
					p.DefaultBranch = b
				}
				if d, ok := body["description"].(string); ok {
					p.Description = d
				}
				return reply(p)
			}
		}
		return "", notFound("glab api " + endpoint)
	case len(parts) >= 3 && parts[2] == "remote_mirrors":
		return f.remoteMirrors(method, parts, body, reply)
	}
	return "", fmt.Errorf("fake glab: unexpected %s %s", method, endpoint)
}

func (f *fake) remoteMirrors(method string, parts []string, body map[string]any, reply func(any) (string, error)) (string, error) {
	pid, _ := strconv.Atoi(parts[1])
	if f.forbidden[pid] {
		return "", errors.New("glab api: exit status 1: 403 Forbidden (HTTP 403)")
	}
	ms := f.mirrors[pid]
	find := func() int {
		id, _ := strconv.Atoi(parts[3])
		for i, m := range ms {
			if m.ID == id {
				return i
			}
		}
		return -1
	}
	switch {
	case len(parts) == 3 && method == "GET":
		return reply(ms)
	case len(parts) == 3 && method == "POST":
		if f.echoBody {
			data, _ := json.Marshal(body)
			return "", fmt.Errorf("glab api: exit status 1: {\"message\":\"url is blocked\",\"request\":%s} (HTTP 400)", data)
		}
		raw := body["url"].(string)
		u, _ := url.Parse(raw)
		u.User = url.UserPassword("*****", "*****")
		f.nextID++
		m := remoteMirror{ID: f.nextID, Enabled: true, URL: u.String(), UpdateStatus: "none"}
		f.mirrors[pid] = append(ms, m)
		return reply(m)
	case len(parts) == 4 && method == "GET":
		if i := find(); i >= 0 {
			return reply(ms[i])
		}
	case len(parts) == 4 && method == "PUT":
		if i := find(); i >= 0 {
			return reply(ms[i])
		}
	case len(parts) == 4 && method == "DELETE":
		if i := find(); i >= 0 {
			f.mirrors[pid] = append(ms[:i:i], ms[i+1:]...)
			return "", nil
		}
	case len(parts) == 5 && parts[4] == "sync" && method == "POST":
		if i := find(); i >= 0 && f.syncResult != "" {
			now := time.Now().UTC()
			ms[i].UpdateStatus, ms[i].LastError = f.syncResult, f.syncError
			ms[i].LastUpdateStartedAt, ms[i].LastUpdateAt = now, now
			if f.syncResult == statusFinished {
				ms[i].LastSuccessfulUpdateAt = now
				if full, ok := githubTarget(ms[i].URL); ok {
					f.ghBranches[full] = f.heads()
				}
			}
			return "", nil
		}
	}
	return "", notFound("glab api remote_mirrors")
}

func (f *fake) gh(args []string) (string, error) {
	switch {
	case len(args) >= 2 && args[0] == "api" && args[1] == "user":
		if f.ghBroken {
			return "", errors.New("gh api: exit status 4: To get started with GitHub CLI, please run:  gh auth login")
		}
		return f.ghUser, nil
	case len(args) >= 3 && args[0] == "config" && args[2] == "git_protocol":
		return f.ghProtocol, nil
	case len(args) >= 3 && args[0] == "api" && args[1] == "--paginate" && strings.HasPrefix(args[2], "user/repos"):
		if f.ghBroken {
			return "", errors.New("gh api: exit status 4: To get started with GitHub CLI, please run:  gh auth login")
		}
		var list []ghListed
		for full, r := range f.repos {
			_, name, _ := strings.Cut(full, "/")
			list = append(list, ghListed{Name: name, FullName: full, Description: r.Description})
		}
		data, err := json.Marshal(list)
		return string(data) + "[]", err
	case len(args) >= 3 && args[0] == "api" && args[1] == "--paginate" && strings.HasSuffix(args[2], "/branches?per_page=100"):
		full := strings.TrimSuffix(strings.TrimPrefix(args[2], "repos/"), "/branches?per_page=100")
		if f.ghEmpty409 && len(f.ghBranches[full]) == 0 {
			return "", errors.New("gh api: exit status 1: gh: Git Repository is empty. (HTTP 409)")
		}
		type branch struct {
			Name   string `json:"name"`
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		var list []branch
		for name, sha := range f.ghBranches[full] {
			b := branch{Name: name}
			b.Commit.SHA = sha
			list = append(list, b)
		}
		data, err := json.Marshal(list)
		return string(data), err
	case len(args) >= 2 && args[0] == "api" && strings.HasPrefix(args[1], "repos/"):
		full := strings.TrimPrefix(args[1], "repos/")
		r, ok := f.repos[full]
		if !ok {
			return "", notFound("gh api " + args[1])
		}
		data, err := json.Marshal(r)
		return string(data), err
	case len(args) >= 3 && args[0] == "repo" && args[1] == "create":
		desc, _ := flagValue(args, "--description")
		f.repos[args[2]] = ghRepo{HTMLURL: "https://github.com/" + args[2], Description: desc}
		return "https://github.com/" + args[2], nil
	case len(args) >= 3 && args[0] == "repo" && args[1] == "edit":
		r := f.repos[args[2]]
		if d, ok := flagValue(args, "--description"); ok {
			r.Description = d
		}
		if b, ok := flagValue(args, "--default-branch"); ok {
			r.DefaultBranch = b
		}
		f.repos[args[2]] = r
		return "", nil
	case len(args) >= 2 && args[0] == "auth" && args[1] == "token":
		return f.token, nil
	}
	return "", fmt.Errorf("fake gh: unexpected %q", args)
}

// flagValue is the value after name in args.
func flagValue(args []string, name string) (string, bool) {
	for i, a := range args[:max(0, len(args)-1)] {
		if a == name {
			return args[i+1], true
		}
	}
	return "", false
}
