package devproj

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"devrc wins", map[string]string{".devrc": "# how to start\n\n  ./serve --port $PORT\nignored\n", "go.mod": "module x"}, "./serve --port $PORT"},
		{"justfile dev recipe", map[string]string{"justfile": "set dotenv-load\n\ndev *args:\n    pnpm dev\n", "package.json": `{"scripts":{"dev":"vite"}}`}, "just dev"},
		{"justfile without dev", map[string]string{"justfile": "devtools := \"x\"\ntest:\n  go test\n", "go.mod": "module x"}, "go run ."},
		{"vite pnpm", map[string]string{"package.json": `{"scripts":{"dev":"vite"}}`, "pnpm-lock.yaml": ""}, "pnpm dev --port $PORT --host 0.0.0.0 --strictPort"},
		{"vite npm", map[string]string{"package.json": `{"scripts":{"dev":"vite --open"}}`}, "npm run dev -- --port $PORT --host 0.0.0.0 --strictPort"},
		{"react-router bun", map[string]string{"package.json": `{"scripts":{"dev":"react-router dev"}}`, "bun.lock": ""}, "bun run dev --port $PORT --host 0.0.0.0 --strictPort"},
		{"astro yarn", map[string]string{"package.json": `{"scripts":{"dev":"astro dev"}}`, "yarn.lock": ""}, "yarn dev --port $PORT --host 0.0.0.0"},
		{"next", map[string]string{"package.json": `{"scripts":{"dev":"next dev --turbopack"}}`, "pnpm-lock.yaml": ""}, "pnpm dev"},
		{"package.json without dev", map[string]string{"package.json": `{"scripts":{"build":"tsc"}}`, "compose.yaml": ""}, "docker compose up"},
		{"compose", map[string]string{"docker-compose.yml": ""}, "docker compose up"},
		{"fastapi main", map[string]string{"pyproject.toml": "dependencies = [\"fastapi[standard]\"]", "main.py": ""}, "uv run fastapi dev main.py --host 0.0.0.0 --port $PORT"},
		{"fastapi src", map[string]string{"pyproject.toml": "dependencies = [\"FastAPI\"]", "src/shop/main.py": ""}, "uv run fastapi dev src/shop/main.py --host 0.0.0.0 --port $PORT"},
		{"django", map[string]string{"pyproject.toml": "dependencies = [\"django\"]", "manage.py": ""}, "uv run python manage.py runserver 0.0.0.0:$PORT"},
		{"go", map[string]string{"go.mod": "module x"}, "go run ."},
		{"cargo", map[string]string{"Cargo.toml": ""}, "cargo run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, tt.files)
			got, err := Detect(dir)
			if err != nil {
				t.Fatalf("Detect() error = %v", err)
			}
			if got.Command != tt.want {
				t.Errorf("Detect() = %q, want %q", got.Command, tt.want)
			}
		})
	}
}

func TestDetectNothing(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"README.md": "hi", ".devrc": "# only comments\n"})
	if _, err := Detect(dir); !errors.Is(err, ErrNoStartCommand) {
		t.Errorf("Detect() error = %v, want ErrNoStartCommand", err)
	}
}

func TestName(t *testing.T) {
	tests := map[string]string{
		"demo-next":     "demo-next",
		"My_App":        "my-app",
		"shop.v2":       "shop-v2",
		"  Hello World": "hello-world",
		"a--b__c":       "a-b-c",
		"-lead":         "lead",
		"trail-":        "trail",
		"日本":            "",
		"Café":          "caf",
	}
	for in, want := range tests {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
	if ValidName("My_App") || !ValidName("my-app") || ValidName("") {
		t.Error("ValidName gives the wrong answer")
	}
}

func TestPortsAssign(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "ports.json")
	ports, err := LoadPorts(path)
	if err != nil {
		t.Fatal(err)
	}
	busy := map[int]bool{4100: true}
	free := func(p int) bool { return !busy[p] }

	a, isNew, err := ports.Assign("a", 4100, 4103, free)
	if err != nil || !isNew || a != 4101 {
		t.Fatalf("Assign(a) = %d, %v, %v; want 4101, true, nil (4100 is listening)", a, isNew, err)
	}
	if err := ports.Save(path); err != nil {
		t.Fatal(err)
	}

	// A later run keeps the saved port, even if it's busy now (its own server).
	ports, err = LoadPorts(path)
	if err != nil {
		t.Fatal(err)
	}
	busy[4101] = true
	if got, isNew, _ := ports.Assign("a", 4100, 4103, free); got != 4101 || isNew {
		t.Errorf("Assign(a) again = %d, %v; want 4101, false", got, isNew)
	}

	// b skips a's port even when it's not listening.
	delete(busy, 4101)
	if got, _, _ := ports.Assign("b", 4100, 4103, free); got != 4102 {
		t.Errorf("Assign(b) = %d, want 4102", got)
	}
	if got, _, _ := ports.Assign("c", 4100, 4103, free); got != 4103 {
		t.Errorf("Assign(c) = %d, want 4103", got)
	}
	if _, _, err := ports.Assign("d", 4100, 4103, free); err == nil {
		t.Error("Assign(d) with the range full = nil error, want one")
	}
}

