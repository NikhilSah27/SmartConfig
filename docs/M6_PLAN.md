# M6 plan: the incident factory and the eval set

Status: **draft 3, 2026-10-09.** Your call (2026-10-09): "first verify
the draft and if need some changes in the plan then change and then
start according to that plan". The questions (section 14) run on their
recommendations until you say otherwise.
- **Draft 1** was written the same day. A review of the whole plan
  (`project-review-2026-10-09.md`, section 4) found errors in it, and
  draft 2 fixed them.
- **Draft 2 was verified against the code** (`cef26b5`) by three
  read-only checkers: the lab, sc, and the consumers (Ubuntu 24.04's own
  programs). Their findings were spot-checked in the code before they
  went in.
  - Most of draft 2 holds: 34 of 44 lab claims, 32 of 37 sc claims, 43
    of the 45 pilot predictions, and every number of the pilot.
  - What did not hold, and the design problems they found, is fixed
    here. The changes are listed at the end ("Changes from draft 2").
- **Sources:** the MILESTONES line, the design mock-ups, the QEMU lab
  (`lab/`), and a pilot of today's checkers on 45 made-up incidents
  (Appendix A).
- **Where it is built** (your call, 2026-10-09, "build in a clone, land
  later"): in a clone, `~/smartconfig-work/m6/smartconfig`, with pushing
  disabled, while the M5 follow-up round ran in the repo.
  - That round is done (2026-10-23). The clone's commits land on main
    one at a time, each with its CI, this plan first, as
    `docs/M6_PLAN.md`. The steps after them are built on main.
  - Step 2 (`sc check --tsv`) is written on top of the round's
    `sc check -v` change, and every step that runs the lab (4 on) comes
    after the landing.

## 0. Summary

- **What you get.** A fixed, versioned set of config incidents, and
  numbers for how well SmartConfig does on them. An incident is one
  change to one file of an Ubuntu 24.04 server. `make eval` says:
  - which of the incidents that really break a machine sc stops before
    the file is saved, and at which severity;
  - which harmless edits it complains about;
  - every incident it misses, each with what really happens.
- **What "really happens" means.** An incident's label comes from doing
  the change to a throwaway VM in the QEMU lab and watching the consumer:
  sudo, sshd, systemd, the boot. It never comes from sc, or from whoever
  wrote the incident.
  - The pilot shows why: sc and my guesses agreed on 25 cases.
  - Yet the consumers' own code predicts a real outcome other than sc's
    for 19 of the 45 cases, and other than the guess for 10 (12 with the
    two "unknown"; Appendix A). Some examples:
    - `update-grub` keeps the old menu when `/etc/default/grub` is
      broken;
    - sudo recovers from a syntax error;
    - the initramfs falls back to `/sbin/init` when `init=` names
      nothing;
    - libmount and udev skip a line they cannot read.
- **The guest is a server as installed.** Without that, the oracle would
  be blind: the lab image's admin logs in by key only and has passwordless
  sudo. So each run puts an "installed server" profile on its overlay: a
  password for the admin, and sudo only through `%sudo`.
- **Each incident is observed twice:** once after it takes effect, and
  once after a reboot, because many lockouts only show at the next boot.
  The worse outcome is the label.
  - **"Works, but not as written" needs its own probe.** Each incident
    says what "as written" means (`expect`: a mount, a sysctl value, the
    kernel booted), and the consumers' messages are read from the
    journal against a control boot. Without that, a skipped line or a
    fallback looks like "same".
  - **Flakes must not become labels.** A no-boot, an emergency, or a
    lockout whose only sign is ssh needs a line that names the cause and
    two runs out of two. The lab's own retry signatures (a stall, a
    panic, slow udev, no ssh, a missed menu) are never a label.
- **The factory** makes incidents in two ways:
  - a hand-written catalogue of about 80, with a minimum per checker by
    risk;
  - generated mutants, deterministic from a seed: typos, deleted lines,
    tabs, CRLF, a file cut short, a bad mode.

  Mutants get no VM label. They measure false alarms (an edit that changes
  nothing must give no finding) and robustness. A sample of the changes
  sc stays silent on goes to a person.
- **Scored on every surface sc has:**
  - `sc edit`;
  - scd, after a change made with another tool;
  - `sc check`;
  - `sc restore`;
  - the rescue entry, once per distinct way a boot fails.

  Each surface is compared by the findings a change *adds*, as sc edit and
  scd judge them.
- **Misses and wrong severities**, within a cap. M6 fixes misses the
  oracle confirms (up to six rules) and the severities it shows to be
  wrong. Each one is your call, with the evidence. The rest are listed
  with their reason. One sc bug is already known: scd misses a mode-only
  change it has no cached check for (section 7).
- **Labels are frozen.** The truth does not depend on sc, so it is run
  once per version of the set and committed. Scoring a new sc build takes
  a few hours, not two or three nights. Whether M7 reuses this set is for M7's
  plan to decide.
- **No new dependency in sc, and no model.** The product changes are
  `sc check --tsv` (question 5) and whatever the rules and severities of
  section 7 turn out to be.

## 1. Goals and non-goals

**Goals.** M6 is done when all of these hold, plus the sign-off in section 11.

1. **The set.** `eval/` in the repo holds about 80 hand-written incidents.
   - The minimums per checker follow risk:
     - **at least 5 that break something and 1 harmless:** fstab,
       sudoers, sshd, the GRUB files (`/etc/default/grub`,
       `grub.d/*.cfg`, `grub.cfg`), netplan, and units with their
       drop-ins;
     - **at least 2 and 1:** the accounts files, nsswitch, preload,
       hosts, the flags, sysctl and udev.
   - Consumers with no checker get incidents too (PAM), and so do
     changes no checker reads: a symlinked unit (scd records link rows
     and checks none). `/etc/default/grub.d` gets its checker in the M5
     follow-up round (its step 4).
   - It also holds the generator's mutants, which are reproducible from a
     seed.
2. **Truth from the consumer.** Every hand-written incident has a label
   from an oracle run in the lab (section 4).
   - The label holds the outcome class, when the outcome showed (after it
     took effect, or after the reboot), the severity that implies, the
     guest profile, and the evidence files.
   - When an incident changes, its label is stale, and `make eval` says
     so.
3. **The score.** `make eval` runs sc on the set in the lab guest as root
   and reports it per surface (section 6). The report goes to the run
   directory, and a freeze step copies it into `eval/REPORT.md`.
4. **No false alarm.** No harmless incident or neutral mutant may get an
   *added* finding at error or blocker.
5. **Robust.** On no mutant may sc:
   - panic;
   - hang past its budget;
   - exit 1, except its documented exit 1 (a validator cut short, a
     file it could not check or read), which comes with its message.
     A note that a check was partial exits 0 (all 19 of the pilot's
     did), so notes are recorded apart (`--tsv`, question 5).
6. **Every miss and wrong severity triaged.** Each is fixed in M6, listed
   as a known limit in MILESTONES, or marked out of scope with a reason.
7. **All checks pass:** gofmt, vet, tests as user and as root, `make race`,
   static build, `make m1-compat`, `make lab-test`. Also
   `make lab-e2e LAB_E2E_ARGS=--deb` in both modes after the lab is
   changed (section 9).

**Non-goals:**
- `sc why` or any model (M7).
- New checkers for consumers sc does not check (PAM, crypttab,
  nftables). Their incidents stay in the set as named misses
  (question 6).
- Scores on other releases or distributions, or on a desktop image.
- LVM or LUKS guests. They are on the open list in MILESTONES, for your
  decision.
- Shipping the eval: `eval/` is never in the package.
- A score target set before the first measurement. The first report sets
  the baseline.

## 2. What the pilot showed (Appendix A)

The pilot ran `sc check --as` as a user on this VM, with sc built from
`44b0394`. It used 45 made-up files: 39 meant to break something and 6
harmless. Its main use is that it shows the pitfalls.

1. **sc agreeing with the author proves little.** sc matched my guess on
   25 of 39. The consumers' code then predicts a real outcome other than
   sc's for 19 of the 45 cases:
   - **Over-severe** (7 predicted):
     - `/etc/default/grub` syntax: `grub-mkconfig` runs with `set -e`,
       reads the settings before it writes `grub.cfg.new`, and keeps the
       old menu;
     - a line libmount or udev skips;
     - an `ld.so.preload` entry that is ignored with a message;
     - a root shell that sulogin replaces with `/bin/sh`.
   - **Under-severe** (2): an ssh drop-in, and a `ListenAddress`, that
     cost SSH at the next start.
   - **Missed** (10).
   - These predictions are mine too. That is why the oracle, not the
     author, labels.
