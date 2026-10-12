#!/usr/bin/env python3
"""factory.py: M6's mutants (docs/M6_PLAN.md, section 5.2).

A mutant is one of the guest's stock files with one small change, made
by an operator from a seed: the same seed and the same files give the
same mutants (on one Python version: random's methods may change
between versions), and a file's mutants do not depend on which other
files are in the set. They are never written into the live /etc: content
mutants are checked with "sc check --as PATH FILE" from a scratch
directory, and mode and owner mutants (the sudoers paths only, the one
place a rule reads the file on disk) are made on disk and put back.

  graph_checks   the graph's check lines: which checker reads which glob
  checker_for    the checker the graph names for a path, or None
  mutate         the mutants of one file
  generate       the mutants of a set of files, at most N per file
  sample         a fixed-size sample of mutant ids, picked by the seed
  write          content mutants as files, and manifest.tsv for them all

Neutral mutants change nothing by construction (a comment added or
changed, a blank line, trailing spaces), so any finding they add is a
false alarm. Changed mutants have no truth label: they are held to the
robustness rule of goal 5. No operator reorders lines: sudoers is
last-match-wins and sshd first-match-wins.

The command line makes a set from a copy of the guest's files:
"factory.py make --seed S --graph GRAPH [--modes JSON] ROOT OUT". Python 3 standard
library only.
"""
import argparse
import collections
import fnmatch
import hashlib
import json
import os
import random
import re
import sys

Mutant = collections.namedtuple("Mutant", "id path checker kind op content mode owner note")
Stock = collections.namedtuple("Stock", "data mode owner")

NEUTRAL = ("comment_add", "comment_edit", "blank_line", "trailing_space")
CHANGED = ("char_drop", "char_double", "char_swap", "char_neighbour", "line_delete", "line_double",
           "line_comment", "yaml_tabs", "crlf", "no_final_newline", "cut", "empty", "bom")
# The checkers whose files take a full-line "#" comment, a blank line and
# trailing spaces without a change of meaning. passwd and group have no
# comment syntax, and a trailing space would join the shell's name; the
# preload file and the flags are absent on a stock guest.
NEUTRAL_OK = ("fstab", "sudoers", "sshd", "unit", "unitdropin", "shsyntax", "grubcfg", "nsswitch",
              "hosts", "netplan", "udev", "sysctl")
# Files read as shell or GRUB script: a line inside an open quote is part
# of a value, not a line of its own.
QUOTED = ("shsyntax", "grubcfg")
MODES = ("0400", "0444", "0600", "0640", "0644", "0660", "0664", "0666", "0755", "0777")
# Users and groups every Ubuntu system has.
OWNERS = ("nobody:root", "root:nogroup")
NOTES = ("# local change", "# edited by hand; see the change log", "# TODO: review this")
BOM = b"\xef\xbb\xbf"

ROWS = ("1234567890", "qwertyuiop", "asdfghjkl", "zxcvbnm")


def _neighbours():
    pos = {c: (r, i) for r, row in enumerate(ROWS) for i, c in enumerate(row)}
    out = {}
    for c, (r, i) in pos.items():
        near = [ROWS[r][j] for j in (i - 1, i + 1) if 0 <= j < len(ROWS[r])]
        near += [ROWS[r2][i] for r2 in (r - 1, r + 1) if 0 <= r2 < len(ROWS) and i < len(ROWS[r2])]
        out[c] = "".join(near)
    return out


NEAR = _neighbours()


def _braces(glob):
    m = re.search(r"\{([^{}]*)\}", glob)
    if not m:
        return [glob]
    out = []
    for alt in m.group(1).split(","):
        out += _braces(glob[:m.start()] + alt + glob[m.end():])
    return out


def graph_checks(text):
    """[(checker, glob)] from the graph's "check CHECKER GLOB..." lines,
    in order, with {a,b} expanded. The graph's globs are
    internal/scope/glob.go's; this reads the part of them the graph uses,
    "*" and "?" within a segment and {a,b}. A glob with a class [...],
    "**" or a "\\" escape is refused, not read differently from Go."""
    out = []
    for line in text.splitlines():
        f = line.split()
        if len(f) >= 3 and f[0] == "check":
            for glob in f[2:]:
                if re.search(r"[\[\\]|\*\*", glob):
                    raise ValueError("graph glob %r: [...], ** and \\ are not read here" % glob)
                out += [(f[1], g) for g in _braces(glob)]
    return out


