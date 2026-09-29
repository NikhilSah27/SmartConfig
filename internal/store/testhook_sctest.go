//go:build sctest

package store

import (
	"os"
	"time"
)

// Only in the signal tests' build of sc (go build -tags sctest):
// SC_TEST_BEFORE_RESTORE_LOCK pauses a restore after its pre-restore row is
// committed and before it takes the write lock for the rename, so a test
// can reliably act in that window.
func init() {
	if d, err := time.ParseDuration(os.Getenv("SC_TEST_BEFORE_RESTORE_LOCK")); err == nil {
		testHookBeforeRestoreLock = func() { time.Sleep(d) }
	}
}
