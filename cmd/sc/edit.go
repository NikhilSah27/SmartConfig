package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"smartconfig/internal/check"
	"smartconfig/internal/fsutil"
	"smartconfig/internal/store"
)

// editStdin is where sc edit reads its answers from; the tests replace it.
var editStdin io.Reader = os.Stdin

func newEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit <path>",
		Short: "Edit a config file; check it before it is saved",
		Long: `Edit a copy of the file with your editor ($SUDO_EDITOR, $VISUAL, $EDITOR,
else editor, nano or vi). The result is checked before it replaces the
file: a problem the edit added is explained, and you can edit again, save
anyway or quit. The file is not touched until you save.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEdit(cmd, args[0])
		},
	}
}

func runEdit(cmd *cobra.Command, arg string) error {
	path, err := filepath.Abs(arg)
	if err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	home := store.Home()

	// Refusals, before the editor opens.
	var base *fsutil.State
	st, err := fsutil.ReadState(path)
	switch {
	case fsutil.IsNotExist(err):
	case err != nil:
		return err
	default:
		base = &st
	}
	if err := s.CanReplace(path, base); err != nil {
		return err
	}
	unlock, err := lockEdit(home)
	if err != nil {
		return err
	}
	defer unlock()

	// The copy to edit, under the file's own name so the editor knows its
	// syntax. Copies an interrupted sc edit left are removed first: the
	// lock says no other sc edit is using them.
	tmp := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return fmt.Errorf("edit %s: %w", path, err)
	}
	if old, _ := filepath.Glob(filepath.Join(tmp, "edit-*")); len(old) > 0 {
		for _, d := range old {
			os.RemoveAll(d)
		}
	}
	dir, err := os.MkdirTemp(tmp, "edit-")
	if err != nil {
		return fmt.Errorf("edit %s: %w", path, err)
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(dir)
		}
	}()
	copyPath := filepath.Join(dir, filepath.Base(path))
	var before []byte
	if base != nil {
		before = base.Data
	}
	if err := os.WriteFile(copyPath, before, 0o600); err != nil {
		return fmt.Errorf("edit %s: %w", path, err)
	}

	c := &check.Checks{Home: home}
	if testHookChecks != nil {
		testHookChecks(c)
	}
	g := c.Graph
	if g == nil {
		g = check.DefaultGraph()
	}
	oldRep, err := c.Check(cmd.Context(), path, before)
	if err != nil {
		return err
	}
	if base == nil {
		oldRep.Findings = nil // a file that is not there has no problems to compare with
	}
	out := cmd.OutOrStdout()
	answers := bufio.NewReader(editStdin)
	intent := "sc edit"
	var after []byte
	for {
		if err := runEditor(copyPath); err != nil {
			return fmt.Errorf("%w; %s is unchanged", err, path)
		}
		after, err = os.ReadFile(copyPath)
		if err != nil {
			return fmt.Errorf("edit %s: %w", path, err)
		}
		if bytes.Equal(after, before) {
			fmt.Fprintf(out, "unchanged: %s was not written\n", show(path))
			return nil
		}
		rep, err := c.Check(cmd.Context(), path, after)
		if err != nil {
			return err
		}
		added := check.Added(oldRep.Findings, rep.Findings)
		rows := make([]findingRow, len(added))
		for i, f := range added {
			rows[i] = findingRow{path, f}
		}
		n, err := findingsTable(out, rows)
		if err != nil {
			return err
		}
		for _, note := range rep.Notes {
			fmt.Fprintln(out, "note: "+note)
		}
		if check.Worst(added) < check.Error {
			break // nothing new, or warnings only: save
		}
		explain(out, added)
		fmt.Fprintf(out, "\nThis edit adds %s. %s is unchanged so far.\n", tally(n), show(path))
		fmt.Fprint(out, "What now? (e)dit again, (s)ave anyway, (q)uit without saving [e]: ")
		answer, readErr := answers.ReadString('\n')
		answer = strings.ToLower(strings.TrimSpace(answer))
		if readErr != nil && answer == "" {
			answer = "q" // end of input: a failing file is never saved unattended
			fmt.Fprintln(out)
		}
		switch {
		case strings.HasPrefix(answer, "s"):
			n[check.Warning] = 0
			intent = "sc edit, saved with " + tally(n)
		case strings.HasPrefix(answer, "q"):
			fmt.Fprintf(out, "not saved: %s is unchanged\n", show(path))
			return exitCode(2)
		default:
			continue
		}
		break
	}

	mode, uid, gid := g.Mode(path), os.Geteuid(), os.Getegid()
	if base != nil {
		mode, uid, gid = base.Meta.Mode, base.Meta.UID, base.Meta.GID
	}
	mutating.Store(true)
	row, prev, err := s.Replace(path, after, mode, uid, gid, base, store.OriginEdit, intent)
	if errors.Is(err, store.ErrFileChanged) {
		kept := filepath.Join(tmp, "kept-"+strings.TrimPrefix(filepath.Base(dir), "edit-"))
		if os.Rename(dir, kept) == nil {
			keep = true
			return fmt.Errorf("%s changed on disk while you were editing; it was not written. Your version is kept at %s", path, filepath.Join(kept, filepath.Base(path)))
		}
		keep = true
		return fmt.Errorf("%s changed on disk while you were editing; it was not written. Your version is kept at %s", path, copyPath)
	}
	if err != nil {
		return err
	}
	was := "new file"
	if prev != nil {
		was = "before: " + prev.ID
	}
	fmt.Fprintf(out, "saved %s as %s (%s)", show(path), row.ID, was)
	if apply := g.Apply(path); apply != "" {
		fmt.Fprintf(out, "; takes effect %s", apply)
	}
	fmt.Fprintln(out)
	return nil
}

// runEditor runs the user's editor on file and waits for it. The editor is
// $SUDO_EDITOR, $VISUAL or $EDITOR, split on spaces as sudoedit does (never
// run through a shell), else the first of editor, nano and vi.
func runEditor(file string) error {
	var argv []string
	for _, v := range []string{"SUDO_EDITOR", "VISUAL", "EDITOR"} {
		if argv = strings.Fields(os.Getenv(v)); len(argv) > 0 {
			break
		}
	}
	if len(argv) == 0 {
		for _, e := range []string{"/usr/bin/editor", "/usr/bin/nano", "/bin/nano", "/usr/bin/vi", "/bin/vi"} {
			if _, err := os.Stat(e); err == nil {
				argv = []string{e}
				break
			}
		}
	}
	if len(argv) == 0 {
		return errors.New("no editor found: set EDITOR")
	}
	ed := exec.Command(argv[0], append(argv[1:], file)...)
	ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := ed.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("the editor (%s) ended with %s", argv[0], ee.ProcessState)
		}
		return fmt.Errorf("cannot run the editor: %w", err)
	}
	return nil
}

// lockEdit takes the lock that lets one sc edit run at a time, and writes
// this process's pid into it.
func lockEdit(home string) (unlock func(), err error) {
	p := filepath.Join(home, "edit.lock")
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", p, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		b, _ := os.ReadFile(p)
		f.Close()
		pid := strings.TrimSpace(string(b))
		if pid == "" {
			pid = "?"
		}
		return nil, fmt.Errorf("another sc edit is running (pid %s)", pid)
	}
	f.Truncate(0)
	f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return func() { f.Close() }, nil
}
