"""Unit tests for lab/incidents.py, M6's incident format
(docs/M6_PLAN.md, section 3). No QEMU, no guest: the edits run on
fixture text, and the files are written to a temporary directory.

  python3 -m unittest discover -s lab -p 'test_incidents.py'
"""
import contextlib
import io
import json
import os
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)
sys.dont_write_bytecode = True

import incidents  # noqa: E402
from incidents import IncidentError  # noqa: E402

FACTS = {"root_uuid": "0d4f9a1c-2b3e-4f5a-8c7d-9e0f1a2b3c4d", "nic": "enp0s2",
         "admin": "owner", "kernel": "6.8.0-142-generic"}
FSTAB = (b"# /etc/fstab: static file system information.\n"
         b"LABEL=cloudimg-rootfs\t/\text4\tdiscard,commit=30,errors=remount-ro\t0 1\n"
         b"LABEL=BOOT\t/boot\text4\tdefaults\t0 2\n"
         b"LABEL=UEFI\t/boot/efi\tvfat\tumask=0077\t0 1\n")


def incident(**over):
    inc = {
        "id": "fstab-uuid-one-char",
        "path": "/etc/fstab",
        "change": [{"append": "UUID={root_uuid~1} /data ext4 defaults 0 2"}],
        "applies": "fstab-now",
        "expect": [{"run": "findmnt -n /data", "exit": 0, "as_written": "a mount on /data"}],
        "cause": "A disk's UUID with one character wrong, on a line without nofail.",
        "fix": "restore",
        "guess": "blocker",
        "source": "the journey film; the M3 owner scenario",
    }
    inc.update(over)
    return {k: v for k, v in inc.items() if v is not None}


def plan(edits, base=FSTAB):
    return incidents.plan_change(edits, base, FACTS)


class TestContentEdits(unittest.TestCase):
    def test_append_adds_a_line_with_the_facts(self):
        a = plan([{"append": "UUID={root_uuid~1} /data ext4 defaults 0 2"}])
        self.assertEqual(a.kind, "content")
        self.assertEqual(a.content, FSTAB + b"UUID=0d4f9a1c-2b3e-4f5a-8c7d-9e0f1a2b3c4e /data ext4 defaults 0 2\n")

    def test_append_to_a_file_without_a_final_newline(self):
        a = plan([{"append": "x"}], base=b"a\nb")
        self.assertEqual(a.content, b"a\nb\nx\n")

    def test_append_keeps_a_newline_the_text_has(self):
        self.assertEqual(plan([{"append": "x\n"}], base=b"a\n").content, b"a\nx\n")

    def test_insert_before_the_first_line_that_matches(self):
        a = plan([{"insert": {"before": r"^LABEL=BOOT", "text": "tmpfs /tmp tmpfs defaults 0 0"}}])
        lines = a.content.decode().splitlines()
        self.assertEqual(lines[2], "tmpfs /tmp tmpfs defaults 0 0")
        self.assertTrue(lines[3].startswith("LABEL=BOOT"))

    def test_insert_with_no_match_is_refused(self):
        with self.assertRaisesRegex(IncidentError, "insert: no line matches"):
            plan([{"insert": {"before": "^nothing", "text": "x"}}])

    def test_replace_every_match_or_count(self):
        base = b"a=1\nb=1\nc=1\n"
        self.assertEqual(plan([{"replace": {"pattern": "=1", "with": "=2"}}], base).content, b"a=2\nb=2\nc=2\n")
        self.assertEqual(plan([{"replace": {"pattern": "=1", "with": "=2", "count": 1}}], base).content,
                         b"a=2\nb=1\nc=1\n")

    def test_replace_is_per_line_anchored(self):
        a = plan([{"replace": {"pattern": r"^LABEL=BOOT\t/boot\text4\tdefaults", "with": r"LABEL=BOOT\t/boot\text4\tdefautls"}}])
        self.assertIn(b"LABEL=BOOT\t/boot\text4\tdefautls\t0 2\n", a.content)

    def test_replace_backreference_but_not_in_a_value(self):
        a = plan([{"replace": {"pattern": r"^(LABEL=BOOT)", "with": r"#\1"}}])
        self.assertIn(b"#LABEL=BOOT\t", a.content)
        facts = dict(FACTS, admin="a\\1b")
        a = incidents.plan_change([{"replace": {"pattern": "^x$", "with": "{admin}"}}], b"x\n", facts)
        self.assertEqual(a.content, b"a\\1b\n")

    def test_replace_pattern_escapes_a_fact(self):
        facts = dict(FACTS, nic="en.0")
        a = incidents.plan_change([{"replace": {"pattern": "{nic}", "with": "eth9"}}], b"enX0 en.0\n", facts)
        self.assertEqual(a.content, b"enX0 eth9\n")

    def test_replace_with_no_match_is_refused(self):
        with self.assertRaisesRegex(IncidentError, "replace: nothing matches"):
            plan([{"replace": {"pattern": "^nothing", "with": "x"}}])

    def test_delete_every_line_that_matches(self):
        a = plan([{"delete": r"^LABEL=(BOOT|UEFI)"}])
        self.assertEqual(a.content.count(b"\n"), 2)
        self.assertNotIn(b"/boot", a.content)

    def test_delete_with_no_match_is_refused(self):
        with self.assertRaisesRegex(IncidentError, "delete: no line matches"):
            plan([{"delete": "^nothing"}])

    def test_write_the_whole_content_crlf_and_bom_kept(self):
        self.assertEqual(plan([{"write": "﻿a\r\nb\r\n"}]).content, "﻿a\r\nb\r\n".encode())
        self.assertEqual(plan([{"write": ""}]).content, b"")

    def test_lines_match_without_their_crlf(self):
        a = plan([{"delete": "^b$"}], base=b"a\r\nb\r\nc\r\n")
        self.assertEqual(a.content, b"a\r\nc\r\n")

    def test_edits_in_order(self):
        a = plan([{"delete": "^LABEL=UEFI"}, {"append": "# end"}])
        self.assertTrue(a.content.endswith(b"defaults\t0 2\n# end\n"))

    def test_bytes_that_are_not_utf8_survive(self):
        a = plan([{"append": "x"}], base=b"\xff\xfe\n")
        self.assertEqual(a.content, b"\xff\xfe\nx\n")

    def test_a_change_that_changes_nothing_is_refused(self):
        with self.assertRaisesRegex(IncidentError, "leaves the file as it was"):
            plan([{"replace": {"pattern": "defaults", "with": "defaults"}}])

    def test_a_content_edit_needs_a_file(self):
        with self.assertRaisesRegex(IncidentError, "no file to change"):
            plan([{"append": "x"}], base=None)


