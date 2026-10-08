#!/usr/bin/env python3
"""e2e.py: the M4 owner scenario in a QEMU VM, end to end (make lab-e2e).

One firmware mode per run, about 25 minutes under TCG, on a throwaway
overlay of the reference image (vm.py provision):

  boot 0  install   bin/sc, the units, the drop-in and 42_smartconfig go in
                    over ssh (lab/guest/install.sh); update-grub
  boot 1  normal    no menu; sc-boot-seen and sc-boot-ok record an ok boot.
                    The edit: a line for a disk that does not exist goes
                    into /etc/fstab; scd records it as BAD
  boot 2  broken    nothing is typed: the disk times out, local-fs.target
                    fails, emergency mode (outcome a, b or c); a hard reset
  boot 3  rescue    GRUB's menu shows by itself; "SmartConfig rescue" is
                    picked; sc's report stands above the prompt; Enter gives
                    a root shell; the report's commands put /etc/fstab back
  boot 4  healthy   the menu once more, left alone; the ok verdict clears it
  boot 5  normal    no menu again (--no-boot5 leaves it out); poweroff

Every check has an ID from the STEP11 design (section 4), a class ([M4]:
SmartConfig did not do what it should; [lab]: the lab could not tell) and
a strength (H: the mode stops; F: it fails, the mode goes on; W: a
warning). Known [lab] flakes (section 6) are retried, the whole boot
again after a reset; nothing else is.

  e2e.py --mode uefi|bios [--boot2 natural|reset-at-timeout] [--no-boot5] [--keep]

Exit status 0 PASS, 1 FAIL (an [M4] check failed), 3 INCONCLUSIVE (a [lab]
check failed, the retries ran out, or the time did). The run directory
(vm.py's cache, runs/<UTC>-<mode>-<git7>/) gets result.txt (a row per
check), ledger.json, the logs and evidence/. One line goes to stdout:

  PASS uefi 24m10s boot2=b goal3=multi-user boot5=yes retries=1 head=<sha> dirty=no sc=<12>

boot5=no says --no-boot5 left boot 5 out (make lab-e2e's last line says so too).

Progress goes to stderr and e2e.log. lab/README.md explains how to read a
failed run. Python 3 standard library only; dev use only, never shipped.
"""
import argparse
import collections
import contextlib
import hashlib
import json
import os
import re
import shutil
import sys
import threading
import time
import traceback

sys.dont_write_bytecode = True  # no lab/__pycache__ from the imports below

import console  # noqa: E402
import serialmux  # noqa: E402
import vm as labvm  # noqa: E402

LabError = labvm.LabError

REPO = labvm.REPO
HOST = labvm.GUEST_HOSTNAME
GUEST_DIR = "/home/%s/sclab" % labvm.GUEST_USER  # not /tmp: facts.sh runs from it in the rescue boot

# The edit (E.1) and how it shows. The console shortens unit names
# ("dev-d…6c1e2a-9b7d-4c1e-8f2a-5d6e7f8a9b0c"), so a match uses the tail.
BAD_UUID = "3f6c1e2a-9b7d-4c1e-8f2a-5d6e7f8a9b0c"
BAD_TAIL = BAD_UUID[-17:]
BAD_LINE = "UUID=%s /mnt/backup ext4 defaults 0 2" % BAD_UUID

RESCUE_TITLE = "SmartConfig rescue"
UBUNTU_TITLE = "Ubuntu"
# The README's GRUB password recipe (--grub-password, checks 6.x): a
# superuser and a password GRUB takes with a US layout, as qcodes too.
GRUB_USER, GRUB_PASSWORD = "admin", "sclabpw7"
GRUB_USER_RX, GRUB_PASS_RX = r"Enter username:", r"Enter password:"
# 6.1, as root in the guest: the README's steps, scripted. The lines
# grub.cfg marks --unrestricted are its output.
GRUB_RECIPE = r"""set -e
hash=$(printf '%s\n%s\n' @PW@ @PW@ | grub-mkpasswd-pbkdf2 | sed -n 's/^.* is \(grub\.pbkdf2\.[^ ]*\)$/\1/p')
[ -n "$hash" ]
printf 'set superusers="@USER@"\nexport superusers\npassword_pbkdf2 @USER@ %s\n' "$hash" >>/etc/grub.d/40_custom
sed -i '/gnulinux-simple/s/\${CLASS}/${CLASS} --unrestricted/' /etc/grub.d/10_linux
update-grub >/dev/null 2>&1
grep -n -- --unrestricted /boot/grub/grub.cfg
""".replace("@USER@", GRUB_USER).replace("@PW@", GRUB_PASSWORD)


def recipe_problems(rc, out, err):
    """6.1: the recipe ran (exit 0), and grub.cfg has exactly one
    --unrestricted line, Ubuntu's menuentry (the README's own check)."""
    lines = [l for l in out.split("\n") if "--unrestricted" in l]
    p = [] if rc == 0 else ["the recipe: exit %s: %s" % (rc, err.strip()[-200:])]
    if len(lines) != 1 or "menuentry 'Ubuntu'" not in lines[0]:
        p.append("grub.cfg's --unrestricted lines: %s" % (" | ".join(lines) or "none"))
    return p, lines
RESCUE_ECHO = "SmartConfig rescue: root read-only, /etc/fstab ignored"
RESCUE_ARGS = "fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1"
DROPIN_LINE = "ExecStartPre=-/usr/sbin/sc status --console"
FLAG_BLOCK = 'if [ "${smartconfig_pending}" = "1" ] ; then'
RECORDFAIL_BLOCK = re.compile(r'^if \[ "\$\{recordfail\}" = 1 \] ; then\n\s*set timeout=(\d+)', re.M)
RESTORE_REFUSED = "it is on a read-only file system: remount it read-write first"
GRUBENV_ERRORS = re.compile(r"no GRUB environment block|grub-editenv not found|grub-editenv (?:un)?set failed")
# The undo the rescue report must give, exactly (3.6), run one at a time (3.9).
UNDO = ("mount -o remount,rw /", "sc restore {GOOD}", "sync", "systemctl daemon-reload", "systemctl reboot")
MENU_ENTRIES = {
    "uefi": ["Ubuntu", "Advanced options for Ubuntu", "UEFI Firmware Settings", RESCUE_TITLE],
    "bios": ["Ubuntu", "Advanced options for Ubuntu", RESCUE_TITLE],
}

# What goes to the guest (0.4): the name in the staging directory, the
# source in the repo, the mode install.sh gives it, where it goes.
INSTALLED = (
    ("sc", "bin/sc", "755", ("/usr/sbin/sc",)),
    ("42_smartconfig", "scripts/42_smartconfig", "755", ("/etc/grub.d/42_smartconfig",)),
    ("41_sclab", "lab/guest/41_sclab", "755", ("/etc/grub.d/41_sclab",)),
    ("43_sclab", "lab/guest/43_sclab", "755", ("/etc/grub.d/43_sclab",)),
    ("scd.service", "scripts/scd.service", "644", ("/etc/systemd/system/scd.service",)),
    ("sc-boot-seen.service", "scripts/sc-boot-seen.service", "644", ("/etc/systemd/system/sc-boot-seen.service",)),
    ("sc-boot-ok.service", "scripts/sc-boot-ok.service", "644", ("/etc/systemd/system/sc-boot-ok.service",)),
    ("smartconfig-rescue.conf", "scripts/smartconfig-rescue.conf", "644",
     ("/etc/systemd/system/rescue.service.d/50-smartconfig.conf",
      "/etc/systemd/system/emergency.service.d/50-smartconfig.conf")),
)
HELPERS = (("install.sh", "lab/guest/install.sh"), ("facts.sh", "lab/guest/facts.sh"))

# Serial patterns (console.Console.expect: re.M, no re.S). A captured line
# ends in \n, so a line still arriving is not taken half.
LINUX_RX = r"Linux version (\S+)"
CMDLINE_RX = r"Command line: ([^\n]*)\n"
# The kernel says it once more, a moment later.
KCMDLINE_RX = r"Kernel command line: ([^\n]*)\n"
PANIC_RX = r"Kernel panic - not syncing|IO-APIC \+ timer doesn't work"
# The one panic that is TCG's (plan A, lab/testdata/q3c-panic-tcg.raw). Any
# other is not a known flake of the lab.
TCG_PANIC_RX = r"IO-APIC \+ timer doesn't work"
GRUB_PROMPT_RX = r"(?:^|\s)grub> "
COUNTDOWN_RX = r"The highlighted entry will be executed automatically in (\d+)s\."
BAD_TIME_RX = r"Timed out waiting for device [^\n]*" + re.escape(BAD_TAIL)
LOCALFS_DEPEND_RX = r"Dependency failed for local-fs\.target"
PROMPT_RX = r"Press Enter for maintenance"
PROMPT_END_RX = r"\(or press Control-D to continue\): "
LOGIN_RX = r"\b%s login: " % re.escape(HOST)
SHELL_RX = r"root@[\w.-]+:[^\n]*# "
SEEN_DONE_RX = r"Finished sc-boot-seen\.service"
INIT_RX = r"Run /init as init process"
LOG_ROW = re.compile(r"^([0-9a-f]{6})\s+(\d{4}-\d\d-\d\d \d\d:\d\d)\s+(\S+)\s+(\S+)\s+(\S+)(?:\s+(.*?))?\s*$")

EXIT = {"PASS": 0, "FAIL": 1, "INCONCLUSIVE": 3}


# ---------------------------------------------------------------- the checks

CheckSpec = collections.namedtuple("CheckSpec", "id cls strength channel title")

# ID, class, strength, channel, what it checks. Channels (design section 4):
# S serial text, V the VGA text screen (bios), K keys sent, SSH, C the
# serial root shell, M QMP; host is this machine. A strength given here can
# be changed per mode or outcome where the design says so (record()).
REGISTRY = collections.OrderedDict((r[0], CheckSpec(*r)) for r in (
    ("P.1", "lab", "H", "host", "tools: QEMU, qemu-img, ssh, scp, go; OVMF for uefi"),
    ("P.2", "lab", "H", "host", "bin/sc is statically linked, CGO_ENABLED=0, no sctest tag"),
    ("P.3", "lab", "H", "host", "HEAD, dirty and the artefacts' sha256 recorded (LAB_REQUIRE_CLEAN=1: clean tree)"),
    ("P.4", "lab", "H", "host", "flock on cache/lock; no qemu-system-x86 of this uid runs"),
    ("P.5", "lab", "H", "host", "the cached image's sha256 is the pin; this checkout's reference image exists"),
    ("0.1", "lab", "H", "M", "overlay on ref-<h>.qcow2 (0444); QEMU paused, mux and QMP attached, then cont"),
    ("0.2", "lab", "H", "S+SSH", "login prompt, then ssh"),
    ("0.3", "lab", "H", "SSH", "the guest starts clean: no sc, /boot is LABEL=BOOT, grubenv, root locked, GRUB settings"),
    ("0.4", "lab", "H", "SSH", "files copied and installed (modes, owner, sha256 = host's); units enabled; update-grub"),
    ("0.5", "M4", "H", "SSH", "update-grub adds the rescue entry; grub.cfg has it and the flag block as designed"),
    ("0.6", "M4", "H", "SSH", "both drop-ins; analyze verify silent; three units enabled; scd active"),
    ("0.7", "M4", "H", "SSH", "scd baseline; sc log has /etc/fstab; no menu flag; no boots file"),
    ("1.0", "lab", "H", "V", "bios: the VGA screen shows SeaBIOS (else every V check would pass vacuously)"),
    ("1.1", "M4", "H", "S|V", "no menu: observers pending=[] timeout=[0], post hidden; no GNU GRUB before the kernel"),
    ("1.2", "M4", "H", "S", "Command line is the default entry's (EXP_DEFAULT)"),
    ("1.3", "M4", "H", "SSH", "is-system-running --wait: running (degraded: W)"),
    ("1.4", "M4", "H", "SSH", "boots file: B1 seen, B1 ok R1 local-fs=active emergency=inactive rescue=inactive"),
    ("1.5", "M4", "H", "SSH", "sc-boot-seen and sc-boot-ok succeeded; no grubenv complaint in their journal"),
    ("1.6", "M4", "H", "SSH", "grubenv has no smartconfig_pending"),
    ("1.7", "M4", "H", "SSH", "sc status: this boot normal and healthy, scd running, exit 0"),
    ("1.8", "M4", "F", "SSH", "sc-boot-seen is ordered before no target but shutdown.target; critical-chain does not name it"),
    ("1.9", "M4", "H", "SSH", "scd baseline this boot; GOOD = newest fstab row; sc cat GOOD = /etc/fstab"),
    ("E.1", "lab", "H", "SSH", "the bad line appended to /etc/fstab (N, BAD_SHA)"),
    ("E.2", "M4", "H", "SSH", "scd records the edit: a new newest row, BAD, stable for 10 s"),
    ("E.3", "M4", "F", "SSH", "scd's journal: blocker fstab-source-missing, line N ... (BAD)"),
    ("E.4", "M4", "F", "SSH", "sc check exits 2"),
    ("E.5", "M4", "F", "SSH", "sc status: exit 2, the BAD row, sc restore GOOD, sync, next boot; no remount, no reboot"),
    ("E.6", "lab", "H", "SSH+M", "the reboot gives a guest RESET"),
    ("2.1", "M4", "H", "S|V", "no menu: observers pending=[]"),
    ("2.2", "M4", "H", "S", "Command line is EXP_DEFAULT"),
    ("2.3", "M4", "H", "S", "the bad device times out, then local-fs.target fails"),
    ("2.4", "M4", "H", "S+SSH", "outcome a (no ssh), b (ssh, emergency.target active) or c (ssh, inactive)"),
    ("2.5", "M4", "F", "S", "the emergency report matches console-emergency.golden (F in every outcome)"),
    ("2.6", "M4", "H", "SSH", "b/c: B2 seen, B2 bad local-fs!=active; grubenv smartconfig_pending=1"),
    ("2.7", "lab", "H", "M", "system_reset after the settle time gives a RESET"),
    ("3.0", "lab", "H", "K", "nothing was typed or keyed since boot 2's reset"),
    ("3.1", "M4", "H", "S|V", "observers pending=[1] -> post timeout=[30] style=[menu]; menu: one rescue entry, *Ubuntu, 30s"),
    ("3.2", "M4", "H", "K+S", "pick SmartConfig rescue and Enter: the kernel has fstab=no"),
    ("3.3", "M4", "H", "S|V", "GRUB echoes 'SmartConfig rescue: root read-only, /etc/fstab ignored' (W on bios)"),
    ("3.4", "M4", "H", "S", "Command line is the rescue entry's (EXP_RESCUE)"),
    ("3.5", "M4", "H", "S", "before the prompt: no bad-device wait, no local-fs failure"),
    ("3.6", "M4", "H", "S", "the report matches console-rescue-<outcome>.golden; 80 columns; 25 rows to the prompt"),
    ("3.7", "M4", "H", "C", "Enter gives a # prompt with no password"),
    ("3.8.ro", "M4", "H", "C", "rescue: / is mounted ro"),
    ("3.8.mounts", "M4", "F", "C", "rescue: /boot and /mnt/backup not mounted; rescue.target active"),
    ("3.8.units", "M4", "F", "C", "rescue: sc-boot-seen ConditionResult=no; sc-boot-ok never started"),
    ("3.8.boots", "M4", "F", "C", "rescue: no B3 line, B2 seen; K and B3 as the report said"),
    ("3.8.sc", "M4", "F", "C", "rescue: status --console 2, diff +UUID, cat GOOD, check 2, restore refused on ro"),
    ("3.8.hashes", "M4", "F", "C", "rescue: store, boots and fstab unchanged; no sc-check-*/sc-store-* left"),
    ("3.8.vcs1", "M4", "F", "C", "rescue: /dev/vcs1 (the screen) shows sc restore GOOD"),
    ("3.8.vga", "M4", "W", "V", "bios: the VGA screen shows sc restore GOOD"),
    ("3.9", "M4", "H", "C+M", "the report's commands, one at a time: rw, restore (PRE), fstab = FSTAB_SHA, reboot"),
    ("4.1", "M4", "H", "S|V", "pending=[1], a 30 s menu with *Ubuntu that times out by itself"),
    ("4.1.cmdline", "M4", "H", "S", "boot 4's Command line is EXP_DEFAULT (every attempt, the flag known or not)"),
    ("4.1.nokey", "lab", "H", "K", "no key was sent in boot 4 (every attempt)"),
    ("4.2", "M4", "H", "S+SSH", "no bad-device wait; local-fs active, emergency and rescue inactive"),
    ("4.3", "M4", "H", "SSH", "boots file: B4 seen, B4 ok R4 > R1; matches the ledger; no B3 line"),
    ("4.4", "M4", "H", "SSH", "no menu flag, no next_entry; /etc/fstab = FSTAB_SHA, no 3f6c1e2a"),
    ("4.5", "M4", "H", "SSH", "sc status as 1.7, for B4"),
    ("4.6", "M4", "F", "SSH", "the two newest fstab rows: restore from GOOD, pre-restore PRE = BAD_SHA; units as 1.5"),
    ("5.1", "M4", "H", "S|V", "no menu: pending=[]"),
    ("5.1.ok", "M4", "H", "SSH", "the boots file has B5 ok (every attempt, the flag known or not)"),
    ("5.1.notime", "M4", "H", "S", "no bad-device timeout in boot 5 (every attempt)"),
    ("5.2", "M4", "H", "SSH+M", "a delayed poweroff gives SHUTDOWN"),
    ("6.1", "M4", "H", "SSH", "--grub-password: the README's recipe; grub.cfg has one --unrestricted entry, Ubuntu's"),
    ("6.2", "M4", "H", "S|V+SSH", "--grub-password: the default boot asks for no password and comes up"),
    ("6.3", "M4", "H", "S|V", "--grub-password: the rescue entry asks for the superuser and the password"),
    ("6.4", "M4", "H", "S", "--grub-password: with them it boots (fstab=no), and its shell reboots"),
    ("6.5", "M4", "H", "S|V+SSH", "--grub-password: Ubuntu from the menu asks for none, and the boot is ok"),
    ("K.1", "lab", "H", "K", "no lone ESC was ever sent (design section 6)"),
    ("T.1", "lab", "H", "host", "nothing of the run is left: QEMU, its port, the reader threads"),
))


class Row:
    """One check's result. cause is why it failed: M4 or lab (a [lab]
    problem in an [M4] check, say QEMU died, makes it lab)."""

    FIELDS = ("id", "status", "cls", "strength", "channel", "evidence", "expected", "source", "seen", "notes")

    def __init__(self, cid, status, strength, cause="", evidence="", expected="", source="", seen="", notes=""):
        spec = REGISTRY[cid]
        self.id = cid
        self.status = status
        self.cls = spec.cls
        self.strength = strength
        self.channel = spec.channel
        self.cause = cause or spec.cls
        self.evidence = evidence
        self.expected = expected
        self.source = source
        self.seen = seen
        self.notes = notes
        self.t = time.time()

    def json(self):
        d = {k: getattr(self, k) for k in self.FIELDS}
        d["cause"] = self.cause
        d["t"] = round(self.t, 2)
        return d


def _cell(value, limit=400):
    s = " ".join(str(value).replace("\t", " ").split())
    return s if len(s) <= limit else s[:limit - 3] + "..."


def result_line(row):
    """A row of result.txt: tab-separated, one line."""
    cls = row.cls if row.cause == row.cls else "%s(%s)" % (row.cls, row.cause)
    return "\t".join(_cell(x) for x in (row.id, row.status, cls, row.strength, row.channel, row.evidence,
                                         row.expected, row.source, row.seen, row.notes))


def merge(old, new):
    """Which of two results of one check stands: a FAIL of SmartConfig's
    stays (a later attempt never hides it), a SKIP never replaces a
    result, otherwise the newer one."""
    if old is None:
        return new
    if old.status == "FAIL" and old.cause == "M4":
        return old
    if new.status == "SKIP" and old.status != "SKIP":
        return old
    return new


def verdict(rows, lab_error=None):
    """PASS, FAIL (an [M4] failure) or INCONCLUSIVE ([lab] failure, an
    error of the lab, or checks never run)."""
    if any(r.status == "FAIL" and r.cause == "M4" for r in rows):
        return "FAIL"
    if lab_error or any(r.status in ("FAIL", "NOTRUN") for r in rows):
        return "INCONCLUSIVE"
    return "PASS"


class Stop(Exception):
    """An H check failed: the mode stops here."""

    def __init__(self, row):
        super().__init__("%s %s: %s" % (row.id, row.status, row.seen))
        self.row = row


class Retry(Exception):
    """A known [lab] flake (design section 6): the boot again after a reset."""

    def __init__(self, sig, detail=""):
        super().__init__("%s: %s" % (sig, detail))
        self.sig = sig
        self.detail = detail


class SshLost(LabError):
    """ssh itself failed (exit 255), or its command ran out of time while
    the host stood still: nothing the guest said. Never an [M4] result."""


# Retries per boot by signature; the mode allows RETRIES_MODE in all.
MENU_SIGS = ("grub-prompt", "menu-missed")
# bios: the VGA screen is polled; a gap longer than this between two polls
# before the menu's first screen means its countdown cannot be held to
# 30 s, so the boot is retried ("vga-gap") rather than the range widened.
MAX_VGA_GAP = 2.0
PER_BOOT = {"menu": 1, "no-ssh": 1, "vga-gap": 2, "stall": 1}  # and RETRIES_PANIC for "panic"

# bios: one guest reboot gives two guest RESETs about 15 ms apart (the first
# bios run: QMP seq 3 and 4, nothing on serial or VGA between them), the
# kernel's reset and then the firmware's own. A guest RESET this soon after
# the one before it (QEMU's timestamps) is the same boot's start, not a
# reset of its own; one any later is (the boot's waits say so).
RESET_CHAIN = 2.0
# stall_dump samples QEMU twice, this many seconds apart.
STALL_SAMPLE = 5.0
# 2.5 waits this long for what ends boot 2's report on the console.
REPORT_WAIT = 30


def ovmf_problems(ref_json, ovmf_code):
    """P.5 (uefi): the reference image's VARS.fd was made under one
    OVMF_CODE; the json beside it has that file's sha256. A package update
    since then means another firmware under the old variables."""
    try:
        with open(ref_json) as f:
            want = json.load(f).get("ovmf_code_sha256")
    except (OSError, ValueError) as e:
        return ["%s: %s" % (ref_json, e)]
    got = labvm.sha256_file(ovmf_code)
    if want and got != want:
        return ["%s is %s, and the reference image was made under %s: make lab-image LAB_FORCE=1" % (ovmf_code, got[:12], want[:12])]
    return []


def stale_recordfail(env):
    """1.6, 4.4: grubenv's lines after a healthy boot may not have
    recordfail=1. Ubuntu's grub-common.service unsets it; sc's units write
    grubenv too, and a write of theirs that loses that unset (the chunk C
    review) leaves a menu at every boot on a machine with a recordfail
    timeout."""
    return ["grubenv: %s after a healthy boot (a lost unset)" % x for x in env if x.strip() == "recordfail=1"]


