# Project log and recovery guide

Everything done so far, why, the current state of the development VM, and how
to get back to work if something breaks. Written for a human or a Claude Code
session picking the project up cold.

Last updated: 2026-10-02, M2 done and tagged `m2` (the 24-hour soak is
deferred to the next session). M1 was tagged `m1` on 2026-09-28.

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

Status (2026-10-02): **M2 done and tagged `m2`**. `scd` is installed and
enabled on the dev VM, and the soak check is due next session. Plan:
[M2_PLAN.md](M2_PLAN.md), live progress: [WORKLOG.md](WORKLOG.md). Before
that: **M1 done and tagged `m1`** (store + CLI, hardened by four review rounds
and five rounds of fixes, see [reviews/](reviews/)).

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
  mode. The root account is locked (`passwd -S root` → `L`), so emergency mode
  cannot open a shell.
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
| OS | Ubuntu 24.04.5 LTS, kernel 7.0.0-34-generic running, 7.0.0-38 installed (boots at the next restart), systemd 255, GRUB 2.12 |
| Go | 1.22.2 from apt; builds use 1.26.8, which go.mod requires (cached in `~/go/pkg/mod`) |
| Root filesystem | `/dev/sda2`, UUID `e41c582c-c4d8-4225-9e5d-249c8248cb80` |
| `/etc/fstab` | original, sha256 starts `9d71ab603c19f301`, 446 bytes, 0644 root:root |
| `/etc/hosts` | original, sha256 starts `c2646361092fcc60`, 273 bytes |
| SmartConfig store | `/var/lib/smartconfig`, migrated to M2 on 2026-10-02 and filled by `scd` (1,546 rows on 2026-10-03, after the VM was rolled back to the pre-S5 snapshot; 2,121 before); the 4 film-run rows for `/etc/fstab` keep their ids (`2c6901` is the original); the M1 copy is `changes.db.m1-backup` (do not copy it back: it would drop every M2 row) |
| SmartConfig watcher | `scd.service` enabled, binary `/usr/local/sbin/sc` (since 2026-10-03 the build of `b6ab3cc`, M2 plus follow-ups 1-8, sha256 `396f84cb…`); the `m2` build is `/var/backups/smartconfig/sc-m2`; `sudo journalctl -u scd` |
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
SC=/usr/local/sbin/sc               # if it is gone: /var/backups/smartconfig/sc-m2, else sc-m1 (file rows only)
sudo $SC log /etc/fstab             # find the last good id
sudo $SC diff <id>                  # confirm what changed
sudo $SC restore <id>               # put it back; the broken state is saved too
sudo systemctl daemon-reload        # after touching fstab
```

`sc-m1` reads the migrated store too. It restores file rows and refuses
link, deleted and fingerprint rows with one line.

For `/etc/fstab` specifically, `2c6901` in `/var/lib/smartconfig` is the
original file of this VM.

### The machine no longer boots (before M4 exists)

1. Reboot, hold **Shift** (BIOS) or press **Esc** (UEFI) to show GRUB.
2. Press `e` on "Ubuntu", add ` init=/bin/bash` to the end of the `linux` line,
   press Ctrl-X.
3. `mount -o remount,rw /`
4. Restore with a static `sc`, or fix the file with nano:
   - `/usr/local/sbin/sc restore <id>`, the M2 binary, which also restores
     links and undoes creations;
   - if it is gone: `/var/backups/smartconfig/sc-m2 restore <id>` (the `m2`
     build, the same restores);
   - if that is gone too: `/var/backups/smartconfig/sc-m1 restore <id>` (the `m1`
     build). It still restores file rows on the migrated store and refuses
     link, deleted and fingerprint rows with one line (`make m1-compat`
     proves this).
5. `sync`, then `exec /sbin/init` or `reboot -f`.

If that fails: boot a live USB, mount `/dev/sda2`, and fix the file there.

### Backups made at the m1 tag (root only)

`/var/backups/smartconfig/` (mode 0700): `sc-m1` (the tagged binary),
`store-m1.tar` (the SmartConfig store; `2c6901` is this VM's original
fstab), `etc-boot-m1.tar.zst` (`/etc` and `/boot/grub`), `manifest-m1.txt`
and `sha256-m1.txt` (mode, owner, size and checksum of every file, to see
exactly what changed after a risky test). Added later: `sc-m2` (the `m2`
build, 2026-10-03), and the manifests and checksums before S2, before S5
and after the S5 `apt upgrade`.

### VirtualBox snapshot

Taking a VirtualBox snapshot of the VM before destructive tests is the
fastest way back from a completely broken system.

---

## 7. How to resume with Claude Code

Open Claude Code in the checkout and say:

> Read CLAUDE.md, then docs/WORKLOG.md (its "Now" section says what is in
> progress), docs/M2_PLAN.md and its Appendix C, then continue.

Rules that carry over: plan first; one step at a time, each with its tests
and mutation checks, a commit, a WORKLOG line and a push; run gofmt, vet and
the tests as user, under `make race` and as root before calling anything
done; stop and ask before touching the real /etc, /boot, store or systemd
state, and before changing an approved plan.
[JOURNEY.md](JOURNEY.md) tells the story so far and why each path was chosen.

The full Claude Code transcript of the M1 session is kept only on the dev VM
(`~/.claude/projects/-home-vboxuser-code-smartconfig/`), not in this public
repo, because it contains a credential typed during the session.
