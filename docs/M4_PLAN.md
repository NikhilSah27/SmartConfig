# M4 plan: the rescue path

Status: **approved** (2026-10-04, "approved": every recommendation of
section 10). Drafted in the session from the boot research in a
throwaway QEMU VM (Appendix A).

## 0. Summary

- **What you get.**
  - A boot menu entry, **SmartConfig rescue**. It boots this machine to a
    root shell in about 40 s, however broken `/etc/fstab` is. Root stays
    read-only and `/etc/fstab` is ignored.
  - Above that shell's prompt, **`sc status`** says:
    - which boot was the last healthy one;
    - which recorded files changed since then, and which of those
      changes sc's checkers call a blocker;
    - the exact commands to put a file back.
  - **The menu comes back by itself after a failed boot**, even on a
    desktop install whose GRUB menu is hidden.
  - **sc works on a read-only root:** `log`, `diff`, `cat`, `check` and
    `status`. That includes a store a crash left half-written (a hot
    journal); the store is never changed.
  - **Every boot gets a verdict:** ok, bad, or never reached multi-user.
- **What the research changed** (Appendix A):
  1. **Emergency mode on Ubuntu 24.04 is not a dead end.** Ubuntu
     patches sulogin to open a root shell without a password even
     though root is locked (`sulogin-lockedpwd.patch`; util-linux
     2.39.3-9ubuntu6.6 on this VM too). Our docs say the opposite: the
     fstab rule's explanation, PROJECT_LOG, JOURNEY and the "without
     SmartConfig" film. M4 corrects them first (step 1). The real
     problems are elsewhere:
     - over SSH the machine is gone;
     - every boot waits 90 s;
     - the same fault ended three ways in three boots;
     - on a serial console, the console getty fights the shell for
       input;
     - nobody tells the owner what changed.
  2. **`rescue.target` alone does not help.** It still pulls in
     `local-fs.target`, so the bad line waits 90 s and the boot drops to
     emergency mode, with root mounted read-write. `fstab=no` is what
     makes the rescue boot reliable.
  3. **The usual "boot succeeded" signals miss this failure.** Nothing
     pulls in systemd's `boot-complete.target` on Ubuntu, and a mount
     that never happened is "inactive (dependency)", not failed. Worse,
     Ubuntu's `grub-common.service` clears GRUB's `recordfail` on such a
     boot, so the menu does not come back. sc needs its own verdict,
     which checks `local-fs.target`, and its own menu flag.
- **Store: no schema change.** M2 and M3 binaries refuse a store whose
  schema is newer than theirs, and the backup binaries kept for rescue
  must keep reading it. Boot verdicts go to a small append-only file
  next to the store, and "changed since the last healthy boot" is found
  by row order, which is insert order.
- **How it gets built.** 13 steps in five chunks, one commit each with
  its tests, the worklog pushed after every step, and a review per
  chunk. Boot behaviour is proven by an automated end-to-end test in the
  QEMU lab (`make lab-e2e`), never by breaking this VM's boot. The
  sign-off on this VM needs your OK and a snapshot.

## 1. Goals and non-goals

**Goals.** M4 is done when all of these hold, plus the sign-off in
section 7.

1. **A broken file can be undone from the console without a live USB.**
   One menu entry and one command: `sc restore ID`.
2. **The rescue path changes nothing by itself.** Root stays read-only
   until the owner remounts it, and `sc status` only reads.
3. **The menu shows after a failed boot.** That covers a boot that
   never reached multi-user and one that reached it with
   `local-fs.target` failed.
4. **sc reads the store on a read-only root, hot journal included,**
   without writing to it.
5. **Boot verdicts are right.** "ok" only when multi-user was reached
   with `local-fs.target` active, no emergency or rescue mode, and no
   failed units; "bad" otherwise.
6. **Normal boots do not get noticeably slower.** Two oneshot units:
   one early, one after multi-user, each measured in the lab and on
   this VM.
7. **All checks pass.** gofmt, vet, tests as a user and as root, race,
   the static build, `make m1-compat`, and `make lab-e2e` in BIOS and
   UEFI mode.

