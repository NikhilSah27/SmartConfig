package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"smartconfig/internal/store"
)

func TestOctalModeSpecialBits(t *testing.T) {
	cases := map[os.FileMode]string{
		0o644:                        "0644",
		0o755 | os.ModeSetuid:        "4755",
		0o2755&0o777 | os.ModeSetgid: "2755",
		0o1777&0o777 | os.ModeSticky: "1777",
		0o755 | os.ModeSetuid | os.ModeSetgid | os.ModeSticky: "7755",
	}
	for m, want := range cases {
		if got := octalMode(m); got != want {
			t.Errorf("octalMode(%v) = %s, want %s", m, got, want)
		}
	}
}

func TestQuietSnapshotWhenUnchanged(t *testing.T) {
	t.Setenv("SC_HOME", t.TempDir())
	mustSC(t, "init")
	p := filepath.Join(t.TempDir(), "conf")
	os.WriteFile(p, []byte("x\n"), 0o644)
	id := mustSC(t, "snapshot", "-q", p)
	if again := mustSC(t, "snapshot", "-q", p); again != id {
		t.Fatalf("-q when unchanged printed %q, want %q", again, id)
	}
}

func TestRestoreMessageWhenNoPreviousFile(t *testing.T) {
	t.Setenv("SC_HOME", t.TempDir())
	mustSC(t, "init")
	p := filepath.Join(t.TempDir(), "conf")
	os.WriteFile(p, []byte("x\n"), 0o640)
	id := strings.TrimSpace(mustSC(t, "snapshot", "-q", p))
	os.Remove(p)
	out := mustSC(t, "restore", id)
	want := "restored " + p + " from " + id + " (mode 0640 " + userName(os.Getuid()) + ":" + groupName(os.Getgid()) + "), no previous file existed\n"
	if out != want {
		t.Fatalf("got  %q\nwant %q", out, want)
	}
}

// sc log's columns come in the spec's order, and WHEN is local time.
func TestLogColumnsAndLocalTime(t *testing.T) {
	oldLocal := time.Local
	time.Local = time.FixedZone("TEST", 5*3600+30*60)
	t.Cleanup(func() { time.Local = oldLocal })
	home := t.TempDir()
	t.Setenv("SC_HOME", home)
	mustSC(t, "init")
	p := filepath.Join(t.TempDir(), "conf")
	os.WriteFile(p, []byte("x\n"), 0o644)
	mustSC(t, "snapshot", p, "-m", "why")
	out := mustSC(t, "log", p)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if got := strings.Fields(lines[0]); strings.Join(got, " ") != "ID WHEN ORIGIN FILE SIZE WHAT" {
		t.Fatalf("header %q", lines[0])
	}
	s, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cs, _ := s.List(p, 1)
	when := time.Unix(cs[0].TS, 0).In(time.Local).Format("2006-01-02 15:04")
	f := strings.Fields(lines[1])
	if f[0] != cs[0].ID || f[1]+" "+f[2] != when || f[3] != "manual" || f[4] != p || f[5] != "2" || f[6] != "why" {
		t.Fatalf("row %q, want local time %s", lines[1], when)
	}
}
