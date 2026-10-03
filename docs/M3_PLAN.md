# M3 plan: checkers, `sc check` and `sc edit`

Status: **approved 2026-10-03** ("great go ahead": every recommendation in section 14). Drafted by Claude in the session (your pick), from the roadmap, the M2 plan's deferred list and probes on this VM (Appendix A).

## 0. Summary

- **What you get.**
  - `sudo sc edit /etc/fstab` opens your editor on a copy, checks the result before it replaces the file, and tells you in plain words when the edit would stop the machine booting or lock you out. You can fix it, save anyway, or quit; the real file is untouched until you save.
  - `sc check` runs the same checks on the files as they are now, or on any saved version (`sc check <id>`).
  - `scd` runs the check after it records a change made with any editor or tool, and puts a warning in the journal when the change added a problem.
- **How a check works.** Each checker is the consumer's own validator in its check-only form (`findmnt --verify`, `visudo -c`, `sshd -t`, …) run on a scratch copy, plus a few rules of our own with a fixed explanation each. No model, no network.
- **Three things the research changed** (Appendix A):
  1. A stock Ubuntu already fails some validators (`pwck -r` exits 2, 8 of 121 udev rule files fail `udevadm verify`). So an edit is judged by what it **adds**: the old and the new version are both checked, and only new findings are blamed on the edit.
  2. Some "checks" apply things: `netplan generate --root-dir` still asks systemd to reload. Each validator form in this plan was run here first; netplan uses its generator binary directly.
  3. `findmnt --verify` misses what matters most and overstates the rest: it accepts a misspelt mount option, and it reports a missing disk as an error even with `nofail`. Our own rules add the severity.
- **The file graph** is one built-in text file, like the scope: which files each checker reads, which files are checked together, and when a saved change takes effect.
- **Store.** No schema change. Findings are computed on demand, not stored. `sc-m2` and `sc-m1` keep reading the store.
- **Not in M3.** Most of the 13 items the M2 plan parked under "M3" (apt change sets, account-set restore, applied markers, redaction, ACLs, …). Section 13 lists each one; question 1 asks you to confirm.
- **How it gets built.** 16 steps in five chunks, one commit each with its tests, WORKLOG pushed after every step. The M2 soak keeps running: nothing is installed until the soak check is done and you say so.

## 1. Goals and non-goals

**Goals.** M3 is done when all of these hold, plus the sign-off in section 11.

1. **A bad edit is caught before it is saved.** `sc edit FILE` never replaces the file with a version that adds a `blocker` or `error` finding unless you answer "save anyway".
2. **A bad edit made with any editor is reported.** With scd running, a change that adds a finding gives one journal line within seconds, at err for a blocker.
3. **Every finding explains itself.** Each has a rule id, a severity, a line number when known, one sentence saying what is wrong, and a fixed explanation: why it matters and how to fix it.
4. **Checks never change the system.** Validators run only in forms shown here to be check-only, on scratch copies in a private directory, with a timeout. They never run inside the watcher's event loop (watch-list rule 23).
5. **Checks never leak contents.** The journal carries rule ids, line numbers and our own sentences, never a validator's raw output (visudo and pwck quote the offending line). Fingerprint-only files are never copied or checked.
6. **A missing validator is not an error.** From a rescue shell `sshd` or `findmnt` may be absent: the check says "no validator found" and our own rules still run. `sc` stays one static binary with no new dependency.
7. **History stays truthful.** An `sc edit` gives exactly its rows (the state before, if not yet recorded, and one `edit` row), never an extra automatic row, as for restores in M2.
8. **All checks pass.** gofmt, vet, `go test ./...` as user and as root, `make race`, static build, `make m1-compat`.

**Non-goals** (not built in M3, not even as stubs): everything in section 13; the rescue path and `sc status` (M4); packaging (M5); any model (M7).

## 2. What the probes on this VM showed

All run on fabricated scratch files; the real `/etc` was only read. Details in Appendix A.

