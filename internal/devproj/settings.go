// Package devproj runs the developer's projects as dev servers: it finds a project's start
// command, gives it a stable port, and keeps it running in tmux.
package devproj

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/Jenish-1235/dsbox/internal/config"
	"github.com/Jenish-1235/dsbox/internal/layout"
)

// Settings are what dev needs from the machine settings.
type Settings struct {
	Layout      layout.Layout
	ProjectsDir string
	Host        string
	AppsDomain  string
	// AppsTLS is config.TLSInternal to serve https next to http, with
	// certificates from Caddy's local CA. Anything else means http only.
	AppsTLS string
	PortMin int
	PortMax int
	// Forge is where ds publish puts a project with no origin:
	// config.ForgeGitHub (also when unset) or config.ForgeGitLab.
	Forge string
}

// LoadSettings reads /etc/devbox/devbox.env, then ~/.devbox/config.env (the
// developer's own choices, e.g. DEVBOX_FORGE), then lets the environment
// override each value (tests and the end-to-end run use that). Missing values
// get sensible defaults, so dev works even before provisioning.
func LoadSettings(l layout.Layout) Settings {
	file := map[string]string{}
	for _, path := range []string{l.MachineConfig(), l.UserConfig()} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if entries, err := config.Parse(data); err == nil {
			for _, e := range entries {
				file[e.Key] = e.Value
			}
		}
	}
	get := func(key, def string) string {
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		if v, ok := file[key]; ok {
			return v
		}
		return def
	}
	num := func(key string, def int) int {
		n, err := strconv.Atoi(get(key, ""))
		if err != nil || n < 1024 || n > 65535 {
			return def
		}
		return n
	}
	s := Settings{
		Layout:      l,
		ProjectsDir: get(config.ProjectsDir, ""),
		Host:        get(config.Host, "devbox"),
		AppsDomain:  get(config.AppsDomain, ""),
		AppsTLS:     get(config.AppsTLS, config.TLSOff),
		PortMin:     num(config.PortMin, 4100),
		PortMax:     num(config.PortMax, 4999),
		Forge:       get(config.Forge, ""),
	}
	if s.ProjectsDir == "" {
		s.ProjectsDir = filepath.Join(l.Home, "projects")
	}
	if s.Host == "" {
		s.Host = "devbox"
	}
	if s.Forge == "" {
		s.Forge = config.ForgeGitHub
	}
	if s.PortMin > s.PortMax {
		s.PortMin, s.PortMax = 4100, 4999
	}
	return s
}

// PortsFile is where each project's port is remembered.
func (s Settings) PortsFile() string { return filepath.Join(s.Layout.StateDir(), "ports.json") }

// LogFile is where a project's server output goes.
func (s Settings) LogFile(name string) string { return filepath.Join(s.Layout.Logs(), name+".log") }

// URL is where the developer opens the project in the developer's browser.
func (s Settings) URL(name string, port int) string {
	if s.AppsDomain != "" {
		return fmt.Sprintf("%s://%s.%s", s.AppsScheme(), name, s.AppsDomain)
	}
	return fmt.Sprintf("http://%s:%d", s.Host, port)
}

// AppsScheme is https when the devbox's own CA signs the project sites (TLS)
// or for a real domain, and http for an internal single-word apps domain
// without TLS.
func (s Settings) AppsScheme() string {
	if s.TLS() || strings.Contains(s.AppsDomain, ".") {
		return "https"
	}
	return "http"
}

// TLS reports whether Caddy serves the project sites on https too, with
// certificates from its local CA. It needs an apps domain.
func (s Settings) TLS() bool { return s.AppsDomain != "" && s.AppsTLS == config.TLSInternal }

// CAFile is the root certificate of Caddy's local CA. devbox-web runs Caddy
// as the developer with XDG_DATA_HOME=~/.local/share, so it lives here.
func (s Settings) CAFile() string {
	return filepath.Join(s.Layout.Home, ".local", "share", "caddy", "pki", "authorities", "local", "root.crt")
}

// AllowedHosts is DEV_ALLOWED_HOSTS: the host names the developer's browser uses for dev
// servers, comma-separated.
func (s Settings) AllowedHosts() string {
	if s.AppsDomain != "" {
		return s.Host + ",*." + s.AppsDomain
	}
	return s.Host
}

// Name turns a folder name into a project name: lowercase, with only a-z, 0-9
// and single dashes. It returns "" when nothing usable is left.
func Name(folder string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(folder) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if (r == '-' || r == '_' || r == '.' || unicode.IsSpace(r)) && !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// ValidName reports whether name is already a clean project name.
func ValidName(name string) bool { return name != "" && Name(name) == name && !Reserved(name) }

// Reserved names are taken by devbox pages: dbs.<apps domain> lists databases.
func Reserved(name string) bool { return name == "dbs" }
