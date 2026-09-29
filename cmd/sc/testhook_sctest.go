//go:build sctest

package main

import (
	"os"
	"time"
)

// Only in the signal tests' build of sc (go build -tags sctest):
// SC_TEST_AFTER_RUN holds sc back for a while after the command has
// returned, so a test can send a signal in that window.
func init() {
	if d, err := time.ParseDuration(os.Getenv("SC_TEST_AFTER_RUN")); err == nil {
		testHookAfterRun = func() { time.Sleep(d) }
	}
}