def _match(glob, path):
    # As scope.Glob for the part graph_checks lets through: * and ? do not
    # cross a /.
    g, p = glob.split("/"), path.split("/")
    return len(g) == len(p) and all(fnmatch.fnmatchcase(b, a) for a, b in zip(g, p))


def checker_for(checks, path):
    for checker, glob in checks:
        if _match(glob, path):
            return checker
    return None


def _subseed(seed, *parts):
    h = hashlib.sha256(("%s\0" % seed + "\0".join(parts)).encode("utf-8", "surrogateescape"))
    return int.from_bytes(h.digest()[:8], "big")


def _is_comment(line, checker):
    s = line.lstrip(" \t")
    if checker == "sudoers":
        # "#include", "#includedir" and "#<uid>" are not comments.
        return s.startswith("#") and not re.match(r"#(include|\d)", s)
    if checker in ("unit", "unitdropin", "sysctl"):
        return s[:1] in ("#", ";")
    return s.startswith("#")


HEREDOC = re.compile(r"<<(-?)\s*(['\"]?)([A-Za-z_][A-Za-z0-9_]*)\2")


def _quote_states(lines):
    """For shell and GRUB script: whether each line ends inside a quote or
    a heredoc, where its line end and the next lines are part of a value
    (a simple scanner: '...', "..." with backslash escapes, # comments at
    the start of a word, and <<WORD or <<-WORD up to the line WORD)."""
    state, out, heredocs = None, [], []
    for line in lines:
        if heredocs and state is None:
            # A line of a heredoc's body, or the line that ends it.
            dash, word = heredocs[0]
            if (line.lstrip("\t") if dash else line).rstrip("\r\n") == word:
                heredocs.pop(0)
            out.append(bool(heredocs))
            continue
        i, n = 0, len(line)
        while i < n:
            c = line[i]
            if state is None:
                if c == "\\":
                    i += 1
                elif c in "'\"":
                    state = c
                elif c == "#" and (i == 0 or line[i - 1] in " \t\n;"):
                    break
                elif c == "<":
                    m = HEREDOC.match(line, i)
                    if m:
                        heredocs.append((m.group(1) == "-", m.group(3)))
                        i = m.end() - 1
            elif state == "'":
                if c == "'":
                    state = None
            else:
                if c == "\\":
                    i += 1
                elif c == '"':
                    state = None
            i += 1
        out.append(state is not None or bool(heredocs))
    return out


def _yaml_quote_states(lines):
    """For YAML: whether each line ends inside a quoted scalar ("..." or
    '...') that goes on to the next line. A quote opens a scalar only
    where one may start: after "key:", "- ", "[", "{" or ",", or at the
    start of the line."""
    state, out = None, []
    for line in lines:
        i, n = 0, len(line)
        while i < n:
            c = line[i]
            if state is None:
                before = line[:i].rstrip(" \t")
                if c == "#" and (i == 0 or line[i - 1] in " \t"):
                    break
                if c in "'\"" and (before == "" or before[-1] in ":-[{,"):
                    state = c
            elif state == '"':
                if c == "\\":
                    i += 1
                elif c == '"':
                    state = None
            elif c == "'":
                if line[i + 1:i + 2] == "'":
                    i += 1  # '' is a quote inside the scalar
                else:
                    state = None
            i += 1
        out.append(state is not None)
    return out


def _yaml_block_lines(lines):
    """The lines inside a YAML block scalar (after "key: |" or "key: >"),
    whose spaces and comments are part of a value."""
    inside, block = set(), None
    for i, line in enumerate(lines):
        body = line.rstrip("\r\n")
        indent = len(body) - len(body.lstrip(" "))
        if block is not None:
            if body.strip() == "" or indent > block:
                inside.add(i)
                continue
            block = None
        if re.search(r":\s*[|>][+-]?\d*\s*$", body):
            block = indent
    return inside