**Non-goals** (not built, not even as stubs): packaging (M5; M4 installs
by hand like scd); LUKS, LVM and btrfs roots; an entry for every kernel;
managing a GRUB password; network in rescue; a TUI; the deferred items
in section 9.

## 2. What the lab showed

| Question | Answer (details in Appendix A) |
|---|---|
| A bad fstab line, normal boot | After a 90 s wait, emergency mode gives a root shell with no password. The outcome varies: stuck in emergency mode, continued to multi-user, or the getty hung up the shell. sc records nothing (scd is not running). |
| `ro fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1` | Rescue shell about 20 s after systemd starts. Root is read-only, `/boot` and `/boot/efi` are not mounted, journald and udevd run. **Recommended.** |
| Without `fstab=no` | rescue.target: a 90 s wait, then emergency mode with root read-write. emergency.target: `grub-initrd-fallback.service` pulls in sysinit, root goes read-write, and the bad mount waits in the background. |
| The static sc on a read-only root | log, diff and cat work. check needs a writable `TMPDIR` (`/run` works). restore gives a clean "read-only file system" error until `mount -o remount,rw /`, then works. |
| A hot journal on a read-only root | Every read fails with `attempt to write a readonly database (776)`. A copy of the database and its journal in `/run` reads fine; the real store is untouched. |
| GRUB entry `/etc/grub.d/42_smartconfig` | Generated by update-grub. Works through `grub-reboot`, the menu (UEFI serial, BIOS screen) and Secure Boot (shim plus signed kernel). |
| recordfail | The 30 s menu returns only when the failed boot never reached multi-user. `grub-common.service` clears it otherwise. |
| A report before the shell | A drop-in on rescue.service and emergency.service with `ExecStartPre=-sc …` prints right above the "Press Enter" line. A separate unit's output scrolls away. |
| A boot-ok marker | `boot-complete.target` is unused. A unit after multi-user.target that checks `local-fs.target` tells ok from bad. A boot that never got there leaves only the early "seen" line. |
| Speed under TCG | About 75 s to a login prompt and about 47 s to the rescue shell. Some boots stall for minutes, and two panicked ("IO-APIC + timer"; `no_timer_check` fixes it). |

## 3. Architecture

```
GRUB ─ "Ubuntu" (default)
     └ "SmartConfig rescue": ro fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1
         └ rescue.service + drop-in: ExecStartPre=-/usr/local/sbin/sc status --console
             └ shell: mount -o remount,rw / ; sc restore ID ; sync ; systemctl reboot

every boot: sc-boot-seen.service (early, after remount-fs)  → boots: "<id> seen", grubenv smartconfig_pending=1
            sc-boot-ok.service (after multi-user.target)   → boots: "<id> ok|bad <rowid> <why>"; ok clears the flag
next boot:  42_smartconfig in grub.cfg: smartconfig_pending=1 → show the menu for 30 s
```

### 3.1 `sc status`

Read-only. It prints, one fact per line, in plain text for an 80×25
console:
- **This boot:** its id; rescue (from `/proc/cmdline` and
  `systemd.unit=`) or normal; root read-only or read-write.
- **Last healthy boot:** its time and the store row it ended at.
- **Changes since then**, newest first: id, time, file, tier, and for a
  file with a checker the worst finding of that version. The two
  versions are checked against each other as `sc edit` does, so only
  the problems a change added are named.
- **What to do**, with the exact commands for the newest blocker:
  remount, `sc restore ID`, `sync`, `systemctl reboot` (after
  `systemctl daemon-reload` when the boot is in the fstab-failure
  emergency mode).
- **scd:** running or not, by trying its lock in `$SC_HOME`. No shell
  call is needed.

`--console` is the rescue form. It caps itself at 10 s and 20 lines
(older changes are counted), and runs the checks with sc's own rules
only when the validators are not there or the root is read-only.

### 3.2 Boot verdicts (`$SC_HOME/boots`)

