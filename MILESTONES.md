# Milestones

- [x] M1 store + CLI: snapshot, log, cat, diff, restore — done 2026-09-27,
      hardened by four review rounds 2026-09-27/28 (tag `m1`)
- [x] M2 watcher daemon: scd with inotify, auto-snapshot edits made with any editor
      — done 2026-10-02 (tag `m2`); the 24-hour soak was closed after 5.6 h (your call, 2026-10-03)
- [x] M3 file graph + checkers: tiers, real validators, regex rules with canned
      explanations, sc edit and sc check — done 2026-10-03 (tag `m3`)
- [x] M4 rescue path: GRUB entry, rescue.target service printing sc status,
      boot-ok verification, restore from read-only root — done 2026-10-07
      (tag `m4`)
- [x] M5 package: .deb (dpkg-deb, not nfpm: plan question 1); install,
      upgrade, remove and purge on a clean VM (the QEMU lab), and on the
      dev VM over the hand install, with an upgrade, a remove and an
      install again — done 2026-10-09 (tag `m5`, package 0.5.0)
- [ ] M6 incident factory and eval set
- [ ] M7 local model: sc why with llama.cpp, opt-in

Current: M5 is done (tag `m5` on `8f91158`, 2026-10-09). Plan
[docs/M5_PLAN.md](docs/M5_PLAN.md): its questions ran on the
recommended answers (dpkg-deb, `/usr/sbin`, the store kept on purge,
the hand install moved aside, the name `smartconfig`, the command `sc`);
the license (question 6) is yours, later (your answer, 2026-10-09).
Signed off: the lab's `--deb` run in both modes, and on the dev VM the
install over the hand install, a reboot, an upgrade, the remove and the
install again; the dev VM runs 0.5.0, built at the tag. Next: M6. Its
plan is drafted (`~/smartconfig-work/M6_PLAN.draft.md`, not yet in
`docs/`) and is being revised after the review of 2026-10-09; nothing of
M6 is built. That review also found work no milestone owns: "Open, with
no milestone yet", at the end of this file. The M3 and M4 follow-ups
below are done, all but M4's LUKS item (out, your call), and signed off
on 2026-10-08 (the lab in both modes, accept-m2/m3/m4 as root, the
install). M4 is done (tag `m4`, 2026-10-07). M3 is done (tag `m3`,
2026-10-03). The M2 soak was closed after 5.6 h (your call).

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
- Pinned modernc.org/sqlite v1.29.10 because newer releases needed Go 1.25
  and this machine had Go 1.22.2. Since 2026-09-28 the build uses Go 1.26.8
  (go.mod); the pin is unchanged (PROJECT_LOG D3).

### Hardening after acceptance (2026-09-27/28)

(The `m1` tag message says "five independent review rounds". There were four
review rounds, which led to five rounds of fixes; the tag is left as
published.)

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
- `sc log` printed intents and paths as they are; a newline or tab in an
  intent broke the table. Fixed in M2 (`c56fe24`): names, link targets and
  intents that hold control characters are printed quoted.
- Some errors lack the command's context, and `sc help <unknown>` exits 0
  with usage text. Left for later.
- logrotate reads hidden files in /etc/logrotate.d, including sc's
  `.NAME.sc-tmp-*` during a restore's few milliseconds; no suffix avoids it.
- kill -9 during a restore can leave a `.NAME.sc-tmp-*` file next to the
  target. The watcher does not record those names; a walk reports one
  older than 10 minutes once per run (a warning line) and leaves it for
  the admin: it may hold the only copy of what the restore was writing.
- Restoring a file owned by another user, or into a directory that is not
  root's, goes through paths that a hostile user could swap; M1 is for root
  on root-owned config. M2 handles user-owned paths (plan step 6).

## M2 notes

