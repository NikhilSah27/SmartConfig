package boot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Unix(1_790_000_000, 0)

// Seen and verdict lines come back per boot, in the order the boots were
// first seen; lines nobody wrote are skipped.
func TestReadBack(t *testing.T) {
	home := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(Seen(home, "a", t0))
	must(Record(home, "a", "ok", t0.Add(time.Minute), 10, "local-fs=active emergency=inactive rescue=inactive failed-units=0"))
	must(Seen(home, "b", t0.Add(time.Hour)))
	must(Record(home, "c", "bad", t0.Add(2*time.Hour), 12, "local-fs=inactive\n emergency=inactive"))
	f, _ := os.OpenFile(filepath.Join(home, FileName), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("junk\nd seen notatime\ne ok 1\nf seen 5 extra\n\n")
	f.Close()
	bs, err := Read(home)
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, b := range bs {
		got += fmt.Sprintf("%s seen=%d %s at=%d row=%d %q\n", b.ID, b.Seen.Unix(), b.Verdict, b.At.Unix(), b.RowID, b.Why)
	}
	zero := time.Time{}.Unix()
	want := fmt.Sprintf("a seen=%d ok at=%d row=10 %q\n", t0.Unix(), t0.Unix()+60, "local-fs=active emergency=inactive rescue=inactive failed-units=0") +
		fmt.Sprintf("b seen=%d  at=%d row=0 \"\"\n", t0.Unix()+3600, zero) +
		fmt.Sprintf("c seen=%d bad at=%d row=12 %q\n", zero, t0.Unix()+7200, "local-fs=inactive emergency=inactive")
	if got != want {
		t.Errorf("got\n%swant\n%s", got, want)
	}
	if fi, _ := os.Stat(filepath.Join(home, FileName)); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if bs, err := Read(t.TempDir()); err != nil || bs != nil {
		t.Errorf("no file: %v %v", bs, err)
	}
	if err := Record(home, "a", "fine", t0, 1, ""); err == nil {
		t.Error("an unknown verdict was written")
	}
}

// Past maxSize the file keeps its last keepLines lines.
func TestTrim(t *testing.T) {
	home := t.TempDir()
	line := strings.Repeat("x", 100)
	var b strings.Builder
	for i := 0; b.Len() <= maxSize; i++ {
		fmt.Fprintf(&b, "old%d seen %d %s\n", i, i, line)
	}
	os.WriteFile(filepath.Join(home, FileName), []byte(b.String()), 0o600)
	if err := Seen(home, "new", t0); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, FileName))
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != keepLines+1 || !strings.HasPrefix(lines[len(lines)-1], "new seen ") {
		t.Errorf("%d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

// The last healthy boot is the last "ok" one, this boot included; a boot
// failed when its verdict is bad, or when it was seen and never got one
// and is not this boot.
func TestLastHealthyAndFailed(t *testing.T) {
	bs := []Boot{
		{ID: "a", Seen: t0, Verdict: "ok", RowID: 5},
		{ID: "b", Seen: t0, Verdict: "ok", RowID: 9},
		{ID: "c", Seen: t0}, // stuck in emergency mode
		{ID: "d", Verdict: "bad"},
		{ID: "e", Seen: t0, Verdict: "ok", RowID: 20}, // this boot
	}
	if b, i, ok := LastHealthy(bs); !ok || b.ID != "e" || i != 4 {
		t.Errorf("last healthy %v %d %v", b, i, ok)
	}
	if b, i, ok := LastHealthy(bs[:4]); !ok || b.ID != "b" || i != 1 {
		t.Errorf("last healthy before e %v %d %v", b, i, ok)
	}
	if _, i, ok := LastHealthy(nil); ok || i != -1 {
		t.Error("no boots")
	}
	for i, want := range []bool{false, false, true, true, false} {
		if got := bs[i].Failed("e"); got != want {
			t.Errorf("%s failed %v", bs[i].ID, got)
		}
	}
	if (Boot{ID: "e", Seen: t0}).Failed("e") {
		t.Error("this boot, on its way, failed")
	}
	if (Boot{ID: "x"}).Failed("e") {
		t.Error("a boot with no line at all failed")
	}
}

// Only a boot that mounted its filesystems and is in neither emergency
// nor rescue mode is ok; failed units are counted, not decisive.
func TestJudge(t *testing.T) {
	for _, tc := range []struct {
		local, emergency, rescue string
		failed                   int
		want                     string
	}{
		{"active", "inactive", "inactive", 0, "ok"},
		{"active", "inactive", "inactive", 2, "ok"},
		{"inactive", "inactive", "inactive", 0, "bad"},
		{"failed", "inactive", "inactive", 0, "bad"},
		{"active", "active", "inactive", 0, "bad"},
		{"active", "inactive", "active", 0, "bad"},
		{"", "", "", 0, "bad"},
	} {
		v, why := Judge(tc.local, tc.emergency, tc.rescue, tc.failed)
		if v != tc.want || !strings.Contains(why, fmt.Sprintf("failed-units=%d", tc.failed)) {
			t.Errorf("%+v: %s %q", tc, v, why)
		}
	}
}

func TestCurrentID(t *testing.T) {
	p := filepath.Join(t.TempDir(), "boot_id")
	old := IDPath
	IDPath = p
	t.Cleanup(func() { IDPath = old })
	os.WriteFile(p, []byte("aa661a28-7d4f-4668-8f5f-a57e548b8c5a\n"), 0o644)
	if id, err := CurrentID(); err != nil || id != "aa661a28-7d4f-4668-8f5f-a57e548b8c5a" {
		t.Errorf("%q %v", id, err)
	}
	os.WriteFile(p, []byte("a b\n"), 0o644)
	if _, err := CurrentID(); err == nil {
		t.Error("a boot id with a space")
	}
}

// A line of any length (zeroed blocks after a power cut) is one line to
// skip, not a file that cannot be read.
func TestReadLongLine(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, FileName), append(make([]byte, 70<<10), []byte("\na seen 1\n")...), 0o600)
	if bs, err := Read(home); err != nil || len(bs) != 1 || bs[0].ID != "a" {
		t.Errorf("%+v %v", bs, err)
	}
}

