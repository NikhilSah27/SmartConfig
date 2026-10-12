"""Unit tests for lab/factory.py, M6's mutants (docs/M6_PLAN.md, 5.2).
No QEMU, no guest: the mutants are made from the guest's files as
eval/guest/ holds them, and from small made-up files.

  python3 -m unittest discover -s lab -p 'test_factory.py'
"""
import contextlib
import hashlib
import io
import json
import os
import re
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)
sys.dont_write_bytecode = True

import factory  # noqa: E402
from factory import Stock  # noqa: E402

TOP = os.path.dirname(HERE)
EVAL = os.path.join(TOP, "eval")
with open(os.path.join(TOP, "internal", "check", "default.graph"), encoding="utf-8") as _f:
    CHECKS = factory.graph_checks(_f.read())


def guest_files():
    """{path: Stock} for eval/guest/, with each file's mode in the guest."""
    with open(os.path.join(EVAL, "guest.json"), encoding="utf-8") as f:
        meta = json.load(f)["paths"]
    files = {}
    for p, s in factory.read_root(os.path.join(EVAL, "guest")).items():
        files[p] = Stock(s.data, int(meta[p]["mode"], 8), meta[p]["owner"])
    return files


def meaning(data, checker):
    """The lines a consumer reads, as the neutral operators see them:
    no full-line comments, no blank lines, no trailing spaces."""
    f = factory._File(data, checker)
    return [line.rstrip() for i, line in enumerate(f.lines) if line.strip() and not f.comment[i]]


def mutants(text, checker, seed="t", n=200, path="/etc/x"):
    return factory.mutate(path, Stock(text.encode(), None, None), checker, seed, n)


class TestGraph(unittest.TestCase):
    def test_braces_and_globs(self):
        checks = factory.graph_checks("# a comment\ncheck grubcfg /boot/grub/{grub.cfg,custom.cfg}\n"
                                      "apply grubcfg at the next boot\ncheck sudoers /etc/sudoers /etc/sudoers.d/*\n")
        self.assertEqual(checks, [("grubcfg", "/boot/grub/grub.cfg"), ("grubcfg", "/boot/grub/custom.cfg"),
                                  ("sudoers", "/etc/sudoers"), ("sudoers", "/etc/sudoers.d/*")])
        self.assertEqual(factory.checker_for(checks, "/etc/sudoers.d/README"), "sudoers")
        self.assertIsNone(factory.checker_for(checks, "/etc/sudoers.d/a/b"))  # * stops at a /
        self.assertIsNone(factory.checker_for(checks, "/boot/grub/grubenv"))

    def test_globs_go_reads_otherwise_are_refused(self):
        # scope.Glob: [^x] negates and [!x] does not, ** spans segments, \\
        # escapes; fnmatch differs on each. The graph uses none of them.
        for glob in ("/etc/udev/rules.d/[!7]*.rules", "/etc/udev/rules.d/[^7]*.rules", "/etc/x/**", "/etc/a\\*b"):
            with self.assertRaisesRegex(ValueError, "not read here", msg=glob):
                factory.graph_checks("check udev %s\n" % glob)

    def test_the_first_match_wins(self):
        # As Graph.Checker: the first check line whose glob matches.
        checks = factory.graph_checks("check a /etc/*\ncheck b /etc/x\n")
        self.assertEqual(factory.checker_for(checks, "/etc/x"), "a")

    def test_the_real_graph(self):
        for path, checker in (("/etc/fstab", "fstab"), ("/etc/sudoers.d/README", "sudoers"),
                              ("/etc/ssh/sshd_config.d/50-cloud-init.conf", "sshd"),
                              ("/etc/systemd/system/x.service", "unit"),
                              ("/etc/systemd/system/x.service.d/override.conf", "unitdropin"),
                              ("/etc/default/grub", "shsyntax"), ("/boot/grub/grub.cfg", "grubcfg"),
                              ("/etc/netplan/50-cloud-init.yaml", "netplan"), ("/etc/passwd", "passwd"),
                              ("/etc/pam.d/common-auth", None), ("/etc/ssh/sshd_config.d/x.txt", None)):
            self.assertEqual(factory.checker_for(CHECKS, path), checker, path)


