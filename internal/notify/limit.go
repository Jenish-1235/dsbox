package notify

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Limiter allows at most one push per key (project) every Every. It keeps
// one small file per key in Dir and locks it, so hooks that fire together
// (Notification and Stop) can't both send.
type Limiter struct {
	Dir   string
	Every time.Duration
}

// Allow reports whether key may send a push at now, and records it if so.
func (l Limiter) Allow(key string, now time.Time) (bool, error) {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return false, fmt.Errorf("create %s: %w", l.Dir, err)
	}
	path := filepath.Join(l.Dir, safeKey(key))
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	// Closing releases the lock. Writes are checked below; a close error on
	// this one-line timestamp file can't be acted on.
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return false, fmt.Errorf("lock %s: %w", path, err)
	}
	// Closing the file releases the lock.

	data, err := io.ReadAll(f)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if last, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
		if since := now.Sub(time.Unix(0, last)); since >= 0 && since < l.Every {
			return false, nil
		}
	}
	if err := f.Truncate(0); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := f.WriteAt([]byte(strconv.FormatInt(now.UnixNano(), 10)+"\n"), 0); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// safeKey turns a project name into a file name.
func safeKey(key string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, key)
	if clean == "" || strings.Trim(clean, ".") == "" {
		return "_"
	}
	return clean
}
