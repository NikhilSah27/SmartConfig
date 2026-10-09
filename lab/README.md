# lab: the QEMU rescue lab (`make lab-e2e`)

The M4 owner scenario, end to end, in a throwaway Ubuntu 24.04 VM. It
installs this checkout's `bin/sc`, units, drop-in and `42_smartconfig`,
breaks `/etc/fstab` the way an owner would, and checks that GRUB's menu
comes back by itself, that "SmartConfig rescue" gives a root shell with
sc's report above the prompt, and that the report's own commands fix the
machine. Plan: `docs/M4_PLAN.md` (step 11, sign-off S1); the check IDs
below are the step 11 design's (docs/WORKLOG.md links it).

Dev only: Python 3 standard library and POSIX sh, never shipped. The VM
runs are not in CI; the unit tests are, through `go test` (`TestLabPython`
in `cmd/sc`). No sudo, no KVM, no host networking changes: QEMU runs under TCG as
you, with user networking and ssh forwarded from 127.0.0.1 only. Nothing
outside the lab cache and `bin/` is touched, but for one empty lock file,
`$XDG_RUNTIME_DIR/smartconfig-lab.lock` (a tmpfs of yours, gone at
logout): it keeps the lab to one command per user whatever the cache.

## What one run proves

`lab/e2e.py --mode uefi|bios` (OVMF with a serial console, or SeaBIOS
with the VGA text screen) runs six boots on one overlay disk (nine with
`--grub-password`):

| Boot | What happens | Checks |
|---|---|---|
| 0 install | ssh in, copy the files, `install.sh`, `update-grub` | 0.1-0.7 |
| 1 normal | no menu; `sc-boot-seen` and `sc-boot-ok` record an ok boot; then the edit: a line for a missing disk goes into `/etc/fstab`, scd records it as BAD | 1.0-1.9, E.1-E.6 |
| 2 broken | nothing is typed; the disk times out, local-fs fails, emergency mode (outcome a, b or c); a hard reset | 2.1-2.7 |
| 3 rescue | the menu shows by itself; "SmartConfig rescue" is picked; the report matches the golden Go renders; Enter gives `#`; the report's commands restore fstab and reboot | 3.0-3.9 |
| 4 healthy | the menu once more, left to time out; an ok verdict clears it | 4.1-4.6 |
| 5 normal | no menu again; poweroff (`--no-boot5` leaves it out) | 5.1-5.2 |
| D package | `--deb` only, before the poweroff: the package was what boot 0 installed (`dpkg -i`; its postinst ran update-grub, and 0.5 holds that, not one of the lab's own); a later build of it goes in over it (scd restarted, update-grub, the store's rows and boots kept), then `dpkg -r` (scd stopped, no unit enabled, no rescue entry, no flag, the store kept), then `dpkg -P` (42_smartconfig gone, the store still kept); then the README's hand install up to M4 (the `m4` tag's units, drop-ins and `42_smartconfig`, with this tree's `sc` from the package in `/usr/local/sbin`, scd running) and `dpkg -i` over it (each file moved aside as `NAME.dpkg-old`, the units from `/usr/lib`, scd from `/usr/sbin/sc`, postinst's update-grub, one rescue entry); then `dpkg -r` and `dpkg -i` once more (the package's own `42_smartconfig` stays; `dpkg --verify` clean) | D.1-D.5 |
| 6a-6c GRUB password | `--grub-password` only, before the poweroff: the README's GRUB superuser recipe; the default boot asks for nothing; the rescue entry asks for the user and the password, and boots with them; Ubuntu from the menu asks for nothing | 6.1-6.5 |

Every check has a class and a strength. **[M4]** checks SmartConfig
(**[M5]**: the package's D.x); **[lab]** checks the lab itself (tools, QEMU, the VGA channel, keys). **H**
stops the mode, **F** fails it and goes on, **W** only warns. A known lab
flake is retried, the whole boot after a reset, at most `RETRIES_MODE`
times a mode: TCG's own panic ("IO-APIC + timer doesn't work"; any other
panic is not retried, and in the rescue boot it fails the check: that
kernel runs with the arguments 42_smartconfig wrote), a healthy boot that
TCG starved into emergency mode (slow udev on `LABEL=BOOT`, `LABEL=UEFI`
or ttyS0), a missed menu or a `grub>` prompt, on bios a gap of over 2 s in
the VGA polling before the menu's first screen (its countdown cannot be
held to 30 s then), a login prompt without ssh, a stall (below). Nothing
else is ever retried, but for two reads over ssh (`facts.sh normal` and
the boots file): one that ran out of time while the host stood still is
run once more. On bios a menu counts as missed only while the
screen does not say that GRUB drew none: observer lines still on it with
`timeout=[0]` or `style=[hidden]` are GRUB's decision, and with the flag
set that fails x.1. A reboot over ssh that
ssh lost (exit 255) is sent once more only when the guest answers from the
same boot with no reboot queued; otherwise its RESET decides. A guest RESET within 2 s
of the one before it (on bios every guest reboot gives two, about 15 ms
apart: the firmware resets once more) is the same boot's start, not a
reset of its own; `e2e.log` and `ledger.json` (`chained_resets`) list each.
On bios the VGA screen is the only evidence of what GRUB did: a failed
poll, or screens with neither an observer line nor a menu, is a [lab]
failure, never a pass.

