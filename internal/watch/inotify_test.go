//go:build linux

package watch

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func newInotify(t *testing.T) *Inotify {
	t.Helper()
	in, err := NewInotify()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() })
	return in
}

// readUntil reads events until want returns true for the whole list, or
// fails after a deadline.
func readUntil(t *testing.T, in *Inotify, want func([]Event) bool) []Event {
	t.Helper()
	var all []Event
	got := make(chan []Event)
	errc := make(chan error, 1)
	go func() {
		buf := make([]byte, ReadBuf)
		for {
			evs, err := in.Read(buf)
			if err != nil {
				errc <- err
				return
			}
			got <- evs
		}
	}()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evs := <-got:
			all = append(all, evs...)
			if want(all) {
				return all
			}
		case err := <-errc:
			t.Fatalf("read: %v (events so far %+v)", err, all)
		case <-deadline:
			t.Fatalf("no matching events in time; got %+v", all)
		}
	}
}

func has(evs []Event, name string, mask uint32) *Event {
	for i := range evs {
		if evs[i].Name == name && evs[i].Mask&mask == mask {
			return &evs[i]
		}
	}
	return nil
}

func TestInotifyEvents(t *testing.T) {
	in := newInotify(t)
	dir := t.TempDir()
	wd, err := in.AddWatch(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "conf")
	os.WriteFile(p, []byte("x"), 0o644) // CREATE, CLOSE_WRITE
	os.Chmod(p, 0o600)                  // ATTRIB
	os.Rename(p, filepath.Join(dir, "conf2"))
	os.Mkdir(filepath.Join(dir, "sub"), 0o755) // CREATE|ISDIR
	evs := readUntil(t, in, func(evs []Event) bool {
		return has(evs, "sub", syscall.IN_CREATE|syscall.IN_ISDIR) != nil
	})
	for _, c := range []struct {
		name string
		mask uint32
	}{
		{"conf", syscall.IN_CREATE},
		{"conf", syscall.IN_CLOSE_WRITE},
		{"conf", syscall.IN_ATTRIB},
		{"conf", syscall.IN_MOVED_FROM},
		{"conf2", syscall.IN_MOVED_TO},
	} {
		e := has(evs, c.name, c.mask)
		if e == nil || e.Wd != wd {
			t.Errorf("no %#x event for %s on wd %d: %+v", c.mask, c.name, wd, evs)
		}
	}
	from, to := has(evs, "conf", syscall.IN_MOVED_FROM), has(evs, "conf2", syscall.IN_MOVED_TO)
	if from == nil || to == nil || from.Cookie == 0 || from.Cookie != to.Cookie {
		t.Fatalf("rename cookies: %+v %+v", from, to)
	}
}

// Several events arrive in one read, with names padded to different
// lengths, and the watched directory's own events have an empty name.
func TestInotifySeveralEventsPerRead(t *testing.T) {
	in := newInotify(t)
	dir := t.TempDir()
	in.AddWatch(dir)
	names := []string{"a", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "ccc.conf", "é"}
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, n), nil, 0o644)
	}
	time.Sleep(50 * time.Millisecond) // let them queue up
	buf := make([]byte, ReadBuf)
	evs, err := in.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) < 2*len(names) {
		t.Fatalf("one read gave %d events, want at least %d: %+v", len(evs), 2*len(names), evs)
	}
	for _, n := range names {
		if has(evs, n, syscall.IN_CREATE) == nil {
			t.Errorf("no CREATE for %q", n)
		}
	}
	// The directory's own events have an empty name.
	os.Chmod(dir, 0o700)
	evs = readUntil(t, in, func(evs []Event) bool { return has(evs, "", syscall.IN_ATTRIB|syscall.IN_ISDIR) != nil })
	if e := has(evs, "", syscall.IN_ATTRIB); e.Name != "" {
		t.Fatalf("%+v", e)
	}
}

func TestParseEvents(t *testing.T) {
	ev := func(wd int32, mask, cookie uint32, name string, pad int) []byte {
		b := make([]byte, 16+len(name)+pad)
		binary.NativeEndian.PutUint32(b[0:], uint32(wd))
		binary.NativeEndian.PutUint32(b[4:], mask)
		binary.NativeEndian.PutUint32(b[8:], cookie)
		binary.NativeEndian.PutUint32(b[12:], uint32(len(name)+pad))
		copy(b[16:], name)
		return b
	}
	var b []byte
	b = append(b, ev(-1, syscall.IN_Q_OVERFLOW, 0, "", 0)...) // a synthetic overflow
	b = append(b, ev(3, syscall.IN_MOVED_FROM, 7, "x", 15)...)
	b = append(b, ev(3, syscall.IN_MOVED_TO, 7, "sixteen-chars-ab", 16)...)
	evs, err := parseEvents(b)
	if err != nil || len(evs) != 3 {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs[0].Wd != -1 || evs[0].Mask != syscall.IN_Q_OVERFLOW || evs[0].Name != "" {
		t.Fatalf("overflow %+v", evs[0])
	}
	if evs[1].Name != "x" || evs[1].Cookie != 7 || evs[2].Name != "sixteen-chars-ab" || evs[2].Wd != 3 {
		t.Fatalf("%+v", evs)
	}
	for _, cut := range []int{5, 20, 16 + 16 + 3, len(b) - 1} {
		if _, err := parseEvents(b[:cut]); !errors.Is(err, errShortEvent) {
			t.Errorf("cut at %d: %v", cut, err)
		}
	}
}

// A symlink to a directory, and a file, cannot be watched; watching the
// same directory twice gives the same descriptor.
func TestInotifyAddWatch(t *testing.T) {
	in := newInotify(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	link := filepath.Join(dir, "link")
	os.Symlink(sub, link)
	if _, err := in.AddWatch(link); err == nil {
		t.Fatal("watched a symlink to a directory")
	}
	f := filepath.Join(dir, "f")
	os.WriteFile(f, nil, 0o644)
	if _, err := in.AddWatch(f); err == nil {
		t.Fatal("watched a file")
	}
	w1, err1 := in.AddWatch(sub)
	w2, err2 := in.AddWatch(sub)
	if err1 != nil || err2 != nil || w1 != w2 {
		t.Fatalf("%d %v, %d %v", w1, err1, w2, err2)
	}
	// After rm_watch the kernel sends IN_IGNORED for it.
	if err := in.RmWatch(w1); err != nil {
		t.Fatal(err)
	}
	evs := readUntil(t, in, func(evs []Event) bool {
		for _, e := range evs {
			if e.Wd == w1 && e.Mask&syscall.IN_IGNORED != 0 {
				return true
			}
		}
		return false
	})
	_ = evs
}

// Close unblocks a Read that is waiting.
func TestInotifyCloseUnblocksRead(t *testing.T) {
	in, err := NewInotify()
	if err != nil {
		t.Fatal(err)
	}
	in.AddWatch(t.TempDir())
	done := make(chan error, 1)
	go func() {
		_, err := in.Read(make([]byte, ReadBuf))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	in.Close()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("read after close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not unblock Read")
	}
}