// A line a crash cut short, or zeros a power cut left at the end, are
// not glued to the next boot's line; a last line with no end is not read,
// nor are verdicts with a row or a word that do not parse (the M4 final
// review, B5: the next boot's "seen" was lost in the torn line).
func TestReadTornLines(t *testing.T) {
	for name, tail := range map[string]string{"torn": "bbbb seen 2", "zeros": string(make([]byte, 4096))} {
		home := t.TempDir()
		os.WriteFile(filepath.Join(home, FileName), []byte("aaaa seen 1\naaaa ok 2 7 why\n"+tail), 0o600)
		if bs, _ := Read(home); len(bs) != 1 || bs[0].ID != "aaaa" {
			t.Errorf("%s, before the next line: %+v", name, bs)
		}
		if err := Seen(home, "cccc", t0); err != nil {
			t.Fatal(err)
		}
		bs, err := Read(home)
		if err != nil || bs[len(bs)-1].ID != "cccc" || bs[len(bs)-1].Seen != t0 || bs[0].Verdict != "ok" {
			t.Errorf("%s: %+v %v", name, bs, err)
		}
	}
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, FileName), []byte("aaaa ok 2 x why\naaaa maybe 2 7 why\naaaa seen 1\n"), 0o600)
	if bs, _ := Read(home); len(bs) != 1 || bs[0].Verdict != "" {
		t.Errorf("an unreadable verdict was read: %+v", bs)
	}
}

// The boots file is written by root at boot: a symlink in its place is
// refused, and its target left as it was (the M4 final review, C6).
func TestAppendRefusesSymlink(t *testing.T) {
	home := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("keep\n"), 0o600)
	os.Symlink(victim, filepath.Join(home, FileName))
	if err := Seen(home, "cccc", t0); err == nil {
		t.Error("appended through a symlink")
	}
	os.Remove(filepath.Join(home, FileName))
	os.Mkdir(filepath.Join(home, FileName), 0o700)
	if err := Seen(home, "cccc", t0); err == nil {
		t.Error("appended to a directory")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep\n" {
		t.Errorf("victim: %q", b)
	}
}

// Appends are serialized by a lock, also across a trim: none is lost.
func TestAppendWhileTrimming(t *testing.T) {
	home := t.TempDir()
	var b strings.Builder
	for i := 0; b.Len() <= maxSize; i++ {
		fmt.Fprintf(&b, "old%d seen %d %s\n", i, i, strings.Repeat("x", 100))
	}
	os.WriteFile(filepath.Join(home, FileName), []byte(b.String()), 0o600)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := Seen(home, fmt.Sprintf("new%d", i), t0); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	data, _ := os.ReadFile(filepath.Join(home, FileName))
	for i := 0; i < 40; i++ {
		if !strings.Contains(string(data), fmt.Sprintf("\nnew%d seen ", i)) {
			t.Errorf("new%d lost", i)
		}
	}
}
