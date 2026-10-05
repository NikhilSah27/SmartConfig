# lab: the QEMU rescue lab (`make lab-e2e`)

The M4 owner scenario, end to end, in a throwaway Ubuntu 24.04 VM. It
installs this checkout's `bin/sc`, units, drop-in and `42_smartconfig`,
breaks `/etc/fstab` the way an owner would, and checks that GRUB's menu
comes back by itself, that "SmartConfig rescue" gives a root shell with
sc's report above the prompt, and that the report's own commands fix the
machine. Plan: `docs/M4_PLAN.md` (step 11, sign-off S1); the check IDs
below are the step 11 design's (docs/WORKLOG.md links it).

Dev only: Python 3 standard library and POSIX sh, never shipped, not in
CI. No sudo, no KVM, no host networking changes: QEMU runs under TCG as
you, with user networking and ssh forwarded from 127.0.0.1 only. Nothing
outside the lab cache and `bin/` is touched.

## What one run proves

`lab/e2e.py --mode uefi|bios` (OVMF with a serial console, or SeaBIOS
with the VGA text screen) runs six boots on one overlay disk:

| Boot | What happens | Checks |
|---|---|---|
| 0 install | ssh in, copy the files, `install.sh`, `update-grub` | 0.1-0.7 |
| 1 normal | no menu; `sc-boot-seen` and `sc-boot-ok` record an ok boot; then the edit: a line for a missing disk goes into `/etc/fstab`, scd records it as BAD | 1.0-1.9, E.1-E.6 |
| 2 broken | nothing is typed; the disk times out, local-fs fails, emergency mode (outcome a, b or c); a hard reset | 2.1-2.7 |
| 3 rescue | the menu shows by itself; "SmartConfig rescue" is picked; the report matches the golden Go renders; Enter gives `#`; the report's commands restore fstab and reboot | 3.0-3.9 |
| 4 healthy | the menu once more, left to time out; an ok verdict clears it | 4.1-4.6 |
| 5 normal | no menu again; poweroff (`--no-boot5` leaves it out) | 5.1-5.2 |

Every check has a class and a strength. **[M4]** checks SmartConfig;
**[lab]** checks the lab itself (tools, QEMU, the VGA channel, keys). **H**
stops the mode, **F** fails it and goes on, **W** only warns. A known lab
flake is retried, the whole boot after a reset, at most `RETRIES_MODE`
times a mode: a TCG panic, a healthy boot that TCG starved into emergency
mode (slow udev on `LABEL=BOOT`, `LABEL=UEFI` or ttyS0), a missed menu or a
`grub>` prompt, on bios a gap of over 2 s in the VGA polling before the
menu's first screen (its countdown cannot be held to 30 s then), a login
prompt without ssh. Nothing else is ever retried. A reboot over ssh that
ssh lost (exit 255) is sent once more only when the guest answers from the
same boot with no reboot queued; otherwise its RESET decides. A guest RESET within 2 s
of the one before it (on bios every guest reboot gives two, about 15 ms
apart: the firmware resets once more) is the same boot's start, not a
reset of its own; `e2e.log` and `ledger.json` (`chained_resets`) list each.
On bios the VGA screen is the only evidence of what GRUB did: a failed
poll, or screens with neither an observer line nor a menu, is a [lab]
failure, never a pass.

Verdicts: **PASS** (exit 0), **FAIL** (1: an [M4] check failed),
**INCONCLUSIVE** (3: a [lab] check failed, the retries or the time ran
out). `make lab-e2e` runs both modes, echoes each mode's summary line, and
ends with `lab-e2e: PASS`, `FAIL` (a mode FAILed) or `INCONCLUSIVE`; make
shows the last two as `Error 1` and `Error 3` and exits 2. Without boot 5
(`--no-boot5`) the summary lines and make's last line say `boot5=no`: such
a PASS is not a whole one. A run on a dirty tree says `dirty=yes` and does
not count for sign-off; `LAB_REQUIRE_CLEAN=1` refuses one. Preflight (P.2)
rebuilds the tree with the Makefile's recipe into the run directory and
refuses a `bin/sc` that is not byte for byte that build (`make build`;
`make lab-e2e` builds first). Go stamps the commit into `sc`, so do not
commit while `make lab-e2e` runs: the next mode's P.2 refuses the build. Sign-off also needs at least one run with
`goal3=multi-user` (boot 2 outcome b or c).

Boot 2's outcome is b or c only once ssh answers with `multi-user.target`
active (then `sc boot verdict` gives B2 a bad line), else a. ssh can come
up without that: when the console getty hangs up the emergency shell,
systemd starts `default.target` again, waits for the device once more and
drops back into emergency mode (plan A2). The report can be lost in that
race (plan A7), so 2.5 is a W wherever the serial text shows the shell
returning, as in c. The hang-up can also end the shell before its first
line: the console then has the report, the getty's login prompt and no
"You are in emergency mode". 2.5 then holds the block above the login
prompt to the golden, as strictly, and notes it. `sc status --console`
itself must outlive the hang-up: `sc: interrupted by hangup` (as in two
runs before it did) fails 2.5.

## Prerequisites