class _File:
    """A file split into lines, with what each operator needs to know."""

    def __init__(self, data, checker):
        self.data, self.checker = data, checker
        self.text = data.decode("utf-8", "surrogateescape")
        self.lines = self.text.splitlines(keepends=True)
        n = len(self.lines)
        body = [x.rstrip("\r\n") for x in self.lines]
        self.cont = [b.endswith("\\") for b in body]
        if checker in QUOTED:
            self.open = _quote_states(self.lines)
        elif checker == "netplan":
            self.open = _yaml_quote_states(self.lines)
        else:
            self.open = [False] * n
        self.block = _yaml_block_lines(self.lines) if checker == "netplan" else set()
        self.comment = [_is_comment(b, checker) and b.strip() != "" for b in body]
        self.content = [i for i in range(n) if body[i].strip() and not self.comment[i]]

    def gap_ok(self, i):
        """A line may go in before line i (i == len: at the end)."""
        if i > 0 and (self.cont[i - 1] or self.open[i - 1] or (i - 1) in self.block):
            return False
        return i not in self.block

    def join(self, lines):
        return "".join(lines).encode("utf-8", "surrogateescape")


def _eol(line):
    return line[len(line.rstrip("\r\n")):] or ""


# Each operator returns (new bytes, note) or None when it has nothing to
# work on in this file.

def _comment_add(f, rng):
    gaps = [i for i in range(len(f.lines) + 1) if f.gap_ok(i)]
    if not gaps:
        return None
    i, note = rng.choice(gaps), rng.choice(NOTES)
    lines = list(f.lines)
    if i == len(lines) and lines and not lines[-1].endswith("\n"):
        lines[-1] += "\n"
    lines.insert(i, note + "\n")
    return f.join(lines), "a comment before line %d" % (i + 1)


def _comment_edit(f, rng):
    cand = [i for i, c in enumerate(f.comment)
            if c and not f.lines[i].lstrip().startswith("#!") and i not in f.block
            and not f.cont[i] and not (i > 0 and (f.open[i - 1] or f.cont[i - 1]))]
    if not cand:
        return None
    i = rng.choice(cand)
    lines = list(f.lines)
    lines[i] = lines[i].rstrip("\r\n") + " (local note)" + _eol(lines[i])
    return f.join(lines), "comment on line %d reworded" % (i + 1)


def _blank_line(f, rng):
    gaps = [i for i in range(1, len(f.lines) + 1) if f.gap_ok(i)]
    if not gaps:
        return None
    i = rng.choice(gaps)
    lines = list(f.lines)
    if i == len(lines) and not lines[-1].endswith("\n"):
        lines[-1] += "\n"
    lines.insert(i, "\n")
    return f.join(lines), "a blank line before line %d" % (i + 1)


def _trailing_space(f, rng):
    # Not after a sudoers include: the rest of that line is a file name.
    cand = [i for i, line in enumerate(f.lines)
            if line.strip() and not f.cont[i] and not f.open[i] and i not in f.block
            and not (i > 0 and f.open[i - 1])
            and not (f.checker == "sudoers" and re.match(r"\s*[#@]include", line))]
    if not cand:
        return None
    i, pad = rng.choice(cand), rng.choice(("  ", " \t", "   "))
    lines = list(f.lines)
    lines[i] = lines[i].rstrip("\r\n") + pad + _eol(lines[i])
    return f.join(lines), "trailing spaces on line %d" % (i + 1)


def _token(f, rng, least):
    """A line of content, and a word in it of at least LEAST characters:
    a keyword or a value."""
    cand = []
    for i in f.content:
        words = [m for m in re.finditer(r"[A-Za-z0-9_]+", f.lines[i]) if len(m.group()) >= least]
        if words:
            cand.append((i, words))
    if not cand:
        return None
    i, words = rng.choice(cand)
    return i, rng.choice(words)


