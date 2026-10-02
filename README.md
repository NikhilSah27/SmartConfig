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
only. Milestones 3 to 7 (checkers, rescue boot path, package, incident
factory, local model) are planned.

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
storing new content and logs that once.

To remove the watcher, keep the store: the M1 binary
(`/var/backups/smartconfig/sc-m1` on the dev VM) still reads it and restores
file rows.

```sh
sudo systemctl disable --now scd
sudo rm /etc/systemd/system/scd.service /usr/local/sbin/sc
sudo systemctl daemon-reload
```

## Layout

```
cmd/sc/            the CLI (cobra), one file per command; sc watch is the daemon
internal/store/    blobs + SQLite records: record, list, get, restore, diff, migrations
internal/fsutil/   reads that never follow symlinks, atomic write-back and symlinks
internal/scope/    which paths are watched, their tiers, fingerprint-only rules
internal/watch/    the watcher: inotify, debounced worker, rescans, limits
scripts/           scd.service, smoke.sh (M1) and accept-m2.sh (M2) acceptance runs
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
