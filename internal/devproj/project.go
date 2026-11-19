package devproj

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jenish-1235/dsbox/internal/ui"
)

// ErrReported means the problem was already printed.
var ErrReported = errors.New("reported")

// Project is one folder dev can run.
type Project struct {
	Name string
	Dir  string
}

// Dev runs the dev commands.
type Dev struct {
	S   Settings
	Out ui.Printer
	// W gets progress dots and raw output.
	W io.Writer
}

// Resolve finds the project arg names: a project name, a path, or (when arg
// is empty) the project containing the current directory.
func (d Dev) Resolve(arg string) (Project, error) {
	if arg == "" || arg == "." || strings.ContainsRune(arg, os.PathSeparator) {
		dir := arg
		if dir == "" {
			dir = "."
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return Project{}, fmt.Errorf("find project: %w", err)
		}
		if arg == "" {
			abs = d.projectRoot(abs)
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return Project{}, fmt.Errorf("no project folder at %s", abs)
		}
		name := Name(filepath.Base(abs))
		if err := checkName(name, filepath.Base(abs)); err != nil {
			return Project{}, err
		}
		return Project{Name: name, Dir: abs}, nil
	}
	if err := checkName(Name(arg), arg); err != nil {
		return Project{}, err
	}
	dir := filepath.Join(d.S.ProjectsDir, arg)
	if _, err := os.Stat(dir); err != nil {
		// Folder names can differ from project names (My_App → my-app).
		if p, ok := d.byName(arg); ok {
			return p, nil
		}
		return Project{Name: Name(arg), Dir: dir}, nil
	}
	return Project{Name: Name(arg), Dir: dir}, nil
}

// checkName rejects a project name made from folder that is empty. A reserved
// name still resolves, so a project made before names were checked can be
// stopped, attached to and read; Up refuses to start it.
func checkName(name, folder string) error {
	if name == "" {
		return fmt.Errorf("can't make a project name from %q; rename the folder", folder)
	}
	return nil
}

func (d Dev) byName(name string) (Project, bool) {
	entries, _ := os.ReadDir(d.S.ProjectsDir)
	for _, e := range entries {
		if e.IsDir() && Name(e.Name()) == name {
			return Project{Name: name, Dir: filepath.Join(d.S.ProjectsDir, e.Name())}, true
		}
	}
	return Project{}, false
}

// projectRoot is the top folder under the projects dir that contains dir, or
// the nearest git root, or dir itself.
func (d Dev) projectRoot(dir string) string {
	if rel, err := filepath.Rel(d.S.ProjectsDir, dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		first, _, _ := strings.Cut(rel, string(os.PathSeparator))
		return filepath.Join(d.S.ProjectsDir, first)
	}
	for p := dir; ; p = filepath.Dir(p) {
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return p
		}
		if filepath.Dir(p) == p {
			return dir
		}
	}
}

// Up starts p in tmux, or reports where it already runs. Either way p is
// recorded as wanted, so ds restore starts it again after a reboot.
func (d Dev) Up(ctx context.Context, p Project) error {
	if Reserved(p.Name) {
		// A project called dbs would clash with dbs.<apps domain>.
		return fmt.Errorf("%q can't be a project name: the devbox uses %s.<apps domain> for its own page; rename the folder", filepath.Base(p.Dir), p.Name)
	}
	if Running(ctx)[p.Name] {
		d.want(p)
		if u, ok := d.URL(p); ok {
			d.Out.OK("%s is running → %s", p.Name, u)
		} else {
			d.Out.OK("%s is running (ds attach %s to see it)", p.Name, p.Name)
		}
		return nil
	}
	plan, err := Detect(p.Dir)
	if errors.Is(err, ErrNoStartCommand) {
		d.Out.Fail("Don't know how to start %s", p.Name)
		d.Out.Hint("put the start command in %s/.devrc, e.g.: pnpm dev --port $PORT --host 0.0.0.0", p.Dir)
		d.Out.Hint("ds sets PORT and HOST=0.0.0.0; the server must listen on them")
		return ErrReported
	}
	if err != nil {
		return err
	}
	ports, err := LoadPorts(d.S.PortsFile())
	if err != nil {
		return err
	}
	port, isNew, err := ports.Assign(p.Name, d.S.PortMin, d.S.PortMax, PortFree)
	if err != nil {
		return err
	}
	if isNew {
		if err := ports.Save(d.S.PortsFile()); err != nil {
			return err
		}
	}
	// Give the project its name (<name>.<apps domain>) before it starts.
	if err := d.S.SyncRoutes(ports); err != nil {
		d.Out.Warn("Couldn't update project names: %v", err)
	}
	url := d.S.URL(p.Name, port)
	if !PortFree(port) {
		d.Out.Fail("Port %d for %s is in use by something dev didn't start", port, p.Name)
		d.Out.Hint("find it with: ss -ltnp 'sport = :%d'", port)
		return ErrReported
	}
	log := d.S.LogFile(p.Name)
	if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
		return fmt.Errorf("create logs folder: %w", err)
	}
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		return fmt.Errorf("clear log %s: %w", log, err)
	}
	if err := startWindow(ctx, p.Name, p.Dir, plan.Command, log, d.env(port)); err != nil {
		return err
	}
	d.want(p)
	fmt.Fprintf(d.W, "Starting %s: %s %s\n", p.Name, plan.Command, d.Out.Dim("(from "+plan.Source+", port "+strconv.Itoa(port)+")"))
	return d.wait(ctx, p, port, url, 60*time.Second)
}