| Consumer | Check-only form | Needs root | Time | Notes |
|---|---|---|---|---|
| fstab | `findmnt --verify --tab-file F` | no (root adds the on-disk type check) | 20-100 ms | exit 1 on `[E]`; misses a misspelt option; `[E]` even with `nofail` |
| sudoers | `visudo -c -f F` | no | 30 ms | exit 1, `F:2:19: syntax error`; quotes the line |
| sshd | `sshd -t -f F` | yes, or `-h KEY` with any host key | 20-90 ms | exit 255, `line 2: Bad configuration option`; a drop-in checks alone |
| systemd unit | `systemd-analyze verify F` | no | 0.2-0.7 s | exit 1; needs the unit's file name; a stock unit already fails (man page lookup) |
| `/etc/default/grub` | `sh -n F` | no | 10 ms | exit 2 |
| `grub.cfg` | `grub-script-check F` | no | 10-30 ms | exit 1 |
| netplan | `/usr/libexec/netplan/generate --root-dir D` | no | 10-30 ms | **not** `netplan generate`: it calls `systemctl daemon-reload` even with `--root-dir` |
| passwd, group | `pwck -r P S`, `grpck -r G` | no for scratch | 15-30 ms | exit 2; the stock system already exits 2 (missing home dirs of system users); quotes the line |
| sysctl | `sysctl --dry-run -p F` | no | 10-20 ms | exit 1; checked as root: the value did not change |
| udev rules | `udevadm verify F` | no | 10-40 ms | exit 1; 8 of 121 stock files already fail |

Not usable as they are: `nft -c` (needs root and talks to the kernel), `logrotate -d` (exit 1 on a good file), `apparmor_parser -Q` (works; left for later).

## 3. Architecture

```
cmd/sc            check.go, edit.go, scope.go      thin, as today
internal/check    graph.go     the file graph (default.graph, embedded)
                  check.go     Finding, Severity, Checker, baseline diff
                  run.go       the one place that starts a validator
                  rules.go     rule ids, severities, explanations
                  fstab.go, sudoers.go, sshd.go, unit.go, grub.go, plain.go, …
internal/store    Replace(): write a file and its row together (shares Restore's code)
internal/watch    after a recorded row: hand the path to a checker goroutine
```

- `internal/check` knows nothing about the store or the watcher: it takes a path and the bytes to check, and returns findings. That keeps it testable with fake validators.
- One function starts processes (`run.go`); everything else calls it.

## 4. The file graph

One text file built into the binary, parsed like the scope (first matching line wins, same glob language):

```
# check NAME GLOB…      these files are read by checker NAME
# apply NAME TEXT       when a saved change takes effect
# mode  GLOB OCTAL      mode for a file sc edit creates (default 0644)
check fstab    /etc/fstab
apply fstab    at the next boot (now: systemctl daemon-reload, then mount -a)
check sudoers  /etc/sudoers /etc/sudoers.d/*
mode  /etc/sudoers.d/* 0440
apply sudoers  at the next sudo
check sshd     /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf
apply sshd     at the next start of ssh.service (systemctl restart ssh)
check unit     /etc/systemd/system/*.{service,socket,timer,mount,path,target}
apply unit     after systemctl daemon-reload
check shsyntax /etc/default/grub
apply shsyntax after update-grub, at the next boot
check grubcfg  /boot/grub/{grub.cfg,custom.cfg}
…
```

- `sc scope PATH` (promised for M3 in the M2 plan) prints what SmartConfig knows about a path: recorded or excluded and by which scope line, tier, fingerprint-only, its checker, and when a change applies.
- The graph is deliberately small in M3: checker, set and apply text. Dependency edges such as "grub.cfg is generated from /etc/default/grub" become real when applied markers are built (section 13).

## 5. Checkers

### 5.1 Findings and severity

```go
type Finding struct {
	Rule     string   // "fstab-source-missing"
	Severity Severity // Blocker, Error, Warning
	Path     string   // the real path, never the scratch copy
	Line     int      // 0 when unknown
	Text     string   // one sentence, ours; may name a device or option, never a secret
	Raw      string   // the validator's own lines; shown by sc check and sc edit only
}
```

