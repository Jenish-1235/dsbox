package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const valid = `# comment
DEVBOX_HOST=devbox
DEVBOX_USER=alex

  # indented comment
DEVBOX_PROJECTS_DIR=/home/alex/projects
DEVBOX_DOMAIN=
DEVBOX_APPS_DOMAIN=
DEVBOX_PORT_MIN=4100
DEVBOX_PORT_MAX=4999
DEVBOX_DOZZLE_PORT=9999
DEVBOX_CHANNEL=main
DEVBOX_APPS_TLS=internal
DEVBOX_FORGE=gitlab
`

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []Entry
		wantErr string // substring; "" means no error
	}{
		{"empty", "", nil, ""},
		{"comments and blanks", "# a\n\n   \n\t# b\n", nil, ""},
		{"simple", "A=1\nB_2=x\n", []Entry{{"A", "1", 1}, {"B_2", "x", 2}}, ""},
		{"empty value", "A=\n", []Entry{{"A", "", 1}}, ""},
		{"surrounding spaces trimmed", "  A=1  \r\n", []Entry{{"A", "1", 1}}, ""},
		{"equals in value", "A=b=c\n", []Entry{{"A", "b=c", 1}}, ""},
		{"no expansion", "A=x\nB=y/$A\n", nil, "line 2: the value of B must be plain"},
		{"no equals", "# c\nJUST_A_WORD\n", nil, "line 2: expected KEY=value"},
		{"export", "export A=1\n", nil, `line 1: drop the "export"`},
		{"lowercase key", "a=1\n", nil, `line 1: "a" isn't a valid name`},
		{"key starts with digit", "1A=1\n", nil, `line 1: "1A" isn't a valid name`},
		{"space around equals", "A = 1\n", nil, "isn't a valid name"},
		{"quoted value", "A=\"x\"\n", nil, "line 1: the value of A must be plain"},
		{"inline comment", "A=1 # one\n", nil, "line 1: the value of A must be plain"},
		{"tilde", "A=~/x\n", nil, "must be plain"},
		{"duplicate", "A=1\n\nA=2\n", nil, "line 3: A is already set on line 1"},
		{"every bad line reported", "x\ny\n", nil, "line 2:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.in))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Parse(%q) error = %v, want it to contain %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", tt.in, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		replace map[string]string // key → new line ("" removes the line)
		want    []string          // substrings, one per expected problem
	}{
		{"valid", nil, nil},
		{"domains set", map[string]string{
			"DEVBOX_DOMAIN=":      "DEVBOX_DOMAIN=devbox.example.com",
			"DEVBOX_APPS_DOMAIN=": "DEVBOX_APPS_DOMAIN=apps.example-1.io",
		}, nil},
		{"unknown key", map[string]string{"DEVBOX_CHANNEL=main": "DEVBOX_CHANNEL=main\nDEVBOX_CHANEL=main"}, []string{"DEVBOX_CHANEL (line 13): unknown setting"}},
		{"missing required", map[string]string{"DEVBOX_PORT_MIN=4100": ""}, []string{"DEVBOX_PORT_MIN: missing"}},
		{"user and projects dir are optional", map[string]string{"DEVBOX_USER=alex": "", "DEVBOX_PROJECTS_DIR=/home/alex/projects": ""}, nil},
		{"empty required", map[string]string{"DEVBOX_HOST=devbox": "DEVBOX_HOST="}, []string{"DEVBOX_HOST (line 2): missing"}},
		{"bad host", map[string]string{"DEVBOX_HOST=devbox": "DEVBOX_HOST=Dev_Box"}, []string{"DEVBOX_HOST (line 2): must be a host name"}},
		{"bad user", map[string]string{"DEVBOX_USER=alex": "DEVBOX_USER=9lives"}, []string{"must be a Linux user name"}},
		{"relative projects dir", map[string]string{"DEVBOX_PROJECTS_DIR=/home/alex/projects": "DEVBOX_PROJECTS_DIR=projects"}, []string{"must be an absolute path"}},
		{"unclean projects dir", map[string]string{"DEVBOX_PROJECTS_DIR=/home/alex/projects": "DEVBOX_PROJECTS_DIR=/home/alex/projects/"}, []string{"must be an absolute path"}},
		{"port not a number", map[string]string{"DEVBOX_PORT_MIN=4100": "DEVBOX_PORT_MIN=low"}, []string{"DEVBOX_PORT_MIN (line 9): must be a port number"}},
		{"port too high", map[string]string{"DEVBOX_PORT_MAX=4999": "DEVBOX_PORT_MAX=70000"}, []string{"DEVBOX_PORT_MAX (line 10): must be a port number from 1024 to 65535"}},
		{"privileged app port", map[string]string{"DEVBOX_PORT_MIN=4100": "DEVBOX_PORT_MIN=80"}, []string{"from 1024"}},
		{"min not below max", map[string]string{"DEVBOX_PORT_MIN=4100": "DEVBOX_PORT_MIN=4999"}, []string{"DEVBOX_PORT_MIN (line 9): must be below DEVBOX_PORT_MAX"}},
		{"dozzle inside range", map[string]string{"DEVBOX_DOZZLE_PORT=9999": "DEVBOX_DOZZLE_PORT=4500"}, []string{"DEVBOX_DOZZLE_PORT (line 11): is inside the app port range"}},
		{"bad channel", map[string]string{"DEVBOX_CHANNEL=main": "DEVBOX_CHANNEL=beta"}, []string{"must be main or release"}},
		{"tls off", map[string]string{"DEVBOX_APPS_TLS=internal": "DEVBOX_APPS_TLS=off"}, nil},
		{"tls is optional", map[string]string{"DEVBOX_APPS_TLS=internal": ""}, nil},
		{"tls empty", map[string]string{"DEVBOX_APPS_TLS=internal": "DEVBOX_APPS_TLS="}, nil},
		{"forge github", map[string]string{"DEVBOX_FORGE=gitlab": "DEVBOX_FORGE=github"}, nil},
		{"forge is optional", map[string]string{"DEVBOX_FORGE=gitlab": ""}, nil},
		{"forge empty", map[string]string{"DEVBOX_FORGE=gitlab": "DEVBOX_FORGE="}, nil},
		{"bad forge", map[string]string{"DEVBOX_FORGE=gitlab": "DEVBOX_FORGE=GitHub"}, []string{`DEVBOX_FORGE (line 14): must be github or gitlab, not "GitHub"`}},
		{"bad tls", map[string]string{"DEVBOX_APPS_TLS=internal": "DEVBOX_APPS_TLS=acme"}, []string{`DEVBOX_APPS_TLS (line 13): must be off or internal, not "acme"`}},
		{"single-label domain", map[string]string{"DEVBOX_DOMAIN=": "DEVBOX_DOMAIN=localhost"}, []string{"DEVBOX_DOMAIN (line 7): \"localhost\" isn't a domain"}},
		{"uppercase domain", map[string]string{"DEVBOX_DOMAIN=": "DEVBOX_DOMAIN=Dev.Example.com"}, []string{"isn't a domain"}},
		{"hyphen-edged label", map[string]string{"DEVBOX_APPS_DOMAIN=": "DEVBOX_APPS_DOMAIN=-apps.example.com"}, []string{"DEVBOX_APPS_DOMAIN"}},
		{"numeric tld", map[string]string{"DEVBOX_DOMAIN=": "DEVBOX_DOMAIN=10.0.0.1"}, []string{"isn't a domain"}},
		{"several problems", map[string]string{
			"DEVBOX_CHANNEL=main":  "DEVBOX_CHANNEL=nightly",
			"DEVBOX_PORT_MIN=4100": "DEVBOX_PORT_MIN=x",
		}, []string{"DEVBOX_PORT_MIN", "DEVBOX_CHANNEL"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := strings.Split(valid, "\n")
			for i, l := range lines {
				if repl, ok := tt.replace[l]; ok {
					lines[i] = repl
				}
			}
			text := strings.Join(lines, "\n")
			entries, err := Parse([]byte(text))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			_, probs := Validate(entries)
			if len(probs) != len(tt.want) {
				t.Fatalf("Validate() = %v, want %d problems %q", probs, len(tt.want), tt.want)
			}
			for i, w := range tt.want {
				if !strings.Contains(probs[i].String(), w) {
					t.Errorf("Validate() problem %d = %q, want it to contain %q", i, probs[i], w)
				}
			}
		})
	}
}