`sc watch`, run as `scd.service`, records every change under `/etc`,
`/boot/grub` and each login's `authorized_keys`, `rc` and `environment`.
That covers edits by any tool, mode and owner changes, symlinks, deletions,
creations and changes made while it was stopped. Plan:
[docs/M2_PLAN.md](docs/M2_PLAN.md). Sign-off runs and the final review:
[docs/WORKLOG.md](docs/WORKLOG.md) (2026-10-02) and
[docs/reviews/2026-10-02-m2-final.md](docs/reviews/2026-10-02-m2-final.md).

Signed off on the dev VM: acceptance and smoke runs, the real store
migrated, the owner scenario, a reboot, and `kill -9` during the startup
rescan and in the middle of a 190-file dpkg burst. The 24-hour soak was
deferred by the owner, then closed after 5.6 h (2026-10-03, the owner's
call); it passed on what it saw. No build since has had a 24-hour soak.

### Deliberate limits

- Nothing is pruned. The store stops taking new content at 256 MiB free
  disk and logs that once. This VM adds about 0 rows a day in normal use.
- A tier only sets the journald priority of a row's line: 1-2 warning,
  3 notice, 4 info. Alerts and checks are M3.
- `first seen` rows get no journal line of their own, only the count in
  the baseline summary. That includes a file created while scd was down.
- Reading the journal needs `sudo` (or the `adm` group).
- A new login's `~/.ssh` is picked up by the hourly rescan or a passwd
  change, not at once.

### M2 follow-ups (from the final review, all low or medium)

- Rate limit for a system file a program rewrites constantly: done, 20
  rows, then one every 5 min with the newest state (`064bf68`).
- Digest rows: done, they get a random id, so a journal reader cannot
  test guesses at a low-entropy secret (`4ec437a`).
- Watches, all done: `deleted` rows when a login `.ssh` moves away
  (`bd8fad9`), re-walk after a directory swap (`c0cfe77`), stale watches
  after an overflow (`465b30d`), listings trimmed by each walk (`d76a295`).
- Home files, all done: at startup, a home root that is not a real
  directory is skipped instead of failing the start (`5b57aaf`); orphan
  objects count against the per-file limit (`2a8b686`).
- Stale `.NAME.sc-tmp-*` files: done, reported, not removed (`7463e5d`).

## M3 notes

Checkers for the files that stop a boot or lock the owner out: `sc check`,
`sc check <id>` and `sc check --as PATH FILE`; `sc edit`, which checks an
edit before it replaces the file; a check by scd after every change it
records; and `sc scope`. Plan: [docs/M3_PLAN.md](docs/M3_PLAN.md), with
its changes after approval (C1 to C9) in section 15. Reviews:
[chunk A](docs/reviews/2026-10-03-m3-chunk-a.md),
[B](docs/reviews/2026-10-03-m3-chunk-b.md),
[C](docs/reviews/2026-10-03-m3-chunk-c.md),
[D](docs/reviews/2026-10-03-m3-chunk-d.md),
[final](docs/reviews/2026-10-03-m3-final.md).

34 rules across 15 checkers at the tag, each with a fixed explanation
(`sc check -v`). Follow-ups added a 16th checker, for unit drop-ins (1),
and the rules `unit-dropin-orphan` (1) and `sshd-listen-missing` (2). Each validator was first run on this VM in the form sc uses, to make
sure it only checks, and severities follow what the consumer really does: an fstab
line with `nofail` is a warning, a passwd file without root is a warning
where nss-systemd supplies root.

### Deliberate limits

- Findings are worked out when asked, never stored.
- `sc edit` and scd judge a change by the findings it adds; `sc check`
  shows them all.
- A file is checked alone, except netplan's files, which are merged as
  netplan merges them, and a unit's drop-in, which is checked with its
  unit, and an sshd drop-in, which is checked inside sshd_config with
  the other drop-ins (alone, with a note, when one cannot be read or
  another file stops sshd). Cross-file conflicts (two sudoers drop-ins)
  are not looked for.
  A drop-in for every unit with a prefix (`foo-.service.d`) is not
  checked.
