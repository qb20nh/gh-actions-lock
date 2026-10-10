//go:build !windows

package lockfile

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockMutationFile(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ErrConcurrentMutation
		}
		return fmt.Errorf("locking mutation guard: %w", err)
	}
	return nil
}
