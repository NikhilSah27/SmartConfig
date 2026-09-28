# Review of the round-2 fixes (R2-1 to R2-5)

Date: 2026-09-27. Same method as round 1: three reviewers (transactions, files, regressions) ran the code and tried to break it; two skeptics re-checked every finding. 15 findings, all confirmed; many are the same issue seen through different lenses, so they are grouped below.

Raw results with full evidence: `~/smartconfig-work/review-r2-results.json` on the VM.

## Issues and what we do

| Id | Issue | Seen by | Severity | What we do |
|---|---|---|---|---|
| U1 | **A restore whose COMMIT fails (or that is killed) after the rename loses the pre-restore row**, so the overwritten content is only an unreferenced blob. A regression from R2-3: before it, the pre-restore row was committed first. | all three | medium | **R3-1:** commit the pre-restore row in its own transaction before the rename again; under the restore lock, re-check the file's stamp (device, inode, size, mtime, ctime) and start over if it changed, so a concurrent restore still cannot make it stale. The error names the saved pre-restore id. |
| U2 | Timestamps are taken before the write lock, so concurrent restores are logged out of commit order; a snapshot can describe content that changed before it committed. | transactions, regressions | medium/low | **R3-2:** take timestamps under the lock; `sc snapshot` re-checks the file's stamp under the lock and re-reads if it changed. |
| U3 | R2-3 moved the pre-restore read, hash and blob write back under the lock (an 8 MB restore held it 0.4-1.1 s instead of 0.02-0.2 s), undoing most of R2-2. | all three | low | **R3-1:** the read, hash and blob write happen before the lock; under it only a stat. |
| U4 | COMMIT is retried for up to about 30 s for every write, holding SQLite's PENDING lock, so meanwhile even `sc log` fails; a snapshot has nothing on disk to protect. | transactions, regressions | low | **R3-3:** retry COMMIT only when the caller has already changed a file on disk (a restore after its rename). |
| U5 | `writeTx` is not panic-safe: a recovered panic would pool a connection with its transaction still open. | transactions | low | **R3-3:** roll back (or discard the connection) on panic, then re-panic. |
| U6 | Interrupting `sc restore` (Ctrl-C, SIGTERM) while it waits for the lock leaves the prepared temp file, a full copy, in the config directory. | files, regressions | low | **R3-4:** on SIGINT/SIGTERM, `sc` removes its pending temp files and exits with one line. (kill -9 cannot be caught; M2's watcher cleans stale `.*.sc-tmp-*` at start.) |
| U7 | Errors from the prepare step lack "(file not changed)". | files | low | **R3-5.** |
| U8 | The round-1 review doc says `syncDir` uses O_NOFOLLOW; the code rightly does not, and no test guards that. | files | low | **R3-5:** correct the doc; test `syncDir` through a symlinked directory. |

## Checked and fine (examples)

- (transactions) conn.Raw(ErrBadConn) really discards the connection in Go 1.22.2. Source path: /usr/lib/go-1.22/src/database/sql/sql.go:2062-2084 (Raw passes f's error to release), then 2110-2115 (closemuRUnlockCondReleaseConn calls c.close(err) on ErrBadConn), then 2121-2137 and 559 (releaseConn), then putConn 1465ff (an ErrBadConn conn is not reused; dc.Close())…
- (transactions) SQLITE_BUSY on COMMIT leaves the transaction active; every other COMMIT error ends it. In VdbeHalt (modernc lib/sqlite_linux_amd64.go around 62150-62160), 'rc == SQLITE_BUSY && readOnly' returns BUSY without rolling back, and any other rc calls _sqlite3RollbackAll. So the retry loop is valid. After a non-BUSY error the explicit ROLLBACK fails with …
- (transactions) Non-BUSY COMMIT failure probe (TestProbeCommitIOError, with RLIMIT_FSIZE set to the DB size so the DB cannot grow at commit): Restore returned 'file restored but not recorded: commit: disk I/O error (778)'. The file on disk was restored, no pre-restore or restore row was committed, no hot journal was left, OpenConnections was 0 (conn discarded), an…
- (files) Restore onto a missing file: exit 0, file created with the snapshot's mode and owner, 'no previous file existed', no temp file left (edge.sh 'missing').
- (files) Restore onto a directory, a symlink (live and dangling), or a FIFO: exit 1, one stderr line 'sc: restore P: save current state: P is not a regular file / is a symlink, refusing (file not changed)', log row count unchanged (3 -> 3), no .sc-tmp-* left, the symlink's target file is untouched (edge.sh).
- (files) Immutable target (chattr +i) and append-only target (+a): rename fails with EPERM, exit 1, one line ending '(file not changed)', content still 'bad', rows unchanged, temp removed by the deferred Discard. Immutable parent directory: CreateTemp fails EPERM, clean. Attributes were removed afterwards.
- (regressions) Build and static checks on HEAD (45b117d, which adds only a WORKLOG.md change on top of 762edcb), in the scratch copy /home/vboxuser/smartconfig-work/review-r2/regress: the CGO_ENABLED=0 build is 'statically linked'. go vet ./... and gofmt -l print nothing.
- (regressions) Tests as the normal user: go test -count=1 ./... gives ok for all 3 packages. As root (sudo env PATH HOME=/root GOCACHE GOMODCACHE CGO_ENABLED=0 go test -v): every test passes, including TestRestoreOwner and the 9 new round-2 tests.
- (regressions) Race detector: CGO_ENABLED=1 go test -race -count=3 ./... and -race -count=2 -cpu 1,4,8 ./internal/store are both clean.
