package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"smartconfig/internal/check"
	"smartconfig/internal/store"
)

// editEnv is checkEnv plus an initialised store, a graph with an apply
// line and a mode line, and an editor: a script that, each time it runs,
// replaces the file it is given with the next of rounds ("" leaves the
// file as it is). answers is what sc edit reads at its prompt.
func editEnv(t *testing.T, answers string, rounds ...string) (dir, fstab string) {
	t.Helper()
	dir, fstab = checkEnv(t)
	g, err := check.ParseGraph("check fstab " + fstab + " " + dir + "/drop.d/*\napply fstab at the next boot\nmode " + dir + "/drop.d/* 0440\n")
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	testHookChecks = func(c *check.Checks) { c.Graph, c.Run.Dirs = g, []string{empty} }
	mustSC(t, "init")
	ed := t.TempDir()
	for i, r := range rounds {
		if r != "" {
			os.WriteFile(filepath.Join(ed, fmt.Sprintf("round-%d", i+1)), []byte(r), 0o644)
		}
	}
	script := filepath.Join(ed, "editor")
	os.WriteFile(script, []byte(`#!/bin/sh
d=`+ed+`
n=$(cat $d/n 2>/dev/null || echo 0); n=$((n+1)); echo $n > $d/n
for f; do :; done
[ -f $d/round-$n ] && cp $d/round-$n "$f"
[ -x $d/hook ] && $d/hook
exit 0
`), 0o755)
	t.Setenv("SUDO_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", script)
	editStdin = strings.NewReader(answers)
	t.Cleanup(func() { editStdin = os.Stdin })
	return dir, fstab
}

// editorRuns is how many times the editor ran.
func editorRuns(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(os.Getenv("EDITOR")), "n"))
	return strings.TrimSpace(string(b))
}

// origins is "origin intent" of p's rows, oldest first.
func origins(t *testing.T, p string) string {
	t.Helper()
	s, err := store.Open(store.Home())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cs, _ := s.List(p, 0)
	var out []string
	for i := len(cs) - 1; i >= 0; i-- {
		out = append(out, cs[i].Origin+" "+cs[i].Intent)
	}
	return strings.Join(out, " | ")
}

const (
	goodFstab = "/dev/null /data ext4 defaults 0 2\n"
	okEdit    = "/dev/null /data ext4 defaults,noatime 0 2\n"
	badEdit   = "/dev/sc-no-such-disk /data ext4 defaults 0 2\n"
	prompt    = "What now? (e)dit again, (s)ave anyway, (q)uit without saving [e]: "
)

func content(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestEditSaves(t *testing.T) {
	_, fstab := editEnv(t, "", okEdit)
	os.WriteFile(fstab, []byte(goodFstab), 0o640)
	os.Chmod(fstab, 0o640)
	r := sc(t, "edit", fstab)
	if r.code != 0 || r.stderr != "" || content(fstab) != okEdit {
		t.Fatalf("%+v, file %q", r, content(fstab))
	}
	if origins(t, fstab) != "manual before sc edit | edit sc edit" {
		t.Fatalf("rows: %s", origins(t, fstab))
	}
	if fi, _ := os.Stat(fstab); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", fi.Mode())
	}
	f := strings.Fields(strings.TrimSpace(r.stdout))
	// note: ... \n saved PATH as ID (before: ID); takes effect at the next boot
	if !strings.Contains(r.stdout, "saved "+fstab+" as ") || !strings.HasSuffix(r.stdout, ")\ntakes effect at the next boot\n") || len(f) < 6 {
		t.Errorf("stdout %q", r.stdout)
	}
	if left, _ := filepath.Glob(filepath.Join(store.Home(), "tmp", "*")); len(left) != 0 {
		t.Errorf("scratch copies left: %v", left)
	}

	// The editor leaves the file as it is: nothing is written or recorded.
	_, fstab = editEnv(t, "", "")
	os.WriteFile(fstab, []byte(goodFstab), 0o644)
	if r := sc(t, "edit", fstab); r.code != 0 || r.stdout != "unchanged: "+fstab+" was not written\n" || origins(t, fstab) != "" {
		t.Fatalf("unchanged: %+v rows %q", r, origins(t, fstab))
	}
}

