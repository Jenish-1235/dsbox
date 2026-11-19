package dockui

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Styles use basic ANSI colours, which follow the terminal's own palette and
// so read well on dark and light themes alike.
func stBold() lipgloss.Style   { return lipgloss.NewStyle().Bold(true) }
func stDim() lipgloss.Style    { return lipgloss.NewStyle().Faint(true) }
func stGreen() lipgloss.Style  { return lipgloss.NewStyle().Foreground(lipgloss.Green) }
func stYellow() lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Yellow) }
func stRed() lipgloss.Style    { return lipgloss.NewStyle().Foreground(lipgloss.Red) }
func stCyan() lipgloss.Style   { return lipgloss.NewStyle().Foreground(lipgloss.Cyan) }
func stTab() lipgloss.Style    { return lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1) }
func stTabOff() lipgloss.Style { return lipgloss.NewStyle().Faint(true).Padding(0, 1) }
func stKey() lipgloss.Style    { return lipgloss.NewStyle().Bold(true) }

func healthStyle(h Health) lipgloss.Style {
	switch h {
	case Up, Healthy:
		return stGreen()
	case Starting, Paused:
		return stYellow()
	case Unhealthy, Failed:
		return stRed()
	}
	return stDim()
}

func dot(h Health) string {
	if h == Stopped || h == Failed {
		return healthStyle(h).Render("○")
	}
	return healthStyle(h).Render("●")
}

// fit pads or truncates s (which may hold escape codes) to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		return ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

func hints(pairs ...string) string {
	var b []string
	for i := 0; i+1 < len(pairs); i += 2 {
		b = append(b, stKey().Render(pairs[i])+" "+stDim().Render(pairs[i+1]))
	}
	return strings.Join(b, "  ")
}

// View draws the screen.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "ds docker"
	return v
}

func (m Model) bodyHeight() int { return max(m.height-4, 3) }

// logsHeight is the logs viewport's height: the body less the title line.
func (m Model) logsHeight() int { return m.bodyHeight() - 1 }

func (m Model) render() string {
	var body, keys string
	switch {
	case m.mode == modeHelp:
		body, keys = m.helpView(), hints("any key", "back")
	case m.mode == modeLogs:
		body, keys = m.logsView(), hints("↑↓/pgup/pgdn", "scroll", "g/G", "top/end", "f", "follow", "esc", "back")
	case m.tab == tabDisk:
		body = m.disk.vp.View()
		if m.disk.du == nil {
			body = m.diskContent()
		}
		keys = hints("↑↓", "scroll", "p", "prune dangling", "V", "prune volumes", "r", "refresh", "1", "containers", "?", "keys", "q", "quit")
	case m.mode == modeConnect:
		body, keys = m.connectView(), hints("↑↓", "pick", "enter", "copy", "esc", "close")
	default:
		body = m.listView(m.bodyHeight())
		keys = m.listHints()
	}
	lines := []string{m.header(), ""}
	bodyLines := strings.Split(body, "\n")
	h := m.bodyHeight()
	if len(bodyLines) > h {
		bodyLines = bodyLines[:h]
	}
	for len(bodyLines) < h {
		bodyLines = append(bodyLines, "")
	}
	lines = append(lines, bodyLines...)
	lines = append(lines, m.statusLine(), fit(keys, m.width))
	return strings.Join(lines, "\n")
}

func (m Model) listHints() string {
	if _, ok := m.selected(); !ok {
		if m.width < 110 {
			return hints("↑↓", "move", "tab", "project", "P", "project actions", "/", "filter", "2", "disk", "?", "keys", "q", "quit")
		}
		return hints("↑↓/jk", "move", "tab", "next project", "P", "project actions", "/", "filter", "2", "disk", "?", "keys", "q", "quit")
	}
	if m.width < 110 {
		return hints("enter", "logs", "s", "shell", "r", "restart", "x", "stop", "u", "start", "c", "connect", "?", "more")
	}
	return hints("enter", "logs", "s", "shell", "r", "restart", "x", "stop", "u", "start", "D", "remove", "c", "connect",
		"P", "project", "tab", "next project", "/", "filter", "2", "disk", "?", "keys", "q", "quit")
}

