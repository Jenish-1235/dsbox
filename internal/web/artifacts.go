package web

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Jenish-1235/dsbox/internal/artifact"
)

// The artifact hub, artifacts.<apps domain>:
//
//	/                        every artifact, newest first, filterable by project and text
//	/<project>/<slug>        the viewer: a top bar and the artifact in a sandboxed iframe
//	/<project>/<slug>/raw    the artifact itself, sandboxed by its CSP header
//
// It only reads. ds artifact submit and rm write to the folder directly.

const artifactsCSS = `.tools{display:flex;flex-wrap:wrap;gap:8px;margin:16px 0}
.tools input,.tools select{font:inherit;color:var(--text);background:var(--card);border:1px solid var(--line);border-radius:8px;padding:8px 12px;min-width:0;max-width:100%}
.tools input{flex:1 1 220px}.tools select{flex:0 1 auto}
a.art{display:block;padding:12px 16px;border-top:1px solid var(--line);color:var(--text)}.card>a.art:first-child{border-top:0}
a.art:hover{background:var(--bg);text-decoration:none}a.art[hidden]{display:none}
.art b{display:block;font-weight:600;overflow-wrap:anywhere}.art small{color:var(--muted);font-size:13px}.art .p{color:var(--accent)}
`

const viewerCSS = `html,body{height:100%}main{max-width:none;height:100%;padding:0;display:flex;flex-direction:column}
.bar{display:flex;align-items:center;gap:12px;padding:8px 16px;background:var(--card);border-bottom:1px solid var(--line)}
.bar .t{flex:1;min-width:0}.bar b,.bar small{display:block;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.bar small{color:var(--muted);font-size:12px}.bar a{white-space:nowrap}
iframe{flex:1;width:100%;border:0;background:#fff}
`

// filterJS hides the rows that don't match the project and the text, and keeps
// the project in the address (?project=) so a filtered list can be shared.
const filterJS = `<script>
(()=>{const q=document.getElementById("q"),p=document.getElementById("p"),none=document.getElementById("none"),rows=[...document.querySelectorAll("a.art")];
function f(){const t=q.value.trim().toLowerCase(),pr=p.value;let n=0;
for(const r of rows){const ok=(!pr||r.dataset.project===pr)&&(!t||r.dataset.q.includes(t));r.hidden=!ok;if(ok)n++}
none.hidden=n>0||rows.length===0;const u=new URL(location.href);if(pr)u.searchParams.set("project",pr);else u.searchParams.delete("project");history.replaceState(null,"",u)}
q.addEventListener("input",f);p.addEventListener("change",f);f()})();
</script>
`

func (s Server) serveArtifacts(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	if path == "" {
		writeHTML(w, http.StatusOK, s.artifactsPage(r.URL.Query().Get("project"), time.Now()))
		return
	}
	parts := strings.Split(path, "/")
	switch {
	case len(parts) == 2:
		s.artifactViewer(w, parts[0], parts[1])
	case len(parts) == 3 && parts[2] == "raw":
		s.artifactRaw(w, r, parts[0], parts[1])
	default:
		s.artifactNotFound(w, path)
	}
}

func writeHTML(w http.ResponseWriter, code int, page []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(page)
}

// artifactRaw sends the artifact's own HTML. The CSP sandbox gives it an
// opaque origin whether it's shown in the viewer or opened on its own, so its
// scripts can't read the hub, other artifacts or other devbox pages.
func (s Server) artifactRaw(w http.ResponseWriter, r *http.Request, project, slug string) {
	a, f, err := s.Artifacts.Open(project, slug)
	if err != nil {
		s.artifactError(w, project+"/"+slug, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Security-Policy", artifact.Sandbox)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", a.Updated, f)
}

func (s Server) artifactViewer(w http.ResponseWriter, project, slug string) {
	a, err := s.Artifacts.Get(project, slug)
	if err != nil {
		s.artifactError(w, project+"/"+slug, err)
		return
	}
	var b bytes.Buffer
	title := html.EscapeString(a.Title)
	raw := html.EscapeString("/" + url.PathEscape(a.Project) + "/" + url.PathEscape(a.Slug) + "/raw")
	headCSS(&b, a.Title+" · artifacts", viewerCSS)
	fmt.Fprintf(&b, `<div class="bar"><a href="/" title="All artifacts">← all</a><div class="t"><b>%s</b><small>%s · updated %s</small></div><a href="%s" target="_blank" rel="noopener">open raw ↗</a></div>
<iframe src="%s" sandbox="%s" title="%s"></iframe>
`, title, html.EscapeString(a.Project), artifact.Ago(a.Updated, time.Now()), raw, raw, strings.TrimPrefix(artifact.Sandbox, "sandbox "), title)
	foot(&b)
	writeHTML(w, http.StatusOK, b.Bytes())
}

func (s Server) artifactError(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, artifact.ErrNotFound) {
		s.artifactNotFound(w, id)
		return
	}
	http.Error(w, "Can't read the artifacts: "+err.Error(), http.StatusInternalServerError)
}

