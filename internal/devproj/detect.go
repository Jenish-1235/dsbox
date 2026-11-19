package devproj

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrNoStartCommand means dev couldn't work out how to start a project.
var ErrNoStartCommand = errors.New("no start command found")

// Plan is how to start a project. Command is a shell command; it runs with
// PORT and HOST=0.0.0.0 in its environment, so it can use $PORT.
type Plan struct {
	Command string
	// Source says where the command came from, for messages.
	Source string
}

// Detect works out the start command for the project in dir.
func Detect(dir string) (Plan, error) {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	if cmd, ok := devrc(filepath.Join(dir, ".devrc")); ok {
		return Plan{Command: cmd, Source: ".devrc"}, nil
	}
	for _, jf := range []string{"justfile", "Justfile", ".justfile"} {
		if hasRecipe(filepath.Join(dir, jf), "dev") {
			return Plan{Command: "just dev", Source: jf}, nil
		}
	}
	if p, ok, err := nodePlan(dir, has); err != nil || ok {
		return p, err
	}
	for _, f := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"} {
		if has(f) {
			return Plan{Command: "docker compose up", Source: f}, nil
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "pyproject.toml")); err == nil && strings.Contains(strings.ToLower(string(data)), "fastapi") {
		entry := fastapiEntry(dir)
		return Plan{Command: "uv run fastapi dev " + entry + " --host 0.0.0.0 --port $PORT", Source: "pyproject.toml (FastAPI)"}, nil
	}
	if has("manage.py") {
		return Plan{Command: "uv run python manage.py runserver 0.0.0.0:$PORT", Source: "manage.py (Django)"}, nil
	}
	if has("go.mod") {
		return Plan{Command: "go run .", Source: "go.mod"}, nil
	}
	if has("Cargo.toml") {
		return Plan{Command: "cargo run", Source: "Cargo.toml"}, nil
	}
	return Plan{}, ErrNoStartCommand
}

// devrc returns the first line of path that isn't blank or a # comment.
func devrc(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }() // read-only
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			return line, true
		}
	}
	return "", false
}

// recipeLine matches a just recipe header such as "dev:", "dev *args:" or
// "@dev: build", but not an assignment ("dev := ...").
func recipeRE(name string) *regexp.Regexp {
	return regexp.MustCompile(`^@?` + regexp.QuoteMeta(name) + `(\s+[^:=]*)?:([^=]|$)`)
}

// hasRecipe reports whether the justfile at path defines recipe name.
func hasRecipe(path, name string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	re := recipeRE(name)
	for line := range strings.Lines(string(data)) {
		if re.MatchString(strings.TrimRight(line, "\r\n")) {
			return true
		}
	}
	return false
}

// HasRecipe reports whether dir has a justfile with recipe name.
func HasRecipe(dir, name string) bool {
	for _, jf := range []string{"justfile", "Justfile", ".justfile"} {
		if hasRecipe(filepath.Join(dir, jf), name) {
			return true
		}
	}
	return false
}

func nodePlan(dir string, has func(string) bool) (Plan, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return Plan{}, false, nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return Plan{}, false, fmt.Errorf("read %s: %w", filepath.Join(dir, "package.json"), err)
	}
	script, ok := pkg.Scripts["dev"]
	if !ok {
		return Plan{}, false, nil
	}
	pm, run := "npm", "npm run dev"
	switch {
	case has("pnpm-lock.yaml"):
		pm, run = "pnpm", "pnpm dev"
	case has("bun.lock"), has("bun.lockb"):
		pm, run = "bun", "bun run dev"
	case has("yarn.lock"):
		pm, run = "yarn", "yarn dev"
	}
	var flags string
	switch {
	case strings.Contains(script, "astro"):
		// astro dev has no --strictPort.
		flags = "--port $PORT --host 0.0.0.0"
	case strings.Contains(script, "vite"), strings.Contains(script, "react-router dev"):
		flags = "--port $PORT --host 0.0.0.0 --strictPort"
	}
	cmd := run
	if flags != "" {
		if pm == "npm" {
			cmd += " --"
		}
		cmd += " " + flags
	}
	return Plan{Command: cmd, Source: "package.json (" + pm + ")"}, true, nil
}

// fastapiEntry finds the FastAPI app file: main.py, app/main.py or src/*/main.py.
func fastapiEntry(dir string) string {
	for _, c := range []string{"main.py", "app/main.py"} {
		if _, err := os.Stat(filepath.Join(dir, c)); err == nil {
			return c
		}
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "src", "*", "main.py")); len(m) > 0 {
		if rel, err := filepath.Rel(dir, m[0]); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return "main.py"
}
