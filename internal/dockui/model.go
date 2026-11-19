package dockui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	refreshEvery = 2 * time.Second
	apiTimeout   = 15 * time.Second
	logTail      = 500
	logKeep      = 5000
	shellScript  = "command -v bash >/dev/null && exec bash || exec sh"
)

type tab int

const (
	tabContainers tab = iota
	tabDisk
)

type mode int

const (
	modeList    mode = iota
	modeFilter       // typing a filter
	modeConfirm      // y/n question in the status bar
	modeProject      // choosing a compose project action
	modeConnect      // connect panel
	modeLogs         // logs view
	modeHelp         // key list
)

// row is one line of the main list: a project header (ci < 0) or a container.
type row struct{ gi, ci int }

type confirmation struct {
	question string
	run      tea.Cmd
}

type logState struct {
	seq    int
	id     string
	title  string
	lines  []string
	follow bool
	done   string // why the stream ended, if it did
	cancel context.CancelFunc
	closer io.Closer
	vp     viewport.Model
}

type diskState struct {
	du      *DiskUsage
	err     error
	loading bool
	vp      viewport.Model
}

// Model is the ds docker UI.
type Model struct {
	c    *Client
	host string

	width, height int
	tab           tab
	mode          mode

	all     []Group
	groups  []Group // after the filter
	rows    []row
	cursor  int
	loaded  bool
	listErr error

	filter textinput.Model
	status string
	bad    bool // status is an error

	confirm      confirmation
	project      string
	connect      []ConnectLine
	connectTitle string
	connectSel   int

	logs logState
	disk diskState
}

// New returns the model; host is the name the laptop uses for the VM.
func New(c *Client, host string) Model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter by service, project or image"
	return Model{c: c, host: host, width: 100, height: 30, filter: ti}
}

// Messages.
type (
	tickMsg       struct{}
	containersMsg struct {
		cs  []Container
		err error
	}
	doneMsg struct {
		text string
		err  error
	}
	connectMsg struct {
		title string
		lines []ConnectLine
		err   error
	}
	logsOpenedMsg struct {
		seq int
		r   *bufio.Reader
		c   io.Closer
		err error
	}
	logLinesMsg struct {
		seq   int
		lines []string
		err   error
		more  tea.Cmd // reads the next batch
	}
	diskMsg struct {
		du  DiskUsage
		err error
	}
)

func apiCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), apiTimeout)
}

func (m Model) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := apiCtx()
		defer cancel()
		cs, err := m.c.Containers(ctx)
		return containersMsg{cs, err}
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

// Init starts the first fetch and the refresh timer.
func (m Model) Init() tea.Cmd { return tea.Batch(m.fetch(), tick()) }

// selected returns the container under the cursor.
func (m Model) selected() (Container, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return Container{}, false
	}
	r := m.rows[m.cursor]
	if r.ci < 0 {
		return Container{}, false
	}
	return m.groups[r.gi].Containers[r.ci], true
}

// selectedGroup returns the group of the row under the cursor.
func (m Model) selectedGroup() (Group, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return Group{}, false
	}
	return m.groups[m.rows[m.cursor].gi], true
}

// rebuild applies the filter and rebuilds rows, keeping the selection on the
// same container or header.
func (m *Model) rebuild() {
	var keepID, keepGroup string
	if c, ok := m.selected(); ok {
		keepID = c.ID
	} else if g, ok := m.selectedGroup(); ok {
		keepGroup = g.Name
	}
	m.groups = Filter(m.all, m.filter.Value())
	m.rows = m.rows[:0]
	for gi, g := range m.groups {
		m.rows = append(m.rows, row{gi, -1})
		for ci := range g.Containers {
			m.rows = append(m.rows, row{gi, ci})
		}
	}
	idx := slices.IndexFunc(m.rows, func(r row) bool {
		if r.ci < 0 {
			return keepID == "" && m.groups[r.gi].Name == keepGroup
		}
		return m.groups[r.gi].Containers[r.ci].ID == keepID
	})
	switch {
	case idx >= 0:
		m.cursor = idx
	case m.cursor >= len(m.rows):
		m.cursor = len(m.rows) - 1
	}
	if !m.loaded && len(m.rows) > 1 {
		m.cursor = 1 // start on the first container, not a header
	}
	m.cursor = max(m.cursor, 0)
}

func (m *Model) say(format string, a ...any) {
	m.status, m.bad = fmt.Sprintf(format, a...), false
}

func (m *Model) fail(err error) {
	m.status, m.bad = err.Error(), true
}

