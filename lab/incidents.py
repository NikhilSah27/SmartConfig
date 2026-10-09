#!/usr/bin/env python3
"""incidents.py: M6's incidents (docs/M6_PLAN.md, section 3).

An incident is one JSON file, eval/incidents/<consumer>/<id>.json: one
change to one file of the lab guest, what makes it take effect, and how
to tell that it took effect as written.

  load, load_all  read and check one incident, or the whole set
  change_kind     what kind of change it is: content, create, mode,
                  owner, symlink or unlink
  surfaces        which of sc's surfaces score it (sc edit, scd, sc check,
                  sc restore), by that kind and by scd's scope
  substitute      put the guest's own values into a text ({root_uuid},
                  {root_uuid~1}, {nic}, {admin}, {kernel})
  plan_change     what a change does to the guest's file: its new content,
                  or its new mode, owner or link target, or its removal
  content_hash    the hash its truth label is keyed on: the fields that
                  decide what the change does, not the prose

The command line checks a set: "incidents.py check eval". Python 3
standard library only.
"""
import argparse
import collections
import hashlib
import json
import os
import re
import sys

REQUIRED = ("id", "path", "change", "applies", "cause", "fix", "guess")
OPTIONAL = ("expect", "source", "harmless")
# What decides the truth: a change to any of these makes the label stale.
# cause, guess and source are prose and leave it as it is.
HASHED = ("id", "path", "change", "applies", "expect", "fix", "harmless")
GUESSES = ("blocker", "error", "warning", "none", "unknown")
APPLIES = ("sudo", "login", "netplan", "sysctl", "udev", "now", "fstab-now",
           "update-grub", "boot", "socket:ssh")
UNIT = re.compile(r"[A-Za-z0-9:_.\\@-]+\.(service|socket|mount|automount|swap|target|timer|path)")
NAME = re.compile(r"[a-z0-9][a-z0-9-]*")

CONTENT_OPS = ("append", "insert", "replace", "delete", "write")
# Each of these is a change of its own: never mixed with another edit.
SINGLE_OPS = ("create", "mode", "owner", "symlink", "unlink")
SURFACES = ("edit", "scd", "check", "restore")

FACTS = ("root_uuid", "nic", "admin", "kernel")
PLACEHOLDER = re.compile(r"\{(root_uuid|nic|admin|kernel)(?:~([1-9]))?\}")
# A "{name~N}" whose name is not a fact is a typo, not a udev ATTR{...}.
LOOKS_LIKE = re.compile(r"\{([A-Za-z_]+)~[0-9]+\}")


class IncidentError(ValueError):
    pass


Action = collections.namedtuple("Action", "kind content mode owner target")


def _fail(where, msg):
    raise IncidentError("%s: %s" % (where, msg))


def _text(where, v, empty=False):
    if not isinstance(v, str) or (not empty and not v):
        _fail(where, "must be a %sstring" % ("" if empty else "non-empty "))
    return v


def _regex(where, v):
    _text(where, v)
    try:
        re.compile(v)
    except re.error as e:
        _fail(where, "not a regular expression: %s" % e)
    return v


def _placeholders(where, v):
    """Refuse a {name~N} for a name that is not a fact, and ~N on a fact
    that is not a UUID."""
    for m in LOOKS_LIKE.finditer(v):
        if m.group(1) not in FACTS:
            _fail(where, "unknown placeholder {%s~...}: the facts are %s" % (m.group(1), ", ".join(FACTS)))
    for m in PLACEHOLDER.finditer(v):
        if m.group(2) and m.group(1) != "root_uuid":
            _fail(where, "{%s~%s}: only {root_uuid} takes ~N" % (m.group(1), m.group(2)))
    return v


