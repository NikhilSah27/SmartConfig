#!/usr/bin/env python3
"""console.py: read and drive the lab guest's consoles.

Serial (through serialmux.Mux, or a CLI mux's run directory):
  Console.expect    wait for the earliest of some regexes, from a mark
  Console.send      type text, one byte every 15 ms
  Console.key       one menu key (Ctrl-N, Ctrl-P, Enter) in one write
  Console.cmd       run a command in the root shell: its output and status,
                    through the sentinel echo "__SCnnnnnn""_$?__"
GRUB's menu, on serial or on the VGA text screen:
  parse_menu, pick  read the menu; move the highlight to an entry, watching
                    the "*" after every key (closed loop)
  vgatext           an 80-column VGA text dump (pmemsave 0xb8000) as rows
What a boot did, from its serial text:
  classify_boot     how it ended: login, emergency, rescue, panic, grub>,
                    shutdown, and whether that is a known [lab] flake
  observer_lines    the lab's GRUB observer echoes ("sclab: pre ...")
  report_block, match_golden
                    sc's rescue report above systemd's prompt against a
                    golden rendered by Go (lab/testdata/console-*.golden)
  ppm_to_png        a QEMU screendump (P6) as PNG

The command line is for debugging a run directory by hand; "console.py
-h" lists it. Python 3 standard library only.
"""
import argparse
import collections
import os
import random
import re
import socket
import struct
import sys
import time
import zlib

import serialmux

HERE = os.path.dirname(os.path.abspath(__file__))
TESTDATA = os.path.join(HERE, "testdata")

# GRUB menu keys on serial. Ctrl-N and Ctrl-P never put an ESC on the
# wire, so a split arrow sequence cannot turn into a lone ESC (which
# leaves the menu for grub>): the M4 spike (S2) moved the highlight with
# Ctrl-N on OVMF serial. VGA keys are QEMU key names (QMP send-key).
SERIAL_KEYS = {"down": b"\x0e", "up": b"\x10", "enter": b"\r"}
VGA_KEYS = {"down": "down", "up": "up", "enter": "ret"}


def _blen(s):
    return len(s.encode("utf-8", "surrogateescape"))


def _decode(b):
    return b.decode("utf-8", "surrogateescape")


def printable(s):
    """s with the undecodable bytes shown as U+FFFD."""
    return s.encode("utf-8", "surrogateescape").decode("utf-8", "replace")


class Hit:
    """One expect match. start and end are byte offsets in serial.txt;
    before is the text from the search start to the match; t the seconds
    since t0 when it was seen."""

    __slots__ = ("index", "match", "start", "end", "before", "t")

    def __init__(self, index, match, start, end, before, t):
        self.index = index
        self.match = match
        self.start = start
        self.end = end
        self.before = before
        self.t = t

    def group(self, *a):
        return self.match.group(*a)

    @property
    def text(self):
        return printable(self.match.group(0))

    def __repr__(self):
        return "Hit(%d, %r, %d-%d, +%.1f)" % (self.index, self.text[:60], self.start, self.end, self.t)


# rc is None when the sentinel did not come back in time.
CmdResult = collections.namedtuple("CmdResult", "rc out start end")


