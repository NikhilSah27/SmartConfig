# Next steps

The roadmap from here to the end of M2, and what comes after. Written
2026-09-27, while the M2 plan is being drafted.
[WORKLOG.md](WORKLOG.md) stays the one source of truth for what is happening
right now. This page is updated at milestone boundaries.

## Where we are

- M1 (store + CLI) is done and accepted. Two follow-up fixes hardened it
  (`c720f22`, `ec15ece`).
- gofmt, vet and tests are clean at HEAD. The suite also passes under the
  race detector in a scratch clone.
- A separate process is drafting the M2 plan (the `scd` watcher). No M2 code
  until you approve it.
- An independent review of the two fixes is still running.

## Rules we keep

- One milestone at a time. Nothing from M3 or later gets built, not even
  stubs.
- Before a step starts, WORKLOG says what it is. After every step, WORKLOG
  is pushed.
- Each step is one code commit, then one worklog commit, then a push.
- Claude stops and asks before any change to the approved plan, any new
  dependency or tool, anything that touches the real `/etc`, `/boot` or
  store, and any destructive VM action (reboot, enabling a unit,
  `apt upgrade`).
- Only one process commits to the checkout at a time.
- Nothing secret goes into the repo or onto the Pages site. Backups of `/etc`
  stay on the VM and are readable by root only.
- Evidence copied into `docs/` quotes paths, counts, modes and hashes only,
  never file contents from `/etc` (it can hold password hashes and keys).

## 1. Now, while the M2 plan is drafted

- [x] **Save the research outputs outside /tmp.** Done 2026-09-27: copied
      to `~/smartconfig-work/` (mode 0700), because `/tmp` is wiped at every
      boot. Repeated when each running process finishes.

- [ ] **Close the fix review.** Write its result into WORKLOG: what was
      found, confirmed and rejected. Each confirmed finding becomes a small
      fix commit with a test, before any M2 code. If nothing is confirmed,
      say so.
      Its stress runs show `database is locked` failures under many
      concurrent writers on both versions, and more often after `ec15ece`
      (2 to 7 times), because the restore now holds the write lock across the
      rename and its fsync. The restore then aborts cleanly instead of leaving
      an unrecorded change, but the review has to settle whether the busy
      handling needs work. Either way, M2 must keep write transactions short,
      because `scd` will be a second writer next to `sc`.
- [ ] **Freeze M1.** Put an annotated tag `m1` on the last M1 code commit
      (`ec15ece`, unless the review adds a fix) and push it.
- [ ] **Back up, outside the repo and outside /tmp.** Everything goes in
      `/var/backups/smartconfig` (mode 0700, root only):
  - `sc-m1`: the static binary built from tag `m1`. The rescue steps in
    PROJECT_LOG point here from now on. The repo's `bin/sc` will be half-built
    during M2.
  - `store-m1.tar`: a copy of `/var/lib/smartconfig` (4 rows; `2c6901` is the
    original fstab).
  - `etc-boot-m1.tar.zst` and `manifest-m1.txt`: `/etc` and `/boot/grub`, plus
    a sorted list of mode, owner, size and sha256 for each file. After each
    risky session, Claude rebuilds the manifest and diffs it, so WORKLOG can
    say "/etc left as found" or list exactly what changed.
- [ ] **You take a VirtualBox snapshot** named `m1-frozen` once WORKLOG
      says the freeze is done (see "What only you can do").
