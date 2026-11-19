// Package backup copies files aside before ds replaces them.
package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Store keeps one backup folder per ds run, created on first use.
type Store struct {
	dir string
}

// New returns a Store that writes under root/<timestamp>.
func New(root string, now time.Time) Store {
	return Store{dir: filepath.Join(root, now.Format("20060102-150405"))}
}

// Dir is the folder this run's backups go into.
func (s Store) Dir() string { return s.dir }

// Save copies path into the backup folder at rel, keeping its permissions,
// and returns where the copy went.
func (s Store) Save(path, rel string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s for backup: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s for backup: %w", path, err)
	}
	dst := filepath.Join(s.dir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("create backup folder: %w", err)
	}
	if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("write backup %s: %w", dst, err)
	}
	return dst, nil
}