class TestOtherKinds(unittest.TestCase):
    def test_create(self):
        a = plan([{"create": "Defaults:{admin} !authenticate\n"}], base=None)
        self.assertEqual((a.kind, a.content, a.mode), ("create", b"Defaults:owner !authenticate\n", None))
        a = plan([{"create": {"content": "", "mode": "0440"}}], base=None)
        self.assertEqual((a.content, a.mode), (b"", 0o440))

    def test_create_over_a_file_is_refused(self):
        with self.assertRaisesRegex(IncidentError, "there already"):
            plan([{"create": "x"}])

    def test_mode_owner_symlink_unlink(self):
        self.assertEqual(plan([{"mode": "0666"}]), ("mode", None, 0o666, None, None))
        self.assertEqual(plan([{"mode": "644"}]).mode, 0o644)
        self.assertEqual(plan([{"owner": "{admin}:root"}]).owner, "owner:root")
        self.assertEqual(plan([{"symlink": "/dev/null"}]).target, "/dev/null")
        self.assertEqual(plan([{"unlink": True}]).kind, "unlink")
        for edits in ([{"mode": "0666"}], [{"owner": "root:root"}], [{"unlink": True}]):
            with self.assertRaisesRegex(IncidentError, "no file to change"):
                plan(edits, base=None)

    def test_a_symlink_may_make_a_new_path(self):
        # Masking a unit links a path in /etc/systemd/system that was not there.
        self.assertEqual(plan([{"symlink": "/dev/null"}], base=None), ("symlink", None, None, None, "/dev/null"))

    def test_a_single_change_is_never_mixed(self):
        for other in ({"mode": "0600"}, {"append": "x"}):
            with self.assertRaisesRegex(IncidentError, "a change of its own"):
                plan([{"mode": "0666"}, other])

    def test_change_kind(self):
        self.assertEqual(incidents.change_kind([{"append": "x"}, {"delete": "y"}]), "content")
        for op, v in (("create", "x"), ("mode", "0600"), ("owner", "a:b"), ("symlink", "/x"), ("unlink", True)):
            self.assertEqual(incidents.change_kind([{op: v}]), op)


