package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/Jenish-1235/dsbox/internal/config"
	"github.com/Jenish-1235/dsbox/internal/devproj"
	"github.com/Jenish-1235/dsbox/internal/layout"
	"github.com/Jenish-1235/dsbox/internal/ui"
)

// cert prints where the devbox's root certificate is and how to trust it on
// a laptop, once, so https://<project>.devbox opens without a warning.
func cert(l layout.Layout, out ui.Printer, w io.Writer) error {
	s := devproj.LoadSettings(l)
	if !s.TLS() {
		out.Warn("https is off (%s=%s)", config.AppsTLS, s.AppsTLS)
		out.Hint("set %s=internal in config/devbox.env, then %s, then ds update", config.AppsTLS, config.Fix)
		return errReported
	}
	file := s.CAFile()
	if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
		out.Fail("No certificate yet; Caddy creates it when it first serves https")
		out.Hint("ds doctor; journalctl -u devbox-web -n 20")
		return errReported
	}
	download := fmt.Sprintf("http://%s/root.crt", s.AppsDomain)
	fmt.Fprintf(w, `Root certificate: %s
Download:         %s

Trust it once per laptop (Chrome and Edge use the system's list; restart them after):
  Windows, PowerShell (confirm the dialog):
    Invoke-WebRequest %s -OutFile devbox-root.crt
    Import-Certificate -FilePath devbox-root.crt -CertStoreLocation Cert:\CurrentUser\Root
  macOS:
    curl -o devbox-root.crt %s
    security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db devbox-root.crt
  Firefox has its own list: Settings → Privacy & Security → Certificates →
    View Certificates → Authorities → Import → devbox-root.crt → trust for websites

Devices without it (phones) keep using http://<project>.%s.
`, file, download, download, download, s.AppsDomain)
	return nil
}