- **blocker**: the machine may not boot, or you may lose root, sudo or SSH access.
- **error**: the consumer rejects the file; a service fails at its next start or reload.
- **warning**: accepted, but probably not what you meant.
- A validator failure with no rule of ours gets the checker's general rule (`sudoers-syntax`, `sshd-invalid`, …) with the severity in the table below.

### 5.2 Only what the edit added

- `sc edit` and scd check the previous content and the new content, and report findings present in the new and absent in the old. A finding's identity is its rule plus its text with the line number left out.
- `sc check` shows everything it finds: there is no "before".
- Cost: two validator runs of 10-700 ms each (section 2).

### 5.3 How a validator is run (`run.go`)

- The tool is looked up in `/usr/sbin`, `/usr/bin`, `/sbin`, `/bin` only, never through `$PATH`. Not found: the check carries on with our rules and says `no validator found (sshd)`.
- Arguments are passed as a list, never through a shell. Environment: `LC_ALL=C` and a fixed `PATH`. Stdin is `/dev/null`.
- The copy lives in `$SC_HOME/tmp/check-XXXX/` (0700, root; `$SC_HOME` is never under a watched root), under the file's own base name, and is removed afterwards.
- Timeout 10 s; the whole process group is killed. Output is cut at 64 KiB.
- Validators run as the caller (root for scd and `sudo sc`). They are only ever given files from the graph, which are root's: nothing under a login's `.ssh` has a checker, so no user-controlled content reaches a validator.

### 5.4 The first set

| Checker | Files | Validator | Our rules (severity) |
|---|---|---|---|
| fstab | `/etc/fstab` | `findmnt --verify --tab-file` | `fstab-source-missing`: a device that does not exist, on a line without `nofail` or `noauto`, other than `/` (blocker: 90 s wait, then emergency mode, and root is locked on Ubuntu; PROJECT_LOG D6). The same with `nofail`: warning. `fstab-root-source` (error; Ubuntu most likely still boots, D6). `fstab-fstype-mismatch` (as source-missing; root only). `fstab-option-typo`: an option one or two letters away from a common one, e.g. `defalts` (as source-missing; findmnt accepts it). `fstab-fields` (error). |
| sudoers | `/etc/sudoers`, `/etc/sudoers.d/*` | `visudo -c -f` | `sudoers-syntax` (blocker: sudo refuses to run at all). `sudoers-mode`: a drop-in not 0440 root (error). |
| sshd | `sshd_config`, `sshd_config.d/*.conf` | `sshd -t -f` | `sshd-invalid` (blocker: the running server keeps going, the next start fails and is not retried; watch list). |
| unit | units in `/etc/systemd/system` | `systemd-analyze verify` | `unit-syntax` (error), `unit-exec-missing` (error), `unit-unknown-key` (warning). |
| grub | `/etc/default/grub`, `grub.cfg`, `custom.cfg` | `sh -n`, `grub-script-check` | `grub-default-syntax` (blocker: update-grub fails or writes a broken menu), `grubcfg-syntax` (blocker). |
| plain | files with no validator | none | `nsswitch-no-files`: the `passwd:` line names no `files` or `systemd` (blocker: users vanish). `preload-missing-lib`: a library named in `/etc/ld.so.preload` does not exist (blocker: every dynamic program prints an error or fails; the static `sc` still runs). `flag-nologin`, `flag-sshd-not-to-be-run`: the file exists (warning). `hosts-no-localhost` (warning). |

**Second set**, same framework, after the first set is reviewed: netplan (`netplan-invalid`, blocker over SSH), passwd and group (`passwd-invalid`, `group-invalid`), sysctl (`sysctl-invalid`), udev rules (`udev-invalid`).

Each rule has its explanation in `rules.go`: two to five lines, what it means, why it matters, how to fix it. `sc check -v` and `sc edit` print it.

## 6. CLI

### 6.1 `sc check [PATH|ID]…`

