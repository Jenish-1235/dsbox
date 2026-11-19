package dockui

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// fakeDocker serves h on a unix socket and returns a Client for it.
func fakeDocker(t *testing.T, h http.Handler) *Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return NewClient(sock)
}

const containersJSON = `[
 {"Id":"aaaaaaaaaaaaaaaa","Names":["/shop-db-1"],"Image":"postgres:18","State":"running","Status":"Up 2 hours (healthy)",
  "Ports":[{"IP":"0.0.0.0","PrivatePort":5432,"PublicPort":5433,"Type":"tcp"},{"IP":"::","PrivatePort":5432,"PublicPort":5433,"Type":"tcp"}],
  "Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"db","com.docker.compose.project.working_dir":"/home/alex/projects/shop"}},
 {"Id":"bbbbbbbbbbbbbbbb","Names":["/shop-api-1"],"Image":"shop-api","State":"exited","Status":"Exited (1) 5 minutes ago",
  "Ports":[],"Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"api"}},
 {"Id":"cccccccccccccccc","Names":["/scratch"],"Image":"redis:8","State":"running","Status":"Up About a minute",
  "Ports":[{"IP":"127.0.0.1","PrivatePort":6379,"PublicPort":6380,"Type":"tcp"}],"Labels":{}},
 {"Id":"dddddddddddddddd","Names":["/blog-web-1"],"Image":"nginx","State":"running","Status":"Up 3 days",
  "Ports":[{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8080,"Type":"tcp"}],"Labels":{"com.docker.compose.project":"blog","com.docker.compose.service":"web"}}
]`

func TestClient(t *testing.T) {
	var posted []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1.45/containers/json", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "1" {
			t.Errorf("containers/json all = %q, want 1", r.URL.Query().Get("all"))
		}
		_, _ = io.WriteString(w, containersJSON)
	})
	mux.HandleFunc("GET /v1.45/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "missing" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"No such container: missing"}`)
			return
		}
		_, _ = io.WriteString(w, `{"Config":{"Env":["POSTGRES_PASSWORD=x","PATH=/bin","EMPTY="],"Tty":false},"State":{"StartedAt":"2026-11-18T10:00:00Z"}}`)
	})
	mux.HandleFunc("POST /v1.45/containers/{id}/{verb}", func(w http.ResponseWriter, r *http.Request) {
		posted = append(posted, r.PathValue("verb")+" "+r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1.45/containers/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tail") != "500" {
			t.Errorf("logs tail = %q, want 500", r.URL.Query().Get("tail"))
		}
		for i, s := range []string{"one\n", "two\nthree\n"} {
			hdr := make([]byte, 8)
			hdr[0] = byte(1 + i%2)
			binary.BigEndian.PutUint32(hdr[4:], uint32(len(s)))
			_, _ = w.Write(hdr)
			_, _ = io.WriteString(w, s)
		}
	})
	mux.HandleFunc("GET /v1.45/system/df", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"LayersSize":1000,"Images":[{"Id":"sha256:1","RepoTags":["postgres:18"],"Size":400000000,"Containers":1}],
		  "Volumes":[{"Name":"shop_pg","UsageData":{"Size":2048,"RefCount":1}}],"BuildCache":[{"ID":"x","Size":5000,"InUse":false}]}`)
	})
	mux.HandleFunc("POST /v1.45/images/prune", func(w http.ResponseWriter, r *http.Request) {
		if f := r.URL.Query().Get("filters"); f != `{"dangling":["true"]}` {
			t.Errorf("images/prune filters = %q", f)
		}
		_, _ = io.WriteString(w, `{"SpaceReclaimed":100}`)
	})
	mux.HandleFunc("POST /v1.45/build/prune", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"SpaceReclaimed":23}`)
	})
	c := fakeDocker(t, mux)
	ctx := t.Context()

	cs, err := c.Containers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 4 || cs[0].Service() != "db" || cs[2].Service() != "scratch" || cs[0].Project() != "shop" {
		t.Fatalf("Containers = %+v", cs)
	}

	d, err := c.Inspect(ctx, "aaa")
	if err != nil {
		t.Fatal(err)
	}
	if d.Env["POSTGRES_PASSWORD"] != "x" || d.TTY || d.StartedAt.IsZero() {
		t.Errorf("Inspect = %+v", d)
	}
	_, err = c.Inspect(ctx, "missing")
	if err == nil || !strings.Contains(err.Error(), "No such container") {
		t.Errorf("Inspect(missing) err = %v", err)
	}

	for _, f := range []func() error{
		func() error { return c.Restart(ctx, "a") },
		func() error { return c.Stop(ctx, "b") },
		func() error { return c.Start(ctx, "c") },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(posted, ","); got != "restart a,stop b,start c" {
		t.Errorf("posted = %s", got)
	}

	r, err := c.Logs(ctx, "a", 500, false, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	_ = r.Close()
	if string(b) != "one\ntwo\nthree\n" {
		t.Errorf("Logs = %q", b)
	}

	du, err := c.DiskUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(du.Images) != 1 || du.Volumes[0].Name != "shop_pg" || du.BuildCache[0].Size != 5000 {
		t.Errorf("DiskUsage = %+v", du)
	}
	if n, err := c.PruneDangling(ctx); err != nil || n != 123 {
		t.Errorf("PruneDangling = %d, %v; want 123", n, err)
	}
}

func TestDemux(t *testing.T) {
	var buf bytes.Buffer
	for _, s := range []string{"hello ", "world\n"} {
		hdr := make([]byte, 8)
		hdr[0] = 1
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(s)))
		buf.Write(hdr)
		buf.WriteString(s)
	}
	got, err := io.ReadAll(newDemux(io.NopCloser(&buf)))
	if err != nil || string(got) != "hello world\n" {
		t.Errorf("demux = %q, %v", got, err)
	}
}

