# Project log and recovery guide

Everything done so far, why, the current state of the development VM, and how
to get back to work if something breaks. Written for a human or a Claude Code
session picking the project up cold.

Last updated: 2026-10-07, M4 sign-off S1 to S3 passed; its final review is in.
Tags: `m3` 2026-10-03, `m2` 2026-10-02, `m1` 2026-09-28.

---

## 1. Where things are

| What | Where |
|------|-------|
| Code | https://github.com/NikhilSah27/SmartConfig (public, branch `main`) |
| Live docs site | https://nikhilsah27.github.io/SmartConfig/ (GitHub Pages, serves `docs/`) |
| Project rules | [CLAUDE.md](../CLAUDE.md), read first, every time |
| Milestone status | [MILESTONES.md](../MILESTONES.md) |
| The story so far, choices and deviations | [JOURNEY.md](JOURNEY.md) |
| Visual explainers | [docs/README.md](README.md) (system map, narrated films) |
| Dev VM checkout | `/home/vboxuser/code/smartconfig` |
| Dev VM data store | `/var/lib/smartconfig` (root only) |
| QEMU rescue lab cache | `~/.cache/smartconfig-lab` (images, runs; [lab/README.md](../lab/README.md)) |
| Scratch outside the repo | `~/smartconfig-work` (plans as drafted, raw review output, lab evidence in `signoff/`) |

Status (2026-10-07): **M4, the rescue path, is built**; sign-off S1 to
S3 passed, and S4's final review is in ([M4_PLAN.md](M4_PLAN.md)). The
build S1 tested (`sc` `cafca151…`) is installed on the dev VM since S2,
and runs as `scd`; the final review's fixes are not installed yet. **M3
is done** (tag `m3`). Before that: **M2** (tag `m2`) and
**M1** (tag `m1`, store + CLI, hardened by four review rounds and five
rounds of fixes, see [reviews/](reviews/)). Live progress:
[WORKLOG.md](WORKLOG.md).

M2 research: [M2_WATCHLIST.md](M2_WATCHLIST.md) lists what the watcher must watch,
ignore and beware of, verified on this VM, plus two M1 fixes needed first and
the decisions to make before M2 code starts.

---

## 2. What happened, in order (2026-09-27)

1. **Brief and plan.** Read the M1 spec and CLAUDE.md, wrote a plan and file
   tree. Found a contradiction in the spec before coding (see decision D1) and
   stopped to ask.
2. **Commits, one step each** (`git log` has the details):
   - `690ab95` CLAUDE.md, go.mod, .gitignore
   - `481aa99` internal/fsutil: ReadWithMeta, WriteAtomic
   - `93394d1` internal/store: blobs + SQLite, snapshot/list/get/restore, diff
   - `9dbe3f9` cmd/sc: init, snapshot, log, cat, diff, restore
   - `394dfcc` Makefile
   - `ef200fe` scripts/smoke.sh
   - `020c205` MILESTONES.md, M1 marked done
3. **Acceptance run as root.** `sudo ./scripts/smoke.sh` passed against the
   real `/etc/hosts`; the root-only ownership test passed.
4. **Real fstab break-and-restore** (for the film): snapshot `/etc/fstab`,
   changed one UUID character (`c4d8` → `c4b8`), `sc diff`, `sc restore`.
   `cmp` confirmed the file identical afterwards; `systemctl daemon-reload` run.
   Syscalls captured with `strace`.
5. **Boot-failure research** with systemd's own generator (no reboot, see D6).
6. **Visual docs:** system map, narrated films. Fixed two playback bugs found
   by loading the pages in headless Chrome (`847e14a`, `994f16c`).
7. **README**, branch renamed `master` → `main`, repo created on GitHub,
   GitHub Pages enabled (`f5d81bc`).
8. **Final check from a fresh clone of GitHub:** go test, go vet, gofmt clean;
   static binary; root tests pass; `sudo ./scripts/smoke.sh` PASS; all four
   Pages URLs load and their scripts run.

---

## 3. Decisions and why