def last_line(text):
    """The last line of text that is not empty."""
    lines = text.strip().split("\n")
    return lines[-1].strip()


def qmp_time(ev):
    """A QMP event's time: QEMU's timestamp, else when the reader got it."""
    ts = ev.get("timestamp") or {}
    if "seconds" in ts:
        return ts["seconds"] + ts.get("microseconds", 0) / 1e6
    return ev.get("t", 0.0)


def chained_reset(cur, nxt, window=RESET_CHAIN):
    """nxt (the next RESET or SHUTDOWN after cur, or None) is a guest RESET
    within window s of cur: the boot cur began begins at nxt instead."""
    return (nxt is not None and nxt["event"] == "RESET" and bool((nxt.get("data") or {}).get("guest"))
            and 0 <= qmp_time(nxt) - qmp_time(cur) <= window)


def flag_after_retry(flag, sig, userspace=False):
    """The menu flag a boot can expect after a reset that ended an attempt
    with sig: True, False, or None (not known). sc-boot-seen sets it early
    in every boot and an ok verdict unsets it, so a boot that reached
    userspace (slow-udev, no-ssh) may have changed it; a panic, a stall,
    a grub> prompt, a missed menu or a VGA poll gap came before userspace.
    userspace: the attempt's console had the kernel's "Run /init"; a panic
    after that may have come after sc-boot-seen."""
    if sig == "panic" and userspace:
        return None
    if sig in ("panic", "stall", "vga-gap") + MENU_SIGS:
        return flag
    if sig == "slow-udev" and flag is True:
        return True  # seen sets it (if /boot was there) or leaves it set
    return None


# ---------------------------------------------------------------- parsing what the guest says


class Block:
    """One facts.sh block: its rc, its lines, and its lines' numbers (1-based)
    in the text it came from."""

    def __init__(self, name, rc, first):
        self.name = name
        self.rc = rc
        self.lines = []
        self.first = first
        self.last = first


def parse_facts(text):
    """facts.sh's output as {name: Block}. Lines before the first header
    (a console's echo of the command) are dropped."""
    blocks = collections.OrderedDict()
    cur = None
    for n, line in enumerate(text.split("\n"), 1):
        m = re.match(r"^== (\S+) rc=(-?\d+)\s*$", line)
        if m:
            cur = Block(m.group(1), int(m.group(2)), n)
            blocks[cur.name] = cur
            continue
        if cur is not None:
            cur.lines.append(line)
            cur.last = n
    for b in blocks.values():
        while b.lines and not b.lines[-1].strip():
            b.lines.pop()
    return blocks


class Facts:
    """facts.sh output saved as evidence: blocks by name, with helpers."""

    def __init__(self, text, evidence):
        self.text_all = text
        self.evidence = evidence  # the file, relative to the run directory
        self.blocks = parse_facts(text)

    def has(self, name):
        return name in self.blocks

    def rc(self, name):
        b = self.blocks.get(name)
        return None if b is None else b.rc

    def lines(self, name):
        b = self.blocks.get(name)
        return [] if b is None else b.lines

    def text(self, name):
        return "\n".join(self.lines(name))

    def need(self, *names):
        """Problems unless every block named is there with rc 0: a check
        never reads a missing or failed block as "nothing found"."""
        p = []
        for n in names:
            if n not in self.blocks:
                p.append("facts.sh printed no '%s' block" % n)
            elif self.blocks[n].rc != 0:
                p.append("facts.sh %s: exit %s: %s" % (n, self.blocks[n].rc, _cell(" | ".join(self.lines(n)[:3]), 200)))
        return p

    def kv(self, name):
        """KEY=VALUE lines as a dict (the first = splits)."""
        out = collections.OrderedDict()
        for line in self.lines(name):
            if "=" in line:
                k, v = line.split("=", 1)
                out.setdefault(k.strip(), v.strip())
        return out

    def ev(self, *names):
        spans = ["%d-%d" % (self.blocks[n].first, self.blocks[n].last) for n in names if n in self.blocks]
        return "%s:%s" % (self.evidence, ",".join(spans)) if spans else self.evidence


def parse_kv(text):
    """install.sh's key=value facts: {key: [values in order]}."""
    out = collections.OrderedDict()
    for line in text.split("\n"):
        if "=" in line and re.match(r"^[a-z_]+=", line):
            k, v = line.split("=", 1)
            out.setdefault(k, []).append(v)
    return out


def mounts_map(lines):
    """facts.sh's mounts block: {mountpoint: {SOURCE, LABEL, FSTYPE, OPTIONS} or None}."""
    out = {}
    for line in lines:
        parts = line.split(" ", 1)
        if len(parts) != 2:
            continue
        out[parts[0]] = None if parts[1].strip() == "-" else dict(re.findall(r'(\w+)="([^"]*)"', parts[1]))
    return out


def paths_map(lines):
    """facts.sh's paths block: {path: the rest of its line}."""
    out = {}
    for line in lines:
        parts = line.split(" ", 1)
        if len(parts) == 2:
            out[parts[0]] = parts[1].strip()
    return out


def show_units(lines):
    """systemctl show of several units (blank-line separated): {Id: {key: value}}."""
    out, cur = {}, {}
    for line in lines + [""]:
        if not line.strip():
            if cur.get("Id"):
                out[cur["Id"]] = cur
            cur = {}
            continue
        if "=" in line:
            k, v = line.split("=", 1)
            cur[k] = v
    return out


def log_rows(text):
    """sc log's rows: [(id, when, origin, file, size, what)], newest first."""
    rows = []
    for line in text.split("\n"):
        m = LOG_ROW.match(line)
        if m:
            rows.append(tuple(g or "" for g in m.groups()))
    return rows


def boots_entries(text):
    """The boots file as [(boot id, kind, rest)] in order (seen, ok, bad)."""
    out = []
    for line in text.split("\n"):
        f = line.strip().split(" ", 2)
        if len(f) >= 3 and f[1] in ("seen", "ok", "bad"):
            out.append((f[0], f[1], f[2]))
    return out


def boots_by_id(entries):
    """[(id, {kind: rest})] in the order each id first shows."""
    order, kinds = [], {}
    for bid, kind, rest in entries:
        if bid not in kinds:
            order.append(bid)
            kinds[bid] = {}
        kinds[bid][kind] = rest
    return [(b, kinds[b]) for b in order]


def failed_after(entries, good_id):
    """The boots after good_id's that have no ok verdict, as [(id, kinds)]."""
    ids = boots_by_id(entries)
    at = [i for i, (b, _) in enumerate(ids) if b == good_id]
    if not at:
        return None
    return [(b, k) for b, k in ids[at[0] + 1:] if "ok" not in k]


def ok_line_rx(boot_id):
    return re.compile(r"^%s ok \d+ (-?\d+) local-fs=active emergency=inactive rescue=inactive failed-units=\d+$"
                      % re.escape(boot_id), re.M)


def seen_line_rx(boot_id):
    return re.compile(r"^%s seen \d+$" % re.escape(boot_id), re.M)


def bad_line_rx(boot_id):
    return re.compile(r"^%s bad \d+ -?\d+ local-fs=(?!active)\S+ emergency=(active|inactive) rescue=inactive "
                      r"failed-units=-?\d+$" % re.escape(boot_id), re.M)


def norm_cmdline(s):
    return " ".join(s.split())


def grub_entries(cfg):
    """grub.cfg's top-level menu entries: [{title, id, body, pos}]."""
    out = []
    for m in re.finditer(r"^menuentry '((?:[^']|'\\'')*)'([^\n]*)\{\n(.*?)^\}", cfg, re.M | re.S):
        opts = m.group(2)
        mid = re.search(r"--id[ =]'?([\w.-]+)", opts) or re.search(r"\$menuentry_id_option '([^']*)'", opts)
        out.append({"title": m.group(1).replace("'\\''", "'"), "id": mid.group(1) if mid else "",
                    "body": m.group(3), "pos": m.start()})
    return out


def linux_args(body):
    """The kernel command line an entry gives, as the kernel prints it:
    BOOT_IMAGE=<file> and the arguments, single spaces. (None, 0) without
    a linux line; the count says how many there are."""
    lines = re.findall(r"^\s*linux\s+(\S+)([^\n]*)$", body, re.M)
    if not lines:
        return None, 0
    path, rest = lines[0]
    return norm_cmdline("BOOT_IMAGE=%s %s" % (path, rest)), len(lines)


def last_ro_rw(tokens):
    for t in reversed(tokens):
        if t in ("ro", "rw"):
            return t
    return None


def last_console(tokens):
    cons = [t for t in tokens if t.startswith("console=")]
    return cons[-1] if cons else None


def observer_problems(obs, platform, pending, post_timeout, style, recordfail=None):
    """What is wrong with the last pre and post observer lines of a boot.
    recordfail: what the pre line must show, "" after a clean boot (a
    healthy boot unsets it; one left set is a lost write to grubenv) or
    "1" where an earlier attempt of this boot was reset after GRUB had
    started its kernel; None: not checked (with the flag set). With "1",
    00_header sets no style, and without the flag the flag block must
    not either: style "hidden" then means none."""
    pre = [o for o in obs if o.get("phase") == "pre"]
    post = [o for o in obs if o.get("phase") == "post"]
    p = []
    if recordfail is not None:
        if pre and pre[-1].get("recordfail") != recordfail:
            p.append("pre recordfail=[%s], not [%s]" % (pre[-1].get("recordfail"), recordfail))
        if recordfail == "1" and style == "hidden":
            style = ""
    if not pre:
        p.append("no 'sclab: pre' line")
    else:
        for k, want in (("platform", platform), ("pending", pending), ("timeout", "0")):
            if pre[-1].get(k) != want:
                p.append("pre %s=[%s], not [%s]" % (k, pre[-1].get(k), want))
    if not post:
        p.append("no 'sclab: post' line")
    else:
        for k, want in (("timeout", post_timeout), ("style", style)):
            if post[-1].get(k) != want:
                p.append("post %s=[%s], not [%s]" % (k, post[-1].get(k), want))
    return p


def observers_seen(obs):
    """The last pre and post observer lines, short."""
    out = []
    for phase in ("pre", "post"):
        o = [x for x in obs if x.get("phase") == phase]
        if o:
            out.append("%s %s" % (phase, " ".join("%s=[%s]" % (k, v) for k, v in o[-1].items() if k != "phase")))
    return "; ".join(out) or "no observer line"


def menu_problems(menu, lines, countdowns=(30,)):
    """3.1/4.1: what is wrong with GRUB's menu as first drawn: problems
    and notes."""
    if menu is None:
        return ["no GRUB menu"], []
    p, notes = [], []
    if not any("GNU GRUB  version" in line for line in lines):
        p.append("no 'GNU GRUB  version' header")
    n = menu.entries.count(RESCUE_TITLE)
    if n != 1:
        p.append("%d entries titled %r, not 1" % (n, RESCUE_TITLE))
    if menu.entries[:1] != ["Ubuntu"] or menu.selected != 0:
        sel = menu.entries[menu.selected] if menu.selected is not None else None
        p.append("the highlight is on %r, not on the first entry Ubuntu" % sel)
    if menu.countdown not in countdowns:
        p.append("the countdown says %ss, not 30s" % menu.countdown)
    elif menu.countdown != 30:
        notes.append("first seen at %ss (the screen is polled)" % menu.countdown)
    sentence = "The highlighted entry will be executed automatically in %ss." % menu.countdown
    if not any(sentence in line for line in lines):
        p.append("no line %r" % sentence)
    return p, notes


def tolerate_report(lines):
    """2.5 only (the emergency report, F, W in outcome c): the report's
    lines as the golden has them, and notes (W) on what a real run of a
    boot that went on to multi-user may add: scd already running, or more
    changed files under the blocker's row (the golden has one). Never for
    3.6: see rescue_report_lines."""
    out, notes = [], []
    rows = None
    for line in lines:
        if re.match(r"^scd: +running \(pid \d+\)$", line):
            notes.append("scd was already running: %r" % line)
            out.append("scd:           not running")
            continue
        if line.startswith("Changed since "):
            rows = 0
            out.append(line)
            continue
        if rows is not None:
            if not line.strip():
                rows = None
            elif rows >= 1:
                notes.append("another row: %r" % line)
                continue
            else:
                rows += 1
        out.append(line)
    return out, notes


def rescue_report_lines(blk):
    """3.6: the rescue report's lines exactly as written. The one tolerance
    is the systemd status lines report_block took out of it (a W); scd
    running or a second change row is a mismatch, not a note."""
    notes = []
    if blk.stripped:
        notes.append("%d systemd status lines taken out: %s" % (len(blk.stripped), " | ".join(blk.stripped[:3])))
    return list(blk.lines), notes


# The reason the rescue report gives for the last failed boot, by boot 2's
# outcome (design 2.4, 3.6).
RESCUE_REASONS = {"a": "never reached multi-user", "b": "a mount failed, emergency mode", "c": "a mount failed"}


def rescue_golden(lines, outcome, bind, load=console.load_golden):
    """3.6: the rescue golden the report matches, held to boot 2's outcome
    (2.4). Outcome b or c (ssh saw boot 2): the reason must be that
    outcome's; when another golden matches instead, the rest of the report
    is sound and the disagreement is an F. Outcome a (no ssh: nothing says
    whether B2 got a verdict): golden a, b or c, a note, and 3.8.boots
    holds the reason to the boots file. Returns (letter matched or None,
    the GoldenResult, problems, strength for record(): None, "F" or "H",
    notes)."""
    want = outcome if outcome in ("b", "c") else "a"
    res = console.match_golden(lines, load("rescue-" + want), bind)
    if res.ok:
        return want, res, [], None, []
    for other in "abc":
        if other == want:
            continue
        r2 = console.match_golden(lines, load("rescue-" + other), bind)
        if not r2.ok:
            continue
        if want == "a":
            return other, r2, [], None, ["B2 has a verdict although boot 2 gave no ssh: golden rescue-%s "
                                         "(3.8.boots holds it to the boots file)" % other]
        return other, r2, ["the reason is %r (golden rescue-%s), but boot 2's outcome by ssh was %s, whose reason "
                           "is %r" % (RESCUE_REASONS[other], other, outcome, RESCUE_REASONS[want])], "F", []
    return None, res, list(res.problems), "H", []


def boots_reason(after):
    """The reason (a, b or c) sc status gives for the last failed boot in
    after (failed_after's list), as the boots file has it: no bad line (a),
    emergency=active (b) or not (c). None without a failed boot."""
    if not after:
        return None
    kinds = after[-1][1]
    if "bad" not in kinds:
        return "a"
    return "b" if " emergency=active " in " %s " % kinds["bad"] else "c"


# 2.4's ssh probe: boot 2's id, then emergency.target's and
# multi-user.target's states.
PROBE_CMD = "cat /proc/sys/kernel/random/boot_id; systemctl is-active emergency.target multi-user.target"


def parse_probe(rc, out):
    """2.4's probe as (boot id, emergency.target, multi-user.target), or
    None when ssh did not answer it."""
    f = out.split()
    if rc not in (0, 3) or len(f) < 3 or not re.match(r"^[0-9a-f-]{36}$", f[0]):
        return None
    return f[0], f[1], f[2]


def probe_outcome(probe):
    """2.4: b or c once boot 2 has reached multi-user.target, which sc boot
    verdict (sc-boot-ok, After=multi-user.target) needs to write B2's bad
    line: b with emergency.target active, else c. None until then: ssh
    can come up while the boot never gets there (the second bios run:
    the console getty hung up the emergency shell, systemd started
    default.target again, waited for the device once more and dropped
    back into emergency mode; plan A2's third ending)."""
    if probe is None or probe[2] != "active":
        return None
    return "b" if probe[1] == "active" else "c"


# The transient unit reboot_ssh's reboot is queued in, to ask about.
REBOOT_UNIT = "sclab-reboot"

# The console getty hung up boot 2's emergency shell (plan A2, A7):
# sulogin returned with nothing typed (3.0 holds boot 2 to no input) and
# systemd-sulogin-shell went on to default.target.
HANGUP_RX = re.compile(r"Reloading system manager configuration\.|^Starting default\.target", re.M)

# sc status --console ended by the getty's hang-up (B2, B3 of the step 11
# runs). Since cmd/sc/main.go's ignoreHangup it must outlive one: an [M4]
# failure, never a W.
SC_HUNGUP_RX = re.compile(r"^sc: interrupted by hangup", re.M)


def getty_hangup(text):
    """Evidence (lines) in boot 2's serial text that the console getty
    hung up the emergency shell: the shell returning after the first
    emergency prompt. [] if none."""
    first = text.find("Press Enter for maintenance")
    if first < 0:
        return []
    return [m.group(0) for m in HANGUP_RX.finditer(text) if m.start() > first]


# The getty's banner (/etc/issue) above its login prompt.
GETTY_BANNER_RX = re.compile(r"^\S.* %s tty\S+$" % re.escape(HOST))


GETTY_BANNER_M = re.compile(GETTY_BANNER_RX.pattern, re.M)
SHELL_LINE = "You are in emergency mode"


# The first line of the report's rest, after its header (This boot, Last
# healthy, Failed since, scd): a report slower than 2 s writes the header
# first and this later (cmd/sc/status.go consoleHead), so the getty can
# come between the two.
REPORT_REST_RX = re.compile(r"^(Changed since |The newest changes|Nothing recorded has changed|Nothing is recorded|sc: )", re.M)

# The getty's banner and its login prompt, from the banner's line (or
# the prompt's, without one) on.
GETTY_PIECE_RX = re.compile(r"(?:%s\n+)?.*%s" % (GETTY_BANNER_RX.pattern, LOGIN_RX), re.M)


def report_ends(text, pos):
    """What can end the report on the console, from pos: [(offset, kind)]."""
    return [(m.start(), kind) for kind, m in (("shell", re.compile(SHELL_LINE).search(text, pos)),
                                              ("banner", GETTY_BANNER_M.search(text, pos)),
                                              ("login", re.compile(LOGIN_RX).search(text, pos))) if m]


def emergency_report(text, final=False):
    """2.5: boot 2's report and how it ended: (console.Block, kind), or
    (None, None). It starts at the last "This boot:" above the emergency
    shell's first line (the first one, where the shell has none) and ends
    at the earliest of what can follow it on the console: that line
    ("shell"), the getty's banner ("banner") or its login prompt
    ("login"). The getty starts when it likes (plan A2): before the
    report, after it, or between it and the shell's line; or between the
    report's header and its rest, when sc wrote the header first (the
    chunk G review). The getty's lines are then taken out, with the blank
    lines it put before them; with the header alone so far, the rest is
    waited for. With none of them after the report yet there is no block,
    unless final (the wait for one is over): then it ends with the text
    ("end")."""
    shell = text.find(SHELL_LINE)
    s = text.rfind("This boot:", 0, shell) if shell >= 0 else text.find("This boot:")
    if s < 0:
        return None, None
    cuts, pos = [], s  # cuts: (start, end) of getty lines inside the report
    while True:
        ends = report_ends(text, pos)
        if not ends or min(ends)[1] == "shell" or REPORT_REST_RX.search(text, s, min(ends)[0]):
            break  # the shell's line, or the report came whole before the getty
        line = text.rfind("\n", 0, min(ends)[0]) + 1
        piece = GETTY_PIECE_RX.match(text, line)
        rest = REPORT_REST_RX.search(text, piece.end()) if piece else None
        if rest is None or 0 <= shell < rest.start():
            if final:
                break  # the report is the header alone
            return None, None  # the header so far: wait for the getty's prompt and the rest
        while line - 2 > s and text[line - 1] == text[line - 2] == "\n":
            line -= 1  # the getty's blank lines before its banner
        cuts.append((line, piece.end()))
        pos = piece.end()
    ends = report_ends(text, pos)
    if not ends and not final:
        return None, None
    e, kind = min(ends) if ends else (len(text), "end")
    body, at = [], s
    for a, b in cuts:
        body.append(text[at:a])
        at = b
    body.append(text[at:e])
    kept, stripped = [], []
    for line in "".join(body).split("\n"):
        (stripped if console.STATUS_LINE.match(line) else kept).append(line)
    if kind != "shell":
        while kept and not kept[-1].strip():
            kept.pop()
    return console.Block(kept, stripped, s, e, None), kind


# 3.8: the files facts.sh hashes that must be there (their -journal and
# -wal files may not be, then "-" both times).
HASHED = ("/var/lib/smartconfig/changes.db", "/var/lib/smartconfig/boots", "/etc/fstab")


def hashes_problems(before, after):
    """3.8.hashes: facts.sh's hashes-before and hashes-after lines: the same,
    and a sha256 (not "-") for each of HASHED."""
    p = []
    if before != after:
        p.append("hashes changed: %s -> %s" % (before, after))
    got = paths_map(before)
    for path in HASHED:
        if not re.match(r"^[0-9a-f]{64}$", got.get(path, "")):
            p.append("%s: no sha256 (%s)" % (path, got.get(path, "no line")))
    return p


def vga_evidence_problems(error, screens, obs, menu_shown):
    """bios: why the VGA screens cannot carry a boot's decision check (x.1),
    as [lab] problems (none when they can): the watch's poll failed, or
    its screens show neither an observer line nor a menu."""
    p = []
    if error:
        p.append("the VGA poll failed: %s" % error)
    if not obs and not menu_shown:
        p.append("no observer line and no GRUB menu on %d VGA screens" % screens)
    return p


def menu_countdowns(gap):
    """bios: the countdowns the first polled screen of a 30 s menu may show
    (countdowns_after); a Retry when the poll before it was more than
    MAX_VGA_GAP s earlier."""
    if gap is not None and gap > MAX_VGA_GAP:
        raise Retry("vga-gap", "%.1f s between VGA polls before the menu's first screen (at most %.1f): its "
                    "countdown cannot be held to 30 s" % (gap, MAX_VGA_GAP))
    return countdowns_after(gap)


def verdict_journal_rx(boot_id):
    """sc boot verdict's line in the sc-boot journal for an ok boot."""
    return re.compile(r"^boot %s: ok \(" % re.escape(boot_id), re.M)


def build_problems(bin_sha, fresh):
    """P.2: bin/sc against a fresh build of this tree with the Makefile's
    recipe (fresh: a vm.Result, sha256 or None)."""
    r, fresh_sha = fresh
    if r.rc != 0 or not fresh_sha:
        return ["a fresh build of this tree failed: exit %s: %s" % (r.rc, _cell(r.err or r.out, 300))]
    if bin_sha != fresh_sha:
        return ["bin/sc (%s) is not this tree's build (%s): make build" % (bin_sha[:12], fresh_sha[:12])]
    return []


