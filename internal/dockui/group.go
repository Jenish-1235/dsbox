package dockui

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// OtherGroup holds containers that aren't part of a compose project.
const OtherGroup = "Other"

// Group is a compose project and its containers.
type Group struct {
	Name       string
	Dir        string // the compose working directory, if known
	Containers []Container
}

// Running counts the group's running containers.
func (g Group) Running() int {
	n := 0
	for _, c := range g.Containers {
		if c.State == "running" {
			n++
		}
	}
	return n
}

// Compose reports whether the group is a compose project.
func (g Group) Compose() bool { return g.Name != OtherGroup }

// GroupByProject groups containers by compose project, projects sorted by
// name and stand-alone containers last under Other. Within a group,
// containers are sorted by service.
func GroupByProject(cs []Container) []Group {
	idx := map[string]int{}
	var groups []Group
	for _, c := range cs {
		name := c.Project()
		if name == "" {
			name = OtherGroup
		}
		i, ok := idx[name]
		if !ok {
			i = len(groups)
			idx[name] = i
			groups = append(groups, Group{Name: name})
		}
		if groups[i].Dir == "" {
			groups[i].Dir = c.Labels["com.docker.compose.project.working_dir"]
		}
		groups[i].Containers = append(groups[i].Containers, c)
	}
	slices.SortFunc(groups, func(a, b Group) int {
		if (a.Name == OtherGroup) != (b.Name == OtherGroup) {
			if a.Name == OtherGroup {
				return 1
			}
			return -1
		}
		return cmp.Compare(a.Name, b.Name)
	})
	for _, g := range groups {
		slices.SortFunc(g.Containers, func(a, b Container) int {
			return cmp.Or(cmp.Compare(a.Service(), b.Service()), cmp.Compare(a.Name(), b.Name()))
		})
	}
	return groups
}

// Filter keeps containers whose service, name, project or image contain q
// (case-insensitive) and drops groups left empty. A group whose name matches
// keeps all its containers.
func Filter(groups []Group, q string) []Group {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return groups
	}
	var out []Group
	for _, g := range groups {
		if strings.Contains(strings.ToLower(g.Name), q) {
			out = append(out, g)
			continue
		}
		var keep []Container
		for _, c := range g.Containers {
			hay := strings.ToLower(c.Service() + " " + c.Name() + " " + c.Image)
			if strings.Contains(hay, q) {
				keep = append(keep, c)
			}
		}
		if len(keep) > 0 {
			g.Containers = keep
			out = append(out, g)
		}
	}
	return out
}

// Address is one published port as the laptop sees it.
type Address struct {
	Host    string // "devbox", or "localhost" when it's VM-only
	Public  int
	Private int
	Proto   string
	VMOnly  bool // bound to loopback: not reachable from the laptop
}

// String is "devbox:5433→5432", or "devbox:8080" when both ports match.
func (a Address) String() string {
	s := a.Host + ":" + strconv.Itoa(a.Public)
	if a.Private != a.Public {
		s += "→" + strconv.Itoa(a.Private)
	}
	if a.Proto != "" && a.Proto != "tcp" {
		s += "/" + a.Proto
	}
	return s
}

// HostPort is "devbox:5433", for copying.
func (a Address) HostPort() string { return a.Host + ":" + strconv.Itoa(a.Public) }

// Addresses turns a container's published ports into laptop addresses,
// one per public port (Docker lists IPv4 and IPv6 bindings separately),
// reachable ones first, then by private port. Unpublished ports are left out.
func Addresses(ports []Port, host string) []Address {
	type key struct {
		pub, priv int
		proto     string
	}
	seen := map[key]int{}
	var out []Address
	for _, p := range ports {
		if p.PublicPort == 0 {
			continue
		}
		loop := p.IP == "127.0.0.1" || p.IP == "::1"
		k := key{p.PublicPort, p.PrivatePort, p.Type}
		if i, ok := seen[k]; ok {
			if !loop && out[i].VMOnly { // one binding is reachable: prefer it
				out[i].Host, out[i].VMOnly = host, false
			}
			continue
		}
		a := Address{Host: host, Public: p.PublicPort, Private: p.PrivatePort, Proto: p.Type}
		if loop {
			a.Host, a.VMOnly = "localhost", true
		}
		seen[k] = len(out)
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b Address) int {
		// Laptop-reachable first, then by container port.
		return cmp.Or(compareBool(a.VMOnly, b.VMOnly), cmp.Compare(a.Private, b.Private),
			cmp.Compare(a.Public, b.Public), cmp.Compare(a.Proto, b.Proto))
	})
	return out
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// Health is a container's state as shown: what it is and how it's doing.
type Health int

// Health values, from good to bad.
const (
	Up        Health = iota // running, no health check
	Healthy                 // running and its health check passes
	Starting                // running, health check not yet passing
	Paused                  // paused or restarting
	Unhealthy               // running but the health check fails
	Stopped                 // exited 0, created
	Failed                  // exited non-zero, dead
)

// Status is a parsed container status line.
type Status struct {
	Health Health
	Label  string // "healthy", "running", "exited 1"
	Age    string // "2h" up, or "5m ago" since it stopped
}

var (
	reHealth = regexp.MustCompile(`\s*\((healthy|unhealthy|health: starting)\)\s*$`)
	reExited = regexp.MustCompile(`^Exited \((-?\d+)\) (.*) ago$`)
)

// ParseStatus reads Docker's state and status text ("Up 2 hours (healthy)",
// "Exited (1) 5 minutes ago").
func ParseStatus(state, status string) Status {
	switch state {
	case "running":
		s := Status{Health: Up, Label: "running"}
		rest := status
		if m := reHealth.FindStringSubmatch(status); m != nil {
			rest = status[:len(status)-len(m[0])]
			switch m[1] {
			case "healthy":
				s.Health, s.Label = Healthy, "healthy"
			case "unhealthy":
				s.Health, s.Label = Unhealthy, "unhealthy"
			default:
				s.Health, s.Label = Starting, "starting"
			}
		}
		s.Age = compactDuration(strings.TrimPrefix(rest, "Up "))
		return s
	case "exited":
		if m := reExited.FindStringSubmatch(status); m != nil {
			s := Status{Health: Stopped, Label: "exited", Age: compactDuration(m[2]) + " ago"}
			if m[1] != "0" {
				s.Health, s.Label = Failed, "exited "+m[1]
			}
			return s
		}
		return Status{Health: Stopped, Label: "exited"}
	case "paused":
		return Status{Health: Paused, Label: "paused"}
	case "restarting":
		return Status{Health: Paused, Label: "restarting"}
	case "dead":
		return Status{Health: Failed, Label: "dead"}
	case "removing":
		return Status{Health: Paused, Label: "removing"}
	}
	return Status{Health: Stopped, Label: cmp.Or(state, "unknown")}
}

// compactDuration turns Docker's "2 hours" into "2h".
func compactDuration(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "less than a second":
		return "<1s"
	case "about a minute":
		return "1m"
	case "about an hour":
		return "1h"
	case "":
		return ""
	}
	n, unit, ok := strings.Cut(s, " ")
	if !ok {
		return s
	}
	if _, err := strconv.Atoi(n); err != nil {
		return s
	}
	units := map[string]string{
		"second": "s", "minute": "m", "hour": "h", "day": "d", "week": "w", "month": "mo", "year": "y",
	}
	if u, ok := units[strings.TrimSuffix(unit, "s")]; ok {
		return n + u
	}
	return s
}

// HumanSize formats bytes as "1.2 GB".
func HumanSize(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "kMGTPE"[exp])
}