class Console:
    """The guest's serial console as text: from an in-process mux, or
    from a run directory (serial.txt, input.sock) a CLI mux keeps.

    pos is the expect position, a byte offset in serial.txt. why says
    why the last expect returned None: timeout, closed (the mux stopped:
    QEMU is gone) or abort."""

    def __init__(self, run=None, mux=None, clock=time.time):
        if mux is not None and run is None:
            run = mux.run
        if run is None:
            raise ValueError("Console needs a run directory or a mux")
        self.run = run
        self.mux = mux
        self.clock = clock
        self.t0 = mux.t0 if mux is not None else serialmux.read_t0(run, clock)
        self.pos = 0
        self.why = None
        self._rng = random.SystemRandom()

    # -- reading -----------------------------------------------------------

    def data(self, start=0, end=None):
        """serial.txt's bytes from start (to end)."""
        if self.mux is not None:
            return self.mux.txt(start, end)
        try:
            with open(os.path.join(self.run, "serial.txt"), "rb") as f:
                f.seek(start)
                return f.read() if end is None else f.read(max(0, end - start))
        except FileNotFoundError:
            return b""

    def size(self):
        if self.mux is not None:
            return self.mux.size()
        try:
            return os.path.getsize(os.path.join(self.run, "serial.txt"))
        except FileNotFoundError:
            return 0

    def text(self, start=0, end=None):
        """serial.txt from start as str; bytes that are not UTF-8 are kept
        as surrogates, so len(text.encode('utf-8', 'surrogateescape'))
        is the byte length."""
        return _decode(self.data(start, end))

    def sync(self):
        """pos := the end of the log. Returns it."""
        self.pos = self.size()
        return self.pos

    def line_no(self, offset):
        """The serial.txt line (1-based) a byte offset is on."""
        return self.data(0, offset).count(b"\n") + 1

    def tail(self, n=40):
        """The last n lines of serial.ts."""
        try:
            with open(os.path.join(self.run, "serial.ts"), "rb") as f:
                lines = f.read().decode("utf-8", "replace").split("\n")
        except FileNotFoundError:
            return ""
        if lines and lines[-1] == "":
            lines.pop()
        return "\n".join(lines[-n:])

    def _closed(self):
        return self.mux is not None and self.mux.closed

    def expect(self, patterns, timeout, start=None, advance=True, abort=None):
        """Waits until one of patterns (str regexes, compiled with re.M, or
        compiled patterns) matches serial.txt from start (default: pos).
        The match that starts earliest wins, the lower index on a tie.
        Returns a Hit and, with advance, moves pos past it; returns None
        after timeout seconds, when the mux closes or when abort()
        returns true (why says which)."""
        pats = [p if hasattr(p, "search") else re.compile(p, re.M) for p in patterns]
        if start is None:
            start = self.pos
        deadline = time.monotonic() + timeout
        self.why = None
        while True:
            closed = self._closed()
            size = self.size()
            text = self.text(start)
            best = None
            for i, p in enumerate(pats):
                m = p.search(text)
                if m and (best is None or m.start() < best[1].start()):
                    best = (i, m)
            if best is not None:
                i, m = best
                s = start + _blen(text[:m.start()])
                e = s + _blen(m.group(0))
                if advance:
                    self.pos = e
                return Hit(i, m, s, e, text[:m.start()], self.clock() - self.t0)
            if closed:
                self.why = "closed"
                return None
            if abort is not None and abort():
                self.why = "abort"
                return None
            left = deadline - time.monotonic()
            if left <= 0:
                self.why = "timeout"
                return None
            if self.mux is not None:
                self.mux.wait(size, min(left, 0.5))
                time.sleep(min(0.1, max(0.0, deadline - time.monotonic())))
            else:
                time.sleep(min(left, 0.25))

    # -- writing -----------------------------------------------------------

    def send(self, data, delay=0.015):
        """Types data (bytes or str), one byte every delay seconds (0: in
        one write). Logged to input.log. False if it could not be sent."""
        if isinstance(data, str):
            data = data.encode()
        if self.mux is not None:
            return self.mux.write(data, pace=delay)
        path, fd = serialmux.unix_path(os.path.join(self.run, "input.sock"))
        c = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            c.connect(path)
            if delay:
                for b in data:
                    c.sendall(bytes([b]))
                    time.sleep(delay)
            else:
                c.sendall(data)
            return True
        except OSError:
            return False
        finally:
            c.close()
            if fd is not None:
                os.close(fd)

    def line(self, text, delay=0.015):
        """Types text and Enter."""
        return self.send(text + "\r", delay)

    def key(self, name):
        """One GRUB menu key on serial (down, up, enter) in one write."""
        return self.send(SERIAL_KEYS[name], 0)

    def cmd(self, command, timeout=180, delay=0.005):
        """Runs command in the shell on the console and waits for its
        sentinel: the typed line ends in echo "__SCnnnnnn""_$?__", which
        prints __SCnnnnnn_<status>__ only when it runs. out is what came
        between the typed line and the sentinel. rc is None on timeout
        (out then holds what came so far)."""
        tag = "SC%06d" % self._rng.randrange(1000000)
        start = self.sync()
        if not self.send('%s; echo "__%s""_$?__"\r' % (command, tag), delay):
            return CmdResult(None, "", start, start)
        hit = self.expect([r"__%s_(\d+)__" % tag], timeout, start=start)
        if hit is None:
            return CmdResult(None, self.text(start), start, self.size())
        before = hit.before
        # Drop the echo of the typed line: up to the newline after its
        # tag, or the first newline if readline redrew it beyond recognition.
        i = before.find('%s""_' % tag)
        j = before.find("\n", max(i, 0))
        out = before[j + 1:] if j >= 0 else ""
        return CmdResult(int(hit.group(1)), out, start, hit.end)

    def prepare_shell(self, timeout=180):
        """Sets up a fresh root shell for cmd: "stty cols 200" alone first
        (a long first line would wrap at 80), then no pager, no color, a
        dumb terminal, and kernel messages off the console. Returns the
        first CmdResult that failed, or the last one."""
        r = self.cmd("stty cols 200", timeout)
        if r.rc != 0:
            return r
        return self.cmd("export SYSTEMD_PAGER= SYSTEMD_COLORS=0 TERM=dumb; dmesg -n 1", timeout)


