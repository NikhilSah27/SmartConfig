# Worklog

What we are doing now, and a dated log of what is done. Updated and pushed
after every step. For the full history, decisions and recovery guide see
[PROJECT_LOG.md](PROJECT_LOG.md).

## Now

**Progress at a glance:** [visuals/progress.html](visuals/progress.html),
live at https://nikhilsah27.github.io/SmartConfig/visuals/progress.html.

**M2 plan approved** (2026-09-28, "start M2, all my picks"): every
recommendation in [M2_PLAN.md](M2_PLAN.md) section 16 and in
[NEXT_STEPS.md](NEXT_STEPS.md) holds.

**Overnight run, starting 04:30 IST (23:00 UTC) on 28 Sep, unattended:**

1. Setup from the roadmap answers: CLAUDE.md edits (question 8), local git
   hooks against secrets (6), CI (5), a supported Go checked with govulncheck
   (9), `make race` (4).
2. M2 steps 1 to 16 in plan order, one step at a time, each with its tests,
   checks, commit, worklog line and push; chunk reviews as the plan says.
3. Only in the repo and in throwaway `SC_HOME`s. No install of the watcher,
   no reboot, nothing written to `/etc`, `/boot` or the real store.
4. It stops, and writes why here, at the acceptance run and the sign-off
   runs (they need the VM snapshot and your OK), or when the plan says
   "stop and ask".
5. Planning and design agents run on Fable; work that needs several agents
   (chunk reviews, checks) runs as ultracode workflows.

**Now: M2 step 4** (chunk C): the scope glob language (`internal/scope`,
`glob.go`). Chunk A is reviewed and closed. From now on one thing at a
time: no review runs while the next chunk is built (your call,
2026-09-30).

**Still waiting for you:**

- [ ] Ruleset on main (roadmap question 7) and host details (question 13).

Done at the tag: `m1` on `df1a378` (pushed); backups in
`/var/backups/smartconfig` (binary `sc-m1`, store, `/etc` and `/boot/grub`,
manifest and checksums), verified; recovery guide points to `sc-m1`.

**Decisions already made for M2** (2026-09-27, "go with your picks"):

1. Scope: all of `/etc` minus the exclude list; the tiers only decide how
   loudly to alert.
2. Symlinks: supported in M2 (needed for systemd enable, disable and mask).
3. Deleted and newly created files: recorded in history.
4. SSH host private keys: fingerprint, mode and owner only, no content.
5. Outside `/etc`: `authorized_keys` files and `/boot/grub`; not whole
   `.ssh` folders (they hold private keys).
6. The two M1 fixes go first, as a small M1 follow-up.

## Log

### 2026-09-27

- **M1 built and accepted.** Store, CLI, Makefile, smoke test; acceptance run
  passed as root. Commits `690ab95` to `020c205`.
- **Visual docs.** System map, narrated films; fixed two playback bugs.
  `847e14a`, `994f16c`.
- **Published.** README, branch `main`, public repo
  https://github.com/NikhilSah27/SmartConfig, GitHub Pages site
  https://nikhilsah27.github.io/SmartConfig/. `f5d81bc`.
- **Full check from a fresh clone:** tests, vet, gofmt clean, static binary,
  root smoke PASS, all Pages URLs load.
- **Project log and recovery guide.** `2980474`.
- **Exploratory test of `sc`:** 35 real-world checks (bad inputs, setuid
  restore, deleted files, immutable files, corrupt blobs, 8 parallel
  snapshots). No bugs; two test-script mistakes corrected.
- **M2 research:** what the watcher must watch, ignore and beware of,
  researched on this VM by 16 agents, key claims re-tested.
  [M2_WATCHLIST.md](M2_WATCHLIST.md), `9df9dd3`.
- **logrotate and sc's temp files:** confirmed logrotate reads
  `.<name>.sc-tmp-*` in `/etc/logrotate.d`; tested `~`, `.dpkg-tmp` and
  `.swp` suffixes and none avoids it. Impact is one skipped duplicate stanza
  if logrotate runs during a restore's few milliseconds. Left as is.
- **M1 fix 1:** `ReadWithMeta` now opens with `O_NOFOLLOW|O_NONBLOCK` and
  requires the opened file to be the one lstat saw. New test reproduces a
  swapped file, a symlink and a FIFO; it fails on the old code (read the
  wrong file, followed the symlink, blocked on the FIFO). `c720f22`.