class TestPlaceholders(unittest.TestCase):
    def test_facts(self):
        self.assertEqual(incidents.substitute("{nic} {admin} {kernel}", FACTS), "enp0s2 owner 6.8.0-142-generic")

    def test_uuid_off_by_n(self):
        u = FACTS["root_uuid"]
        one = incidents.substitute("{root_uuid~1}", FACTS)
        self.assertEqual(sum(a != b for a, b in zip(u, one)), 1)
        self.assertEqual(one[-1], "e")
        two = incidents.substitute("{root_uuid~2}", FACTS)
        self.assertEqual(two[-2:], "5e")  # each of the last two digits moved on
        self.assertEqual(incidents.substitute("{root_uuid~1}", dict(FACTS, root_uuid="AF")), "A0")
        self.assertEqual(incidents.substitute("{root_uuid~1}", dict(FACTS, root_uuid="1f-")), "10-")

    def test_other_braces_are_left_alone(self):
        rule = 'SUBSYSTEM=="net", ATTR{address}=="52:54:00:12:34:56", NAME="eth9"'
        self.assertEqual(incidents.substitute(rule, FACTS), rule)

    def test_a_missing_fact(self):
        with self.assertRaisesRegex(IncidentError, "no nic"):
            incidents.substitute("{nic}", {"admin": "owner"})

    def test_unknown_or_misused_placeholders_are_refused_at_load(self):
        with self.assertRaisesRegex(IncidentError, r"unknown placeholder \{rootuuid~"):
            incidents.check(incident(change=[{"append": "UUID={rootuuid~1} /x ext4 defaults 0 2"}]))
        with self.assertRaisesRegex(IncidentError, r"only \{root_uuid\} takes ~N"):
            incidents.check(incident(change=[{"append": "{nic~1}"}]))


class TestCheck(unittest.TestCase):
    def assertRefused(self, regex, **over):
        with self.assertRaisesRegex(IncidentError, regex):
            incidents.check(incident(**over))

    def test_the_plan_example_is_good(self):
        self.assertEqual(incidents.check(incident()), "content")

    def test_fields(self):
        inc = incident()
        del inc["cause"]
        with self.assertRaisesRegex(IncidentError, "missing cause"):
            incidents.check(inc)
        self.assertRefused("unknown field label", label="x")
        with self.assertRaisesRegex(IncidentError, "must be a JSON object"):
            incidents.check([])

    def test_id_and_path(self):
        self.assertRefused(r"\.id: must be", id="Fstab_1")
        for p in ("etc/fstab", "/", "/etc/../etc/fstab", "/etc//fstab", "/etc/fstab/", 7):
            self.assertRefused(r"\.path: must be", path=p)

    def test_applies(self):
        for a in ("sudo", "boot", "socket:ssh", "restart:ssh.service", "restart:serial-getty@ttyS0.service"):
            incidents.check(incident(applies=a))
        for a in ("reboot", "restart:", "restart:ssh", "restart:a b.service", ""):
            self.assertRefused(r"\.applies: must be", applies=a)

    def test_edits(self):
        self.assertRefused("non-empty list of edits", change=[])
        self.assertRefused("one key", change=[{"append": "x", "delete": "y"}])
        self.assertRefused("unknown edit", change=[{"chmod": "0666"}])
        self.assertRefused("not a regular expression", change=[{"delete": "(unclosed"}])
        self.assertRefused('"before": REGEX', change=[{"insert": {"text": "x"}}])
        self.assertRefused(r'\.count: must be', change=[{"replace": {"pattern": "a", "with": "b", "count": 0}}])
        self.assertRefused("octal mode", change=[{"mode": 666}])
        self.assertRefused("octal mode", change=[{"mode": "0999"}])
        self.assertRefused('"USER:GROUP"', change=[{"owner": "root"}])
        self.assertRefused("must be true", change=[{"unlink": "yes"}])
        self.assertRefused("non-empty string", change=[{"append": ""}])

    def test_expect(self):
        incidents.check(incident(expect=[]))
        incidents.check(incident(expect=[{"run": "sysctl -n vm.swappiness", "match": "^10$", "as_written": "10"}]))
        bad = (
            [{"run": "x", "as_written": "y"}],
            [{"run": "x", "as_written": "y", "exit": 0, "match": "z"}],
            [{"run": "x", "exit": 0}],
            [{"run": "x", "as_written": "y", "exit": 0, "timeout": 5}],
        )
        for e in bad:
            self.assertRefused("a probe is", expect=e)
        self.assertRefused("an exit status", expect=[{"run": "x", "as_written": "y", "exit": 256}])
        self.assertRefused("not a regular expression", expect=[{"run": "x", "as_written": "y", "no_match": "("}])

    def test_fix_guess_harmless(self):
        incidents.check(incident(fix=[{"delete": "^UUID="}]))
        self.assertRefused(r"\.fix: must be a non-empty list", fix="undo")
        self.assertRefused(r"\.guess: must be one of", guess="fatal")
        incidents.check(incident(harmless=True, guess="none"))
        self.assertRefused("harmless incident's guess", harmless=True)
        self.assertRefused("true or false", harmless="yes")


