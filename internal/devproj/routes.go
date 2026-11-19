package devproj

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// PagesAddr is where `ds serve` listens: the live pages for the bare apps
// domain (http://devbox) and dbs.<apps domain>.
const PagesAddr = "127.0.0.1:4099"

// RoutesFile is the Caddyfile the devbox-web service runs: one site per project,
// <name>.<apps domain> → 127.0.0.1:<port>. Caddy reloads it when it changes.
func (s Settings) RoutesFile() string {
	return filepath.Join(s.Layout.StateDir(), "caddy", "Caddyfile")
}

// CheatsheetFile is the cheatsheet installed by the welcome component.
func (s Settings) CheatsheetFile() string {
	return filepath.Join(s.Layout.Root, "share", "cheatsheet.txt")
}

// Routes renders the Caddyfile for every project that has a port. Projects
// that aren't running answer with how to start them instead of an error page.
// It sticks to Caddyfile syntax that Ubuntu's Caddy (2.6) understands.
//
// With TLS, each site answers on http and https: Caddy's local CA signs the
// certificates (tls internal), http isn't redirected (phones that don't trust
// the CA keep using it), and Caddy doesn't try to add its CA to the system
// trust store (it runs as the developer, without root). The catch-all for
// unknown names stays http only: browsers and curl reject a *.devbox
// certificate (a wildcard right under the top-level name).
func (s Settings) Routes(ports Ports) []byte {
	var b bytes.Buffer
	scheme, domain, tls := s.AppsScheme(), s.AppsDomain, s.TLS()
	b.WriteString("# Written by ds from ~/.devbox/state/ports.json; edits are overwritten.\n")
	if tls {
		b.WriteString("{\n\tauto_https disable_redirects\n\tskip_install_trust\n}\n")
	} else {
		b.WriteString("{\n\tauto_https off\n}\n")
	}
	// site writes the address line of a site block for hosts.
	site := func(hosts ...string) {
		var addrs []string
		for _, h := range hosts {
			if tls {
				addrs = append(addrs, "http://"+h, "https://"+h)
			} else {
				addrs = append(addrs, scheme+"://"+h)
			}
		}
		fmt.Fprintf(&b, "\n%s {\n", strings.Join(addrs, ", "))
		if tls {
			b.WriteString("\ttls internal\n")
		}
	}
	names := make([]string, 0, len(ports))
	for name := range ports {
		// A reserved or empty name would repeat a devbox address, and Caddy
		// rejects the whole file over it.
		if ValidName(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		site(name + "." + domain)
		fmt.Fprintf(&b, "\treverse_proxy 127.0.0.1:%d\n", ports[name])
		b.WriteString("\thandle_errors {\n")
		fmt.Fprintf(&b, "\t\trespond \"%s isn't running. Start it on the devbox with: ds up %s\" 502\n", name, name)
		b.WriteString("\t}\n}\n")
	}
	// The devbox pages: projects + cheatsheet, and the databases.
	site(domain, "dbs."+domain)
	fmt.Fprintf(&b, "\treverse_proxy %s\n", PagesAddr)
	b.WriteString("\thandle_errors {\n")
	b.WriteString("\t\trespond \"The devbox pages aren't running. On the devbox: ds doctor\" 502\n")
	b.WriteString("\t}\n}\n")
	catchAll := scheme
	if tls {
		catchAll = "http"
	}
	fmt.Fprintf(&b, "\n%s://*.%s {\n", catchAll, domain)
	// Caddy numbers the host's labels from the right, starting at 0, so the
	// project label comes right after the apps domain's own labels.
	project := strings.Count(domain, ".") + 1
	fmt.Fprintf(&b, "\trespond \"No project called {labels.%d}. See %s://%s for the list.\" 404\n}\n", project, scheme, domain)
	return b.Bytes()
}

// SyncRoutes rewrites the routes file when the projects changed. It does
// nothing until an apps domain is set, or when names aren't set up (no
// routes folder from the provision step "names").
func (s Settings) SyncRoutes(ports Ports) error {
	if s.AppsDomain == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Dir(s.RoutesFile())); err != nil {
		return nil
	}
	return writeIfChanged(s.RoutesFile(), s.Routes(ports))
}

// SyncAllRoutes loads the saved ports and syncs; for ds apply.
func (s Settings) SyncAllRoutes() error {
	ports, err := LoadPorts(s.PortsFile())
	if err != nil {
		return err
	}
	return s.SyncRoutes(ports)
}

func writeIfChanged(path string, want []byte) error {
	if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, want) {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, want, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}