class TestDeterminism(unittest.TestCase):
    def setUp(self):
        self.files = guest_files()

    def test_the_same_seed_gives_the_same_mutants(self):
        a = factory.generate(self.files, CHECKS, "seed-1", 30)
        self.assertEqual(a, factory.generate(self.files, CHECKS, "seed-1", 30))
        b = factory.generate(self.files, CHECKS, "seed-2", 30)
        self.assertNotEqual([m.content for m in a], [m.content for m in b])

    def test_a_files_mutants_do_not_depend_on_the_others(self):
        alone = factory.generate({"/etc/fstab": self.files["/etc/fstab"]}, CHECKS, "s", 30)
        every = [m for m in factory.generate(self.files, CHECKS, "s", 30) if m.path == "/etc/fstab"]
        self.assertEqual(alone, every)

    def test_at_most_n_content_mutants_each_new(self):
        ms = factory.generate(self.files, CHECKS, "s", 25)
        self.assertEqual(len({m.id for m in ms}), len(ms))
        for path in {m.path for m in ms}:
            content = [m.content for m in ms if m.path == path and m.content is not None]
            self.assertLessEqual(len(content), 25, path)
            self.assertEqual(len(set(content)), len(content), path)
            self.assertNotIn(self.files[path].data, content, path)

    def test_no_checker_no_mutant(self):
        files = dict(self.files, **{"/etc/pam.d/x": Stock(b"auth required pam_unix.so\n", 0o644, None)})
        self.assertFalse([m for m in factory.generate(files, CHECKS, "s", 10) if m.path == "/etc/pam.d/x"])

    def test_sample(self):
        ids = ["m%d" % i for i in range(50)]
        s = factory.sample(ids, "s", 7)
        self.assertEqual(s, factory.sample(list(reversed(ids)), "s", 7))
        self.assertEqual((len(s), s), (7, sorted(s)))
        self.assertLessEqual(set(s), set(ids))
        self.assertNotEqual(s, factory.sample(ids, "t", 7))
        self.assertEqual(factory.sample(ids[:3], "s", 7), ids[:3])


