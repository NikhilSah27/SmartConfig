//go:build sctest

package main

import (
	"fmt"
	"os"
	"time"

	"smartconfig/internal/check"
)

// Only in the signal tests' build of sc (go build -tags sctest):
// SC_TEST_AFTER_RUN holds sc back for a while after the command has
// returned, and SC_TEST_IN_STATUS inside sc status before it reads
// anything (it says "sctest: in status" on stderr first), so a test can
// send a signal in that window.
// SC_TEST_STATUS_CHECKS holds it back once its store and scratch are
// open. SC_TEST_CONSOLE_LIMIT is sc status --console's limit.
// SC_TEST_TOOLS is the one directory validators are looked up in.
func init() {
	if d, err := time.ParseDuration(os.Getenv("SC_TEST_AFTER_RUN")); err == nil {
		testHookAfterRun = func() { time.Sleep(d) }
	}
	if d, err := time.ParseDuration(os.Getenv("SC_TEST_IN_STATUS")); err == nil {
		testHookInStatus = func() {
			fmt.Fprintln(os.Stderr, "sctest: in status")
			time.Sleep(d)
		}
	}
	if d, err := time.ParseDuration(os.Getenv("SC_TEST_STATUS_CHECKS")); err == nil {
		testHookStatusChecks = func() { time.Sleep(d) }
	}
	if d, err := time.ParseDuration(os.Getenv("SC_TEST_CONSOLE_LIMIT")); err == nil {
		consoleLimit = d
	}
	if d := os.Getenv("SC_TEST_TOOLS"); d != "" {
		testHookChecks = func(c *check.Checks) { c.Run.Dirs = []string{d} }
	}
}
