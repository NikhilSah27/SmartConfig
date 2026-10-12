package main

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

// atTerminal reports whether a command may ask the owner a question: only
// when both in and out are terminals. Without one, sc restore read a
// script's next line as its answer, or waited with its prompt in a file
// (the chunk J review, J6); sc restore and sc prune then refuse and name
// the flag that does not ask. The tests replace it.
var atTerminal = func(in io.Reader, out io.Writer) bool {
	return isTerminal(in) && isTerminal(out)
}

// isTerminal reports whether v is a file open on a terminal (TCGETS works
// on it). SyscallConn leaves the file's blocking mode as it is, where Fd
// would set it.
func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok || f == nil {
		return false
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return false
	}
	tty := false
	rc.Control(func(fd uintptr) {
		var t syscall.Termios
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
		tty = e == 0
	})
	return tty
}
