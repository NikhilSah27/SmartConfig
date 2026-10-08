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

**Now: M3 is being built** ([M3_PLAN.md](M3_PLAN.md), approved 2026-10-03,
"great go ahead": all 7 recommendations of its section 14, which also
passes gate G1, scd may run validators as root). 16 steps in five chunks,
one at a time. Done: step 1 (`45df7da`). Step 2 (`95da13b`). Step 3 (`b665691`). Step 4 (`efc2ce3`): chunk A is built and reviewed
([reviews/2026-10-03-m3-chunk-a.md](reviews/2026-10-03-m3-chunk-a.md),
fixes `a2e325f`). Step 5 (`afa9403`). Step 6 (`5f6068b`). Step 7 (`c997865`): chunk B is built and reviewed
([reviews/2026-10-03-m3-chunk-b.md](reviews/2026-10-03-m3-chunk-b.md),
fixes `a946f6f`). Chunk C (steps 8-11) is built: four agents built the checkers in
separate worktrees, Claude reviewed and integrated each; reviewed
([reviews/2026-10-03-m3-chunk-c.md](reviews/2026-10-03-m3-chunk-c.md),
fixes `41c87bf`). Timing-bound tests made robust (`5b1d5a5`). Step 12 (`580e1d1`). Step 14 (`7ccb3d9`).
Step 13 is in (`a73dfde`, `2e4d4ee`) and the acceptance script is
committed, not run (`2ff038b`). Chunk D is reviewed
([reviews/2026-10-03-m3-chunk-d.md](reviews/2026-10-03-m3-chunk-d.md),
fixes `2fb037e`, plan change C8). Step 15, the docs, is in (see the log).
**M3 is built. Now: the sign-off runs** (plan section 11; your call,
2026-10-03: close the M2 soak now and sign off, on the snapshot you
already have). S1, S2 and S3 are done, and S4's final review is
closed ([reviews/2026-10-03-m3-final.md](reviews/2026-10-03-m3-final.md),
fixes `297fbc7`, `bb31f3e`, plan change C9, 8 follow-ups in MILESTONES).
`accept-m3` passes on the fixed build. **M3 is done: tag `m3`**
(2026-10-03); the fixed build runs as scd.

