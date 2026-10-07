# Review of chunk F: M3's follow-ups

Date: 2026-10-07, after M3 follow-ups 1-5, 7 and 8 were built (`9e215cc..4f90bbc`:
unit drop-ins, sshd's `ListenAddress`, an sshd drop-in inside `sshd_config`, notes and
the validators' words, a stop signal and the validator, scd's two-row lookup, the line
of an unclosed quote and of each swap line). Two reviewers (AI agents), each in its own
clone of `4f90bbc` (pushing disabled) with its own `sc` build and throwaway stores: A on
`internal/check` (the new checkers and rules, the runner, `Stop`), B on how they are
used (`sc check -v`, `sc edit`, the signal handler, scd's checker and the store,
`accept-m3.sh`, the docs, the tests). No sudo, nothing written to `/etc` or the real
store, no QEMU. Claude reran each failing test the reviewers wrote before fixing it.
Reports: `~/smartconfig-work/review-m3fu-f/findings-{a,b}.md`.

**Gate: no high-severity finding open.** A found one high (a template alias's drop-in
was not checked); it is fixed. All 4 medium findings and all low ones are fixed, and so
are the test gaps and docs, in `cfb704e`. Nothing goes on a follow-up list.

## What held up

- Unit drop-ins against the real systemd 255: journald, getty instances, the
  display-manager → gdm3 → gdm alias chain, masked units, a template drop-in; paths are
  the real ones in findings; nothing is left in `$SC_HOME/tmp`.
- The sshd check inside `sshd_config`, with this VM's own file: a `Match` a drop-in
  leaves open ends with it, a relative `Include` works, the drop-ins keep their order.
- No validator's raw output in a note, none in scd's journal; `sc check` without `-v`
  prints none of it.
- `sshd-listen-missing` in every address form tried; the loopback-only skip; ssh.socket's
  `FreeBind=yes`.
- `Store.Around` gives scd's checker the same two rows as the old loop in every case it
  meets (B, against the old code); its query plans.
- `sc edit` cannot save after a signal during its check; `editing`, `ignoreHangup`, scd's
  first signal, `ForceStop` and `mutating` behave as before; sc dies by the signal with
  one stderr line.
- `accept-m3.sh`'s new cases give the exit status and rule they expect (run as a user).

## Findings

| Area | Finding | Severity | Fixed in |
|---|---|---|---|
| unit drop-ins (A1) | A drop-in for a template alias or its instance (`autovt@.service.d`, `autovt@tty2.service.d`; autovt@ is getty@ on Ubuntu) was not checked: systemd reports under `getty@i.service` / `getty@tty2.service`, not among the names sc kept. The autologin drop-in without its `ExecStart=` reset, which leaves no login prompt, passed. | **high** | `unitNames`: the alias's names, a template alias's in the instance's form |
| sshd together (A2) | With a space (or a glob or quote character) in `$SC_HOME`, the rewritten `Include` split into patterns that matched nothing: a broken drop-in passed, no note. | medium | the path is quoted; a quote or pattern character in it: checked alone, with a note |
| sshd together (A3) | When the second run (the candidate empty) did not finish, another file's problem became a blocker of a good drop-in, with no note. | medium | a note; what was said about other files or without a line is left out |
| signals (B1) | A signal to `sc check` with more files often left a scratch copy (8 of 20 runs): the command went on to the next file after `Stop`. A removed scratch directory could be made again by a checker. | medium | `Scratch` and `scratchMkdir` refuse after `Stop`, under its lock; a run killed by `Stop` returns `ErrStopped`, and the command ends quietly |
| signals (B2) | A validator being started when `Stop` ran was not killed and ran on after sc died (10 of 30 runs). | low | started and registered under `Stop`'s lock; none starts after it |
| signals (B3) | After Ctrl-C, `sc check` could print "findmnt was killed" (12 of 60 runs). | low | `ErrStopped` (as B1) |
| signals (B4) | `Stop` killed `sc boot`'s grub-editenv in the middle of rewriting grubenv in place. | low | `Runner.KeepOnStop` for sc boot's runner |
| console (B5) | `sc status --console`'s time limit left check scratch copies (7 of 167 stopped runs, a writable `$SC_HOME/tmp`). | low, pre-existing | `check.Stop()` there too |
| unit drop-ins (A4) | A unit a generator makes (an `/etc/init.d` script, an fstab mount) got `unit-dropin-orphan`: verify runs no generators. | low | a note when a generator directory has it |
| unit drop-ins (A5) | `-.mount.d` (the root mount's own) was skipped as a prefix drop-in. | low | a stem of `-` is the unit's own name |
| unit drop-ins (A6) | A `:` in `$SC_HOME` split `SYSTEMD_UNIT_PATH`: the drop-in silently unchecked. | low | not checked, with a note |
| reports (B6) | A file whose note said "it was not checked" was counted clean ("no problems found", exit 0). | low | `Report.Unchecked` (nothing was checked: `sc check` counts it not checked, exit 1; scd says nothing); a validator that exited without a word sc understands marks the report incomplete |
| fstab (A7) | Picking a line by a message naming its source never worked for a tag (`UUID=…`, no trailing colon in findmnt 2.39.3): with three swap lines and the middle one missing, a healthy line got a finding. | low | the source followed by `:` or the end of the message, unquoted |
| run (A8) | `Stop` could leave a scratch directory (made after it, or made again). | low (reasoned) | as B1 |
| output (B7, B9) | Notes printed unescaped (some carry a path's stem or sshd's text); the `-v` block ran into the tally line. | cleanup | `show()`; a blank line after the block |
| tests (A9, B8) | Behaviour no test pinned (A: 13 of 30 mutations survived; B: `show()` on the validators' lines, the hint's condition). | cleanup | a test for each; each fails on the code before its fix |
| docs (B10) | MILESTONES and the worklog overstated follow-up 5; README named only `.service` drop-ins and did not say `sc edit` shows the validators' words. | cleanup | docs |

Along the way: the quote scanner's whole-file fallback was unreachable (a file whose
every line is closed read alone is closed read as one), so it and the scanner's line
counting were removed, and the scanner is a yes/no check.

Not pinned by a test, by reasoning only: `ErrStopped`'s quiet end in `cmd/sc` (the
handler usually claims the end first; `TestStop` pins `ErrStopped` itself) and the
console limit's `Stop` (no deterministic window; B's probe showed the leak).

## Tests

New: `TestUnitNames`, `TestUnitAliasPath`, `TestUnitDropInTemplateAlias` (real systemd),
`TestUnitDropInGenerated`, `TestUnitDropInColonHome`, two `TestUnitDropIn` cases,
`TestSshdTogetherScratchPath` (real sshd), `TestSshdTogetherSecondRunUnfinished`, three
`TestSshdTogether` cases (a quoted, a lowercase and a cross-directory `Include`),
`TestFstabTagSource`, `TestRunCancelledBeforeStart`, more of `TestStop` and
`TestShQuoteOpen`; in `cmd/sc` `TestSignalScratchNextTarget`,
`TestSignalValidatorStarting`, `TestSignalKeepsGrubEditenv` (B's, kept), `TestCheckCLIUnchecked`,
`TestCheckCLISaidShown`. Of 30 fixes undone one at a time, 28 made a test fail (B3's
through `TestStop`); the other two are the ones named above. gofmt, vet, `go test ./...`
as a user and as root, `make race` (uncached) and `make m1-compat` pass.