- **M1 fix 2:** `store.Restore` now inserts its row, renames the file while
  holding the write lock, then commits (rolled back if the write fails). A
  watcher that sees the rename and takes the lock finds the restore row
  instead of an unexplained change. Tests pause inside the restore to prove
  it and fail on the old order. `ec15ece`.
- **Full check after both fixes:** gofmt, vet, all tests as user and as root,
  static build, `sudo ./scripts/smoke.sh` PASS, `/etc/hosts` unchanged.
- **Next-steps roadmap** published as [NEXT_STEPS.md](NEXT_STEPS.md): what
  to do now, the path from plan approval to "M2 done" (five reviewed chunks,
  reboot test, 24-hour soak with a real apt upgrade), what comes after, and
  13 questions. A critic's corrections are applied.
- **Research outputs saved** from `/tmp` to `~/smartconfig-work/` on the VM,
  because `/tmp` is wiped at every boot.
- **Review of the M1 fixes:** three reviewers plus two skeptics per finding.
  13 of 14 findings confirmed, none breaking M1 as used today. Decisions per
  finding in [reviews/2026-09-27-m1-fixes.md](reviews/2026-09-27-m1-fixes.md).
- **M2 plan** drafted three ways, judged by two judges, merged, checked by a
  critic (1 blocker, 6 major fixed) and published as [M2_PLAN.md](M2_PLAN.md)
  for approval.
- **R2-1:** write transactions now always end: COMMIT retried while readers
  are busy, explicit ROLLBACK, a connection that cannot be cleaned is
  discarded. Two new tests; a mutation check reproduced the reviewer's bug
  (an uncommitted row visible in the same process). `c5b693e`.
- **R2-4:** `sc snapshot` now compares with the latest row under the write
  lock, so a snapshot taken during a restore no longer records a spurious
  extra row. The new test fails on the old code. `09a7dbd`.
- **R2-2:** a restore now writes and syncs its temp file before taking the
  database lock and holds the lock only for the rename; the directory sync
  can no longer hang on a FIFO. Three new tests; the FIFO test hangs with the
  old code. `1e6e91a`.
- **R2-3:** a restore now records the previous state, inserts its own row
  and renames the file under one lock; a failure records nothing, a
  concurrent restore can no longer make "previous state" stale, and errors
  say whether the file changed. Three new tests, all failing on the old
  code. `c0d3839`.
- **R2-5:** "is a symlink, refusing" is now reported only when the path
  itself is a symlink; a loop further up the path keeps its real error.
  `a517893`.
- **Full check after round 2:** everything clean, including the race
  detector and the root smoke run.
- **Stress comparison** (12 parallel writers, 180 snapshots and 180 restores
  per run, 2 runs per version): now 1 snapshot and 1 restore failure in 360
  each, fewer than before fix 2 (3 and 3). A script bug in the first run
  (a missing id after a failed first snapshot) was found and fixed before
  these numbers.
- **Review of round 2:** 15 findings, all confirmed, grouped into 8 issues.
  The main one (U1) is a regression from R2-3 and is fixed first in round 3.
  [reviews/2026-09-27-m1-fixes-round2.md](reviews/2026-09-27-m1-fixes-round2.md)
- **R3-3:** a panic inside a write transaction now rolls it back; only a
  restore (whose rename comes before the commit) retries COMMIT, so other
  commands no longer hold SQLite's lock for 30 s. `3277111`.
- **R3-1:** the undo point (pre-restore row) is committed before the file
  is touched again, so a failed or interrupted restore can be undone;
  a stamp check under the lock keeps concurrent restores accurate; restore
  timestamps are taken under the lock. The error after a failed commit names
  the saved id. New tests fail on the round-2 code. `e7420bb`.
- **R3-2:** `sc snapshot` records what is on disk when it commits (re-reads
  if the file changed meanwhile) and takes its timestamp under the lock.
  `f944a1c`.
- **R3-4:** Ctrl-C or SIGTERM during `sc` now removes prepared temp files,
  prints one line and exits 1; tested with the real binary. `f4a0948`.
- **R3-5:** review doc corrected (the directory sync follows a symlinked
  directory on purpose) and a test pins it. `41dd177`.
