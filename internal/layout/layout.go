// Package layout knows where devbox keeps its files on disk.
package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Layout is the set of directories devbox uses. Root is ~/.devbox unless
// DEVBOX_HOME overrides it, and Etc is /etc/devbox unless DEVBOX_ETC does
// (both used by tests).
type Layout struct {
	Home string
	Root string
	Etc  string
}

// FromEnv builds the layout for the current user.
func FromEnv() (Layout, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Layout{}, fmt.Errorf("find home directory: %w", err)
	}
	root := os.Getenv("DEVBOX_HOME")
	if root == "" {
		root = filepath.Join(home, ".devbox")
	}
	etc := os.Getenv("DEVBOX_ETC")
	if etc == "" {
		etc = "/etc/devbox"
	}
	return Layout{Home: home, Root: root, Etc: etc}, nil
}

// Src is the clone of the devbox-setup repo.
func (l Layout) Src() string { return filepath.Join(l.Root, "src") }

// Bin holds ds and the other devbox commands. It is on PATH.
func (l Layout) Bin() string { return filepath.Join(l.Root, "bin") }

// StateDir holds ds bookkeeping: applied version, file hashes, previous binary.
func (l Layout) StateDir() string { return filepath.Join(l.Root, "state") }

// StateFile is where ds records what it applied.
func (l Layout) StateFile() string { return filepath.Join(l.StateDir(), "ds.json") }

// PreviousBinary is the ds binary from before the last update, kept for rollback.
func (l Layout) PreviousBinary() string { return l.PreviousCommand("ds") }

// PreviousCommand is the binary of command name (ds, dev, …) from before the
// last update, kept for rollback.
func (l Layout) PreviousCommand(name string) string {
	return filepath.Join(l.StateDir(), name+".prev")
}

// Logs holds output from dev servers and background services.
func (l Layout) Logs() string { return filepath.Join(l.Root, "logs") }

// Backups holds copies of files ds replaced, one folder per run.
func (l Layout) Backups() string { return filepath.Join(l.Root, "backups") }

// Templates holds project templates for dev new.
func (l Layout) Templates() string { return filepath.Join(l.Root, "templates") }

// UpdatesFile caches the last check for updates, written by ds check-updates.
func (l Layout) UpdatesFile() string { return filepath.Join(l.StateDir(), "updates.json") }

// MirrorsFile caches the last check of the developer's GitHub mirrors, written by ds check-mirrors.
func (l Layout) MirrorsFile() string { return filepath.Join(l.StateDir(), "mirrors.json") }

// Inbox holds screenshots uploaded from the laptop (devbox-paste), kept a week.
func (l Layout) Inbox() string { return filepath.Join(l.Root, "inbox") }

// NotifyTopicFile holds the developer's private ntfy topic for phone pushes.
// It only exists after ds notify setup.
func (l Layout) NotifyTopicFile() string {
	return filepath.Join(l.Home, ".config", "devbox", "ntfy-topic")
}

// NotifyStateDir holds when each project last sent a phone push (rate limit).
func (l Layout) NotifyStateDir() string { return filepath.Join(l.StateDir(), "notify") }

// MachineConfig is the machine settings file that provision.sh installs.
func (l Layout) MachineConfig() string { return filepath.Join(l.Etc, "devbox.env") }

// UserConfig is the developer's own settings (same format as MachineConfig),
// which override the machine's: ~/.devbox/config.env.
func (l Layout) UserConfig() string { return filepath.Join(l.Root, "config.env") }

// RepoConfig is the repo's copy of the machine settings.
func (l Layout) RepoConfig() string { return filepath.Join(l.Src(), "config", "devbox.env") }

// Components is where component definitions live inside the source clone.
func (l Layout) Components() string { return filepath.Join(l.Src(), "components") }

// Dirs lists every directory devbox expects to exist.
func (l Layout) Dirs() []string {
	return []string{l.Root, l.Bin(), l.StateDir(), l.Logs(), l.Backups(), l.Templates(), l.Inbox()}
}

// Ensure creates any missing devbox directories.
func (l Layout) Ensure() error {
	for _, dir := range l.Dirs() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// Expand turns "~/x" into a path under Home. Other paths are returned unchanged.
func (l Layout) Expand(path string) string {
	if path == "~" {
		return l.Home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(l.Home, rest)
	}
	return path
}

// Pretty turns a path under Home back into "~/x" for display.
func (l Layout) Pretty(path string) string {
	if rel, err := filepath.Rel(l.Home, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return path
}
