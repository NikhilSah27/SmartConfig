"""Unit tests for lab/e2e.py's pure parts (no QEMU, no network): the check
registry, the verdict, what it reads from facts.sh, install.sh, grub.cfg,
the boots file, sc log and the rescue report, and its command line. Then
the checks on a run with no VM (FakeRun): a healthy run's text and facts
with one fault at a time, and execute() with the VM and the flow stubbed.

  python3 -m unittest discover -s lab

The flow itself needs the VM: make lab-e2e.
"""
import argparse
import collections
import contextlib
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)
sys.dont_write_bytecode = True  # no lab/__pycache__ from the imports below

import console  # noqa: E402
import e2e  # noqa: E402
import serialmux  # noqa: E402
import vm as labvm  # noqa: E402

TESTDATA = os.path.join(HERE, "testdata")
B1 = "1a2b3c4d-9e8f-4a5b-8c7d-6e5f4a3b2c1d"
B2 = "2b3c4d5e-0000-4000-8000-000000000002"
B3 = "3c4d5e6f-0000-4000-8000-000000000003"
B4 = "4d5e6f70-0000-4000-8000-000000000004"


def fixture(name):
    with open(os.path.join(TESTDATA, name), "rb") as f:
        return f.read()


class TestRegistry(unittest.TestCase):
    # Every check the STEP11 design names (section 4), and the section 6
    # rule on lone ESCs. 3.8 is split by its bullets (ro H, the rest F).
    DESIGN = ("0.1 0.2 0.3 0.4 0.5 0.6 0.7 1.0 1.1 1.2 1.3 1.4 1.5 1.6 1.7 1.8 1.9 E.1 E.2 E.3 E.4 E.5 E.6 "
              "2.1 2.2 2.3 2.4 2.5 2.6 2.7 3.0 3.1 3.2 3.3 3.4 3.5 3.6 3.7 3.8.ro 3.8.mounts 3.8.units "
              "3.8.boots 3.8.sc 3.8.hashes 3.8.vcs1 3.8.vga 3.9 4.1 4.2 4.3 4.4 4.5 4.6 5.1 5.2 T.1").split()

    def test_every_design_check_is_there(self):
        for cid in self.DESIGN:
            self.assertIn(cid, e2e.REGISTRY)
        for cid in ("P.1", "P.2", "P.3", "P.4", "P.5", "K.1"):
            self.assertIn(cid, e2e.REGISTRY)

    def test_classes_and_strengths(self):
        r = e2e.REGISTRY
        for cid in ("P.1", "P.5", "0.1", "0.2", "0.3", "0.4", "1.0", "E.1", "E.6", "2.7", "3.0", "T.1", "K.1"):
            self.assertEqual((r[cid].cls, r[cid].strength), ("lab", "H"), cid)
        for cid in ("1.8", "E.3", "E.4", "E.5", "2.5", "4.6", "3.8.mounts", "3.8.sc", "3.8.vcs1"):
            self.assertEqual((r[cid].cls, r[cid].strength), ("M4", "F"), cid)
        for cid in ("0.5", "0.6", "0.7", "1.1", "2.3", "2.6", "3.1", "3.6", "3.8.ro", "3.9", "4.1", "5.1", "5.2",
                    "4.1.cmdline", "5.1.ok", "5.1.notime"):
            self.assertEqual((r[cid].cls, r[cid].strength), ("M4", "H"), cid)
        self.assertEqual((r["4.1.nokey"].cls, r["4.1.nokey"].strength), ("lab", "H"))
        self.assertEqual(r["3.8.vga"].strength, "W")


def row(cid, status, cause=None):
    r = e2e.Row(cid, status, e2e.REGISTRY[cid].strength)
    if cause:
        r.cause = cause
    return r


class TestVerdict(unittest.TestCase):
    def test_verdicts(self):
        ok = [row("1.2", "PASS"), row("1.8", "WARN"), row("1.0", "SKIP")]
        self.assertEqual(e2e.verdict(ok), "PASS")
        self.assertEqual(e2e.verdict(ok + [row("1.8", "FAIL")]), "FAIL")
        self.assertEqual(e2e.verdict(ok + [row("0.2", "FAIL")]), "INCONCLUSIVE")
        self.assertEqual(e2e.verdict(ok + [row("1.2", "FAIL", cause="lab")]), "INCONCLUSIVE")
        self.assertEqual(e2e.verdict(ok + [row("4.1", "NOTRUN")]), "INCONCLUSIVE")
        self.assertEqual(e2e.verdict(ok, lab_error="QEMU exited"), "INCONCLUSIVE")
        # SmartConfig's failure wins over the lab's.
        self.assertEqual(e2e.verdict([row("0.2", "FAIL"), row("1.8", "FAIL")], "x"), "FAIL")
        self.assertEqual(e2e.EXIT, {"PASS": 0, "FAIL": 1, "INCONCLUSIVE": 3})

    def test_merge(self):
        fail, ok, skip = row("1.1", "FAIL"), row("1.1", "PASS"), row("1.1", "SKIP")
        self.assertIs(e2e.merge(None, ok), ok)
        self.assertIs(e2e.merge(fail, ok), fail)  # a later attempt never hides it
        self.assertIs(e2e.merge(ok, skip), ok)  # a retried attempt's SKIP keeps the result
        self.assertIs(e2e.merge(ok, fail), fail)
        lab = row("1.1", "FAIL", cause="lab")
        self.assertIs(e2e.merge(lab, ok), ok)

    def test_result_line(self):
        r = row("1.2", "PASS")
        r.seen = "a\tb\nc"
        r.notes = "x" * 1000
        line = e2e.result_line(r)
        self.assertNotIn("\n", line)
        self.assertEqual(line.split("\t")[:5], ["1.2", "PASS", "M4", "H", "S"])
        self.assertEqual(line.split("\t")[8], "a b c")
        self.assertTrue(line.endswith("..."))
        self.assertIn("\tM4(lab)\t", e2e.result_line(row("1.2", "FAIL", cause="lab")))

    def test_flag_after_retry(self):
        f = e2e.flag_after_retry
        for flag in (True, False, None):
            for sig in ("panic", "stall", "grub-prompt", "menu-missed"):
                self.assertIs(f(flag, sig), flag)
            self.assertIsNone(f(flag, "no-ssh"))
        self.assertIs(f(True, "slow-udev"), True)
        self.assertIsNone(f(False, "slow-udev"))
        for flag in (True, False):  # a VGA poll gap is before userspace
            self.assertIs(f(flag, "vga-gap"), flag)


FACTS = """\
root@sclab:~# sh /home/owner/sclab/facts.sh rescue 1d5b0a c7146c; echo "__SC000001""_$?__"
== facts rc=0
mode=rescue
good=1d5b0a
== mounts rc=0
/ SOURCE="/dev/vda1" LABEL="cloudimg-rootfs" FSTYPE="ext4" OPTIONS="ro,relatime"
/boot -
/boot/efi -
/mnt/backup -
== show:sc-boot-seen.service rc=0
Id=sc-boot-seen.service
ConditionResult=no
== dropins rc=0
Id=rescue.service
DropInPaths=/usr/lib/systemd/system/service.d/10-timeout-abort.conf /etc/systemd/system/rescue.service.d/50-smartconfig.conf

Id=emergency.service
DropInPaths=/etc/systemd/system/emergency.service.d/50-smartconfig.conf
== sc-restore rc=1
sc: store /var/lib/smartconfig: it is on a read-only file system: remount it read-write first

== paths rc=0
/usr/sbin/sc file 755 root:root abc
/var/lib/smartconfig/boots -
== end rc=0
"""


class TestFacts(unittest.TestCase):
    def test_blocks(self):
        f = e2e.Facts(FACTS, "evidence/facts-rescue.txt")
        self.assertEqual(list(f.blocks), ["facts", "mounts", "show:sc-boot-seen.service", "dropins", "sc-restore",
                                          "paths", "end"])
        self.assertEqual(f.rc("sc-restore"), 1)
        # The blank line after it goes.
        self.assertEqual(f.lines("sc-restore"), ["sc: store /var/lib/smartconfig: " + e2e.RESTORE_REFUSED])
        self.assertEqual(f.kv("facts"), {"mode": "rescue", "good": "1d5b0a"})
        self.assertEqual(f.kv("show:sc-boot-seen.service")["ConditionResult"], "no")
        self.assertEqual(f.ev("mounts"), "evidence/facts-rescue.txt:5-9")
        self.assertEqual(f.ev("facts", "nothing"), "evidence/facts-rescue.txt:2-4")
        self.assertIsNone(f.rc("nothing"))
        self.assertEqual(f.text("nothing"), "")

    def test_maps(self):
        f = e2e.Facts(FACTS, "x")
        m = e2e.mounts_map(f.lines("mounts"))
        self.assertEqual(m["/"]["OPTIONS"], "ro,relatime")
        self.assertEqual(m["/"]["LABEL"], "cloudimg-rootfs")
        self.assertIsNone(m["/boot"])
        p = e2e.paths_map(f.lines("paths"))
        self.assertEqual(p["/var/lib/smartconfig/boots"], "-")
        self.assertEqual(p["/usr/sbin/sc"], "file 755 root:root abc")
        d = e2e.show_units(f.lines("dropins"))
        self.assertIn("/etc/systemd/system/rescue.service.d/50-smartconfig.conf", d["rescue.service"]["DropInPaths"])
        self.assertIn("emergency.service", d)

    def test_install_facts(self):
        out = ("uid=0\nmanifest=ok\nmanifest_out=sc: OK\nfile=/usr/sbin/sc 755 root:root aa\n"
               "update_grub_rc=0\nupdate_grub_err=Sourcing file `/etc/default/grub'\n"
               "update_grub_err=Adding SmartConfig rescue entry: /boot/vmlinuz-6.8.0-142-generic\n"
               "verify_rc=0\ndone=1\n")
        kv = e2e.parse_kv(out)
        self.assertEqual(kv["update_grub_err"][1], "Adding SmartConfig rescue entry: /boot/vmlinuz-6.8.0-142-generic")
        self.assertEqual(kv["file"], ["/usr/sbin/sc 755 root:root aa"])
        self.assertNotIn("verify_out", kv)


class TestGuestText(unittest.TestCase):
    def test_log_rows(self):
        out = ("ID      WHEN              ORIGIN       FILE        SIZE  WHAT\n"
               "f00d12  2026-10-04 12:10  restore      /etc/fstab  146   restored from 1d5b0a\n"
               "a1b2c3  2026-10-04 12:09  pre-restore  /etc/fstab  202   before restoring 1d5b0a\n"
               "c7146c  2026-10-04 12:03  scd          /etc/fstab  202\n")
        rows = e2e.log_rows(out)
        self.assertEqual([r[0] for r in rows], ["f00d12", "a1b2c3", "c7146c"])
        self.assertEqual(rows[0][2], "restore")
        self.assertEqual(rows[0][5], "restored from 1d5b0a")
        self.assertEqual(rows[2][5], "")
        self.assertEqual(e2e.log_rows("no snapshots\n"), [])

    def test_boots(self):
        text = ("%s seen 100\n%s ok 101 7 local-fs=active emergency=inactive rescue=inactive failed-units=0\n"
                "%s seen 200\n%s bad 290 9 local-fs=inactive emergency=active rescue=inactive failed-units=2\n"
                "%s seen 400\n%s ok 401 12 local-fs=active emergency=inactive rescue=inactive failed-units=0\n"
                % (B1, B1, B2, B2, B4, B4))
        self.assertEqual(e2e.ok_line_rx(B1).search(text).group(1), "7")
        self.assertTrue(e2e.seen_line_rx(B2).search(text))
        self.assertEqual(e2e.bad_line_rx(B2).search(text).group(1), "active")
        self.assertIsNone(e2e.bad_line_rx(B2).search(text.replace("local-fs=inactive", "local-fs=active")))
        self.assertIsNone(e2e.ok_line_rx(B2).search(text))
        entries = e2e.boots_entries(text + "garbage\n\n")
        self.assertEqual(len(entries), 6)
        after = e2e.failed_after(entries, B1)
        self.assertEqual([b for b, _ in after], [B2])
        self.assertIsNone(e2e.failed_after(entries, B3))
        self.assertEqual([b for b, _ in e2e.boots_by_id(entries)], [B1, B2, B4])


GRUB_CFG = """\
### BEGIN /etc/grub.d/00_header ###
if [ -s $prefix/grubenv ]; then
  load_env
fi
function recordfail {
  set recordfail=1
}
terminal_output console
if [ "${recordfail}" = 1 ] ; then
  set timeout=0
else
  if [ x$feature_timeout_style = xy ] ; then
    set timeout_style=hidden
    set timeout=0
  fi
fi
### END /etc/grub.d/00_header ###
### BEGIN /etc/grub.d/10_linux ###
menuentry 'Ubuntu' --class ubuntu --class gnu-linux --class gnu --class os $menuentry_id_option 'gnulinux-simple-1bfe' {
\trecordfail
\tload_video
\tsearch --no-floppy --fs-uuid --set=root 0b1c
\tlinux\t/vmlinuz-6.8.0-142-generic root=UUID=1bfe ro  no_timer_check console=tty1 console=ttyS0
\tinitrd\t/initrd.img-6.8.0-142-generic
}
submenu 'Advanced options for Ubuntu' $menuentry_id_option 'gnulinux-advanced-1bfe' {
\tmenuentry 'Ubuntu, with Linux 6.8.0-142-generic' --class ubuntu $menuentry_id_option 'gnulinux-6.8.0' {
\t\tlinux\t/vmlinuz-6.8.0-142-generic root=UUID=1bfe ro  no_timer_check console=tty1 console=ttyS0
\t}
}
### END /etc/grub.d/10_linux ###
### BEGIN /etc/grub.d/30_uefi-firmware ###
menuentry 'UEFI Firmware Settings' $menuentry_id_option 'uefi-firmware' {
\tfwsetup
}
### END /etc/grub.d/30_uefi-firmware ###
### BEGIN /etc/grub.d/41_sclab ###
echo "sclab: pre platform=${grub_platform} pending=[${smartconfig_pending}] recordfail=[${recordfail}] \
timeout=[${timeout}] style=[${timeout_style}]"
### END /etc/grub.d/41_sclab ###
### BEGIN /etc/grub.d/42_smartconfig ###
menuentry 'SmartConfig rescue' --class ubuntu --class gnu-linux --class os --id smartconfig-rescue {
\tload_video
\tinsmod gzio
\tsearch --no-floppy --fs-uuid --set=root 0b1c
\techo\t'SmartConfig rescue: root read-only, /etc/fstab ignored'
\tlinux\t/vmlinuz-6.8.0-142-generic root=UUID=1bfe no_timer_check console=tty1 console=ttyS0 ro fstab=no \
systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1
\tinitrd\t/initrd.img-6.8.0-142-generic
}
# SmartConfig: the last boot did not come up healthy, so show the menu.
if [ "${smartconfig_pending}" = "1" ] ; then
\tset timeout_style=menu
\tif [ "${timeout}" = "0" ] ; then
\t\tset timeout=30
\tfi
fi
### END /etc/grub.d/42_smartconfig ###
### BEGIN /etc/grub.d/43_sclab ###
echo "sclab: post timeout=[${timeout}] style=[${timeout_style}]"
### END /etc/grub.d/43_sclab ###
"""

# What the research lab's kernels printed (q5 fixture), with no_timer_check.
EXP_DEFAULT = ("BOOT_IMAGE=/vmlinuz-6.8.0-142-generic root=UUID=1bfe ro no_timer_check console=tty1 "
               "console=ttyS0")


class TestGrubCfg(unittest.TestCase):
    def test_entries(self):
        entries = e2e.grub_entries(GRUB_CFG)
        self.assertEqual([(x["title"], x["id"]) for x in entries], [
            ("Ubuntu", "gnulinux-simple-1bfe"), ("UEFI Firmware Settings", "uefi-firmware"),
            ("SmartConfig rescue", "smartconfig-rescue")])
        default, n = e2e.linux_args(entries[0]["body"])
        self.assertEqual((default, n), (EXP_DEFAULT, 1))
        rescue, n = e2e.linux_args(entries[2]["body"])
        self.assertTrue(rescue.endswith(" ro " + e2e.RESCUE_ARGS), rescue)
        self.assertEqual(e2e.last_ro_rw(rescue.split()), "ro")
        self.assertEqual(e2e.last_ro_rw("a rw b ro c rw".split()), "rw")
        self.assertEqual(e2e.last_console(rescue.split()), "console=ttyS0")
        self.assertEqual(e2e.linux_args("fwsetup\n"), (None, 0))
        self.assertEqual(e2e.grub_entries("menuentry 'It'\\''s' --id x {\n\tlinux /v a\n}\n")[0]["title"], "It's")

    def test_kernel_line_matches(self):
        # The kernel's "Command line:" as the research lab saw it.
        text = serialmux.clean(fixture("q5-menu-rescue.raw")).decode("utf-8", "replace")
        line = [x for x in text.split("\n") if "Command line: " in x][0].split("Command line: ", 1)[1]
        self.assertEqual(e2e.norm_cmdline(line), e2e.norm_cmdline(
            "BOOT_IMAGE=/vmlinuz-6.8.0-142-generic root=UUID=1bfefdfb-34c9-46c7-854f-399d0a1dea04 ro "
            "console=tty1 console=ttyS0 " + e2e.RESCUE_ARGS))

    def test_flag_block_after_recordfail(self):
        rf = e2e.RECORDFAIL_BLOCK.search(GRUB_CFG)
        self.assertEqual(rf.group(1), "0")
        self.assertGreater(GRUB_CFG.find(e2e.FLAG_BLOCK), rf.start())
        self.assertLess(GRUB_CFG.find("sclab: pre "), GRUB_CFG.find(e2e.FLAG_BLOCK))
        self.assertGreater(GRUB_CFG.find("sclab: post "), GRUB_CFG.find(e2e.FLAG_BLOCK))
        # The flag block as scripts/42_smartconfig writes it.
        with open(os.path.join(HERE, "..", "scripts", "42_smartconfig")) as f:
            self.assertIn(e2e.FLAG_BLOCK, f.read())


class TestMenuAndObservers(unittest.TestCase):
    def test_uefi_menu_with_flag(self):
        text = serialmux.clean(fixture("s2-uefi-menu-ctrln.raw")).decode("utf-8", "surrogateescape")
        obs = console.observer_lines(text)
        self.assertEqual(e2e.observer_problems(obs, "efi", "1", "30", "menu"), [])
        self.assertTrue(e2e.observer_problems(obs, "efi", "", "0", "hidden"))
        self.assertTrue(e2e.observer_problems(obs, "pc", "1", "30", "menu"))
        self.assertIn("pre platform=[efi]", e2e.observers_seen(obs))
        # The first draw: *Ubuntu, 30s (cut before the first key).
        first = text[:text.index("29s.")]
        menu = console.parse_menu(first)
        self.assertEqual(e2e.menu_problems(menu, first.split("\n")), ([], []))
        self.assertEqual(menu.entries, e2e.MENU_ENTRIES["uefi"])

    def test_bios_menus(self):
        rows = console.vgatext(fixture("vga-bios-menu.bin"))
        menu = console.parse_menu(rows)
        self.assertEqual(e2e.menu_problems(menu, rows, (30, 29)), ([], []))
        self.assertEqual(menu.entries, e2e.MENU_ENTRIES["bios"])
        rows = console.vgatext(fixture("vga-bios-menu-adv.bin"))
        p, _ = e2e.menu_problems(console.parse_menu(rows), rows, (30, 29))
        self.assertTrue(any("highlight" in x for x in p), p)
        self.assertEqual(e2e.menu_problems(None, []), (["no GRUB menu"], []))
        rows = console.vgatext(fixture("vga-bios-hidden.bin"))
        self.assertIsNone(console.parse_menu(rows))
        self.assertFalse(e2e.whole_menu(rows))
        self.assertTrue(e2e.whole_menu(console.vgatext(fixture("vga-bios-menu.bin"))))
        # After the first key GRUB clears the countdown: not the menu as first drawn.
        self.assertFalse(e2e.whole_menu(console.vgatext(fixture("vga-bios-menu-rescue.bin"))))
        obs = console.observer_lines(rows)
        self.assertEqual(e2e.observer_problems(obs, "pc", "", "0", "hidden"), [])
        self.assertTrue(any("SeaBIOS" in r for r in rows))
        self.assertEqual(e2e.observer_problems([], "pc", "", "0", "hidden"),
                         ["no 'sclab: pre' line", "no 'sclab: post' line"])


GOLDEN_A = console.load_golden("rescue-a")
REPORT_A = """\
This boot:     3c4d5e6f (rescue), root read-only
Last healthy:  2026-10-04 12:00, boot 1a2b3c4d
Failed since:  1 boot, last 10-04 12:05: never reached multi-user
scd:           not running

Changed since the last healthy boot, worst first:
c7146c 10-04 12:03  blocker fstab-source-missing, line 4  /etc/fstab

To put /etc/fstab back:
  mount -o remount,rw /
  sc restore 1d5b0a
  sync
  systemctl daemon-reload
  systemctl reboot
The menu shows once more: the first entry, Ubuntu, is the one.
"""


class TestReport(unittest.TestCase):
    def test_tolerate_and_match(self):
        noisy = REPORT_A.replace("scd:           not running", "scd:           running (pid 812)").replace(
            "/etc/fstab\n\nTo put", "/etc/fstab\n9f8e7d 12:04  no problem found  /etc/hosts\n"
            "1 more file (sc status lists them all)\n\nTo put")
        lines, notes = e2e.tolerate_report(noisy.split("\n"))
        self.assertEqual(lines, REPORT_A.split("\n"))
        self.assertEqual(len(notes), 3)
        r = console.match_golden(lines, GOLDEN_A, {"GOOD": "1d5b0a", "BAD": "c7146c", "B1": B1, "N": 4})
        self.assertTrue(r.ok, r.problems)
        self.assertEqual((r.values["K boots"], r.values["B3:8"]), ("1 boot", "3c4d5e6f"))
        self.assertEqual(e2e.tolerate_report(REPORT_A.split("\n")), (REPORT_A.split("\n"), []))

    def test_undo_commands(self):
        cmds = e2e.undo_commands(REPORT_A.split("\n"))
        self.assertEqual(cmds, [c.format(GOOD="1d5b0a") for c in e2e.UNDO])
        self.assertEqual(e2e.undo_commands(["To undo /etc/x, which is new, move it aside:", "  cd /etc/",
                                            "  mv x x.sc-off", "  sync", "next"]), ["cd /etc/", "mv x x.sc-off", "sync"])

    def test_every_golden_gives_the_undo(self):
        for name in ("rescue-a", "rescue-b", "rescue-c", "emergency"):
            lines = console.golden_lines(console.load_golden(name))
            cmds = [c.replace("<GOOD>", "1d5b0a") for c in e2e.undo_commands(lines)]
            want = [c.format(GOOD="1d5b0a") for c in e2e.UNDO]
            self.assertEqual(cmds, want if name != "emergency" else want[1:], name)