func (m Model) header() string {
	tabs := []string{"1 Containers", "2 Disk"}
	var parts []string
	for i, t := range tabs {
		if tab(i) == m.tab {
			parts = append(parts, stTab().Render(t))
		} else {
			parts = append(parts, stTabOff().Render(t))
		}
	}
	left := stBold().Render("Docker") + "  " + strings.Join(parts, " ")
	running, total := 0, 0
	for _, g := range m.all {
		running += g.Running()
		total += len(g.Containers)
	}
	right := stDim().Render(fmt.Sprintf("%d running · %d stopped", running, total-running))
	if m.listErr != nil {
		right = stRed().Render("docker unreachable")
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fit(left, m.width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) statusLine() string {
	switch m.mode {
	case modeConfirm:
		return fit(stYellow().Render(m.confirm.question)+" "+stKey().Render("y")+stDim().Render("/n"), m.width)
	case modeProject:
		return fit(stYellow().Render("project "+m.project+":")+"  "+hints("r", "restart", "x", "stop", "u", "start", "esc", "cancel"), m.width)
	case modeFilter:
		return fit(m.filter.View(), m.width)
	}
	if m.listErr != nil && m.tab == tabContainers {
		return fit(stRed().Render(m.listErr.Error()), m.width)
	}
	s := m.status
	switch {
	case s == "":
		if f := m.filter.Value(); f != "" {
			return fit(stCyan().Render("filter: "+f)+stDim().Render("  esc clears"), m.width)
		}
		return ""
	case m.bad:
		return fit(stRed().Render(s), m.width)
	}
	return fit(stCyan().Render(s), m.width)
}

// columns are the widths of the container line's fields.
type columns struct{ svc, state, age, ports, image int }

func (m Model) columns(groups []Group) columns {
	var c columns
	for _, g := range groups {
		for _, ct := range g.Containers {
			st := ParseStatus(ct.State, ct.Status)
			c.svc = max(c.svc, lipgloss.Width(ct.Service()))
			c.state = max(c.state, len(st.Label))
			c.age = max(c.age, lipgloss.Width(st.Age))
			c.ports = max(c.ports, lipgloss.Width(portsText(Addresses(ct.Ports, m.host))))
		}
	}
	c.svc = min(c.svc, 24)
	// indent 4 + dot 2 + gaps 2 each
	rest := m.width - 4 - 2 - c.svc - 2 - c.state - 2 - c.age - 2
	c.ports = min(c.ports, max(rest, 0))
	c.image = rest - c.ports - 2
	if c.image < 8 {
		c.image = 0
		if rest < c.ports {
			c.ports = max(rest, 0)
		}
	}
	return c
}

func portsText(addrs []Address) string {
	var parts []string
	for _, a := range addrs {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, "  ")
}

func (m Model) containerLine(ct Container, cols columns, selected bool) string {
	st := ParseStatus(ct.State, ct.Status)
	mark := "    "
	if selected {
		mark = "  " + stCyan().Render("▌") + " "
	}
	svc := fit(ct.Service(), cols.svc)
	if selected {
		svc = stBold().Render(svc)
	} else if st.Health == Stopped {
		svc = stDim().Render(svc)
	}
	addrs := Addresses(ct.Ports, m.host)
	var ports []string
	for _, a := range addrs {
		if a.VMOnly {
			ports = append(ports, stDim().Render(a.String()))
		} else {
			ports = append(ports, a.String())
		}
	}
	line := mark + dot(st.Health) + " " + svc + "  " +
		healthStyle(st.Health).Render(fit(st.Label, cols.state)) + "  " +
		stDim().Render(fit(st.Age, cols.age)) + "  " +
		fit(strings.Join(ports, "  "), cols.ports)
	if cols.image > 0 {
		line += "  " + stDim().Render(fit(ct.Image, cols.image))
	}
	return fit(line, m.width)
}

func (m Model) groupLine(g Group, selected bool) string {
	mark := "  "
	if selected {
		mark = stCyan().Render("▌") + " "
	}
	name := stBold().Render(g.Name)
	if !g.Compose() {
		name = stBold().Render("Other") + stDim().Render(" (not compose)")
	}
	counts := fmt.Sprintf("%d/%d running", g.Running(), len(g.Containers))
	cs := stGreen()
	switch {
	case g.Running() == 0:
		cs = stDim()
	case g.Running() < len(g.Containers):
		cs = stYellow()
	}
	line := mark + name + "  " + cs.Render(counts)
	if g.Dir != "" {
		line += "  " + stDim().Render(prettyDir(g.Dir))
	}
	return fit(line, m.width)
}

func prettyDir(d string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(d, home); ok && (rest == "" || rest[0] == '/') {
			return "~" + rest
		}
	}
	return d
}

// listView renders the grouped list; height <= 0 renders all of it.
func (m Model) listView(height int) string {
	if !m.loaded {
		if m.listErr != nil {
			return stRed().Render("  can't reach docker: "+m.listErr.Error()) + "\n" +
				stDim().Render("  is the docker service running? is your user in the docker group?")
		}
		return stDim().Render("  loading …")
	}
	if len(m.rows) == 0 {
		if m.filter.Value() != "" {
			return stDim().Render("  nothing matches " + m.filter.Value())
		}
		return stDim().Render("  no containers. docker compose up -d in a project starts some")
	}
	cols := m.columns(m.groups)
	var lines []string
	cursorLine := 0
	for i, r := range m.rows {
		if r.ci < 0 {
			if i > 0 {
				lines = append(lines, "")
			}
			if i == m.cursor {
				cursorLine = len(lines)
			}
			lines = append(lines, m.groupLine(m.groups[r.gi], i == m.cursor))
			continue
		}
		if i == m.cursor {
			cursorLine = len(lines)
		}
		lines = append(lines, m.containerLine(m.groups[r.gi].Containers[r.ci], cols, i == m.cursor))
	}
	if height <= 0 || len(lines) <= height {
		return strings.Join(lines, "\n")
	}
	// Scroll so the cursor stays in view, with a line of context.
	start := min(max(cursorLine-height/2, 0), len(lines)-height)
	return strings.Join(lines[start:start+height], "\n")
}

func (m Model) connectView() string {
	var b []string
	b = append(b, "  "+stBold().Render("Connect to "+m.connectTitle)+stDim().Render("  from your laptop"), "")
	w := 0
	for _, l := range m.connect {
		w = max(w, lipgloss.Width(l.Label))
	}
	for i, l := range m.connect {
		mark := "    "
		val := l.Value
		if i == m.connectSel {
			mark = "  " + stCyan().Render("▌") + " "
			val = stBold().Render(val)
		}
		b = append(b, fit(mark+stDim().Render(fit(l.Label, w))+"  "+val, m.width))
	}
	return strings.Join(b, "\n")
}

func (m Model) logsView() string {
	follow := stDim().Render("paused")
	if m.logs.follow {
		follow = stGreen().Render("following")
	}
	title := "  " + stBold().Render("Logs ") + m.logs.title + "  " + follow
	if m.logs.done != "" {
		title += "  " + stYellow().Render(m.logs.done)
	}
	content := m.logs.vp.View()
	if len(m.logs.lines) == 0 {
		content = stDim().Render("  (no output yet)")
	}
	// The viewport is logsHeight tall, so with the title it fills the body
	// exactly and its last line (the newest, when following) stays on screen.
	return fit(title, m.width) + "\n" + content
}

func (m Model) helpView() string {
	rows := [][2]string{
		{"↑↓ j k", "move"},
		{"tab / shift+tab", "next / previous project"},
		{"/", "filter (enter keeps it, esc clears)"},
		{"enter, l", "logs: last 500 lines, follows; f toggles follow, esc back"},
		{"s", "shell in the container (bash, else sh); exit to come back"},
		{"r  x  u", "restart, stop, start the container"},
		{"D", "remove the container (asks y/n; volumes stay)"},
		{"c", "connect info; copies the URL to your clipboard"},
		{"P", "project actions: docker compose restart, stop, start"},
		{"1  2", "containers, disk"},
		{"p", "disk: prune dangling images and build cache (asks y/n)"},
		{"V", "disk: prune unused anonymous volumes (asks y/n)"},
		{"r", "disk: refresh"},
		{"?", "this list"},
		{"q, ctrl+c", "quit"},
	}
	b := []string{"  " + stBold().Render("Keys"), ""}
	for _, r := range rows {
		b = append(b, fit("  "+stKey().Render(fit(r[0], 18))+"  "+r[1], m.width))
	}
	b = append(b, "", fit(stDim().Render("  Addresses are as your laptop sees them: "+m.host+":<port>. Grey ones are bound to the VM only."), m.width))
	return strings.Join(b, "\n")
}

func (m Model) diskContent() string {
	if m.disk.err != nil {
		return stRed().Render("  " + m.disk.err.Error())
	}
	if m.disk.du == nil {
		return stDim().Render("  reading disk usage …")
	}
	du := m.disk.du
	var b []string
	section := func(title, summary string) {
		if len(b) > 0 {
			b = append(b, "")
		}
		b = append(b, "  "+stBold().Render(title)+"  "+stDim().Render(summary))
	}

	// Images
	var imgTotal, dangling int64
	nDangling := 0
	imgs := slices.Clone(du.Images)
	slices.SortFunc(imgs, func(a, b struct {
		ID         string   `json:"Id"`
		RepoTags   []string `json:"RepoTags"`
		Size       int64    `json:"Size"`
		SharedSize int64    `json:"SharedSize"`
		Containers int      `json:"Containers"`
		Created    int64    `json:"Created"`
	}) int {
		return cmp.Compare(b.Size, a.Size)
	})
	for _, im := range imgs {
		imgTotal += im.Size
		if isDangling(im.RepoTags) {
			dangling += im.Size
			nDangling++
		}
	}
	section("Images", fmt.Sprintf("%d · %s on disk (layers) · %d dangling %s", len(imgs), HumanSize(du.LayersSize), nDangling, HumanSize(dangling)))
	nameW := max(min(m.width-30, 60), 20)
	for _, im := range imgs {
		name := "<none>"
		if !isDangling(im.RepoTags) {
			name = strings.Join(im.RepoTags, ", ")
		}
		use := stDim().Render("unused")
		if im.Containers > 0 {
			use = stGreen().Render(fmt.Sprintf("%d ctr", im.Containers))
		} else if isDangling(im.RepoTags) {
			use = stYellow().Render("dangling")
		}
		b = append(b, fit("    "+fit(name, nameW)+"  "+fmt.Sprintf("%9s", HumanSize(im.Size))+"  "+use, m.width))
	}

	// Volumes
	vols := slices.Clone(du.Volumes)
	slices.SortFunc(vols, func(a, b struct {
		Name      string `json:"Name"`
		UsageData struct {
			Size     int64 `json:"Size"`
			RefCount int   `json:"RefCount"`
		} `json:"UsageData"`
	}) int {
		return cmp.Compare(b.UsageData.Size, a.UsageData.Size)
	})
	var volTotal, volUnused int64
	for _, v := range vols {
		volTotal += max(v.UsageData.Size, 0)
		if v.UsageData.RefCount == 0 {
			volUnused += max(v.UsageData.Size, 0)
		}
	}
	section("Volumes", fmt.Sprintf("%d · %s · %s not used by any container", len(vols), HumanSize(volTotal), HumanSize(volUnused)))
	for _, v := range vols {
		use := stDim().Render("unused")
		if v.UsageData.RefCount > 0 {
			use = stGreen().Render(fmt.Sprintf("%d ctr", v.UsageData.RefCount))
		}
		size := "?"
		if v.UsageData.Size >= 0 {
			size = HumanSize(v.UsageData.Size)
		}
		b = append(b, fit("    "+fit(v.Name, nameW)+"  "+fmt.Sprintf("%9s", size)+"  "+use, m.width))
	}

	// Build cache
	var cacheTotal, cacheFree int64
	for _, c := range du.BuildCache {
		cacheTotal += c.Size
		if !c.InUse && !c.Shared {
			cacheFree += c.Size
		}
	}
	section("Build cache", fmt.Sprintf("%d entries · %s · %s reclaimable", len(du.BuildCache), HumanSize(cacheTotal), HumanSize(cacheFree)))
	return strings.Join(b, "\n")
}

func isDangling(tags []string) bool {
	return len(tags) == 0 || (len(tags) == 1 && tags[0] == "<none>:<none>")
}

// Run starts the UI and returns when the user quits.
func Run(ctx context.Context, c *Client, host string) error {
	p := tea.NewProgram(New(c, host), tea.WithContext(ctx))
	final, err := p.Run()
	if fm, ok := final.(Model); ok {
		fm.closeLogs()
	}
	if err != nil {
		return fmt.Errorf("run docker ui: %w", err)
	}
	return nil
}

// Dump prints the main view once as plain text, width columns wide.
func Dump(ctx context.Context, c *Client, host string, width int) (string, error) {
	cs, err := c.Containers(ctx)
	if err != nil {
		return "", err
	}
	m := New(c, host)
	m.width = width
	m.all = GroupByProject(cs)
	m.rebuild()
	m.loaded = true
	m.cursor = -1
	out := m.header() + "\n\n" + m.listView(0) + "\n\n" + fit(m.listHints(), width)
	var b strings.Builder
	for line := range strings.Lines(ansi.Strip(out)) {
		b.WriteString(strings.TrimRight(line, " \n") + "\n")
	}
	return b.String(), nil
}