def summary_line(v, mode, took, outcome, retries, head, dirty, sc, boot5, forced=False, grubpw=False):
    """The one line on stdout; make lab-e2e reads boot5=no, dirty=yes and
    boot2=a(forced) from it (forced: --boot2 reset-at-timeout, which
    leaves 2.5 and 2.6 out). grubpw=yes: --grub-password was given, so
    a PASS has 6.x passed too (a 6.x row never reached is NOTRUN, which
    makes a run INCONCLUSIVE)."""
    goal3 = {"a": "early-only", "b": "multi-user", "c": "multi-user"}.get(outcome, "-")
    return "%s %s %s boot2=%s%s goal3=%s boot5=%s retries=%s head=%s dirty=%s sc=%s%s" % (
        v, mode, took, outcome or "-", "(forced)" if forced and outcome else "", goal3, boot5, retries, head, dirty,
        sc or "-", " grubpw=yes" if grubpw else "")


def undo_commands(lines):
    """The commands the report gives (the two-space lines after "To put")."""
    cmds, inside = [], False
    for line in lines:
        if line.startswith("To put ") or line.startswith("To undo "):
            inside = True
            continue
        if inside:
            if line.startswith("  ") and line.strip():
                cmds.append(line.strip())
            elif cmds:
                break
    return cmds


def elf_is_static(data):
    """True for an ELF executable with no PT_INTERP and no PT_DYNAMIC
    segment (what file(1) calls statically linked)."""
    if data[:4] != b"\x7fELF" or data[4] != 2 or data[5] != 1:  # 64-bit, little-endian
        return False
    phoff = int.from_bytes(data[0x20:0x28], "little")
    phentsize = int.from_bytes(data[0x36:0x38], "little")
    phnum = int.from_bytes(data[0x38:0x3a], "little")
    if phentsize < 4 or phoff + phentsize * phnum > len(data):
        return False
    for i in range(phnum):
        p_type = int.from_bytes(data[phoff + i * phentsize:phoff + i * phentsize + 4], "little")
        if p_type in (2, 3):  # PT_DYNAMIC, PT_INTERP
            return False
    return True


def go_build_problems(text):
    """What `go version -m` says against a shipped binary."""
    p = []
    settings = dict(re.findall(r"^\s*build\s+(\S+?)=(.*)$", text, re.M))
    if settings.get("CGO_ENABLED") != "0":
        p.append("CGO_ENABLED=%s, not 0" % settings.get("CGO_ENABLED"))
    if "sctest" in settings.get("-tags", ""):
        p.append("built with -tags=%s" % settings["-tags"])
    return p


def whole_menu(rows):
    """rows show GRUB's whole menu: its entries and its countdown line."""
    m = console.parse_menu(rows)
    return m is not None and bool(m.entries) and m.countdown is not None


