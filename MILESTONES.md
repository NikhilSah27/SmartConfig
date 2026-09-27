# Milestones

- [x] M1 store + CLI: snapshot, log, cat, diff, restore — done 2026-09-27
- [ ] M2 watcher daemon: scd with inotify, auto-snapshot edits made with any editor
- [ ] M3 file graph + checkers: tiers, real validators, regex rules with canned
      explanations, sc edit and sc check
- [ ] M4 rescue path: GRUB entry, rescue.target service printing sc status,
      boot-ok verification, restore from read-only root
- [ ] M5 package: .deb with nfpm, install on a clean VM
- [ ] M6 incident factory and eval set
- [ ] M7 local model: sc why with llama.cpp, opt-in

Current: M2.

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
- No locking beyond SQLite's own (busy_timeout 5s, immediate transactions).
- Not done: file watching, validators, GRUB/rescue integration, packaging,
  pruning old blobs. All belong to later milestones.
- The owner half of the restore test (uid/gid) only runs as root; as a normal
  user it is skipped. The smoke run checks root:root on /etc/hosts.
- Pinned modernc.org/sqlite v1.29.10 because newer releases need Go 1.25 and
  this machine has Go 1.22.2.
