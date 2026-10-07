package main

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
)

// TestMain: started under nohup, go test's children would inherit SIGHUP
// ignored, and sc keeps a hangup its caller ignores (main.go), so the
// tests that hang one up failed (TestConsoleStatusOutlivesHangup,
// TestWatchSIGHUPRescans, and the lab's own through TestLabPython: the
// chunk H flake hunt). A handler in this process is reset to the default
// in every child it starts.
func TestMain(m *testing.M) {
	if signal.Ignored(syscall.SIGHUP) {
		signal.Notify(make(chan os.Signal, 1), syscall.SIGHUP)
	}
	os.Exit(m.Run())
}