func TestEditBlocker(t *testing.T) {
	// Quit: the file is never touched, exit 2.
	_, fstab := editEnv(t, "q\n", badEdit)
	os.WriteFile(fstab, []byte(goodFstab), 0o644)
	r := sc(t, "edit", fstab)
	if r.code != 2 || r.stderr != "" || content(fstab) != goodFstab || origins(t, fstab) != "" {
		t.Fatalf("quit: %+v file %q rows %q", r, content(fstab), origins(t, fstab))
	}
	for _, want := range []string{
		"blocker   " + fstab + "  1     fstab-source-missing  /dev/sc-no-such-disk (for /data) is not a device on this machine\n",
		"\nfstab-source-missing:\n  The line names a disk, partition, image file or bind-mount source that",
		"\nThis edit adds 1 blocker. " + fstab + " is unchanged so far.\n" + prompt + "not saved: " + fstab + " is unchanged\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("quit: stdout lacks %q:\n%s", want, r.stdout)
		}
	}

	// Quit throws the edit away, as asked.
	if left, _ := filepath.Glob(filepath.Join(store.Home(), "tmp", "*")); len(left) != 0 {
		t.Errorf("quit left %v", left)
	}

	// End of input: a failing file is never saved unattended, and the
	// edit is kept, since nobody said to throw it away.
	_, fstab = editEnv(t, "", badEdit)
	os.WriteFile(fstab, []byte(goodFstab), 0o644)
	r = sc(t, "edit", fstab)
	i := strings.LastIndex(r.stdout, prompt+"\nnot saved: "+fstab+" is unchanged; your version is kept at ")
	if r.code != 2 || content(fstab) != goodFstab || i < 0 {
		t.Fatalf("end of input: %+v", r)
	}
	if kept := strings.TrimSpace(r.stdout[strings.LastIndex(r.stdout, " at ")+4:]); content(kept) != badEdit {
		t.Errorf("end of input: kept version %s holds %q", kept, content(kept))
	}

	// Edit again (the default), then a clean version: saved as a plain edit.
	_, fstab = editEnv(t, "\n", badEdit, okEdit)
	os.WriteFile(fstab, []byte(goodFstab), 0o644)
	if r := sc(t, "edit", fstab); r.code != 0 || content(fstab) != okEdit || editorRuns(t) != "2" ||
		origins(t, fstab) != "manual before sc edit | edit sc edit" {
		t.Fatalf("edit again: %+v rows %q runs %s", r, origins(t, fstab), editorRuns(t))
	}

	// Save anyway: saved, and the row says so.
	_, fstab = editEnv(t, "S\n", badEdit)
	os.WriteFile(fstab, []byte(goodFstab), 0o644)
	if r := sc(t, "edit", fstab); r.code != 0 || content(fstab) != badEdit ||
		origins(t, fstab) != "manual before sc edit | edit sc edit, saved with 1 blocker" {
		t.Fatalf("save anyway: %+v rows %q", r, origins(t, fstab))
	}
}

// Only what the edit adds stops the save: a warning is shown and saved, and
// a blocker that was already in the file is not blamed on the edit.
func TestEditBaseline(t *testing.T) {
	_, fstab := editEnv(t, "", "/dev/null /data ext4 defalts,nofail 0 2\n")
	os.WriteFile(fstab, []byte(goodFstab), 0o644)
	r := sc(t, "edit", fstab)
	if r.code != 0 || !strings.Contains(r.stdout, "warning   "+fstab+"  1     fstab-option-typo") || strings.Contains(r.stdout, "What now?") ||
		origins(t, fstab) != "manual before sc edit | edit sc edit" {
		t.Fatalf("warning: %+v rows %q", r, origins(t, fstab))
	}

	_, fstab = editEnv(t, "", "# a comment\n"+badEdit)
	os.WriteFile(fstab, []byte(badEdit), 0o644)
	r = sc(t, "edit", fstab)
	if r.code != 0 || strings.Contains(r.stdout, "fstab-source-missing") || strings.Contains(r.stdout, "What now?") || content(fstab) != "# a comment\n"+badEdit {
		t.Fatalf("old blocker: %+v", r)
	}
}

