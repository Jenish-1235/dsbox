// Package dockui is ds docker: a terminal UI for the Docker containers on the
// devbox, grouped by project, with logs, shells, actions and connect info.
package dockui

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// apiPrefix pins the Engine API version. Docker 29 accepts 1.44 and later.
const apiPrefix = "/v1.45"

// Client talks to the Docker Engine API over its unix socket.
type Client struct {
	http *http.Client
	base string // e.g. http://docker
}

// SocketPath returns the Docker socket: DOCKER_HOST when it's unix://, else
// the default.
func SocketPath() string {
	if h, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok && h != "" {
		return h
	}
	return "/var/run/docker.sock"
}

// NewClient returns a client for the Docker socket at path.
func NewClient(path string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}
	return &Client{http: &http.Client{Transport: tr}, base: "http://docker"}
}

// Port is one port mapping of a container.
type Port struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

// Container is one row of the container list.
type Container struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	Created int64             `json:"Created"`
	State   string            `json:"State"`  // running, exited, ...
	Status  string            `json:"Status"` // "Up 2 hours (healthy)"
	Ports   []Port            `json:"Ports"`
	Labels  map[string]string `json:"Labels"`
}

// Name is the container name without the leading slash.
func (c Container) Name() string {
	if len(c.Names) == 0 {
		return c.ID[:min(12, len(c.ID))]
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// Project is the compose project, or "" for a stand-alone container.
func (c Container) Project() string { return c.Labels["com.docker.compose.project"] }

// Service is the compose service, or the container name.
func (c Container) Service() string {
	if s := c.Labels["com.docker.compose.service"]; s != "" {
		if n := c.Labels["com.docker.compose.container-number"]; n != "" && n != "1" {
			return s + "-" + n
		}
		return s
	}
	return c.Name()
}

// ShortID is the 12-character id.
func (c Container) ShortID() string { return c.ID[:min(12, len(c.ID))] }

// APIError is a non-2xx answer from the Engine API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("docker api: status %d", e.Status)
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values) (*http.Response, error) {
	u := c.base + apiPrefix + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		defer func() { _ = resp.Body.Close() }() // read-only body
		var body struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body)
		return nil, &APIError{Status: resp.StatusCode, Message: body.Message}
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, v any) error {
	resp, err := c.do(ctx, http.MethodGet, path, q)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }() // read-only body
	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *Client) post(ctx context.Context, path string, q url.Values) error {
	resp, err := c.do(ctx, http.MethodPost, path, q)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// Containers lists every container, running or not.
func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	var out []Container
	if err := c.getJSON(ctx, "/containers/json", url.Values{"all": {"1"}}, &out); err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	return out, nil
}

// Details is what ds docker needs from a container inspect.
type Details struct {
	Env       map[string]string
	TTY       bool
	StartedAt time.Time
}

// Inspect returns a container's environment and settings.
func (c *Client) Inspect(ctx context.Context, id string) (Details, error) {
	var raw struct {
		Config struct {
			Env []string `json:"Env"`
			Tty bool     `json:"Tty"`
		} `json:"Config"`
		State struct {
			StartedAt time.Time `json:"StartedAt"`
		} `json:"State"`
	}
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/json", nil, &raw); err != nil {
		return Details{}, fmt.Errorf("inspect container %s: %w", id, err)
	}
	d := Details{Env: map[string]string{}, TTY: raw.Config.Tty, StartedAt: raw.State.StartedAt}
	for _, kv := range raw.Config.Env {
		k, v, _ := strings.Cut(kv, "=")
		d.Env[k] = v
	}
	return d, nil
}

// Start starts a container.
func (c *Client) Start(ctx context.Context, id string) error {
	if err := c.post(ctx, "/containers/"+url.PathEscape(id)+"/start", nil); err != nil {
		return fmt.Errorf("start %s: %w", id, err)
	}
	return nil
}

// Stop stops a container.
func (c *Client) Stop(ctx context.Context, id string) error {
	if err := c.post(ctx, "/containers/"+url.PathEscape(id)+"/stop", nil); err != nil {
		return fmt.Errorf("stop %s: %w", id, err)
	}
	return nil
}

