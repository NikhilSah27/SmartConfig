"""Unit tests for lab/e2e.py's pure parts (no QEMU, no network): the check
registry, the verdict, what it reads from facts.sh, install.sh, grub.cfg,
the boots file, sc log and the rescue report, and its command line.

  python3 -m unittest discover -s lab

The flow itself needs the VM: make lab-e2e.
"""
import argparse
import collections
import contextlib
import io
import json
import os
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
            for sig in ("panic", "grub-prompt", "menu-missed"):
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
/usr/local/sbin/sc file 755 root:root abc
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
        self.assertEqual(p["/usr/local/sbin/sc"], "file 755 root:root abc")
        d = e2e.show_units(f.lines("dropins"))
        self.assertIn("/etc/systemd/system/rescue.service.d/50-smartconfig.conf", d["rescue.service"]["DropInPaths"])
        self.assertIn("emergency.service", d)

    def test_install_facts(self):
        out = ("uid=0\nmanifest=ok\nmanifest_out=sc: OK\nfile=/usr/local/sbin/sc 755 root:root aa\n"
               "update_grub_rc=0\nupdate_grub_err=Sourcing file `/etc/default/grub'\n"
               "update_grub_err=Adding SmartConfig rescue entry: /boot/vmlinuz-6.8.0-142-generic\n"
               "verify_rc=0\ndone=1\n")
        kv = e2e.parse_kv(out)
        self.assertEqual(kv["update_grub_err"][1], "Adding SmartConfig rescue entry: /boot/vmlinuz-6.8.0-142-generic")
        self.assertEqual(kv["file"], ["/usr/local/sbin/sc 755 root:root aa"])
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
c7146c 12:03  blocker fstab-source-missing, line 4  /etc/fstab

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
    str offsets); cmd() answers by the command's first word(s)."""

    def __init__(self, text="", cmds=None):
        self.t = text
        self.cmds = cmds or {}

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


class FakeMux:
    def __init__(self, inputs=()):
        self._inputs = list(inputs)

    def inputs(self, start=0):
        return self._inputs[start:]


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
        self.args = argparse.Namespace(mode=mode, boot2="natural", no_boot5=False, keep=False)
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

    def log(self, msg):
        self.logged.append(msg)

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


def rescue_facts(boots, mounts=MOUNTS, before=HASHES, after=HASHES):
    return facts_text(
        ("facts", 0, ["mode=rescue", "good=" + GOOD, "bad=" + BAD]),
        ("boot-id", 0, [B3]),
        ("mounts", 0, list(mounts)),
        ("active", 0, ["local-fs.target=inactive", "rescue.target=active", "emergency.target=inactive"]),
        ("show:sc-boot-seen.service", 0, ["Id=sc-boot-seen.service", "ConditionResult=no"]),
        ("show:sc-boot-ok.service", 0, ["Id=sc-boot-ok.service", "ExecMainStartTimestampMonotonic=0"]),
        ("boots", 0, boots.strip().split("\n")),
        ("sc-status-console", 2, ["  sc restore " + GOOD]),
        ("sc-diff", 1, ["+UUID=3f6c1e2a-9b7d-4c1e-8f2a-5d6e7f8a9b0c /mnt/backup ext4 defaults 0 2"]),
        ("sc-cat-good", 0, [FSTAB_SHA]),
        ("sc-check", 2, ["/etc/fstab: blocker fstab-source-missing, line 4"]),
        ("sc-restore", 1, ["sc: store /var/lib/smartconfig: " + e2e.RESTORE_REFUSED]),
        ("hashes-before", 0, list(before)),
        ("hashes-after", 0, list(after)),
        ("leftovers", 0, []),
        ("vcs1", 0, ["  sc restore " + GOOD]))


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
    """facts.sh normal blocks for checks 0.7, 1.5, 1.6, 1.8, 4.4, 4.6:
    (name, rc, lines), each replaceable (None: left out)."""
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
        ("critical-chain", (0, ["multi-user.target @21.6s", "└─getty.target @21.5s"])),
    ))
    blocks.update(over)
    return e2e.Facts(facts_text(*[(n, b[0], b[1]) for n, b in blocks.items() if b is not None]), "evidence/f.txt")


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
                            (["next_entry="], "PASS"), (["next_entry=2"], "FAIL"), (["recordfail=1"], "PASS")):
            self.assertEqual(self.run_check("4.4", normal_facts(grubenv=(0, env))).status, status, env)

    def test_18_critical_chain_unread(self):
        for chain in ((1, ["Failed to get ID: Connection timed out"]), (0, []), None):
            row = self.run_check("1.8", normal_facts(**{"critical-chain": chain}))
            self.assertEqual((row.status, row.strength), ("FAIL", "F"), chain)
        row = self.run_check("1.8", normal_facts(**{"critical-chain": (0, ["multi-user.target @9s",
                                                                           "└─sc-boot-seen.service @1s"])}))
        self.assertIn("names sc-boot-seen", row.notes)

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
        r.flow()
        five = [c for c in e2e.REGISTRY if c.startswith("5.")]
        self.assertEqual(len(five), 4)
        self.assertEqual([r.status(c) for c in five], ["SKIP"] * 4)


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
echo "PASS $m 0m01s boot2=b goal3=multi-user boot5=${FAKE_B5:-$b5} retries=0 head=x dirty=no sc=y"
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


