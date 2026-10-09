# Review of chunk I: the M5 follow-ups' store items

Date: 2026-10-09, after M5 follow-ups 1 and 2 were built (`8e59efc..cef26b5`):
- `sc status` says when the store is under scd's free-space floor;
- `store.Prune`, and the writers putting their blob again under the lock;
- `sc prune`.

**How it was reviewed:**
- **The reviewer:** one AI agent, in its own clone of `cef26b5` (pushing disabled),
  with its own `sc` build and throwaway stores.
- **The limits:** no sudo, nothing written to `/etc` or the real store, no QEMU.
- **Claude's checks:**
  - reran the reviewer's 7 failing tests on `cef26b5`: all 7 fail;
  - ran them again on the fixes, unchanged: 6 pass;
  - the 7th replays a writer of an earlier release, which this release cannot change:
    `sc prune` refuses in that case instead (I4).
- **Report:** `~/smartconfig-work/review-m5fu-i/findings.md`; tests and scripts in its
  `clone/` and `scratch/`.

**Gate: no high-severity finding open.** The reviewer found two highs, both data loss.
- I1: a version that was current until an hour ago was deleted.
- I2: `sc status` and the rescue report lost their undo.

Both are fixed, as are every medium and low finding and the test gaps. Nothing goes on a
follow-up list.

## What held up

- **Rowids.** The newest row of a path is never deleted, so the highest rowid survives
  and SQLite never reuses one. Rowid order, `NewestRowID`, `Rows(after, n)` and the
  boots file's rowids keep their meaning, and there is no VACUUM.
- **The boot-line rule** ("each path's newest row at or before each line") is exact: a
  line of -1 and duplicate lines are harmless.
- **The re-put protocol.**
  - Every insert of a file row puts its blob again under the lock: Record, and
    commitWrite for restore and sc edit.
  - 300 Records against about 130k concurrent prunes left no row without its blob, on
    `cef26b5` and again on the fixes (384 prunes).
- **Origins.** Manual, pre-restore, restore and edit rows are never selected.
- **Crash safety.**
  - All rows commit before any blob is touched.
  - A kill while the rows are deleted rolls back.
  - A kill while the blobs are deleted leaves only unused blobs, which the next run
    removes.
  - The dry run leaves the store as it was.
- **Read-only stores** are refused before anything is written: a store this user may not
  write, a read-only root, a repaired copy.
- **The CLI.** It refuses bad `--older-than` values (overflow included), a missing
  value and extra arguments. End of input means no.
- **The status line.** It shows below the floor and not at it; not when statfs fails;
  not on the rescue console. The exit status is unchanged, and the lab's console goldens
  are unaffected.

## Findings

| Area | Finding | Severity | Fixed |
|---|---|---|---|
| the rule (I1) | A version recorded over 90 days ago but current until an hour ago was deleted, with its blob: the undo of the newest change. Also lost: sc edit's "before" id (scd's row) and scd's checker's version before. | **high** | A version goes only once a newer row replaced it before the cutoff (`next.ts < before`, `prev.ts < before` kept for a clock that steps back). |
| `sc status` (I2) | Prune protected only the version at each boot line. It also deleted a path's first row: the "did not exist" proof of a new file (b), and the first-seen row of a file that later came back, which then got called new and "moved aside", the M4 final review's B2 again (c). With no healthy boot, `sc status` compares with rows prune deleted (a). | **high** | Each path's first row is always kept. `sc prune` refuses until an ok verdict with a row is recorded, which is when `sc status` is bounded. |
| snapshot (I3) | `sc snapshot` of a file scd had recorded inserted nothing ("unchanged since <scd's id>"), so the user's snapshot was an automatic row, and was pruned. | medium | `Obs.Explicit`: an explicit snapshot equal to an automatic row is a manual row of its own. The reviewer's fix applied to every manual observation; that also gave sc edit a duplicate "before" row, against M3's rule that an edit gives exactly its rows (`TestReplaceWhileWatching`), so it is limited to `sc snapshot`. |
| older writers (I4) | BlobGrace covers only a blob a pre-follow-up writer has just written. A file going back to old content can lose its blob to prune between such a writer's put and its insert. | low | `sc prune` refuses while scd runs an `sc` an upgrade replaced (`/proc/<pid>/exe` ends " (deleted)"): restart scd first. The store-level replay still fails, by design. |
| big stores (I5) | The dry run ran the real DELETE under the write lock; the real run held it for the deletes and the whole blob walk. At 600k rows, scd and readers failed with SQLITE_BUSY after 5.7 s. | medium | The plan is read in pages of 5000 with no lock. Rows are deleted in batches of 1000, each its own transaction. Blobs go in locked batches of 500, each first adding the blobs of rows newer than the plan. At 600k rows (the reviewer's probe, `SC_REVIEW_BIG=600000`): the dry run took 17 s with no lock, and a reader meanwhile waited 15 ms. The real run took 1 min 40 s, and a writer meanwhile waited 156 ms (was SQLITE_BUSY after 5.7 s). |
| Ctrl-C (I6) | `mutating` was set before the prompt: the first Ctrl-C was swallowed, and a later "y" still deleted. | medium | `mutating` is set only for the real run. Prune checks `stopping()` between batches, before any blob if rows were left, and says what it did. Rerun of the reviewer's script: sc ends at the Ctrl-C, nothing deleted. |
| errors (I7) | A blob error after the rows were deleted hid that they were; one blob that could not be removed blocked the rest. | low | Such a blob is skipped and counted. The CLI says what was deleted, then what could not be removed, and why. |
| output (I8) | The status line was 83 columns, "nothing to prune" 103. "scd holds back" was printed with no scd running. scd logged MB where status said MiB. | low | 76 and 71 columns, worded for either case; two lines; scd logs MiB. |
| boots lines (I9) | The boots file is read once, before the prompt. | low (reasoned) | No change needed. A row goes only if the row after it is older than the cutoff. A verdict recorded after the plan has a rowid at or after that row, so it could not keep the deleted one anyway. |
| cleanup (I10) | Comments said rows are never deleted. A killed writer's temp files in `objects/` were never reclaimed. Small totals printed "0.0 MiB". | cleanup | Comments fixed. Temp files older than BlobGrace go. Sizes print as bytes, KiB or MiB. |
| tests (I11) | Mutations survived: `isBlobName` (dead in the tests), sc edit's put under the lock, the "only unused blobs" output, a row at the cutoff. | cleanup | New tests: pages, clock stepped back, blobs (junk names, temp files, a blob that cannot be removed), the Replace race, a row recorded during the blob step, interrupts, every refusal, the reviewer's three `sc status` cases. 18 mutations after the fixes: 17 killed; the one survivor ("rows newer than the plan read anyway") is equivalent, as it only keeps more. |
