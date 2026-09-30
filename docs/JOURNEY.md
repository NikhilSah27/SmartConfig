# The SmartConfig journey

How the project started, which path we chose at each turn and why, and where
we have moved away from the plan. For the day-to-day log see
[WORKLOG.md](WORKLOG.md); for decisions and recovery, [PROJECT_LOG.md](PROJECT_LOG.md).
Every fact here comes from those files, the reviews and git history.

Last updated: 2026-09-30, M2 steps 1-5, chunks A and C reviewed.

## 1. The idea

A safety net for the Linux config files that take a machine down (fstab,
GRUB, sshd, sudoers, PAM). Snapshot a file before it is edited, warn before a
bad edit is applied, explain what broke, and restore one file from a rescue
shell. Target: Ubuntu 24.04, on a VirtualBox VM used as the lab.

It is built in seven milestones, strictly one at a time and never ahead of
the current one, not even as stubs ([MILESTONES.md](../MILESTONES.md)):
M1 store and CLI, M2 watcher, M3 checkers, M4 rescue path, M5 .deb package,
M6 incident set, M7 local model.

**Ground rules chosen at the start (CLAUDE.md), and why:**

| Choice | Why |
|---|---|
| Go, one static binary (`CGO_ENABLED=0`) | It must run from a broken system with nothing else working. |
| SQLite through `modernc.org/sqlite` (pure Go) | Keeps the binary static; no C library needed. |
| Write a config file back only by temp file, fsync, rename | A crash can never leave a half-written fstab. |
| Tests never touch `/etc` or the real store | The lab machine must stay bootable. |
| Small commits, WORKLOG updated and pushed after every step | Anyone, human or Claude, can pick the work up cold. |
| Stop and ask before touching the real system, adding a dependency or changing an approved plan | The owner decides anything that is hard to undo. |

## 2. M1: the store and the `sc` command (2026-09-27 to 28)

- **Built in one sitting** as seven commits: file helpers, store, CLI,
  Makefile, smoke test, milestones. `sc init`, `snapshot`, `log`, `cat`,
  `diff`, `restore`.
- **First decision taken to the owner:** the spec's snapshot id collides when
  the same file is saved twice in one second, which the acceptance run itself
  does. We stopped and asked, then chose "hash again with a counter" (PROJECT_LOG D1).
- **Proved on the real VM:** a one-character UUID typo in `/etc/fstab`, found
  with `sc diff` and undone with `sc restore`, byte for byte. Research with
  systemd's own fstab generator showed which typos really stop a boot: a data
  disk line does (90 s wait, emergency mode, and root is locked so there is no
  shell); the root line most likely does not.
- **Published:** GitHub repo, a docs site, a system map and narrated films.

**Hardening: four review rounds, five rounds of fixes.** Instead of calling M1
done after its tests passed, it was attacked on purpose. Each round, AI
reviewers ran the code and tried to break it, and two skeptics had to confirm
each finding before it counted.

| Round | Findings | What they were about |
|---|---|---|
| 1 | 14 | Concurrency and security of the first two follow-up fixes |
| 2 | 15 | Transactions, restore atomicity |
| 3 (gate) | 18 | Restore logic, signals, spec conformance |
| 4 | 7 | Ctrl-C handling; led to a second design of signal handling |

Round 2 found one way to lose data: a restore that failed or was killed
right after replacing the file lost its undo point (finding U1, a regression
of a round-2 fix); round 3 fixed it. The gate and signal reviews found no
data loss. The fixes made `sc` safe to run next
to the future watcher: every write is one locked transaction, a restore
records an undo point before it touches the file, and Ctrl-C always tells the
truth about whether the file changed. **M1 was tagged `m1` on 2026-09-28**,
and backups of the binary, the store, `/etc` and `/boot/grub` were made.

## 3. Planning M2: the watcher (2026-09-27 to 28)

- **Research first, no code.** [M2_WATCHLIST.md](M2_WATCHLIST.md) studied what
  on this VM changes, what must be watched and what is noise. It found that
  `/etc` holds 791 symlinks (systemd enable/mask), so symlinks had to be in scope.
- **Six decisions by the owner** (2026-09-27): all of `/etc` minus an exclude
  list; symlinks supported; deletions and creations recorded; SSH host keys
  fingerprint-only; `authorized_keys` and `/boot/grub` outside `/etc`; the two
  M1 fixes first.
- **Roadmap with 13 questions** ([NEXT_STEPS.md](NEXT_STEPS.md)), answered
  "yes to every recommendation" on 2026-09-28: CI, git hooks against secrets,
  a supported Go, `make race`, tiers only as log priority in M2, `boot_id`
  deferred to M4, reboot and 24-hour soak as part of acceptance.