# -- GRUB's menu ---------------------------------------------------------------

# version: "2.12". entries: the titles in menu order. selected: the index
# of the highlighted ("*") entry, or None. countdown: the seconds the first
# "executed automatically in Ns." line said, or None. grub_prompt: a grub>
# prompt came after the menu. gone: a boot started after it.
Menu = collections.namedtuple("Menu", "version entries selected countdown grub_prompt gone")


class MenuError(Exception):
    """pick() could not reach the entry. menu is what was last read."""

    def __init__(self, msg, menu=None, keys=()):
        super().__init__(msg)
        self.menu = menu
        self.keys = list(keys)


_BOX = "\u2500-\u257f"
HEADER = re.compile(r"GNU GRUB +version (\S+)")
# An entry: "*Ubuntu" or " Advanced options for Ubuntu" on serial (after
# Cleaner), " │*Ubuntu    ...    │" in a VGA row.
ENTRY = re.compile(r"^(?: │)?([ *])([^\s%s](?:[^%s]*[^\s%s])?)\s*(?:│\s*)?$" % (_BOX, _BOX, _BOX))
COUNTDOWN = re.compile(r"executed automatically in (\d+)s")
GRUB_PROMPT = re.compile(r"(?:^|\s)grub> ")
# A line after the menu that means a boot has started.
GONE = re.compile(r"^(?:EFI stub: |\[ *\d+\.\d+\] |Booting |Loading Linux|Loading initial ramdisk|"
                  r"SmartConfig rescue: )")


def parse_menu(lines):
    """The GRUB menu after the last "GNU GRUB  version" in lines (a list,
    or text): serial text, where every redraw adds lines (the newest "*"
    line is the highlight), or vgatext() rows. The entries are those drawn
    before the countdown line, so a duplicate title shows. None if no
    menu header is there."""
    if isinstance(lines, str):
        lines = lines.split("\n")
    hdr = None
    for i, line in enumerate(lines):
        m = HEADER.search(line)
        if m:
            hdr = (i, m.group(1))
    if hdr is None:
        return None
    entries = []
    sel = None
    countdown = None
    grub = gone = False
    initial = True
    for line in lines[hdr[0] + 1:]:
        if GONE.match(line):
            gone = True
            break
        if GRUB_PROMPT.search(line):
            grub = True
            continue
        m = COUNTDOWN.search(line)
        if m:
            if countdown is None:
                countdown = int(m.group(1))
            initial = False
            continue
        m = ENTRY.match(line)
        if not m:
            continue
        star, title = m.groups()
        if initial:
            entries.append(title)
        if star == "*":
            sel = title
    selected = entries.index(sel) if sel in entries else None
    return Menu(hdr[1], entries, selected, countdown, grub, gone)