func TestGroupByProject(t *testing.T) {
	c := fakeDocker(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, containersJSON) }))
	cs, err := c.Containers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	gs := GroupByProject(cs)
	var got []string
	for _, g := range gs {
		var svcs []string
		for _, c := range g.Containers {
			svcs = append(svcs, c.Service())
		}
		got = append(got, g.Name+":"+strings.Join(svcs, ","))
	}
	if want := "blog:web shop:api,db Other:scratch"; strings.Join(got, " ") != want {
		t.Errorf("GroupByProject = %s, want %s", strings.Join(got, " "), want)
	}
	if gs[1].Dir != "/home/alex/projects/shop" || gs[1].Running() != 1 {
		t.Errorf("shop group = %+v", gs[1])
	}
	if f := Filter(gs, "postgres"); len(f) != 1 || f[0].Name != "shop" || len(f[0].Containers) != 1 {
		t.Errorf("Filter(postgres) = %+v", f)
	}
	if f := Filter(gs, "SHOP"); len(f) != 1 || len(f[0].Containers) != 2 {
		t.Errorf("Filter(SHOP) = %+v", f)
	}
}

func TestAddresses(t *testing.T) {
	ports := []Port{
		{IP: "0.0.0.0", PrivatePort: 5432, PublicPort: 5433, Type: "tcp"},
		{IP: "::", PrivatePort: 5432, PublicPort: 5433, Type: "tcp"},
		{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 80, Type: "tcp"},
		{IP: "127.0.0.1", PrivatePort: 9000, PublicPort: 9001, Type: "tcp"},
		{IP: "0.0.0.0", PrivatePort: 53, PublicPort: 5353, Type: "udp"},
		{PrivatePort: 6379, Type: "tcp"}, // exposed, not published
	}
	var got []string
	for _, a := range Addresses(ports, "devbox") {
		got = append(got, a.String())
	}
	want := "devbox:5353→53/udp devbox:80 devbox:5433→5432 localhost:9001→9000"
	if strings.Join(got, " ") != want {
		t.Errorf("Addresses = %s, want %s", strings.Join(got, " "), want)
	}
}

func TestParseStatus(t *testing.T) {
	for _, tc := range []struct{ state, status, label, age string }{
		{"running", "Up 2 hours (healthy)", "healthy", "2h"},
		{"running", "Up About a minute", "running", "1m"},
		{"running", "Up 5 seconds (health: starting)", "starting", "5s"},
		{"running", "Up 3 weeks (unhealthy)", "unhealthy", "3w"},
		{"exited", "Exited (0) 5 minutes ago", "exited", "5m ago"},
		{"exited", "Exited (137) About an hour ago", "exited 137", "1h ago"},
		{"created", "Created", "created", ""},
	} {
		s := ParseStatus(tc.state, tc.status)
		if s.Label != tc.label || s.Age != tc.age {
			t.Errorf("ParseStatus(%q, %q) = %q %q, want %q %q", tc.state, tc.status, s.Label, s.Age, tc.label, tc.age)
		}
	}
}