- As a user, `sc check` cannot read some files (sudoers, a 0600 netplan
  file). It says so and exits 1; it never guesses. A validator cut short
  (out of time, killed) also gives exit 1.
- The admins are the members of sudo and admin. On a machine where root
  logs in with a password and nobody is in sudo, `sc check` reports
  `group-no-admin`; scd and `sc edit` do not, as it is not new.
- `apply` lines only say when a change takes effect (`at the next boot`);
  whether it was applied is not tracked.
- scd does not check the startup baseline's `first seen` rows, restores,
  `sc edit` rows or fingerprint-only files.
- No checker yet for nft, logrotate, AppArmor or PAM: none has a proven
  check-only form. The M2 plan's other "M3" items wait (plan section 13;
  see "Open, with no milestone yet" at the end).

### M3 follow-ups (from the final review, none high)

1. Done (`1246469`): a checker for unit drop-ins (`*.service.d/*.conf`,
   what `systemctl edit` writes), which verifies the unit together with
   its drop-ins (medium).
2. Done (`e9133ca`): sshd: a warning for a `ListenAddress` this machine
   does not have (no SSH there, and none at all when it is the only one).
3. Done (`ef2989d`): sshd: a drop-in is checked inside the main file, as
   sshd reads it, not alone (a false blocker when the two only work
   together).