2. **Run as a user, the check is not the product.** 19 of the 45 cases
   said their check was incomplete, 5 of the 6 harmless ones among them
   (F10, F11, S05, H06, N04). Every one of those notes exited 0:
   - sshd could not read a host key;
   - findmnt could not read the disks;
   - netplan could not read the files it merges;
   - visudo could not read what the file includes.

   scd and `sudo sc` run as root, so the score is taken as root, in the
   lab guest.
3. **A bad `root=` breaks the rescue entry too.** It is a predicted miss
   at blocker (G04).
   - The kernel line of every 10_linux entry carries
     `GRUB_CMDLINE_LINUX`, and so does the SmartConfig rescue entry
     (`scripts/42_smartconfig:89-104`).
   - On the rescue line it comes after the entry's own `root=`, and the
     last `root=` wins.
   - A missing `init=` (G03) is no such case: the initramfs falls back to
     `/sbin/init`.
4. **Some misses are a blind spot of the lab image itself.** The image's
   admin has key-only ssh and passwordless sudo, so it could never show a
   PAM typo or a sudo-group change. Hence the profile in 4.1.

## 3. What an incident is

Each incident is one JSON file in `eval/incidents/<consumer>/<id>.json`.

```json
{
  "id": "fstab-uuid-one-char",
  "path": "/etc/fstab",
  "change": [{"append": "UUID={root_uuid~1} /data ext4 defaults 0 2"}],
  "applies": "fstab-now",
  "expect": [{"run": "findmnt -n /data", "as_written": "a mount on /data"}],
  "cause": "A disk's UUID with one character wrong, on a line without nofail.",
  "fix": "restore",
  "guess": "blocker",
  "source": "the journey film; the M3 owner scenario"
}
```

- **`change`** is a list of edits of the guest's own file. Each incident
  makes **one** change: a case with two changes is two incidents.
  - The edits: `append`, `insert` (before a line matching a pattern),
    `replace`, `delete`, `write` (whole content), `mode`, `owner`,
    `symlink` (make the path a link to a target), `unlink` and `create`
    (a new file).
  - Values that differ per machine come from the guest: `{root_uuid}`,
    `{root_uuid~1}`, `{nic}`, `{admin}`, `{kernel}`.
  - A `create` or a `symlink` makes any missing parent directory first,
    as `systemctl edit` does for a drop-in; undoing the change removes
    the directories it made.
  - Incidents are never applied through apt or dpkg. scd checks only
    the rows it records as `auto`; changes made during an apt run are
    tagged `apt` once the M5 follow-up round's step 8 is in, and are
    not checked.
- **`applies`** says what the oracle does to make the change take effect.
  It follows the file graph's `apply` lines:
  - `sudo`, `login`;
  - `restart:<unit>` (`daemon-reload` first, as the graph says for
    units), and `socket:ssh` (`daemon-reload` and a restart of
    `ssh.socket`, which a new `Port` or `ListenAddress` needs). A unit
    the guest never starts (`x.service`) needs `restart:x.service`:
    `systemctl --failed` does not list a unit that only failed to load;
  - `netplan` (`netplan apply`);
  - `sysctl` (`sysctl --system`);
  - `udev` (`udevadm control --reload`, then `udevadm trigger`);
  - `now`, for files read at every lookup: nsswitch, preload, hosts,
    passwd, group, shadow, PAM;
  - `fstab-now` (`daemon-reload`, then `mount -a`);
  - `update-grub`;
  - `boot`: nothing until the reboot. It is for `grub.cfg` and
    `custom.cfg`, which `update-grub` would rewrite (the graph's
    `grubcfg` line says "at the next boot").

  Every incident that is not harmless is then observed after a reboot as
  well, `now` ones included (4.3).
- **`expect`** says what "as written" means, as probes the oracle runs
  through the root channel: a command and what its output shows when the
  change took effect as written. For example `findmnt -n /data`,
  `sysctl -n <key>`, `ip -br link`, `/proc/cmdline` against
  `GRUB_DEFAULT`, `sudo -i true`. A probe that shows the change did not
  take effect as written makes the outcome "changed" (4.3). A harmless
  incident needs none.
- **Which surfaces apply depends on the kind of change** (6.1):
  - **content:** every surface;
  - **mode or owner:** not sc edit, which keeps the mode and owner.
    Only one rule reads the file on disk (`sudoers-mode`), so a mode or
    owner change of any other path cannot give a finding, and
    `/etc/shadow` has no checker at all. scd misses even a sudoers mode
    change unless it has a cached check of that version (section 7);
  - **symlink:** scd records a link row, and sc restore puts it back,
    but scd never checks a link row and `sc check PATH` refuses a link.
    So a symlink incident is scored on "recorded" and the restore only;
    on every finding surface it is a miss by construction, which the
    report names as such;
  - **unlink:** scd's deletion row and the restore only. `sc check` on a
    missing path exits 1 with only an error line, and scd does not check
    a deletion row;
  - **a create sc edit cannot make:** sc edit writes nothing for an
    empty new file ("unchanged"), gives a new file the graph's mode for
    the path (0644 when no `mode` line matches), not the incident's, and
    makes no directory. Such a create is not scored on sc edit
    (`incidents.surfaces`);
  - **a path outside scd's scope** (`/etc/sudoers.d/*.*`, for example):
    no scd row, so no restore of it; the report says "not recorded".
    sc edit does not look at the scope: its own rows, and their restore,
    still apply.
- **`cause`** is one unchecked sentence, written with the incident, that
  names each miss in the report.
- **`exposure`** (optional) is for a change that weakens security
  while everything still works: probes run as an unprivileged user
  (`nobody`) through the root channel, each with what a pass `shows`.
  One that passes makes the outcome "exposed" (4.3). For example
  `cat /etc/shadow`.
- **`fix`** is `restore` (put back what was there, or remove what the
  change made) or an edit. The oracle runs it, then the incident's
  `applies` again (`update-grub`, `netplan apply` and the rest; for
  `boot`, the reboot that proves health), and checks that the machine
  is healthy again.
- **`guess`** is the author's guess at the severity. It is kept, and never
  scored.
- **`"harmless": true`** marks a harmless incident. Its truth must come
  out as "same".
- **Nothing from this VM goes into `eval/`:** no UUIDs, interface names
  or user names. Base files come from the lab guest (the public cloud
  image) or are made up. The pilot's files in
  `~/smartconfig-work/m6plan/` use this VM's identifiers and are never
  copied.

## 4. The truth: oracles in the lab

### 4.1 The guest

- **The base is the lab's reference image as it is**, with **no
  SmartConfig installed** (question 3): the Ubuntu 24.04 cloud image, the
  user `owner`, and `60-sclab.cfg`.
- **Each run puts an "installed server" profile on its overlay**, not
  into the image:
  - `owner` gets a password. The image's ttyS0 getty already runs (the
    kernel line has `console=ttyS0`); with a password it accepts a
    console login;
  - `/etc/sudoers.d/90-cloud-init-users` is removed, so sudo works only
    through `%sudo`, and asks for the password (tested with `sudo -S`);
  - ssh stays key-only, as on a cloud server.

  `labels.json` and the report name the profile.
- **The order matters, because the profile takes the lab's own root
  away.** Today the lab drives the guest as `owner` over ssh with
  `sudo -n` (`lab/e2e.py:1458, 1462, 1617, 2314, 3823`), and the profile
  makes every one of those fail. So each run:
  1. installs the root shell (4.2) over ssh with `sudo -n`;
  2. proves the shell answers;
  3. applies the profile through it;
  4. from then on does every root action through it: the applies, the
     reboots, the fixes, the dumps.
- **In the sc-side run and the rescue check, the profile and the shell
  come before `dpkg -i`.** scd records all of `/etc`, so done after its
  baseline they would become store rows, and the rescue report would
  list them next to the incident.
- **"Stock" is not quite stock, and the report says so.**
  - The lab image has snapd masked, the apt timers off, cloud-init
    disabled, `fwupd-refresh.timer` and `unattended-upgrades.service`
    disabled, and `no_timer_check` on every kernel line, the rescue
    entry's too (`lab/cloud-init/user-data.in`, `60-sclab.cfg`).
  - The cloud image's own `/etc/default/grub.d/50-cloudimg-settings.cfg`
    is read after `/etc/default/grub`. It sets
    `GRUB_CMDLINE_LINUX_DEFAULT` and `GRUB_TERMINAL=console`, and
    `60-sclab.cfg` then sets `GRUB_TIMEOUT`, `GRUB_TIMEOUT_STYLE` and
    `GRUB_RECORDFAIL_TIMEOUT`. A change to any of those keys in
    `/etc/default/grub` would come out "same", so the catalogue makes
    such changes in the `grub.d` file that wins, or not at all.
    `GRUB_CMDLINE_LINUX` is safe: `60-sclab.cfg` appends to it.
  - There is one kernel. No incident needs a second one (5.1).