```
$ sudo sc check /etc/fstab
SEVERITY  FILE        LINE  RULE                  PROBLEM
blocker   /etc/fstab  12    fstab-source-missing  UUID=1111… is not a disk on this machine
1 blocker. sc check -v explains; sc log /etc/fstab lists the versions to restore.
```

- No argument: every file on disk that has a checker.
- An id checks that saved version (file rows only).
- Exit 0: no blocker or error. Exit 2: at least one. Exit 1: `sc` itself failed (one line on stderr, as today).
- `-v` adds each rule's explanation and the validator's own lines.

### 6.2 `sc edit PATH`

1. **Refusals, before anything is written:** a fingerprint-only path; a symlink, directory or device; a parent directory that is not a real directory owned by root or the caller (the restore rules of M2 5.6).
2. **One at a time:** a lock in `$SC_HOME`; a second `sc edit` says who holds it.
3. **Copy** the file to `$SC_HOME/tmp/edit-XXXX/<name>` (0600) and run the editor: `$SUDO_EDITOR`, `$VISUAL`, `$EDITOR`, then `editor`, `nano`, `vi`. No shell.
4. **Unchanged:** say so, write nothing.
5. **Check** the new content against the old (5.2). Warnings are printed and do not stop the save.
6. **A new blocker or error:** print the findings with their explanations, then
   `What now? (e)dit again, (s)ave anyway, (q)uit without saving [e]:`
   End of input (no terminal) means quit: a failing file is never saved unattended.