func TestValidateReturnsSettings(t *testing.T) {
	entries, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	got, probs := Validate(entries)
	want := Settings{Host: "devbox", User: "alex", ProjectsDir: "/home/alex/projects",
		AppsTLS: "internal", PortMin: 4100, PortMax: 4999, DozzlePort: 9999, Channel: "main", Forge: "gitlab"}
	if len(probs) > 0 || got != want {
		t.Errorf("Validate() = %+v, %v, want %+v, no problems", got, probs, want)
	}
}

// The file provision.sh installs must pass ds's own checks.
func TestRepoConfigIsValid(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "devbox.env"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse(config/devbox.env): %v", err)
	}
	if _, probs := Validate(entries); len(probs) > 0 {
		t.Errorf("config/devbox.env problems: %v", probs)
	}
}

func TestInspect(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo.env")
	if err := os.WriteFile(repo, []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		machine     string // "" means the file doesn't exist
		wantOK      bool
		wantMissing bool
		wantDiffers bool
		wantChanges []string
		wantErr     bool
	}{
		{name: "same", machine: valid, wantOK: true},
		{name: "missing", wantMissing: true},
		{name: "comment changed", machine: strings.Replace(valid, "# comment", "# other", 1),
			wantDiffers: true, wantChanges: []string{"comments or layout only"}},
		{name: "value changed", machine: strings.Replace(valid, "CHANNEL=main", "CHANNEL=release", 1),
			wantDiffers: true, wantChanges: []string{`DEVBOX_CHANNEL is "release" here, "main" in the repo`}},
		{name: "setting removed", machine: strings.Replace(valid, "DEVBOX_DOZZLE_PORT=9999\n", "", 1),
			wantDiffers: true, wantChanges: []string{"DEVBOX_DOZZLE_PORT is missing (repo: 9999)"}},
		{name: "syntax error", machine: valid + "oops\n", wantDiffers: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			machine := filepath.Join(t.TempDir(), "devbox.env")
			if tt.machine != "" {
				if err := os.WriteFile(machine, []byte(tt.machine), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			r := Inspect(machine, repo)
			if r.OK() != tt.wantOK || r.Missing != tt.wantMissing || r.Differs != tt.wantDiffers || (r.Err != nil) != tt.wantErr {
				t.Errorf("Inspect() = OK %v missing %v differs %v err %v, want %v %v %v err %v",
					r.OK(), r.Missing, r.Differs, r.Err, tt.wantOK, tt.wantMissing, tt.wantDiffers, tt.wantErr)
			}
			if !slices.Equal(r.Changes, tt.wantChanges) {
				t.Errorf("Inspect() changes = %q, want %q", r.Changes, tt.wantChanges)
			}
		})
	}
}