func TestRename(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"package.json":       "{\n  \"name\": \"react-vite\",\n  \"dependencies\": {\"react-vite-x\": \"1\"}\n}\n",
		"go.mod":             "module github.com/x/golang-ddd\n\ngo 1.27\n",
		"pyproject.toml":     "[project]\nname = \"python-fastapi\"\n",
		"uv.lock":            "[[package]]\nname = \"fastapi\"\n\n[[package]]\nname = \"python-fastapi\"\nsource = { editable = \".\" }\n",
		"index.html":         "<html>\n<head><title>react-vite</title></head>\n<script src=\"/react-vite.js\"></script>\n",
		"src/app/layout.tsx": "export const metadata = {\n  title: \"nextjs\",\n};\nimport x from \"nextjs\";\n",
	})
	for _, tmpl := range []string{"react-vite", "golang-ddd", "python-fastapi", "nextjs"} {
		if err := Rename(dir, tmpl, "shop"); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{
		"package.json":       "{\n  \"name\": \"shop\",\n  \"dependencies\": {\"react-vite-x\": \"1\"}\n}\n",
		"go.mod":             "module github.com/x/shop\n\ngo 1.27\n",
		"pyproject.toml":     "[project]\nname = \"shop\"\n",
		"uv.lock":            "[[package]]\nname = \"fastapi\"\n\n[[package]]\nname = \"shop\"\nsource = { editable = \".\" }\n",
		"index.html":         "<html>\n<head><title>shop</title></head>\n<script src=\"/react-vite.js\"></script>\n",
		"src/app/layout.tsx": "export const metadata = {\n  title: \"shop\",\n};\nimport x from \"nextjs\";\n",
	}
	for f, w := range want {
		got, _ := os.ReadFile(filepath.Join(dir, f))
		if string(got) != w {
			t.Errorf("%s = %q, want %q", f, got, w)
		}
	}
}

func TestSettingsAndResolve(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	write(t, projects, map[string]string{"My_App/src/x.go": ""})
	t.Setenv("DEVBOX_PROJECTS_DIR", projects)
	t.Setenv("DEVBOX_PORT_MIN", "5000")
	t.Setenv("DEVBOX_PORT_MAX", "5010")
	t.Setenv("DEVBOX_APPS_DOMAIN", "")
	t.Setenv("DEVBOX_HOST", "")
	s := LoadSettings(layout.Layout{Home: root, Root: filepath.Join(root, ".devbox"), Etc: filepath.Join(root, "etc")})
	if s.ProjectsDir != projects || s.PortMin != 5000 || s.PortMax != 5010 || s.Host != "devbox" {
		t.Fatalf("LoadSettings() = %+v", s)
	}
	if got := s.URL("shop", 5001); got != "http://devbox:5001" {
		t.Errorf("URL() = %q", got)
	}
	s.AppsDomain = "apps.example.com"
	if got := s.URL("shop", 5001); got != "https://shop.apps.example.com" {
		t.Errorf("URL() with apps domain = %q", got)
	}
	if got := s.AllowedHosts(); got != "devbox,*.apps.example.com" {
		t.Errorf("AllowedHosts() = %q", got)
	}

	d := Dev{S: s}
	p, err := d.Resolve("my-app")
	if err != nil || p.Dir != filepath.Join(projects, "My_App") || p.Name != "my-app" {
		t.Errorf("Resolve(my-app) = %+v, %v", p, err)
	}
	t.Chdir(filepath.Join(projects, "My_App", "src"))
	p, err = d.Resolve("")
	if err != nil || p.Name != "my-app" {
		t.Errorf("Resolve(\"\") in a subfolder = %+v, %v", p, err)
	}
}

