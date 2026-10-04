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

func runEdit(cmd *cobra.Command, arg string) (err error) {
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
	out := cmd.OutOrStdout()

	// Refusals, before the editor opens. The file must hold still while it
	// is read: a torn copy would only be refused at the save.
	var base *fsutil.State
	for attempt := 1; ; attempt++ {
		st, err := fsutil.ReadState(path)
		if fsutil.IsNotExist(err) {
			break
		}
		if err == nil && st.Stable {
			base = &st
			break
		}
		if err != nil && !errors.Is(err, fsutil.ErrReplaced) {
			return err
		}
		if attempt == 3 {
			return fmt.Errorf("%s kept changing while being read, try again", path)
		}
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
	// syntax. A copy an interrupted sc edit left may hold someone's work:
	// it is kept, never removed (the lock says no sc edit is using it).
	tmp := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return fmt.Errorf("edit %s: %w", path, err)
	}
	old, _ := filepath.Glob(filepath.Join(tmp, "edit-*"))
	for _, d := range old {
		fmt.Fprintf(out, "note: an earlier sc edit did not finish; its copy is kept in %s\n", keepCopy(d))
	}
	dir, err := os.MkdirTemp(tmp, "edit-")
	if err != nil {
		return fmt.Errorf("edit %s: %w", path, err)
	}
	copyPath := filepath.Join(dir, filepath.Base(path))
	// Once the editor has changed the copy it is the user's work: any
	// failure from then on keeps it and says where. Only a save, an
	// unchanged file and an explicit quit remove it.
	edited, discard := false, false
	defer func() {
		if err != nil && edited && !discard {
			kept := filepath.Join(keepCopy(dir), filepath.Base(path))
			err = fmt.Errorf("%w. Your version is kept at %s", err, kept)
			return
		}
		os.RemoveAll(dir)
	}()
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
	g := c.GraphInUse()
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
		edited = true
		if err := store.TooBig(path, after); err != nil {
			return err
		}
		// The old content is checked now, right before the new, so both
		// see the same machine; a file that was not there has no problems
		// to compare with.
		var had []check.Finding
		if base != nil {
			oldRep, err := c.Check(cmd.Context(), path, before)
			if err != nil {
				return err
			}
			had = oldRep.Findings
		}
		rep, err := c.Check(cmd.Context(), path, after)
		if err != nil {
			return err
		}
		added := check.Added(had, rep.Findings)
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
			// End of input: a failing file is never saved unattended, and
			// nobody said to throw the edit away.
			kept := filepath.Join(keepCopy(dir), filepath.Base(path))
			fmt.Fprintf(out, "\nnot saved: %s is unchanged; your version is kept at %s\n", show(path), kept)
			discard = true
			return exitCode(2)
		}
		switch {
		case strings.HasPrefix(answer, "s"):
			n[check.Warning] = 0
			intent = "sc edit, saved with " + tally(n)
		case strings.HasPrefix(answer, "q"):
			fmt.Fprintf(out, "not saved: %s is unchanged\n", show(path))
			discard = true
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
		return fmt.Errorf("%s changed on disk while you were editing; it was not written", path)
	}
	if err != nil {
		return err
	}
	was := "new file"
	if prev != nil {
		was = "before: " + prev.ID
	}
	fmt.Fprintf(out, "saved %s as %s (%s)\n", show(path), row.ID, was)
	if apply := g.Apply(path); apply != "" {
		fmt.Fprintf(out, "takes effect %s\n", apply)
	}
	return nil
}

// keepCopy renames an sc edit scratch directory so that it is kept, and
// returns its new name (its old one if the rename fails: the next sc edit
// then keeps it).
func keepCopy(dir string) string {
	kept := filepath.Join(filepath.Dir(dir), "kept-"+strings.TrimPrefix(filepath.Base(dir), "edit-"))
	if os.Rename(dir, kept) != nil {
		return dir
	}
	return kept
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
	// While the editor runs, Ctrl-C and Ctrl-\ are its to handle
	// (handleSignals).
	editing.Store(true)
	err := ed.Run()
	editing.Store(false)
	if err != nil {
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
		return nil, err // "open PATH: why"
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