def pick(target, read, press, key_timeout=5.0, resends=1, poll=0.25, max_moves=12,
         sleep=time.sleep, clock=time.monotonic):
    """Moves GRUB's highlight to the one entry titled target, closed loop:
    read() returns the menu now (a Menu or None), press(name) sends a key
    (down, up). After each key it reads at poll intervals until the "*"
    moves; after key_timeout it sends the key again, resends times at
    most. A menu still being drawn (target not listed yet, or no "*" for
    a moment) is read again until key_timeout. Returns the Menu with
    target highlighted; Enter is the caller's. Raises MenuError if the
    menu is gone, a grub> prompt shows, target is not there exactly once,
    or the highlight does not move."""
    keys = []

    def ended(m):
        return m is None or m.gone or m.grub_prompt

    def wait(done):
        deadline = clock() + key_timeout
        m = read()
        while not done(m) and clock() < deadline:
            sleep(poll)
            m = read()
        return m

    menu = wait(lambda m: ended(m) or (target in m.entries and m.selected is not None))
    while True:
        if menu is None or menu.gone:
            raise MenuError("the menu is gone", menu, keys)
        if menu.grub_prompt:
            raise MenuError("a grub> prompt instead of the menu", menu, keys)
        n = menu.entries.count(target)
        if n != 1:
            raise MenuError("%d menu entries are titled %r" % (n, target), menu, keys)
        if menu.selected is None:
            raise MenuError("no entry is highlighted", menu, keys)
        want = menu.entries.index(target)
        if menu.selected == want:
            return menu
        if len(keys) >= max_moves:
            raise MenuError("%r is not highlighted after %d keys" % (target, len(keys)), menu, keys)
        key = "down" if want > menu.selected else "up"
        before = menu.selected

        def moved(m):
            return ended(m) or (m.selected is not None and m.selected != before)

        new = menu
        for _ in range(1 + resends):
            press(key)
            keys.append(key)
            new = wait(moved)
            if moved(new):
                break
        else:
            raise MenuError("the highlight did not move after %d presses of %s" % (1 + resends, key),
                            new, keys)
        menu = new


def serial_menu(console, start):
    """read and press for pick() on the serial console: the menu as the
    text since start (the boot's mark) shows it; keys in single writes."""
    def read():
        return parse_menu(console.text(start))

    def press(name):
        console.key(name)

    return read, press


def vga_menu(dump, sendkey, note=None):
    """read and press for pick() on the VGA screen: dump() returns a
    4000-byte pmemsave of 0xb8000 (the 80x25 text screen; take it through
    QMP, HMP misreads "4000 /path"), sendkey(qcode) presses a QEMU key,
    note(kind, text) logs it (Mux.note)."""
    def read():
        buf = dump()
        return parse_menu(vgatext(buf)) if buf else None

    def press(name):
        key = VGA_KEYS[name]
        if note is not None:
            note("sendkey", key)
        sendkey(key)

    return read, press


# -- VGA text ------------------------------------------------------------------

# CP437's glyphs for the bytes Python's codec decodes as controls (GRUB's
# help line draws its arrows with 0x18 and 0x19).
_CP437_LOW = " ☺☻♥♦♣♠•◘○◙♂♀♪♫☼►◄↕‼¶§▬↨↑↓→←∟↔▲▼"
CP437 = [_CP437_LOW[b] if b < 32 else bytes([b]).decode("cp437") for b in range(256)]
CP437[0x7F] = "⌂"