- [ ] **Check the M2 plan against these limits when it arrives** (the plan
      is drafted by a separate process; these become review checks, and any
      gap is fixed before you see the plan):
  - Keep SQLite's rollback journal and never switch to WAL. A WAL store
    cannot be opened from a read-only root (tested), and M4's rescue path
    needs exactly that. The rollback journal alone is not enough: a writer
    killed mid-transaction leaves a hot journal, so M2 must test that a
    `kill -9` of scd mid-transaction leaves a store the next writable open
    rolls back cleanly (`PRAGMA integrity_check` ok, no committed row lost).
  - Order rows by insert order (rowid), never by wall clock.
  - Test the schema migration on a copy of the real 4-row store. After
    migration, `sc-m1` can still log, cat, diff and restore file rows, and for
    every new row kind an `sc-m1` restore either fails cleanly leaving the
    file untouched, or does the right thing, proven by a test.
  - Keep write transactions short, so `sc restore` never fails with
    `database is locked` while `scd` works.
  - Tiers only set the log priority of scd's line for each change. Alerts,
    labels and "pending until update-grub" logic stay out of M2.
  - Scripts that change the real `/etc` or boot state refuse to run unless
    `systemd-detect-virt` reports a VM, or `SC_ALLOW_REAL_HOST=1` is set.
    The repo is public.
  - Write the acceptance run as a script that CI could also run. The
    "M2 done" list below is the acceptance bar.
