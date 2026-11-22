package devproj

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jenish-1235/dsbox/internal/layout"
)

func TestURLScheme(t *testing.T) {
	tests := []struct {
		domain, tls, want string
	}{
		{"", "off", "http://devbox:4100"},
		{"", "internal", "http://devbox:4100"},
		{"devbox", "off", "http://shop.devbox"},
		{"devbox", "", "http://shop.devbox"},
		{"devbox", "internal", "https://shop.devbox"},
		{"apps.example.dev", "off", "https://shop.apps.example.dev"},
		{"apps.example.dev", "internal", "https://shop.apps.example.dev"},
	}
	for _, tt := range tests {
		s := Settings{Host: "devbox", AppsDomain: tt.domain, AppsTLS: tt.tls}
		if got := s.URL("shop", 4100); got != tt.want {
			t.Errorf("URL(shop, 4100) with domain %q, tls %q = %q, want %q", tt.domain, tt.tls, got, tt.want)
		}
	}
}

func TestLoadSettingsTLS(t *testing.T) {
	root := t.TempDir()
	l := layout.Layout{Home: root, Root: filepath.Join(root, ".devbox"), Etc: filepath.Join(root, "etc")}
	t.Setenv("DEVBOX_APPS_DOMAIN", "devbox")
	t.Setenv("DEVBOX_APPS_TLS", "internal")
	s := LoadSettings(l)
	if !s.TLS() {
		t.Errorf("LoadSettings() with DEVBOX_APPS_TLS=internal: TLS() = false, want true")
	}
	if want := filepath.Join(root, ".local/share/caddy/pki/authorities/local/root.crt"); s.CAFile() != want {
		t.Errorf("CAFile() = %q, want %q", s.CAFile(), want)
	}
}

func TestLoadSettingsForge(t *testing.T) {
	root := t.TempDir()
	l := layout.Layout{Home: root, Root: filepath.Join(root, ".devbox"), Etc: filepath.Join(root, "etc")}
	for env, want := range map[string]string{"": "github", "github": "github", "gitlab": "gitlab"} {
		t.Setenv("DEVBOX_FORGE", env)
		if got := LoadSettings(l).Forge; got != want {
			t.Errorf("LoadSettings() with DEVBOX_FORGE=%q: Forge = %q, want %q", env, got, want)
		}
	}
}