def _edit(where, e):
    if not isinstance(e, dict) or len(e) != 1:
        _fail(where, "an edit is an object with one key, one of %s" % ", ".join(CONTENT_OPS + SINGLE_OPS))
    (op, v), = e.items()
    where = "%s.%s" % (where, op)
    if op in ("append", "write"):
        _placeholders(where, _text(where, v, empty=(op == "write")))
    elif op == "insert":
        if not isinstance(v, dict) or set(v) != {"before", "text"}:
            _fail(where, 'must be {"before": REGEX, "text": TEXT}')
        _placeholders(where, _regex(where + ".before", v["before"]))
        _placeholders(where, _text(where + ".text", v["text"]))
    elif op == "replace":
        if not isinstance(v, dict) or not {"pattern", "with"} <= set(v) <= {"pattern", "with", "count"}:
            _fail(where, 'must be {"pattern": REGEX, "with": TEXT} and maybe "count"')
        _placeholders(where, _regex(where + ".pattern", v["pattern"]))
        _placeholders(where, _text(where + ".with", v["with"], empty=True))
        if "count" in v and (type(v["count"]) is not int or v["count"] < 1):
            _fail(where + ".count", "must be a whole number of 1 or more")
    elif op == "delete":
        _placeholders(where, _regex(where, v))
    elif op == "create":
        if isinstance(v, str):
            v = {"content": v}
        if not isinstance(v, dict) or not {"content"} <= set(v) <= {"content", "mode"}:
            _fail(where, 'must be TEXT or {"content": TEXT, "mode": "0644"}')
        _placeholders(where, _text(where + ".content", v["content"], empty=True))
        if "mode" in v:
            _mode(where + ".mode", v["mode"])
    elif op == "mode":
        _mode(where, v)
    elif op == "owner":
        _text(where, v)
        if not re.fullmatch(r"[^:\s]+:[^:\s]+", v):
            _fail(where, 'must be "USER:GROUP"')
        _placeholders(where, v)
    elif op == "symlink":
        _placeholders(where, _text(where, v))
    elif op == "unlink":
        if v is not True:
            _fail(where, "must be true")
    else:
        _fail(where, "unknown edit; the edits are %s" % ", ".join(CONTENT_OPS + SINGLE_OPS))
    return op


def _mode(where, v):
    if not isinstance(v, str) or not re.fullmatch(r"0?[0-7]{3,4}", v) or int(v, 8) > 0o7777:
        _fail(where, 'must be an octal mode as a string, like "0644"')


def _edits(where, v):
    """Check a list of edits; return its kind."""
    if not isinstance(v, list) or not v:
        _fail(where, "must be a non-empty list of edits")
    ops = [_edit("%s[%d]" % (where, i), e) for i, e in enumerate(v)]
    single = [op for op in ops if op in SINGLE_OPS]
    if single and len(ops) > 1:
        _fail(where, "%s is a change of its own: one incident makes one change" % single[0])
    return single[0] if single else "content"


def change_kind(edits):
    """content, create, mode, owner, symlink or unlink."""
    return _edits("change", edits)


def _expect(where, v):
    if not isinstance(v, list):
        _fail(where, "must be a list of probes")
    for i, p in enumerate(v):
        w = "%s[%d]" % (where, i)
        tests = [k for k in ("exit", "match", "no_match") if isinstance(p, dict) and k in p]
        if not isinstance(p, dict) or not {"run", "as_written"} <= set(p) or len(tests) != 1 or \
                set(p) - {"run", "as_written", "exit", "match", "no_match"}:
            _fail(w, 'a probe is {"run": CMD, "as_written": WHAT, and one of "exit": N, "match": REGEX, "no_match": REGEX}')
        _placeholders(w + ".run", _text(w + ".run", p["run"]))
        _text(w + ".as_written", p["as_written"])
        t = tests[0]
        if t == "exit":
            if type(p["exit"]) is not int or not 0 <= p["exit"] <= 255:
                _fail(w + ".exit", "must be an exit status, 0 to 255")
        else:
            _regex("%s.%s" % (w, t), p[t])


def check(inc, where="incident"):
    """Check a parsed incident; return its kind of change."""
    if not isinstance(inc, dict):
        _fail(where, "must be a JSON object")
    missing = [k for k in REQUIRED if k not in inc]
    if missing:
        _fail(where, "missing " + ", ".join(missing))
    extra = sorted(set(inc) - set(REQUIRED) - set(OPTIONAL))
    if extra:
        _fail(where, "unknown field " + ", ".join(extra))
    if not isinstance(inc["id"], str) or not NAME.fullmatch(inc["id"]):
        _fail(where + ".id", "must be lower case letters, digits and dashes")
    p = inc["path"]
    if not isinstance(p, str) or not p.startswith("/") or p == "/" or os.path.normpath(p) != p or "//" in p:
        _fail(where + ".path", "must be an absolute, normal path to a file")
    kind = _edits(where + ".change", inc["change"])
    a = inc["applies"]
    if not isinstance(a, str) or not (a in APPLIES or (a.startswith("restart:") and UNIT.fullmatch(a[8:]))):
        _fail(where + ".applies", "must be one of %s, or restart:UNIT" % ", ".join(APPLIES))
    _expect(where + ".expect", inc.get("expect", []))
    _text(where + ".cause", inc["cause"])
    fix = inc["fix"]
    if fix != "restore":
        _edits(where + ".fix", fix)
    if inc["guess"] not in GUESSES:
        _fail(where + ".guess", "must be one of " + ", ".join(GUESSES))
    if "source" in inc:
        _text(where + ".source", inc["source"])
    if "harmless" in inc:
        if type(inc["harmless"]) is not bool:
            _fail(where + ".harmless", "must be true or false")
        if inc["harmless"] and inc["guess"] != "none":
            _fail(where + ".guess", 'a harmless incident\'s guess is "none"')
    return kind


