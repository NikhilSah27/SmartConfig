//go:build linux

// Package watch is scd: it watches the scope's directories with inotify
// and records every change through the store (plan sections 3 and 6).
package watch

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"syscall"
)

// DirMask is the set of events watched on every directory (M2_WATCHLIST
// section 8): any of them makes a path dirty; IN_MODIFY only extends the
// quiet period. IN_DONT_FOLLOW and IN_ONLYDIR refuse a symlink to a
// directory and anything that is not a directory.
const DirMask = syscall.IN_CLOSE_WRITE | syscall.IN_MOVED_TO | syscall.IN_MOVED_FROM |
	syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_ATTRIB | syscall.IN_DELETE_SELF |
	syscall.IN_MOVE_SELF | syscall.IN_MODIFY | syscall.IN_DONT_FOLLOW | syscall.IN_ONLYDIR

// ReadBuf is the size of one read from an inotify instance.
const ReadBuf = 64 << 10

const eventHeader = 16 // wd, mask, cookie, len (struct inotify_event)

// Event is one inotify event. Name is empty for an event on the watched
// directory itself; Wd is -1 for IN_Q_OVERFLOW.
type Event struct {
	Wd     int
	Mask   uint32
	Cookie uint32
	Name   string
}

// Inotify is one inotify instance. Read blocks in Go's poller, so Close
// from another goroutine unblocks it.
type Inotify struct {
	fd int // for add/rm watch; f owns it (f.Fd() would make it blocking)
	f  *os.File
}

// NewInotify opens an instance with IN_CLOEXEC|IN_NONBLOCK.
func NewInotify() (*Inotify, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("inotify init: %w", err)
	}
	return &Inotify{fd: fd, f: os.NewFile(uintptr(fd), "inotify")}, nil
}

// AddWatch watches directory dir with DirMask and returns its watch
// descriptor. Watching an inode that is already watched returns the same
// descriptor (inotify(7)). A symlink, even to a directory, is refused.
func (in *Inotify) AddWatch(dir string) (int, error) {
	wd, err := syscall.InotifyAddWatch(in.fd, dir, DirMask)
	if err != nil {
		return -1, fmt.Errorf("watch %s: %w", dir, err)
	}
	return wd, nil
}

// RmWatch stops watch wd. The kernel then sends IN_IGNORED for it.
func (in *Inotify) RmWatch(wd int) error {
	if _, err := syscall.InotifyRmWatch(in.fd, uint32(wd)); err != nil {
		return fmt.Errorf("unwatch %d: %w", wd, err)
	}
	return nil
}

// Read waits for events and returns those of one read into buf, which
// should be ReadBuf bytes. After Close it returns os.ErrClosed.
func (in *Inotify) Read(buf []byte) ([]Event, error) {
	n, err := in.f.Read(buf)
	if err != nil {
		return nil, err
	}
	return parseEvents(buf[:n])
}

// Close closes the instance, ends its watches and unblocks Read.
func (in *Inotify) Close() error { return in.f.Close() }

// errShortEvent means a read ended inside an event, which the kernel never
// does.
var errShortEvent = errors.New("inotify: truncated event")

// parseEvents splits one read into events. Each struct inotify_event is
// followed by len bytes of name, padded with NULs.
func parseEvents(b []byte) ([]Event, error) {
	var evs []Event
	for len(b) > 0 {
		if len(b) < eventHeader {
			return evs, errShortEvent
		}
		e := Event{
			Wd:     int(int32(binary.NativeEndian.Uint32(b[0:]))),
			Mask:   binary.NativeEndian.Uint32(b[4:]),
			Cookie: binary.NativeEndian.Uint32(b[8:]),
		}
		n := int(binary.NativeEndian.Uint32(b[12:]))
		if len(b) < eventHeader+n {
			return evs, errShortEvent
		}
		name := b[eventHeader : eventHeader+n]
		for i, c := range name {
			if c == 0 {
				name = name[:i]
				break
			}
		}
		e.Name = string(name)
		evs = append(evs, e)
		b = b[eventHeader+n:]
	}
	return evs, nil
}