- **The plan itself** was written three times independently (lean, traps,
  admin), judged, the lean draft chosen and the best ideas of the other two
  merged in, then checked by a critic (one blocker, six major issues, all
  applied). **Approved 2026-09-28** with all recommendations:
  17 steps in five reviewed chunks ([M2_PLAN.md](M2_PLAN.md)).

## 4. Building M2

**Overnight run, 2026-09-28 to 29 (unattended).** Setup first: local hooks
that refuse secrets, `make race`, Go 1.26.8 (the old 1.22.2 had three known
vulnerabilities), CLAUDE.md edits. A very slow disk that night exposed a real
M1 race (two lines after a double Ctrl-C) and two timing-dependent tests;
all three were fixed. Then:

- **Step 1:** history ordered by insert order, not the clock, which can step back.
- **Step 2:** the database created readable by root only (0600).
- **Step 3 was left half done**: the run stopped with the code uncommitted and
  no note why.

**Resumed interactively, 2026-09-30.**

- Step 3 (the schema upgrade that adds row kinds) was checked, finished and
  pushed. CI went live once the GitHub token had the `workflow` scope, and the
  VM snapshot `m1-frozen` was taken.
- **The owner asked for strictly one step at a time.** From then on no review
  runs while the next chunk is built.
- **Chunk A review found a serious bug in step 3.** The backup of an M1
  database read the file through a second descriptor; closing it silently
  dropped SQLite's lock for the whole process. With several processes opening
  an old store at once (sc and scd at boot), 13 of 20 failed and an index
  could be corrupted. Fixed by reading the backup through SQLite itself: 0 of
  80 failed afterwards. The tests had missed it because they used goroutines,
  which share one process; the new tests use real processes.
- **Steps 4 and 5:** the pattern language and the default scope. Checked
  against this VM's real `/etc` (read-only): the numbers matched the plan's
  research prototype exactly.
- **First scope change**, after the owner reviewed what is excluded:
  `/etc/alternatives` is now recorded (an iptables legacy/nft switch was
  invisible), and TLS private keys and PPP secrets are fingerprinted instead
  of ignored (a replaced key is now seen; secrets are still never stored).

- **Chunk C review** found no secret leak for the named paths, but two
  medium gaps: `ssh-keygen` writes a new private host key under a temporary
  name first, which would have been stored with its content; and scripts
  that systemd generators and run-parts directories execute were hidden by
  the noise rules. Both fixed, plus four small ones, each with a test that
  fails on the old code.

## 5. Where we moved away from the plan

All changes since approval, with reasons, are kept in one place:
[M2_PLAN.md Appendix C](M2_PLAN.md#appendix-c-changes-after-approval). In short:

| | Change | Why |
|---|---|---|
| C1 | Overnight run stopped in step 3 | Unknown; finished interactively |
| C2 | No overlapping reviews | Owner: one step at a time |
| C3 | Light review by one reviewer agent plus Claude as skeptic | Enough for a light review, far cheaper; same method |
| C4 | Backup read through SQLite; process-level lock tests | Review finding D1 (high) |
| C5 | New known limit for never-migrated stores on read-only disks | Accepted: the real store is migrated long before M4 |
| C6 | SC_HOME excluded by a path check, not a glob | Glob characters in a path cannot misfire |
| C7 | Record alternatives; fingerprint private keys and secrets | Owner, after reviewing the excluded list |
| C8 | Fingerprint host-key temp names; more consumer directories in block D | Chunk C review findings D1-D6 |
| C9 | Record `~/.ssh/rc` and `environment`; fingerprint dropbear, WireGuard, apt auth and LUKS key files | Owner, on two questions from the chunk C review |

## 6. Lessons so far

- **A test that passes on the first run proves little.** Every step now gets
  mutation checks: break the code on purpose and confirm a test fails. This
  found weak or missing tests in steps 3, 4 and 5 before they were committed.
- **Concurrency must be tested the way it happens.** Goroutines in one process
  hid a lock bug that separate processes showed at once.
- **Independent review pays.** Every review round has found something the
  author's own tests did not.
- **Check the real machine, read-only.** Running the scope against the real
  `/etc` listing is what makes the exclude list trustworthy.

## 7. Where we are

M1 done. M2: steps 1-5 of 17 done, plus the scope changes; chunks A and C
reviewed and closed. Next: chunk B (row kinds for links, deletions and
fingerprints through the CLI), starting with step 6.
