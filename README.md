# SmartConfig

A safety net for the config files that take a Linux machine down. Snapshot a
file before it is edited, warn before a bad edit is applied, explain what broke,
and restore one file from the rescue shell.

Target: Ubuntu 24.04. Built one milestone at a time; see
[MILESTONES.md](MILESTONES.md).

## Status

**Milestone 1 is done:** a single static Go binary, `sc`, that can snapshot a
config file, list its history, print a snapshot, diff it against the file on
disk, and restore it with its original mode and owner. Every restore can itself
be undone.

**Milestone 2 is done** (tag `m2`, 2026-10-02): `sc watch` (run as the
`scd` service) records every change under `/etc`, `/boot/grub` and each
login's `~/.ssh` without anyone typing `sc`,
including symlinks (systemd enable, disable, mask), deletions and new files.
SSH host keys, `/etc/machine-id`, `/etc/ssl/private`, WireGuard and a few
other key stores are kept as fingerprints only. Every other recorded file is
stored with its content, `/etc/shadow` and network files that hold Wi-Fi
keys among them, in a store only root can read (`sc scope PATH` says which).

**Milestone 3 is done** (tag `m3`, 2026-10-03; plan
[docs/M3_PLAN.md](docs/M3_PLAN.md)): checkers. `sc check` finds the
problems in a config file that stop a boot or lock you out, `sc edit`
checks an edit before it replaces the file, scd checks every change it
records, and `sc scope` explains what SmartConfig does with a path.

**Milestone 4 is done** (tag `m4`, 2026-10-07; plan
[docs/M4_PLAN.md](docs/M4_PLAN.md)): the rescue path. A boot menu entry,
**SmartConfig rescue**, opens a root shell however broken `/etc/fstab`
is, `sc status` says above its prompt what changed since the last
healthy boot and how to put it back, and the menu comes back by itself
after a failed boot.

**Milestone 5 is done** (tag `m5`, 2026-10-09; plan
[docs/M5_PLAN.md](docs/M5_PLAN.md)): the package. `make deb` builds one
`.deb`, `smartconfig`, that installs, upgrades, removes and purges
cleanly on Ubuntu 24.04 and takes over a hand install (see "Install").

Next: milestone 6, an incident factory and eval set (its plan is being
drafted), then milestone 7, a local model (`sc why`, opt-in), which is
not planned yet. [MILESTONES.md](MILESTONES.md) lists both and the open
work that no milestone owns yet.

## Build

Go 1.26.8 or newer (set in go.mod; an older `go` command downloads it).
The binary is always static (`CGO_ENABLED=0`) so it still runs on a
half-broken system.

```sh
make build      # -> bin/sc
make test       # go test ./...
make race       # tests under the race detector
make vet
make fmt        # fails if gofmt would change anything
make m1-compat  # the M1 binary still works on an M2 store
make smoke      # M1 acceptance run against the real /etc/hosts (asks for sudo; restores it)
make accept-m2  # M2 acceptance run in the VM (asks for sudo; its own test paths only)
make accept-m3  # M3 acceptance run in the VM (asks for sudo, scd stopped; made-up files, one test unit)
make accept-m4  # M4 acceptance run in the VM, the parts that need no reboot (asks for sudo; writes nothing real)
make lab-test   # the QEMU rescue lab's own tests (no VM)
make lab-e2e    # the M4 owner scenario in a QEMU VM, UEFI and BIOS (no sudo; see lab/README.md)
```

## Install (M5)

One package, `smartconfig`, for Ubuntu 24.04 on amd64: `sc` in
`/usr/sbin`, scd, the boot units, the rescue entry and its report.

```sh
make deb                    # dpkg-deb only; it prints the file: dist/smartconfig_<version>_amd64.deb
sudo apt install ./dist/smartconfig_<version>_amd64.deb   # that file; scd starts, update-grub adds the rescue entry
sc version
```

With the file under a home directory only its owner can read, apt says
`Download is performed unsandboxed as root ...`: that is harmless.

The first boot verdict comes at the next boot. If `update-grub` fails
during the install, the package is in and says so; fix what it reports
and run `sudo update-grub`. A newer package over it restarts scd and
keeps the store. Where `/usr/sbin/policy-rc.d` forbids packages to
start, restart or stop services (some images ship one), the package
says that scd was not restarted, or still runs, and the command to do
it by hand.