**D1. Snapshot ids: rehash on conflict.** The spec's id is
`sha256(path + "\n" + ts + "\n" + blob)[:6]` with `ts` in whole seconds. Two
rows for the same path, content and second collide, and the acceptance run
itself does this (the pre-restore row right after the second manual
snapshot). Chosen fix: if the id exists, hash again with `"\n1"`, `"\n2"`, …
appended. Ids stay 6 characters. Covered by
`TestRestoreSameSecondSameContent`.

**D2. Module name `smartconfig`.** Replaced the `github.com/YOURNAME/…`
placeholder. It still works as a GitHub repo because nothing imports it from
outside.

**D3. SQLite driver pinned to `modernc.org/sqlite v1.29.10`.** Newer versions
require Go 1.25; the VM had Go 1.22.2. Since 2026-09-28 go.mod requires Go
1.26.8 (roadmap question 9), which the `go` command downloads by itself, so
do not set `GOTOOLCHAIN=local`. The SQLite pin is unchanged.

**D4. `sc snapshot -q` prints the id even when unchanged,** so
`id=$(sc snapshot -q …)` always gets something usable.

**D5. Small choices:** diff labels drop the path's leading slash
(`a/etc/hosts`); a missing final newline shows diff(1)'s marker; blobs are
checksum-verified before use and a corrupt blob is never restored; restore
refuses a symlink target; `sc log` with no rows prints `no snapshots`.

**D6. Which fstab typos break a boot (verified 2026-09-27).** Ran
`systemd-fstab-generator` with `SYSTEMD_FSTAB` pointing at broken copies:
- A typo in the **root** line (`/`): the generated `-.mount` only has
  `After=` on the device, no `Requires=`, and root is already mounted by the
  initramfs. Ubuntu most likely still boots.
- A typo in a **data disk** line (e.g. `/data`): `data.mount` gets
  `Requires=systemd-fsck@<device>.service`, so boot waits 90 s
  (`DefaultTimeoutStartSec`), fails `local-fs.target`, and stops in emergency
  mode. The root account is locked (`passwd -S root` → `L`), but Ubuntu
  24.04's sulogin opens a root shell there anyway, at the console only
  (`sulogin-lockedpwd.patch`; shown in the M4 lab). Over SSH the machine is
  gone.
This is the scenario M3 (checkers) and M4 (rescue path) must handle.

---

## 4. Known issues and follow-ups

- `changes.db` was created mode **0644** by M1 (its directory is 0700, so it
  was not readable by others). Since M2 step 2, `sc init` creates it 0600
  and tightens an existing one; the real store stays 0644 until M2's
  `sc init` runs on it (sign-off S2).
- The ownership part of the restore test only runs as root
  (`sudo -E go test ./internal/store`).
- Film narration uses the browser's speech engine. On Linux it may be silent
  (needs `speech-dispatcher` and working audio). The films detect this and
  turn the voice off instead of stalling.
- The claude.ai artifact links in `docs/README.md` are private to the owner;
  the GitHub Pages links work for everyone.

---

## 5. State of the dev VM