- [ ] **One docs-tidy commit:**
  - PROJECT_LOG: status line, today's history, the rescue binary path,
    the snapshot commands, and the resume prompt ("read docs/WORKLOG.md and
    continue from Now"). Also a tip: after editing fstab, run
    `sudo findmnt --verify`.
  - M2_WATCHLIST: status line; section 8 points to where each decision is
    answered; rule 11's boot_id part and rules 17 and 21 marked "deferred to
    M3/M4, see M2_PLAN"; rule 12 and section 9 marked as describing the code
    before `ec15ece`.
  - README: the `docs/` line.
  - MILESTONES: M1 notes list the follow-up fixes.
- [ ] **Publish this page** and link it from WORKLOG.
- [ ] **Ask you the open questions** below, so the answers are in before M2
      step 1.
- [ ] Start no new review swarms until the plan drafts and the fix review
      finish. The VM is at load 18 to 22 on 9 CPUs.

## 2. From plan approval to M2 done

### 2.1 Right after you approve the plan

- [ ] Commit `docs/M2_PLAN.md` with:
  - its evidence appendix;
  - all 15 watch-list decisions answered;
  - the steps grouped into five chunks.

  WORKLOG "Now" says "M2 step 1", and the commit is pushed. This comes first
  because everything in /tmp, including the drafts and their evidence, is
  deleted at the next boot.
- [ ] Apply the tooling answers you said yes to, one commit each, before
      chunk A.

### 2.2 The loop for every step

1. WORKLOG says "Now: step N" and is pushed.
2. Code and tests.
3. Checks:
   - `gofmt -l .`, `go vet ./...` and `go test ./...` as the user;
   - `sudo -E go test` when root-only tests are touched;
   - `make race` if approved;
   - CI green, if approved.
4. One code commit, one worklog commit, push.

Claude does not ask between steps. It stops and asks when:

- the plan has to change;
- a new dependency is needed (for example `golang.org/x/sys`);
- something would touch the real `/etc`, `/boot` or store;
- a step has run about 2 hours without a commit (then it gets split);
- a test flakes and the reason is unclear.

### 2.3 Five chunks, each reviewed

The step numbers of the approved plan win. The chunks only group them.

| Chunk | What | Review | You see (FYI) |
|---|---|---|---|
| A | store housekeeping and schema migration | light | - |
| B | row kinds (file, link, deleted, digest) through the CLI | normal | a sample `sc log` |
| C | glob matcher and default scope | light | a table of what gets watched on this VM, per tier, and what is excluded |
| D | the inotify watcher engine | full: concurrency, events, security | - |
| E | `sc watch`, systemd unit, acceptance script, docs | normal | - |

- Reviewers run the code and try to break it. A skeptic re-checks each
  finding. Only confirmed findings become fix commits. Each review gets a
  line in WORKLOG.
- For chunks A to C, a review may run while the next chunk is built. There
  are never two unreviewed chunks.
- Chunk D's timing tests run with no review swarm active. Tests set the
  quiet period and the clock themselves, and poll until a deadline instead
  of sleeping. `go test -count=5 ./internal/watch/...` must pass before each
  watcher commit.
- At the end of chunk B, the M1 smoke run still passes and `sc-m1` still
  reads the migrated store.

### 2.4 M2 is done when all of these hold

- [x] Every plan step is committed and every chunk review is closed.
- [x] Checks are clean as user and as root (plus `make race` if approved).
      `file bin/sc` says statically linked.
- [x] `sudo ./scripts/accept-m2.sh` and `sudo ./scripts/smoke.sh` pass.
- [x] The owner scenario, run by hand after a fresh snapshot:
  - edit `/etc/ssh/sshd_config` with nano and never type `sc`: `sc log`
    shows the change and the old version;
  - `chmod -x /etc/grub.d/41_custom` shows as a mode-only row (then put it
    back). Not `10_linux`: if update-grub ran before the bit came back, the
    boot menu would lose its Linux entries. `41_custom` changes nothing here
    (BIOS VM, no `/boot/grub/custom.cfg`);
  - `systemctl mask` of a harmless unit shows as a link row (then unmask);
  - an edit made while scd was stopped appears once it starts;
  - a restore shows as a restore, with no extra automatic row.
- [x] Reboot test, with your OK: `scd.service` enabled for real, the VM
      rebooted. `systemd-analyze critical-chain` and `blame` show nothing
      waiting on scd, and scd is running.
- [x] Failure paths: `kill -9` scd during an apt burst and during its
      startup rescan. The unit restarts it, the missed change is recorded
      exactly once, and `PRAGMA integrity_check` reports ok. A static check
      shows no unit is ordered `After=` or `Requires=` scd.
- [ ] (Deferred 2026-10-02, checked next session.) A 24-hour soak that
      includes one `apt-daily-upgrade` run, plus a
      deliberate `apt upgrade` of the 13 pending updates while scd watches
      (apparmor alone owns 248 files under `/etc`). Pass means no crash, no
      flood, no unexplained rows, and bounded memory and CPU. The `/etc`
      manifest diff lists only expected changes.
- [x] A final review of the whole milestone leaves no open high-severity
      finding.
- [x] A fresh clone from GitHub builds and tests clean.
- [x] Docs updated: MILESTONES (M2 notes and the next "Current"), README,
      PROJECT_LOG, this page. Tag `m2`.
- [ ] You take the snapshot `m2-accepted` and sign off.

Rough size: 2.5 to 3 working days of session time plus the 24-hour soak,
so 3 to 4 calendar days. This is an estimate from code size, not a
measurement.

## 3. After M2

1. **Decide the milestone order** at M2 sign-off (question 1).
2. **Nested virtualization (optional speed-up).** When the VM is powered
   off for the `m2-accepted` snapshot, turn on nested VT-x if your host
   allows it. It is not required: QEMU is already installed and runs
   throwaway boot tests without `/dev/kvm` (TCG), just more slowly.
3. **Research first, docs only.** If M4 is next, prove the rescue recipe in
   a throwaway QEMU VM, never by breaking the dev VM's boot:
   - kernel arguments `ro fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1`;
   - root stays read-only;
   - the static `sc` reads the store;
   - the GRUB menu comes back after a failed boot.
4. **One owner scenario for each remaining milestone** in MILESTONES.md.
   One each, no detailed specs.
5. **Plan, approve, build** the next milestone with the same loop and
   reviewed chunks.
6. **A victim machine for boot-breaking tests** (M4, M6): QEMU with
   `-snapshot` inside the dev VM, and a VirtualBox linked clone for final
   acceptance.
7. **M3:** fstab is the first checker, with `findmnt --verify` as the
   reference in its tests. SmartConfig adds severity and a warning at save
   time.
8. **M7:** llama.cpp cannot live in the static binary. It would run as a
   separate opt-in process, and it is a new dependency, so we ask then.

## What only you can do

- [ ] Keep the VM running until the drafts and review results are committed.
      Do not reboot, power off or restore it before then.
- [ ] Take the `m1-frozen` snapshot:
  - GUI (VirtualBox 7.2, which this VM reports): in VirtualBox Manager select
    the VM, open the **Snapshots** tab above the right-hand panel, click
    **Take**, name it `m1-frozen`.
  - Host CLI: first `VBoxManage list vms` to get the exact name, then
    `VBoxManage snapshot "<VM name>" take m1-frozen --description "M1 done, before M2" --live`,
    then check it with `VBoxManage snapshot "<VM name>" list`.
- [ ] Know the way back. With the VM powered off, run
      `VBoxManage snapshot "<VM name>" restore m1-frozen`, then start it. A
      restore also rolls back Claude's memory and anything not yet pushed.
- [ ] Answer the open questions below.
- [ ] Approve or change the M2 plan.
- [ ] Only if you approve CI: run `gh auth refresh -h github.com -s workflow`
      on the VM, or better, use a fine-grained token limited to SmartConfig
      with Contents and Workflows read/write, and **no** Administration.
- [ ] Only if you approve the ruleset: create it yourself in the GitHub web
      UI (Settings > Rules > Rulesets > New branch ruleset, target the default
      branch, tick "Restrict deletions" and "Block force pushes", no bypass
      list). The VM's token never gets admin rights: the ruleset exists to
      guard `main` against mistakes made from this VM.
- [ ] Do not install the 13 pending updates until the M2 soak. That means
      neither Software Updater nor `apt upgrade`. They come from
      noble-updates, which unattended-upgrades does not install here.
- [ ] Before M2 acceptance: take a snapshot `m2-pre-accept`, and say yes or
      no to enabling scd, a reboot, the soak and the `apt upgrade`.
- [ ] At M2 sign-off, in this order:
  - shut the VM down;
  - optionally enable nested VT-x (Settings > System > Processor), a
    speed-up for later boot tests, not a requirement;
  - take `m2-accepted`;
  - start the VM and sign off.
- [ ] If the VM password is used anywhere else, change it there.

## Open questions, with our recommendation

**Answered 2026-09-28:** yes to every recommendation below. Question 7 (the
ruleset) and question 13 (host details) still need you.

1. **Build the rescue path (M4) before the checkers (M3)?** Answered
   2026-10-03: no, M3 first (M4's boot tests need QEMU without KVM, slow
   on this VM; M3 is built in the repo while the M2 soak runs). Was: decide at M2
   sign-off; nothing changes before then. We lean yes, if the feasibility
   check in section 3 holds. The worst failures leave root locked with no
   shell, and only M4 recovers from them. The cost: "warn before a bad
   edit" comes later, and the films need a small edit.
2. **Tiers in M2 only as log priority?** Yes. Alerts and labels go to M3.
3. **boot_id column in M2 or M4?** M4. CLAUDE.md says "not even stubs",
   and M4 needs its own boot-ok marker anyway.
4. **Race tests with `CGO_ENABLED=1`, tests only?** Yes, as `make race`.
   The shipped binary stays static.
5. **CI on every push?** Yes. Pin the runner to ubuntu-24.04 and the
   actions by SHA, give it read-only permissions, and set no required
   checks.
6. **Local git hooks against leaking secrets?** Yes. GitHub push protection
   does not catch a plain password, and anything in `docs/` is public on
   Pages within a minute. The hooks block a private denylist (such as the
   VM password) and the patterns of password hashes (`$6$...`, `$y$...`),
   `-----BEGIN ... PRIVATE KEY-----` and `psk=`.
7. **Ruleset on main (no force-push, no deletion)?** Yes, created by you
   in the web UI (see "What only you can do").
8. **CLAUDE.md edits** (`docs/` in Layout, "read WORKLOG first",
   "push after every step", the race exception)? Yes.
9. **Go 1.22.2 or a supported Go?** Move to a supported Go (1.27.x, or
   1.26.8, which is already in the module cache) in one commit before
   chunk A, checked with govulncheck. On Linux, govulncheck finds 2 reachable
   standard-library issues (GO-2026-4341, GO-2025-3849), and M2 makes `sc` a
   root daemon. govulncheck would be a new tool, so this is your call.
10. **A GitHub issue as well as WORKLOG?** No. WORKLOG only.
11. **Should the chunk B and C checkpoints block work?** No, FYI only.
12. **Reboot, 24-hour soak and apt upgrade for M2 acceptance?** Yes, each
    after a fresh snapshot and an announcement.
13. **Host OS, Hyper-V status, free disk?** Please tell us before M2 ends.
    It decides whether nested VT-x can speed up later boot tests and how many
    snapshots and clones the host can hold. Boot tests work without it.

## How disagreements were settled

- **Milestone order:** one advisor proposed swapping M3 and M4. This is your
  decision (question 1), made at M2 sign-off.
- **Alerts in M2:** one advisor wanted all alerting moved out of M2, but
  decision 1 gives tiers alert loudness. We keep the smallest form: tier =
  log priority.
- **boot_id:** proposed for M2. We defer it to M4, following CLAUDE.md and
  the lean draft.
- **Nested virtualization now or later:** not now. Powering off now would
  wipe /tmp and stop running work, so it happens at the `m2-accepted`
  power-off.
- **Where backups live:** one place, `/var/backups/smartconfig`, root only.
  Not the home directory, never the repo.
- **Reviews in parallel with building:** allowed for chunks A to C. Chunk
  D's timing tests run alone.
- **CI optional or strongly advised:** we advise it, but it is tooling, so
  it is your call (question 5).
- **Owner scenarios for M3 to M7:** after the order decision, not now.
- **Snapshot timing:** after the freeze, which takes minutes. If that is
  delayed by hours, take one now as well.

## Risks to watch

- **/tmp is wiped at every boot.** Anything not committed is lost.
- **`bin/sc` is half-built during M2.** Use `sc-m1` for rescue.
- **The migration changes the real store** that holds the fstab recovery
  row. Test on a copy first, and ask before the first real run.
- **VM load makes timing tests flaky.** Tests loosened just to pass can
  hide real bugs.
- **"Never blocks boot" is untested without a real reboot.**
- **Applying the 13 updates early** throws away the best apt-burst test.
- **Old Go version.** Go 1.22.2 is past end of life (2 reachable stdlib
  issues on Linux), and M2 makes `sc` a root daemon.
- **Secrets.** The VM password is in local Claude transcripts. It must
  never reach the repo or Pages.
- **Tracking drift.** WORKLOG is the source of truth. PROJECT_LOG and this
  page are updated at milestone boundaries.

## Review corrections applied

A critic checked this roadmap against the VM, the repo and GitHub. Its
corrections are applied above: the hot-journal and `sc-m1` restore checks,
the failure-path tests, the safe `41_custom` scenario, the VirtualBox 7.2
steps, the token without admin rights, the secret-pattern hooks, the Go
vulnerability count, and nested virtualization as a speed-up rather than a
requirement.

## Evidence

- `/usr/lib/tmpfiles.d/tmp.conf`: `D /tmp 1777 root root 30d`.
  `systemd-tmpfiles-setup` runs with `--remove --boot`.
- GitHub API: 0 tags, 0 rulesets, and the only workflow is
  `pages-build-deployment`. The gh token scopes are `gist`, `read:org` and
  `repo`.
- `apt list --upgradable`: 13 packages, all from noble-updates, 0 security.
  `50unattended-upgrades` allows only the release and `-security` origins.
- `dpkg-query` shows 248 apparmor conffiles under `/etc`. The next
  `apt-daily-upgrade` run is Mon 2026-09-28 06:32 UTC.
- `uptime`: load 18 to 22 on 9 CPUs, about 1.7 to 2 GiB available.
- No `/dev/kvm`, and 0 vmx/svm flags. `qemu-system-x86` is installed and runs
  without KVM (TCG).
- govulncheck on a copy of the repo: GO-2026-4341 and GO-2025-3849 are
  reachable on Linux (GO-2025-3750 is Windows-only).
- The guest's DMI OEM strings (`dmidecode -t 11`) report VirtualBox 7.2.2.
- Scratchpad tests: a read-only rollback-journal store serves `log`, `cat`
  and `diff`. A WAL store fails with "unable to open database file (14)".
- `passwd -S root` shows `L`, and the recovery menu's root option runs
  plain `/sbin/sulogin`.