**Now: M4 is being built** ([M4_PLAN.md](M4_PLAN.md), approved
2026-10-04, "approved": all 7 recommendations of its section 10). 13
steps in five chunks, one at a time. Done: steps 1-3, chunk A built and reviewed
([reviews/2026-10-04-m4-chunk-a.md](reviews/2026-10-04-m4-chunk-a.md),
fixes `49a0a5b`). Done: steps 4-6, chunk B built and reviewed
([reviews/2026-10-04-m4-chunk-b.md](reviews/2026-10-04-m4-chunk-b.md),
fixes `40288d8`, plan change C4). Done: steps 7-10, chunk C built and reviewed
([reviews/2026-10-04-m4-chunk-c.md](reviews/2026-10-04-m4-chunk-c.md),
fixes `683678a`, plan change C5). Step 11, the QEMU lab (`lab/`,
`make lab-e2e`), is committed (`9f29ea5`, with its one product fix
`55d2bde`). The step 11 check on `618e4e9`: UEFI passed; BIOS did not in
three runs, none of them a fault of sc (log, 2026-10-05), and the last
one needed a lab fix (`bec333b`). The second round (on `225e2d6`) gave
no verdict either: UEFI stalled before GRUB, and this VM went down
during BIOS (log, 2026-10-05). The lab now retries such a stall
(`152ecbe`). **The step 11 check passed** on `56f5359`: `make lab-e2e`
PASS, UEFI and BIOS, clean tree, with boot 5, no retries (log,
2026-10-05). **The chunk D review is closed** (2026-10-06;
[reviews/2026-10-05-m4-chunk-d.md](reviews/2026-10-05-m4-chunk-d.md),
plan change C6). Two reviewers: 1 high (narrow), 10 medium, 13 low,
5 cleanups, none of which changed the PASS on `56f5359`. Fixes
`3115b71` (sc: the 60 s limit, the hangup flag, the login hint),
`9d3df9e` (lab: verdict classes, teardown, the cache); from the runs
of the fixes `1d25037` and `c70d886` (sc: the console writer's turn,
a host pause as one slice), `64911f4` and `0ccba27` (lab: the second
command line, a SHUTDOWN not lost); the mutation tests `fcb1b6d` and
`a371738` (326 lab tests). `make lab-e2e` on `22383a0`: UEFI PASS
(24m29s, 0 retries) and, in a bios-only rerun of the same tree, BIOS
PASS (45m14s, 2 lab retries, both the host's pauses); both with boot 5
and `dirty=no`. Open items are in the write-up; the host's pauses are
yours (below). A check-in every 30 min resumes work after a usage
limit (your ask); no VM is up. **Step 12 is in** (`3e72281`):
`scripts/accept-m4.sh` (`make accept-m4`), the non-boot parts as root,
nothing real written; checked as a user, not run as root yet. Its run
is part of sign-off S1 (with all checks and `make lab-e2e`) and needs
your OK. **Step 13 is in** (`b722bf4`): the docs (README "When the
machine does not boot", MILESTONES, PROJECT_LOG, CLAUDE.md; see the
log). All 13 steps are built. **The chunk E review is closed**
([reviews/2026-10-06-m4-chunk-e.md](reviews/2026-10-06-m4-chunk-e.md),
plan change C7): 1 high (the README's GRUB password recipe), 4 medium,
12 low, 6 cleanups, all fixed. Fixes `654ee7f` (sc status: the undo is
the newest blocker before a newer error; the menu promise only while
its flag is set) and `4b91d97` (accept-m4.sh: the runtime units stay
loaded, so step 2 can read their journal; the real store read-only; a
bad verdict; more). **M4 is built and reviewed.** **Sign-off S1
passed** (your OK, 2026-10-06 14:22 UTC, "yes start S1"; log): all
checks, `make accept-m4` as root PASS (`19f2200`), and `make lab-e2e`
PASS in both modes on `2c58f8b`, one `sc` build (`cafca1512050`):
BIOS 44m18s and UEFI 37m19s, no retries, boot 5, clean tree. On the
way: an ovmf update made the lab's reference image stale, and `make
lab-image` was found gone since `9d3df9e` (back in `b9761a9`).
**Sign-off S2 passed** (your OK and your snapshot, 2026-10-06 19:45
UTC, "took the snapshot"; log). The M4 build (`cafca1512050`, the one
S1 tested), the two boot units, the drop-ins and `42_smartconfig` are
installed on this VM, `update-grub` has run; the reboot's verdict ok,
flag clear, 23.5 s, sc off the critical chain, the menu hidden, `sc
status` healthy. You reset the two boots after it (a black desktop;
your answer). **Sign-off S3 passed** (your OK and a fresh snapshot,
2026-10-06 21:05 UTC, "Yes, taken — go"; your "s3 done" and two
photos, 2026-10-07 06:27 UTC; log): the bad line in `/etc/fstab` (row
`053fdf`, scd: blocker); two failed boots, both verdict bad, each with
the login screen up over emergency mode (on a desktop gdm takes the
screen, so the way in is the next boot's menu); the rescue boot with
the report above "Press Enter" (your photos); your `sc restore fb5cf4`;
and the boot after it: verdict ok, flag clear, `/etc/fstab` back
(sha256 `9d71ab60…`), `sc status` healthy, exit 0. New on a desktop,
for S4 (log): a red `[FAILED] grub-initrd-fallback.service` right above
the report (Ubuntu's unit, which cannot write grubenv on the read-only
root), `sc boot seen` twice in each failed boot (harmless here), and
the README's "stops in emergency mode". **S4: the final review is
closed** (your OK, 2026-10-07 06:45 UTC, "go ahead with S4";
[reviews/2026-10-07-m4-final.md](reviews/2026-10-07-m4-final.md), plan
change C8): three reviewers, 2 high, 6 medium, all fixed, and 13 low
(`69ea585` … `d9c8817`, log); the rest is the M4 follow-up list in
MILESTONES. A lab run on `87c6636` was stopped at your word (09:31 UTC)
and does not count (log). **S4 is closed and M4 is done: tag `m4`**
(your OK, 2026-10-07 13:20 UTC, "yes, go ahead after it passes"; no
snapshot, your call; log). `make lab-e2e` on `59203a3`, clean tree,
one `sc` build (`1dce09f5ff12`): UEFI PASS (19m35s) and, in a BIOS-only
rerun of the same tree after a `[lab]` ssh timeout, BIOS PASS (19m59s);
no retries, boot 5. `accept-m4` as root PASS on that build, and that
build is installed on this VM (`sc`, both units, `42_smartconfig`,
`update-grub`; the S1 build is kept as `sc-2c58f8b`). The annotated tag
`m4` is on `59203a3` and pushed. No VM is running.
(The reboot check of that install is folded into the follow-ups' one,
below: no reboot came between.)

**Now: M5, the package** (your call, 2026-10-07: M5 starts after the
follow-ups either way). The plan, for your read: [M5_PLAN.md](M5_PLAN.md);
its questions run on the recommended answers until you say otherwise.
Its steps, drafted in `~/smartconfig-work/fu-scratch/wt-m5`, land one at
a time, each with its checks, CI and a worklog line.

**The M3 and M4 follow-ups: done and signed off** (2026-10-08). Chunks
F, G and H, each reviewed. `make lab-e2e LAB_E2E_ARGS=--grub-password`
on `643cec3` (clean, `sc` `22a34f3df9b3`): UEFI PASS (29m19s, 1 retry)
and, in a BIOS-only rerun of the same tree after host stalls cost two
runs their verdicts, BIOS PASS (24m21s, 2 retries); run files in
`~/smartconfig-work/signoff/lab-e2e-643cec3/`. As root (Claude, your OK
of 2026-10-07): `accept-m2`, `accept-m3` and `accept-m4` PASS on
`d686341` with that build (`accept-m4` fixed there: the hardened test
units ran `sc` from `/home`, which `ProtectHome=` hides), then that build
installed: scd `Type=notify` (startup rescan 413 ms), the three units
enabled, `systemd-analyze verify` clean, `grub-script-check` ok, the
rescue entry in `grub.cfg`, `sc status` exit 0, no failed units; the
`59203a3` build kept as `/var/backups/smartconfig/sc-59203a3`. Log
`~/smartconfig-work/signoff/signoff-fu-20261008T035149Z-d686341.log`.
**At the next session after a reboot** (yours to start: a reboot ends
this session): the first boot with the follow-ups' units. Read-only:
its verdict ok, the flag clear, `sc-boot-seen` and `sc-boot-ok` done
under their sandbox, scd active (`Type=notify`), `sudo sc status` exit
0, no failed units; log them.

**The M3 and M4 follow-ups, as planned** (your call,
2026-10-07: "if you have finished everything from v1 to v4 then only
start the M5 plan", then "All follow-ups"). Every open item of the M3
and M4 follow-up lists and the actionable "Open from the reviews" notes
in MILESTONES, one at a time, each with its tests, checks, commit,
worklog line, push and CI; where an item has a real choice, you pick.
Out: M4 follow-up 8 (LUKS, a non-goal), and the items that are yours
(below). Order, severity first, in three chunks, each closed by a
review:
- **Chunk F, M3's checkers (done 2026-10-07, reviewed:
  [reviews/2026-10-07-m3-followups-chunk-f.md](reviews/2026-10-07-m3-followups-chunk-f.md),
  fixes `cfb704e`):** M3 1 (unit drop-ins, the one medium),
  2 and 3 (sshd: `ListenAddress`, a drop-in with its main file), 4 (raw
  lines for `-v`), 5 (Ctrl-C or SIGTERM to `sc check`), 7 (a check's row
  lookup), 8 (cosmetics).
- **Chunk G, `sc status` and restore (done 2026-10-07, reviewed:
  [reviews/2026-10-07-m4-followups-chunk-g.md](reviews/2026-10-07-m4-followups-chunk-g.md),
  fixes `0a55306`):** M4 1 (a restore under an
  unmounted `/boot`), 2 (a file replaced by a symlink), 9 (a torn verdict
  line), 7 (cosmetics); the notes: the mount hint for `/usr/local` and
  `/boot`, SIGHUP before `signal.Notify`, edits recorded after the
  healthy boot's row, the report held until `sc` returns. Your picks
  (2026-10-07): M4 1 refuses (nothing written, "mount it first"); the
  verdict waits for scd's startup rescan (scd `Type=notify` once the
  rescan is done, `sc-boot-ok` after it); the console prints its header
  at once and the changes when ready; the early SIGHUP stays a
  documented limit. M4 4 (soft-reboot, chunk H) is documented as a
  limit (systemd 255 has no soft-reboot count).
- **Chunk H, boot units, GRUB and the lab (done 2026-10-08, reviewed:
  [reviews/2026-10-07-m4-followups-chunk-h.md](reviews/2026-10-07-m4-followups-chunk-h.md),
  fixes `a1c1228`):** M4 3 (`GRUB_TOP_LEVEL`),
  4 (soft-reboot), 5 (the units' hardening, the test knob), 6 (the test
  gaps); the notes: lab check 1.8, the GRUB password recipe in the lab,
  the lab's own items, the `cmd/sc` flake.
- **Then:** all checks, `make lab-e2e` in both modes, `accept-m2`,
  `accept-m3` and `accept-m4` as root (`accept-m2` too, as `scd.service`
  changed) and one install of the new build, the reboot and its
  read-only check; then M5. Your calls (2026-10-07): Claude runs these
  itself, with sudo ("Permissions are granted to you"); a step that
  still needs you (auto mode refusing sudo) is skipped, written under
  "Still waiting for you" with what is left, and the work goes on; after
  the follow-ups M5 starts either way.
  The install puts in `scd.service` too (`Type=notify` since `32cc097`):
  `sc` first, then the unit, `daemon-reload`, `restart scd`.

**M2 follow-ups done; the soak (S6) runs** (2026-10-03). M2 is done
(tag `m2`). All 8 follow-ups from the final review are in (log,
2026-10-03), and the build of `b6ab3cc` runs as scd since 14:27 UTC (the
`m2` binary is kept as `sc-m2`). Earlier that day the VM came back from
the pre-S5 snapshot; the `m2` install, the `apt upgrade` and the
post-upgrade manifest were redone (S5's results stand, log 2026-10-02).

**The soak (S6) is closed** (your call, 2026-10-03, after 5.6 h instead
of 24; log). It passed on what it saw. Next: your VM settings and snapshot, then the M3 plan
for your approval. The soak check, once scd has run about 24 h on this
build: uptime, restarts, memory, log volume,
unexplained rows, and `/etc` against `manifest-post-s5apt.txt` in
`/var/backups/smartconfig`. (Kernel 7.0.0-38 has run since 2026-10-05.)
To remove the watcher: README "Watch every change"; keep the
store (`sc-m1` still reads it; copying `changes.db.m1-backup` back would
drop every M2 row). One thing at a time: no review runs while the next
step is built (your call, 2026-09-30).

**Still waiting for you:**

- [ ] Reboot this VM when it suits you (it ends a running session); the
  next session runs the read-only check above.
- [ ] Read [M5_PLAN.md](M5_PLAN.md): questions 1-6 (dpkg-deb or nfpm,
  `/usr/sbin`, purge, the hand install, the package's name, the license).

- [ ] Ruleset on main (roadmap question 7) and host details (question 13).
- [ ] On the host, before the soak: 4 vCPUs and the VMSVGA graphics
  controller (black screens after login, log 2026-10-03).
- [ ] On the host: why this VM stands still for minutes at a time. In
  the lab runs of 2026-10-06 it did for 167 and 318 s (UEFI), 384 s
  (BIOS, which cost that run its verdict), and 250, 364, 400, 400 and
  265 s back to back (the BIOS rerun: about 24 minutes in which the VM
  barely ran, `signoff/lab-e2e-22383a0/bios-stall-5-1.txt`). On
  2026-10-05 it also went down without a shutdown three times, and once
  more on 2026-10-06 at about 11:30 UTC, idle (`last -x`: crash). Sleep
  or power saving on the host is the first thing to look at.
- [ ] A VirtualBox snapshot of this state (after the VM settings, if you
  change them), so a restore by mistake no longer undoes M2.

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
- **M2 step 4** `7ba575b`: new package `internal/scope` with the glob language
  of plan 7.2 (`**`, `*` with leading dots, classes, escapes, braces);
  every pattern validated at compile time. Mutation checks: 14 of 14
  caught, two after adding cases. Checks clean as user, race and root.
- **M2 step 5** `b5ad961`: the default scope (plan 7.3, embedded), `Recorded`,
  `Tier`, `FingerprintOnly`, login homes. Against this VM's real `/etc`
  and `/boot/grub` (read-only listing): 323 watched directories, 1,143
  files, 234 links, tiers 342/119/66/850, fingerprint-only exactly
  machine-id and the three host keys; the same as the plan's prototype.
  Mutation checks: 24 of 24 caught. Checks clean as user, race and root.
- **Scope change** `78b8cc7`, your call after reviewing the excluded list:
  `/etc/alternatives` recorded (firewall families at tier 3), TLS private
  keys, credstore and PPP secrets fingerprint-only at tier 2. On this VM:
  327 directories, 1,147 files, 380 links. The new tests fail 14 times on
  the old scope.
- **Journey and deviations recorded:** [JOURNEY.md](JOURNEY.md) tells how
  the project started, the path chosen and why; M2_PLAN.md Appendix C lists
  every change since the plan was approved (C1-C7).
- **Chunk C review closed**
  ([reviews/2026-09-30-m2-chunk-c.md](reviews/2026-09-30-m2-chunk-c.md)):
  one reviewer, every finding reproduced first. No leak for the named
  secret paths, no panics, glob matches plan 7.2.
  - **D1 (medium), `80bc523`:** `ssh-keygen -A` writes a private host key
    as `NAME.XXXXXXXXXX` first; that name is now fingerprint-only.
  - **D2 (medium), `310bc1f`:** systemd generator and run-parts
    directories added to block D, so scripts they run (`foo.disabled`,
    `sedAbC123`) are recorded.
  - **D3-D6 (low):** deep paths decided at the first excluded directory
    (8 s to under 1 ms), paths cleaned where a wrong answer would store a
    secret, `\**` and `[**]` accepted. Test gaps closed.
  - Plan Appendix C gains C8. Checks clean as user, race and root.
- **Scope change C9** `1aca688`, your call on the review's two questions:
  `~/.ssh/rc`, `~/.ssh/environment` and `/etc/ssh/sshrc` recorded at
  tier 2; dropbear host keys, WireGuard, apt auth and LUKS key files
  fingerprint-only. None exist on this VM today.
- **M2 step 6** `4034618`: `fsutil.ReadState` reads files and symlinks
  through the parent directory without following any symlink, refuses
  directories, FIFOs, sockets, devices and big files without opening
  them, and says whether the path changed during the read;
  `SymlinkAtomic` and `RemoveFile`. `sc snapshot` now also refuses a file
  in a symlinked directory. M1 fsutil tests unchanged. Mutation checks:
  15 of 15 caught; the new tests found a trailing-slash bug while the
  step was written. Checks clean as user, race and root; builds for
  arm64 too. Plan note C10 (API reuse).
- **M2 step 7** `5e9bb25`: rows have kinds (file, link, digest, deleted);
  `Record` batches up to 50 observations with dedup, stamp re-check under
  the lock, proof-of-absence rows and computed intents; `Snapshot` is
  built on it, so `sc snapshot` records symlinks as links and
  fingerprint-only paths as digests. A writer killed mid-transaction
  after a cache spill leaves only committed rows. Mutation checks: 19 of
  20 caught, one equivalent. Checks clean as user, race and root. Plan
  note C11 (computed intents only for watcher rows).
- **M2 step 8** `83c706c`: restore handles links (atomic symlink, owner set
  first) and deletions (removes the file after a pre-restore row); it
  refuses fingerprint-only paths, digest rows and unsafe directories
  (symlinked, missing, or owned by another user) before writing
  anything. A watcher recording during a restore adds no row after it.
  Mutation checks: 15 of 16 caught, one equivalent. Checks clean as
  user, race and root.
- **M2 step 9** `3a3dcf5`: the CLI understands row kinds: link and
  fingerprint lines in `sc snapshot`, link/deleted/digest in `sc log`,
  link targets in `cat` and `diff`, and the four restore lines of plan
  5.6. No command shows a fingerprint-only path's content. Mutation
  checks: 12 of 12 caught. Checks clean as user, race and root.
- **M2 step 10** `d388381`: `make m1-compat` builds `sc-m1` from the `m1` tag
  without touching the checkout and proves it still reads a migrated
  store, restores file rows, refuses every new row kind with one line
  and changes nothing. The test fails when pointed at the M2 binary, so
  it tells them apart. Checks clean as user, race and root.
- **Chunk B (steps 6-10) done**, waiting for its review.
- **Chunk B review closed**
  ([reviews/2026-10-01-m2-chunk-b.md](reviews/2026-10-01-m2-chunk-b.md)):
  two reviewers, every finding reproduced first.
  - **D1 (medium, found by both), `cae8992`:** a fingerprint-only file
    reached through a symlinked directory (`/proc/self/root/etc/machine-id`)
    was stored with its content; the rule now also checks the resolved path.
  - **D2-D5 (low):** kind-change intents, swap races now retried, control
    characters quoted in output, more 64-bit architectures build.
  - Ten test gaps closed; each reported mutation now fails a test.
  - A lesson: one commit briefly held a failing test because `go test` was
    piped through `tail`, which hid its exit status. It was fixed before
    any push; checks now run with `pipefail`.
  - Checks clean as user, race and root; `make m1-compat` passes.
- **M2 step 11** `e410264`: new package `internal/watch` with the inotify
  layer (syscall package only): nonblocking instance whose `Close`
  unblocks `Read`, directory-only watches that refuse symlinks, and an
  event parser. Mutation checks: 9 of 9 caught; 5 runs in a row pass.
  Checks clean as user, race and root.
- **M2 step 12** `e6b1c32`: the watcher. Two inotify readers, one debounced
  worker recording through the store with the stamp check, proof of
  absence for "did not exist", a startup rescan with "while not
  watching" and a one-line baseline summary, log lines without content,
  panic recovery, the single-instance lock and the SC_HOME-in-a-root
  refusal. Writer matrix and structural tests pass; 5 runs in a row and
  the race detector clean. Mutation checks: 17 of 20 caught, 3
  equivalent; one test was found not to test what it claimed and fixed.
  Plan note C12.
- **M2 step 13** `836a2a9`: overflow (a real kernel overflow in the test),
  periodic and rate-limited rescans, root changes (passwd, late roots, a
  root moved away and back), the watch limit, the free-space floor,
  user-file limits, the dirty-set bound, store-error backoff, and
  `TestKillDuringBaseline`. The race detector found a real race in the
  worker's wait computation; fixed. One race-mode run of the whole suite
  failed in `internal/watch` with output not kept; the next 9 race runs,
  5 normal runs and the root run passed. The chunk D review is asked to
  look for flakiness. Mutation checks: 14 of 14. Plan note C13.
- **Chunk D (steps 11-13) done**, waiting for its full review.
- **Chunk D review closed**
  ([reviews/2026-10-01-m2-chunk-d.md](reviews/2026-10-01-m2-chunk-d.md)):
  three reviewers (concurrency, events, security), every finding
  reproduced first.
  - **Security (high), `1beb8f3`:** as uid 1000, a user could make the
    root watcher store a root-only file through a directory named
    `authorized_keys` and a symlink swap. Nothing below `.ssh` entries is
    recorded now, and the watcher never reads through a symlink below a
    root. The attack is a permanent root-only test.
  - **Events and concurrency, `962a118` and `3cbbd2d`:** an endless
    "moved" loop, a crash on a moved directory over the dirty bound, new
    directories left unwatched by a racing rescan, missing deleted rows
    when paths change type, rescan loops on busy hosts, and a hang after a
    panic. All fixed with tests that fail when the fix is reverted.
  - Checks clean as user (5 watcher runs), race (2 runs) and root.
- **M2 step 14** `39ae262`: `sc watch` with `--root`, `runContext`, exit 0 on
  SIGTERM (the real binary is tested), journald priority prefixes only
  when `JOURNAL_STREAM` names stderr. Mutation checks: 6 of 6. Checks
  clean as user, race and root.
- **M2 step 15** `833a899`: `scripts/scd.service` (plan 9) with
  `TestUnitFile`, and `scripts/accept-m2.sh` (plan 12, 19 steps, `make
  accept-m2`). The acceptance run has not been run: it is yours.
- **M2 step 16** `51a4b0a`: README (sc watch, install by hand, scope),
  CLAUDE.md layout, PROJECT_LOG (rescue binary, resume prompt).
- **Chunk E (steps 14-16) done**, waiting for its review.
- **Chunk E review closed**
  ([reviews/2026-10-01-m2-chunk-e.md](reviews/2026-10-01-m2-chunk-e.md)):
  two reviewers. The acceptance script, started as this worklog said
  (`sudo make accept-m2`), would have restored root's own
  `authorized_keys` under nested sudo, and could stop itself with `kill
  -STOP 0`: both fixed `e865164` before it was ever run. `sc watch`: SIGPIPE
  no longer kills it silently, SIGHUP rescans instead of stopping, fatal
  lines are logged at err `d56237f`. Docs warn that the M2 `sc` upgrades an
  M1 store on first use `0bdf909`. Plan note C14. Checks clean as user (5
  watcher runs), race and root; `make m1-compat` passes.

### 2026-10-02

- **M2 sign-off S1, checks and acceptance** (on `d20ae38`, your OK):
  gofmt, vet, `go test ./...` as user and as root, `make race` all clean;
  `file bin/sc` says statically linked; `make m1-compat` passes.
  `make accept-m2` PASS, all 19 steps (baseline 14 s after the restart,
  `integrity_check` ok). Its test paths, runtime unit and scratch store
  were gone afterwards.
- **M2 sign-off S1 closed:** `sudo ./scripts/smoke.sh` PASS. `/etc/hosts`
  is back byte for byte (same sha256, 0644 root:root), no scratch store
  left.
- **M2 sign-off S2 closed, the real store** (your yes):
  1. Backups in `/var/backups/smartconfig` present; `store-m1.tar` lists,
     `sc-m1` runs. New: `manifest-pre-s2.txt` and `sha256-pre-s2.txt`,
     `/etc` and `/boot/grub` just before the install.
  2. Rehearsal on a copy: the 4 rows keep their ids, `changes.db.m1-backup`
     made, integrity ok, user_version 1; `sc-m1` still reads the migrated
     copy. The real store was untouched; the copy was deleted.
  3. Installed `bin/sc` from `595b440` (sha256 `a0bee866…f307`, static)
     to `/usr/local/sbin/sc` and `scripts/scd.service`;
     `systemd-analyze verify` clean; `systemctl enable --now scd`.
     Journal: watching `/etc`, `/boot/grub`, 2 `.ssh` dirs (329
     directories); baseline 1530 first seen, 0 changed, 0 deleted, 6.9 s;
     no warnings, 0 restarts, 21 MB.
  4. Real store after: 1534 rows (1142 file, 381 link, 7 digest auto rows
     plus the 4 M1 rows with their ids), integrity ok, journal mode
     delete, 7.1 MB; `changes.db.m1-backup` kept; `sc-m1` reads it.
  5. `/etc` against the manifest: only `scd.service` and its
     `multi-user.target.wants` link are new; nothing else changed.
- **M2 sign-off S3 closed, the owner scenario** (after your snapshot;
  run by Claude, nano driven through a pty so it writes as nano does):
  1. nano adds a comment line to `/etc/ssh/sshd_config` (written in
     place, same inode), no `sc` typed: one row, `T2 changed (fa203e)` at
     journald warning; `sc diff 868df4` shows the old version.
  2. `chmod -x /etc/grub.d/41_custom`, then `+x`: two mode-only rows,
     `mode 0755->0644` and back, T1, content unchanged.
  3. `systemctl mask rsync.service` (disabled, inactive), then unmask: a
     `did not exist` row, a link row `created` (`sc cat` prints
     `/dev/null`), then `deleted`; T1.
  4. `systemctl stop scd`, a comment line added to `/etc/hosts`, start:
     `baseline: 0 first seen, 1 changed` (400 ms), the row says `changed
     while not watching`, T3 at notice.
  5. `sc restore` of both files with scd running: a pre-restore and a
     restore row each, no journal line and no automatic row in the next
     8 s. Both files back byte for byte (`/etc/hosts` sha256 as before),
     `sshd -t` ok.
  After: `/etc` against `manifest-pre-s2.txt` differs only by the scd
  unit and its link; integrity ok, 1545 rows (11 new, all expected);
  scd active, 0 restarts, no err lines.
- **M2 sign-off S4 closed, the reboot test** (Claude scheduled the reboot
  with `systemd-run --on-active=30 systemctl reboot`, your OK):
  - Shutdown clean: scd stopped in under a second, `reboot.target`
    reached.
  - **Incident on the first boot after it (not scd, as far as the logs
    show):** the boot itself was normal (graphical.target at 10.0 s; scd
    baseline 0/0/0 done at 7 s and silent afterwards). You logged in at
    24 s; the desktop then stayed black. The journal shows the GNOME
    desktop-icons extension (DING) relaunched 10 times in about 35 s (once
    on a normal boot), no reason logged, no gnome-shell crash. You reset
    the VM at about 60 s (journal cut mid-stream, "uncleanly shut down"
    on the next boot). The next boot was fine. Nothing scd touches
    changed: `/etc` against `manifest-pre-s2.txt` differs only by the scd
    unit, its link and `cups/subscriptions.conf{,.O}` (rewritten by cupsd
    at 22:15, excluded by `default.scope` line 87). Side result: the store
    was open under a running scd at the hard reset and is intact
    (integrity ok, 1545 rows).
  - Checks on the good boot: 16.2 s to graphical.target (14.6 s and
    10.0 s on the two boots before); scd is not in `critical-chain`, 116
    ms in `blame` (the exec only); nothing is ordered after it except
    `shutdown.target` and `multi-user.target`. scd active, 0 restarts,
    25 MB; baseline 0 first seen, 0 changed, 0 deleted (3.1 s).
- **M2 sign-off S5 closed, failure paths** (after your snapshot; scripts
  and logs in `~/smartconfig-work/signoff/`):
  - **Kill during the startup rescan.** scd stopped, `/etc/hosts` edited
    and `/etc/sc-s5-test.conf` created, page cache dropped, scd started
    and `kill -9`ed right after its `watching` line, before its baseline
    line. systemd restarted it 10 s later (`NRestarts=1`). The killed
    instance had already committed the new file as `first seen`; the new
    one recorded `/etc/hosts` as `changed while not watching` and nothing
    twice (2 new rows). Undone afterwards (`sc restore`, `rm`).
  - **The apt upgrade, your "all 31":** 25 upgraded, among them kernel
    7.0.0-38 (not booted yet), gnome-shell, apparmor, Xorg; 6 mesa
    packages are phased (10%) and held back by apt. Across `/etc` and
    `/boot/grub` it changed only `/boot/grub/grub.cfg` (manifest diff), and
    scd recorded it once (`cc5cff`). No burst, so the kill could not
    land in one.
  - **Kill during a dpkg burst instead:** `logcheck-database` (190
    conffiles under `/etc/logcheck`, no maintainer scripts, no service)
    installed, then purged with `kill -9` after the first change line.
    Install: 190 `did not exist` plus 190 `created` rows, each newest row
    matches the disk. Purge: killed after 100 of the 190 deletions; the
    restart recorded the other 90 as `deleted while not watching`:
    exactly 190 rows, every newest row matches the disk, no two identical
    automatic rows in a row, integrity ok, `NRestarts=2`. `/etc` is the
    same as after the upgrade (the 3 other files in `/etc/logcheck`
    belong to rsyslog, gpg-agent and cracklib-runtime).
  - Static check of acceptance step 18 on the real unit: no unit is
    ordered after or bound to `scd.service`.
  - Test-script lesson: `journalctl -f | grep -m N` only ends when
    journalctl writes again, so after a one-second burst it hung until
    its timeout and the first kill never fired. The scripts now poll.
  - For M3: a file created while scd is down is a `first seen` row, which
    gets no journal line of its own (plan 6, 7.6); if scd is killed
    before its baseline summary, not even the count is logged. The row is
    in the store. The existence-flag alerts deferred to M3 (plan 15)
    should cover this case.
- **M2 sign-off S6 deferred** (your call: short on time). It needs no time
  from you: scd keeps running in normal use, and the soak checks run at
  the start of the next session on whatever it has run by then.
- **M2 sign-off S7, fresh clone:** `git clone` from GitHub at `49c6d13`:
  gofmt, vet and `go test ./...` clean, `bin/sc` statically linked. CI
  green on the same commit (user, root, race, static build).
- **M2 final review closed**
  ([reviews/2026-10-02-m2-final.md](reviews/2026-10-02-m2-final.md)): three
  reviewers (security, integrity, operations). No high-severity finding;
  every finding reproduced first, one only partly.
  - **Medium, fixed `9cee575`:** a user renaming `~/.ssh` in a loop
    held back every rescan except the startup one.
  - **Medium, docs:** fixed in step 17.
  - **Medium, deferred:** a system file rewritten constantly is not
    rate-limited (no such writer on this VM).
  - **Low, fixed `3349449`:** snapd's `user/*.wants/snap.*` links are now
    excluded.
  - **Low, deferred:** the rest, listed under "M2 follow-ups" in
    MILESTONES.
  - Checks clean as user, root and race; `go test -count=5
    ./internal/watch/...` passes; the scope test against this VM's real
    `/etc` listing passes.
- **M2 step 17:** MILESTONES (M2 done, M2 notes, follow-ups, Current: M3),
  README (status, `sudo journalctl`, retention, how to remove the watcher),
  PROJECT_LOG (status, VM state, recovery guide now points at
  `/usr/local/sbin/sc` with `sc-m1` as fallback), NEXT_STEPS (2.4 ticked,
  soak deferred). Tag `m2`.
- **`m2` installed:** `/usr/local/sbin/sc` replaced by the build of tag
  `m2` (sha256 `03fbc30d…`, static; was `a0bee866…` from `595b440`), scd
  restarted: baseline 0 first seen, 0 changed, 0 deleted (2.2 s),
  integrity ok, 2121 rows, 15 MB. CI green on `112bceb` (tag `m2`).

### 2026-10-03

- **VM rolled back to the pre-S5 snapshot, then 3 hard resets** (found
  by Claude in the journal; nothing in the repo changed):
  - The VM resumed at 00:01 UTC from the live snapshot of 22:22 UTC
    (clock jump, NIC and USB reset in that boot's journal). It was the
    snapshot taken for S5, so the VM's disk and memory went back to before
    S5; "Now" lists what that undid.
  - Right after the resume rtkit reported its canary thread starving; the
    log stops at 00:02:28 and the VM was reset.
  - The next two boots were normal up to login (scd baseline 0/0/0 in 2.3
    and 3.7 s, graphical.target at 12 and 17 s, password accepted), then
    the desktop stayed black and the VM was reset again. The desktop
    session's journal of both boots was lost (0 lines). The boot after
    them is fine.
  - Same picture as the first boot after S4. scd is not implicated: idle
    before each login, 0 restarts, 25 MB, about 2 s CPU. What the logs do
    show: every boot has kernel `clocksource: Long readout interval`
    warnings (the host holds the vCPUs off for up to 5.6 s), several have
    rtkit starvation; 9 vCPUs on an i7-13700HX host; GNOME on Wayland with
    software rendering (VirtualBox graphics adapter, no 3D). Suggested on
    the host, before the soak: 4 vCPUs and the VMSVGA graphics controller.
  - After: store integrity ok, 1545 rows; `/etc` and `/boot/grub` match
    `manifest-pre-s5.txt` except `cups/subscriptions.conf{,.O}` (cupsd,
    excluded).
- **`m2` reinstalled** (your yes): built at tag `m2` in a clean checkout,
  sha256 `03fbc30d…`, the same binary as before the rollback; installed to
  `/usr/local/sbin/sc`, scd restarted: baseline 0 first seen, 0 changed,
  0 deleted (555 ms), 0 restarts, 13 MB; store integrity ok, 1545 rows.
- **Clock 6 h 24 min behind, fixed:** the VM was paused for about 6.4
  hours during the session (61 min of uptime against 7.4 h of real time).
  No guest-additions service resyncs the clock on resume, and timesyncd's
  next poll was up to 34 min away; apt refused the mirror's index as
  "not valid yet". `systemctl restart systemd-timesyncd` stepped it to
  07:32 UTC (GitHub's Date header agrees). Commits `f592ece` and
  `cc5e620` carry committer times 6 h 24 min early.
- **`apt upgrade` rerun** (your yes; the same 31 as your "all 31"): 25
  upgraded, 8 new (kernel 7.0.0-38, not booted yet), 6 mesa packages held
  back by phasing. Across `/etc` and `/boot/grub` only
  `/boot/grub/grub.cfg` changed, recorded once (`862cd3`, T1).
  `manifest-post-s5apt.txt` and `sha256-post-s5apt.txt` saved again in
  `/var/backups/smartconfig`. scd 0 restarts, 11 MB; store integrity ok,
  1546 rows.
- **M2 follow-up 1, startup race, fixed `5b57aaf`:** New checked SC_HOME
  against every root, login `.ssh` roots too. A user who swapped `~/.ssh`
  for a symlink to a directory holding SC_HOME, between the lstat and that
  check, failed the start (reproduced with a test hook); 5 such failures
  in 10 min leave the unit failed (`StartLimitBurst=5`). Now New checks
  only the scope's roots; the startup rescan skips such a `.ssh` with a
  log line, as later rescans already did. New test
  `TestSSHRootSwappedAtStartup`. Build, gofmt, vet, `go test ./...` as
  user, and `internal/watch` and `cmd/sc` as root: clean. Not installed:
  scd still runs the `m2` build.
  - `make race` is flaky on this VM, with and without the change:
    `TestRescanUnderBusyEvents` ("3 rescans for one request") failed in 1
    of 3 full race runs of the watch package on the parent commit and 3 of
    7 with the change, and never alone (6 of 6 pass each way);
    `TestUserFileLimits` timed out once in 7, never alone (8 of 8 each
    way). Load on a CPU-starved VM; CI is the reference.
- **M2 follow-up 2, a root moved away, fixed `bd8fad9`:** a rescan marked
  stored paths only under usable roots, so after `mv ~/.ssh ~/.ssh.old`
  its files kept present rows, and a new `~/.ssh` gave `changed` rows
  with no `deleted` row first. A login root that is missing, a symlink or
  not a directory is now kept as gone: the rescan marks its stored paths
  and the worker reads them below it (absent, or the symlink refused).
  Moved away while watching: `deleted (found by rescan)`; a new `.ssh`
  then gives `created`. While scd is down: `deleted while not watching`.
  A scope root that is gone still gets no rows, since a separate `/boot`
  may only be unmounted (mount tracking is M4); a root that only fails
  with another error (an NFS home's `EACCES`) is not taken as gone.
  New tests `TestLoginRootGone`, `TestRootGoneAtStartup`.
  - A user renaming their `.ssh` in a loop now gets a `deleted` and a
    `created` row per file, about one pair a minute (the `created` row
    waits `UserFileGap`): at most 4 files per login, no new objects. The
    general rate limit is follow-up 8.
  - Checks: build, gofmt, vet, `go test ./...` as user, `internal/watch`
    and `cmd/sc` as root; the root tests 5 times under race: clean.
    `make race`: only the known `TestRescanUnderBusyEvents` flake (4
    rescans this time). Not installed.
- **M2 follow-up 3, directory swap, fixed `c0cfe77`:** `renameat2(RENAME_EXCHANGE)`
  of watched directories a and b gives MOVED_FROM a, MOVED_TO b,
  MOVED_FROM b, MOVED_TO a. The second MOVED_FROM removed, by path, the
  watches MOVED_TO b had just added for a's old directory, so it and its
  subdirectories stayed unwatched until the hourly rescan. A directory
  moved away is now walked again when another directory is found at its
  name. New test `TestDirExchange` (raw `renameat2`, amd64 and arm64:
  the syscall package has no wrapper, and `x/sys` would be a new
  dependency): edits in both swapped trees come from events, no rescan.
  Checks: build, gofmt, vet, `go test ./...` as user, `internal/watch`
  and `cmd/sc` as root, the test 5 times under race, `make race`: all
  clean. Not installed.
- **M2 follow-up 4, stale watch after an overflow, fixed `465b30d`:** when an
  overflow lost a directory's move away and a new directory was made at
  its name, the rescan watched the new one and left the old one's watch
  filed under the same name. A name created in the old directory then
  entered the new one's listing, and a later file of that name was
  recorded `first seen` instead of `did not exist` and `created`
  (reproduced). `walk` now removes another watch it finds under the name
  it watches. The review's other symptom (a subdirectory moved away in
  the old directory unwatching the new one's) was already covered by
  `c0cfe77`'s re-walk. New test `TestOverflowDropsStaleWatch` (a real
  overflow; also checks that every watch is its path's). Checks: build,
  gofmt, vet, `go test ./...` as user, `internal/watch` and `cmd/sc` as
  root, the test 5 times under race, `make race`: all clean. Not
  installed.
- **M2 follow-up 5, listings only grew, fixed `d76a295`:** a walk merged
  every name of the old listing into the new one (to keep names an event
  added while it listed), so names of files removed long ago stayed, and
  a file made later under such a name lost its proof of absence: `first
  seen` instead of `did not exist` and `created` (reproduced). A name
  from the old listing is now kept only if it is still there (`lstat`,
  only for names the new listing lacks); one that exists is never
  dropped, since a rename over it would then look like a create. New test
  `TestWalkTrimsListing`; `TestProofOfAbsence` and the writer matrix
  still pass. Checks: build, gofmt, vet, `go test ./...` as user,
  `internal/watch` and `cmd/sc` as root, the proof tests 5 times under
  race, `make race`: all clean. Not installed.
- **M2 follow-up 6, orphan objects of home files, fixed `2a8b686`:** `Record`
  stores a new object before its transaction finds the path moved, and
  keeps it. The home-file limit counted rows only, so a user who kept
  rewriting a file in their `.ssh` got about ten retries 50 ms apart
  (the `Quiet` of the tests), each leaving an object of up to 64 KiB,
  before every row: reproduced, 11 tries in 1.5 s. A new object stored
  for a home file whose record finds it moved now counts like a row, so
  the retry waits `UserFileGap`: 1 try. Such orphans are still kept
  (nothing is pruned in M2); they are now bounded like rows. New test
  `TestUserFileOrphansLimited`. Checks: build, gofmt, vet, `go test
  ./...` as user, `internal/watch` and `cmd/sc` as root, the user-file
  tests 5 times under race, `make race`: all clean. Not installed.
- **M2 follow-up 7, stale restore temp files, done `7463e5d`:** `kill -9`
  during `sc restore` can leave `.<base>.sc-tmp-<random>` next to its
  target, and the scope excludes those names, so nothing said so. A walk
  now logs one older than 10 minutes once per run, at warning: `stale
  temp file PATH (written TIME), left by an interrupted sc restore; not
  recorded, remove it`. Reported, not removed: it may hold the only copy
  of what the restore was writing, a fresh one may be a restore at work,
  and a removal in `/etc` is the admin's call. MILESTONES no longer says
  the watcher clears them. New test `TestStaleTempReported`. Checks:
  build, gofmt, vet, `go test ./...` as user, `internal/watch` and
  `cmd/sc` as root, the test 5 times under race: clean.
  - `make race` failed in `TestRescanUnderBusyEvents` (a 30 s wait timed
    out; "7 rescans" on a rerun). Alone under race, back to back, it
    failed 4 of 4 and then 2 of 4 on `6c1f306` (before today's
    follow-ups) and exactly the same on this commit: the VM's CPU
    starvation (pressure up to 13% this hour), not the changes. CI passes
    it. Worth making less timing-bound before M3.
- **M2 follow-up 8, your picks** (asked with three options each): rate
  limit "20/h, then 1 per 5 min"; digest ids "random id" (over a keyed
  hash or leaving them); install "after follow-up 8".
- **M2 follow-up 8a, digest ids, fixed `4ec437a`:** a digest row's id was
  the first 6 hex of sha256(path, ts, fingerprint) and the journal prints
  it, so a journal reader could test guesses at a short fingerprint-only
  secret (reproduced: the id followed the formula). Digest rows now get 6
  random hex digits with the same uniqueness check; file, link and
  deleted rows keep the formula (M1 ids still reproduced), and existing
  rows keep their ids. Plan 5.2 updated, change C15. New test
  `TestDigestIDRandom`. Checks: build, gofmt, vet, `make m1-compat`,
  `internal/store` and `cmd/sc` as root: clean.
  - The VM was paused again (about 2 h, uptime against the clock) and has
    run about twice as slow since: the watch package's timing-bound tests
    (`TestOverflow`, `TestOverflowDropsStaleWatch`,
    `TestRescanUnderBusyEvents`) timed out in `make test` and `make race`
    at CPU pressure up to 15%. Run back to back at normal load, the whole
    watch package passes on `6c1f306` (37.8 s) and on this code (44.9 s,
    8 more tests), and `TestOverflow` alone passes 10 of 10 on both at
    0.6 s. `gnome-shell` averages over half a CPU (software rendering).
- **M2 follow-up 8b, rate limit, done `064bf68`** (your pick): every change of
  a system file got a row, so one rewritten every second meant about
  86,000 rows, objects and journal lines a day. Each system file now has
  a row budget: 20 rows, one more earned every 5 min. With none left the
  next row waits, then records the newest state with ` (rate-limited)`;
  the first wait logs `PATH changes constantly: recording it at most
  every 5 min, the newest state` at warning. Budgets back to full are
  dropped at each rescan, so the next flood warns again. `.ssh` files keep
  their 60 s rule; restore rows are not counted (written by `sc`). A file
  rewritten without pause now gets about 300 rows a day. Plan 6.3 and
  C16, README, MILESTONES updated. New test `TestPathRateLimit`; the test
  setup gives other tests a budget of 1000 (`TestRestoreWhileWatching`
  changes one file 20 times and waited 5 min for the 21st row).
  `accept-m2.sh` checked by reading: no file gets more than a few
  watcher rows (its restores are `sc` rows; its 20,000 chmods run while
  scd is stopped). Checks: build, gofmt, vet, `go test ./...` as user,
  `internal/watch`, `internal/store` and `cmd/sc` as root, `make race`:
  all clean. Not installed.
- **All M2 follow-ups are done.** Next: install the new build (your pick:
  once, after 8), then the soak check (S6).
- **New build installed** (your pick: once, after follow-up 8): the build
  of `b6ab3cc` (M2 plus follow-ups 1-8; static, sha256 `396f84cb…`, clean
  tree) to `/usr/local/sbin/sc`; the `m2` binary kept as
  `/var/backups/smartconfig/sc-m2` (sha256 `03fbc30d…`). Unit unchanged
  since `m2`, store schema unchanged (`user_version` 1). scd restarted:
  baseline 0 first seen, 0 changed, 0 deleted (368 ms), 0 restarts, 13 MB;
  integrity ok, 1546 rows. The `m2` build's run before it (about 7 h, 2 of
  them paused): 4.5 s CPU, 12.7 MB peak, 0 restarts, one row
  (`grub.cfg`, the upgrade). PROJECT_LOG: VM state and recovery guide now
  name the new build and `sc-m2`.
- **M3 starts with its plan** (your "lets do m3"; asked with options):
  order M3 before M4, plan drafted by Claude in the session. The soak
  keeps running on the build of `b6ab3cc`. The VM settings (4 vCPUs,
  VMSVGA) and the snapshot are still open on your side.
- **M3 plan drafted** ([M3_PLAN.md](M3_PLAN.md)): `sc edit`, `sc check`,
  checks in scd after a recorded change, a built-in file graph; no store
  change; 16 steps in five chunks. Probed on this VM first (fabricated
  files in `~/smartconfig-work/m3plan/`; the real `/etc` only read), which
  changed the design: a stock system already fails some validators (pwck
  exit 2, 8 of 121 udev rule files), so only findings an edit adds are
  blamed on it; `netplan generate --root-dir` still asks for a
  daemon-reload, so the generator binary is used; `findmnt --verify`
  accepts a misspelt option and reports a missing disk as an error even
  with `nofail`, so our rules set the severity. Waiting for you: the 7
  questions in section 14.
- **M3 plan approved** ("great go ahead"): all 7 recommendations (scope,
  both checker sets, scd runs validators, "save anyway" prompt, no store
  change, one review per chunk plus a final one, sign-off after the soak
  check). Step 1 starts.
- **M3 step 1, `check: findings, rules, runner`, `45df7da`:** new package
  `internal/check`. `Finding` and `Severity`; the rules table (empty: each
  checker adds its rules with its step); `Runner`, the one place that
  starts a validator (fixed tool directories, never $PATH; argument list,
  no shell; `LC_ALL=C` and a fixed PATH; stdin /dev/null; 10 s timeout
  that kills the process group; 64 KiB output cap; a missing tool is a
  result, not an error); `Scratch`, the private copy under
  `$SC_HOME/tmp`. Found while testing: `os/exec` reports a leftover
  child only when the tool exits 0, so the group is killed after every
  run. A mutation (kill the tool only, not its group) fails
  `TestRunTimeoutKillsGroup`. Checks: build, gofmt, vet, `go test ./...`
  as user, the package as root and 3 times under race, static: clean.
- **M3 step 2, `check: file graph`, `95da13b`:** `default.graph` and its
  parser (`check`, `apply`, `mode` lines; first match wins), with the
  scope's glob language exported as `scope.Glob`. The built-in graph
  holds only its header until the checkers arrive; `TestDefaultGraph`
  then requires sample paths per checker that the scope records and that
  are not fingerprint-only. Plan change C1 (your OK): no `with` line,
  since M3 checks only the file that changed. Checks: build, gofmt, vet,
  `go test ./...`, both packages under race: clean.
- **M3 step 3, `check: fstab`, `b665691`:** `Checks.Check` (graph lookup,
  scratch copy, sorted findings) and the fstab checker: sc's own rules on
  every line, then `findmnt --verify --tab-file` on the scratch copy.
  Six rules with explanations. Severities follow what
  `systemd-fstab-generator` does (probed, plan A14, change C2): blocker
  only for a local filesystem without `nofail`/`noauto`; `/`, swap and
  network filesystems are errors; `nofail`/`noauto` lines warnings.
  Tests: 19-line fabricated fstab against a fake machine; findmnt outputs
  captured here as golden files (user and root); a hanging findmnt; the
  real findmnt where installed. Checks: build, gofmt, vet, `go test
  ./...`, the package as root and twice under race, static: clean.
- **M3 step 4, `check: baseline diff`, `efc2ce3`:** `Added(before, after)`
  gives what an edit added: a finding is the same when its rule, severity
  and text are, wherever its line moved; the same finding once more, or
  with a higher severity, is added. `Worst`. 11 cases. Checks: build,
  gofmt, vet, `go test ./...`: clean. Chunk A (steps 1-4) is built; its
  review is next.
- **M3 chunk A review closed**
  ([reviews/2026-10-03-m3-chunk-a.md](reviews/2026-10-03-m3-chunk-a.md)):
  one reviewer, 10 findings, each reproduced and fixed in `a2e325f`. Three
  were high: the baseline diff hid a newly broken line when the file
  already had a bad one; a UUID in the wrong letter case passed; findmnt's
  word on devices sc cannot look up was dropped. Also: false blockers on
  working type and option spellings, stdout and stderr read apart, a
  validator that did not really run is now a note, and leftovers are
  killed while the tool is still a zombie. Plan change C3. Checks: build,
  gofmt, vet, `go test ./...`, the package as root and 3 times under
  race, static: clean.
- **M3 step 5, `sc check`, `afa9403`:** the command of plan 6.1: a table
  (severity, file, line, rule, one sentence), `-v` for explanations and
  the validators' own lines, exit 2 for a blocker or an error. No
  argument: every regular file the graph has a checker for
  (`Graph.Files`); an id checks that saved version. What could not be
  checked is a `note:` line. Plan change C4: a user without write access
  to `$SC_HOME` gets a private scratch directory, so a readable file is
  checked without sudo. Tried read-only on the real `/etc/fstab` as a
  user (scratch in a temp `SC_HOME`): no problems, one note (not root,
  types not compared). A test found a crash on a pattern with no fixed
  directory (`**/name`); fixed before the commit. Checks: build, gofmt,
  vet, `go test ./...`, `check` and `cmd/sc` as root, race on the new
  tests, `make m1-compat`: clean.
- **M3 step 6, `store: Replace`, `5f6068b`:** a file and its row written in
  one commit under the write lock (restore's commit, now shared as
  `commitWrite`), for `sc edit`. Saves the state before unless the newest
  row holds it; one row of the new origin `edit`; a new file gets a `did
  not exist` row first; `ErrFileChanged` when the path is no longer the
  version read; the restore refusals apply. Tests: rows, modes, undo by
  restore, the three races, refusals, 20 rounds against a running watcher
  (no extra row), and `sc-m1` lists and restores an edit row. Checks:
  build, gofmt, vet, `go test ./...`, `store` and `cmd/sc` as root,
  race on `store` and the watcher test, `make m1-compat`: clean.
- **Your call:** "use multiagent if possible if that can be done
  accurately, not necessary". Chunk C's checkers (sudoers, sshd, units,
  grub, plain rules) are independent: they are built by parallel agents
  in separate worktrees after step 7, and integrated, checked and
  committed here one at a time. Shared code stays in the session.
- **M3 step 7, `sc edit`, `c997865`:** the command of plan 6.2. The editor
  opens a copy; the result is checked against the old content and only
  what the edit added counts; a new blocker or error is explained and
  asked about (edit again, save anyway, quit; end of input quits, exit
  2); saved through `store.Replace` with the file's mode and owner. One
  `sc edit` at a time (a lock); a file that changed on disk meanwhile is
  not written and the edited version is kept under `$SC_HOME/tmp/kept-*`;
  copies an interrupted `sc edit` left are removed by the next one.
  Tests drive it with a script as editor: clean save, unchanged, quit,
  end of input, edit again, save anyway, warning only, an old blocker not
  blamed, new file, changed on disk, refusals, the lock, a failing or
  missing editor, an editor with arguments. Checks: build, gofmt, vet,
  `go test ./...`, `store` and `cmd/sc` as root, race on the new tests
  and `store`, static: clean. Chunk B (steps 5-7) is built; its review
  is next.
- **M3 chunk B review closed**
  ([reviews/2026-10-03-m3-chunk-b.md](reviews/2026-10-03-m3-chunk-b.md)):
  one reviewer, 10 findings, all fixed in `a946f6f`. Two were high and lost
  the user's work: `sc edit` deleted the edited copy when the save
  failed, and a Ctrl-C from inside the editor killed sc and orphaned the
  editor. Now the copy is kept after any failure and its path printed,
  and SIGINT/SIGQUIT are the editor's while it runs. Also: `sc check`
  exits 1 when a file could not be read; ids as `sc cat` takes them; a
  `deleted` row before a creation after an unrecorded deletion; 80-column
  lines. Plan change C5. Checks: build, gofmt, vet, `go test ./...`,
  `store`, `check` and `cmd/sc` as root (one timeout of the M2 test
  `TestWatchSIGHUPRescans` under load; 8 of 8 alone), race on the new
  tests, `make m1-compat`: clean.
- **M3 step 9, `check: sshd`, `4a3451c`** (parallel agent, reviewed and
  integrated here): `sshd -t -f` on the scratch copy. One `sshd-invalid`
  blocker per line of this file sshd names; the text names the keyword,
  never the value; a problem in an included file is a note; deprecation
  notices with exit 0 are nothing. A `HostKey` line naming a key that does
  not exist, when sshd has no other, is a finding (the server would not
  start; the agent's choice, kept). No host key a user may read, a
  missing `/run/sshd` or a missing file is a note, never a clean run. 20
  golden outputs from OpenSSH 9.6p1 on fabricated files. Checks: gofmt,
  vet, the package as user and as root (the real-sshd test included) and
  under race, static: clean. Step 8 (sudoers) follows when its agent
  reports.
- **sshd test fix, `d4ad4e9`:** CI's root pass failed `TestSshdReal`: on
  a runner where ssh.service never ran there is no `/run/sshd`, and sshd
  (as root) rightly does not finish, which the checker reports as a note.
  The test assumed the dev VM's `/run/sshd`; it now expects the note
  there. Checked as root with `/run` hidden in a private mount namespace.
- **M3 step 8, `check: sudoers`, `c588cdd`** (parallel agent, reviewed and
  integrated here): `visudo -c -f` on the scratch copy; one
  `sudoers-syntax` finding per message about this file, text of ours
  (never the line): syntax errors and an alias defined twice are
  blockers, a bad Defaults option or a missing include an error, unused
  or undefined aliases warnings. Problems in included files and anything
  visudo could not read are notes. `sudoers-mode`, changed in review:
  error only where sudo ignores the file (root does not own it, others or
  a non-root group may write it, per sudoers(5)); any other mode than
  0440 is a warning (sudo reads it, only `visudo -c` complains). Note
  from the agent: sudo 1.9.15 recovers from a syntax error by dropping
  the rest of the line; the explanation says so. Checks: gofmt, vet, the
  package as user, as root and as root without `/run`, under race: clean.
- **M3 step 10, `check: systemd units and grub`, `ff952e7`** (parallel
  agent, reviewed and integrated here): `systemd-analyze verify` on the
  scratch copy (keeps the unit's name): a skipped line is
  `unit-unknown-key` (warning), a missing Exec command `unit-exec-missing`
  (error), anything that stops the unit loading or starting `unit-syntax`
  (error); other units' problems and failed man lookups are ignored.
  `/etc/default/grub` with `sh -n` and `grub.cfg`/`custom.cfg` with
  `grub-script-check`: blockers at the line named. Checked as root here
  too (the agent could not): clean.
- **M3 step 11, `check: plain rules`, `7aeb144`** (parallel agent,
  reviewed and integrated here): `nsswitch-no-files`, `preload-missing-lib`,
  `flag-nologin`, `flag-sshd-not-to-be-run`, `hosts-no-localhost`, each
  reading the file as glibc 2.39 does (the agent read glibc's sources).
  Changed in review, on the agent's own warning: `systemd` alone is not a
  local source for `passwd:` (it gives root and nobody, not /etc/passwd);
  my brief had it wrong. Left for later, from the agent: a bad action in
  any nsswitch line makes glibc reject the whole file.
- Chunk C as a whole: gofmt, vet, `go test ./...` as user, the check
  package as root, as root without `/run`, and under race: clean.
- **M3 chunk C review closed**
  ([reviews/2026-10-03-m3-chunk-c.md](reviews/2026-10-03-m3-chunk-c.md)):
  one reviewer, 8 findings, fixed in `41c87bf` except one cleanup. Two were
  high: harmless systemd remarks (`User=nobody`) were reported as "the
  unit does not load" (new `unit-notice` warning), and a bad nsswitch
  action, which makes glibc reject the whole file, went unnoticed (new
  `nsswitch-invalid` blocker; 13 cases checked against glibc 2.39 by
  bind-mounting fabricated files in a private mount namespace). Also:
  sudoers names sudo skips, the mode of a saved version, the walk depth
  of brace patterns. `sc check` with no argument now checks what scd
  records (10 files here; snapd's generated units are left out, as in
  M2). Plan change C6. Checks: build, gofmt, vet, `go test ./...`, the
  check package as root and under race: clean.
- **Timing-bound tests, `5b1d5a5`:** `TestRescanUnderBusyEvents` generated
  files without end, so a slow worker hit the dirty bound for real and its
  (correct) rescans failed the test; it now makes 60 files, below the
  bound, and a mutation that counts rescan marks still fails it (15
  rescans). The overflow tests and `TestWatchSIGHUPRescans` get 60 s
  upper bounds (a full kernel queue to work through; sc watch's 10 s
  rescan gap). Watch package: three race runs in a row clean; `make
  race` clean.
- **M3 step 12, `watch: check after a recorded change`, `580e1d1`:** after an
  automatic file row, the worker queues the path for one checker
  goroutine (a bounded set, 100 paths) and never waits. The checker
  compares the newest content with the content before the first queued
  change and logs only what was added (`T1 /etc/fstab: check: blocker
  RULE, line N: SENTENCE (ID)` at err; errors at warning; warnings at
  notice; 5 lines at most), and `check: ok again` when a reported file is
  clean. No validator output reaches the journal. Not checked: deleted,
  link and digest rows, restore and edit rows, the startup baseline. A
  full queue drops the path with one line. Tests: the lines and their
  priorities, no repeat, ok again, quiet cases, a slow validator does
  not hold up rows, the queue bound (3 times, and under race). Checks:
  build, gofmt, vet, `go test ./...`, `watch` and `cmd/sc` as root,
  `watch` under race: clean. Not installed (gate G2).
- **M3 step 14, `sc scope`, `7ccb3d9`** (built while the step 13 agents
  work; no shared files): explains a path, one property a line: recorded
  or not and the deciding scope line (or the excluded directory above
  it, sc's own directory, or outside every watched directory), tier and
  its line, fingerprint-only and its line, checker and when a change
  applies. Uses the machine's scope (login `.ssh` roots, SC_HOME left
  out). `scope.Explain` is tested to agree with `Recorded`, `Tier` and
  `FingerprintOnly`. Scope lines go on their own line (80 columns).
  Checks: build, gofmt, vet, `go test ./...`, the new tests as root:
  clean.
- **`sc check --as PATH FILE`, `c53180f`** (plan change C7): checks a
  candidate as if it were at PATH, before it is copied there; rules about
  the file on disk at PATH do not apply. It lets the acceptance run give
  every checker fabricated content as root with the real validators,
  without writing a broken fstab, sudoers, netplan or passwd anywhere.
- **M3 step 13a, `check: netplan and udev`, `a73dfde`** (parallel agent,
  reviewed and integrated here): netplan merges the file with the
  machine's other netplan files in a scratch root and runs netplan's
  generator with `--root-dir` (traced as root here: it starts only
  `systemctl is-system-running` and writes only under the scratch root);
  `netplan-invalid` blocker, texts never repeat a value (Wi-Fi
  passwords). The Runner takes the generator's full path from a
  one-entry list. udev: `udevadm verify --no-style`, `udev-invalid`
  (error) and the new `udev-notice` (warning). Checks: gofmt, vet, the
  package as user, as root and under race: clean. Step 13b (passwd,
  group, sysctl) follows when its agent reports.
- **M3 step 13b, `check: passwd, group and sysctl`, `2e4d4ee`** (parallel
  agent, stopped at the usage limit before it reported; its worktree
  survived). It had left one mutation of its own self-check applied in
  `sysctl.go`: restored from its backup, and the mutation confirmed to
  fail a test; its temporary capture test removed. Reviewed here without
  its report: pwck/grpck run read-only on the copy and a made-up shadow
  file (the real hashes are never read; traced as root: no writes, no
  lock), severities from glibc's parsing, missing home directories are
  no finding, quoted lines never reach a finding; `passwd-root`,
  `passwd-shell-missing`. sysctl `--dry-run` traced as root: no writes.
  The real passwd, group, sysctl.conf and sysctl.d files check clean.
  Checks: gofmt, vet, the package as user, as root and under race.
- **`scripts/accept-m3.sh`, `2ff038b`:** committed now that every rule it
  names exists; its step 1 (`sc check --as`, all checkers) passes as a
  user. The rest needs root and writes a test unit under
  `/etc/systemd/system`: sign-off S1, with your OK.
- **Chunk D review closed, `2fb037e`**
  ([reviews/2026-10-03-m3-chunk-d.md](reviews/2026-10-03-m3-chunk-d.md),
  plan change C8). One reviewer agent, 10 findings, each reproduced or
  tested here first.
  - `passwd-root` was a false blocker on stock Ubuntu: nss-systemd
    supplies root when no line does. Shown with a fabricated passwd
    bind-mounted in a private mount namespace: `getent` gives uid 0 and
    sudo still works. It is now a warning there; a root line with another
    uid stays a blocker.
  - netplan: an error that may come from a file sc could not read is a
    note.
  - `sc check --as` no longer waits on a FIFO.
  - `sc scope` checks the roots with lstat, as scd does.
  - scd's checker validates each version once, and still checks the new
    version when the old one fails.
  - The sysctl finding did not reproduce on procps 4.0.4.
  - Cleanups: `Explain` is built on `decide`; one no-login list and one
    `PathError` helper.

  Checks: gofmt and vet clean; all tests pass as a user, as root and
  under race. A stray `sc` binary from `go build ./cmd/sc` got into the
  fix commit; it was taken out before the push, and `/sc` is now
  ignored.
- **M3 step 15, docs.**
  - README: M3 status; `make accept-m3`; a new section, "Check before
    it breaks", with `sc check` (`<id>`, `--as`, `-v`, exit codes),
    `sc edit`, `sc scope`, what each checker runs, and scd's check
    lines. The example output comes from a made-up fstab.
  - MILESTONES: the current state, and M3 notes with their deliberate
    limits.
  - CLAUDE.md: `accept-m3.sh` added to the layout.

  Every relative link resolves.
- **M2 sign-off S6 closed after 5.6 h** (your call: finish M3 now). scd
  on the `b6ab3cc` build from 14:27 to 20:04 UTC:
  - Health: 0 restarts, 16 MB of memory (21.6 MB peak), 8.9 s of CPU.
  - Journal: 2 lines, the start and the baseline (0 first seen,
    0 changed, 0 deleted, 368 ms). Nothing at warning or above.
  - Store: integrity ok, 1546 rows, the same as after the S5 upgrade.
    No new row and none unexplained.
  - `/etc` and `/boot/grub` against `manifest-post-s5apt.txt`: only
    `cups/subscriptions.conf{,.O}` changed (cupsd rewrites them; the
    scope leaves them out).

  Not covered: `apt-daily-upgrade` did not run (skipped: the VM reports
  no AC power), and the deliberate `apt upgrade` was S5's rerun, before
  this build. A longer run and an apt run under scd come with M3's
  install.
- **M3 sign-off S1 passed** (your OK: "close soak now, sign off").
  - Checks: `make build fmt vet m1-compat` on `36fa076`; the tests last
    passed as a user, as root and under race on `2fb037e` (only docs
    changed after); CI green.
  - `sudo ./scripts/accept-m3.sh`, with the real scd stopped:
    - First run: steps 0-3 passed, step 4 failed on a script bug. After
      a piped answer, sc edit's "saved" line follows the prompt on the
      same line, and the script's grep wanted it at the start of a line.
      Fixed in `6db9415`, which also makes `fail()` print its newlines.
    - Second run: **PASS**, all 6 steps. Every validator was found and
      run as root. `sc check` on the real system: no problems in 24
      files. scd's check line and "ok again" for the test unit; sc edit
      through quit, edit again and save anyway with exactly 2 rows;
      `sc scope`. The real fstab, sudoers, sshd, grub, nsswitch, hosts,
      passwd, group, sysctl, netplan and sudoers.d files are unchanged.
    - After each run the test unit, the runtime unit and the scratch
      directories were gone. The real scd started again with a baseline
      of 0 first seen, 0 changed, 0 deleted.
- **M3 sign-off S2** (your yes). The M3 build of `468c633` (Go code as in
  `2fb037e`; sha256 `8e96663f…`) is installed as `/usr/local/sbin/sc`.
  The b6ab3cc build it replaces is kept as
  `/var/backups/smartconfig/sc-b6ab3cc` (sha256 `396f84cb…`); to roll
  back, install it again and restart scd. The unit file did not change.
  scd restarted at 20:15:47 UTC: baseline 0 first seen, 0 changed,
  0 deleted (1.1 s), 16 MB.
  - `sudo sc check`: no problems found in 24 files, exit 0. There is
    nothing to review.
  - As a user: no problems in 19 files, exit 1. Five files only root may
    read (grub.cfg, a 0600 netplan file, sudoers and two drop-ins) each
    get a note, and so do three checks that were incomplete without root
    (fstab types, the netplan merge, sshd's host keys).
- **M3 sign-off S3, the owner scenario, passed** (your yes; Claude ran
  the editors from a script).
  - fstab: `sudo sc edit /etc/fstab`, where the editor adds a data disk
    with a mistyped UUID (made up). sc showed `fstab-source-missing` as a
    blocker on the new line, with its explanation (a 90 s wait, then
    emergency mode with root locked), and the prompt. Answering `q`
    gave "not saved" and exit 2. fstab kept its sha256 (`9d71ab60…`), no
    row was added and no copy left behind.
  - sshd: `PermitRootLogn no` appended in place to the real
    `/etc/ssh/sshd_config`, as nano saves. Within about 1 s scd logged
    the row (`c89eb3`, T2) and `check: blocker sshd-invalid, line 132`
    at err. `sudo sc check` showed the same, exit 2. `sc restore 2043f5`
    put the file back: sha256 `64325541…` as before, 0644 root:root,
    `sshd -t` clean, `sc check` clean, ssh.service never restarted.
  - Seen here, for the final review: scd says nothing after a restore
    (restore rows are not checked, plan section 7), so no "ok again".
    It also still holds sshd_config as failing, so an unrelated later
    change would log "ok again" at that point.
- **M3 sign-off S4: final review closed**
  ([reviews/2026-10-03-m3-final.md](reviews/2026-10-03-m3-final.md),
  plan change C9).
  - Three reviewer agents, each in its own clone: security, checker
    correctness against the real tools, and operations.
  - Two high findings, both fixed in `297fbc7`:
    - fstab bind, image and swap-file sources were never looked for.
    - An emptied or admin-less passwd, group or sudoers file passed,
      though each locks Ubuntu's owner out (shown with sudo in a private
      mount namespace).
  - Fixed in `297fbc7` and `bb31f3e`: 6 of 7 medium findings, among them
    a relative `SC_HOME` giving a false clean and a timed-out validator
    giving "ok again"; plus 7 low ones and a cleanup, among them "ok
    again" at a restore (the S3 gap).
  - Eight items go on the follow-up list in MILESTONES, the largest a
    checker for unit drop-ins.
  - Checks: gofmt and vet clean; all tests pass as a user, as root and
    under race; `make m1-compat` passes; CI green.
  - `accept-m3.sh`, with cases for the new rules, passed again as root
    on `bb31f3e` with scd stopped and started again around it.
  - `sc check` as root on the real system: no problems in 24 files.
- **M3 done, tag `m3`** (your yes: "install and tag"). The fixed build
  (code of `bb31f3e`, sha256 `cf5ba078…`) is installed as
  `/usr/local/sbin/sc`; the S2 build is kept as
  `/var/backups/smartconfig/sc-m3pre`. scd restarted at 21:25 UTC:
  baseline 0 first seen, 0 changed, 0 deleted, 14 MB. `sudo sc check`:
  no problems in 24 files. The annotated tag `m3` is on `c253cfd` and
  pushed.

### 2026-10-04

- **M4 boot research done** (QEMU, TCG, Ubuntu 24.04 cloud image,
  root locked; your call "QEMU research, then plan"). Results:
  - The rescue recipe `ro fstab=no systemd.unit=rescue.target
    SYSTEMD_SULOGIN_FORCE=1`, from a generated GRUB entry, gives a root
    shell with a read-only root in about 40 s. It also works under
    Secure Boot.
  - The static sc reads the store there. A hot journal blocks the read
    (SQLITE_READONLY_ROLLBACK) until the store is copied to /run.
  - `sc restore` after a remount fixed a bad fstab, and the next boot
    was healthy (an end-to-end script passed in 408 s).
  - Ubuntu's sulogin opens a root shell in emergency mode although root
    is locked. Checked here too: util-linux 2.39.3-9ubuntu6.6 changelog,
    `sulogin-lockedpwd.patch`.
  - recordfail brings the menu back only for failures that never reach
    multi-user. boot-complete.target is unused on Ubuntu.
  - The agent was cut off twice, by the usage limit and by a dropped
    connection, and resumed with its context both times. The lab was
    moved out of /tmp, which Ubuntu empties at boot, to
    `~/smartconfig-work/m4lab`, its overlays rebased onto relative
    paths.
- **M4 plan drafted** ([M4_PLAN.md](M4_PLAN.md)): 13 steps in five
  chunks, no store schema change (boot verdicts in `$SC_HOME/boots`, so
  sc-m1, sc-m2 and sc-m3 keep reading the store), and an automated
  QEMU owner scenario (`make lab-e2e`). Waiting for your approval.
- **M4 plan approved** (your "approved": every recommendation of its
  section 10). Building starts with step 1.
- **M4 step 1, "Ubuntu's emergency mode gives a root shell"** (commit
  below).
  - Rule explanations corrected (fstab-source-missing, sudoers-no-rules,
    group-no-admin), with a test against "no shell" claims; it fails on
    the old texts.
  - PROJECT_LOG's recovery guide rewritten: Enter at "Press Enter for
    maintenance" first, then the lab's rescue arguments, `init=/bin/bash`
    last; the backup binaries listed as they are now.
  - docs/README and JOURNEY corrected.
  - The "without SmartConfig" film rewritten to the real 24.04 screen (a
    shell that tells nothing, a blind fix); it loads in headless Chrome
    with its chapters built.
  - Checks: gofmt, vet and tests clean.
- **M4 step 2, the store on a read-only root.**
  - Opened with `mode=ro` where `access(2)` says sc may not write. Every
    write returns "remount it read-write first".
  - A hot journal is read from a repaired private copy (`/run` for root),
    and the store is untouched. Tested by chmod as a user with the
    existing crash helper; a mutation gives the lab's error 776. Checked
    by hand with a SIGKILLed Python writer: one note line, the committed
    rows, and a self-repair once writable.
  - Plan change C1: an older schema on a read-only root is an error, not
    read through old columns.
  - Checks: gofmt, vet, tests as a user, as root and under race, and
    m1-compat clean.
- **M4 step 3, scratch fallback for `sc check`.**
  - With `$SC_HOME` and `$TMPDIR` read-only, the copies go to `/run`
    (root) or the runtime directory, then `/dev/shm`, and are removed.
    With none writable, one line names the places tried.
  - Plan change C2: no "own rules only" check without any scratch
    directory, since several checkers write companion files first.
  - Checks: gofmt, vet and tests clean; cmd/sc and store pass as root
    and under race.
- **M4 chunk A review closed**
  ([reviews/2026-10-04-m4-chunk-a.md](reviews/2026-10-04-m4-chunk-a.md),
  fixes `49a0a5b`).
  - One medium finding: a repaired store copy or a scratch directory
    outlived a signal or a broken pipe (`sc log | head` left a full copy
    in /run per run). They are now tracked private directories, removed
    on signals and swept after an hour, and SIGPIPE is caught.
  - Low findings: four surviving mutations and no root coverage, now
    tested (including a read-only bind mount in `unshare --mount`);
    migrations on the copy; a journal race; messages; the copy's
    location; two film lines.
  - Also: a watcher test's 5 s wait (a flake under a loaded root pass)
    is now 60 s.
  - Checks: tests pass as a user, as root and under race, and
    m1-compat passes.
- **M4 step 4, boot verdicts.**
  - `internal/boot` keeps `$SC_HOME/boots` (seen and verdict lines,
    append-only, trimmed past 1 MiB). The hidden `sc boot seen` and
    `sc boot verdict` ask systemctl through the M3 runner.
  - On this VM, as a user with a throwaway home, the verdict is "ok",
    with failed-units=2 (whoopsie).
  - Plan change C3: failed units are counted, not decisive; otherwise
    every boot here would be "bad" and keep the menu showing.
  - Checks: gofmt, vet and tests clean; boot, cmd/sc and store pass as
    root and under race.
- **M4 step 5, the boot units.**
  - `scripts/sc-boot-seen.service` runs early, outside `local-fs.target`,
    so a boot that fails on a disk is still seen. It waits only for the
    store's filesystem (`RequiresMountsFor`): the lab prototype's
    `Before=local-fs.target` would write under a separate `/var`'s mount
    point.
  - `scripts/sc-boot-ok.service` runs after `multi-user.target`.
  - `systemd-analyze verify` is clean for both. They are not installed;
    that is sign-off S2.
- **M4 step 6, `sc status`.**
  - It shows this boot, the last healthy boot, the boots that failed
    since, and scd's state (read from /proc, never its lock).
  - It lists the files changed since the last healthy boot with the
    problem each change added, and gives the undo commands for the
    newest blocker; in the rescue boot, with remount and reboot.
  - `--console` caps time at 10 s and output at 20 lines, but judges
    every change, a fix found while testing.
  - Store.Rows and Store.AsOf.
  - Checks: tests pass as a user, as root and under race.
- **M4 chunk B review closed**
  ([reviews/2026-10-04-m4-chunk-b.md](reviews/2026-10-04-m4-chunk-b.md),
  fixes `40288d8`, plan change C4). The reviewer also booted the units
  in a throwaway QEMU guest.
  - One high finding: `sc-boot-seen`'s 10 s timeout killed it on slow
    boots, so the failed boot left no trace. It no longer holds up
    sysinit and gets 90 s.
  - Medium: the healthy boot after a fix still reported failure; the
    console's time cap could drop a blocker; the console did not fit
    80x25.
  - Low: eleven more, fixed or deferred (the scd rescan race).
  - The ten surviving mutations are now tested.
  - Checks: tests pass as a user, as root and under race.
- **M4 step 7, `scripts/42_smartconfig`.** It adds the "SmartConfig
  rescue" GRUB entry: the newest kernel with an initrd, root= as
  10_linux gives it, and the lab's rescue arguments. It also adds the
  `smartconfig_pending` menu flag. btrfs and ZFS roots get no entry.
  Tested on fabricated `/boot` trees; `grub-script-check` accepts the
  output.
- **M4 step 8, the menu flag.** `sc boot seen` sets
  `smartconfig_pending=1` in grubenv, and a healthy verdict unsets it.
  It is left alone where grubenv is not GRUB's 1024-byte block, such as
  a separate /boot that is not mounted yet. Tested with a fake and with
  the real `grub-editenv`.
- **M4 step 9, the rescue drop-in.** `scripts/smartconfig-rescue.conf`
  goes into `rescue.service.d` and `emergency.service.d` and runs `sc
  status --console` before the shell. `systemd-analyze verify` accepts
  both services with it.
- **M4 step 10, the `+i`/`+a` precheck.** `sc restore` and `sc edit`
  refuse an immutable target, or one in an immutable or append-only
  directory, before writing anything, and name the `chattr` to run.
  Tested as root with chattr.
  - Checks: tests pass as a user, as root and under race, and m1-compat
    passes.
- **M4 chunk C review closed**
  ([reviews/2026-10-04-m4-chunk-c.md](reviews/2026-10-04-m4-chunk-c.md),
  fixes `683678a`, plan change C5).
  - The review: 11 lab boots, UEFI and BIOS; nothing high or medium; 8
    low findings and 5 cleanups.
  - The fixes were then checked by a four-agent adversarial workflow
    (ultracode). It refuted one fix (the console could still overflow
    80x25) and raised 18 smaller issues; all are fixed in the same
    commit.
  - The step 11 design workflow, run alongside, found one more bug: in
    the emergency drop-in, `sc status` said "normal" because only the
    service is activating there. Fixed.
  - Deferred: multipath `root=`, a whole-boot ordering test.
  - Checks: tests pass as a user, as root and under race; sc builds for
    six architectures.

### 2026-10-05

- **M4 step 11, the QEMU rescue lab** (`9f29ea5`). `lab/` runs the M4
  owner scenario in a VM under TCG, for UEFI and BIOS: a healthy boot,
  an fstab edit, the broken boot, GRUB's menu and the rescue entry, `sc
  status --console` and `sc restore` in the rescue shell, the healthy
  boot after. Dev only: no sudo, no KVM, not in CI. `make lab-image`
  once, then `make lab-e2e`; `make lab-test` runs its 157 unit tests.
  - Built from the design workflow's spec by agents in a worktree, then
    hardened against false PASSes by an ultracode workflow (9 risks, each
    fix checked by undoing it).
  - Runs on the uncommitted tree (not sign-off): UEFI PASS twice (40m,
    32m), BIOS PASS (30m) after four lab fixes its earlier runs found.
  - Product finding, fixed first (`55d2bde`): on BIOS the console getty
    can start next to `emergency.service` and hang up the console; `sc
    status --console` then died by SIGHUP and the report was lost. It now
    drops SIGHUP in sc's own handler (not SIG_IGN, which the commands
    it runs would inherit; that leak failed `TestWatchSIGHUPRescans`
    first). The lab now fails 2.5 if `sc: interrupted by hangup` shows.
  - The local secret-scan hook read the lab's raw serial captures as
    binary and skipped the commit's added lines. It now reads them as
    text (`grep -a`); both commits were scanned again and are clean.
  - Checks: tests pass as a user, as root and under race; `make
    lab-test` passes.
- **M4 step 11 check, first round (`618e4e9`): UEFI PASS, BIOS not yet.**
  - `618e4e9` came first, from a BIOS run that ended INCONCLUSIVE at
    1.0: ssh lost the reboot command (exit 255) and the lab gave up. It
    now asks the guest and sends the command once more only when no
    reboot is queued.
  - UEFI on `618e4e9`: PASS in 30m51s, no retries, clean tree, with
    boot 5; boot 2 ended as outcome a.
  - BIOS run 1: cut off at 1.5 by a signal when the session ended.
    Nothing to read from it.
  - BIOS run 2: INCONCLUSIVE in 40m22s. Every check up to 4.6 passed,
    with boot 2 as outcome b (`goal3=multi-user`, which sign-off needs
    once). In boot 5 the guest kernel stopped at `smpboot: x86: Booting
    SMP configuration:` for 18 minutes, before any userspace; the lab
    gave up after 900 s (5.1, [lab]). The same step took 0.1 s in the
    boots before it. A TCG stall, seen once; the lab does not retry it,
    and that is left so unless it comes back.
  - BIOS run 3: FAIL in 36m05s, at 2.5 alone ("no report above 'You are
    in emergency mode'"); boot 5 passed. The report was on the console,
    whole and right. About 5 s later the serial getty's hang-up ended
    the emergency shell before its first line: the console had the
    report, then a login prompt, and no shell. The lab looked for the
    report only above the shell's line.
  - Lab fix (`bec333b`): with no shell line in boot 2, 2.5 takes the
    block above the login prompt and holds it to the golden as
    strictly; a wrong report or none is still an [M4] failure. Three
    tests from that run's console text; that run's whole boot 2,
    replayed, now gives 2.5 a note and no failure.
  - For the chunk D review: in that ending the owner sees the report
    and a login prompt, not a root shell, so its commands need a login
    and `sudo` first. It is Ubuntu's race (plan A2), and sc's report
    came through; whether the report or the README should say so is
    open.
  - Checks: `make lab-test` passes (166 tests). No Go file changed.
- **M4 step 11 check, second round (`225e2d6`): no verdict; the lab now
  retries a stall (`152ecbe`).**
  - UEFI: INCONCLUSIVE in 16m59s at 1.1. Boot 0 passed (0.1 to 0.7). In
    boot 1 the firmware started shim, and the console then stood still
    for 596 s with no line of GRUB's (it took 0.5 s in the run that
    passed). A lab stop, not a fault of sc: GRUB never reached its
    configuration.
  - BIOS: cut off at 0.3, four minutes in. This VM went down at about
    17:15 UTC without a shutdown (`last` says crash) and came back at
    17:18. The same happened at 04:54 (BIOS run 1 of the first round)
    and at 06:00. A whole `make lab-e2e` needs the VM up for over an
    hour.
  - That is the second stall in four whole runs, so it is no longer
    left alone. Neither run says whether QEMU's CPU spun or the host
    was paused.
  - Lab change (`152ecbe`): a stall is a [lab] retry, once per boot,
    only where sc has no part in the boot yet: before GRUB ran its
    configuration (no observer line, no menu for a whole
    `BUDGET_MENU`), or in the kernel before `Run /init` in a boot that
    should reach a login prompt. Anywhere else it stays INCONCLUSIVE.
    Each stall leaves `evidence/stall-*.txt` (QEMU's CPU time over
    5 s, its registers twice, the gaps in the lab's own running), so
    the next one says what stood still.
  - Found on the way: x.1 held a retried attempt to `style=[hidden]`.
    After a reset later than GRUB, Ubuntu's recordfail is still 1 and
    no style is set, so a retry after a panic would have failed x.1
    as [M4] on UEFI. It now expects `style=[]` there (sc's flag block
    must change nothing without the flag).
  - Checks: `make lab-test` passes (174 tests); the failed run's boot 1,
    replayed, is a stall retry; the registers command and the CPU time
    reader were tried against this QEMU. No Go file changed.
- **M4 step 11 check, third round (`56f5359`): `make lab-e2e` PASS.**
  - UEFI: PASS in 15m18s, no retries, `dirty=no`, with boot 5. Boot 2
    ended as outcome b (`goal3=multi-user`, which sign-off needs
    once). One warning, 2.5: scd was already running when the report
    was made.
  - BIOS: PASS in 25m02s, no retries, `dirty=no`, with boot 5. Boot 2
    ended as outcome a. Two warnings, 3.1 and 4.1: no observer line on
    the VGA screen, as the design expects where the menu wipes them.
  - No stall, so the new retry did not run. The new gap notes did: in
    the BIOS run the lab's own process stood still four times, for 19,
    233, 16 and 126 s, all in boot 5, and the guest's console jumped
    by the same lengths. So what looked like a TCG stall is, at least
    here, this whole VM not running on its host. Longer pauses of the
    same kind would explain the 10 and 18 minute stops of the earlier
    rounds; those runs had no gap notes, so that is not shown. The
    UEFI run had no gap and took 15 minutes.
  - Evidence kept in `~/smartconfig-work/signoff/lab-e2e-56f5359/`
    (result, log, ledger, mux log per mode) and the run's own log
    beside it.
- **M4 chunk D review: fixes, their check, two more fixes; paused.**
  - Two reviewers: the checks and verdicts, and the machinery with the
    Go fix. Fixed in `3115b71` (sc: `status --console` stops itself
    after 60 s, since systemd ends the rescue unit, shell and all, at
    90 s; the hangup flag set earlier; a login hint in the emergency
    report) and `9d3df9e` (lab: ssh loss and host pauses never an [M4]
    result, product faults never INCONCLUSIVE, one-shot signals, the
    cache must be the lab's, one lab per user, the recordfail rule by
    attempt).
  - `make lab-e2e` on `85e9f59`: FAIL, both modes, no retries.
    UEFI 3.6: the rescue report on ttyS0 ended mid-row; sc gave each
    console one 2 s write. BIOS 3.4: the rescue kernel's first
    "Command line:" lost one byte on serial ("oot=UUID="), after the
    host stood still 379 s; its second print was whole. The host was
    loaded by the test agents beside the run.
  - `1d25037`: a console keeps its turn while it takes bytes or its
    queue drains (5 s stalled, 10 s at most). `64911f4`: the lab reads
    the kernel's second command line where the first lost a byte;
    `check_decision`'s bios notes were lost (found by a test agent).
  - Checks on `64911f4`: tests as a user, as root and under race;
    `make lab-test` (196).
  - Not run yet: `make lab-e2e` on the fixed tree.
- **M4 chunk D review: the scenario tests are in (`fcb1b6d`).**
  - The review's mutations of `lab/e2e.py` (a check's condition removed
    or loosened; 261 on the fixed code, 115 of the first 158 had
    survived `make lab-test`) now all have a test: `execute()` end to
    end with the VM and the flow stubbed (exit codes, NOTRUN, the
    ledger, teardown, the summary line); one fault at a time in every
    check from 0.5 to 5.1, on the text and facts of the passing run of
    `56f5359`; the glue (`boot()`'s flag and limits, `flow()`,
    `wait_healthy_end`, `panicked`, `wait_for`, `wait_ssh`, the
    preflight). `lab/test_e2e.py` only, +1873 lines; `make lab-test`
    196 -> 283, three runs in a row, 12 s each; `go test ./cmd/sc/` ok.
  - One test failed against `64911f4`: `test_wait_for_and_panics`
    expected a Retry from a panic and got None. `64911f4` had given
    `FakeRun` a `wait_for` of its own (the fake console then had no
    `expect`), which shadowed the real `E2E.wait_for` the test drives.
    The new tests give `FakeCon` an `expect`, so that override is
    removed and both tests run the real method; each still fails when
    its code path is broken (the panic branch; the second command line).
  - Mutations after, on `fcb1b6d` (`scratch-a/mutate.py -j 6 --fast`
    on copies of clone-a): 260 killed, 0 survived. The 261st,
    "check_cmdline ignored", no longer applies: `64911f4` rewrote that
    text.
  - Still to do: the machinery tests (clone-b), then `make lab-e2e`.
- **M4 chunk D review: the machinery tests are in (`a371738`).**
  - The review's mutations of `lab/vm.py`, `lab/console.py`,
    `lab/serialmux.py` and the guest scripts (`clone-b/.rv/mut.py`:
    123 Python, 23 shell, of which 68 and 13 had survived) all have a
    test now: 123 of 123 and 23 of 23 killed on `a371738`. The one
    shell mutant that still passed, "hashes-after cached", added a bare
    `:` and changed nothing; it is replaced by the block dropped.
  - Covered with stand-ins (never a QEMU, a download or the real cache;
    `SC_LAB_CACHE` and `XDG_RUNTIME_DIR` point at temp dirs): the pinned
    image, the key, the reference image (three 0444 files, the qcow2
    last, a failed `qemu-img check`, the kernel pin, panics retried),
    QEMU as a child (own session, `PR_SET_PDEATHSIG`, quit then
    SIGKILL) with a stand-in script, Qmp on a fake unix-socket server,
    `SSH_OPTIONS`, `SeedServer` on 127.0.0.1, `running_qemu`'s uid,
    `render_seed`, `fill_template`, `classify_boot`, `parse_menu`,
    `pick`, `report_block`, `expect`, the mux's send timeout and wait;
    `install.sh`'s step rcs and not-root refusal, `facts.sh`'s timeout
    per part (the mode's limit), a part's stdin, a failing `sc cat`.
    `lab/test_lab.py` and `cmd/sc/unit_test.go` only, +998 lines;
    `make lab-test` 283 -> 325, three runs in a row, 15 s each;
    `go test ./cmd/sc/` ok; gofmt and vet clean.
  - Seen on the way, not of this step: `test_the_first_signal_is_the_
    only_one` (from `9d3df9e`) is load-bound. With four CPU burners
    beside it, 3 of 20 runs had the child hang past the 60 s limit
    (SIGHUP twice, SIGTERM once); once, with `go test` beside
    `make lab-test`, the SIGINT case ended "SystemExit 143" with no
    teardown. The same child script run directly under the same load:
    0 hangs in 120. Not understood yet; `make lab-test` alone passes.
    Also `test_the_loop_measures_its_wakes` (from `152ecbe`) failed
    once in the repo, right after a `go test` loop: it expects the
    late wake to measure exactly "0.2 s". One `go test ./cmd/sc/` of
    14 failed right after three `lab-test` runs; its output was not
    kept, and 13 later runs passed. In the repo after the merge:
    `make lab-test` three runs in a row ok (22 s), `go test ./cmd/sc/` ok.
- **M4 chunk D review: `make lab-e2e` on `fc47ea3` FAILed; two fixes.**
  (Log: `~/smartconfig-work/signoff/lab-e2e-20261005T233510Z-fc47ea3.log`;
  runs `20261005T233511Z-uefi-fc47ea3`, `20261006T001025Z-bios-fc47ea3`.)
  UEFI INCONCLUSIVE at 5.2 only; BIOS 5.2 the same and 2.5 FAIL. Every
  other row passed, boot 2 outcome b, no retries.
  - 5.2, both modes, the lab (`0ccba27`): the guest powered off, QEMU
    sent SHUTDOWN (qmp.log +2110.886) and exited 0, the lab said "no
    SHUTDOWN after poweroff (QEMU exited)" at once. The QMP reader runs
    the listeners before an event is in `events`; e2e's listener is
    `mux.mark`, which since `9d3df9e` drains up to 1 s, and the serial
    loop was reconnecting (marks.log: SHUTDOWN#24 at +2111.96). In that
    second `wait_event`'s abort (QEMU not alive) returned None. Now an
    abort waits for the reader's EOF (5 s at most) and looks once more.
    Test: a fake QMP sends SHUTDOWN and closes under a slow listener with
    abort true; it failed on the old code. `make lab-test` 326, three
    runs; `go test ./cmd/sc/` ok.
  - bios 2.5, sc (`c70d886`): no line of the report on ttyS0 in boot 2.
    Boot 2's journal (the kept disk booted once through an overlay,
    TCG, deleted after) shows the host pause: no entry between
    monotonic 107.868 and 118.201, both guest clocks jumped 10.3 s
    (mux.log "+545.63 gap 10.0 s"), and emergency.service's job began
    at 107.825, so `sc status --console` ran across it. `writeConsole`
    measured the stall in wall-clock time; the slice over the pause,
    with ttyS0's queue busy with systemd's flood, counted as a 10 s
    stall and ttyS0 was given up with nothing written (tty1 took the
    report, so no stdout fallback). Ruled out on a pty: Write gives
    `ErrDeadlineExceeded`, not EAGAIN, and the old writer waited the
    whole 5 s. Now each slice counts at most `consoleStep` (500 ms)
    towards stall and turn: one pause is one slice. Test
    `TestWriteConsoleHostPause` failed on the old writer (0 of 1260
    bytes). `make build fmt vet test race`, the root pass, `make
    lab-test` ok. Not changed: the report text, the goldens.
  - Seen on the way: a Type=idle unit is active (start-pre) from its
    first moment, so "Started emergency.service" on serial dates the
    unit's start, not the ExecStartPre's end (probe with a transient
    user unit). systemd's own status lines after 126.9 (guest) are in
    the journal but not on ttyS0 in that boot; not understood, not
    needed for the fix. Next: step 2 again, `make lab-e2e` on `c70d886`.

### 2026-10-06

- **M4 chunk D review closed: `make lab-e2e` PASS on `22383a0`, both
  modes; write-up and plan change C6.**
  - Run 1 (log `~/smartconfig-work/signoff/lab-e2e-20261006T011750Z-22383a0.log`):
    UEFI PASS in 24m29s, 0 retries, `boot2=b goal3=multi-user`, boot 5,
    `dirty=no`, through host pauses of 167 and 318 s. BIOS INCONCLUSIVE
    at 5.2 only: the lab's `cat boot_id` over ssh timed out (180 s)
    across a 384 s host pause; every [M4] row passed.
  - Run 2, `LAB_MODES=bios` on the same tree (log `...T020514Z-22383a0-bios.log`):
    BIOS PASS in 45m14s, 2 lab retries, `boot2=b goal3=multi-user`,
    boot 5, `dirty=no`. Boot 3: a menu-missed retry. Boot 5: the first
    real stall retry, the kernel at `smpboot` for 900 s;
    `evidence/stall-5-1.txt` shows mux gaps of 250, 364, 400, 400 and
    265 s and a 5 s CPU sample spanning 265 s: the host stopped this
    VM for most of 24 minutes. Neither retry is sc's. Both runs on the
    same commit and the same `sc` (`a0502d6461db`); evidence in
    `~/smartconfig-work/signoff/lab-e2e-22383a0/`.
  - Write-up `docs/reviews/2026-10-05-m4-chunk-d.md`: the 29 findings
    and item 6 with their fix commits, the three verification runs,
    the mutation work (261 of 261 scenario, 123 of 123 and 23 of 23
    machinery, the `check_decision` notes bug), and the open items:
    the host's pauses, an ssh retry across a pause, systemd's lines
    missing on ttyS0, the mux's 1 s drain, two load-bound tests, one
    unkept `go test` failure, `facts.sh` pipe statuses (B14), the
    hangup window before `signal.Notify` (B5), and the README's and
    the registry's stale "2.5 is a W in c".
  - Plan change C6 (`docs/M4_PLAN.md`): the 60 s limit of
    `sc status --console`, the console writer's turn (5 s stalled,
    10 s at most, a host pause one slice), the login line in the
    emergency report, the hangup flag from `main`; replaces C4's "no
    time cap" and chunk C's 2 s write.
  - Docs only, no code; a check-in every 30 min resumes work after a
    usage limit; no VM. Next: step 12, `scripts: accept-m4.sh`, not
    built yet (build it in the repo with its checks; its run as root
    needs the user's OK, sign-off S1).
- Follow-up to the chunk D write-up: its open item on the stale 2.5
  text is fixed (`4ce59a3`: `lab/README.md` and the registry text in
  `lab/e2e.py` say F in every outcome, as `check_emergency_report`
  does; `make lab-test` 326 ok three runs, `go test ./cmd/sc/` ok).
  This worklog's "Now" corrected: step 12 is not built yet, and the
  30-minute check-in does run.
- **M4 step 12: `scripts/accept-m4.sh`** (`3e72281`), `make accept-m4`:
  the parts of M4 that need no reboot, as root on this VM, bin/sc on a
  throwaway `SC_HOME`, nothing real written. The two boot units as
  runtime units with a copy of grubenv bound over the real one (seen and
  the flag, then this boot's verdict, which must be ok, and no flag);
  `sc status` and `--console` on a made-up fstab with a missing disk,
  recorded from a private mount namespace; restore's chattr +i/+a
  refusals; a read-only store on a tmpfs and a copy with a hot journal;
  `grub-mkconfig` on copies of `/etc/grub.d` with and without
  `42_smartconfig`, in private mount namespaces; `sc status` on the real
  store; the real files' checksums. Checked as a user: `bash -n`, the
  status, read-only and hot-journal sections against bin/sc (boots lines
  written by hand, the fstab row made in a test database: unprivileged
  user namespaces are off on Ubuntu 24.04), the grub.cfg checks on this
  machine's kernel and root, both runtime units through `systemd-analyze
  verify`, `make build fmt vet test`. Not run as root: that is S1 and
  needs the user's OK. This VM had gone down again before this session
  (about 11:30 UTC, idle, `last -x`: crash); the check-in was recreated.
- **M4 step 13: the docs** (by the 30-minute check-in, after step 12).
  - README: M4 in the status; `make accept-m4`, `lab-test` and `lab-e2e`;
    a section "When the machine does not boot (M4)": what the rescue
    path gives, the rescue report as the lab's UEFI run of `22383a0`
    printed it, what to type, the emergency-mode login prompt and the
    rescue shell's quiet console (chunk D item 6), the install by hand
    and its removal, `sc status`'s exit codes, sc on a read-only root,
    the chattr refusal, where it does less, and the security note with
    a GRUB superuser recipe (`--unrestricted` on 10_linux's default
    entry only), marked as not tested in the lab yet.
  - MILESTONES: the M4 line and "Current"; M4 notes with the plan, the
    reviews, deliberate limits and what is open from the reviews.
  - PROJECT_LOG: last updated, status, the lab cache and scratch rows,
    this VM's state (kernel 7.0.0-38, BIOS, the M3 build as scd, the
    backups; M4 not installed), a recovery section for the rescue entry
    beside the one without it, the resume prompt.
  - CLAUDE.md: `internal/boot` and the scripts in the layout.
  - Docs only: links checked, no test reads these files.
- **M4 chunk E review closed** (steps 12-13; started by the 30-minute
  check-in, `6ce3263..8f071ed`). Two read-only reviewer agents, no root,
  no QEMU: A on `accept-m4.sh`, B on the docs. Reports in
  `~/smartconfig-work/review-m4e/` (their harness refused the file
  writes; the session saved the returned text unchanged). Write-up
  [reviews/2026-10-06-m4-chunk-e.md](reviews/2026-10-06-m4-chunk-e.md).
  - 1 high: the README's GRUB password recipe would stop every boot at
    the password prompt with a saved or "Advanced options" default,
    `GRUB_SAVEDEFAULT`, `GRUB_DISABLE_SUBMENU` or ZFS, and its check did
    not show which entry boots. Now: use it only with `GRUB_DEFAULT=0`
    alone, check the one marked line, do not reboot otherwise, how to
    undo; the conffile prompt; what the password closes.
  - A product bug (B4, medium): with an older fstab blocker and a newer
    error, `sc status` gave the undo for the error's file. `654ee7f`:
    the newest blocker, else the newest error (plan 3.1), and the menu
    promise only while its flag is set (B8); "60 s" for "1m0s".
  - The script (A1, medium): the runtime oneshots are unloaded once they
    finish, so step 2 would have failed on an empty journal read on the
    real run. `4b91d97`: `RemainAfterExit=yes`, plus the real store read
    read-only in a namespace (A2), grubenv compared byte for byte, the
    made units checked, os-prober off through `/etc/default/grub.d`, a
    sturdier cleanup, exact initrd, wider checksums, a bad verdict with
    a fake systemctl (2b).
  - Docs: what the rescue boot does not mount, S2/S3 on this VM, the
    5-file count (C7), the menu "after a failed boot", the removal's
    directories, stale lines.
  - Plan change C7. Checks: gofmt, vet, `go test ./...` as a user and as
    root, `make race`, `make lab-test` (326); the new tests fail on the
    old code; the script's new parts replayed as a user.
  - Not run: `accept-m4.sh` as root, and `make lab-e2e` on the fixed
    build: both are S1, which needs the user's OK.
- **CI was red** on most pushes since `c70d886` (2026-10-06 00:15 UTC),
  unnoticed: every red run failed only
  `test_an_event_sent_before_qemu_exits_is_not_lost_to_abort` (lab,
  from `0ccba27`), in CI's root pass. The test asked whether QMP had
  closed right after `wait_event` returned the SHUTDOWN, which can be
  before the reader reads EOF: a race in the test, not in the lab.
  Reproduced here 1 in 25 beside four CPU burners; `0a1b71f` asks after
  the reader's EOF: 0 in 25 under the same load, `make lab-test` 326 ok,
  CI green on `0a1b71f`. Lesson: check `gh run list` after each push.
- **M4 sign-off S1 started** (your OK: "yes start S1").
  - Checks on this build: gofmt, vet, `go test ./...` as a user and as
    root, `make race`, `make lab-test` (326), CI green (`0a1b71f`), and
    `make m1-compat`.
  - `make accept-m4` as root on `19f2200`: **PASS**, the script's first
    root run (`~/smartconfig-work/signoff/accept-m4-20261006T142311Z-19f2200.log`).
    The boot units took 19 and 35 ms; this boot's verdict is ok
    (`failed-units=0`); the bad verdict kept the flag; `sc status` and
    `--console` gave the blocker and `sc restore` of the good version;
    the chattr refusals; the read-only and hot-journal stores;
    `42_smartconfig` under `grub-mkconfig` adds only its part, and this
    machine's grub.cfg is what grub-mkconfig makes today; this machine's
    store, read-only: no problem in its 3 newest files, exit 0. Nothing
    left behind (no runtime unit, mount, work dir or private dir; the
    real grubenv has no flag).
  - `make lab-e2e` starts on the commit of this entry.
- **M4 sign-off S1 passed.**
  - `make lab-e2e` on `2c58f8b` (clean tree, `sc` `cafca1512050`):
    BIOS **PASS** in 44m18s (0 retries, boot2=b, boot 5). UEFI was
    INCONCLUSIVE at P.5 in 1 s: `ovmf` was upgraded by apt at 06:34 UTC
    today (2024.02-2ubuntu0.9 to 0.10), after the reference image was
    made, and the lab refuses another firmware under the old variables.
  - `make lab-image` did nothing: the chunk D fixes (`9d3df9e`) had
    dropped its rule, and plain provision would have kept the image
    anyway (its name does not depend on the firmware). So, without a
    commit (one build for both modes): `vm.py provision --force`, 216 s,
    then UEFI alone on the same commit: **PASS** in 37m19s (0 retries,
    boot2=b, boot 5). Evidence in
    `~/smartconfig-work/signoff/lab-e2e-2c58f8b/`; logs
    `lab-e2e-20261006T142402Z-2c58f8b.log` and
    `lab-e2e-20261006T151513Z-2c58f8b-uefi.log`.
  - `b9761a9`: the `lab-image` rule back, with `LAB_FORCE=1` for an
    ovmf update, in P.5's advice and the lab README; checked by running
    it and `make lab-test`.
  - Next: S2, with your OK and your fresh VirtualBox snapshot.
- **M4 sign-off S2: installed; the reboot is next** (your OK and your
  snapshot, 19:45 UTC, "took the snapshot").
  - Before (`~/smartconfig-work/signoff/pre-s2-20261006T194841Z.txt`):
    boot `2c1e65ee`, up since 19:37 UTC; the boot before it ended at
    17:38 UTC without a shutdown (`last -x`: crash), whether your
    power-off for the snapshot or the host is yours to say. BIOS, GPT.
    Boot times of the last seven boots 12.4 to 33.7 s (this one 28.8 s).
    grubenv empty, `GRUB_DEFAULT=0`, hidden menu, timeout 0; no failed
    units. New in `/var/backups/smartconfig`: `sc-m3` (`cf5ba078`, the
    build scd ran since M3), `manifest-pre-m4s2.txt` and
    `sha256-pre-m4s2.txt` (`/etc` and `/boot/grub`).
  - Installed with the README's commands: `bin/sc` as built for S1
    (`cafca1512050`, `2c58f8b`, clean; no code or script change since),
    scd restarted on it (baseline 0/0/0); both units enabled,
    `systemd-analyze verify` clean; both drop-ins; `42_smartconfig`,
    `update-grub` exit 0, `grub-script-check` ok. grub.cfg: the default
    is still entry 0, Ubuntu; "SmartConfig rescue" last, on
    7.0.0-38 with `ro fstab=no systemd.unit=rescue.target
    SYSTEMD_SULOGIN_FORCE=1`; the menu block only under
    `smartconfig_pending=1`.
  - After: `/etc` and `/boot/grub` against the manifest differ by
    exactly grub.cfg, `42_smartconfig`, the two units, their two links
    and the two drop-ins with their directories; scd recorded each.
    `sudo sc status`: no healthy boot recorded yet, exit 0.
- **M4 sign-off S2: the reboot and its checks** (evidence
  `~/smartconfig-work/signoff/post-s2-20261006T203734Z.txt`).
  - The reboot: `systemd-run --on-active=120 systemctl reboot`, clean
    shutdown at 19:54:39 UTC (boot `2c1e65ee`).
  - **The S2 boot (`276489a3`): ok.** Verdict ok (local-fs active, no
    emergency or rescue, 0 failed units), grubenv empty after it.
    23.5 s to "Startup finished" (graphical.target at 22.6 s
    monotonic), inside the 12.4-33.7 s of the seven boots before.
    `sc-boot-seen` done at 5.2 s; `sc-boot-ok` starts after
    multi-user.target, 0.6 s.
  - **It ended without a shutdown:** its journal ends 7 s after gdm's
    greeter came up (19:55:02), nobody logged in (`last -x`). The next
    boot (`75d27954`): verdict ok, 38.1 s; you logged in at 19:57:44 and
    DING (desktop icons) started 24 times in 90 s (2 on a normal boot),
    the black-desktop symptom of M2's S4; the journal ends 19:59:23, no
    shutdown (`last -x`: crash). The boot after it (`94783975`, now):
    verdict ok, 37.6 s, DING 2, you logged in at 20:30.
  - Not sc, as far as the evidence goes: both ends came after their
    boot's ok verdict, and the DING loop happened before M4 (M2 S4,
    2026-10-02). Neither left the menu flag: `grub-editenv` fsyncs
    grubenv (strace on a copy: `O_TRUNC`, write 1024, `fsync`), so each
    ok verdict was on disk before the reset.
  - Boot time on this boot: sysinit.target's critical chain is
    apparmor and snapd.apparmor, not sc; `sc-boot-seen` 847 ms,
    `sc-boot-ok` 521 ms, scd 107 ms in `blame`. graphical.target came
    at 22.6, 37.4 and 36.7 s monotonic on the three boots (12.1 to
    33.6 s on the six before); the extra in the last two is
    plymouth-quit-wait (13.7 s) and NetworkManager, after an unclean
    end each.
  - `/etc` and `/boot/grub` against `manifest-pre-m4s2.txt`: the S2
    files only, and `cups/subscriptions.conf{,.O}` (cupsd; out of
    scope, as in M2's S4). scd running, 0 restarts; `sc status`: last
    healthy this boot, nothing changed since, exit 0. No failed units.
  - The two resets were yours (your answer: "yes"), after a black
    screen. The menu was hidden on every boot by the config: sc's flag
    cleared by each ok verdict, and Ubuntu's recordfail cleared by
    `grub-common.service` at 10.2, 15.3 and 14.7 s, before each reset
    (as in the lab, A6); you reported no menu.
  - **S2 passed.**
- **M4 sign-off S3: the bad line is in; the failing boot is next** (your
  OK and your fresh snapshot, 21:05 UTC, "Yes, taken — go").
  - Before (`~/smartconfig-work/signoff/pre-s3-20261006T210624Z.txt`):
    boot `94783975` ok, grubenv empty, `/etc/fstab` 446 bytes, sha256
    `9d71ab60…`, the same as its newest row `fb5cf4`; `sc status`
    healthy, exit 0. Checked first on a copy: `sc check --as
    /etc/fstab` gives the blocker, exit 2.
  - The edit at 21:06:29 UTC, as root, the lab's line and method
    (appended; the plan's owner uses nano, the writer does not matter
    to the rescue path): line 10, `UUID=3f6c1e2a-9b7d-4c1e-8f2a-5d6e7f8a9b0c
    /mnt/backup ext4 defaults 0 2`, sha256 `1ab60193…`. scd at once:
    row `053fdf`, "check: blocker fstab-source-missing, line 10"; `sc
    status` lists it and the undo `sc restore fb5cf4`, exit 2. No
    daemon-reload: the owner reboots.
  - Next: the reboot (`systemd-run --on-active=120 systemctl reboot`);
    you drive the console.
  - Since (2026-10-07 05:58 UTC; read as the user, no root, so no
    verdicts and no system journal yet;
    `~/smartconfig-work/signoff/pre-s3-rescue-20261007T055831Z.txt`):
    two boots after `94783975` (ended 21:09): `928e5ac3` (21:16 to
    01:20, then gone without a shutdown, `last -x`: crash, the host
    again) and `f11e5522` (05:41). grubenv `smartconfig_pending=1`;
    `/etc/fstab` still the bad one (sha256 `1ab60193…`); `sc status`:
    this boot `f11e5522` (emergency), root read-write, scd running;
    `systemctl is-system-running`: maintenance; the desktop came up all
    the same (both boots). No rescue boot yet.
  - Next: you boot "SmartConfig rescue" from the menu, run the report's
    commands, then Ubuntu; then the checks as root.
- **M4 sign-off S3: the rescue boot and the restore; S3 passed** (your
  "s3 done" and two photos, 2026-10-07 06:27 UTC; checks as root,
  `~/smartconfig-work/signoff/post-s3-20261007T062818Z.txt`, photos in
  `~/smartconfig-work/signoff/s3-photos/`).
  - The failed boots (`928e5ac3`, `f11e5522`): verdict bad both
    (`local-fs=inactive emergency=active`, newest row 1566, the bad
    edit). Each waited 90 s for the missing disk (the device timed out
    at 92.4 and 93.9 s), reached emergency.target at 95.2 and 94.8 s,
    and started gdm about 1.5 s later: on this desktop the login screen
    comes up over the emergency shell and the report printed there (the
    tty1-versus-gdm question of plan section 7: gdm). 1 min 42 s and
    1 min 40 s to "Startup finished". `f11e5522` ended with a clean
    reboot at 06:14:05.
  - The menu came up by itself, no key pressed, before the rescue boot
    and again after it (your answer, 2026-10-07: "yes, the menu came up
    by itself both times").
  - The rescue boot (`7ce2c935`, your photos): "SmartConfig rescue" from
    the menu, and the report above "Press Enter" as in the README: this
    boot (rescue), root read-only; last healthy `94783975`; failed since
    2 boots, "a mount failed, emergency mode"; scd not running; `053fdf`
    blocker fstab-source-missing, line 10; the five commands; "The menu
    shows once more". You ran `mount -o remount,rw /` and `sc restore
    fb5cf4`: "restored /etc/fstab from fb5cf4 (mode 0644 root:root),
    previous state saved as 8f7c6e"; then the rest. As designed it has
    no seen line and no verdict (`/var/lib` read-only), and no journal
    (volatile on a read-only root); `last -x` has its shutdown.
  - The boot after (`8438a7f4`, Ubuntu's entry by its command line):
    verdict ok at 9.9 s (`local-fs=active`, newest row 1568, the
    restore), grubenv empty after it, 9.95 s to "Startup finished", no
    failed units, `is-system-running` running. `/etc/fstab` 446 bytes,
    0644 root:root, sha256 `9d71ab60…` (= `fb5cf4`). `sc log
    /etc/fstab`: `8f7c6e` pre-restore (518 bytes, the bad file) and
    `c8c9bd` "restored from fb5cf4". `sc status`: healthy this boot,
    nothing changed since, exit 0. scd running, 0 restarts; `sc` still
    `cafca151…`.
  - The clock: each boot of this VM starts minutes behind (this one
    8.5 min: the kernel at 06:11:25, NTP set 06:20:45 at 47 s), and the
    rescue boot has no network to fix it, so its rows say 06:13. sc does
    not mind: the line between "came up with" and "changed since" is the
    row id in the verdict, not a time.
  - New on a desktop, not seen in the lab; for S4:
    - A red `[FAILED] Failed to start grub-initrd-fallback.service`
      right above the report (photo). Ubuntu's unit is in
      `rescue.target.wants` on this install (not in the lab's cloud
      image) and runs `grub-editenv … unset initrdfail`, which cannot
      write on the read-only root. Harmless: the boot after ran it ok.
      The README's rescue section could say to expect it.
    - `sc boot seen` ran twice in each failed boot (2.3 and 5.0 s; two
      seen lines in `boots`), once in every normal boot and in the lab's
      failed boot. sysinit.target waited 90 s for local-fs there; a
      second start job for it in that window starts the finished oneshot
      again. Harmless here: both before the verdict, and `sc status`
      counts boots by id ("2 boots"). Whether the unit wants
      `RemainAfterExit=yes` is for the final review.
    - The README says such a boot may "stop in emergency mode" and prints
      the report there; on a desktop the login screen comes up over it,
      the mount missing (`maintenance`). The next boot's menu is the way
      in, as plan section 8 says ("M4 does not depend on emergency
      mode"); the README could say so.
  - **S3 passed.** Next: S4 (final review, docs, tag `m4`), with your OK.
- **M4 sign-off S4: the final review** (your OK, 06:45 UTC, "go ahead
  with S4"). Write-up
  [reviews/2026-10-07-m4-final.md](reviews/2026-10-07-m4-final.md); the
  reports in `~/smartconfig-work/review-m4final/findings-{a,b,c}.md`.
  - Three reviewer agents in their own clones of `81319d8`: A boot
    integration, B the rescue-time CLI and its data, C security and the
    docs. Read-only here, no QEMU; the S2 and S3 journals read as root.
  - 2 high (B), both reproduced and fixed:
    - `69ea585`: a stopped sole console held `sc status --console`, and
      the shell, past systemd's 90 s (the fallback write to stdout,
      the same console, had no limit). Now stdout only when no console
      opens, and one 80 s deadline for every write.
    - `4a8f7b4`: an fstab first seen after the line (scd's baseline,
      no healthy boot yet) was "new", and the undo moved it aside;
      without a healthy boot a blocker in the oldest row listed was
      missed. Now new only with proof of absence, else compared with
      the first version, else "fix it by hand".
  - 6 medium, fixed:
    - `8c6f30e` (A1, C2): "exit" in the rescue shell after the remount
      gave an "ok" verdict that cleared the flag and hid the next
      undo. Both units now need a command line without `fstab=no`. A
      whole-boot `systemd-analyze verify` test came with it.
    - `580edca` (A2): the rescue entry keeps the default entry's options
      but `quiet splash` (a `nomodeset` was lost).
    - `734b670` (B3, B4): a deleted flag file is no error; no store in
      rescue says to mount `/var`, not `sc init`.
    - C1, C3: docs, below.
  - 13 low, fixed:
    - `580edca` masks Ubuntu's grub-initrd-fallback in the rescue boot:
      S3's red FAILED line.
    - `03ec9ca`: a boot with a verdict is not seen again; the flag is set
      before the seen line, and by a bad verdict; the boots file ends a
      torn line before the next, skips one unended, and refuses a
      symlink.
    - `d9c8817`: the 60 s stop removes the `/run` copies; the undo's
      paths are shell-quoted.
  - S3's three desktop observations:
    - The double seen was a sound card's transaction while sysinit waited
      90 s; harmless.
    - The FAILED line is masked now.
    - The login screen over emergency mode is documented, with the
      reboot to the menu.
  - Docs (C1, C3 to C13):
    - README, PROJECT_LOG, docs/README: the desktop case and ssh; the
      rescue shell asks for root's password if root has one; "exit"
      ends nothing well; the rescue boot's journal; the lab's 40 s; the
      30 s; `grep -s`; removal order.
    - MILESTONES: status, C1 to C8, follow-ups 1-9.
    - M4_PLAN: sections 0, 3.4, 5 and 6, and plan change C8.
  - Checks on each commit: gofmt, vet, `go test ./...` as a user and as
    root, `make race`, `make lab-test` (326) where the lab or scripts
    changed, `make m1-compat`. Every new test fails on the code before
    its fix. CI green on each.
  - Not run yet:
    - `make lab-e2e` on the fixed tree (next, no sudo).
    - `accept-m4` as root, the install, the tag: your word.
- **Paused at your word** ("stop and save the state", 09:31 UTC).
  - `make lab-e2e` on `87c6636` (log
    `~/smartconfig-work/signoff/lab-e2e-20261007T091132Z-87c6636.log`,
    run `~/.cache/smartconfig-lab/runs/20261007T091134Z-uefi-87c6636/`)
    stopped by SIGTERM to its process group after 20 min, in UEFI mode:
    INCONCLUSIVE, "interrupted (signal, exit 143)" at 3.5.
  - Before that, 41 rows passed, 0 failed. Among them: 0.5, the rescue
    entry with the default entry's options and the mask; 1.x, the
    healthy boot with both changed units; 2.x, the failing boot; 3.2,
    the rescue kernel with `fstab=no`. Not a sign-off run; it is to be
    rerun.
  - Stopped: the 30-minute check-in job and the run's waiter. QEMU is
    gone. The reviewers' clones and reports stay in
    `~/smartconfig-work/review-m4final/`.
- **M4 sign-off S4 closed; M4 done, tag `m4`** (your OK, 13:20 UTC,
  "yes, go ahead after it passes"; then "i don't need snapshot, go
  ahead" and "don't wait start the work. Approved").
  - Session start, 13:10 UTC: up 10 min (boot `c95d3ab6`); HEAD and
    `origin/main` both `59203a3`, clean; no QEMU; CI green.
  - `make lab-e2e` on `59203a3`, detached, `LAB_REQUIRE_CLEAN=1` (log
    `~/smartconfig-work/signoff/lab-e2e-20261007T131334Z-59203a3.log`):
    - UEFI **PASS**, 19m35s, boot2=a, boot 5, 0 retries, `dirty=no`, sc
      `1dce09f5ff12` (run `20261007T131337Z-uefi-59203a3`).
    - BIOS INCONCLUSIVE at 0.4 after 3m14s: `[lab]` ssh exit 255 while
      `install.sh` ran (24.2 s; the lab's ssh gives up after about 15 s
      without an answer). The guest's console was quiet at its login
      prompt, no panic or reboot. This VM's desktop was busy in that
      minute (Chrome: WebGL and H.264 encoding in the journal). Not sc
      (run `20261007T133312Z-bios-59203a3`).
    - BIOS-only rerun of the same tree, no commit between (log
      `lab-e2e-20261007T133745Z-59203a3-bios.log`): **PASS**, 19m59s,
      boot2=a, boot 5, 0 retries, `dirty=no`, the same sc (run
      `20261007T133747Z-bios-59203a3`).
    - Both modes took about 20 min, against 37 to 45 min on 2026-10-06.
    - Evidence: `~/smartconfig-work/signoff/lab-e2e-59203a3/` (both
      modes, and the inconclusive BIOS run as `bios-inconclusive-*`).
  - Step 3, run by you: Claude Code's auto mode refused sudo to Claude,
    so you ran `sudo sh ~/smartconfig-work/signoff/step3-m4-59203a3.sh`
    (log `step3-m4-20261007T142427Z-59203a3.log`). It runs
    `scripts/accept-m4.sh` itself, not `make accept-m4` (which rebuilds
    `bin/sc`), and checks before and after it that HEAD is `59203a3`,
    the tree is clean and `bin/sc` is `1dce09f5ff12`.
    - A first try (14:21 UTC) stopped at that clean-tree check, before
      anything ran: root's git does not read the user's
      `~/.config/git/ignore`, which ignores `.claude/settings.local.json`.
      The script now runs git as the user.
    - `accept-m4`: **PASS**, exit 0. Step 7 noted that this machine's
      grub.cfg was not what grub-mkconfig made today (the S1
      `42_smartconfig` was still installed); step 8: healthy, exit 0.
    - Before: boot `c95d3ab6`, scd pid 903 with 0 restarts, both units
      enabled, grubenv empty, no failed units, `/usr/local/sbin/sc` the
      S1 build `cafca151…`. New in `/var/backups/smartconfig`:
      `sc-2c58f8b` (that build), `manifest-pre-m4inst.txt` and
      `sha256-pre-m4inst.txt` (`/etc` and `/boot/grub`).
    - Installed with the README's commands, each exit 0, then
      `update-grub`.
    - After: `/usr/local/sbin/sc` is `1dce09f5ff12`; scd restarted on
      it (pid 13382, 0 restarts); both units enabled, `systemd-analyze
      verify` clean; `grub-script-check` ok. grub.cfg: Ubuntu still
      entry 0, "SmartConfig rescue" last, on 7.0.0-38 with
      `systemd.mask=grub-initrd-fallback.service ro fstab=no
      systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1`; grubenv empty.
    - `/etc` and `/boot/grub` against the manifest: exactly grub.cfg,
      `42_smartconfig` and the two units (the drop-ins were already
      these). scd recorded the four. `sudo sc status`: healthy, exit 0.
      No failed units.
  - The annotated tag `m4` is on `59203a3`, the commit the installed
    build was made from, and pushed.
  - The next boot is the first with the new units and rescue entry; its
    verdict is read at the next session.
- **Your call: all M3 and M4 follow-ups before the M5 plan** ("if you
  have finished everything from v1 to v4 then only start the M5 plan";
  asked which, "All follow-ups"). M1 and M2 have nothing open (M2's 8
  follow-ups are in; its soak was closed by you); M3 has 7 follow-ups
  open, M4 9 and 8 review notes. The order and the chunks are in "Now".
  Out: M4 follow-up 8 (LUKS, a non-goal) and your own items.
- **M3 follow-up 1, unit drop-ins, done `1246469`** (chunk F). A new
  checker, `unitdropin`, for `/etc/systemd/system/NAME.d/*.conf`.
  `systemd-analyze verify` loads NAME by name, with a scratch directory
  first on `SYSTEMD_UNIT_PATH`, where the candidate takes the place of
  the drop-in of the same name (probed on this VM, systemd 255: the
  scratch copy wins, and a trailing ":" keeps the stock path).
  - The drop-in's own lines are its findings. A finding about the unit
    as a whole counts only if a second run, with the drop-in empty, does
    not give it too: a second `ExecStart=` without the empty one before
    it is the drop-in's; a problem the unit had before is not.
  - Found while probing: for an alias (`sshd.service.d` on Ubuntu)
    systemd names the unit `ssh.service`, so the alias is followed
    through the unit path's symlink; a template's drop-in is said twice
    (template and instance), now one finding. `verify` checks only the
    first command of each `Exec…` list (systemd's limit): an added
    `ExecStartPre=` after one with `-` is not looked at.
  - No unit of that name: `unit-dropin-orphan` (warning). A prefix
    drop-in (`foo-.service.d`) gets a note, not a check.
  - Tests: `TestUnitDropIn` (11 cases, a fake verify that answers by
    the drop-in's size), `TestUnitAlias`, `TestUnitDropInRealVerify`
    (journald, real systemd). 8 mutations of the new code, each caught.
    `accept-m3.sh` has three new cases (run as a user here with this
    build: as expected; as root at the end, with your run).
  - Checks: gofmt, vet, `go test ./...` as a user and as root, `make
    race`, `make m1-compat`. The M4 rescue drop-ins on this VM check
    clean with the new build.
- **M3 follow-up 2, sshd `ListenAddress`, done `e9133ca`** (chunk F). A new
  rule of sc's own, `sshd-listen-missing` (warning): a `ListenAddress`
  whose literal address no interface here has. `sshd -t` binds nothing
  and passes it.
  - Probed on this VM: Ubuntu 24.04 starts sshd through `ssh.socket`,
    which has `FreeBind=yes`, so the bind does not fail at boot; SSH just
    takes no connections there, and none at all when that is the only
    address. The review's "gone at the next boot" holds for an sshd that
    binds for itself.
  - Host names are not looked up; wildcards and loopback are skipped. A
    machine with no address but loopback (the rescue boot, no network)
    gets nothing.
  - Tests: `TestSshdListen` (12 cases), `TestSshdListenReal` (every
    address of this machine, then 192.0.2.7). 11 mutations, each caught;
    the one that first survived (`Unmap`) got a case.
  - The first full run failed `TestRulesTable`: the explanation had six
    lines (at most five, for the console). Shortened; all checks then
    passed: gofmt, vet, `go test ./...` as a user and as root, `make
    race`, `make m1-compat`.
- **M3 follow-up 3, an sshd drop-in with sshd_config, done `ef2989d`** (chunk
  F). `sshd -t` now reads the drop-in where sshd does: in a scratch copy
  of `sshd_config` whose `Include` names a scratch copy of the drop-in
  directory (the candidate in place of its namesake).
  - Probed with OpenSSH 9.6 on this VM: `AuthorizedKeysCommand` in a
    drop-in with its user in `sshd_config` was a false blocker alone and
    is clean now. A `Match` a drop-in leaves open ends with the drop-in,
    and a second `Subsystem sftp` passes: no 9.6 setting makes another
    file's line fail, so that path is tested with a fake sshd.
  - The drop-in's own lines are its findings; a line-less refusal or
    another file's line counts only when a second run with the candidate
    empty does not give it. Alone, with a note, when `sshd_config` does
    not include it, a file cannot be read, or another file stops sshd.
  - Tests: `TestSshdTogether` (real sshd on fake `/etc/ssh` trees, 9
    cases), `TestSshdTogetherOther`; 9 mutations, each caught.
  - Checks: gofmt, vet, `go test ./...` as a user and as root, `make
    race`, `make m1-compat`.
- **CI red once, on `6fb78f0`, fixed in `762909e`.** CI's root pass
  failed `TestSshdTogether`: its runner has no `/run/sshd`, so `sshd -t`
  as root adds a note to a clean run, and the case that falls back to
  the drop-in alone is one. This VM has `/run/sshd`; a reproduction in
  a private mount namespace was refused by Claude Code's safety check, so
  CI was the test: green on `762909e`.
- **A time limit that runs out before the validator starts is a
  timeout, `56c5d31`.** The race pass of follow-up 4's checks failed
  `TestSudoersVisudoBroken` ("run visudo: context deadline exceeded"):
  with other tests running alongside, more than its 200 ms passed before
  the start, and exec refuses a done context. Now that is a `TimedOut`
  result, a note like any timeout; `TestRunTimeoutBeforeStart` (1 ns)
  gave the error before. Checks are run alone from now on.
- **M3 follow-up 4, notes and the validators' words, done `c77b060`**
  (chunk F). A note is sc's sentence; what a validator said behind it
  (which may quote a file) is `Report.Said`: `sc check -v` prints it,
  without `-v` a line says `-v` shows it; `sc edit` prints it under the
  note. About 15 notes changed in 9 checkers.
  - The drop-in checks' second runs say nothing into `Said`; the sshd
    together check keeps the first run's lines only when it keeps its
    result (a test with a fake sshd says them once).
  - Tests: `notesSaid` in the check tests (the old notes' raw text is
    asserted as said lines), `TestCheckCLISaid`, `TestEditSaid`; 8
    mutations, each caught (one only after a test was extended).
  - Checks: gofmt, vet, `go test ./...` as a user and as root, `make
    race` (uncached), `make m1-compat`.
- **CI red on `84747b8` (follow-up 4), fixed in `a43a7bf`.** CI's root pass:
  when the sshd check inside `sshd_config` needed no second run, sshd's
  words behind a note ("did not finish checking", no `/run/sshd` on the
  runner) were not handed to `Said`. A real bug, which this VM cannot
  show (it has `/run/sshd`); `TestSshdTogetherSaid` (a fake sshd) failed
  before the fix. The check package passed as a user, as root and under
  race; CI is the full check.
- **M3 follow-up 5, a stop signal and the validator, done `aca4ab5`**
  (chunk F). The validator has its own process group, which a Ctrl-C at
  the terminal does not reach, and sc ended by the signal without its
  cleanups: a hung validator ran on, its scratch copy stayed an hour.
  `check.Stop` kills every validator group not yet reaped and removes
  every scratch directory; sc's signal handler calls it where it removes
  its temp files (scd at its second signal only). A group leaves the set
  before it is reaped, so its id cannot be another's.
  - Tests: `TestSignalStopsValidator` (SIGINT and SIGTERM to `sc check`,
    a fake findmnt with a child: both gone, no scratch, sc dies by the
    signal within 3 s), `TestStop` (running, scratch, started after).
    4 mutations, each caught. Full checks passed.
- **M3 follow-up 7, scd's check reads two rows, done `8923fd7`** (chunk F).
  `checkPath` listed the path's whole history (0.1 s at 20,000 rows) to
  find two rows. `Store.Around` gets them by index search: the change by
  its id (found while writing it: ids are unique, a UNIQUE index, so the
  old loop's handling of a repeated id was never needed), and the row
  before the first queued change through `AsOf`. `TestPathQueryPlans`
  asserts both SQLite plans (no scan, no sort); `TestAround`; 5
  mutations, each caught. Checks: gofmt, vet, `go test ./...` as a user
  and as root, `make race` (uncached), `make m1-compat`.
- **M3 follow-up 8, two cosmetics, done `c4c4b2b`** (chunk F; all of chunk F's
  follow-ups are now in: 1-5, 7, 8; 6 was done in M4).
  - `/etc/default/grub`: dash puts an unterminated quote at the end of
    the file, past its last line. sc now names the line that leaves a
    quote open read on its own (with the next line's quotes read as one
    file, the mistake seems a line later), unless the next such line
    closes it (a value over two lines). The real dash's test: line 2,
    not 4. "end of file unexpected" goes on the last line.
  - `/etc/fstab`: the captured findmnt output shows a heading per line
    in the file's order (two "none" for two swap lines), not one per
    mount point: the k-th heading is the k-th line when each has one;
    else a message naming one line's source picks it.
  - Tests: `TestShQuoteOpen`, `TestFstabHeadings`; 7 mutations, each
    caught. Full checks passed.
  - Next: the chunk F review (two reviewers), once CI is green.
- **The chunk F review runs** (two reviewers, clones of `4f90bbc` in
  `~/smartconfig-work/review-m3fu-f/`): A the `internal/check` changes, B
  the CLI, signals, scd's checker, `accept-m3.sh` and the docs. No code
  change until it is closed. Your picks for chunk G and M4 4 are in "Now".
- **The chunk F review is closed**
  ([reviews/2026-10-07-m3-followups-chunk-f.md](reviews/2026-10-07-m3-followups-chunk-f.md),
  fixes `cfb704e`). Two reviewers on `4f90bbc`: A (`internal/check`) 8
  findings, one high: a drop-in for a template alias (`autovt@.service.d`,
  getty@'s alias on Ubuntu) was not checked, so the autologin drop-in
  without its `ExecStart=` reset passed; B (the CLI, signals, scd, docs)
  nothing high, the worst a stop signal that left a scratch copy when
  `sc check` had more files (8 of 20 runs). All fixed, with a test each
  (each failing tests the reviewers wrote reran first); 28 of 30 fixes,
  undone one at a time, made a test fail (two are reasoned only, named in
  the write-up). Full checks passed (race uncached).
  - Next: chunk G (`sc status` and restore), with your picks in "Now".
- **M4 follow-up 1, restore under an unmounted mount point, done `0d22229`**
  (chunk G, your pick: refuse). `sc restore` writes nothing when the
  path is under the deepest `/etc/fstab` mount point that the mount
  table does not list (a separate `/boot` in the rescue shell), and says
  `mount /boot` first, exit 1; without an fstab or a mount table it goes
  ahead. Tests: `TestRestoreUnmountedMount` (refused, file unchanged;
  mounted, restored), `TestUnmountedMount` (the deepest point, escaped
  names, no mount table). Full checks passed.
- **M4 follow-up 2, a checked file now a symlink, done `17f4a34`** (chunk G).
  `sc status` calls it an error ("now a symlink to X, not checked") with
  the healthy version's restore; a link that was a link stays as it was.
  `TestStatusFileNowLink`, which fails without the change. Full checks
  passed.
- **M4 follow-up 9, a torn verdict line, done `4adc132`** (chunk G). The
  next append ends a fragment a crash left with " #torn", and `Read`
  skips it: a verdict cut in its row id can no longer pull the healthy
  boot's line back. Old boots files have no mark and read as before.
  The new case in `TestReadTornLines` failed before. Full checks passed.
- **M4 follow-up 7, the cosmetics, done `284c381`** (chunk G). The console's
  rows have their date (the lab's goldens and test reports too); the
  row -1 healthy boot has its own title; `sc check` and `sc status` make
  no `$SC_HOME/tmp`; a WAL store on a read-only root gets sc's own
  "sc needs delete" (a copy was tried and dropped: sc refuses WAL
  anyway); `FS_IOC_GETFLAGS` is only for the ports sc builds for (mips
  and sparc were never built: the review's case cannot happen). Tests
  for each; `make lab-test` 326 and the full checks passed.
- **The rescue mount hint, done `845b395`** (chunk G; the chunk E review's
  note). `sc status` puts `mount <point>` in its undo when the file is
  under an unmounted mount point of fstab (a separate `/boot`), as `sc
  restore` now refuses there. `TestStatusUndoMounts`. Full checks
  passed (a first run's root pass used the system's Go 1.22, as the path
  to the toolchain was taken outside the repo; rerun right).
- **The console header first, done `7fdb494`** (chunk G, your pick for the
  chunk C review's note). A rescue report not ready after 2 s shows its
  header (This boot, Last healthy, Failed since, scd), then the rest or
  the stop. Not at once, as asked: a report in time stays one write,
  which other console lines cannot cut, and the M4 lab matches it whole;
  a hung one still shows the header long before the 60 s stop.
  `TestConsoleStatusHeaderFirst`. Full checks passed.
- **The verdict waits for scd's rescan, done `32cc097`** (chunk G, your
  pick for the chunk B review's note). scd is `Type=notify` and sends
  `READY=1` once its startup rescan is recorded and logged; `sc-boot-ok`
  is ordered after `scd.service`. scd spells out systemd's default
  dependencies but the target's `After=`, so boot does not wait for the
  rescan; a rescan that hangs fails the start after 5 minutes. An older
  `sc` under this unit would never be ready: the README says to install
  `sc` first. `TestReadyAfterBaseline`, `TestSdNotify`, the unit tests.
  Full checks passed; the first run's root pass lost one fake
  `systemd-analyze` to a VM pause (11.6 s against the 10 s limit, 3 of 3
  reruns pass), so fake machines get a minute, `b7a89e6`.
- **Soft-reboot and the early SIGHUP documented, `2ae67e8`** (chunk G, your
  picks: M4 follow-up 4 and the chunk D note stay as limits). The README
  says a soft-reboot is the same boot to sc (systemd 255 has no count of
  them), so a failure in it brings no menu; the SIGHUP window (3 to 5 ms
  before `signal.Notify`) was already said in `main`, and the rescue
  shell starts even if the report dies. Docs only. Chunk G's items are
  all in; next, its review.
- **The chunk G review, closed: [reviews/2026-10-07-m4-followups-chunk-g.md](reviews/2026-10-07-m4-followups-chunk-g.md),
  fixes `0a55306`.** Two reviewers in clones of `0fcabd1`: A (boot integration)
  found 2 medium real-machine bugs (scd never ready while a startup path
  is held back: under the free-space floor or with a store error systemd
  restarted it every 5 minutes and each verdict waited 5 minutes; READY
  before a startup path an event touched), 2 that would have failed the
  sign-off (accept-m2 step 18 on `sc-boot-ok`'s `After=scd.service`; lab
  2.5 when the getty comes between a slow report's header and its rest),
  the late header and a torn line at a trim. B (status and restore) found
  low ones: a covered mount taken as mounted, nested mount points, a
  symlinked mount point, the console's symlink row without "error", the
  row -1 boundary's label, fstab blocking its own undo, a file mount
  point, two titles, and test gaps. All fixed, each with a test; 22 of 22
  fixes undone one at a time fail a test. Full checks passed (the race
  pass once lost `test_the_loop_measures_its_wakes`, the load-bound lab
  test chunk D noted, a chunk H item; the rerun passed). Chunk G is
  done; chunk H is next. `accept-m2` joins the sign-off runs
  (`scd.service` changed).
- **M4 follow-up 3, `GRUB_TOP_LEVEL`, done `af23c2e`** (chunk H). The rescue
  entry boots the kernel `GRUB_TOP_LEVEL` names, as `10_linux` puts it
  first, when it has an initrd; else the newest with one, and a word on
  stderr. `TestGrubScriptTopLevel` (fails without the change). Full
  checks passed.
- **M4 follow-up 5, the units' hardening, done `8c5cc40`** (chunk H). Both
  boot units: `NoNewPrivileges=yes`, `ProtectHome=yes`; `sc-boot-seen`
  also `PrivateNetwork=yes` (`sc-boot-ok` asks systemd over its socket).
  Ubuntu's sysinit units (`systemd-resolved`, `-timesyncd`, `nftables`)
  set up such namespaces this early. `42_smartconfig` takes no
  `SC_GRUB_BOOT`; the tests rewrite its `boot=/boot` line. Checked by
  `systemd-analyze verify` and `TestBootUnits`; that a broken boot still
  gets its seen line with them is for the lab run after chunk H. Full
  checks passed.
- **M4 follow-up 6, the test gaps, done `270c3c5`** (chunk H). `TestBootUnits`
  refuses `sc-boot-seen` ordered after `sysinit.target`/`basic.target`
  and any binding or conflict on `sc-boot-ok`;
  `TestStatusConsoleCapWarnings` pins the 5-file cap with room to spare
  and no undo for warnings. Each of the six mutations (four unit lines,
  the cap, the undo's severity) and the row -1 fallback's fails a test.
  Full checks passed.
- **Lab check 1.8, done `f0bf189`** (chunk H; the final review's A8).
  `facts.sh normal` prints `sc-boot-seen`'s `Before=` (`seen-before`);
  1.8 fails on a target in it but `shutdown.target`, or an unread one.
  This VM's installed unit: `grub-common.service shutdown.target
  sc-boot-ok.service grub-initrd-fallback.service`. Lab tests for each
  case; undoing the check fails one. Full checks and `make lab-test`
  passed.
- **The lab's items (chunk D), done `b092a79`** (chunk H). A read over ssh
  that ran out of time while the host stood still runs once more; a
  mark does not wait out its drain while serial reconnects; `facts.sh`'s
  filtered parts keep the command's status; the wake test takes any late
  wake. Explained, no change: systemd's lines missing on ttyS0 (v255
  turns its console output off once a `Type=idle` unit stops waiting
  while a unit has the console: `emergency.service` and its shell). Not
  reproduced: the signal test's hang (0 of 135 runs beside four CPU
  burners). A test for each change, each failing without it. Full
  checks and `make lab-test` passed (a first run stopped at `gofmt` on
  the test's stubs).
- **The GRUB password recipe in the lab, done `818dace`** (chunk H; the
  chunk E note). `--grub-password` adds checks 6.1-6.5 after boot 5: the
  README's recipe scripted as root (one `--unrestricted` line,
  Ubuntu's), the default boot asks for nothing, the rescue entry from
  the menu asks for the user and the password (serial in UEFI, the VGA
  screen and qcodes in BIOS) and boots with them (`fstab=no`, its shell
  reboots), Ubuntu from the menu asks for nothing and is ok. Lab tests
  for the recipe's judging, the two prompts and the summary tag; full
  checks and `make lab-test` passed. Then `make lab-e2e
  LAB_E2E_ARGS=--grub-password` on `818dace` (clean tree), log
  `~/smartconfig-work/signoff/lab-e2e-20261007T212317Z-818dace.log`,
  run files in `signoff/lab-e2e-818dace/`: **PASS** both modes, UEFI
  27m03s (outcome b), BIOS 26m06s (outcome a), 0 retries, `grubpw=yes`.
  It also ran chunk H's hardened boot units through the broken boot 2
  (seen line and menu as before) and the new check 1.8 (seen's
  `Before=` has no target but `shutdown.target`). The README now says
  the lab runs the recipe.
- **The `cmd/sc` flake, found and fixed `e37758b`** (chunk H; the chunk D
  note). A hunt (`~/smartconfig-work/fu-scratch/h7/hunt.sh`: three
  `make lab-test`, then `go test ./cmd/sc/`, ten times) failed at once,
  because it ran under `nohup`: SIGHUP ignored is inherited, sc keeps an
  ignored hangup (by design), and `TestConsoleStatusOutlivesHangup`,
  `TestWatchSIGHUPRescans` and the lab's signal test (via
  `TestLabPython`) failed every time. The earlier unkept failure fits
  that. `TestMain` un-ignores SIGHUP in the test process, so children
  start with the default; the lab test's child resets it. Under `nohup`
  those tests pass now, and the hunt then ran 10 of 10 clean. Full
  checks passed. Chunk H's items are all in; next, its review.
- **The chunk H review, closed: [reviews/2026-10-07-m4-followups-chunk-h.md](reviews/2026-10-07-m4-followups-chunk-h.md),
  fixes `a1c1228`.** Two reviewers in clones of `100da93`, no high finding
  and no sign-off false pass. A (GRUB script, units, tests): the units'
  comments (the early-namespace precedent was wrong; `PrivateNetwork=`
  keeps file-system sockets), a blocklist test that let ordering and
  sandbox keys through, `.old` kernels. B (the lab): four medium ones in
  the `--grub-password` stage, each a failure in the wrong class (a
  prompt in 6a/6c a [lab] timeout, 6a's verdict not awaited before the
  flag, a retried 6c expecting a menu, 6.4's waits outside `ran_out`),
  and low ones (a dead VGA watch, 1.8's docstring, `facts.sh` dropping
  output, 11 surviving mutations, the README). All fixed with tests;
  28 of 28 fixes undone one at a time fail a test. Full checks and
  `make lab-test` passed. Chunk H is done; next, the sign-off runs:
  `make lab-e2e LAB_E2E_ARGS=--grub-password`, then `accept-m2`,
  `accept-m3`, `accept-m4` and the install as root.
- **The sign-off lab run on `526f14b`: INCONCLUSIVE twice, not yet
  passed** (2026-10-08). `make lab-e2e LAB_E2E_ARGS=--grub-password`,
  logs `~/smartconfig-work/signoff/lab-e2e-20261008T000839Z-526f14b.log`
  and `lab-e2e-20261008T005326Z-526f14b.log`. No [M4] failure. UEFI lost
  its verdict both times to the host (ssh exit 255 during host stalls of
  12-181 s; then a slow-udev retry and no kernel in 600 s; this VM had
  about 0.6-1 GB free, no swap, load 16-25, snapd busy). BIOS failed
  **both times at the same place**: 6.2's `cat
  /proc/sys/kernel/random/boot_id` over ssh, exit 255 after about 31 s,
  right after 6.1 (the recipe and `update-grub`). In `818dace` the same
  step passed; this looks systematic, not load: next, read that run's
  serial and ssh logs (`~/.cache/smartconfig-lab/runs/*bios-526f14b`),
  fix, rerun. Waiting on it: the root steps
  (`~/smartconfig-work/signoff/signoff-fu-526f14b.sh`: accept-m2/m3/m4
  and the install; its WANT is this build) and the reboot. The M5 plan
  draft is in `~/smartconfig-work/fu-scratch/m5/M5_PLAN.draft.md`.
- **The lab's ssh tolerates a silent guest, `1cadd8c`.** Why bios lost 6.2
  twice: right after the recipe's `update-grub`, ssh to the busy guest on
  the loaded host got no answer to 3 keepalives (`ServerAliveInterval=5`,
  15 s) and gave up, exit 255, its message hidden by `LogLevel=ERROR`;
  the path itself is the same as in `818dace` (which passed), so this
  is the environment. `ServerAliveCountMax=12` (60 s; each call keeps its
  own timeout), and `boot_id` (a read) runs once more after ssh's own
  failure. Lab tests for both, each failing without its change. Full
  checks and `make lab-test` passed. Next: the sign-off lab run again.
- **M5 started: the plan, for your read, in
  [M5_PLAN.md](M5_PLAN.md)** (your call, 2026-10-07: M5 starts after
  the follow-ups either way). Its questions take the recommended answers
  until you say otherwise: dpkg-deb, not nfpm (no new dependency);
  `/usr/sbin/sc`; the store kept on purge; the hand install's known
  copies moved aside; the package named `smartconfig`.
- **The follow-ups signed off** (2026-10-08): the lab (`643cec3`, both
  modes, `--grub-password`), `accept-m2/m3/m4` as root and the install
  (`d686341`, the same `sc`); details in "Now". The root steps ran from
  this session with your OK; auto mode allowed them this time.
- **M5 step 1, `sc version`, done `50e1128`.** `sc version` prints the
  package version, the commit (Go's own stamp, `-dirty` for a changed
  tree) and Go's version. `scripts/version.sh`: `0.N.0` at the tag `mN`,
  else `0.N.99+git<commit time>.<sha7>`, from the commit alone (the
  lab's P.2 builds again and compares); `make build` passes it with
  `-X main.version`. `TestVersion`, `TestVersionScript` (a git repo of
  its own: no tag, at `m4`, after it). Full checks passed.
- **M5 steps 2 and 3, `/usr/sbin/sc` and `make deb`, done `9c25edd` and
  `7f70233`.** The units, the rescue drop-in, the README, the lab and
  `accept-m4` name `/usr/sbin/sc` (Q2's recommended answer). `make deb`
  builds `dist/smartconfig_<version>_amd64.deb` with `dpkg-deb
  --root-owner-group` (Q1: no new tool): `sc` in `/usr/sbin`, the units
  and drop-ins in `/usr/lib/systemd/system`, `42_smartconfig` a conffile,
  docs, control and md5sums; reproducible (two builds of a commit match).
  `TestBuildDeb`. Full checks, `make lab-test` and `make deb` passed. The
  hand install on this VM is unchanged until the package replaces it.
- **M5 steps 4 and 7, the maintainer scripts, done `e710ce2`.** `postinst`,
  `prerm`, `postrm` as `dh_installsystemd` writes them (enable, mask,
  purge), scd restarted on every configure (start would leave a hand
  install's old scd running), `update-grub` on install and remove, the
  flag unset on remove, the store kept on purge and said (Q3). `preinst`
  on a first install moves the README's hand install aside as
  `NAME.dpkg-old` (Q4), disabling its units first. Tests: each script's
  calls on install, upgrade, a unit the owner disabled, a failing
  `update-grub`, remove and purge; the takeover with a foreign unit left
  alone. Full checks passed (twice: the restart came from rereading the
  takeover, after the first run).
- **M5 step 5, CI builds the package, done `922e5cc`.** CI fetches the
  tags, runs `make deb`, prints its control and contents, and keeps it as
  the `deb` artifact (2.7 MB; the upload action pinned by commit, as the
  others). The plan gains Q6: the license its copyright file names.
- **M5 step 6, the lab's `--deb`, done `7c4e466`.** The lab installs the
  package of the tree in its clean guest (`dpkg -i`), holds 0.4 to the
  package's own files, runs the M4 scenario on it, then D.1 (a later
  build over it: scd restarted, store and boots kept), D.2 (`dpkg -r`:
  sc gone, no rescue entry, flag unset, store kept) and D.3 (`dpkg -P`:
  `42_smartconfig` gone, store kept and said). `42_smartconfig` now adds
  nothing without `sc` (a removed package leaves it, a conffile).
  Tests: `install.sh`'s deb mode, D.1-D.3 against canned answers (each
  failure), 0.4's package variant, the tags; the GRUB script without
  `sc`. Full checks and `make lab-test` (350) passed. Next: `make
  lab-e2e LAB_E2E_ARGS=--deb`, both modes.
- **The lab's `--deb` run on `1c5850f`: UEFI PASS, BIOS INCONCLUSIVE; a
  lab fix, `7bbaa82`** (2026-10-08). `make lab-e2e LAB_E2E_ARGS=--deb`,
  log `~/smartconfig-work/signoff/lab-e2e-20261008T050330Z-1c5850f-deb.log`,
  run files in `~/smartconfig-work/signoff/lab-e2e-1c5850f/`. UEFI passed
  everything, `deb=yes` (24m06s): 0.4 to 0.7 on the package's own files,
  the M4 scenario on it, D.1 (the `+lab1` build over it, scd restarted),
  D.2 (`dpkg -r`) and D.3 (`dpkg -P`). BIOS lost its verdict at 1.1, a
  [lab] error: the reboot gave its two guest RESETs 13 ms apart, but the
  host stood still 2.5 s while QMP's reader marked the serial log for the
  second, past the 2 s wait, and it came as a reset of its own. Fixed: a
  QMP round trip after the wait. Tests for it; full checks and `make
  lab-test` (353) passed. Found while reading for M5 step 8: the plan's
  step 7 also asks for the takeover of a hand install in the lab, which
  only `TestDebPreinstHandInstall` covers. Next: that, as D.4.
- **M5 step 7b, the takeover in the lab (D.4), done `4df3912`.** After
  the purge, the lab puts in the README's hand install up to M4 from the
  `m4` tag's own files (the package's `sc` in `/usr/local/sbin`, scd
  running from it), then `dpkg -i` of the package: each of the 7 files
  must be `NAME.dpkg-old`, not executable, and named; scd restarted from
  `/usr/sbin/sc`; the units from `/usr/lib`, enabled; the drop-ins from
  `/usr/lib` only; one rescue entry; `sc` in root's PATH `/usr/sbin/sc`.
  A hand install that does not come up is a [lab] error. `--deb` now
  needs the `m4` tag in the clone. Tests for each check (each undone
  fails one), `stage_hand`, `stage_debs`. Full checks and `make lab-test`
  (356) passed. Next: step 8, the README.
- **M5 step 8, the README, done `d222530`.** README "Install (M5)": `make
  deb`, `apt install`, `sc version`; the first verdict at the next boot,
  a failed `update-grub`, an upgrade, the takeover of a hand install,
  remove and purge (the store kept). The hand-install and removal blocks
  of the watcher and the rescue path point to it; the Layout lists name
  the package's scripts. Full checks passed. Next: the M5 review (two
  reviewers), then the sign-off.