def _char_op(f, rng, op):
    t = _token(f, rng, 2)
    if t is None:
        return None
    i, m = t
    word, line = m.group(), f.lines[i]
    j = rng.randrange(len(word))
    if op == "char_drop":
        new = word[:j] + word[j + 1:]
    elif op == "char_double":
        new = word[:j] + word[j] + word[j:]
    elif op == "char_swap":
        pairs = [k for k in range(len(word) - 1) if word[k] != word[k + 1]]
        if not pairs:
            return None
        k = rng.choice(pairs)
        new = word[:k] + word[k + 1] + word[k] + word[k + 2:]
    else:
        keys = [k for k in range(len(word)) if word[k].lower() in NEAR]
        if not keys:
            return None
        k = rng.choice(keys)
        c = rng.choice(NEAR[word[k].lower()])
        new = word[:k] + (c.upper() if word[k].isupper() else c) + word[k + 1:]
    lines = list(f.lines)
    lines[i] = line[:m.start()] + new + line[m.end():]
    return f.join(lines), "line %d: %s -> %s" % (i + 1, word, new)


def _line_op(f, rng, op):
    if not f.content:
        return None
    i = rng.choice(f.content)
    lines = list(f.lines)
    if op == "line_delete":
        del lines[i]
        what = "deleted"
    elif op == "line_double":
        if not lines[i].endswith("\n"):
            lines[i] += "\n"
        lines.insert(i, lines[i])
        what = "doubled"
    else:
        lines[i] = "#" + lines[i]
        what = "commented out"
    return f.join(lines), "line %d %s" % (i + 1, what)


def _yaml_tabs(f, rng):
    if f.checker != "netplan":
        return None
    cand = [i for i in f.content if f.lines[i].startswith(" ") and i not in f.block]
    if not cand:
        return None
    i = rng.choice(cand)
    lines = list(f.lines)
    lines[i] = "\t" + lines[i].lstrip(" ")
    return f.join(lines), "line %d indented with a tab" % (i + 1)


def _whole(f, rng, op):
    d = f.data
    if op == "crlf":
        if b"\n" not in d or b"\r\n" in d:
            return None
        return d.replace(b"\n", b"\r\n"), "CRLF line ends"
    if op == "no_final_newline":
        return (d[:-1], "no final newline") if d.endswith(b"\n") else None
    if op == "cut":
        if len(d) < 2:
            return None
        r = rng.randrange(1, len(d))
        return d[:r], "cut at byte %d of %d" % (r, len(d))
    if op == "empty":
        return (b"", "empty") if d else None
    if op == "bom":
        return (BOM + d, "a BOM") if not d.startswith(BOM) else None
    raise ValueError(op)


def _apply(op, f, rng):
    if op == "comment_add":
        return _comment_add(f, rng)
    if op == "comment_edit":
        return _comment_edit(f, rng)
    if op == "blank_line":
        return _blank_line(f, rng)
    if op == "trailing_space":
        return _trailing_space(f, rng)
    if op.startswith("char_"):
        return _char_op(f, rng, op)
    if op.startswith("line_"):
        return _line_op(f, rng, op)
    if op == "yaml_tabs":
        return _yaml_tabs(f, rng)
    return _whole(f, rng, op)


def mutate(path, stock, checker, seed, n=40):
    """At most N mutants of the file at PATH, whose stock content, mode
    (an int, or None) and owner are STOCK, read by CHECKER. The
    operators take turns; a mutant equal to the stock file or to an
    earlier one is dropped. Mode and owner mutants, for the sudoers
    paths, come on top of the N."""
    rng = random.Random(_subseed(seed, path))
    f = _File(stock.data, checker)
    ops = (NEUTRAL if checker in NEUTRAL_OK else ()) + CHANGED
    out, seen, count = [], {stock.data}, collections.Counter()
    for turn in range(n * 20):
        if len(out) >= n:
            break
        op = ops[turn % len(ops)]
        r = _apply(op, f, rng)
        if r is None or r[0] in seen:
            continue
        seen.add(r[0])
        count[op] += 1
        kind = "neutral" if op in NEUTRAL else "changed"
        out.append(Mutant("%s#%s.%d" % (path, op, count[op]), path, checker, kind, op, r[0], None, None, r[1]))
    if checker == "sudoers":
        for m in MODES:
            if stock.mode is None or int(m, 8) != stock.mode:
                out.append(Mutant("%s#mode.%s" % (path, m), path, checker, "mode", "mode", None, int(m, 8), None,
                                  "mode " + m))
        for o in OWNERS:
            if o != stock.owner:
                out.append(Mutant("%s#owner.%s" % (path, o), path, checker, "owner", "owner", None, None, o,
                                  "owner " + o))
    return out


