package notify

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// DefaultServer is the public ntfy server; its phone apps subscribe there.
const DefaultServer = "https://ntfy.sh"

// Message is one push: a short title and body, plus ntfy tags (emoji names
// shown in front of the title).
type Message struct {
	Title string
	Body  string
	Tags  []string
}

// Client publishes to an ntfy server. The zero value uses ntfy.sh and
// http.DefaultClient.
type Client struct {
	Server string       // e.g. https://ntfy.sh; empty means DefaultServer
	HTTP   *http.Client // nil means http.DefaultClient
}

// Send publishes m to topic. Bound it with ctx: hooks give it a few seconds.
func (c Client) Send(ctx context.Context, topic string, m Message) error {
	req, err := c.request(ctx, topic, m)
	if err != nil {
		return err
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("send push: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // read-only body
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("send push: ntfy answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c Client) request(ctx context.Context, topic string, m Message) (*http.Request, error) {
	if !topicPattern.MatchString(topic) {
		return nil, fmt.Errorf("invalid ntfy topic %q", topic)
	}
	server := c.Server
	if server == "" {
		server = DefaultServer
	}
	u, err := url.JoinPath(server, topic)
	if err != nil {
		return nil, fmt.Errorf("build ntfy URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(m.Body))
	if err != nil {
		return nil, fmt.Errorf("build ntfy request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("User-Agent", "devbox")
	if m.Title != "" {
		// Header values must be ASCII; ntfy decodes RFC 2047 words.
		req.Header.Set("Title", mime.QEncoding.Encode("utf-8", m.Title))
	}
	if len(m.Tags) > 0 {
		req.Header.Set("Tags", strings.Join(m.Tags, ","))
	}
	return req, nil
}