func (s Server) artifactNotFound(w http.ResponseWriter, id string) {
	var b bytes.Buffer
	head(&b, "not found · artifacts")
	fmt.Fprintf(&b, "<h1>No artifact %s</h1>\n<p class=\"muted\"><a href=\"/\">← all artifacts</a> · on the devbox, ds artifact ls lists them</p>\n", html.EscapeString(id))
	foot(&b)
	writeHTML(w, http.StatusNotFound, b.Bytes())
}

func (s Server) artifactsPage(project string, now time.Time) []byte {
	var b bytes.Buffer
	headCSS(&b, "artifacts · devbox", artifactsCSS)
	home := html.EscapeString(fmt.Sprintf("%s://%s", s.Dev.S.AppsScheme(), s.Dev.S.AppsDomain))
	fmt.Fprintf(&b, "<h1>Artifacts</h1>\n<p class=\"muted\"><a href=\"%s\">← devbox</a> · design docs, reports and dashboards from every project, newest first</p>\n", home)
	arts, err := s.Artifacts.List()
	if err != nil {
		fmt.Fprintf(&b, "<p class=\"muted\">Can't read the artifacts: %s</p>\n", html.EscapeString(err.Error()))
		foot(&b)
		return b.Bytes()
	}
	if len(arts) == 0 {
		b.WriteString("<p class=\"muted\">No artifacts yet. On the devbox: ds artifact submit report.html --title \"Report\"</p>\n")
		foot(&b)
		return b.Bytes()
	}
	counts := map[string]int{}
	for _, a := range arts {
		counts[a.Project]++
	}
	projects := make([]string, 0, len(counts))
	for p := range counts {
		projects = append(projects, p)
	}
	slices.Sort(projects)

	b.WriteString("<div class=\"tools\"><input id=\"q\" type=\"search\" placeholder=\"Filter by title\" aria-label=\"Filter by title\" autocomplete=\"off\">\n<select id=\"p\" aria-label=\"Project\"><option value=\"\">All projects</option>\n")
	for _, p := range projects {
		sel := ""
		if p == project {
			sel = " selected"
		}
		fmt.Fprintf(&b, "<option value=\"%s\"%s>%s (%d)</option>\n", html.EscapeString(p), sel, html.EscapeString(p), counts[p])
	}
	b.WriteString("</select></div>\n<div class=\"card\">\n")
	for _, a := range arts {
		q := strings.ToLower(a.Title + " " + a.Project + " " + a.Slug)
		fmt.Fprintf(&b, "<a class=\"art\" href=\"/%s/%s\" data-project=\"%s\" data-q=\"%s\"><b>%s</b><small><span class=\"p\">%s</span> · <time datetime=\"%s\" title=\"%s\">%s</time></small></a>\n",
			url.PathEscape(a.Project), url.PathEscape(a.Slug), html.EscapeString(a.Project), html.EscapeString(q),
			html.EscapeString(a.Title), html.EscapeString(a.Project),
			a.Updated.Format(time.RFC3339), a.Updated.Local().Format("2 Jan 2006 15:04"), artifact.Ago(a.Updated, now))
	}
	b.WriteString("</div>\n<p id=\"none\" class=\"muted\" hidden>Nothing matches.</p>\n")
	b.WriteString(filterJS)
	foot(&b)
	return b.Bytes()
}
