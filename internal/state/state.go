// Package state records what ds has applied, so updates and rollbacks know
// where they stand and can tell their own edits from ds's.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// State is persisted as JSON in ~/.devbox/state/ds.json.
type State struct {
	// Current is the commit of devbox-setup that is applied.
	Current string `json:"current"`
	// Previous is the commit before the last update or rollback.
	Previous string `json:"previous"`
	// UpdatedAt is when Current was applied.
	UpdatedAt time.Time `json:"updated_at"`
	// Files maps each managed file to the sha256 of what ds last wrote there.
	Files map[string]string `json:"files"`
	// PendingAfterApply lists components whose after_apply commands failed;
	// the next apply runs them again even if no file changed.
	PendingAfterApply []string `json:"pending_after_apply,omitempty"`
}

// Load reads the state file. A missing file is an empty state.
func Load(path string) (State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{Files: map[string]string{}}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("parse state %s: %w", path, err)
	}
	if s.Files == nil {
		s.Files = map[string]string{}
	}
	return s, nil
}

// Save writes the state file atomically.
func (s State) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state folder: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}