def vgatext(buf, cols=80):
    """A VGA text-mode dump as rows of text (right-stripped): even bytes
    are CP437 characters, odd bytes attributes; cell (r, c) is at byte
    2*(cols*r + c). 4000 bytes are the 80x25 screen of SeaBIOS and GRUB;
    Linux's console scrolls through the 32 KiB window, so a late check
    dumps all of it and searches."""
    chars = buf[0::2]
    text = "".join(CP437[b] for b in chars)
    return [text[r * cols:(r + 1) * cols].rstrip() for r in range(len(chars) // cols)]


def vgaattrs(buf, cols=80):
    """The attribute bytes of each row of a VGA text dump."""
    attrs = buf[1::2]
    return [attrs[r * cols:(r + 1) * cols] for r in range(len(attrs) // cols)]


# -- screendump ----------------------------------------------------------------

def ppm_to_png(data):
    """A binary PPM (P6, maxval up to 255, as QEMU's screendump writes it)
    as PNG bytes (8-bit RGB). Raises ValueError on anything else."""
    if data[:2] != b"P6":
        raise ValueError("not a binary PPM (P6)")
    pos = 2
    vals = []
    num = re.compile(rb"\d+")
    while len(vals) < 3:
        while pos < len(data) and (data[pos] in b" \t\r\n\v\f" or data[pos] == 0x23):
            if data[pos] == 0x23:  # a comment, to the end of its line
                nl = data.find(b"\n", pos)
                pos = len(data) if nl < 0 else nl + 1
            else:
                pos += 1
        m = num.match(data, pos)
        if not m:
            raise ValueError("bad PPM header")
        vals.append(int(m.group()))
        pos = m.end()
    w, h, maxv = vals
    if pos >= len(data) or data[pos] not in b" \t\r\n\v\f":
        raise ValueError("bad PPM header")
    pos += 1  # exactly one whitespace byte before the pixels
    if not 0 < maxv < 256:
        raise ValueError("PPM maxval %d: only 8-bit is supported" % maxv)
    if w <= 0 or h <= 0:
        raise ValueError("PPM size %dx%d" % (w, h))
    px = data[pos:pos + w * h * 3]
    if len(px) != w * h * 3:
        raise ValueError("PPM pixel data is short")
    if maxv != 255:
        px = bytes(v * 255 // maxv for v in px)
    stride = w * 3
    raw = b"".join(b"\x00" + px[y * stride:(y + 1) * stride] for y in range(h))

    def chunk(kind, body):
        return struct.pack(">I", len(body)) + kind + body + struct.pack(">I", zlib.crc32(kind + body) & 0xFFFFFFFF)

    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(raw, 6)) + chunk(b"IEND", b""))


# -- how a boot ended ----------------------------------------------------------

# kind: login, emergency, rescue, maintenance (a sulogin prompt with no
# "You are in ... mode" before it), panic, grub, shutdown. pos: where in
# the text. line: the line it is on. retry: the [lab] signature that
# allows a retry (panic, slow-udev, grub-prompt) or None. devices: the
# "Timed out waiting for device" descriptions before it. after: the kinds
# seen later in the text (a login after an emergency prompt, a shutdown
# after a login).
BootEnd = collections.namedtuple("BootEnd", "kind pos line retry devices after")

BOOT_EVENTS = (
    ("panic", re.compile(r"Kernel panic - not syncing|IO-APIC \+ timer doesn't work")),
    ("maintenance", re.compile(r"Press Enter for maintenance|Give root password for maintenance")),
    ("grub", re.compile(r"(?:^|\s)grub> ", re.M)),
    ("shutdown", re.compile(r"reboot: (?:Restarting system|Power down|System halted)")),
)
BOOT_MODE = re.compile(r"You are in (rescue|emergency) mode")
TIMED_OUT = re.compile(r"Timed out waiting for device (.*)")
# The devices of a healthy boot that TCG can starve past systemd's 90 s.
LAB_DEVICES = re.compile(r"by-label/(?:BOOT|UEFI)\b|ttyS0")


def classify_boot(text, host="sclab", bad="8f2a-5d6e7f8a9b0c"):
    """How the boot whose serial text (from its mark) is text ended: the
    earliest of a login prompt for host, a maintenance prompt (named by
    the mode line before it), a panic, a grub> prompt or a shutdown.
    None if none of them is there yet. An emergency prompt after device
    timeouts that name only the lab's devices (LABEL=BOOT, LABEL=UEFI,
    ttyS0) and not the bad fstab line's UUID (ending in bad) is the
    slow-udev [lab] signature."""
    found = []
    login = re.compile(r"\b%s login: " % re.escape(host))
    for kind, rx in (("login", login),) + BOOT_EVENTS:
        m = rx.search(text)
        if m:
            found.append((m.start(), kind))
    if not found:
        return None
    found.sort()
    pos, kind = found[0]
    if kind == "maintenance":
        modes = BOOT_MODE.findall(text, 0, pos)
        if modes:
            kind = modes[-1]
    devices = [d.strip() for d in TIMED_OUT.findall(text, 0, pos)]
    retry = None
    if kind == "panic":
        retry = "panic"
    elif kind == "grub":
        retry = "grub-prompt"
    elif kind in ("emergency", "maintenance"):
        if devices and not any(bad in d for d in devices) and any(LAB_DEVICES.search(d) for d in devices):
            retry = "slow-udev"
    s = text.rfind("\n", 0, pos) + 1
    e = text.find("\n", pos)
    line = text[s:] if e < 0 else text[s:e]
    return BootEnd(kind, pos, line, retry, devices, [k for _, k in found[1:]])


# The lab's observers (lab/guest/41_sclab, 43_sclab) echo GRUB's state:
# "sclab: pre platform=efi pending=[1] recordfail=[] timeout=[0] style=[hidden]".
OBSERVER = re.compile(r"sclab: (pre|post)\b([^\n]*)")
_FIELD = re.compile(r"(\w+)=(\[[^\]\n]*\]|\S+)")


def observer_lines(text):
    """The observer echoes in text (serial text or VGA rows joined) as
    dicts: phase (pre or post) and each field, brackets removed."""
    out = []
    if not isinstance(text, str):
        text = "\n".join(text)
    for m in OBSERVER.finditer(text):
        d = {"phase": m.group(1)}
        for f in _FIELD.finditer(m.group(2)):
            v = f.group(2)
            d[f.group(1)] = v[1:-1] if v.startswith("[") and v.endswith("]") else v
        out.append(d)
    return out


# -- sc's rescue report against a golden ---------------------------------------

# lines: the report's lines; stripped: the systemd and kernel status lines
# taken out of it (a W); start, end: str offsets of the block in the text;
# screen: the lines from the report's first through systemd's "Press Enter
# for maintenance" (status lines out; None if that is not there yet), which
# must fit an 80x25 screen (screen_rows(screen) <= 25).
Block = collections.namedtuple("Block", "lines stripped start end screen")

# Lines systemd or the kernel may print into the report while it is written.
STATUS_LINE = re.compile(r"^(?:\[ *(?:OK|FAILED|DEPEND|TIME|INFO|WARN|DONE|SKIP) *\] |\[[ *]{6}\] |"
                         r"\[ *\d+\.\d+\] | {9}[A-Z][a-z]+ )")


def report_block(text, end=r"You are in (?:rescue|emergency) mode", start=r"This boot:"):
    """The rescue report in text: from the last start before the first
    end, up to end, with interleaved status lines taken out. None if
    either is missing."""
    e = re.search(end, text)
    if e is None:
        return None
    s = None
    for s in re.finditer(start, text[:e.start()]):
        pass
    if s is None:
        return None
    kept, stripped = [], []
    for line in text[s.start():e.start()].split("\n"):
        (stripped if STATUS_LINE.match(line) else kept).append(line)
    screen = None
    p = text.find("Press Enter for maintenance", e.start())
    if p >= 0:
        nl = text.find("\n", p)
        screen = [line for line in text[s.start():len(text) if nl < 0 else nl].split("\n")
                  if not STATUS_LINE.match(line)]
    return Block(kept, stripped, s.start(), e.start(), screen)


def screen_rows(lines, width=80):
    """How many rows lines take on a width-column console."""
    return sum(max(1, -(-len(line) // width)) for line in lines)


class GoldenError(Exception):
    """A golden has a placeholder this matcher does not know."""


# Placeholders in a golden (TestStatusConsoleLab writes them): <NAME>.
# Specific names stand for one value: every use must show the same text,
# and a value passed in bind must be that text. Generic names match any
# value of their form each time.
#   <GOOD> <BAD> <PRE> <ID1>..  a store id (6 hex)        specific
#   <ID>                        a store id                generic
#   <B1:8> <B2:8>.. <CUR:8>     a boot id's first 8 hex   specific
#   <B1> .. <CUR>               the same                  specific
#   <BOOT> <BOOT:8>             a boot id's first 8 hex   generic
#   <N> <K>                     a number                  specific
#   <K boots>                   "1 boot" or "K boots"     specific
#   <NUM> <PID>                 a number                  generic
#   <HH:MM> <MM-DD HH:MM> <YYYY-MM-DD HH:MM> (<TIME> <DATE>)   generic
PLACEHOLDERS = (
    (re.compile(r"(?:GOOD|BAD|PRE|ID\d+)$"), r"[0-9a-f]{6}", True),
    (re.compile(r"ID$"), r"[0-9a-f]{6}", False),
    (re.compile(r"(?:CUR|B\d+)(?::8)?$"), r"[0-9a-f]{8}", True),
    (re.compile(r"BOOT(?::8)?$"), r"[0-9a-f]{8}", False),
    (re.compile(r"(?:N|K)$"), r"\d+", True),
    (re.compile(r"K boots$"), r"(?:1 boot|\d+ boots)", True),
    (re.compile(r"(?:NUM|PID)$"), r"\d+", False),
    (re.compile(r"(?:HH:MM|TIME)$"), r"\d\d:\d\d", False),
    (re.compile(r"MM-DD HH:MM$"), r"\d\d-\d\d \d\d:\d\d", False),
    (re.compile(r"(?:YYYY-MM-DD HH:MM|DATE)$"), r"\d{4}-\d\d-\d\d \d\d:\d\d", False),
)
TOKEN = re.compile(r"<([A-Z][A-Za-z0-9 :_-]*)>")

# ok, problems (list of str), values (placeholder name -> text seen).
GoldenResult = collections.namedtuple("GoldenResult", "ok problems values")


def _placeholder(name):
    for rx, pat, same in PLACEHOLDERS:
        if rx.match(name):
            return pat, same
    raise GoldenError("unknown placeholder <%s>" % name)


def short_boot(boot_id):
    """A boot id as sc status shows it: its first 8 hex digits."""
    return boot_id.replace("-", "")[:8]


def _bound(name, bind):
    """The text a placeholder must show, from bind (None: not bound).
    <B1:8> takes bind["B1"] (a whole boot id is shortened), <K boots>
    takes bind["K"]."""
    if name in bind:
        return str(bind[name])
    m = re.match(r"(CUR|B\d+)(?::8)?$", name)
    if m:
        for key in (m.group(1), m.group(1) + ":8"):
            if key in bind:
                return short_boot(str(bind[key]))
    if name == "K boots" and "K" in bind:
        k = int(bind["K"])
        return "1 boot" if k == 1 else "%d boots" % k
    return None


def _group(name):
    return "p_" + re.sub(r"\W", "_", name)


def _golden_line(line, values):
    """A golden line as a regex; values fixes the specific names already
    known. Returns (regex, names it captures)."""
    out, names, at = [], [], 0
    for m in TOKEN.finditer(line):
        out.append(re.escape(line[at:m.start()]))
        name = m.group(1)
        pat, same = _placeholder(name)
        known = _bound(name, values) if same else None
        if known is not None:
            out.append(re.escape(known))
        elif same and name in names:
            out.append("(?P=%s)" % _group(name))
        elif same:
            out.append("(?P<%s>%s)" % (_group(name), pat))
            names.append(name)
        else:
            out.append("(?:%s)" % pat)
        at = m.end()
    out.append(re.escape(line[at:]))
    return re.compile("".join(out)), names


def _trim(lines):
    lines = [line.rstrip() for line in lines]
    while lines and not lines[-1]:
        lines.pop()
    return lines


def golden_lines(golden):
    """A golden's text as lines; checks its placeholders."""
    lines = _trim(golden.split("\n"))
    for line in lines:
        for m in TOKEN.finditer(line):
            _placeholder(m.group(1))
    return lines


def match_golden(lines, golden, bind=None, width=80):
    """Compares the report's lines with a golden (its text) line by line.
    bind fixes specific placeholders to the run's values, e.g. {"GOOD":
    "1d5b0a", "BAD": "c7146c", "B1": <boot id>, "N": 4, "K": 1}; values
    in the result has them and the ones read from the lines. Every line
    must also be at most width columns. Trailing blanks and blank lines
    at the end do not count. The first mismatch ends the comparison."""
    want = golden_lines(golden)
    problems = []
    for i, line in enumerate(lines, 1):
        if len(line.rstrip("\n")) > width:
            problems.append("line %d is %d columns, more than %d: %r" % (i, len(line), width, line))
    got = _trim(lines)
    values = dict(bind or {})
    if len(got) != len(want):
        problems.append("%d lines, the golden has %d" % (len(got), len(want)))
    for i in range(max(len(got), len(want))):
        if i >= len(want):
            problems.append("line %d is not in the golden: %r" % (i + 1, got[i]))
            break
        if i >= len(got):
            problems.append("line %d is missing: %r" % (i + 1, want[i]))
            break
        rx, names = _golden_line(want[i], values)
        m = rx.fullmatch(got[i])
        if m is None:
            problems.append("line %d: expected %r, got %r" % (i + 1, _show(want[i], values), got[i]))
            break
        for name in names:
            values[name] = m.group(_group(name))
    return GoldenResult(not problems, problems, values)


def _show(line, values):
    def sub(m):
        known = _bound(m.group(1), values)
        return m.group(0) if known is None else known
    return TOKEN.sub(sub, line)


def load_golden(name, testdata=TESTDATA):
    """lab/testdata/console-<name>.golden (rescue-a, rescue-b, rescue-c,
    emergency) as text."""
    with open(os.path.join(testdata, "console-%s.golden" % name), encoding="utf-8") as f:
        return f.read()


# -- command line --------------------------------------------------------------

def _pos_file(run):
    return os.path.join(run, "expect.pos")


def _load_pos(con):
    try:
        with open(_pos_file(con.run)) as f:
            con.pos = int(f.read().strip())
    except (OSError, ValueError):
        con.pos = 0


def _save_pos(con):
    with open(_pos_file(con.run), "w") as f:
        f.write("%d\n" % con.pos)


def _unescape(s):
    return s.encode("latin-1", "backslashreplace").decode("unicode_escape").encode("latin-1")


def _read_capture(path):
    with open(path, "rb") as f:
        data = f.read()
    if path.endswith(".raw"):
        data = serialmux.clean(data)
    return _decode(data)


def main(argv=None):
    ap = argparse.ArgumentParser(prog="console.py", description="Debug a lab run's consoles by hand.")
    ap.add_argument("--run", default=os.environ.get("LAB_RUN"), help="the run directory (default $LAB_RUN)")
    sub = ap.add_subparsers(dest="op", required=True)
    p = sub.add_parser("expect", help="wait for a regex after the expect position")
    p.add_argument("-t", type=float, default=120)
    p.add_argument("regex", nargs="+")
    sub.add_parser("sync", help="expect position := end of serial.txt")
    sub.add_parser("since", help="serial.txt from the expect position")
    p = sub.add_parser("tail", help="the last lines of serial.ts")
    p.add_argument("n", nargs="?", type=int, default=40)
    p = sub.add_parser("send", help=r"type TEXT (Python escapes: \r \x0e)")
    p.add_argument("-d", type=int, default=15, help="ms per byte")
    p.add_argument("text")
    p = sub.add_parser("line", help="type TEXT and Enter")
    p.add_argument("text")
    p = sub.add_parser("key", help="a GRUB menu key on serial: down, up, enter")
    p.add_argument("name", choices=sorted(SERIAL_KEYS))
    p = sub.add_parser("cmd", help="run COMMAND in the console's shell")
    p.add_argument("-t", type=float, default=180)
    p.add_argument("command")
    p = sub.add_parser("vga", help="decode a VGA text dump (pmemsave 0xb8000)")
    p.add_argument("file")
    p = sub.add_parser("menu", help="the GRUB menu in a VGA dump (.bin) or a serial capture")
    p.add_argument("file")
    p = sub.add_parser("classify", help="how the boot in a serial capture ended")
    p.add_argument("--host", default="sclab")
    p.add_argument("file")
    p = sub.add_parser("png", help="convert a P6 screendump to PNG")
    p.add_argument("src")
    p.add_argument("dst")
    a = ap.parse_args(argv)

    if a.op == "vga":
        with open(a.file, "rb") as f:
            print("\n".join(vgatext(f.read())))
        return 0
    if a.op == "menu":
        if a.file.endswith(".bin"):
            with open(a.file, "rb") as f:
                menu = parse_menu(vgatext(f.read()))
        else:
            menu = parse_menu(_read_capture(a.file))
        print(menu)
        return 0 if menu else 1
    if a.op == "classify":
        end = classify_boot(_read_capture(a.file), a.host)
        print(end)
        return 0 if end else 1
    if a.op == "png":
        with open(a.src, "rb") as f:
            png = ppm_to_png(f.read())
        with open(a.dst, "wb") as f:
            f.write(png)
        return 0

    if not a.run:
        ap.error("--run DIR or $LAB_RUN is needed for %s" % a.op)
    con = Console(a.run)
    _load_pos(con)
    if a.op == "expect":
        hit = con.expect(a.regex, a.t)
        if hit is None:
            print("%s after %ss waiting for %r; recent output:\n%s"
                  % (con.why, a.t, a.regex, printable(con.text(con.pos))[-600:]))
            return 1
        print("%d +%.1f %s" % (hit.index, hit.t, hit.text[:200]))
        _save_pos(con)
        return 0
    if a.op == "sync":
        con.sync()
        _save_pos(con)
        return 0
    if a.op == "since":
        sys.stdout.write(printable(con.text(con.pos)))
        return 0
    if a.op == "tail":
        print(con.tail(a.n))
        return 0
    if a.op == "send":
        return 0 if con.send(_unescape(a.text), a.d / 1000.0) else 1
    if a.op == "line":
        return 0 if con.line(a.text) else 1
    if a.op == "key":
        return 0 if con.key(a.name) else 1
    if a.op == "cmd":
        r = con.cmd(a.command, a.t)
        _save_pos(con)
        sys.stdout.write(printable(r.out))
        if r.rc is None:
            print("\nTIMEOUT")
            return 124
        return r.rc
    return 2


if __name__ == "__main__":
    sys.exit(main())