7. **Save:** if the file changed on disk since step 3, refuse and say where your version is kept. Otherwise the file is replaced atomically with its mode and owner (a new file: the graph's mode, root), and its row is written in the same store transaction as the write, as a restore does, so scd adds no row of its own.
8. **Rows:** the state before, if the newest row does not already hold it (`manual`, "before sc edit"); then one row with the new origin `edit`, intent `sc edit`, or `sc edit, saved with 1 blocker`.
9. **Last line:** `saved /etc/fstab as 3fa2c1 (before: 9b01de); takes effect at the next boot`.

### 6.3 `sc scope PATH`

See section 4. Read-only; works without root for the scope part.

## 7. scd: a check after every recorded change

- After the worker records an automatic row for a path the graph knows, it hands the path to **one checker goroutine** through a bounded queue (100 paths, a set). The worker never waits for it. A full queue drops the path and logs one line; `sc check` still finds the problem.
- The goroutine checks the new row's content against the previous row's, and logs only new findings:
  `T1 /etc/fstab: check: blocker fstab-source-missing, line 12 (sc check /etc/fstab)`
  at err for a blocker, warning for an error, notice for a warning.
- When a file that was logged as failing passes again: `T1 /etc/fstab: check: ok again`.
- Not checked: `first seen` rows of the startup baseline (1,500 files at boot), restores and `sc edit` rows (already checked or deliberate), fingerprint-only rows, rows held by the rate limit until they are written.
- A change to one file of a set (a sudoers drop-in) checks that file alone in M3; cross-file conflicts are out of scope.

## 8. Store

- No migration; `user_version` stays 1.
- New: `Store.Replace(path, data, mode, uid, gid, origin, intent)`, built from Restore's write path (prepare, lock, stamp check, commit, row).
- New origin string `edit`. `sc log` already prints origins as they are; `sc-m2` and `sc-m1` read such rows as file rows (`make m1-compat` gains one case).

## 9. Order of implementation

**The loop for every step** is M2's: WORKLOG says what the step is; code and tests; gofmt, vet, tests as user and root, race for timing code; one code commit; one worklog commit; push; CI green before the next step.

| # | Chunk | Commit | Tests |
|---|---|---|---|
| 1 | A | `check: findings, rules, runner` | fake tool in a temp dir: timeout kills the group, output cap, missing tool, env and argv exact |
| 2 | A | `check: file graph` | parse errors; every graph glob lies inside the scope; `For(path)` |
| 3 | A | `check: fstab` | golden outputs from this VM; own rules on fabricated fstabs; real `findmnt` when present |
| 4 | A | `check: baseline diff` | a finding that only moved lines is not new |
| 5 | B | `sc check` | table, `-v`, exit codes, by id, no validator |
| 6 | B | `store: Replace` | rows, refusals, kill between write and commit; with a watcher: no extra row |
| 7 | B | `sc edit` | `EDITOR` is a script; unchanged; clean save; blocker then q, e, s; end of input quits; changed on disk; new file mode |
| 8 | C | `check: sudoers` | syntax, mode rule |
| 9 | C | `check: sshd` | with `-h` and a throwaway key when not root |
| 10 | C | `check: units, grub` | scratch copy under the unit's name; stock-unit noise is not new |
| 11 | C | `check: plain rules` | nsswitch, ld.so.preload, flags, hosts |
| 12 | D | `watch: check after a row` | new finding logged once; ok again; baseline rows not checked; a slow validator does not delay rows; queue bound |
| 13 | D | `check: second set` | netplan through the generator binary (asserts no reload request), passwd/group, sysctl, udev |
| 14 | D | `sc scope` | recorded, excluded by which line, tier, checker |
| 15 | E | `scripts: accept-m3.sh`, docs | section 11 |
| 16 | E | sign-off runs, final review, tag `m3` | section 11 |

**Gates.** G1: before step 12, your OK that scd may run validators as root (question 3). G2: before any install or run on the real system, the M2 soak check is done and you say yes.

**Tests never touch `/etc`.** Every checker takes the file to read as an argument, so tests use temp dirs. Real validators are used when installed (`exec.LookPath`), else the case is skipped; the golden outputs keep the parsing covered in CI.

## 10. Estimate

About 4 to 5 working days: chunk A one day, B one day, C one day, D one day, E and sign-off half a day plus your time. M2 took 5 days with a larger review load.

## 11. Acceptance and sign-off

**Owner scenario (the one for M3).** You add a data disk to `/etc/fstab` with `sudo sc edit /etc/fstab` and mistype its UUID. Before anything is saved, sc says this machine would wait 90 seconds at boot and stop in emergency mode with root locked, and why. You quit; fstab was never broken. Then the same typo with nano: within seconds the journal has the warning, `sc check` explains it, and `sc restore` puts the file back.

**`sudo ./scripts/accept-m3.sh`** (in the VM, refuses to run elsewhere, undoes itself): a scratch `$SC_HOME` and its own test daemon as in M2; test files only under `/etc/sc-accept.d`; real validators on fabricated files; `sc edit` driven by a script as editor, through every answer; the scd journal lines.

**Sign-off runs, each with your OK** (after the M2 soak check and a fresh snapshot):
- S1: all checks; `accept-m3.sh` passes.
- S2: install the M3 build, restart scd; `sc check` on the real system, read-only. Its findings on a healthy system are reviewed with you: each is a real problem or a rule to fix.
- S3: the owner scenario on the real `/etc/fstab` (quit without saving) and a real `sshd_config` typo made with nano, then restored.
- S4: final review; docs; tag `m3`.

## 12. Risks

- **A validator that changes something.** Mitigation: only the forms in section 2, each run here first; a test asserts netplan's generator makes no reload request; new forms need the same proof.
- **False alarms** make people ignore warnings. Mitigation: the baseline rule (5.2); S2 reviews every finding on the healthy system; `fstab-option-typo` only fires near a known option.
- **False calm:** a check passes and the machine still breaks (a misspelt ext4-only option, a conflict between two files). The output says what was checked; `sc check` never says "safe".
- **Root runs more programs.** scd starts validators as root. They are distribution tools reading root's own files, with a timeout and no shell (5.3); user-owned files have no checker.
- **This VM is slow and gets paused.** Timing-bound tests have failed here under load. The checker goroutine's tests use hooks, not sleeps; the existing flaky watch tests are fixed before chunk D.

## 13. Deferred (the M2 plan's "M3" items)

| Item | Now |
|---|---|
| Validators; existence-flag alerts; `sc scope PATH` | **in M3** |
| Applied markers ("pending until update-grub") | later; M3 only prints the `apply` text |
| Account-set grouping and set restore; PAM grouping | later; M3 checks passwd and group files one by one |
| apt/dpkg change sets | later (with the M5 hook file) |
| Active/inert labels, vendor-override detection | later |
| Inode flags, ACLs, xattrs, directory metadata | later (needs a store change) |
| Secret redaction in `sc diff` and `sc cat` | later |
| `/etc/alternatives` chains, rc?.d links | later |
| Extra roots (`/usr/local/etc`, crontabs, …) | later |
| Content-based secret detection (final review) | later |
| nft, logrotate, apparmor, PAM checkers | later, when a check-only form is proven |

"Later" is decided at M3 sign-off: either an M3 follow-up list, as for M2, or a milestone of its own.

## 14. Questions for you

Each has a recommendation, and the plan assumes it.

1. **Scope.** M3 keeps to its MILESTONES line (graph, validators, rules, `sc edit`, `sc check`) plus the existence flags and `sc scope`; the other items of section 13 wait. *Recommendation: yes.*
2. **Checker sets.** First set: fstab, sudoers, sshd, units, grub, plain rules. Second set in the same milestone: netplan, passwd/group, sysctl, udev. *Recommendation: both.*
3. **scd runs validators** as root after recording a change, to warn about edits made with any editor (gate G1). *Recommendation: yes.*
4. **A blocker in `sc edit`:** ask, with "save anyway" as an answer, as visudo does; end of input quits. *Recommendation: yes, rather than a hard refusal.*
5. **No store change:** findings are not stored. *Recommendation: yes; it keeps `sc-m2` as a fallback.*
6. **Reviews:** one review per chunk by a single reviewer, and one final multi-agent review before the tag (I ask before starting it). *Recommendation: yes; M2's five multi-agent chunk reviews were its largest cost.*
7. **Sign-off on the real system** (section 11, S2 and S3), each after a snapshot and your OK, and only after the M2 soak check. *Recommendation: yes.*

## 15. Changes after approval

| # | Date | Change | Why | Commit |
|---|---|---|---|---|
| C1 | 2026-10-03 | The graph has no `with` line. The built-in graph starts with its header only; each checker's `check`, `apply` and `mode` lines are added with the step that builds it. | Section 7 checks only the file that changed, so `with` would have been parsed and never read. Your OK (asked). | `95da13b` |
| C2 | 2026-10-03 | fstab severities follow `systemd-fstab-generator` (A14): a missing device or misspelt option is a blocker only on a local filesystem without `nofail` or `noauto`; on `/`, swap and network filesystems it is an error; with `nofail` or `noauto` a warning. New rule `fstab-verify` (warning): a findmnt error sc has no rule for. `fstab-fields` is a line libmount ignores (fewer than three fields, or a non-numeric dump or pass field). findmnt's missing-mount-point error is not reported. | The generator requires swap from `swap.target`, which `sysinit.target` only wants, and network mounts from `remote-fs.target`; only `local-fs.target` fails into emergency mode. systemd creates a missing mount point. | `b665691` |
| C3 | 2026-10-03 | From the chunk A review: `Finding` has a `Key` (never shown) that tells apart findings with the same text; the runner returns stdout and stderr apart; `fstab-option-typo` fires only one slip from a common option, against a list of known options (`mountopts.txt`); findmnt's type warning is ignored for compatible types and is a warning within the ext family; `Report.Notes` says when a validator did not really run. | [reviews/2026-10-03-m3-chunk-a.md](reviews/2026-10-03-m3-chunk-a.md): three findings could hide a line that stops the boot. | `a2e325f` |
| C4 | 2026-10-03 | `sc check` without the right to write `$SC_HOME` (a user, no sudo) puts its scratch copies in a private directory of its own under `$TMPDIR` (0700, removed afterwards). An argument is a row id when it is 4 to 64 hex digits, else a path (`./name` for a file named like an id). | Section 5.3 put every scratch copy under `$SC_HOME`, which only root may write; `/etc/fstab` is world-readable and should be checkable without sudo. | `afa9403` |

---

## Appendix A. Evidence (2026-10-03, this VM)

Inputs were fabricated files in a scratch directory (kept in `~/smartconfig-work/m3plan/`); results from the real system are counts and exit codes only.

- **A1 tools present:** findmnt (util-linux 2.39.3), sshd (OpenSSH 9.6p1), visudo 1.9.15p5, systemd-analyze (systemd 255), netplan 1.1.2, grub-script-check, pwck, grpck, sysctl (procps-ng 4.0.4), udevadm, nft 1.0.9, logrotate 3.21.0, apparmor_parser. Absent: named-checkconf, nginx, apache2ctl, testparm, cryptsetup.
- **A2 findmnt:** good file exit 0; unknown UUID exit 1 with two `[E]` lines; unknown `/dev` path exit 1; the option `defalts` exit 0, also as root; type `xfs` on an ext4 disk: `[W]` as root, nothing as a user (it cannot read the disk); a missing disk with `nofail` still exit 1 with `[E]`.
- **A3 visudo:** `-c -f` works as a user; exit 1 and `sudoers.bad:2:19: syntax error` followed by the offending line. The real system as root: 3 files parsed OK, all 0440 root.
- **A4 sshd:** as a user `sshd -t -f` fails with "no hostkeys available"; with `-h` and a throwaway key it works. Unknown keyword and bad value: exit 255 with the line number. A drop-in file alone checks clean.
- **A5 pwck/grpck:** scratch files exit 0 (good) and 2 (bad), quoting the bad line. The real system as root: pwck exit 2 on a stock install (system users whose home directory does not exist), grpck exit 0.
- **A6 systemd-analyze verify:** exit 1 for an unknown key, a missing `ExecStart` binary and a broken section header; a scratch copy must carry the unit's file name. The 14 real units in `/etc/systemd/system` together exit 1 because of one stock unit's man page reference.
- **A7 netplan:** `netplan generate --root-dir D` generated into `D/run` and then tried `systemctl daemon-reload` (refused only because it ran as a user). `/usr/libexec/netplan/generate --root-dir D` gave the same result and messages with no reload request; `netplan get --root-dir D all` also parses, in 250-380 ms.
- **A8 grub:** `grub-script-check` exit 1 on an unclosed `menuentry`; `sh -n` exit 2 on an unterminated quote in a `default/grub`-style file.
- **A9 sysctl:** `--dry-run -p F` exit 1 on an unknown key or a non-assignment line. As root with a file setting `vm.swappiness` to 61: printed the line, the value stayed 60.
- **A10 udevadm verify:** exit 1 with file and line; on the real system 113 of 121 rule files pass and 8 fail.
- **A11 rejected for now:** `nft -c -f` fails as a user ("cache initialization failed") and reads kernel state as root; `logrotate -d` exits 1 on a good file; `apparmor_parser -Q -K` works (exit 1 with a line number) but is left for later.
- **A12 editors:** `$EDITOR` and `$VISUAL` unset; `/usr/bin/editor` is nano; vi is vim.tiny.
- **A14 systemd-fstab-generator** (run with `SYSTEMD_FSTAB` on a fabricated file, output into a scratch directory): a plain ext4 line lands in `local-fs.target.requires` with `Requires=systemd-fsck@…`; with `nofail` in `local-fs.target.wants`; with `noauto` nowhere; swap in `swap.target.requires`; nfs in `remote-fs.target.requires`. `sysinit.target` has `Wants=swap.target local-fs.target`; `local-fs.target` has `OnFailure=emergency.target`. findmnt reports a line with one or two fields, or a non-numeric fifth field, as `parse error at line N -- ignored`; three fields parse.
- **A13 files per consumer here:** 1 active fstab line, 2 files in `sudoers.d` (one is its README), 0 sshd drop-ins, 2 netplan files, 14 service units in `/etc/systemd/system`, 5 udev rule files, 11 sysctl.d files, 31 pam.d files, 114 entries in `/etc/apparmor.d`.
