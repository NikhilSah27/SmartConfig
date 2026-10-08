# Review of chunk H: M4's last follow-ups (the boot units, GRUB, the lab)

Date: 2026-10-07, after chunk H was built (`7d20491..100da93`: `GRUB_TOP_LEVEL` in the
rescue entry, the boot units' hardening and no `SC_GRUB_BOOT`, the final review's test
gaps, lab check 1.8, the lab's chunk D items, the README's GRUB password recipe in the
lab (`--grub-password`, checks 6.1-6.5), and the `cmd/sc` flake under `nohup`). Two
reviewers (AI agents), each in its own clone of `100da93` (pushing disabled): A on
`scripts/42_smartconfig`, the two boot units and the Go tests; B on the lab. No sudo,
nothing written to `/etc`, `/boot` or `/var/lib`, no QEMU; both read the evidence of the
`--grub-password` lab run on `818dace`, which passed in both firmware modes. Claude reran
the probes each reviewer wrote before fixing. Reports:
`~/smartconfig-work/review-m4fu-h/findings-{a,b}.md`.

**Gate: no high-severity finding open.** Neither reviewer found a high one, nor a case
where a sign-off run passes when it should not. All medium and low findings, the test
gaps and the docs are fixed in `a1c1228`. Nothing goes on a follow-up list.

## What held up

- `GRUB_TOP_LEVEL` as `10_linux` takes it (exported by grub-mkconfig, GRUB 2.12's
  `grub_move_to_front`, a subshell); `boot=/boot` leaves no variable from root's
  environment.
- The sandbox, functionally: neither `sc boot seen` nor `sc boot verdict` uses `/home`,
  `/root`, `/run/user`, a set-user-ID file or an abstract socket; the options add no
  implicit dependency; `ProtectHome=` ignores missing paths and triggers no automount
  (systemd v255 `chase()`); the lab's broken boot ran `sc-boot-seen` with them.
- Check 1.8 now fails on a target edge and on an unread order; `facts.sh`'s blocks carry
  the command's status; only the two read-only calls retry, each with its own budget;
  the mark's drain (the connection read to its end before `_ser` is `None`).
- The recipe as the README gives it (it changes exactly `10_linux`'s `gnulinux-simple`
  line and appends three lines to `40_custom`); UEFI typing (GRUB echoes the user, not
  the password); BIOS typing (the `vga-enter-6b` screens show it typed one key at a
  time); a PASS with `grubpw=yes` has every 6.x passed.
- The `nohup` fix (`TestMain`, the lab's `signals()`).

## Findings

| Area | Finding | Severity | Fixed by |
|---|---|---|---|
| lab 6.2, 6.5 (B1) | GRUB stopped at "Enter username:" in a boot nobody types in made the kernel wait run out after 600 s: a [lab] error, never the recipe's [M4] failure; the "GRUB asked" checks were dead code. | medium | the wait for the kernel takes GRUB's prompt too (serial, or the VGA screens between short waits): 6.2 or 6.5 fails (H) |
| lab 6a (B2) | Nothing waited for boot 6a's verdict before the flag was set: a late `sc-boot-ok` would unset it, and 6.3 fail [M4] on a healthy guest. | medium | 6.2 waits for 6a's ok line (as boot 5) |
| lab 6b, 6c (B3) | A retried 6c (or 6b) expected a menu whatever the flag: a known flake (no-ssh) after an ok verdict became 6.5 FAIL [M4]. 6a and 6c had no healthy-end wait. | medium | `menu=a.flag`; the flag not known: a LabError; `wait_healthy_end` in 6a and 6c |
| lab 6.4 (B4) | Its waits ignored the gap and `/init` rule: a stall or a host pause failed 6.4 [M4]. A non-TCG panic in 6b was a LabError. | medium | `ran_out("6.4", ...)`; a panic in 6b fails the check, as in 3 |
| lab BIOS 6.3 (B5) | A dead VGA watch, QEMU gone or a reset polled to the end: 6.3 FAIL [M4]. | low | a LabError |
| lab 1.8 (B6) | The docstring claimed more than it checks (multi-user waits for seen through grub-common, by design). | low | says what it checks |
| facts.sh (B7) | A failing command's stdout was dropped. | low | the output kept, the status returned |
| tests (B8) | 11 lab mutations survived (the retry's gap condition, which calls retry, `--no-boot5` and 6.x, the tag, the flag, BIOS keys and screens, 6.4's `fstab=no`, the asked checks, the mux's notify); `p_consoles` and `p_vcs` reverted passed. | low | a test for each; the console and VGA parts read through the test's root |
| unit tests (A3, A4, B9) | The ordering guards were a list of what is forbidden: `After=sockets.target`, `After = sysinit.target` (systemd trims the spaces), a dropped `Conflicts=shutdown.target`, and `ProtectSystem=` (it makes `/boot` read-only: no menu flag, the unit still green) all passed. | low | `TestBootUnitsExact`: each unit's lines, as systemd reads them, exactly |
| docs (A1) | The unit's comment named systemd-resolved and -timesyncd as early namespaces: theirs come after local-fs (`PrivateTmp=` orders them after systemd-tmpfiles-setup). | low | udevd's `PrivateMounts=` and the lab run |
| docs (A2) | `sc-boot-ok` was said to need the network for systemctl's socket; `PrivateNetwork=` keeps file-system sockets. | low | `PrivateNetwork=yes` on ok too |
| 42_smartconfig (A5) | A `.old` kernel `GRUB_TOP_LEVEL` names was passed over (10_linux boots it with its counterpart's initrd), with a false word. | low | `10_linux`'s `.old` sort and initrd names; a kernel at the root of `/` stays out (the entry reads `/boot`'s device), said in the comment |
| 42_smartconfig (A6) | The `GRUB_TOP_LEVEL` word came before "no entry for a btrfs root". | cleanup | printed with "Adding SmartConfig rescue entry" |
| docs (B10) | The lab README: "Nothing else is ever retried", "six boots"; `--no-boot5 --grub-password` skipped 6.x silently; make's last line had no `grubpw`. | cleanup | the README; the two flags together refused; `grubpw=yes` on make's last line |

## Tests

New or extended: `TestBootUnitsExact`, `TestGrubScriptTopLevel` (a `.old` kernel, its
sort, a btrfs root), `TestLabGuestFacts` (partial output, the consoles, vcs1), and in the
lab `TestGrubPassword` (a prompt in 6a and 6c, 6a's verdict, a retried 6b/6c, 6.4's
waits, a panic in 6b, BIOS prompts and keys, the flag, `--no-boot5`, the tag), the ssh
retry with no pause and its callers, the no-boot5 skip of 6.x, and
`test_serial_gone_ends_a_mark_already_waiting`. Of 28 fixes undone one at a time (18 in
the lab, 7 in the GRUB script and units, 3 in `facts.sh`), all 28 make a test fail.
Full checks, `make lab-test`, and the root and race passes pass. The fixes to the stage
itself are proven by the sign-off's `make lab-e2e LAB_E2E_ARGS=--grub-password`.
