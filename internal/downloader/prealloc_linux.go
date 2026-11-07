//go:build linux

package downloader

import (
	"os"

	"golang.org/x/sys/unix"
)

// preallocate reserves disk space for the whole result before any bytes are
// fetched. Parallel range writes then land in a single contiguous extent,
// which avoids fragmentation on ext4/xfs/btrfs and surfaces "disk full"
// before a long transfer has already burned bandwidth. Failure is non-fatal:
// not every filesystem (or overlay) supports fallocate.
func preallocate(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	if err := unix.Fallocate(int(f.Fd()), 0, 0, size); err == nil {
		return nil
	}
	return f.Truncate(size)
}