An install by hand from before the package (this README up to M4: `sc`
in `/usr/local/sbin`, the units and drop-ins in `/etc/systemd/system`,
`/etc/grub.d/42_smartconfig`) is taken over on the first install: its
units are disabled and each of its files is kept as `NAME.dpkg-old`,
which systemd, GRUB and PATH pass over; anything else at those paths is
left, and named. If dpkg cannot unpack the package, the hand install's
`42_smartconfig` is put back and nothing else of it was touched. A purge
deletes `42_smartconfig.dpkg-old`, as dpkg does next to a conffile.
Until the next healthy boot, `sudo sc status` lists the hand install's
units and drop-ins as `deleted; /usr/lib's in use`: the package's own
stand in for them, and that is no problem.

`sudo apt remove smartconfig` stops scd, takes the rescue entry out of
`grub.cfg` and unsets the menu flag; the history in `/var/lib/smartconfig`
is kept, after `sudo apt purge smartconfig` too (it says how to delete
it).

## Use

The M2 `sc` upgrades an existing store (made by M1) on first use, keeping a
copy as `changes.db.m1-backup`. On a machine whose store must stay as M1 left
it, use the M1 binary until you choose to upgrade.

```sh
sudo ./bin/sc init                                    # create /var/lib/smartconfig
sudo ./bin/sc snapshot /etc/fstab -m "before editing" # -> snapshot 2c6901 ...
sudo ./bin/sc log /etc/fstab                          # history, newest first
sudo ./bin/sc diff 2c6                                # snapshot vs file on disk
sudo ./bin/sc cat 2c6901                              # print a snapshot
sudo ./bin/sc restore 2c6                             # put it back, atomically
./bin/sc version                                      # the package version and the commit it was built from
```

`sc log` shows `link`, `deleted` or `digest` in its SIZE column for symlinks,
deletions and fingerprint-only files; `sc restore` puts links back and can
undo the creation of a file (a "did not exist" row). Fingerprint-only files
(SSH host keys, `/etc/machine-id`, TLS private keys) are never shown or
restored.

Ids are 6 hex characters; any unique prefix works. Data lives under
`$SC_HOME` (default `/var/lib/smartconfig`): file contents in `objects/`,
history in `changes.db` (SQLite, pure Go).

## Watch every change (M2)

```sh
sudo ./bin/sc watch                  # foreground; Ctrl-C stops it (exit 0)
./bin/sc watch --root ~/some/dir     # as a normal user, on a directory of your own
```

The package runs it as scd (Install, above):

```sh
sudo journalctl -u scd -p warning   # boot- and access-critical changes (needs sudo)
```

`scd` creates `/var/lib/smartconfig` itself; `sc init` is not needed. It
counts as started (`Type=notify`) once its startup rescan is recorded, which
boot does not wait for; a file held back (low space, a store error) is
counted in the `baseline:` line and recorded later. An upgrade of the
package restarts it. What is
watched is the built-in scope (`internal/scope/default.scope`): all of `/etc`
minus generated files, caches and noise, `/boot/grub/grub.cfg` and
`custom.cfg`, and each login's `authorized_keys`, `rc` and `environment`.
Every row's tier (1 boot, 2 access, 3 network, 4 other) sets its journald
priority. File contents never appear in the journal.

When the disk has less than 256 MiB free, scd holds back every change that
needs new content, tries again each minute and logs that once; `sudo sc
status` says so while it lasts. Nothing is deleted by itself.
`sudo sc prune` deletes the rows scd recorded more than 90 days ago
(`--older-than 30d`, or hours: `36h`), then the stored versions no row
uses any more. It keeps each file's newest row, each file's version at
every boot the boots file records (what `sc status` compares with), and
every manual, `sc edit`, restore and pre-restore row. It says what would
go and asks first; `--yes` does not ask. A pruned row's id may be given
to a new row later. A file that a program rewrites
without pause gets 20 rows, then one every 5 minutes with its newest
content, marked `(rate-limited)`, and one warning line.

Removing the package (Install, above) keeps the store. The M1 binary
(`/var/backups/smartconfig/sc-m1` on the dev VM) still reads it and restores
file rows.

## Check before it breaks (M3)

```sh
./bin/sc check                             # every file sc has a checker for
./bin/sc check /etc/fstab                  # one file as it is now
sudo ./bin/sc check 2c6901                 # a saved version (sc log lists them)
./bin/sc check --as /etc/fstab new.fstab   # a file you are about to copy there
./bin/sc check -v /etc/fstab               # with each rule's explanation and the validators' own words
sudo ./bin/sc edit /etc/fstab              # edit a copy, checked before it is saved
./bin/sc scope /etc/fstab                  # recorded? tier? checker? applied when?
```