def load(path):
    """Read and check eval/incidents/<consumer>/<id>.json."""
    where = path
    try:
        with open(path, "rb") as f:
            inc = json.loads(f.read().decode("utf-8"))
    except (OSError, UnicodeDecodeError, ValueError) as e:
        _fail(where, "cannot read it as JSON: %s" % e)
    check(inc, where)
    stem = os.path.basename(path)[:-len(".json")] if path.endswith(".json") else None
    if stem != inc["id"]:
        _fail(where + ".id", "is %r, but the file is named %s" % (inc["id"], os.path.basename(path)))
    consumer = os.path.basename(os.path.dirname(os.path.abspath(path)))
    if not NAME.fullmatch(consumer):
        _fail(where, "its directory %r is not a consumer name" % consumer)
    inc["consumer"] = consumer
    return inc


def load_all(root):
    """Every incident under ROOT/incidents, sorted by consumer and id.
    Anything there but <consumer>/<id>.json is refused, and so is an id
    used twice."""
    top = os.path.join(root, "incidents")
    if not os.path.isdir(top):
        _fail(top, "no such directory")
    out, seen = [], {}
    for consumer in sorted(os.listdir(top)):
        d = os.path.join(top, consumer)
        if not os.path.isdir(d):
            _fail(d, "only consumer directories go in incidents/")
        for name in sorted(os.listdir(d)):
            f = os.path.join(d, name)
            if not name.endswith(".json") or not os.path.isfile(f):
                _fail(f, "only <id>.json files go in a consumer directory")
            inc = load(f)
            if inc["id"] in seen:
                _fail(f, "id %r is used by %s too" % (inc["id"], seen[inc["id"]]))
            seen[inc["id"]] = f
            out.append(inc)
    return out