func (d Dev) env(port int) []string {
	env := []string{
		"PORT=" + strconv.Itoa(port),
		"HOST=0.0.0.0",
		// Vite and Next.js refuse requests for other host names than
		// localhost. The ADLC templates read DEV_ALLOWED_HOSTS; plain Vite
		// reads its own variable.
		"DEV_ALLOWED_HOSTS=" + d.S.AllowedHosts(),
		"__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=" + d.allowedHost(),
	}
	if path := os.Getenv("PATH"); d.S.Layout.Home != "" {
		shims := filepath.Join(d.S.Layout.Home, ".local", "share", "mise", "shims")
		env = append(env, "PATH="+shims+string(os.PathListSeparator)+path)
	}
	return env
}

func (d Dev) allowedHost() string {
	if d.S.AppsDomain != "" {
		return "." + d.S.AppsDomain
	}
	return d.S.Host
}

// wait polls the port until it accepts connections, the window closes, or
// the timeout passes.
func (d Dev) wait(ctx context.Context, p Project, port int, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	dots := 0
	for i := 0; ; i++ {
		if listening(port) {
			if dots > 0 {
				fmt.Fprintln(d.W)
			}
			d.Out.OK("%s is running → %s", p.Name, url)
			return nil
		}
		if i%4 == 3 {
			if !Running(ctx)[p.Name] {
				if dots > 0 {
					fmt.Fprintln(d.W)
				}
				// A server that can't start would fail again after every reboot.
				d.unwant(p.Name)
				d.Out.Fail("%s stopped before it was ready", p.Name)
				d.tail(p.Name)
				d.Out.Hint("fix the error above, then ds up %s; the start command can be set in .devrc", p.Name)
				return ErrReported
			}
			fmt.Fprint(d.W, ".")
			dots++
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(d.W)
			d.Out.Warn("%s didn't open port %d within %s; it's still running in tmux", p.Name, port, timeout)
			d.tail(p.Name)
			d.Out.Hint("make sure the server uses $PORT (%d) and listens on 0.0.0.0; ds attach %s to look, ds stop %s to stop", port, p.Name, p.Name)
			return ErrReported
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func listening(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("localhost", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// tail prints the last lines of name's log.
func (d Dev) tail(name string) {
	lines := lastLines(d.S.LogFile(name), 15)
	if len(lines) == 0 {
		d.Out.Text("%s", d.Out.Dim("(no output in "+d.S.Layout.Pretty(d.S.LogFile(name))+")"))
		return
	}
	d.Out.Text("%s", d.Out.Dim("Last lines of "+d.S.Layout.Pretty(d.S.LogFile(name))+":"))
	for _, l := range lines {
		fmt.Fprintln(d.W, "  "+l)
	}
}

func lastLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }() // read-only
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, strings.TrimRight(sc.Text(), "\r"))
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return lines
}

// Stop interrupts p's server, waits up to 8s for it to exit, then closes its
// window. ds restore no longer starts it.
func (d Dev) Stop(ctx context.Context, p Project) error {
	d.unwant(p.Name)
	if !Running(ctx)[p.Name] {
		d.Out.OK("%s isn't running", p.Name)
		return nil
	}
	if err := interrupt(ctx, p.Name); err != nil {
		return err
	}
	for range 32 {
		time.Sleep(250 * time.Millisecond)
		if !Running(ctx)[p.Name] {
			d.Out.OK("Stopped %s", p.Name)
			return nil
		}
	}
	if err := killWindow(ctx, p.Name); err != nil {
		return err
	}
	d.Out.OK("Stopped %s (closed its window after it ignored Ctrl-C)", p.Name)
	return nil
}

// Status is one row of dev ls.
type Status struct {
	Name    string
	Running bool
	URL     string
}

// List returns every project folder plus anything running, sorted by name.
func (d Dev) List(ctx context.Context, runningOnly bool) []Status {
	running := Running(ctx)
	ports, _ := LoadPorts(d.S.PortsFile())
	names := map[string]bool{}
	for n := range running {
		names[n] = true
	}
	if !runningOnly {
		entries, _ := os.ReadDir(d.S.ProjectsDir)
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				if n := Name(e.Name()); n != "" {
					names[n] = true
				}
			}
		}
	}
	var out []Status
	for n := range names {
		st := Status{Name: n, Running: running[n]}
		if port, ok := ports[n]; ok {
			st.URL = d.S.URL(n, port)
		}
		out = append(out, st)
	}
	slices.SortFunc(out, func(a, b Status) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// URL returns p's URL, if it has a port yet.
func (d Dev) URL(p Project) (string, bool) {
	ports, err := LoadPorts(d.S.PortsFile())
	if err != nil {
		return "", false
	}
	port, ok := ports[p.Name]
	if !ok {
		return "", false
	}
	return d.S.URL(p.Name, port), true
}