class TestNeutral(unittest.TestCase):
    def test_neutral_on_the_guests_files(self):
        files, seen = guest_files(), set()
        for m in factory.generate(files, CHECKS, "neutral", 200):
            if m.kind != "neutral":
                continue
            seen.add(m.op)
            with self.subTest(m.id):
                stock = files[m.path].data
                self.assertNotEqual(m.content, stock)
                self.assertEqual(meaning(m.content, m.checker), meaning(stock, m.checker), m.note)
        self.assertEqual(seen, set(factory.NEUTRAL))

    def test_no_neutral_mutant_without_a_comment_syntax(self):
        for checker in ("passwd", "group", "preload", "flag"):
            self.assertFalse([m for m in mutants("root:x:0:0:root:/root:/bin/bash\n", checker) if m.kind == "neutral"])

    def neutral(self, text, checker):
        return [m for m in mutants(text, checker) if m.kind == "neutral"]

    def test_a_continued_line_is_left_whole(self):
        # Nothing goes in between, and the \ still ends its line.
        text = "Cmnd_Alias X = /bin/a, \\\n\t/bin/b\n%sudo ALL=(ALL:ALL) ALL\n"
        ms = self.neutral(text, "sudoers")
        self.assertTrue(ms)
        for m in ms:
            self.assertIn(b"/bin/a, \\\n\t/bin/b", m.content, m.note)

    def test_a_shell_quote_is_left_whole(self):
        text = 'A=0\nB="one\ntwo"\n# a note\nC=1\n'
        ms = self.neutral(text, "shsyntax")
        self.assertTrue(ms)
        for m in ms:
            self.assertIn(b'B="one\ntwo"\n', m.content, m.note)

    def test_sudoers_directives_are_not_comments(self):
        text = "# a comment\n#includedir /etc/sudoers.d\n#include /etc/sudoers.local\n%sudo ALL=(ALL:ALL) ALL\n"
        for m in self.neutral(text, "sudoers"):
            self.assertIn(b"\n#includedir /etc/sudoers.d\n", m.content, m.note)
            self.assertIn(b"\n#include /etc/sudoers.local\n", m.content, m.note)
        edits = [m for m in mutants(text, "sudoers") if m.op == "comment_edit"]
        self.assertTrue(edits)
        for m in edits:
            self.assertTrue(m.content.startswith(b"# a comment (local note)\n"), m.content)

    def test_a_heredoc_is_left_whole(self):
        for text, body in (('A=$(cat <<EOT\none\n# two\nEOT\n)\nB=1\n', b"<<EOT\none\n# two\nEOT\n"),
                           ("A=$(cat <<-'EOT'\n\tone\n\tEOT\n)\n# c\nB=1\n", b"<<-'EOT'\n\tone\n\tEOT\n")):
            ms = self.neutral(text, "shsyntax")
            self.assertTrue(ms)
            for m in ms:
                self.assertIn(body, m.content, m.note)
        self.assertEqual(factory._quote_states(["A=$(cat <<EOT\n", "x\n", "EOT\n", "B=1\n"]),
                         [True, True, False, False])

    def test_a_quoted_yaml_scalar_is_left_whole(self):
        text = ('network:\n  version: 2\n  x: "one\n\n    # two\n    three"\n  y: it\'s\n  z: \'a\'\'b\'\n'
                "  w: 'it''s\n    long'\n")
        ms = self.neutral(text, "netplan")
        self.assertTrue(ms)
        for m in ms:
            self.assertIn(b'  x: "one\n\n    # two\n    three"\n', m.content, m.note)
        # A quote inside a plain scalar, and '' inside a quoted one, open nothing.
        self.assertEqual(factory._yaml_quote_states(text.splitlines(keepends=True)),
                         [False, False, True, True, True, False, False, False, True, False])

    def test_the_quote_scanner(self):
        lines = ['A="x\\"y"\n', "B='it'\\''s'\n", '# "a comment\n', 'C=1 # "after a word\n',
                 "D=\"it's\"\n", 'E="open\n', 'still"\n', "F='a\n", "b'\n"]
        self.assertEqual(factory._quote_states(lines), [False, False, False, False, False, True, False, True, False])

    def test_what_is_a_comment(self):
        self.assertTrue(factory._is_comment("; a note", "unit"))
        self.assertTrue(factory._is_comment("; a note", "sysctl"))
        self.assertFalse(factory._is_comment("; a note", "fstab"))
        self.assertFalse(factory._is_comment("#1000 ALL=(ALL) ALL", "sudoers"))
        self.assertTrue(factory._is_comment("#1000 ALL=(ALL) ALL", "fstab"))
        self.assertTrue(factory._is_comment("  # indented", "sshd"))

    def test_comment_edit_leaves_these(self):
        text = "#!/bin/sh\nA=1 \\\n# inside a continued line\nB=2\n"
        self.assertFalse([m for m in mutants(text, "shsyntax") if m.op == "comment_edit"])

    def test_every_checker_that_may_have_neutral_mutants_gets_them(self):
        for checker in factory.NEUTRAL_OK:
            ms = mutants("# a comment\nkey = value\nother = value\n", checker, n=60)
            self.assertEqual({m.op for m in ms if m.kind == "neutral"}, set(factory.NEUTRAL), checker)

    def test_a_yaml_block_scalar_is_left_whole(self):
        text = "network:\n  version: 2\n  note: |\n    one\n    two\n  renderer: networkd\n"
        ms = self.neutral(text, "netplan")
        self.assertTrue(ms)
        for m in ms:
            self.assertRegex(m.content, rb"  note: \|[ \t]*\n    one\n    two\n", m.note)