class TestHost(unittest.TestCase):
    @staticmethod
    def elf(*types):
        """A 64-bit little-endian ELF header with program headers of types."""
        head = bytearray(64)
        head[:6] = b"\x7fELF\x02\x01"
        head[0x20:0x28] = (64).to_bytes(8, "little")
        head[0x36:0x38] = (56).to_bytes(2, "little")
        head[0x38:0x3a] = len(types).to_bytes(2, "little")
        for t in types:
            ph = bytearray(56)
            ph[:4] = t.to_bytes(4, "little")
            head += ph
        return bytes(head)

    def test_elf_is_static(self):
        self.assertTrue(e2e.elf_is_static(self.elf(1, 1, 4)))  # LOAD, LOAD, NOTE
        self.assertFalse(e2e.elf_is_static(self.elf(1, 3)))  # INTERP
        self.assertFalse(e2e.elf_is_static(self.elf(1, 2)))  # DYNAMIC
        self.assertFalse(e2e.elf_is_static(b"#!/bin/sh\n"))
        self.assertFalse(e2e.elf_is_static(self.elf(1)[:100]))  # cut short

    def test_go_build_problems(self):
        ok = "bin/sc: go1.26.8\n\tpath\tsmartconfig/cmd/sc\n\tbuild\t-ldflags=\"-s -w\"\n\tbuild\tCGO_ENABLED=0\n"
        self.assertEqual(e2e.go_build_problems(ok), [])
        self.assertEqual(e2e.go_build_problems(ok.replace("CGO_ENABLED=0", "CGO_ENABLED=1")),
                         ["CGO_ENABLED=1, not 0"])
        self.assertEqual(e2e.go_build_problems(ok + "\tbuild\t-tags=sctest\n"), ["built with -tags=sctest"])

    def test_in_order(self):
        lines = ["row", "  sc restore 1d5b0a", "x", "  sync", "It takes effect at the next boot (now: ...)."]
        want = [("restore", lambda x: x == "  sc restore 1d5b0a"), ("sync", lambda x: x == "  sync"),
                ("next", lambda x: x.startswith("It takes effect"))]
        self.assertIsNone(e2e.in_order(lines, 1, want))
        self.assertEqual(e2e.in_order(lines, 2, want), "restore")
        self.assertEqual(e2e.in_order(lines[:4], 1, want), "next")

    def test_sh_quote(self):
        s = "printf '%s\\n' 'a b' && echo \"$HOME\" `x`"
        out = subprocess.run(["sh", "-c", "printf '%s' " + e2e.sh_quote(s)], capture_output=True, text=True)
        self.assertEqual(out.stdout, s)

    def test_countdowns_after(self):
        # A polled screen of a 30 s menu that was not there gap s before.
        self.assertEqual(e2e.countdowns_after(None), (30,))
        self.assertEqual(e2e.countdowns_after(0.5), (30, 29))
        self.assertEqual(e2e.countdowns_after(1.0), (30, 29))
        self.assertEqual(e2e.countdowns_after(2.5), (30, 29, 28, 27))
        self.assertEqual(e2e.countdowns_after(100), tuple(range(30, -1, -1)))

    def test_duration(self):
        self.assertEqual(e2e.duration(1450.7), "24m10s")
        self.assertEqual(e2e.duration(5), "0m05s")

    def test_command_line(self):
        env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
        e = os.path.join(HERE, "e2e.py")
        r = subprocess.run([sys.executable, e, "--help"], capture_output=True, text=True, env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        for opt in ("--mode", "--boot2", "--no-boot5", "--keep"):
            self.assertIn(opt, r.stdout)
        r = subprocess.run([sys.executable, e, "--mode", "kvm"], capture_output=True, text=True, env=env)
        self.assertEqual(r.returncode, 2)  # make lab-e2e counts it INCONCLUSIVE, never FAIL


# ---------------------------------------------------------------- the checks against a run with no VM
#
# The static audit of e2e.py listed ways a run could PASS without the
# evidence (risks 1-9). Each test below drives the check itself on a run
# that has no QEMU (FakeRun), and fails if the hole is there again.

REPO = os.path.dirname(HERE)
GOOD, BAD, PRE = "1d5b0a", "c7146c", "a1b2c3"
B5 = "5e6f7081-0000-4000-8000-000000000005"
FSTAB_SHA = "4" * 64
Mark = collections.namedtuple("Mark", "txt inp")


class FakeCon:
    """The serial console as the checks read it: a fixed text (offsets are
    str offsets); cmd() answers by the command's first word(s); typed is
    what the guest prints when something is sent ({what was sent: text})."""

    def __init__(self, text="", cmds=None, typed=None):
        self.t = text
        self.cmds = cmds or {}
        self.typed = typed or {}
        self.sent = []
        self.why = None

    def text(self, start=0, end=None):
        return self.t[start:end]

    def size(self):
        return len(self.t)

    def line_no(self, offset):
        return self.t[:offset].count("\n") + 1

    def cmd(self, command, timeout=180, delay=0.005):
        for prefix, (rc, out) in self.cmds.items():
            if command.startswith(prefix):
                return console.CmdResult(rc, out, 0, 0)
        raise AssertionError("unexpected command %r" % command)

    def expect(self, patterns, timeout, start=None, advance=True, abort=None):
        """console.Console.expect on the text as it is: the earliest match
        (the lower index on a tie), or None at once, as after a timeout."""
        start = start or 0
        text = self.t[start:]
        best = None
        for i, p in enumerate(patterns):
            m = (p if hasattr(p, "search") else re.compile(p, re.M)).search(text)
            if m and (best is None or m.start() < best[1].start()):
                best = (i, m)
        if best is None:
            self.why = "timeout"
            return None
        i, m = best
        return console.Hit(i, m, start + m.start(), start + m.end(), text[:m.start()], 1.0)

    def send(self, data, delay=0.015):
        self.sent.append(data)
        self.t += self.typed.get(data, "")
        return True

    def line(self, text, delay=0.015):
        return self.send(text + "\r", delay)

    def prepare_shell(self, timeout=180):
        return console.CmdResult(0, "", 0, 0)

    def tail(self, n=40):
        return "\n".join(self.t.split("\n")[-n:])


class FakeMux:
    gaps = []

    def __init__(self, inputs=()):
        self._inputs = list(inputs)

    def size(self):
        return 0

    def wait(self, size, timeout):
        return 0

    def inputs(self, start=0):
        return self._inputs[start:]

    def stop(self):
        pass


def _fake_mux_note(self, kind, data):
    self.notes = getattr(self, "notes", []) + [(kind, data)]


FakeMux.note = _fake_mux_note


class FakeWatch:
    """A VgaWatch as the checks see it: one screen (rows), the poll error,
    and the gap before the poll that saw the screen."""

    def __init__(self, rows=None, error=None, gap=0.5):
        now = time.time()
        self.screens = [(now, rows, None, now - gap)] if rows is not None else []
        self.error = error
        self.gap = gap

    def first(self, pred):
        for _, rows, path, _ in self.screens:
            if pred(rows):
                return rows, path, self.gap
        return None

    def last_rows(self):
        return self.screens[-1][1] if self.screens else None

    def all_rows(self):
        return [r for _, rows, _, _ in self.screens for r in rows]

    def files(self):
        return "evidence/vga-r1-000.bin (%d screens)" % len(self.screens)

    def stop(self):
        pass


class FakeHit:
    """A console.Hit of one pattern whose group(1) is text."""

    def __init__(self, text, start=0):
        self.text, self.start, self.end, self.t = text, start, start + len(text), 1.0

    def group(self, *a):
        return self.text


class FakeRun(e2e.E2E):
    """An E2E with no VM, no cache and no lock: what the checks keep and
    read. The tests stub the I/O a check does (ssh, QEMU) on the instance."""

    def __init__(self, mode="uefi", con=None, **values):  # no super(): it makes a run directory in the cache
        self.tmp = tempfile.mkdtemp(prefix="sclab-test-")
        self.args = argparse.Namespace(mode=mode, boot2="natural", no_boot5=False, keep=False, grub_password=False, deb=False)
        self.deb = False
        self.mode = mode
        self.uefi = mode == "uefi"
        self.conf = labvm.load_conf()
        self.run = self.tmp
        self.evdir = os.path.join(self.tmp, "evidence")
        os.makedirs(self.evdir)
        self.t_start = time.monotonic()
        self.deadline = self.t_start + 3600
        self.rows = collections.OrderedDict()
        self.history = []
        self.v = collections.OrderedDict(values)
        self.attempts = []
        self.retries = 0
        self.flag = False
        self.ctx = "P.1"
        self.lab_error = None
        self.machine = self.qmp = self.key = self.ref = None
        self.mux = FakeMux()
        self.con = con or FakeCon()
        self.cur = (Mark(0, 0), None)
        self.cur_vga = None
        self.vga_watches = []
        self.chained_resets = []
        self.marks = {}
        self.sha = {}
        self.head, self.git7, self.dirty = "0" * 40, "0000000", False
        self.logged = []
        self._logf = io.StringIO()
        self.stage = self.boot2_mark = None
        self.staged = self.shell = self.at_prompt = self.torn_down = False
        self.settle_until = 0.0

    def log(self, msg):
        self.logged.append(msg)

    # No wait_for of its own: the real E2E.wait_for runs over FakeCon.expect,
    # so the tests see its panic and abort handling too.

    def screendump(self, name):
        pass

    def status(self, cid):
        return self.rows[cid].status if cid in self.rows else None


def fake_run(test, mode="uefi", **kw):
    r = FakeRun(mode, **kw)
    test.addCleanup(shutil.rmtree, r.tmp, True)
    return r


def attempt(label="2", flag=False, vga=None):
    a = e2e.Attempt(label, 1, Mark(0, 0), None, flag)
    a.vga = vga
    return a


def stops(fn, *args):
    """fn(*args); the Stop it raised, or None."""
    try:
        fn(*args)
    except e2e.Stop as s:
        return s
    return None


def facts_text(*blocks):
    """facts.sh output from (name, rc, lines) blocks, then end."""
    out = []
    for name, rc, lines in blocks:
        out.append("== %s rc=%d" % (name, rc))
        out.extend(lines)
    out.append("== end rc=0")
    return "\n".join(out) + "\n"


def grubenv_g(obs, menu=None, seen=False):
    return {"obs": obs, "menu": menu, "menu_lines": [], "menu_ev": "", "menu_seen": seen, "obs_ev": "x",
            "kernel": FakeHit("x")}


class TestBiosVgaEvidence(unittest.TestCase):
    """Risk 1: on bios, 1.1, 2.1 and 5.1 never pass without VGA evidence:
    a failed poll, or no observer line and no menu, is a [lab] failure."""

    def setUp(self):
        self.hidden = console.vgatext(fixture("vga-bios-hidden.bin"))
        self.obs = console.observer_lines(self.hidden)

    def test_pure(self):
        self.assertEqual(e2e.vga_evidence_problems(None, 3, self.obs, False), [])
        self.assertEqual(e2e.vga_evidence_problems(None, 3, [], True), [])
        self.assertEqual(len(e2e.vga_evidence_problems("pmemsave: QMP gone", 3, self.obs, False)), 1)
        self.assertEqual(len(e2e.vga_evidence_problems(None, 0, [], False)), 1)

    def test_hidden_menu_with_observers_passes(self):
        r = fake_run(self, "bios")
        r.check_decision("2.1", attempt(vga=FakeWatch(self.hidden)), grubenv_g(self.obs), False)
        self.assertEqual(r.status("2.1"), "PASS")

    def test_poll_error_is_lab(self):
        r = fake_run(self, "bios")
        with self.assertRaises(e2e.LabError):
            r.check_decision("2.1", attempt(vga=FakeWatch(self.hidden, error="pmemsave: QMP gone")),
                             grubenv_g(self.obs), False)
        self.assertNotIn("2.1", r.rows)

    def test_no_observer_no_menu_is_lab(self):
        seabios = ["SeaBIOS (version 1.16.3-debian-1.16.3-2)", "Booting from Hard Disk..."]
        for cid in ("1.1", "2.1", "5.1"):
            for w in (FakeWatch(seabios), FakeWatch([]), None):
                r = fake_run(self, "bios")
                with self.assertRaises(e2e.LabError, msg="%s %s" % (cid, w and w.screens)):
                    r.check_decision(cid, attempt(vga=w), grubenv_g([]), False)
                self.assertNotIn(cid, r.rows)

    def test_uefi_unchanged(self):
        # Serial is whole: no observer line there is SmartConfig's (H).
        r = fake_run(self, "uefi")
        s = stops(r.check_decision, "2.1", attempt(), grubenv_g([]), False)
        self.assertIsNotNone(s)
        self.assertEqual(s.row.id, "2.1")
        self.assertEqual(r.rows["2.1"].cause, "M4")


RESCUE_TAIL = ("You are in rescue mode. After logging in, type \"journalctl -xb\" to view\n"
               "system logs, \"systemctl reboot\" to reboot, or \"exit\"\nto continue bootup.\n"
               "Press Enter for maintenance\n(or press Control-D to continue): ")


def rescue_report(letter="a"):
    return REPORT_A.replace("never reached multi-user", e2e.RESCUE_REASONS[letter])


class TestRescueReport(unittest.TestCase):
    """Risks 2 and 3: 3.6 holds the report's reason to boot 2's outcome by
    ssh (b, c), and takes nothing out of the report but status lines."""

    def check(self, outcome, report, **v):
        text = "[    5.012345] Run /init as init process\n" + report + RESCUE_TAIL
        r = fake_run(self, con=FakeCon(text), GOOD=GOOD, BAD=BAD, B1=B1, N=4, outcome=outcome, **v)
        return r, stops(r.check_rescue_report, attempt("3", True), len(text))

    def test_matching_outcomes_pass(self):
        for letter in "abc":
            r, s = self.check(letter, rescue_report(letter))
            self.assertIsNone(s)
            self.assertEqual((r.status("3.6"), r.v["reason"]), ("PASS", letter))
            self.assertEqual(r.v["undo"], [c.format(GOOD=GOOD) for c in e2e.UNDO])
            self.assertEqual((r.v["K"], r.v["B3_short"]), (1, "3c4d5e6f"))

    def test_reason_against_ssh_outcome_is_F(self):
        # B2's verdict line said emergency=inactive (the old code chose
        # golden c from it); ssh saw emergency.target active: outcome b.
        for outcome, said in (("b", "c"), ("c", "b"), ("b", "a"), ("c", "a")):
            r, s = self.check(outcome, rescue_report(said), B2_emergency="inactive" if said == "c" else "active")
            self.assertIsNone(s, outcome + said)  # F: the mode goes on
            row = r.rows["3.6"]
            self.assertEqual((row.status, row.strength, row.cause), ("FAIL", "F", "M4"), outcome + said)
            self.assertIn("outcome by ssh was %s" % outcome, row.notes)
            self.assertEqual(r.v["reason"], said)  # 3.8.boots holds it to the boots file
            self.assertEqual(e2e.verdict(r.rows.values()), "FAIL")

    def test_outcome_a_takes_any_golden(self):
        for said in "bc":
            r, s = self.check("a", rescue_report(said))
            self.assertIsNone(s)
            self.assertEqual((r.status("3.6"), r.v["reason"]), ("WARN", said))

    def test_no_golden_is_H(self):
        r, s = self.check("b", rescue_report("b").replace("sc restore 1d5b0a", "sc restore 999999"))
        self.assertIsNotNone(s)
        self.assertEqual((s.row.id, s.row.strength), ("3.6", "H"))

    def test_scd_running_fails(self):
        r, s = self.check("a", rescue_report("a").replace("scd:           not running",
                                                           "scd:           running (pid 812)"))
        self.assertIsNotNone(s)
        self.assertEqual(r.status("3.6"), "FAIL")

    def test_extra_change_row_fails(self):
        r, s = self.check("c", rescue_report("c").replace(
            "/etc/fstab\n\nTo put", "/etc/fstab\n9f8e7d 12:04  no problem found  /etc/hosts\n\nTo put"))
        self.assertIsNotNone(s)
        self.assertEqual(r.status("3.6"), "FAIL")

    def test_status_lines_are_the_only_tolerance(self):
        r, s = self.check("a", rescue_report("a").replace(
            "scd:           not running\n", "scd:           not running\n[  OK  ] Finished systemd-tmpfiles-setup.service\n"))
        self.assertIsNone(s)
        self.assertEqual(r.status("3.6"), "WARN")
        self.assertIn("systemd status lines taken out", r.rows["3.6"].notes)
        blk = console.report_block(rescue_report("a") + RESCUE_TAIL, end=r"You are in rescue mode")
        self.assertEqual(e2e.rescue_report_lines(blk), (blk.lines, []))

    def test_rescue_golden(self):
        bind = {"GOOD": GOOD, "BAD": BAD, "B1": B1, "N": 4}
        lines = rescue_report("b").split("\n")
        self.assertEqual(e2e.rescue_golden(lines, "b", bind)[::3], ("b", None))
        self.assertEqual(e2e.rescue_golden(lines, "c", bind)[::3], ("b", "F"))
        self.assertEqual(e2e.rescue_golden(lines, "a", bind)[::3], ("b", None))
        self.assertEqual(e2e.rescue_golden(lines[:-2], "a", bind)[::3], (None, "H"))


HASHES = ["/var/lib/smartconfig/changes.db " + "1" * 64, "/var/lib/smartconfig/changes.db-journal -",
          "/var/lib/smartconfig/changes.db-wal " + "2" * 64, "/var/lib/smartconfig/boots " + "3" * 64,
          "/etc/fstab " + FSTAB_SHA]
MOUNTS = ['/ SOURCE="/dev/vda1" LABEL="cloudimg-rootfs" FSTYPE="ext4" OPTIONS="ro,relatime"', "/boot -",
          "/boot/efi -", "/mnt/backup -"]
OK1 = "%s ok 101 7 local-fs=active emergency=inactive rescue=inactive failed-units=0" % B1


def rescue_facts(boots, mounts=MOUNTS, before=HASHES, after=HASHES, **over):
    """facts.sh rescue as a healthy rescue boot prints it; over replaces
    blocks by name: (rc, lines), or None for a block left out."""
    blocks = collections.OrderedDict((
        ("facts", (0, ["mode=rescue", "good=" + GOOD, "bad=" + BAD])),
        ("boot-id", (0, [B3])),
        ("mounts", (0, list(mounts))),
        ("active", (0, ["local-fs.target=inactive", "rescue.target=active", "emergency.target=inactive"])),
        ("show:sc-boot-seen.service", (0, ["Id=sc-boot-seen.service", "ConditionResult=no"])),
        ("show:sc-boot-ok.service", (0, ["Id=sc-boot-ok.service", "ExecMainStartTimestampMonotonic=0"])),
        ("boots", (0, boots.strip().split("\n"))),
        ("sc-status-console", (2, ["  sc restore " + GOOD])),
        ("sc-diff", (1, ["+UUID=3f6c1e2a-9b7d-4c1e-8f2a-5d6e7f8a9b0c /mnt/backup ext4 defaults 0 2"])),
        ("sc-cat-good", (0, [FSTAB_SHA])),
        ("sc-check", (2, ["/etc/fstab: blocker fstab-source-missing, line 4"])),
        ("sc-restore", (1, ["sc: store /var/lib/smartconfig: " + e2e.RESTORE_REFUSED])),
        ("hashes-before", (0, list(before))),
        ("hashes-after", (0, list(after))),
        ("leftovers", (0, [])),
        ("vcs1", (0, ["  sc restore " + GOOD])),
    ))
    blocks.update({k.replace("_", "-"): v for k, v in over.items()})
    return facts_text(*[(n, b[0], b[1]) for n, b in blocks.items() if b is not None])


class TestRescueFacts(unittest.TestCase):
    """Risks 2 and 4 in 3.8: the boots file must give the report's reason;
    a mount point with no line is not "not mounted"; "-" is no hash."""

    def check(self, boots, reason="a", b2=None, **kw):
        r = fake_run(self, con=FakeCon(cmds={"sh ": (0, rescue_facts(boots, **kw))}), GOOD=GOOD, BAD=BAD, B1=B1,
                     B2=b2, FSTAB_SHA=FSTAB_SHA, K=1, reason=reason, B3_short="3c4d5e6f")
        self.assertIsNone(stops(r.check_rescue_facts, attempt("3", True)))
        return r

    def test_good(self):
        r = self.check("%s seen 100\n%s\n%s seen 200\n" % (B1, OK1, B2))
        for cid in ("3.8.ro", "3.8.mounts", "3.8.units", "3.8.boots", "3.8.sc", "3.8.hashes", "3.8.vcs1"):
            self.assertEqual(r.status(cid), "PASS", "%s: %s" % (cid, r.rows[cid].notes))

    def test_boots_reason(self):
        bad = "%s bad 290 9 local-fs=inactive emergency=%s rescue=inactive failed-units=2"
        seen2 = "%s seen 200" % B2
        self.assertIsNone(e2e.boots_reason([]))
        for line, want in ((None, "a"), (bad % (B2, "active"), "b"), (bad % (B2, "inactive"), "c")):
            boots = "\n".join(x for x in ("%s seen 100" % B1, OK1, seen2, line) if x)
            self.assertEqual(e2e.boots_reason(e2e.failed_after(e2e.boots_entries(boots), B1)), want)
            # The report's reason (3.6, outcome a: any golden) must be the boots file's.
            for reason in "abc":
                r = self.check(boots, reason=reason)
                self.assertEqual(r.status("3.8.boots"), "PASS" if reason == want else "FAIL", (want, reason))

    def test_mount_line_missing(self):
        for gone in ("/boot -", "/mnt/backup -"):
            r = self.check("%s seen 100\n%s\n%s seen 200\n" % (B1, OK1, B2), mounts=[m for m in MOUNTS if m != gone])
            self.assertEqual(r.status("3.8.mounts"), "FAIL", gone)
            self.assertIn("no line for", r.rows["3.8.mounts"].notes)

    def test_hash_dash(self):
        boots = "%s seen 100\n%s\n%s seen 200\n" % (B1, OK1, B2)
        dashes = [h.split(" ")[0] + " -" for h in HASHES]
        r = self.check(boots, before=dashes, after=dashes)
        self.assertEqual(r.status("3.8.hashes"), "FAIL")
        for path in e2e.HASHED:
            self.assertIn(path, r.rows["3.8.hashes"].notes)
        self.assertEqual(e2e.hashes_problems(HASHES, HASHES), [])
        self.assertEqual(len(e2e.hashes_problems(HASHES, HASHES[:-1])), 1)


def normal_facts(**over):
    return e2e.Facts(normal_text(**over), "evidence/f.txt")


def normal_text(**over):
    """facts.sh normal as boot 1 of a healthy run prints it (0.7 reads the
    same blocks in boot 0): (name, rc, lines), each replaceable (None:
    left out)."""
    show = ["Result=success", "ExecMainStatus=0", "ConditionResult=yes"]
    blocks = collections.OrderedDict((
        ("grubenv", (0, [])),
        ("fstab", (0, ["LABEL=cloudimg-rootfs / ext4 discard,commit=30,errors=remount-ro 0 1"])),
        ("fstab-sha256", (0, [FSTAB_SHA])),
        ("paths", (0, ["/var/lib/smartconfig/boots -"])),
        ("show:sc-boot-seen.service", (0, ["Id=sc-boot-seen.service"] + show)),
        ("show:sc-boot-ok.service", (0, ["Id=sc-boot-ok.service"] + show)),
        ("journal-sc-boot", (0, ["boot %s: ok (local-fs=active emergency=inactive rescue=inactive "
                                 "failed-units=0)" % B1])),
        ("journal-scd", (0, ["baseline: 14 files"])),
        ("sc-log-fstab", (0, ["%s  2026-10-04 12:00  scd  /etc/fstab  146" % GOOD])),
        ("seen-before", (0, ["Before=grub-common.service shutdown.target sc-boot-ok.service grub-initrd-fallback.service"])),
        ("critical-chain", (0, ["multi-user.target @21.6s", "└─getty.target @21.5s"])),
        ("active", (0, ["local-fs.target=active", "emergency.target=inactive", "rescue.target=inactive",
                        "scd.service=active"])),
        ("boots", (0, ["%s seen 100" % B1, OK1])),
        ("sc-status", (0, status_lines(B1))),
    ))
    blocks.update(over)
    return facts_text(*[(n, b[0], b[1]) for n, b in blocks.items() if b is not None])


def status_lines(boot_id):
    """sc status in a healthy boot (the uefi run of 56f5359, boot 1)."""
    return ["This boot:     %s (normal), root read-write" % console.short_boot(boot_id),
            "Last healthy:  this boot, 2026-10-04 12:00", "scd:           running (pid 630)", "",
            "Nothing recorded has changed since this boot came up."]


class TestFactsBlocks(unittest.TestCase):
    """Risks 4 and 5: a block that is missing or failed is never read as
    "nothing found"; 4.4 wants no smartconfig_pending line at all."""

    def run_check(self, cid, f, **v):
        r = fake_run(self, FSTAB_SHA=FSTAB_SHA, **v)
        fn = {"0.7": r.check_07, "1.6": r.check_16, "1.8": r.check_18, "4.4": r.check_44}[cid]
        stops(fn, f)
        return r.rows[cid]

    def test_good(self):
        for cid in ("0.7", "1.6", "1.8", "4.4"):
            self.assertEqual(self.run_check(cid, normal_facts()).status, "PASS", cid)

    def test_grubenv_unread(self):
        broken = {"grubenv": (1, ["grub-editenv: error: cannot open `/boot/grub/grubenv': No such file"])}
        for cid in ("0.7", "1.6", "4.4"):
            for over in (broken, {"grubenv": None}):
                row = self.run_check(cid, normal_facts(**over))
                self.assertEqual(row.status, "FAIL", "%s %s" % (cid, over))
                self.assertIn("grubenv", row.notes)

    def test_44_empty_flag_line(self):
        for env, status in ((["smartconfig_pending="], "FAIL"), (["smartconfig_pending=1"], "FAIL"),
                            (["next_entry="], "PASS"), (["next_entry=2"], "FAIL"),
                            # A healthy boot unsets recordfail; one left is a lost grubenv write (chunk C).
                            (["recordfail=1"], "FAIL"), (["recordfail="], "PASS")):
            self.assertEqual(self.run_check("4.4", normal_facts(grubenv=(0, env))).status, status, env)
        self.assertEqual(self.run_check("1.6", normal_facts(grubenv=(0, ["recordfail=1"]))).status, "FAIL")
        self.assertEqual(self.run_check("1.6", normal_facts(grubenv=(0, []))).status, "PASS")

    def test_18_critical_chain_unread(self):
        for chain in ((1, ["Failed to get ID: Connection timed out"]), (0, []), None):
            row = self.run_check("1.8", normal_facts(**{"critical-chain": chain}))
            self.assertEqual((row.status, row.strength), ("FAIL", "F"), chain)
        row = self.run_check("1.8", normal_facts(**{"critical-chain": (0, ["multi-user.target @9s",
                                                                           "└─sc-boot-seen.service @1s"])}))
        self.assertIn("names sc-boot-seen", row.notes)
        self.assertEqual(self.run_check("1.8", normal_facts()).status, "PASS")
        # Ordered before a target boot waits for (the M4 final review, A8:
        # the chain alone never shows a oneshot): F, as is an unread order.
        for before in ((0, ["Before=grub-common.service sysinit.target shutdown.target"]),
                       (0, ["Before=basic.target"]), (0, []), (1, ["Failed to get properties"]), None):
            row = self.run_check("1.8", normal_facts(**{"seen-before": before}))
            self.assertEqual((row.status, row.strength), ("FAIL", "F"), before)
        self.assertIn("ordered before sysinit.target",
                      self.run_check("1.8", normal_facts(**{"seen-before": (0, ["Before=sysinit.target"])})).notes)

    def test_units_journal(self):
        r = fake_run(self)
        self.assertIsNone(stops(r.check_units, "1.5", normal_facts(), B1))
        self.assertEqual(r.status("1.5"), "PASS")
        for journal in ((0, []), (0, ["-- No entries --"]), (1, ["Failed to open journal"]), None,
                        (0, ["boot %s: ok (local-fs=active ...)" % B2])):
            r = fake_run(self)
            s = stops(r.check_units, "1.5", normal_facts(**{"journal-sc-boot": journal}), B1)
            self.assertEqual((s and s.row.id, r.status("1.5")), ("1.5", "FAIL"), journal)
            r = fake_run(self)  # 4.6 is F: the mode goes on
            self.assertIsNone(stops(r.check_units, "4.6", normal_facts(**{"journal-sc-boot": journal}), B4))
            self.assertEqual(r.status("4.6"), "FAIL", journal)

    def test_need(self):
        f = normal_facts(grubenv=(2, ["x"]))
        self.assertEqual(f.need("fstab"), [])
        self.assertEqual(len(f.need("grubenv", "nothing", "fstab")), 2)


class TestHeldRows(unittest.TestCase):
    """Risk 6: 4.1's and 5.1's other checks are rows of their own and run
    on every attempt, also when the flag is not known and 4.1/5.1 skip."""

    class Reached(Exception):
        pass

    def boot4_run(self, cmdline=EXP_DEFAULT, keys=()):
        r = fake_run(self, EXP_DEFAULT=EXP_DEFAULT)
        r.mux = FakeMux(keys)
        r.grub_phase = lambda a, cid, menu, pick=None: grubenv_g([])
        r.kernel_cmdline = lambda a, g, what: FakeHit(cmdline)

        def reached(*args):
            raise self.Reached()

        r.wait_healthy_end = reached
        return r

    def test_boot4_cmdline_unknown_flag(self):
        r = self.boot4_run("BOOT_IMAGE=/vmlinuz-6.8.0-142-generic root=UUID=1bfe ro " + e2e.RESCUE_ARGS)
        s = stops(r.boot4, attempt("4", None))
        self.assertIsNotNone(s)
        self.assertEqual(s.row.id, "4.1.cmdline")
        self.assertEqual((r.status("4.1"), r.status("4.1.nokey")), ("SKIP", "PASS"))
        self.assertEqual(e2e.verdict(r.rows.values()), "FAIL")

    def test_boot4_keys_unknown_flag(self):
        r = self.boot4_run(keys=[serialmux.Entry(1.0, "serial", "\\x1b[B", "")])
        s = stops(r.boot4, attempt("4", None))
        self.assertIsNotNone(s)
        self.assertEqual((s.row.id, s.row.cause), ("4.1.nokey", "lab"))
        self.assertEqual(r.status("4.1.cmdline"), "PASS")
        self.assertEqual(e2e.verdict(r.rows.values()), "INCONCLUSIVE")

    def test_boot4_good_goes_on(self):
        r = self.boot4_run()
        with self.assertRaises(self.Reached):
            r.boot4(attempt("4", None))
        self.assertEqual([r.status(c) for c in ("4.1", "4.1.cmdline", "4.1.nokey")], ["SKIP", "PASS", "PASS"])

    def boot5_run(self, boots, serial=""):
        r = fake_run(self, con=FakeCon(serial))
        r.grub_phase = lambda a, cid, menu, pick=None: grubenv_g([])
        r.wait_healthy_end = lambda a, cid: None
        r.wait_ssh = lambda a: B5
        r.poll = lambda fn, budget, every=5.0: fn()
        r.boots_text = lambda strict=False: boots
        return r

    OK5 = "%s seen 500\n%s ok 501 14 local-fs=active emergency=inactive rescue=inactive failed-units=0\n" % (B5, B5)

    def test_boot5_not_ok_unknown_flag(self):
        r = self.boot5_run("%s seen 500\n" % B5)
        s = stops(r.boot5, attempt("5", None))
        self.assertIsNotNone(s)
        self.assertEqual(s.row.id, "5.1.ok")
        self.assertEqual(r.status("5.1"), "SKIP")
        self.assertEqual(e2e.verdict(r.rows.values()), "FAIL")

    def test_boot5_bad_device_unknown_flag(self):
        serial = ("[ TIME ] Timed out waiting for device dev-d…6c1e2a-9b7d-4c1e-8f2a-5d6e7f8a9b0c.device"
                  " - /dev/disk/by-uuid/3f6c1e2a.\n")
        r = self.boot5_run(self.OK5, serial)
        s = stops(r.boot5, attempt("5", None))
        self.assertIsNotNone(s)
        self.assertEqual(s.row.id, "5.1.notime")
        self.assertEqual(r.status("5.1.ok"), "PASS")

    def test_boot5_good(self):
        r = self.boot5_run(self.OK5)
        self.assertIsNone(stops(r.boot5, attempt("5", None)))
        self.assertEqual([r.status(c) for c in ("5.1", "5.1.ok", "5.1.notime")], ["SKIP", "PASS", "PASS"])

    def test_no_boot5_skips_every_5_row(self):
        r = fake_run(self)
        r.args.no_boot5 = True
        r.boot = lambda label, fn: attempt(label, False)
        r.reboot_ssh = r.edit = r.reset_settled = lambda *a: None
        with self.assertRaises(e2e.LabError) as c:  # no boot checked 1.1
            r.flow()
        self.assertIn("1.1 was never checked", str(c.exception))
        r.skip("1.1", "a retried attempt: the menu flag is not known")
        with self.assertRaises(e2e.LabError):  # a SKIP is not a check
            r.flow()
        r.record("1.1")
        r.flow()
        five = [c for c in e2e.REGISTRY if c.startswith("5.")]
        self.assertEqual(len(five), 4)
        self.assertEqual([r.status(c) for c in five], ["SKIP"] * 4)
        self.assertEqual({c: r.status(c) for c in e2e.REGISTRY if c.startswith("6.")},
                         {c: "SKIP" for c in ("6.1", "6.2", "6.3", "6.4", "6.5")})


class TestFreshBuild(unittest.TestCase):
    """Risk 7: P.2 fails unless bin/sc is byte for byte the Makefile's
    build of the tree as it is now."""

    def p2(self, fresh=None, make_rc=0):
        r = fake_run(self)
        sc = os.path.join(r.tmp, "sc")
        data = TestHost.elf(1, 1, 4) + b"this tree"
        with open(sc, "wb") as f:
            f.write(data)
        calls = []

        def run_timed(argv, timeout, input=None):
            calls.append(list(argv))
            if argv[0] == "go":
                return labvm.Result(argv, 0, "%s: go1.26.8\n\tbuild\tCGO_ENABLED=0\n" % sc, "", False, 0.1)
            out = [x for x in argv if x.startswith("BIN=")][0][4:]
            if make_rc == 0:
                with open(out, "wb") as f:
                    f.write(data if fresh is None else fresh)
            return labvm.Result(argv, make_rc, "", "cmd/sc/main.go:9: syntax error" if make_rc else "", False, 0.1)

        with mock.patch.object(e2e.labvm, "run_timed", run_timed):
            s = stops(r.check_p2, sc)
        return r, s, calls, data

    def test_fresh(self):
        r, s, calls, data = self.p2()
        self.assertIsNone(s)
        self.assertEqual(r.status("P.2"), "PASS")
        out = os.path.join(r.tmp, "sc.fresh")
        self.assertEqual(calls[1], ["env", "-u", "MAKEFLAGS", "-u", "MFLAGS", "-u", "MAKELEVEL", "make", "-s", "-C",
                                    e2e.REPO, "build", "BIN=" + out])
        self.assertFalse(os.path.exists(out))
        self.assertEqual(r.v["sc_fresh_sha256"], e2e.hashlib.sha256(data).hexdigest())
        with open(os.path.join(r.evdir, "build-check.txt")) as f:
            self.assertIn(r.v["sc_fresh_sha256"], f.read())

    def test_stale(self):
        r, s, _, _ = self.p2(fresh=TestHost.elf(1, 1, 4) + b"the tree now")
        self.assertIsNotNone(s)
        self.assertEqual((s.row.id, r.rows["P.2"].cause), ("P.2", "lab"))
        self.assertIn("not this tree's build", r.rows["P.2"].notes)
        self.assertNotIn("sc_fresh_sha256", r.v)

    def test_build_fails(self):
        r, s, _, _ = self.p2(make_rc=2)
        self.assertIsNotNone(s)
        self.assertEqual(s.row.id, "P.2")
        self.assertIn("fresh build of this tree failed", r.rows["P.2"].notes)

    def test_makefile_build_takes_bin(self):
        if not shutil.which("make"):
            self.skipTest("no make")
        env = {k: v for k, v in os.environ.items() if k not in ("MAKEFLAGS", "MFLAGS", "MAKELEVEL")}
        out = subprocess.run(["make", "-n", "-C", REPO, "build", "BIN=/x/sc.fresh"], capture_output=True, text=True,
                             env=env)
        self.assertEqual(out.returncode, 0, out.stderr)
        self.assertIn("-o /x/sc.fresh ./cmd/sc", out.stdout)


FAKE_E2E = r"""#!/bin/sh
# lab/e2e.py's stand-in: the summary line for its arguments; FAKE_B5 and
# FAKE_RC override.
shift
b5=yes m=
while [ $# -gt 0 ]; do
	case $1 in --no-boot5) b5=no ;; --mode) m=$2; shift ;; esac
	shift
done
echo "progress $m" >&2
v=PASS
case ${FAKE_RC:-0} in 1) v=FAIL ;; 3) v=INCONCLUSIVE ;; esac
[ -n "$FAKE_SILENT" ] || echo "$v $m 0m01s boot2=${FAKE_B2:-b} goal3=multi-user boot5=${FAKE_B5:-$b5} retries=0 head=x dirty=${FAKE_DIRTY:-no} sc=y"
exit ${FAKE_RC:-0}
"""


class TestBoot5Said(unittest.TestCase):
    """Risk 8: a run without boot 5 says boot5=no, on e2e.py's summary line
    and on make lab-e2e's last line."""

    def test_summary_line(self):
        for no5, want in ((True, " boot5=no "), (False, " boot5=yes ")):
            r = fake_run(self, outcome="b")
            r.args.no_boot5 = no5
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                r.finish("PASS", None)
            line = out.getvalue()
            self.assertEqual(line.count("\n"), 1)
            self.assertIn(want, line)
            self.assertTrue(line.startswith("PASS uefi "), line)

    def main(self, *argv):
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(e2e.labvm, "install_signal_handlers"), \
                mock.patch.object(e2e.labvm, "load_conf", side_effect=e2e.LabError("no conf in this test")), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                rc = e2e.main(list(argv))
            except SystemExit as e:
                rc = ("exit", e.code)
        return rc, out.getvalue()

    def test_main_fallback_line(self):
        rc, out = self.main("--mode", "bios", "--no-boot5")
        self.assertEqual(rc, 3)
        self.assertEqual(out, "INCONCLUSIVE bios - boot2=- goal3=- boot5=no retries=- head=- dirty=- sc=-\n")

    def test_no_abbreviations(self):
        # make lab-e2e looks for --no-boot5 spelt out.
        self.assertEqual(self.main("--mode", "uefi", "--no-boot")[0], ("exit", 2))

    def make(self, args, **env):
        if not (shutil.which("make") and shutil.which("timeout")):
            self.skipTest("no make or timeout")
        d = tempfile.mkdtemp(prefix="sclab-test-")
        self.addCleanup(shutil.rmtree, d, True)
        fake = os.path.join(d, "fake-e2e.sh")
        with open(fake, "w") as f:
            f.write(FAKE_E2E)
        e = {k: v for k, v in os.environ.items() if k not in ("MAKEFLAGS", "MFLAGS", "MAKELEVEL")}
        e.update(env)
        # -o build: the recipe without building bin/sc.
        r = subprocess.run(["make", "-s", "-o", "build", "-C", REPO, "lab-e2e", "LAB=sh " + fake] + args,
                           capture_output=True, text=True, env=e)
        return r.returncode, r.stdout.strip().split("\n")

    def test_make_last_line(self):
        rc, lines = self.make(["LAB_E2E_ARGS=--no-boot5"])
        self.assertEqual((rc, lines[-1]), (0, "lab-e2e: PASS boot5=no"))
        self.assertEqual(len(lines), 3)  # each mode's summary line, then make's
        rc, lines = self.make([])
        self.assertEqual((rc, lines[-1]), (0, "lab-e2e: PASS"))
        # From the summary line too, however boot 5 was left out.
        rc, lines = self.make(["LAB_MODES=uefi"], FAKE_B5="no")
        self.assertEqual(lines[-1], "lab-e2e: PASS boot5=no")
        rc, lines = self.make(["LAB_MODES=uefi", "LAB_E2E_ARGS=--no-boot5"], FAKE_RC="1")
        self.assertEqual((rc, lines[-1]), (2, "lab-e2e: FAIL boot5=no"))
        rc, lines = self.make(["LAB_MODES=uefi"], FAKE_RC="3")
        self.assertEqual((rc, lines[-1]), (2, "lab-e2e: INCONCLUSIVE"))
        # Exit 1 with no FAIL line is the interpreter's (a traceback), not a verdict on sc.
        rc, lines = self.make(["LAB_MODES=uefi"], FAKE_RC="1", FAKE_SILENT="1")
        self.assertEqual((rc, lines[-1]), (2, "lab-e2e: INCONCLUSIVE"))
        # What makes a PASS not count for sign-off is in the last line.
        rc, lines = self.make([], FAKE_DIRTY="yes", FAKE_B2="a(forced)")
        self.assertEqual((rc, lines[-1]), (0, "lab-e2e: PASS dirty=yes boot2=a(forced)"))


class TestVgaGap(unittest.TestCase):
    """Risk 9: on bios a VGA poll gap over MAX_VGA_GAP before the menu's
    first screen is a [lab] retry, not a wider range of countdowns."""

    def setUp(self):
        self.menu = console.vgatext(fixture("vga-bios-menu.bin"))

    def test_menu_countdowns(self):
        self.assertEqual(e2e.MAX_VGA_GAP, 2.0)
        self.assertEqual(e2e.menu_countdowns(None), (30,))
        self.assertEqual(e2e.menu_countdowns(0.6), (30, 29))
        self.assertEqual(e2e.menu_countdowns(2.0), (30, 29, 28))
        with self.assertRaises(e2e.Retry) as c:
            e2e.menu_countdowns(2.1)
        self.assertEqual(c.exception.sig, "vga-gap")

    def test_wait_retries_after_a_gap(self):
        r = fake_run(self, "bios")
        g = {}
        with self.assertRaises(e2e.Retry) as c:
            r.vga_menu_wait(attempt("3", True, FakeWatch(self.menu, gap=7.0)), g, 600)
        self.assertEqual(c.exception.sig, "vga-gap")
        self.assertNotIn("menu", g)

    def test_wait_short_gap(self):
        r = fake_run(self, "bios")
        g = {}
        r.vga_menu_wait(attempt("3", True, FakeWatch(self.menu, gap=0.6)), g, 600)
        self.assertEqual(g["countdowns"], (30, 29))
        self.assertEqual(g["menu"].entries, e2e.MENU_ENTRIES["bios"])

    def test_boot_retries_a_gap_twice(self):
        r = fake_run(self, "bios")
        r.flag = True
        resets = []
        r.reset_vm = lambda why: resets.append(why)

        def fn(a):
            raise e2e.Retry("vga-gap", "test")

        with self.assertRaises(e2e.LabError):
            r.boot("3", fn)
        self.assertEqual((len(resets), r.retries, r.flag), (2, 2, True))
        self.assertEqual([a.result for a in r.attempts], ["retry"] * 3)


UEFI_SILENT = ('BdsDxe: loading Boot0007 "Ubuntu" from HD(15,GPT)/\\EFI\\ubuntu\\shimx64.efi\n'
               'BdsDxe: starting Boot0007 "Ubuntu" from HD(15,GPT)/\\EFI\\ubuntu\\shimx64.efi\n')
PRE_CLEAN = "sclab: pre platform=efi pending=[] recordfail=[] timeout=[0] style=[hidden]\n"
KERNEL = "[    0.000000] Linux version 6.8.0-142-generic\n[    1.217443] smpboot: x86: Booting SMP configuration:\n"


class TestStall(unittest.TestCase):
    """A console that stops before GRUB's configuration ran, or in the
    kernel before /init, is a [lab] retry (README "Stalls"): once a boot,
    with stall-*.txt as evidence. Anywhere later it is no retry."""

    def run_(self, mode="uefi", text=""):
        r = fake_run(self, mode, con=FakeCon(text))
        r.b = lambda key: 0  # every budget has run out
        r.wait_for = lambda a, patterns, budget, start=None, what="it": None
        r.unexpected = lambda a: None
        return r

    def test_uefi_nothing_from_grub(self):
        # The 16:54 uefi run on 225e2d6: shim started, then 596 s of nothing.
        r = self.run_(text=UEFI_SILENT)
        a = attempt("1")
        with self.assertRaises(e2e.Retry) as c:
            r.grub_phase(a, "1.1", menu=False)
        self.assertEqual(c.exception.sig, "stall")
        self.assertIn("shimx64.efi", c.exception.detail)
        self.assertIn("evidence/stall-1-1.txt", c.exception.detail)
        with open(r.evpath("stall-1-1.txt")) as f:
            dump = f.read()
        self.assertIn("mux gaps over 10 s", dump)
        self.assertIn("no QEMU state", dump)  # no machine here: the retry goes on
        with self.assertRaises(e2e.Retry) as c:  # a menu was due (boot 3): the same
            r.grub_phase(attempt("3", True), "3.1", menu=True)
        self.assertEqual(c.exception.sig, "stall")

    def test_uefi_grub_ran(self):
        # The observer line is there: the flag block may have run. No retry.
        for seen in (PRE_CLEAN, "                 GNU GRUB  version 2.12\n"):
            r = self.run_(text=UEFI_SILENT + seen)
            with self.assertRaises(e2e.LabError) as c:
                r.grub_phase(attempt("1"), "1.1", menu=False)
            self.assertNotIsInstance(c.exception, e2e.Retry)
            self.assertIn("no kernel", str(c.exception))
            self.assertEqual(os.listdir(r.evdir), [])

    def test_bios_screens(self):
        seabios = ["SeaBIOS (version 1.16.3-debian-1.16.3-2)", "Booting from Hard Disk..."]
        hidden = console.vgatext(fixture("vga-bios-hidden.bin"))
        menu = console.vgatext(fixture("vga-bios-menu-rescue.bin"))  # no countdown: not the menu waited for
        for rows, error, silent in ((seabios, None, True), (hidden, None, False), (menu, None, False),
                                    (seabios, "screendump failed", False), (None, None, False)):
            r = self.run_("bios")
            a = attempt("1", vga=FakeWatch(rows, error=error))
            self.assertEqual(r.grub_silent(a), silent, (rows, error))
            for menu_due in (False, True):
                a = attempt("1", menu_due, vga=FakeWatch(rows, error=error))
                with self.assertRaises((e2e.Retry, e2e.LabError)) as c:
                    r.grub_phase(a, "1.1", menu=menu_due)
                self.assertEqual(isinstance(c.exception, e2e.Retry), silent, (rows, error, menu_due))
        self.assertFalse(self.run_("bios").grub_silent(attempt("1")))  # no watch at all

    def test_kernel_before_init(self):
        # bios run 2 on 618e4e9, boot 5: the kernel stood at smpboot for 18 minutes.
        r = self.run_(text=UEFI_SILENT + PRE_CLEAN + KERNEL)
        with self.assertRaises(e2e.Retry) as c:
            r.wait_healthy_end(attempt("5"), "5.1")
        self.assertEqual(c.exception.sig, "stall")
        self.assertIn("smpboot: x86: Booting SMP configuration:", c.exception.detail)
        # Userspace began: sc's units may have run. No retry.
        r = self.run_(text=UEFI_SILENT + PRE_CLEAN + KERNEL + "[    5.194221] Run /init as init process\n")
        with self.assertRaises(e2e.LabError) as c:
            r.wait_healthy_end(attempt("5"), "5.1")
        self.assertNotIsInstance(c.exception, e2e.Retry)
        self.assertIn("no login prompt", str(c.exception))

    def test_once_a_boot(self):
        r = fake_run(self)
        r.flag = True
        resets = []
        r.reset_vm = lambda why: resets.append(why)

        def fn(a):
            raise e2e.Retry("stall", "test")

        with self.assertRaises(e2e.LabError) as c:
            r.boot("1", fn)
        self.assertIn("no retry left", str(c.exception))
        self.assertEqual((len(resets), r.retries, r.flag), (1, 1, True))

    def test_dump_with_a_machine(self):
        r = fake_run(self)
        r.mux.gaps = [(431.5, 596.2)]
        self.addCleanup(setattr, FakeMux, "gaps", [])

        class Machine:
            cpu = iter((100.0, 109.5))

            def cpu_seconds(self):
                return next(self.cpu)

        class Qmp:
            def hmp(self, line):
                return "CPU#0\nRIP=000000007e1a2b3c HLT=0\n" if line == "info registers -a" else ""

        r.machine, r.qmp = Machine(), Qmp()
        with mock.patch.object(e2e, "STALL_SAMPLE", 0.01):
            ev = r.stall_dump(attempt("1"), "nothing from GRUB")
        self.assertEqual(ev, "evidence/stall-1-1.txt")
        with open(r.evpath("stall-1-1.txt")) as f:
            dump = f.read()
        self.assertIn("mux gaps over 10 s (seconds since t0, length): +431.5 596.2s", dump)
        self.assertIn("QEMU used 9.5 s of CPU in 0.01 s (2 vCPUs)", dump)
        self.assertEqual(dump.count("RIP=000000007e1a2b3c"), 2)

    def test_observers_after_a_reset(self):
        # recordfail=1 after a reset later than GRUB: 00_header sets no
        # style, and without the flag 42_smartconfig must not either.
        late = console.observer_lines("sclab: pre platform=efi pending=[] recordfail=[1] timeout=[0] style=[]\n"
                                      "sclab: post timeout=[0] style=[]\n")
        clean = console.observer_lines(PRE_CLEAN + "sclab: post timeout=[0] style=[hidden]\n")
        f = e2e.observer_problems
        self.assertEqual(f(late, "efi", "", "0", "hidden", "1"), [])
        self.assertEqual(f(clean, "efi", "", "0", "hidden", ""), [])
        # After a clean boot a recordfail left set is a lost grubenv write
        # (the chunk C fault), not a style to make room for.
        self.assertEqual(f(late, "efi", "", "0", "hidden", ""),
                         ["pre recordfail=[1], not []", "post style=[], not [hidden]"])
        self.assertEqual(f(clean, "efi", "", "0", "hidden", "1"),
                         ["pre recordfail=[], not [1]", "post style=[hidden], not []"])
        obs = console.observer_lines("sclab: pre platform=efi pending=[] recordfail=[1] timeout=[0] style=[]\n"
                                     "sclab: post timeout=[30] style=[menu]\n")
        self.assertEqual(f(obs, "efi", "", "0", "hidden", "1"),
                         ["post timeout=[30], not [0]", "post style=[menu], not []"])
        # With the flag set recordfail is not held to anything.
        obs = console.observer_lines("sclab: pre platform=efi pending=[1] recordfail=[1] timeout=[0] style=[]\n"
                                     "sclab: post timeout=[30] style=[menu]\n")
        self.assertEqual(f(obs, "efi", "1", "30", "menu"), [])

    def test_decision_by_attempt(self):
        # 1.1 on a first attempt holds recordfail to unset; an attempt
        # after one that was reset in its kernel holds it to 1.
        stale = ("sclab: pre platform=efi pending=[] recordfail=[1] timeout=[0] style=[]\n"
                 "sclab: post timeout=[0] style=[]\n")
        for late, text, status in ((False, stale, "FAIL"), (True, stale, "PASS"),
                                   (False, PRE_CLEAN + "sclab: post timeout=[0] style=[hidden]\n", "PASS"),
                                   (True, PRE_CLEAN + "sclab: post timeout=[0] style=[hidden]\n", "FAIL")):
            r = fake_run(self)
            a = attempt("1")
            a.late = late
            g = grubenv_g(console.observer_lines(text))
            stops(r.check_decision, "1.1", a, g, False)
            self.assertEqual(r.status("1.1"), status, (late, text))

    def test_boot_marks_a_late_attempt(self):
        # A retry whose attempt had the kernel's first line makes the next
        # attempt late; a panic after /init also makes the flag unknown.
        for text, sig, late, flag in ((UEFI_SILENT, "stall", False, False),
                                      (UEFI_SILENT + PRE_CLEAN + KERNEL, "stall", True, False),
                                      (UEFI_SILENT + PRE_CLEAN + KERNEL, "panic", True, False),
                                      (UEFI_SILENT + PRE_CLEAN + KERNEL + "Run /init as init process\n", "panic", True, None)):
            r = fake_run(self, con=FakeCon(text))
            r.reset_vm = lambda why: None
            seen = []

            def fn(a):
                seen.append(a.late)
                if len(seen) == 1:
                    raise e2e.Retry(sig, "test")

            r.boot("1", fn)
            self.assertEqual((seen, r.flag), ([False, late], flag), (text[-40:], sig))


class TestVerdictClasses(unittest.TestCase):
    """The chunk D review: what the lab cannot read is never an [M4]
    result, and what sc or its GRUB script got wrong is never a lab
    error to run again."""

    def lost(self, results, gaps_after=()):
        r = fake_run(self)
        r.machine = argparse.Namespace(ssh_port=1)
        results = list(results)

        def ssh(port, key, command, timeout, input=None):
            r.mux.gaps = list(gaps_after)
            return results.pop(0)

        self.addCleanup(setattr, FakeMux, "gaps", [])
        p = mock.patch.object(e2e.labvm, "ssh", ssh)
        p.start()
        self.addCleanup(p.stop)
        return r

    def test_ssh_lost_is_never_an_answer(self):
        r = self.lost([ssh_result(255, err="Connection reset by peer")] * 3)
        with self.assertRaises(e2e.SshLost) as c:
            r.sudo("sc check /etc/fstab")
        self.assertIsInstance(c.exception, e2e.LabError)
        self.assertIn("exit 255", str(c.exception))
        self.assertEqual(r.ssh("true", 5, lost=True).rc, 255)  # the caller reads rc itself
        with self.assertRaises(e2e.SshLost):  # 1.3: not "is-system-running says (Connection reset)"
            r.system_running("1.3")
        self.assertNotIn("1.3", r.rows)

    def test_a_timeout_is_the_guests_unless_the_host_stood_still(self):
        slow = labvm.Result(["ssh"], 124, "", "(timed out after 300 s)", True, 300.0)
        r = self.lost([slow])
        self.assertEqual(r.ssh("sc status", 300).rc, 124)  # sc hanging is sc's: the check fails it
        r = self.lost([slow], gaps_after=[(100.0, 233.0)])
        with self.assertRaises(e2e.SshLost) as c:
            r.ssh("sc status", 300)
        self.assertIn("the host stood still", str(c.exception))

    def test_a_read_across_a_pause_runs_once_more(self):
        # A read whose time ran out while the host stood still runs once
        # more with a fresh budget (the chunk D review's idea); a second
        # timeout with no pause in it is the guest's.
        slow = labvm.Result(["ssh"], 124, "", "(timed out after 300 s)", True, 300.0)
        seen = labvm.Result(["ssh"], 0, "seen\n", "", False, 1.0)
        r = self.lost([slow, seen], gaps_after=[(100.0, 233.0)])
        self.assertEqual(r.sudo("cat /var/lib/smartconfig/boots", retry=True).out, "seen\n")
        self.assertTrue(any("once more" in m for m in r.logged), r.logged)
        r = self.lost([slow, slow], gaps_after=[(100.0, 233.0)])
        self.assertEqual(r.ssh("cat x", 300, retry=True).rc, 124)
        # No pause: the timeout is the guest's, and nothing runs again.
        calls = []
        r = fake_run(self)
        r.machine = argparse.Namespace(ssh_port=1)
        p = mock.patch.object(e2e.labvm, "ssh", lambda *a, **kw: (calls.append(a[2]), slow)[1])
        p.start()
        self.addCleanup(p.stop)
        r.ssh("cat x", 300, retry=True)
        self.assertEqual(calls, ["cat x"])

    def test_the_boot_id_is_asked_twice(self):
        # ssh failing by itself once (a busy guest's silent sshd) does not
        # end the mode; twice it does, as ssh's own (526f14b, bios 6.2).
        bid = "88d315a4-3412-4f33-898d-ed5bc66babbb"
        r = self.lost([ssh_result(255), labvm.Result(["ssh"], 0, bid + "\n", "", False, 1.0)])
        self.assertEqual(r.boot_id(), bid)
        self.assertTrue(any("once more" in m for m in r.logged), r.logged)
        r = self.lost([ssh_result(255)] * 2)
        with self.assertRaises(e2e.SshLost):
            r.boot_id()

    def test_ssh_waits_a_minute_for_a_silent_guest(self):
        opts = " ".join(labvm.SSH_OPTIONS)
        self.assertIn("ServerAliveInterval=5", opts)
        self.assertIn("ServerAliveCountMax=12", opts)

    def test_the_reads_that_run_once_more(self):
        # Only reads: facts.sh normal and the boots file; nothing that
        # changes the guest (the chunk H review: the flag was not pinned).
        seen = []
        r = fake_run(self)

        def ssh(command, timeout, input=None, lost=False, retry=False):
            seen.append((command.split()[-1] if "facts.sh" in command else command, retry))
            return ssh_result(0, out="== end rc=0\n")
        r.ssh = ssh
        r.facts("t")
        r.boots_text()
        r.sudo("grub-editenv /boot/grub/grubenv set smartconfig_pending=1")
        self.assertEqual([x[1] for x in seen], [True, True, False], seen)

    def test_poll(self):
        r = fake_run(self)
        answers = [e2e.SshLost("x"), "", "row"]

        def fn():
            v = answers.pop(0)
            if isinstance(v, Exception):
                raise v
            return v

        with mock.patch.object(e2e.time, "sleep", lambda s: None):
            self.assertEqual(r.poll(fn, 60, every=1.0), "row")  # a lost call is "not yet"
            with self.assertRaises(e2e.SshLost):  # the last word was ssh's own: no verdict
                r.poll(lambda: (_ for _ in ()).throw(e2e.SshLost("gone")), 0)
            self.assertEqual(r.poll(lambda: "", 0), "")

    def test_poll_gives_back_a_gap(self):
        r = fake_run(self)
        self.addCleanup(setattr, FakeMux, "gaps", [])
        clock = [1000.0]
        r.deadline = 9000.0
        calls = []

        def fn():
            calls.append(clock[0])
            if len(calls) == 2:  # the host stands still for 233 s
                clock[0] += 233
                r.mux.gaps = [(1.0, 233.0)]
            return "row" if clock[0] >= 1300 else ""

        def sleep(secs):
            clock[0] += secs

        with mock.patch.object(e2e.time, "monotonic", lambda: clock[0]), mock.patch.object(e2e.time, "sleep", sleep):
            self.assertEqual(r.poll(fn, 120, every=10.0), "row")  # 120 s of the lab's own time, not of the wall's

    def bios_boot3(self, rows):
        r = fake_run(self, "bios", con=FakeCon("[    0.000000] Linux version 6.8.0-142-generic\n"))
        r.b = lambda key: 0
        r.wait_for = lambda a, patterns, budget, start=None, what="it": FakeHit("Linux version 6.8.0", 0)
        r.unexpected = lambda a: None
        a = attempt("3", True, vga=FakeWatch(rows))
        try:
            r.grub_phase(a, "3.1", menu=True)
        except (e2e.Retry, e2e.Stop) as e:
            return r, e
        return r, None

    def test_bios_no_menu_with_recordfail(self):
        # After boot 2 (a) recordfail is 1 and 00_header sets no style. A
        # flag that was never set leaves "timeout=[0] style=[]" on the
        # screen: GRUB drew no menu. That is 3.1 failed, not a menu the
        # polling missed.
        pre = "sclab: pre platform=pc pending=[] recordfail=[1] timeout=[0] style=[]"
        for post, kind in (("sclab: post timeout=[0] style=[]", e2e.Stop),
                           ("sclab: post timeout=[0] style=[hidden]", e2e.Stop),
                           ("sclab: post timeout=[30] style=[menu]", e2e.Retry)):
            r, e = self.bios_boot3(["SeaBIOS", pre, post])
            self.assertIsInstance(e, kind, post)
            if kind is e2e.Stop:
                self.assertEqual((r.rows["3.1"].status, r.rows["3.1"].cause), ("FAIL", "M4"))
            else:
                self.assertEqual(e.sig, "menu-missed")
        r, e = self.bios_boot3(["SeaBIOS", "Booting from Hard Disk..."])  # the menu wiped them: missed by the polling
        self.assertEqual(e.sig, "menu-missed")

    def test_rescue_wait_ran_out(self):
        up = UEFI_SILENT + PRE_CLEAN + KERNEL + "[    5.194221] Run /init as init process\n"
        r = fake_run(self, con=FakeCon(up))
        with self.assertRaises(e2e.Stop):  # sc's report never returned: 3.5 failed, not a run to repeat
            r.ran_out("3.5", attempt("3", True), 0, "no 'Press Enter for maintenance' 900 s after the kernel")
        self.assertEqual((r.rows["3.5"].status, r.rows["3.5"].cause), ("FAIL", "M4"))
        self.addCleanup(setattr, FakeMux, "gaps", [])
        r = fake_run(self, con=FakeCon(up))
        r.mux.gaps = [(700.0, 400.0)]  # the host stood still in the wait
        with self.assertRaises(e2e.LabError) as c:
            r.ran_out("3.5", attempt("3", True), 0, "no prompt")
        self.assertNotIsInstance(c.exception, e2e.Stop)
        self.assertNotIn("3.5", r.rows)
        FakeMux.gaps = []
        r = fake_run(self, con=FakeCon(UEFI_SILENT + PRE_CLEAN + KERNEL))  # the kernel never reached /init
        with self.assertRaises(e2e.LabError):
            r.ran_out("3.5", attempt("3", True), 0, "no prompt")
        self.assertNotIn("3.5", r.rows)

    def test_a_byte_lost_on_serial_is_not_the_command_line(self):
        # A bios run of 85e9f59, boot 3: the kernel's first line reached
        # serial as "oot=UUID=" (the host had just stood still for 379 s);
        # its second line, 0.1 s of guest time later, was whole.
        exp = ("BOOT_IMAGE=/vmlinuz-6.8.0-142-generic root=UUID=1bfefdfb-34c9-46c7-854f-399d0a1dea04 no_timer_check "
               "console=tty1 console=ttyS0 ro fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1")
        lossy = exp.replace(" root=", " oot=")
        first = "[    0.000000] Command line: %s\n"
        second = "[    0.098611] Kernel command line: %s\n"

        def run(text, expected=exp):
            r = fake_run(self, con=FakeCon(text))
            a = attempt("3", True)
            hit = r.wait_for(a, [e2e.CMDLINE_RX], 0)
            s = stops(r.check_cmdline, "3.4", a, {}, expected, hit)
            return r.rows["3.4"], s

        row, s = run(first % exp + second % exp)
        self.assertEqual((row.status, row.notes), ("PASS", ""))
        row, s = run(first % lossy + "[    0.000000] BIOS-provided physical RAM map:\n" + second % exp)
        self.assertEqual((row.status, row.seen), ("PASS", exp))
        self.assertIn("serial lost part of the first", row.notes)
        self.assertIn("oot=UUID=", row.notes)
        # Both lines wrong: that is the command line (42_smartconfig wrote it).
        row, s = run(first % lossy + second % lossy)
        self.assertEqual((row.status, row.cause, row.seen), ("FAIL", "M4", lossy))
        self.assertIsNotNone(s)
        # The second line whole but not what grub.cfg says: no help.
        row, s = run(first % exp + second % exp, expected=exp + " quiet")
        self.assertEqual(row.status, "FAIL")
        # No second line (yet): the first stands.
        row, s = run(first % lossy)
        self.assertEqual((row.status, row.seen), ("FAIL", lossy))

    def test_bios_wiped_observers_are_a_note(self):
        # bios, the flag set, a menu shown: the menu wipes the observers'
        # lines, as it must. That is a note in the row (a healthy run has
        # it in every 3.1 and 4.1), not a WARN, and not lost either: the
        # menu's own notes once overwrote it (the test agent found that).
        rows = console.vgatext(fixture("vga-bios-menu.bin"))
        menu = console.parse_menu(rows)
        r = fake_run(self, "bios")
        a = attempt("3", True, vga=FakeWatch(rows))
        g = grubenv_g([], menu=menu, seen=True)
        g["menu_lines"] = rows
        g["countdowns"] = (30, 29)
        stops(r.check_decision, "3.1", a, g, True)
        row = r.rows["3.1"]
        self.assertEqual(row.status, "PASS", row.notes)
        self.assertIn("observers: no 'sclab: pre' line", row.notes)

    def test_summary_says_forced(self):
        line = e2e.summary_line("PASS", "bios", "20m00s", "a", 0, "h", "no", "s", "yes", True)
        self.assertIn(" boot2=a(forced) goal3=early-only ", line)
        self.assertIn(" boot2=a goal3=", e2e.summary_line("PASS", "bios", "20m00s", "a", 0, "h", "no", "s", "yes"))


def qmp_event(seq, name, guest, secs):
    return {"seq": seq, "event": name, "data": {"guest": guest, "reason": "guest-reset" if guest else "host"},
            "timestamp": {"seconds": int(secs), "microseconds": int(round((secs % 1) * 1e6))}, "t": secs}


class FakeQmp:
    """QMP's event list as settle_reset reads it: wait_event returns at
    once (the events are all there already); late ones are in the list
    only after a sync (the reader still had them), or "gone": QMP fails."""

    def __init__(self, events, late=()):
        self.events = events
        self.late = late
        self.waits = []
        self.syncs = 0

    def wait_event(self, names, since=0, timeout=None, abort=None):
        self.waits.append((since, timeout))
        for ev in self.events[since:]:
            if ev["event"] in names:
                return ev
        return None

    def sync(self):
        self.syncs += 1
        if self.late == "gone":
            raise e2e.labvm.QmpError("QMP is closed: cannot run query-status")
        self.events.extend(self.late)
        self.late = ()

    def events_since(self, since, names=None):
        return [e for e in self.events[since:] if names is None or e["event"] in names]


class FakeMachine:
    def alive(self):
        return True


class StartedWatch:
    started = []

    def __init__(self, machine, prefix, hz=2.0):
        self.prefix = prefix

    def start(self):
        StartedWatch.started.append(self.prefix)
        return self


class TestResetChain(unittest.TestCase):
    """bios: one guest reboot gives two guest RESETs 15 ms apart (the first
    bios run, QMP seq 3 and 4). The boot begins at the second; a later or
    a host reset is not chained, and the boot's waits see it."""

    def test_chained_reset(self):
        first = qmp_event(3, "RESET", True, 100.0)
        self.assertEqual(e2e.RESET_CHAIN, 2.0)
        self.assertTrue(e2e.chained_reset(first, qmp_event(4, "RESET", True, 100.015)))
        self.assertTrue(e2e.chained_reset(first, qmp_event(4, "RESET", True, 102.0)))
        self.assertFalse(e2e.chained_reset(first, qmp_event(4, "RESET", True, 102.5)))
        self.assertFalse(e2e.chained_reset(first, qmp_event(4, "RESET", False, 100.015)))
        self.assertFalse(e2e.chained_reset(first, qmp_event(4, "SHUTDOWN", True, 100.015)))
        self.assertFalse(e2e.chained_reset(first, None))
        self.assertTrue(e2e.chained_reset(qmp_event(3, "RESET", False, 100.0), qmp_event(4, "RESET", True, 100.5)))
        self.assertEqual(e2e.qmp_time({"t": 7.5}), 7.5)

    def boot_at(self, mode, events, first, late=()):
        r = fake_run(self, mode)
        r.qmp, r.machine = FakeQmp(events, late), FakeMachine()
        r.marks = {e["seq"]: Mark(10 * e["seq"], e["seq"]) for e in events + list(late if late != "gone" else [])}
        StartedWatch.started = []
        with mock.patch.object(e2e, "VgaWatch", StartedWatch):
            got = r.new_boot(events[first])
        return r, got

    def test_bios_double_reset(self):
        events = [qmp_event(0, "RESUME", False, 1.0), qmp_event(1, "RESET", True, 100.0),
                  qmp_event(2, "RESET", True, 100.015)]
        r, got = self.boot_at("bios", events, 1)
        self.assertEqual(got["seq"], 2)
        self.assertEqual(r.cur, (Mark(20, 2), events[2]))
        self.assertEqual(r.con.pos, 20)
        a = e2e.Attempt("1", 1, r.cur[0], r.cur[1], False)
        self.assertEqual(a.since, 3)  # the second RESET is not "the guest did RESET by itself"
        # The VGA watch started at the first RESET, once: no early screen is lost.
        self.assertEqual([os.path.basename(p) for p in StartedWatch.started], ["vga-r1"])
        self.assertEqual(r.chained_resets, [[1, 2, 0.015]])
        self.assertTrue(any("second guest RESET 15 ms" in x for x in r.logged))

    def test_single_reset(self):
        events = [qmp_event(0, "RESET", True, 100.0)]
        r, got = self.boot_at("uefi", events, 0)
        self.assertIs(got, events[0])
        self.assertEqual((r.cur[1], r.chained_resets, StartedWatch.started), (events[0], [], []))
        self.assertEqual((r.qmp.waits, r.qmp.syncs), ([(1, e2e.RESET_CHAIN)], 1))

    def test_a_chained_reset_the_reader_still_holds(self):
        """The --deb bios run of 1c5850f: the second RESET, 13 ms after the
        first by QEMU's clock, was 2.5 s in QMP's reader (its serial mark),
        past the wait; then 1.1 took it for a reset of its own."""
        events = [qmp_event(0, "RESUME", False, 1.0), qmp_event(1, "RESET", True, 100.0)]
        late = [qmp_event(2, "RESET", True, 100.013)]
        r, got = self.boot_at("bios", events, 1, late)
        self.assertEqual((got["seq"], r.chained_resets, r.qmp.syncs), (2, [[1, 2, 0.013]], 2))
        self.assertEqual(e2e.Attempt("1", 1, r.cur[0], r.cur[1], False).since, 3)

    def test_qmp_gone_at_the_sync(self):
        events = [qmp_event(0, "RESET", True, 100.0)]
        r, got = self.boot_at("bios", events, 0, "gone")
        self.assertIs(got, events[0])
        self.assertEqual(r.chained_resets, [])
        self.assertTrue(any("QMP sync after a RESET: QMP is closed" in x for x in r.logged))

    def test_late_or_host_reset_not_chained(self):
        for second in (qmp_event(1, "RESET", True, 103.0), qmp_event(1, "RESET", False, 100.01),
                       qmp_event(1, "SHUTDOWN", True, 100.01)):
            events = [qmp_event(0, "RESET", True, 100.0), second]
            r, got = self.boot_at("bios", events, 0)
            self.assertIs(got, events[0])
            self.assertEqual(r.chained_resets, [])
            a = e2e.Attempt("1", 1, r.cur[0], r.cur[1], False)
            self.assertEqual(a.since, 1)  # unexpected() still sees the late one

    def test_host_reset_then_firmware(self):
        """2.7's system_reset may be chained too; reset_vm still returns
        the host's RESET for 2.7 to judge."""
        events = [qmp_event(0, "RESET", False, 100.0), qmp_event(1, "RESET", True, 100.2)]
        r = fake_run(self, "uefi")
        r.qmp, r.machine = FakeQmp(events), FakeMachine()
        r.qmp.mark = lambda: 0
        r.machine.system_reset = lambda: None
        r.marks = {0: Mark(0, 0), 1: Mark(5, 0)}
        ev = r.reset_vm("2.7")
        self.assertIs(ev, events[0])
        self.assertIs(r.cur[1], events[1])

    def test_ledger_has_them(self):
        events = [qmp_event(0, "RESET", True, 100.0), qmp_event(1, "RESET", True, 100.015)]
        r, _ = self.boot_at("uefi", events, 0)
        r.save_ledger()
        with open(os.path.join(r.run, "ledger.json")) as f:
            self.assertEqual(json.load(f)["chained_resets"], [[0, 1, 0.015]])


def ssh_result(rc, out="", err=""):
    return argparse.Namespace(rc=rc, out=out, err=err, secs=0.0, ok=rc == 0)


class TestGrubPassword(unittest.TestCase):
    """--grub-password (6.x): the README's recipe, judged by its output,
    and GRUB's two prompts answered on serial. The boots themselves (and
    the VGA path in bios) are the lab run's."""

    OK = ("12:menuentry 'Ubuntu' --class ubuntu --class gnu-linux --class gnu --class os --unrestricted "
          "$menuentry_id_option 'gnulinux-simple-x' {")

    def test_the_recipe(self):
        for want in ("grub-mkpasswd-pbkdf2", 'set superusers="admin"', "export superusers", "password_pbkdf2 admin ",
                     "/etc/grub.d/40_custom", "/gnulinux-simple/s/", "--unrestricted", "update-grub",
                     "grep -n -- --unrestricted /boot/grub/grub.cfg"):
            self.assertIn(want, e2e.GRUB_RECIPE)
        subprocess.run(["sh", "-n", "-c", e2e.GRUB_RECIPE], check=True)
        self.assertEqual(e2e.recipe_problems(0, self.OK + "\n", ""), ([], [self.OK]))
        for rc, out in ((1, self.OK + "\n"), (0, ""), (0, self.OK + "\n" + self.OK.replace("'Ubuntu'", "'Ubuntu, with Linux 6.8'")),
                        (0, self.OK.replace("'Ubuntu'", "'SmartConfig rescue'"))):
            self.assertTrue(e2e.recipe_problems(rc, out, "update-grub: error")[0], (rc, out))

    def test_both_prompts_answered_on_serial(self):
        con = FakeCon("GNU GRUB\nEnter username: ", typed={"admin\r": "\nEnter password: ", "sclabpw7\r": "\n"})
        r = fake_run(self, "uefi", con=con)
        r.grub_login(attempt("6b"), {"enter_pos": 9})
        self.assertEqual(con.sent, ["admin\r", "sclabpw7\r"])
        self.assertEqual(r.rows["6.3"].status, "PASS")

    def test_no_prompt_stops(self):
        # The rescue entry booted without asking: the recipe does not hold.
        for text, typed in (("GNU GRUB\n[    0.000000] Linux version 6.8.0", {}),
                            ("GNU GRUB\nEnter username: ", {"admin\r": "\n[    0.000000] Linux version 6.8.0"})):
            con = FakeCon(text, typed=typed)
            r = fake_run(self, "uefi", con=con)
            s = stops(r.grub_login, attempt("6b"), {"enter_pos": 9})
            self.assertEqual((s.row.id, s.row.status), ("6.3", "FAIL"), text)
            self.assertNotIn("sclabpw7\r", con.sent)

    POST_HIDDEN = "sclab: post timeout=[0] style=[hidden]\n"

    @staticmethod
    def outcome(fn, *a):
        try:
            return fn(*a)
        except (e2e.Stop, e2e.LabError, e2e.Retry) as e:
            return e

    def test_a_prompt_in_the_default_boot_fails_6_2(self):
        # The recipe broke: GRUB stops at its prompt and nobody types. It
        # was a [lab] timeout after 600 s; it is 6.2's failure (the chunk H
        # review). On serial (uefi) and on the VGA screen (bios).
        r = fake_run(self, con=FakeCon(UEFI_SILENT + PRE_CLEAN + self.POST_HIDDEN + "Enter username: "))
        end = self.outcome(r.boot6a, attempt("6a", False))
        self.assertIsInstance(end, e2e.Stop)
        self.assertEqual((r.rows["6.2"].status, r.rows["6.2"].cause), ("FAIL", "M4"))
        r = fake_run(self, "bios", con=FakeCon(""))
        r.b = lambda key: 1
        end = self.outcome(r.boot6a, attempt("6a", False, vga=FakeWatch(["", "  Enter username:", ""])))
        self.assertIsInstance(end, e2e.Stop)
        self.assertIn("GRUB asked", r.rows["6.2"].notes)

    def test_a_prompt_after_ubuntu_fails_6_5(self):
        con = FakeCon(UEFI_SILENT + PRE_FLAG + POST_FLAG + UEFI_MENU)
        r = fake_run(self, con=con)

        def pick(a, g, target, cid="3.2"):
            g["enter_pos"] = con.size()
            con.t += "\nEnter username: "
        r.pick = pick
        end = self.outcome(r.boot6c, attempt("6c", True))
        self.assertIsInstance(end, e2e.Stop)
        self.assertEqual((r.rows["6.5"].status, r.rows["6.5"].cause), ("FAIL", "M4"))

    def test_6a_waits_for_its_verdict(self):
        # 6.3 sets the flag next: an ok verdict given after that would unset
        # it (the chunk H review). 6.2 needs boot 6a's ok line.
        for boots, status in (("", "FAIL"), (OK1.replace(B1, B2) + "\n", "PASS")):
            r = fake_run(self, con=FakeCon(UEFI_SILENT + PRE_CLEAN + self.POST_HIDDEN + KERNEL))
            r.wait_healthy_end = lambda a, cid: None
            r.wait_ssh = lambda a: B2
            r.boots_text = lambda strict=False: boots
            r.b = lambda key: 0
            self.outcome(r.boot6a, attempt("6a", False))
            self.assertEqual(r.rows["6.2"].status, status, boots)

    def test_a_retried_6b_or_6c_with_the_flag_unknown_is_the_labs(self):
        # An ok verdict in the attempt before may have cleared the flag: no
        # menu then is not the recipe's (the chunk H review).
        for label, fn in (("6b", "boot6b"), ("6c", "boot6c")):
            r = fake_run(self, con=FakeCon(UEFI_SILENT + PRE_CLEAN + self.POST_HIDDEN + KERNEL))
            end = self.outcome(getattr(r, fn), attempt(label, None))
            self.assertIsInstance(end, e2e.LabError, label)
            self.assertEqual(dict(r.rows), {}, label)

    def rescue_boot(self, cmdline, after_kernel, gap=False):
        con = FakeCon(UEFI_SILENT + PRE_FLAG + POST_FLAG + UEFI_MENU, typed={"\r": "\nroot@sclab:~# "})
        r = fake_run(self, con=con)
        r.mux.gaps = []
        expect = con.expect

        def expect_with_a_gap(patterns, timeout, **kw):  # the host stands still during the wait
            if gap and e2e.PROMPT_RX in patterns:
                r.mux.gaps.append((10.0, 400.0))
            return expect(patterns, timeout, **kw)
        con.expect = expect_with_a_gap
        kernel = "\n" + e2e.RESCUE_ECHO + "\n[    0.000000] Linux version 6.8.0-142-generic\n" + \
            "[    0.000000] Command line: BOOT_IMAGE=/vmlinuz root=UUID=x %s\n" % cmdline + after_kernel

        def pick(a, g, target, cid="3.2"):
            g["enter_pos"] = con.size()
            con.t += kernel
        r.pick = pick
        r.grub_login = lambda a, g: None
        r.qmp = argparse.Namespace(mark=lambda: 0)
        r.wait_reset = lambda since, budget, names, what: {"event": "RESET", "data": {"guest": True}}
        r.new_boot = lambda ev: ev
        self.addCleanup(setattr, FakeMux, "gaps", [])
        return r, self.outcome(r.boot6b, attempt("6b", True))

    def test_64_the_rescue_boot(self):
        good = "ro fstab=no systemd.unit=rescue.target SYSTEMD_SULOGIN_FORCE=1"
        up = "[    2.1] Run /init as init process\nPress Enter for maintenance\n"
        r, end = self.rescue_boot(good, up)
        self.assertIsNone(end)
        self.assertEqual(r.rows["6.4"].status, "PASS")
        r, end = self.rescue_boot(good.replace("fstab=no ", ""), up)  # the entry lost fstab=no
        self.assertEqual((r.rows["6.4"].status, "fstab=no" in r.rows["6.4"].notes), ("FAIL", True))
        # No prompt: before /init, or with the host standing still, the
        # lab's (as boot 3's ran_out); with userspace up and no gap, 6.4's.
        for after, gap, want in (("", False, e2e.LabError), ("[    2.1] Run /init as init process\n", True, e2e.LabError),
                                 ("[    2.1] Run /init as init process\n", False, e2e.Stop)):
            r, end = self.rescue_boot(good, after, gap)
            self.assertIsInstance(end, want, (after, gap))

    def test_a_panic_in_the_rescue_boot_fails_its_check(self):
        r = fake_run(self, con=FakeCon("[    3.0] Kernel panic - not syncing: VFS: Unable to mount root fs\n"))
        r.at("6.4")
        end = self.outcome(r.panicked, attempt("6b", True), "Kernel panic - not syncing: VFS")
        self.assertIsInstance(end, e2e.Stop)
        self.assertEqual(r.rows["6.4"].status, "FAIL")

    def test_bios_prompts_on_the_vga_screen_since_the_enter(self):
        r = fake_run(self, "bios")
        r.b = lambda key: 1
        r.unexpected = lambda a: None
        a = attempt("6b", True, vga=FakeWatch(["Enter username:"]))  # the boot's own watch: before the Enter
        a.vga2 = FakeWatch([""])
        self.assertFalse(r.grub_prompt(a, {"enter_pos": 0}, e2e.GRUB_USER_RX, 1))
        a.vga2 = FakeWatch(["", "Enter username: "])
        self.assertTrue(r.grub_prompt(a, {"enter_pos": 0}, e2e.GRUB_USER_RX, 1))
        # A watch that failed is the lab's (the chunk H review).
        a.vga2 = FakeWatch(error="pmemsave: QMP gone")
        self.assertIsInstance(self.outcome(r.grub_login, a, {"enter_pos": 0}), e2e.LabError)
        self.assertNotIn("6.3", r.rows)

    def test_bios_types_each_key_then_enter(self):
        r = fake_run(self, "bios")
        keys = []
        r.machine = argparse.Namespace(sendkey=keys.append)
        r.grub_type("sclabpw7")
        self.assertEqual(keys, list("sclabpw7") + ["ret"])

    def test_the_stage_sets_the_flag_before_the_rescue_boot(self):
        r = fake_run(self)
        r.sudo = lambda command, timeout=None, lost=False, retry=False: ssh_result(0, out=self.OK + "\n")
        r.reboot_ssh = lambda *a: None
        seen = []
        r.boot = lambda label, fn: seen.append((label, r.flag))
        r.flag = False
        r.grub_password()
        self.assertEqual(seen, [("6a", False), ("6b", True), ("6c", True)])

    def test_not_with_no_boot5(self):
        with self.assertRaises(SystemExit), contextlib.redirect_stderr(io.StringIO()):
            e2e.main(["--mode", "uefi", "--no-boot5", "--grub-password"])

    def test_the_summary_line_says_so(self):
        self.assertTrue(e2e.summary_line("PASS", "uefi", "1m", "b", 0, "h", "no", "s", "yes", grubpw=True).endswith(" grubpw=yes"))
        self.assertNotIn("grubpw", e2e.summary_line("PASS", "uefi", "1m", "b", 0, "h", "no", "s", "yes"))
        for no5, want in ((False, True), (True, False)):  # 6.x never run without boot 5
            r = fake_run(self, outcome="b")
            r.args.grub_password, r.args.no_boot5 = True, no5
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                r.finish("PASS", None)
            self.assertEqual("grubpw=yes" in out.getvalue(), want, no5)


class TestDeb(unittest.TestCase):
    """--deb (D.x, M5): the package's upgrade, remove, purge, takeover of a
    hand install and install again, judged from what the guest answers,
    one fault at a time (the M5 review, B5); 0.4 against the package's own
    files."""

    UP = "0.4.99+git3.20261008023503.643cec3+lab1"
    DEB4 = "dpkg -i %s/smartconfig.deb" % e2e.GUEST_DIR  # D.4's and D.5's; D.1 installs smartconfig-up.deb
    ADDING = "Adding SmartConfig rescue entry: /boot/vmlinuz-6.8.0-142-generic\ndone\n"
    TAKEN = "".join("smartconfig: the hand-installed %s is now %s.dpkg-old\n" % (f, f) for f in e2e.HAND_PATHS)
    UNITS = "".join("unit=%s /usr/lib/systemd/system/%s.service enabled\n" % (u, u) for u in ("scd", "sc-boot-seen", "sc-boot-ok"))
    POST = ("".join("aside=%s\n" % f for f in e2e.HAND_PATHS) +
            "there=/etc/grub.d/42_smartconfig\nscd=active\npid=1300\nexe=/usr/sbin/sc\nrescue=1\n" + UNITS +
            "".join("dropin=%s /usr/lib/systemd/system/%s.service.d/50-smartconfig.conf\n" % (s, s) for s in ("rescue", "emergency")) +
            "grubd=" + "c" * 64 + "\nwhich=/usr/sbin/sc\n")
    REMOVED = ("sc=gone\nscd=inactive\nrescue=0\n" +
               "".join("unit=%s /dev/null masked\n" % u for u in ("scd", "sc-boot-seen", "sc-boot-ok")) + "rows=14\nboots=abc\n")
    AGAIN = "grubd=" + "c" * 64 + "\nscd=active\npid=1400\nexe=/usr/sbin/sc\nrescue=1\n" + UNITS

    def guest(self, **over):
        """A fake sudo: the answers of a guest where all went well, but
        what over replaces (by the command's first words). An answer in a
        tuple exits 1; a list is one answer for each call, in turn."""
        answers = {
            "echo pid=": "pid=611\nrows=12\nboots=abc\n",
            "dpkg -i %s/smartconfig-up.deb" % e2e.GUEST_DIR: "Setting up smartconfig (%s) ...\n%s" % (self.UP, self.ADDING),
            "echo version=": "version=%s\nscd=active\npid=742\nrows=14\nboots=abc\nrescue=1\n" % self.UP,
            "grub-editenv": "flag=set\npid=742\nrows=14\nboots=abc\n",
            "dpkg -r": ["", ""],
            "test -e /usr/sbin/sc": self.REMOVED,
            "dpkg -P": "smartconfig: the change history in /var/lib/smartconfig is kept; to delete it: sudo rm -r /var/lib/smartconfig\n",
            "test -e /etc/grub.d": "grubd=gone\nrows=14\nboots=abc\n",
            "set -e": "", "echo scd=": "scd=active\npid=900\nexe=/usr/local/sbin/sc\nrescue=1\n",
            self.DEB4: [self.TAKEN + "Setting up smartconfig\n" + self.ADDING, "Setting up smartconfig\n" + self.ADDING],
            "for f in": self.POST,
            "pid=$(systemctl": "pid=1300\nexe=/usr/sbin/sc\n",
            "echo grubd=": self.AGAIN,
        }
        answers.update(over)
        r = fake_run(self)
        r.deb = True
        r.v["deb_up_version"] = self.UP
        r.sha["/etc/grub.d/42_smartconfig"] = "c" * 64
        r.asked = []

        def sudo(command, timeout=None, lost=False, retry=False):
            r.asked.append(command)
            for k, out in answers.items():
                if command.startswith(k):
                    if isinstance(out, list):
                        out = out.pop(0)
                    rc = 1 if isinstance(out, tuple) else 0
                    return ssh_result(rc, out=out[0] if isinstance(out, tuple) else out)
            raise AssertionError("unexpected %r" % command)
        r.sudo = sudo
        return r

    def test_all_well(self):
        r = self.guest()
        r.deb_lifecycle()
        self.assertEqual([r.status(c) for c in ("D.1", "D.2", "D.3", "D.4", "D.5")], ["PASS"] * 5)
        self.assertEqual(r.rows["D.4"].seen, "7 of 7 aside; scd active /usr/sbin/sc; rescue=1; sc /usr/sbin/sc")
        self.assertEqual(r.rows["D.5"].seen, "42_smartconfig the package's; scd active /usr/sbin/sc; rescue=1; verify clean")
        # D.4 asks for each NAME.dpkg-old that is a file and cannot run.
        post = [c for c in r.asked if c.startswith("for f in")]
        self.assertIn('[ -f "$f.dpkg-old" ] && [ ! -x "$f.dpkg-old" ] && echo "aside=$f"', post[0])
        # D.2 set the flag, and read it back, before the remove (B4).
        flag = [c for c in r.asked if c.startswith("grub-editenv")]
        self.assertEqual(len(flag), 1)
        self.assertIn("grub-editenv /boot/grub/grubenv list | grep -qx smartconfig_pending=1 && echo flag=set", flag[0])

    def test_what_fails_each(self):
        def up(old, new):
            after = "version=%s\nscd=active\npid=742\nrows=14\nboots=abc\nrescue=1\n" % self.UP
            self.assertIn(old, after)
            return {"echo version=": after.replace(old, new)}

        def removed(old, new):
            self.assertIn(old, self.REMOVED)
            return {"test -e /usr/sbin/sc": self.REMOVED.replace(old, new)}

        def post(old, new):
            self.assertIn(old, self.POST)
            return {"for f in": self.POST.replace(old, new)}

        def again(old, new):
            self.assertIn(old, self.AGAIN)
            return {"echo grubd=": self.AGAIN.replace(old, new)}

        purged = "grubd=gone\nrows=14\nboots=abc\n"
        taken = self.TAKEN + "Setting up smartconfig\n" + self.ADDING
        for over, cid, says in (
                (up("version=" + self.UP, "version=0.4.99"), "D.1", "dpkg-query says 0.4.99"),
                (up("scd=active", "scd=failed"), "D.1", "scd is failed"),
                (up("pid=742", "pid=611"), "D.1", "not restarted"),
                (up("rows=14", "rows=9"), "D.1", "sc log has 9 rows, before 12"),
                (up("boots=abc", "boots=fff"), "D.1", "the boots file: fff, before abc"),
                (up("rescue=1", "rescue=2"), "D.1", "2 rescue entries"),
                ({"dpkg -i %s/smartconfig-up.deb" % e2e.GUEST_DIR: ("dpkg: error\n" + self.ADDING,)}, "D.1", "dpkg -i: exit 1"),
                ({"dpkg -i %s/smartconfig-up.deb" % e2e.GUEST_DIR: "Setting up smartconfig\n"}, "D.1",
                 "postinst's update-grub added no rescue entry"),
                ({"dpkg -i %s/smartconfig-up.deb" % e2e.GUEST_DIR: self.ADDING +
                  "smartconfig: update-grub failed; grub.cfg has no rescue entry yet: run sudo update-grub\n"}, "D.1",
                 "postinst: update-grub failed"),
                ({"dpkg -r": [("dpkg: error",), ""]}, "D.2", "dpkg -r: exit 1"),
                (removed("sc=gone", "sc=there"), "D.2", "sc=there, not gone"),
                (removed("scd=inactive", "scd=active"), "D.2", "scd still active"),
                (removed("rescue=0", "rescue=1"), "D.2", "rescue=1, not 0"),
                (removed("rescue=0\n", "rescue=0\nflag=1\n"), "D.2", "grubenv: smartconfig_pending=1"),
                (removed("unit=sc-boot-seen /dev/null masked", "unit=sc-boot-seen /usr/lib/systemd/system/sc-boot-seen.service enabled"),
                 "D.2", "the units: "),
                (removed("rows=14", "rows=0"), "D.2", "sc log has 0 rows"),
                (removed("boots=abc", "boots="), "D.2", "the boots file"),
                ({"dpkg -P": ("smartconfig: the change history in /var/lib/smartconfig is kept",)}, "D.3", "dpkg -P: exit 1"),
                ({"test -e /etc/grub.d": purged.replace("grubd=gone", "grubd=there")}, "D.3", "42_smartconfig there"),
                ({"test -e /etc/grub.d": purged.replace("rows=14", "rows=0")}, "D.3", "sc log has 0 rows"),
                ({"test -e /etc/grub.d": purged.replace("boots=abc", "boots=fff")}, "D.3", "the boots file"),
                ({"dpkg -P": ""}, "D.3", "did not say"),
                ({self.DEB4: [(taken,), ""]}, "D.4", "dpkg -i: exit 1"),
                ({self.DEB4: [taken.replace("/etc/grub.d/42_smartconfig is", "x is"), self.ADDING]}, "D.4",
                 "did not name /etc/grub.d/42_smartconfig"),
                ({self.DEB4: [self.TAKEN, self.ADDING]}, "D.4", "postinst's update-grub added no rescue entry"),
                (post("aside=/etc/systemd/system/scd.service\n", ""), "D.4", "no /etc/systemd/system/scd.service.dpkg-old"),
                (post("there=/etc/grub.d/42_smartconfig\n", "there=/etc/grub.d/42_smartconfig\nthere=/usr/local/sbin/sc\n"),
                 "D.4", "/usr/local/sbin/sc still there"),
                (post("exe=/usr/sbin/sc", "exe=/usr/local/sbin/sc.dpkg-old"), "D.4", "runs /usr/local/sbin/sc.dpkg-old"),
                (post("pid=1300", "pid=900"), "D.4", "MainPID 900 (before 900)"),
                (post("pid=1300", "pid=0"), "D.4", "MainPID 0"),
                (post("scd=active", "scd=failed"), "D.4", "scd failed"),
                (post("unit=scd /usr/lib/systemd/system/scd.service", "unit=scd /etc/systemd/system/scd.service"), "D.4",
                 "unit scd /etc/systemd/system/scd.service enabled"),
                (post("sc-boot-ok.service enabled", "sc-boot-ok.service disabled"), "D.4",
                 "unit sc-boot-ok /usr/lib/systemd/system/sc-boot-ok.service disabled"),
                (post("dropin=rescue ", "dropin=rescue /etc/systemd/system/rescue.service.d/50-smartconfig.conf "), "D.4",
                 "rescue.service drop-ins: /etc/"),
                (post("dropin=emergency /usr/lib/systemd/system/emergency.service.d/50-smartconfig.conf", "dropin=emergency "),
                 "D.4", "emergency.service drop-ins: none"),
                (post("rescue=1", "rescue=2"), "D.4", "2 rescue entries"),
                (post("which=/usr/sbin/sc", "which=/usr/local/sbin/sc"), "D.4", "sc in PATH is /usr/local/sbin/sc"),
                (post("grubd=" + "c" * 64, "grubd=" + "f" * 64), "D.4", "not the package's"),
                ({"dpkg -r": ["", ("dpkg: error",)]}, "D.5", "dpkg -r: exit 1"),
                ({self.DEB4: [taken, "smartconfig: the hand-installed /etc/grub.d/42_smartconfig is now "
                                     "/etc/grub.d/42_smartconfig.dpkg-old\n" + self.ADDING]}, "D.5",
                 "the package's own files taken for a hand install"),
                ({self.DEB4: [taken, "Setting up smartconfig\n"]}, "D.5", "postinst's update-grub added no rescue entry"),
                (again("grubd=" + "c" * 64, "grubd="), "D.5", "42_smartconfig is not the package's"),
                (again("rescue=1", "rescue=0"), "D.5", "0 rescue entries"),
                (again("exe=/usr/sbin/sc", "exe=/usr/local/sbin/sc"), "D.5", "runs /usr/local/sbin/sc"),
                (again("pid=1400", "pid=1300"), "D.5", "MainPID 1300 (before 1300)"),
                (again("unit=scd /usr/lib/systemd/system/scd.service enabled", "unit=scd /usr/lib/systemd/system/scd.service disabled"),
                 "D.5", "unit scd /usr/lib/systemd/system/scd.service disabled"),
                ({"echo grubd=": self.AGAIN + "verify=missing   c /etc/grub.d/42_smartconfig\n"}, "D.5",
                 "dpkg --verify: missing   c /etc/grub.d/42_smartconfig"),
        ):
            r = self.guest(**over)
            s = stops(r.deb_lifecycle)
            self.assertEqual((s and s.row.id, r.status(cid)), (cid, "FAIL"), over)
            self.assertIn(says, r.rows[cid].notes, over)
            self.assertEqual(len(r.rows[cid].notes.split("; ")), 1, (over, r.rows[cid].notes))  # one fault, one problem

    def test_the_flag_not_set_is_the_labs(self):
        r = self.guest(**{"grub-editenv": "pid=742\nrows=14\nboots=abc\n"})
        with self.assertRaisesRegex(e2e.LabError, "D.2: the menu flag could not be set"):
            r.deb_lifecycle()
        self.assertFalse(any(c.startswith("dpkg -r") for c in r.asked))

    def test_a_hand_install_that_does_not_come_up_is_the_labs(self):
        for over in ({"set -e": ("install: cannot stat",)}, {"echo scd=": "scd=failed\npid=0\nexe=\nrescue=1\n"},
                     {"echo scd=": "scd=active\npid=900\nexe=/usr/sbin/sc\nrescue=1\n"},
                     {"echo scd=": "scd=active\npid=900\nexe=/usr/local/sbin/sc\nrescue=0\n"}):
            r = self.guest(**over)
            with self.assertRaisesRegex(e2e.LabError, "D.4: the hand install did not come up"):
                r.deb_lifecycle()
            self.assertEqual(r.status("D.3"), "PASS")

    def test_the_hand_files_are_the_m4_tags(self):
        r = fake_run(self)
        r.stage = r.tmp
        calls = []

        def run_timed(argv, timeout, input=None, cwd=None):
            calls.append((argv, cwd))
            if argv[2] == "m4:scripts/42_smartconfig":
                return labvm.Result(argv, 0, "#! /bin/sh\n# 42_smartconfig — m4\n", "", False, 0.1)
            return labvm.Result(argv, 0, "[Unit]\n", "", False, 0.1)

        with mock.patch.object(e2e.labvm, "run_timed", run_timed):
            lines = r.stage_hand()
        self.assertEqual([c[0] for c in calls], [["git", "show", "m4:" + src] for _, src in e2e.HAND])
        self.assertEqual({c[1] for c in calls}, {e2e.REPO})
        with open(os.path.join(r.tmp, "hand-42_smartconfig"), "rb") as f:
            data = f.read()
        self.assertEqual(data, "#! /bin/sh\n# 42_smartconfig — m4\n".encode())  # the bytes git gave
        self.assertIn("%s  hand-42_smartconfig\n" % e2e.hashlib.sha256(data).hexdigest(), lines)
        self.assertEqual(len(lines), 5)

        def no_tag(argv, timeout, input=None, cwd=None):
            return labvm.Result(argv, 128, "", "fatal: invalid object name 'm4'.\n", False, 0.1)

        with mock.patch.object(e2e.labvm, "run_timed", no_tag), \
                self.assertRaisesRegex(e2e.LabError, "git show m4:scripts/scd.service: exit 128 .*git fetch --tags"):
            r.stage_hand()

    def test_stage_debs(self):
        """Both packages, the sha256 of the first's files, and D.4's hand
        files: MANIFEST's lines for all."""
        r = fake_run(self)
        r.stage = os.path.join(r.tmp, "stage")
        os.makedirs(r.stage)
        calls = []

        def run_timed(argv, timeout, input=None, cwd=None):
            calls.append(argv[:3])
            if argv[:2] == ["sh", "scripts/version.sh"]:
                return labvm.Result(argv, 0, "0.4.99+git1.abc\n", "", False, 0.1)
            if "scripts/build-deb.sh" in argv:
                with open(argv[-1], "wb") as f:
                    f.write(argv[1].encode())
            elif argv[0] == "dpkg-deb":
                for _, dest in e2e.DEB_FILES:  # this tree's files, but the one other
                    with open(os.path.join(e2e.REPO, e2e.DEB_SOURCES[dest]), "rb") as f:
                        data = f.read() if dest != other else b"another\n"
                    os.makedirs(os.path.dirname(argv[-1] + dest), exist_ok=True)
                    with open(argv[-1] + dest, "wb") as f:
                        f.write(data)
            return labvm.Result(argv, 0, "[Unit]\n" if argv[0] == "git" else "", "", False, 0.1)

        other = None
        if not os.path.exists(os.path.join(e2e.REPO, "bin/sc")):
            self.skipTest("no bin/sc (make build)")
        with mock.patch.object(e2e.labvm, "run_timed", run_timed):
            lines = r.stage_debs()
        names = [l.split()[1] for l in lines]
        self.assertEqual(names, ["smartconfig.deb", "smartconfig-up.deb"] + [n for n, _ in e2e.HAND])
        self.assertEqual((r.v["deb_version"], r.v["deb_up_version"]), ("0.4.99+git1.abc", "0.4.99+git1.abc+lab1"))
        with open(os.path.join(e2e.REPO, "bin/sc"), "rb") as f:
            self.assertEqual(r.sha["/usr/sbin/sc"], e2e.hashlib.sha256(f.read()).hexdigest())
        # The build's stand-ins in the environment are not passed on (B6).
        self.assertEqual(calls[1:3], [["env", "-u", "SC_BIN"]] * 2)
        # A package file not this tree's: the lab cannot vouch for it.
        for other in ("/usr/sbin/sc", "/usr/lib/systemd/system/emergency.service.d/50-smartconfig.conf"):
            with mock.patch.object(e2e.labvm, "run_timed", run_timed), \
                    self.assertRaisesRegex(e2e.LabError, "the package's %s is not this tree's" % other):
                r.stage_debs()

    def test_04_takes_the_package_files(self):
        r = fake_run(self)
        r.deb = True
        for name in ("41_sclab", "43_sclab"):
            r.sha[name] = "a" * 64
        for _, dest in e2e.DEB_FILES:
            r.sha[dest] = "b" * 64
        lines = ["file=/etc/grub.d/41_sclab 755 root:root " + "a" * 64, "file=/etc/grub.d/43_sclab 755 root:root " + "a" * 64]
        lines += ["file=%s %s root:root %s" % (dest, mode, "b" * 64) for mode, dest in e2e.DEB_FILES]
        inst = e2e.parse_kv("\n".join(["manifest=ok", "install_rc=0", "deb_rc=0"] + lines +
                                       ["enable_rc=0", "enable_scd_rc=0", "update_grub_rc=0", "done=1"]))
        r.check_04(ssh_result(0), inst, "install.txt")
        self.assertEqual(r.status("0.4"), "PASS", r.rows["0.4"].notes)
        for k, bad in (("deb_rc", "1"), ("enable_rc", "1"), ("enable_scd_rc", "3"), ("manifest", "fail"), ("done", "0")):
            r = fake_run(self)
            r.deb, r.sha = True, dict(r.sha, **{n: "a" * 64 for n in ("41_sclab", "43_sclab")},
                                      **{dest: "b" * 64 for _, dest in e2e.DEB_FILES})
            stops(r.check_04, ssh_result(0), dict(inst, **{k: [bad]}), "install.txt")
            self.assertEqual(r.rows["0.4"].notes, "%s=%s" % (k, bad))
        inst["file"] = [l.replace(" 755 root", " 644 root") if "/usr/sbin/sc" in l else l for l in inst["file"]]
        stops(r.check_04, ssh_result(0), inst, "install.txt")
        self.assertIn("/usr/sbin/sc", r.rows["0.4"].notes)

    def test_the_summary_line_says_so(self):
        self.assertTrue(e2e.summary_line("PASS", "uefi", "1m", "b", 0, "h", "no", "s", "yes", deb=True).endswith(" deb=yes"))
        with self.assertRaises(SystemExit), contextlib.redirect_stderr(io.StringIO()):
            e2e.main(["--mode", "uefi", "--no-boot5", "--deb"])


class TestFlowSkips(unittest.TestCase):
    """flow()'s skip lists (the M5 review, B5): --no-boot5 skips 5.x, 6.x
    and D.x; without --grub-password 6.x are skipped, without --deb D.x."""

    def flow(self, no_boot5=False, deb=False, grubpw=False):
        r = fake_run(self)
        r.args.no_boot5, r.args.grub_password, r.deb = no_boot5, grubpw, deb
        ran = []
        r.boot = lambda label, fn: argparse.Namespace(flag=False)
        for name in ("reboot_ssh", "decided", "edit", "reset_settled", "deb_lifecycle", "grub_password"):
            setattr(r, name, (lambda n: lambda *a, **k: ran.append(n) or {"data": {"guest": True}})(name))
        r.machine = argparse.Namespace(wait_exit=lambda timeout: 0)
        r.flow()
        return r, ran

    def skipped(self, r, prefix):
        return {r.status(c) for c in e2e.REGISTRY if c.startswith(prefix)}

    def test_no_boot5(self):
        r, ran = self.flow(no_boot5=True, deb=True, grubpw=True)
        for prefix in ("5.", "6.", "D."):
            self.assertEqual(self.skipped(r, prefix), {"SKIP"}, prefix)
        self.assertNotIn("deb_lifecycle", ran)

    def test_deb_without_grub_password(self):
        r, ran = self.flow(deb=True)
        self.assertEqual(self.skipped(r, "6."), {"SKIP"})
        self.assertIn("deb_lifecycle", ran)
        self.assertNotIn("SKIP", self.skipped(r, "D."))

    def test_without_deb(self):
        r, ran = self.flow(grubpw=True)
        self.assertEqual(self.skipped(r, "D."), {"SKIP"})
        self.assertEqual((r.status("5.2"), "grub_password" in ran, "deb_lifecycle" in ran), ("PASS", True, False))


class TestRebootSsh(unittest.TestCase):
    """A bios run of 9f29ea5: ssh lost the guest (exit 255 after 20 s, no
    answer to its keepalives) on 1.0's reboot, which never happened. A
    lost command is sent again only when the guest answers from the same
    boot with no reboot queued; otherwise the guest's RESET decides."""

    RESET = qmp_event(1, "RESET", True, 100.0)

    def reboot(self, answers, events):
        r = fake_run(self)
        r.qmp, r.machine = FakeQmp(events), FakeMachine()
        r.qmp.mark = lambda: 0
        r.new_boot = lambda ev: ev
        calls = []

        def ssh(command, timeout=None, lost=False, retry=False):
            calls.append(command)
            return answers.pop(0)
        r.ssh = ssh
        try:
            return r.reboot_ssh("1.0"), calls, r.logged
        except e2e.LabError as e:
            return e, calls, r.logged

    def sent(self, calls):
        return [c for c in calls if "systemd-run" in c]

    def test_reboot(self):
        got, calls, _ = self.reboot([ssh_result(0, B1 + "\n"), ssh_result(0)], [self.RESET])
        self.assertIs(got, self.RESET)
        self.assertEqual(self.sent(calls), ["sudo -n sync; sudo -n systemd-run --unit=sclab-reboot --on-active=3 "
                                            "systemctl reboot"])

    def test_lost_before_queued_is_sent_again(self):
        got, calls, logged = self.reboot([ssh_result(0, B1 + "\n"), ssh_result(255),
                                          ssh_result(3, "%s\ninactive\ninactive\n" % B1), ssh_result(0)], [self.RESET])
        self.assertIs(got, self.RESET)
        self.assertEqual(len(self.sent(calls)), 2)
        self.assertIn("sclab-reboot.timer sclab-reboot.service", calls[2])
        self.assertTrue(any("sent again" in x for x in logged))

    def test_lost_after_queued_waits(self):
        for answer in (ssh_result(0, "%s\nactive\ninactive\n" % B1), ssh_result(0, "%s\ninactive\nactivating\n" % B1),
                       ssh_result(0, "%s\ninactive\ninactive\n" % B2), ssh_result(255)):
            got, calls, _ = self.reboot([ssh_result(0, B1 + "\n"), ssh_result(255), answer], [self.RESET])
            self.assertIs(got, self.RESET, answer)
            self.assertEqual(len(self.sent(calls)), 1, answer)

    def test_lost_and_no_reset_is_lab(self):
        got, _, _ = self.reboot([ssh_result(0, B1 + "\n"), ssh_result(255), ssh_result(255)], [])
        self.assertIsInstance(got, e2e.LabError)
        self.assertIn("no RESET after reboot", str(got))

    def test_lost_twice_is_lab(self):
        got, calls, _ = self.reboot([ssh_result(0, B1 + "\n"), ssh_result(255),
                                     ssh_result(3, "%s\ninactive\ninactive\n" % B1), ssh_result(255)], [self.RESET])
        self.assertIsInstance(got, e2e.LabError)
        self.assertIn("exit 255", str(got))
        self.assertEqual(len(self.sent(calls)), 2)

    def test_refused_is_lab(self):
        got, calls, _ = self.reboot([ssh_result(0, B1 + "\n"), ssh_result(1, err="sudo: a password is required")],
                                    [self.RESET])
        self.assertIsInstance(got, e2e.LabError)
        self.assertEqual(len(calls), 2)


class TestBoot2Probe(unittest.TestCase):
    """The second bios run: the console getty hung up boot 2's emergency
    shell, systemd started default.target again, ssh answered while the
    device was awaited once more, then emergency mode came back and ssh
    went. multi-user.target was never reached, so no verdict: outcome a,
    not c; 2.5 is W (plan A7: the report is lost then); 2.6 never reads a
    failed ssh as an empty boots file."""

    TRANSIENT = ssh_result(3, "%s\ninactive\ninactive\n" % B2)

    def test_parse_probe(self):
        self.assertEqual(e2e.parse_probe(3, "%s\ninactive\nactive\n" % B2), (B2, "inactive", "active"))
        self.assertEqual(e2e.parse_probe(0, "%s\nactive\nactive\n" % B2), (B2, "active", "active"))
        self.assertIsNone(e2e.parse_probe(255, ""))
        self.assertIsNone(e2e.parse_probe(3, "%s\ninactive\n" % B2))
        self.assertIsNone(e2e.parse_probe(0, "Connection reset\nby peer\nx\n"))

    def test_probe_outcome(self):
        self.assertEqual(e2e.probe_outcome((B2, "inactive", "active")), "c")
        self.assertEqual(e2e.probe_outcome((B2, "active", "active")), "b")
        self.assertIsNone(e2e.probe_outcome((B2, "inactive", "inactive")))
        self.assertIsNone(e2e.probe_outcome((B2, "active", "activating")))
        self.assertIsNone(e2e.probe_outcome(None))

    def probe(self, results):
        r = fake_run(self)
        r.machine = FakeMachine()
        conf = {"SSH_PROBE_EVERY": 10, "BUDGET_BOOT2_SSH": 180}
        r.b = lambda k: conf[k]
        calls = []
        clock = [time.monotonic()]

        def ssh(command, timeout, input=None, lost=False, retry=False):
            calls.append(command)
            return results[min(len(calls), len(results)) - 1]

        def sleep(secs):
            clock[0] += secs

        r.ssh = ssh
        with mock.patch.object(e2e.time, "monotonic", lambda: clock[0]), mock.patch.object(e2e.time, "sleep", sleep):
            got = r.probe_boot2()
        self.assertTrue(all(c == e2e.PROBE_CMD for c in calls))
        return got, len(calls)

    def test_waits_for_multi_user(self):
        got, n = self.probe([ssh_result(255), self.TRANSIENT, self.TRANSIENT, ssh_result(3, "%s\ninactive\nactive\n" % B2)])
        self.assertEqual((got, n), (("c", B2, "inactive", "active", 3), 4))

    def test_bounce_is_a(self):
        got, n = self.probe([ssh_result(255)] + [self.TRANSIENT] * 7 + [ssh_result(255, err="Connection reset by peer")])
        self.assertEqual(got, ("a", B2, "inactive", "inactive", 7))
        self.assertEqual(n, 19)  # every 10 s for 180 s

    def test_no_ssh_is_a(self):
        got, _ = self.probe([ssh_result(255)])
        self.assertEqual(got, ("a", None, None, None, 0))

    def test_getty_hangup(self):
        text = fixture("bios-getty-hangup.txt").decode()
        self.assertEqual(e2e.getty_hangup(text), ["Reloading system manager configuration.", "Starting default.target"])
        stuck = "Reloading system manager configuration.\nThis boot:     x\nYou are in emergency mode.\n" \
                "Press Enter for maintenance\n(or press Control-D to continue): "
        self.assertEqual(e2e.getty_hangup(stuck), [])  # a reload before the prompt is not the shell returning

    def setUp(self):
        p = mock.patch.object(e2e, "REPORT_WAIT", 0)  # the console's text is all there
        p.start()
        self.addCleanup(p.stop)

    def report_row(self, text, outcome):
        r = fake_run(self, "bios", con=FakeCon(text), GOOD=GOOD, BAD="7b76ec", B1=B1, N=4)
        r.check_emergency_report(attempt("2"), outcome, B2)
        return r.rows["2.5"]

    def test_25_hung_up_is_f(self):
        # The report lost in a hang-up is sc's in every outcome since
        # ignoreHangup (the chunk D review): never a W again.
        text = fixture("bios-getty-hangup.txt").decode().replace("sc: interrupted by hangup\n", "")
        for outcome in "abc":
            row = self.report_row(text, outcome)
            self.assertEqual((row.status, row.strength, row.cause), ("FAIL", "F", "M4"), outcome)
            self.assertIn("plan A2, A7", row.notes)

    def test_25_sc_hung_up_is_f(self):
        # B2's serial: the getty's hang-up ended sc status --console, which
        # must outlive one since cmd/sc/main.go's ignoreHangup.
        for outcome in "ac":
            row = self.report_row(fixture("bios-getty-hangup.txt").decode(), outcome)
            self.assertEqual((row.status, row.strength, row.cause), ("FAIL", "F", "M4"), outcome)
            self.assertIn("must outlive one", row.notes)

    def test_25_stuck_without_report_is_f(self):
        text = "[DEPEND] Dependency failed for local-fs.target - Local File Systems.\nYou are in emergency mode. " \
               "After logging in\nPress Enter for maintenance\n(or press Control-D to continue): "
        row = self.report_row(text, "a")
        self.assertEqual((row.status, row.strength, row.cause), ("FAIL", "F", "M4"))
        self.assertEqual(self.report_row(text, "c").strength, "F")

    # A bios run of 618e4e9: the report whole, then the getty's banner and
    # login prompt, and no line of the emergency shell. (The fixture has
    # the report's login hint, which sc got in the chunk D review for
    # this very ending.)
    BEFORE_SHELL = dict(GOOD="7228aa", BAD="6e6b92", B1="5dfd9262-0c0f-4d36-acc1-b2bd414fdcd6", N=4)
    BEFORE_SHELL_B2 = "80744ff3-f62e-4f08-a040-6ff6baaa9561"

    def before_shell_row(self, text, outcome="b"):
        r = fake_run(self, "bios", con=FakeCon(text), **self.BEFORE_SHELL)
        r.check_emergency_report(attempt("2"), outcome, self.BEFORE_SHELL_B2)
        return r.rows["2.5"]

    def test_25_report_above_the_login_prompt(self):
        text = fixture("bios-getty-before-shell.txt").decode()
        blk, kind = e2e.emergency_report(text)
        self.assertEqual(kind, "banner")
        self.assertEqual(blk.lines[0], "This boot:     80744ff3 (emergency), root read-write")
        self.assertEqual(blk.lines[-1], "The menu shows once more: the first entry, Ubuntu, is the one.")
        row = self.before_shell_row(text)
        # What a healthy run shows too is a note, not a WARN.
        self.assertEqual((row.status, row.strength), ("PASS", "F"))
        self.assertIn("before its first line", row.notes)
        self.assertIn("scd was already running", row.notes)

    def test_25_the_getty_where_it_likes(self):
        # The getty's banner and login prompt come when they like (plan
        # A2): the same report passes in every order seen or possible.
        text = fixture("bios-getty-before-shell.txt").decode()
        s, b = text.index("This boot:"), text.index("\nUbuntu 24.04.5")
        head, report, getty = text[:s], text[s:b + 1], text[b + 1:]
        shell = "You are in emergency mode. After logging in, type \"journalctl -xb\" to view\n" \
                "Press Enter for maintenance\n(or press Control-D to continue): "
        for name, order, kind, note in (
                ("report, shell, getty (a bios run of 618e4e9)", head + report + shell + "\n" + getty, "shell", None),
                ("getty, report, shell (the uefi run of 56f5359)", head + getty + "\n" + report + shell, "shell", None),
                ("report, getty, shell", head + report + getty + "\n" + shell, "banner", "between the report and the shell"),
                ("report, getty, no shell (a bios run of 618e4e9)", head + report + getty, "banner", "before its first line")):
            blk, got = e2e.emergency_report(order)
            self.assertEqual(got, kind, name)
            row = self.before_shell_row(order)
            self.assertEqual((row.status, row.strength), ("PASS", "F"), (name, row.notes))
            if note:
                self.assertIn(note, row.notes, name)
        # A slow report: its header, the getty, then the rest (sc shows
        # the header first after 2 s; the chunk G review). The getty's
        # lines are taken out, with or without its blank line before them.
        cut = report.index("\n\nChanged since") + 1
        header, rest = report[:cut], report[cut:]
        for name, order in (("header, getty, rest, shell", head + header + getty + rest + shell),
                            ("header, blank, getty, rest, shell", head + header + "\n" + getty + rest + shell),
                            ("header, status, getty, rest, shell",
                             head + header + "[  OK  ] Started polkit.service - Authorization Manager.\n\n" + getty + rest + shell)):
            blk, got = e2e.emergency_report(order)
            self.assertEqual(got, "shell", name)
            self.assertEqual(blk.lines[:5], report.split("\n")[:5], name)
            row = self.before_shell_row(order)
            self.assertIn(row.status, ("PASS", "WARN") if "status" in name else ("PASS",), (name, row.cause, row.notes))
            self.assertIn("between the report's header and its rest", row.notes, name)
        # The header and the getty so far: the rest is waited for; when the
        # wait is over, the header alone is a cut report.
        for partial in (head + header + "\n" + getty, head + header + "\n" + getty[:getty.index("\n")] + "\n"):
            self.assertEqual(e2e.emergency_report(partial), (None, None))
        blk, got = e2e.emergency_report(head + header + "\n" + getty, final=True)
        self.assertEqual(blk.lines[-1], "scd:           running (pid 629)")
        row = self.before_shell_row(head + header + "\n" + getty)
        self.assertEqual((row.status, row.strength, row.cause), ("FAIL", "F", "M4"))
        # getty, report, and the shell's line not there yet: no block
        # until the wait for one is over; then it ends with the text.
        early = head + getty + "\n" + report
        self.assertEqual(e2e.emergency_report(early), (None, None))
        blk, got = e2e.emergency_report(early, final=True)
        self.assertEqual((got, blk.lines[-1]), ("end", "The menu shows once more: the first entry, Ubuntu, is the one."))
        row = self.before_shell_row(early)
        self.assertEqual(row.status, "PASS")
        self.assertIn("nothing came after the report", row.notes)
        # Two reports (outcome c: the shell came back): the first one.
        twice = head + report + shell + "\n" + report.replace("root read-write", "root READ-WRITE") + shell
        self.assertEqual(e2e.emergency_report(twice)[0].lines[0], "This boot:     80744ff3 (emergency), root read-write")

    def test_25_wrong_report_above_the_login_prompt_is_f(self):
        text = fixture("bios-getty-before-shell.txt").decode()
        for wrong in (text.replace("  sc restore 7228aa", "  sc restore 6e6b92"),
                      text.replace("  systemctl daemon-reload\n", ""),
                      text.replace("The menu shows once more", "sc: store: database is locked\nThe menu shows once more")):
            self.assertNotEqual(wrong, text)
            row = self.before_shell_row(wrong)
            self.assertEqual((row.status, row.strength, row.cause), ("FAIL", "F", "M4"))

    def test_25_login_prompt_without_report_is_f(self):
        text = fixture("bios-getty-before-shell.txt").decode()
        cut = text[:text.index("This boot:")] + text[text.index("\nUbuntu 24.04.5"):]
        self.assertEqual(e2e.emergency_report(cut, final=True), (None, None))
        row = self.before_shell_row(cut)
        self.assertEqual((row.status, row.strength, row.cause), ("FAIL", "F", "M4"))
        self.assertIn("no report above", row.notes)

    def test_26_reads_through_ssh_only(self):
        r = fake_run(self)
        r.sudo = lambda command, timeout=None, lost=False, retry=False: ssh_result(255, err="kex_exchange_identification: Connection reset by peer")
        self.assertEqual(r.boots_text(), "")  # a poll goes on
        with self.assertRaises(e2e.LabError):
            r.boots_text(strict=True)
        r.sudo = lambda command, timeout=None, lost=False, retry=False: ssh_result(1, err="cat: /var/lib/smartconfig/boots: No such file")
        self.assertEqual(r.boots_text(strict=True), "")  # no file is the guest's answer, not ssh's

    def run_26(self, boots, env_rc=0, sync_rc=0, env="smartconfig_pending=1\nrecordfail=1\n"):
        r = fake_run(self)
        r.poll = lambda fn, budget, every=5.0: fn()
        r.boots_text = lambda strict=False: boots
        answers = {"grub-editenv": ssh_result(env_rc, env if env_rc == 0 else ""),
                   "sync": ssh_result(sync_rc)}
        r.sudo = lambda command, timeout=None, lost=False, retry=False: answers[command.split()[0]]
        r.check_26(B2)
        return r.rows["2.6"]

    BOOTS_C = "%s seen 10\n%s bad 11 5 local-fs=inactive emergency=inactive rescue=inactive failed-units=1\n" % (B2, B2)

    def test_26_pass(self):
        self.assertEqual(self.run_26(self.BOOTS_C).status, "PASS")

    def test_26_ssh_gone_is_lab(self):
        for env_rc, sync_rc in ((255, 0), (0, 255)):
            with self.assertRaises(e2e.LabError):
                self.run_26(self.BOOTS_C, env_rc, sync_rc)

    def test_26_no_verdict_is_m4(self):
        with self.assertRaises(e2e.Stop) as c:
            self.run_26("%s seen 10\n" % B2)
        self.assertEqual((c.exception.row.cause, c.exception.row.strength), ("M4", "H"))



# ---------------------------------------------------------------- a healthy run, and one fault at a time
#
# The chunk D review's mutation run: with a check's condition taken out, a
# healthy real run still passes, so only a unit test can notice. The tests
# below start from the console text and the facts of a run that passes
# (the uefi run of 56f5359, cut down to what the checks read), break one
# thing, and expect that check's row to FAIL as SmartConfig's.

BAD_SHA = "b" * 64
EXP_RESCUE = ("BOOT_IMAGE=/vmlinuz-6.8.0-142-generic root=UUID=1bfe no_timer_check console=tty1 console=ttyS0 ro "
              + e2e.RESCUE_ARGS)
POST_CLEAN = "sclab: post timeout=[0] style=[hidden]\n"
PRE_FLAG = "sclab: pre platform=efi pending=[1] recordfail=[] timeout=[0] style=[hidden]\n"
POST_FLAG = "sclab: post timeout=[30] style=[menu]\n"
# GRUB's menu on serial as serialmux.Cleaner leaves it (s2-uefi-menu-ctrln.raw), without the frame.
UEFI_MENU = ("\n                             GNU GRUB  version 2.12\n\n*Ubuntu\n Advanced options for Ubuntu\n"
             " UEFI Firmware Settings\n SmartConfig rescue\n\n"
             "   The highlighted entry will be executed automatically in 30s.\n"
             "   The highlighted entry will be executed automatically in 29s.\n")
INIT = "[    2.763209] Run /init as init process\n"
LOGIN = "\nUbuntu 24.04.5 LTS sclab ttyS0\n\nsclab login: "
TIMED_OUT = "[ TIME ] Timed out waiting for device dev-d…6c1e2a-9b7d-4c1e-8f2a-5d6e7f8a9b0c.\n"
DEPEND = "[DEPEND] Dependency failed for local-fs.target - Local File Systems.\n"
EMERGENCY_TAIL = ('You are in emergency mode. After logging in, type "journalctl -xb" to view\n'
                  'system logs, "systemctl reboot" to reboot, or "exit"\nto continue bootup.\n'
                  "Press Enter for maintenance\n(or press Control-D to continue): ")
# TCG's own panic (q3c-panic-tcg.raw) and one that is not.
TCG_PANIC = "[    1.233642] Kernel panic - not syncing: IO-APIC + timer doesn't work!  Boot with apic=debug\n"
VFS_PANIC = "[    1.911873] Kernel panic - not syncing: VFS: Unable to mount root fs on unknown-block(0,0)\n"
JOURNAL_OK = "boot %s: ok (local-fs=active emergency=inactive rescue=inactive failed-units=0)\n"
BAD2 = "%s bad 290 9 local-fs=inactive emergency=active rescue=inactive failed-units=2" % B2
OK4 = "%s ok 401 12 local-fs=active emergency=inactive rescue=inactive failed-units=0" % B4
BOOTS_1 = "%s seen 100\n%s\n" % (B1, OK1)
BOOTS_2 = BOOTS_1 + "%s seen 200\n%s\n" % (B2, BAD2)
BOOTS_4 = BOOTS_2 + "%s seen 400\n%s\n" % (B4, OK4)


def kernel_lines(cmdline=EXP_DEFAULT):
    return "[    0.000000] Linux version 6.8.0-142-generic\n[    0.000000] Command line: %s\n%s" % (cmdline, INIT)


# A boot with no menu (boots 0, 1, 5) and one with the 30 s menu left alone (boot 4), to the login prompt.
BOOT_CLEAN = UEFI_SILENT + PRE_CLEAN + POST_CLEAN + kernel_lines() + LOGIN
BOOT_MENU = UEFI_SILENT + PRE_FLAG + POST_FLAG + UEFI_MENU + kernel_lines() + LOGIN


def render(golden, **values):
    """A golden (lab/testdata/console-<golden>.golden) as sc prints it in
    this file's run; values replace or add placeholders."""
    fill = {"YYYY-MM-DD HH:MM": "2026-10-04 12:00", "MM-DD HH:MM": "10-04 12:05", "HH:MM": "12:03", "N": 4,
            "GOOD": GOOD, "BAD": BAD, "K boots": "1 boot", "B1:8": console.short_boot(B1),
            "B2:8": console.short_boot(B2), "B3:8": console.short_boot(B3)}
    fill.update(values)
    return console.TOKEN.sub(lambda m: str(fill[m.group(1)]), console.load_golden(golden))


class FakeClock:
    """time.monotonic and time.sleep as e2e.py sees them: a sleep, and a
    wait on run's mux, pass the time at once, so a loop that waits for
    its budget ends."""

    def __init__(self, test, run=None):
        self.now = time.monotonic()
        for name, fn in (("monotonic", lambda: self.now), ("sleep", self.sleep)):
            p = mock.patch.object(e2e.time, name, fn)
            p.start()
            test.addCleanup(p.stop)
        if run is not None:
            run.deadline = self.now + 10 ** 6
            run.mux.wait = lambda size, timeout: self.sleep(timeout)

    def sleep(self, secs):
        self.now += secs


def answers(run, table):
    """run.ssh (and so run.sudo) answers by the first key of table that is
    in the command: (rc, out), an ssh_result, or a function of the command
    that gives one. Returns the list the commands asked are added to."""
    asked = []

    def ssh(command, timeout=None, input=None, lost=False, retry=False):
        asked.append(command)
        for key, v in table.items():
            if key in command:
                v = v(command) if callable(v) else v
                return v if hasattr(v, "rc") else ssh_result(*v)
        raise AssertionError("unexpected ssh command %r" % command)

    run.ssh = ssh
    return asked


def outcome_of(fn, *args):
    """fn(*args); the Stop, Retry or LabError that ended it, or None."""
    try:
        fn(*args)
    except (e2e.Stop, e2e.Retry, e2e.LabError) as e:
        return e
    return None


class FaultCase(unittest.TestCase):
    """What every "one fault" test asserts: the check's row is a FAIL of
    SmartConfig's with its strength; an H failure stops the mode at that
    row, an F failure lets it go on."""

    def assert_fails(self, r, end, cid, strength, name):
        self.assertIn(cid, r.rows, "%s: no %s row (ended with %r)" % (name, cid, end))
        row = r.rows[cid]
        self.assertEqual((row.status, row.cause, row.strength), ("FAIL", "M4", strength), "%s: %s" % (name, row.notes))
        if strength == "H":
            self.assertIsInstance(end, e2e.Stop, name)
            self.assertEqual(end.row.id, cid, name)
        else:
            self.assertIsNone(end, name)
        self.assertEqual(e2e.verdict(r.rows.values()), "FAIL", name)

    def assert_passes(self, r, *cids):
        for cid in cids:
            self.assertEqual(r.status(cid), "PASS", "%s: %s" % (cid, r.rows[cid].notes if cid in r.rows else "no row"))


RESCUE_ENTRY = re.search(r"menuentry 'SmartConfig rescue'.*?\n}\n", GRUB_CFG, re.S).group(0)
RESCUE_LINUX = re.search(r"\tlinux\t[^\n]*\n", RESCUE_ENTRY).group(0)
INSTALL = ("uid=0\nmanifest=ok\ninstall_rc=0\nupdate_grub_rc=0\n"
           "update_grub_err=Generating grub configuration file ...\n"
           "update_grub_err=Adding SmartConfig rescue entry: /boot/vmlinuz-%s\nupdate_grub_err=done\n"
           "grub_script_check_rc=0\nverify_rc=0\ndone=1\n" % labvm.load_conf()["KERNEL"])


def installed_facts(**over):
    """facts.sh normal after install.sh (boot 0), as 0.6 and 0.7 read it."""
    blocks = {
        "dropins": (0, ["Id=rescue.service", "DropInPaths=/usr/lib/systemd/system/service.d/10-timeout-abort.conf "
                        "/etc/systemd/system/rescue.service.d/50-smartconfig.conf", "", "Id=emergency.service",
                        "DropInPaths=/etc/systemd/system/emergency.service.d/50-smartconfig.conf"]),
        "units-cat": (0, ["# /etc/systemd/system/rescue.service.d/50-smartconfig.conf", "[Service]", e2e.DROPIN_LINE,
                          "# /etc/systemd/system/emergency.service.d/50-smartconfig.conf", "[Service]",
                          e2e.DROPIN_LINE]),
        "is-enabled": (0, ["sc-boot-seen.service=enabled", "sc-boot-ok.service=enabled", "scd.service=enabled"]),
    }
    blocks.update(over)
    return normal_facts(**blocks)


class TestBoot0Faults(FaultCase):
    """0.5, 0.6 and 0.7: what install.sh, grub.cfg and facts.sh must show
    after the install, one fault at a time."""

    def check_05(self, cfg=GRUB_CFG, install=INSTALL):
        r = fake_run(self)
        return r, outcome_of(r.check_05, e2e.parse_kv(install), cfg, "evidence/install.txt")

    def test_05_good(self):
        r, end = self.check_05()
        self.assertIsNone(end)
        self.assert_passes(r, "0.5")  # not a WARN either: the observers are around the flag block
        self.assertEqual((r.v["EXP_DEFAULT"], r.v["EXP_RESCUE"]), (EXP_DEFAULT, EXP_RESCUE))

    def test_05_one_fault(self):
        def entry(old, new):
            self.assertIn(old, RESCUE_ENTRY)
            return GRUB_CFG.replace(RESCUE_ENTRY, RESCUE_ENTRY.replace(old, new))

        def cfg(old, new):
            self.assertIn(old, GRUB_CFG)
            return GRUB_CFG.replace(old, new)

        flag_first = 'if [ "${smartconfig_pending}" = "1" ] ; then\n\tset timeout_style=menu\nfi\n' + GRUB_CFG
        for name, kw, said in (
                ("update-grub did not add the entry", dict(install=INSTALL.replace("Adding SmartConfig", "Skipping")),
                 "update-grub said"),
                ("grub-script-check fails",
                 dict(install=INSTALL.replace("grub_script_check_rc=0", "grub_script_check_rc=1")),
                 "grub-script-check"),
                ("two rescue entries", dict(cfg=cfg(RESCUE_ENTRY, RESCUE_ENTRY * 2)), "2 entries with --id"),
                ("no rescue entry", dict(cfg=cfg(RESCUE_ENTRY, "")), "0 entries with --id"),
                ("the first entry is not Ubuntu", dict(cfg=cfg("menuentry 'Ubuntu' ", "menuentry 'Ubuntu (old)' ")),
                 "the first entry is"),
                ("no linux line", dict(cfg=entry(RESCUE_LINUX, "")), "no linux line"),
                ("two linux lines", dict(cfg=entry(RESCUE_LINUX, RESCUE_LINUX * 2)), "more than one linux line"),
                ("no echo", dict(cfg=entry("\techo\t'%s'\n" % e2e.RESCUE_ECHO, "")), "has no echo"),
                ("another echo", dict(cfg=entry("/etc/fstab ignored'", "/etc/fstab read'")), "has no echo"),
                ("quiet", dict(cfg=entry("console=ttyS0 ro ", "console=ttyS0 quiet ro ")), "has quiet"),
                ("splash", dict(cfg=entry("console=ttyS0 ro ", "console=ttyS0 splash ro ")), "has splash"),
                ("rw after ro", dict(cfg=entry(" ro fstab=no", " ro rw fstab=no")), "last ro/rw is rw"),
                ("no ro at all", dict(cfg=entry(" ro fstab=no", " fstab=no")), "last ro/rw is None"),
                ("no SYSTEMD_SULOGIN_FORCE", dict(cfg=entry(" SYSTEMD_SULOGIN_FORCE=1", "")), "lacks"),
                ("no fstab=no", dict(cfg=entry("fstab=no ", "")), "lacks"),
                ("a second root=", dict(cfg=entry(" ro fstab=no", " root=/dev/vda9 ro fstab=no")), "root= differs"),
                ("the serial console dropped", dict(cfg=entry(" console=ttyS0 ro", " ro")), "drops console=ttyS0"),
                ("no_timer_check dropped", dict(cfg=entry(" no_timer_check", "")), "drops no_timer_check"),
                ("no recordfail block", dict(cfg=cfg('if [ "${recordfail}" = 1 ] ; then', "if false ; then")),
                 "no recordfail block"),
                ("recordfail's timeout is 30",
                 dict(cfg=cfg('= 1 ] ; then\n  set timeout=0', '= 1 ] ; then\n  set timeout=30')),
                 "sets timeout=30"),
                ("no flag block", dict(cfg=cfg(e2e.FLAG_BLOCK, 'if [ "${smartconfig_pending}" = "2" ] ; then')),
                 "no menu flag block"),
                ("the flag block before recordfail's", dict(cfg=flag_first), "comes before the recordfail block")):
            r, end = self.check_05(**kw)
            self.assert_fails(r, end, "0.5", "H", name)
            self.assertIn(said, r.rows["0.5"].notes, name)

    def test_05_deb_holds_postinst(self):
        """--deb (the M5 review, B1): 0.5 holds the package's postinst to the
        update-grub it ran, in dpkg's output; install.sh runs none."""
        k = labvm.load_conf()["KERNEL"]
        adding = "Adding SmartConfig rescue entry: /boot/vmlinuz-%s\n" % k
        deb = ("uid=0\nmanifest=ok\ninstall_rc=0\ndeb_rc=0\ndeb_out=Setting up smartconfig (0.5.0) ...\n"
               "deb_out=Generating grub configuration file ...\ndeb_out=" + adding + "deb_out=done\n"
               "grub_script_check_rc=0\nverify_rc=0\ndone=1\n")

        def run(install):
            r = fake_run(self)
            r.deb = True
            return r, outcome_of(r.check_05, e2e.parse_kv(install), GRUB_CFG, "evidence/install.txt")

        r, end = run(deb)
        self.assertIsNone(end)
        self.assert_passes(r, "0.5")
        for name, install, said in (
                ("postinst ran no update-grub", deb.replace("deb_out=" + adding, ""), "dpkg -i (postinst's update-grub) said"),
                ("an update-grub of install.sh's own does not count",
                 deb.replace("deb_out=" + adding, "") + "update_grub_rc=0\nupdate_grub_err=" + adding, "postinst's update-grub"),
                ("postinst's update-grub failed",
                 deb + "deb_out=smartconfig: update-grub failed; grub.cfg has no rescue entry yet: run sudo update-grub\n",
                 "postinst: update-grub failed")):
            r, end = run(install)
            self.assert_fails(r, end, "0.5", "H", name)
            self.assertIn(said, r.rows["0.5"].notes, name)

    def test_06_deb_drop_ins_from_usr_lib(self):
        lib = installed_facts(dropins=(0, [l.replace("/etc/systemd/system/", "/usr/lib/systemd/system/")
                                           for l in installed_facts().lines("dropins")]))
        for f, ok in ((lib, True), (installed_facts(), False)):
            r = fake_run(self)
            r.deb = True
            end = outcome_of(r.check_06, e2e.parse_kv(INSTALL), f, "evidence/install.txt")
            if ok:
                self.assertIsNone(end)
                self.assert_passes(r, "0.6")
            else:
                self.assert_fails(r, end, "0.6", "H", "the drop-ins from /etc")

    def check_06(self, f=None, install=INSTALL):
        r = fake_run(self)
        return r, outcome_of(r.check_06, e2e.parse_kv(install), f or installed_facts(), "evidence/install.txt")

    def test_06(self):
        r, end = self.check_06()
        self.assertIsNone(end)
        self.assert_passes(r, "0.6")
        both = installed_facts().lines("dropins")
        cat = installed_facts().lines("units-cat")
        for name, kw in (
                ("rescue.service has no drop-in", dict(f=installed_facts(dropins=(0, both[:1] + both[2:])))),
                ("emergency.service has no drop-in", dict(f=installed_facts(dropins=(0, both[:4])))),
                ("the ExecStartPre once", dict(f=installed_facts(**{"units-cat": (0, cat[:3])}))),
                ("the ExecStartPre three times", dict(f=installed_facts(**{"units-cat": (0, cat + cat[:3])}))),
                ("systemd-analyze verify fails", dict(install=INSTALL.replace("verify_rc=0", "verify_rc=1"))),
                ("systemd-analyze verify says something",
                 dict(install=INSTALL + "verify_out=sc-boot-ok.service: Unknown key 'Befor' in section [Unit]\n")),
                ("sc-boot-seen not enabled", dict(f=installed_facts(**{"is-enabled": (0, [
                    "sc-boot-seen.service=disabled", "sc-boot-ok.service=enabled", "scd.service=enabled"])}))),
                ("sc-boot-ok not enabled", dict(f=installed_facts(**{"is-enabled": (0, [
                    "sc-boot-seen.service=enabled", "scd.service=enabled"])}))),
                ("scd not running", dict(f=installed_facts(active=(0, ["scd.service=failed"]))))):
            r, end = self.check_06(**kw)
            self.assert_fails(r, end, "0.6", "H", name)

    def test_07(self):
        for name, over in (
                ("the menu flag is set already", dict(grubenv=(0, ["smartconfig_pending=1"]))),
                ("the flag is there, empty", dict(grubenv=(0, ["smartconfig_pending="]))),
                ("a boots file before any boot",
                 dict(paths=(0, ["/var/lib/smartconfig/boots file 600 root:root abc"]))),
                ("the boots file not looked at", dict(paths=(0, ["/usr/sbin/sc file 755 root:root abc"]))),
                ("scd made no baseline", {"journal-scd": (0, ["watching /etc, /boot/grub (227 directories)"])}),
                ("sc log has no row",
                 {"sc-log-fstab": (0, ["ID      WHEN              ORIGIN  FILE        SIZE  WHAT"])})):
            r = fake_run(self)
            end = outcome_of(r.check_07, normal_facts(**over))
            self.assert_fails(r, end, "0.7", "H", name)


def menu_of(entries=tuple(e2e.MENU_ENTRIES["uefi"]), selected=0, countdown=30):
    return console.Menu("2.12", list(entries), selected, countdown, False, False)


MENU_LINES = ["GNU GRUB  version 2.12", "*Ubuntu", "   The highlighted entry will be executed automatically in 30s."]


class TestDecisionFaults(FaultCase):
    """x.1, 1.2/2.2/3.4 and the boots file's ok line: what GRUB decided,
    one fault at a time."""

    def test_observer_fields(self):
        f = e2e.observer_problems
        clean = PRE_CLEAN + POST_CLEAN
        self.assertEqual(f(console.observer_lines(clean), "efi", "", "0", "hidden"), [])
        for old, new, said in (("platform=efi", "platform=pc", "pre platform=[pc], not [efi]"),
                               ("pending=[]", "pending=[1]", "pre pending=[1], not []"),
                               ("pending=[]", "pending=[0]", "pre pending=[0], not []"),
                               ("recordfail=[] timeout=[0]", "recordfail=[] timeout=[5]", "pre timeout=[5], not [0]"),
                               ("post timeout=[0]", "post timeout=[30]", "post timeout=[30], not [0]"),
                               ("post timeout=[0] style=[hidden]", "post timeout=[0] style=[menu]",
                                "post style=[menu], not [hidden]")):
            self.assertIn(old, clean)
            self.assertEqual(f(console.observer_lines(clean.replace(old, new)), "efi", "", "0", "hidden"), [said])
        flagged = PRE_FLAG + POST_FLAG
        self.assertEqual(f(console.observer_lines(flagged), "efi", "1", "30", "menu"), [])
        unset = console.observer_lines(flagged.replace("pending=[1]", "pending=[]"))
        self.assertEqual(f(unset, "efi", "1", "30", "menu"), ["pre pending=[], not [1]"])
        # The last line of each kind counts: an earlier attempt's lines are not this boot's.
        self.assertEqual(f(console.observer_lines(flagged + clean), "efi", "", "0", "hidden"), [])

    def test_menu_problems(self):
        f = e2e.menu_problems
        self.assertEqual(f(menu_of(), MENU_LINES), ([], []))
        self.assertEqual(f(menu_of(countdown=29), [x.replace("30s", "29s") for x in MENU_LINES], (30, 29)),
                         ([], ["first seen at 29s (the screen is polled)"]))
        uefi = e2e.MENU_ENTRIES["uefi"]
        for name, menu, lines, said in (
                ("a 5 s menu", menu_of(countdown=5), [x.replace("30s", "5s") for x in MENU_LINES],
                 "the countdown says 5s"),
                ("29 s on serial, which is not polled", menu_of(countdown=29),
                 [x.replace("30s", "29s") for x in MENU_LINES],
                 "the countdown says 29s"),
                ("no countdown: it waits for a key", menu_of(countdown=None), MENU_LINES[:2],
                 "the countdown says None"),
                ("no rescue entry", menu_of(uefi[:3]), MENU_LINES, "0 entries titled"),
                ("two rescue entries", menu_of(uefi + uefi[3:]), MENU_LINES, "2 entries titled"),
                ("the highlight on the second entry", menu_of(selected=1), MENU_LINES, "the highlight is on 'Advanced"),
                ("the highlight on the rescue entry", menu_of(selected=3), MENU_LINES,
                 "the highlight is on 'SmartConfig"),
                ("no highlight", menu_of(selected=None), MENU_LINES, "the highlight is on None"),
                ("the rescue entry first", menu_of(uefi[3:] + uefi[:3]), MENU_LINES,
                 "the highlight is on 'SmartConfig"),
                ("no header", menu_of(), MENU_LINES[1:], "no 'GNU GRUB  version' header"),
                ("no countdown line", menu_of(), MENU_LINES[:2], "no line 'The highlighted entry")):
            problems, _ = f(menu, lines)
            self.assertTrue(any(said in p for p in problems), (name, problems))

    def decide(self, mode, flag, obs_text, menu=None, seen=False, lines=MENU_LINES, vga=None):
        r = fake_run(self, mode)
        g = grubenv_g(console.observer_lines(obs_text), menu, seen)
        g["menu_lines"] = lines if menu is not None else []
        a = attempt("1", flag, vga=vga)
        return r, outcome_of(r.check_decision, "1.1", a, g, flag)

    def test_uefi_decisions(self):
        clean, flagged = PRE_CLEAN + POST_CLEAN, PRE_FLAG + POST_FLAG
        r, end = self.decide("uefi", False, clean)
        self.assertIsNone(end)
        self.assert_passes(r, "1.1")
        r, end = self.decide("uefi", True, flagged, menu_of())
        self.assertIsNone(end)
        self.assert_passes(r, "1.1")
        for name, args, said in (
                ("a menu although the flag is unset", (False, clean, None, True),
                 "GNU GRUB (a menu) showed before the kernel"),
                ("the flag is set in grubenv", (False, clean.replace("pending=[]", "pending=[1]")), "pre pending=[1]"),
                ("a timeout before the flag block", (False, clean.replace("[] timeout=[0]", "[] timeout=[5]")),
                 "pre timeout"),
                ("the flag block gave a menu", (False, PRE_CLEAN + POST_FLAG), "post timeout=[30]"),
                ("no observer line", (False, ""), "no 'sclab: pre' line"),
                ("flag set: no menu", (True, flagged), "no GRUB menu"),
                ("flag set: the flag block did nothing", (True, PRE_FLAG + POST_CLEAN, menu_of()), "post timeout=[0]"),
                ("flag set: the flag not in grubenv", (True, PRE_CLEAN + POST_FLAG, menu_of()), "pre pending=[]"),
                ("flag set: a 5 s menu", (True, flagged, menu_of(countdown=5), False, [
                    x.replace("30s", "5s") for x in MENU_LINES]), "the countdown says 5s"),
                ("flag set: the highlight is not on Ubuntu", (True, flagged, menu_of(selected=3)),
                 "the highlight is on"),
                ("flag set: no rescue entry", (True, flagged, menu_of(e2e.MENU_ENTRIES["bios"][:2])),
                 "0 entries titled")):
            r, end = self.decide("uefi", *args)
            self.assert_fails(r, end, "1.1", "H", name)
            self.assertIn(said, r.rows["1.1"].notes, name)
        # Another set of entries than the image's is worth a look, not a failure.
        r, end = self.decide("uefi", True, flagged, menu_of(e2e.MENU_ENTRIES["bios"]))
        self.assertEqual((end, r.status("1.1")), (None, "WARN"))

    def test_bios_decisions(self):
        hidden = console.vgatext(fixture("vga-bios-hidden.bin"))
        clean = "\n".join(hidden)
        watch = FakeWatch(hidden)
        # A menu with the flag unset is all of 1.1/2.1/5.1's H content on bios.
        r, end = self.decide("bios", False, clean, None, True, vga=watch)
        self.assert_fails(r, end, "1.1", "H", "bios: a menu although the flag is unset")
        # The screen is polled: an observer line that is off is a WARN there, and is said.
        r, end = self.decide("bios", False, "sclab: pre platform=pc pending=[] recordfail=[] timeout=[0] "
                             "style=[hidden]\nsclab: post timeout=[30] style=[menu]\n", vga=watch)
        self.assertEqual((end, r.status("1.1")), (None, "WARN"))
        self.assertIn("observers: post timeout=[30]", r.rows["1.1"].notes)
        # Flag set: the menu wiped the observers' lines, as it must. No WARN for that.
        menu = menu_of(e2e.MENU_ENTRIES["bios"])
        r, end = self.decide("bios", True, "", menu, True, vga=watch)
        self.assertEqual((end, r.status("1.1")), (None, "PASS"))
        # Observers still on the screen under a menu that says otherwise: worth a WARN.
        r, end = self.decide("bios", True, clean, menu, True, vga=watch)
        self.assertEqual((end, r.status("1.1")), (None, "WARN"))
        r, end = self.decide("bios", True, clean, None, False, vga=watch)
        self.assert_fails(r, end, "1.1", "H", "bios: flag set, the observers say no menu was drawn")
        r, end = self.decide("bios", True, "", menu_of(e2e.MENU_ENTRIES["bios"], selected=2), True, vga=watch)
        self.assert_fails(r, end, "1.1", "H", "bios: the highlight on the rescue entry")

    def test_cmdline(self):
        for cid, expected in (("1.2", EXP_DEFAULT), ("2.2", EXP_DEFAULT), ("3.4", EXP_RESCUE)):
            r = fake_run(self)
            r.check_cmdline(cid, attempt(), None, expected, FakeHit(expected.replace(" ", "  ")))  # the spaces do not count
            self.assert_passes(r, cid)
            for name, seen in (("another root", expected.replace("1bfe", "0b1c")),
                               ("one more argument", expected + " quiet"),
                               ("one less", expected.replace(" no_timer_check", "")),
                               ("the other entry", EXP_RESCUE if expected == EXP_DEFAULT else EXP_DEFAULT)):
                r = fake_run(self)
                end = outcome_of(r.check_cmdline, cid, attempt(), None, expected, FakeHit(seen))
                self.assert_fails(r, end, cid, "H", "%s %s" % (cid, name))
                self.assertEqual(r.rows[cid].seen, seen)

    def test_ok_line(self):
        ok = e2e.ok_line_rx(B1)
        self.assertEqual(ok.search(BOOTS_4).group(1), "7")
        for old, new in (("emergency=inactive", "emergency=active"), ("local-fs=active", "local-fs=failed"),
                         ("rescue=inactive", "rescue=active"), (" ok ", " bad "), (B1 + " ok", B5 + " ok")):
            self.assertIn(old, OK1)
            self.assertIsNone(ok.search(BOOTS_1.replace(old, new)), new)
        self.assertEqual(ok.search(BOOTS_1.replace(" 7 ", " -1 ")).group(1), "-1")  # 1.4 says so: R1 is -1


class TestBoot1Faults(FaultCase):
    """Boot 1 (1.1 to 1.9) on the text and facts of a healthy boot, then
    with one thing wrong."""

    def boot1(self, text=BOOT_CLEAN, flag=False, boots=BOOTS_1, ssh=None, **facts):
        r = fake_run(self, con=FakeCon(text), EXP_DEFAULT=EXP_DEFAULT)
        FakeClock(self, r)
        r.wait_ssh = lambda a: B1
        facts.setdefault("boots", (0, boots.strip().split("\n")))
        table = {"is-system-running": (0, "running\n"),
                 "systemctl --failed": (0, "snapd.service loaded failed failed\n"),
                 "cat /var/lib/smartconfig/boots": (0, boots), "-t scd | grep -c": (0, "1\n"),
                 "-t sc-boot": (0, JOURNAL_OK % B1), "facts.sh normal": (0, normal_text(**facts)),
                 "sc cat %s" % GOOD: (0, FSTAB_SHA + "  -\n")}
        table.update(ssh or {})
        answers(r, table)
        return r, outcome_of(r.boot1, attempt("1", flag))

    def test_good(self):
        r, end = self.boot1()
        self.assertIsNone(end)
        self.assertEqual(r.status("1.0"), "SKIP")
        self.assert_passes(r, "1.1", "1.2", "1.3", "1.4", "1.5", "1.6", "1.7", "1.8", "1.9")
        self.assertEqual((r.v["B1"], r.v["R1"], r.v["GOOD"], r.v["FSTAB_SHA"]), (B1, 7, GOOD, FSTAB_SHA))
        self.assertIn("evidence/facts-boot1.txt", r.rows["1.4"].evidence)

    def test_the_flag_is_known_after_boot_1(self):
        # A retried boot 1 (flag not known) skips 1.1; its ok verdict then makes the flag unset for boot 2.
        r, end = self.boot1(flag=None)
        self.assertIsNone(end)
        self.assertEqual(r.status("1.1"), "SKIP")
        self.assert_passes(r, "1.2", "1.4", "1.9")
        r.flag = None  # as boot() leaves it after a retry by no-ssh
        self.assertIsNone(outcome_of(r.boot1, attempt("1b", None)))
        self.assertIs(r.flag, False)

    def test_one_fault(self):
        show = ["Result=success", "ExecMainStatus=0", "ConditionResult=yes"]
        journal = [(JOURNAL_OK % B1).strip()]
        status = status_lines(B1)
        for cid, strength, name, kw in (
                ("1.1", "H", "a menu showed", dict(text=BOOT_CLEAN.replace(POST_CLEAN, POST_CLEAN + UEFI_MENU))),
                ("1.1", "H", "the flag is set", dict(text=BOOT_CLEAN.replace("pending=[]", "pending=[1]"))),
                ("1.1", "H", "the flag block made a menu of it", dict(text=BOOT_CLEAN.replace(POST_CLEAN, POST_FLAG))),
                ("1.1", "H", "no post line", dict(text=BOOT_CLEAN.replace(POST_CLEAN, ""))),
                ("1.2", "H", "quiet on the command line",
                 dict(text=BOOT_CLEAN.replace(EXP_DEFAULT, EXP_DEFAULT + " quiet"))),
                ("1.2", "H", "the rescue entry booted", dict(text=BOOT_CLEAN.replace(EXP_DEFAULT, EXP_RESCUE))),
                ("1.3", "H", "the system is starting for ever", dict(ssh={"is-system-running": (1, "starting\n")})),
                ("1.3", "H", "maintenance", dict(ssh={"is-system-running": (1, "maintenance\n")})),
                ("1.4", "H", "no seen line", dict(boots=OK1 + "\n")),
                ("1.4", "H", "no ok line", dict(boots="%s seen 100\n" % B1)),
                ("1.4", "H", "an ok line of another boot", dict(boots=BOOTS_1.replace(B1 + " ok", B5 + " ok"))),
                ("1.4", "H", "ok with emergency=active",
                 dict(boots=BOOTS_1.replace("emergency=inactive", "emergency=active"))),
                ("1.4", "H", "R1 below 0", dict(boots=BOOTS_1.replace(" 7 ", " -1 "))),
                ("1.5", "H", "sc-boot-ok failed", {"show:sc-boot-ok.service": (0, ["Result=exit-code"] + show[1:])}),
                ("1.5", "H", "sc-boot-seen exit 1",
                 {"show:sc-boot-seen.service": (0, [show[0], "ExecMainStatus=1", show[2]])}),
                ("1.5", "H", "sc-boot-seen's condition",
                 {"show:sc-boot-seen.service": (0, show[:2] + ["ConditionResult=no"])}),
                ("1.5", "H", "sc-boot-ok not shown", {"show:sc-boot-ok.service": None}),
                ("1.5", "H", "systemctl show failed", {"show:sc-boot-ok.service": (1, show)}),
                ("1.5", "H", "grub-editenv failed", {"journal-sc-boot": (0, journal + [
                    "sc: boot verdict: grub-editenv unset failed: exit status 1"])}),
                ("1.5", "H", "no GRUB environment block", {"journal-sc-boot": (0, [
                    "sc: boot seen: /boot/grub/grubenv: no GRUB environment block"] + journal)}),
                ("1.6", "H", "the flag set after an ok boot", dict(grubenv=(0, ["smartconfig_pending=1"]))),
                ("1.6", "H", "the flag emptied, not unset", dict(grubenv=(0, ["smartconfig_pending="]))),
                ("1.7", "H", "sc status exit 2", {"sc-status": (2, status)}),
                ("1.7", "H", "sc status names another boot", {"sc-status": (0, status_lines(B2))}),
                ("1.7", "H", "this boot is not normal", {"sc-status": (0, [status[0].replace("(normal)", "(emergency)")]
                                                                     + status[1:])}),
                ("1.7", "H", "root read-only",
                 {"sc-status": (0, [status[0].replace("read-write", "read-only")] + status[1:])}),
                ("1.7", "H", "the last healthy boot is another", {"sc-status": (0, [
                    status[0], "Last healthy:  2026-10-04 11:00, boot 0a0b0c0d"] + status[2:])}),
                ("1.7", "H", "scd not running",
                 {"sc-status": (0, status[:2] + ["scd:           not running"] + status[3:])}),
                ("1.7", "H", "failed since", {"sc-status": (0, status[:2] + [
                    "Failed since:  1 boot, last 10-04 12:05: a mount failed"] + status[2:])}),
                ("1.9", "H", "no baseline in this boot", {"journal-scd": (0, ["watching /etc (227 directories)"])}),
                ("1.9", "H", "no fstab row", {"sc-log-fstab": (0, ["ID      WHEN              ORIGIN  FILE"])}),
                ("1.9", "H", "sc cat GOOD is not /etc/fstab", dict(ssh={"sc cat %s" % GOOD: (0, "5" * 64 + "  -\n")})),
                ("1.9", "H", "sc cat GOOD fails", dict(ssh={"sc cat %s" % GOOD: (0, "sc: no snapshot\n")}))):
            r, end = self.boot1(**kw)
            self.assert_fails(r, end, cid, strength, name)

    def test_warnings(self):
        # degraded: SmartConfig's units are held by 1.5, another unit's failure is a W.
        r, end = self.boot1(ssh={"is-system-running": (1, "degraded\n")})
        self.assertEqual((end, r.status("1.3")), (None, "WARN"))
        self.assertIn("snapd.service", r.rows["1.3"].notes)
        r, end = self.boot1(**{"sc-status": (0, status_lines(B1)[:3])})
        self.assertEqual((end, r.status("1.7")), (None, "WARN"))
        self.assertIn("Nothing recorded has changed", r.rows["1.7"].notes)


LOG_EDIT = ("ID      WHEN              ORIGIN  FILE        SIZE  WHAT\n"
            "%s  2026-10-04 12:03  auto    /etc/fstab  218   changed\n"
            "%s  2026-10-04 12:00  auto    /etc/fstab  146   first seen\n" % (BAD, GOOD))
# scd's journal and sc status after the edit (ssh.log of the uefi run of 56f5359), this file's ids.
SCD_JOURNAL = ("baseline: 0 first seen, 0 changed and 0 deleted while not watching (11.837s)\n"
               "T1 /etc/fstab: changed (%s)\n"
               "T1 /etc/fstab: check: blocker fstab-source-missing, line 4: UUID=%s (for /mnt/backup) is not a device "
               "on this machine (%s)\n" % (BAD, e2e.BAD_UUID, BAD))
STATUS_ROW = "%s  2026-10-04 12:03  /etc/fstab  blocker fstab-source-missing, line 4\n" % BAD
STATUS_EDIT = ("This boot:     1a2b3c4d (normal), root read-write\nLast healthy:  this boot, 2026-10-04 12:00\n"
               "scd:           running (pid 630)\n\nChanged since this boot came up, newest first:\n"
               "ID      WHEN              FILE        PROBLEM\n" + STATUS_ROW + "\n"
               "To put /etc/fstab back as it was during the last healthy boot:\n  sc restore %s\n  sync\n"
               "It takes effect at the next boot (now: systemctl daemon-reload, then mount -a).\n" % GOOD)


class TestEditFaults(FaultCase):
    """The edit (E.1 to E.6): scd records the bad line, sc check and sc
    status say what a restore takes."""

    def edit(self, reset_by_guest=True, **ssh):
        r = fake_run(self, GOOD=GOOD)
        self.clock = FakeClock(self, r)
        r.reboot_ssh = lambda cid, how="reboot": qmp_event(1, "RESET", reset_by_guest, 100.0)
        table = {"printf": (0, "4\n%s  /etc/fstab\n" % BAD_SHA), "sc log -n 3": (0, LOG_EDIT),
                 "-t scd": (0, SCD_JOURNAL),
                 "sc check": (2, "blocker   /etc/fstab  4     fstab-source-missing  UUID=%s\n" % e2e.BAD_UUID),
                 "sc status": (2, STATUS_EDIT)}
        table.update(ssh)
        self.asked = answers(r, table)
        return r, outcome_of(r.edit)

    def test_good(self):
        r, end = self.edit()
        self.assertIsNone(end)
        self.assert_passes(r, "E.1", "E.2", "E.3", "E.4", "E.5", "E.6")
        self.assertEqual((r.v["N"], r.v["BAD_SHA"], r.v["BAD"]), (4, BAD_SHA, BAD))
        # BAD stood for 10 s before it was taken: asked every 2 s.
        self.assertGreaterEqual(len([c for c in self.asked if "sc log -n 3" in c]), 6)

    def test_e2_needs_a_new_row_that_stays(self):
        flap = iter(range(10000))
        for name, log in (
                ("scd recorded nothing", (0, LOG_EDIT.split("\n", 2)[0] + "\n" + LOG_EDIT.split("\n", 2)[2])),
                ("sc log shows no row", (0, "ID      WHEN              ORIGIN  FILE        SIZE  WHAT\n")),
                ("sc log fails", (1, "")),
                ("a new row at every look", lambda c: (0, "%06x  2026-10-04 12:03  auto  /etc/fstab  218  changed\n%s"
                                                       % (next(flap), LOG_EDIT.split("\n", 1)[1])))):
            r, end = self.edit(**{"sc log -n 3": log})
            self.assert_fails(r, end, "E.2", "H", name)
            self.assertIsNone(r.v["BAD"], name)
            self.assertIn("after 120 s", r.rows["E.2"].notes, name)

    def test_one_fault(self):
        def status(old, new):
            self.assertIn(old, STATUS_EDIT)
            return {"sc status": (2, STATUS_EDIT.replace(old, new))}

        restore, sync = "  sc restore %s\n" % GOOD, "  sync\n"
        for cid, name, ssh in (
                ("E.3", "scd checked nothing", {"-t scd": (0, SCD_JOURNAL.split("T1 /etc/fstab: check:")[0])}),
                ("E.3", "the blocker is on another snapshot",
                 {"-t scd": (0, SCD_JOURNAL.replace("(%s)\n" % BAD, "(%s)\n" % GOOD))}),
                ("E.3", "the blocker is on another line", {"-t scd": (0, SCD_JOURNAL.replace("line 4:", "line 3:"))}),
                ("E.4", "sc check finds nothing", {"sc check": (0, "No problem found.\n")}),
                ("E.4", "sc check fails", {"sc check": (1, "sc: store: database is locked\n")}),
                ("E.5", "sc status exit 0", {"sc status": (0, STATUS_EDIT)}),
                ("E.5", "no row for BAD", status(STATUS_ROW, "")),
                ("E.5", "the row is GOOD's", status(STATUS_ROW, STATUS_ROW.replace(BAD, GOOD))),
                ("E.5", "the row is of another line", status("line 4\n", "line 3\n")),
                ("E.5", "no sc restore", status(restore, "")),
                ("E.5", "sc restore of another snapshot", status(restore, "  sc restore %s\n" % BAD)),
                ("E.5", "sc restore only above the row",
                 {"sc status": (2, STATUS_EDIT.replace(restore, "").replace(STATUS_ROW, restore + STATUS_ROW))}),
                ("E.5", "no sync", status(sync, "")),
                ("E.5", "sync before sc restore", status(restore + sync, sync + restore)),
                ("E.5", "not said when it takes effect",
                 status("It takes effect at the next boot", "Reboot to apply it")),
                ("E.5", "it says remount", status(restore, "  mount -o remount,rw /\n" + restore)),
                ("E.5", "it says reboot", status(sync, sync + "  systemctl reboot\n"))):
            r, end = self.edit(**ssh)
            self.assert_fails(r, end, cid, "F", name)
            self.assert_passes(r, *[c for c in ("E.1", "E.2", "E.3", "E.4", "E.5", "E.6") if c != cid])

    def test_e1_is_the_labs(self):
        r, end = self.edit(printf=(1, ""))  # the append failed: nothing to hold scd to
        self.assertIsInstance(end, e2e.Stop)
        self.assertEqual((end.row.id, end.row.cause, e2e.verdict(r.rows.values())), ("E.1", "lab", "INCONCLUSIVE"))
        r, end = self.edit(printf=(0, "5\n%s  /etc/fstab\n" % BAD_SHA))  # another image's fstab: said, and N is bound
        self.assertEqual((r.status("E.1"), r.v["N"]), ("WARN", 5))

    def test_e6_wants_the_guests_own_reset(self):
        r, end = self.edit(reset_by_guest=False)
        self.assertIsInstance(end, e2e.Stop)
        self.assertEqual((end.row.id, end.row.status, end.row.cause), ("E.6", "FAIL", "lab"))
        self.assertEqual(e2e.verdict(r.rows.values()), "INCONCLUSIVE")


BOOT_BROKEN = (UEFI_SILENT + PRE_CLEAN + POST_CLEAN + kernel_lines()
               + "[  OK  ] Finished sc-boot-seen.service - SmartConfig: record that this boot started (sc boot seen).\n"
               + TIMED_OUT + DEPEND + render("emergency") + EMERGENCY_TAIL)


class TestBoot2Faults(FaultCase):
    """Boot 2 (2.1 to 2.6): the bad line's boot, with nothing typed."""

    def boot2(self, text=BOOT_BROKEN, outcome="b", **ssh):
        r = fake_run(self, con=FakeCon(text), EXP_DEFAULT=EXP_DEFAULT, GOOD=GOOD, BAD=BAD, B1=B1, N=4)
        FakeClock(self, r)
        r.unexpected = lambda a: None
        r.probe_boot2 = lambda: (outcome, None if outcome == "a" else B2, "active" if outcome == "b" else "inactive",
                                 "inactive" if outcome == "a" else "active", 0 if outcome == "a" else 1)
        table = {"cat /var/lib/smartconfig/boots": (0, BOOTS_2), "grub-editenv": (0, "smartconfig_pending=1\n"),
                 "'sync'": (0, "")}
        table.update(ssh)
        answers(r, table)
        return r, outcome_of(r.boot2, attempt("2", False))

    def test_good(self):
        for outcome in "bc":
            boots = BOOTS_2 if outcome == "b" else BOOTS_2.replace("emergency=active", "emergency=inactive")
            r, end = self.boot2(outcome=outcome, **{"cat /var/lib/smartconfig/boots": (0, boots)})
            self.assertIsNone(end)
            self.assert_passes(r, "2.1", "2.2", "2.3", "2.4", "2.5", "2.6")
            self.assertEqual((r.v["outcome"], r.v["B2"]), (outcome, B2))
            self.assertEqual(r.v["B2_emergency"], "active" if outcome == "b" else "inactive")
            self.assertIs(r.flag, True)  # sc-boot-seen set it: boot 3 expects the menu
        r, end = self.boot2(outcome="a")
        self.assertIsNone(end)
        self.assert_passes(r, "2.1", "2.2", "2.3", "2.4", "2.5")
        self.assertEqual((r.status("2.6"), r.v["outcome"], r.v["B2"]), ("SKIP", "a", None))
        self.assertEqual(r.v["B2_short"], console.short_boot(B2))  # from the report: 3.8.boots holds the file to it

    def test_one_fault(self):
        no_seen = BOOTS_2.replace("%s seen 200\n" % B2, "")
        for cid, name, kw in (
                ("2.1", "a menu showed", dict(text=BOOT_BROKEN.replace(POST_CLEAN, POST_CLEAN + UEFI_MENU))),
                ("2.1", "the flag was set before the bad boot",
                 dict(text=BOOT_BROKEN.replace("pending=[]", "pending=[1]"))),
                ("2.2", "another command line", dict(text=BOOT_BROKEN.replace(EXP_DEFAULT, EXP_DEFAULT + " fstab=no"))),
                ("2.3", "emergency mode with no device timeout", dict(text=BOOT_BROKEN.replace(TIMED_OUT, ""))),
                ("2.3", "a login prompt and no timeout", dict(text=BOOT_BROKEN.split(TIMED_OUT)[0] + LOGIN)),
                ("2.3", "the timeout, but local-fs.target did not fail", dict(text=BOOT_BROKEN.replace(DEPEND, ""))),
                ("2.3", "the timeout, then a login prompt", dict(text=BOOT_BROKEN.split(DEPEND)[0] + LOGIN)),
                ("2.3", "local-fs.target failed before the timeout",
                 dict(text=BOOT_BROKEN.replace(TIMED_OUT + DEPEND, DEPEND + TIMED_OUT + "[  OK  ] Reached x.\n"))),
                ("2.4", "no prompt and no login", dict(text=BOOT_BROKEN.split("This boot:")[0])),
                ("2.6", "no seen line for B2", {"cat /var/lib/smartconfig/boots": (0, no_seen)}),
                ("2.6", "no bad line for B2",
                 {"cat /var/lib/smartconfig/boots": (0, BOOTS_2.replace(BAD2 + "\n", ""))}),
                ("2.6", "B2 bad with local-fs=active",
                 {"cat /var/lib/smartconfig/boots": (0, BOOTS_2.replace("local-fs=inactive", "local-fs=active"))}),
                ("2.6", "the menu flag is not set", {"grub-editenv": (0, "recordfail=1\n")}),
                ("2.6", "the menu flag is 0", {"grub-editenv": (0, "smartconfig_pending=0\n")}),
                ("2.6", "sync fails", {"'sync'": (1, "")})):
            r, end = self.boot2(**kw)
            self.assert_fails(r, end, cid, "H", name)

    def test_25_is_held_in_boot_2(self):
        # 2.5 is F: the mode goes on to 2.6 and the reset.
        r, end = self.boot2(text=BOOT_BROKEN.replace("  sync\n", ""))
        self.assert_fails(r, end, "2.5", "F", "the report lacks a line")
        self.assert_passes(r, "2.6")
        r, end = self.boot2(text=BOOT_BROKEN.replace(render("emergency"), ""))
        self.assert_fails(r, end, "2.5", "F", "no report")

    def test_25_waits_for_the_reports_end(self):
        # 2.4's prompt can be on the console before sc has written all of
        # its report: 2.5 reads when what ends the report is there.
        con = FakeCon(BOOT_BROKEN[:BOOT_BROKEN.index("Changed since")])
        r = fake_run(self, con=con, GOOD=GOOD, BAD=BAD, B1=B1, N=4)
        clock = FakeClock(self, r)
        r.mux.wait = lambda size, timeout: (clock.sleep(timeout), setattr(r.con, "t", BOOT_BROKEN))
        r.check_emergency_report(attempt("2"), "b", B2)
        self.assert_passes(r, "2.5")
        self.assertNotIn("nothing came after the report", r.rows["2.5"].notes)

    def test_the_flag_is_set_from_23_on(self):
        # A flake after 2.3 retries boot 2 with the menu expected (sc-boot-seen ran).
        r, end = self.boot2(text=BOOT_BROKEN.replace(DEPEND, DEPEND + TCG_PANIC))
        self.assertIsInstance(end, e2e.Retry)
        self.assertEqual((end.sig, r.status("2.3"), r.flag), ("panic", "PASS", True))
        self.assertNotIn("2.4", r.rows)

    def test_a_panic_that_is_not_tcgs_is_no_retry(self):
        r, end = self.boot2(text=BOOT_BROKEN.replace(DEPEND, DEPEND + VFS_PANIC))
        self.assertIsInstance(end, e2e.LabError)
        self.assertNotIsInstance(end, e2e.Retry)
        self.assertIn("not TCG's", str(end))
        self.assertNotIn("2.4", r.rows)

    def test_an_end_that_is_no_end_of_boot_2(self):
        # A grub> prompt or a shutdown is not one of 2.4's outcomes, and nothing says it is sc's.
        for tail, said in (("[   95.103312] reboot: Power down\n", "boot 2 ended in shutdown"),
                           ("error: no such device: 0b1c.\ngrub> ", "boot 2 ended in grub")):
            r, end = self.boot2(text=BOOT_BROKEN.split("This boot:")[0] + tail)
            self.assertIsInstance(end, e2e.LabError)
            self.assertIn(said, str(end))
            self.assertEqual((r.status("2.3"), r.status("2.4")), ("PASS", None))

    def test_reset_at_timeout(self):
        r = fake_run(self, con=FakeCon(BOOT_BROKEN), EXP_DEFAULT=EXP_DEFAULT)
        FakeClock(self, r)
        r.args.boot2 = "reset-at-timeout"
        self.assertIsNone(outcome_of(r.boot2, attempt("2", False)))
        self.assert_passes(r, "2.1", "2.2", "2.3", "2.4")
        self.assertEqual((r.status("2.5"), r.status("2.6"), r.v["outcome"], r.flag), ("SKIP", "SKIP", "a", True))


BOOTS_3 = BOOTS_2
RESTORED = "restored /etc/fstab from %s (mode 0644 root:root), previous state saved as %s\n" % (GOOD, PRE)
# What the console shows after Enter on the rescue entry: GRUB's echo, the kernel, sc's report, the prompt.
AFTER_ENTER = "\n" + e2e.RESCUE_ECHO + "\n" + kernel_lines(EXP_RESCUE) + rescue_report("b") + RESCUE_TAIL
UNDO_ANSWERS = {"mount ": (0, ""), "findmnt": (0, "rw,relatime\n"), "sc restore": (0, RESTORED),
                "sha256sum": (0, FSTAB_SHA + "  /etc/fstab\n"), "sync": (0, ""), "systemctl daemon-reload": (0, "")}


class TestBoot3Faults(FaultCase):
    """Boot 3 (3.0 to 3.9): the menu, the rescue entry, sc's report, the
    root shell and the report's commands."""

    def boot3(self, menu=UEFI_MENU, after=AFTER_ENTER, enter="\nroot@sclab:~# ", facts=None, events=None, cmds=None,
              **v):
        cmds = dict({"sh ": (0, facts or rescue_facts(BOOTS_3))}, **dict(UNDO_ANSWERS, **(cmds or {})))
        con = FakeCon(UEFI_SILENT + PRE_FLAG + POST_FLAG + menu, cmds=cmds, typed={"\r": enter})
        values = dict(EXP_RESCUE=EXP_RESCUE, GOOD=GOOD, BAD=BAD, B1=B1, B2=B2, N=4, outcome="b", FSTAB_SHA=FSTAB_SHA)
        r = fake_run(self, con=con, **dict(values, **v))
        FakeClock(self, r)
        r.flag = True
        r.qmp = FakeQmp([qmp_event(0, "RESET", True, 100.0)] if events is None else events)
        r.qmp.mark = lambda: 0
        r.machine = FakeMachine()
        r.new_boot = lambda ev: ev

        def pick(a, g, target, cid="3.2"):  # 3.2's keys: console.pick has its own tests (test_lab.py)
            g["keys"], g["enter_pos"] = ["0e", "0e", "0e"], con.size()
            con.t += after

        r.pick = pick
        return r, outcome_of(r.boot3, attempt("3", True))

    def test_good(self):
        r, end = self.boot3()
        self.assertIsNone(end)
        self.assert_passes(r, "3.0", "3.1", "3.2", "3.3", "3.4", "3.5", "3.6", "3.7", "3.8.ro", "3.8.mounts",
                           "3.8.units", "3.8.boots", "3.8.sc", "3.8.hashes", "3.8.vcs1", "3.9")
        self.assertEqual(r.status("3.8.vga"), "SKIP")
        self.assertEqual((r.v["K"], r.v["reason"], r.v["B3"], r.v["PRE"]), (1, "b", B3, PRE))
        self.assertEqual(r.con.sent, ["\r", "systemctl reboot\r"])  # Enter at the prompt; the reboot is not awaited
        self.assertFalse(r.shell)

    def test_one_fault(self):
        noise = "".join("EXT4-fs (vda1): re-mounted, pass %d\n" % i for i in range(12))
        on_rescue = UEFI_MENU.replace("*Ubuntu", " Ubuntu").replace(" SmartConfig rescue", "*SmartConfig rescue")
        rw = rescue_facts(BOOTS_3, mounts=[MOUNTS[0].replace("ro,relatime", "rw,relatime")] + MOUNTS[1:])
        for cid, name, kw in (
                ("3.1", "a 5 s menu", dict(menu=UEFI_MENU.replace("in 30s.", "in 5s."))),
                ("3.1", "the highlight is on the rescue entry", dict(menu=on_rescue)),
                ("3.1", "two rescue entries",
                 dict(menu=UEFI_MENU.replace(" SmartConfig rescue\n", " SmartConfig rescue\n" * 2))),
                ("3.3", "GRUB did not echo the entry's line",
                 dict(after=AFTER_ENTER.replace(e2e.RESCUE_ECHO + "\n", ""))),
                ("3.4", "another root than grub.cfg's entry", dict(EXP_RESCUE=EXP_RESCUE.replace("1bfe", "0b1c"))),
                ("3.4", "an argument more than grub.cfg's entry",
                 dict(after=AFTER_ENTER.replace(EXP_RESCUE, EXP_RESCUE + " quiet"))),
                ("3.5", "a login prompt, no rescue prompt", dict(after=AFTER_ENTER.split("This boot:")[0] + LOGIN)),
                ("3.5", "the bad device was waited for", dict(after=AFTER_ENTER.replace(INIT, INIT + TIMED_OUT))),
                ("3.5", "local-fs.target failed", dict(after=AFTER_ENTER.replace(INIT, INIT + DEPEND))),
                ("3.6", "no report above the prompt", dict(after=AFTER_ENTER.replace(rescue_report("b"), ""))),
                ("3.6", "the report scrolled off: 25 rows",
                 dict(after=AFTER_ENTER.replace("Press Enter for maintenance", noise + "Press Enter for maintenance"))),
                ("3.6", "a line of the report is gone",
                 dict(after=AFTER_ENTER.replace("  systemctl daemon-reload\n", ""))),
                ("3.7", "a password is asked for", dict(enter="\nGive root password for maintenance\nPassword: ")),
                ("3.7", "sulogin gives up",
                 dict(enter="\nCannot open access to console, the root account is locked.\n")),
                ("3.7", "nothing after Enter", dict(enter="")),
                ("3.8.ro", "/ is mounted read-write", dict(facts=rw)),
                ("3.9", "the remount fails", dict(cmds={"mount ": (32, "mount: /: cannot remount read-write\n")})),
                ("3.9", "a SHUTDOWN, not a reset", dict(events=[qmp_event(0, "SHUTDOWN", True, 100.0)])),
                ("3.9", "a reset the guest did not ask for", dict(events=[qmp_event(0, "RESET", False, 100.0)]))):
            r, end = self.boot3(**kw)
            self.assert_fails(r, end, cid, "H", name)

    def test_kernel_without_fstab_no_is_a_missed_pick(self):
        # Enter landed on another entry: the lab's keys, not sc's entry. The boot again.
        r, end = self.boot3(after=AFTER_ENTER.replace(EXP_RESCUE, EXP_DEFAULT))
        self.assertIsInstance(end, e2e.Retry)
        self.assertEqual(end.sig, "menu-missed")
        self.assertNotIn("3.2", r.rows)

    def test_36_wants_the_prompt_under_the_report(self):
        # The screen from the report to the prompt is what must fit: with no prompt read, 3.6 cannot say so.
        text = INIT + rescue_report("b") + RESCUE_TAIL.split("Press Enter")[0]
        r = fake_run(self, con=FakeCon(text), GOOD=GOOD, BAD=BAD, B1=B1, N=4, outcome="b")
        end = outcome_of(r.check_rescue_report, attempt("3", True), len(text))
        self.assert_fails(r, end, "3.6", "H", "no prompt after the report")
        self.assertIn("no 'Press Enter for maintenance' after the report", r.rows["3.6"].notes)

    def rescue(self, boots=BOOTS_3, v=None, **over):
        values = dict(GOOD=GOOD, BAD=BAD, B1=B1, B2=B2, FSTAB_SHA=FSTAB_SHA, K=1, reason="b", B3_short="3c4d5e6f")
        r = fake_run(self, con=FakeCon(cmds={"sh ": (0, rescue_facts(boots, **over))}), **dict(values, **(v or {})))
        return r, outcome_of(r.check_rescue_facts, attempt("3", True))

    def test_38_good(self):
        r, end = self.rescue()
        self.assertIsNone(end)
        self.assert_passes(r, "3.8.ro", "3.8.mounts", "3.8.units", "3.8.boots", "3.8.sc", "3.8.hashes", "3.8.vcs1")

    def test_38_one_fault(self):
        mounted = 'SOURCE="/dev/vda16" LABEL="BOOT" FSTYPE="ext4" OPTIONS="rw,relatime"'
        refused = "sc: store /var/lib/smartconfig: " + e2e.RESTORE_REFUSED
        third = "6f708192-0000-4000-8000-000000000006"
        for cid, name, kw in (
                ("3.8.mounts", "/boot is mounted", dict(mounts=[MOUNTS[0], "/boot " + mounted] + MOUNTS[2:])),
                ("3.8.mounts", "/mnt/backup is mounted", dict(mounts=MOUNTS[:3] + ["/mnt/backup " + mounted])),
                ("3.8.mounts", "rescue.target is not active", dict(active=(0, ["rescue.target=inactive"]))),
                ("3.8.units", "sc-boot-seen ran on the read-only root",
                 {"show:sc-boot-seen.service": (0, ["Id=sc-boot-seen.service", "ConditionResult=yes"])}),
                ("3.8.units", "sc-boot-seen not shown", {"show:sc-boot-seen.service": None}),
                ("3.8.units", "sc-boot-ok started",
                 {"show:sc-boot-ok.service": (0, ["Id=sc-boot-ok.service", "ExecMainStartTimestampMonotonic=2351120"])}),
                ("3.8.boots", "the rescue boot is in the boots file", dict(boots=BOOTS_3 + "%s seen 300\n" % B3,
                                                                           v=dict(K=2, reason="a"))),
                ("3.8.boots", "no seen line for B2", dict(boots=BOOTS_3.replace("%s seen 200\n" % B2, ""))),
                ("3.8.boots", "two failed boots, the report said 1",
                 dict(boots=BOOTS_3 + "%s seen 250\n" % third, v=dict(reason="a"))),
                ("3.8.boots", "no failed boot in the file", dict(boots=BOOTS_1, v=dict(B2=None, reason=None))),
                ("3.8.boots", "B1 is not in the file",
                 dict(boots=BOOTS_3.replace(B1, third), v=dict(K=None, reason=None))),
                ("3.8.boots", "the report named another boot as this one", dict(v=dict(B3_short="0a0b0c0d"))),
                ("3.8.boots", "boot 2's report named another boot (outcome a)",
                 dict(v=dict(B2=None, B2_short="0a0b0c0d"))),
                ("3.8.sc", "sc status --console exit 0", dict(sc_status_console=(0, ["  sc restore " + GOOD]))),
                ("3.8.sc", "sc status --console did not run", dict(sc_status_console=None)),
                ("3.8.sc", "sc diff shows no added line", dict(sc_diff=(0, [" LABEL=BOOT /boot ext4 defaults 0 2"]))),
                ("3.8.sc", "sc cat GOOD is not boot 1's fstab", dict(sc_cat_good=(0, ["5" * 64]))),
                ("3.8.sc", "sc check exit 0", dict(sc_check=(0, ["/etc/fstab: blocker fstab-source-missing, line 4"]))),
                ("3.8.sc", "sc check finds another problem",
                 dict(sc_check=(2, ["/etc/fstab: blocker fstab-syntax, line 4"]))),
                ("3.8.sc", "sc restore worked on a read-only root",
                 dict(sc_restore=(0, ["restored /etc/fstab from " + GOOD]))),
                ("3.8.sc", "sc restore not run: facts.sh found the root writable", dict(sc_restore=(125, [refused]))),
                ("3.8.sc", "sc restore did not run", dict(sc_restore=None)),
                ("3.8.sc", "sc restore failed for another reason", dict(sc_restore=(1, ["sc: no snapshot " + GOOD]))),
                ("3.8.hashes", "the store changed",
                 dict(after=["/var/lib/smartconfig/changes.db " + "9" * 64] + HASHES[1:])),
                ("3.8.hashes", "sc check left a file", dict(leftovers=(0, ["/etc/sc-check-812345"]))),
                ("3.8.hashes", "the leftovers were not looked for",
                 dict(leftovers=(1, ["find: /etc: Permission denied"]))),
                ("3.8.vcs1", "the screen does not show the restore", dict(vcs1=(0, ["sclab login:"]))),
                ("3.8.vcs1", "the screen shows another snapshot's restore", dict(vcs1=(0, ["  sc restore " + BAD])))):
            r, end = self.rescue(**kw)
            self.assert_fails(r, end, cid, "F", name)
            for other in ("3.8.ro", "3.8.mounts", "3.8.units", "3.8.boots", "3.8.sc", "3.8.hashes", "3.8.vcs1"):
                if other != cid:
                    self.assertEqual(r.status(other), "PASS", "%s: %s is %s" % (name, other, r.rows[other].notes))

    def undo(self, cmds=None, undo=None, events=None, gap=False):
        con = FakeCon(cmds=dict(UNDO_ANSWERS, **(cmds or {})))
        r = fake_run(self, con=con, GOOD=GOOD, FSTAB_SHA=FSTAB_SHA,
                     undo=[c.format(GOOD=GOOD) for c in e2e.UNDO] if undo is None else undo)
        r.shell = True
        r.qmp = FakeQmp([qmp_event(0, "RESET", True, 100.0)] if events is None else events)
        r.qmp.mark = lambda: 0
        r.machine = FakeMachine()
        r.new_boot = lambda ev: ev
        if gap:  # the host stood still while a command ran
            real = con.cmd
            con.cmd = lambda c, t=180: (setattr(r.mux, "gaps", r.mux.gaps + [(1.0, 400.0)]), real(c, t))[1]
            self.addCleanup(setattr, FakeMux, "gaps", [])
        return r, outcome_of(r.run_undo, attempt("3", True))

    def test_39_good(self):
        r, end = self.undo()
        self.assertIsNone(end)
        self.assert_passes(r, "3.9")
        self.assertEqual((r.v["PRE"], r.shell, r.con.sent), (PRE, False, ["systemctl reboot\r"]))

    def test_39_one_fault(self):
        want = [c.format(GOOD=GOOD) for c in e2e.UNDO]
        for name, kw, said in (
                ("the report restores another snapshot", dict(undo=[c.replace(GOOD, BAD) for c in want]),
                 "the report's commands"),
                ("the report has no sync", dict(undo=[c for c in want if c != "sync"]), "the report's commands"),
                ("the report has no remount", dict(undo=want[1:]), "the report's commands"),
                ("the report gives no command", dict(undo=[]), "the report's commands"),
                ("the report reboots before the restore", dict(undo=want[:1] + want[:0:-1]), "the report's commands"),
                ("the remount fails", dict(cmds={"mount ": (32, "mount: /: cannot remount\n")}), "exit 32"),
                ("/ is still read-only", dict(cmds={"findmnt": (0, "ro,relatime\n")}), "after the remount"),
                ("sc restore fails", dict(cmds={"sc restore": (1, "sc: store: database is locked\n")}), "exit 1"),
                ("sc restore does not say what it saved", dict(cmds={"sc restore": (0, "restored /etc/fstab\n")}),
                 "previous state saved as"),
                ("sc restore restored from another snapshot",
                 dict(cmds={"sc restore": (0, RESTORED.replace("from " + GOOD, "from " + BAD))}),
                 "previous state saved as"),
                ("/etc/fstab is not boot 1's after the restore",
                 dict(cmds={"sha256sum": (0, "5" * 64 + "  /etc/fstab\n")}),
                 "after the restore"),
                ("sync fails", dict(cmds={"sync": (1, "")}), "'sync' exit 1"),
                ("daemon-reload fails", dict(cmds={"systemctl daemon-reload": (1, "Failed to reload daemon\n")}),
                 "exit 1"),
                ("sc restore never returns", dict(cmds={"sc restore": (None, "")}), "no answer in time"),
                ("the guest powers off", dict(events=[qmp_event(0, "SHUTDOWN", True, 100.0)]),
                 "systemctl reboot gave SHUTDOWN"),
                ("the reset is not the guest's", dict(events=[qmp_event(0, "RESET", False, 100.0)]),
                 "systemctl reboot gave RESET")):
            r, end = self.undo(**kw)
            self.assert_fails(r, end, "3.9", "H", name)
            self.assertIn(said, r.rows["3.9"].notes, name)

    def test_39_no_answer_while_the_host_stood_still_is_the_labs(self):
        r, end = self.undo(cmds={"sc restore": (None, "")}, gap=True)
        self.assertIsInstance(end, e2e.LabError)
        self.assertIn("while the host stood still", str(end))
        self.assertNotIn("3.9", r.rows)
        r, end = self.undo(gap=True)  # a gap, but every command answered: nothing to excuse
        self.assertIsNone(end)
        self.assert_passes(r, "3.9")


LOG_4 = ["ID      WHEN              ORIGIN       FILE        SIZE  WHAT",
         "f00d12  2026-10-04 12:10  restore      /etc/fstab  146   restored from %s" % GOOD,
         "%s  2026-10-04 12:10  pre-restore  /etc/fstab  202   before restoring %s" % (PRE, GOOD),
         "%s  2026-10-04 12:03  auto         /etc/fstab  202   changed" % BAD,
         "%s  2026-10-04 12:00  auto         /etc/fstab  146   first seen" % GOOD]


class TestBoot4Faults(FaultCase):
    """Boot 4 (4.1 to 4.6): the menu once more, left alone; a healthy boot
    with /etc/fstab back; the ok verdict against the ledger."""

    def boot4(self, text=BOOT_MENU, boots=BOOTS_4, ssh=None, v=None, **facts):
        values = dict(EXP_DEFAULT=EXP_DEFAULT, GOOD=GOOD, BAD=BAD, PRE=PRE, B1=B1, B2=B2, B3=B3, K=1, R1=7,
                      FSTAB_SHA=FSTAB_SHA, BAD_SHA=BAD_SHA)
        r = fake_run(self, con=FakeCon(text), **dict(values, **(v or {})))
        FakeClock(self, r)
        r.flag = True
        r.wait_ssh = lambda a: B4
        blocks = {"boots": (0, boots.strip().split("\n")), "journal-sc-boot": (0, [(JOURNAL_OK % B4).strip()]),
                  "sc-status": (0, status_lines(B4)), "sc-log-fstab": (0, LOG_4)}
        table = {"is-system-running": (0, "running\n"), "cat /var/lib/smartconfig/boots": (0, boots),
                 "-t sc-boot": (0, JOURNAL_OK % B4), "facts.sh normal": (0, normal_text(**dict(blocks, **facts))),
                 "sc cat %s" % PRE: (0, BAD_SHA + "  -\n")}
        table.update(ssh or {})
        answers(r, table)
        return r, outcome_of(r.boot4, attempt("4", True))

    def test_good(self):
        r, end = self.boot4()
        self.assertIsNone(end)
        self.assert_passes(r, "4.1.cmdline", "4.1.nokey", "4.1", "4.2", "4.3", "4.4", "4.5", "4.6")
        self.assertEqual((r.v["B4"], r.v["R4"]), (B4, 12))
        self.assertIs(r.flag, False)  # the ok verdict unset it: boot 5 expects no menu

    def test_one_fault(self):
        show = ["Result=success", "ExecMainStatus=0", "ConditionResult=yes"]
        fstab = normal_facts().lines("fstab")
        active = ["local-fs.target=active", "emergency.target=inactive", "rescue.target=inactive"]
        with_b3 = BOOTS_4.replace("%s seen 400" % B4, "%s seen 300\n%s seen 400" % (B3, B4))
        for cid, strength, name, kw in (
                ("4.1", "H", "no menu although the flag is set", dict(text=BOOT_MENU.replace(UEFI_MENU, ""))),
                ("4.1", "H", "a 5 s menu", dict(text=BOOT_MENU.replace("in 30s.", "in 5s."))),
                ("4.1", "H", "the highlight is on the rescue entry", dict(text=BOOT_MENU.replace(
                    "*Ubuntu", " Ubuntu").replace(" SmartConfig rescue", "*SmartConfig rescue"))),
                ("4.1", "H", "no rescue entry", dict(text=BOOT_MENU.replace(" SmartConfig rescue\n", ""))),
                ("4.1", "H", "the menu, but the flag is not in grubenv",
                 dict(text=BOOT_MENU.replace(PRE_FLAG, PRE_CLEAN))),
                ("4.2", "H", "the bad device was waited for", dict(text=BOOT_MENU.replace(INIT, INIT + TIMED_OUT))),
                ("4.2", "H", "emergency mode again",
                 dict(text=BOOT_MENU.split(LOGIN)[0] + TIMED_OUT + DEPEND + EMERGENCY_TAIL)),
                ("4.2", "H", "local-fs.target failed", dict(active=(0, ["local-fs.target=failed"] + active[1:]))),
                ("4.2", "H", "emergency.target active",
                 dict(active=(0, [active[0], "emergency.target=active", active[2]]))),
                ("4.2", "H", "rescue.target active", dict(active=(0, active[:2] + ["rescue.target=active"]))),
                ("4.2", "H", "the units' states not read", dict(active=(1, []))),
                ("4.2", "H", "the system never came up", dict(ssh={"is-system-running": (1, "starting\n")})),
                ("4.3", "H", "no seen line for B4", dict(boots=BOOTS_4.replace("%s seen 400\n" % B4, ""))),
                ("4.3", "H", "no ok line for B4", dict(boots=BOOTS_4.replace(OK4 + "\n", ""))),
                ("4.3", "H", "R4 is R1: no restore between them", dict(boots=BOOTS_4.replace(" 401 12 ", " 401 7 "))),
                ("4.3", "H", "R1 is not known", dict(v=dict(R1=None))),
                ("4.3", "H", "the rescue boot left a line", dict(boots=with_b3, v=dict(K=None))),
                ("4.3", "H", "an ok boot between B1 and B4", dict(boots=BOOTS_4.replace(BAD2, OK1.replace(B1, B2)))),
                ("4.4", "H", "/etc/fstab is not boot 1's", {"fstab-sha256": (0, ["5" * 64])}),
                ("4.4", "H", "the bad line is still there", dict(fstab=(0, fstab + [e2e.BAD_LINE]))),
                ("4.4", "H", "the menu flag is still set", dict(grubenv=(0, ["smartconfig_pending=1"]))),
                ("4.5", "H", "sc status still says failed", {"sc-status": (2, status_lines(B4)[:2] + [
                    "Failed since:  1 boot, last 10-04 12:05: a mount failed"] + status_lines(B4)[2:])}),
                ("4.5", "H", "sc status is of another boot", {"sc-status": (0, status_lines(B1))}),
                ("4.6", "F", "the newest row is no restore", {"sc-log-fstab": (0, LOG_4[:1] + LOG_4[3:])}),
                ("4.6", "F", "restored from another snapshot", {"sc-log-fstab": (0, [LOG_4[0], LOG_4[1].replace(
                    "restored from " + GOOD, "restored from " + BAD)] + LOG_4[2:])}),
                ("4.6", "F", "no pre-restore row under it", {"sc-log-fstab": (0, LOG_4[:2] + LOG_4[3:])}),
                ("4.6", "F", "the pre-restore row is not the one sc restore named",
                 {"sc-log-fstab": (0, LOG_4[:2] + [LOG_4[2].replace(PRE, "0f0e0d")] + LOG_4[3:])}),
                ("4.6", "F", "one row only", {"sc-log-fstab": (0, LOG_4[:2])}),
                ("4.6", "F", "PRE is not the bad fstab", dict(ssh={"sc cat %s" % PRE: (0, FSTAB_SHA + "  -\n")})),
                ("4.6", "F", "sc restore named no PRE", dict(v=dict(PRE=None))),
                ("4.6", "F", "sc-boot-ok failed", {"show:sc-boot-ok.service": (0, ["Result=exit-code"] + show[1:])}),
                ("4.6", "F", "grub-editenv failed", {"journal-sc-boot": (0, [
                    (JOURNAL_OK % B4).strip(), "sc: boot verdict: grub-editenv unset failed: exit status 1"])})):
            r, end = self.boot4(**kw)
            self.assert_fails(r, end, cid, strength, name)

    def ledger(self, boots, tries4=0, **v):
        r = fake_run(self, **dict(dict(B1=B1, B2=B2, B3=B3, K=1), **v))
        for n in range(tries4 + 1):  # boot 4's attempts: all but the last were retried
            a = attempt("4", True)
            a.result = "retry" if n < tries4 else None
            r.attempts.append(a)
        return r.ledger_problems(boots, B4)

    def test_ledger(self):
        third = "6f708192-0000-4000-8000-000000000006"
        seen3 = "%s seen 350\n" % third
        self.assertEqual(self.ledger(BOOTS_4), [])
        self.assertEqual(self.ledger(BOOTS_4, B2=None, K=None, B3=None), [])  # nothing known: nothing to hold it to
        # A boot 4 that was retried after userspace began left a seen line of its own.
        retried = BOOTS_4.replace("%s seen 400" % B4, seen3 + "%s seen 400" % B4)
        self.assertEqual(self.ledger(retried, tries4=1), [])
        for name, boots, kw, said in (
                ("the rescue boot left a line",
                 BOOTS_4.replace("%s seen 400" % B4, "%s seen 300\n%s seen 400" % (B3, B4)),
                 dict(K=None), "a line for the rescue boot B3"),
                ("B1 has no ok line", BOOTS_4.replace(OK1 + "\n", ""), {}, "no B1 ok line"),
                ("B1 is not in the file", BOOTS_4.replace(B1, third), {}, "no B1 ok line"),
                ("a boot after B4", BOOTS_4 + "%s seen 500\n" % B5, {}, "the last boot in the file"),
                ("an empty file", "", {}, "no B1 ok line"),
                ("a verdict with no seen line", BOOTS_4.replace("%s seen 200\n" % B2, ""), {}, "without a seen line"),
                ("one failed boot more than the report said", retried, {}, "the ledger expects 1"),
                ("two more, one retry of boot 4", retried.replace(seen3, seen3 + "%s seen 360\n" % B5), dict(tries4=1),
                 "the ledger expects 1 (+1 boot 4 retries)"),
                ("no failed boot, the report said 1", BOOTS_1 + "%s seen 400\n%s\n" % (B4, OK4), dict(B2=None),
                 "0 boots between B1 and B4"),
                ("B2 came up ok", BOOTS_4.replace(BAD2, OK1.replace(B1, B2)), {}, "an ok boot between B1 and B4"),
                ("the first boot after B1 is not B2", BOOTS_4.replace(B2, third), {}, "the first boot after B1 is")):
            problems = self.ledger(boots, **kw)
            self.assertTrue(any(said in p for p in problems), (name, problems))


class TestBoot5Faults(FaultCase):
    """Boot 5 (5.1): no menu again, and that is looked at before anything
    in the boot can be retried."""

    def boot5(self, text=BOOT_CLEAN, flag=False, healthy_end=None):
        r = fake_run(self, con=FakeCon(text))
        FakeClock(self, r)
        r.flag = True
        r.wait_ssh = lambda a: B5
        if healthy_end is not None:
            r.wait_healthy_end = healthy_end
        answers(r, {"cat /var/lib/smartconfig/boots": (0, BOOTS_4 + TestHeldRows.OK5)})
        return r, outcome_of(r.boot5, attempt("5", flag))

    def test_good(self):
        r, end = self.boot5()
        self.assertIsNone(end)
        self.assert_passes(r, "5.1", "5.1.ok", "5.1.notime")
        self.assertEqual((r.v["B5"], r.flag), (B5, False))

    def test_a_menu_fails_51(self):
        for name, text in (("a menu", BOOT_CLEAN.replace(POST_CLEAN, POST_CLEAN + UEFI_MENU)),
                           ("the flag still set", BOOT_CLEAN.replace("pending=[]", "pending=[1]")),
                           ("the flag block made a menu of it", BOOT_CLEAN.replace(POST_CLEAN, POST_FLAG))):
            r, end = self.boot5(text)
            self.assert_fails(r, end, "5.1", "H", name)
            self.assert_passes(r, "5.1.ok", "5.1.notime")  # the boot's other rows are recorded first

    def test_51_is_recorded_before_a_retry(self):
        def no_ssh(a, cid):
            raise e2e.Retry("no-ssh", "a login prompt, but no ssh in 600 s")

        r, end = self.boot5(healthy_end=no_ssh)
        self.assertIsInstance(end, e2e.Retry)
        self.assert_passes(r, "5.1")  # 5b's attempt (flag unknown) skips it: this result stands
        r, end = self.boot5(BOOT_CLEAN.replace(POST_CLEAN, POST_CLEAN + UEFI_MENU), healthy_end=no_ssh)
        self.assertIsInstance(end, e2e.Retry)
        self.assertEqual((r.rows["5.1"].status, r.rows["5.1"].cause), ("FAIL", "M4"))
        self.assertEqual(e2e.verdict(r.rows.values()), "FAIL")


class TestGlue(FaultCase):
    """What ties the checks together: boot()'s retries, flow()'s second
    looks, the waits that tell a flake of the lab from a failure, and
    the small [lab] checks."""

    def boot_run(self, text=""):
        r = fake_run(self, con=FakeCon(text))
        r.resets = []
        r.reset_vm = lambda why: r.resets.append(why)
        return r

    def test_boot_passes_the_flag_on(self):
        # no-ssh: the boot reached userspace, so the next attempt does not know the flag.
        r = self.boot_run(BOOT_CLEAN)
        r.flag = True
        flags = []

        def fn(a):
            flags.append(a.flag)
            if len(flags) == 1:
                raise e2e.Retry("no-ssh", "test")

        a = r.boot("4", fn)
        self.assertEqual((flags, r.flag, a.n, a.flag), ([True, None], None, 2, None))
        self.assertEqual(([x.result for x in r.attempts], r.retries, len(r.resets)), (["retry", "done"], 1, 1))
        with open(os.path.join(r.run, "ledger.json")) as f:
            self.assertEqual([x["flag_expected"] for x in json.load(f)["attempts"]], [True, None])

    def test_boot_the_modes_retries_run_out(self):
        limit = fake_run(self).b("RETRIES_MODE")
        for used, retried in ((limit, False), (limit - 1, True)):
            r = self.boot_run()
            r.retries = used  # by the boots before this one
            calls = []

            def fn(a):
                calls.append(a.n)
                if len(calls) == 1:
                    raise e2e.Retry("stall", "test")

            end = outcome_of(r.boot, "5", fn)
            if retried:
                self.assertIsNone(end)
                self.assertEqual((calls, len(r.resets), r.retries), ([1, 2], 1, limit))
            else:
                self.assertIsInstance(end, e2e.LabError)
                self.assertIn("no retry left (%d in this mode)" % limit, str(end))
                self.assertEqual((calls, r.resets, r.retries), ([1], [], limit))

    def test_boot_limits_by_signature(self):
        r = self.boot_run()

        def panic(a):
            raise e2e.Retry("panic", "test")

        self.assertIsInstance(outcome_of(r.boot, "1", panic), e2e.LabError)
        self.assertEqual(len(r.resets), r.b("RETRIES_PANIC"))
        self.assertLess(r.b("RETRIES_PANIC"), r.b("RETRIES_MODE"))
        # A grub> prompt and a missed menu are one kind: one retry a boot for both.
        r = self.boot_run()
        sigs = iter(("grub-prompt", "menu-missed", "grub-prompt"))

        def menu(a):
            raise e2e.Retry(next(sigs), "test")

        self.assertIsInstance(outcome_of(r.boot, "3", menu), e2e.LabError)
        self.assertEqual((len(r.resets), [a.result for a in r.attempts]), (1, ["retry", "retry"]))

    def test_boot_stopped_is_not_retried(self):
        r = self.boot_run()
        end = outcome_of(r.boot, "1", lambda a: r.record("1.2", problems=["Command line differs"]))
        self.assertIsInstance(end, e2e.Stop)
        self.assertEqual(([a.result for a in r.attempts], r.resets), (["stopped"], []))

    def flow_run(self, unknown=(), by_guest=True, qemu_rc=0):
        r = fake_run(self)
        r.machine = argparse.Namespace(wait_exit=lambda timeout: qemu_rc)
        calls = []

        def boot(label, fn):
            calls.append("boot " + label)
            a = attempt(label, None if label in unknown else False)
            cid = {"1": "1.1", "5": "5.1"}.get(label[0])
            if cid and a.flag is None:  # as boot1 and boot5 do
                r.skip(cid, "a retried attempt: the menu flag is not known")
            elif cid:
                r.record(cid)
            return a

        def reboot_ssh(cid, how="reboot"):
            calls.append("%s %s" % (how, cid))
            return qmp_event(1, "SHUTDOWN" if how == "poweroff" else "RESET", by_guest, 100.0)

        r.boot, r.reboot_ssh = boot, reboot_ssh
        r.edit = lambda: calls.append("edit")
        r.reset_settled = lambda: calls.append("reset")
        return r, calls, outcome_of(r.flow)

    FLOW = ["boot 0", "reboot 0.7", "boot 1", "edit", "boot 2", "reset", "boot 3", "boot 4", "reboot 5.1", "boot 5",
            "poweroff 5.2"]

    def test_flow(self):
        r, calls, end = self.flow_run()
        self.assertIsNone(end)
        self.assertEqual(calls, self.FLOW)
        self.assert_passes(r, "1.1", "5.1", "5.2")

    def test_flow_looks_again_when_the_flag_was_unknown(self):
        # Boot 1 or 5 retried after userspace began: its x.1 was skipped. A clean reboot repeats the boot.
        r, calls, end = self.flow_run(unknown=("1", "5"))
        self.assertIsNone(end)
        want = list(self.FLOW)
        want[3:3] = ["reboot 1.1", "boot 1b"]
        want[-1:-1] = ["reboot 5.1", "boot 5b"]
        self.assertEqual(calls, want)
        self.assert_passes(r, "1.1", "5.1", "5.2")

    def test_flow_never_passes_an_unchecked_decision(self):
        for unknown, cid, last in ((("1", "1b"), "1.1", "boot 1b"), (("5", "5b"), "5.1", "boot 5b")):
            r, calls, end = self.flow_run(unknown=unknown)
            self.assertIsInstance(end, e2e.LabError)
            self.assertIn("%s was never checked" % cid, str(end))
            self.assertEqual((calls[-1], r.ctx, r.status(cid)), (last, cid, "SKIP"))

    def test_52(self):
        for name, kw in (("the host's shutdown", dict(by_guest=False)), ("QEMU exit 1", dict(qemu_rc=1)),
                         ("QEMU did not exit", dict(qemu_rc=None))):
            r, calls, end = self.flow_run(**kw)
            self.assert_fails(r, end, "5.2", "H", name)

    def healthy_end(self, text, label="4", cid="4.2"):
        r = fake_run(self, con=FakeCon(text))
        FakeClock(self, r)
        r.unexpected = lambda a: None
        try:
            return r, r.wait_healthy_end(attempt(label), cid)
        except (e2e.Stop, e2e.Retry, e2e.LabError) as e:
            return r, e

    def test_healthy_end(self):
        up = UEFI_SILENT + PRE_CLEAN + POST_CLEAN + kernel_lines()
        # What the boot before still printed after this boot's mark is not this boot's end.
        for text in (BOOT_CLEAN, "[  196.601943] reboot: Restarting system\n" + BOOT_CLEAN):
            r, end = self.healthy_end(text)
            self.assertIsInstance(end, console.BootEnd)
            self.assertEqual((end.kind, r.rows), ("login", {}))
        # Any end that is no known flake of the lab fails the check, as SmartConfig's.
        for name, text, said in (
                ("emergency mode on the bad device", up + TIMED_OUT + DEPEND + EMERGENCY_TAIL,
                 "the boot ended in emergency"),
                ("a maintenance prompt with no timeout at all", up + DEPEND + EMERGENCY_TAIL,
                 "the boot ended in emergency"),
                ("the guest powered off", up + "[   31.224031] reboot: Power down\n", "the boot ended in shutdown")):
            r, end = self.healthy_end(text)
            self.assert_fails(r, end, "4.2", "H", name)
            self.assertIn(said, r.rows["4.2"].notes, name)
        # The lab's known flakes: the boot again, and no row.
        slow = "[ TIME ] Timed out waiting for device dev-disk-by\\x2dlabel-BOOT.device - /dev/disk/by-label/BOOT.\n"
        for name, text, sig in (("slow udev", up + slow + EMERGENCY_TAIL, "slow-udev"),
                                ("TCG's panic", up + TCG_PANIC, "panic"),
                                ("a grub> prompt", UEFI_SILENT + "error: no such device: 0b1c.\ngrub> ",
                                 "grub-prompt")):
            r, end = self.healthy_end(text)
            self.assertIsInstance(end, e2e.Retry, name)
            self.assertEqual((end.sig, r.rows), (sig, {}), name)
        # A panic that is not TCG's is nobody's known flake: no retry, and no [M4] row outside the rescue boot.
        r, end = self.healthy_end(up + VFS_PANIC)
        self.assertIsInstance(end, e2e.LabError)
        self.assertEqual((r.rows, "not TCG's" in str(end)), ({}, True))

    def test_wait_for_and_panics(self):
        r = fake_run(self, con=FakeCon(kernel_lines() + TCG_PANIC))
        self.assertEqual(r.wait_for(attempt("1"), [e2e.INIT_RX], 5).index, 0)  # what came before the panic is a hit
        end = outcome_of(r.wait_for, attempt("1"), [e2e.LOGIN_RX], 5)
        self.assertIsInstance(end, e2e.Retry)
        self.assertEqual(end.sig, "panic")
        self.assertIsNone(fake_run(self, con=FakeCon(kernel_lines())).wait_for(attempt("1"), [e2e.LOGIN_RX], 5))
        # The rescue entry's kernel panics (a wrong root=): 42_smartconfig wrote its arguments.
        for label, ctx in (("3", "3.5"), ("1", "1.3"), ("4", "4.2")):
            r = fake_run(self, con=FakeCon(kernel_lines(EXP_RESCUE) + VFS_PANIC))
            r.at(ctx)
            end = outcome_of(r.wait_for, attempt(label, True), [e2e.PROMPT_RX, e2e.LOGIN_RX], 5)
            if label == "3":
                self.assert_fails(r, end, "3.5", "H", "the rescue entry's kernel panics")
                self.assertIn("VFS: Unable to mount root fs", r.rows["3.5"].notes)
                self.assertEqual(r.rows["3.5"].source, "scripts/42_smartconfig")
            else:
                self.assertIsInstance(end, e2e.LabError, label)
                self.assertEqual(r.rows, {}, label)

    def test_vga_menu_wait_panic(self):
        r = fake_run(self, "bios", con=FakeCon(TCG_PANIC))
        FakeClock(self, r)
        r.unexpected = lambda a: None
        a = attempt("3", True, FakeWatch(["SeaBIOS (version 1.16.3-debian-1.16.3-2)"]))
        end = outcome_of(r.vga_menu_wait, a, {}, 600)
        self.assertIsInstance(end, e2e.Retry)
        self.assertEqual(end.sig, "panic")

    def test_wait_ssh(self):
        r = fake_run(self)
        r.machine = argparse.Namespace(ssh_port=1, alive=lambda: True)
        answers(r, {"boot_id": (0, B4 + "\n")})
        a = attempt("4")
        with mock.patch.object(e2e.labvm, "wait_ssh", lambda *args, **kw: ssh_result(0)):
            self.assertEqual((r.wait_ssh(a), a.boot_id), (B4, B4))
        with mock.patch.object(e2e.labvm, "wait_ssh", lambda *args, **kw: None):
            # A login prompt and no ssh under TCG is the lab's known flake; QEMU gone is not.
            end = outcome_of(r.wait_ssh, a)
            self.assertIsInstance(end, e2e.Retry)
            self.assertEqual(end.sig, "no-ssh")
            r.machine.alive = lambda: False
            self.assertIsInstance(outcome_of(r.wait_ssh, a), e2e.LabError)

    def test_grub_prompt_is_retried(self):
        r = fake_run(self, con=FakeCon(UEFI_SILENT + "error: no such device: 0b1c.\ngrub> "))
        for menu in (False, True, None):
            end = outcome_of(r.grub_phase, attempt("1", menu), "1.1", menu)
            self.assertIsInstance(end, e2e.Retry, menu)
            self.assertEqual(end.sig, "grub-prompt", menu)

    def test_a_menu_that_waits_for_a_key(self):
        # The flag is set and GRUB drew its menu, but with no countdown: it
        # waits for a key for ever. That is x.1 failed, not a run to repeat.
        no_countdown = UEFI_MENU.split("   The highlighted")[0]
        r = fake_run(self, con=FakeCon(UEFI_SILENT + PRE_FLAG + "sclab: post timeout=[-1] style=[menu]\n" + no_countdown))
        end = outcome_of(r.grub_phase, attempt("4", True), "4.1", True)
        self.assert_fails(r, end, "4.1", "H", "a menu with no countdown")
        self.assertIn("waits for a key for ever", r.rows["4.1"].notes)
        r = fake_run(self, con=FakeCon(UEFI_SILENT + PRE_FLAG + POST_FLAG))  # GRUB ran, then nothing: whose is not known
        end = outcome_of(r.grub_phase, attempt("4", True), "4.1", True)
        self.assertIsInstance(end, e2e.LabError)
        self.assertEqual(r.rows, {})

    def test_an_entry_that_does_not_boot(self):
        error = "\nerror: file `/vmlinuz-6.8.0-142-generic' not found.\n\nPress any key to continue..."
        for said, kind in ((error, e2e.Stop), ("\n" + e2e.RESCUE_ECHO + "\n", e2e.LabError)):
            con = FakeCon(UEFI_SILENT + PRE_FLAG + POST_FLAG + UEFI_MENU)
            r = fake_run(self, con=con)

            def pick(a, g, target, cid="3.2"):
                g["enter_pos"] = con.size()
                con.t += said

            r.pick = pick
            end = outcome_of(r.grub_phase, attempt("3", True), "3.1", True, e2e.RESCUE_TITLE)
            self.assertIsInstance(end, kind)
            if kind is e2e.Stop:  # GRUB said why: the entry 42_smartconfig wrote is wrong
                self.assert_fails(r, end, "3.2", "H", "GRUB's error after Enter")
                self.assertIn("the entry did not boot: error: file", r.rows["3.2"].notes)
            else:
                self.assertNotIn("3.2", r.rows)

    def test_30(self):
        key = serialmux.Entry(1.0, "serial", "1b5b42", "down")
        for name, boot2_inp, a, status in (
                ("nothing since boot 2's reset", 3, e2e.Attempt("3", 1, Mark(0, 3), None, True), "PASS"),
                ("a key in boot 2", 2, e2e.Attempt("3", 1, Mark(0, 3), None, True), "FAIL"),
                # A retried boot 3: the first attempt's own keys (its pick) came after boot 2's reset.
                ("the keys of the attempt before", 1, e2e.Attempt("3", 2, Mark(0, 3), None, True), "PASS"),
                ("a key since the retried attempt's reset", 1, e2e.Attempt("3", 2, Mark(0, 2), None, True), "FAIL")):
            r = fake_run(self)
            r.mux = FakeMux([key, key, key])
            r.boot2_mark = Mark(0, boot2_inp)
            end = outcome_of(r.check_30, a)
            self.assertEqual(r.status("3.0"), status, name)
            if status == "FAIL":
                self.assertIsInstance(end, e2e.Stop, name)
                self.assertEqual((end.row.cause, e2e.verdict(r.rows.values())), ("lab", "INCONCLUSIVE"), name)

    def test_27(self):
        for by_guest, status in ((False, "PASS"), (True, "FAIL")):
            r = fake_run(self)
            clock = FakeClock(self, r)
            start = clock.now
            r.settle_until = start + 35
            r.reset_vm = lambda why: qmp_event(3, "RESET", by_guest, 100.0)
            end = outcome_of(r.reset_settled)
            self.assertEqual((r.status("2.7"), r.rows["2.7"].cause), (status, "lab"))
            self.assertEqual(end is None, status == "PASS")
            self.assertAlmostEqual(clock.now - start, 35)  # the reset waits for the boot to settle

    def test_k1(self):
        down, esc = serialmux.Entry(1.0, "serial", "1b5b42", "down"), serialmux.Entry(2.0, "serial", "1b", "")
        for inputs, status in (([down, down], "PASS"), ([down, esc], "FAIL")):
            r = fake_run(self)
            r.mux = FakeMux(inputs)
            r.check_k1()  # at the end of the run: recorded, never a Stop
            self.assertEqual((r.status("K.1"), r.rows["K.1"].cause), (status, "lab"))

    def test_a_w_check_that_fails_is_a_warning(self):
        r = fake_run(self)
        r.record("3.8.vga", problems=["'sc restore 1d5b0a' not in the 32 KiB VGA window"])
        r.record("3.3", problems=["not seen between Enter and the kernel"], strength="W")  # bios
        self.assertEqual((r.status("3.8.vga"), r.status("3.3"), e2e.verdict(r.rows.values())), ("WARN", "WARN", "PASS"))


class DoneMachine:
    """The VM as the end of a run sees it: stopped, with what it left."""

    def __init__(self, left=()):
        self.left = list(left)
        self.stops = 0

    def alive(self):
        return False

    def stop(self):
        self.stops += 1
        return 0

    def leftovers(self):
        return list(self.left)


def passing(r, upto=None, faults=None):
    """flow() of a healthy run: every check of boots 0 to 5 passes, up to
    the check upto (left out). faults: {check: its problem}."""
    for cid in e2e.REGISTRY:
        if cid[0] in "PKT" or cid == "0.1":
            continue
        if cid == upto:
            return
        r.at(cid)
        r.record(cid, problems=[faults[cid]] if cid in (faults or {}) else [])


class TestExecute(unittest.TestCase):
    """execute() from preflight to the summary line, with the preflight,
    the VM and the flow stubbed: the verdict, the exit status make reads,
    result.txt and ledger.json."""

    def execute(self, flow=passing, preflight=None, start_vm=None, machine=None, dirty=False, **args):
        r = fake_run(self)
        r.dirty, r.mux = dirty, None  # the mux is start_vm's
        for k, v in args.items():
            setattr(r.args, k, v)
        r.dumps = []
        r.failure_dump = lambda: r.dumps.append(1) or "== facts rc=0\nmode=dump"

        def pre(stack):
            for cid in ("P.1", "P.2", "P.3", "P.4", "P.5"):
                r.at(cid)
                r.record(cid)

        def start():
            r.at("0.1")
            r.machine, r.mux = machine or DoneMachine(), FakeMux()
            r.record("0.1")

        r.preflight = (lambda stack: preflight(r)) if preflight else pre
        r.start_vm = (lambda: start_vm(r)) if start_vm else start
        r.flow = lambda: flow(r)
        out, err = io.StringIO(), io.StringIO()
        # No thread of another test counts as a leftover of this run.
        with mock.patch.object(e2e.labvm, "discard_disks") as discard, \
                mock.patch.object(e2e.threading, "enumerate", lambda: []), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = r.execute()
        r.discards, r.out, r.err = discard.call_count, out.getvalue(), err.getvalue()
        with open(os.path.join(r.run, "result.txt")) as f:
            lines = f.read().split("\n")
        r.header = lines[:2]
        r.result = collections.OrderedDict((x.split("\t")[0], x.split("\t")) for x in lines[3:] if x)
        with open(os.path.join(r.run, "ledger.json")) as f:
            r.ledger = json.load(f)
        return r, rc

    def consistent(self, r, rc, verdict):
        """The exit status, the summary line, result.txt and ledger.json say the same."""
        self.assertEqual(rc, {"PASS": 0, "FAIL": 1, "INCONCLUSIVE": 3}[verdict])
        self.assertEqual(r.out.count("\n"), 1)
        self.assertTrue(r.out.startswith(verdict + " uefi "), r.out)
        self.assertIn("# lab/e2e.py uefi: %s in " % verdict, r.header[0])
        self.assertEqual(r.ledger["verdict"], verdict)
        self.assertEqual(sorted(r.result), sorted(e2e.REGISTRY))  # a row for every check
        recorded = {c["id"]: c for c in r.ledger["checks"]}  # these runs record a check once
        for cid, cells in r.result.items():
            if cid in recorded:
                cls, cause = e2e.REGISTRY[cid].cls, recorded[cid]["cause"]
                self.assertEqual(cells[1:3], [recorded[cid]["status"], cls if cause == cls else "%s(%s)" % (cls, cause)])
            else:  # never recorded: filled in at the end
                self.assertIn(cells[1], ("NOTRUN", "SKIP"), cid)
        self.assertTrue(r._logf.closed)

    def test_pass(self):
        r, rc = self.execute()
        self.consistent(r, rc, "PASS")
        self.assertEqual({cells[1] for cells in r.result.values()}, {"PASS"})
        self.assertEqual((r.discards, r.machine.stops, r.ledger["lab_error"]), (1, 1, None))
        self.assertNotIn("lab error", r.header[1])
        self.assertEqual(r.err, "")
        r, rc = self.execute(keep=True)
        self.assertEqual((rc, r.discards), (0, 0))  # --keep: the disks stay after a PASS too

    def test_an_m4_failure_exits_1(self):
        # F: the mode goes on to its end, every other check passes; the verdict is FAIL all the same.
        r, rc = self.execute(lambda r: passing(r, faults={"1.8": "critical-chain names sc-boot-seen"}))
        self.consistent(r, rc, "FAIL")
        self.assertEqual(r.result["1.8"][1:4], ["FAIL", "M4", "F"])
        self.assertEqual({cells[1] for cid, cells in r.result.items() if cid != "1.8"}, {"PASS"})
        self.assertEqual((r.dumps, r.discards), ([1], 0))  # the guest's state is kept and shown
        self.assertIn("failing: 1.8\tFAIL", r.err)
        self.assertIn("-- facts.sh dump --\n== facts rc=0", r.err)

    def test_an_m5_failure_exits_1(self):
        # The M5 review (B2): a broken package (D.2 finds the rescue entry
        # still in grub.cfg) is SmartConfig's FAIL, not INCONCLUSIVE.
        r, rc = self.execute(lambda r: passing(r, faults={"D.2": "rescue=1, not 0"}), deb=True)
        self.consistent(r, rc, "FAIL")
        self.assertEqual(r.result["D.2"][1:4], ["FAIL", "M5", "H"])

    def test_an_m5_failure_stays_after_a_lab_error(self):
        def flow(r):
            passing(r, "D.4")
            r.at("D.4")
            r.record("D.4", problems=["2 rescue entries in grub.cfg"], stop=False)
            raise e2e.LabError("ssh failed (exit 255)")
        r, rc = self.execute(flow, deb=True)
        self.assertEqual((rc, r.result["D.4"][1:3]), (1, ["FAIL", "M5"]))

    def test_a_stop_exits_1_and_leaves_the_rest_notrun(self):
        r, rc = self.execute(lambda r: passing(r, faults={"1.2": "Command line differs"}))  # H: the mode stops there
        self.consistent(r, rc, "FAIL")
        self.assertEqual((r.result["1.1"][1], r.result["1.2"][1], r.result["1.3"][1], r.result["5.2"][1]),
                         ("PASS", "FAIL", "NOTRUN", "NOTRUN"))
        self.assertEqual((r.result["K.1"][1], r.result["T.1"][1]), ("PASS", "PASS"))  # the teardown still ran
        self.assertIsNone(r.ledger["lab_error"])
        self.assertTrue(any("stopped: 1.2" in x for x in r.logged))

    def test_checks_never_reached_are_inconclusive(self):
        # Every check that ran passed, but the flow ended early: not a PASS.
        r, rc = self.execute(lambda r: passing(r, "5.1"))
        self.consistent(r, rc, "INCONCLUSIVE")
        notrun = [c for c, cells in r.result.items() if cells[1] == "NOTRUN"]
        self.assertEqual(notrun, ["5.1", "5.1.ok", "5.1.notime", "5.2", "6.1", "6.2", "6.3", "6.4", "6.5", "D.1", "D.2", "D.3", "D.4", "D.5"])
        self.assertEqual(r.result["5.1"][8], "not reached")
        self.assertEqual(r.discards, 0)
        self.assertIn("failing: 5.1\tNOTRUN", r.err)

    def test_a_lab_error_exits_3(self):
        def flow(r):
            passing(r, "2.3")
            r.at("2.3")
            raise e2e.LabError("QEMU exited (status 1), while waiting for the bad device's timeout")

        r, rc = self.execute(flow)
        self.consistent(r, rc, "INCONCLUSIVE")
        self.assertEqual(r.result["2.3"][1:3], ["FAIL", "M4(lab)"])  # an [M4] check the lab could not finish
        self.assertIn("[lab] QEMU exited (status 1)", r.result["2.3"][9])
        self.assertIn("QEMU exited (status 1)", r.ledger["lab_error"])
        self.assertIn("; lab error: QEMU exited (status 1)", r.header[1])
        self.assertEqual((r.dumps, r.discards), ([1], 0))

    def test_a_lab_error_lands_on_the_next_check_when_the_current_one_passed(self):
        def flow(r):
            passing(r, "1.3")  # 1.2 passed, then the wait for 1.3's login prompt ended the run
            raise e2e.LabError("no login prompt 900 s after the kernel")

        r, rc = self.execute(flow)
        self.consistent(r, rc, "INCONCLUSIVE")
        self.assertEqual((r.result["1.2"][1], r.result["1.3"][1:3]), ("PASS", ["FAIL", "M4(lab)"]))

    def test_an_m4_failure_is_not_replaced_by_a_lab_error(self):
        def flow(r):
            passing(r, "E.4")
            r.at("E.4")
            r.record("E.4", problems=["sc check exit 0"])  # F: the mode goes on
            raise e2e.LabError("ssh failed (exit 255): sc status")

        r, rc = self.execute(flow)
        self.assertEqual(rc, 1)  # SmartConfig's failure wins over the lab's
        self.assertEqual(r.result["E.4"][1:3], ["FAIL", "M4"])
        self.assertEqual((r.ledger["verdict"], "ssh failed" in r.ledger["lab_error"]), ("FAIL", True))
        self.assertTrue(r.out.startswith("FAIL uefi "))
        self.assertEqual([c["cause"] for c in r.ledger["checks"] if c["id"] == "E.4"], ["M4", "lab"])  # both on record

    def test_anything_else_exits_3(self):
        def retry(r):
            passing(r, "1.1")
            raise e2e.Retry("panic", "outside a boot")

        def bug(r):
            passing(r, "1.1")
            raise KeyError("GOOD")

        for flow, said in ((retry, "a retry outside a boot: panic"), (bug, "e2e.py: KeyError('GOOD')")):
            r, rc = self.execute(flow)
            self.consistent(r, rc, "INCONCLUSIVE")
            self.assertIn(said, r.ledger["lab_error"])
            self.assertEqual((r.result["1.0"][1], r.result["1.1"][1:3]), ("PASS", ["FAIL", "M4(lab)"]))
            self.assertEqual(r.dumps, [1])

    def test_no_dump_after_an_interrupt(self):
        def ctrl_c(r):
            passing(r, "3.5")
            raise KeyboardInterrupt()

        def sigterm(r):
            passing(r, "3.5")
            raise SystemExit(143)

        for flow, said in ((ctrl_c, "interrupted (KeyboardInterrupt)"), (sigterm, "interrupted (signal, exit 143)")):
            r, rc = self.execute(flow)
            self.consistent(r, rc, "INCONCLUSIVE")
            self.assertEqual(r.ledger["lab_error"], said)
            # Stop now: nothing is asked of the guest first; QEMU is stopped and T.1 says what is left.
            self.assertEqual((r.dumps, r.machine.stops, r.result["T.1"][1]), ([], 1, "PASS"))
            self.assertNotIn("facts.sh dump", r.err)

    def test_qemu_never_started(self):
        def preflight(r):
            r.record("P.1")
            r.at("P.2")
            r.record("P.2", problems=["bin/sc is not this tree's build: make build"])

        def start_vm(r):
            raise AssertionError("QEMU started after a failed preflight")

        r, rc = self.execute(preflight=preflight, start_vm=start_vm)
        self.consistent(r, rc, "INCONCLUSIVE")
        self.assertEqual((r.result["P.2"][1:3], r.ledger["lab_error"], r.machine), (["FAIL", "lab"], None, None))
        self.assertEqual(r.result["K.1"][1::7], ["SKIP", "QEMU never started"])
        self.assertEqual(r.result["T.1"][1::7], ["SKIP", "QEMU never started"])
        self.assertEqual({cells[1] for c, cells in r.result.items() if c[0] not in "PKT"}, {"NOTRUN"})
        self.assertEqual(r.dumps, [1])  # asked for; with no machine there is nothing to ask (test_failure_dump)

    def test_qemu_started_and_the_mux_did_not(self):
        m = DoneMachine()

        def start_vm(r):
            r.at("0.1")
            r.machine = m
            raise e2e.LabError("serial.sock: no connection in 30 s")

        r, rc = self.execute(start_vm=start_vm)
        self.consistent(r, rc, "INCONCLUSIVE")
        # K.1 could not be checked: with a QEMU that ran, that is not a SKIP.
        self.assertEqual([r.result[c][1] for c in ("0.1", "K.1", "T.1")], ["FAIL", "NOTRUN", "PASS"])
        self.assertEqual(m.stops, 1)

    def test_what_the_run_left_behind(self):
        left = ["qemu-system-x86_64 pid 4242 still runs", "port 53691 still listens"]
        r, rc = self.execute(machine=DoneMachine(left))
        self.consistent(r, rc, "INCONCLUSIVE")  # every boot passed; the lab did not clean up
        self.assertEqual(r.result["T.1"][1:3], ["FAIL", "lab"])
        self.assertIn("pid 4242", r.result["T.1"][9])
        self.assertEqual(r.discards, 0)

    def test_a_teardown_that_fails(self):
        class Stuck(DoneMachine):
            def stop(self):
                raise OSError("QMP socket: Broken pipe")

        r, rc = self.execute(machine=Stuck())
        self.consistent(r, rc, "INCONCLUSIVE")
        self.assertEqual(r.result["T.1"][1:3], ["FAIL", "lab"])
        self.assertIn("teardown: OSError", r.ledger["lab_error"])

    def test_a_lone_esc_is_inconclusive(self):
        def flow(r):
            r.mux = FakeMux([serialmux.Entry(2.0, "serial", "1b", "")])
            passing(r)

        r, rc = self.execute(flow)
        self.consistent(r, rc, "INCONCLUSIVE")
        self.assertEqual(r.result["K.1"][1:3], ["FAIL", "lab"])

    def test_dirty_is_said(self):
        for dirty, word in ((True, "dirty=yes"), (False, "dirty=no")):
            r, rc = self.execute(dirty=dirty)
            self.assertEqual(rc, 0)
            self.assertIn(" %s " % word, r.out)
            self.assertIn(" %s " % word, r.header[1])
            self.assertIs(r.ledger["dirty"], dirty)

    def test_forced_is_said(self):
        def flow(r):
            r.v["outcome"] = "a"
            passing(r)

        self.assertIn(" boot2=a(forced) ", self.execute(flow, boot2="reset-at-timeout")[0].out)
        self.assertIn(" boot2=a goal3=early-only ", self.execute(flow)[0].out)


class TestFailureDump(unittest.TestCase):
    """What a failed run asks of the guest, and for how long: the
    teardown (T.1, result.txt) must fit before make's timeout."""

    def dump_run(self, **kw):
        r = fake_run(self, **kw)
        self.clock = FakeClock(self, r)
        r.machine = argparse.Namespace(alive=lambda: True, ssh_port=1)
        r.staged = True
        r.t_start = self.clock.now
        self.timeouts = []

        def ssh(port, key, command, timeout, input=None):
            self.timeouts.append(timeout)
            out = "== facts rc=0\nmode=dump\n" if "facts.sh dump" in command else ""
            return labvm.Result([], 0, out, "", False, 0.1)

        for name, fn in (("port_open", lambda port: True), ("ssh", ssh)):
            p = mock.patch.object(e2e.labvm, name, fn)
            p.start()
            self.addCleanup(p.stop)
        return r

    def test_over_ssh_within_the_modes_time(self):
        r = self.dump_run()
        mode = r.b("BUDGET_MODE")
        for used, want in ((0, [20, 300]), (mode - 45 - 100, [20, 100]), (mode - 45 - 5, [5, 5]), (mode + 600, [1, 1])):
            self.timeouts[:] = []
            self.clock.now = r.t_start + used
            self.assertIn("mode=dump", r.failure_dump())
            self.assertEqual([round(t, 3) for t in self.timeouts], want, used)
        with open(r.evpath("facts-dump.txt")) as f:
            self.assertIn("mode=dump", f.read())

    def test_no_way_in(self):
        r = self.dump_run()
        r.staged = False  # facts.sh never reached the guest
        self.assertIsNone(r.failure_dump())
        r.staged, r.machine.alive = True, lambda: False
        self.assertIsNone(r.failure_dump())
        r.machine = None
        self.assertIsNone(r.failure_dump())
        self.assertEqual((self.timeouts, os.listdir(r.evdir)), ([], []))

    def test_at_the_rescue_prompt(self):
        # Stopped at boot 3's prompt: Enter, as 3.7 would have, then facts.sh on the console.
        con = FakeCon(typed={"\r": "\nroot@sclab:~# "}, cmds={"sh ": (0, "== facts rc=0\nmode=dump\n")})
        r = self.dump_run(con=con)
        r.at_prompt = True
        self.assertIn("mode=dump", r.failure_dump())
        self.assertEqual((con.sent, r.shell, self.timeouts), (["\r"], True, []))


class TestPreflight(unittest.TestCase):
    """P.1 to P.5 with the host's tools, the build, the lock and the cache
    stubbed: what must stop a run before QEMU starts."""

    def preflight(self, mode="uefi", dirty=False, staged="a" * 64, ref_mode=0o444, made_under=None, missing=(), **env):
        r = fake_run(self, mode)
        r.dirty, r.cache = dirty, r.tmp
        code = os.path.join(r.tmp, "OVMF_CODE_4M.fd")
        with open(code, "wb") as f:
            f.write(b"this firmware")
        r.conf["OVMF_CODE"] = r.conf["OVMF_VARS"] = code
        ref = {"h12": "fcb2359f531b"}
        for k in ("qcow2", "vars", "json"):
            ref[k] = os.path.join(r.tmp, "ref-fcb2359f531b." + k)
            with open(ref[k], "w") as f:
                json.dump({"ovmf_code_sha256": made_under or labvm.sha256_file(code)}, f)
            os.chmod(ref[k], ref_mode)
        r.check_p2 = lambda: (r.v.__setitem__("sc_fresh_sha256", "a" * 64), r.record("P.2"))
        r.stage_files = lambda: r.sha.__setitem__("sc", staged)
        environ = {k: v for k, v in os.environ.items() if k != "LAB_REQUIRE_CLEAN"}
        environ.update(env)
        with mock.patch.object(e2e.shutil, "which", lambda tool: None if tool in missing else "/usr/bin/" + tool), \
                mock.patch.object(e2e.labvm, "lab_lock", lambda cache: contextlib.nullcontext()), \
                mock.patch.object(e2e.labvm, "check_image", lambda conf, cache: None), \
                mock.patch.object(e2e.labvm, "find_ref", lambda conf, cache: ref), \
                mock.patch.object(e2e.labvm, "key_paths", lambda cache: (os.path.join(cache, "key"), "")), \
                mock.patch.dict(os.environ, environ, clear=True), contextlib.ExitStack() as stack:
            end = outcome_of(r.preflight, stack)
        return r, end

    def stopped_at(self, cid, said, **kw):
        r, end = self.preflight(**kw)
        self.assertIsInstance(end, e2e.Stop, kw)
        self.assertEqual((end.row.id, end.row.cause), (cid, "lab"), kw)
        self.assertIn(said, r.rows[cid].notes)
        self.assertEqual(e2e.verdict(r.rows.values()), "INCONCLUSIVE")

    def test_good(self):
        for mode in ("uefi", "bios"):
            r, end = self.preflight(mode)
            self.assertIsNone(end)
            self.assertEqual([r.status(c) for c in ("P.1", "P.2", "P.3", "P.4", "P.5")], ["PASS"] * 5)
            self.assertEqual((r.v["head"], r.key), (r.head, os.path.join(r.tmp, "key")))
            self.assertIn("dirty=no", r.rows["P.3"].seen)

    def test_p1_a_tool_is_missing(self):
        self.stopped_at("P.1", "missing: go", missing=("go",))

    def test_p3_the_staged_sc_is_the_build_p2_checked(self):
        self.stopped_at("P.3", "is not the build P.2 checked", staged="b" * 64)

    def test_p3_a_dirty_tree(self):
        r, end = self.preflight(dirty=True)
        self.assertIsNone(end)  # allowed, and said: the summary line has dirty=yes
        self.assertIn("dirty=yes", r.rows["P.3"].seen)
        self.stopped_at("P.3", "the tree is dirty and LAB_REQUIRE_CLEAN=1", dirty=True, LAB_REQUIRE_CLEAN="1")
        self.assertIsNone(self.preflight(LAB_REQUIRE_CLEAN="1")[1])
        self.assertIsNone(self.preflight(dirty=True, LAB_REQUIRE_CLEAN="0")[1])

    def test_p5_the_reference_image_is_read_only(self):
        self.stopped_at("P.5", "is 644, not 444", ref_mode=0o644)

    def test_p5_the_firmware_the_image_was_made_under(self):
        self.stopped_at("P.5", "make lab-image", made_under="0" * 64)
        self.assertIsNone(self.preflight("bios", made_under="0" * 64)[1])  # bios has no OVMF

    def test_ovmf_problems(self):
        d = tempfile.mkdtemp(prefix="sclab-test-")
        self.addCleanup(shutil.rmtree, d, True)
        code, ref = os.path.join(d, "OVMF_CODE_4M.fd"), os.path.join(d, "ref.json")
        with open(code, "wb") as f:
            f.write(b"this firmware")
        sha = labvm.sha256_file(code)
        for text, said in ((json.dumps({"ovmf_code_sha256": sha}), None),
                           (json.dumps({"ovmf_code_sha256": "0" * 64}),
                            "the reference image was made under 000000000000"),
                           (json.dumps({"image_sha256": "1" * 64}), None),  # an image from before the json had it
                           ("{not json", "ref.json")):
            with open(ref, "w") as f:
                f.write(text)
            problems = e2e.ovmf_problems(ref, code)
            self.assertEqual(len(problems), 1 if said else 0, text)
            if said:
                self.assertIn(said, problems[0])
        os.unlink(ref)
        self.assertEqual(len(e2e.ovmf_problems(ref, code)), 1)  # no json is not "nothing to compare"


if __name__ == "__main__":
    unittest.main()
