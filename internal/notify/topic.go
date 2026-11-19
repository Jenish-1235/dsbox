// Package notify sends short phone pushes through ntfy (https://ntfy.sh) when
// Claude Code finishes or needs the developer. A push carries a title (the
// project name) and a fixed message, never code, prompts or file contents.
package notify

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// topicPattern is what ntfy accepts as a topic name.
var topicPattern = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

// NewTopic returns a fresh private topic, "devbox-" and 24 random lowercase
// letters and digits (120 bits). Anyone who knows a topic can read its
// pushes, so it must be unguessable.
func NewTopic() string {
	// rand.Text is 26 base32 characters (A-Z, 2-7) from the system's CSPRNG.
	return "devbox-" + strings.ToLower(rand.Text())[:24]
}

// LoadTopic reads the topic file. ok is false when it doesn't exist, which
// means phone pushes are off.
func LoadTopic(path string) (topic string, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}
	topic = strings.TrimSpace(string(data))
	if !topicPattern.MatchString(topic) {
		return "", false, fmt.Errorf("%s doesn't hold a valid ntfy topic", path)
	}
	return topic, true, nil
}

// SaveTopic writes topic to path, readable only by its owner (0600, in a
// 0700 folder when it creates one).
func SaveTopic(path, topic string) error {
	if !topicPattern.MatchString(topic) {
		return fmt.Errorf("invalid ntfy topic %q", topic)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create folder for %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(topic+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// WriteFile keeps the mode of an existing file; make sure it's private.
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("protect %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	return nil
}

// EnsureTopic loads the topic, creating and saving one when there is none.
func EnsureTopic(path string) (topic string, created bool, err error) {
	topic, ok, err := LoadTopic(path)
	if err != nil || ok {
		return topic, false, err
	}
	topic = NewTopic()
	if err := SaveTopic(path, topic); err != nil {
		return "", false, err
	}
	return topic, true, nil
}