| Item | Value |
|------|-------|
| OS | Ubuntu 24.04.5 LTS, kernel 7.0.0-38-generic, systemd 255, GRUB 2.12 (`grub-common` 2.12-1ubuntu7.3); BIOS boot, GRUB menu hidden (`GRUB_TIMEOUT=0`) |
| Go | 1.22.2 from apt; builds use 1.26.8, which go.mod requires (cached in `~/go/pkg/mod`) |
| Root filesystem | `/dev/sda2`, UUID `e41c582c-c4d8-4225-9e5d-249c8248cb80` |
| `/etc/fstab` | original, sha256 starts `9d71ab603c19f301`, 446 bytes, 0644 root:root |
| `/etc/hosts` | original, sha256 starts `c2646361092fcc60`, 273 bytes |
| SmartConfig store | `/var/lib/smartconfig`, migrated to M2 on 2026-10-02 and filled by `scd` (1,546 rows on 2026-10-03, after the VM was rolled back to the pre-S5 snapshot; 2,121 before); the 4 film-run rows for `/etc/fstab` keep their ids (`2c6901` is the original); the M1 copy is `changes.db.m1-backup` (do not copy it back: it would drop every M2 row) |
| SmartConfig watcher | `scd.service` enabled, binary `/usr/local/sbin/sc`: since 2026-10-03 21:25 UTC the M3 build (code of `bb31f3e`, sha256 `cf5ba078…`). Earlier builds in `/var/backups/smartconfig`: `sc-m3pre` (M3 before its final fixes), `sc-b6ab3cc` (M2 with its follow-ups), `sc-m2`, `sc-m1`. `sudo journalctl -u scd` |
| SmartConfig rescue path (M4) | not installed: no boot units, drop-ins or `42_smartconfig` yet (sign-off S2) |
| sudo | passwordless for `vboxuser` via `/etc/sudoers.d/90-vboxuser-nopasswd` |
| GitHub CLI | `gh`, logged in as NikhilSah27 (token in `~/.config/gh/hosts.yml`) |
| Claude Code | `~/.claude/settings.json` has `defaultMode: bypassPermissions` and `Bash(sudo:*)` allowed (throwaway test VM) |

The VM is a disposable test machine: the plan is to break it on purpose.
Before a destructive test, say what is about to break.

---

## 6. Recovery guide

### Fresh machine or lost checkout

```sh
sudo apt install -y golang-go git make gh    # any Go 1.21+; it fetches 1.26.8
git clone https://github.com/NikhilSah27/SmartConfig.git smartconfig
cd smartconfig
make build test vet fmt                      # first run needs the network
file bin/sc                                  # must say "statically linked"
sudo ./scripts/smoke.sh                      # must end with PASS
```

### Put back passwordless sudo (test VM only)

In a normal terminal (sudo will ask for the password once):

```sh
echo 'vboxuser ALL=(ALL) NOPASSWD: ALL' | sudo tee /etc/sudoers.d/90-vboxuser-nopasswd >/dev/null
sudo chmod 0440 /etc/sudoers.d/90-vboxuser-nopasswd
sudo visudo -c        # every file must say "parsed OK"
```

### GitHub access

```sh
gh auth login --hostname github.com --git-protocol https --web
gh auth setup-git     # lets plain `git push` use that login
git push
```

### A config file got broken (the machine still boots)

```sh
SC=/usr/local/sbin/sc               # if it is gone: in /var/backups/smartconfig, sc-m3, sc-m3pre, sc-b6ab3cc, sc-m2, else sc-m1 (file rows only)
sudo $SC log /etc/fstab             # find the last good id
sudo $SC diff <id>                  # confirm what changed
sudo $SC restore <id>               # put it back; the broken state is saved too
sudo systemctl daemon-reload        # after touching fstab
```

`sc-m1` reads the migrated store too. It restores file rows and refuses
link, deleted and fingerprint rows with one line.

For `/etc/fstab` specifically, `2c6901` in `/var/lib/smartconfig` is the
original file of this VM.

### The machine no longer boots (M4's rescue path installed)

1. After a failed boot the menu shows by itself for 30 s (on a desktop
   the failed boot comes up to the login screen with the mount missing:
   reboot to get the menu); otherwise hold **Shift** (BIOS) or press
   **Esc** (UEFI) to show it. Pick **SmartConfig rescue**.
2. Above "Press Enter for maintenance", `sc status` says what changed
   since the last healthy boot, worst first, and gives the commands for
   the newest blocker (with none, the newest error). With `/var` on a
   filesystem of its own there is no report until `mount /var`; then run
   `sc status`. Press Enter and type them: `mount -o remount,rw /`,
   `sc restore <id>`, `sync`, `systemctl daemon-reload`,
   `systemctl reboot` (not "exit": that goes on to the desktop with
   fstab still ignored).
3. After a failed boot, the next boot shows the menu once more (the
   rescue boot cannot clear its flag): pick the first entry, Ubuntu. Its
   verdict is ok, and
   `sudo sc status` says so.

