# SmartConfig docs

Visual explainers for the project. Each page is a single self-contained HTML
file in `visuals/`: open it in a browser straight from disk. They need an
internet connection only for the Google Fonts; everything else is inline.

The same pages are also published as private artifacts on claude.ai (links
below). The published copy and the file here are the same page; if they ever
differ, the published one is newer.

| Page | What it shows | Real or concept |
|------|---------------|-----------------|
| [visuals/system-map.html](visuals/system-map.html) · [online](https://claude.ai/artifact/WcpR823C8pcvG2WmJEMsck) | Roadmap, how M1 was built commit by commit, the full architecture with playable scenarios, step-by-step snapshot and restore flows, and a working in-browser copy of `sc` | M1 real; later milestones marked as planned |
| [visuals/film-fstab-restore.html](visuals/film-fstab-restore.html) · [online](https://claude.ai/artifact/BXPUipny6hAzdM5QNrTZu8) | Narrated film: open the laptop, snapshot `/etc/fstab`, a one-character typo, diff, restore, with an X-ray of the system calls, files and database rows | Real run on this VM (2026-09-27 08:26), syscalls from `strace` |
| [visuals/films-journey-after-without.html](visuals/films-journey-after-without.html) · [online](https://claude.ai/artifact/XgbShorWnZbd8zk1qbvHS7) | Three narrated films: the whole journey M1 to M7; the finished product rescuing a broken boot; the same typo without SmartConfig | M1 real; M2 to M7 are mock-ups of the design; boot failure checked with systemd's fstab generator |

## What is real in the films

- Every `sc` command, id, hash and output line in the M1 scenes comes from real
  runs on the development VM.
- The boot failure in the "after it's built" and "without SmartConfig" films
  uses a typo in a *data disk* line (`/data`). Running
  `systemd-fstab-generator` against that file on the VM shows `data.mount`
  requiring the missing device, so boot waits 90 s and stops in emergency mode.
  The root account is locked (`passwd -S root` shows `L`), as on every default
  Ubuntu install, so emergency mode cannot open a shell.
- A typo in the *root* line (`/`) is different: the root filesystem is already
  mounted by the initramfs, and the generated `-.mount` does not require the
  device, so Ubuntu most likely still boots.
- Anything tagged "concept" shows milestones 2 to 7, which are not built yet.
  Their commands and messages are the current design and may change.
- Downtime numbers in "without SmartConfig" are illustrative.

## Voice and sound

The films use the browser's built-in speech (`speechSynthesis`) for narration
and generate sound effects with the Web Audio API. Voices differ between
browsers; Chrome and Edge usually have the most natural ones. Both can be
switched off in the player.