func TestTemplates(t *testing.T) {
	root := t.TempDir()
	l := layout.Layout{Home: root, Root: root}
	write(t, root, map[string]string{
		"adlc/templates/nextjs/README.md":           "# Next.js app\n",
		"templates/mine/README.md":                  "\n## My own thing\n",
		"templates/nextjs/README.md":                "shadowed",
		"templates/.hidden/README.md":               "x",
		"adlc/templates/event-driven-golang/go.mod": "module x\n",
		"adlc/templates/ddd-golang/go.mod":          "module x\n",
	})
	ts := Settings{Layout: l}.Templates()
	names := make([]string, 0, len(ts))
	for _, tm := range ts {
		names = append(names, tm.Name)
	}
	if got, want := strings.Join(names, ","), "ddd-golang,nextjs,mine,empty"; got != want {
		t.Fatalf("Templates() names = %s, want %s (event-driven-golang hidden, empty last)", got, want)
	}
	if !ts[1].ADLC || ts[1].Summary != "Next.js app" {
		t.Errorf("nextjs = %+v, want ADLC with its README summary", ts[1])
	}
	if ts[0].Summary != "Go HTTP service with domain-driven layers" {
		t.Errorf("ddd-golang summary = %q, want the built-in one", ts[0].Summary)
	}
	if ts[2].ADLC || ts[2].Summary != "My own thing" {
		t.Errorf("mine = %+v", ts[2])
	}
}

func TestNewEmpty(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	l := layout.Layout{Home: root, Root: filepath.Join(root, ".devbox")}
	d := Dev{S: Settings{Layout: l, ProjectsDir: filepath.Join(root, "projects")}, Out: ui.Plain(io.Discard), W: io.Discard}
	if err := d.New(t.Context(), EmptyTemplate, "notes"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "projects", "notes")
	for _, f := range []string{"README.md", ".gitignore", ".git"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("empty project is missing %s: %v", f, err)
		}
	}
	out, err := exec.CommandContext(t.Context(), "git", "-C", dir, "log", "--oneline").Output()
	if err != nil || !strings.Contains(string(out), "empty project") {
		t.Errorf("git log = %q (err %v), want the first commit", out, err)
	}
}

func TestResolveRejectsBadNames(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	write(t, projects, map[string]string{"dbs/.devrc": "true\n", "DBS_/x": "", "--/x": ""})
	d := Dev{S: Settings{ProjectsDir: projects}}
	for _, arg := range []string{
		filepath.Join(projects, "--"), // no letters or digits
		"..",                          // no name at all
	} {
		if p, err := d.Resolve(arg); err == nil {
			t.Errorf("Resolve(%q) = %+v, want an error", arg, p)
		}
	}
}

// A reserved name (dbs.<apps domain> is the databases page) still resolves, so
// an existing project can be stopped and read, but it never starts.
func TestReservedProjectResolvesButNeverStarts(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	write(t, projects, map[string]string{"dbs/.devrc": "true\n", "DBS_/x": ""})
	d := Dev{S: Settings{ProjectsDir: projects}, Out: ui.Plain(io.Discard), W: io.Discard}
	for _, arg := range []string{"dbs", "DBS_", filepath.Join(projects, "dbs")} {
		p, err := d.Resolve(arg)
		if err != nil || p.Name != "dbs" {
			t.Errorf("Resolve(%q) = %+v, %v; want project dbs", arg, p, err)
			continue
		}
		if err := d.Up(t.Context(), p); err == nil || !strings.Contains(err.Error(), "can't be a project name") {
			t.Errorf("Up(%q) = %v, want the reserved-name error", arg, err)
		}
	}
}

func TestNewRemovesUnfinishedProject(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	l := layout.Layout{Home: root, Root: filepath.Join(root, ".devbox")}
	// package.json as a folder makes Rename fail after the copy and git init.
	write(t, l.Templates(), map[string]string{"broken/README.md": "# Broken\n", "broken/package.json/x": ""})
	d := Dev{S: Settings{Layout: l, ProjectsDir: filepath.Join(root, "projects")}, Out: ui.Plain(io.Discard), W: io.Discard}
	if err := d.New(t.Context(), "broken", "shop"); err == nil {
		t.Fatal("New() from a broken template succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "projects", "shop")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unfinished project left behind: %v", err)
	}

	// A folder that was already there is never removed.
	write(t, root, map[string]string{"projects/mine/keep": "x"})
	if err := d.New(t.Context(), "broken", "mine"); !errors.Is(err, ErrReported) {
		t.Fatalf("New() over an existing folder = %v, want ErrReported", err)
	}
	if _, err := os.Stat(filepath.Join(root, "projects", "mine", "keep")); err != nil {
		t.Errorf("existing project touched: %v", err)
	}
}