A finding is a `blocker` (the machine may not boot, or you may be locked
out), an `error` (something stops working) or a `warning`:

```
$ sc check --as /etc/fstab new.fstab
SEVERITY  FILE                         LINE  RULE                  PROBLEM
blocker   /etc/fstab (from new.fstab)  1     fstab-source-missing  UUID=11111111-2222-3333-4444-555555555555 (for /data) is not a device on this machine
blocker   /etc/fstab (from new.fstab)  2     fstab-option-typo     option "defalts" of /x looks like a misspelling of "defaults"
2 blockers in 1 file.
sc check -v explains; sc log FILE lists the versions to restore.
```

`sc check` exits 2 when a file has a blocker or an error, 1 when a file
could not be checked (run it with sudo for files only root may read), and
0 otherwise. With no argument it checks the files scd records that have a
checker. A note says, in sc's words, when a file was not (fully)
checked; what the validator said, which may quote a file, `sc check`
shows with `-v` and `sc edit` under the note.

What is checked, each with the system's own validator in a check-only form
plus rules of sc's own: `/etc/fstab` (`findmnt --verify`), sudoers
(`visudo -c`), `sshd_config` and its drop-ins (`sshd -t`, a drop-in
inside `sshd_config` as sshd reads it, and a warning for a
`ListenAddress` no interface here has), systemd units in
`/etc/systemd/system` and their drop-ins in `NAME.d/*.conf`, each drop-in
with its unit (`systemd-analyze verify`), `/etc/default/grub` and
`grub.cfg` (`sh -n`, `grub-script-check`), netplan (netplan's generator on
a scratch copy of all its files), udev rules (`udevadm verify`), passwd
and group (`pwck -r`, `grpck -r`, with a made-up shadow file), sysctl
(`sysctl --dry-run`), and `nsswitch.conf`, `ld.so.preload`, `/etc/hosts`,
`/etc/nologin`, `/etc/ssh/sshd_not_to_be_run` by sc's rules alone. Validators run on copies in a private
scratch directory, from fixed system directories (not `$PATH`), with no
shell and a 10-second limit. If one is missing (in a rescue shell, say),
sc's own rules still run and a note says so.

`sc edit` opens `$SUDO_EDITOR`, `$VISUAL`, `$EDITOR`, `editor`, `nano` or
`vi` on a copy. If the edit adds a blocker or an error, it explains each
one and asks `(e)dit again, (s)ave anyway, (q)uit`; without a terminal it
quits. The file is not touched until you save. The save is atomic and
keeps the mode and owner, and `sc log` shows it as an `edit` row (after a
row of the state before, if that was not yet recorded). A copy
that was not saved is kept under `$SC_HOME/tmp/kept-*`.

With the M3 build, scd also checks each change it records, made with any
editor or tool, against the version before. Each problem the change added
gets one journal line, at err for a blocker, warning for an error and
notice for a warning, and `ok again` follows when the file is fixed:

```
T1 /etc/fstab: check: blocker fstab-source-missing, line 2: ... (3fa2c1)
```

The journal gets sc's own sentence, never a line of the file or a
validator's output (both can quote a secret).

## When the machine does not boot (M4)

A line in `/etc/fstab` for a disk that is not there makes Ubuntu wait
90 s at boot. Then it stops in emergency mode, where over SSH the
machine is gone, or comes up without the mount. A desktop comes up to
its login screen all the same, with emergency mode out of sight behind
it, and nothing on the screen says what went wrong (sign-off S3). At the
console, M4 gives you:

- **A menu entry, SmartConfig rescue.** It boots the newest kernel (the
  one `GRUB_TOP_LEVEL` names, if set and it has an initrd) with
  root read-only and `/etc/fstab` ignored (`ro fstab=no
  systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1`), with the default
  entry's options but `quiet splash`, straight to a root shell: no 90 s
  wait, however broken fstab is (about 40 s from power-on in the QEMU
  lab, which emulates the CPU).