func TestConnect(t *testing.T) {
	pub := func(priv, public int) []Address {
		return []Address{{Host: "devbox", Public: public, Private: priv, Proto: "tcp"}}
	}
	for _, tc := range []struct {
		image string
		env   map[string]string
		addrs []Address
		want  string
	}{
		{"postgres:18", map[string]string{"POSTGRES_PASSWORD": "x"}, pub(5432, 5499), "postgres://postgres:x@devbox:5499/postgres"},
		{"docker.io/library/postgres:16-alpine", map[string]string{"POSTGRES_USER": "app", "POSTGRES_PASSWORD": "p@ss", "POSTGRES_DB": "shop"},
			pub(5432, 5433), "postgres://app:p%40ss@devbox:5433/shop"},
		{"mysql:8", map[string]string{"MYSQL_ROOT_PASSWORD": "r", "MYSQL_DATABASE": "d"}, pub(3306, 3307), "mysql://root:r@devbox:3307/d"},
		{"mariadb:11", map[string]string{"MARIADB_USER": "u", "MARIADB_PASSWORD": "p", "MARIADB_DATABASE": "d"}, pub(3306, 3306), "mysql://u:p@devbox:3306/d"},
		{"redis:8", nil, pub(6379, 6380), "redis://devbox:6380"},
		{"bitnami/redis", map[string]string{"REDIS_PASSWORD": "pw"}, pub(6379, 6379), "redis://:pw@devbox:6379"},
		{"mongo:8", map[string]string{"MONGO_INITDB_ROOT_USERNAME": "root", "MONGO_INITDB_ROOT_PASSWORD": "pw"}, pub(27017, 27018),
			"mongodb://root:pw@devbox:27018/?authSource=admin"},
		{"rabbitmq:4-management", nil, append(pub(5672, 5672), pub(15672, 15673)...), "http://devbox:15673"},
		{"nginx", nil, pub(80, 8080), "devbox:8080"},
		{"postgres:18", nil, nil, "port 5432 isn't published; add ports: [\"<host>:5432\"] to reach it"},
	} {
		lines := Connect(tc.image, tc.env, tc.addrs)
		if len(lines) == 0 || lines[0].CopyValue() != tc.want {
			t.Errorf("Connect(%s) = %+v, want first %q", tc.image, lines, tc.want)
		}
	}
	if l := Connect("rabbitmq:4", nil, append(pub(5672, 5672), pub(15672, 15673)...)); l[1].Value != "amqp://guest:guest@devbox:5672" {
		t.Errorf("rabbitmq amqp = %+v", l)
	}
}

// A port published on the VM's 127.0.0.1 only isn't offered as a laptop URL.
func TestConnectLoopbackOnly(t *testing.T) {
	loop := func(priv, public int) []Port {
		return []Port{{IP: "127.0.0.1", PrivatePort: priv, PublicPort: public, Type: "tcp"}}
	}
	both := append(loop(5432, 5432), Port{IP: "0.0.0.0", PrivatePort: 5432, PublicPort: 5432, Type: "tcp"})
	for _, tc := range []struct {
		image string
		ports []Port
		want  string
		note  bool
	}{
		{"postgres:18", loop(5432, 5433), `port 5432 is published on the VM's localhost only; use ports: ["5433:5432"] to reach it from the laptop`, true},
		{"redis:8", loop(6379, 6379), `port 6379 is published on the VM's localhost only; use ports: ["6379:6379"] to reach it from the laptop`, true},
		{"rabbitmq:4", loop(5672, 5672), `port 5672 is published on the VM's localhost only; use ports: ["5672:5672"] to reach it from the laptop`, true},
		{"rabbitmq:4-management", append(loop(15672, 15672), Port{IP: "0.0.0.0", PrivatePort: 5672, PublicPort: 5672, Type: "tcp"}),
			"amqp://guest:guest@devbox:5672", false},
		{"postgres:18", both, "postgres://postgres@devbox:5432/postgres", false},
		{"postgres:18", nil, `port 5432 isn't published; add ports: ["<host>:5432"] to reach it`, true},
	} {
		lines := Connect(tc.image, nil, Addresses(tc.ports, "devbox"))
		if len(lines) == 0 || lines[0].Value != tc.want || lines[0].Note != tc.note {
			t.Errorf("Connect(%s, %+v) = %+v, want first %q (note %v)", tc.image, tc.ports, lines, tc.want, tc.note)
		}
		for _, l := range lines {
			if !l.Note && strings.Contains(l.Value, "localhost") && !strings.Contains(l.Label, "VM only") {
				t.Errorf("Connect(%s): %+v offers a VM-only address without saying so", tc.image, l)
			}
		}
	}

	// A note isn't copied.
	m := New(nil, "devbox")
	if cmd := m.copy(Connect("postgres:18", nil, Addresses(loop(5432, 5432), "devbox"))[0]); cmd != nil || !strings.Contains(m.status, "localhost only") {
		t.Errorf("copy(note) = %v, status %q; want no clipboard and the note shown", cmd, m.status)
	}
}

