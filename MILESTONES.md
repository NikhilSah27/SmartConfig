# Milestones

- [x] M1 store + CLI: snapshot, log, cat, diff, restore — done 2026-09-27,
      hardened by four review rounds 2026-09-27/28 (tag `m1`)
- [ ] M2 watcher daemon: scd with inotify, auto-snapshot edits made with any editor
- [ ] M3 file graph + checkers: tiers, real validators, regex rules with canned
      explanations, sc edit and sc check
- [ ] M4 rescue path: GRUB entry, rescue.target service printing sc status,
      boot-ok verification, restore from read-only root
- [ ] M5 package: .deb with nfpm, install on a clean VM
- [ ] M6 incident factory and eval set
- [ ] M7 local model: sc why with llama.cpp, opt-in

Current: M2.

## M1 notes

Deliberate choices and things left out:

- Id collisions: the spec's id, sha256(path\nts\nblob)[:6], collides when the
  same path and content are recorded in the same second (a pre-restore right
  after a manual snapshot, as the smoke run does). When an id is taken the
  hash is retried with "\n<n>" appended (n = 1, 2, ...). Ids stay 6 chars.
- `sc snapshot -q` prints only the id even when the content is unchanged, so
  `id=$(sc snapshot -q ...)` always captures a usable id.
- Diff labels drop the path's leading slash: `a/etc/hosts (snapshot <id>)`,
  not `a//etc/hosts`.
- A final line without a newline is shown with diff(1)'s
  `\ No newline at end of file` marker, so that difference is visible.
- `sc diff <id>` errors if the file is no longer on disk; it does not diff
  against empty.
- Blobs are checksum-verified when read; a corrupt blob is an error and is
  never restored.
- The restore target must not be a symlink (the pre-restore snapshot refuses
  it), so restore never replaces a symlink with a regular file.
- `sc log` with no rows prints "no snapshots"; `-n 0` means no limit.
- Locking is SQLite's own: every write is one BEGIN IMMEDIATE transaction on
  its own connection (busy_timeout 5 s). A restore's COMMIT, which comes
  after its rename, is retried up to 6 times while readers are busy; other
  writes give up after one busy timeout.
- Not done: file watching, validators, GRUB/rescue integration, packaging,
  pruning old blobs. All belong to later milestones.
- The owner half of the restore test (uid/gid) only runs as root; as a normal
  user it is skipped. The smoke run checks root:root on /etc/hosts.
- Pinned modernc.org/sqlite v1.29.10 because newer releases need Go 1.25 and
  this machine has Go 1.22.2.

### Hardening after acceptance (2026-09-27/28)

Four independent review rounds (reviewers plus two skeptics per finding,
see `docs/reviews/`) led to these behaviours. Each has a test.

- Reads open the file with O_NOFOLLOW|O_NONBLOCK and require it to be the
  same file lstat saw; a file swapped for a symlink or FIFO is refused, and
  one replaced during the read is read again.
- `sc snapshot` compares with the newest row, re-checks the file's stamp
  (device, inode, size, mtime, ctime, mode, owner) and takes its timestamp
  under the write lock; if the file changed after it was read, it reads it
  again, up to 3 times, then fails with "kept changing, try again".
- `sc restore`:
  1. writes and fsyncs the temp file before taking any lock;
  2. saves the current file and commits the pre-restore row in its own
     transaction; this is the undo point and survives anything after it;
  3. under the write lock, checks the file is still the version it saved
     (else starts over, up to 3 times), inserts the restore row, renames,
     commits.
  A failed restore still keeps its pre-restore row, as the spec asks, and a
  restore that starts over because the file kept changing can leave one
  pre-restore row per attempt. If recording fails after the rename, the
  error says so and names the saved id.
- Signals (SIGINT, SIGTERM, SIGHUP, SIGQUIT, SIGABRT and the other stop
  signals; ones inherited as ignored stay ignored):
  - during `sc snapshot` or `sc restore` (including printing the result),
    the first signal asks it to stop at the next safe point; the command
    then prints its normal one line, which says whether the file changed
    (and names the saved id if it did). While sc waits for the database
    lock this takes up to one busy timeout (5 s);
  - a second signal stops at once, unless a restore may already have
    renamed its file: then sc waits for that restore's report (after the
    first signal it makes at most one more COMMIT attempt);
  - any other command stops at once;
  - afterwards sc ends by the signal itself for SIGINT, SIGTERM and SIGHUP,
    so a calling shell stops its loop; this is a deliberate exception to
    "exit 1 on any error". The other signals end with exit status 1 and
    never print a Go stack trace;
  - prepared temp files are removed on every path except kill -9.
- A panic inside a database transaction rolls it back.

### Known limits (deliberate for M1)

- `sc restore` needs the current file (if it exists) to be something it can
  save: a regular file of at most 8 MB, not a symlink, directory or FIFO.
  Otherwise it refuses with one line and changes nothing; move the file away
  first.
- The stamp check can miss a same-size rewrite within the timestamp
  granularity on filesystems with coarse timestamps (vfat, ext4 with
  128-byte inodes). Not an issue on this VM (ext4, nanosecond times).
- `sc log` prints intents and paths as they are; a newline or tab in an
  intent breaks the table. Escaping is left for later.
- Some errors lack the command's context, and `sc help <unknown>` exits 0
  with usage text. Left for later.
- logrotate reads hidden files in /etc/logrotate.d, including sc's
  `.NAME.sc-tmp-*` during a restore's few milliseconds; no suffix avoids it.
- kill -9 during a restore can leave a `.NAME.sc-tmp-*` file next to the
  target. M2's watcher is planned to clear stale ones at start.
- Restoring a file owned by another user, or into a directory that is not
  root's, goes through paths that a hostile user could swap; M1 is for root
  on root-owned config. M2 handles user-owned paths (plan step 6).