// Update handles a message.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeViewports()
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.fetch(), tick())
	case containersMsg:
		if msg.err != nil {
			m.listErr = msg.err
			return m, nil
		}
		m.listErr = nil
		m.all = GroupByProject(msg.cs)
		m.rebuild()
		m.loaded = true
		return m, nil
	case doneMsg:
		if msg.err != nil {
			m.fail(msg.err)
		} else {
			m.say("%s", msg.text)
		}
		cmds := []tea.Cmd{m.fetch()}
		if m.tab == tabDisk {
			m.disk.loading = true
			cmds = append(cmds, m.fetchDisk())
		}
		return m, tea.Batch(cmds...)
	case connectMsg:
		if m.mode != modeList || m.tab != tabContainers {
			// The user moved on (logs, filter, a y/n question, the disk tab)
			// before the inspect came back: don't take over that screen.
			return m, nil
		}
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		m.connect, m.connectTitle, m.connectSel, m.mode = msg.lines, msg.title, 0, modeConnect
		if len(msg.lines) == 0 {
			m.mode = modeList
			m.say("%s publishes no ports", msg.title)
			return m, nil
		}
		return m, m.copy(msg.lines[0])
	case logsOpenedMsg:
		if msg.seq != m.logs.seq {
			if msg.c != nil {
				_ = msg.c.Close()
			}
			return m, nil
		}
		if msg.err != nil {
			m.logs.done = msg.err.Error()
			return m, nil
		}
		m.logs.closer = msg.c
		return m, readLines(msg.seq, msg.r)
	case logLinesMsg:
		if msg.seq != m.logs.seq || m.mode != modeLogs {
			return m, nil
		}
		m.appendLogs(msg.lines)
		if msg.err != nil {
			if errors.Is(msg.err, io.EOF) {
				m.logs.done = "stream ended (container stopped)"
			} else if !errors.Is(msg.err, context.Canceled) {
				m.logs.done = msg.err.Error()
			}
			return m, nil
		}
		return m, msg.more
	case diskMsg:
		m.disk.loading = false
		m.disk.err = msg.err
		if msg.err == nil {
			m.disk.du = &msg.du
		}
		m.disk.vp.SetContent(m.diskContent())
		return m, nil
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	if m.mode == modeFilter {
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) resizeViewports() {
	m.logs.vp.SetWidth(m.width)
	m.logs.vp.SetHeight(m.logsHeight())
	m.disk.vp.SetWidth(m.width)
	m.disk.vp.SetHeight(m.bodyHeight())
	m.filter.SetWidth(max(m.width-4, 10))
	if m.disk.du != nil {
		m.disk.vp.SetContent(m.diskContent())
	}
}

func (m Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		m.closeLogs()
		return m, tea.Quit
	}
	switch m.mode {
	case modeFilter:
		switch k {
		case "enter":
			m.mode = modeList
			m.filter.Blur()
			return m, nil
		case "esc":
			m.mode = modeList
			m.filter.Blur()
			m.filter.SetValue("")
			m.rebuild()
			return m, nil
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.rebuild()
		return m, cmd
	case modeConfirm:
		m.mode = modeList
		if k == "y" || k == "Y" {
			m.say("%s", strings.TrimSuffix(m.confirm.question, "?")+" …")
			return m, m.confirm.run
		}
		m.say("cancelled")
		return m, nil
	case modeProject:
		m.mode = modeList
		verb := map[string]string{"r": "restart", "x": "stop", "u": "start"}[k]
		if verb == "" {
			m.say("cancelled")
			return m, nil
		}
		m.say("%s %s …", verb, m.project)
		return m, composeCmd(m.project, verb)
	case modeConnect:
		switch k {
		case "esc", "q", "c":
			m.mode = modeList
		case "up", "k":
			m.connectSel = max(m.connectSel-1, 0)
		case "down", "j":
			m.connectSel = min(m.connectSel+1, len(m.connect)-1)
		case "enter", "y":
			return m, m.copy(m.connect[m.connectSel])
		}
		return m, nil
	case modeHelp:
		m.mode = modeList
		return m, nil
	case modeLogs:
		return m.logsKey(msg)
	}

	// modeList
	switch k {
	case "q":
		return m, tea.Quit
	case "?":
		m.mode = modeHelp
		return m, nil
	case "1":
		m.tab = tabContainers
		return m, nil
	case "2":
		m.tab = tabDisk
		if m.disk.du == nil && !m.disk.loading {
			m.disk.loading = true
			return m, m.fetchDisk()
		}
		return m, nil
	}
	if m.tab == tabDisk {
		return m.diskKey(msg)
	}
	return m.listKey(k)
}