// A connect result that arrives after the user moved to another screen leaves it alone.
func TestConnectMsgStale(t *testing.T) {
	msg := connectMsg{title: "db", lines: []ConnectLine{{Label: "postgres", Value: "postgres://devbox:5432/db"}}}
	for _, tc := range []struct {
		name string
		mode mode
		tab  tab
		want mode
	}{
		{"list", modeList, tabContainers, modeConnect},
		{"logs", modeLogs, tabContainers, modeLogs},
		{"filter", modeFilter, tabContainers, modeFilter},
		{"confirm", modeConfirm, tabContainers, modeConfirm},
		{"disk tab", modeList, tabDisk, modeList},
	} {
		m := New(nil, "devbox")
		m.mode, m.tab = tc.mode, tc.tab
		next, _ := m.Update(msg)
		if got := next.(Model).mode; got != tc.want {
			t.Errorf("%s: mode after connectMsg = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Following logs shows the newest line, also after a resize.
func TestLogsShowNewestLine(t *testing.T) {
	m := New(nil, "devbox")
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = next.(Model)
	next, _ = m.openLogs(Container{ID: "a", Names: []string{"/web"}})
	m = next.(Model)
	defer m.closeLogs()
	var lines []string
	for i := range 40 {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}
	m.appendLogs(lines)
	for _, h := range []int{12, 7, 30} {
		next, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: h})
		m = next.(Model)
		m.logs.vp.GotoBottom()
		out := m.render()
		if !strings.Contains(out, "line 39") {
			t.Errorf("height %d: newest line missing:\n%s", h, out)
		}
		if n := len(strings.Split(out, "\n")); n != h {
			t.Errorf("height %d: rendered %d lines", h, n)
		}
	}
}

func TestDumpLayout(t *testing.T) {
	c := fakeDocker(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, containersJSON) }))
	for _, width := range []int{80, 160} {
		out, err := Dump(t.Context(), c, "devbox", width)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if n := len([]rune(line)); n > width {
				t.Errorf("width %d: line is %d wide: %q", width, n, line)
			}
		}
		for _, want := range []string{"shop", "devbox:5433→5432", "exited 1", "Other", "localhost:6380→6379"} {
			if !strings.Contains(out, want) {
				t.Errorf("width %d: dump lacks %q:\n%s", width, want, out)
			}
		}
	}
}

func press(t *testing.T, m Model, keys ...tea.KeyPressMsg) Model {
	t.Helper()
	for _, k := range keys {
		next, _ := m.Update(k)
		m = next.(Model)
	}
	return m
}

func char(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func TestModelKeys(t *testing.T) {
	c := fakeDocker(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, containersJSON) }))
	cs, err := c.Containers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	next, _ := New(c, "devbox").Update(containersMsg{cs: cs})
	m := next.(Model)
	if ct, ok := m.selected(); !ok || ct.Service() != "web" {
		t.Fatalf("first selection = %v %v, want web", ct.Service(), ok)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if g, _ := m.selectedGroup(); g.Name != "shop" {
		t.Errorf("tab went to %s, want shop", g.Name)
	}
	m = press(t, m, char('j'), char('j'), char('D'))
	if m.mode != modeConfirm || !strings.Contains(m.confirm.question, "remove db") {
		t.Errorf("D: mode %v question %q", m.mode, m.confirm.question)
	}
	m = press(t, m, char('n'))
	if m.mode != modeList || m.status != "cancelled" {
		t.Errorf("n: mode %v status %q", m.mode, m.status)
	}
	m = press(t, m, char('/'), char('r'), char('e'), char('d'), tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.groups) != 1 || m.groups[0].Name != OtherGroup {
		t.Errorf("filter red: groups %+v", m.groups)
	}
	m = press(t, m, char('2'))
	if m.tab != tabDisk || !strings.Contains(m.render(), "prune dangling") {
		t.Errorf("2: tab %v", m.tab)
	}
}