- `qemu-system-x86`, `qemu-utils`, `ovmf`, `openssh-client`, `curl`, Go
- about 3 GB free in the cache, 2 GB RAM for the guest
- no other VM of yours running (the lab refuses to start a second one)

## Commands

```sh
make lab-image                 # once: the pinned image, then the reference image (~30 min)
make lab-image LAB_IMAGE_FROM=~/smartconfig-work/m4lab/noble-server-cloudimg-amd64.img
make lab-e2e                   # both modes, ~25 min each under TCG
make lab-e2e LAB_MODES=uefi LAB_E2E_ARGS=--no-boot5     # while iterating
make lab-test                  # the unit tests and sh -n (no QEMU, seconds)
make lab-clean                 # old runs, stale reference images

python3 lab/e2e.py --mode bios --boot2 reset-at-timeout --keep
python3 lab/vm.py up uefi      # a throwaway VM to poke at by hand:
python3 lab/vm.py ssh          #   its shell (as owner, with sudo)
python3 lab/vm.py console      #   its serial console (Ctrl-] leaves)
python3 lab/vm.py stop
```

`--boot2 reset-at-timeout` resets boot 2 right at the device timeout,
which forces outcome a. `--keep` keeps the run's disk after a PASS too.

## Cost

A mode takes about 25 minutes on this host (75 at most: `BUDGET_MODE`),
two CPUs at nice 10. Under TCG this host has stalled for over ten minutes
at a time; the budgets in `lab.conf` are wall clock and generous for that.

## The cache

`${SC_LAB_CACHE:-${XDG_CACHE_HOME:-~/.cache}/smartconfig-lab}`, mode 0700:

```
lock, key, key.pub               the flock; the lab's ssh key (no passphrase)
images/<release>-<name>.img      the pinned cloud image (0444, sha256 = lab.conf)
ref-<h12>.qcow2 .VARS.fd .json   the reference image (0444); <h12> hashes the
                                 image, cloud-init/*.in, guest/60-sclab.cfg,
                                 the key and PROVISION_REV
runs/<UTC>-<mode>-<git7>/        one e2e run (also -provision-<h12>, -up-<mode>-<git7>)
```

A run directory holds:

| File | What |
|---|---|
| `result.txt` | a row per check: ID, status, class, strength, channel, evidence, expected, from, seen, notes |
| `ledger.json` | the run's values (GOOD, BAD, PRE, B1-B5, N, K, R1, R4, the outcome), every boot attempt, every check result in order |
| `e2e.log` | the progress lines |
| `serial.raw`, `serial.txt`, `serial.ts` | the serial console: raw, cleaned, with `[+seconds]` per line |
| `input.log`, `marks.log` | every byte and key sent; where each boot starts (QMP RESET) |
| `qmp.log`, `qemu.log`, `mux.log`, `ssh.log` | QMP traffic, QEMU's command line and output, the serial mux, every ssh call |
| `evidence/` | facts.sh output per boot, install.sh output, grub.cfg, the reports, VGA dumps (bios), the menu PNG |
| `stage/` | exactly what was copied to the guest, with `MANIFEST` |
| `disk.qcow2`, `VARS.fd` | the overlay; deleted after a PASS unless `--keep` |

## Reading a failed run

The last lines say which mode, the verdict and the run directory, then the
failing rows, the last 40 serial lines and `facts.sh dump` from the guest
(over ssh, or the rescue shell). Then:

1. `result.txt`: the first FAIL row. Its EVIDENCE column names the file
   and line range (`serial.txt:812-830`, `evidence/facts-boot4.txt:40-52`);
   NOTES says what differed from EXPECTED. A class shown as `M4(lab)` is
   an [M4] check that the lab could not finish (QEMU died, time ran out).
2. `serial.ts` around those lines, for the timing.
3. `ledger.json` `attempts`: which boots were retried and why.
4. INCONCLUSIVE with no FAIL row: `e2e.log` has the lab error.

A FAIL is a SmartConfig bug until shown otherwise: it gets its own product
commit and a test, never a lab change that makes it pass.

## Files

| File | |
|---|---|
| `lab.conf` | pins (image, kernel, OVMF), machine size, budgets, retries |
| `vm.py` | cache, image, key, provisioning, overlays, QEMU and QMP, the lock, ssh, gc; the debug CLI |
| `serialmux.py` | the one client of QEMU's serial socket: logs, cleaned text, input log, marks |
| `console.py` | expect, the shell sentinel, the GRUB menu parser and closed-loop pick, VGA text, the boot classifier, golden matching |
| `e2e.py` | the scenario: check registry, retries, ledger, verdicts, result.txt, teardown |
| `cloud-init/` | the provisioning recipe (user `owner` with a key, root locked, no passwords) |
| `guest/` | `60-sclab.cfg` (baked into the reference image), the GRUB observers `41_sclab`/`43_sclab` (echo only), `install.sh`, `facts.sh` (read-only) |
| `testdata/` | short excerpts of real console captures; `console-*.golden` from `go test ./cmd/sc -run TestStatusConsoleLab -update` |
| `test_lab.py`, `test_e2e.py` | unit tests; `go test ./cmd/sc` runs them too (TestLabPython) |
