package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"smartconfig/internal/boot"
)

// bootEnv gives sc boot a fake boot id and a fake systemctl whose
// is-active prints states (three lines) and list-units failed lines.
func bootEnv(t *testing.T, states string, failed int) (home string) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "home")
	t.Setenv("SC_HOME", home)
	dir := t.TempDir()
	id := filepath.Join(dir, "boot_id")
	os.WriteFile(id, []byte("b0b0-1\n"), 0o644)
	oldID, oldRunner, oldEnv := boot.IDPath, bootRunner, grubenvPath
	boot.IDPath = id
	bootRunner.Dirs = []string{dir}
	// GRUB's environment block, and a grub-editenv that logs its calls.
	grubenvPath = filepath.Join(dir, "grubenv")
	os.WriteFile(grubenvPath, []byte("# GRUB Environment Block\n"+strings.Repeat("#", 1024-25)), 0o644)
	os.WriteFile(filepath.Join(dir, "grub-editenv"), []byte("#!/bin/sh\necho \"$*\" >> "+filepath.Join(dir, "editenv.log")+"\n"), 0o755)
	t.Cleanup(func() { boot.IDPath, bootRunner, grubenvPath = oldID, oldRunner, oldEnv })
	if states != "" {
		script := "#!/bin/sh\ncase $1 in\nis-active) printf '" + states + "'; exit 3;;\n" +
			"list-units) [ " + itoa(failed) + " -lt 0 ] && exit 1; i=0; while [ $i -lt " + itoa(failed) + " ]; do echo \"x$i.service loaded failed failed X\"; i=$((i+1)); done;;\nesac\n"
		os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755)
	}
	return home
}

func itoa(n int) string { return strconv.Itoa(n) }

// A boot that came up with its filesystems mounted is ok, whatever units
// failed; the verdict names the newest store row.
func TestBootVerdict(t *testing.T) {
	home := bootEnv(t, `active\ninactive\ninactive\n`, 2)
	mustSC(t, "init")
	f := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(f, []byte("a\n"), 0o644)
	mustSC(t, "snapshot", f)
	os.WriteFile(f, []byte("b\n"), 0o644)
	mustSC(t, "snapshot", f)
	mustSC(t, "boot", "seen")
	out := mustSC(t, "boot", "verdict")
	if out != "boot b0b0-1: ok (local-fs=active emergency=inactive rescue=inactive failed-units=2)\n" {
		t.Errorf("stdout %q", out)
	}
	bs, err := boot.Read(home)
	if err != nil || len(bs) != 1 || bs[0].ID != "b0b0-1" || bs[0].Seen.IsZero() || bs[0].Verdict != "ok" || bs[0].RowID != 2 {
		t.Errorf("boots %+v %v", bs, err)
	}
}

// A mount that never happened leaves local-fs.target inactive: bad.
func TestBootVerdictBad(t *testing.T) {
	home := bootEnv(t, `inactive\ninactive\ninactive\n`, 0)
	if r := sc(t, "boot", "verdict"); r.code != 0 || !strings.Contains(r.stdout, ": bad (local-fs=inactive ") {
		t.Fatalf("%+v", r)
	}
	if bs, _ := boot.Read(home); len(bs) != 1 || bs[0].Verdict != "bad" || bs[0].RowID != -1 { // no store: no row
		t.Errorf("boots %+v", bs)
	}
}

// Without systemctl, or with an answer sc cannot read, no verdict is
// written: one line, exit 1.
func TestBootVerdictNoSystemctl(t *testing.T) {
	home := bootEnv(t, "", 0)
	if r := sc(t, "boot", "verdict"); r.code != 1 || r.stderr != "sc: systemctl not found\n" {
		t.Errorf("%+v", r)
	}
	bootEnv(t, `active\n`, 0)
	if r := sc(t, "boot", "verdict"); r.code != 1 || !strings.Contains(r.stderr, "gave 1 states, not 3") {
		t.Errorf("%+v", r)
	}
	if bs, _ := boot.Read(home); len(bs) != 0 {
		t.Errorf("boots %+v", bs)
	}
}

// sc boot seen makes $SC_HOME if it is not there yet (the first boot
// after an install) and is hidden from the command list.
func TestBootSeen(t *testing.T) {
	home := bootEnv(t, "", 0)
	mustSC(t, "boot", "seen")
	if bs, _ := boot.Read(home); len(bs) != 1 || bs[0].Seen.IsZero() || bs[0].Verdict != "" {
		t.Errorf("boots %+v", bs)
	}
	if strings.Contains(mustSC(t, "--help"), "\n  boot ") {
		t.Error("sc boot is listed")
	}
}

