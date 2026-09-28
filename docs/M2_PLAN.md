# M2 plan: `scd`, the watcher

**Status:** approved by the owner on 2026-09-28, with every recommendation in section 16 (and the 13 in NEXT_STEPS). Revised after the critic's review; Appendix B lists what changed. While writing and revising this plan, nothing in the repo, `/etc`, `/boot` or `/var/lib/smartconfig` was changed. All experiments ran in `scratchpad/m2plan/synth/` and `scratchpad/m2plan/final/` (Appendix A).

> **Before step 1 (added 2026-09-27):** the independent review of the M1
> follow-up fixes confirmed 13 findings ([reviews/2026-09-27-m1-fixes.md](reviews/2026-09-27-m1-fixes.md)).
> Five small fixes (R2-1 to R2-5) land in the store and fsutil code before M2
> step 1: a dedicated connection with retried COMMIT, the temp file prepared
> before the lock, restore in one transaction with the pre-restore row,
> snapshot's unchanged check under the lock, and a precise ELOOP message.
> Steps below that rebuild the same functions build on those fixes.
> The drafting runs' scratch files cited in the evidence are kept on the VM
> in `~/smartconfig-work/` (the session's `/tmp` scratchpad is wiped at boot).

## 0. Summary

- **What you get.** After `sudo systemctl enable --now scd`, every change to a watched config file goes into the SmartConfig store without anyone running `sc`. That covers:
  - edits made with any editor or tool;
  - chmod and chown;
  - new files and deleted files;
  - symlinks (systemd enable, disable and mask).
- **What is watched.**
  - All of `/etc` except a noise list: about 1,145 files and 234 symlinks on this VM (S7, S15).
  - `/boot/grub/grub.cfg` and `/boot/grub/custom.cfg`.
  - `authorized_keys` and `authorized_keys2` of every login account.
  - SSH host private keys and `/etc/machine-id` get a fingerprint, mode and owner only. Their content is never stored, shown or restored.
- **How it works.**
  - There is one new subcommand, `sc watch`. The systemd unit `scd.service` runs it, and it creates its store on first start.
  - It uses inotify on directories through Go's `syscall` package, so there is no new dependency.
  - It uses two inotify instances: one for `/etc` and `/boot/grub`, and one for users' `.ssh` directories. So a user cannot flood the event queue that `/etc` depends on.
  - Each event only marks a path "dirty". After 500 ms of quiet the path is read. A row is written only if its content, mode, owner or link target changed.
- **Nothing is lost silently.** A full rescan runs at start, after an inotify overflow, and every hour.
- **History tells the truth.**
  - A `did not exist` row is written only when scd can prove the path was absent.
  - A restore gives exactly its two rows (pre-restore, restore), never an extra one. This holds for files, links and deletions.
- **Store.**
  - One migration adds two columns, `kind` and `target`.
  - SQLite's rollback journal stays. A `kill -9` in the middle of a write is rolled back at the next open (tested, S13).
  - The M1 binary still works on the migrated store and refuses the new row kinds with one line. A test proves it.
- **Not in M2.** Validators, apt grouping, the file graph, `sc status`, `boot_id`, the rescue boot entry, packaging and retention. Section 15 lists each one with its milestone.
- **How it gets built.**
  - 17 steps in five reviewed chunks, one commit each, each with its tests.
  - WORKLOG is updated and pushed after every step.
  - Then you run an acceptance script with sudo in the VM.
  - M2 is done only after the sign-off runs from NEXT_STEPS (section 13), each with your OK.

## 1. Goals and non-goals

**Goals.** M2 is done when all of these hold, plus everything in section 13.

1. **Any change is recorded.** Edits to a watched file by any editor or tool are recorded without anyone running `sc`.
   - A content change, a mode-only change and an owner-only change each give one row.
   - `touch` (mtime only) gives no row.
2. **Symlinks are history.** They are recorded as link rows (target text and owner), and they can be restored. A file can be restored over a link, and a link over a file.
3. **Deletions and creations are recorded.**
   - When a recorded path disappears, scd writes a `deleted` row.
   - When scd can prove that a path was created while it was watching (6.4), it writes a `did not exist` row before the path's first row. Otherwise the first row is `first seen`.
   - Restoring a `did not exist` row undoes the creation, for example a `systemctl mask` (question 3).
4. **Fingerprint-only files.** SSH host private keys and `/etc/machine-id` get a sha256, mode and owner only.
   - This holds whoever writes the row: scd, a manual `sc snapshot`, or the pre-restore step of a restore.
   - `sc cat`, `sc diff` and `sc restore` refuse these paths, whatever kind of row is asked for.
5. **Nothing is lost silently.** Some changes produce no usable event:
   - changes made while scd was stopped;
   - changes whose events were lost to a queue overflow;
   - writes through mmap or hard links.

   The rescans at start, after an overflow and every hour find all of these.
6. **Restores explain themselves.** `sc restore` of a file, a link or a deletion never produces an extra, unexplained row.
7. **scd is safe to run on every boot.**
   - It never delays boot beyond starting its own binary.
   - It starts on a fresh machine, because it creates `$SC_HOME` itself.
   - No user can stop it from starting. A symlinked `~/.ssh` is skipped, never followed.
   - It never writes under a watched tree and never uses root-reserved disk blocks.
   - It stays static (CGO_ENABLED=0) and makes no NSS lookups.
   - A recoverable panic becomes one line and exit 1.
   - The unit sets `GOTRACEBACK=none`. A fatal runtime error then prints only Go's one-line message, except a stack overflow, which Go always dumps in full (S16).
   - systemd restarts scd in every case.
8. **The store survives a crash.**
   - SQLite's rollback journal is kept, and WAL is never used.
   - A `kill -9` mid-transaction leaves a hot journal, and the next writable open rolls it back. `integrity_check` says ok and no committed row is lost (S13). Steps 7 and 13 test this.
9. **All checks pass.**
   - `gofmt`, `go vet` and `go test ./...` are clean, both as your user and with `sudo -E`.
   - The static build works.
   - The M1 `scripts/smoke.sh`, the new `scripts/accept-m2.sh` and `make m1-compat` all PASS in the VM.

**Non-goals.** These are not built in M2, not even as stubs.

- **M3 items:**
  - validators;
  - apt/dpkg change sets;
  - account-set and PAM grouping;
  - applied markers ("pending until update-grub");
  - active/inert labels and vendor-override labels;
  - secret redaction in `sc diff`;
  - inode flags, ACLs and directory metadata.
- **M4 items:**
  - `sc status`;
  - mount tracking;
  - the rescue path;
  - a `boot_id` column (NEXT_STEPS question 3).
- **M5 items:** the .deb and a user-editable scope file.
- **Also not built:** retention and pruning, a TUI, color, and fanotify.

The plan is written for Go 1.22.2. If you approve the Go upgrade in NEXT_STEPS question 9, the toolchain moves first, before chunk A. Nothing below depends on it.

## 2. How the decisions, the research rules and the roadmap checks are honoured

**Decisions already made** (WORKLOG, "Decisions already made for M2"):

| # | Decision | In M2 |
|---|---|---|
| 1 | All of /etc minus excludes; tiers only set alert loudness | `/etc` is a root, with the default exclude list (7.3). A tier sets only the journald priority of scd's log line (7.6). |
| 2 | Symlinks supported | `kind='link'` rows with the target in a `target` column. Restore uses `fsutil.SymlinkAtomic`. |
| 3 | Deletions and creations recorded | `kind='deleted'` rows, with intent `deleted` or `did not exist`. `did not exist` is written only with proof of absence (6.4). |
| 4 | Host private keys: fingerprint, mode, owner only | `kind='digest'` rows: sha256, mode, uid and gid, with no object file. The store enforces this for every writer, and `cat`, `diff` and `restore` refuse these paths (5.6). |
| 5 | Outside /etc: authorized_keys files and /boot/grub | Roots: `/boot/grub` (only `grub.cfg` and `custom.cfg`) and `<home>/.ssh` for each login account (only `authorized_keys` and `authorized_keys2`). |
| 6 | M1 fixes first | Done in `c720f22` and `ec15ece`. The restore design below relies on `ec15ece`. |

**Design rules** (M2_WATCHLIST section 4, R1-R23, and the critic's missing rules C1-C9 in its section 5):

| Rule | M2 | How |
|---|---|---|
| R1 directory watches only | yes | `inotify_add_watch` on directories only; rows keyed by path |
| R2 event mask | yes | CLOSE_WRITE, MOVED_TO, MOVED_FROM, CREATE, DELETE, ATTRIB, DELETE_SELF, MOVE_SELF and MODIFY (MODIFY only extends the quiet period), with DONT_FOLLOW and ONLYDIR; never OPEN, ACCESS or CLOSE_NOWRITE |
| R3 quiet period and stability | yes | 500 ms quiet, 5 s cap; a read is accepted only if fstat is unchanged across it; the stamp (dev, ino, size, mtime, ctime) is re-checked under the write lock |
| R4 structural temp rule | yes | when the quiet period ends, the path must exist, be a regular file or symlink, and be recorded (7.2) |
| R5 deletion only if still absent | yes | decided when the quiet period ends, only if the newest row is live |
| R6 dedup on the full key | yes | (kind, blob, target, mode, uid, gid) against the newest row; mtime ignored |
| R7 metadata-only rows | partly | mode, uid and gid only; flags and ACLs are question 10 |
| R8 watch a new dir, then walk it | yes | add the watch, then walk, adding a watch before listing each subdir |
| R9 live wd map | simplified | a moved-in dir is watched and walked, and a known inode keeps its wd, which is re-mapped (inotify(7)); a moved-out dir drops its watches and marks its stored paths dirty; no cookie pairing |
| R10 dedicated reader, rescan on overflow | yes | the readers never touch SQLite; an overflow triggers a rescan of that instance's roots |
| R11 rescan at start plus a backstop; boot_id | partly | rescans at start, on overflow and hourly; `ts` is a label only; `boot_id` deferred to M4 (NEXT_STEPS question 3, CLAUDE.md "not even stubs") |
| R12 restores announce themselves | yes | the `ec15ece` lock order for file, link and deletion restores, plus the stamp re-check under the lock; `.*.sc-tmp-*` excluded |
| R13 SC_HOME not under a root | yes | SC_HOME resolved through its longest existing ancestor and compared with every usable root; scd refuses to start before creating anything; logs go to the journal only |
| R14 watch budget, ENOSPC | partly | two instances instead of one (system roots, home roots), so a user's event flood cannot overflow `/etc`'s queue; flock on `$SC_HOME/scd.lock`; ENOSPC is logged and those dirs are covered by every rescan; `sc status` is M4 |
| R15 never follow symlinks | yes | WalkDir (lstat), DONT_FOLLOW\|ONLYDIR, parent dirfd plus O_NOFOLLOW reads; symlinked roots skipped |
| R16 apt grouping | no | question 6 |
| R17 active/inert labels | no | M3; the scope only encodes which names each consumer runs or skips (7.3 blocks D0 and D) |
| R18 temp name | unchanged | `.<base>.sc-tmp-*` |
| R19 static, no NSS | yes | homes parsed from `/etc/passwd` directly; no shell-outs |
| R20 explicit glob language plus a real-listing test | yes | 7.2; fixture tests in `go test`; the real `/etc` listing is checked in acceptance step 1 |
| R21 edited vs applied | no | question 5 (M3) |
| R22 no "who" | yes | origins: manual, pre-restore, restore, auto |
| R23 validators off the loop | n/a | no validators in M2 |
| C1 inode flags and ACLs; C2 directory metadata | no | question 10 (M3) |
| C3 disk budget | partly | free-space floor, limits for user-owned files, bounded dirty set, backoff; retention later (question 9) |
| C4 hostile user paths | partly | a separate inotify instance; flat `.ssh` watches filtered to two names; a symlinked `.ssh` is never watched; reads go through a parent dir fd with O_NOFOLLOW; restores into non-root or symlinked dirs are refused (question 4) |
| C5 consumer dirs before excludes | yes | block D0 (names the consumer skips itself) and block D (record the rest) come before the noise excludes |
| C6 insert order | yes | newest = highest rowid, never ts; sc never runs VACUUM |
| C7 unit never blocks or blinds boot | yes | Type=exec, no Protect*=, `GOTRACEBACK=none`, watches added before the baseline read |
| C8 short write transactions | yes | at most 50 rows per transaction; no hashing and no blob fsync inside it |
| C9 mounts | no | M4 (single ext4 root here) |

**Roadmap checks** (NEXT_STEPS section 1, "Check the M2 plan against these limits"):

| Check | In this plan |
|---|---|
| Rollback journal, never WAL; a `kill -9` mid-transaction rolls back cleanly | 5.1; `TestCrashMidRecord` (step 7), `TestKillDuringBaseline` (step 13), acceptance step 3, sign-off S5 |
| Order by insert order (rowid), never wall clock | 5.3, step 1 |
| Migration tested on a copy of the real store; `sc-m1` compatibility proven by a test | S5; step 3; step 10 (`make m1-compat`); sign-off S2 |
| Short write transactions, so `sc restore` never hits `database is locked` | 5.4; test in step 12 (restores during a 2,000-file baseline) |
| Tiers only set log priority | 7.6 |
| Scripts that change the real `/etc` refuse to run outside a VM unless `SC_ALLOW_REAL_HOST=1` | acceptance step 0 |
| Acceptance as a script; the "M2 done" list is the bar | sections 12 and 13 |
| Steps grouped into five reviewed chunks | section 10 |
| Ask before the real store is migrated | gate G1 (section 10), sign-off S2 |

## 3. Architecture

```
   inotify #1: /etc, /boot/grub          inotify #2: login ~/.ssh dirs
   (IN_CLOEXEC|IN_NONBLOCK, os.NewFile)  (same)
            |                                   |
   reader goroutine #1                 reader goroutine #2
     (never touches SQLite, never hashes)
     read -> parse -> lock mu ->
       (instance, wd) -> dir, scope check
       dir created / moved in : add watch, walk, mark files dirty
       dir moved out          : rm watches under it, mark stored paths dirty
       Q_OVERFLOW             : drop this instance's listings,
                                request a rescan of its roots
       root gone (IGNORED, MOVE_SELF, DELETE_SELF) : request a rescan
       anything else          : mark(path, created?)
                                due = min(now+quiet, first+cap)
     unlock -> poke worker
            \                                   /
   dirty set  map[path]{first, due, reason, created}   (max 10,000)
   listings   map[dir]names   (proof of absence, 6.4)
                         |
   worker goroutine  (the only SQLite writer in scd)
     sleep until earliest due / rescan request / hourly tick / stop
     rescan: roots -> WalkDir -> per dir (under mu): add watch, list,
             store listing -> mark every recorded path due now ->
             mark store.LivePaths() (filtered by scope) due now
     up to 50 due paths -> fsutil.ReadState (outside mu)
                        -> store.Record(batch) (one short IMMEDIATE tx)
                        -> one log line per recorded row
```

- **Locking.** `mu` guards the wd maps, the dirty set and the directory listings.
  - The worker takes `mu` only per directory (add_watch, the map insert and storing the listing) and to pop due paths.
  - It never holds `mu` across disk reads or SQLite, so an fsync never blocks a reader.
- **Panics.** All three goroutines recover panics into an error that ends `Run`. The CLI then prints one line and exits 1, and systemd restarts scd.
- **Why two instances.**
  - The kernel queue (16,384 events) belongs to one instance. An overflow drops the events of every watch in that instance, quiet ones included (M2_WATCHLIST section 6, the "twowd" test).
  - Home roots are user-writable. With one instance, a `chmod` loop on `authorized_keys` could overflow `/etc`'s queue again and again, and intermediate `/etc` versions would be lost.
  - With two instances, such a flood costs only rescans of the home roots. It uses 2 of the 128 instances allowed per uid.

## 4. File tree (new and changed)

```
cmd/sc/
  main.go          run() -> runContext(ctx, ...); registers watch
  watch.go         NEW  sc watch: flags, signals, journald prefixes
  snapshot.go      CHG  links; fingerprint-only paths
  log.go           CHG  SIZE cell says link / deleted / digest
  cat.go diff.go   CHG  kind-aware; refuse fingerprint-only paths
  restore.go       CHG  output lines for link, deleted, nothing to do
  main_test.go     CHG  kind cases; error cases
  watch_test.go    NEW
  unit_test.go     NEW  TestUnitFile; bash -n on the two scripts
internal/fsutil/
  fsutil.go        CHG  Stamp, State, ReadState, StampOf, SymlinkAtomic,
                        RemoveFile; ReadWithMeta rebuilt on ReadState
  open_linux.go    CHG  open parent dir O_DIRECTORY|O_NOFOLLOW, then openat
  meta_linux.go    CHG  stamp from syscall.Stat_t
internal/store/
  store.go         CHG  rowid order; Init makes changes.db 0600;
                        fingerprint rule; Snapshot built on Record
  migrate.go       NEW  migrations under PRAGMA user_version
  record.go        NEW  Record (batch), dedup key, did-not-exist, LivePaths
  restore.go       NEW  Restore moved here; link and deleted rows; refusals
  crash_test.go    NEW  kill -9 of a helper process inside a transaction
  m1compat_test.go NEW  TestM1Compat (runs only with SC_M1_BIN set)
internal/scope/    NEW  (pure: no inotify, no store)
  glob.go          glob compiler and matcher
  scope.go         rules, Recorded(path), Tier, FingerprintOnly, roots
  homes.go         login homes from /etc/passwd bytes
  default.scope    embedded with //go:embed
  testdata/passwd  fixture
internal/watch/    NEW
  inotify_linux.go syscall wrapper: init, add/rm watch, parse events
  watcher.go       Config, New, Run: readers, dirty set, listings, worker
  rescan.go        walks, triggers, limits
  lock_linux.go    single-instance flock
scripts/
  scd.service      NEW  systemd unit
  accept-m2.sh     NEW  acceptance run (sudo)
  build-sc-m1.sh   NEW  builds the M1 binary from git archive, offline
Makefile           CHG  m1-compat and accept-m2 targets
```

- **Why scope is its own package.** Both the store and the CLI need it. The store takes its fingerprint-only rule from it, so every writer (CLI, scd, restore) obeys decision 4.
  - The dependency runs one way: `store` imports `scope`, and `scope` imports nothing of ours.
  - Question 12 covers adding the two packages to the CLAUDE.md Layout section.
- **No new dependency.** The standard library already has everything needed (checked in Go 1.22.2, S1):
  - `syscall.InotifyInit1`, `InotifyAddWatch`, `InotifyRmWatch` and every `IN_*` constant used here;
  - `syscall.Openat`, `Flock` and `Statfs`;
  - `encoding/binary.NativeEndian`, `embed` and `os/signal`.

  `golang.org/x/sys` stays an indirect dependency.

## 5. Data model

### 5.1 SQL, migration and journal

`store.open` runs the migrations inside one `BEGIN IMMEDIATE` transaction:

1. Read `PRAGMA user_version`. M1 stores and fresh files are at 0.
2. Apply each migration from that version on.
3. Set the new version.
4. Commit.

Migration 0 is the M1 schema with `IF NOT EXISTS`, so fresh stores and M1 stores take the same path.

```sql
-- migration 0: the M1 schema (store.go:34-48), a no-op on M1 stores
CREATE TABLE IF NOT EXISTS changes (
  id TEXT PRIMARY KEY, ts INTEGER NOT NULL, path TEXT NOT NULL,
  blob TEXT NOT NULL, size INTEGER NOT NULL, mode INTEGER NOT NULL,
  uid INTEGER NOT NULL, gid INTEGER NOT NULL, origin TEXT NOT NULL,
  intent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS changes_path_ts ON changes(path, ts DESC);

-- migration 1 (M2)
ALTER TABLE changes ADD COLUMN kind   TEXT NOT NULL DEFAULT 'file';
                         -- file | link | deleted | digest
ALTER TABLE changes ADD COLUMN target TEXT NOT NULL DEFAULT '';
                         -- symlink target text (kind link)
CREATE INDEX IF NOT EXISTS changes_path_seq ON changes(path);
                         -- path + implicit rowid: newest row per path
PRAGMA user_version = 1;
```

- **Journal mode.** The store keeps SQLite's default rollback journal (`journal_mode=delete`) and never switches to WAL, because a WAL store cannot be opened from a read-only root, which M4's rescue path needs (NEXT_STEPS, tested there). `open` checks that `journal_mode` is `delete`.
- **Crash recovery.** A writer killed mid-transaction leaves a hot journal. The next writable open rolls it back.
  - Prototype (S13): the database file had grown to 12.4 MB inside the open transaction. After `kill -9`, a 17,920-byte journal was left behind.
  - The reopen showed the 10 committed rows, `integrity_check` ok, and the journal gone.
  - Step 7 turns this into a test.
- **No `boot_id` column.** NEXT_STEPS question 3 recommends M4, and CLAUDE.md says not to build ahead. With this migration framework, M4 adds it with one `ALTER`.
- **Backup.** Before the first migration of a non-empty M1 store, `changes.db` is copied to `changes.db.m1-backup` (0600, fsynced). The copy is taken while the transaction holds the write lock.
- **Newer stores.** A store with `user_version` greater than 1 is refused with one line, for example: `store /var/lib/smartconfig was written by a newer sc (schema 2)`.
- **What existing rows get.** `kind='file'` and `target=''` come from the column defaults. Nothing is rewritten, ids and blobs are unchanged, and the M1 index stays.
- **Checked on a copy of the real store (S5).** The copy had 4 rows, all with the same `ts`, at user_version 0.
  - `integrity_check` said ok and the version became 1.
  - The newest-row query uses `changes_path_seq`.
  - `user_version` is transactional: a rolled-back set leaves it unchanged.

  That run also added a `boot_id` column, which has since been dropped. Two plain `ALTER`s behave the same way.
- **Concurrent first opens.** In the drafting run, four concurrent first opens of a fresh store all succeeded.
- **The M1 binary on a migrated store (S6; automated in step 10).** It still snapshots and lists, and its rows get `kind='file'`. It refuses link, deleted and digest rows with one line.
- **The real store is migrated only with your OK.** Gate G1 (section 10) and sign-off S2 (section 13).
- **Store permissions.** `store.Init` makes `changes.db` mode 0600. It is 0644 today (PROJECT_LOG known issue). scd calls `Init` at start, so an existing file is fixed too.
- **Origins.** The only new origin is `auto`. Origin is free text, so it needs no schema change.

### 5.2 Row kinds

| kind | blob | target | size | mode, uid, gid | object file |
|---|---|---|---|---|---|
| file | sha256(content) | '' | bytes | fstat of the open file | yes (0600) |
| link | '' | link text | len(target) | lstat | no |
| digest | sha256(content) | '' | bytes | fstat | **no** |
| deleted | '' | '' | 0 | 0 | no |

- **Why link and deleted rows have an empty blob.** The judges showed that with a 64-hex blob, an M1 binary (for example a copy kept for rescue) restores a link row as a 0777 regular file holding the target text.
  - With an empty blob, M1 refuses with `sc: invalid blob name ""`.
  - With a digest row, M1 fails on the missing object, also in one line (S6).
- **Ids.** `makeID(path, ts, key)`, where the key is:
  - the blob for file and digest rows, so M1 ids are reproduced exactly;
  - `"link\n" + target` for link rows;
  - `"deleted"` for deleted rows.

  Collision retry works as in M1.
- **Dedup key.** (kind, blob, target, mode, uid, gid), compared with the newest row. This means two `deleted` rows in a row never happen.

### 5.3 Queries and ordering

- **Newest row for a path:** `SELECT … WHERE path = ? ORDER BY rowid DESC LIMIT 1`. The query plan is `SEARCH changes USING INDEX changes_path_seq (path=?)` (S5).
- **`sc log`:** `ORDER BY rowid DESC`. Today it is `ts DESC, rowid DESC` (store.go:290).
  - Wall-clock `ts` becomes a label only, so a clock stepping back cannot make an older row the "newest".
  - This matters here: the real store has 4 rows in the same second, and this VM was once paused for about 9 hours (M2_WATCHLIST section 9).
- **Live paths for rescans:** `SELECT path FROM changes WHERE rowid IN (SELECT max(rowid) FROM changes GROUP BY path) AND kind <> 'deleted'`. The caller filters the result through `Recorded` (7.2).
- **Why rowid order is safe.**
  - Rows are never deleted, and sc never runs VACUUM (auto_vacuum stays at SQLite's default, off).
  - The SQLite docs say VACUUM may renumber the rowids of tables without an INTEGER PRIMARY KEY. That is the only way this order could break.
  - An explicit sequence column can come with pruning, if pruning is ever added (section 15).

### 5.4 Go API

```go
// fsutil
type Stamp struct {
	Dev, Ino         uint64
	Size             int64
	MtimeNs, CtimeNs int64
}
type State struct {
	Kind   string // "file" or "link"
	Data   []byte // file content
	Target string // link text
	Meta   Meta   // mode, uid, gid (lstat for links)
	Stamp  Stamp  // fstat of the open file, or lstat of the link
	Stable bool   // false: it changed while being read
}
// ReadState never follows a symlink. Regular files are opened through
// the parent directory: open(dir, O_DIRECTORY|O_NOFOLLOW), then
// openat(name, O_NOFOLLOW|O_NONBLOCK), and fstat must match the lstat.
// Absent: IsNotExist. Dirs, FIFOs, sockets, devices, > 8 MB: ErrNotRecordable.
func ReadState(path string) (State, error)
func StampOf(path string) (Stamp, error)          // lstat only
func SymlinkAtomic(path, target string, uid, gid int) error
	// symlink ".<base>.sc-tmp-<rand>", Lchown, rename over, fsync dir
func RemoveFile(path string) error // refuses dirs; unlink; fsync dir

// store: Init, Open, Get, List, Blob keep their M1 signatures.
// Change gains Kind and Target.
// Open sets the fingerprint rule from scope.Default(); scd and tests
// replace it with the rule of their own scope.
func (s *Store) SetFingerprintOnly(f func(path string) bool)
func (s *Store) FingerprintOnly(path string) bool

type Obs struct {
	Path       string
	State      *fsutil.State // nil: the path is absent
	Digest     bool          // fingerprint only even if the rule says no
	                         // (user files over the size limit, 6.3)
	Created    bool          // proof of absence (6.4): "did not exist" first
	CheckStamp bool          // re-lstat under the write lock
	Force      bool          // insert even if equal (pre-restore)
	Origin     string
	Intent     string        // "" = computed: changed, mode 0644->0600, ...
}
type Result struct {
	Change   Change
	Recorded bool
	Moved    bool // the file changed after it was read: read it again
}
func (s *Store) Record(obs []Obs) ([]Result, error)  // at most 50
func (s *Store) Snapshot(path, origin, intent string) (Change, bool, error)
	// M1 signature, now built on Record
func (s *Store) Restore(id string) (restored Change, prev *Change, err error)
	// M1 signature; restored.ID == "" means nothing to do (5.6)
func (s *Store) LivePaths() ([]string, error)
```

`Record` works in five steps:

1. **Outside any transaction,** read each path's newest row, and drop the observations that equal it, unless `Force` is set. These cost no write and no lock.
   - An observation becomes a digest row when `Obs.Digest` is set or `FingerprintOnly(path)` holds.
2. **Still outside the transaction,** write the new blobs, each fsynced. Only file rows get blobs, never digest rows.
3. **`BEGIN IMMEDIATE`.** This waits for any restore in progress to commit.
4. **For each observation, under the lock:**
   - re-read the newest row;
   - if `CheckStamp` is set, `StampOf(path)` must equal `State.Stamp`, or the path must still be absent; otherwise the result is `Moved`;
   - compare the key again;
   - if `Created` is set and the path has no rows, insert the `did not exist` row first;
   - insert.
5. **Commit.**

- **Lock time.** The lock is held for about one commit. In the drafting run, 100 rows in one transaction took 48 ms.
- **Orphan blobs.** A blob written in step 2 whose row is then skipped stays behind as an unreferenced object. That is harmless, because objects are content-addressed.

### 5.5 Why the stamp re-check under the lock is needed

`ec15ece` makes `sc restore` insert its row, rename the file and commit, all under the write lock (store.go:358-373). That alone is not enough for scd. Here is the case it misses:

1. scd reads the user's broken edit B.
2. `sc restore` renames the good version G into place and commits.
3. scd then inserts B as the newest row, although G is on disk.
4. scd would also record G after the restore row, as an extra `auto` row.

**The fix.**

- Inside its own transaction, scd calls `lstat` again and compares the stamp.
- After a restore it sees the restored file's new inode, so it drops B and reads the path again.
- The next read gives G, which equals the restore row, so no row is written.

**The reverse order is covered too.** If scd reads G before the restore commits, its `BEGIN IMMEDIATE` waits. It then finds the restore row and inserts nothing.

**The same holds for links and deletions** (5.6):

- After a link restore, the new link has a new inode, so the stamp differs.
- After a removal the path is absent, which also differs from what scd read.

### 5.6 Restore

`Restore(id)` works in this order. Nothing is written before step 4.

1. **Look up the row.**
2. **Refuse, with one line, anything unsafe.** None of these refusals writes an object or a row.
   - **Fingerprint-only path, whatever the row's kind:** `sc: /etc/machine-id is fingerprint-only; sc never restores it`.
     - This covers deleted and `did not exist` rows, and file rows of such paths from M1 days.
     - It also covers the pre-restore row, which would otherwise store a host key's content (the critic's finding: the pre-restore went through `Snapshot`, store.go:345, without the rule).
     - `/etc/machine-id` must never be restored (M2_WATCHLIST.md:167). A missing one triggers first-boot presets of every unit (M2_WATCHLIST.md:334).
   - **Digest row** (a user file over the size limit, question 9): `sc: 1a2b3c keeps only a fingerprint of /home/u/.ssh/authorized_keys, not its content`.
   - **Unsafe target directory (question 4).**
     - The rule: every directory from `/` down to the target's parent, checked with lstat, must be a real directory, not a symlink, and owned by root or by the caller. Tests restore into their own temp dirs, which is why the caller is allowed.
     - Otherwise: `sc: refusing to restore into /home/vboxuser/.ssh (owner uid 1000, not root); see it with: sc cat 1a2b3c`.
     - On this VM the rule refuses users' homes, `/etc/colord` and `/etc/gnome-remote-desktop`. The last two are owned by service users (S9).
   - **A deleted row whose path is already absent** (or whose parent is gone): there is nothing to do. No row is written, and the CLI prints the "nothing to do" line and exits 0.
3. **For a file row,** read the blob and verify its checksum, as M1 does.
4. **Pre-restore row.** It records whatever is there now, file or link, with `Force`.
5. **Insert the restore row and write under the lock.** The restore row has the source row's kind. The write happens in `insert`'s `during` callback, under the write lock, exactly as `ec15ece` does for files (store.go:364-373):
   - file: `WriteAtomic`;
   - link: `SymlinkAtomic` (uid and gid are set before the rename, as CLAUDE.md requires);
   - deleted: `RemoveFile` (directories refused).

   Then commit. If the write fails, the row is rolled back.

**Output** (one line on stdout; the file line is M1's format, restore.go:34-35):

```
restored /etc/hosts from 1a2b3c (mode 0644 root:root), previous state saved as 4d5e6f
restored /etc/localtime from 1a2b3c (link -> /usr/share/zoneinfo/Etc/UTC, owner root:root), previous state saved as 4d5e6f
removed /etc/sc-accept.d/x.service to match 1a2b3c (did not exist), previous state saved as 4d5e6f
nothing to do: /etc/sc-accept.d/x.service is already absent, as in 1a2b3c
```

**Behaviour changes from M1.** Both are tested in step 8.

- A restore can now replace a symlink with a file and the reverse. M1 never did this on purpose (MILESTONES M1 notes).
- A restore can remove a file (question 3).

## 6. Event pipeline

### 6.1 Configuration

Every duration and limit is a `watch.Config` field, so tests can shrink them (section 11). The defaults:

```go
type Config struct {
	Home         string        // $SC_HOME
	Roots        []string      // nil: the scope's roots plus login .ssh roots
	ScopeText    string        // "": the embedded default scope
	PasswdPath   string        // "/etc/passwd"
	Quiet, Cap   time.Duration // 500 ms, 5 s
	RescanEvery  time.Duration // 1 h
	RescanMinGap time.Duration // 10 s
	StoreBackoff time.Duration // 30 s
	FloorBackoff time.Duration // 60 s
	FloorBytes   uint64        // 256 MiB
	UserFileMax  int64         // 64 KiB
	UserFileGap  time.Duration // 60 s
	MaxDirty     int           // 10,000
	Batch        int           // 50
	Log          io.Writer     // stderr
}
```

### 6.2 Start (`watch.New` and `Run`)

1. **Roots.**
   - The roots are the scope's roots (`/etc`, `/boot/grub`) plus `<home>/.ssh` for each account in `PasswdPath`.
     - `PasswdPath` is parsed directly, with no NSS.
     - An account counts only if its shell's base name is not `nologin`, `false`, `sync`, `halt` or `shutdown`.
     - On this VM that gives 2 roots, `/root/.ssh` and `/home/vboxuser/.ssh` (S10).
   - A root is used only if `lstat` shows a real directory. A missing root, or one that is a symlink (for example `ln -s / ~/.ssh`), is skipped:
     - it is logged once;
     - it is checked again at every rescan;
     - it is never watched and never compared with SC_HOME.
   - `--root DIR` replaces the list. Without `--root`, `sc watch` must run as root.
2. **SC_HOME check, before anything is created.**
   - **Resolve SC_HOME.** It is made absolute and cleaned. Its longest existing ancestor is found with `lstat` and resolved with `filepath.EvalSymlinks`, and the missing rest is appended. A dangling link on the way is refused with one line.
   - **Resolve the roots.** Each usable root is resolved with `EvalSymlinks` too.
   - **Compare.** If the resolved SC_HOME equals or lies below a resolved root, scd prints `sc: SC_HOME /etc/sc-accept.d/home is inside watched root /etc` and exits 1, with nothing created.
   - **Recheck.** The same comparison runs at every rescan. A root that now contains SC_HOME is skipped and logged.
   - **Resolution is for the check only.** Walking and watching always use the unresolved paths with lstat semantics.
   - **Why.** The draft resolved SC_HOME itself with `EvalSymlinks`, which fails when SC_HOME does not exist yet. So scd could not start on a fresh machine. It also resolved a symlinked `~/.ssh` to `/`, which let any user stop scd from starting. The new resolution handles a missing SC_HOME, one reached through a link, and a dangling link (S12).
3. **Create SC_HOME.** `MkdirAll(SC_HOME, 0700)`. scd needs no `sc init`.
4. **Single instance.**
   - scd takes `flock(LOCK_EX|LOCK_NB)` on `$SC_HOME/scd.lock` and writes its pid there.
   - If the lock is held: `sc: scd already running on /var/lib/smartconfig (pid N)`, exit 1.
   - The kernel drops the lock when the process dies. A second open in the same process also conflicts (S4), so this can be tested in one process.
5. **Store.**
   - `store.Init` is idempotent: it creates `objects/`, sets the dir to 0700, migrates, and sets `changes.db` to 0600.
   - Then `store.Open`, then `SetFingerprintOnly` with this scope's digest rule.
6. **inotify.** Two instances, one for the system roots and one for the home roots.
   - Each is `InotifyInit1(IN_CLOEXEC|IN_NONBLOCK)`, wrapped with `os.NewFile`.
   - A blocked `Read` then parks in Go's poller, and `Close` unblocks it (13.8 ms in the drafting run).
7. **Goroutines.** Start the two readers and the worker. Each one recovers panics into an error.
8. **First rescan and logging.**
   - A rescan with reason `startup` runs.
   - After the first walk scd logs `watching /etc, /boot/grub, 2 .ssh dirs (325 directories)`.
   - After the first pass it logs one summary: `baseline: N first seen, M changed and K deleted while not watching (Ts)`.
   - `first seen` rows get no line of their own.

### 6.3 Readers and worker

**Each reader goroutine.** For each `read()` (64 KiB buffer, parsed with `binary.NativeEndian`):

9. **`IN_Q_OVERFLOW` (wd -1):**
   - log once;
   - drop this instance's directory listings (6.4);
   - request a rescan of this instance's roots;
   - keep reading.
10. **Unknown wd:** skip.
    - `IN_IGNORED`: drop the wd, and request a rescan if it was a root.
    - `IN_MOVE_SELF` or `IN_DELETE_SELF` on a root: request a rescan.
    - Events with an empty name (the directory itself) are ignored; directory metadata is question 10.
11. **Scope check.** The path is dir + "/" + name. It is dropped unless `Recorded` holds (7.2). Excluded directories are never watched.
12. **`ISDIR` with CREATE or MOVED_TO:**
    - add the watch (DONT_FOLLOW\|ONLYDIR);
    - walk the subtree, adding a watch on each directory before listing it, and store each listing;
    - mark every file and link dirty. They are marked `created` only if the new directory itself qualifies under 6.4.

    `add_watch` on an inode that is already watched returns the existing wd, which is re-mapped to the new path (inotify(7); drafting run: `wd before rename 1, add_watch on renamed dir 1`).
13. **`ISDIR` with MOVED_FROM:**
    - `rm_watch` every wd at or below the old path, and drop their listings;
    - add a *prefix* dirty entry. The worker expands it to the live stored paths under the old path. They are now absent, so they get `deleted` rows.

    `ISDIR` with DELETE needs nothing, because the children's events and IGNORED cover it.
14. **Anything else:** `mark(path)`.
    - A new entry gets `first = now`, and `due = min(now + Quiet, first + Cap)`.
    - CREATE and MOVED_TO set `created` only with proof of absence (6.4).
    - Then the reader pokes the worker.
15. **Dirty-set bound.** The dirty set holds at most `MaxDirty` paths. Beyond that it is cleared and a full rescan is requested.

**Worker goroutine** (the only SQLite writer in scd):

16. **Wait.** Sleep until the earliest due time, a requested rescan, the periodic tick (`RescanEvery`), or stop.
17. **Rescan** (at most one per `RescanMinGap`; later requests are merged into the next one):
    1. Recompute the roots (6.2 steps 1-2), including the login homes.
    2. For each root, run `filepath.WalkDir` with lstat semantics. For each in-scope directory, under `mu`:
       - `add_watch`, recording the wd with this walk's generation;
       - list the directory, store the listing, and mark every recorded file and link due now, with the rescan's reason.
    3. Mark `store.LivePaths()`, filtered by `Recorded`, due now. This is how deletions are found. A manual row outside every root is never re-read.
    4. Remove the watches still tagged with an older generation. A watch the reader added mid-walk carries the current generation, so it is kept.

    ENOSPC on `add_watch` is logged once per rescan. Those directories are still walked and read by every rescan.
18. **Pop up to `Batch` due entries** under `mu`. Then, outside `mu`, handle each entry:
    - **`ReadState` outcomes:**
      - absent: an observation with no state (a deletion is written only if the newest row is live);
      - not recordable (directory, FIFO, socket, device, over 8 MB): skipped, and over-8-MB is logged once per path;
      - unstable, or replaced while being read: re-marked for now + `Quiet`. Once `first + Cap` has passed, it is recorded anyway with the intent suffix ` (still changing)`.
    - **Fingerprint only:** decided by the store's rule (5.4).
    - **User-owned files under a home root (question 9):**
      - over `UserFileMax`, set `Obs.Digest`;
      - if the path was recorded less than `UserFileGap` ago, re-mark it for the last row's time + `UserFileGap`.
    - **Free-space floor (question 9):** if `statfs($SC_HOME)` shows `Bavail*Bsize` under `FloorBytes`, observations that need a new blob wait.
      - They are re-marked for + `FloorBackoff`.
      - One line is logged on entering this state and one on leaving it.
      - Link, deleted, digest and mode/owner-only rows still go through.
    - **Record:** `store.Record(batch)` with origin `auto` and `CheckStamp`.
      - A `Moved` result re-marks the path for now + `Quiet`.
      - After 10 consecutive `Moved` results, the path is recorded without the check, with the intent suffix ` (still changing)`.
    - **Store errors** (disk full, EROFS, database locked for 5 s): log once per error text, re-mark for + `StoreBackoff`.
    - **Recorded:** one log line per row (7.6). If the path is `PasswdPath`, request a rescan, because login homes may have changed.
19. **Stop** (SIGTERM or SIGINT cancels the context):
    - both inotify files are closed and the readers return;
    - the worker finishes its current batch;
    - `Run` returns nil and the process exits 0.

    Paths still in their quiet period are left for the next start's rescan.

### 6.4 Proof of absence (`did not exist` rows)

**Why proof is needed.** A `did not exist` row claims that the path was absent before its first row, and restoring it deletes the file (question 3). The draft set `created` on any CREATE or MOVED_TO. On a first start almost no path has rows yet, so any rename-writer running during the baseline would have produced a false `did not exist` row for a file that always existed: sed -i, dpkg, useradd, `ln -sf`, systemctl enable, or chrome's daily tmp+mv. Restoring that row would then delete the file.

**The rule.** scd marks a path `created` only when all of these hold:

- The event is CREATE or MOVED_TO for a file or link. Or the path lies inside a directory that arrived by CREATE\|ISDIR or MOVED_TO\|ISDIR and that itself qualifies.
- The parent directory's listing is complete: a walk added the watch, listed the directory and stored the names before the reader handled the event.
- The name is not in that listing.
- The path still has no rows when `Record` takes the write lock (5.4).

**Otherwise the first row is `first seen`.** This holds, for example, for a sed -i on a file that existed but was not yet recorded, and for an `rm` then recreate before the baseline reached the file.

**Keeping the listings current.**

- Every walk replaces its directory's listing.
- An overflow drops the listings of that instance until the rescan rebuilds them, because lost events could hide a create, delete, create sequence.
- The listings cost one string per entry of each watched directory, a few thousand on this VM.

**The cost is on the safe side.** A real creation during the baseline or right after an overflow is recorded as `first seen`, so a restore cannot undo it. An existing file is never labelled as created.

### 6.5 Intents (the WHAT column)

- **Basic intents:** `first seen`, `did not exist`, `created`, `changed`, `mode 0644->0600`, `owner 0:0->1000:1000`, `link -> <target>`, `now a link -> <target>`, `now a file` and `deleted`.
- **They combine,** as in `changed, mode 0644->0600`.
- **Suffixes** are added when no live event explained a change:
  - ` while not watching` after the startup rescan;
  - ` (found by rescan)` after an overflow or periodic rescan.

### 6.6 Why this is enough for the writers seen in the research

- **In-place writers** (nano, cp, tee, `>`, grub-mkconfig, ucf) are read only after 500 ms of silence.
- **Rename writers** (sed -i, visudo, dpkg, useradd, install, ln -sf, sc restore) end with MOVED_TO on the real name. Their temp names are either excluded or gone when the quiet period ends.
- **Metadata changes** (chmod, chown, and install's late chmod) give ATTRIB.
- **Writes inotify cannot see** (mmap writes, hard-link writes, anything lost to an overflow or ENOSPC) are caught by the next rescan.

## 7. Scope and tiers

### 7.1 Where the scope lives

- `internal/scope/default.scope` is embedded in the binary. There is no file on disk in M2 (question 11).
- It is parsed once and cached. Every pattern is validated at parse time, so a bad pattern is a startup error and a test failure.
- Two rules are built in and not written in the file:
  - `root <home>/.ssh` for each login account;
  - `exclude $SC_HOME/**`.

### 7.2 Glob language (R20)

- **Patterns.** A pattern is an absolute path, or starts with `**/`, and is split on `/`.
- **`**`** as a whole segment matches zero or more segments. So `/a/b/**` also matches `/a/b` itself, which is how a directory is pruned.
- **`*`** matches any run of characters within one segment, **including a leading dot** (unlike the shell).
- **`?` and classes.** `?` matches one character. `[...]` is a character class with Go `path.Match` syntax:
  - `[^x]` negates, but `[!x]` does **not** (it is a class of `!` and `x`);
  - a literal `-` inside a class must be escaped (`[.\-]`);
  - `\` escapes the next character anywhere.
- **Braces.** `{a,b,}` gives alternatives, expanded as text before splitting, so an alternative may contain `/` or `*`.
  - Several groups are allowed; nesting is not.
  - Empty alternatives are allowed (`authorized_keys{,2}`).
- **`Recorded(p)`** holds when both of these are true:
  - p lies under a current root: a scope root, or a usable login `.ssh` root;
  - neither p nor any directory between that root and p is excluded.

  A path outside every root is never recorded by scd and never re-read by a rescan, even when the store has a manual row for it.
- **Evaluation order.**
  - include/exclude: the first matching line wins, and no match means included.
  - A root itself is never excluded.
  - `digest`: any match. `tier`: the first match, else tier 4.
- **Paths are raw bytes.** Log lines quote a path with `%q` only when it contains a space, a quote, a backslash, a control character or non-UTF-8 bytes.
- **Checked on this VM (S2).**
  - In Go 1.22, `path.Match("snap[-.]*", …)` is a syntax error even when the name does not match, so parse-time validation catches it.
  - `[!x]*` does not match `abc`, and `*~` matches `.foo~`.

### 7.3 `default.scope` (this exact text was checked against this VM's /etc, 7.4)

```
# SmartConfig default scope (M2), built into sc.
# Lines: root DIR | include GLOB | exclude GLOB | digest GLOB | tier N GLOB
# include/exclude: the FIRST matching line wins; no match = included.
# Only paths under a root are recorded. A root itself is never excluded.
# An excluded directory is not watched. digest: any match.
# tier: first match, else 4.
# Built in, not written here: root <home>/.ssh for each login account,
# and exclude $SC_HOME/**.

root /etc
root /boot/grub

# A. Never recorded anywhere: sc's own temp files, editor swap, lock and
#    probe files (they can hold a copy of a secret file's buffer).
exclude **/.*.sc-tmp-*
exclude **/.*.sw[a-p]
exclude **/.*.{swx,swpx}
exclude **/.#*
exclude **/#*#
exclude **/{4913,DEADJOE}
exclude **/.goutputstream-*

# B. /boot/grub: only the two files GRUB reads.
include /boot/grub/{grub.cfg,custom.cfg}
exclude /boot/grub/**

# C. <home>/.ssh roots: only the files sshd reads.
include **/.ssh/authorized_keys{,2}
exclude **/.ssh/*

# D0. Names inside the block D directories that their consumer skips
#     itself (checked on this VM). dpkg leaves each conffile of a package
#     being upgraded as NAME.dpkg-new until it is configured.
exclude /etc/grub.d/**/{*.dpkg-*,*~}
exclude /etc/apparmor.d/**/*.dpkg-{new,old,dist,bak,remove}
exclude /etc/apparmor.d/**/{*~,*.orig,*.rej}
exclude /etc/{sudoers.d,apt/apt.conf.d,kernel,cron.d,cron.hourly,cron.daily,cron.weekly,cron.monthly}/**/*.dpkg-{new,tmp,old,dist,bak,remove,backup}
exclude /etc/{sudoers.d,apt/apt.conf.d,kernel,cron.d,cron.hourly,cron.daily,cron.weekly,cron.monthly}/**/{*.ucf-new,*.ucf-old,*.ucf-dist,*~}

# D. Consumers that run or parse names the noise rules below would hide
#    (backup suffixes, sed/mktemp names): record everything else there.
include /etc/{grub.d,initramfs-tools,apparmor.d}/**
include /etc/{sudoers.d,apt/apt.conf.d,kernel,logrotate.d,NetworkManager/dispatcher.d}/**
include /etc/cron.{d,hourly,daily,weekly,monthly}/**
include /etc/dconf/db/*.d/**

# E. Editor, package and tool staging names and backups (noise filter only;
#    the "still there after the quiet period" rule is the real temp guard).
exclude **/*~
exclude **/*.dpkg-{new,tmp,old,dist,bak,remove,backup}
exclude **/*.{ucf-new,ucf-old,ucf-dist,merge-error}
exclude **/*.pam-{new,old}
exclude **/{sed??????,XX??????,*.tmp}
exclude **/*.{old,orig,bak,save,rej,distUpgrade,disabled}
exclude /etc/{passwd,shadow,group,gshadow,subuid,subgid}{.lock,+,-,.edit}
exclude /etc/{passwd,shadow,group,gshadow,subuid,subgid}.[0-9]*
exclude /etc/{nshadow,npasswd,.pwd.lock,.pwd??????}
exclude /etc/security/opasswd

# F. Generated files and runtime state.
exclude /etc/ld.so.cache
exclude /etc/ssl/certs/**
exclude /etc/alternatives/**
exclude /etc/rc?.d/**
exclude /etc/console-setup/cached_*
exclude /etc/xml/**
exclude /etc/dconf/db/*
exclude /etc/systemd/system/snap{-,.}*
exclude /etc/systemd/system/*.wants/snap{-,.}*
exclude /etc/systemd/system/snapd.mounts.target.wants/**
exclude /etc/systemd/user/snap.*
exclude /etc/udev/rules.d/70-snap.*.rules
exclude /etc/cups/{printers,classes,subscriptions}.conf*
exclude /etc/cups/ssl/**
exclude /etc/cups/*.O
exclude /etc/{printcap,mtab,.updated,adjtime}
exclude /etc/brltty/**
exclude /etc/apt/preferences.d.save/**
exclude /etc/tmp[a-z0-9_][a-z0-9_][a-z0-9_][a-z0-9_][a-z0-9_][a-z0-9_][a-z0-9_][a-z0-9_]/**
exclude /etc/{.git,.hg,.bzr,_darcs}/**
exclude /etc/.etckeeper

# G. Secrets with no rescue value.
exclude /etc/{ssl/private,credstore,credstore.encrypted}/**
exclude /etc/brlapi.key
exclude /etc/cloud/cloud.cfg.d/99-installer.cfg
exclude /etc/ppp/*-secrets

# H. Fingerprint, mode and owner only; content is never stored, shown or
#    restored.
digest /etc/ssh/ssh_host_*_key
digest /etc/machine-id

# I. Tiers: only how loudly scd logs a change (1 boot, 2 access, 3 network).
tier 1 /etc/{fstab,crypttab,modules,sysctl.conf,ld.so.conf,ld.so.preload,rc.local,machine-id}
tier 1 /etc/{fstab.d,default/grub.d,grub.d,initramfs-tools,modprobe.d,modules-load.d,depmod.d}/**
tier 1 /etc/{sysctl.d,tmpfiles.d,ld.so.conf.d,kernel,dbus-1}/**
tier 1 /etc/default/grub
tier 1 /boot/grub/{grub.cfg,custom.cfg}
tier 1 /etc/systemd/{system,system.control,system.attached,system-generators,system.conf.d}/**
tier 1 /etc/systemd/system.conf
tier 1 /etc/udev/rules.d/**
tier 1 /etc/udev/udev.conf
tier 1 /etc/apparmor.d/{tunables,abstractions,abi}/**
tier 1 /etc/apparmor/parser.conf
tier 2 /etc/{sudoers,sudo.conf,passwd,group,shadow,gshadow,nsswitch.conf,login.defs,shells}
tier 2 /etc/{environment,profile,bash.bashrc,nologin,hosts.allow,hosts.deny}
tier 2 /etc/{sudoers.d,pam.d,security,profile.d,polkit-1,gdm3,ssh/sshd_config.d}/**
tier 2 /etc/ssh/{sshd_config,sshd_not_to_be_run,ssh_host_*_key}
tier 2 /etc/default/{ssh,keyboard,console-setup}
tier 2 /etc/systemd/logind.conf
tier 2 /etc/systemd/{logind.conf.d,user}/**
tier 2 /etc/systemd/{system-environment-generators,user-generators,user-environment-generators,system-preset}/**
tier 2 **/.ssh/authorized_keys{,2}
tier 3 /etc/{resolv.conf,hosts,nftables.conf,ca-certificates.conf,ssl/openssl.cnf,default/ufw}
tier 3 /etc/{netplan,NetworkManager,network,ufw,iptables,apt,dpkg}/**
tier 3 /etc/systemd/resolved.conf
tier 3 /etc/systemd/{resolved.conf.d,network}/**
```

Notes on the rules:

- **Block D0: names a consumer skips itself.** Each exclude matches only names that the directory's own consumer ignores, as checked on this VM (S14):
  - **grub.d:** grub-mkconfig skips `*.dpkg-*` and `*~` (grub-mkconfig_lib:212-225, grub-mkconfig:295-309).
  - **apparmor.d:** `apparmor_parser` skips `.dpkg-{new,old,dist,bak,remove}` silently and `~`, `.orig` and `.rej` with a message. It loads `.dpkg-tmp`, `.dpkg-backup`, `.ucf-*` and `.bak`, so those stay recorded.
  - **sudoers.d:** sudo skips names that contain a dot or end in `~` (sudoers(5)).
  - **kernel and cron.\*:** run-parts, and cron for `cron.d`, runs only `[A-Za-z0-9_-]` names (run-parts --test, cron(8)).
  - **apt.conf.d:** apt ignores `.dpkg-*`, `.ucf-*` and `~` (apt-config dump).
- **Why D0 matters.** Without it, block D would record dpkg's staging names. dpkg writes every conffile of a package being upgraded as `NAME.dpkg-new` and handles conffiles later, at configure (dpkg(1)). `/etc/apparmor.d` alone holds 259 dpkg conffiles (S14), and apparmor has a pending update. The soak's apt upgrade would have produced hundreds of `did not exist`, `created` and `deleted` noise rows.
- **Left unfiltered on purpose.**
  - `initramfs-tools`: mkinitramfs runs `hooks/foo.dpkg-old` (M2_WATCHLIST section 5).
  - `logrotate.d`: logrotate reads backup and temp names (M2_WATCHLIST section 7).
  - `NetworkManager/dispatcher.d` and `dconf`: not checked here. They hold no dpkg conffiles on this VM except one in dconf.
- **Block D (consumers).**
  - `grub.d`, `initramfs-tools` and `apparmor.d` were tested in the research: grub-mkconfig runs an executable `10_linux.bak`, mkinitramfs runs `foo.dpkg-old`, and apparmor_parser loads `p.bak`.
  - The others (sudoers.d, apt.conf.d, kernel/run-parts, logrotate.d, cron.*, NM dispatcher.d) are included because each parses or runs dot-free names such as `sedAbC123` (checked for run-parts in S14), and logrotate also reads `.bak`.
  - Block A comes before D0 and D, so swap files and sc temp files are never recorded, even in those directories.
- **Snap rules.** They use `snap{-,.}*` because `snap[-.]*` is invalid in Go (S2). So the 10 dpkg-owned `snapd.*` enable links stay recorded, and `snapd.apparmor.service` is kept (S7).
- **dconf.** `/etc/dconf/db/*.d/**` is included before `/etc/dconf/db/*` is excluded. The compiled `ibus` database is dropped, and the `ibus.d/` sources stay.
- **`/etc/tmpfiles.d`** is not hit by the installer-mkdtemp rule. `/etc/tmpxdvph4n_` is.
- **`/etc/ld.so.cache`** is excluded. It is generated output (58,003 B, rewritten by ldconfig after most installs), and the research says never to snapshot it.

### 7.4 Checked against this VM (S7, S15)

A prototype of the matcher above was run against `sudo find /etc /boot/grub /root/.ssh /home/vboxuser/.ssh -xdev` (read-only). The D0 lines change nothing in today's listing, because no staging names exist right now (S15).

- **Size:** 325 watched directories, 1,145 recorded regular files and 234 recorded symlinks.
- **Tiers:** tier 1: 342, tier 2: 121, tier 3: 66, tier 4: 850.
- **Fingerprint only:** exactly `/etc/machine-id` and the three `ssh_host_*_key` files.
- **/boot:** only `/boot/grub/grub.cfg` is recorded (`custom.cfg` does not exist).
- **.ssh:** only the two `authorized_keys` files are recorded.
- **Tier 1-2 paths that the rules exclude: 60.** These are 59 snapd runtime names (snap-*.mount, `snap.*` units, `70-snap.*.rules`, `snapd.mounts.target.wants/*`) plus `/etc/security/opasswd`, all excluded on purpose. They become the allowlist of the real-listing test.
- **Outside every root:** `/var/log/syslog` is not recorded.
- **Synthetic names, all as intended.** Kept:
  - `grub.d/10_linux.bak`, `grub.d/40_custom.orig`, `grub.d/10_linux.disabled`;
  - `initramfs-tools/hooks/foo.dpkg-old`, `hooks/foo.dpkg-new`;
  - `apparmor.d/p.dpkg-tmp`, `p.dpkg-backup`, `p.ucf-new`, `p.bak`;
  - `sudoers.d/sedAbC123`, `sudoers.d/XXab12cd`, `sudoers.d/README`;
  - `logrotate.d/rsyslog.dpkg-new`.
- **Excluded:**
  - `grub.d/10_linux.dpkg-new`, `10_linux.dpkg-old`, `10_linux~`;
  - `apparmor.d/usr.bin.foo.dpkg-new`, `abstractions/base.dpkg-dist`, `p.dpkg-remove`, `p~`, `p.orig`;
  - `sudoers.d/x.dpkg-new`, `sudoers.d/x~`;
  - `apt.conf.d/01autoremove.dpkg-new`, `20auto-upgrades.ucf-dist`;
  - `cron.daily/logrotate.dpkg-new`, `kernel/postinst.d/zz-update-grub.dpkg-new`;
  - `.README.swp`, `.hosts.sc-tmp-123`, `apt.conf.d/4913`, `/etc/sedAbC123`;
  - `passwd-`, `passwd.lock`, `shadow.1234`, `grubenv`, `i386-pc/*`, `~/.ssh/id_ed25519`, `known_hosts`.

### 7.5 Login homes

`scope.LoginHomes(passwd []byte)` parses the passwd lines. It returns each home whose account's shell base name is not `nologin`, `false`, `sync`, `halt` or `shutdown`. Duplicates and relative homes are dropped.

- **Tests:** against `testdata/passwd`.
- **When homes change:** after `PasswdPath` is recorded, scd rescans and adds the new `.ssh` roots.
- **A brand-new `~/.ssh`** of an existing user is picked up at the next periodic rescan (section 15).
- **A `~/.ssh` that is a symlink** is never watched (6.2).

### 7.6 Tiers as journald priority

- **One line per recorded row, on stderr**, path first so it fits an 80-column console. Examples:
  - `T2 /etc/sudoers: mode 0440->0666 (9f8e7d)`
  - `T1 /etc/fstab: changed (1a2b3c)`
- **File content never appears** in any line. A test writes a marker string into a watched file and checks that it is never printed.
- **The priority prefix** is written only when `$JOURNAL_STREAM` equals the `dev:ino` of stderr's fstat, as systemd.exec(5) says to check (S3). In a terminal, lines get a `15:04:05` time instead.
  - `<4>` warning for tiers 1-2;
  - `<5>` notice for tier 3;
  - `<6>` info for tier 4, start, stop and summaries;
  - `<3>` err for scd's own problems (overflow, ENOSPC, disk floor, store errors).
- **Reading the journal.** journald parses the prefix (checked in drafting with a transient unit). So `journalctl -u scd -p warning` lists the tier 1-2 changes plus scd's own errors.
- **Baseline.** `first seen` rows are not logged one by one; a single summary line covers them.

## 8. CLI changes

New:

```
sc watch [--root DIR]...
    Watch /etc, /boot/grub and each login account's ~/.ssh and record
    every change in $SC_HOME, which is created if missing. Runs until
    SIGINT or SIGTERM; scd.service runs it. Needs root unless --root is
    given.
    --root DIR   watch DIR instead of the default roots (repeatable; for
                 tests and trials). The default rules still apply.
```

- **Exit codes.** Exit 0 on a signal. Exit 1 with one stderr line when:
  - SC_HOME is inside a root, or its path holds a dangling link;
  - another scd holds the lock;
  - the store cannot be created or opened;
  - inotify init fails;
  - it is not root and no `--root` was given;
  - an internal error or panic occurs.
- **Testing entry point.** `run()` becomes `runContext(ctx, …)`, so tests stop the watcher by cancelling a context.
- **No NSS.** Nothing in `sc watch` does an NSS lookup.

Changed:

- **`sc snapshot PATH`.**
  - A symlink is recorded as a link row: `snapshot 1a2b3c  /etc/localtime  (link -> /usr/share/zoneinfo/Etc/UTC)`.
  - Fingerprint-only paths print `(fingerprint only)`.
  - Manual dedup uses the full key, so a chmod-only change is no longer reported as "unchanged".
  - A directory or FIFO is still refused.
- **`sc log`.**
  - Rows are ordered by insertion.
  - The SIZE cell shows `link`, `deleted` or `digest` for those kinds.
  - Origin `auto` appears.
  - ORIGIN stays field 4, so `scripts/smoke.sh:56` still works.
- **`sc cat ID`.**
  - Link rows print the target.
  - Deleted and digest rows fail with one line, for example `1a2b3c records that /etc/x did not exist`.
  - A fingerprint-only path is refused whatever the row's kind: `sc: /etc/ssh/ssh_host_rsa_key is fingerprint-only; sc never shows its content`.
- **`sc diff ID`.**
  - The disk side is read with `ReadState` (file content or link target).
  - Link rows diff their target text.
  - Deleted and digest rows, and fingerprint-only paths, are refused as above. So `sc diff` never reads a host key from disk.
- **`sc restore ID`.**
  - Link and deleted rows are restored (5.6).
  - Fingerprint-only paths, digest rows and unsafe directories are refused, each with one line and nothing written.
  - The output lines are the ones in 5.6.

## 9. systemd unit (`scripts/scd.service`)

```ini
[Unit]
Description=SmartConfig watcher (scd): records config file changes
Documentation=https://github.com/NikhilSah27/SmartConfig
# multi-user.target is ordered after every service it wants, this one too.
# Type=exec makes scd count as started once the binary runs, so boot waits
# only for the exec, never for the baseline scan. No other unit orders
# itself after scd or requires it.
StartLimitIntervalSec=10min
StartLimitBurst=5

[Service]
Type=exec
ExecStart=/usr/local/sbin/sc watch
SyslogIdentifier=scd
# A fatal Go runtime error prints one line instead of a goroutine dump.
Environment=GOTRACEBACK=none
Restart=on-failure
RestartSec=10s
TimeoutStopSec=10s
Nice=10
IOSchedulingClass=best-effort
IOSchedulingPriority=7
# Deliberately no ProtectSystem=, ProtectHome=, PrivateTmp=,
# TemporaryFileSystem= or InaccessiblePaths=: they could hide /etc, /boot,
# /root or /home from the watcher.

[Install]
WantedBy=multi-user.target
```

- **Boot ordering.** On this VM, `multi-user.target` has an implicit `After=` on each service it wants: 40 services are in its After list (S8). With `Type=exec`, systemd counts the unit as started once the binary has been executed (systemd.service(5), S8). So boot waits only for the exec, never for the baseline.
- **`GOTRACEBACK=none`.** It must be set in the environment. `debug.SetTraceback("none")` in code has no effect, because it cannot go below the default level (S16).
  - With the variable set, a concurrent map write or a goroutine panic prints one line.
  - A stack overflow is still dumped in full (S16).
- **I/O priority.** best-effort 7, not `idle`. This VM uses mq-deadline, which honours I/O classes (S8). An idle-class commit fsync could hold the SQLite write lock past `sc`'s 5 s busy timeout.
- **Restart limits.** A startup that keeps failing is retried at most 5 times in 10 minutes, then left failed. It does not loop forever.
- **Verification.** `systemd-analyze verify` exits 0 on this unit text with ExecStart pointed at an existing binary. With the real path it only complains that `/usr/local/sbin/sc` is not installed yet (S8). The unit name is free (`systemctl list-unit-files 'scd*'` lists 0). The `Environment=` line was added after that check; acceptance step 2 runs `systemd-analyze verify` again on the generated unit.
- **Install by hand until M5.** README gets these commands. They are documented, not scripted, and they are not run on this VM before gate G1:
  ```
  sudo install -m 0755 bin/sc /usr/local/sbin/sc
  sudo install -m 0644 scripts/scd.service /etc/systemd/system/
  sudo systemctl daemon-reload && sudo systemctl enable --now scd
  ```
  scd creates `/var/lib/smartconfig` itself, so `sc init` is not needed.

## 10. Order of implementation

### 10.1 The loop for every step (NEXT_STEPS 2.2 plus your standing request)

1. WORKLOG's "Now" names the step before it starts, and is pushed.
2. Write the code and its tests.
3. Run the checks:
   - `gofmt -l .` must be empty, and `go vet ./...` and `go test ./...` must be clean;
   - `sudo -E go test ./...` too, where a step has root-only parts;
   - the CGO_ENABLED=0 build must work;
   - `make race`, if you approve NEXT_STEPS question 4.
4. Commit: one code commit, then one `docs: worklog: M2 step N` commit (a dated log line with the hash, and "Now" moved to the next step), then `git push`.
5. For watcher commits (steps 11-13), run `go test -count=5 ./internal/watch/` with no review swarm running. Run `-count=20` once at the end of chunk D.

**Stop and ask** when:

- the plan has to change;
- a new dependency or tool is needed;
- something would touch the real `/etc`, `/boot` or store;
- a step has run about 2 hours without a commit;
- a test flakes for an unclear reason.

### 10.2 Gates

- **G1: the real store.**
  - No M2 binary opens `/var/lib/smartconfig` until you say yes.
  - Tests, the smoke run, `make m1-compat` and the acceptance run all use throwaway SC_HOMEs.
  - The first real run is sign-off item S2, after backups.
- **G2: the real system.** These happen only after a fresh VirtualBox snapshot, an announcement and your OK:
  - anything that writes to the real `/etc`, `/boot` or systemd state beyond the acceptance script's own test paths;
  - the owner scenario, enabling `scd.service`, the reboot, and the apt upgrade.

### 10.3 Chunks (NEXT_STEPS 2.3)

| Chunk | Steps | What | Review | You see (FYI) |
|---|---|---|---|---|
| A | 1-3 | store housekeeping and schema migration | light | - |
| C | 4-5 | glob matcher and default scope | light | a table of what gets watched on this VM, per tier, and what is excluded |
| B | 6-10 | row kinds (file, link, deleted, digest) through the CLI, and the sc-m1 check | normal | a sample `sc log`; `make m1-compat` and the M1 smoke run pass |
| D | 11-13 | the inotify watcher engine | full: concurrency, events, security | - |
| E | 14-17 | `sc watch`, the unit, the acceptance script, docs | normal | - |

- **Chunk C is built before chunk B,** because the store's fingerprint-only rule comes from `internal/scope`. NEXT_STEPS says the plan's step numbers win over the chunk letters.
- **No two unreviewed chunks.** A chunk's review may run while the next chunk is built, but there are never two unreviewed chunks.

### 10.4 Steps (one commit each)

| # | Chunk | Commit | Content | Tests |
|---|---|---|---|---|
| 0 | - | `docs: M2 plan` | this plan as `docs/M2_PLAN.md`, after your approval | none |
| 1 | A | `store: order history by insert order, not wall clock` | `List` and the newest-row query use `ORDER BY rowid DESC` | clock stepped back one hour: newest is the later insert; `sc log` order |
| 2 | A | `store: create changes.db with mode 0600` | `Init` chmods `changes.db`, including an existing 0644 file | `TestInitMode` checks dir 0700 and db 0600, including a pre-existing 0644 db |
| 3 | A | `store: schema version 1 adds kind and target` | `migrate.go`: migrations 0 and 1 in one IMMEDIATE transaction; M1 backup; newer versions refused; `journal_mode` checked | M1 fixture from the literal M1 DDL with same-second rows: ids, order, kind `file`, version 1, index present, backup 0600, reopen is a no-op; 4 concurrent first opens; newer version refused; an injected failure leaves version 0 and the table intact; M1's INSERT and SELECT texts still work on v1; `journal_mode` is `delete` |
| 4 | C | `scope: glob language` | `glob.go` | `**` matching zero and many segments; `/a/**` matches `/a`; `*` within one segment, including a leading dot; `?`; `[^x]`; `[!x]` is literal; `[.\-]`; `\*`; braces with several groups, an empty alternative, a `*` and a `/` inside; nested braces give an error; `snap[-.]*` fails at parse time; backslash and UTF-8 names |
| 5 | C | `scope: default scope, roots, tiers, fingerprint list, login homes` | `scope.go`, `default.scope`, `homes.go`; `Recorded` requires a root | all synthetic cases from 7.4, including the D0 names; a curated tier 1-2 list never excluded; tier and fingerprint table; a path outside every root is not recorded; passwd fixture gives root and a bash user, not sync, nologin or false; `TestDefaultScopeRealListing` reads `$SC_ETC_LISTING`, skips when it is unset, allows only the snapd runtime names and opasswd, and pins the kept count to a range |
| 6 | B | `fsutil: read files and symlinks with a stamp; atomic symlink write` | `Stamp`, `State`, `ReadState` (parent dirfd plus openat), `StampOf`, `SymlinkAtomic`, `RemoveFile`; `ReadWithMeta` rebuilt on `ReadState` | ReadState on a file; on links (dangling, to `/dev/null`, to a dir; never followed); absent; dir and FIFO refused without blocking; over 8 MB; a symlinked parent dir refused; unstable read via a hook; stamp changes on chmod, rename-over and write, not on read; SymlinkAtomic over a file, over a link and new, with no temp left and lchown before rename (root half); RemoveFile refuses a dir; M1 fsutil tests unchanged |
| 7 | B | `store: record files, links, deletions and fingerprints` | `record.go`: `Obs`, `Record` (at most 50), `LivePaths`; `Snapshot` rebuilt on it; fingerprint rule from `scope.Default()` with `SetFingerprintOnly`; in the same commit, the symlink case of `TestCLIErrorsAreOneLine` (main_test.go:113-116) becomes FIFO and dir cases, so `go test ./...` stays clean | touch gives no row; chmod gives a row with the same blob; chown; content; link created and retargeted; file to link and back; delete gives one `deleted` row and a second observation adds none; recreate gives `created`; `Created` on a new path gives `did not exist` plus `created` in one transaction with adjacent rowids; `Created` on a path that has rows gives no `did not exist`; a digest row writes no object; a manual snapshot of a fingerprint-only path gives a digest row; a moved stamp gives `Moved` and no row; in a batch of 50 with one moved, 49 are recorded; manual "unchanged" kept; pre-restore always inserts; a file row id equals M1's `makeID`; `TestSnapshotRefuses` rewritten (link accepted, dir and FIFO refused); `TestCrashMidRecord`: a helper process (the test binary re-run) is SIGKILLed inside Record's transaction after a cache spill; the reopen shows the committed rows, `integrity_check` ok, no journal left |
| 8 | B | `store: restore links and deletions; refuse fingerprint paths and unsafe dirs` | `restore.go`: refusals before any write; `WriteAtomic`, `SymlinkAtomic` and `RemoveFile` all inside `insert`'s `during` callback | link row restored; file over link and link over file; `did not exist` restore removes the file after a pre-restore row; already absent gives no row; fingerprint-only path: a deleted row and a file row (inserted with the rule off, as M1 would have) both fail with one line, no object and no row; digest row refused; symlinked ancestor refused; dir owned by another user refused (root half: temp dir chowned to 65534); every refusal leaves the row count and objects dir unchanged; `TestRecordDuringRestore` for file, link and deleted restores: Record with CheckStamp from a goroutine while Restore is paused in `testHookAfterRestoreWrite` writes no auto row after the restore row; `TestRestoreHoldsLockAcrossRename` still passes; `TestRestoreWriteFailureRecordsNothing` (store_test.go:411) now fails the write itself (parent dir 0500 as a normal user, skipped as root), because a missing directory is refused earlier, and a new case checks that refusal writes nothing |
| 9 | B | `cli: snapshot, log, cat, diff and restore understand row kinds` | section 8 | `TestCLI` extended; `TestCLIErrorsAreOneLine`: cat of deleted and digest rows, restore of a digest row, cat, diff and restore of a fingerprint-only path; the restore output lines of 5.6; ORIGIN is still field 4 |
| 10 | B | `scripts: prove sc-m1 still works on a migrated store` | `scripts/build-sc-m1.sh` (git archive of tag `m1`, else `ec15ece`; `GOPROXY=off go build`; nothing in the checkout changes, S17); `TestM1Compat` in `internal/store` (skips unless `SC_M1_BIN` is set); Makefile `m1-compat` | `TestM1Compat`: a v1 store made by the M2 code holds file, link, deleted, `did not exist` and digest rows; sc-m1 `log` lists all; sc-m1 `cat`, `diff` and `restore` of the file row work; sc-m1 `restore` of each other kind exits 1 with one `sc:` line, leaves the target unchanged (sha256, mode, owner and inode, or link text) and adds no row; sc-m1 `snapshot` adds a `kind='file'` row; the M2 code reads the rows sc-m1 wrote |
| 11 | D | `watch: inotify on the syscall package` | `inotify_linux.go` | a real instance on `t.TempDir()`: CREATE, CLOSE_WRITE, MOVED_FROM and MOVED_TO with cookie, ATTRIB, ISDIR; several events per buffer and name padding; a synthetic overflow record; add_watch on a symlink to a dir fails; `Close` unblocks `Read` |
| 12 | D | `watch: watcher with quiet period, proof of absence, rescans at start and new directories` | `watcher.go`, `Config`, two instances, flock, SC_HOME and root checks, panic recovery | writer matrix (11.3); structural and proof-of-absence tests (11.4); restore test; restores during a 2,000-file baseline never fail with `database is locked`; marker string never logged; an injected panic makes `Run` return an error; a second instance fails; SC_HOME under a root makes `New` fail before creating anything; a missing SC_HOME is created 0700; a symlinked `.ssh` root: scd starts, does not watch it, logs it once |
| 13 | D | `watch: overflow, periodic rescan, root changes, watch limit, disk floor, user-file limits, crash` | `rescan.go`, backoff | real overflow; a flood on a home root overflows only the home instance; periodic tick; rescan rate limit; passwd change adds a home root; a missing root appears later; IN_IGNORED or MOVE_SELF on a root; ENOSPC through a hook; low space through a statfs hook; user file over the limit gives a digest row, with the gap between rows; store-error backoff; dirty set bound; a manual row outside the roots is never re-read; `TestKillDuringBaseline` (11.4) |
| 14 | E | `cli: sc watch` | `cmd/sc/watch.go`, `runContext`, journald prefixes, `--root` | end to end with `--root` and cancel; SC_HOME inside a root gives exit 1, one line, and nothing created; second instance gives one line; prefix present only when `JOURNAL_STREAM` matches stderr |
| 15 | E | `scripts: scd.service and the M2 acceptance run` | unit, `accept-m2.sh`, Makefile `accept-m2` | `TestUnitFile`: Type=exec, Restart=on-failure, StartLimitBurst present, WantedBy=multi-user.target, IOSchedulingClass not idle, `Environment=GOTRACEBACK=none`, and none of ProtectHome=, ProtectSystem=, TemporaryFileSystem=, InaccessiblePaths= or Before=; `bash -n` on `accept-m2.sh` and `build-sc-m1.sh`; the acceptance run itself is yours |
| 16 | E | `docs: sc watch and scd install notes` | README (sc watch, install by hand, no `sc init` needed), PROJECT_LOG (rescue binary, resume prompt), CLAUDE.md Layout if question 12 says yes | none |
| 17 | E | `docs: M2 done` | MILESTONES (M2 notes, box ticked, next "Current"), NEXT_STEPS, WORKLOG; tag `m2` | only after every item in section 13 holds |

This order fixes two ordering bugs:

- **The draft's CLI step came before the scope it needs.** The scope (steps 4-5) now comes before the store and CLI steps that use it.
- **The critic's finding on step 5.** The CLI test change now lands in the same commit (step 7) as the Snapshot change that needs it.

## 11. Test details

### 11.1 Ground rules

- All tests set `SC_HOME` to `t.TempDir()` and watch only other `t.TempDir()` directories.
- No test reads or writes `/etc`, `/boot` or `/var/lib`.
  - The default-scope tests are pure string matching.
  - The passwd tests use fixtures.
  - The real-listing test reads only the file named by `$SC_ETC_LISTING`, which the acceptance script produces.
  - Reading `/proc/sys/fs/inotify/*` is allowed.
- `t.TempDir()` is on the same ext4 as `/etc` here, so inotify behaves as it will in production.
- **Go 1.22 only.** No `t.Context()`: tests use `context.WithCancel` plus `t.Cleanup`.
- **Root-only halves** skip as a normal user, as in M1, and run with `sudo -E go test ./...`.
  - `TMPDIR` is unset on this VM. If it ever pointed into a user-owned directory, root runs of the restore tests would be refused by the directory-owner rule.
- **Crash tests** re-run the test binary as a helper process (`os.Args[0] -test.run=^TestHelper…$` plus an env var). They send signals only to their own child (S13).
- **Watchers never run in parallel,** because inotify instances are per uid (limit 128).

### 11.2 Helpers (internal/watch)

- **`startWatcher(t, cfg)`:**
  - uses `Quiet` 50 ms, `Cap` 1 s, backoffs and gaps of 100-300 ms, `RescanMinGap` 50 ms, and a long `RescanEvery` except in the tick test;
  - uses a `ScopeText` whose `root`, `include`, `exclude` and `digest` lines point into `t.TempDir()`, and a fixture `PasswdPath`;
  - lets in-package tests call the unexported `w.requestRescan()`;
  - registers a `t.Cleanup` that cancels the watcher and waits for it.
- **`waitFor(cond, 5s)`** polls the store every 10 ms. All timings are Config fields, so no test waits on a production-length timer.
- **`testHookProcessed func(path string)`** gives negative checks: the path was processed, and there is still no row.
- **The sentinel barrier** also gives negative checks:
  1. Do the action.
  2. Write `<root>/.barrier-N`.
  3. Wait for its row.

  The worker pops paths in due order, so everything marked before the barrier has been handled by then. A path re-marked after a moved stamp can slip past the barrier, so `testHookProcessed` is used for that case.
- **Other hooks:**
  - `testHookBeforeRead` (per instance) stalls a reader;
  - `testHookBeforeRecord` stalls the worker;
  - `testHookStatfs` fakes free space;
  - `testHookAddWatch` injects ENOSPC;
  - `testHookPanic` panics in the worker;
  - `testHookRescan` counts rescans.

### 11.3 Writer matrix

Each writer must give exactly the expected rows, and no row for temp names.

| Writer emulated | Expected |
|---|---|
| sed -i: write `sedAbC123`, rename over | one `changed` row with the final content |
| nano, cp, tee: O_TRUNC, write half, pause 20 ms, write the rest | one row with the full content |
| install: unlink, create 0600, write, close, chmod 0644 | final row has mode 0644 |
| mv in from another temp dir on the same filesystem | `did not exist` + `created` |
| visudo: write `x.tmp`, rename | one row |
| dpkg: `x.dpkg-new` renamed over `x`; dir `d.dpkg-new` renamed to `d` | rows for `x` and `d/*` only |
| vim rename-save: `x` → `x~`, write a new `x` | one `changed` row, no `deleted`, no `x~` row |
| ln -sf: temp link renamed over | one `link` row |
| chmod -x | row with the same blob, `mode 0755->0644` |
| touch | no row |
| rm | one `deleted` row; a later observation adds nothing |
| `.x.swp`, `x.dpkg-old`, `x~` in a normal dir | never recorded |
| `x.bak` under a consumer include (test scope: `include <tmp>/cons/**` before `exclude **/*.bak`) | recorded |
| `x.dpkg-new` under that consumer, with a D0-style exclude in the test scope | never recorded |

### 11.4 Structural, proof-of-absence, restore, roots, overflow, degraded and crash tests

- **Structural:**
  - 50 × (mkdir a new dir, write a file into it at once): all 50 recorded, each with `did not exist` + `created`.
  - `mv dir dir2`: rows under `dir2`, and `deleted` rows under `dir`.
  - Files changed or removed while the watcher is stopped: `changed while not watching` and `deleted while not watching` at the next start.
  - A digest-rule file: a `digest` row and no object file.
  - Home roots from a fixture passwd: `.ssh/authorized_keys` recorded, `.ssh/id_x` not.
- **Proof of absence (6.4):**
  - A directory holds 20 unrecorded files. The worker is stalled by `testHookBeforeRecord` after the walk listed the directory. A sed-style rename-over runs on one file, and an `rm` plus recreate on another. After the worker is released: every file has one `first seen` row and no `did not exist` row.
  - A file created after the listing: `did not exist` + `created`.
  - After an overflow, a file created before the rescan finishes: `first seen (found by rescan)`, no `did not exist`.
- **Restore:**
  1. Record, edit, record.
  2. Run `store.Restore` in-process 20 times each for a file row, a link row and a `did not exist` row (re-creating the file between rounds).
  3. After each restore, wait for `testHookProcessed(path)`.
  4. Assert the two newest rows are `restore` and `pre-restore`, with no `auto` row after them.

  Also: 20 restores while the watcher records a 2,000-file baseline in another temp root. None fails with `database is locked`.
- **Roots:**
  - **Symlinked `.ssh` root:** the fixture home's `.ssh` is a symlink to `/`. `New` succeeds, the root is not watched, and it is logged once.
  - **Missing root:** a root that is missing at start is watched after it appears and a rescan runs.
  - **passwd change:** adding a fixture passwd line (under a watched root) with a new home makes its `.ssh` a root after the triggered rescan.
  - **Root moved:** renaming a root away and back gives IN_MOVE_SELF or IN_IGNORED, a rescan is requested, and later edits are recorded.
  - **Rate limit:** two rescan requests within `RescanMinGap` run one rescan (`testHookRescan`).
  - **Periodic tick:** with `RescanEvery` 200 ms, a write through a hard link from an unwatched temp dir (no event) is recorded `(found by rescan)`.
  - **Outside the roots:** a manual row for a file outside every root is never read by a rescan (`testHookProcessed` never fires) and gets no `deleted` row after the file is removed.
- **Overflow (real kernel, no sysctl change):**
  1. Stall the system reader.
  2. Alternate `chmod` on two files, as many times as `/proc/sys/fs/inotify/max_queued_events` plus 4,000. Two files are needed, because identical consecutive events coalesce.
  3. Write `late`.
  4. Release the reader.
  5. Expect the overflow line, and `late` recorded `(found by rescan)`.

  The drafting runs and a judge reproduced this as uid 1000: 16,385 events read, 1 overflow marker, no event for `late`. The test skips if the limit is over 100,000.
- **Instance isolation:** the same flood on a home root, with only the home reader stalled, gives an overflow line for the home instance only. A change under the system root made during the flood is recorded from its own event, without `(found by rescan)`.
- **Degraded:**
  - **Low space:** the statfs hook reports 50 MB free. Nothing is recorded and one line is logged. When the hook is restored, the change is recorded after `FloorBackoff`.
  - **Store error:** the objects dir is set to 0500 as a normal user. The error is logged, retried, and the change is recorded after the chmod back. Skipped as root.
  - **ENOSPC:** a hook makes `add_watch` fail for one directory. The error is logged once, and a change there is found by the next rescan.
  - **User files:** a user-owned file over `UserFileMax` gives a digest row, and a second change within `UserFileGap` is recorded only after the gap.
  - **Dirty-set bound:** more than `MaxDirty` marks clear the set and request one rescan.
- **Crash:**
  - `TestCrashMidRecord` (store, step 7), as prototyped in S13.
  - `TestKillDuringBaseline` (watch, step 13): a helper process runs a watcher over 2,000 files and is SIGKILLed after its first batch commits. A second watcher on the same SC_HOME then finishes the baseline. Checks: `integrity_check` is ok, every file has exactly one `first seen` row, and the lock file does not block the restart.

### 11.5 cmd/sc

- Kind-aware output and errors (step 9).
- `sc watch --root DIR` run through `runContext` in a goroutine:
  1. Write a file.
  2. Poll until `sc log` shows it.
  3. Cancel.
  4. Expect exit 0.
- Refusals give one line each.
- `TestUnitFile`, plus `bash -n` on the two scripts.

### 11.6 sc-m1 compatibility

- `make m1-compat` runs `scripts/build-sc-m1.sh bin/sc-m1`, then `SC_M1_BIN=$(CURDIR)/bin/sc-m1 go test -count=1 -run TestM1Compat ./internal/store`.
- `bin/` is gitignored.
- The build uses `git archive`, so the working tree, the index and HEAD never change (S17).

## 12. Acceptance script (`sudo ./scripts/accept-m2.sh`, or `make accept-m2`)

**What it touches.**

- **Store.** A throwaway `SC_HOME=$(mktemp -d /tmp/sc-accept.XXXXXX)` (0700). The real store and `/etc/systemd/system` are untouched.
  - The scratch store briefly holds copies of watched secrets such as `/etc/shadow` (0600 blobs in a 0700 dir).
  - It is deleted on exit.
- **Test files.**
  - `/etc/sc-accept.d/`, later moved to `/etc/sc-accept2.d/`;
  - one inert file, `/etc/modprobe.d/sc-accept-inert.txt`. modprobe reads only `*.conf`, so it has no effect.
- **Cleanup.** An EXIT trap cleans up whatever happens:
  - stops the unit;
  - removes the runtime unit and runs `daemon-reload`;
  - removes the test files and the listing file;
  - deletes SC_HOME.
- **Journal checks are scoped to this run.**
  - `jr` means `journalctl -q -o cat _SYSTEMD_INVOCATION_ID=$INV`, and `INV` is re-read after every start.
  - Whole-run checks use `journalctl -u scd-accept --since @$T0` (S18).
  - Stale lines from an earlier run or invocation cannot satisfy a check.
- **Output.** Each step prints `== N. …` and fails with `FAIL: …`.

**Steps.** `D=/etc/sc-accept.d`, `D2=/etc/sc-accept2.d`.

0. **Preconditions.**
   - Running as root.
   - **VM guard:** `systemd-detect-virt -q` must succeed (this VM says `oracle`, S18), unless `SC_ALLOW_REAL_HOST=1` is set.
   - **No real watcher:**
     - `systemctl is-active --quiet scd.service` must fail;
     - no process may have the argv `…/sc watch`. This is read from `/proc/*/cmdline`, because `pgrep -f` matches its own shell (S18).
   - `bin/sc` exists and `file bin/sc` says statically linked.
   - `python3` exists. Without it, steps 3 and 16 and the integrity checks are skipped with a note.
   - The test paths do not exist yet.
   - Record the sha256 of `/etc/fstab`, `/etc/hosts` and `/etc/machine-id`, and set `T0=$(date +%s)`.
1. **Scope against the real /etc (R20).**
   - `find /etc /boot/grub -xdev -printf '%y %p\n'` goes to a 0644 file under `/tmp`.
   - Run `sudo -u "$SUDO_USER" -H env SC_ETC_LISTING=<file> go test -count=1 -run TestDefaultScopeRealListing ./internal/scope`.
   - The step is skipped with a note if `SUDO_USER` is unset.
2. **Start.**
   - Generate `/run/systemd/system/scd-accept.service` from `scripts/scd.service`: ExecStart becomes `$PWD/bin/sc watch`, and `Environment=SC_HOME=…` is added.
   - Run `systemd-analyze verify`, `daemon-reload` and `systemctl start scd-accept`. Read `INV`.
   - Why a unit file in /run and not `systemd-run`: a stopped transient unit is garbage-collected and cannot be started again (S8), and step 15 needs to stop and start it.
3. **Kill during the startup rescan.**
   - Poll with python3, read-only, until `changes` has at least one row while `baseline:` is not yet in `jr`. Then `systemctl kill -s KILL scd-accept`.
   - Expect systemd to restart it within 20 s, with a new `INV`.
   - If the baseline finished before the kill, print a note. `TestCrashMidRecord` and `TestKillDuringBaseline` are the strict checks.
4. **Baseline.** Wait up to 5 min for `baseline:` in the current invocation, then check:
   - `SC_HOME` is 0700 and `changes.db` is 0600;
   - `PRAGMA integrity_check` says ok (python3, read-only), and no path has two `first seen` rows;
   - `sc log -n 0` has `/etc/fstab`, `/etc/sudoers`, `/etc/default/grub`, `/etc/pam.d/common-auth`, `/boot/grub/grub.cfg` and `/root/.ssh/authorized_keys`;
   - it has `/etc/systemd/system/display-manager.service` with SIZE `link`, and `/etc/ssh/ssh_host_ed25519_key` and `/etc/machine-id` with SIZE `digest`;
   - nothing is recorded under `/etc/ssl/certs/`, `/etc/alternatives/`, `/boot/grub/grubenv` or `/etc/ld.so.cache`.

   The script prints how long the baseline took.
5. **Refusals.** None of these writes anything: the row count is the same before and after.
   - A second `bin/sc watch` on the same SC_HOME exits 1 with `already running`.
   - `SC_HOME=/etc/sc-accept.d/home bin/sc watch` exits 1, and stderr contains `inside watched root /etc`. `/etc/sc-accept.d` still does not exist.
   - `sc cat`, `sc diff` and `sc restore` of the `ssh_host_ed25519_key` row each exit 1 with one line.
   - `sc restore` of the `/etc/machine-id` row exits 1 with `fingerprint-only`, and the file's sha256 is unchanged.
   - `sc restore` of the `$SUDO_USER` `authorized_keys` row exits 1 with one line (another user's directory). This is skipped if `SUDO_USER` is unset.
6. **New directory.** `mkdir $D && printf 'one\n' > $D/a.conf` gives `did not exist` + `created` within 3 s. `A_ID` is the `created` row (content `one`, mode 0644).
7. **sed -i.** `sed -i s/one/two/ $D/a.conf` gives exactly one `changed` row with content `two`. No row has a path matching `sed??????`.
8. **Slow in-place write.** `{ echo p1; sleep 0.2; echo p2; } > $D/a.conf` gives exactly one new row, and `sc cat` shows both lines.
9. **Remove and recreate.** `rm $D/a.conf; sleep 0.1; echo three > $D/a.conf` gives one `changed` row and no `deleted` row.
10. **Metadata.** `chmod 600 $D/a.conf` gives a `mode 0644->0600` row. `touch $D/a.conf` gives no row after 2 s.
11. **Symlinks.**
    - `ln -s /dev/null $D/x.service` gives `did not exist` + `created` with SIZE `link`. `X_DNE` is the `did not exist` row.
    - `ln -sfn /etc/hosts $D/x.service` gives a second `link` row.
12. **Restore across processes, 5 times.** Each round runs `sc restore $A_ID`. After 2 s:
    - the two newest rows for `a.conf` are `restore` and `pre-restore`, with no `auto` row after them;
    - the file holds `one` with mode 0644.
13. **Undo a creation.**
    - `sc restore $X_DNE` prints `removed …`, and `x.service` is gone.
    - The newest row is `restore`, and 2 s later no `auto` row has been added.
    - A second `sc restore $X_DNE` prints `nothing to do` and adds no row.
14. **Delete and move.**
    - `printf 'b\n' > $D/b.conf; printf 'c\n' > $D/c.conf; mkdir $D/sub; printf 's\n' > $D/sub/s.conf`: each file gets `did not exist` + `created`.
    - `rm $D/a.conf` gives a `deleted` row.
    - `mv $D $D2`: within 15 s, `$D2/b.conf`, `$D2/c.conf` and `$D2/sub/s.conf` each get `did not exist` + `created`, and the three old paths each get one `deleted` row. `a.conf` and `x.service` get no new row, because they were already deleted.
15. **While stopped.**
    1. `systemctl stop scd-accept`.
    2. `echo b2 > $D2/b.conf; rm $D2/c.conf`.
    3. `systemctl start scd-accept`, then read the new `INV` and wait for `baseline:` in it.

    Expect `changed while not watching` for `b.conf` and `deleted while not watching` for `c.conf`. This is the third start, within `StartLimitBurst`'s 5.
16. **Overflow.**
    1. `kill -STOP` the unit's MainPID.
    2. python3 alternates `os.chmod` on `$D2/b.conf` and `$D2/sub/s.conf`, 20,000 times.
    3. `printf 'late\n' > $D2/late.conf`.
    4. `kill -CONT`.

    Expect an overflow line in `jr`, and `late.conf` recorded within 15 s with `(found by rescan)`.
17. **Loudness and no content.** Create `/etc/modprobe.d/sc-accept-inert.txt` containing `SC-SECRET-$$`, then check:
    - it appears in `jr -p warning` (tier 1);
    - no `/etc/sc-accept2.d` line appears in `jr -p notice` (tier 4 lines are info);
    - no line of `journalctl -u scd-accept --since @$T0` contains `SC-SECRET-$$`.
18. **Nothing excluded was recorded, nothing was written, nothing is ordered after scd.**
    - `sc log -n 0` has no `.sc-tmp-`, `.swp`, `~` or `sed??????` paths.
    - The sha256 of `/etc/fstab`, `/etc/hosts` and `/etc/machine-id` is unchanged, because scd never writes a watched file.
    - No unit file under `/etc/systemd`, `/run/systemd/system` or `/usr/lib/systemd` has an `After=`, `Requires=`, `Requisite=`, `BindsTo=` or `PartOf=` line naming `scd.service` or `scd-accept.service`.
19. **`PASS`.** The trap cleans up.

After that, still part of acceptance:

- `sudo ./scripts/smoke.sh` (M1) PASS;
- `make m1-compat` PASS as your user;
- `go test ./...` and `sudo -E go test ./...` PASS.

Filling the disk and exhausting inotify watches are covered by unit tests only.

## 13. Sign-off runs and "M2 done" (NEXT_STEPS 2.4)

These come after step 16 and before step 17. Each gets a WORKLOG line. Items marked G2 need a fresh VirtualBox snapshot, an announcement and your OK.

- **S1. Checks and acceptance.**
  - Every step is committed, and every chunk review is closed.
  - The checks are clean as user and as root (plus `make race` if approved), and `file bin/sc` says statically linked.
  - Section 12 PASS: `accept-m2.sh`, `smoke.sh` and `make m1-compat`.
- **S2. The real store (gate G1, your yes).**
  1. Backups in `/var/backups/smartconfig` (NEXT_STEPS section 1).
  2. A rehearsal on a copy: `sudo cp -a /var/lib/smartconfig <tmp>`, then `SC_HOME=<tmp> sc log`. The 4 rows keep their ids, and `changes.db.m1-backup` exists.
  3. Only then install the binary and the unit as in section 9, and run `systemctl enable --now scd`. This migrates the real store.
- **S3. The owner scenario (G2), by hand.**
  - Edit `/etc/ssh/sshd_config` with nano and never type `sc`: `sc log` shows the change and the old version.
  - `chmod -x /etc/grub.d/41_custom` shows as a mode-only row, then put it back. (Not `10_linux`: see NEXT_STEPS.)
  - `systemctl mask` of a harmless unit shows as a link row, then unmask.
  - An edit made while scd was stopped appears once it starts.
  - A restore shows as a restore, with no extra automatic row.
- **S4. Reboot test (G2).**
  - With `scd.service` enabled for real, reboot the VM.
  - `systemd-analyze critical-chain` and `blame` show nothing waiting on scd, and scd is running.
- **S5. Failure paths (G2).**
  - `kill -9` scd during its startup rescan, and during an apt burst (the deliberate apt upgrade of S6).
  - The unit restarts it, the missed change is recorded exactly once, and `PRAGMA integrity_check` is ok.
  - The static check of acceptance step 18 is run again for `scd.service`.
- **S6. 24-hour soak (G2).**
  - It includes one `apt-daily-upgrade` run, plus a deliberate `apt upgrade` of the 13 pending updates while scd watches. apparmor alone has 259 conffiles under `/etc/apparmor.d` (S14).
  - Pass means no crash, no flood, no unexplained rows, and bounded memory and CPU.
  - The `/etc` manifest diff lists only expected changes.
- **S7. Close-out.**
  - A final review of the whole milestone leaves no open high-severity finding.
  - A fresh clone from GitHub builds and tests clean.
  - Step 17 (docs, tag `m2`).
  - You take the snapshot `m2-accepted` and sign off.

## 14. Risks

- **First-start cost.** About 1,380 baseline rows on this VM (7.4).
  - The drafting run measured 12.9 ms per row with 50-row transactions and 51 ms per row with one row per transaction. The blob fsync dominates.
  - So the baseline takes about 20 s, once. This was not re-run for this plan.
  - A cold-cache walk took 8.9 s, a warm one 0.16 s.
  - With Type=exec, boot never waits for any of this. Later starts only hash files and write nothing.
- **apt upgrades.** An upgrade touches about 110 /etc entries (research).
  - D0 keeps dpkg's staging names out where the consumer skips them. `initramfs-tools` and `logrotate.d` still record them, because their consumers run or read them.
  - A very large burst can overflow the system queue. That costs a rescan and the intermediate versions, not the final state.
  - Until question 6 is settled, apt's rows show as `auto`.
- **`first seen` instead of `did not exist`.** A real creation made during the baseline or just after an overflow is labelled `first seen`, so a restore cannot undo it. This is deliberate: a false `did not exist` would let a restore delete a file that always existed.
- **A hot journal needs a writable open.** A crash leaves a hot journal (S13), and the next writable open rolls it back. M4's read-only rescue path must handle a store in that state. That is M4 work.
- **Orphan blobs.** When a race makes scd skip a row after writing its blob, the object stays unreferenced. There is no pruning yet.
- **Rowid order.** It relies on never running VACUUM (5.3).
- **User-controlled `.ssh` dirs.**
  - They have their own inotify instance, so a flood there cannot overflow `/etc`'s queue. It can still force home-root rescans, at most one per `RescanMinGap`.
  - Watches there are flat and filtered to two names.
  - User files have a size cap and a minimum gap between rows.
  - A symlinked `.ssh` is never watched.
  - File reads go through the parent directory fd with O_NOFOLLOW, and restores there are refused.
  - One gap remains: symlink targets are still read by path (`readlink`). A `.ssh` swapped mid-read could make root record a link target string from somewhere else into its own 0700 store. dirfd-based link reads come in M4.
- **Fatal runtime errors.** A stack overflow still prints a full Go dump to the journal, even with `GOTRACEBACK=none` (S16). A hand-run `sc watch` without that variable prints Go's normal traceback on any fatal runtime error.
- **Disk.** The store shares `/` (about 29 GiB available now, S11). The floor keeps free space above 256 MiB for non-root users, so root's reserved blocks are never touched. There is no retention yet.
- **Glob mistakes could hide a tier 1-2 file.** This is covered by the fixture test, the real-listing check at acceptance, and acceptance step 4.
- **Behaviour changes from M1.** Tests are updated in the same commits.
  - `sc snapshot` of a symlink now succeeds.
  - Manual dedup includes mode, owner and kind.
  - `sc log` is ordered by insertion.
  - A restore can replace a symlink with a file and the reverse, and can delete a file (question 3).
  - Restores are refused into non-root-owned or symlinked directories (question 4).
  - `sc cat`, `diff` and `restore` refuse fingerprint-only paths.
- **Watch budget.** About 325 of the 65,536 watches per uid. Root already uses about 180 (research).
- **journald rate limit.** The defaults apply here: 10,000 lines per 30 s (S11). An extreme burst could drop per-row lines. The rows are still recorded.
- **Account files.** They are recorded one file at a time. A `useradd` gives several separate rows, and a single-file restore can desync the set, exactly as in M1 today (question 8).
- **Deferred gaps.**
  - A brand-new `~/.ssh` is seen only at the next periodic rescan.
  - `chattr +i`, ACLs and directory modes are not recorded (question 10).
- **logrotate.** It can read `.<name>.sc-tmp-*` during a restore into `/etc/logrotate.d`. This was known and left as is in the M1 research.

## 15. Deferred (with milestone)

| Item | Milestone |
|---|---|
| apt/dpkg change sets (dpkg lock window), origin apt | M3 (question 6) |
| apt Pre/Post-Invoke hook file | M5 (it must be installed by the .deb) |
| Account-set grouping (passwd, shadow, group, gshadow, subuid, subgid), lock-aware debounce, set restore with pwck/grpck | M3 (question 8) |
| PAM common-* grouping and the /var/lib/pam state check | M3 |
| Applied markers (grub.cfg, initrd, ld.so.cache), "pending until update-grub/update-initramfs" | M3 (question 5) |
| Initramfs-source tagging and "also run update-initramfs" hints | M3 |
| Active/inert labels per consumer dir, rename activation and deactivation alerts, exec-bit-lost alerts | M3 |
| Vendor-override detection (same name in /usr/lib) | M3 |
| Existence-flag alerts beyond tier priority (ld.so.preload, nologin, sshd_not_to_be_run, rc.local) | M3 |
| Inode flags (chattr +i/+a), ACLs and xattrs in the dedup key; directory metadata rows | M3 (question 10) |
| +i/+a precheck before a restore | M4 |
| Secret redaction in `sc diff` and `sc cat` (shadow hashes, psk=, password=, 90-installer-network.cfg) | M3 |
| /etc/alternatives chain resolution, rc?.d links of non-native scripts | M3 |
| `sc scope PATH` (explain why a path is or is not watched) | M3, with the file graph |
| Validators (sshd -t, visudo -c, …) | M3 |
| Extra roots (/usr/local/lib/systemd and friends, /var/spool/cron/crontabs, /var/lib/polkit-1/localauthority, /usr/local/etc, debconf grub-pc/install_devices) | M3 at the earliest; decision 5 limits M2 |
| `boot_id` column | M4 (NEXT_STEPS question 3) |
| `sc status` (watch health, ENOSPC subtrees), mount tracking (separate /boot, ESP/vfat), read-only-root reads including a hot journal left by a crash | M4 |
| dirfd or uid-drop restore into user-owned dirs; dirfd link reads | M4 |
| User-editable scope file (/etc/smartconfig/scope as a conffile); unit in /usr/lib/systemd/system, binary in /usr/sbin | M5 |
| Retention and pruning of old automatic rows and blobs; an explicit sequence column if rows are ever deleted | not in any milestone yet; needs its own decision |
| Home-dir watch, so a brand-new ~/.ssh is seen before the periodic rescan | later, only if wanted |
| fanotify | not planned |

## 16. Questions for you

Each question has a recommendation, and the plan above assumes it. The same list is in the `questions` field. The `boot_id` question is already NEXT_STEPS question 3; this plan follows its recommendation (M4).

1. **Name.** MILESTONES says `scd`, while CLAUDE.md says one binary. The plan uses the subcommand `sc watch`, run by `scd.service` with `SyslogIdentifier=scd`. *Recommendation: yes.*
2. **Tiers in M2.** MILESTONES puts tiers in M3. In M2 a tier only sets the journald priority of scd's line: tiers 1-2 warning, 3 notice, 4 info. *Recommendation: yes.*
3. **Restoring a deletion.** Restoring a `deleted` or `did not exist` row removes the current file, after a pre-restore row so the restore can be undone. This is how a `systemctl mask` is undone. `did not exist` rows are written only with proof of absence, and fingerprint-only paths are never touched. *Recommendation: yes.*
4. **Restores into non-root or symlinked directories** are refused, with a pointer to `sc cat`. That covers users' authorized_keys, and on this VM also /etc/colord and /etc/gnome-remote-desktop. *Recommendation: refuse in M2; a safe dirfd or uid-drop restore comes in M4.*
5. **grub.cfg and applied markers** (research decisions 6 and 10). *Recommendation: record grub.cfg content as an ordinary tier-1 row (about 10 KB per version); applied markers in M3.*
6. **apt grouping** (research decision 8). *Recommendation: nothing in M2, so apt's edits are recorded as `auto`. The lock-window origin comes in M3 and the hook file with the M5 .deb.*
7. **snapd runtime units and udev rules** (research decision 11). *Recommendation: exclude them with the narrowed patterns, so the dpkg-owned `snapd.*` links stay recorded.*
8. **Account files** (research decision 12). *Recommendation: record each file on its own in M2, as today; grouping and set restore come in M3.*
9. **Disk budget** (research decision 15). *Recommendation:*
   - *a 256 MiB free-space floor;*
   - *for user-owned files under homes, content kept only up to 64 KiB, and at most one row per 60 s;*
   - *no retention in M2.*
10. **Flags, ACLs and directory modes.** `chattr +i`, `setfacl` and a world-writable `sudoers.d` would not be recorded. *Recommendation: defer to M3 and list it as a known gap.*
11. **Scope file.** *Recommendation: rules built into the binary only; a user-editable file comes with M5.*
12. **CLAUDE.md Layout.** *Recommendation: add lines for `internal/scope` and `internal/watch` in the docs commit (step 16).*
13. **Acceptance side effects.** *Recommendation: yes.* In the VM, the script:
    - writes test files under /etc/sc-accept.d (moved to /etc/sc-accept2.d) and one inert /etc/modprobe.d/*.txt file;
    - installs a runtime unit in /run/systemd/system and runs daemon-reload;
    - pauses its own test daemon with SIGSTOP and kills it once with SIGKILL;
    - refuses to run outside a VM (unless `SC_ALLOW_REAL_HOST=1`) or while a real scd runs.

    An EXIT trap undoes all of it.
14. **Sign-off runs on the real system** (section 13, S2-S6). These are: migrating the real store after backups, enabling scd.service, the owner scenario on real /etc files, a reboot, `kill -9` during an apt upgrade, and a 24-hour soak including the 13 pending updates. *Recommendation: yes, each after a fresh snapshot and an announcement; Claude asks again right before the real store is migrated.*

---

## Appendix A. Evidence

Everything ran in `scratchpad/m2plan/synth/` and `scratchpad/m2plan/final/`. /etc and the store were only read. Per NEXT_STEPS, the evidence quotes paths, counts, modes and hashes only, never file contents from /etc.

**S1: the Go 1.22.2 standard library has everything needed.**
```
$ go doc syscall.InotifyInit1 / InotifyAddWatch / InotifyRmWatch / Openat / Flock / Statfs
func InotifyInit1(flags int) (fd int, err error)
func InotifyAddWatch(fd int, pathname string, mask uint32) (watchdesc int, err error)
func InotifyRmWatch(fd int, watchdesc uint32) (success int, err error)
func Openat(dirfd int, path string, flags int, mode uint32) (fd int, err error)
func Flock(fd int, how int) (err error)
func Statfs(path string, buf *Statfs_t) (err error)
zerrors_linux_amd64.go: IN_ATTRIB 0x4 IN_CLOEXEC 0x80000 IN_CLOSE_WRITE 0x8 IN_CREATE 0x100
  IN_DELETE 0x200 IN_DELETE_SELF 0x400 IN_DONT_FOLLOW 0x2000000 IN_IGNORED 0x8000
  IN_ISDIR 0x40000000 IN_MODIFY 0x2 IN_MOVED_FROM 0x40 IN_MOVED_TO 0x80
  IN_MOVE_SELF 0x800 IN_NONBLOCK 0x800 IN_ONLYDIR 0x1000000 IN_Q_OVERFLOW 0x4000
$ go doc encoding/binary.NativeEndian -> var NativeEndian nativeEndian
```

**S2: path.Match in Go 1.22.**
```
"snap[-.]*" "zzz"          false syntax error in pattern   (reported even without a match)
"[!x]*"     "abc"          false <nil>
"[^x]*"     "abc"          true  <nil>
"*~"        ".foo~"        true  <nil>
"*"         ".hidden"      true  <nil>
"snap-*.mount" "snap-firmware\x2dupdater-258.mount" true <nil>
```

**S3: JOURNAL_STREAM** (`man systemd.exec`): "…contains the device and inode numbers of the connection file descriptor… The device and inode numbers of the file descriptors should be compared with the values set in the environment variable… [it is not] sufficient to only check whether $JOURNAL_STREAM is set".

**S4: flock conflicts inside one process.**
```
first  flock: <nil>
second flock (same process, new open): resource temporarily unavailable
second flock after first closed: <nil>
```

**S5: the migration on a copy of the real store.** The copy was made with `sudo cp`; the original was only read. (This run also added a `boot_id` column, since dropped from the plan.)
```
before: PRAGMA user_version -> 0; rows 1..4 = 2c6901 8b8728 a8d6f5 fb5cf4, all ts 1790497592
ALTER TABLE + CREATE INDEX changes_path_seq + user_version=1 in BEGIN IMMEDIATE:
PRAGMA integrity_check -> ok ; PRAGMA user_version -> 1 ; kinds all 'file'
EXPLAIN QUERY PLAN newest row -> SEARCH changes USING INDEX changes_path_seq (path=?)
BEGIN; PRAGMA user_version=7; ROLLBACK; PRAGMA user_version -> 1
/var/lib/smartconfig: -rw-r--r-- root root changes.db (0644 today)
```

**S6: an M1 binary on a v1 store holding link, digest and deleted rows.** Built from `2784d36` in a scratch copy.
```
sc-m1 restore <link row>    -> sc: invalid blob name ""          rc=1
sc-m1 cat/diff <link row>   -> sc: invalid blob name ""          rc=1
sc-m1 restore <digest row>  -> sc: read blob 2d711642b726: open …: no such file or directory  rc=1
sc-m1 restore <deleted row> -> sc: invalid blob name ""          rc=1
sc-m1 log                   -> lists all rows, rc=0
sc-m1 snapshot f.txt        -> snapshot bc78ce … ; row 5 kind 'file', target ''
```

**S7: the scope prototype against the real listing.** `sudo find /etc /boot/grub /root/.ssh /home/vboxuser/.ssh -xdev -printf '%y %p\n'` gave 395 d, 1963 f, 791 l.
```
watched dirs 325, recorded files 1145, links 234
tier 1: 342  tier 2: 121  tier 3: 66  tier 4: 850
digest: /etc/machine-id /etc/ssh/ssh_host_{ecdsa,ed25519,rsa}_key
kept under /boot: /boot/grub/grub.cfg ; kept under .ssh: the two authorized_keys
excluded but matching a tier 1-2 rule: 60 (59 snapd runtime names + /etc/security/opasswd)
/etc/grub.d/10_linux.bak rec=true tier=1 ; /etc/sudoers.d/sedAbC123 rec=true tier=2
/etc/sudoers.d/.README.swp rec=false ; /etc/apt/apt.conf.d/4913 rec=false
/etc/tmpfiles.d/x.conf rec=true tier=1 ; /etc/tmpxdvph4n_/resolv.conf rec=false
/etc/dconf/db/ibus.d/00-upstream-settings rec=true ; /etc/dconf/db/ibus rec=false
…/multi-user.target.wants/snapd.apparmor.service rec=true ; …/snap-firefox.mount rec=false
/etc/ld.so.cache rec=false ; /boot/grub/grubenv rec=false ; /root/.ssh/id_ed25519 rec=false
```

**S8: systemd.**
```
systemd-analyze verify (plan's unit, ExecStart -> existing binary)  -> rc=0
systemd-analyze verify (ExecStart=/usr/local/sbin/sc)  -> "Command /usr/local/sbin/sc is not executable"
systemctl show -p After multi-user.target -> 62 units, 40 of them services
man systemd.service: "exec … will consider the unit started immediately after the main
  service binary has been executed"
systemd-run --user --unit=sc-synth-gc … ; stop ; start ->
  "Failed to start sc-synth-gc.service: Unit sc-synth-gc.service not found." rc=5
/sys/block/sda/queue/scheduler -> none [mq-deadline]
systemctl list-unit-files 'scd*' -> 0 lines
```

**S9: directories in the roots not owned by root.**
```
sudo find /etc /boot/grub -xdev -type d ! -user root
colord 755 /etc/colord ; gnome-remote-desktop 755 /etc/gnome-remote-desktop
```

**S10: login shells (counts and paths only).**
```
accounts whose shell is not nologin or false: 3
  after the plan's filter (also drops sync, halt, shutdown): 2, homes /root and /home/vboxuser
  the third is the sync account, dropped by its shell
/home/vboxuser/.ssh 0700 vboxuser, authorized_keys 0600 0 B ; /root/.ssh/authorized_keys 0600 root 0 B
```

**S11: disk, journald and inotify.**
```
df -T /tmp -> /dev/sda2 ext4, 30633884 1K-blocks available ; stat -f /var/lib -> bsize 4096 bavail 7658471
journald: no rate-limit setting is active in journald.conf and there is no journald.conf.d,
  so the documented defaults apply (10,000 messages per 30 s)
/proc/sys/fs/inotify: max_queued_events 16384, max_user_watches 65536, max_user_instances 128
ls -la /etc/ld.so.cache -> 58003 B
```

**S12: SC_HOME resolution and symlinked roots** (prototype in `final/res`; `<t>` is its temp tree).
```
EvalSymlinks(missing): lstat /var/lib/definitely-missing-sc: no such file or directory
resolve(/var/lib/definitely-missing-sc) = "/var/lib/definitely-missing-sc", <nil>
resolve(<t>/var/lib/goodlink/sub/home) = "<t>/etc/sub/home", <nil>     (goodlink -> ../../etc)
resolve(<t>/var/lib/dangling/sub/home) = "", lstat <t>/var/etc: no such file or directory
resolve(/etc/sc-accept.d/home) = "/etc/sc-accept.d/home", <nil>        (below root /etc: refused)
root <t>/home/u/.ssh usable: <t>/home/u/.ssh is a symlink               (ln -s / .ssh: skipped)
root /etc usable: <nil>
```

**S13: kill -9 mid-transaction** (helper-process test in a scratch copy of the repo, modernc.org/sqlite v1.29.10, the store's own DSN).
```
helper: 10 committed rows, then BEGIN IMMEDIATE, cache_size=10, 3000 uncommitted rows, SIGKILL
db file size while the transaction is open: 12402688
after kill -9: journal exists=true size=17920
after reopen: rows=10 integrity=ok journal_mode=delete journal exists=false
--- PASS: TestKill9MidTransaction (0.24s)
without a cache spill (500 small rows): a 16928 B journal is left, not hot; rows=10, integrity ok
```

**S14: which names each block D consumer skips.**
```
/usr/share/grub/grub-mkconfig_lib:212-225 grub_file_is_not_garbage: *.dpkg-*, *.rpmsave|*.rpmnew,
  README*, *.sig are garbage; grub-mkconfig:295-309 also skips *~ and */#*#, runs only test -x files
apparmor_parser 4.0.1 -N -Q on a scratch dir (p1 plus 14 suffixed copies and .hidden):
  loaded:            p1, .dpkg-tmp, .dpkg-backup, .ucf-new, .ucf-old, .ucf-dist, .bak
  skipped silently:  .hidden, .dpkg-new, .dpkg-old, .dpkg-dist, .dpkg-bak, .dpkg-remove
  "Ignoring: …":     ~, .orig, .rej
run-parts --test on good sedAbC123 x.dpkg-new x.dpkg-old x.dpkg-tmp x.ucf-new x~ x.bak
  -> runs good and sedAbC123 only
apt-config dump: Dir::Ignore-Files-Silently ~$ \.disabled$ \.bak$ \.dpkg-[a-z]+$ \.ucf-[a-z]+$
  \.save$ \.orig$ \.distUpgrade$
sudoers(5): "skipping file names that end in '~' or contain a '.' character"
cron(8): cron.d names "must conform to the same naming convention as used by run-parts";
  "any file containing dots will be ignored" (.dpkg-dist, .dpkg-old, .dpkg-new)
dpkg(1): "During unpack, dpkg extracts new filesystem objects into pathname.dpkg-new";
  conffiles "are processed at a later stage"
dpkg conffiles per dir: apparmor.d 259, logrotate.d 17, apt/apt.conf.d 15, grub.d 13,
  cron.daily 8, kernel 7, cron.d 4, cron.weekly 3, initramfs-tools 2, cron.monthly 2,
  sudoers.d 1, dconf/db 1, cron.hourly 1
```

**S15: the scope prototype re-run with block D0.**
```
watched dirs 325, recorded files 1145, links 234 ; tiers 342/121/66/850 ; allowlist 60 (unchanged)
/etc/apparmor.d/usr.bin.foo.dpkg-new rec=false (D0) ; /etc/apparmor.d/p.dpkg-tmp rec=true
/etc/apparmor.d/abstractions/base.dpkg-dist rec=false ; /etc/apparmor.d/p~ rec=false
/etc/grub.d/10_linux.dpkg-new rec=false ; /etc/grub.d/10_linux.bak rec=true
/etc/initramfs-tools/hooks/foo.dpkg-new rec=true ; /etc/logrotate.d/rsyslog.dpkg-new rec=true
/etc/sudoers.d/x.dpkg-new rec=false ; /etc/sudoers.d/XXab12cd rec=true ; /etc/sudoers.d/README rec=true
/etc/apt/apt.conf.d/20auto-upgrades.ucf-dist rec=false ; /etc/cron.daily/logrotate.dpkg-new rec=false
/var/log/syslog rec=false (outside every root)
```

**S16: Go fatal-error output** (static test binary, Go 1.22.2; line counts of stderr, all rc=2).
```
                               concurrent map writes   stack overflow   goroutine panic
default                        69 lines                266 lines        7 lines
debug.SetTraceback("none")     73 lines (no effect)    266 lines        7 lines
GOTRACEBACK=none (env)         1 line                  266 lines        1 line
```

**S17: building sc-m1 without touching the checkout.**
```
git archive ec15ece | tar -x -C final/m1src
GOPROXY=off GOFLAGS=-mod=readonly CGO_ENABLED=0 go build -trimpath ./cmd/sc   real 0m0.994s
file sc-m1 -> statically linked
git status --short (checkout) -> 0 lines ; git tag -l -> 0 tags (m1 not tagged yet: fall back to ec15ece)
```

**S18: acceptance helpers.**
```
systemd-detect-virt -> oracle (rc 0)
systemctl is-active scd.service -> inactive (rc 4)
pgrep -af 'sc watch' -> matched only the shell running pgrep itself
journalctl _SYSTEMD_INVOCATION_ID=<systemd-journald's InvocationID> -> that invocation's lines only
journalctl -u systemd-journald --since "@<epoch>" -> accepted (systemd 255)
```

**Evidence from the drafting and judging runs (not re-run here).**
- Real overflow as uid 1000, with no sysctl change: "overflow event wd=-1 mask=0x4000 at position 16385 … 0 events for 'late'". Reproduced by two drafts and one judge.
- `Close` unblocked `Read` after 13.8 ms.
- add_watch on a renamed dir returned the same wd.
- add_watch with DONT_FOLLOW\|ONLYDIR on a symlink to a dir: "not a directory".
- Write costs:
  - M1 snapshot path: 134 ms per row;
  - one row per transaction: 51.1 ms per row;
  - 50 rows per transaction: 12.9 ms per row;
  - 100 rows in one transaction: 48 ms.
- Walk: cold 8.87 s, warm 163 ms.
- Four concurrent first opens all succeeded.
- journald parses `<N>` prefixes.
- apt-get and dpkg take `F_SETLK` write locks on `/var/lib/dpkg/lock-frontend` and `lock` (relevant to question 6).
- An M1 restore of a link row with a 64-hex blob writes a 0777 regular file holding the target text (a judge).

**Code and document references.**
- `internal/store/store.go`:
  - 34-48: the M1 schema;
  - 141-149: manual-only blob dedup;
  - 173: `makeID`;
  - 290: `ORDER BY ts DESC, rowid DESC`;
  - 336-381: Restore (345 is the pre-restore through `Snapshot`; the lock order is at 358-373).
- `internal/store/store_test.go`: 80 (`TestInitMode`), 139 (`TestSnapshotRefuses`), 358 (`TestRestoreHoldsLockAcrossRename`), 411 (`TestRestoreWriteFailureRecordsNothing`).
- `internal/fsutil/fsutil.go`: 25-35 (`CheckRegular` refuses symlinks), 84-86 (`WriteAtomic`, temp `.<base>.sc-tmp-*`).
- `internal/fsutil/open_linux.go`: `O_NOFOLLOW|O_NONBLOCK`.
- `cmd/sc/main.go`: 21-38 (`run` with recover).
- `cmd/sc/restore.go`: 34-35 (M1 restore output line).
- `cmd/sc/main_test.go`: 109-129 (`TestCLIErrorsAreOneLine`, symlink case at 113-116).
- `scripts/smoke.sh`: 56 (ORIGIN is field 4).
- `docs/M2_WATCHLIST.md`: 167 (machine-id never restored), 334 (missing machine-id triggers first-boot presets).
- `docs/NEXT_STEPS.md`: 67-89 (plan checks), 111-114 (chunks), 165-198 (M2 done), 275 and 311 (boot_id to M4), 330-331 (ask before migrating the real store).

## Appendix B. How this plan was put together

**Base.** Draft 1 (lean) had the best combined judge score: 37 + 41 = 78, against 75 for draft 2 (traps) and 60 for draft 3 (admin).

**Ideas taken from the other drafts:**
- **From draft 2:**
  - `/etc/ld.so.cache` excluded;
  - step 0 publishes the plan;
  - commit hashes in WORKLOG;
  - the single-instance flock;
  - the widened consumer include block;
  - the sentinel barrier;
  - `TestUnitFile`;
  - newer schemas refused, and a backup before migrating;
  - `StartLimit*` in the unit;
  - `/boot/grub` limited to grub.cfg and custom.cfg;
  - the dirty-set bound.
- **From draft 3:**
  - `IOSchedulingClass=best-effort` with priority 7;
  - `did not exist` rows and restoring them;
  - the `JOURNAL_STREAM` dev:ino check;
  - the marker-string test.
- **From both:** batches of up to 50 rows instead of the draft's 1-3 minute baseline.

**Errors fixed after the judges:**
- Link rows had a real 64-hex blob, which an M1 binary restores as a regular file. They now use an empty blob plus a `target` column (S6).
- "Nothing is ordered after scd" was wrong. It is now stated correctly, with Type=exec (S8).
- `JOURNAL_STREAM` presence was replaced by the dev:ino comparison (S3).
- Goroutine panics are recovered.
- The CLI step came before the scope step. The order is fixed.
- There was no single-instance lock. flock is added.
- The rowid-and-VACUUM caveat is now stated.
- Retention was assigned to M5. It is now "not in any milestone yet".
- The open research decisions are now questions 5-10.
- The non-root-directory restore rule also hits two /etc directories (S9). Question 4 now says so.

**Revision after the critic** (one blocker, six major, eight minor issues; all applied except the notes below):

- **Blocker: start sequence.** It now works on a fresh machine and against a symlinked `~/.ssh`.
  - SC_HOME is resolved through its longest existing ancestor.
  - Symlinked roots are skipped.
  - The order is check, then `MkdirAll`, then flock, then Init (6.2, S12).
  - Tests cover a missing SC_HOME and a symlinked root, and acceptance checks the exact refusal message.
- **False `did not exist` rows.** A proof-of-absence rule (6.4) and its tests were added.
- **Fingerprint leak through restore.** The store owns the fingerprint rule (5.4).
  - Restores of fingerprint-only paths are refused whatever the row kind, before anything is written (5.6).
  - `cat` and `diff` refuse them too.
  - Tests are in step 8.
- **`boot_id`.** Removed from migration 1. M4 per NEXT_STEPS question 3.
- **Acceptance state.** Section 12 was re-traced: files are created before the delete, move, stop and overflow steps; step 12 restores the `created` row; journal checks are scoped by invocation ID and `--since`.
- **NEXT_STEPS checks.**
  - Crash tests: steps 7 and 13, acceptance step 3, sign-off S5. Rollback journal stated (5.1).
  - VM guard: acceptance step 0.
  - Definition of done aligned with NEXT_STEPS 2.4 (section 13).
  - sc-m1 compatibility automated (step 10).
  - Chunks A-E (10.3).
  - Real-store gate G1.
- **Block D noise.** Block D0 added from verified consumer skip rules (S14). It is wider than the critic's minimum: it also covers apparmor's `.dpkg-bak`, `.dpkg-remove`, `~`, `.orig` and `.rej`, and dpkg/ucf staging names in sudoers.d, apt.conf.d, kernel and cron.*.
- **Minor issues.**
  - The CLI test change moved to step 7.
  - The restore lock order is stated for all three write kinds, refusals come first, and output lines are defined.
  - Every duration and limit is a Config field, with tests for each rescan trigger.
  - `Recorded` requires a root.
  - A second inotify instance for home roots.
  - `GOTRACEBACK=none` in the unit, with goal 7 reworded.
  - Appendix A reduced to counts and paths (S10, S11).
  - Acceptance refuses to run next to a real scd.

## Critic notes not applied

- **`debug.SetTraceback("none")` in `sc watch`:** not used. It cannot lower the traceback level below Go's default (S16). The unit sets `Environment=GOTRACEBACK=none` instead, which the critic offered as the alternative.
- **"No stack traces reach the user" for every fatal error:** not fully achievable. A Go stack overflow dumps in full even with `GOTRACEBACK=none` (S16), so goal 7 and section 14 state this as a known limit instead.