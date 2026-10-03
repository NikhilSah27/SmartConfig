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
SSH host keys, `/etc/machine-id` and other secrets are kept as fingerprints
only.

**Milestone 3 is done** (tag `m3`, 2026-10-03; plan
[docs/M3_PLAN.md](docs/M3_PLAN.md)): checkers. `sc check` finds the
problems in a config file that stop a boot or lock you out, `sc edit`
checks an edit before it replaces the file, scd checks every change it
records, and `sc scope` explains what SmartConfig does with a path.
Milestones 4 to 7 (rescue boot path,
package, incident factory, local model) are planned.

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
```

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

Installed by hand until the package milestone:

```sh
sudo install -m 0755 bin/sc /usr/local/sbin/sc
sudo install -m 0644 scripts/scd.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now scd
sudo journalctl -u scd -p warning   # boot- and access-critical changes (needs sudo)
```

`scd` creates `/var/lib/smartconfig` itself; `sc init` is not needed. What is
watched is the built-in scope (`internal/scope/default.scope`): all of `/etc`
minus generated files, caches and noise, `/boot/grub/grub.cfg` and
`custom.cfg`, and each login's `authorized_keys`, `rc` and `environment`.
Every row's tier (1 boot, 2 access, 3 network, 4 other) sets its journald
priority. File contents never appear in the journal.

Nothing is pruned yet. When the disk has less than 256 MiB free, scd stops
storing new content and logs that once. A file that a program rewrites
without pause gets 20 rows, then one every 5 minutes with its newest
content, marked `(rate-limited)`, and one warning line.

To remove the watcher, keep the store: the M1 binary
(`/var/backups/smartconfig/sc-m1` on the dev VM) still reads it and restores
file rows.

```sh
sudo systemctl disable --now scd
sudo rm /etc/systemd/system/scd.service /usr/local/sbin/sc
sudo systemctl daemon-reload
```

## Check before it breaks (M3)

```sh
./bin/sc check                             # every file sc has a checker for
./bin/sc check /etc/fstab                  # one file as it is now
sudo ./bin/sc check 2c6901                 # a saved version (sc log lists them)
./bin/sc check --as /etc/fstab new.fstab   # a file you are about to copy there
./bin/sc check -v /etc/fstab               # with each rule's explanation
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
checker.

What is checked, each with the system's own validator in a check-only form
plus rules of sc's own: `/etc/fstab` (`findmnt --verify`), sudoers
(`visudo -c`), `sshd_config` (`sshd -t`), systemd units in
`/etc/systemd/system` (`systemd-analyze verify`), `/etc/default/grub` and
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

## Layout

```
cmd/sc/            the CLI (cobra), one file per command; sc watch is the daemon
internal/store/    blobs + SQLite records: record, list, get, restore, diff, migrations
internal/fsutil/   reads that never follow symlinks, atomic write-back and symlinks
internal/scope/    which paths are watched, their tiers, fingerprint-only rules
internal/watch/    the watcher: inotify, debounced worker, rescans, limits, checks
internal/check/    checkers: the file graph, rules and explanations, the validator runner
scripts/           scd.service and the acceptance runs: smoke.sh (M1), accept-m2.sh, accept-m3.sh
docs/              worklog, plans, reviews, visual explainers
```

## Docs

Current work and progress: [docs/WORKLOG.md](docs/WORKLOG.md). Progress at a
glance, with charts: https://nikhilsah27.github.io/SmartConfig/visuals/progress.html.

Start with [docs/PROJECT_LOG.md](docs/PROJECT_LOG.md): what has been done,
why, the state of the dev VM, and how to recover or resume after a crash.

[docs/README.md](docs/README.md) lists interactive explainers: a system map
with a working in-browser copy of `sc`, a narrated film of a real
`/etc/fstab` break-and-restore, and three films covering the milestone plan,
the finished product, and the same failure without SmartConfig. They are live
at https://nikhilsah27.github.io/SmartConfig/, or download the HTML files and
open them in a browser.