// A list-units that fails is an unknown count, not zero; a store that
// cannot be read gives row -1, never 0, which would mean "every row".
func TestBootVerdictUnknowns(t *testing.T) {
	home := bootEnv(t, `active\ninactive\ninactive\n`, -1)
	os.MkdirAll(home, 0o700)
	os.WriteFile(filepath.Join(home, "changes.db"), []byte("not a database"), 0o600)
	r := sc(t, "boot", "verdict")
	if r.code != 0 || !strings.Contains(r.stdout, ": ok (") || !strings.Contains(r.stdout, "failed-units=-1)") {
		t.Fatalf("%+v", r)
	}
	if bs, _ := boot.Read(home); len(bs) != 1 || bs[0].RowID != -1 {
		t.Errorf("boots %+v", bs)
	}
}

// editenvCalls is what the fake grub-editenv was asked, one call a line.
func editenvCalls(t *testing.T) string {
	b, _ := os.ReadFile(filepath.Join(bootRunner.Dirs[0], "editenv.log"))
	return strings.ReplaceAll(string(b), grubenvPath, "ENV")
}

// The menu flag: sc boot seen sets it, a healthy verdict unsets it, a bad
// one leaves it; without GRUB's 1024-byte block it is not touched, and the
// journal says so.
func TestBootMenuFlag(t *testing.T) {
	bootEnv(t, `active\ninactive\ninactive\n`, 0)
	mustSC(t, "boot", "seen")
	mustSC(t, "boot", "verdict")
	if got := editenvCalls(t); got != "ENV set smartconfig_pending=1\nENV unset smartconfig_pending\n" {
		t.Errorf("healthy boot: %q", got)
	}

	bootEnv(t, `inactive\ninactive\ninactive\n`, 0)
	mustSC(t, "boot", "seen")
	mustSC(t, "boot", "verdict")
	if got := editenvCalls(t); got != "ENV set smartconfig_pending=1\n" {
		t.Errorf("bad boot: %q", got)
	}

	bootEnv(t, "", 0)
	os.Remove(grubenvPath) // a separate /boot, not mounted yet
	if out := mustSC(t, "boot", "seen"); !strings.Contains(out, "no GRUB environment block at ") || editenvCalls(t) != "" {
		t.Errorf("no grubenv: %q, calls %q", out, editenvCalls(t))
	}
	os.WriteFile(grubenvPath, []byte("short"), 0o644)
	if out := mustSC(t, "boot", "seen"); !strings.Contains(out, "no GRUB environment block") || editenvCalls(t) != "" {
		t.Errorf("not a grubenv: %q", out)
	}
	// A symlink is not GRUB's block, whatever it points at.
	real := filepath.Join(t.TempDir(), "grubenv")
	os.WriteFile(real, make([]byte, 1024), 0o644)
	os.Remove(grubenvPath)
	os.Symlink(real, grubenvPath)
	if out := mustSC(t, "boot", "seen"); !strings.Contains(out, "no GRUB environment block") || editenvCalls(t) != "" {
		t.Errorf("a symlink: %q", out)
	}
	// A grub-editenv that fails is said, and the boot still recorded.
	os.Remove(grubenvPath)
	os.WriteFile(grubenvPath, make([]byte, 1024), 0o644)
	os.WriteFile(filepath.Join(bootRunner.Dirs[0], "grub-editenv"), []byte("#!/bin/sh\nexit 1\n"), 0o755)
	if out := mustSC(t, "boot", "seen"); !strings.Contains(out, "grub-editenv set failed (exit 1)") {
		t.Errorf("a failing grub-editenv: %q", out)
	}
}

// The real grub-editenv, where installed, on a grubenv of the test: the
// flag is set and unset as GRUB's load_env will read it.
func TestBootMenuFlagRealEditenv(t *testing.T) {
	editenv, err := exec.LookPath("grub-editenv")
	if err != nil {
		t.Skip("no grub-editenv")
	}
	old := grubenvPath
	grubenvPath = filepath.Join(t.TempDir(), "grubenv")
	t.Cleanup(func() { grubenvPath = old })
	if out, err := exec.Command(editenv, grubenvPath, "create").CombinedOutput(); err != nil {
		t.Fatalf("create: %v %s", err, out)
	}
	list := func() string {
		out, _ := exec.Command(editenv, grubenvPath, "list").Output()
		return string(out)
	}
	if note := menuFlag(context.Background(), true); note != "" || list() != "smartconfig_pending=1\n" {
		t.Fatalf("set: %q, list %q", note, list())
	}
	if note := menuFlag(context.Background(), false); note != "" || list() != "" {
		t.Fatalf("unset: %q, list %q", note, list())
	}
}