def content_hash(inc):
    """sha256 of the fields that decide what the incident does, as
    canonical JSON: the same for any key order or spacing in the file."""
    core = {k: inc[k] for k in HASHED if k in inc}
    blob = json.dumps(core, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(blob.encode("utf-8")).hexdigest()


def surfaces(inc, in_scope=True):
    """The sc surfaces that score this incident (docs/M6_PLAN.md, 3 and
    6.1). in_scope: whether scd records the path (sc scope PATH)."""
    kind = change_kind(inc["change"])
    if kind in ("content", "create"):
        s = ["edit", "scd", "check", "restore"]
    elif kind in ("mode", "owner"):
        s = ["scd", "check", "restore"]  # sc edit keeps the mode and owner
    else:
        s = ["scd", "restore"]  # symlink, unlink: no checker reads them
    if not in_scope:
        # No scd row, so nothing for sc restore to put back; sc edit
        # does not look at the scope and keeps its own rows.
        s = [x for x in s if x not in ("scd", "restore")]
    return tuple(s)


def _uuid_off(value, n):
    """VALUE with its last N hex digits each moved on by one."""
    chars, done = list(value), 0
    for i in range(len(chars) - 1, -1, -1):
        if done == n:
            break
        c = chars[i]
        if c in "0123456789abcdefABCDEF":
            d = (int(c, 16) + 1) % 16
            chars[i] = ("%x" if not c.isupper() else "%X") % d
            done += 1
    if done < n:
        raise IncidentError("{root_uuid~%d}: %r has fewer than %d hex digits" % (n, value, n))
    return "".join(chars)


def substitute(text, facts, quote=None):
    """TEXT with each placeholder replaced by the guest's value from
    FACTS. quote, if given, is applied to each value (re.escape for a
    pattern)."""
    def one(m):
        name, n = m.group(1), m.group(2)
        if not facts.get(name):
            raise IncidentError("{%s}: the guest's facts have no %s" % (name, name))
        v = facts[name]
        if n:
            v = _uuid_off(v, int(n))
        return quote(v) if quote else v
    return PLACEHOLDER.sub(one, text)


def _lines(text):
    return text.splitlines(keepends=True)


def _eol(line):
    return line.rstrip("\r\n")


def _as_line(text):
    return text if text.endswith("\n") else text + "\n"


def _apply_content(op, v, text, facts):
    sub = lambda t: substitute(t, facts)  # noqa: E731
    pat = lambda t: substitute(t, facts, quote=re.escape)  # noqa: E731
    if op == "append":
        if text and not text.endswith("\n"):
            text += "\n"
        return text + _as_line(sub(v))
    if op == "write":
        return sub(v)
    if op == "insert":
        rx, lines = re.compile(pat(v["before"])), _lines(text)
        for i, line in enumerate(lines):
            if rx.search(_eol(line)):
                return "".join(lines[:i]) + _as_line(sub(v["text"])) + "".join(lines[i:])
        raise IncidentError("insert: no line matches %r" % v["before"])
    if op == "replace":
        # A value must not be read as a backreference: double its "\".
        repl = substitute(v["with"], facts, quote=lambda s: s.replace("\\", "\\\\"))
        out, n = re.subn(pat(v["pattern"]), repl, text, count=v.get("count", 0), flags=re.M)
        if n == 0:
            raise IncidentError("replace: nothing matches %r" % v["pattern"])
        return out
    if op == "delete":
        rx = re.compile(pat(v))
        lines = _lines(text)
        kept = [line for line in lines if not rx.search(_eol(line))]
        if len(kept) == len(lines):
            raise IncidentError("delete: no line matches %r" % v)
        return "".join(kept)
    raise IncidentError("not a content edit: %s" % op)


def plan_change(edits, base, facts):
    """What EDITS do to a file whose content now is BASE (bytes, or None
    when there is no file), with the guest's FACTS. Returns an Action:
    kind content: content is the new bytes; create: content and mode
    (None: the default); mode: mode as an int; owner: "USER:GROUP";
    symlink: target; unlink: nothing more. An edit that does not fit
    the file (no line matches, or the file is there or not) raises
    IncidentError, and so does a change that changes nothing."""
    kind = _edits("change", edits)
    if kind == "create":
        if base is not None:
            raise IncidentError("create: the file is there already")
        v = edits[0]["create"]
        v = {"content": v} if isinstance(v, str) else v
        mode = int(v["mode"], 8) if "mode" in v else None
        return Action("create", substitute(v["content"], facts).encode("utf-8", "surrogateescape"), mode, None, None)
    if base is None:
        raise IncidentError("%s: there is no file to change (a new file is a create)" % kind)
    (op, v), = edits[0].items()
    if kind == "mode":
        return Action("mode", None, int(v, 8), None, None)
    if kind == "owner":
        return Action("owner", None, None, substitute(v, facts), None)
    if kind == "symlink":
        return Action("symlink", None, None, None, substitute(v, facts))
    if kind == "unlink":
        return Action("unlink", None, None, None, None)
    text = base.decode("utf-8", "surrogateescape")
    for e in edits:
        (op, v), = e.items()
        text = _apply_content(op, v, text, facts)
    out = text.encode("utf-8", "surrogateescape")
    if out == base:
        raise IncidentError("the change leaves the file as it was")
    return Action("content", out, None, None, None)


def main(argv=None):
    ap = argparse.ArgumentParser(description="Check M6's incident set.")
    sub = ap.add_subparsers(dest="cmd", required=True)
    c = sub.add_parser("check", help="read and check every incident under DIR/incidents")
    c.add_argument("dir", help="the set, e.g. eval")
    args = ap.parse_args(argv)
    try:
        incs = load_all(args.dir)
    except IncidentError as e:
        print("incidents: %s" % e, file=sys.stderr)
        return 1
    kinds = collections.Counter(change_kind(i["change"]) for i in incs)
    consumers = sorted({i["consumer"] for i in incs})
    print("%d incidents, %d harmless, in %d consumers (%s); changes: %s" % (
        len(incs), sum(1 for i in incs if i.get("harmless")), len(consumers), ", ".join(consumers),
        ", ".join("%s %d" % kv for kv in sorted(kinds.items()))))
    return 0


if __name__ == "__main__":
    sys.exit(main())