class TestChanged(unittest.TestCase):
    def test_every_operator_on_the_guests_files(self):
        files = guest_files()
        ms = factory.generate(files, CHECKS, "changed", 200)
        by_op = {}
        for m in ms:
            if m.kind == "changed":
                by_op.setdefault(m.op, []).append(m)
                self.assertNotEqual(m.content, files[m.path].data, m.id)
        self.assertEqual(set(by_op), set(factory.CHANGED))
        self.assertEqual({m.checker for m in by_op["yaml_tabs"]}, {"netplan"})
        for m in by_op["crlf"]:
            self.assertEqual(m.content, files[m.path].data.replace(b"\n", b"\r\n"))
        for m in by_op["cut"]:
            stock = files[m.path].data
            self.assertTrue(stock.startswith(m.content) and 0 < len(m.content) < len(stock))
        for m in by_op["bom"]:
            self.assertEqual(m.content, factory.BOM + files[m.path].data)
        self.assertTrue(all(m.content == b"" for m in by_op["empty"]))

    def test_char_operators_change_one_word_of_content(self):
        text = "# comment words here\nLABEL=cloudimg-rootfs / ext4 defaults 0 1\n"
        for m in mutants(text, "fstab"):
            if m.op.startswith("char_"):
                lines = m.content.decode().splitlines()
                self.assertEqual(lines[0], "# comment words here", m.note)
                self.assertNotEqual(lines[1], text.splitlines()[1], m.note)
                self.assertRegex(m.note, r"^line 2: \w+ -> \w+$")

    def test_whole_file_operators_skip_what_is_there(self):
        import random
        rng = random.Random(1)
        for op, data in (("crlf", b"a\r\nb\r\n"), ("crlf", b"no newline"), ("no_final_newline", b"a"),
                         ("bom", factory.BOM + b"a\n"), ("empty", b""), ("cut", b"a")):
            self.assertIsNone(factory._whole(factory._File(data, "fstab"), rng, op), (op, data))

    def test_keyboard_neighbours(self):
        # A plain grid: the rows are not staggered.
        self.assertEqual(sorted(factory.NEAR["s"]), sorted("adwx"))
        self.assertEqual(sorted(factory.NEAR["1"]), sorted("2q"))
        for c, near in factory.NEAR.items():
            for d in near:
                self.assertIn(c, factory.NEAR[d], (c, d))


class TestModes(unittest.TestCase):
    def test_sudoers_paths_only(self):
        files = guest_files()
        ms = [m for m in factory.generate(files, CHECKS, "s", 10) if m.kind in ("mode", "owner")]
        self.assertEqual({m.path for m in ms}, {"/etc/sudoers"})
        modes = [m.mode for m in ms if m.kind == "mode"]
        self.assertNotIn(files["/etc/sudoers"].mode, modes)
        self.assertEqual(len(modes), len(factory.MODES) - (files["/etc/sudoers"].mode in
                                                            [int(x, 8) for x in factory.MODES]))
        self.assertEqual([m.owner for m in ms if m.kind == "owner"], list(factory.OWNERS))
        self.assertTrue(all(m.content is None for m in ms))

    def test_the_stock_mode_and_owner_are_left_out(self):
        ms = factory.mutate("/etc/sudoers.d/x", Stock(b"%sudo ALL=(ALL:ALL) ALL\n", 0o644, "nobody:root"),
                            "sudoers", "s", 5)
        self.assertNotIn(0o644, [m.mode for m in ms])
        self.assertEqual([m.owner for m in ms if m.kind == "owner"], ["root:nogroup"])