def qmp_event(seq, name, guest, secs):
    return {"seq": seq, "event": name, "data": {"guest": guest, "reason": "guest-reset" if guest else "host"},
            "timestamp": {"seconds": int(secs), "microseconds": int(round((secs % 1) * 1e6))}, "t": secs}


class FakeQmp:
    """QMP's event list as settle_reset reads it: wait_event returns at
    once (the events are all there already)."""

    def __init__(self, events):
        self.events = events
        self.waits = []

    def wait_event(self, names, since=0, timeout=None, abort=None):
        self.waits.append((since, timeout))
        for ev in self.events[since:]:
            if ev["event"] in names:
                return ev
        return None


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

    def boot_at(self, mode, events, first):
        r = fake_run(self, mode)
        r.qmp, r.machine = FakeQmp(events), FakeMachine()
        r.marks = {e["seq"]: Mark(10 * e["seq"], e["seq"]) for e in events}
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
        self.assertEqual(r.qmp.waits, [(1, e2e.RESET_CHAIN)])

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

        def ssh(command, timeout, input=None):
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

    def report_row(self, text, outcome):
        r = fake_run(self, "bios", con=FakeCon(text), GOOD=GOOD, BAD="7b76ec", B1=B1, N=4)
        r.check_emergency_report(attempt("2"), outcome, B2)
        return r.rows["2.5"]

    def test_25_hung_up_is_w(self):
        text = fixture("bios-getty-hangup.txt").decode().replace("sc: interrupted by hangup\n", "")
        row = self.report_row(text, "a")
        self.assertEqual((row.status, row.strength), ("WARN", "W"))
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
        self.assertEqual(self.report_row(text, "c").strength, "W")

    def test_26_reads_through_ssh_only(self):
        r = fake_run(self)
        r.sudo = lambda command, timeout=None: ssh_result(255, err="kex_exchange_identification: Connection reset by peer")
        self.assertEqual(r.boots_text(), "")  # a poll goes on
        with self.assertRaises(e2e.LabError):
            r.boots_text(strict=True)
        r.sudo = lambda command, timeout=None: ssh_result(1, err="cat: /var/lib/smartconfig/boots: No such file")
        self.assertEqual(r.boots_text(strict=True), "")  # no file is the guest's answer, not ssh's

    def run_26(self, boots, env_rc=0, sync_rc=0):
        r = fake_run(self)
        r.poll = lambda fn, budget, every=5.0: fn()
        r.boots_text = lambda strict=False: boots
        answers = {"grub-editenv": ssh_result(env_rc, "smartconfig_pending=1\nrecordfail=1\n" if env_rc == 0 else ""),
                   "sync": ssh_result(sync_rc)}
        r.sudo = lambda command, timeout=None: answers[command.split()[0]]
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


if __name__ == "__main__":
    unittest.main()