- **Labels are keyed on the image's sha256 and the provisioning recipe**,
  not on the lab's per-cache key (`ref_hash` includes `key.pub`,
  `lab/vm.py:394`).
- **One VM at a time**, under the lab's lock, as today.

### 4.2 A root channel no incident can break

- **What it is (recommended, question 4).** A root shell on a second
  serial port (ttyS1).
- **How it gets there:**
  - It is installed per run into the eval overlay over ssh (4.1, the
    order). It is not baked into the reference image: there it would be
    in every lab-e2e boot, which has one serial port. `/dev/ttyS1`
    exists there with no UART behind it, so the unit would fail and
    degrade check 1.3 to a warning (`e2e.py:247, 2083-2085`).
  - The second chardev is added in eval runs only. `Vm` hardcodes
    `serial.sock` and `qmp.sock` and waits for exactly those two
    (`vm.py:1119-1122, 1174-1175, 1208-1213`), so it gets an optional
    extra chardev. The serial `Mux` already takes any socket, but its
    log names are fixed (`serialmux.py:48, 233-234, 273`), so it gets a
    log prefix. `vm.py up` and `console` use one `console.sock`
    (`vm.py:1690-1692`), so they get a way to reach the second port.
- **What the unit looks like.** Not a copy of `serial-getty@`, which
  would not survive: it has default dependencies, and `sysinit.target`
  has `Conflicts=emergency.service emergency.target`; it also has
  `Conflicts=rescue.service` and binds to the tty's device. Instead:
  - a name of its own, `DefaultDependencies=no`, and `IgnoreOnIsolate=yes`;
  - `WantedBy=` `sysinit.target`, `rescue.target` and `emergency.target`;
  - no `BindsTo=` a device: TCG starves udev on ttyS0 (the lab's
    slow-udev retry), and ttyS1 would be no different;
  - `ExecStart=/bin/sh` itself, not root's login shell, which an
    incident (P05) removes.
- **Whether it survives emergency mode is proven in step 4**, before
  anything relies on it. Emergency mode comes from `OnFailure=` of
  `local-fs.target` (`OnFailureJobMode=replace-irreversibly`), not an
  isolate. Two isolate paths remain, and step 4 tries both: a
  `default.target` that points at nothing (systemd falls back to
  `rescue.target`), and `emergency` on the kernel line.
- **Nothing is written to it before the shell's sentinel appears.**
  OVMF may use the second serial port as a console as well (not yet
  checked). If it does, firmware and GRUB output show there, and a byte
  written before Linux runs is a key press for GRUB, which could open the
  hidden menu. Step 4 checks this.
- **What it does not depend on:** no login, no PAM, no network, no new
  package. It is never in the package.
- **It does not help after a no-boot.** After a GRUB error, a panic or an
  initramfs shell there is no shell at all. Those incidents run on an
  overlay that is thrown away afterwards; their fix is proven by the
  rescue check (6.3).
- **The alternative, qemu-guest-agent, is worse.** It needs apt at
  provisioning and a virtio-serial port, and its service is down in
  emergency mode.

### 4.3 What is observed

1. **Take effect.** Do what `applies` says, then observe:

   | Check | What the oracle runs | What it looks at |
   |---|---|---|
   | sudo | `sudo -S -v` as `owner`, with the password | works, refused, or a warning |
   | login | a new ssh login (key), a console login (password), `id owner` | each works or not |
   | units | `systemctl --failed`, compared with the stock list | units newly failed |
   | network | the guest's address and routes (`ip route get` an outside address), through the root channel; ssh from the host | kept or lost |
   | `update-grub` | its exit status, and whether `grub.cfg` changed | |
   | `expect` | the incident's own probes (section 3) | as written, or not |
   | `exposure` | the incident's exposure probes, as `nobody` (section 3) | exposed, or not |
   | journal | the boot's journal against the control boot's, for the consumers' messages (for example ld.so's "cannot be preloaded (…): ignored.", udev's "Invalid key", the initramfs's "Target filesystem doesn't have requested") | new messages |

2. **Reboot** (every incident that is not harmless, `now` ones too: a
   change to passwd, group or nsswitch can hit a service's own user, and
   that shows at boot). Observe the same, plus GRUB, the kernel, the
   initramfs, and the systemd target reached.
3. **Fix.** Run `fix`, then the incident's `applies` again, then the
   health check: sudo, an ssh login, no unit newly failed. If the health
   check fails, the guest is discarded.

