//go:build !linux

package downloader

import "os"

// preallocate falls back to a logical truncate on platforms without
// fallocate support.
func preallocate(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	return f.Truncate(size)
}
