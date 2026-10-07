//go:build linux

package check

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// toolDirs is where validators are looked up, never through $PATH.
var toolDirs = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// fullPathTools are the validators that live outside toolDirs. A checker
// names one by its full path, and it is looked up there only.
var fullPathTools = []string{"/usr/libexec/netplan/generate"}

// Runner starts validators (plan 5.3). It is the only place in sc that
// runs another program on a file's content.
type Runner struct {
	Dirs    []string      // where tools are looked up; nil: toolDirs
	Timeout time.Duration // 0: 10 s
	MaxOut  int           // most output kept; 0: 64 KiB
}

// Result is what one validator run gave.
type Result struct {
	Found     bool   // the tool exists; false from a rescue shell without it
	Exit      int    // its exit status; -1 when it was killed
	Out       []byte // its stdout, at most MaxOut bytes
	Err       []byte // its stderr, at most MaxOut bytes
	Truncated bool   // more of either than MaxOut
	TimedOut  bool   // killed after Timeout, with its process group
}

// Run starts tool with args in dir and waits for it. tool is a name looked
// up in Dirs, or one of fullPathTools (with Dirs set, which only tests do,
// that too is looked up there, by its base name). The arguments go as
// a list, never through a shell; the environment is LC_ALL=C and a fixed
// PATH; stdin is /dev/null. A tool that is not installed, exits non-zero or
// times out is a Result, not an error.
func (r Runner) Run(ctx context.Context, dir, tool string, args ...string) (Result, error) {
	return r.RunEnv(ctx, dir, nil, tool, args...)
}

// RunEnv is Run with env added to the environment, for a setting a tool
// takes from there only (systemd-analyze's unit path).
func (r Runner) RunEnv(ctx context.Context, dir string, env []string, tool string, args ...string) (Result, error) {
	name, dirs := tool, r.Dirs
	if dirs == nil {
		dirs = toolDirs
	}
	switch {
	case slices.Contains(fullPathTools, tool):
		name = filepath.Base(tool)
		if r.Dirs == nil {
			dirs = []string{filepath.Dir(tool)}
		}
	case tool == "" || strings.ContainsRune(tool, '/'):
		return Result{}, fmt.Errorf("run %q: not a tool name", tool)
	}
	path := ""
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			path = p
			break
		}
	}
	if path == "" {
		return Result{}, nil
	}
	timeout, maxOut := r.Timeout, r.MaxOut
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	if maxOut == 0 {
		maxOut = 64 << 10
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, path, args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"LC_ALL=C", "PATH=" + strings.Join(toolDirs, ":")}, env...)
	// Its own process group, so a timeout kills what it started too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	// Two pipes: a tool that buffers stdout and not stderr would otherwise
	// have one stream land in the middle of the other's line.
	out, errOut := &capWriter{max: maxOut}, &capWriter{max: maxOut}
	cmd.Stdout, cmd.Stderr = out, errOut
	if err := cmd.Start(); err != nil {
		// Start refuses a context that is done: a time that ran out on a
		// busy machine before the tool started is a timeout all the same.
		if ctx.Err() == nil && tctx.Err() != nil {
			return Result{Found: true, TimedOut: true, Exit: -1}, nil
		}
		return Result{Found: true}, fmt.Errorf("run %s: %w", tool, err)
	}
	// Whatever the tool started and left behind is killed too, while the
	// tool is still a zombie: until Wait reaps it, its pid and so its
	// process group id cannot belong to anything else.
	waitExited(cmd.Process.Pid)
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	err := cmd.Wait()
	res := Result{Found: true, Out: out.buf, Err: errOut.buf, Truncated: out.cut || errOut.cut}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return res, fmt.Errorf("run %s: %w", tool, ctx.Err())
	case tctx.Err() != nil:
		res.TimedOut, res.Exit = true, -1
	case errors.Is(err, exec.ErrWaitDelay):
		// It exited 0; something outside its group still held its output.
	case errors.As(err, &ee):
		res.Exit = ee.ExitCode()
	case err != nil:
		return res, fmt.Errorf("run %s: %w", tool, err)
	}
	return res, nil
}

// waitExited blocks until pid has exited and leaves it unreaped (waitid
// with WNOWAIT, as os.Process.Wait does before it reaps).
func waitExited(pid int) {
	var info [128]byte // siginfo_t
	for {
		_, _, e := syscall.Syscall6(syscall.SYS_WAITID, 1 /* P_PID */, uintptr(pid),
			uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if e != syscall.EINTR {
			return
		}
	}
}

// capWriter keeps the first max bytes and drops the rest, so a noisy tool
// is never blocked and never fills memory.
type capWriter struct {
	buf []byte
	max int
	cut bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - len(w.buf); room < len(p) {
		w.buf = append(w.buf, p[:room]...)
		w.cut = true
	} else {
		w.buf = append(w.buf, p...)
	}
	return len(p), nil
}

// staleScratch is the age past which a check-* directory is left over
// from a run that was killed: a validator gets seconds, not an hour.
const staleScratch = time.Hour

// sweepScratch removes check-* directories under tmp older than
// staleScratch: copies (netplan's hold Wi-Fi passwords) that a killed sc
// left behind. sc edit's kept-* copies are the user's and stay.
func sweepScratch(tmp string) {
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "check-") {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > staleScratch {
			os.RemoveAll(filepath.Join(tmp, e.Name()))
		}
	}
}

// Scratch writes data as <home>/tmp/check-XXXX/<base name of path>, a
// private copy for a validator to read (plan 5.3): the directory is 0700,
// the file 0600, and it keeps the file's own name, which some validators
// need. home is $SC_HOME, which is never under a watched root. cleanup
// removes the directory. Leftovers older than an hour are swept.
func Scratch(home, path string, data []byte) (file string, cleanup func(), err error) {
	name := filepath.Base(path)
	if name == "." || name == "/" {
		return "", nil, fmt.Errorf("scratch copy of %q: no file name", path)
	}
	// Absolute: a validator runs in the scratch directory, where a
	// relative path (SC_HOME=home) would not find its input.
	tmp, err := filepath.Abs(filepath.Join(home, "tmp"))
	if err != nil {
		return "", nil, fmt.Errorf("scratch copy of %s: %w", path, err)
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "", nil, fmt.Errorf("scratch copy of %s: %w", path, err)
	}
	sweepScratch(tmp)
	dir, err := os.MkdirTemp(tmp, "check-")
	if err != nil {
		return "", nil, fmt.Errorf("scratch copy of %s: %w", path, err)
	}
	cleanup = func() { os.RemoveAll(dir) }
	file = filepath.Join(dir, name)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("scratch copy of %s: %w", path, err)
	}
	return file, cleanup, nil
}