- An append-only text file, mode 0600. Each line is shorter than
  `PIPE_BUF` and written with `O_APPEND`. Above 1 MiB it keeps the last
  1,000 lines.
- `sc boot seen` (hidden, run by `sc-boot-seen.service`) appends
  `<boot_id> seen <time>`. The unit is ordered after
  `systemd-remount-fs.service`, and runs only when `/var/lib` is
  writable (`ConditionPathIsReadWrite`).
- `sc boot verdict` (hidden, run by `sc-boot-ok.service`, which is
  `WantedBy` and `After` `multi-user.target`) does three things:
  - it asks systemd for `local-fs.target`, `emergency.target` and
    `rescue.target`, and the failed-unit count, through the M3 runner:
    `/usr/bin/systemctl is-active …`, a fixed path, no shell, a
    timeout;
  - it appends `<boot_id> ok|bad <rowid> <why>`, where rowid is the
    newest store row;
  - it clears the menu flag only on "ok".
- **Readers:**
  - The last healthy boot is the last "ok" line.
  - A boot is failed if it has a "seen" line and no verdict, or a
    "bad" verdict.
  - The changes since the last healthy boot are the rows after its
    rowid.
- **No schema change:** sc-m1, sc-m2 and sc-m3 keep reading the store.

### 3.3 The menu after a failed boot

- `sc boot seen` sets `smartconfig_pending=1` in `/boot/grub/grubenv`
  with `grub-editenv`, through the runner. A good verdict unsets it.
- `42_smartconfig` also adds a few lines to grub.cfg: when
  `smartconfig_pending` is 1, GRUB sets `timeout_style=menu` and
  `timeout=30`, and the default entry stays the default.
- These are the same writes Ubuntu's recordfail already makes at every
  boot.
- **Skipped where GRUB cannot write grubenv,** decided the way
  `00_header` decides it: btrfs, zfs, LVM or RAID under `/boot`, or a
  `/boot` that is not mounted.

### 3.4 The rescue entry: `scripts/42_smartconfig`

- It picks the newest kernel the way `10_linux` does, and uses
  `prepare_grub_to_access_device`.
- Its `linux` line keeps `root=` and every token of `GRUB_CMDLINE_LINUX`
  and the `console=` tokens, drops `quiet splash`, and adds the recipe:
  `ro fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1`.
  Upstream sulogin needs the last argument; Ubuntu's does not.
- It is installed by hand (README) like scd, until M5:
  `install -m 0755 scripts/42_smartconfig /etc/grub.d/` and
  `update-grub`.
- The text the entry prints is "SmartConfig rescue: root read-only,
  /etc/fstab ignored".

### 3.5 sc on a read-only root

- **Read-only open.** When the store's directory or file cannot be
  written, the store is opened with `mode=ro`, and migrations never run
  on that open. A store at an older schema is still read, through the
  columns it has.
- **Hot journal** (`SQLITE_READONLY_ROLLBACK`). sc copies `changes.db`
  and its journal into a private 0700 directory under `/run`, opens the
  copy (SQLite rolls it back there), and links `objects/` back to the
  store. It says once: "the store has a write a crash left unfinished;
  sc reads a repaired copy; the next sc run on a writable root repairs
  the store itself".
- **Scratch for `sc check`** (M3 follow-up 6). Scratch copies fall back
  in turn to `$SC_HOME/tmp`, `$TMPDIR`, `/run/sc-check` (0700) and
  `/dev/shm`. With none writable, sc's own rules run and a note says
  that no validator could.
- **Restore** keeps its clean "read-only file system (file not changed)"
  error. The rescue text says to remount first.

### 3.6 Before a restore: immutable files

`sc restore` reads the target's and its directory's inode flags
(`FS_IOC_GETFLAGS`). It refuses an immutable (`+i`) or append-only (`+a`)
target before writing anything, and names the `chattr` to run. This is
the M2 plan's deferred "+i/+a precheck".

## 4. CLI