// The developer's ~/.devbox/config.env overrides the machine's settings file.
func TestLoadSettingsUserConfig(t *testing.T) {
	root := t.TempDir()
	l := layout.Layout{Home: root, Root: filepath.Join(root, ".devbox"), Etc: filepath.Join(root, "etc")}
	write(t, root, map[string]string{
		"etc/devbox.env":     "DEVBOX_FORGE=github\nDEVBOX_HOST=box\n",
		".devbox/config.env": "DEVBOX_FORGE=gitlab\n",
	})
	t.Setenv("DEVBOX_FORGE", "")
	t.Setenv("DEVBOX_HOST", "")
	if err := os.Unsetenv("DEVBOX_FORGE"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("DEVBOX_HOST"); err != nil {
		t.Fatal(err)
	}
	s := LoadSettings(l)
	if s.Forge != "gitlab" || s.Host != "box" {
		t.Errorf("Forge, Host = %q, %q; want gitlab (user file), box (machine file)", s.Forge, s.Host)
	}
	t.Setenv("DEVBOX_FORGE", "github")
	if got := LoadSettings(l).Forge; got != "github" {
		t.Errorf("with DEVBOX_FORGE=github in the environment: Forge = %q, want github", got)
	}
}

func checkCaddy26(t *testing.T, got string) {
	t.Helper()
	if strings.Contains(got, "<<") {
		t.Error("Routes() uses heredocs, which Ubuntu's Caddy 2.6 can't read")
	}
	if strings.Count(got, "{") != strings.Count(got, "}") {
		t.Errorf("Routes() has unbalanced braces:\n%s", got)
	}
}

func TestRoutesTLS(t *testing.T) {
	s := Settings{Host: "devbox", AppsDomain: "devbox", AppsTLS: "internal"}
	got := string(s.Routes(Ports{"shop": 4100, "api": 4101}))
	for _, want := range []string{
		"{\n\tauto_https disable_redirects\n\tskip_install_trust\n}\n",
		"http://api.devbox, https://api.devbox {\n\ttls internal\n\treverse_proxy 127.0.0.1:4101\n",
		"http://shop.devbox, https://shop.devbox {\n\ttls internal\n\treverse_proxy 127.0.0.1:4100\n",
		"http://devbox, https://devbox, http://dbs.devbox, https://dbs.devbox, http://artifacts.devbox, https://artifacts.devbox {\n\ttls internal\n\treverse_proxy 127.0.0.1:4099\n",
		// No certificate for the catch-all: clients reject *.devbox.
		"\nhttp://*.devbox {\n\trespond",
		"See https://devbox for the list.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Routes() is missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"auto_https off", "https://*.devbox", "\tredir "} {
		if strings.Contains(got, bad) {
			t.Errorf("Routes() with TLS contains %q:\n%s", bad, got)
		}
	}
	if n := strings.Count(got, "tls internal"); n != 3 {
		t.Errorf("Routes() has tls internal %d times, want 3 (two projects, the pages)", n)
	}
	checkCaddy26(t, got)
}

func TestRoutes(t *testing.T) {
	s := Settings{Host: "devbox", AppsDomain: "devbox", AppsTLS: "off"}
	got := string(s.Routes(Ports{"shop": 4100, "api": 4101}))
	for _, want := range []string{
		"auto_https off",
		"http://api.devbox {\n\treverse_proxy 127.0.0.1:4101\n",
		"http://shop.devbox {\n\treverse_proxy 127.0.0.1:4100\n",
		"ds up shop",
		"http://devbox, http://dbs.devbox, http://artifacts.devbox {\n\treverse_proxy 127.0.0.1:4099\n",
		"http://*.devbox {",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Routes() is missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"https://", "tls internal"} {
		if strings.Contains(got, bad) {
			t.Errorf("Routes() without TLS contains %q:\n%s", bad, got)
		}
	}
	checkCaddy26(t, got)
	if strings.Index(got, "api.devbox") > strings.Index(got, "shop.devbox") {
		t.Error("Routes() should list projects in name order")
	}
}

func TestRoutesNotFoundNamesTheProject(t *testing.T) {
	// Caddy counts a host's labels from the right, from 0: in nope.devbox the
	// project is labels.1, in nope.dev.example.com it is labels.3.
	for _, tt := range []struct{ domain, want string }{
		{"devbox", "{labels.1}"},
		{"devbox.lan", "{labels.2}"},
		{"dev.example.com", "{labels.3}"},
	} {
		got := string(Settings{Host: "devbox", AppsDomain: tt.domain, AppsTLS: "off"}.Routes(Ports{"shop": 4100}))
		if want := "*." + tt.domain + " {\n\trespond \"No project called " + tt.want + "."; !strings.Contains(got, want) {
			t.Errorf("Routes() for %s is missing %q in:\n%s", tt.domain, want, got)
		}
		checkCaddy26(t, got)
	}
}

func TestRoutesSkipsReservedNames(t *testing.T) {
	s := Settings{Host: "devbox", AppsDomain: "devbox", AppsTLS: "off"}
	got := string(s.Routes(Ports{"dbs": 4100, "": 4101, "shop": 4102, "artifacts": 4103}))
	// One dbs.devbox and artifacts.devbox (the devbox pages): Caddy rejects a file that repeats one.
	for _, host := range []string{"dbs.devbox", "artifacts.devbox"} {
		if n := strings.Count(got, host); n != 1 {
			t.Errorf("%s appears %d times, want once:\n%s", host, n, got)
		}
	}
	if strings.Contains(got, "127.0.0.1:4100") || strings.Contains(got, "127.0.0.1:4101") || strings.Contains(got, "127.0.0.1:4103") || !strings.Contains(got, "http://shop.devbox {") {
		t.Errorf("Routes() should skip dbs, artifacts and the empty name and keep shop:\n%s", got)
	}
}

func TestSyncRoutesOnlyWhenSetUp(t *testing.T) {
	root := t.TempDir()
	l := layout.Layout{Home: root, Root: filepath.Join(root, ".devbox")}
	s := Settings{Layout: l, Host: "devbox", AppsDomain: "devbox"}

	// No routes folder yet (names not provisioned): nothing is written.
	if err := s.SyncRoutes(Ports{"shop": 4100}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.RoutesFile()); !os.IsNotExist(err) {
		t.Fatalf("routes written before provisioning: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.RoutesFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncRoutes(Ports{"shop": 4100}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.RoutesFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "http://shop.devbox") {
		t.Errorf("routes file = %s, want shop.devbox", data)
	}
}