- **Full check after round 3:** everything clean; stress 1/360 snapshot and
  0/360 restore failures under 12 simultaneous writers.
- **Final gate review** (round 3 fixes plus all of M1 against its spec):
  18 findings, all low except one missing test; no data loss. Round 4 fixes
  the ones that break CLAUDE.md rules.
  [reviews/2026-09-28-m1-gate.md](reviews/2026-09-28-m1-gate.md)
- **R4-1:** Ctrl-C and friends now stop `sc` at a safe point with a truthful
  one-line message; SIGHUP is handled, SIGQUIT prints no stack trace, and
  signals a script ignores stay ignored. Tested in-process and with the real
  binary; the binary tests fail on the round-3 handler. `4be4bb7`.
- **R4-3:** a file replaced while being read is now retried instead of
  failing the snapshot or restore. `423e9dc`.
- **R4-2:** tests for the M1 spec clauses the gate review found untested (special mode
  bits, restore guards, id formula, output formats). Mutation checks now
  catch a dropped setuid bit and a removed guard. `348ec67`.
- **R4-4:** MILESTONES.md now lists M1's hardened behaviour and its known
  limits. `2dee5bb`.
- **Full check from a fresh GitHub clone after round 4:** all clean; stress
  2/360 snapshot, 0/360 restore failures.
- **Review of the round-4 signal handling:** 7 findings, all confirmed, no
  data loss. Round 5 fixes them.
  [reviews/2026-09-28-m1-signals.md](reviews/2026-09-28-m1-signals.md)
- **R5-1:** signals, second version. A second Ctrl-C can no longer hide a
  replaced file, Ctrl-C stops shell loops again (sc ends by the signal), no
  line can contradict a finished result, and no stop signal prints a stack
  trace. Six real-binary tests, two mutation checks. `151b7fd`.
- **Verified R5-1 with the reviewers' own scripts:** double Ctrl-C after
  the rename now reports the saved id (3/3), a late signal no longer hides
  the result, SIGABRT/SIGTRAP print one line and leave no temp file.
- **Final check at the tag** from a fresh GitHub clone: all clean. Stress
  under 12 writers: failures are rare and only the safe "database is
  locked (file not changed)", the same as round 4 (noise between runs).
- **Tagged `m1`** on `df1a378` and pushed the tag.
- **Backups** in `/var/backups/smartconfig` (root only): `sc-m1`, the
  store, `/etc` and `/boot/grub`, manifest and checksums. Verified: the
  archive has 3145 entries including fstab and grub.cfg; `sc-m1` reads the
  real store.
- **Progress page facts** gathered by 4 agents from git, the reviews, the
  tests and the docs; two doc mistakes fixed (`ec9a58a`).

### 2026-09-28

- **Independent fact check of the progress page** (3 agents: text, charts,
  links; 356 checks, every link opened). They raised 30 notes (some the same
  problem seen by two agents, one needing no change); every real problem was
  fixed before publishing. The main ones: the page said "severity falling"
  (medium findings per round were 1, 2, 0, 2), "no finding was rated high" (a
  reviewer rated one high; only the skeptics did not), "each fix came with a
  failing test" (R4-2 and R4-4 did not), "exact permissions" (sc restores mode
  and owner, not ACLs or xattrs), "symlinks are never followed" (only the file
  itself; symlinked parent directories are left to M2), and it did not say the
  reviewers were AI agents. Two similar overclaims in this log (R3-1, R4-2)
  are corrected too.
- **Progress page published:** [visuals/progress.html](visuals/progress.html),
  live at https://nikhilsah27.github.io/SmartConfig/visuals/progress.html.
  Renders without errors at desktop and phone width; all three charts'
  tooltips work.
- **M2 plan approved** with all recommendations; the answers are recorded
  in M2_PLAN.md and NEXT_STEPS.md. Overnight work scheduled for 04:30 IST
  (23:00 UTC).

### 2026-09-28 night (unattended)

- **Secret hooks (question 6):** local `pre-commit`, `commit-msg` and
  `pre-push` hooks refuse the private denylist (kept outside the repo, mode
  0600) and the shapes of crypt hashes (`$6$`, `$y$`), private keys, `psk=`
  values and GitHub tokens. Tested with 11 cases, including a commit that
  bypassed the commit hooks being refused at push. The hooks are local and not
  in the repo.