| Command | What |
|---|---|
| `sc status` | Section 3.1. Exit 0 healthy, 2 when the last boot failed or a change since the last healthy boot has a blocker or error, 1 when sc itself failed. |
| `sc status --console` | The rescue form (section 3.1). |
| `sc boot seen`, `sc boot verdict` | Hidden; run only by the two units. |
| `sc restore` | Also refuses `+i`/`+a` targets (section 3.6). |

## 5. Security

- **The rescue entry is a password-less root shell from a menu item.**
  On Ubuntu 24.04 that is no new power: emergency mode already gives
  one at the console (the sulogin patch), and so does editing any GRUB
  entry (Ubuntu sets no GRUB password).
- **The README says how to add a GRUB superuser password** that guards
  all entries but the default (`--unrestricted`), and that disk
  encryption still asks for the passphrase in rescue (untested here).
- **The new programs sc runs are `systemctl` and `grub-editenv`,** both
  through the M3 runner: fixed paths, no shell, `LC_ALL=C`, timeouts.
- **`/run` copies of the store are 0700, root-only,** and removed when
  sc exits.

## 6. Order of implementation

| Step | Chunk | Commit | Tests |
|---|---|---|---|
| 1 | A | `docs, check: Ubuntu's emergency mode gives a root shell` (fstab explanation, PROJECT_LOG recovery guide, JOURNEY, the film's console text) | rule text test |
| 2 | A | `store: read-only open, hot journal copy` | a read-only store directory as a user; a hot journal made by a killed writer; no write to the store (checksums) |
| 3 | A | `check: scratch fallback when the store is read-only` | read-only `$SC_HOME` and `TMPDIR` |
| 4 | B | `boot: seen and verdict lines, sc boot` (with the runner calls faked in tests) | ok, bad (local-fs inactive), never reached multi-user, a rotated file |
| 5 | B | `scripts: sc-boot-seen and sc-boot-ok units` | `systemd-analyze verify`; ordering read back |
| 6 | B | `sc status` (normal and `--console`) | fabricated boots and rows; a read-only store; the 10 s and 20-line caps |
| 7 | C | `scripts: 42_smartconfig` (entry and menu flag) | run against fabricated `/boot` trees; `grub-script-check` on its output |
| 8 | C | `boot: grubenv flag` (set at seen, cleared on ok, skipped where unsupported) | faked `grub-editenv` |
| 9 | C | `scripts: rescue and emergency drop-ins` | `systemd-analyze verify` |
| 10 | C | `restore: refuse +i and +a targets` | as root on a scratch ext4 loop image, or skipped |
| 11 | D | `lab: QEMU rescue lab and make lab-e2e` (from the research scripts; the image is cached outside the repo) | the owner scenario end to end, BIOS and UEFI |
| 12 | E | `scripts: accept-m4.sh` (the non-boot parts on this VM, as root) | |
| 13 | E | docs: README "When the machine does not boot", PROJECT_LOG, MILESTONES | |

The chunk reviews are after steps 3, 6, 10 and 13; then the sign-off.

## 7. Acceptance and sign-off

**Owner scenario (the one for M4).** You add a line for a disk that is
not there to `/etc/fstab` with nano, ignore scd's warning, and reboot.
- The boot fails (90 s, then emergency mode or a degraded desktop).
- At the next boot the menu shows by itself, with "SmartConfig rescue".
- You pick it. Above the prompt the console says that `/etc/fstab`
  changed since the last healthy boot, with blocker
  `fstab-source-missing`, and gives the commands.
- You press Enter and run them. The next boot is normal, its verdict is
  "ok", and `sc status` says healthy.

**`make lab-e2e`** runs exactly that in QEMU, BIOS and UEFI, and passes
before anything is installed on this VM.

**Sign-off runs, each with your OK:**
- **S1:** all checks, `make lab-e2e`, and `accept-m4.sh` as root on this
  VM (no reboot).
- **S2:** a fresh VirtualBox snapshot. Install the M4 build, the units,
  the drop-ins and `42_smartconfig`, run update-grub, and reboot once
  normally. The verdict is "ok", the boot is not slower
  (`systemd-analyze`), and the menu stays hidden.
- **S3:** the owner scenario on this VM. It is a desktop, so the
  tty1-versus-gdm question gets its answer here.
- **S4:** final review, docs, tag `m4`.

## 8. Risks

- **The lab is slow and flaky under TCG** (stalls, timer panics, a slow
  udev). Use `no_timer_check`, up to 3 tries per boot, and trust
  guest-side timings.
- **A separate `/boot` is not mounted in rescue.** `sc status` says
  `mount /boot` before grub commands. restore never needs it unless the
  file is under `/boot/grub`.
- **Kernel updates.** `update-grub` reruns `42_smartconfig`, so the
  entry follows the newest kernel. If that kernel is the broken part,
  the stock "Advanced options" entries remain.
- **grubenv writes at every boot.** These are the same writes recordfail
  makes, skipped where GRUB cannot write.
- **Inconsistent fstab-failure boots.** M4 does not depend on emergency
  mode. It depends on the next boot's menu and the rescue entry.
- **A hot journal copy needs RAM in `/run`.** It is the size of
  `changes.db` (under 1 MB here); objects are not copied.

## 9. Deferred

- dirfd or uid-drop restores into user-owned directories, and dirfd
  link reads (M2's list)
- mount tracking beyond what `sc status` shows
- an entry for the previous kernel
- LUKS, LVM and btrfs roots
- GRUB password management
- network in rescue
- M3 follow-ups 1-5, 7 and 8 (MILESTONES), decided at M4 sign-off as
  for M2

## 10. Questions for you

1. **Scope** as in sections 1 and 9. *Recommendation: yes.*
2. **Show the menu after a failed boot through sc's own grubenv flag**
   (two writes per boot, like recordfail, skipped where unsupported).
   *Recommendation: yes. Ubuntu's recordfail misses the fstab failure
   that reaches multi-user.*
3. **No GRUB password by default.** The README explains how to add one.
   *Recommendation: yes. Console access already gives root on Ubuntu
   24.04.*
4. **Install by hand** (README), like scd, until the M5 package.
   *Recommendation: yes.*
5. **Keep the QEMU lab in the repo** (`lab/`, `make lab-e2e`, not in CI),
   with the image cached under `~/.cache/smartconfig-lab`.
   *Recommendation: yes. It is the only safe way to test boot breakage.*
6. **Boot verdicts in a file, not a store column** (section 3.2), so the
   rescue binaries keep reading the store. This replaces NEXT_STEPS
   question 3's `boot_id` column. *Recommendation: yes.*
7. **Correct the "no shell" claims first** (step 1). *Recommendation:
   yes.*

## 11. Estimate

About 4 working days: chunk A 1 day; B 1 day; C 1 day; D (the lab)
half a day, and boots under TCG are slow; E and the sign-off half a day,
plus your time for S2 and S3, which need reboots of this VM.

## 12. Changes after approval

| # | Date | Change | Why | Commit |
|---|---|---|---|---|

---

## Appendix A. Evidence (QEMU lab, 2026-10-04)

**Setup:**
- Ubuntu 24.04 cloud image, kernel 6.8.0-142, systemd 255.4-1ubuntu8.17,
  util-linux 2.39.3-9ubuntu6.6 (this VM runs the same systemd and
  util-linux).
- Root locked (`passwd -S root` gives `L`). sc built from tag `m3`.
- UEFI (OVMF) unless noted, TCG, `-smp 2 -m 2048`, niced.
- A copy-on-write overlay over the image: this VM's own boot was never
  touched.
- The lab, its scripts and console logs are in `~/smartconfig-work/m4lab`
  (`NOTES.txt` is the index).

**A1. Boot times** (guest-side):

| Boot | Time |
|---|---|
| First boot to login | 108.6 s |
| Later boots to login | about 74–78 s |
| Rescue entry, firmware to prompt | about 47 s (systemd starts at uptime 21 s) |
| Emergency mode with `fstab=no` | 29.8 s |

Some boots stalled 1–6 minutes; two panicked ("IO-APIC + timer doesn't
work"), and `no_timer_check` avoids it.

**A2. Bad fstab line** (`UUID=3f6c1e2a-… /mnt/backup ext4 defaults 0 2`;
`sc check` flags it as a blocker):
- From reboot to emergency prompt: 177 s. The `[ TIME ]` line for the
  device, then `[DEPEND]` for the mount and `local-fs.target`.
- "Press Enter for maintenance" gives `root@m4lab:~#` without a
  password. sulogin ran without `--force`.
- Three runs ended three ways:
  - continued to graphical, with the serial getty and the shell
    splitting the typed input;
  - stuck in emergency mode, no ssh;
  - reached multi-user, then the getty hung up the console and the
    shell exited.
- `systemctl reboot` from that shell did not reboot (the stale mount
  waited again) until after `systemctl daemon-reload`.

**A3. Kernel argument combinations** (with the bad line):

| Arguments | Result |
|---|---|
| A: `ro systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1` | 90 s, then emergency mode; root read-write; `/boot` mounted |
| **B: A plus `fstab=no`** | rescue in about 20 s; root read-only; `/boot` and `/boot/efi` not mounted |
| C: `systemd.unit=emergency.target` | `grub-initrd-fallback.service` (`Requires=sysinit.target`) remounts root read-write and starts the 90 s wait in the background |
| D: C plus `fstab=no` | shell in about 8 s; root read-only |
| E: stock "recovery mode" | the 90 s wait, then emergency; the recovery menu never showed |

**A4. sc on a read-only root:**
- `sc log`, `sc diff` and `sc cat` work.
- `sc check` fails with `mkdir /tmp/sc-check-…: read-only file system`,
  and works with `TMPDIR=/run`.
- `sc restore` refuses cleanly while root is read-only. After
  `mount -o remount,rw /`, `sc restore 43e4d2` gave `restored
  /etc/fstab from 43e4d2 … previous state saved as bcdc8a`. The next
  boot reported `running` with 0 failed units.

**A5. Hot journal:**
- Made with a sqlite3 writer killed mid-transaction: `changes.db`
  1,245,184 bytes plus a `changes.db-journal` of 5,632 bytes.
- On a read-only root, every sc read failed with `attempt to write a
  readonly database (776)`.
- A copy of both files in `/run`, with `objects/` linked back, read
  fine, and the real store was untouched.
- 200 random `kill -9`s of `sc snapshot` left no journal: power loss is
  the realistic cause.

**A6. GRUB:**
- The `42_smartconfig` entry works through `grub-reboot
  smartconfig-rescue`, from the menu over UEFI serial and on the BIOS
  screen, and under Secure Boot (MS keys; lockdown `[integrity]`).
- After a failed boot that never reached multi-user, with the desktop's
  default recordfail timeout, a hard reset brought back the menu with
  "executed automatically in 30s".
- After a failed boot that did reach multi-user, `grub-common.service`
  cleared recordfail and no menu came back.
- Pressing Esc to reach the hidden UEFI menu: a second press drops to
  `grub>`, where `normal` boots the default.

**A7. A report before the shell:**
- A drop-in `ExecStartPre=-/usr/local/sbin/sc-rescue-report` on
  rescue.service and emergency.service printed right above "You are in
  rescue mode … Press Enter". `sc log` took 1.3 s.
- A separate unit's output was pushed off the screen by about 40 status
  lines.
- In the fstab-failure emergency mode where the getty hung up the
  console, the report's `sc` output was lost.

**A8. A boot-ok marker:**
- `boot-complete.target` exists and nothing pulls it in;
  `systemd-boot-check-no-failures.service` is disabled.
- The failed mount ends "inactive (dependency)", so `systemctl --failed`
  is empty.
- A prototype pair of units wrote these lines:
  - normal boots: `ok … local-fs=active failed_units=0`;
  - the failure that reached multi-user: `bad … local-fs=inactive`;
  - the failure stuck in emergency mode: only `seen`;
  - read-only rescue boots: nothing.