// Restart restarts a container.
func (c *Client) Restart(ctx context.Context, id string) error {
	if err := c.post(ctx, "/containers/"+url.PathEscape(id)+"/restart", nil); err != nil {
		return fmt.Errorf("restart %s: %w", id, err)
	}
	return nil
}

// Remove removes a container, stopping it first if it runs. Volumes stay.
func (c *Client) Remove(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), url.Values{"force": {"1"}})
	if err != nil {
		return fmt.Errorf("remove %s: %w", id, err)
	}
	return resp.Body.Close()
}

// Logs opens a container's log stream: the last tail lines, then (with
// follow) new ones until ctx ends. Close the returned reader when done.
func (c *Client) Logs(ctx context.Context, id string, tail int, follow, tty bool) (io.ReadCloser, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {strconv.Itoa(tail)}}
	if follow {
		q.Set("follow", "1")
	}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs", q)
	if err != nil {
		return nil, fmt.Errorf("read logs of %s: %w", id, err)
	}
	if tty {
		return resp.Body, nil
	}
	return newDemux(resp.Body), nil
}

// demux strips the 8-byte frame headers Docker puts on non-TTY log streams.
type demux struct {
	r    *bufio.Reader
	c    io.Closer
	left int // bytes left in the current frame
}

func newDemux(rc io.ReadCloser) *demux { return &demux{r: bufio.NewReader(rc), c: rc} }

func (d *demux) Read(p []byte) (int, error) {
	for d.left == 0 {
		var hdr [8]byte
		if _, err := io.ReadFull(d.r, hdr[:]); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, io.EOF
			}
			return 0, err
		}
		d.left = int(binary.BigEndian.Uint32(hdr[4:]))
	}
	n, err := d.r.Read(p[:min(len(p), d.left)])
	d.left -= n
	return n, err
}

func (d *demux) Close() error { return d.c.Close() }

// DiskUsage is docker system df.
type DiskUsage struct {
	LayersSize int64 `json:"LayersSize"`
	Images     []struct {
		ID         string   `json:"Id"`
		RepoTags   []string `json:"RepoTags"`
		Size       int64    `json:"Size"`
		SharedSize int64    `json:"SharedSize"`
		Containers int      `json:"Containers"`
		Created    int64    `json:"Created"`
	} `json:"Images"`
	Volumes []struct {
		Name      string `json:"Name"`
		UsageData struct {
			Size     int64 `json:"Size"`
			RefCount int   `json:"RefCount"`
		} `json:"UsageData"`
	} `json:"Volumes"`
	BuildCache []struct {
		ID     string `json:"ID"`
		Size   int64  `json:"Size"`
		InUse  bool   `json:"InUse"`
		Shared bool   `json:"Shared"`
	} `json:"BuildCache"`
}

// DiskUsage returns sizes of images, volumes and build cache.
func (c *Client) DiskUsage(ctx context.Context) (DiskUsage, error) {
	var du DiskUsage
	if err := c.getJSON(ctx, "/system/df", nil, &du); err != nil {
		return DiskUsage{}, fmt.Errorf("read disk usage: %w", err)
	}
	return du, nil
}

type pruneReport struct {
	SpaceReclaimed int64 `json:"SpaceReclaimed"`
}

func (c *Client) prune(ctx context.Context, path string, q url.Values) (int64, error) {
	resp, err := c.do(ctx, http.MethodPost, path, q)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }() // read-only body
	var r pruneReport
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, err
	}
	return r.SpaceReclaimed, nil
}

// PruneDangling removes dangling images and unused build cache, and returns
// the bytes freed.
func (c *Client) PruneDangling(ctx context.Context) (int64, error) {
	img, err := c.prune(ctx, "/images/prune", url.Values{"filters": {`{"dangling":["true"]}`}})
	if err != nil {
		return 0, fmt.Errorf("prune dangling images: %w", err)
	}
	cache, err := c.prune(ctx, "/build/prune", nil)
	if err != nil {
		return img, fmt.Errorf("prune build cache: %w", err)
	}
	return img + cache, nil
}

// PruneVolumes removes unused anonymous volumes (Docker's default; named
// volumes such as database data stay) and returns the bytes freed.
func (c *Client) PruneVolumes(ctx context.Context) (int64, error) {
	n, err := c.prune(ctx, "/volumes/prune", nil)
	if err != nil {
		return 0, fmt.Errorf("prune volumes: %w", err)
	}
	return n, nil
}