**Where a boot ends.** The lab ends a boot today on the ttyS0 login
prompt (`console.py`, `e2e.py`'s `wait_healthy_end`). An incident that
masks that getty (`units-getty-masked`) never shows it, and the lab
would call the boot a stall. Step 6 ends a boot on the root shell's
sentinel on ttyS1, or the target reached, instead (chunk A's review,
A5).

**Outcome classes, and the severity each gives:**

| Class | What it means | Severity |
|---|---|---|
| no-boot | GRUB error, panic, initramfs shell ("Gave up waiting for root") | blocker |
| emergency | emergency or rescue mode | blocker |
| access-lost | the admin's sudo, ssh or console login gone, or the network | blocker |
| degraded | a unit newly failed | error |
| rejected | the consumer refuses the file, a service fails at its next start, or `update-grub` fails | error (question 6 asks whether `update-grub` failing is error or blocker) |
| exposed | it all works, but security is weaker than the stock file's: for example a world-writable sudoers drop-in, or `/etc/shadow` readable by all | error (question 6) |
| delayed | the boot or a login waits, then goes on: an NFS mount that times out, `wait-online`, an automount | warning (question 6) |
| changed | it works, but not as written: an `expect` probe fails, a line is skipped, a file is ignored, a fallback is taken | warning |
| same | nothing changed | none |
| inconclusive | the oracle could not tell | never scored |

**How the classes combine:** the worst class seen wins, and the label
records when it showed. U03, for example, is "same" when it takes effect
(the running sshd keeps serving after `daemon-reload`) and "access-lost"
after the reboot. H05 is no such example: `ssh.service` runs
`sshd -t` before it starts and does not retry after that fails
(`RestartPreventExitStatus=255`), so ssh is lost when it takes effect.

**What a no-boot, emergency or access-lost label needs:**
- **A causal line** that names the incident: on the console, a device
  timeout with its UUID, the initramfs naming the missing root, or a
  GRUB error; for access-lost, the consumer's own refusal, read through
  the root channel.
- **Emergency is read from the console, not from where the boot ends.**
  When the ttyS0 getty hangs up the emergency shell, systemd can go on to
  `multi-user.target` with ssh up (the lab's boot 2 outcome c). So the
  class comes from "You are in emergency mode" and the failed
  `local-fs.target`, not from the final target or from ssh.
- **Two runs out of two**, for no-boot, emergency, and an access-lost
  whose only sign is ssh from the host.
- **A control boot.** Each truth run also boots the unchanged guest. A
  run whose control boot fails is inconclusive.
- **None of the lab's own flakes.** The lab retries on these signatures,
  and the oracle calls each one inconclusive, never a label:
  - a stall, and TCG's own panic;
  - `slow-udev` (a healthy boot that TCG starved into emergency mode;
    `console.py:529-563`), which looks like **emergency**;
  - `no-ssh` (a login prompt but no ssh; `e2e.py:2075`), which looks like
    **access-lost**;
  - `vga-gap` (`e2e.py:1057`), and a missed menu;
  - `grub-prompt` (`console.py:557`), with one exception: for an
    incident on `grub.cfg`, `custom.cfg` or a `grub.d` script, `grub>`
    can be the real outcome. There it needs the causal line (the GRUB
    error) and two runs out of two, like any no-boot.

### 4.4 Cost

- **A boot incident takes 7 to 15 minutes:** apply, `update-grub` (30 to
  75 s), reboot, classify, fix, and a reboot to prove health. A no-boot,
  emergency or ssh-only access-lost outcome runs twice.
- **An incident whose `applies` is `now`** takes a minute, plus its
  reboot.
- **A cold boot takes about 2 to 3 minutes** to ssh (1m02 to 2m25 in 23
  of the lab's 24 passing runs), plus the shell and the profile. One
  follows each discarded guest.
- **Snapshots of a booted guest would save the cold boots, but they need
  preparation:**
  - every writable drive must support snapshots, and the UEFI vars are
    raw pflash (`lab/vm.py:1113-1114`), so they become qcow2;
  - HMP `savevm` is synchronous and blocks the monitor, so the QMP
    background job `snapshot-save` is used instead. It needs block node
    names, and the lab's `-drive` lines have none
    (`vm.py:1113-1117`): step 6 adds `node-name=` or moves to
    `-blockdev`;
  - each snapshot holds up to 2 GB of guest RAM (`MEM=2048`).
- **Disk.** `/` has 6.3 GiB free, and the lab cache is 3.9 GB, 3.3 GB of
  it 53 run directories: `KEEP_RUNS` prunes only when `make lab-clean`
  runs (`vm.py:1589-1600`). Each truth run starts with `make lab-clean`
  and a count of what the run will need.
- **The first catalogue takes 10 to 20 hours: two to three nights.**
  About 65 incidents reboot, at 7 to 15 minutes each, and on top come the
  repeated no-boot, emergency and access-lost runs, the control boots,
  and a cold boot after each discarded guest. It runs in UEFI mode only,
  because only there does the serial console show GRUB (BIOS shows it on
  VGA alone). Each incident's result is written as it finishes, so a run
  that dies resumes where it stopped. This VM has gone down without a
  shutdown four times.
- **It runs again only for incidents that changed, or for a new image.**

## 5. The factory

### 5.1 The catalogue (hand-written)

About 80 incidents to start, harmless ones included, with the minimums
of goal 1:

| Consumer | Kinds of incident |
|---|---|
| fstab | UUID one character off; a disk by `LABEL=` that is not attached, without nofail (the q35 guest has no USB controller); option typo; bind source; swap file; an NFS server at an unused address in 10.0.2.0/24 (a literal address: a name would need DNS or a second change; `nfs-common` must be in the image, or the mount fails at once, which is a different incident); wrong type; `x-systemd.automount` (the oracle touches the path, with a timeout); CRLF. Harmless: noatime, tmpfs. |
| sudoers | missing ALL; unclosed paren, on the `%sudo` line; `%sudo` removed; `@includedir` removed (two incidents, not one: under the profile only the first loses sudo); a drop-in that sudo ignores; a world-writable drop-in (sudo skips it; a 0644 one it accepts); the admin's rule in a file sc does not record. Harmless. |
| sshd | `PermitRootLogin yess`; keyword typo; `AllowUsers` shutting out every admin; a `ListenAddress` not on the machine; a global line after `Match`; a keyword accepted inside `Match` that silently applies only there; a bad drop-in. Harmless. |
| GRUB | `/etc/default/grub` syntax (unclosed quote, spaces around `=`, unquoted space); `root=` pointing at nothing; `init=` pointing at nothing (falls back); `GRUB_DEFAULT` naming a kernel version the guest does not have (one kernel is enough: GRUB falls back to entry 0 of the "Advanced options" submenu, and the `expect` probe reads `/proc/cmdline`); a broken `grub.d/*.cfg`; `custom.cfg` and `grub.cfg` syntax (`applies: boot`). No change to a key that `50-cloudimg-settings.cfg` or `60-sclab.cfg` overrides (4.1). Harmless. |
| netplan | tab; key typo; interface not on the machine (the DHCP address stays until the reboot); a gateway off the subnet (needs a static address, since DHCP keeps its own default route; observed with `ip route get` through the root channel, because ssh from the host comes from 10.0.2.2, which is on-link: no default route is access-lost). Harmless. |
| units | the ExecStart binary missing; broken section header (both `applies: restart:x.service`); second ExecStart in `ssh.service.d`; a masked getty (symlink); `default.target` pointing at nothing (symlink). Harmless. |
| accounts and login | nsswitch without files; root's shell gone; the admin out of group sudo; `/etc/shadow` readable by all (exposed); a PAM module typo; `ld.so.preload` naming nothing; hosts without localhost (systemd-resolved still answers for localhost, so this may well be "same"); `/etc/nologin`. Harmless. |
| sysctl, udev | a value the kernel rejects; key typo; a NIC rename (only if the guest's netplan has no `set-name`: netplan's own `99-netplan-<nic>.rules` sorts after a `70-*.rules` rename and undoes it; read the guest's file first; only a reboot shows it); rule typo. Harmless. |

Each incident is reviewed when it is written: the change must be one an
owner or a tool could plausibly make. Each one makes one change.

**The first catalogue (step 3, 2026-10-12):** 83 incidents in 14
consumers, 15 of them harmless (81, and two that chunk A's review
added). What the guest's own files changed in
the table above:
- the image has no `nfs-common`, so there is no `mount.nfs`. The NFS
  incident is the other one: a share on a guest with no NFS client,
  which fails at once. A server that times out needs `nfs-common` in the
  image, a change to the provisioning;
- the guest's netplan file has `set-name`, so there is no NIC-rename
  incident;
- `x-systemd.automount` is tried on the missing backup disk, not on NFS.

Each change is checked against the guest's files, as
`eval/guest.json` (the type, mode and owner of each path an incident
changes, with the sha256 of each copy; the directory each new file or
link goes in; and the guest's facts) and `eval/guest/` (a copy of each
file a content change edits) record them. Both are read from the
reference image with `debugfs`, before the profile.

**The minimums count what the author means to break.** Some incidents
will come out "same" (`hosts-no-localhost`, which systemd-resolved may
answer anyway, for one). After the truth run (step 7) the minimums are
counted again on the labels, and a consumer that falls short gets
incidents that do break something.

**For fstab, the reboot labels most incidents.** `fstab-now` is
`mount -a`, which skips `noauto` lines and makes no mount point (only
`X-mount.mkdir` does); systemd, at boot, does both.

### 5.2 Mutants (generated)

`lab/factory.py` applies operators to the guest's stock files, for every
path in the graph. It works from a seed and makes at most N per file,
about 1,000 in all. They are run in one loop inside the guest, not one
ssh call each.

- **Content mutants are checked with `sc check --as PATH FILE`**, from
  files in a scratch directory, never written into the live `/etc`.
  Writing about 1,000 broken versions of `ld.so.preload`,
  `nsswitch.conf`, `passwd`, `sudoers` or `fstab` into the guest would
  break the loop and sc's own validators, and scd would record and check
  every one. The stock baseline is taken with `--as` too.
- **Neutral mutants** have a truth known by construction: nothing
  changes.
  - The operators: a comment added or changed; blank lines; trailing
    spaces.
  - A reorder is used only where it is proven neutral for that consumer.
    sudoers is last-match-wins and sshd first-match-wins, so neither gets
    one.
  - An *added* finding on a neutral mutant is a false alarm.
- **Changed mutants:**
  - drop, double, swap or replace a character (a keyboard neighbour) in a
    keyword or value;
  - delete, double or comment out a line;
  - tabs for spaces in YAML;
  - CRLF, or no final newline;
  - the file cut at a random byte, or empty;
  - a BOM.

  A changed mutant has no truth label. It is held to the robustness rule
  of goal 5. A fixed-size sample of the ones sc is silent on, picked by
  the seed, is listed for a person; the ones that matter become catalogue
  incidents.
- **Mode and owner mutants** are made on disk, for the sudoers paths only
  (`sudoers-mode` is the one rule that reads the file on disk; `--as`
  leaves it out: `internal/check/checks.go`, `CheckSaved`), and checked
  with `sc check PATH`. The mode is put back after each.

## 6. Scoring sc

### 6.1 The sc-side run (`make eval`)

This runs in a lab guest with this tree's package installed (`dpkg -i`)
and scd running, after the root shell and the profile (4.1). It is
driven through the root channel. Each run records `sc status` and the
store's free space first: below 256 MiB scd holds back every change that
needs new content (the M5 follow-up round's step 1), and a scd that is
holding back must not count as a miss. For each incident, by kind of
change (section 3):

1. **sc edit** (content changes only).
   - It runs as `EDITOR=<script> sc edit PATH </dev/null`, with
     `SUDO_EDITOR` and `VISUAL` unset (they outrank `EDITOR`). sc edit
     reads its answer from stdin and does not check for a terminal
     (`cmd/sc/edit.go:123`).
   - The harness reads sc edit's stdout, its human table: sc edit has no
     `--tsv`. The editor's own output lands in the same stream, so the
     `EDITOR` script prints nothing. An `EDITOR` script cannot read the
     findings: the check runs after the editor exits.
   - With stdin at end of input, an added blocker or error makes sc edit
     quit. It exits 2, the file is unchanged, and it leaves a `kept-*`
     copy, which the harness removes.
   - With only warnings or nothing, it saves, and sc restore undoes the
     save before step 2.
2. **Another tool.** The change is made through the root channel. Then
   scd's journal lines are read:
   - their format is `T<tier> <path>: check: <sev> <rule>, line N:
     <text> (<row>)`, without `, line N` when the finding has none; at
     most 5 lines, then `T<tier> <path>: check: N more (sc check
     <path>)` (`internal/watch/checker.go:246-253`). scd quotes a path
     that has a space, `"` or `\` (watch's own `show()`,
     `watcher.go:1372-1380`);
   - scd also logs `ok again` lines, and, once per path per run, that a
     validator did not finish; a second incident on the same path never
     gets that notice again;
   - scd logs only what the change added, with three exceptions: when
     the version before cannot be checked it logs every finding; sc edit
     and restore rows log nothing; link, deleted and digest rows are
     never checked;
   - **a mode-only change** of a sudoers file is logged only if scd has a
     cached check of that version (section 7). Until that is fixed, each
     mode incident runs right after a restart of scd, so its result is
     the same every time;
   - their absence is recorded after a wait.
3. **sc check.** `sc check --tsv PATH`, on disk. Its findings minus the
   stock file's own findings (one `sc check --tsv` of the stock file per
   path, once) are what the change added. They are subtracted as
   `check.Added` does: by rule, severity, text and key
   (`internal/check/check.go:65-85`), so `--tsv` prints the key (a hash
   of the line, question 5).
4. **sc restore.** sc restores the row from before the change, as
   `sc restore ID </dev/null`, then the health check must pass.
   - With the M5 follow-up round's step 3, sc restore checks the version
     it is about to write and asks when that version adds a blocker or an
     error; end of input means it does not restore. Restoring the
     version from before a breaking change adds nothing, so it does not
     ask.
   - If it asks anyway (the incident removed a stock finding that the
     old version brings back), that counts against the restore surface,
     and the harness restores with `--force` so the health check can
     run.
   - Evidence from `sc cat` and `sc diff` of a secret-bearing file
     (`/etc/shadow`, netplan) needs `--show-secrets` once the round's
     steps 6 and 7 are in.
   - `sc log` lines may carry the `apt` origin (the round's step 8).

Mutants get step 3 only, with `--as` (5.2), in one loop. The whole set
takes about 1.5 to 3 hours under TCG.

### 6.2 What is measured (the report)

All of these use *added* findings, so the surfaces can be compared.

- **Catch rate.** By truth severity: a finding at that severity or
  higher, and any finding at all.
- **Severity agreement.** The truth against sc's highest added severity,
  over-severe and under-severe counted apart.
- **Line agreement.** Whether sc's finding names the changed line.
- **False alarms.** Harmless incidents and neutral mutants, by severity.
- **Surfaces agree.** sc edit's decision, scd's lines and sc check, where
  each one applies.
- **Restore.** Every restore leaves a healthy guest.
- **Misses by name.** Each with its cause, truth, when it showed, and its
  evidence.
- **Robustness.** Goal 5.
- **What it was measured on.** The image, the profile, and the stale or
  unlabelled incidents.

The first full report is the baseline.

### 6.3 The rescue check

This runs once per **distinct way a boot fails** in the truth: a missing
device in fstab, a bind source, a broken unit on the boot path, a bad
`root=`, and whatever else the truth run shows. That is about five, not
one per incident.

- **What it reuses.** It reuses the M4 path in `lab/e2e.py` (boots 2 to
  4): the failed boot, the menu, "SmartConfig rescue", whether the
  report names the file and the restore, its commands, then a healthy
  boot. It runs **without the server profile**: e2e's boots 0 and 1
  install and check over ssh with `sudo -n`, and what it tests is sc's
  rescue path, not the admin's access.
- **What has to be generalised first.** That code is written for fstab,
  in more places than the three draft 2 named:
  - `RESCUE_REASONS` (`e2e.py:851`) and the goldens that name
    `fstab-source-missing`;
  - `BAD_UUID` and `BAD_LINE` (`e2e.py:68-70`), and about 60 other
    fstab references in `e2e.py`;
  - `classify_boot`'s bad-UUID tail (`console.py:532`);
  - `facts.sh`'s rescue mode (the `/etc/fstab` hashes,
    `sc check /etc/fstab`, `/mnt/backup`);
  - the checks E.1-E.6, 2.3, 3.5, 3.8, 3.9, 4.2, 4.4 and 5.1.notime;
  - the typed `sc restore {GOOD}` (`e2e.py:106`), against the restore
    check of the M5 follow-up round;
  - and the classifier has no end state yet for an initramfs shell or
    "Gave up waiting for root", which G04's no-boot needs.

  The round's step 9 changes `e2e.py` first, so these lines are read
  again when step 11 starts. After it is generalised,
  `make lab-e2e LAB_E2E_ARGS=--deb` must pass in both modes again.
- **When a failure is expected.** A bad `root=` reaches the rescue entry
  too. Unless section 7 fixes it, that path is an "expected rescue
  failure", a known limit, not a FAIL.
- **Modes and cost.** It runs in UEFI mode at step 12 and in both modes
  at sign-off. Each path takes about 20 minutes: 7 to 9.5 to get an
  installed guest, then 9.5 to 11 for the path.

## 7. Misses and severities: what happens to them

**What each finding in the first report gets:**
- **A rule in M6.** The finding is a miss with blocker truth, or a miss
  with error truth whose fix is a graph line. It needs a check-only way
  to see it and no new dependency. At most six rules.
- **A severity change.** A rule the oracle shows too high or too low.
  Each one is your call, with the evidence (question 6).
- **A known limit**, listed in MILESTONES.
- **Out of scope**, with the reason. For example: a consumer with no
  checker, or a fact sc cannot know when it checks, such as whether an
  NFS server answers.

**The pilot's predicted candidates.** The oracle must confirm each one
first. `/etc/default/grub.d/*.cfg` (G08) is no longer one: the M5
follow-up round checks it (its step 4).

| Candidate | Would catch | Note |
|---|---|---|
| `grub-cmdline-root`: `root=` in `GRUB_CMDLINE_LINUX*` naming nothing on this machine | G04 | breaks the rescue entry too |
| `sudoers-no-admin`: no rule left for the members of `sudo` or `admin` | S03 | like `passwd-no-admin` |
| `sshd-no-admin`: `AllowUsers`, `AllowGroups` or `DenyUsers` shut out every admin | H03 | `sshd -t` accepts the file |
| `netplan-nic-missing`: a matched interface the machine does not have | N02 | at blocker when it is the only path to the network |
| `sysctl-value`: a value of the wrong kind for its key | C01 | `sysctl --dry-run` writes nothing, so cannot see it |
| scd's mode check (a bug) | a sudoers mode change | scd checks the version before a change with `Check`, which reads the mode on disk now (`internal/watch/checker.go:176-180`, `sudoers.go:174`), so before and after give the same `sudoers-mode` finding and `check.Added` drops it. Seen in a scratch run: a `chmod 0666` logged nothing on a fresh scd, and an error after a content change. Fix: check the version before with `CheckSaved`, or take its mode from its row |
| severities | G01, G05, G06 (`grub-default-syntax`), F05, P02, P05, D01 too high; H04, U03 too low; G08 once the M5 follow-up round checks `grub.d` (its step 4) with the same rule | your call, with the evidence |

PAM gets no checker in M6. No check-only form has been proven for it, and
M3 deferred it. Its incidents stay in the set as named misses.

## 8. Where it lives

```
eval/                        the set: data, never shipped (not in the .deb)
  README.md                  what an incident is; how to add one; how to run the eval
  incidents/<consumer>/*.json
  guest.json, guest/         the guest the incidents are written against (5.1)
  labels.json                the truth: id, content hash, image sha256, recipe, profile,
                             outcome, when, severity, evidence
  REPORT.md                  the latest score, copied in by the freeze step
lab/incidents.py             applying changes, the oracles, the root channel, the sc-side run
lab/oracle.py                the outcome class, when and severity from what the oracle saw (4.3)
lab/factory.py               the mutation operators, deterministic
lab/test_incidents.py, lab/test_oracle.py, lab/test_factory.py
cmd/sc/check.go              --tsv (question 5)
Makefile                     eval-truth, eval, eval-freeze
```

- **The lab stays as it is:** dev only, standard-library Python, no sudo.
- **Runs write only to their run directory.** That keeps the tree clean
  (the lab's P.3 and `LAB_REQUIRE_CLEAN`).
- **`make eval-freeze`** is a separate step, after a run. It copies the
  labels and the report into `eval/`, and the cited evidence to
  `~/smartconfig-work/signoff/eval-<UTC>/`, before `KEEP_RUNS=10` can
  remove the run.
- **The lab's Python unit tests already run in CI**, through
  `TestLabPython` (`cmd/sc/unit_test.go:281-300`), which compiles
  `lab/*.py` and runs `unittest discover -s lab`; the new ones join them.

## 9. Order of implementation

The loop is the usual one: one step at a time, with tests, the full
checks, one commit, a worklog line, push, and CI green. Truth and eval
runs start detached, with their log in `signoff/`. The repo is not
changed while they run, so the nights they take are planned.

| # | Chunk | Step | Tests |
|---|---|---|---|
| 0 | - | this plan, as `docs/M6_PLAN.md` | |
| 1 | A | the `eval/` format: loader, edits (`symlink`, `unlink` and `create` included), placeholders, `applies` (with `boot`), `expect` probes, the surfaces per kind of change, content hash | every edit on fixtures; bad files refused; the hash is stable |
| 2 | A | `sc check --tsv`, on top of the M5 follow-up round's step 6 | columns (rule, severity, path, line, key, text), escaping (as `show()`), note records and the incomplete and unchecked flags, exit codes as today, no `Raw` or `Said` lines (so no secret leaks) |
| 3 | A | the first catalogue (about 80) | all parse; ids are unique; the minimums of goal 1 |
| 4 | B | the lab: an optional extra chardev in `Vm`, a log prefix for the `Mux`, the second port in `vm.py up` and `console`; the root shell unit (4.2) installed per run; the order and the server profile (4.1) | unit tests; one live run with `vm.py up`: the shell outlives emergency mode and both isolate paths; nothing is written to it before its sentinel (OVMF on COM2) |
| 5 | B | the take-effect oracles, the `expect` probes, the journal against the control boot, and the health check (4.3) | fake guest transcripts for each class |
| 6 | B | the boot oracle: the reboot, the causal-line rule, emergency read from the console, the control boot, two out of two, every lab retry signature as inconclusive; try `snapshot-save` with qcow2 vars and node names | the classifier on saved console logs from the lab's own runs, stalls, slow-udev and no-ssh among them; a boot that ends with no ttyS0 login prompt |
| 7 | B | the first truth run (two to three nights, detached, after `make lab-clean`), then `eval-freeze`. Each label is read against its evidence. | the stale-label check; goal 1's minimums on the labels |
| 8 | C | the sc-side run (6.1) | a fake guest; one live incident end to end |
| 9 | C | the scorer and the report | canned results; every metric |
| 10 | C | the factory (5.2) and its run | determinism; the neutral operators are neutral on fixtures |
| 11 | C | generalise `e2e.py`'s rescue path (the list in 6.3), then `make lab-e2e LAB_E2E_ARGS=--deb` in both modes | the lab's tests; both modes pass |
| 12 | C | the rescue check (6.3), UEFI | |
| 13 | C | the first full report, which is the baseline | |
| 14 | D | triage with you (section 7); then the rules and severity changes, one commit each, each with its incidents as its tests | the checker tests, as in M3; the report again |
| 15 | E | docs (`eval/README.md`, README, MILESTONES); the final review | |
| 16 | E | the sign-off runs (section 11); tag `m6` | |

Reviews: one reviewer at the end of each of chunks A to D. A multi-agent
final review before the tag, which I ask you about first (question 8).

**While the M5 follow-up round runs** (in the repo, by another session):
steps 0, 1 and 3 are built in the clone and land after the round, one at
a time. So is the half of step 10 that needs no lab, the factory itself
(your call, 2026-10-12: "build the mutant factory now"); its run waits
for step 8. And so is step 5's applier: the shell that makes each change
and undoes it, the applies commands and the probe commands, tested on a
copy of the guest's files (after "continue", 2026-10-12); the root
channel it runs over is step 4's. And the rules of 4.3 that make a
label from what the oracle saw (`lab/oracle.py`), tested on made-up
observations; gathering them is steps 5 and 6. With them, `labels.json`
and step 7's stale-label check: a label is keyed on the incident's
content hash, and on the image, the recipe (lab.conf's `IMAGE_SHA256`
and `PROVISION_REV`) and the profile. Step 2 is written on the round's `sc check -v` change. Steps 4 on
use the lab, so they wait for the round, whose sign-off runs the lab too.

## 10. Estimate

History says code steps go fast, while lab runs and gates set the
calendar. M3 was estimated at 4 to 5 days and took about one. M4's lab
step alone took 16 hours and about ten runs.

- **Session time:** about 4 to 6 working days.
- **Machine time, with the repo frozen during each run:**
  - the truth run: 10 to 20 hours, two to three nights;
  - each sc-side run: 1.5 to 3 hours;
  - `lab-e2e --deb` after the e2e change: about an hour for both modes;
  - the rescue check: about 2 hours a mode for five paths;
  - the sign-off runs: one more night.
- **Calendar:** about two weeks, most of it nights of lab time. This
  assumes the host's stalls and crashes are looked at first (WORKLOG,
  "Still waiting for you"). Without that, every number here can double.

## 11. Acceptance and sign-off

**Owner scenario (the one for M6).** You ask how good sc really is.
`make eval` answers on one page:
- Of the N incidents that break an Ubuntu 24.04 server (each one shown
  by breaking a throwaway VM, twice for a boot that fails), sc stops X
  before they are saved.
- It flags Y at the right severity.
- It complains about none of the M harmless edits.
- Here are the K incidents it misses, and each severity it gets wrong,
  each with what really happens.

**Sign-off, each step with your OK:**
- S1: all checks; `make lab-test`; `make lab-e2e LAB_E2E_ARGS=--deb` in
  both modes, on the tag candidate.
- S2: `make eval-truth` on a clean tree: every label current, and the
  control boots ok.
- S3: `make eval` on the tag candidate:
  - no added false alarm at error or blocker;
  - goal 5 holds;
  - every restore leaves a healthy guest;
  - frozen into `eval/`.
- S4: the rescue check for each distinct path, in both modes. Expected
  failures are known limits.
- S5: the final review, the docs, and the tag `m6`.

Nothing in M6 runs on this VM's own system: no `sudo` and no reboot here.

## 12. Risks

- **The truth is only as good as the oracle.**
  - Mitigation: the server profile, the reboot observation, the
    causal-line rule, two runs out of two, and the control boots.
  - The harmless incidents check the oracle itself.
- **Host stalls and crashes.** Of 50 lab runs, 24 passed, 18 were
  inconclusive, 6 failed and 2 gave no verdict. Of the 18, 3 were
  preflight refusals and 2 were stopped, so about 13 were the host's or
  TCG's trouble. The truth run needs nights of steady machine.
  - Mitigation: results are written per incident and runs resume; there
    is an inconclusive class; the host itself should be looked at.
- **Disk.** 6.6 GB free on `/`. Snapshots, overlays and kept runs are
  counted before each run.
- **Teaching to the test.**
  - The mutants and the catalogue grow separately from the rules.
  - A rule or severity change needs the oracle's evidence.
  - Misses are named, never hidden in a percentage.
- **The guest is not every machine.** It is one image and one disk
  layout, with no desktop, no LVM and no LUKS. The report says what it was
  measured on.
- **A lab root shell.** It has no password, but it exists only in a
  throwaway VM's overlay, behind the lab's own socket.

## 13. Not in M6

- `sc why` or any model (M7). Whether M7 reuses the set is M7's plan's
  call.
- New consumers (PAM, crypttab, nftables, logrotate, AppArmor). Their
  incidents stay as named misses.
- Other releases, a desktop image, LVM or LUKS guests.
- VM runs in CI. CI already runs the lab's unit tests.
- A public dashboard of the score.

## 14. Questions for you

Each has a recommendation, and the plan assumes it.

1. **Scope**, and what comes first. The catalogue, the mutants, the truth
   from the lab, the score on every surface, and a capped chunk of rules
   and severities. *Recommendation: yes, after your decision on the work
   no milestone owns (MILESTONES).*
2. **Truth only from the oracle**, with the reboot observation and the
   no-boot rules of 4.3. *Recommendation: yes. It costs two to three
   nights of lab time per full run.*
3. **The guest:** the stock image without SmartConfig, plus the "installed
   server" profile per run. *Recommendation: yes. Without the profile,
   the PAM and sudo incidents cannot show anything.*
4. **The root channel:** a root shell on a second serial port, installed
   per run (no new package). The alternative is qemu-guest-agent.
   *Recommendation: the serial shell.*
5. **`sc check --tsv`:** one line per finding with rule, severity, path,
   line, key and our text, escaped as today's `show()`, plus a record for
   each note and the incomplete and unchecked flags; never the
   validator's raw lines. The table could be parsed for `sc check PATH`,
   but it does not carry what the harness needs: the key, without which
   "what the change added" differs from what sc edit and scd compute
   when a line moves or repeats; and the notes, which say a check was
   partial while sc still exits 0. *Recommendation: `--tsv`.*
6. **Fixes in M6:**
   - up to six rules for misses the oracle confirms;
   - severity changes it shows, each your call, the first being whether
     "`update-grub` fails, the old menu stays" is an error or a blocker;
   - the severities of the two new outcome classes: "exposed" at error,
     "delayed" at warning (4.3);
   - the scd bug with mode changes (section 7).

   PAM and other new consumers wait. *Recommendation: yes.*
7. **`eval/` in the public repo:** made-up files and the public cloud
   image's, nothing from this VM. *Recommendation: yes.*
8. **Reviews:** one reviewer per chunk, and a multi-agent final review
   that I ask you about first. *Recommendation: yes.*
9. **The host first.** The stalls and crashes in the worklog should be
   looked at before step 7, the first two-night run. *Recommendation:
   yes.*

## Changes from draft 1 (the review of 2026-10-09)

- **The pilot's lessons.** G03 is no longer counted as a blocker (the
  initramfs falls back to `/sbin/init`); the rescue-path point is now
  about `root=` alone. "One guess wrong outright" is replaced by
  predicted outcomes (Appendix A).
- **The guest.** It gets an installed-server profile (4.1).
- **Observation.** Each incident is observed after it takes effect and
  after a reboot, the worse outcome wins, and `applies` covers every
  graph line (3, 4.3).
- **No-boot labels.** They need a causal line, two runs out of two and
  control boots, and there is an inconclusive class (4.3).
- **The sc-side run matches the CLI** (6.1):
  - sc edit runs with `</dev/null`;
  - scd's line format is as it really is;
  - "added" findings are compared;
  - surfaces are chosen per kind of change;
  - there is a `symlink` edit;
  - out-of-scope paths are noted.
- **Machine time** is re-estimated: two nights for the truth, and the
  calendar planned (4.4, 10).
- **The root shell** is installed per run, not baked into the image
  (4.2).
- **The rescue check** runs once per failure path. `e2e.py` is
  generalised first and re-validated, and expected failures are a known
  limit (6.3).
- **Runs write only to their run directory**, and a freeze step copies
  the results out (8).
- **Goals.** Goal 1 has minimums by risk, and the catalogue is about 80.
  Goal 4 counts added findings. Goal 5 allows sc's documented exit 1
  with its note.
- **The Q8 CI half** is dropped: the lab's unit tests already run in CI.
- **The M7 promises** are removed; `cause` is unchecked prose.

## Changes from draft 2 (the verification of 2026-10-09)

Three read-only checkers compared draft 2 with the code at `cef26b5`,
the lab cache and this host's Ubuntu 24.04 programs; their findings were
spot-checked in the code.
- **"Changed" can be observed** (3, 4.3). Each incident has `expect`
  probes, and the journal is compared with the control boot. Without
  them, 10 of the pilot's 45 predictions ("changed → warning") would
  have come out "same".
- **One reboot rule** (3, 4.3): every incident that is not harmless
  reboots, `now` ones too.
- **Flakes** (4.3): all of the lab's retry signatures are inconclusive
  (slow-udev looks like emergency, no-ssh like access-lost); `grub>` is
  a real outcome only for GRUB incidents, with its causal line;
  access-lost with ssh as its only sign needs two runs out of two;
  emergency is read from the console (the getty hang-up race).
- **Two new outcome classes** (4.3): "exposed" and "delayed".
- **H05's timing** (4.3, A1): ssh is lost when it takes effect; U03 is
  the example of a loss that shows only after the reboot.
- **The guest** (4.1): the order (the shell first, then the profile,
  then every root action through the shell, since the lab uses
  `sudo -n`); the profile and the shell before `dpkg -i`; the ttyS0
  getty already exists; what else "stock" changes, and the GRUB keys
  `50-cloudimg-settings.cfg` overrides.
- **The root channel** (4.2): what `Vm`, the `Mux` and `vm.py up` need;
  what the unit must look like to survive emergency mode; nothing
  written before the sentinel (OVMF on COM2).
- **Costs** (4.4, 10): a cold boot is 2 to 3 minutes, not 4 to 5; the
  truth run is 10 to 20 hours; `snapshot-save` needs node names; disk and
  `make lab-clean`.
- **The catalogue** (5.1): methods for the NFS, USB, automount, gateway,
  NIC-rename and `GRUB_DEFAULT` incidents (no second kernel); the
  sudoers split; a world-writable drop-in, not 0644; keys the image
  overrides.
- **Mutants** (5.2): content mutants with `--as`, never written into
  the live `/etc`; mode mutants for the sudoers paths only.
- **The sc side** (3, 6.1): scd's real line format and its exceptions;
  the scd mode bug (section 7); symlink and unlink incidents score
  "recorded" and the restore only; sc edit records outside scd's scope;
  sc edit's environment and stdout; `--tsv` carries the key and the
  notes (question 5).
- **The M5 follow-up round** (status, 1, 6.1, 6.3, 7, 9): G08 drops out
  of the candidates (its step 4); the restore check (its step 3); the
  store floor (step 1); secrets (steps 6 and 7); the `apt` origin
  (step 8); `--tsv` on top of step 6; `e2e.py` read again after step 9.
- **The rescue check** (6.3) runs without the profile; the list of
  fstab-specific code to generalise is complete; "step 13" is step 12.
- **Smaller fixes:** the pilot's numbers (19 of 45 differ from sc, 10 to
  12 from the guess; the 7 "None" rows; the notes exit 0); goal 5's exit
  rule; the A6 line for `/etc/default/grub`; the stall tally; F04, F09,
  G02, H04, C01, D01 and P03 in A1.

---

## Appendix A. Evidence (2026-10-09, this VM)

**A1. The pilot.**
- **How it ran:** sc built from `44b0394` into a scratch directory, then
  `sc check --as PATH FILE` as the user `vboxuser`, with a scratch
  `SC_HOME` and `TMPDIR`.
- **Where the files are:** `~/smartconfig-work/m6plan/`: `run-pilot.sh SC`,
  `cases/`, `index.txt` and `results.txt`. `results.txt` keeps no notes,
  and a rerun overwrites it.
- **"Guess"** is the drafter's guess, made before anything else.
- **"Predicted on the guest"** comes from the consumers' code and docs
  where they are cited (A6), under the installed-server profile. "?"
  marks a weaker prediction. It is not a label; the oracle decides.
- **19 of the 45 cases printed a note that their check was incomplete**,
  because the pilot ran as a user. Every one exited 0 (or 2 for a
  blocker): they are notes, not sc's incomplete check, which exits 1.
- **The 7 "None" rows** are the 6 harmless cases and F09 (guess
  "unknown", predicted "same").
- **Flawed cases:**
  - U01 makes two changes (the missing binary and the `Restrt=` typo);
  - F08's server name cannot resolve at all (`.invalid`);
  - G02 needs an `expect` probe to show anything (it falls back to the
    same kernel);
  - S03 makes two changes (`%sudo` and `@includedir` both gone).

  The catalogue fixes all four.

| Case | Checked as | Guess | sc (as a user) | Predicted on the guest |
|---|---|---|---|---|
| F01-uuid-onechar | `/etc/fstab` | blocker | blocker: `fstab-source-missing` | emergency → blocker |
| F02-usb-no-nofail | `/etc/fstab` | blocker | blocker: `fstab-source-missing` | emergency → blocker |
| F03-usb-nofail | `/etc/fstab` | warning | warning: `fstab-source-missing` | changed → warning |
| F04-opt-typo-root | `/etc/fstab` | error | error: `fstab-option-typo` | degraded (`systemd-remount-fs` fails and `/` stays read-only; it is wanted, not required, by `local-fs.target`) → error |
| F05-few-fields | `/etc/fstab` | error | error: `fstab-fields` | changed (libmount skips the line) → warning |
| F06-swapfile-missing | `/etc/fstab` | error | error: `fstab-source-missing` | degraded → error |
| F07-bind-src-missing | `/etc/fstab` | blocker | blocker: `fstab-source-missing` | emergency → blocker |
| F08-nfs-unreachable | `/etc/fstab` | error | nothing | degraded (remote-fs) → error |
| F09-crlf | `/etc/fstab` | unknown | nothing | same (libmount strips a `\r` before the newline; one mid-line is a parse error) → none |
| F10-benign-noatime | `/etc/fstab` | none | nothing | same → none |
| F11-tmpfs-ok | `/etc/fstab` | none | nothing | same → none |
| S01-missing-ALL | `/etc/sudoers` | blocker | blocker: `sudoers-syntax` | access-lost: sudo skips the bad `%sudo` rule (error_recovery) → blocker |
| S02-unclosed-paren | `/etc/sudoers` | blocker | blocker: `sudoers-syntax` | as S01 → blocker |
| S03-no-includedir | `/etc/sudoers` | unknown | nothing | access-lost → blocker |
| S04-dropin-dot-name | `/etc/sudoers.d/admins.conf` | warning | warning: `sudoers-ignored` | changed → warning |
| S05-benign | `/etc/sudoers` | none | nothing | same → none |
| H01-permitroot-yess | `/etc/ssh/sshd_config` | blocker | blocker: `sshd-invalid` | access-lost at the next start → blocker |
| H02-key-typo | `/etc/ssh/sshd_config` | blocker | blocker: `sshd-invalid` | access-lost → blocker |
| H03-allowusers-nobody | `/etc/ssh/sshd_config` | blocker | nothing | access-lost → blocker |
| H04-listen-foreign | `/etc/ssh/sshd_config` | blocker | warning: `sshd-listen-missing` | access-lost after `socket:ssh` or a reboot (the socket generator listens on it; `FreeBind=yes`, so no unit fails) → blocker |
| H05-match-then-global | `/etc/ssh/sshd_config` | error | blocker: `sshd-invalid` | access-lost when it takes effect (`sshd -t` fails before the start, no retry) → blocker |
| H06-benign | `/etc/ssh/sshd_config` | none | nothing | same → none |
| G01-unclosed-quote | `/etc/default/grub` | blocker | blocker: `grub-default-syntax` | rejected (update-grub fails, old menu kept) → error |
| G02-default-removed-kernel | `/etc/default/grub` | warning | nothing | changed (GRUB falls back to entry 0 of the "Advanced options" submenu, the same kernel; only a probe shows it) → warning (?) |
| G03-cmdline-init-missing | `/etc/default/grub` | blocker | nothing | changed (initramfs falls back to `/sbin/init`) → warning |
| G04-cmdline-root-wrong | `/etc/default/grub` | blocker | nothing | no-boot, the rescue entry too → blocker |
| G05-spaces-around-eq | `/etc/default/grub` | blocker | blocker: `grub-default-syntax` | rejected → error |
| G06-unquoted-space | `/etc/default/grub` | blocker | blocker: `grub-default-syntax` | rejected → error |
| G07-benign | `/etc/default/grub` | none | nothing | same → none |
| G08-grubd-unclosed-quote | `/etc/default/grub.d/50-cloudimg-settings.cfg` | blocker | nothing ("no checker reads this file") | rejected → error |
| N01-tab-indent | `/etc/netplan/50-cloud-init.yaml` | blocker | blocker: `netplan-invalid` | access-lost at the next boot → blocker |
| N02-nic-not-here | `/etc/netplan/50-cloud-init.yaml` | blocker | nothing | access-lost at the next boot → blocker |
| N03-key-typo | `/etc/netplan/50-cloud-init.yaml` | blocker | blocker: `netplan-invalid` | access-lost at the next boot → blocker |
| N04-benign | `/etc/netplan/50-cloud-init.yaml` | none | nothing | same → none |
| U01-exec-missing | `/etc/systemd/system/x.service` | error | error: `unit-exec-missing`; warning: `unit-unknown-key` | rejected → error (two changes: split it) |
| U02-bad-section | `/etc/systemd/system/x.service` | error | error: `unit-syntax` | rejected → error |
| U03-ssh-dropin-double-exec | `/etc/systemd/system/ssh.service.d/override.conf` | blocker | error: `unit-syntax` | access-lost at the next start → blocker |
| P01-nsswitch-no-files | `/etc/nsswitch.conf` | blocker | blocker: `nsswitch-no-files` | access-lost → blocker |
| P02-preload-missing | `/etc/ld.so.preload` | error | error: `preload-missing-lib` | changed (a message, then ignored) → warning |
| P03-hosts-no-localhost | `/etc/hosts` | warning | warning: `hosts-no-localhost` | changed → warning (?; systemd-resolved still answers for localhost, so it may be "same") |
| P04-pam-module-typo | `/etc/pam.d/common-auth` | blocker | nothing ("no checker reads this file") | access-lost (password sudo and console login) → blocker |
| P05-root-shell-missing | `/etc/passwd` | error | error: `passwd-shell-missing` | changed (sulogin falls back to `/bin/sh`) → warning (?) |
| C01-sysctl-bad-value | `/etc/sysctl.d/99-x.conf` | error | nothing | degraded (systemd-sysctl fails: v255 returns the write error) → error |
| C02-sysctl-key-typo | `/etc/sysctl.d/99-x.conf` | warning | warning: `sysctl-unknown-key` | changed → warning |
| D01-udev-typo | `/etc/udev/rules.d/70-x.rules` | error | error: `udev-invalid` | changed (udev logs "Invalid key" and drops the whole rule) → warning |

**Tally, predicted truth against sc:**
- **Blockers (18):** sc caught 11 at blocker, 2 lower (H04, U03), and
  missed 5 (S03, H03, G04, N02, P04).
- **Errors (10):** 4 right, 3 higher (G01, G05, G06), and 3 missed (F08,
  G08, C01).
- **Warnings (10):** 4 right, 4 higher (F05, P02, P05, D01), and 2 missed
  (G02, G03).
- **None (7):** all 7 silent.

**A2. What sc has today** (`44b0394`): 16 `check` lines in
`internal/check/default.graph` and 36 rule ids. The graph checks
`/etc/default/grub` but not `/etc/default/grub.d/*.cfg`. Its sshd `apply`
line did not mention `ssh.socket`. That was fixed in `970f5ac`
(2026-10-09); the installed 0.5.0 still has the old text.

**A3. Lab timings.** In `lab-e2e` UEFI on `8f91158`, boots 0 to 4 took
5m24 (with the install), 4m01 (with the edit), 5m03 (with the 90 s device
timeout), 2m15 and 3m46. The whole mode took 30m00.

**A4. The rescue entry's command line** is
`root=… ${GRUB_CMDLINE_LINUX} <GRUB_CMDLINE_LINUX_DEFAULT without quiet
and splash> systemd.mask=grub-initrd-fallback.service ro fstab=no
systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1`
(`scripts/42_smartconfig:89-104`). A `root=` in `GRUB_CMDLINE_LINUX`
comes after the entry's own, and the last one wins, in the kernel and in
initramfs-tools.

**A5. The lab image's access.** `lab/cloud-init/user-data.in`:
`ssh_pwauth: false`, `lock_passwd: true`, `disable_root: true`, and
`owner` with `sudo: "ALL=(ALL) NOPASSWD:ALL"`, written to
`/etc/sudoers.d/90-cloud-init-users`. Its GRUB settings are in
`/etc/default/grub.d/60-sclab.cfg`.

**A6. The consumers' code the predictions cite:**
- `/usr/sbin/grub-mkconfig`: `set -e` (line 2); it sources
  `/etc/default/grub` (line 162) and `grub.d/*.cfg` (line 164) before it
  writes `grub.cfg.new` (line 279),
  and copies that over `grub.cfg` only after `grub-script-check` passes
  (lines 313-325).
- `/usr/share/initramfs-tools/init`: "Target filesystem doesn't have
  requested ${init}", then it tries `/sbin/init`, `/etc/init`,
  `/bin/init` and `/bin/sh`.
- `/usr/share/doc/openssh-server/README.Debian`: with socket activation,
  a new `Port` or `ListenAddress` needs `systemctl daemon-reload` and
  `systemctl restart ssh.socket`.
- `cmd/sc/edit.go:123`: the answer is read from stdin with no terminal
  check.

**A7. The lab runs and the disk.** 50 runs in the lab cache: 24 PASS,
18 INCONCLUSIVE (3 of them preflight refusals, 2 stopped), 6 FAIL, 2 with
no verdict. `/` has 6.3 GiB free, and the cache is 3.9 GB, 3.3 GB of it
53 run directories. UEFI vars are raw pflash (`lab/vm.py:1113-1114`), QMP
`hmp()` defaults to a 10 s timeout (`vm.py:1021`), `KEEP_RUNS=10`
(`lab.conf:56`, applied by `make lab-clean`), and there is one serial
chardev (`vm.py:1119`).
