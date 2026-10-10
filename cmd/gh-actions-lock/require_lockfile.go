package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
)

func requireLockfile() error {
	path := parserlock.Path
	if dir := os.Getenv("GH_ACTIONS_LOCK_WORKFLOWS_DIR"); dir != "" {
		path = filepath.Join(dir, "actions.lock")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving required lockfile: %w", err)
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving repository directory: %w", err)
	}
	// Trust the checkout location, including system aliases such as macOS /tmp.
	// Reject links in the repository-controlled path beneath that location.
	current := filepath.VolumeName(abs) + string(filepath.Separator)
	remainder := strings.TrimPrefix(abs, current)
	if relative, err := filepath.Rel(root, abs); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		current, remainder = root, relative
	}
	for _, part := range strings.Split(remainder, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("required lockfile %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("required lockfile %s has a symlinked path component: %s", path, current)
		}
		if current == abs && !info.Mode().IsRegular() {
			return fmt.Errorf("required lockfile %s must be a regular file", path)
		}
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("reading required lockfile %s: %w", path, err)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return fmt.Errorf("required lockfile %s is empty", path)
	}
	return nil
}