func (m Model) listKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, len(m.rows)-1)
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = max(len(m.rows)-1, 0)
	case "pgdown", "ctrl+d":
		m.cursor = min(m.cursor+m.bodyHeight()/2, len(m.rows)-1)
	case "pgup", "ctrl+u":
		m.cursor = max(m.cursor-m.bodyHeight()/2, 0)
	case "tab":
		for i := m.cursor + 1; i < len(m.rows); i++ {
			if m.rows[i].ci < 0 {
				m.cursor = i
				return m, nil
			}
		}
		if len(m.rows) > 0 {
			m.cursor = 0
		}
	case "shift+tab":
		start := m.cursor - 1
		if r, ok := m.row(); ok && r.ci >= 0 {
			// From inside a project, go to its own header first.
			start = m.cursor - 1 - r.ci
			if start >= 0 && start != m.cursor {
				m.cursor = start
				return m, nil
			}
		}
		for i := start; i >= 0; i-- {
			if m.rows[i].ci < 0 {
				m.cursor = i
				return m, nil
			}
		}
	case "/":
		m.mode = modeFilter
		return m, m.filter.Focus()
	case "esc":
		if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.rebuild()
			m.say("filter cleared")
		}
	case "P":
		g, ok := m.selectedGroup()
		if !ok {
			return m, nil
		}
		if !g.Compose() {
			m.say("these containers aren't part of a compose project")
			return m, nil
		}
		m.mode, m.project = modeProject, g.Name
	}
	c, ok := m.selected()
	if !ok {
		if slices.Contains([]string{"enter", "l", "s", "r", "x", "u", "D", "c"}, k) {
			if g, gok := m.selectedGroup(); gok && g.Compose() {
				m.say("%s: P for project actions, j/k to pick a container", g.Name)
			}
		}
		return m, nil
	}
	name := c.Service()
	switch k {
	case "enter", "l":
		return m.openLogs(c)
	case "s":
		if c.State != "running" {
			m.say("%s isn't running; u starts it", name)
			return m, nil
		}
		cmd := exec.Command("docker", "exec", "-it", c.ID, "sh", "-c", shellScript)
		m.say("shell in %s ended", name)
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
			if err != nil {
				return doneMsg{err: fmt.Errorf("shell in %s: %w", name, err)}
			}
			return doneMsg{text: "shell in " + name + " ended"}
		})
	case "r":
		m.say("restarting %s …", name)
		return m, m.action("restarted "+name, func(ctx context.Context) error { return m.c.Restart(ctx, c.ID) })
	case "x":
		m.say("stopping %s …", name)
		return m, m.action("stopped "+name, func(ctx context.Context) error { return m.c.Stop(ctx, c.ID) })
	case "u":
		m.say("starting %s …", name)
		return m, m.action("started "+name, func(ctx context.Context) error { return m.c.Start(ctx, c.ID) })
	case "D":
		m.mode = modeConfirm
		m.confirm = confirmation{
			question: fmt.Sprintf("remove %s (%s)? its volumes stay", name, c.Name()),
			run:      m.action("removed "+name, func(ctx context.Context) error { return m.c.Remove(ctx, c.ID) }),
		}
	case "c":
		return m, m.fetchConnect(c)
	}
	return m, nil
}

func (m Model) row() (row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return row{}, false
	}
	return m.rows[m.cursor], true
}

func (m Model) action(text string, f func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := f(ctx); err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: text}
	}
}

func composeCmd(project, verb string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", "compose", "-p", project, verb).CombinedOutput()
		if err != nil {
			last := strings.TrimSpace(string(out))
			if i := strings.LastIndexByte(last, '\n'); i >= 0 {
				last = last[i+1:]
			}
			return doneMsg{err: fmt.Errorf("docker compose -p %s %s: %w %s", project, verb, err, last)}
		}
		past := map[string]string{"restart": "restarted", "stop": "stopped", "start": "started"}[verb]
		return doneMsg{text: past + " project " + project}
	}
}

func (m Model) fetchConnect(c Container) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := apiCtx()
		defer cancel()
		d, err := m.c.Inspect(ctx, c.ID)
		if err != nil {
			return connectMsg{err: err}
		}
		return connectMsg{title: c.Service(), lines: Connect(c.Image, d.Env, Addresses(c.Ports, m.host))}
	}
}

func (m *Model) copy(l ConnectLine) tea.Cmd {
	v := l.CopyValue()
	if l.Note {
		m.say("%s", v)
		return nil
	}
	m.say("copied %s", v)
	return tea.SetClipboard(v)
}