- **`make race` (question 4)** `2a915c1` and **the CLAUDE.md edits (question
  8)** `f4c79be`.
- **Go 1.26.8 (question 9)** `20330e4`: govulncheck v1.8.0 found 3
  standard-library vulnerabilities with Go 1.22.2 (2 reachable on Linux) and
  none with 1.26.8. The recovery guide no longer sets `GOTOOLCHAIN=local`.
- **Slow disk tonight:** a synchronous 4 KB write takes 25 to 340 ms (normally
  a few ms), worst while big builds run, so the whole suite runs 20 to 30
  times slower. Two timing tests failed as root because of it, identically on
  Go 1.22.2.
- **Tests count COMMIT attempts instead of timing them** (`9a746c3`)
  (`TestSnapshotDoesNotRetryCommit`, `TestInterruptAfterRenameStopsRetries`),
  so a slow disk cannot fail them. Mutation checks: with the retry bugs put
  back, both fail (6 attempts, want 1).
- **Signal fix: one line even when a second Ctrl-C races the result**
  (`270b095`). The slow disk exposed a real M1 race: after a first Ctrl-C the
  restore printed "interrupted (file not changed)", and a second Ctrl-C before
  sc exited printed another line. Now the first to claim the end prints, the
  other stays silent, and a finished command also claims it. A new real-binary
  test holds sc open after its line (a hook built only with `-tags sctest`,
  absent from the shipped binary) and fails with two lines without the fix.
- **M2 step 1** `a1a6419`: history is ordered by insert order (rowid), not by
  the wall-clock timestamp, so a clock stepping back cannot make an older row
  the newest. Two new tests (store and `sc log`) fail on the old queries.
  Checks clean as user, race and root.
- **After-rename signal test made deterministic** `cb7a635`: it missed its
  window 5 of 5 times on tmpfs (so probably on a CI runner's fast disk too); a
  pause built only into the signal tests' binary now makes it catch the window
  on the first try.
- **M2 step 2** `34da00e`: `sc init` creates `changes.db` mode 0600 and
  tightens an existing 0644 one; the journal gets the same mode.
  Mutation-checked. Checks clean as user, race and root.

### 2026-09-30

- The overnight run stopped part way through step 3; its uncommitted code
  was reviewed, finished and checked here.
- **M2 step 3** `69e8f9b`: schema version 1 adds `kind` and `target` and
  the index `changes_path_seq`, migrated in one IMMEDIATE transaction;
  an M1 database with rows is copied to `changes.db.m1-backup` (0600)
  first; a newer schema or a WAL store is refused with one line. The
  concurrent-first-open test now holds all 4 opens until each has read
  version 0, so it races on every run. Mutation checks: 6 of 6 guards
  caught. Checks clean as user, race and root. The real store is not
  touched (gate G1).
- **Chunk A (steps 1-3) done**, waiting for its light review.
- **VirtualBox snapshot `m1-frozen` taken** (you, 2026-09-30).
- **CI pushed** `3e266cc` after you added the `workflow` scope to the token:
  gofmt, vet, tests as user and root, race, static build, on every push
  to main and every pull request.
- **Chunk A review closed**
  ([reviews/2026-09-30-m2-chunk-a.md](reviews/2026-09-30-m2-chunk-a.md)):
  one reviewer, every finding reproduced before fixing.
  - **D1 (high), fixed `ee0ddea`:** the step 3 backup read `changes.db`
    with a second descriptor, whose close dropped the process's SQLite
    locks; with several processes opening an M1 store at once, 13 of 20
    failed mid-migration and an index could be corrupted. The copy now
    comes through SQLite itself. After: 0 of 80 failed, integrity ok. Two
    new tests use real processes and fail on the old code.
  - **D3 (low), fixed `397877a`:** a symlink at the backup's name no
    longer counts as a backup.
  - **Test gap, `b3d3f8e`:** a store upgraded by a newer sc while this one
    waits for the lock is refused, not set back.
  - **D2 accepted:** a never-migrated M1 store on a read-only disk cannot
    be read by the new sc; the real store is migrated at sign-off S2, well
    before the M4 rescue path. Goes into the M2 known limits.
  - Checks clean as user, race and root; the new tests pass 20 of 20.
