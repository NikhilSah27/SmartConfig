# SmartConfig docs

What we are doing right now and what is done: [WORKLOG.md](WORKLOG.md).
Where each milestone stands, what comes next, and the open work no
milestone owns yet: [../MILESTONES.md](../MILESTONES.md).
The plans, each with the changes made while it was built:
[M2](M2_PLAN.md) (Appendix C), [M3](M3_PLAN.md) (section 15),
[M4](M4_PLAN.md), [M5](M5_PLAN.md) (section 9). Reviews: [reviews/](reviews/).
How we got here, the path we chose and why: [JOURNEY.md](JOURNEY.md) (M1
and M2 in full, M3 to M5 in brief).
The roadmap written at M1 for M2, kept as it was then: [NEXT_STEPS.md](NEXT_STEPS.md).
A progress dashboard as of 28 Sep 2026 (M1 done): [visuals/progress.html](visuals/progress.html).

For the history of the project, its decisions, and a recovery guide, see
[PROJECT_LOG.md](PROJECT_LOG.md).
For the milestone 2 watcher research (what to watch and what not), see
[M2_WATCHLIST.md](M2_WATCHLIST.md).

Visual explainers for the project. Each page is a single self-contained HTML
file in `visuals/`: open it in a browser straight from disk. They need an
internet connection only for the Google Fonts; everything else is inline.

The same pages are also published as private artifacts on claude.ai (links
below), as they were when each was made. Since 2026-10-09 the files here
also carry a status note at the top saying what has been built since; the
published copies do not.

Each page shows the project as it was when the page was made: all four at
M1, on 27 and 28 Sep 2026. Milestones 2 to 5 are built since; see
[../MILESTONES.md](../MILESTONES.md) for where things stand now.

| Page | What it shows | Real or concept |
|------|---------------|-----------------|
| [visuals/progress.html](visuals/progress.html) · [online](https://claude.ai/artifact/2Paa7FUJfrYTZAq1hYFPRP) | Progress dashboard: how the code and tests grew commit by commit, the four review rounds and their findings, what `sc` does today, the VM's state, and what is waiting before M2 | Real as of 28 Sep 2026 (M1): numbers from git, the saved review results and the VM, checked by an independent fact check |
| [visuals/system-map.html](visuals/system-map.html) · [online](https://claude.ai/artifact/WcpR823C8pcvG2WmJEMsck) | Roadmap, how M1 was built commit by commit, the full architecture with playable scenarios, step-by-step snapshot and restore flows, and a working in-browser copy of `sc` | M1 real; later milestones as designed at M1 (M2 to M5 are built since) |
| [visuals/film-fstab-restore.html](visuals/film-fstab-restore.html) · [online](https://claude.ai/artifact/BXPUipny6hAzdM5QNrTZu8) | Narrated film: open the laptop, snapshot `/etc/fstab`, a one-character typo, diff, restore, with an X-ray of the system calls, files and database rows | Real run on this VM (2026-09-27 08:26), syscalls from `strace` |
| [visuals/films-journey-after-without.html](visuals/films-journey-after-without.html) · [online](https://claude.ai/artifact/XgbShorWnZbd8zk1qbvHS7) | Three narrated films: the whole journey M1 to M7; the finished product rescuing a broken boot; the same typo without SmartConfig | M1 real; M2 to M7 are mock-ups of the design; boot failure checked with systemd's fstab generator |

## What is real in the films

- Every `sc` command, id, hash and output line in the M1 scenes comes from real
  runs on the development VM.
- The boot failure in the "after it's built" and "without SmartConfig" films
  uses a typo in a *data disk* line (`/data`). Running
  `systemd-fstab-generator` against that file on the VM shows `data.mount`
  requiring the missing device, so boot waits 90 s and stops in emergency mode
  (a desktop comes up to its login screen instead, the mount missing: M4's
  sign-off S3).
  The root account is locked (`passwd -S root` shows `L`), as on every default
  Ubuntu install. Ubuntu 24.04's sulogin still opens a root shell in
  emergency mode at the console (its `sulogin-lockedpwd.patch`, shown in the
  M4 lab), but over SSH the machine may be gone, and nothing says what changed.
- A typo in the *root* line (`/`) is different: the root filesystem is already
  mounted by the initramfs, and the generated `-.mount` does not require the
  device, so Ubuntu most likely still boots.
- Anything tagged "concept" shows milestones 2 to 7, which were not built when
  the films were made (2 to 5 are built since). Their commands and messages
  were the design then and differ in places from what was built (the
  README shows the real ones).
- Downtime numbers in "without SmartConfig" are illustrative.

## Voice and sound

The films use the browser's built-in speech (`speechSynthesis`) for narration
and generate sound effects with the Web Audio API. Voices differ between
browsers; Chrome and Edge usually have the most natural ones. Both can be
switched off in the player.