A boot that stops in emergency mode prints the same report, with root
already read-write; at a `login:` prompt there, log in and put `sudo`
before each command. If the entry is missing or does not boot, or there
is no report, follow the next section; `sc status` and `sc log` work in
that shell too. On a desktop the login screen covers that console:
reboot to the menu, or run `sudo sc status` in a terminal.

### The machine no longer boots (without the rescue entry)

1. If the console says **"Press Enter for maintenance"**, press Enter.
   Ubuntu 24.04 opens a root shell there although root is locked (its
   sulogin patch; shown in the M4 lab). Go to step 3.
2. Otherwise, reboot and show GRUB. Hold **Shift** (BIOS) or press **Esc**
   once (UEFI); a second Esc drops to `grub>`. Press `e` on "Ubuntu" and add
   ` fstab=no systemd.unit=rescue.target` to the end of the `linux` line,
   then press Ctrl-X and Enter at the prompt. This rescue boot ignores
   `/etc/fstab` and keeps root read-only. If it fails too, add
   ` init=/bin/bash` instead.
3. `mount -o remount,rw /`. Until then sc can read the store, even one a
   crash left half-written (the M4 build; the backups cannot).
4. Restore with a static `sc`, or fix the file with nano. Try each of these
   in turn:
   - `/usr/local/sbin/sc restore <id>`, the installed build (M4, since
     sign-off S2);
   - `/var/backups/smartconfig/sc-m3`, the M3 build `scd` ran before M4;
   - `sc-m3pre`, the M3 build before the final review's fixes;
   - `sc-b6ab3cc`, the M2 build with its follow-ups;
   - `sc-m2`, the `m2` build: the same restores, links and creations
     included;
   - `sc-m1`, the `m1` build. It restores file rows on the migrated store
     and refuses link, deleted and fingerprint rows with one line (`make
     m1-compat` proves this).
5. Leave the shell:
   - from the rescue boot (step 2), run `sync`, then `systemctl reboot`;
   - from emergency mode, run `systemctl daemon-reload`, then
     `systemctl reboot` (without the reload, the reboot waits for the
     missing disk again);
   - from `init=/bin/bash`, run `sync`, then `reboot -f`.

If that fails: boot a live USB, mount `/dev/sda2`, and fix the file there.

### Backups made at the m1 tag (root only)

`/var/backups/smartconfig/` (mode 0700): `sc-m1` (the tagged binary),
`store-m1.tar` (the SmartConfig store; `2c6901` is this VM's original
fstab), `etc-boot-m1.tar.zst` (`/etc` and `/boot/grub`), `manifest-m1.txt`
and `sha256-m1.txt` (mode, owner, size and checksum of every file, to see
exactly what changed after a risky test). Added later: `sc-m2` (the `m2`
build, 2026-10-03), `sc-b6ab3cc` (M2 with its follow-ups), `sc-m3pre`
and `sc-m3` (M3 before and after its final review's fixes), and the
manifests and checksums before M2's S2, before S5, after the S5 `apt
upgrade`, and before M4's S2 (`manifest-pre-m4s2.txt`,
`sha256-pre-m4s2.txt`).

### VirtualBox snapshot

Taking a VirtualBox snapshot of the VM before destructive tests is the
fastest way back from a completely broken system.

---

## 7. How to resume with Claude Code

Open Claude Code in the checkout and say:

> Read CLAUDE.md, then docs/WORKLOG.md (its "Now" section says what is in
> progress) and the current milestone's plan (docs/M4_PLAN.md), then
> continue.

Rules that carry over: plan first; one step at a time, each with its tests
and mutation checks, a commit, a WORKLOG line and a push; run gofmt, vet and
the tests as user, under `make race` and as root before calling anything
done; stop and ask before touching the real /etc, /boot, store or systemd
state, and before changing an approved plan.
[JOURNEY.md](JOURNEY.md) tells the story so far and why each path was chosen.

The full Claude Code transcript of the M1 session is kept only on the dev VM
(`~/.claude/projects/-home-vboxuser-code-smartconfig/`), not in this public
repo, because it contains a credential typed during the session.