4. Done (`c77b060`): notes keep a validator's raw lines for `-v`.
5. Done (`aca4ab5`, with the chunk F review's fixes): Ctrl-C or SIGTERM
   to `sc check`: the running validator is killed, no other starts, its
   scratch copy is removed and no other is made (they were left, the copy
   swept after an hour). `sc boot`'s grub-editenv is left to finish.
6. A read-only root (M4's rescue shell): run sc's own rules without a
   scratch copy. Done another way in M4 (step 3, C2: a private scratch
   directory in `/run`).
7. Done (`8923fd7`): scd: look up only the rows a check needs, not a path's
   whole history.
8. Done (`c4c4b2b`): cosmetic: an unclosed quote in `/etc/default/grub` was reported
   past the last line; two swap lines shared one findmnt heading.

## M4 notes

The rescue path: the GRUB entry **SmartConfig rescue** (`42_smartconfig`),
which boots to a root shell with root read-only and `/etc/fstab` ignored;
`sc status`, and its console form in a drop-in for `rescue.service` and
`emergency.service`, printed above the shell's prompt; a verdict for every
boot from two units (`sc-boot-seen`, `sc-boot-ok`) in `$SC_HOME/boots`;
the menu flag `smartconfig_pending` in grubenv, which shows the menu
after a failed boot; sc on a read-only root, hot journal included; and
`sc restore` refusing `chattr +i`/`+a` targets. The store's schema is
unchanged, so the M1, M2 and M3 binaries still read it. Plan:
[docs/M4_PLAN.md](docs/M4_PLAN.md), with its changes after approval (C1
to C8) in section 12. Reviews:
[chunk A](docs/reviews/2026-10-04-m4-chunk-a.md),
[B](docs/reviews/2026-10-04-m4-chunk-b.md),
[C](docs/reviews/2026-10-04-m4-chunk-c.md),
[D](docs/reviews/2026-10-05-m4-chunk-d.md),
[E](docs/reviews/2026-10-06-m4-chunk-e.md), and the
[final review](docs/reviews/2026-10-07-m4-final.md).

The boot itself is tested in a QEMU VM: `make lab-e2e` runs the owner
scenario (a bad fstab line, a failed boot, the
menu, the rescue entry, the report, the restore, a healthy boot) in UEFI
and BIOS mode, about 25 minutes each ([lab/README.md](lab/README.md)).
On the dev VM, `make accept-m4` runs the parts that need no reboot; the
boot is tested there only in sign-off S2 and S3, with your OK and a
snapshot.

### Deliberate limits

- No rescue entry for a btrfs or ZFS root; LVM, LUKS and multipath roots
  are not supported. With `GRUB_DISABLE_RECOVERY=true` there is no rescue
  entry, and the menu flag stays.
- One entry, for the newest kernel; the stock "Advanced options" entries
  remain for an older one.
- The verdict needs `local-fs.target` active and neither emergency nor
  rescue mode; failed units are counted, not judged (plan change C3).
- The console report uses sc's own rules only, lists at most 5 files,
  worst first, and stops itself after 60 s (C4, C6, C7).
- The rescue boot cannot clear the menu flag, so the first boot after a
  fix shows the menu once more. It gets no verdict, even when it goes on
  to the desktop ("exit" in its shell): its command line has `fstab=no`.
- A boot that cannot write `/var` gets no line in `boots`.
- No GRUB password is managed: the README says how to set one.

### Open from the reviews

- Done (`32cc097`, your pick): edits made while scd was down could be recorded
  after the healthy boot's row, if scd's startup rescan still ran when
  the verdict was given; the undo then named an older, still good,
  version (chunk B). scd is `Type=notify`, ready once that rescan is
  recorded, and `sc-boot-ok` is ordered after it; scd has no default
  dependencies (they are spelt out, but the target's), so boot does not
  wait for the rescan.
- Done (`7fdb494`, your pick): a report that is slow shows its header first (after
  2 s: This boot, Last healthy, Failed since, scd), so a hung `sc` still
  says which boot was healthy before the 60 s stop; one in time is one
  write, as the lab matches it whole (chunk C, C6).
- Stays (`2ae67e8`, your pick): a SIGHUP in the first milliseconds of `sc status
  --console`, before `signal.Notify` runs, ends sc by the kernel's
  default (chunk D, B5; 3 to 5 ms, said in `main`). The shell still
  starts: the drop-in's `ExecStartPre=-` lets the report fail.
- Done (`e37758b`): one `go test ./cmd/sc/` run of 14 failed right after
  three lab-test runs, its output not kept (chunk D). Found: run under
  `nohup`, the tests' children inherit SIGHUP ignored and sc keeps it
  so; three hangup tests failed that way every time. The tests now give
  their children SIGHUP as a shell does; 10 rounds of three lab-test runs
  and `go test ./cmd/sc/` then passed.
- Done (`b092a79`): the lab (chunk D): a read over ssh whose time ran out
  while the host stood still runs once more with a fresh budget; a mark
  no longer waits out its 1 s drain while serial reconnects; `facts.sh`'s
  filtered parts keep the command's exit status; the wake test takes any
  late wake (it measured 0.3 s beside a race pass). systemd's lines
  missing on ttyS0 are systemd's rule: once a `Type=idle` unit
  (`emergency.service`) stops waiting while a unit has the console,
  systemd writes no more status there (v255 `manager.c`,
  `manager_dispatch_idle_pipe_fd`). The signal test did not hang in 135
  runs beside four CPU burners; it stays as it is. The host's pauses
  stay yours to look at.
- Done (`f0bf189`): the lab's check 1.8 could not fail: `critical-chain`
  follows only units that became active, and the boot units are oneshots
  that never do (final review, A8). It now reads `sc-boot-seen`'s
  `Before=`: no target but `shutdown.target`.
- Done (`818dace`): the GRUB password recipe in the README was not
  tested in the lab (chunk E). `make lab-e2e LAB_E2E_ARGS=--grub-password`
  runs it (checks 6.1-6.5); it passed in both modes on 2026-10-07, and
  Ubuntu's signed EFI GRUB takes `password_pbkdf2`.
- Done (`845b395`): `sc status` puts `mount /boot` (each unmounted mount point of
  fstab the file is under, the outermost first, since the chunk G review)
  in its undo (chunk E, C7); `/usr/local` was
  where sc ran from (the hand install), so it is mounted when sc runs.
  Since M5, sc is `/usr/sbin/sc`, on `/` (merged-usr). With no store in a
  rescue or emergency boot it says to mount `/var` (final review, B4).

### M4 follow-ups (from the final review; no high one is open)

1. Done (`0d22229`, your pick: refuse): a restore under a separate `/boot` that is
   not mounted wrote to the root filesystem's own `/boot/grub`, if one is
   there, and said it worked; `sc restore` now refuses under a mount
   point of `/etc/fstab` that is not mounted (B8).
2. Done (`17f4a34`): a checked file replaced by a symlink was not judged ("now a
   symlink"); `sc status` now calls it an error, with the healthy
   version's restore (B9).
3. Done (`af23c2e`): `GRUB_TOP_LEVEL` was ignored: the rescue entry booted the newest
   kernel (A9). It now boots the one `GRUB_TOP_LEVEL` names, as `10_linux`
   puts it first, when that one has an initrd (else it says so).
4. Documented (`2ae67e8`, your pick): `systemctl soft-reboot` starts a new session
   with the same boot id: it gets no "seen" line (the boot has its
   verdict), so a failure in it before multi-user brings no menu (A5,
   the cost of A3's fix). systemd 255 has no soft-reboot count to tell
   the sessions apart; the README says so ("Where it does less").
5. Done (`8c5cc40`): the units ran as full root: now `NoNewPrivileges=`,
   `ProtectHome=` and, for `sc-boot-seen`, `PrivateNetwork=` (C14).
   `42_smartconfig` honoured its test knob `SC_GRUB_BOOT` from root's
   environment; the tests now rewrite its `boot=/boot` line.
6. Done (`270c3c5`): test gaps from the mutation runs: the Go tests missed `sc-boot-seen`
   ordered after sysinit or basic, `sc-boot-ok` with `Requires=` on
   local-fs or `Conflicts=` with emergency, the row -1 fallback, a
   warning-only change without undo, and the 5-file console cap (A7,
   B section 4).
7. Done (`284c381`): cosmetics (B): the console's times have their date; a command
   that only reads makes no `$SC_HOME/tmp`; a WAL store on a read-only
   root says what it says on a writable one ("sc needs delete"), not
   "unable to open database file (14)"; the "Last healthy" boot whose row
   is not known has its own title, not "no healthy boot is recorded".
   `FS_IOC_GETFLAGS`: mips and sparc were never built (sc builds for the
   64-bit ports with `fstatat`, and Go has no sparc port), so the
   constant is now only for the ports sc builds for.
8. `fstab=no` does not cover crypttab: a second LUKS volume asks for its
   passphrase in the rescue boot (`luks.crypttab=no`, if wanted; LUKS is
   a non-goal) (A10).
9. Done (`4adc132`): a torn verdict line whose row id lost digits pulled the line
   back (more rows counted as changed); a line a crash cut short is now
   ended with a mark, and the mark is never read as a line (B5).

## M5 notes

The package: `smartconfig`, one `.deb` built with dpkg-deb (`make deb`,
reproducible), with `sc` in `/usr/sbin`, the units and drop-ins in
`/usr/lib/systemd/system` and `42_smartconfig` as a conffile. Its
maintainer scripts restart scd, run `update-grub`, take over a hand
install (moved aside as `NAME.dpkg-old`), and keep the store on remove
and purge. Plan: [docs/M5_PLAN.md](docs/M5_PLAN.md), with its changes
(C1 to C8) in section 9. Review:
[docs/reviews/2026-10-08-m5.md](docs/reviews/2026-10-08-m5.md). Signed
off in the QEMU lab (`make lab-e2e LAB_E2E_ARGS=--deb`, both modes:
install, the M4 scenario, upgrade, remove, purge, the takeover of the
`m4` hand install) and on the dev VM (the install over the hand install,
a reboot, an upgrade, a remove and an install again).

### Deliberate limits

- The plan's questions ran on its recommended answers (dpkg-deb,
  `/usr/sbin`, the store kept on purge, the hand install moved aside, the
  name `smartconfig`, the command `sc`). You have not confirmed them yet.
- No license yet (question 6, yours, later): the package's `copyright`
  file says all rights are reserved.
- The command `sc` hides universe's `sc` spreadsheet for anyone with both
  installed (question 7).
- Not built: an apt repository or PPA, signing, arm64, other
  distributions, a source package, a man page; lintian is not run.
- Purge keeps `/var/lib/smartconfig`, so its copies of secret-bearing
  files stay too (see "Open, with no milestone yet").
- With `DPKG_ROOT` set, the scripts still reload systemd and run
  `update-grub` on the running system (C6).
- Two items an earlier plan gave to M5 are not in it: the apt
  Pre/Post-Invoke hook file and a scope file users can edit (M2 plan
  section 15). They are listed below.

## M5 follow-ups (the round of 2026-10-09)

**Where they come from.** The items with the most at stake in "Open, with
no milestone yet" (below).

**Your picks** (2026-10-09, "do the M5 follow-up round"), all four on
the recommended answer:
- **Retention:** a manual `sc prune`.
- **Restore:** it checks the version it writes, and asks.
- **Secrets:** hidden when shown.
- **apt:** its changes are tagged.

**How it runs.** One step at a time, each with its tests, the checks, a
commit, a worklog line, a push and CI. One reviewer closes each chunk.

**Chunk I, the store: done and reviewed** (2026-10-09,
[reviews/2026-10-09-m5-followups-chunk-i.md](docs/reviews/2026-10-09-m5-followups-chunk-i.md),
fixes `9d43980`).
1. Done (`8e59efc`): `sc status` says when the store's filesystem is
   under scd's 256 MiB floor. Changes that need new content then wait,
   retried each minute, and scd logs that once.
2. Done (`f2a6f0b`, `f2e90b7`, the review's fixes `9d43980`): `sc prune`
   (store and CLI).
   - **What it deletes:** versions scd recorded that a newer row replaced
     more than 90 days ago (`--older-than`).
   - **What it keeps:**
     - each path's newest and first rows;
     - every manual, edit, restore and pre-restore row (an explicit
       `sc snapshot` is a manual row even when scd has the same state);
     - for each path, its version at each boot the boots file lists,
       which is what `sc status` compares against.
   - **Blobs:** it then deletes the stored content no row uses any more,
     and a killed writer's temp files.
   - **When it deletes nothing:** until a healthy boot with a row is
     recorded, and while scd runs an `sc` an upgrade replaced.
   - **How it runs:**
     - it asks first, and `--yes` skips the question;
     - the plan is read with no lock, and it deletes in short batches;
     - a signal stops it between batches.
   - **No `VACUUM`:** it would renumber the rowids.
   - **Writers:** blobs go only under the write lock, and every writer
     puts its blob again under the lock.

**Chunk J, restore and checks:**
3. Done (`a4571cb`): `sc restore` checks the version it is about to write
   against the file as it is now (file rows with a checker; links,
   deletions and digest rows are not checked).
   - If the version adds a blocker or an error, it lists them and asks.
     End of input means no: nothing is written, exit 2. `--force` skips
     the check.
   - Nothing changes when it adds nothing, which covers restoring the
     last healthy version from the rescue report.
4. `/etc/default/grub.d/*.cfg` is checked as `/etc/default/grub` is.
5. `sc help <unknown>` exits 1, with one line.

**Chunk K, secrets:**
6. A secret list in the scope: content stored, shown hidden. It covers
   `/etc/shadow`, `/etc/gshadow`, NetworkManager's system-connections,
   `/etc/cloud/cloud.cfg.d/90-installer-network.cfg`, and netplan files.
   - `sc cat` and `sc diff` replace the shadow and gshadow password
     field, `psk=`, `password=`, `secret=` and private-key values, and
     netplan's `password:`, with a marker.
   - `sc check -v` does the same to a validator's raw lines from those
     files.
7. `--show-secrets` shows them, as root.

**Chunk L, apt:**
8. The package ships `/etc/apt/apt.conf.d/50smartconfig` (a conffile).
   - Its `DPkg::Pre-Invoke` and `Post-Invoke` lines run `sc apt begin`
     and `sc apt end`, guarded, so a missing `sc` breaks nothing.
   - These mark a dpkg run in `/run/smartconfig`.
   - scd tags each change by when it saw it: origin `apt`, and the run as
     its intent.
   - `sc log` shows them; there is no "undo a run".
   - It is a new origin string with no schema change, so `sc-m1` and
     `sc-m2` read these rows as file rows, and `make m1-compat` gains a
     case.
9. The lab's `--deb` mode checks that the upgrade's rows carry origin
   `apt`.

**Then, each with your OK:**
- all checks;
- `accept-m2`, `accept-m3` and `accept-m4` as root;
- `make lab-e2e LAB_E2E_ARGS=--deb` in both modes;
- the new package installed on this VM, a reboot, and its read-only
  check.

## Open, with no milestone yet

**Where these come from.** The M3 plan (section 13) said its "later"
items would be given a home at M3's sign-off; that was not done. The
2026-10-09 review of the whole plan (after M5) found these open with no
milestone, follow-up list or owner.

**What happens to them.** Items 1 to 4, 11 and `sc help` are now in the
M5 follow-ups above (your picks, 2026-10-09). The rest wait for your
decision: part of M6 or M7, a milestone of its own, or a known limit.

They are ranked by what is at stake:

1. **Retention** (→ M5 follow-ups, chunk I).
   - Rows are never deleted and the store is never vacuumed (M2 plan
     section 15: "needs its own decision").
   - Below 256 MiB free, scd holds back the changes that need new content
     and logs one line, while `sc status` still says scd is running.
2. **Secrets** (→ chunk K).
   - These are stored with their content, kept forever and after a
     purge: `/etc/shadow`, NetworkManager's system-connections (Wi-Fi
     keys), and `/etc/cloud/cloud.cfg.d/90-installer-network.cfg`.
   - `sc cat` and `sc diff` print them.
   - Redaction (M2 plan: "M3") and content-based detection were never
     built. This must be settled before M7 sends diffs to a model.
3. **`sc restore` writes without a check** (→ chunk J), and there is no
   account-set restore. Putting back an older `/etc/group` alone can drop
   today's admin from sudo, which `sc edit` would refuse (M2 plan:
   "M3"). The account-set restore stays open.
4. **The apt hook and change sets** (→ chunk L, tags only).
   - The M2 plan put the hook in M5.
   - A package that changes many files gives one row each: installing
     `logcheck-database` gave 190 (M2's S5).
   - Nothing groups those rows, or undoes "that install". Undoing a run
     stays open.
5. **The rescue entry on LVM, LUKS and multipath roots** is not
   supported (M4 non-goal), and Ubuntu Server's guided install uses LVM
   by default.
6. **Applied markers** ("pending until update-grub"): unattended
   upgrades can apply an old edit overnight without a word (M2 plan:
   "M3").
7. **ACLs, xattrs, inode flags and directory modes** are not recorded
   (M2 plan question 10; needs a store change).
8. **Roots outside `/etc`**: `/usr/local/lib/systemd`, crontabs,
   `/usr/local/etc`, the ESP's `grub.cfg` (M2 plan: "M3 at the
   earliest").
9. **A scope file users can edit** (M2 plan: "M5").
10. **Restores into directories a user owns** are refused (M2 plan:
    "M4", the dirfd restore).
11. **`/etc/default/grub.d/*.cfg` is not checked** (→ chunk J), though the
    cloud image keeps its GRUB settings there (found by the M6 pilot).
12. Smaller: active/inert labels and vendor-override detection;
    `/etc/alternatives` chains and `rc?.d` links; cross-file checks
    (two sudoers drop-ins); a home-directory watch; `sc help <unknown>`
    exits 0 (→ chunk J).
