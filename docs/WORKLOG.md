# Worklog

What we are doing now, and a dated log of what is done. Updated and pushed
after every step. For the full history, decisions and recovery guide see
[PROJECT_LOG.md](PROJECT_LOG.md).

## Now

**Current task:** round 5 (R5-1), the signal handling fixes from the
last review, before the `m1` tag. Then the M2 plan waits for your approval.

- [x] Review of `c720f22` and `ec15ece`: 14 findings, 13 confirmed, 1 split.
      None breaks M1 as used today; most matter once the watcher runs next
      to `sc`. Every finding has a decision:
      [reviews/2026-09-27-m1-fixes.md](reviews/2026-09-27-m1-fixes.md).
- [x] M2 plan written, judged and critiqued: [M2_PLAN.md](M2_PLAN.md)
      (17 steps in 5 chunks, 14 questions for you). **Waiting for your
      approval.**
- [x] R2-1 store: dedicated connection for write transactions; COMMIT
      retried on SQLITE_BUSY; never pool a connection with an open
      transaction. `c5b693e`
- [x] R2-4 store: snapshot's unchanged check inside the write transaction. `09a7dbd`
- [x] R2-2 fsutil/store: temp file written before the lock; directory opened
      without blocking. `1e6e91a`
- [x] R2-3 store: restore in one transaction with its pre-restore row;
      clearer errors. `c0d3839`
- [x] R2-5 fsutil: precise ELOOP message. `a517893`
- [x] Full check after round 2: gofmt, vet, tests as user and root, race
      detector, static build, root smoke PASS, `/etc/hosts` unchanged.
- [x] Stress, 12 parallel writers x 15 rounds, 2 runs per version:
      failures before fix 2 3+3/360, after fix 2 2+4/360, now 1+1/360
      (snapshot+restore). The rest wait more than 5 s for the lock under
      that load.
- [x] Independent review of round 2: 15 findings (all confirmed), grouped
      into 8 issues U1-U8. U1 is a regression from R2-3: a restore whose
      commit fails after the rename lost its pre-restore row.
      [reviews/2026-09-27-m1-fixes-round2.md](reviews/2026-09-27-m1-fixes-round2.md)
- [x] R3-1 store: pre-restore row committed before the rename again, with a
      stamp re-check under the lock; reads and hashing outside the lock.
      `e7420bb`
- [x] R3-2 store: timestamps under the lock; snapshot stamp re-check. `f944a1c`
- [x] R3-3 store: writeTx panic-safe; COMMIT retried only after a rename. `3277111`
- [x] R3-4 sc: remove pending temp files on Ctrl-C/SIGTERM. `f4a0948`
- [x] R3-5 small: prepare errors say "file not changed" (in R3-1); doc
      fix; syncDir test. `41dd177`
- [x] Full check after round 3: gofmt, vet, tests as user and root, race
      detector, static build, root smoke PASS, `/etc/hosts` unchanged.
      Stress (12 writers, 2 runs): 1 snapshot and 0 restore failures in 360
      each, the best so far.
- [x] Final gate review: 18 findings, all confirmed, rated low by the
      skeptics except one missing test; no data loss. Some break CLAUDE.md
      rules (every feature has a test; no stack traces), so a small round 4
      comes first. [reviews/2026-09-28-m1-gate.md](reviews/2026-09-28-m1-gate.md)
- [x] R4-1 sc: signal handling reworked (stop at a safe point, truthful
      message, no stack trace, ignored signals stay ignored). `4be4bb7`
- [x] R4-2 tests for spec clauses without one (setuid/sticky, guards, id
      formula, output formats). `348ec67`
- [x] R4-3 fsutil/store: "replaced while being read" retried. `423e9dc`
- [x] R4-4 MILESTONES.md: deliberate deviations and known limits. `2dee5bb`
- [x] Full check from a fresh GitHub clone at `5dc4041`: gofmt, vet, tests
      as user and root, race detector, static build, root smoke PASS,
      `/etc/hosts` unchanged; stress 2/360 snapshot and 0/360 restore
      failures under 12 writers.
- [x] Focused review of the new signal handling: 7 findings, all
      confirmed, no data loss; three real behaviour issues (a second Ctrl-C
      after the rename, shell loops not stopping, a signal right after the
      result). [reviews/2026-09-28-m1-signals.md](reviews/2026-09-28-m1-signals.md)
- [x] R5-1 sc: signal handling, second version. `151b7fd`
- [x] Re-ran the reviewers' reproduction scripts against the new binary:
      every scenario now behaves as intended (table in the review doc).
- [ ] Then tag `m1`, backups, and your `m1-frozen` snapshot.

**Open for you:** approve the M2 plan and answer its 14 questions
([M2_PLAN.md section 16](M2_PLAN.md)); answer the 13 roadmap questions
([NEXT_STEPS.md](NEXT_STEPS.md)).

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
  is touched again, so a failed or interrupted restore can always be undone;
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
- **R4-2:** tests for every M1 spec clause that had none (special mode
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