def countdowns_after(gap):
    """The countdowns a polled screen can show of a 30 s menu that was not
    there gap seconds before (None: not known): 30 down to 30 - ceil(gap)."""
    if gap is None:
        return (30,)
    return tuple(range(30, max(0, 30 - int(-(-gap // 1))) - 1, -1))


def duration(secs):
    secs = int(secs)
    return "%dm%02ds" % (secs // 60, secs % 60)


def in_order(lines, start, wanted):
    """The first of wanted (predicates with names) not found in lines after
    start, in order; None when all are there."""
    i = start
    for name, pred in wanted:
        while i < len(lines) and not pred(lines[i]):
            i += 1
        if i >= len(lines):
            return name
        i += 1
    return None


# ---------------------------------------------------------------- the VGA screen (bios)


class VgaWatch:
    """bios: the VGA text screen (pmemsave 0xb8000, 80x25) polled in a
    thread; each new screen is kept as rows and saved as evidence."""

    def __init__(self, machine, prefix, hz=2.0, keep=300):
        self.vm = machine
        self.prefix = prefix
        self.every = 1.0 / hz
        self.keep = keep
        self.screens = []  # (time, rows, file or None, start of the poll before)
        self.error = None
        self._lock = threading.Lock()
        self._stop = threading.Event()
        self._thread = None

    def start(self):
        self._thread = threading.Thread(target=self._run, name="vga", daemon=True)
        self._thread.start()
        return self

    def _run(self):
        tmp = self.prefix + ".poll"
        last = None
        before = None  # when the poll before this one began
        try:
            while not self._stop.is_set():
                t = time.time()
                try:
                    buf = self.vm.vga_dump(tmp)
                except Exception as e:  # QEMU gone: the boot's own waits say so
                    self.error = str(e)
                    return
                buf = buf[:4000]
                if len(buf) == 4000 and buf != last:
                    last = buf
                    rows = console.vgatext(buf)
                    with self._lock:
                        path = None
                        if len(self.screens) < self.keep:
                            path = "%s-%03d.bin" % (self.prefix, len(self.screens))
                            with open(path, "wb") as f:
                                f.write(buf)
                        self.screens.append((time.time(), rows, path, before))
                before = t
                self._stop.wait(max(0.0, self.every - (time.time() - t)))
        finally:
            with contextlib.suppress(OSError):
                os.unlink(tmp)

    def stop(self):
        self._stop.set()
        if self._thread is not None:
            self._thread.join(15)

    def alive(self):
        return self._thread is not None and self._thread.is_alive()

    def first(self, pred):
        """The first screen whose rows pred accepts: (rows, file, seconds
        it can have been up before this poll saw it), or None."""
        with self._lock:
            for t, rows, path, before in self.screens:
                if pred(rows):
                    return rows, path, (t - before) if before is not None else None
        return None

    def last_rows(self):
        with self._lock:
            return self.screens[-1][1] if self.screens else None

    def all_rows(self):
        with self._lock:
            return [r for _, rows, _, _ in self.screens for r in rows]

    def files(self):
        with self._lock:
            names = [os.path.basename(p) for _, _, p, _ in self.screens if p]
        if not names:
            return "no VGA screen"
        return "evidence/%s..%s (%d screens)" % (names[0], names[-1], len(names))


# ---------------------------------------------------------------- one boot attempt


class Attempt:
    """One attempt at one boot of the scenario: from the reset (its mark
    in the serial logs and its QMP event) to the next."""

    def __init__(self, label, n, mark, event, flag):
        self.label = label
        self.n = n
        self.mark = mark
        self.event = event
        self.since = (event["seq"] + 1) if event else 0  # QMP events after the one that began it
        self.flag = flag  # the menu flag expected at its start: True, False, None (not known)
        self.late = False  # an earlier attempt of this boot was reset after its kernel began (recordfail=1)
        self.vga = None
        self.vga2 = None
        self.boot_id = None
        self.result = None
        self.detail = ""
        self.t = time.time()

    def json(self):
        return {"boot": self.label, "attempt": self.n, "flag_expected": self.flag,
                "mark": self.mark._asdict() if self.mark else None,
                "event": self.event, "boot_id": self.boot_id, "result": self.result, "detail": self.detail}


# ---------------------------------------------------------------- the run


class E2E:
    def __init__(self, args, conf):
        self.args = args
        self.mode = args.mode
        self.uefi = args.mode == "uefi"
        self.conf = conf
        self.cache = labvm.ensure_cache()
        self.head, self.git7, self.dirty = labvm.git_head()
        self.run = labvm.new_run_dir(self.cache, self.mode, self.git7)
        self.evdir = os.path.join(self.run, "evidence")
        self._logf = open(os.path.join(self.run, "e2e.log"), "a", buffering=1)
        self.t_start = time.monotonic()
        self.deadline = self.t_start + max(60, conf.int("BUDGET_MODE") - 120)  # teardown fits before make's timeout
        self.rows = collections.OrderedDict()
        self.history = []
        self.v = collections.OrderedDict()  # GOOD, BAD, B1, N, ...: the ledger's values
        self.attempts = []
        self.retries = 0
        self.flag = False  # the menu flag the next boot expects
        self.ctx = "P.1"  # the check being worked on: a [lab] error lands on it
        self.lab_error = None
        self.machine = None
        self.mux = None
        self.con = None
        self.qmp = None
        self.key = None
        self.ref = None
        self.marks = {}
        self.cur = (None, None)
        self.sha = {}
        self.stage = None
        self.staged = False
        self.shell = False  # a root shell on the serial console (boot 3)
        self.at_prompt = False  # boot 3's "Press Enter for maintenance" is waiting
        self.boot2_mark = None
        self.settle_until = 0.0
        self.cur_vga = None  # bios: the VGA watch of the boot the last reset began
        self.vga_watches = []
        self.chained_resets = []  # [first seq, the seq a boot began at, seconds]: settle_reset
        self.torn_down = False

    # -- logging, evidence, results -----------------------------------------

    def log(self, msg):
        line = "[%s] %s" % (duration(time.monotonic() - self.t_start), msg)
        sys.stderr.write(line + "\n")
        sys.stderr.flush()
        self._logf.write(line + "\n")

    def evpath(self, name):
        return os.path.join(self.evdir, name)

    def save(self, name, text):
        path = self.evpath(name)
        with open(path, "w", encoding="utf-8", errors="surrogateescape") as f:
            f.write(text if text.endswith("\n") or not text else text + "\n")
        return "evidence/" + name

    def sev(self, start, end=None):
        """Evidence in serial.txt: the lines of byte offsets start to end."""
        a = self.con.line_no(start)
        b = self.con.line_no(end) if end is not None else a
        return "serial.txt:%d-%d" % (a, b)

    def at(self, cid):
        self.ctx = cid

    def record(self, cid, problems=(), warnings=(), seen="", expected="", source="", evidence="",
               strength=None, status=None, stop=True, notes=()):
        """Records a check's result and logs it. An H check that fails
        raises Stop (unless stop is false: the run is ending anyway); a W
        check that fails is a WARN. notes: what a healthy run shows too;
        they go into the row's NOTES and make no WARN."""
        spec = REGISTRY[cid]
        strength = strength or spec.strength
        problems, warnings = list(problems), list(warnings)
        if status is None:
            if problems:
                status = "WARN" if strength == "W" else "FAIL"
            elif warnings:
                status = "WARN"
            else:
                status = "PASS"
        notes = "; ".join(problems + warnings + list(notes))
        row = Row(cid, status, strength, spec.cls, evidence, expected, source, seen, notes)
        self.history.append(row.json())
        self.rows[cid] = merge(self.rows.get(cid), row)
        self.log("  %-10s %-5s %s%s" % (cid, status, _cell(seen, 120), (" | " + _cell(notes, 300)) if notes else ""))
        if stop and status == "FAIL" and strength == "H":
            raise Stop(row)
        return row

    def skip(self, cid, why):
        return self.record(cid, status="SKIP", seen=why)

    @staticmethod
    def stop_on(rows):
        """Stop for the first of rows (recorded with stop=False) that is an
        H failure."""
        for row in rows:
            if row.status == "FAIL" and row.strength == "H":
                raise Stop(row)

    def fail_lab(self, cid, why):
        """A [lab] failure while cid was being worked on (no Stop: the mode
        is stopping already). If cid has passed already, the failure goes on
        the next check in the registry that has no result."""
        ids = list(REGISTRY)
        if cid in self.rows and self.rows[cid].status in ("PASS", "WARN", "SKIP"):
            later = [c for c in ids[ids.index(cid) + 1:] if c not in self.rows]
            cid = later[0] if later else cid
        spec = REGISTRY.get(cid) or REGISTRY["P.1"]
        row = Row(spec.id, "FAIL", spec.strength, "lab", seen=why, notes="[lab] " + why)
        self.history.append(row.json())
        old = self.rows.get(spec.id)
        if old is None or not (old.status == "FAIL" and old.cause == "M4"):
            self.rows[spec.id] = row
        self.log("  %-10s FAIL  [lab] %s" % (spec.id, _cell(why, 300)))

    def left(self, budget):
        """budget, cut to what the mode has left; LabError when nothing is."""
        rest = self.deadline - time.monotonic()
        if rest <= 0:
            raise LabError("the mode's time (BUDGET_MODE=%s s) ran out" % self.conf["BUDGET_MODE"])
        return min(float(budget), rest)

    def b(self, key):
        return self.conf.int(key)

    def save_ledger(self, verdict_=None):
        data = {
            "mode": self.mode, "run": self.run, "head": self.head, "dirty": self.dirty,
            "artefacts": self.sha, "values": self.v, "flag_expected": self.flag,
            "retries": self.retries, "attempts": [a.json() for a in self.attempts],
            "chained_resets": self.chained_resets,
            "checks": self.history, "verdict": verdict_, "lab_error": self.lab_error,
            "boot2": self.args.boot2, "boot5": not self.args.no_boot5,
        }
        tmp = os.path.join(self.run, "ledger.json.part")
        with open(tmp, "w") as f:
            json.dump(data, f, indent=1, default=str)
            f.write("\n")
        os.replace(tmp, os.path.join(self.run, "ledger.json"))

    # -- the guest over ssh ---------------------------------------------------

    def ssh(self, command, timeout, input=None, lost=False, retry=False):
        """command in the guest; a vm.Result. ssh failing by itself (exit
        255), or a timeout while the host stood still (a mux gap), is an
        SshLost unless lost is true (the caller reads rc itself): a check
        must never take it for the guest's answer. retry: the command
        only reads, and one that ran out of time while the host stood
        still runs once more, with a fresh budget, before that (the chunk
        D review: a pause then costs the call, not the mode)."""
        gaps = len(self.mux.gaps)
        r = self._ssh_once(command, timeout, input)
        if retry and r.timed_out and len(self.mux.gaps) > gaps:
            gaps = len(self.mux.gaps)
            self.log("ssh: %s ran out of time while the host stood still; once more" % _cell(command, 80))
            r = self._ssh_once(command, timeout, input)
        if not lost and (r.rc == 255 or (r.timed_out and len(self.mux.gaps) > gaps)):
            raise SshLost("ssh %s: %s: %s" % ("failed (exit 255)" if r.rc == 255 else "ran out of time while the host stood still",
                                             _cell(command, 80), _cell(r.err, 200)))
        return r

    def _ssh_once(self, command, timeout, input):
        r = labvm.ssh(self.machine.ssh_port, self.key, command, self.left(timeout), input=input)
        with open(os.path.join(self.run, "ssh.log"), "a", encoding="utf-8", errors="replace") as f:
            f.write("== %s rc=%s %.1fs $ %s\n" % (duration(time.monotonic() - self.t_start), r.rc, r.secs, command))
            if r.out:
                f.write(r.out[-6000:] + ("" if r.out.endswith("\n") else "\n"))
            if r.err:
                f.write("-- stderr\n" + r.err[-3000:] + ("" if r.err.endswith("\n") else "\n"))
        return r

    def sudo(self, command, timeout=None, lost=False, retry=False):
        return self.ssh("sudo -n sh -c %s" % sh_quote(command), timeout or self.b("BUDGET_CMD"), lost=lost, retry=retry)

    def facts(self, tag):
        """sudo facts.sh normal, saved as evidence/facts-<tag>.txt."""
        r = self.ssh("sudo -n sh %s/facts.sh normal" % GUEST_DIR, 2 * self.b("BUDGET_CMD_SC"), retry=True)
        name = "facts-%s.txt" % tag
        ev = self.save(name, r.out + ("== ssh-stderr rc=%s\n%s" % (r.rc, r.err) if r.err.strip() else ""))
        if r.rc != 0 or "\n== end rc=0" not in "\n" + r.out:
            raise LabError("facts.sh normal (%s) exit %s: %s" % (tag, r.rc, r.err.strip()[-300:]))
        return Facts(r.out, ev)

    def poll(self, fn, budget, every=5.0):
        """fn() until it gives something true or budget s pass; its last
        value. A lost ssh call is "not yet"; if the last one was lost, its
        SshLost. Time the host stood still (the mux's gaps) is given back."""
        deadline = time.monotonic() + self.left(budget)
        gaps = len(self.mux.gaps)
        while True:
            lost = None
            try:
                v = fn()
            except SshLost as e:
                v, lost = None, e
            new = self.mux.gaps[gaps:]
            gaps += len(new)
            deadline = min(deadline + sum(g[1] for g in new), self.deadline)
            if v or time.monotonic() + every > deadline:
                if lost is not None:
                    raise lost
                return v
            time.sleep(every)

    def boots_text(self, strict=False):
        """The boots file over ssh; "" if it cannot be read. strict: ssh
        failing (255) is a LabError, never an empty file."""
        r = self.sudo("cat /var/lib/smartconfig/boots", lost=True, retry=True)
        if strict and r.rc == 255:
            raise LabError("ssh failed reading the boots file: %s" % r.err.strip()[-200:])
        return r.out if r.rc == 0 else ""

    def poll_verdict_journal(self, bid):
        """Until sc boot verdict's ok line for bid is in the journal (1.5,
        4.6 read it from facts.sh, which runs next)."""
        rx = verdict_journal_rx(bid)
        return self.poll(lambda: rx.search(self.sudo("journalctl -b -o cat --no-pager -t sc-boot").out),
                         self.b("BUDGET_POLL"))

    def boot_id(self):
        r = self.ssh("cat /proc/sys/kernel/random/boot_id", self.b("BUDGET_CMD"), lost=True)
        if r.rc == 255:
            # ssh's own failure: it only reads, so once more before it
            # ends the mode (a busy guest after update-grub: 526f14b).
            self.log("boot id: ssh failed (exit 255): %s; once more" % _cell(r.err, 120))
            r = self.ssh("cat /proc/sys/kernel/random/boot_id", self.b("BUDGET_CMD"))
        bid = r.out.strip()
        if r.rc != 0 or not re.match(r"^[0-9a-f-]{36}$", bid):
            raise LabError("no boot id over ssh: %r %r" % (r.out, r.err))
        return bid

    # -- serial ---------------------------------------------------------------

    def unexpected(self, a):
        """Why waiting on this boot makes no sense any more, or None."""
        if not self.machine.alive():
            return "QEMU exited (status %s)" % self.machine.proc.returncode
        evs = self.qmp.events_since(a.since, ("RESET", "SHUTDOWN"))
        if evs:
            return "the guest did %s by itself (%s)" % (evs[0]["event"], json.dumps(evs[0]["data"]))
        return None

    def wait_for(self, a, patterns, budget, start=None, what="it"):
        """The earliest of patterns in this boot's serial text from start
        (default: the boot's mark), a console.Hit; None after budget s. A
        panic is a Retry; QEMU gone or a reset nobody asked for a LabError."""
        pats = list(patterns) + [PANIC_RX]
        hit = self.con.expect(pats, self.left(budget), start=a.mark.txt if start is None else start,
                              advance=False, abort=lambda: self.unexpected(a) is not None)
        if hit is None:
            if self.con.why == "closed":
                raise LabError("the serial console closed while waiting for %s" % what)
            if self.con.why == "abort":
                raise LabError("%s, while waiting for %s" % (self.unexpected(a), what))
            return None
        if hit.index == len(pats) - 1:
            self.panicked(a, hit.text)
        return hit

    def text(self, a, end=None):
        return self.con.text(a.mark.txt, end)

    # -- QEMU, marks, transitions -------------------------------------------

    def _on_event(self, ev):
        # QMP's reader thread, before wait_event can return the event.
        if ev["event"] in ("RESET", "SHUTDOWN"):
            self.marks[ev["seq"]] = self.mux.mark("%s#%d" % (ev["event"], ev["seq"]))

    def new_boot(self, ev):
        """A reset began a boot: (bios) the VGA watch at once, so SeaBIOS's
        screen is seen; then the RESET the boot really begins at
        (settle_reset), its mark and its event. Returns that event; the
        caller's checks keep judging ev, the reset they caused."""
        if not self.uefi:
            w = VgaWatch(self.machine, self.evpath("vga-r%d" % ev["seq"]), hz=2.0).start()
            self.vga_watches.append(w)
            self.cur_vga = w
        ev = self.settle_reset(ev)
        mark = self.marks.get(ev["seq"]) or self.mux.mark("%s#%d" % (ev["event"], ev["seq"]))
        self.cur = (mark, ev)
        self.con.pos = mark.txt
        return ev

    def settle_reset(self, ev):
        """The RESET a boot begins at: ev, or the last guest RESET of those
        that followed it each within RESET_CHAIN s (chained_reset). Waits
        RESET_CHAIN s for the next one; three at most."""
        for _ in range(3):
            nxt = self.qmp.wait_event(("RESET", "SHUTDOWN"), since=ev["seq"] + 1, timeout=RESET_CHAIN,
                                      abort=lambda: not self.machine.alive())
            if not chained_reset(ev, nxt):
                return ev
            self.log("[lab] a second guest RESET %.0f ms after %s#%d (the firmware's own reset): the boot "
                     "begins at RESET#%d" % (1000 * (qmp_time(nxt) - qmp_time(ev)), ev["event"], ev["seq"], nxt["seq"]))
            self.chained_resets.append([ev["seq"], nxt["seq"], round(qmp_time(nxt) - qmp_time(ev), 3)])
            ev = nxt
        return ev

    def wait_reset(self, since, budget, names=("RESET",), what="a reset"):
        ev = self.qmp.wait_event(names, since=since, timeout=self.left(budget),
                                 abort=lambda: not self.machine.alive())
        if ev is None:
            raise LabError("no %s within %d s (QEMU %s)" % (what, budget, "runs" if self.machine.alive() else "exited"))
        return ev

    def reboot_ssh(self, cid, how="reboot"):
        """sync, then a reboot (or poweroff) in 3 s, over ssh; waits for the
        guest's RESET (SHUTDOWN). The event. Under TCG ssh can lose the
        guest, the command run or not (no answer to its keepalives, exit
        255: a bios run of 9f29ea5, after facts.sh). The guest is asked
        then, and the command sent once more only when it answers from the
        same boot with no reboot queued; else its RESET tells."""
        self.at(cid)
        since = self.qmp.mark()
        bid = self.boot_id()
        cmd = "sudo -n sync; sudo -n systemd-run --unit=%s --on-active=3 systemctl %s" % (REBOOT_UNIT, how)
        r = self.ssh(cmd, self.b("BUDGET_CMD"), lost=True)
        if r.rc == 255:
            q = self.ssh("cat /proc/sys/kernel/random/boot_id; systemctl is-active %s.timer %s.service"
                         % (REBOOT_UNIT, REBOOT_UNIT), self.b("BUDGET_CMD"), lost=True)
            got = q.out.split() if q.rc != 255 else []
            if got == [bid, "inactive", "inactive"]:
                self.log("%s: ssh lost the guest (exit 255) before the %s was queued: sent again" % (cid, how))
                r = self.ssh(cmd, self.b("BUDGET_CMD"), lost=True)
            else:
                self.log("%s: ssh lost the guest (exit 255); it says %s: waiting for its reset"
                         % (cid, " ".join(got) or "nothing"))
                r = None
        if r is not None and r.rc != 0:
            raise LabError("systemd-run systemctl %s: exit %s %s" % (how, r.rc, r.err.strip()))
        if how == "poweroff":
            return self.wait_reset(since, self.b("BUDGET_SHUTDOWN"), ("SHUTDOWN",), "SHUTDOWN after poweroff")
        ev = self.wait_reset(since, self.b("BUDGET_RESET"), ("RESET", "SHUTDOWN"), "RESET after reboot")
        if ev["event"] != "RESET":
            raise LabError("the reboot gave %s %s" % (ev["event"], json.dumps(ev["data"])))
        self.new_boot(ev)
        return ev

    def reset_vm(self, why):
        """A hard reset (QMP system_reset) and the boot it begins."""
        self.log("reset: %s" % why)
        since = self.qmp.mark()
        self.machine.system_reset()
        ev = self.wait_reset(since, 60, ("RESET",), "RESET after system_reset")
        self.new_boot(ev)
        return ev

    # -- the boots, with retries ---------------------------------------------

    def boot(self, label, fn):
        """fn(attempt) for the boot the last reset began; after a known
        [lab] flake, a reset and the whole boot again. The last attempt."""
        sigs = collections.Counter()
        n = 0
        late = False
        while True:
            n += 1
            mark, ev = self.cur
            a = Attempt(label, n, mark, ev, self.flag)
            a.late = late
            a.vga = None if self.uefi else self.cur_vga
            self.attempts.append(a)
            self.log("boot %s%s (menu flag expected: %s)" % (label, "" if n == 1 else ", attempt %d" % n,
                                                              {True: "set", False: "unset", None: "unknown"}[a.flag]))
            try:
                fn(a)
                a.result = "done"
                return a
            except (Stop, LabError):
                a.result = "stopped"
                raise
            except Retry as r:
                a.result = "retry"
                a.detail = "%s: %s" % (r.sig, r.detail)
                kind = "menu" if r.sig in MENU_SIGS else r.sig
                sigs[kind] += 1
                limit = dict(PER_BOOT, panic=self.b("RETRIES_PANIC")).get(kind, self.b("RETRIES_MODE"))
                if self.retries >= self.b("RETRIES_MODE") or sigs[kind] > limit:
                    raise LabError("boot %s: %s (%s); no retry left (%d in this mode)"
                                   % (label, r.sig, _cell(r.detail, 160), self.retries))
                self.retries += 1
                text = self.text(a)
                late = late or re.search(LINUX_RX, text) is not None
                self.flag = flag_after_retry(self.flag, r.sig, re.search(INIT_RX, text) is not None)
                self.log("boot %s: [lab] %s: %s; retry %d of %d" % (label, r.sig, _cell(r.detail, 160),
                                                                   self.retries, self.b("RETRIES_MODE")))
                self.stop_vga(a)
                self.reset_vm("retry boot %s after %s" % (label, r.sig))
            finally:
                self.stop_vga(a)
                self.save_ledger()

    def stop_vga(self, a):
        for w in (a.vga, a.vga2):
            if w is not None:
                w.stop()

    def panicked(self, a, line):
        """The kernel panicked. TCG's own panic (IO-APIC + timer) is the
        lab's flake: a Retry. Any other in the rescue boot is the kernel
        42_smartconfig's entry started with the arguments it wrote (a
        wrong root= ends in "VFS: Unable to mount root fs"): the current
        check failed, H. Anywhere else nothing says whose it is: a
        LabError, never retried."""
        text = self.text(a)
        if re.search(TCG_PANIC_RX, text):
            raise Retry("panic", line)
        m = re.search(r"Kernel panic - not syncing[^\n]*", text)
        what = "a kernel panic that is not TCG's: %s" % _cell(m.group(0) if m else line, 160)
        if a.label.startswith("3") or a.label == "6b":
            self.record(self.ctx, problems=["the rescue entry's kernel: " + what], source="scripts/42_smartconfig",
                        evidence=self.sev(a.mark.txt, self.con.size()), strength="H")
        raise LabError(what)

    def ran_out(self, cid, a, gaps, what):
        """A wait in the rescue boot ran out. With userspace up (the
        kernel's "Run /init") and no gap in the lab's own running since
        gaps (the mux's count before the wait), nothing says the lab held
        it up: sc's report runs in the unit's ExecStartPre, and one that
        never returns leaves no prompt. That is cid failed (H), not a
        LabError to run again."""
        if re.search(INIT_RX, self.text(a)) and len(self.mux.gaps) == gaps:
            self.record(cid, problems=[what], expected="within the budget; the host did not stand still",
                        source="scripts/smartconfig-rescue.conf", evidence=self.sev(a.mark.txt, self.con.size()),
                        strength="H")
        raise LabError(what)

    # A stall (README "Stalls"): the guest's console stops for a whole
    # budget where SmartConfig has no part in the boot yet. Either before
    # GRUB ran its configuration (42_smartconfig's flag block comes after
    # 41_sclab's line), or in the kernel before /init (sc is userspace).
    # A reset there loses nothing sc did, so the boot is retried.

    def grub_silent(self, a):
        """True when nothing of GRUB's configuration shows in this boot: no
        observer line and no menu, on serial (uefi) or on the VGA screens
        (bios; a failed or empty watch cannot tell, so False)."""
        if self.uefi:
            text = self.text(a)
            return not console.observer_lines(text) and "GNU GRUB" not in text
        w = a.vga
        if w is None or w.error or not w.screens:
            return False
        rows = w.all_rows()
        return not console.observer_lines(rows) and not any("GNU GRUB" in r for r in rows)

    def grub_stall(self, a, budget):
        """After a wait for GRUB ran out: a stall if GRUB showed nothing."""
        if self.grub_silent(a):
            self.stall(a, "nothing from GRUB %d s after the reset; the last line: %s"
                       % (budget, _cell(last_line(self.text(a)), 120)))

    def stall(self, a, what):
        raise Retry("stall", "%s (%s)" % (what, self.stall_dump(a, what)))

    def stall_dump(self, a, what):
        """Evidence of what stood still: the gaps in the lab's own running
        (a paused or starved host), QEMU's CPU time over 5 s and its
        registers before and after (a vCPU that spins, halts or moves)."""
        out = ["boot %s, attempt %d: %s" % (a.label, a.n, what),
               "mux gaps over %g s (seconds since t0, length): %s"
               % (serialmux.GAP, ", ".join("+%s %ss" % g for g in self.mux.gaps) or "none")]
        try:
            c0, r0 = self.machine.cpu_seconds(), self.qmp.hmp("info registers -a")
            time.sleep(STALL_SAMPLE)
            c1, r1 = self.machine.cpu_seconds(), self.qmp.hmp("info registers -a")
            out += ["QEMU used %.1f s of CPU in %g s (%s vCPUs)" % (c1 - c0, STALL_SAMPLE, self.conf["SMP"]),
                    "", "registers:", r0.rstrip(), "", "%g s later:" % STALL_SAMPLE, r1.rstrip()]
        except Exception as e:  # evidence only: the retry goes on without it
            out.append("no QEMU state: %r" % (e,))
        return self.save("stall-%s-%d.txt" % (a.label, a.n), "\n".join(out))

    def grub_phase(self, a, cid, menu, pick=None, pick_cid="3.2", login=None, asks_none=False):
        """The boot from its reset to the kernel: GRUB's observer lines and
        its menu. menu: True (a 30 s menu must show), False (none may) or
        None (not known after a retry: not checked; nothing is pressed).
        pick: the entry to boot (boot 3, and 6.x), its keys checked as
        pick_cid; otherwise a menu times out. login(a, g), after the
        Enter: GRUB's password prompts (6.3). asks_none: GRUB's username
        prompt instead of the kernel fails cid (6.2, 6.5)."""
        self.at(cid)
        g = {"obs": [], "menu": None, "menu_lines": [], "menu_ev": "", "kernel": None, "enter_pos": None,
             "menu_seen": False, "obs_ev": "", "keys": []}
        budget = self.b("BUDGET_MENU")
        if menu:
            if self.uefi:
                hit = self.wait_for(a, [COUNTDOWN_RX, LINUX_RX, GRUB_PROMPT_RX], budget, what="the GRUB menu")
                if hit is None:
                    self.grub_stall(a, budget)
                    if "GNU GRUB" in self.text(a):
                        self.record(cid, problems=["GRUB shows a menu with no countdown: it waits for a key for ever"],
                                    expected="a 30 s menu (smartconfig_pending=1)", source="42_smartconfig's flag block",
                                    evidence=self.sev(a.mark.txt, self.con.size()), strength="H")
                    raise LabError("no GRUB menu and no kernel %d s after the reset" % budget)
                if hit.index == 2:
                    raise Retry("grub-prompt", hit.text)
                if hit.index == 0:
                    text = self.text(a)
                    g["menu"] = console.parse_menu(text)
                    g["menu_lines"] = text.split("\n")
                    g["menu_ev"] = self.sev(a.mark.txt, hit.end)
            else:
                self.vga_menu_wait(a, g, budget)
                if g["menu"] is None:
                    # The serial text is whole, a polled screen is not: the
                    # menu counts as missing only if the observers, which a
                    # hidden menu leaves on the screen, say so.
                    # hidden, or a timeout of 0 with any style (recordfail
                    # after a reset leaves the style empty): GRUB drew none.
                    post = [o for o in console.observer_lines(a.vga.all_rows() if a.vga else []) if o["phase"] == "post"]
                    if not post or not (post[-1].get("style") == "hidden" or post[-1].get("timeout") == "0"):
                        raise Retry("menu-missed", "no menu on the VGA screen; observers: %s" % observers_seen(post))
            if g["menu"] is None:
                self.record(cid, problems=["no GRUB menu: the kernel started without one"],
                            expected="a 30 s menu (smartconfig_pending=1)", source="42_smartconfig's flag block",
                            evidence=g["menu_ev"] or self.sev(a.mark.txt, self.con.size()))
        if pick:
            if pick_cid == "3.2":
                self.check_30(a)
            self.pick(a, g, pick, pick_cid)
            if login is not None:
                login(a, g)
        start = g["enter_pos"] if g["enter_pos"] is not None else a.mark.txt
        if asks_none:
            hit = self.kernel_unless_asked(a, cid, start, budget)
        else:
            hit = self.wait_for(a, [LINUX_RX, GRUB_PROMPT_RX], budget, start=start, what="the kernel")
        if hit is None:
            if not pick:
                self.grub_stall(a, budget)
            else:
                after = self.text(a)[max(0, start - a.mark.txt):] if self.uefi else \
                    "\n".join(a.vga2.all_rows() if a.vga2 is not None else [])
                err = re.search(r"^\s*error: [^\n]*", after, re.M)
                if err:
                    self.record(pick_cid, problems=["the entry did not boot: %s" % err.group(0).strip()],
                                expected="the kernel of %s" % pick, source="scripts/42_smartconfig",
                                evidence=self.sev(start, self.con.size()) if self.uefi else a.vga2.files(), strength="H")
            raise LabError("no kernel %d s after the %s" % (budget, "Enter" if pick else "reset"))
        if hit.index == 1:
            raise Retry("grub-prompt", hit.text)
        g["kernel"] = hit
        if self.uefi:
            before = self.text(a, hit.start)
            g["obs"] = console.observer_lines(before)
            g["menu_seen"] = "GNU GRUB" in before
            g["obs_ev"] = self.sev(a.mark.txt, hit.start)
        else:
            if a.vga is not None:
                a.vga.stop()
            rows = a.vga.all_rows() if a.vga is not None else []
            g["obs"] = console.observer_lines(rows + list(g["menu_lines"]))
            g["menu_seen"] = any("GNU GRUB" in r for r in rows)
            g["obs_ev"] = a.vga.files() if a.vga is not None else "no VGA screen"
        return g

    def vga_menu_wait(self, a, g, budget):
        """bios: polls the VGA watch until a whole menu (with its countdown)
        is on the screen, or the kernel starts on serial without one."""
        deadline = time.monotonic() + self.left(budget)
        while True:
            hit = a.vga.first(whole_menu) if a.vga is not None else None
            if hit is not None:
                rows, path, gap = hit
                g["countdowns"] = menu_countdowns(gap)  # a Retry after a long gap
                g["menu"], g["menu_lines"] = console.parse_menu(rows), rows
                g["menu_ev"] = "evidence/" + os.path.basename(path) if path else a.vga.files()
                self.screendump("menu-%s.png" % a.label)
                return
            rows = a.vga.last_rows() if a.vga is not None else None
            text = self.text(a)
            if re.search(LINUX_RX, text):
                g["menu_ev"] = a.vga.files() if a.vga is not None else ""
                return
            if re.search(PANIC_RX, text):
                self.panicked(a, "before the menu")
            if re.search(GRUB_PROMPT_RX, "\n".join(rows or [])):
                raise Retry("grub-prompt", "on the VGA screen")
            why = self.unexpected(a) or (a.vga.error if a.vga is not None else "no VGA watch")
            if why:
                raise LabError("%s, while waiting for the GRUB menu" % why)
            if time.monotonic() > deadline:
                self.grub_stall(a, budget)
                raise LabError("no GRUB menu on the VGA screen and no kernel %d s after the reset" % budget)
            time.sleep(0.25)

    def screendump(self, name):
        """bios: the display as PNG evidence (best effort)."""
        ppm = self.evpath(name[:-4] + ".ppm")
        try:
            self.machine.screendump(ppm)
            deadline = time.monotonic() + 5
            data = b""
            while time.monotonic() < deadline:
                with contextlib.suppress(OSError):
                    with open(ppm, "rb") as f:
                        data = f.read()
                try:
                    png = console.ppm_to_png(data)
                    break
                except ValueError:
                    time.sleep(0.2)
            else:
                return
            with open(self.evpath(name), "wb") as f:
                f.write(png)
        except LabError as e:
            self.log("screendump %s: %s" % (name, e))
        finally:
            with contextlib.suppress(OSError):
                os.unlink(ppm)

    def check_30(self, a):
        """3.0: nothing typed or keyed since boot 2's reset (since this
        attempt's reset on a retried one), up to the menu."""
        self.at("3.0")
        start = self.boot2_mark.inp if (a.n == 1 and self.boot2_mark is not None) else a.mark.inp
        got = self.mux.inputs(start)
        self.record("3.0", problems=["%d inputs: %s" % (len(got), ", ".join("%s %s" % (e.kind, e.data) for e in got[:5]))]
                    if got else [], seen="%d inputs since input.log entry %d" % (len(got), start),
                    expected="none", source="design 6: nothing is typed in boot 2", evidence="input.log")

    def pick(self, a, g, target, cid="3.2"):
        """cid's keys: move the highlight to target, closed loop, then Enter."""
        self.at(cid)
        key_timeout = self.b("BUDGET_MENU_KEY")
        if self.uefi:
            read, press = console.serial_menu(self.con, a.mark.txt)
        else:
            if a.vga is not None:
                a.vga.stop()
            dump = self.evpath("vga-pick-%s.bin" % a.label)
            read, press = console.vga_menu(lambda: self.machine.vga_dump(dump), self.machine.sendkey, self.mux.note)
        try:
            console.pick(target, read, press, key_timeout=key_timeout, resends=1)
        except console.MenuError as e:
            g["keys"] = e.keys
            if e.menu is not None and e.menu.grub_prompt:
                raise Retry("grub-prompt", str(e))
            if e.menu is None or e.menu.gone or "did not move" in str(e) or "highlighted" in str(e):
                raise Retry("menu-missed", str(e))
            self.record(cid, problems=[str(e)], seen="keys %s" % e.keys, evidence=g["menu_ev"])
        g["keys"] = [e.data for e in self.mux.inputs(a.mark.inp)]
        g["enter_pos"] = self.con.size()
        if self.uefi:
            if not self.con.key("enter"):
                raise LabError("could not send Enter on serial")
        else:
            self.mux.note("sendkey", console.VGA_KEYS["enter"])
            self.machine.sendkey(console.VGA_KEYS["enter"])
            a.vga2 = VgaWatch(self.machine, self.evpath("vga-enter-%s-%d" % (a.label, a.n)), hz=4.0).start()
            self.vga_watches.append(a.vga2)

    def kernel_cmdline(self, a, g, what):
        hit = self.wait_for(a, [CMDLINE_RX], self.b("BUDGET_KERNEL_LOGIN"), start=g["kernel"].start,
                            what="the kernel's Command line (%s)" % what)
        if hit is None:
            raise LabError("no 'Command line:' from the kernel")
        return hit

    def cmdline_seen(self, a, hit, expected):
        """The command line the kernel booted with, and notes. Serial can
        lose a byte: the kernel's early console waits only so long for
        the UART, and a starved host leaves QEMU's side full (a bios run
        of 85e9f59 read "oot=UUID=" in the first line, with a 379 s gap
        of the host before it). The kernel prints the line twice. Where
        the first differs from expected and the second is expected whole,
        the channel lost it, not GRUB; both wrong is the command line."""
        seen = norm_cmdline(hit.group(1))
        if seen == expected:
            return seen, []
        again = self.wait_for(a, [KCMDLINE_RX], self.b("BUDGET_POLL"), start=hit.end,
                              what="the kernel's second 'Kernel command line:'")
        if again is not None and norm_cmdline(again.group(1)) == expected:
            return expected, ["serial lost part of the first 'Command line:' (%r); 'Kernel command line:' has it whole"
                              % _cell(seen, 200)]
        return seen, []

    def check_cmdline(self, cid, a, g, expected, hit=None):
        hit = hit or self.kernel_cmdline(a, g, cid)
        seen, notes = self.cmdline_seen(a, hit, expected)
        self.record(cid, problems=[] if seen == expected else ["Command line differs"], notes=notes, seen=seen,
                    expected=expected, source="0.5 grub.cfg", evidence=self.sev(hit.start, hit.end))
        return hit

    def check_decision(self, cid, a, g, flag, extra_problems=(), extra_warnings=(), seen_extra="", stop=True):
        """x.1: what GRUB decided. The observers' lines (H on uefi, W on
        bios, where a menu wipes them), and a 30 s menu with *Ubuntu
        (flag set) or no menu at all (flag unset). On bios the VGA screens
        are the only evidence: a failed poll, or screens with neither an
        observer line nor a menu, is a [lab] failure, never a pass."""
        if not self.uefi:
            w = a.vga
            lp = vga_evidence_problems("no VGA watch for this boot" if w is None else w.error,
                                       len(w.screens) if w is not None else 0, g["obs"],
                                       g["menu"] is not None or g["menu_seen"])
            if lp:
                raise LabError("%s: the VGA screen cannot tell what GRUB did: %s (%s)"
                               % (cid, "; ".join(lp), w.files() if w is not None else "no VGA screen"))
        problems, warnings, notes = list(extra_problems), list(extra_warnings), []
        obs = observer_problems(g["obs"], "efi" if self.uefi else "pc", "1" if flag else "",
                                "30" if flag else "0", "menu" if flag else "hidden",
                                None if flag else "1" if a.late else "")
        # bios: a menu that showed wiped the observers' lines, as it must.
        wiped = not self.uefi and flag and g["menu"] is not None and not g["obs"]
        (problems if self.uefi else notes if wiped else warnings).extend(("observers: " + o) for o in obs)
        if flag:
            mp, mnotes = menu_problems(g["menu"], g["menu_lines"], g.get("countdowns", (30,)))
            problems += mp
            warnings += mnotes
            m = g["menu"]
            if m is not None and m.entries != MENU_ENTRIES[self.mode]:
                warnings.append("entries %s, expected %s" % (m.entries, MENU_ENTRIES[self.mode]))
            menu_seen = ("menu %s, *%s, %ss" % (m.entries, m.entries[m.selected] if m.selected is not None else None,
                                                m.countdown)) if m else "no menu"
        else:
            if g["menu_seen"]:
                problems.append("GNU GRUB (a menu) showed before the kernel")
            menu_seen = "no menu" if not g["menu_seen"] else "a menu"
        return self.record(
            cid, problems=problems, warnings=warnings, notes=notes, stop=stop,
            seen="%s; %s%s" % (observers_seen(g["obs"]), menu_seen, seen_extra),
            expected=("pending=[1] -> timeout=[30] style=[menu]; a 30 s menu, *Ubuntu, one %s" % RESCUE_TITLE)
            if flag else "pending=[] recordfail=[%s] timeout=[0] -> style=[%s]; no GNU GRUB before the kernel"
            % (("1", "") if a.late else ("", "hidden")),
            source="41_sclab/43_sclab, 42_smartconfig", evidence="%s %s" % (g["obs_ev"], g["menu_ev"]))

    def boff(self, a, text, pos):
        """A str offset in this boot's text as a byte offset in serial.txt."""
        return a.mark.txt + len(text[:pos].encode("utf-8", "surrogateescape"))

    def boot_end(self, a):
        """This boot's text and how it ended (console.classify_boot), read
        from the kernel's first line: what the boot before it still
        printed after this one's mark (its "reboot: Restarting system") is
        no end of this boot. The offsets are the text's own."""
        text = self.text(a)
        k = re.search(LINUX_RX, text)
        return text, console.classify_boot(" " * k.start() + text[k.start():] if k else text, host=HOST, bad=BAD_TAIL)

    def wait_healthy_end(self, a, cid):
        """The boot from the kernel to its login prompt. A known [lab] flake
        (slow-udev, panic, grub>) is a Retry; any other end fails cid (H)."""
        deadline = time.monotonic() + self.left(self.b("BUDGET_KERNEL_LOGIN"))
        while True:
            size = self.mux.size()
            text, end = self.boot_end(a)
            if end is not None:
                if end.kind == "login":
                    return end
                if end.retry == "panic":
                    self.panicked(a, end.line.strip())
                if end.retry:
                    raise Retry(end.retry, end.line.strip())
                self.record(cid, problems=["the boot ended in %s: %s" % (end.kind, end.line.strip())],
                            seen="devices timed out: %s" % (end.devices or "none"), expected="a login prompt",
                            evidence=self.sev(self.boff(a, text, end.pos)), strength="H")
            why = self.unexpected(a)
            if why:
                raise LabError(why + ", before the login prompt")
            if time.monotonic() > deadline:
                if not re.search(INIT_RX, text):
                    self.stall(a, "the kernel did not reach /init in %d s; its last line: %s"
                               % (self.b("BUDGET_KERNEL_LOGIN"), _cell(last_line(text), 120)))
                raise LabError("no login prompt %d s after the kernel" % self.b("BUDGET_KERNEL_LOGIN"))
            self.mux.wait(size, 1.0)

    def wait_ssh(self, a):
        r = labvm.wait_ssh(self.machine.ssh_port, self.key, self.left(self.b("BUDGET_LOGIN_SSH")), interval=5,
                           abort=lambda: not self.machine.alive())
        if r is None:
            if not self.machine.alive():
                raise LabError("QEMU exited while waiting for ssh")
            raise Retry("no-ssh", "a login prompt, but no ssh in %d s" % self.b("BUDGET_LOGIN_SSH"))
        a.boot_id = self.boot_id()
        return a.boot_id

    def system_running(self, cid):
        r = self.ssh("systemctl is-system-running --wait", self.b("BUDGET_POLL"))
        state = r.out.strip().split("\n")[-1] if r.out.strip() else "(%s)" % r.err.strip()
        problems, warnings = [], []
        if state == "degraded":
            failed = self.ssh("systemctl --failed --no-legend --plain", self.b("BUDGET_CMD"))
            warnings.append("degraded; failed units: %s" % " | ".join(failed.out.strip().split("\n")))
        elif state != "running":
            problems.append("is-system-running says %s" % state)
        return state, problems, warnings

    # -- preflight and boot 0 -----------------------------------------------

    def preflight(self, stack):
        self.at("P.1")
        need = [self.conf["QEMU"], "qemu-img", "ssh", "scp", "go", "make"]
        missing = [t for t in need if not shutil.which(t)]
        if self.uefi:
            missing += [self.conf[k] for k in ("OVMF_CODE", "OVMF_VARS") if not os.path.exists(self.conf[k])]
        self.record("P.1", problems=["missing: %s" % ", ".join(missing)] if missing else [],
                    seen="%s found" % ", ".join(need), expected="all present", source="lab/README.md")

        self.at("P.2")
        self.check_p2()

        self.at("P.3")
        self.stage_files()
        dirty = "yes" if self.dirty else "no"
        problems = []
        if self.dirty and os.environ.get("LAB_REQUIRE_CLEAN") == "1":
            problems.append("the tree is dirty and LAB_REQUIRE_CLEAN=1")
        if self.sha.get("sc") != self.v.get("sc_fresh_sha256"):
            problems.append("the staged sc (%s) is not the build P.2 checked (%s)"
                            % (self.sha.get("sc", "")[:12], (self.v.get("sc_fresh_sha256") or "")[:12]))
        self.v["head"] = self.head
        self.record("P.3", problems=problems, seen="HEAD %s dirty=%s sc %s" % (self.head, dirty, self.sha.get("sc", "")[:12]),
                    expected="recorded", source="design 4 preflight", evidence="evidence/artefacts.sha256")

        self.at("P.4")
        try:
            stack.enter_context(labvm.lab_lock(self.cache))
            self.record("P.4", seen="lock %s held; no qemu-system-x86 of uid %d" % (os.path.join(self.cache, "lock"),
                                                                                  os.getuid()))
        except LabError as e:
            self.record("P.4", problems=[str(e)])

        self.at("P.5")
        problems = []
        try:
            labvm.check_image(self.conf, self.cache)
            self.ref = labvm.find_ref(self.conf, self.cache)
            self.key, _ = labvm.key_paths(self.cache)
            for k in ("qcow2", "vars", "json"):
                mode = os.stat(self.ref[k]).st_mode & 0o777
                if mode != 0o444:
                    problems.append("%s is %o, not 444" % (self.ref[k], mode))
            problems += ovmf_problems(self.ref["json"], self.conf["OVMF_CODE"]) if self.uefi else []
        except LabError as e:
            problems.append(str(e))
        self.record("P.5", problems=problems, seen="image %s; ref-%s" % (self.conf["IMAGE_SHA256"][:12],
                                                                        self.ref["h12"] if self.ref else "?"),
                    expected="sha256 = IMAGE_SHA256", source="lab.conf")

    def check_p2(self, sc=None):
        """P.2: bin/sc is static, CGO_ENABLED=0, no sctest, and this tree's
        build: byte for byte what the Makefile's build recipe makes of the
        tree now, into the run directory (Go builds with -trimpath are
        reproducible), so a stale bin/sc never goes into the guest."""
        sc = sc or os.path.join(REPO, "bin", "sc")
        problems = []
        try:
            with open(sc, "rb") as f:
                data = f.read()
        except OSError as e:
            data = b""
            problems.append("no bin/sc (%s): make build" % e.strerror)
        if data and not elf_is_static(data):
            problems.append("bin/sc is not statically linked")
        gv = labvm.run_timed(["go", "version", "-m", sc], 60) if data else None
        if gv is not None:
            self.save("go-version-m.txt", gv.out + gv.err)
            problems += ["go version -m: exit %s" % gv.rc] if gv.rc != 0 else go_build_problems(gv.out)
        bin_sha = hashlib.sha256(data).hexdigest() if data else ""
        if data:
            out = os.path.join(self.run, "sc.fresh")
            # The recipe as the Makefile has it: no MAKEFLAGS of a make
            # lab-e2e this runs under (its overrides, its jobserver).
            r = labvm.run_timed(["env", "-u", "MAKEFLAGS", "-u", "MFLAGS", "-u", "MAKELEVEL", "make", "-s", "-C", REPO,
                                 "build", "BIN=" + out], 600)
            fresh_sha = None
            with contextlib.suppress(OSError):
                with open(out, "rb") as f:
                    fresh_sha = hashlib.sha256(f.read()).hexdigest()
                os.unlink(out)
            problems += build_problems(bin_sha, (r, fresh_sha))
            self.save("build-check.txt", "bin/sc   %s\nsc.fresh %s\n$ %s\nexit %s\n%s%s" % (
                bin_sha, fresh_sha or "-", " ".join(r.argv), r.rc, r.out, r.err))
            if not problems:
                self.v["sc_fresh_sha256"] = fresh_sha
        self.record("P.2", problems=problems, seen="%s: static ELF, go version -m checked, sha256 %s = a fresh build"
                    % (sc, bin_sha[:12]) if not problems else sc,
                    expected="static, CGO_ENABLED=0, no sctest; the Makefile's build of this tree",
                    source="CLAUDE.md, Makefile build",
                    evidence="evidence/go-version-m.txt evidence/build-check.txt" if gv is not None else "")

    def stage_files(self):
        """The staging directory (run/stage): the files 0.4 installs, the two
        helpers, and MANIFEST in sha256sum's format."""
        self.stage = os.path.join(self.run, "stage")
        os.makedirs(self.stage, 0o700, exist_ok=True)
        lines = []
        for name, src in [(n, s) for n, s, _, _ in INSTALLED] + list(HELPERS):
            with open(os.path.join(REPO, src), "rb") as f:
                data = f.read()
            with open(os.path.join(self.stage, name), "wb") as f:
                f.write(data)
            self.sha[name] = hashlib.sha256(data).hexdigest()
            lines.append("%s  %s\n" % (self.sha[name], name))
        with open(os.path.join(self.stage, "MANIFEST"), "w") as f:
            f.writelines(lines)
        self.save("artefacts.sha256", "".join(lines) + "# HEAD %s dirty=%s\n" % (self.head, "yes" if self.dirty else "no"))

    def start_vm(self):
        """0.1: the overlay, QEMU paused, the mux and QMP attached, then cont.
        QEMU starts from the main thread (PR_SET_PDEATHSIG follows it)."""
        self.at("0.1")
        problems = []
        disk, vars_path = labvm.make_overlay(self.ref, self.run, self.mode)
        info = labvm.image_info(disk)
        with open(self.evpath("qemu-img-info.json"), "w") as f:
            json.dump(info, f, indent=1)
        want = os.path.relpath(self.ref["qcow2"], self.run)
        if info.get("backing-filename") != want:
            problems.append("backing file %s, not %s" % (info.get("backing-filename"), want))
        self.machine = labvm.Vm(self.conf, self.mode, self.run, disk, vars_path, log=self.log)
        self.machine.start()  # the main thread: PR_SET_PDEATHSIG follows it
        self.qmp = self.machine.qmp
        status = self.qmp.cmd("query-status")
        if status.get("running") or status.get("status") != "prelaunch":
            problems.append("QEMU is not paused: %s" % status)
        self.mux = serialmux.Mux(self.run, "serial.sock", t0=self.machine.t0).start()
        self.con = console.Console(mux=self.mux)
        self.qmp.listeners.append(self._on_event)
        self.cur = (self.mux.mark("START"), None)
        if not self.uefi:
            self.cur_vga = VgaWatch(self.machine, self.evpath("vga-start"), hz=2.0).start()
            self.vga_watches.append(self.cur_vga)
        self.machine.cont()
        running = self.qmp.cmd("query-status")
        if not running.get("running"):
            problems.append("cont did not start the machine: %s" % running)
        self.record("0.1", problems=problems,
                    seen="backing %s (0444), -S %s, then %s; ssh 127.0.0.1:%d" % (
                        info.get("backing-filename"), status.get("status"), running.get("status"),
                        self.machine.ssh_port),
                    expected="backing %s; prelaunch, then running" % want, source="design 5",
                    evidence="evidence/qemu-img-info.json qemu.log qmp.log")

    def boot0(self, a):
        g = self.grub_phase(a, "0.2", menu=None)
        self.at("0.2")
        self.wait_healthy_end(a, "0.2")
        login = self.con.expect([LOGIN_RX], 1, start=a.mark.txt, advance=False)
        bid = self.wait_ssh(a)
        self.record("0.2", seen="kernel +%.0fs, login +%.0fs, ssh; boot %s" % (
            g["kernel"].t, login.t if login else -1, bid), expected="login, then ssh",
            evidence=self.sev(g["kernel"].start, login.end if login else None))

        self.at("0.4")
        r = self.ssh("mkdir -p %s && rm -f %s/*" % (GUEST_DIR, GUEST_DIR), self.b("BUDGET_CMD"))
        files = sorted(os.path.join(self.stage, n) for n in os.listdir(self.stage))
        s = labvm.scp_to(self.machine.ssh_port, self.key, files, GUEST_DIR + "/", self.left(self.b("BUDGET_CMD")))
        if r.rc != 0 or s.rc != 0:
            self.record("0.4", problems=["copy to %s: mkdir %s, scp %s %s" % (GUEST_DIR, r.rc, s.rc, s.err.strip())])
        self.staged = True

        self.at("0.3")
        f = self.facts("boot0-clean")
        self.check_03(f)

        self.at("0.4")
        r = self.ssh("sudo -n sh %s/install.sh" % GUEST_DIR, 2 * self.b("BUDGET_CMD_SC"))
        ev = self.save("install.txt", r.out + ("-- stderr\n" + r.err if r.err.strip() else ""))
        inst = parse_kv(r.out)
        self.check_04(r, inst, ev)

        self.at("0.5")
        cfg = self.sudo("cat /boot/grub/grub.cfg")
        self.save("grub.cfg", cfg.out)
        self.check_05(inst, cfg.out, ev)

        self.at("0.7")
        self.poll(lambda: self.sudo("journalctl -b -o cat --no-pager -t scd | grep -c '^baseline:'").out.strip()
                  not in ("", "0"), self.b("BUDGET_POLL"))
        self.poll(lambda: log_rows(self.sudo("sc log /etc/fstab").out), self.b("BUDGET_POLL"))
        f = self.facts("boot0-installed")
        self.at("0.6")
        self.check_06(inst, f, ev)
        self.at("0.7")
        self.check_07(f)

    def check_03(self, f):
        p = []
        paths = paths_map(f.lines("paths"))
        for path in ("/usr/sbin/sc", "/var/lib/smartconfig"):
            if paths.get(path) != "-":
                p.append("%s is there: %s" % (path, paths.get(path)))
        boot = (mounts_map(f.lines("mounts")).get("/boot") or {})
        if boot.get("LABEL") != "BOOT":
            p.append("/boot is %s, not LABEL=BOOT" % boot)
        if f.text("grubenv-stat").strip() != "1024 regular file":
            p.append("grubenv: %r" % f.text("grubenv-stat").strip())
        pw = f.text("root-passwd").split()
        if len(pw) < 2 or pw[1] != "L":
            p.append("passwd -S root: %r" % f.text("root-passwd").strip())
        d = f.kv("grub-defaults")
        for k, want in (("GRUB_TIMEOUT", "0"), ("GRUB_TIMEOUT_STYLE", "hidden"), ("GRUB_RECORDFAIL_TIMEOUT", "0"),
                        ("GRUB_TERMINAL", "console")):
            if d.get(k) != want:
                p.append("%s=%s, not %s" % (k, d.get(k), want))
        linux = d.get("GRUB_CMDLINE_LINUX", "").split()
        if "no_timer_check" not in linux:
            p.append("GRUB_CMDLINE_LINUX has no no_timer_check: %s" % linux)
        if d.get("GRUB_DISABLE_RECOVERY") == "true":
            p.append("GRUB_DISABLE_RECOVERY=true")
        args = linux + d.get("GRUB_CMDLINE_LINUX_DEFAULT", "").split()
        if last_console(args) != "console=ttyS0":
            p.append("the last console= of the GRUB settings is %s" % last_console(args))
        if last_console(f.text("cmdline").split()) != "console=ttyS0":
            p.append("the last console= of /proc/cmdline is %s" % last_console(f.text("cmdline").split()))
        self.record("0.3", problems=p, seen="paths, /boot %s, grubenv, root %s, %s" % (
            boot.get("LABEL"), pw[1] if len(pw) > 1 else "?",
            " ".join("%s=%s" % (k, d.get(k)) for k in ("GRUB_TIMEOUT", "GRUB_TIMEOUT_STYLE", "GRUB_TERMINAL"))),
            expected="no sc; LABEL=BOOT; 1024 regular file; L; hidden 0/0 console; no_timer_check; ttyS0 last",
            source="design 0.3, lab/guest/60-sclab.cfg",
            evidence=f.ev("paths", "mounts", "grubenv-stat", "root-passwd", "grub-defaults", "cmdline"))

    def check_04(self, r, inst, ev):
        p = []
        if r.rc != 0:
            p.append("install.sh exit %s" % r.rc)
        for k, want in (("manifest", "ok"), ("install_rc", "0"), ("daemon_reload_rc", "0"), ("enable_rc", "0"),
                        ("enable_scd_rc", "0"), ("update_grub_rc", "0"), ("done", "1")):
            if inst.get(k) != [want]:
                p.append("%s=%s" % (k, ",".join(inst.get(k, ["(none)"]))))
        got = {}
        for line in inst.get("file", []):
            parts = line.split(" ", 1)
            got[parts[0]] = parts[1] if len(parts) > 1 else ""
        n = 0
        for name, _, mode, dests in INSTALLED:
            for dest in dests:
                n += 1
                want = "%s root:root %s" % (mode, self.sha[name])
                if got.get(dest) != want:
                    p.append("%s: %s, not %s" % (dest, got.get(dest), want))
        self.record("0.4", problems=p, seen="%d files as MANIFEST says; enable and update-grub exit %s" % (
            n, ",".join(inst.get("update_grub_rc", ["?"]))), expected="0755 sc/42/41/43, 0644 units and drop-ins, "
                    "root:root, sha256 = host; enable (no --now), enable --now scd", source="evidence/artefacts.sha256",
                    evidence=ev)

    def check_05(self, inst, cfg, ev):
        p = []
        kernel = self.conf["KERNEL"]
        adding = "Adding SmartConfig rescue entry: /boot/vmlinuz-%s" % kernel
        if adding not in inst.get("update_grub_err", []):
            p.append("update-grub said %s" % inst.get("update_grub_err"))
        if inst.get("grub_script_check_rc") != ["0"]:
            p.append("grub-script-check: %s %s" % (inst.get("grub_script_check_rc"), inst.get("grub_script_check_out")))
        entries = grub_entries(cfg)
        rescue = [e for e in entries if e["id"] == "smartconfig-rescue"]
        if cfg.count("--id smartconfig-rescue") != 1 or len(rescue) != 1:
            p.append("%d entries with --id smartconfig-rescue" % cfg.count("--id smartconfig-rescue"))
        default = entries[0] if entries else None
        if default is None or default["title"] != "Ubuntu":
            p.append("the first entry is %r, not Ubuntu" % (default and default["title"]))
        exp_default, nd = linux_args(default["body"]) if default else (None, 0)
        exp_rescue, nr = linux_args(rescue[0]["body"]) if rescue else (None, 0)
        if exp_default is None or exp_rescue is None:
            p.append("no linux line in the default or the rescue entry")
        if nd > 1 or nr > 1:
            p.append("more than one linux line in an entry (%d, %d)" % (nd, nr))
        if rescue and not re.search(r"^\s*echo\s+'%s'\s*$" % re.escape(RESCUE_ECHO), rescue[0]["body"], re.M):
            p.append("the rescue entry has no echo '%s'" % RESCUE_ECHO)
        if exp_rescue:
            toks = exp_rescue.split()
            for bad in ("quiet", "splash"):
                if bad in toks:
                    p.append("the rescue entry has %s" % bad)
            if last_ro_rw(toks) != "ro":
                p.append("the rescue entry's last ro/rw is %s" % last_ro_rw(toks))
            if (" %s " % RESCUE_ARGS) not in (" %s " % exp_rescue):
                p.append("the rescue entry lacks %r" % RESCUE_ARGS)
            if exp_default:
                rd = [t for t in exp_default.split() if t.startswith("root=")]
                rr = [t for t in toks if t.startswith("root=")]
                if rd != rr:
                    p.append("root= differs: %s, default %s" % (rr, rd))
                # What the default entry passes on (GRUB_CMDLINE_LINUX and
                # _DEFAULT) the rescue entry keeps; 10_linux's $vt_handoff
                # (a desktop's) is the default entry's own.
                lost = [t for t in exp_default.split()
                        if t not in toks and t not in ("ro", "rw", "quiet", "splash", "$vt_handoff")]
                if lost:
                    p.append("the rescue entry drops %s of the default entry" % " ".join(lost))
        rf = RECORDFAIL_BLOCK.search(cfg)
        flag = cfg.find(FLAG_BLOCK)
        if rf is None:
            p.append("no recordfail block from 00_header")
        elif rf.group(1) != "0":
            p.append("the recordfail block sets timeout=%s, not 0" % rf.group(1))
        if flag < 0:
            p.append("no menu flag block")
        elif rf is not None and flag < rf.start():
            p.append("the flag block comes before the recordfail block")
        warnings = []
        pre, post = cfg.find("sclab: pre "), cfg.find("sclab: post ")
        if not (0 <= pre < flag < post):
            warnings.append("[lab] the observers are not around the flag block (%d, %d, %d)" % (pre, flag, post))
        self.v["EXP_DEFAULT"] = exp_default
        self.v["EXP_RESCUE"] = exp_rescue
        self.record("0.5", problems=p, warnings=warnings, seen="EXP_DEFAULT=%s | EXP_RESCUE=%s" % (exp_default, exp_rescue),
                    expected="%s; grub-script-check 0; one entry; no quiet/splash; ro last; %s; same root=; "
                             "flag block after recordfail's timeout=0" % (adding, RESCUE_ARGS),
                    source="scripts/42_smartconfig", evidence="%s evidence/grub.cfg" % ev)

    def check_06(self, inst, f, ev):
        p = []
        drop = show_units(f.lines("dropins"))
        for u in ("rescue.service", "emergency.service"):
            want = "/etc/systemd/system/%s.d/50-smartconfig.conf" % u
            if want not in drop.get(u, {}).get("DropInPaths", "").split():
                p.append("%s DropInPaths: %s" % (u, drop.get(u, {}).get("DropInPaths")))
        n = sum(1 for line in f.lines("units-cat") if line.strip() == DROPIN_LINE)
        if n != 2:
            p.append("%r appears %d times in systemctl cat" % (DROPIN_LINE, n))
        if inst.get("verify_rc") != ["0"] or inst.get("verify_out"):
            p.append("systemd-analyze verify: rc %s: %s" % (inst.get("verify_rc"), inst.get("verify_out")))
        en = f.kv("is-enabled")
        for u in ("sc-boot-seen.service", "sc-boot-ok.service", "scd.service"):
            if en.get(u) != "enabled":
                p.append("%s is-enabled %s" % (u, en.get(u)))
        act = f.kv("active")
        if act.get("scd.service") != "active":
            p.append("scd.service is %s" % act.get("scd.service"))
        self.record("0.6", problems=p, seen="drop-ins %s; verify rc %s; enabled %s; scd %s" % (
            n, ",".join(inst.get("verify_rc", ["?"])), ",".join(en.values()), act.get("scd.service")),
            expected="both drop-ins, the ExecStartPre twice, verify silent, 3 enabled, scd active",
            source="scripts/*.service, smartconfig-rescue.conf",
            evidence="%s %s" % (f.ev("dropins", "units-cat", "is-enabled", "active"), ev))

    def check_07(self, f):
        p = f.need("grubenv", "journal-scd", "sc-log-fstab", "paths")
        if not any(line.startswith("baseline:") for line in f.lines("journal-scd")):
            p.append("no 'baseline:' in scd's journal")
        rows = log_rows(f.text("sc-log-fstab"))
        if not rows:
            p.append("sc log /etc/fstab has no row")
        if any(line.startswith("smartconfig_pending=") for line in f.lines("grubenv")):
            p.append("grubenv has smartconfig_pending")
        if paths_map(f.lines("paths")).get("/var/lib/smartconfig/boots") != "-":
            p.append("a boots file exists")
        self.record("0.7", problems=p, seen="%d fstab rows; grubenv %s" % (len(rows), " ".join(f.lines("grubenv")) or "empty"),
                    expected="baseline:, >= 1 row, no flag, no boots file", source="design 0.7",
                    evidence=f.ev("journal-scd", "sc-log-fstab", "grubenv", "paths"))

    # -- boot 1 and the edit --------------------------------------------------

    def boot1(self, a):
        tag = "boot%s" % a.label
        g = self.grub_phase(a, "1.1", menu=a.flag)
        if self.uefi:
            self.skip("1.0", "uefi: no VGA adapter (-vga none)")
        else:
            rows = a.vga.all_rows() if a.vga is not None else []
            ok = any("SeaBIOS" in r or "Booting from Hard Disk" in r for r in rows)
            self.record("1.0", problems=[] if ok else ["no SeaBIOS or 'Booting from Hard Disk' on the screens"],
                        seen="%d screens" % (len(a.vga.screens) if a.vga else 0), expected="SeaBIOS",
                        source="spike S1/S3", evidence=a.vga.files() if a.vga else "")
        if a.flag is None:
            self.skip("1.1", "a retried attempt: the menu flag is not known (1b repeats it)")
        else:
            self.at("1.1")
            self.check_decision("1.1", a, g, a.flag)
        self.at("1.2")
        self.check_cmdline("1.2", a, g, self.v["EXP_DEFAULT"])
        self.at("1.3")
        self.wait_healthy_end(a, "1.3")
        b1 = self.wait_ssh(a)
        state, problems, warnings = self.system_running("1.3")
        self.record("1.3", problems=problems, warnings=warnings, seen=state, expected="running",
                    source="design 1.3", evidence="ssh.log")
        self.v["B1"] = b1

        self.at("1.4")
        boots = self.poll(lambda: (lambda t: t if ok_line_rx(b1).search(t) else "")(self.boots_text()),
                          self.b("BUDGET_POLL"))
        self.at("1.9")
        self.poll(lambda: self.sudo("journalctl -b -o cat --no-pager -t scd | grep -c '^baseline:'").out.strip()
                  not in ("", "0"), self.b("BUDGET_POLL"))
        self.at("1.5")
        self.poll_verdict_journal(b1)
        f = self.facts(tag)
        self.at("1.4")
        boots = f.text("boots") or boots
        m = ok_line_rx(b1).search(boots)
        r1 = int(m.group(1)) if m else None
        p = []
        if not seen_line_rx(b1).search(boots):
            p.append("no '%s seen' line" % b1)
        if m is None:
            p.append("no '%s ok ...' line" % b1)
        elif r1 < 0:
            p.append("R1 is %d" % r1)
        self.v["R1"] = r1
        self.record("1.4", problems=p, seen="R1=%s; %s" % (r1, " | ".join(boots.strip().split("\n")[-3:])),
                    expected="B1 seen; B1 ok R1>=0 local-fs=active emergency=inactive rescue=inactive",
                    source="internal/boot", evidence=f.ev("boots"))
        self.at("1.5")
        self.check_units("1.5", f, b1)
        self.at("1.6")
        self.check_16(f)
        self.at("1.7")
        self.check_status("1.7", f, b1)
        self.at("1.8")
        self.check_18(f)
        self.at("1.9")
        p = []
        if not any(line.startswith("baseline:") for line in f.lines("journal-scd")):
            p.append("no 'baseline:' in this boot's scd journal")
        rows = log_rows(f.text("sc-log-fstab"))
        good = rows[0][0] if rows else None
        fstab_sha = f.text("fstab-sha256").strip()
        got = None
        if good:
            got = self.sudo("sc cat %s | sha256sum" % good).out.split(" ")[0].strip()
            if got != fstab_sha:
                p.append("sc cat %s hashes to %s, /etc/fstab to %s" % (good, got, fstab_sha))
        else:
            p.append("sc log /etc/fstab has no row")
        self.v["GOOD"], self.v["FSTAB_SHA"] = good, fstab_sha
        self.record("1.9", problems=p, seen="GOOD=%s sc cat sha %s" % (good, (got or "")[:12]),
                    expected="FSTAB_SHA=%s" % fstab_sha[:12], source="sha256 of /etc/fstab in boot 1",
                    evidence=f.ev("journal-scd", "sc-log-fstab", "fstab-sha256"))
        self.flag = False

    def check_units(self, cid, f, bid, extra=()):
        """1.5, 4.6: both units succeeded and their journal (this boot's)
        has no grubenv complaint. The journal must be there and must be
        this boot's: it has sc boot verdict's ok line for bid, so an empty
        or unread block never passes."""
        p = list(extra) + f.need("show:sc-boot-seen.service", "show:sc-boot-ok.service", "journal-sc-boot")
        for u in ("sc-boot-seen.service", "sc-boot-ok.service"):
            s = f.kv("show:" + u)
            for k, want in (("Result", "success"), ("ExecMainStatus", "0"), ("ConditionResult", "yes")):
                if s.get(k) != want:
                    p.append("%s %s=%s" % (u, k, s.get(k)))
        journal = f.text("journal-sc-boot")
        if not verdict_journal_rx(bid).search(journal):
            p.append("sc-boot's journal has no 'boot %s: ok (' line (%d lines)" % (bid, len(f.lines("journal-sc-boot"))))
        bad = [line for line in f.lines("journal-sc-boot") if GRUBENV_ERRORS.search(line)]
        if bad:
            p.append("sc-boot's journal: %s" % " | ".join(bad))
        self.record(cid, problems=p, seen="Result/ExecMainStatus/ConditionResult as shown; %d journal lines" % len(
            f.lines("journal-sc-boot")), expected="success, 0, yes; the verdict's ok line; no grubenv complaint",
                    source="design 1.5", evidence=f.ev("show:sc-boot-seen.service", "show:sc-boot-ok.service",
                                                       "journal-sc-boot"))

    def check_16(self, f):
        """1.6: grubenv, read (rc 0), has no smartconfig_pending line."""
        env = f.lines("grubenv")
        p = f.need("grubenv")
        p += ["grubenv: %s" % x for x in env if x.startswith("smartconfig_pending=")]
        p += stale_recordfail(env)
        self.record("1.6", problems=p, seen=" ".join(env) or "empty", expected="no smartconfig_pending, no recordfail=1",
                    source="sc boot verdict (ok unsets it)", evidence=f.ev("grubenv"))

    def check_18(self, f):
        """1.8: sc-boot-seen is ordered before no target but
        shutdown.target (systemctl show -p Before, read): no target waits
        for it directly (multi-user.target does, through grub-common, by
        design), and
        critical-chain multi-user.target (read, the target in it) does not
        name it. critical-chain alone could not fail: it follows only units
        that became active, and a oneshot never does (the M4 final review,
        A8)."""
        chain = f.text("critical-chain")
        p = f.need("seen-before", "critical-chain")
        before = f.kv("seen-before").get("Before", "").split()
        if "seen-before" in f.blocks and "Before" not in f.kv("seen-before"):
            p.append("systemctl show printed no Before= for sc-boot-seen")
        p += ["sc-boot-seen is ordered before %s: boot waits for it" % u
              for u in before if u.endswith(".target") and u != "shutdown.target"]
        if "multi-user.target" not in chain:
            p.append("critical-chain does not show multi-user.target")
        if "sc-boot-seen" in chain:
            p.append("critical-chain names sc-boot-seen")
        self.record("1.8", problems=p, seen="Before=%s | %s" % (" ".join(before), " | ".join(chain.strip().split("\n")[:3])),
                    expected="no target but shutdown.target; no sc-boot-seen in the chain",
                    source="sc-boot-seen.service (DefaultDependencies=no)",
                    evidence=f.ev("seen-before", "critical-chain", "analyze", "blame"))

    def check_44(self, f):
        """4.4: grubenv, read (rc 0), has no smartconfig_pending line at all
        (sc boot verdict unsets it; an empty value is not unset) and no
        next_entry; /etc/fstab is back."""
        env = f.lines("grubenv")
        p = f.need("grubenv", "fstab", "fstab-sha256")
        p += ["grubenv: %s" % x for x in env if x.startswith("smartconfig_pending=")
              or (x.startswith("next_entry=") and x.split("=", 1)[1])]
        p += stale_recordfail(env)
        sha = f.text("fstab-sha256").strip()
        if sha != self.v["FSTAB_SHA"]:
            p.append("/etc/fstab hashes to %s" % sha)
        n = sum(1 for x in f.lines("fstab") if "3f6c1e2a" in x)
        if n:
            p.append("/etc/fstab still has %d 3f6c1e2a lines" % n)
        self.record("4.4", problems=p, seen="grubenv %s; fstab %s; %d bad lines" % (" ".join(env) or "empty", sha[:12], n),
                    expected="no smartconfig_pending line, no next_entry; FSTAB_SHA; 0", source="1.9",
                    evidence=f.ev("grubenv", "fstab", "fstab-sha256"))

    def check_status(self, cid, f, bid):
        out = f.text("sc-status")
        rc = f.rc("sc-status")
        p, w = [], []
        if rc != 0:
            p.append("sc status exit %s" % rc)
        want = "This boot:     %s (normal), root read-write" % console.short_boot(bid)
        for line in (want, "Last healthy:  this boot, ", "scd:           running (pid "):
            if not any(x.startswith(line) for x in out.split("\n")):
                p.append("no line %r" % line)
        if "Failed since:" in out:
            p.append("a 'Failed since:' line")
        if "Nothing recorded has changed since this boot came up." not in out:
            w.append("no 'Nothing recorded has changed since this boot came up.'")
        self.record(cid, problems=p, warnings=w, seen=" | ".join(out.strip().split("\n")[:4]),
                    expected="%s; Last healthy: this boot; scd running; exit 0" % want, source="cmd/sc/status.go",
                    evidence=f.ev("sc-status"))

    def edit(self):
        good = self.v["GOOD"]
        self.at("E.1")
        r = self.sudo("printf '%%s\\n' '%s' >>/etc/fstab && grep -nxF '%s' /etc/fstab | cut -d: -f1 && "
                      "sha256sum /etc/fstab" % (BAD_LINE, BAD_LINE))
        lines = r.out.split()
        n = int(lines[0]) if r.rc == 0 and lines and lines[0].isdigit() else None
        bad_sha = lines[1] if len(lines) > 1 else None
        self.v["N"], self.v["BAD_SHA"] = n, bad_sha
        self.record("E.1", problems=[] if n and bad_sha else ["the append failed: rc %s %r %r" % (r.rc, r.out, r.err)],
                    warnings=[] if n == 4 else ["N=%s, not 4" % n], seen="N=%s BAD_SHA=%s" % (n, (bad_sha or "")[:12]),
                    expected="N=4", source="the cloud image's 3-line fstab", evidence="ssh.log")

        self.at("E.2")
        state = {"id": None, "since": None}

        def settled():
            rows = log_rows(self.sudo("sc log -n 3 /etc/fstab").out)
            newest = rows[0][0] if rows else None
            if newest != state["id"]:
                state["id"], state["since"] = newest, time.monotonic()
            return newest not in (None, good) and time.monotonic() - state["since"] >= 10

        ok = self.poll(settled, 120, every=2.0)
        bad = state["id"] if ok else None
        self.v["BAD"] = bad
        self.record("E.2", problems=[] if bad else ["the newest fstab row is %s after 120 s" % state["id"]],
                    seen="BAD=%s" % bad, expected="a new row, not GOOD=%s, stable 10 s" % good, source="scd",
                    evidence="ssh.log")

        self.at("E.3")
        rx = re.compile(r"/etc/fstab: check: blocker fstab-source-missing, line %d: .*\(%s\)\s*$" % (n, bad), re.M)
        j = self.poll(lambda: (lambda o: o if rx.search(o) else "")(
            self.sudo("journalctl -b -o cat --no-pager -t scd").out), self.b("BUDGET_POLL"))
        jl = rx.search(j or "")
        self.record("E.3", problems=[] if jl else ["no such line in scd's journal"], seen=jl.group(0) if jl else "",
                    expected=rx.pattern, source="internal/watch/checker.go", evidence="ssh.log")

        self.at("E.4")
        r = self.sudo("sc check /etc/fstab")
        self.record("E.4", problems=[] if r.rc == 2 else ["sc check exit %s" % r.rc],
                    seen="exit %s: %s" % (r.rc, " | ".join(r.out.strip().split("\n")[:2])), expected="exit 2",
                    source="sc check", evidence="ssh.log")

        self.at("E.5")
        r = self.sudo("sc status")
        out = r.out.split("\n")
        p = []
        if r.rc != 2:
            p.append("exit %s" % r.rc)
        row = re.compile(r"^%s\s+\d{4}-\d\d-\d\d \d\d:\d\d\s+/etc/fstab\s+blocker fstab-source-missing, line %d\s*$"
                         % (bad, n))
        at = [i for i, line in enumerate(out) if row.match(line)]
        if not at:
            p.append("no row for %s /etc/fstab blocker fstab-source-missing, line %s" % (bad, n))
        else:
            miss = in_order(out, at[0] + 1, [
                ("  sc restore %s" % good, lambda x: x == "  sc restore %s" % good),
                ("  sync", lambda x: x == "  sync"),
                ("It takes effect at the next boot", lambda x: x.startswith("It takes effect at the next boot"))])
            if miss:
                p.append("no %r after the row" % miss)
        for bad_text in ("remount,rw", "systemctl reboot"):
            if bad_text in r.out:
                p.append("it says %r" % bad_text)
        self.record("E.5", problems=p, seen=" | ".join(x for x in out if x.strip())[-300:],
                    expected="exit 2; the row; sc restore %s; sync; It takes effect at the next boot" % good,
                    source="cmd/sc/status.go", evidence="ssh.log")

        self.at("E.6")
        ev = self.reboot_ssh("E.6")
        self.record("E.6", seen="RESET %s" % json.dumps(ev["data"]), expected="a guest RESET",
                    evidence="marks.log qmp.log", problems=[] if ev["data"].get("guest") else ["not the guest's reset"])

    # -- boot 2 -----------------------------------------------------------------

    def boot2(self, a):
        self.boot2_mark = a.mark if a.n == 1 else self.boot2_mark
        g = self.grub_phase(a, "2.1", menu=a.flag)
        self.at("2.1")
        if a.flag is None:
            self.skip("2.1", "a retried attempt: the menu flag is not known")
        else:
            self.check_decision("2.1", a, g, a.flag)
        self.at("2.2")
        self.check_cmdline("2.2", a, g, self.v["EXP_DEFAULT"])

        self.at("2.3")
        k = g["kernel"].start
        h1 = self.wait_for(a, [BAD_TIME_RX, LOGIN_RX, PROMPT_RX], self.b("BUDGET_KERNEL_RESCUE"), start=k,
                           what="the bad device's timeout")
        if h1 is None:
            raise LabError("no device timeout, prompt or login %d s after the kernel" % self.b("BUDGET_KERNEL_RESCUE"))
        h2 = None
        if h1.index == 0:
            h2 = self.wait_for(a, [LOCALFS_DEPEND_RX, LOGIN_RX, PROMPT_RX], self.b("BUDGET_POLL"), start=h1.start,
                               what="local-fs.target's failure")
        p = []
        if h1.index != 0:
            p.append("%r came before any timeout of the bad device" % h1.text)
        elif h2 is None or h2.index != 0:
            p.append("no 'Dependency failed for local-fs.target' after the timeout")
        t_seen = time.monotonic() if re.search(SEEN_DONE_RX, self.text(a)) else None
        self.record("2.3", problems=p, seen="%s | %s" % (h1.text, h2.text if h2 else ""),
                    expected="Timed out waiting for device ...%s, then Dependency failed for local-fs.target" % BAD_TAIL,
                    source="the bad line (E.1)", evidence=self.sev(h1.start, (h2 or h1).end))
        # sc-boot-seen (DefaultDependencies=no) ran long before the device's
        # 90 s: from here on the menu flag is set.
        self.flag = True

        if self.args.boot2 == "reset-at-timeout":
            self.v["outcome"] = "a"
            self.v["B2"] = None
            self.record("2.4", seen="a (forced: --boot2 reset-at-timeout)", expected="a, b or c",
                        evidence=self.sev(h2.start))
            self.skip("2.5", "--boot2 reset-at-timeout: reset at the 2.3 match")
            self.skip("2.6", "outcome a")
            self.settle_until = time.monotonic()
            return

        self.at("2.4")
        deadline = time.monotonic() + self.left(self.b("BUDGET_BOOT2_END"))
        end = None
        while time.monotonic() < deadline:
            size = self.mux.size()
            if t_seen is None and re.search(SEEN_DONE_RX, self.text(a)):
                t_seen = time.monotonic()
            text, end = self.boot_end(a)
            if end is not None:
                break
            why = self.unexpected(a)
            if why:
                raise LabError(why + ", in boot 2")
            self.mux.wait(size, 1.0)
        if end is not None and end.kind == "panic":
            self.panicked(a, end.line)
        if end is not None and end.kind in ("grub", "shutdown"):
            raise LabError("boot 2 ended in %s: %s" % (end.kind, end.line.strip()))
        end_ev = self.sev(self.boff(a, text, end.pos)) if end is not None else "serial.txt"
        t_end = time.monotonic()
        outcome, b2, state, mu, ssh_seen = self.probe_boot2()
        self.v["outcome"], self.v["B2"] = outcome, b2
        a.boot_id = b2
        if t_seen is None and re.search(SEEN_DONE_RX, self.text(a)):
            t_seen = time.monotonic()
        self.record("2.4", seen="%s: end %s %r; ssh %s; emergency.target %s, multi-user.target %s" % (
            outcome, end.kind if end else "none in %d s" % self.b("BUDGET_BOOT2_END"),
            (end.line.strip() if end else ""), "yes (%d probes)" % ssh_seen if ssh_seen else "no", state, mu),
            problems=[] if end is not None else ["boot 2 came to no prompt and no login in %d s"
                                                 % self.b("BUDGET_BOOT2_END")],
            warnings=["ssh answered, but multi-user.target never became active in %d s: no verdict for B2 (a)"
                      % self.b("BUDGET_BOOT2_SSH")] if outcome == "a" and ssh_seen else [],
            expected="a (no multi-user.target), b or c (ssh, multi-user.target active)", source="design 2.4",
            evidence=end_ev + " ssh.log")

        self.at("2.5")
        self.check_emergency_report(a, outcome, b2)

        if outcome == "a":
            self.skip("2.6", "outcome a: no verdict for B2")
        else:
            self.at("2.6")
            self.check_26(b2)
        self.settle_until = max(t_end, t_seen or t_end) + self.b("RESET_SETTLE")

    def check_26(self, b2):
        """2.6 (b and c): B2 seen, B2 bad local-fs!=active, the menu flag
        set; then sync. What ssh could not read is a LabError, never a
        missing line."""
        seen_rx, bad_rx = seen_line_rx(b2), bad_line_rx(b2)
        boots = self.poll(lambda: (lambda t: t if bad_rx.search(t) else "")(self.boots_text()),
                          self.b("BUDGET_POLL")) or self.boots_text(strict=True)
        envr = self.sudo("grub-editenv /boot/grub/grubenv list", lost=True)
        sync = self.sudo("sync", lost=True)
        if 255 in (envr.rc, sync.rc):
            raise LabError("ssh failed in 2.6 (grub-editenv %s, sync %s): %s"
                           % (envr.rc, sync.rc, (envr.err or sync.err).strip()[-200:]))
        env = envr.out
        p = []
        if not seen_rx.search(boots):
            p.append("no '%s seen' line" % b2)
        m = bad_rx.search(boots)
        if not m:
            p.append("no '%s bad ... local-fs!=active ...' line" % b2)
        if "smartconfig_pending=1" not in env.split("\n"):
            p.append("grubenv has no smartconfig_pending=1: %s" % env.strip())
        if sync.rc != 0:
            p.append("sync exit %s" % sync.rc)
        self.v["B2_emergency"] = m.group(1) if m else None
        self.record("2.6", problems=p, seen="%s; grubenv %s" % (
            " | ".join(x for x in boots.split("\n") if x.startswith(b2)), " ".join(env.split())),
            expected="B2 seen; B2 bad local-fs!=active; smartconfig_pending=1", source="sc boot seen/verdict",
            evidence="ssh.log")

    def probe_boot2(self):
        """2.4's ssh probe, every SSH_PROBE_EVERY s for BUDGET_BOOT2_SSH s:
        b or c as soon as it answers with multi-user.target active
        (probe_outcome), else a. (outcome, B2 or None, emergency.target,
        multi-user.target, how many probes ssh answered)."""
        b2 = state = mu = outcome = None
        ssh_seen = 0
        probe_end = time.monotonic() + self.left(self.b("BUDGET_BOOT2_SSH"))
        while True:
            r = self.ssh(PROBE_CMD, 20, lost=True)
            pr = parse_probe(r.rc, r.out)
            if pr is not None:
                b2, state, mu = pr
                ssh_seen += 1
                outcome = probe_outcome(pr)
                if outcome:
                    break
            if time.monotonic() + self.b("SSH_PROBE_EVERY") > probe_end or not self.machine.alive():
                break
            time.sleep(self.b("SSH_PROBE_EVERY"))
        return outcome or "a", b2, state, mu, ssh_seen

    def check_emergency_report(self, a, outcome, b2):
        """2.5 (F in every outcome): the report above the emergency
        shell, held to the golden. Plan A7 found it lost when the console
        getty hung up the shell, and design 2.5 made it a W in outcome c
        for that; since cmd/sc/main.go's ignoreHangup sc must outlive the
        hang-up, so a report lost or cut is sc's wherever it happens. sc
        ended by the hang-up says so itself (SC_HUNGUP_RX). Where the
        getty's banner or login prompt comes before the shell's line, or
        the shell never prints one, the report ends there
        (emergency_report), held as strictly, with a note."""
        deadline = time.monotonic() + self.left(REPORT_WAIT)
        while True:
            size = self.mux.size()
            text = self.text(a)
            blk, kind = emergency_report(text)
            if blk is not None or time.monotonic() > deadline:
                break
            self.mux.wait(size, 1.0)
        if blk is None:
            blk, kind = emergency_report(text, final=True)
        hung = getty_hangup(text)
        died = ["sc status --console ended by a hangup; it must outlive one (cmd/sc/main.go ignoreHangup)"] \
            if SC_HUNGUP_RX.search(text) else []
        why = ["the console getty hung up the emergency shell (plan A2, A7): %s" % " | ".join(hung)] if hung else []
        if kind in ("banner", "login") and SHELL_LINE not in text:
            why.append("the console getty's hang-up ended the emergency shell before its first line (plan A2, A7): "
                       "no 'You are in emergency mode'; the report is the block above the login prompt")
        elif kind in ("banner", "login"):
            why.append("the getty's banner came between the report and the shell's first line; the report ends at it")
        elif kind == "end":
            why.append("nothing came after the report in %d s; it ends with the console's text" % REPORT_WAIT)
        if blk is not None and (GETTY_BANNER_M.search(text, blk.start, blk.end) or
                                re.compile(LOGIN_RX).search(text, blk.start, blk.end)):
            why.append("the getty's banner or prompt came between the report's header and its rest "
                       "(sc shows the header first when it is slow); taken out")
        if blk is None:
            self.record("2.5", problems=died + ["no report above 'You are in emergency mode'"], notes=why,
                        expected="console-emergency.golden", source="cmd/sc/status.go via TestStatusConsoleLab",
                        evidence=self.sev(a.mark.txt, self.con.size()))
            return
        lines, notes = tolerate_report(blk.lines)
        # A boot that went on to multi-user has scd up when the report is
        # made again; every healthy run of outcome b shows it.
        why += [n for n in notes if n.startswith("scd was already running")]
        notes = [n for n in notes if not n.startswith("scd was already running")]
        if blk.stripped:
            notes.append("%d status lines taken out" % len(blk.stripped))
        bind = {"GOOD": self.v["GOOD"], "BAD": self.v["BAD"], "B1": self.v["B1"], "N": self.v["N"]}
        if b2:
            bind["B2"] = b2
        res = console.match_golden(lines, console.load_golden("emergency"), bind)
        if not b2 and res.ok:
            self.v["B2_short"] = res.values.get("B2:8")
        self.save("report-emergency.txt", "\n".join(blk.lines))
        s = a.mark.txt + len(text[:blk.start].encode("utf-8", "surrogateescape"))
        e = a.mark.txt + len(text[:blk.end].encode("utf-8", "surrogateescape"))
        self.record("2.5", problems=died + res.problems, warnings=notes, notes=why,
                    seen=" | ".join(lines[:2]), expected="console-emergency.golden with %s" % bind,
                    source="lab/testdata/console-emergency.golden", evidence=self.sev(s, e))

    def reset_settled(self):
        """2.7: system_reset once the boot has settled (RESET_SETTLE s after
        the prompt), or at once after --boot2 reset-at-timeout."""
        self.at("2.7")
        wait = self.settle_until - time.monotonic()
        if wait > 0:
            self.log("2.7: %.0f s for the boot to settle" % wait)
            time.sleep(min(wait, self.left(wait)))
        ev = self.reset_vm("2.7")
        self.record("2.7", seen="RESET %s" % json.dumps(ev["data"]), expected="RESET (host)",
                    problems=[] if not ev["data"].get("guest") else ["a guest reset"], evidence="marks.log qmp.log")

    # -- boot 3 -----------------------------------------------------------------

    def boot3(self, a):
        g = self.grub_phase(a, "3.1", menu=True, pick=RESCUE_TITLE)
        self.at("3.1")
        self.check_decision("3.1", a, g, True)
        self.at("3.2")
        hit = self.kernel_cmdline(a, g, "3.2")
        line = norm_cmdline(hit.group(1))
        if "fstab=no" not in line.split():
            raise Retry("menu-missed", "the kernel started without fstab=no: %s" % line)
        self.record("3.2", seen="keys %s, Enter; %s" % (g["keys"], line[-80:]), expected="the rescue entry boots",
                    source="design 3.2", evidence="input.log " + self.sev(hit.start))
        self.at("3.3")
        if self.uefi:
            seen = RESCUE_ECHO in self.con.text(g["enter_pos"], g["kernel"].start)
            ev = self.sev(g["enter_pos"], g["kernel"].start)
        else:
            if a.vga2 is not None:
                a.vga2.stop()
            seen = any(RESCUE_ECHO in r for r in (a.vga2.all_rows() if a.vga2 else []))
            ev = a.vga2.files() if a.vga2 else ""
        self.record("3.3", problems=[] if seen else ["not seen between Enter and the kernel"],
                    strength="H" if self.uefi else "W", seen="seen" if seen else "not seen", expected=RESCUE_ECHO,
                    source="scripts/42_smartconfig", evidence=ev)
        self.at("3.4")
        self.check_cmdline("3.4", a, g, self.v["EXP_RESCUE"], hit)

        self.at("3.5")
        gaps = len(self.mux.gaps)
        p = self.wait_for(a, [PROMPT_RX, LOGIN_RX], self.b("BUDGET_KERNEL_RESCUE"), start=g["kernel"].start,
                          what="the rescue prompt")
        if p is None:
            self.ran_out("3.5", a, gaps, "no 'Press Enter for maintenance' %d s after the kernel"
                         % self.b("BUDGET_KERNEL_RESCUE"))
        if p.index == 1:
            self.record("3.5", problems=["a login prompt instead of the rescue prompt"], evidence=self.sev(p.start))
        pe = self.wait_for(a, [PROMPT_END_RX], 60, start=p.start, what="the prompt's second line")
        before = self.con.text(a.mark.txt, p.start)
        bad = [x for x in (re.search(BAD_TIME_RX, before), re.search(LOCALFS_DEPEND_RX, before)) if x]
        self.record("3.5", problems=["%r before the prompt" % m.group(0) for m in bad],
                    seen="prompt at +%.0fs, no bad-device wait" % p.t if not bad else "",
                    expected="no TIME for %s, no DEPEND for local-fs" % BAD_TAIL, source="fstab=no",
                    evidence=self.sev(g["kernel"].start, p.end))

        self.at_prompt = True
        self.at("3.6")
        self.check_rescue_report(a, (pe or p).end)

        self.at("3.7")
        start = self.con.size()
        gaps = len(self.mux.gaps)
        if not self.con.send("\r", 0):
            raise LabError("could not send Enter on serial")
        h = self.wait_for(a, [SHELL_RX, r"Password:", r"Login incorrect|Cannot open access to console"],
                          self.b("BUDGET_CMD"), start=start, what="the root shell")
        if h is None:
            self.ran_out("3.7", a, gaps, "no shell prompt %d s after Enter" % self.b("BUDGET_CMD"))
        self.at_prompt = False
        self.record("3.7", problems=[] if h.index == 0 else ["%r instead of a # prompt" % h.text], seen=h.text,
                    expected="root@sclab:~# , no Password:", source="Ubuntu sulogin (locked root)",
                    evidence=self.sev(start, h.end))
        self.shell = True
        r = self.con.prepare_shell(self.b("BUDGET_CMD"))
        if r.rc != 0:
            raise LabError("preparing the shell (stty cols 200; export ...) gave %s" % (r.rc,))

        self.check_rescue_facts(a)
        self.run_undo(a)

    def check_rescue_report(self, a, prompt_end):
        """3.6: the report above the rescue prompt against the golden of
        boot 2's outcome (rescue_golden), exactly: only interleaved
        systemd status lines are taken out (a W)."""
        text = self.con.text(a.mark.txt, prompt_end)
        blk = console.report_block(text, end=r"You are in rescue mode")
        outcome = self.v.get("outcome") or "a"
        golden = "rescue-%s" % (outcome if outcome in ("b", "c") else "a")
        self.v["reason"] = None
        if blk is None:
            self.record("3.6", problems=["no report from 'This boot:' to 'You are in rescue mode'"],
                        expected="console-%s.golden" % golden, evidence=self.sev(a.mark.txt, prompt_end))
            return
        lines, notes = rescue_report_lines(blk)
        bind = {"GOOD": self.v["GOOD"], "BAD": self.v["BAD"], "B1": self.v["B1"], "N": self.v["N"]}
        reason, res, problems, strength, more = rescue_golden(lines, outcome, bind)
        notes += more
        if reason is not None:
            golden = "rescue-%s" % reason
        self.v["reason"] = reason
        rows = console.screen_rows(blk.screen) if blk.screen is not None else None
        if rows is None:
            problems.append("no 'Press Enter for maintenance' after the report")
            strength = "H"
        elif rows > 25:
            problems.append("%d rows from the report to the prompt, more than 25" % rows)
            strength = "H"
        k = res.values.get("K boots")
        self.v["K"] = (1 if k == "1 boot" else int(k.split()[0])) if k else None
        self.v["B3_short"] = res.values.get("B3:8")
        self.v["undo"] = undo_commands(lines)
        self.save("report-rescue.txt", "\n".join(blk.lines) + "\n-- to the prompt --\n" + "\n".join(blk.screen or []))
        s = a.mark.txt + len(text[:blk.start].encode("utf-8", "surrogateescape"))
        self.record("3.6", problems=problems, warnings=notes, strength=strength if problems else None,
                    seen="reason %s; K=%s B3=%s rows=%s undo=%s" % (reason, self.v["K"], self.v["B3_short"], rows,
                                                                   self.v["undo"]),
                    expected="console-rescue-%s.golden (boot 2 outcome %s) with %s; <= 80 columns; <= 25 rows" % (
                        outcome if outcome in ("b", "c") else "a|b|c", outcome, bind),
                    source="lab/testdata/console-%s.golden (TestStatusConsoleLab)" % golden,
                    evidence=self.sev(s, a.mark.txt + len(text.encode("utf-8", "surrogateescape"))))

    def check_rescue_facts(self, a):
        good, bad = self.v["GOOD"], self.v["BAD"]
        self.at("3.8.ro")
        r = self.con.cmd("sh %s/facts.sh rescue %s %s" % (GUEST_DIR, good, bad), 3 * self.b("BUDGET_CMD_SC"))
        ev = self.save("facts-rescue.txt", r.out)
        if r.rc is None:
            raise LabError("facts.sh rescue: no answer in %d s" % (3 * self.b("BUDGET_CMD_SC")))
        f = Facts(r.out, ev)
        if not f.has("end"):
            raise LabError("facts.sh rescue: no end block (exit %s)" % r.rc)
        mounts = mounts_map(f.lines("mounts"))
        root = mounts.get("/") or {}
        opts = root.get("OPTIONS", "")
        self.record("3.8.ro", problems=[] if opts == "ro" or opts.startswith("ro,") else ["/ is %s" % opts],
                    seen=opts, expected="ro,...", source="the rescue entry's ro", evidence=f.ev("mounts"))

        self.at("3.8.mounts")
        act = f.kv("active")
        p = f.need("mounts")
        for m in ("/boot", "/mnt/backup"):
            if m not in mounts:
                p.append("no line for %s in the mounts block" % m)  # not read is not "not mounted"
            elif mounts[m]:
                p.append("%s is mounted" % m)
        if act.get("rescue.target") != "active":
            p.append("rescue.target is %s" % act.get("rescue.target"))
        self.record("3.8.mounts", problems=p, seen="/boot %s, /mnt/backup %s, rescue.target %s" % (
            "-" if not mounts.get("/boot") else "mounted", "-" if not mounts.get("/mnt/backup") else "mounted",
            act.get("rescue.target")), expected="neither mounted; rescue.target active", source="fstab=no",
                    evidence=f.ev("mounts", "active"))

        self.at("3.8.units")
        seen_u, ok_u = f.kv("show:sc-boot-seen.service"), f.kv("show:sc-boot-ok.service")
        p = []
        if seen_u.get("ConditionResult") != "no":
            p.append("sc-boot-seen ConditionResult=%s" % seen_u.get("ConditionResult"))
        if ok_u.get("ExecMainStartTimestampMonotonic") != "0":
            p.append("sc-boot-ok ExecMainStartTimestampMonotonic=%s" % ok_u.get("ExecMainStartTimestampMonotonic"))
        self.record("3.8.units", problems=p, seen="seen ConditionResult=%s; ok started at %s" % (
            seen_u.get("ConditionResult"), ok_u.get("ExecMainStartTimestampMonotonic")),
            expected="no; 0", source="ConditionPathIsReadWrite=/var/lib",
            evidence=f.ev("show:sc-boot-seen.service", "show:sc-boot-ok.service"))

        self.at("3.8.boots")
        b3 = f.text("boot-id").strip()
        self.v["B3"] = b3
        a.boot_id = b3
        boots = f.text("boots")
        entries = boots_entries(boots)
        p = []
        if any(bid == b3 for bid, _, _ in entries):
            p.append("a line for this boot B3=%s" % b3)
        b2 = self.v.get("B2")
        after = failed_after(entries, self.v["B1"])
        if after is None:
            p.append("no B1=%s in the boots file" % self.v["B1"])
            after = []
        if b2:
            if not seen_line_rx(b2).search(boots):
                p.append("no '%s seen' line" % b2)
        elif not after or "seen" not in after[0][1]:
            p.append("no seen line after B1's")
        elif self.v.get("B2_short") and console.short_boot(after[0][0]) != self.v["B2_short"]:
            p.append("the boot after B1 is %s, the emergency report said %s" % (after[0][0], self.v["B2_short"]))
        if self.v.get("K") is not None and len(after) != self.v["K"]:
            p.append("%d failed boots after B1 in the file, the report said %s" % (len(after), self.v["K"]))
        # The report's reason (3.6, a golden a, b or c even after outcome a)
        # must be what the boots file says of the last failed boot.
        said = boots_reason(after)
        if said != self.v.get("reason"):
            p.append("the boots file's last failed boot gives reason %s, the report's is %s"
                     % (said, self.v.get("reason")))
        if self.v.get("B3_short") and console.short_boot(b3) != self.v["B3_short"]:
            p.append("the report's boot %s is not this one %s" % (self.v["B3_short"], b3))
        if not self.v.get("B2") and after:
            self.v["B2"] = after[0][0]
        self.record("3.8.boots", problems=p, seen="B3=%s; %d failed after B1: %s" % (
            b3, len(after), [x[0][:8] for x in after]), expected="no B3 line; B2 seen; K=%s" % self.v.get("K"),
            source="the ledger", evidence=f.ev("boot-id", "boots"))

        self.at("3.8.sc")
        p = []
        if f.rc("sc-status-console") != 2:
            p.append("sc status --console exit %s" % f.rc("sc-status-console"))
        if "+UUID=3f6c1e2a" not in f.text("sc-diff"):
            p.append("sc diff %s %s has no +UUID=3f6c1e2a" % (good, bad))
        if f.text("sc-cat-good").strip() != self.v["FSTAB_SHA"]:
            p.append("sc cat %s hashes to %s" % (good, f.text("sc-cat-good").strip()))
        if f.rc("sc-check") != 2 or "fstab-source-missing" not in f.text("sc-check"):
            p.append("sc check exit %s: %s" % (f.rc("sc-check"), " | ".join(f.lines("sc-check")[:2])))
        if f.rc("sc-restore") in (0, 125, None) or RESTORE_REFUSED not in f.text("sc-restore"):
            p.append("sc restore %s: exit %s %r" % (good, f.rc("sc-restore"), f.text("sc-restore")[:200]))
        self.record("3.8.sc", problems=p, seen="status %s, check %s, restore %s" % (
            f.rc("sc-status-console"), f.rc("sc-check"), f.rc("sc-restore")),
            expected="2; +UUID=3f6c1e2a; FSTAB_SHA; 2 with the blocker; refused: %s" % RESTORE_REFUSED,
            source="design 3.8", evidence=f.ev("sc-status-console", "sc-diff", "sc-cat-good", "sc-check", "sc-restore"))

        self.at("3.8.hashes")
        p = f.need("hashes-before", "hashes-after", "leftovers")
        p += hashes_problems(f.lines("hashes-before"), f.lines("hashes-after"))
        if [x for x in f.lines("leftovers") if x.strip()]:
            p.append("left behind: %s" % f.lines("leftovers"))
        self.record("3.8.hashes", problems=p, seen="%d hashes unchanged; %d leftovers" % (
            len(f.lines("hashes-before")), len([x for x in f.lines("leftovers") if x.strip()])),
            expected="unchanged; none", source="design 3.8", evidence=f.ev("hashes-before", "hashes-after", "leftovers"))

        self.at("3.8.vcs1")
        want = "sc restore %s" % good
        vcs = f.text("vcs1")
        self.record("3.8.vcs1", problems=[] if want in vcs else ["%r is not on /dev/vcs1" % want],
                    seen=" | ".join(x.strip() for x in f.lines("vcs1") if x.strip())[-200:], expected=want,
                    source="status.go writeConsoles (every active console)", evidence=f.ev("vcs1"))

        self.at("3.8.vga")
        if self.uefi:
            self.skip("3.8.vga", "uefi: no VGA adapter")
        else:
            try:
                buf = self.machine.pmemsave(labvm.VGA_TEXT, 32768, self.evpath("vga-rescue-32k.bin"))
                rows = console.vgatext(buf)
                hit = any(want in r for r in rows)
                self.record("3.8.vga", problems=[] if hit else ["%r not in the 32 KiB VGA window" % want],
                            seen="found" if hit else "not found", expected=want, source="spike S1 gotcha 3",
                            evidence="evidence/vga-rescue-32k.bin")
            except LabError as e:
                self.record("3.8.vga", problems=["pmemsave: %s" % e])

    def run_undo(self, a):
        """3.9: the report's commands, one at a time, as the owner would."""
        self.at("3.9")
        good = self.v["GOOD"]
        cmds = self.v.get("undo") or []
        want = [c.format(GOOD=good) for c in UNDO]
        if cmds != want:
            self.record("3.9", problems=["the report's commands are %s, not %s" % (cmds, want)])
        p, seen = [], []
        for c in cmds[:-1]:
            sc_cmd = c.startswith("sc ")
            gaps = len(self.mux.gaps)
            r = self.con.cmd(c, self.b("BUDGET_CMD_SC") if sc_cmd else self.b("BUDGET_CMD"))
            seen.append("%s: %s" % (c, r.rc))
            if r.rc is None and len(self.mux.gaps) > gaps:
                raise LabError("3.9: %r got no answer in time while the host stood still" % c)
            if r.rc is None:
                # Never typed again: a restore half done is evidence too.
                self.record("3.9", problems=p + ["%r: no answer in time" % c], seen="; ".join(seen),
                            evidence=self.sev(r.start, r.end))
            if r.rc != 0:
                p.append("%r exit %s: %s" % (c, r.rc, r.out.strip()[-200:]))
            if c.startswith("mount "):
                o = self.con.cmd("findmnt -n -o OPTIONS /", self.b("BUDGET_CMD"))
                if not o.out.strip().startswith("rw,"):
                    p.append("/ is %r after the remount" % o.out.strip())
            if c.startswith("sc restore"):
                m = re.search(r"restored /etc/fstab from %s .*previous state saved as (\w+)" % re.escape(good), r.out)
                self.v["PRE"] = m.group(1) if m else None
                if not m:
                    p.append("no 'restored /etc/fstab from %s ... previous state saved as' line" % good)
                h = self.con.cmd("sha256sum /etc/fstab", self.b("BUDGET_CMD"))
                sha = h.out.strip().split(" ")[0] if h.out.strip() else ""
                if sha != self.v["FSTAB_SHA"]:
                    p.append("/etc/fstab hashes to %s after the restore" % sha)
        if p:
            self.record("3.9", problems=p, seen="; ".join(seen), evidence="input.log serial.txt")
        since = self.qmp.mark()
        if not self.con.line(cmds[-1]):
            raise LabError("could not type %r" % cmds[-1])
        self.shell = False
        ev = self.wait_reset(since, self.b("BUDGET_RESET"), ("RESET", "SHUTDOWN"), "RESET after systemctl reboot")
        if ev["event"] != "RESET" or not ev["data"].get("guest"):
            p.append("systemctl reboot gave %s %s" % (ev["event"], json.dumps(ev["data"])))
        self.new_boot(ev)
        self.record("3.9", problems=p, seen="; ".join(seen) + "; PRE=%s; %s %s" % (
            self.v.get("PRE"), ev["event"], json.dumps(ev["data"])),
            expected="exit 0 each; / rw,; PRE; fstab = FSTAB_SHA; a guest RESET", source="the report (3.6)",
            evidence="input.log marks.log")

    # -- boots 4 and 5 ----------------------------------------------------------

    def boot4(self, a):
        g = self.grub_phase(a, "4.1", menu=a.flag)
        self.at("4.1.cmdline")
        hit = self.kernel_cmdline(a, g, "4.1")
        line, lost = self.cmdline_seen(a, hit, self.v["EXP_DEFAULT"])
        keys = self.mux.inputs(a.mark.inp)
        # Every attempt, whether the flag is known or not (4.1 is skipped
        # then): recorded first, an H failure stops the mode after 4.1.
        held = [self.record("4.1.cmdline", problems=[] if line == self.v["EXP_DEFAULT"] else ["Command line differs"],
                            notes=lost, seen=line, expected=self.v["EXP_DEFAULT"], source="0.5 grub.cfg",
                            evidence=self.sev(hit.start, hit.end), stop=False),
                self.record("4.1.nokey", problems=["%d inputs: %s" % (len(keys), ", ".join(
                    "%s %s" % (e.kind, e.data) for e in keys[:5]))] if keys else [],
                    seen="%d inputs since input.log entry %d" % (len(keys), a.mark.inp),
                    expected="none: the menu times out by itself", source="design 4.1", evidence="input.log",
                    stop=False)]
        self.at("4.1")
        if a.flag is None:
            self.skip("4.1", "a retried attempt: the menu flag is not known (the first attempt's 4.1 stands)")
        else:
            self.check_decision("4.1", a, g, a.flag, seen_extra="; %s" % line[-60:])
        self.stop_on(held)
        self.at("4.2")
        self.wait_healthy_end(a, "4.2")
        b4 = self.wait_ssh(a)
        self.v["B4"] = b4
        state, problems, warnings = self.system_running("4.2")
        self.poll(lambda: ok_line_rx(b4).search(self.boots_text()), self.b("BUDGET_POLL"))
        self.poll_verdict_journal(b4)
        f = self.facts("boot4" if a.n == 1 else "boot4-%d" % a.n)
        text = self.text(a)
        act = f.kv("active")
        p = list(problems)
        if re.search(BAD_TIME_RX, text):
            p.append("the bad device timed out in this boot")
        for u, want in (("local-fs.target", "active"), ("emergency.target", "inactive"), ("rescue.target", "inactive")):
            if act.get(u) != want:
                p.append("%s is %s" % (u, act.get(u)))
        self.record("4.2", problems=p, warnings=warnings, seen="%s; %s" % (state, " ".join(
            "%s=%s" % (u, act.get(u)) for u in ("local-fs.target", "emergency.target", "rescue.target"))),
            expected="no TIME for %s; active, inactive, inactive" % BAD_TAIL, source="design 4.2",
            evidence="%s %s" % (self.sev(a.mark.txt, self.con.size()), f.ev("active")))

        self.at("4.3")
        boots = f.text("boots")
        m = ok_line_rx(b4).search(boots)
        r4 = int(m.group(1)) if m else None
        self.v["R4"] = r4
        p = self.ledger_problems(boots, b4)
        if not seen_line_rx(b4).search(boots):
            p.append("no '%s seen' line" % b4)
        if m is None:
            p.append("no '%s ok ...' line" % b4)
        elif self.v.get("R1") is None or r4 <= self.v["R1"]:
            p.append("R4=%s is not more than R1=%s" % (r4, self.v.get("R1")))
        self.record("4.3", problems=p, seen="R1=%s R4=%s; %d lines" % (self.v.get("R1"), r4, len(boots_entries(boots))),
                    expected="B4 seen, B4 ok R4 > R1; B1, failed boots, B4; no B3", source="the ledger",
                    evidence=f.ev("boots"))

        self.at("4.4")
        self.check_44(f)

        self.at("4.5")
        self.check_status("4.5", f, b4)

        self.at("4.6")
        rows = log_rows(f.text("sc-log-fstab"))
        p = []
        if len(rows) < 2:
            p.append("%d fstab rows" % len(rows))
        else:
            r0, r1 = rows[0], rows[1]
            if r0[2] != "restore" or ("restored from %s" % self.v["GOOD"]) not in r0[5]:
                p.append("the newest row is %s" % (r0,))
            if r1[2] != "pre-restore" or r1[0] != self.v.get("PRE"):
                p.append("the next row is %s, not pre-restore %s" % (r1, self.v.get("PRE")))
        pre_sha = self.sudo("sc cat %s | sha256sum" % self.v.get("PRE")).out.split(" ")[0].strip() \
            if self.v.get("PRE") else ""
        if pre_sha != self.v["BAD_SHA"]:
            p.append("sc cat PRE=%s hashes to %s, not BAD_SHA" % (self.v.get("PRE"), pre_sha))
        self.check_units("4.6", f, b4, extra=p)
        self.flag = False

    def ledger_problems(self, boots, b4):
        """4.3: the boots file against the ledger: B1 ok; after it only
        failed boots (K of them, B2 first) and boot 4's attempts; B4 last;
        no B3."""
        p = []
        entries = boots_entries(boots)
        ids = boots_by_id(entries)
        names = [b for b, _ in ids]
        b1, b3 = self.v["B1"], self.v.get("B3")
        if b3 and b3 in names:
            p.append("a line for the rescue boot B3")
        if b1 not in names or "ok" not in dict(ids)[b1]:
            p.append("no B1 ok line")
            return p
        if not names or names[-1] != b4:
            p.append("the last boot in the file is %s, not B4" % (names[-1] if names else None))
            return p
        between = ids[names.index(b1) + 1:-1]
        tries4 = sum(1 for a in self.attempts if a.label == "4" and a.result == "retry")
        failed = [b for b, k in between if "ok" not in k]
        if any("seen" not in k for _, k in between):
            p.append("a boot without a seen line after B1")
        k = self.v.get("K")
        if k is not None and not (k <= len(between) <= k + tries4):
            p.append("%d boots between B1 and B4, the ledger expects %d (+%d boot 4 retries)" % (len(between), k, tries4))
        if len(failed) < len(between) - tries4:
            p.append("an ok boot between B1 and B4")
        b2 = self.v.get("B2")
        if b2 and (not between or between[0][0] != b2):
            p.append("the first boot after B1 is %s, not B2=%s" % (between[0][0] if between else None, b2))
        return p

    def boot5(self, a):
        g = self.grub_phase(a, "5.1", menu=a.flag)
        self.at("5.1")
        # Before anything in this boot can be retried: a flake later in it
        # must not leave 5.1 unchecked.
        first = [] if a.flag is None else [self.check_decision("5.1", a, g, False, stop=False)]
        self.wait_healthy_end(a, "5.1")
        b5 = self.wait_ssh(a)
        self.v["B5"] = b5
        boots = self.poll(lambda: (lambda t: t if ok_line_rx(b5).search(t) else "")(self.boots_text()),
                          self.b("BUDGET_POLL")) or self.boots_text(strict=True)
        ok = ok_line_rx(b5).search(boots or "")
        text = self.text(a)
        tm = re.search(BAD_TIME_RX, text)
        # Every attempt, whether the flag is known or not (5.1 is skipped
        # then): recorded first, an H failure stops the mode after 5.1.
        held = [self.record("5.1.ok", problems=[] if ok else ["no '%s ok ...' line" % b5],
                            seen=ok.group(0) if ok else " | ".join((boots or "").strip().split("\n")[-3:]),
                            expected="%s ok ... local-fs=active emergency=inactive rescue=inactive" % b5,
                            source="sc boot verdict", evidence="ssh.log", stop=False),
                self.record("5.1.notime", problems=["the bad device timed out in this boot: %r" % tm.group(0)] if tm
                            else [], seen=tm.group(0) if tm else "no bad-device timeout",
                            expected="no TIME for %s" % BAD_TAIL, source="design 6 (never retried)",
                            evidence=self.sev(self.boff(a, text, tm.start()) if tm else a.mark.txt, self.con.size()),
                            stop=False)]
        self.at("5.1")
        if a.flag is None:
            self.skip("5.1", "a retried attempt: the menu flag is not known (5b repeats it)")
        self.stop_on(first + held)
        self.flag = False

    # -- --grub-password: the README's GRUB password recipe (6.x) ------------

    def grub_password(self):
        """6.1-6.5: the README's GRUB superuser recipe in this guest, then
        three boots: the default one (no menu, no password), the rescue
        entry from the menu (the password asked, given, and it boots), and
        Ubuntu from the menu (none asked; the boot is ok, which clears the
        flag the rescue boot could not). It tests the README, not sc."""
        self.at("6.1")
        r = self.sudo(GRUB_RECIPE, self.b("BUDGET_CMD_SC"))
        p, lines = recipe_problems(r.rc, r.out, r.err)
        self.record("6.1", problems=p, seen=" | ".join(lines), expected="one line: menuentry 'Ubuntu' ... --unrestricted",
                    source="README: To close the menu paths", evidence="ssh.log")
        self.reboot_ssh("6.2")
        self.boot("6a", self.boot6a)
        self.at("6.3")
        r = self.sudo("grub-editenv /boot/grub/grubenv set smartconfig_pending=1")
        if r.rc != 0:
            raise LabError("grub-editenv set smartconfig_pending=1: exit %s %s" % (r.rc, r.err.strip()))
        self.flag = True
        self.reboot_ssh("6.3")
        self.boot("6b", self.boot6b)
        self.boot("6c", self.boot6c)

    def grub_asked_vga(self, a):
        """bios: GRUB's username prompt on a VGA screen of this boot, or None."""
        rows = [r for w in (a.vga, a.vga2) if w is not None for r in w.all_rows()]
        return next((r.strip() for r in rows if re.search(GRUB_USER_RX, r)), None)

    def kernel_unless_asked(self, a, cid, start, budget):
        """grub_phase's wait for the kernel where nobody types (6.2, 6.5):
        GRUB's username prompt first fails cid (H). Without this GRUB waits
        for ever, and the wait ran out as a [lab] error (the chunk H
        review). Serial (uefi), or the VGA screens polled between short
        waits (bios). The kernel's first line, or None after budget s."""
        if self.uefi:
            hit = self.wait_for(a, [LINUX_RX, GRUB_PROMPT_RX, GRUB_USER_RX], budget, start=start, what="the kernel")
            if hit is not None and hit.index == 2:
                self.record(cid, problems=["GRUB asked: %r" % hit.text.strip()], expected="no %r" % GRUB_USER_RX,
                            source="README: the recipe marks Ubuntu --unrestricted", evidence=self.sev(start, hit.end))
            return hit
        deadline = time.monotonic() + self.left(budget)
        while True:
            hit = self.wait_for(a, [LINUX_RX, GRUB_PROMPT_RX], max(0.1, min(2.0, deadline - time.monotonic())),
                                start=start, what="the kernel")
            if hit is not None:
                return hit
            asked = self.grub_asked_vga(a)
            if asked:
                self.record(cid, problems=["GRUB asked: %r" % asked], expected="no %r" % GRUB_USER_RX,
                            source="README: the recipe marks Ubuntu --unrestricted",
                            evidence=" ".join(w.files() for w in (a.vga, a.vga2) if w is not None))
            if time.monotonic() >= deadline:
                return None

    def boot6a(self, a):
        """6.2: the default boot with the password set: no menu, no prompt,
        and its verdict is ok before the next step sets the flag (an ok
        given later would unset it: the chunk H review)."""
        self.grub_phase(a, "6.2", menu=a.flag, asks_none=True)
        self.at("6.2")
        self.wait_healthy_end(a, "6.2")
        b = self.wait_ssh(a)
        ok = self.ok_line(b)
        self.record("6.2", problems=[] if ok else ["no '%s ok ...' line" % b],
                    seen="no prompt; %s" % (ok.group(0) if ok else "no ok line"),
                    expected="no %r; %s ok ..." % (GRUB_USER_RX, b[:8]), source="README: the recipe marks Ubuntu --unrestricted",
                    evidence="ssh.log")

    def ok_line(self, b):
        """The boots file's ok line for boot b, polled as boot 5 does; None."""
        boots = self.poll(lambda: (lambda t: t if ok_line_rx(b).search(t) else "")(self.boots_text()),
                          self.b("BUDGET_POLL")) or self.boots_text(strict=True)
        return ok_line_rx(b).search(boots or "")

    def grub_prompt(self, a, g, rx, budget):
        """GRUB's rx after the last Enter: serial (uefi) or the VGA screen
        (bios, polled since the Enter). True when it came."""
        if self.uefi:
            return self.wait_for(a, [rx], budget, start=g["enter_pos"], what=rx) is not None
        deadline = time.monotonic() + self.left(budget)
        while time.monotonic() < deadline:
            if a.vga2 is not None and a.vga2.first(lambda rows: any(re.search(rx, r) for r in rows)) is not None:
                return True
            # What only the lab can lose is never the recipe's (the chunk H review).
            if a.vga2 is None or a.vga2.error:
                raise LabError("the VGA watch since the Enter %s, while waiting for %r"
                               % ("failed: %s" % a.vga2.error if a.vga2 is not None else "was not started", rx))
            why = self.unexpected(a)
            if why:
                raise LabError("%s, while waiting for %r" % (why, rx))
            time.sleep(0.25)
        return False

    def grub_type(self, text):
        """text and Enter, as typed at GRUB's prompt: serial (uefi) or
        the emulated keyboard (bios: a qcode per character)."""
        if self.uefi:
            if not self.con.send(text + "\r"):
                raise LabError("could not type at GRUB's prompt on serial")
            return
        for ch in list(text) + ["ret"]:
            self.mux.note("sendkey", ch)
            self.machine.sendkey(ch)
            time.sleep(0.05)

    def grub_login(self, a, g):
        """6.3: after the Enter on the rescue entry GRUB asks for the
        superuser, then the password; both are typed."""
        self.at("6.3")
        seen = []
        for rx, answer in ((GRUB_USER_RX, GRUB_USER), (GRUB_PASS_RX, GRUB_PASSWORD)):
            if not self.grub_prompt(a, g, rx, self.b("BUDGET_MENU")):
                self.record("6.3", problems=["no %r after %s" % (rx, " and ".join(seen) or "the Enter on " + RESCUE_TITLE)],
                            expected="%r, then %r" % (GRUB_USER_RX, GRUB_PASS_RX), source="README: every other entry asks",
                            evidence=self.sev(g["enter_pos"], self.con.size()) if self.uefi else
                            (a.vga2.files() if a.vga2 else ""))
            seen.append(rx)
            g["enter_pos"] = self.con.size()
            self.grub_type(answer)
        self.record("6.3", seen=", then ".join(seen), expected="%r, then %r" % (GRUB_USER_RX, GRUB_PASS_RX),
                    source="README: every entry but Ubuntu asks for the password", evidence="input.log")

    def boot6b(self, a):
        """6.3, 6.4: the rescue entry from the menu, the password given;
        it boots with fstab=no, and its shell reboots."""
        if a.flag is None:
            raise LabError("boot 6b retried: whether the menu flag is set is not known, so 6.3 cannot be checked")
        g = self.grub_phase(a, "6.3", menu=a.flag, pick=RESCUE_TITLE, pick_cid="6.3", login=self.grub_login)
        self.at("6.4")
        hit = self.kernel_cmdline(a, g, "6.4")
        line = norm_cmdline(hit.group(1))
        p = [] if "fstab=no" in line.split() else ["the kernel started without fstab=no: %s" % line[-120:]]
        # As boot 3's waits (ran_out): a stall or a host pause is the lab's.
        gaps = len(self.mux.gaps)
        pr = self.wait_for(a, [PROMPT_RX], self.b("BUDGET_KERNEL_RESCUE"), start=g["kernel"].start, what="the rescue prompt")
        if pr is None:
            self.ran_out("6.4", a, gaps, "no %r %d s after the kernel" % (PROMPT_RX, self.b("BUDGET_KERNEL_RESCUE")))
        self.at_prompt = True
        start = self.con.size()
        gaps = len(self.mux.gaps)
        if not self.con.send("\r", 0):
            raise LabError("could not send Enter on serial")
        h = self.wait_for(a, [SHELL_RX], self.b("BUDGET_CMD"), start=start, what="the root shell")
        if h is None:
            self.ran_out("6.4", a, gaps, "no root shell %d s after Enter" % self.b("BUDGET_CMD"))
        self.at_prompt = False
        if p:
            self.record("6.4", problems=p, seen=line[-120:], evidence=self.sev(hit.start, self.con.size()))
        self.shell = True
        since = self.qmp.mark()
        if not self.con.line("systemctl reboot"):
            raise LabError("could not type systemctl reboot")
        self.shell = False
        ev = self.wait_reset(since, self.b("BUDGET_RESET"), ("RESET", "SHUTDOWN"), "RESET after systemctl reboot")
        if ev["event"] != "RESET":
            p.append("systemctl reboot gave %s %s" % (ev["event"], json.dumps(ev["data"])))
        self.new_boot(ev)
        self.record("6.4", problems=p, seen="%s; %s" % (line[-80:], ev["event"]),
                    expected="fstab=no, the rescue prompt, a # shell, a RESET", source="scripts/42_smartconfig",
                    evidence=self.sev(hit.start, hit.end))

    def boot6c(self, a):
        """6.5: the menu again (the rescue boot cannot clear the flag);
        Ubuntu from it asks for no password, and the boot's verdict is ok."""
        if a.flag is None:
            # Retried after an ok verdict may have cleared it (the chunk H
            # review): 6.5 cannot be checked; as decided(), the lab's.
            raise LabError("boot 6c retried: whether the menu flag is set is not known, so 6.5 cannot be checked")
        self.grub_phase(a, "6.5", menu=a.flag, pick=UBUNTU_TITLE, pick_cid="6.5", asks_none=True)
        self.at("6.5")
        self.wait_healthy_end(a, "6.5")
        b = self.wait_ssh(a)
        ok = self.ok_line(b)
        p = [] if ok else ["no '%s ok ...' line" % b]
        self.record("6.5", problems=p, seen="no prompt; %s" % (ok.group(0) if ok else "no ok line"),
                    expected="no %r; %s ok ..." % (GRUB_USER_RX, b[:8]), source="README: Ubuntu is --unrestricted",
                    evidence="input.log ssh.log")
        self.flag = False

    # -- the whole mode -------------------------------------------------------

    def flow(self):
        self.boot("0", self.boot0)
        self.reboot_ssh("0.7")
        a = self.boot("1", self.boot1)
        if a.flag is None:
            self.log("1b: the menu flag was not known at boot 1's start; a clean reboot repeats 1.1-1.9")
            self.reboot_ssh("1.1")
            self.boot("1b", self.boot1)
        self.decided("1.1")
        self.edit()
        self.boot("2", self.boot2)
        self.reset_settled()
        self.boot("3", self.boot3)
        self.boot("4", self.boot4)
        if self.args.no_boot5:
            for cid in [c for c in REGISTRY if c.startswith("5.") or c.startswith("6.")]:
                self.skip(cid, "--no-boot5")
            return
        self.reboot_ssh("5.1")
        a = self.boot("5", self.boot5)
        if a.flag is None:
            self.log("5b: the menu flag was not known at boot 5's start; a clean reboot repeats 5.1")
            self.reboot_ssh("5.1")
            self.boot("5b", self.boot5)
        self.decided("5.1")
        if self.args.grub_password:
            self.grub_password()
        else:
            for cid in [c for c in REGISTRY if c.startswith("6.")]:
                self.skip(cid, "no --grub-password")
        self.at("5.2")
        ev = self.reboot_ssh("5.2", "poweroff")
        rc = self.machine.wait_exit(60)
        self.record("5.2", problems=[] if ev["data"].get("guest") and rc == 0 else
                    ["SHUTDOWN %s, QEMU exit %s" % (json.dumps(ev["data"]), rc)],
                    seen="SHUTDOWN %s; QEMU exit %s" % (json.dumps(ev["data"]), rc), expected="a guest SHUTDOWN, QEMU exits 0",
                    evidence="qmp.log")

    def decided(self, cid):
        """1.1 and 5.1 must have been checked in some attempt. Where
        every one had a flake before it (the flag not known, then the
        same in 1b or 5b), a SKIP would let the mode pass with GRUB's
        decision never looked at: a LabError, so INCONCLUSIVE."""
        row = self.rows.get(cid)
        if row is None or row.status == "SKIP":
            self.at(cid)
            raise LabError("%s was never checked: the menu flag was not known in any attempt" % cid)

    def failure_dump(self):
        """What a failed run shows: facts.sh dump from the guest, if any way in."""
        out = None
        # Not past the mode's time: make's timeout would cut the teardown
        # (T.1, result.txt). 45 s of BUDGET_MODE stay for that.
        end = self.t_start + self.b("BUDGET_MODE") - 45

        def left(want):
            return max(1.0, min(want, end - time.monotonic()))
        try:
            if self.machine is None or not self.machine.alive() or not self.staged:
                return None
            if self.at_prompt and not self.shell:
                # Stopped at boot 3's prompt (3.6): Enter, as 3.7 would have.
                start = self.con.size()
                self.con.send("\r", 0)
                self.shell = self.con.expect([SHELL_RX], left(60), start=start, advance=False) is not None
            if self.shell:
                r = self.con.cmd("sh %s/facts.sh dump" % GUEST_DIR, left(300))
                out = r.out
            elif labvm.port_open(self.machine.ssh_port) and labvm.ssh(
                    self.machine.ssh_port, self.key, "true", left(20)).ok:
                r = labvm.ssh(self.machine.ssh_port, self.key, "sudo -n sh %s/facts.sh dump" % GUEST_DIR, left(300))
                out = r.out + r.err
        except Exception as e:  # the run failed already: say so, go on
            out = "facts.sh dump failed: %r" % (e,)
        if out:
            self.save("facts-dump.txt", out)
        return out

    def teardown(self):
        """Design section 5: QMP quit, SIGKILL of its own pid if need be, the
        threads joined, then T.1."""
        if self.torn_down:
            return
        self.torn_down = True
        for w in self.vga_watches:
            w.stop()
        rc = self.machine.stop() if self.machine is not None else None
        if self.mux is not None:
            self.mux.stop()
        if self.machine is None:
            return
        self.at("T.1")
        left = self.machine.leftovers()
        left += ["thread %s still runs" % t.name for t in threading.enumerate()
                 if t is not threading.main_thread() and t.is_alive() and t.name in ("vga", "serialmux", "qmp", "seed")]
        self.record("T.1", problems=left, seen="QEMU exit %s; %s" % (rc, "nothing left" if not left else "left"),
                    expected="no QEMU, no port, no thread", source="design 5", evidence="qemu.log", stop=False)

    def check_k1(self):
        if self.mux is None:
            return
        bad = serialmux.lone_escapes(self.mux.inputs())
        self.record("K.1", problems=["lone ESC: %s" % [(e.t, e.data) for e in bad]] if bad else [],
                    seen="%d inputs, %d lone ESC" % (len(self.mux.inputs()), len(bad)), expected="none",
                    source="design 6", evidence="input.log", stop=False)

    def execute(self):
        """The mode: preflight, boots 0-5, teardown. The verdict."""
        stack = contextlib.ExitStack()  # the lab lock, released last
        try:
            try:
                self.preflight(stack)
                self.start_vm()
                self.flow()
            except Stop as s:
                self.log("stopped: %s %s" % (s.row.id, _cell(s.row.notes, 300)))
            except LabError as e:
                self.lab_error = str(e)
                self.fail_lab(self.ctx, str(e))
            except Retry as r:  # outside a boot: a bug
                self.lab_error = "a retry outside a boot: %s" % r
                self.fail_lab(self.ctx, self.lab_error)
            except (KeyboardInterrupt, SystemExit) as e:
                self.interrupted = True  # stop now: no dump from the guest first
                self.lab_error = "interrupted (%s)" % (e.__class__.__name__ if isinstance(e, KeyboardInterrupt)
                                                       else "signal, exit %s" % e.code)
                self.fail_lab(self.ctx, self.lab_error)
            except Exception as e:
                self.lab_error = "e2e.py: %r" % (e,)
                self._logf.write(traceback.format_exc())
                self.fail_lab(self.ctx, self.lab_error)
            for cid in REGISTRY:
                if cid not in self.rows:
                    self.rows[cid] = Row(cid, "NOTRUN", REGISTRY[cid].strength, seen="not reached")
            v = verdict(self.rows.values(), self.lab_error)
            dump = None
            if v != "PASS" and not getattr(self, "interrupted", False):
                dump = self.failure_dump()
            try:
                self.check_k1()
                self.teardown()
            except Exception as e:  # T.1 says what is left; the verdict stands on its own
                self.lab_error = self.lab_error or "teardown: %r" % (e,)
                self.fail_lab("T.1", "teardown: %r" % (e,))
            for cid in ("K.1", "T.1"):
                if self.rows.get(cid) is not None and self.rows[cid].status == "NOTRUN" and self.machine is None:
                    self.rows[cid] = Row(cid, "SKIP", REGISTRY[cid].strength, seen="QEMU never started")
            v = verdict(self.rows.values(), self.lab_error)
            if v == "PASS" and not self.args.keep:
                labvm.discard_disks(self.run)
            self.finish(v, dump)
            return EXIT[v]
        finally:
            stack.close()
            self._logf.close()

    def finish(self, v, dump):
        took = duration(time.monotonic() - self.t_start)
        rows = list(self.rows.values())
        with open(os.path.join(self.run, "result.txt"), "w", encoding="utf-8", errors="replace") as f:
            f.write("# lab/e2e.py %s: %s in %s, %d retries; run %s\n" % (self.mode, v, took, self.retries, self.run))
            f.write("# HEAD %s dirty=%s sc=%s boot2=%s%s\n" % (
                self.head, "yes" if self.dirty else "no", self.sha.get("sc", "")[:12], self.v.get("outcome", "-"),
                "; lab error: %s" % self.lab_error if self.lab_error else ""))
            f.write("ID\tSTATUS\tCLASS\tSTRENGTH\tCHANNEL\tEVIDENCE\tEXPECTED\tFROM\tSEEN\tNOTES\n")
            for r in rows:
                f.write(result_line(r) + "\n")
        self.save_ledger(v)
        line = summary_line(v, self.mode, took, self.v.get("outcome"), self.retries, self.head,
                            "yes" if self.dirty else "no", self.sha.get("sc", "")[:12],
                            "no" if self.args.no_boot5 else "yes", self.args.boot2 == "reset-at-timeout",
                            getattr(self.args, "grub_password", False) and not self.args.no_boot5)
        if v != "PASS":
            failing = [r for r in rows if r.status == "FAIL"] or [r for r in rows if r.status == "NOTRUN"][:1]
            sys.stderr.write("\n%s: %s\nrun directory: %s\n" % (self.mode, v, self.run))
            for r in failing[:5]:
                sys.stderr.write("failing: %s\n" % result_line(r))
            if self.con is not None:
                sys.stderr.write("-- the last 40 serial lines --\n%s\n" % self.con.tail(40))
            if dump:
                sys.stderr.write("-- facts.sh dump --\n%s\n" % dump.rstrip())
        sys.stdout.write(line + "\n")
        sys.stdout.flush()


def sh_quote(s):
    return "'" + s.replace("'", "'\\''") + "'"


def main(argv=None):
    # No abbreviations: make lab-e2e and the summary line say boot5=no for
    # --no-boot5, spelt out.
    ap = argparse.ArgumentParser(prog="e2e.py", description="The M4 rescue scenario in a QEMU VM (lab/README.md).",
                                 allow_abbrev=False)
    ap.add_argument("--mode", required=True, choices=labvm.MODES)
    ap.add_argument("--boot2", choices=("natural", "reset-at-timeout"), default="natural",
                    help="reset-at-timeout: reset boot 2 right at the device timeout (forces outcome a)")
    ap.add_argument("--no-boot5", action="store_true", help="leave out boot 5 (while iterating)")
    ap.add_argument("--grub-password", action="store_true",
                    help="after boot 5, the README's GRUB password recipe and three more boots (checks 6.x)")
    ap.add_argument("--keep", action="store_true", help="keep disk.qcow2 and VARS.fd after a PASS too")
    args = ap.parse_args(argv)
    if args.grub_password and args.no_boot5:
        ap.error("--grub-password runs after boot 5: not with --no-boot5")
    labvm.install_signal_handlers()
    # Whatever goes wrong in here is the lab's: never exit 1, which make
    # lab-e2e reads as an [M4] failure.
    try:
        conf = labvm.load_conf()
        run = E2E(args, conf)
        run.log("e2e %s: run %s (HEAD %s%s)" % (args.mode, run.run, run.head[:12], ", dirty" if run.dirty else ""))
        return run.execute()
    except (Exception, KeyboardInterrupt, SystemExit) as e:
        if isinstance(e, LabError):
            sys.stderr.write("e2e.py: %s\n" % e)
        else:
            traceback.print_exc()
        sys.stdout.write(summary_line("INCONCLUSIVE", args.mode, "-", None, "-", "-", "-", "",
                                      "no" if args.no_boot5 else "yes") + "\n")
        return EXIT["INCONCLUSIVE"]


if __name__ == "__main__":
    sys.exit(main())