def generate(files, checks, seed, n=40):
    """The mutants of FILES ({path: Stock}), each file read by the checker
    the graph names for it; a file no checker reads has none."""
    out = []
    for path in sorted(files):
        checker = checker_for(checks, path)
        if checker:
            out += mutate(path, files[path], checker, seed, n)
    return out


def sample(ids, seed, k):
    """K of IDS (all of them if fewer), picked by the seed, sorted."""
    ids = sorted(set(ids))
    return sorted(random.Random(_subseed(seed, "sample")).sample(ids, min(k, len(ids))))


def _cell(v):
    if v is None:
        return "-"
    return str(v).replace("\\", "\\\\").replace("\t", "\\t").replace("\n", "\\n").replace("\r", "\\r")


def write(mutants, out):
    """Each content mutant as OUT/files/NNNN, and OUT/manifest.tsv:
    n, id, path, checker, kind, op, file, sha256, mode, owner, note."""
    os.makedirs(os.path.join(out, "files"), exist_ok=True)
    rows = []
    for n, m in enumerate(mutants, 1):
        name = sha = None
        if m.content is not None:
            name = "files/%04d" % n
            with open(os.path.join(out, name), "wb") as f:
                f.write(m.content)
            sha = hashlib.sha256(m.content).hexdigest()
        mode = "%04o" % m.mode if m.mode is not None else None
        rows.append("\t".join(_cell(v) for v in (n, m.id, m.path, m.checker, m.kind, m.op, name, sha, mode,
                                                 m.owner, m.note)))
    with open(os.path.join(out, "manifest.tsv"), "w", encoding="utf-8", errors="surrogateescape") as f:
        f.write("".join(r + "\n" for r in rows))


def read_root(root, meta=None):
    """{path: Stock} for each regular file under ROOT, a copy of the
    guest's files after the installed-server profile (ROOT/etc/fstab is
    /etc/fstab). Links are left out. Each file's mode and owner come from
    META ({path: {"mode": "0440", "owner": "root:root"}}) when it has the
    path; else the mode is the copy's, which a git checkout does not
    keep, and the owner is not known."""
    meta = meta or {}
    files = {}
    for d, dirs, names in os.walk(root):
        dirs.sort()
        for name in sorted(names):
            p = os.path.join(d, name)
            if os.path.islink(p) or not os.path.isfile(p):
                continue
            with open(p, "rb") as f:
                data = f.read()
            path = "/" + os.path.relpath(p, root)
            m = meta.get(path, {})
            mode = int(m["mode"], 8) if "mode" in m else os.stat(p).st_mode & 0o7777
            files[path] = Stock(data, mode, m.get("owner"))
    return files


def main(argv=None):
    ap = argparse.ArgumentParser(description="Make M6's mutants from a copy of the guest's files.")
    sub = ap.add_subparsers(dest="cmd", required=True)
    mk = sub.add_parser("make", help="write OUT/manifest.tsv and OUT/files/")
    mk.add_argument("--seed", required=True)
    mk.add_argument("--per-file", type=int, default=40)
    mk.add_argument("--graph", required=True, help="internal/check/default.graph")
    mk.add_argument("--modes", help="JSON of each path's mode and owner in the guest, "
                                    "{path: {mode, owner}} or eval/guest.json's form, {\"paths\": ...}")
    mk.add_argument("root", help="a copy of the guest's files after the profile: ROOT/etc/fstab is /etc/fstab")
    mk.add_argument("out")
    args = ap.parse_args(argv)
    with open(args.graph, encoding="utf-8") as f:
        checks = graph_checks(f.read())
    meta = None
    if args.modes:
        with open(args.modes, encoding="utf-8") as f:
            meta = json.load(f)
        meta = meta.get("paths", meta)
    ms = generate(read_root(args.root, meta), checks, args.seed, args.per_file)
    write(ms, args.out)
    kinds = collections.Counter(m.kind for m in ms)
    print("%d mutants of %d files: %s" % (len(ms), len({m.path for m in ms}),
                                          ", ".join("%s %d" % kv for kv in sorted(kinds.items()))))
    return 0


if __name__ == "__main__":
    sys.exit(main())