class TestFiles(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = self.tmp.name

    def tearDown(self):
        self.tmp.cleanup()

    def put(self, rel, data):
        p = os.path.join(self.root, "incidents", rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "w") as f:
            f.write(data if isinstance(data, str) else json.dumps(data))
        return p

    def test_load_and_load_all(self):
        self.put("fstab/fstab-uuid-one-char.json", incident())
        self.put("sudoers/sudoers-benign.json", incident(id="sudoers-benign", path="/etc/sudoers",
                                                         change=[{"append": "# a comment"}], applies="sudo",
                                                         expect=None, harmless=True, guess="none"))
        incs = incidents.load_all(self.root)
        self.assertEqual([(i["consumer"], i["id"]) for i in incs],
                         [("fstab", "fstab-uuid-one-char"), ("sudoers", "sudoers-benign")])

    def test_bad_files_are_refused(self):
        cases = (
            ("fstab/x.json", "{not json", "cannot read it as JSON"),
            ("fstab/other-name.json", incident(), "but the file is named other-name.json"),
            ("Fstab/fstab-uuid-one-char.json", incident(), "not a consumer name"),
            ("fstab/notes.txt", "x", "only <id>.json files"),
        )
        for rel, data, regex in cases:
            with self.subTest(rel=rel):
                self.tearDown()
                self.setUp()
                self.put(rel, data)
                with self.assertRaisesRegex(IncidentError, regex):
                    incidents.load_all(self.root)

    def test_an_id_used_twice(self):
        self.put("fstab/fstab-uuid-one-char.json", incident())
        self.put("grub/fstab-uuid-one-char.json", incident())
        with self.assertRaisesRegex(IncidentError, "is used by"):
            incidents.load_all(self.root)

    def test_no_incidents_directory(self):
        with self.assertRaisesRegex(IncidentError, "no such directory"):
            incidents.load_all(self.root)

    def test_the_command_line(self):
        self.put("fstab/fstab-uuid-one-char.json", incident())
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(incidents.main(["check", self.root]), 0)
        self.assertEqual(out.getvalue(), "1 incidents, 0 harmless, in 1 consumers (fstab); changes: content 1\n")
        self.put("fstab/bad.json", "[]")
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            self.assertEqual(incidents.main(["check", self.root]), 1)
        self.assertIn("must be a JSON object", err.getvalue())


class TestHash(unittest.TestCase):
    def test_stable_and_pinned(self):
        # Pinned: a label written today must still match after a refactor.
        self.assertEqual(incidents.content_hash(incident()),
                         "e9d274ed90373a57ea73ced3c7fe6da0079d69a56be3a1e00590fdbdafbd73b4")

    def test_key_order_and_spacing_do_not_count(self):
        inc = incident()
        again = json.loads(json.dumps(dict(reversed(list(inc.items()))), indent=4))
        self.assertEqual(incidents.content_hash(inc), incidents.content_hash(again))

    def test_prose_does_not_count_and_the_change_does(self):
        h = incidents.content_hash(incident())
        self.assertEqual(incidents.content_hash(incident(cause="Other words.", guess="error", source=None)), h)
        self.assertEqual(incidents.content_hash(dict(incident(), consumer="fstab")), h)
        for over in ({"change": [{"append": "UUID={root_uuid~2} /data ext4 defaults 0 2"}]},
                     {"applies": "boot"}, {"expect": []}, {"fix": [{"delete": "^UUID="}]},
                     {"harmless": False}, {"path": "/etc/fstab.d/x"}):
            self.assertNotEqual(incidents.content_hash(incident(**over)), h, over)


class TestSurfaces(unittest.TestCase):
    def test_by_kind(self):
        def s(edits, **kw):
            return incidents.surfaces(incident(change=edits), **kw)
        self.assertEqual(s([{"append": "x"}]), ("edit", "scd", "check", "restore"))
        self.assertEqual(s([{"create": "x"}]), ("edit", "scd", "check", "restore"))
        self.assertEqual(s([{"mode": "0666"}]), ("scd", "check", "restore"))
        self.assertEqual(s([{"owner": "a:b"}]), ("scd", "check", "restore"))
        self.assertEqual(s([{"symlink": "/dev/null"}]), ("scd", "restore"))
        self.assertEqual(s([{"unlink": True}]), ("scd", "restore"))

    def test_outside_scds_scope(self):
        def s(edits):
            return incidents.surfaces(incident(change=edits), in_scope=False)
        self.assertEqual(s([{"append": "x"}]), ("edit", "check"))
        self.assertEqual(s([{"mode": "0666"}]), ("check",))
        self.assertEqual(s([{"symlink": "/dev/null"}]), ())


if __name__ == "__main__":
    unittest.main()
