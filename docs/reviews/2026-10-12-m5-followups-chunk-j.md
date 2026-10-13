# Review of chunk J: the M5 follow-ups' restore and check items

Date: 2026-10-12, after M5 follow-ups 3, 4, 5 and 5a and `8b98817` were built
(`5f93d06..f48e3c0`):
- `sc restore` checks the version it is about to write, and asks;
- `/etc/default/grub.d/*.cfg` is checked as `/etc/default/grub` is;
- `sc help <unknown>` exits 1;
- scd, and then `sc restore`, judge a version by the mode and owner its row recorded.

**How it was reviewed:**
- **The reviewer:** one AI agent, in its own clone of `f48e3c0` (pushing disabled),
  with its own `sc` builds and throwaway stores.
  - The first run died in a crash of this VM before it reported. Its clone and scratch
    are in `~/smartconfig-work/review-m5fu-j/run1-crashed/`.
  - The second run started from a fresh clone, with the same prompt. It took the first
    run's tests and mutation results as leads, and reported only what it reproduced.
- **The limits:** no sudo, nothing written to `/etc` or the real store, no QEMU.
- **Claude's checks:**
  - reran the reviewer's 14 failing tests on `f48e3c0`: all 14 fail;
  - read each finding in the code;
  - every fix has a test that fails without it;
  - mutations after the fixes: the review's 32, anchored on the new code, and 27 for the
    fixes. All 59 are killed (`~/smartconfig-work/fu-scratch/j-mut/`, `results-pass3.txt`;
    R10 by the full suite's signal tests, `results-pass4-R10-full.txt`).
- **Report:** `~/smartconfig-work/review-m5fu-j/findings.md`; its tests and scripts in
  `clone/` and `scratch/`.

**Gate: no high-severity finding.** Three were medium (J1, J2, J3). All 13 are fixed.
Three asked for a decision, and the owner picked the recommended answer for each
(2026-10-12): J1, J6 and J7.

## What held up

- **The scripts.** `accept-m2.sh` restores only `/etc/sc-accept.d/*` (no checker) and
  digest rows, which are refused before any check. `accept-m4.sh` restores a file no
  checker reads. `smoke.sh` restores `/etc/hosts`, whose checker has only a warning. In
  `lab/e2e.py`, every restore is of the rescue version, and `RESTORE_REFUSED` still holds.
- **The answers.** y, Y, yes, " Y ", `y\r\n` and a final y with no newline restore; empty
  input, a blank line, "ye", "yess" and end of input do not.
- **Ctrl-C at the question.** sc ends by SIGINT with one line, writes nothing, adds no row
  and leaves no scratch directory.
- **A check that cannot run.** With no scratch place there is one note, and the restore
  goes on. On a read-only root no validator runs.
- **Mode handling.** `Meta.Mode` is a Go `FileMode` in the store, M1's rows included, so
  setuid, setgid and sticky are judged right.
- **`check.Added`** keys on the line's content, not its number, which is right for moved
  lines.
- **The real drop-ins.** `50-cloudimg-settings.cfg`, a `kdump-tools`-style append and the
  lab's `60-sclab.cfg` give no findings. grub.d is in scd's scope (tier 1).
- **The race detector** found nothing in the restore, help, check and watch tests.

## Findings

| Area | Finding | Severity | Fixed |
|---|---|---|---|
| the rescue undo (J1) | The undo a rescue report prints, `sc restore GOOD`, asked when GOOD had an error the file now lacked, or when the file was now missing or a link (all of GOOD's problems counted as added); without a terminal it exited 2. The README said it asks nothing. | medium | Your pick: the version sc status's undo names (the path's row at the last healthy boot whose row is known) booted healthy, so it is written with no check and no question. Status's boundary code is `healthyLine`, which both use (`4b3c88b`). |
| status and check (J2) | `sc status` and `sc check <id>` judged a version without its row's mode. A sudoers drop-in made world-writable was "no problem found" in the rescue report, with no undo and exit 0, while scd logged an error. | medium | Both use `CheckVersion` with each row's mode and owner (`5a9b069`). |
| grub.d (J3) | `NAME=$NAME word` and `NAME+="..."`, the two ways a drop-in appends wrongly, passed the check. dash runs the word as a command, and update-grub, and with it a kernel's postinst, stops. | medium | Both are blockers; a value of plain characters, `$NAME` and `${NAME}` is judged (`b5ce497`). |
| cut checks (J4) | A check cut short (a validator out of time) restored without a word. | low | Its notes go to stderr (`9a08186`). |
| refusal order (J5) | The check and its question came before Restore's own refusals: a read-only store, a fingerprint-only path or a directory that became a symlink was asked about and a yes then refused; the symlink's refusal read differently with `--force`. | low | `store.RestoreRefusal` runs them first, in Restore's order (`9a08186`). |
| the question (J6) | The question read whatever stdin was and wrote to stdout: `sc restore X > file` waited with its prompt in the file, and a `while read id` loop gave it the next line as its answer. `sc prune` did the same. | low | Your pick: both ask only when stdin and stdout are terminals; without one they read nothing, name `--force` or `--yes` on stderr, and exit 2 (`71ba76d`). |
| removals (J7) | Restoring a "did not exist" or deleted row of a checked file removed it with no question; sc status then called it "error: deleted". | low | Your pick: such a removal asks, unless `--force`, for the files statusProblem calls an error (not flag files, ld.so.preload, or a unit a later directory stands in for) (`4b3c88b`). |
| scd's check (J8) | scd's check of the version after a change read the mode on disk, and remembered it by content alone: a chmod before the check was blamed on the content row, and the chmod's row said nothing. | low | The version after is checked by its row's mode and owner; the result is remembered by content, mode and owner (`ed5c93f`). |
| grub.d (J9) | `GRUB_TIMEOUT=0 GRUB_TIMEOUT_STYLE=hidden`, which sets both, was a blocker; `grub.d/.50-old.cfg`, which sh's `*.cfg` skips, was checked. | low | Assignments in a row are fine. A name with a leading dot that only a wildcard matched has no checker, in every directory of the graph: sh, glob(3), systemd, udev and sudo all skip it (`b5ce497`). |
| help (J10) | `sc help restore abc123` said `unknown command "restore abc123"`; `sc help help` had lost its text. | low | `restore has no command "abc123" (see: sc help restore)`, exit 1; an unknown topic is as before (`87f987e`). |
| scratch (J11) | A scratch directory was made, or its absence noted, before knowing whether the path is checked at all. | cleanup | The checker is looked up first (`9a08186`). |
| docs (J12) | Comments on `mutating`, on a link at the restored path and on grub.d, the README's "asks nothing", the worklog's "Now", and MILESTONES' Open items 3 and 11 no longer said what the code does. | cleanup | Fixed with the findings they belong to, and in this write-up's commit. |
| tests (J13) | Mutations survived the suite: answers ("yes", spaces), the question's newline, the explanation, a check that fails, no scratch directory, the read-only root, the row's uid and gid, a warning that asked, the setgid, sticky and gid bits, scd's version before by gid, `InitDefaultHelpFlag`. | cleanup | The reviewer's passing tests are kept, and new ones added (`TestRestoreAnswers`, `TestRestoreUnchecked`, `TestRestoreCheckCannotRun`, `TestRestoreChecksOwner`, `TestRestoreWarningNotAsked`, `TestRestoreRemovalNotAsked`, `TestRestoreCtrlCAtQuestion`, `TestStatusBeforeByRowMode`, `TestCheckVersionModeBits`, `TestCheckRestoreModeOkAgain`, `TestCheckRowOwners`). All 59 mutations are killed. |

## Not changed

- **A link row restored over a checked file** asks nothing, though sc status then calls
  the path an error ("now a symlink"), as J7 found for removals. The review did not raise
  it; it is on MILESTONES' Open list.
- **A version restored over a file that is now a link, or missing**, other than the rescue
  version, is compared with nothing, so all its problems count as added and are asked
  about. sc status calls such a path an error itself.
