package devproj

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strconv"
)

// Ports maps project names to their ports, saved in ~/.devbox/state/ports.json.
type Ports map[string]int

// LoadPorts reads the ports file; a missing file is an empty map.
func LoadPorts(path string) (Ports, error) {
	p := Ports{}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ports: %w", err)
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("read ports %s: %w", path, err)
	}
	return p, nil
}

// Save writes the ports file atomically.
func (p Ports) Save(path string) error {
	if err := saveJSON(path, p); err != nil {
		return fmt.Errorf("save ports: %w", err)
	}
	return nil
}

// Assign returns name's saved port, or picks the first port in [lo, hi] that no
// other project has and that free reports as free. It reports whether it
// picked a new one (the caller saves).
func (p Ports) Assign(name string, lo, hi int, free func(int) bool) (int, bool, error) {
	if port, ok := p[name]; ok {
		return port, false, nil
	}
	taken := map[int]bool{}
	for _, port := range p {
		taken[port] = true
	}
	for port := lo; port <= hi; port++ {
		if !taken[port] && free(port) {
			p[name] = port
			return port, true, nil
		}
	}
	return 0, false, fmt.Errorf("no free port between %d and %d", lo, hi)
}

// PortFree reports whether nothing listens on port, by trying to listen on it.
func PortFree(port int) bool {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
