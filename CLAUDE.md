# SmartConfig

A safety net for the config files that take a Linux machine down. Snapshot a
file before it is edited, warn before a bad edit is applied, explain what broke,
restore one file from the rescue shell.

Target: Ubuntu 24.04. We build one milestone at a time; MILESTONES.md says which
one is current. Do not build ahead of the current milestone, not even stubs.

Read docs/WORKLOG.md first: its "Now" section says what is in progress and
what is next.

## Rules
- Go 1.26.8+ (go.mod). CGO_ENABLED=0 always: the binary must be static so it runs from a
  broken system. SQLite is modernc.org/sqlite, never mattn/go-sqlite3.
  One exception: `make race` runs the tests with CGO_ENABLED=1 for the race
  detector. The shipped binary is never built that way.
- One binary, cmd/sc. Library code in internal/. The M2 watcher is the
  subcommand `sc watch`, run by scripts/scd.service; no other daemon.
- Data lives under $SC_HOME (default /var/lib/smartconfig). Tests always set
  SC_HOME to t.TempDir(). No test touches /etc or /var/lib.
- Writing a config file back: temp file in the same directory, set mode and
  uid/gid, fsync, rename over the original. Never truncate and write in place.
- Dependencies: cobra, modernc.org/sqlite, go-difflib. Ask before adding another.
- Errors are wrapped with context. The CLI prints one clear line on stderr and
  exits 1. No panics or stack traces reach the user.
- Output is plain text readable on an 80x25 console. No TUI, no color yet.
- Every feature has a test. go test ./..., go vet ./... and gofmt are clean
  before a task is called done.
- Small commits, one logical change each, clear messages.
- After every step, update docs/WORKLOG.md and push to GitHub.

## Layout
cmd/sc            the CLI (cobra); thin, calls into internal/
internal/store    blobs + SQLite records: record, list, get, restore, migrations
internal/fsutil   reads that never follow symlinks, atomic write, atomic symlink
internal/scope    pure: which paths are recorded, tiers, fingerprint-only rules
internal/watch    the watcher (sc watch): inotify, worker, rescans, limits
internal/check    M3 checkers: findings, rules, the one place that runs validators
internal/boot     M4 boot verdicts: $SC_HOME/boots, the last healthy boot
scripts/          scd.service, the M4 units and rescue drop-in, 42_smartconfig,
                  the package (build-deb.sh, deb/, version.sh),
                  smoke.sh, accept-m2.sh, accept-m3.sh, accept-m4.sh, build-sc-m1.sh
lab/              QEMU rescue lab, make lab-e2e (dev only, stdlib Python, not shipped;
                  the VM runs are not in CI, its unit tests are, via TestLabPython)
docs/             WORKLOG (read first), plans, reviews, visual explainers
