# Review of chunk G: M4's follow-ups for `sc status`, restore and the boot units

Date: 2026-10-07, after chunk G was built (`e5af809..0fcabd1`: M4 follow-ups 1, 2, 7
and 9, the rescue mount hint, scd `Type=notify` with the verdict after its startup
rescan, the console's header first, and the soft-reboot and early-SIGHUP limits
documented). Two reviewers (AI agents), each in its own clone of `0fcabd1` (pushing
disabled) with its own `sc` build and throwaway stores: A on boot integration (scd's
`Type=notify`, the boot units, the boots file, the console's header-first timing and
what the lab matches), B on `sc status`, `sc restore` and what they read (the fstab and
mount table checks, the symlink judgement, the cosmetics, the docs). No sudo, nothing
written to `/etc` or the real store, no QEMU; A ran its systemd experiments in its
user's own manager (`systemd-run --user`, units removed after). Claude reran each
failing test the reviewers wrote before fixing it. Reports:
`~/smartconfig-work/review-m4fu-g/findings-{a,b}.md`.

**Gate: no high-severity finding open.** Neither reviewer found a high one. All
medium and low findings, the test gaps and the docs are fixed in `0a55306`. Nothing
goes on a follow-up list.

## What held up

- Boot does not wait for scd's startup rescan: with `DefaultDependencies=no`,
  multi-user.target was active at once in A's user manager, and the verdict's oneshot
  started 0.01 s after READY (a 1 min 42 s baseline). The spelt-out dependencies match
  systemd's defaults (compared with `systemctl show` of the installed scd); only
  `Before=multi-user.target` goes. A hung start fails at the first timeout and the
  ordered oneshot runs at once; scd masked or disabled does not hold the verdict.
- `NOTIFY_SOCKET`: READY once, an abstract `@` socket works, validators run with a
  clean environment, `NotifyAccess=main` fits. `systemctl reload` during startup merges
  into the start job and sends no SIGHUP.
- Torn lines: a fragment, two crashes in a row, a cut `#torn`, zeros from a power cut.
- The header-first race: `final` and `headShown` only under the lock; no line doubled
  or lost in time or slow (but A5 below).
- Every lab regex that reads console rows accepts `<id> MM-DD HH:MM`.
- `unmountedMount` on the usual layouts: a separate `/boot`, `/boot/efi`, escaped
  names, swap, comments, automount, `noauto`, no mount table; the undo's order works on
  a read-only root (`mount` needs no writable `/`).
- A checked file now a symlink: the restore puts the file back over the link and leaves
  its target alone; a link that was a link stays at severity 0. Read-only commands make
  no `$SC_HOME/tmp`, as a user, as root and on a read-only root. The `FS_IOC_GETFLAGS`
  ports build (amd64, arm64, riscv64, loong64, s390x, ppc64le).

## Findings

| Area | Finding | Severity | Fixed by |
|---|---|---|---|
| scd ready (A1) | A startup path held back by the free-space floor or a store error kept scd from ever sending READY: systemd stopped it after 5 min and started it again, for good (2 to 3 starts per 10 min never reach the start limit), and every boot's verdict waited 5 min. | medium | READY once every startup path is recorded or on hold; the `baseline:` line counts those held back |
| accept-m2 (A2) | Step 18 failed on any machine with the M4 units: `sc-boot-ok`'s new `After=scd.service`. | medium (sign-off) | that `After=` allowed |
| lab 2.5 (A4) | A slow report (19 of 20 emergency boots in the lab runs took over 2 s) is written as header, then rest; a getty banner between them ended the block at the header: an [M4] FAIL. | medium (sign-off) | `emergency_report` takes the getty's lines out when the report's rest follows them, and waits for the rest |
| console (A5) | The header timer fired once: a header known after 2 s (a slow `/var`, `systemctl`) was never shown; `TestConsoleStatusHeaderFirst` failed 1 of 3 runs under load. | low-medium | a header known late is shown at once |
| accept-m2 (A3) | Step 3 could no longer kill scd during its startup rescan (`systemctl start` waits for READY), and its 20 s restart wait included a whole rescan. | low | `start --no-block`; the restart wait takes "activating" |
| scd ready (A6) | A live event on a startup path (edited while scd was down, written again at boot) cleared its startup reason: READY came before its row. | low | a `startup` flag events do not clear |
| boots (A7) | A torn last line was still read whole on the append that also trims the file (past 1 MiB). | low | `trim` marks it too |
| tests (A8) | No test pinned the "startup path still pending" wait. | test gap | `TestReadyWaitsForTouchedStartupPath` |
| restore (B1) | A mount covered by a later one (`/srv/data`, then `/srv` on top) counted as mounted: the write landed on `/srv`'s filesystem. | low | the mount table is walked from the root's mount down (parent ids) |
| restore (B2) | Under nested mount points only the deepest was named; `mount /srv/data` fails with `/srv` not mounted. | low | every one below the deepest a path reaches, outermost first, in the refusal and the undo |
| restore (B3) | A symlink as an fstab mount point was matched as text: refusal missed, or a false one. | low | fstab targets resolved as mount(8) does |
| console (B4) | The console's row for a checked file now a symlink dropped "error". | low | "error: now a symlink" |
| status (B5) | With the last healthy boot's row unknown but an earlier one's known, the list and the undo were since the earlier boot but named "the last healthy boot". | low, pre-existing | they name "the healthy boot of <date>" |
| restore (B6) | A bad fstab line above `/etc` (`UUID=typo /etc`) blocked the undo of fstab itself. | low | fstab is never refused |
| restore (B7) | A file that is itself an fstab mount point (a bind mount) got `mount` advice that ends in EBUSY. | low (reasoned) | refused with its own message; the undo says to fix the file mounted on it |
| titles (B8) | The row -1 table title was 87 columns; an unreadable boots file said "no healthy boot is recorded" under its own error. | cleanup | 66 columns; its own title |
| tests (B9) | No test reached SQLite's CANTOPEN for a WAL store, a fstab target with a trailing slash, a quoted mount point, an unchecked file turned symlink, or an unwritable `$SC_HOME/tmp`. | test gap | a test for each |
| docs | The README said scd is started once the rescan is recorded (held paths now count) and named one `mount`; MILESTONES said "any unmounted mount point". | cleanup | docs |

## Tests

New: `TestReadyWhileHeld` (floor; store, as a user), `TestReadyWaitsForTouchedStartupPath`,
the trim case in `TestTrim`, the late-header case in `TestConsoleStatusHeaderFirst`,
`TestUnmountedMounts` (nested, covered, stacked, inner alone, a trailing slash),
`TestUnmountedMountsLinkAndSelf`, `TestRestoreFileMountPoint`, nested and file cases in
`TestStatusUndoMounts`, a quoted mount point in `TestRestoreUnmountedMount`,
`TestStatusFileNowLinkConsole`, `TestStatusHealthyRowUnknownAfterKnown`,
`TestStatusBootsUnreadable`, a CANTOPEN case in `TestReadOnlyWALCLI`, two cases in
`TestCheckCLIScratchFallback`; in the lab, three split-report orders and the wait in
`test_25_the_getty_where_it_likes`. Of 22 fixes undone one at a time, all 22 made a test
fail (one only after its test was corrected: it built its fstab line from the wrong
fake). Not run: `accept-m2.sh`'s steps 2, 3 and 18 (root), by reasoning only; A's
user-manager runs back their systemd behaviour. gofmt, vet, `make lab-test`,
`go test ./...` as a user and as root, `make race` (uncached) and `make m1-compat` pass.
