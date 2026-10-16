# Review of chunk L: the M5 follow-ups' apt tags

Date: 2026-10-16, after M5 follow-ups 8 and 9 were built (`7fe207b..966e322`):
- the package's apt hook marks each dpkg run, and scd tags the rows it records meanwhile with
  origin `apt` and the run (`ea50fad`);
- the lab's `--deb` mode upgrades through apt and checks those rows (`9a40d4e`, and the CI fix
  `c8b1191`).

**How it was reviewed:**
- **The reviewer:** one AI agent, in its own clone of `966e322` (pushing disabled).
- **The limits:** no sudo, no package installed or removed, this machine's package database
  never touched, no QEMU. It ran the real apt-get 2.8.3 in a sandbox of its own (its own
  `APT_CONFIG`, `Dir` and parts, a fake dpkg), a probe copy of `sc` whose mark path it could
  set, and the M5 review's fakeroot `dpkg --root` simulation.
- **Claude's checks:**
  - reran the reviewer's 5 failing tests on `966e322`: all 5 fail
    (`~/smartconfig-work/review-m5fu-l/scratch/verify-main-session.log`);
  - every fix has a test that fails without it; the review's own tests pass after the fixes;
  - mutations: the author's 21 for step 8 (20 killed; the survivor needs a full check queue),
    the review's 24 (11 survived: each now has a test), and the watcher fix's 18 (17 killed;
    the survivor changes only the cost).
- **The fixes:** fix 1 (L1, L3, L5, L6) was built in this session; fix 2 (L2, L4) by an agent
  in its own clone of `966e322`, at the owner's earlier ask, read, applied and checked here.
- **Report:** `~/smartconfig-work/review-m5fu-l/findings.md`; its tests, sandbox and scripts in
  `clone/` and `scratch/`.

**Gate: one high-severity finding, L1, fixed.** One was medium (L2). All 6 are fixed. None
asked for a decision.

## What held up

- **The hook lines** parse (`apt-config -c`) and join the system's other `DPkg::` hooks; the
  guard keeps apt going when `sc` is missing or fails; a package removed but not purged is a
  no-op.
- **`sc apt`** takes about 10 ms and never opens the store or waits.
- **The mark:** `/proc/PID/stat` parsing (a name with ")" or blanks), pid reuse caught by the
  start time, `/run/smartconfig` root-only and in tmpfs, an atomic write: a user cannot plant
  a mark.
- **The package:** conffiles and md5sums right; `dpkg --verify` clean from 0.5.0 to this build;
  the new conffile arrives with no question and goes on purge.
- **The lab's D.1:** the repacked package is valid; dpkg installs its changed `42_smartconfig`
  with no question; apt-get of a local file needs no network.
- **`store.Watched`:** every place that meant "a row scd wrote" is right for apt rows; the boot
  verdict, `sc status` and the rescue report read no origins; 0.5.0 and sc-m1 read apt rows as
  file rows.
- **`--show-secrets`** in the fstab sha checks: every `sc` that runs them is this tree's, as
  root.

## Findings

| Area | Finding | Severity | Fixed |
|---|---|---|---|
| which apt (L1) | apt runs its hook lines from a fork of itself (RunScripts: fork, `system()` each line, `_exit`), gone before dpkg runs. `sc apt begin` named that fork, so the mark was always stale and no row was ever tagged; the lab's D.1 could not pass. The tests modelled no fork, and one took its expected pid from `Runner` itself. | high | `Runner` takes the parent when the process above the shells has its parent's name, one step only; a test runs the hook line from a real fork that `system()`s it (`51c8659`). |
| when (L2) | The tag was read when scd recorded a change, after its quiet time: dpkg's last writes before `sc apt end` were `auto`, an edit saved just before `sc apt begin` was `apt`. | medium | The run is read at each event and kept on the path's entry; the latest event wins (`6022f33`). |
| a silent hook (L3) | A downgrade to 0.5.0 keeps the conffile; 0.5.0's `sc` printed "unknown command" into every apt run, twice. | low | The hook lines send `sc`'s output to `/dev/null` (`51c8659`). |
| rescans (L4) | A rescan during a run (scd restarted by the package's own postinst) tagged edits made while scd was down, however old, as the run's. | low | Only if the file changed after the run began (`6022f33`). |
| tests (L5) | 11 of 24 mutations survived: the mark's modes and begin time, `begin` with no apt, a created file's rows, and D.1's commands (the lab's fakes keyed on the same constants). | low | Tests for each (`51c8659`). |
| docs (L6) | The README said every dpkg run is tagged; a bare `dpkg -i` runs no apt hook. MILESTONES named sc-m2, which nothing tests. | low | Fixed (`51c8659`, and the write-up's commit). |

## Left as it is

- A rescan entry held back a long time (the free-space floor, a store error) keeps the run it
  was marked in; a change found with no event after that run ended could still be tagged apt.
  The end of a run is not recorded.
- apt runs Post-Invoke even when dpkg fails; only a failing Pre-Install-Pkgs hook skips it,
  and the mark then lives until apt exits, which is still the run.
- PackageKit's process model was not checked; the fork rule takes one step only, so at worst a
  PackageKit run is not tagged.
