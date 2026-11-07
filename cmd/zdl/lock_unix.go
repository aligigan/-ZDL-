//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// acquireHistoryLock elects one dashboard process as the writer. Other
// terminals become live viewers instead of racing on the history file.
func acquireHistoryLock(historyPath string) (release func(), owner bool) {
	if historyPath == "" {
		return func() {}, true
	}
	if err := os.MkdirAll(filepath.Dir(historyPath), 0o755); err != nil {
		return func() {}, true
	}
	f, err := os.OpenFile(historyPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return func() {}, false
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, true
}