class TestWrite(unittest.TestCase):
    def test_make(self):
        with tempfile.TemporaryDirectory() as out:
            with contextlib.redirect_stdout(io.StringIO()) as said:
                rc = factory.main(["make", "--seed", "s", "--per-file", "8", "--graph",
                                   os.path.join(TOP, "internal", "check", "default.graph"),
                                   os.path.join(EVAL, "guest"), out])
            self.assertEqual(rc, 0)
            self.assertRegex(said.getvalue(), r"^\d+ mutants of \d+ files: changed \d+, ")
            with open(os.path.join(out, "manifest.tsv"), encoding="utf-8") as f:
                rows = [line.rstrip("\n").split("\t") for line in f]
            self.assertTrue(rows)
            for n, row in enumerate(rows, 1):
                self.assertEqual(len(row), 11, row)
                self.assertEqual(row[0], str(n))
                if row[6] != "-":
                    with open(os.path.join(out, row[6]), "rb") as f:
                        self.assertEqual(hashlib.sha256(f.read()).hexdigest(), row[7])
                else:
                    self.assertIn(row[4], ("mode", "owner"))
            self.assertEqual(sorted(os.listdir(os.path.join(out, "files"))),
                             sorted(os.path.basename(r[6]) for r in rows if r[6] != "-"))

    def test_mode_and_owner_columns(self):
        ms = [factory.Mutant("/etc/sudoers#mode.0440", "/etc/sudoers", "sudoers", "mode", "mode", None, 0o440, None,
                             "mode 0440"),
              factory.Mutant("/etc/sudoers#owner.x", "/etc/sudoers", "sudoers", "owner", "owner", None, None,
                             "nobody:root", "owner nobody:root")]
        with tempfile.TemporaryDirectory() as out:
            factory.write(ms, out)
            with open(os.path.join(out, "manifest.tsv"), encoding="utf-8") as f:
                rows = [line.rstrip("\n").split("\t") for line in f]
        self.assertEqual([r[6:10] for r in rows], [["-", "-", "0440", "-"], ["-", "-", "-", "nobody:root"]])

    def test_make_takes_the_guests_modes(self):
        with tempfile.TemporaryDirectory() as out:
            with contextlib.redirect_stdout(io.StringIO()):
                factory.main(["make", "--seed", "s", "--per-file", "2", "--graph",
                              os.path.join(TOP, "internal", "check", "default.graph"),
                              "--modes", os.path.join(EVAL, "guest.json"), os.path.join(EVAL, "guest"), out])
            with open(os.path.join(out, "manifest.tsv"), encoding="utf-8") as f:
                rows = [line.rstrip("\n").split("\t") for line in f]
        # The guest's /etc/sudoers is 0440 root:root: no MODES or OWNERS entry is left out.
        self.assertEqual(sorted(r[8] for r in rows if r[4] == "mode"), sorted(factory.MODES))
        self.assertEqual([r[9] for r in rows if r[4] == "owner"], list(factory.OWNERS))

    def test_read_root(self):
        with tempfile.TemporaryDirectory() as root:
            os.makedirs(os.path.join(root, "etc"))
            with open(os.path.join(root, "etc", "a"), "w") as f:
                f.write("x\n")
            os.chmod(os.path.join(root, "etc", "a"), 0o600)
            os.symlink("a", os.path.join(root, "etc", "b"))
            self.assertEqual(factory.read_root(root), {"/etc/a": Stock(b"x\n", 0o600, None)})
            meta = {"/etc/a": {"mode": "0440", "owner": "root:root"}}
            self.assertEqual(factory.read_root(root, meta), {"/etc/a": Stock(b"x\n", 0o440, "root:root")})

    def test_cells_are_escaped(self):
        m = factory.Mutant("/x#cut.1", "/x", "fstab", "changed", "cut", b"a", None, None, "a\tb\nc\\d\re")
        with tempfile.TemporaryDirectory() as out:
            factory.write([m], out)
            with open(os.path.join(out, "manifest.tsv"), encoding="utf-8") as f:
                line = f.read()
        self.assertEqual(line.count("\n"), 1)
        self.assertTrue(line.endswith("\ta\\tb\\nc\\\\d\\re\n"), line)
        self.assertTrue(re.match(r"1\t/x#cut\.1\t/x\tfstab\tchanged\tcut\tfiles/0001\t[0-9a-f]{64}\t-\t-\t", line))


if __name__ == "__main__":
    unittest.main()
