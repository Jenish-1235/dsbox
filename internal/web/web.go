// Package web serves the live devbox pages behind Caddy:
//
//	http://<apps domain>       the developer's projects (running or not), databases, cheatsheet
//	http://dbs.<apps domain>   running databases with connection strings
//
// Pages are rendered on every request from tmux, Docker and the cheatsheet,
// so they're always current. Only the developer's Tailscale network can reach them.
package web

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Jenish-1235/dsbox/internal/devproj"
	"github.com/Jenish-1235/dsbox/internal/dockui"
)

// Server renders the pages.
type Server struct {
	Dev    devproj.Dev
	Docker *dockui.Client
}

// Database is one running database container and how to reach it.
type Database struct {
	Project string
	Service string
	Kind    string
	Image   string
	Lines   []dockui.ConnectLine
}

// ServeHTTP picks the page by host name.
func (s Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/root.crt" {
		s.serveCA(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	host, _, _ := strings.Cut(r.Host, ":")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasPrefix(host, "dbs.") {
		_, _ = w.Write(s.databasesPage(ctx))
		return
	}
	_, _ = w.Write(s.homePage(ctx))
}

// Databases lists running containers whose image ds knows how to connect to.
func (s Server) Databases(ctx context.Context) ([]Database, error) {
	cs, err := s.Docker.Containers(ctx)
	if err != nil {
		return nil, err
	}
	var out []Database
	for _, c := range cs {
		kind := dockui.Kind(c.Image)
		if kind == "" || c.State != "running" {
			continue
		}
		env := map[string]string{}
		if d, err := s.Docker.Inspect(ctx, c.ID); err == nil {
			env = d.Env
		}
		project := c.Project()
		if project == "" {
			project = "other"
		}
		out = append(out, Database{
			Project: project, Service: c.Service(), Kind: kind, Image: c.Image,
			Lines: dockui.Connect(c.Image, env, dockui.Addresses(c.Ports, s.Dev.S.Host)),
		})
	}
	return out, nil
}

// serveCA sends the root certificate of the devbox's CA (Caddy's local CA),
// for importing on a laptop. It's public: only the private key is secret.
func (s Server) serveCA(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.Dev.S.CAFile())
	if err != nil {
		http.Error(w, "No certificate yet. On the devbox: ds cert", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="devbox-root.crt"`)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

func (s Server) homePage(ctx context.Context) []byte {
	var b bytes.Buffer
	head(&b, "devbox")
	b.WriteString("<h1>devbox</h1>\n")
	if s.Dev.S.TLS() {
		b.WriteString("<p class=\"muted\">https links need the devbox certificate, once per laptop: <a href=\"/root.crt\">root.crt</a> · ds cert shows how. Phones: use http://.</p>\n")
	}
	b.WriteString("<h2>Projects</h2>\n<div class=\"card\">\n")
	rows := s.Dev.List(ctx, false)
	if len(rows) == 0 {
		b.WriteString(`<div class="row"><span class="muted">No projects yet. On the devbox: ds new</span></div>` + "\n")
	}
	for _, r := range rows {
		state := `<span class="off">○ stopped</span>`
		if r.Running {
			state = `<span class="on">● running</span>`
		}
		link := `<span class="muted">no link yet: ds up ` + html.EscapeString(r.Name) + `</span>`
		if r.URL != "" {
			u := html.EscapeString(r.URL)
			link = fmt.Sprintf(`<a href="%s">%s</a>`, u, u)
		}
		fmt.Fprintf(&b, "<div class=\"row\"><span>%s %s</span>%s</div>\n", state, html.EscapeString(r.Name), link)
	}
	b.WriteString("</div>\n")

	dbs, err := s.Databases(ctx)
	dbsURL := html.EscapeString(fmt.Sprintf("%s://dbs.%s", s.Dev.S.AppsScheme(), s.Dev.S.AppsDomain))
	switch {
	case err != nil:
		fmt.Fprintf(&b, "<h2>Databases</h2>\n<p class=\"muted\">Docker isn't reachable: %s</p>\n", html.EscapeString(err.Error()))
	default:
		fmt.Fprintf(&b, "<h2>Databases</h2>\n<p><a href=\"%s\">%d running</a> · connection strings on %s</p>\n", dbsURL, len(dbs), dbsURL)
	}

	if cheat, err := os.ReadFile(s.Dev.S.CheatsheetFile()); err == nil {
		b.WriteString("<h2>Cheatsheet</h2>\n<pre>")
		b.WriteString(html.EscapeString(string(cheat)))
		b.WriteString("</pre>\n")
	}
	foot(&b)
	return b.Bytes()
}

func (s Server) databasesPage(ctx context.Context) []byte {
	var b bytes.Buffer
	head(&b, "databases · devbox")
	home := html.EscapeString(fmt.Sprintf("%s://%s", s.Dev.S.AppsScheme(), s.Dev.S.AppsDomain))
	fmt.Fprintf(&b, "<h1>Databases</h1>\n<p class=\"muted\"><a href=\"%s\">← devbox</a> · running database containers; click a line to select it, then copy</p>\n", home)
	dbs, err := s.Databases(ctx)
	if err != nil {
		fmt.Fprintf(&b, "<p class=\"muted\">Docker isn't reachable: %s</p>\n", html.EscapeString(err.Error()))
		foot(&b)
		return b.Bytes()
	}
	if len(dbs) == 0 {
		b.WriteString("<p class=\"muted\">No databases running. In a project: just up (or docker compose up -d).</p>\n")
	}
	project := ""
	for _, d := range dbs {
		if d.Project != project {
			if project != "" {
				b.WriteString("</div>\n")
			}
			project = d.Project
			fmt.Fprintf(&b, "<h2>%s</h2>\n<div class=\"card\">\n", html.EscapeString(project))
		}
		fmt.Fprintf(&b, "<div class=\"db\"><div class=\"row\"><span><span class=\"on\">●</span> %s</span><span class=\"muted\">%s · %s</span></div>\n",
			html.EscapeString(d.Service), html.EscapeString(d.Kind), html.EscapeString(d.Image))
		for _, l := range d.Lines {
			fmt.Fprintf(&b, "<div class=\"conn\"><span class=\"muted\">%s</span><code>%s</code></div>\n",
				html.EscapeString(l.Label), html.EscapeString(l.Value))
		}
		b.WriteString("</div>\n")
	}
	if project != "" {
		b.WriteString("</div>\n")
	}
	foot(&b)
	return b.Bytes()
}

func head(b *bytes.Buffer, title string) {
	fmt.Fprintf(b, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
:root{--bg:#f6f5f2;--card:#fff;--text:#1d1d1f;--muted:#6e6e73;--accent:#5b3fd6;--line:#e6e4df;--on:#1a7f37;color-scheme:light dark}
@media (prefers-color-scheme:dark){:root{--bg:#0f0f12;--card:#18181c;--text:#f2f2f4;--muted:#a1a1aa;--accent:#a594ff;--line:#2a2a31;--on:#3fb950}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px/1.5 ui-sans-serif,system-ui,"Segoe UI",sans-serif}
main{max-width:900px;margin:0 auto;padding:32px 16px}h1{font-size:22px;margin:0 0 8px}h2{font-size:13px;text-transform:uppercase;letter-spacing:.05em;color:var(--muted);margin:28px 0 10px}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;overflow:hidden}
.row{display:flex;justify-content:space-between;gap:16px;padding:10px 16px;border-top:1px solid var(--line)}.card>.row:first-child,.card>.db:first-child>.row{border-top:0}
.db{border-top:1px solid var(--line)}.card>.db:first-child{border-top:0}.db .row{border-top:0}
.conn{display:flex;gap:12px;align-items:baseline;padding:2px 16px 8px 34px}.conn span{min-width:110px;font-size:13px}
code{font:13px ui-monospace,"JetBrains Mono","Cascadia Code",monospace;user-select:all;word-break:break-all}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}.muted{color:var(--muted)}.on{color:var(--on)}.off{color:var(--muted)}
pre{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:16px;overflow-x:auto;font:13px/1.55 ui-monospace,"JetBrains Mono","Cascadia Code",monospace}
</style></head><body><main>
`, html.EscapeString(title))
}

func foot(b *bytes.Buffer) { b.WriteString("</main></body></html>\n") }
