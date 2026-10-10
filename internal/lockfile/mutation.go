package lockfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ErrConcurrentMutation indicates another process holds the writer lock.
var ErrConcurrentMutation = errors.New("another actions-lock mutation is in progress")

// ErrStaleLockfile indicates the file changed after this State loaded it.
var ErrStaleLockfile = errors.New("lockfile changed since it was loaded; retry the run")

// BeginMutation checks the loaded baseline and holds the writer lock until
// release is called. Call it before changing workflow or action files.
func (s *State) BeginMutation(ctx context.Context) (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.mutationFile != nil {
		return nil, ErrConcurrentMutation
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("locating mutation lock directory: %w", err)
	}
	dir := filepath.Join(cache, "gh-actions-lock", "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating mutation lock directory: %w", err)
	}
	path := s.lockPath
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	} else if resolved, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(resolved, filepath.Base(path))
	}
	key := sha256.Sum256([]byte(path))
	f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%x.lock", key)), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening mutation lock: %w", err)
	}
	if err := lockMutationFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := s.checkBaseline(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = f.Close()
		return nil, err
	}
	s.mutationFile = f
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.mutationFile = nil
			// Keep the guard inode so another process cannot lock a replacement
			// while a contender still holds the original file open.
			_ = f.Close()
		})
	}, nil
}

// checkBaseline requires s.mu to be held and the mutation lock acquired.
func (s *State) checkBaseline() error {
	contents, err := os.ReadFile(s.lockPath)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking lockfile baseline: %w", err)
	}
	if exists != s.baselineExists || !bytes.Equal(contents, s.baseline) {
		return ErrStaleLockfile
	}
	return nil
}