Verdicts: **PASS** (exit 0), **FAIL** (1: an [M4] or [M5] check failed),
**INCONCLUSIVE** (3: a [lab] check failed, the retries or the time ran
out). `make lab-e2e` runs both modes, echoes each mode's summary line, and
ends with `lab-e2e: PASS`, `FAIL` (a mode FAILed and said so in its
summary line) or `INCONCLUSIVE`; make shows the last two as `Error 1` and
`Error 3` and exits 2. What makes a PASS not a whole one is in the summary
lines and in make's last line: `boot5=no` (`--no-boot5`), `dirty=yes` (a
run on a dirty tree; `LAB_REQUIRE_CLEAN=1` refuses one) and
`boot2=a(forced)` (`--boot2 reset-at-timeout`, which leaves 2.5 and 2.6
out). None of them counts for sign-off.

What the lab could not read is never taken for the guest's answer: an ssh
call that fails by itself (exit 255), or runs out of time while the host
stood still (twice, for the two reads above), is a [lab] error, and a poll gets back the time the host
stood still. The other way round, a wait that runs out in the rescue boot
with userspace up and no gap in the lab's own running fails its check
(3.5, 3.7, 6.4) as [M4]: sc's report runs before the shell, and one that hangs
must not be a run to repeat. 1.1 and 5.1 must have been checked in some
attempt; if every attempt had a flake first, the mode is INCONCLUSIVE. Preflight (P.2)
rebuilds the tree with the Makefile's recipe into the run directory and
refuses a `bin/sc` that is not byte for byte that build (`make build`;
`make lab-e2e` builds first). Go stamps the commit into `sc`, so do not
commit while `make lab-e2e` runs: the next mode's P.2 refuses the build. Sign-off also needs at least one run with
`goal3=multi-user` (boot 2 outcome b or c).

Boot 2's outcome is b or c only once ssh answers with `multi-user.target`
active (then `sc boot verdict` gives B2 a bad line), else a. ssh can come
up without that: when the console getty hangs up the emergency shell,
systemd starts `default.target` again, waits for the device once more and
drops back into emergency mode (plan A2). The report could be lost in that
race (plan A7); since `55d2bde` `sc status --console` outlives the
hang-up, so a report lost or cut fails 2.5 (F) in every outcome, c
included. The hang-up can also end the shell before its first
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
make lab-image LAB_FORCE=1     # after an ovmf update (P.5): the reference image again
make lab-e2e                   # both modes, ~25 min each under TCG
make lab-e2e LAB_MODES=uefi LAB_E2E_ARGS=--no-boot5     # while iterating
make lab-e2e LAB_E2E_ARGS=--grub-password                 # the README's GRUB password recipe too (6.x)
make lab-e2e LAB_E2E_ARGS=--deb                           # from the package (make deb's), and its upgrade, remove, purge (D.x)
make lab-test                  # the unit tests and sh -n (no QEMU, seconds)
make lab-clean                 # old runs, stale reference images

