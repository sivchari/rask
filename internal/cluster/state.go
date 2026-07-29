package cluster

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// DefaultHomeDir returns the directory rask stores its cluster state under,
// rooted at the current user's home directory: ~/.rask.
func DefaultHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving user home directory: %w", err)
	}

	return filepath.Join(home, ".rask"), nil
}

// Dir returns the state directory for the cluster named name, rooted at
// homeDir (as returned by DefaultHomeDir).
func Dir(homeDir, name string) string {
	return filepath.Join(homeDir, "clusters", name)
}

// List returns the names of clusters with a state directory under homeDir,
// sorted alphabetically. A homeDir with no clusters subdirectory yet is not
// an error; it yields a nil, empty list.
func List(homeDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(homeDir, "clusters"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("listing clusters in %s: %w", homeDir, err)
	}

	var names []string

	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	sort.Strings(names)

	return names, nil
}

// Exists reports whether a cluster named name has a state directory under
// homeDir.
func Exists(homeDir, name string) (bool, error) {
	info, err := os.Stat(Dir(homeDir, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}

		return false, fmt.Errorf("checking cluster %q: %w", name, err)
	}

	return info.IsDir(), nil
}