// A new file gets the graph's mode and a "did not exist" row.
func TestEditNewFile(t *testing.T) {
	dir, _ := editEnv(t, "", okEdit)
	os.Mkdir(filepath.Join(dir, "drop.d"), 0o755)
	p := filepath.Join(dir, "drop.d", "90-new")
	r := sc(t, "edit", p)
	if r.code != 0 || content(p) != okEdit || !strings.Contains(r.stdout, "saved "+p+" as ") || !strings.Contains(r.stdout, " (new file)\ntakes effect") {
		t.Fatalf("%+v", r)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o440 {
		t.Errorf("mode %v", fi.Mode())
	}
	if origins(t, p) != "edit did not exist | edit sc edit" {
		t.Errorf("rows %q", origins(t, p))
	}
	// A new file that the editor leaves empty is not created.
	q := filepath.Join(dir, "drop.d", "91-empty")
	if r := sc(t, "edit", q); r.code != 0 || !strings.HasPrefix(r.stdout, "unchanged: ") {
		t.Fatalf("empty new file: %+v", r)
	}
	if _, err := os.Lstat(q); !os.IsNotExist(err) {
		t.Error("an empty new file was created")
	}
}

// The file changed on disk while the editor was open: it is not written,
// and the edited version is kept.
func TestEditFileChangedMeanwhile(t *testing.T) {
	_, fstab := editEnv(t, "", okEdit)
	os.WriteFile(fstab, []byte(goodFstab), 0o644)
	hook := filepath.Join(filepath.Dir(os.Getenv("EDITOR")), "hook")
	os.WriteFile(hook, []byte("#!/bin/sh\necho '# someone else' >> "+fstab+"\n"), 0o755)
	r := sc(t, "edit", fstab)
	if r.code != 1 || content(fstab) != goodFstab+"# someone else\n" || !strings.Contains(r.stderr, "changed on disk while you were editing; it was not written. Your version is kept at ") {
		t.Fatalf("%+v file %q", r, content(fstab))
	}
	kept := strings.TrimSpace(r.stderr[strings.LastIndex(r.stderr, " at ")+4:])
	if content(kept) != okEdit || !strings.HasPrefix(kept, filepath.Join(store.Home(), "tmp", "kept-")) {
		t.Errorf("kept version %s: %q", kept, content(kept))
	}
	// A copy that an interrupted sc edit left may hold someone's work: the
	// next sc edit keeps it and says so.
	os.Remove(hook)
	stale := filepath.Join(store.Home(), "tmp", "edit-stale")
	os.MkdirAll(stale, 0o700)
	os.WriteFile(filepath.Join(stale, "fstab"), []byte("half an edit\n"), 0o600)
	keptStale := filepath.Join(store.Home(), "tmp", "kept-stale")
	r = sc(t, "edit", fstab)
	if r.code != 0 || !strings.HasPrefix(r.stdout, "note: an earlier sc edit did not finish; its copy is kept in "+keptStale+"\n") ||
		content(filepath.Join(keptStale, "fstab")) != "half an edit\n" {
		t.Fatalf("next edit: %+v", r)
	}
	left, _ := filepath.Glob(filepath.Join(store.Home(), "tmp", "*"))
	if len(left) != 2 || !strings.Contains(left[0], "kept-") || !strings.Contains(left[1], "kept-") {
		t.Errorf("tmp holds %v", left)
	}
}

func TestEditRefusals(t *testing.T) {
	dir, fstab := editEnv(t, "", okEdit)
	os.WriteFile(fstab, []byte(goodFstab), 0o644)
	link := filepath.Join(dir, "link")
	os.Symlink(fstab, link)
	if r := sc(t, "edit", link); r.code != 1 || !strings.Contains(r.stderr, "is a symlink; sc writes regular files only") {
		t.Errorf("symlink: %+v", r)
	}
	key := filepath.Join(dir, "ssh_host_key")
	os.WriteFile(key, []byte("PRIVATE\n"), 0o600)
	testHookOpenStore = func(s *store.Store) { s.SetFingerprintOnly(func(p string) bool { return p == key }) }
	t.Cleanup(func() { testHookOpenStore = nil })
	if r := sc(t, "edit", key); r.code != 1 || !strings.Contains(r.stderr, "is fingerprint-only; sc never writes it") {
		t.Errorf("fingerprint-only: %+v", r)
	}
	if r := sc(t, "edit", filepath.Join(dir, "nodir", "x")); r.code != 1 || !strings.Contains(r.stderr, "(file not changed)") {
		t.Errorf("missing directory: %+v", r)
	}
	// Another sc edit holds the lock.
	f, _ := os.OpenFile(filepath.Join(store.Home(), "edit.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	defer f.Close()
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	f.WriteString("4242\n")
	if r := sc(t, "edit", fstab); r.code != 1 || r.stderr != "sc: another sc edit is running (pid 4242)\n" {
		t.Errorf("locked: %+v", r)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if editorRuns(t) != "" || content(fstab) != goodFstab || content(key) != "PRIVATE\n" {
		t.Errorf("the editor ran %q times; fstab %q", editorRuns(t), content(fstab))
	}
}

// The editor's own failure leaves the file alone; an editor given with
// arguments gets them before the file.
func TestEditEditor(t *testing.T) {
	_, fstab := editEnv(t, "", okEdit)
	os.WriteFile(fstab, []byte(goodFstab), 0o644)
	script := os.Getenv("EDITOR")
	t.Setenv("EDITOR", "/bin/false")
	if r := sc(t, "edit", fstab); r.code != 1 || !strings.Contains(r.stderr, "the editor (/bin/false) ended with exit status 1; "+fstab+" is unchanged") || content(fstab) != goodFstab {
		t.Errorf("failing editor: %+v", r)
	}
	t.Setenv("EDITOR", "/nonexistent/editor")
	if r := sc(t, "edit", fstab); r.code != 1 || !strings.Contains(r.stderr, "cannot run the editor") {
		t.Errorf("missing editor: %+v", r)
	}
	// $SUDO_EDITOR wins over $EDITOR, and its arguments are passed on.
	t.Setenv("SUDO_EDITOR", script+" -w  --flag")
	if r := sc(t, "edit", fstab); r.code != 0 || content(fstab) != okEdit {
		t.Errorf("editor with arguments: %+v", r)
	}
}

// A save that fails after the editor ran keeps the edited version and
// says where: the user's work is never thrown away by an error.
func TestEditKeepsWorkWhenSaveFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	_, fstab := editEnv(t, "", okEdit)
	os.WriteFile(fstab, []byte(goodFstab), 0o644)
	objects := filepath.Join(store.Home(), "objects")
	os.Chmod(objects, 0o500)
	defer os.Chmod(objects, 0o700)
	r := sc(t, "edit", fstab)
	i := strings.LastIndex(r.stderr, ". Your version is kept at ")
	if r.code != 1 || content(fstab) != goodFstab || i < 0 || strings.Count(r.stderr, "\n") != 1 {
		t.Fatalf("%+v", r)
	}
	if kept := strings.TrimSpace(r.stderr[i+len(". Your version is kept at "):]); content(kept) != okEdit {
		t.Errorf("kept version %s holds %q", kept, content(kept))
	}

	// More than sc reads back: not written, kept.
	_, fstab = editEnv(t, "", strings.Repeat("# padding padding padding padding\n", 300000))
	os.WriteFile(fstab, []byte(goodFstab), 0o644)
	if r := sc(t, "edit", fstab); r.code != 1 || content(fstab) != goodFstab || !strings.Contains(r.stderr, "is larger than the 8 MB limit (file not changed). Your version is kept at ") {
		t.Fatalf("too big: code %d stderr %q", r.code, r.stderr)
	}
}

// The validator runs only when there is something to compare: not at all
// for an unchanged file, once for a new file, twice for a changed one,
// and both runs come after the editor.
func TestEditValidatorRuns(t *testing.T) {
	runs := func(t *testing.T, existing bool, rounds ...string) string {
		dir, fstab := editEnv(t, "", rounds...)
		tools := t.TempDir()
		log := filepath.Join(tools, "runs")
		os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\necho \"$(cat "+filepath.Dir(os.Getenv("EDITOR"))+"/n 2>/dev/null)\" >> "+log+"\n"), 0o755)
		g, _ := check.ParseGraph("check fstab " + fstab + " " + dir + "/new")
		testHookChecks = func(c *check.Checks) { c.Graph, c.Run.Dirs = g, []string{tools} }
		p := filepath.Join(dir, "new")
		if existing {
			p = fstab
			os.WriteFile(fstab, []byte(goodFstab), 0o644)
		}
		if r := sc(t, "edit", p); r.code != 0 {
			t.Fatalf("%+v", r)
		}
		return strings.Join(strings.Fields(content(log)), " ")
	}
	// Each run logs how many times the editor had run by then.
	if got := runs(t, true, ""); got != "" {
		t.Errorf("unchanged: validator runs %q", got)
	}
	if got := runs(t, true, okEdit); got != "1 1" {
		t.Errorf("changed: validator runs %q, want two after the editor", got)
	}
	if got := runs(t, false, okEdit); got != "1" {
		t.Errorf("new file: validator runs %q, want one", got)
	}
}

// sc edit shows a validator's words behind a note under it, as it shows a
// finding's (M3 follow-up 4): the file is the one being edited.
func TestEditSaid(t *testing.T) {
	_, fstab := editEnv(t, "", okEdit)
	os.WriteFile(fstab, []byte(goodFstab), 0o644)
	tools := t.TempDir()
	os.WriteFile(filepath.Join(tools, "findmnt"), []byte("#!/bin/sh\necho \"findmnt: unrecognized option '--x'\" >&2\nexit 1\n"), 0o755)
	hook := testHookChecks
	testHookChecks = func(c *check.Checks) { hook(c); c.Run.Dirs = []string{tools} }
	r := sc(t, "edit", fstab)
	if r.code != 0 || !strings.Contains(r.stdout, "note: findmnt could not check the file; only sc's own rules ran\n  findmnt: unrecognized option '--x'\n") {
		t.Fatalf("%+v", r)
	}
}