// ---- logs ----

func (m Model) openLogs(c Container) (tea.Model, tea.Cmd) {
	m.closeLogs()
	vp := viewport.New()
	vp.SetWidth(m.width)
	vp.SetHeight(m.logsHeight())
	seq := m.logs.seq + 1
	ctx, cancel := context.WithCancel(context.Background())
	m.logs = logState{seq: seq, id: c.ID, title: c.Service(), follow: true, cancel: cancel, vp: vp}
	if g := c.Project(); g != "" {
		m.logs.title += "  " + g
	}
	m.mode = modeLogs
	client := m.c
	open := func() tea.Msg {
		ictx, icancel := apiCtx()
		d, err := client.Inspect(ictx, c.ID)
		icancel()
		if err != nil {
			return logsOpenedMsg{seq: seq, err: err}
		}
		r, err := client.Logs(ctx, c.ID, logTail, true, d.TTY)
		if err != nil {
			return logsOpenedMsg{seq: seq, err: err}
		}
		return logsOpenedMsg{seq: seq, r: bufio.NewReaderSize(r, 64<<10), c: r}
	}
	return m, open
}

// readLines reads what the stream has: at least one line, then whatever is
// already buffered, so a burst arrives as one message.
func readLines(seq int, r *bufio.Reader) tea.Cmd {
	return func() tea.Msg {
		var lines []string
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				lines = append(lines, cleanLine(line))
			}
			if err != nil {
				return logLinesMsg{seq: seq, lines: lines, err: err}
			}
			if r.Buffered() == 0 || len(lines) >= 1000 {
				return logLinesMsg{seq: seq, lines: lines, more: readLines(seq, r)}
			}
		}
	}
}

// cleanLine drops escape sequences and the trailing newline, and expands tabs.
func cleanLine(s string) string {
	s = ansi.Strip(strings.TrimRight(s, "\r\n"))
	return strings.ReplaceAll(s, "\t", "    ")
}

func (m *Model) appendLogs(lines []string) {
	m.logs.lines = append(m.logs.lines, lines...)
	if over := len(m.logs.lines) - logKeep; over > 0 {
		m.logs.lines = slices.Delete(m.logs.lines, 0, over)
	}
	m.logs.vp.SetContentLines(m.logs.lines)
	if m.logs.follow {
		m.logs.vp.GotoBottom()
	}
}

func (m *Model) closeLogs() {
	if m.logs.cancel != nil {
		m.logs.cancel()
	}
	if m.logs.closer != nil {
		_ = m.logs.closer.Close()
	}
	m.logs.cancel, m.logs.closer = nil, nil
}

func (m Model) logsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "backspace", "h":
		m.closeLogs()
		m.logs.seq++
		m.mode = modeList
		return m, nil
	case "f":
		m.logs.follow = !m.logs.follow
		if m.logs.follow {
			m.logs.vp.GotoBottom()
			m.say("following")
		} else {
			m.say("follow off")
		}
		return m, nil
	case "G", "end":
		m.logs.vp.GotoBottom()
		m.logs.follow = true
		return m, nil
	case "g", "home":
		m.logs.vp.GotoTop()
		m.logs.follow = false
		return m, nil
	}
	var cmd tea.Cmd
	m.logs.vp, cmd = m.logs.vp.Update(msg)
	m.logs.follow = m.logs.vp.AtBottom()
	return m, cmd
}

// ---- disk ----

func (m Model) fetchDisk() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		du, err := m.c.DiskUsage(ctx)
		return diskMsg{du, err}
	}
}

func (m Model) diskKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "r":
		m.disk.loading = true
		m.say("refreshing disk usage …")
		return m, m.fetchDisk()
	case "p":
		m.mode = modeConfirm
		m.confirm = confirmation{
			question: "prune dangling images and unused build cache?",
			run: func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				n, err := m.c.PruneDangling(ctx)
				if err != nil {
					return doneMsg{err: err}
				}
				return doneMsg{text: "pruned dangling images and build cache, freed " + HumanSize(n)}
			},
		}
		return m, nil
	case "V":
		m.mode = modeConfirm
		m.confirm = confirmation{
			question: "DELETE unused anonymous volumes? their data is gone for good (named volumes stay)?",
			run: func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				n, err := m.c.PruneVolumes(ctx)
				if err != nil {
					return doneMsg{err: err}
				}
				return doneMsg{text: "pruned unused anonymous volumes, freed " + HumanSize(n)}
			},
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.disk.vp, cmd = m.disk.vp.Update(msg)
	return m, cmd
}
