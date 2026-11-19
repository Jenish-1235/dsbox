// Package inbox manages ~/.devbox/inbox: screenshots uploaded from the laptop
// (devbox-paste) so Claude Code on the devbox can read them by path.
package inbox

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Keep is how long screenshots stay in the inbox.
const Keep = 7 * 24 * time.Hour

// File is one file in the inbox.
type File struct {
	Path    string
	ModTime time.Time
	Size    int64
}

// List returns the inbox's files, newest first. A missing inbox is empty.
func List(dir string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var files []File
	for _, e := range entries {
		// Skip folders and hidden files (e.g. an upload still in progress).
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // removed while listing
		}
		files = append(files, File{Path: filepath.Join(dir, e.Name()), ModTime: info.ModTime(), Size: info.Size()})
	}
	slices.SortFunc(files, func(a, b File) int {
		if c := b.ModTime.Compare(a.ModTime); c != 0 {
			return c
		}
		return cmp.Compare(b.Path, a.Path)
	})
	return files, nil
}

// Clean removes files last changed more than maxAge before now and returns
// how many it removed.
func Clean(dir string, maxAge time.Duration, now time.Time) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", dir, err)
	}
	removed := 0
	var errs []error
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}