python3 lab/e2e.py --mode bios --boot2 reset-at-timeout --keep
python3 lab/vm.py up uefi      # a throwaway VM to poke at by hand:
python3 lab/vm.py ssh          #   its shell (as owner, with sudo)
python3 lab/vm.py console      #   its serial console (Ctrl-] leaves)
python3 lab/vm.py stop
```

`--boot2 reset-at-timeout` resets boot 2 right at the device timeout,
which forces outcome a. `--grub-password` runs the README's GRUB password
recipe after boot 5 and three more boots (6.x; the summary line ends
`grubpw=yes`); without it 6.x are skipped. `--deb` installs the package
(scripts/build-deb.sh of this tree) instead of the files, so check 0.4
holds the package's files to their paths in it (each must be this tree's), and adds D.1-D.5 (`deb=yes`;
D.4 needs the `m4` tag in the clone: `git fetch --tags`).
Neither runs with `--no-boot5`. `--keep` keeps the run's disk after a PASS too.

## Stalls

Under TCG a boot sometimes stands still for ten minutes and more. The lab
retries one only where SmartConfig has no part in the boot yet, so the
reset loses nothing sc did:

- before GRUB ran its configuration: a whole `BUDGET_MENU` with no
  `sclab: pre` line and no menu (41_sclab's line comes before
  42_smartconfig's flag block);
- in the kernel before `Run /init as init process`: a whole
  `BUDGET_KERNEL_LOGIN` in a boot that should reach a login prompt
  (boots 0, 1, 4 and 5).

Once per boot. A stall anywhere else is still INCONCLUSIVE. Each one
leaves `evidence/stall-<boot>-<attempt>.txt`: QEMU's CPU time over 5 s and
its registers before and after (a vCPU that spins, halts or moves), and
the mux's gaps. A gap is the lab's own process not running for over 10 s,
which means the host was paused or starved, not the guest; `mux.log` has
them for every run.

After a reset that came later than GRUB (a stall in the kernel, a panic),
Ubuntu's `recordfail` is still 1 in the next attempt of that boot and
`00_header` sets no `timeout_style`. Without the flag, 42_smartconfig must
leave it so: x.1 then expects `recordfail=[1]` and `style=[]`. In every
other attempt x.1 holds the observers to `recordfail=[]` and
`style=[hidden]`, and 1.6 and 4.4 hold grubenv to no `recordfail=1`: a
healthy boot unsets it, and one left set is a write to grubenv that sc's
units lost (the chunk C review).

Serial can lose a byte when the host is starved: the kernel's early
console waits only so long for the UART. The kernel prints its command
line twice, so x.2, 3.4 and 4.1.cmdline take the second line where the
first differs and the second is right, with a note. The reports (2.5,
3.6) have no second copy on serial: a report that differs by a missing
character, in a run with gaps in `mux.log`, is read before it is called
a bug. Run `make lab-e2e` on a quiet machine: no test runs, builds or
agents beside it.

## Cost

A mode takes about 25 minutes on this host (75 at most: `BUDGET_MODE`),
two CPUs at nice 10. Under TCG this host has stalled for over ten minutes
at a time; the budgets in `lab.conf` are wall clock and generous for that.

## The cache

`${SC_LAB_CACHE:-${XDG_CACHE_HOME:-~/.cache}/smartconfig-lab}`, mode 0700:

```
.smartconfig-lab                 says the directory is the lab's (see below)
lock, key, key.pub               the flock; the lab's ssh key (no passphrase)
images/<release>-<name>.img      the pinned cloud image (0444, sha256 = lab.conf)
ref-<h12>.qcow2 .VARS.fd .json   the reference image (0444); <h12> hashes the
                                 image, cloud-init/*.in, guest/60-sclab.cfg,
                                 the key and PROVISION_REV
runs/<UTC>-<mode>-<git7>/        one e2e run (also -provision-<h12>, -up-<mode>-<git7>)
```

The lab chmods this directory and `make lab-clean` deletes in it, so it
must be the lab's own: one it made, one with `.smartconfig-lab` in it, or
an older cache that holds nothing but the names above. Any other
directory in `SC_LAB_CACHE` is refused before anything in it is touched,
and `lab-clean` removes only names the lab makes (runs named by their UTC
time, `ref-*`, `*.img`, its own `.part` files). The reference image
remembers the sha256 of the `OVMF_CODE` it was made under; P.5 refuses a
uefi run after the firmware package changed. Its name does not depend on
the firmware, so build it again with `make lab-image LAB_FORCE=1`
(about 4 to 30 minutes).

A run directory holds:

| File | What |
|---|---|
| `result.txt` | a row per check: ID, status, class, strength, channel, evidence, expected, from, seen, notes |
| `ledger.json` | the run's values (GOOD, BAD, PRE, B1-B5, N, K, R1, R4, the outcome), every boot attempt, every check result in order |
| `e2e.log` | the progress lines |
| `serial.raw`, `serial.txt`, `serial.ts` | the serial console: raw, cleaned, with `[+seconds]` per line |
| `input.log`, `marks.log` | every byte and key sent; where each boot starts (QMP RESET) |
| `qmp.log`, `qemu.log`, `mux.log`, `ssh.log` | QMP traffic, QEMU's command line and output, the serial mux, every ssh call |
| `evidence/` | facts.sh output per boot, install.sh output, grub.cfg, the reports, VGA dumps (bios), the menu PNG, `stall-*.txt` after a stall |
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
commit and a test, never a lab change that makes it pass. An INCONCLUSIVE
is read before it is run again: `e2e.log`'s lab error, `mux.log` for gaps
(the host stood still), `evidence/stall-*.txt`.

Stopping a run: one Ctrl-C, SIGTERM or SIGHUP. The first one starts the
teardown (QEMU quit, T.1, `result.txt`, the ledger) and the lab ignores
the rest, so a second Ctrl-C cannot cut it. To keep a run going when the
terminal goes away, start it with `setsid nohup make lab-e2e`.

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
