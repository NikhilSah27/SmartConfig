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
be undone. Milestones 2 to 7 (watcher daemon, checkers, rescue boot path,
package, incident factory, local model) are planned.

## Build

Go 1.22+. The binary is always static (`CGO_ENABLED=0`) so it still runs on a
half-broken system.

```sh
make build      # -> bin/sc
make test       # go test ./...
make vet
make fmt        # fails if gofmt would change anything
sudo make smoke # acceptance run against the real /etc/hosts (restores it)
```

## Use

```sh
sudo ./bin/sc init                                    # create /var/lib/smartconfig
sudo ./bin/sc snapshot /etc/fstab -m "before editing" # -> snapshot 2c6901 ...
sudo ./bin/sc log /etc/fstab                          # history, newest first
sudo ./bin/sc diff 2c6                                # snapshot vs file on disk
sudo ./bin/sc cat 2c6901                              # print a snapshot
sudo ./bin/sc restore 2c6                             # put it back, atomically
```

Ids are 6 hex characters; any unique prefix works. Data lives under
`$SC_HOME` (default `/var/lib/smartconfig`): file contents in `objects/`,
history in `changes.db` (SQLite, pure Go).

## Layout

```
cmd/sc/            the CLI (cobra), one file per command
internal/store/    blobs + SQLite records: snapshot, list, get, restore, diff
internal/fsutil/   read with mode/owner, atomic write-back
scripts/smoke.sh   acceptance run (sudo)
docs/              visual explainers: system map and narrated films
```

## Docs

Start with [docs/PROJECT_LOG.md](docs/PROJECT_LOG.md): what has been done,
why, the state of the dev VM, and how to recover or resume after a crash.

[docs/README.md](docs/README.md) lists interactive explainers: a system map
with a working in-browser copy of `sc`, a narrated film of a real
`/etc/fstab` break-and-restore, and three films covering the milestone plan,
the finished product, and the same failure without SmartConfig. They are live
at https://nikhilsah27.github.io/SmartConfig/, or download the HTML files and
open them in a browser.
