//go:build windows

package main

// Windows builds are not shipped; behave as a single writer.
func acquireHistoryLock(historyPath string) (release func(), owner bool) {
	return func() {}, true
}