- **The menu after a failed boot.** It shows by itself, for 30 s where
  the menu is normally hidden (a desktop install's default), else for
  your `GRUB_TIMEOUT`.
- **The report above the prompt.** Before the shell starts, `sc status
  --console` says which boot was the last healthy one, what changed
  since, worst first, and the commands that put the file back.
- **A verdict for every boot** that can write `/var`, in
  `$SC_HOME/boots`: ok, bad, or never reached multi-user. With scd
  running it comes once scd's startup rescan is recorded, so a file
  edited while scd was down is not taken for a change since. The rescue boot
  itself gets none, and keeps its journal in memory only:
  `journalctl -xb` works while you are in it, and after the reboot
  `journalctl --list-boots` does not list it.

This is the rescue boot in the QEMU lab (`make lab-e2e`), after a bad
fstab line and one failed boot:

```
This boot:     08156d46 (rescue), root read-only
Last healthy:  2026-10-06 01:25, boot 0ed9182e
Failed since:  1 boot, last 10-06 01:29: a mount failed, emergency mode
scd:           not running

Changed since the last healthy boot, worst first:
95838f 10-06 01:26  blocker fstab-source-missing, line 4  /etc/fstab

To put /etc/fstab back:
  mount -o remount,rw /
  sc restore e0e82f
  sync
  systemctl daemon-reload
  systemctl reboot
The menu shows once more: the first entry, Ubuntu, is the one.
You are in rescue mode. After logging in, type "journalctl -xb" to view
...
Press Enter for maintenance
(or press Control-D to continue):
```

Press Enter and type the commands. The next boot is normal and its
verdict is ok. The menu shows once more, because the rescue boot cannot
clear its flag; the first entry, Ubuntu, is the one to pick. End with
`systemctl reboot`, not "exit" or Control-D: those go on to the desktop
with `/etc/fstab` still ignored, a boot that gets no verdict either. A
file with no version recorded from before its change gets no commands;
the report says to fix it by hand.

If the machine stops in emergency mode instead, the same report is
printed there, with root already read-write. Ubuntu sometimes starts a
`login:` prompt on that console as well; if you get one instead of `#`,
log in and put `sudo` before each command. The rescue entry's shell is
the only thing reading its console. On a desktop the login screen comes
up over that console, so the report is not seen there. Reboot: the menu
shows by itself, and SmartConfig rescue prints the report. Or log in and
run `sudo sc status` in a terminal; it gives the same commands, each to
run with `sudo` (not tried in the sign-off).

The package installs all of it (Install, above); the first verdict is
given at the next boot.

On a running system, `sudo sc status` gives the report as a table: one
row per file changed since the last healthy boot, newest first, judged
with the validators too (the console uses sc's own rules only). It only
reads, and exits
0 when healthy, 2 when a boot failed since the last healthy one or a
change since then added a blocker or an error, and 1 when sc itself
failed.

On a read-only root, `sc log`, `diff`, `cat`, `check` and `status` work.
That includes a store a crash left half-written: sc reads a repaired
copy in `/run` and says so, and the store is not touched. As root,
`sc check` puts the validators' copies in `/run` when the store cannot
be written.
`sc restore` says to remount first. It also refuses a file or directory
marked immutable or append-only (`chattr +i`, `+a`) before it writes
anything, and names the `chattr` to run.

Where it does less: a btrfs or ZFS root gets no rescue entry, and LVM,
LUKS and multipath roots are not supported (M4 plan, non-goals). With
`GRUB_DISABLE_RECOVERY=true` there is no rescue entry either. The menu
flag needs GRUB's 1024-byte `grubenv`; on a separate `/boot` that failed
to mount, the menu after a failed boot depends on Ubuntu's own
`recordfail`. The rescue boot mounts only `/`: with `/var` on a
filesystem of its own, run `mount /var` and then `sc status`; with a
separate `/boot`, run `mount /boot` before restoring a file under
`/boot/grub`. `sc restore` refuses
to write under a mount point of `/etc/fstab` that is not mounted (or
another mount covers), and says which to mount, the outermost first: the
file would land on the root filesystem's copy. `sc status` puts those
`mount` lines in its undo. A file that is itself a mount point in fstab
(a bind mount) is not put back; fix the file mounted on it.
`systemctl soft-reboot` keeps the kernel and its boot id, so to sc the
new session is the boot before it, verdict and all: a failure in it
brings no menu at the next boot (systemd 255, Ubuntu 24.04's, does not
count soft reboots). To have a change to fstab or a unit judged, reboot
in full.

**The rescue entry is a root shell from a menu item.** While root is
locked, as it is on Ubuntu by default, it asks for no password; if root
has a password, the shell asks for it (the report above the prompt is
shown either way), so know it before you need it. On Ubuntu 24.04 that
adds no new way in: with a locked root, emergency mode and the stock
recovery entries open the same shell, and without a GRUB password anyone
at the console (or a VM's or server's remote console) can edit a GRUB
entry (`init=/bin/bash`) to get root whatever root's password is. A
GRUB password you already have covers the rescue entry too: it is not
marked `--unrestricted`. Full-disk encryption still asks for its
passphrase in the rescue boot (not tested here). A GRUB password does
not stop a boot from a USB stick; that takes a firmware password, or
disk encryption.

To close the menu paths, give GRUB a superuser password. Every entry
then asks for it unless it is marked `--unrestricted`. The recipe below
marks the first entry, Ubuntu, so that must be the entry that boots:
use it only when this prints `GRUB_DEFAULT=0` and nothing else.

```sh
grep -shE '^GRUB_(DEFAULT|SAVEDEFAULT|DISABLE_SUBMENU)=' /etc/default/grub /etc/default/grub.d/*.cfg
```

With a saved default, a default under "Advanced options",
`GRUB_SAVEDEFAULT`, `GRUB_DISABLE_SUBMENU` or a ZFS root, every boot
would stop at GRUB's password prompt. SmartConfig's lab runs the recipe
in BIOS and UEFI mode (`make lab-e2e LAB_E2E_ARGS=--grub-password`):
the default boot asks for nothing, the rescue entry asks and boots with
the password (Ubuntu's signed EFI GRUB takes `password_pbkdf2`), and
Ubuntu from the menu asks for nothing. Try it on a VM snapshot first all
the same: your machine's GRUB settings are not the lab's.

```sh
grub-mkpasswd-pbkdf2                     # prints grub.pbkdf2.sha512.10000.…
sudoedit /etc/grub.d/40_custom           # add three lines at the end:
                                         #   set superusers="admin"
                                         #   export superusers
                                         #   password_pbkdf2 admin grub.pbkdf2.sha512.10000.…
grep -n gnulinux-simple /etc/grub.d/10_linux
sudoedit /etc/grub.d/10_linux            # on that menuentry line: ${CLASS} --unrestricted
sudo update-grub
sudo grep -n -- --unrestricted /boot/grub/grub.cfg   # exactly one line: menuentry 'Ubuntu'
```

If it is not exactly that one line, do not reboot: take the three lines
out of `40_custom` and run `sudo update-grub` again. A boot that stops
at "Enter username:" goes on with `admin` and the password; GRUB reads
it with a US keyboard layout.

The rescue entry, the recovery entries, older kernels and every other
entry, editing any entry, GRUB's command line, and a `grub-reboot` to
any entry but Ubuntu then need the password. Emergency mode after a
failed boot does not: that is sulogin, not GRUB.

`10_linux` is a configuration file of grub-common. When an update
changes it, apt asks whether to keep yours: keep it (N, the default).
If the new one is taken, every boot stops at the password prompt until
you redo the edit and run `sudo update-grub`. unattended-upgrades skips
such an update, so install it by hand.

`sudo apt remove smartconfig` removes the rescue path with the rest
(Install, above). The boot verdicts, `/var/lib/smartconfig/boots`, stay
with the store.

## Layout

```
cmd/sc/            the CLI (cobra), one file per command; sc watch is the daemon
internal/store/    blobs + SQLite records: record, list, get, restore, diff, migrations
internal/fsutil/   reads that never follow symlinks, atomic write-back and symlinks
internal/scope/    which paths are watched, their tiers, fingerprint-only rules
internal/watch/    the watcher: inotify, debounced worker, rescans, limits, checks
internal/check/    checkers: the file graph, rules and explanations, the validator runner
internal/boot/     boot verdicts: the boots file, ok or bad, the last healthy boot
scripts/           scd.service, the M4 units, drop-in and 42_smartconfig; the package:
                   build-deb.sh, deb/ (its maintainer scripts), version.sh; and the
                   acceptance runs: smoke.sh (M1), accept-m2.sh, accept-m3.sh, accept-m4.sh
lab/               the QEMU rescue lab (make lab-e2e): dev only, not shipped
docs/              worklog, plans, reviews, visual explainers
```

## Docs

Current work: [docs/WORKLOG.md](docs/WORKLOG.md); where each milestone
stands: [MILESTONES.md](MILESTONES.md). A progress dashboard with charts, as
it was on 28 Sep 2026 (M1 done):
https://nikhilsah27.github.io/SmartConfig/visuals/progress.html.

Start with [docs/PROJECT_LOG.md](docs/PROJECT_LOG.md): what has been done,
why, the state of the dev VM, and how to recover or resume after a crash.

[docs/README.md](docs/README.md) lists interactive explainers: a system map
with a working in-browser copy of `sc`, a narrated film of a real
`/etc/fstab` break-and-restore, and three films covering the milestone plan,
the finished product, and the same failure without SmartConfig. They are live
at https://nikhilsah27.github.io/SmartConfig/, or download the HTML files and
open them in a browser.
