package lockfile

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func lockMutationFile(f *os.File) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &windows.Overlapped{}); err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return ErrConcurrentMutation
		}
		return fmt.Errorf("locking mutation guard: %w", err)
	}
	return nil
}
