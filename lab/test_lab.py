"""Unit tests for the lab's Python (no QEMU, no network, a few seconds).

  python3 -m unittest discover -s lab

The fixtures in lab/testdata are short excerpts of real console captures
from the M4 research lab and the step 11 spike:

  q5-menu-rescue.raw       OVMF serial: the GRUB menu after a failed boot
                           (recordfail), three Down keys, Enter on
                           SmartConfig rescue, the rescue kernel's command line
  s2-uefi-menu-ctrln.raw   spike S2/S3: the observer echoes with the flag
                           set, the menu, ESC [ B once and Ctrl-N twice
  q2-login-shutdown.raw    a healthy boot's login prompt, then a reboot
  q2-bad-fstab.raw         the bad fstab line: device timeout, emergency
                           prompt, then getty's login prompt as well
  slow-udev-emergency.raw  a healthy boot that TCG starved: timeouts on
                           ttyS0, LABEL=BOOT and LABEL=UEFI, emergency
  q3c-panic-tcg.raw        "IO-APIC + timer doesn't work" under TCG
  vga-bios-*.bin           spike S1: pmemsave 0xb8000 4000 under SeaBIOS
                           (menu with *Ubuntu and 30s, the highlight on
                           Advanced and on SmartConfig rescue, and the
                           hidden-menu boot showing the observer echoes)
The research captures were made with hostname m4lab; the lab's is sclab.
"""
import contextlib
import hashlib
import http.client
import io
import json
import os
import re
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import types
import unittest
from unittest import mock
import zlib
from contextlib import redirect_stdout

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)
sys.dont_write_bytecode = True  # no lab/__pycache__ from the imports below

import console  # noqa: E402
import serialmux  # noqa: E402

try:
    import vm
except ImportError:  # vm.py is written separately; its tests wait for it
    vm = None

TESTDATA = os.path.join(HERE, "testdata")
BAD_UUID = "3f6c1e2a-9b7d-4c1e-8f2a-5d6e7f8a9b0c"


def fixture(name):
    with open(os.path.join(TESTDATA, name), "rb") as f:
        return f.read()


def cleaned(name):
    return serialmux.clean(fixture(name)).decode("utf-8", "surrogateescape")


def has(name):
    return vm is not None and hasattr(vm, name)


class FakeClock:
    """time.monotonic and time.sleep for pick(): sleeping moves time."""

    def __init__(self):
        self.now = 0.0

    def clock(self):
        return self.now

    def sleep(self, s):
        self.now += s


class FakeMux:
    """What Console needs of a Mux, with the guest played by answer():
    it gets every write and returns text for the console to show."""

    def __init__(self, run, text=b"", answer=None):
        self.run = run
        self.t0 = time.time()
        self.closed = False
        self.buf = bytearray(text)
        self.answer = answer
        self.writes = []

    def txt(self, start=0, end=None):
        return bytes(self.buf[start:end])

    def size(self):
        return len(self.buf)

    def wait(self, size, timeout):
        if len(self.buf) <= size:
            time.sleep(min(timeout, 0.01))
        return len(self.buf)

    def write(self, data, pace=0.0, kind="serial"):
        self.writes.append((bytes(data), pace))
        if self.answer is not None:
            self.buf += self.answer(bytes(data))
        return True


# -- serialmux ----------------------------------------------------------------

class TestCleaner(unittest.TestCase):
    def test_controls_and_escapes(self):
        c = serialmux.Cleaner()
        out = c.feed(b"\x1b[0m\x1b[30m\x1b[47mok\r\n\x07a\x08\tb\x7f\x1b7\x1bMc\x1b(Bd\n")
        self.assertEqual(out, b"ok\na\tbcd\n")

    def test_cursor_position_is_a_newline(self):
        out = serialmux.clean(b"\x1b[05;03H Ubuntu  \x1b[06;03H*Advanced options for Ubuntu\x1b[2;1f")
        self.assertEqual(out, b"\n Ubuntu  \n*Advanced options for Ubuntu\n")

    def test_osc_ends_with_bel_or_st(self):
        out = serialmux.clean(b"a\x1b]0;title\x07b\x1b]2;t\x1b\\c")
        self.assertEqual(out, b"abc")

    def test_sequence_split_across_reads(self):
        c = serialmux.Cleaner()
        self.assertEqual(c.feed(b"x\x1b[0"), b"x")
        self.assertEqual(c.feed(b"5;03H*Ubuntu\x1b"), b"\n*Ubuntu")
        self.assertEqual(c.feed(b"[Kz"), b"z")

    def test_an_escape_inside_a_sequence_starts_a_new_one(self):
        # A sequence cut off by a reset, then GRUB's first cursor move.
        self.assertEqual(serialmux.clean(b"a\x1b[1;\x1b[05;03H*Ubuntu"), b"a\n*Ubuntu")

    def test_utf8_passes(self):
        self.assertEqual(serialmux.clean("┌─┐ ▲".encode()), "┌─┐ ▲".encode())

    def test_real_menu_capture(self):
        txt = serialmux.clean(fixture("q5-menu-rescue.raw"))
        self.assertNotIn(b"\x1b", txt)
        self.assertNotIn(b"\r", txt)
        lines = txt.decode().split("\n")
        self.assertIn("GNU GRUB  version 2.12", lines)
        self.assertIn("*SmartConfig rescue", [line.rstrip() for line in lines])
        self.assertIn("SmartConfig rescue: root read-only, /etc/fstab ignored", lines)


class TestStamper(unittest.TestCase):
    def test_stamps_each_line_once(self):
        now = [101.5]
        st = serialmux.Stamper(100.0, clock=lambda: now[0])
        out = st.feed(b"a\nb")
        now[0] = 103.0
        out += st.feed(b"c\n\nd")
        self.assertEqual(out.decode(), "[+    1.50] a\n[+    1.50] bc\n[+    3.00] \n[+    3.00] d")
        self.assertEqual(st.feed(b""), b"")


class TestInputLog(unittest.TestCase):
    LOG = ("+20.40 sendkey down\n+1221.57 serial 1b5b42\n+1225.67 serial 0e\n"
           "+1226.00 sock 611b\n+1227.00 serial 1b4f42\n+1230.00 error  serial: BrokenPipeError\njunk\n")

    def test_parse(self):
        es = serialmux.parse_input_log(self.LOG)
        self.assertEqual(len(es), 6)
        self.assertEqual(es[0], serialmux.Entry(20.4, "sendkey", "down", ""))
        self.assertEqual(es[1].data, "1b5b42")
        self.assertEqual(es[5].kind, "error")

    def test_lone_escapes(self):
        es = serialmux.parse_input_log(self.LOG + "+1300.00 serial 1b\n")
        bad = serialmux.lone_escapes(es)
        self.assertEqual([e.data for e in bad], ["611b", "1b"])
        self.assertEqual(serialmux.lone_escapes(serialmux.parse_input_log("+1.00 serial 0e0e0d\n")), [])


class FakeQemu:
    """QEMU's serial chardev: a unix socket server, one client at a time."""

    def __init__(self, path):
        self.path = path
        self.srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.srv.bind(path)
        self.srv.listen(1)
        self.srv.settimeout(5)
        self.conn = None

    def accept(self):
        self.conn, _ = self.srv.accept()
        self.conn.settimeout(5)
        return self.conn

    def recv(self, n):
        buf = b""
        while len(buf) < n:
            d = self.conn.recv(n - len(buf))
            if not d:
                break
            buf += d
        return buf

    def reset(self):
        """Closes the client connection with a TCP-style reset."""
        self.conn.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
        self.conn.close()
        self.conn = None

    def close(self):
        if self.conn is not None:
            self.conn.close()
        self.srv.close()
        os.unlink(self.path)


def wait_until(cond, timeout=5.0):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if cond():
            return True
        time.sleep(0.01)
    return cond()


class TestMux(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.run = self.tmp.name
        self.qemu = FakeQemu(os.path.join(self.run, "serial.sock"))
        self.mux = None

    def tearDown(self):
        if self.mux is not None:
            self.mux.stop()
        try:
            self.qemu.close()
        except OSError:
            pass
        self.tmp.cleanup()

    def start(self, **kw):
        kw.setdefault("t0", time.time())
        kw.setdefault("reconnect", 0.5)
        self.mux = serialmux.Mux(self.run, **kw).start()
        self.qemu.accept()
        return self.mux

    def read(self, name):
        with open(os.path.join(self.run, name), "rb") as f:
            return f.read()

    def test_gaps(self):
        mux = self.start()
        mux._gap(serialmux.GAP)  # a wake on time
        self.assertEqual(mux.gaps, [])
        mux._gap(596.25)
        self.assertEqual([g[1] for g in mux.gaps], [596.2])
        self.assertIn(b"gap: the mux did not run for 596.2 s", self.read("mux.log"))
        time.sleep(0.5)  # the loop's own wakes, five a second, add none
        self.assertEqual(len(mux.gaps), 1)

    def test_the_loop_measures_its_wakes(self):
        with mock.patch.object(serialmux, "GAP", 0.05):  # every wake (0.2 s with nothing to read) is late then
            mux = self.start()
            self.assertTrue(wait_until(lambda: len(mux.gaps) >= 2))
        self.assertIn(b"gap: the mux did not run for 0.2 s", self.read("mux.log"))

    def test_a_wall_clock_jump_alone_is_logged_not_kept(self):
        mux = self.start()
        # A wake as late as allowed on both clocks: the wall clock is not ahead.
        mux._gap(serialmux.GAP, serialmux.GAP + 1)
        self.assertNotIn(b"pause", self.read("mux.log"))
        mux._gap(0.2, 3120.0)  # a 52 minute hole the monotonic clock did not count
        self.assertEqual(mux.gaps, [])
        self.assertIn(b"pause: the wall clock jumped 3119.8 s", self.read("mux.log"))

    def test_the_loop_measures_the_wall_clock_too(self):
        # A wall clock that is an hour on at every look, under a monotonic
        # clock that is not: what a suspended machine shows the loop.
        jump = [0.0]

        def wall():
            jump[0] += 3600.0
            return jump[0]

        clocks = types.SimpleNamespace(monotonic=time.monotonic, sleep=time.sleep, time=wall)
        with mock.patch.object(serialmux, "time", clocks):
            mux = self.start()
            # An hour, less the 0.2 s or so the wake took on both clocks.
            said = re.compile(rb"pause: the wall clock jumped 3599\.\d s")
            self.assertTrue(wait_until(lambda: said.search(self.read("mux.log"))))
            mux.stop()
        self.assertEqual(mux.gaps, [])

    def test_a_send_cannot_block_for_ever(self):
        # A guest that stops reading fills the socket: without a timeout
        # the write, and with it the lab, would wait on it with no end.
        mux = self.start()
        self.assertEqual(mux._ser.gettimeout(), 10)
        self.qemu.reset()
        self.qemu.accept()
        self.assertTrue(wait_until(lambda: b"reconnected" in self.read("mux.log")))
        self.assertEqual(mux._ser.gettimeout(), 10)  # the new connection as well

    def test_a_mark_waits_for_what_is_unread(self):
        # The old boot's last line, and the mark taken right behind it
        # from another thread: the line belongs before the mark.
        mux = self.start()
        sent = 0
        for i in range(20):
            line = b"[  %d.0] reboot: Restarting system\n" % i
            self.qemu.conn.sendall(line)
            sent += len(line)
            self.assertEqual(mux.mark("RESET#%d" % i, drain=30.0).txt, sent, i)  # as long as a loaded host needs
        self.assertEqual(mux.mark("x", drain=0).txt, sent)

    def test_a_mark_with_nothing_unread_does_not_wait(self):
        mux = self.start()
        t = time.monotonic()
        mux.mark("RESET", drain=120.0)  # the loop's next idle wake, a fifth of a second away, ends the wait
        self.assertLess(time.monotonic() - t, 60)

    def test_wait_waits(self):
        # Console.expect polls with it: one that came back at once would
        # spin. No loop runs here, so nothing but the text can end it.
        mux = serialmux.Mux(self.run, t0=time.time())
        t = time.monotonic()
        self.assertEqual(mux.wait(0, 0.25), 0)
        self.assertGreaterEqual(time.monotonic() - t, 0.2)
        timer = threading.Timer(0.05, mux._ingest, [b"hi\n"])
        timer.start()
        self.addCleanup(timer.join)
        self.assertEqual(mux.wait(0, 30), 3)
        self.assertEqual(mux.wait(0, 30), 3)  # longer already: no wait
        mux._close("gone")
        self.assertEqual(mux.wait(3, 30), 3)  # closed: nothing more will come

    def test_logs_and_forwards(self):
        mux = self.start(listen=True)
        raw = b"\x1b[0mBdsDxe: starting\r\n\x1b[05;03H*Ubuntu  \x1b[06;03H Advanced\r\n"
        self.qemu.conn.sendall(raw)
        self.assertTrue(wait_until(lambda: mux.size() >= 36))
        self.assertEqual(mux.raw(), raw)
        self.assertEqual(mux.txt(), b"BdsDxe: starting\n\n*Ubuntu  \n Advanced\n")
        self.assertTrue(wait_until(lambda: self.read("serial.txt") == mux.txt()))
        self.assertEqual(self.read("serial.raw"), raw)
        ts = self.read("serial.ts").decode().split("\n")
        self.assertTrue(all(re.match(r"\[\+ *\d+\.\d\d\] ", line) for line in ts[:-1]), ts)

        m = mux.mark("RESET")
        self.assertEqual((m.raw, m.txt, m.inp), (len(raw), mux.size(), 0))
        self.assertTrue(mux.write(b"\x0e"))
        self.assertEqual(self.qemu.recv(1), b"\x0e")
        self.assertTrue(mux.write(b"ls\r", pace=0.001))
        self.assertEqual(self.qemu.recv(3), b"ls\r")
        mux.note("sendkey", "down")

        cli = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        cli.connect(os.path.join(self.run, "input.sock"))
        cli.sendall(b"ab")
        self.assertEqual(self.qemu.recv(2), b"ab")
        cli.close()

        kinds = [(e.kind, e.data) for e in mux.inputs(m.inp)]
        self.assertEqual(kinds, [("serial", "0e"), ("serial", "6c730d"), ("sendkey", "down"), ("sock", "6162")])
        log = serialmux.parse_input_log(self.read("input.log").decode())
        self.assertEqual([(e.kind, e.data) for e in log], kinds)
        self.assertRegex(self.read("marks.log").decode(),
                         r"^\+\d+\.\d\d RESET raw=%d txt=%d input=0\n$" % (m.raw, m.txt))

    def test_reconnects_after_a_reset(self):
        mux = self.start()
        self.qemu.conn.sendall(b"one\n")
        self.assertTrue(wait_until(lambda: mux.size() == 4))
        self.qemu.reset()
        self.qemu.accept()  # the mux comes back
        self.qemu.conn.sendall(b"two\n")
        self.assertTrue(wait_until(lambda: mux.txt() == b"one\ntwo\n"))
        self.assertTrue(mux.alive())
        self.assertTrue(mux.write(b"x"))
        self.assertEqual(self.qemu.recv(1), b"x")
        self.assertIn(b"reconnected", self.read("mux.log"))

    def test_qemu_gone(self):
        mux = self.start()
        self.qemu.reset()
        self.qemu.close()  # no reconnect possible
        self.assertTrue(wait_until(lambda: mux.closed, 5))
        self.assertIn("QEMU closed", mux.why)
        self.assertFalse(mux.write(b"\r"))  # no exception
        errors = [e for e in mux.inputs() if e.kind == "error"]
        self.assertEqual(len(errors), 1)
        self.assertIn("not connected", errors[0].note)

    def test_write_to_a_dead_peer(self):
        mux = self.start()
        self.qemu.reset()
        # Until the mux notices, a write may hit the dead socket: it must
        # come back False or True, never raise.
        for _ in range(3):
            mux.write(b"\x0e")
        mux.stop()
        self.assertTrue(mux.closed)
        self.assertFalse(mux.write(b"\x0e"))

    def test_client_reset_keeps_the_mux(self):
        mux = self.start(listen=True)
        cli = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        cli.connect(os.path.join(self.run, "input.sock"))
        cli.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
        cli.close()
        time.sleep(0.3)
        self.assertTrue(mux.alive())
        self.qemu.conn.sendall(b"still\n")
        self.assertTrue(wait_until(lambda: mux.txt() == b"still\n"))

    def test_no_socket(self):
        self.qemu.close()
        mux = serialmux.Mux(self.run, t0=time.time(), connect_timeout=0.3)
        with self.assertRaises(serialmux.MuxError):
            mux.start()
        self.assertTrue(mux.closed)

    def test_console_on_a_live_mux(self):
        mux = self.start()
        con = console.Console(mux=mux)
        mark = mux.mark("RESET")

        def guest():
            time.sleep(0.2)
            self.qemu.conn.sendall(b"\r\nUbuntu 24.04.3 LTS sclab ttyS0\r\n\r\nsclab login: ")

        threading.Thread(target=guest, daemon=True).start()
        hit = con.expect([r"Press Enter for maintenance", r"\bsclab login: "], 5, start=mark.txt)
        self.assertIsNotNone(hit, con.why)
        self.assertEqual(hit.index, 1)
        self.assertEqual(con.pos, mux.size())

    def test_long_socket_path(self):
        deep = os.path.join(self.run, "d" * 60, "e" * 60)
        os.makedirs(deep)
        q = FakeQemu(os.path.join(os.path.dirname(deep), "s.sock"))  # bind short, then link
        os.rename(q.path, os.path.join(deep, "serial.sock"))
        q.path = os.path.join(deep, "serial.sock")
        try:
            mux = serialmux.Mux(deep, t0=time.time()).start()
            q.accept()
            q.conn.sendall(b"hi\n")
            self.assertTrue(wait_until(lambda: mux.txt() == b"hi\n"))
            mux.stop()
        finally:
            q.close()

    def test_command_line_mux(self):
        with open(os.path.join(self.run, "t0"), "w") as f:
            f.write("%f\n" % time.time())
        env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
        proc = subprocess.Popen([sys.executable, os.path.join(HERE, "serialmux.py"), self.run], env=env,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        try:
            self.qemu.accept()
            self.qemu.conn.sendall(b"\x1b[0mUbuntu 24.04.3 LTS sclab ttyS0\r\n\r\nsclab login: ")
            sock = os.path.join(self.run, "input.sock")
            self.assertTrue(wait_until(lambda: os.path.exists(sock)))
            with redirect_stdout(io.StringIO()) as out:
                rc = console.main(["--run", self.run, "expect", "-t", "5", r"\bsclab login: "])
            self.assertEqual(rc, 0, out.getvalue())
            self.assertRegex(out.getvalue(), r"^0 \+\d+\.\d sclab login: ")
            self.assertEqual(console.main(["--run", self.run, "key", "down"]), 0)
            self.assertEqual(self.qemu.recv(1), b"\x0e")
            self.assertEqual(console.main(["--run", self.run, "send", "-d", "1", r"ab\r"]), 0)
            self.assertEqual(self.qemu.recv(3), b"ab\r")
        finally:
            proc.terminate()
            proc.wait(10)
        self.assertEqual(proc.returncode, 0, proc.stdout.read())
        proc.stdout.close()
        self.assertFalse(os.path.exists(os.path.join(self.run, "input.sock")))
        log = serialmux.parse_input_log(self.read("input.log").decode())
        self.assertEqual(log[0], serialmux.Entry(log[0].t, "sock", "0e", ""))
        self.assertEqual("".join(e.data for e in log[1:]), "61620d")
        self.assertIn(b"sclab login: ", self.read("serial.txt"))
        with open(os.path.join(self.run, "expect.pos")) as f:
            self.assertEqual(int(f.read()), len(self.read("serial.txt")))

    def test_clean_cli(self):
        p = os.path.join(self.run, "x.raw")
        with open(p, "wb") as f:
            f.write(b"\x1b[1ma\r\n")
        out = subprocess.run([sys.executable, "-B", os.path.join(HERE, "serialmux.py"), "--clean", p],
                             capture_output=True, check=True)
        self.assertEqual(out.stdout, b"a\n")


# -- console: expect, send, cmd -------------------------------------------------

class TestConsole(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.run = self.tmp.name

    def tearDown(self):
        self.tmp.cleanup()

    def write_txt(self, data):
        with open(os.path.join(self.run, "serial.txt"), "wb") as f:
            f.write(data)

    def test_expect_earliest_wins(self):
        self.write_txt("┌──┐ boot\nPress Enter for maintenance\nsclab login: ".encode())
        con = console.Console(self.run)
        hit = con.expect([r"\bsclab login: ", r"Press Enter for maintenance"], 1)
        self.assertEqual(hit.index, 1)
        self.assertEqual(hit.start, len("┌──┐ boot\n".encode()))
        self.assertEqual(con.pos, hit.end)
        self.assertEqual(con.data(hit.start, hit.end), b"Press Enter for maintenance")
        self.assertEqual(con.line_no(hit.start), 2)
        hit = con.expect([r"\bsclab login: ", r"Press Enter"], 1)  # from pos on
        self.assertEqual(hit.index, 0)

    def test_expect_tie_goes_to_the_lower_index(self):
        self.write_txt(b"Kernel panic - not syncing: IO-APIC + timer doesn't work!\n")
        hit = console.Console(self.run).expect([r"Kernel panic", r"Kernel"], 1)
        self.assertEqual(hit.index, 0)

    def test_expect_from_a_mark_and_bytes_offsets(self):
        self.write_txt(b"old boot: sclab login: \n\xff\xfe new boot\nsclab login: ")
        con = console.Console(self.run)
        mark = len(b"old boot: sclab login: \n")
        hit = con.expect([r"\bsclab login: "], 1, start=mark, advance=False)
        self.assertEqual(con.pos, 0)
        self.assertEqual(con.data(hit.start, hit.end), b"sclab login: ")
        self.assertIn("new boot", hit.before)

    def test_expect_timeout_and_closed(self):
        self.write_txt(b"nothing here\n")
        con = console.Console(self.run)
        t = time.monotonic()
        self.assertIsNone(con.expect([r"login: "], 0.3))
        self.assertEqual(con.why, "timeout")
        self.assertLess(time.monotonic() - t, 2)
        mux = FakeMux(self.run, b"nothing\n")
        mux.closed = True
        con = console.Console(mux=mux)
        self.assertIsNone(con.expect([r"login: "], 30))
        self.assertEqual(con.why, "closed")
        self.assertIsNone(con.expect([r"login: "], 30, abort=lambda: True))
        mux.closed = False
        self.assertIsNone(con.expect([r"login: "], 30, abort=lambda: True))
        self.assertEqual(con.why, "abort")

    def test_expect_reads_what_came_before_the_close(self):
        # The guest's last line and the close arrive between two looks:
        # the text read after "closed" was seen false is searched once
        # more before expect gives up.
        class Closing(FakeMux):
            def txt(self, start=0, end=None):
                out = super().txt(start, end)
                if not self.closed:
                    self.buf += b"sclab login: "
                    self.closed = True
                return out

        con = console.Console(mux=Closing(self.run, b"[  OK  ] Reached target\n"))
        hit = con.expect([r"\bsclab login: "], 5)
        self.assertIsNotNone(hit, con.why)
        self.assertEqual(con.pos, con.size())
        self.assertIsNone(con.expect([r"\bsclab login: "], 5))  # nothing more will come
        self.assertEqual(con.why, "closed")

    def test_expect_patterns_are_per_line(self):
        # re.M without re.S: ^ is a line start, . does not cross lines.
        self.write_txt(b"Timed out waiting for device x\nsomething 8f2a-5d6e7f8a9b0c\n^B1 seen\n")
        con = console.Console(self.run)
        self.assertIsNone(con.expect([r"Timed out waiting for device .*8f2a-5d6e7f8a9b0c"], 0.1))
        self.assertIsNotNone(con.expect([r"^something"], 0.1))

    def test_send_pacing_and_keys(self):
        mux = FakeMux(self.run)
        con = console.Console(mux=mux)
        con.send("ab")
        con.line("ls")
        con.key("down")
        con.key("up")
        con.key("enter")
        self.assertEqual(mux.writes, [(b"ab", 0.015), (b"ls\r", 0.015), (b"\x0e", 0), (b"\x10", 0), (b"\r", 0)])
        self.assertNotIn(b"\x1b", b"".join(console.SERIAL_KEYS.values()))

    def test_send_through_input_sock(self):
        path = os.path.join(self.run, "input.sock")
        srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        srv.bind(path)
        srv.listen(1)
        got = []

        def serve():
            c, _ = srv.accept()
            buf = b""
            while True:
                d = c.recv(10)
                if not d:
                    break
                buf += d
            c.close()
            got.append(buf)

        t = threading.Thread(target=serve, daemon=True)
        t.start()
        self.assertTrue(console.Console(self.run).send(b"hi\r", 0.001))
        t.join(5)
        srv.close()
        self.assertEqual(got, [b"hi\r"])
        self.assertFalse(console.Console(self.run).send(b"x"))  # no mux listening

    @staticmethod
    def shell(rc=0, out="file1\nfile2\n", garble=False):
        """A root shell on ttyS0: echoes the typed line, prints out, then
        runs the sentinel echo."""
        def answer(data):
            line = data.decode()
            m = re.search(r'echo "__(SC\d{6})""_\$\?__"\r$', line)
            if not m:
                return b""
            echo = line.rstrip("\r")
            if garble:
                echo = "<" + echo[-30:-14] + "  " + echo[-14:]
            return ("root@sclab:~# %s\n%s__%s_%d__\nroot@sclab:~# " % (echo, out, m.group(1), rc)).encode()
        return answer

    def test_cmd(self):
        mux = FakeMux(self.run, b"boot noise\n", self.shell())
        con = console.Console(mux=mux)
        r = con.cmd("ls /", timeout=2)
        self.assertEqual((r.rc, r.out), (0, "file1\nfile2\n"))
        data, pace = mux.writes[0]
        self.assertRegex(data.decode(), r'^ls /; echo "__SC\d{6}""_\$\?__"\r$')
        self.assertEqual(pace, 0.005)
        self.assertEqual(con.pos, r.end)
        mux.answer = self.shell(rc=2, out="")
        r2 = con.cmd("sc check /etc/fstab", timeout=2)
        self.assertEqual((r2.rc, r2.out), (2, ""))
        self.assertNotEqual(mux.writes[0][0], mux.writes[1][0])  # a new tag each time

    def test_cmd_with_a_garbled_echo(self):
        mux = FakeMux(self.run, b"", self.shell(garble=True))
        r = console.Console(mux=mux).cmd("x" * 250, timeout=2)
        self.assertEqual((r.rc, r.out), (0, "file1\nfile2\n"))

    def test_cmd_reads_from_the_end_of_the_log(self):
        # With the echo garbled the output starts at the first newline
        # after the typed line: of what came since, not of the old text.
        old = b"old line 1\nold line 2\n"
        mux = FakeMux(self.run, old, self.shell(garble=True))
        con = console.Console(mux=mux)
        self.assertEqual(con.pos, 0)
        r = con.cmd("x" * 250, timeout=2)
        self.assertEqual((r.rc, r.out, r.start), (0, "file1\nfile2\n", len(old)))

    def test_cmd_timeout(self):
        mux = FakeMux(self.run, b"# ", lambda d: b"typed but never run\n")
        r = console.Console(mux=mux).cmd("sleep 999", timeout=0.3)
        self.assertIsNone(r.rc)
        self.assertIn("never run", r.out)

    def test_prepare_shell(self):
        mux = FakeMux(self.run, b"", self.shell(out=""))
        r = console.Console(mux=mux).prepare_shell(timeout=2)
        self.assertEqual(r.rc, 0)
        first, second = (w[0].decode() for w in mux.writes)
        self.assertTrue(first.startswith("stty cols 200; echo "))
        self.assertTrue(second.startswith("export SYSTEMD_PAGER= SYSTEMD_COLORS=0 TERM=dumb; dmesg -n 1; echo "))

    def test_tail(self):
        with open(os.path.join(self.run, "serial.ts"), "w") as f:
            f.write("".join("[+ %d] line %d\n" % (i, i) for i in range(50)))
        self.assertEqual(console.Console(self.run).tail(2), "[+ 48] line 48\n[+ 49] line 49")


# -- GRUB's menu ---------------------------------------------------------------

def q5_views():
    """The q5 capture as the console saw it: the menu, then after each of
    three Down keys, then after Enter."""
    raw = fixture("q5-menu-rescue.raw")
    cuts = [raw.find(b"*Advanced"), raw.find(b"*UEFI"), raw.find(b"*SmartConfig rescue")]
    cuts.append(raw.find(b"\x1b[2J", cuts[-1]))
    assert all(c > 0 for c in cuts) and cuts == sorted(cuts), cuts
    return raw, cuts + [len(raw)]


class TestMenu(unittest.TestCase):
    def test_uefi_serial_menu(self):
        raw, cuts = q5_views()
        menu = console.parse_menu(serialmux.clean(raw[:cuts[0]]).decode())
        self.assertEqual(menu.version, "2.12")
        self.assertEqual(menu.entries, ["Ubuntu", "Advanced options for Ubuntu", "UEFI Firmware Settings",
                                        "SmartConfig rescue"])
        self.assertEqual((menu.selected, menu.countdown, menu.grub_prompt, menu.gone), (0, 30, False, False))
        for i, cut in enumerate(cuts[1:4], 1):
            self.assertEqual(console.parse_menu(serialmux.clean(raw[:cut]).decode()).selected, i)
        menu = console.parse_menu(cleaned("q5-menu-rescue.raw"))
        self.assertEqual((menu.selected, menu.gone), (3, True))

    def test_ctrl_n_capture(self):
        menu = console.parse_menu(cleaned("s2-uefi-menu-ctrln.raw"))
        self.assertEqual(len(menu.entries), 4)
        self.assertEqual(menu.entries.count("SmartConfig rescue"), 1)
        self.assertEqual((menu.selected, menu.gone), (3, True))

    def test_bios_vga_menu(self):
        for name, sel, countdown in (("vga-bios-menu.bin", 0, 30), ("vga-bios-menu-adv.bin", 1, None),
                                     ("vga-bios-menu-rescue.bin", 2, None)):
            menu = console.parse_menu(console.vgatext(fixture(name)))
            self.assertEqual(menu.entries, ["Ubuntu", "Advanced options for Ubuntu", "SmartConfig rescue"], name)
            self.assertEqual((menu.selected, menu.countdown, menu.gone), (sel, countdown, False), name)
        self.assertIsNone(console.parse_menu(console.vgatext(fixture("vga-bios-hidden.bin"))))

    def test_duplicate_and_grub_prompt(self):
        lines = ["GNU GRUB  version 2.12", " │*Ubuntu      │", " │ SmartConfig rescue  │",
                 " │ SmartConfig rescue │", "   The highlighted entry will be executed automatically in 30s."]
        menu = console.parse_menu(lines)
        self.assertEqual(menu.entries.count("SmartConfig rescue"), 2)
        with self.assertRaisesRegex(console.MenuError, "2 menu entries"):
            console.pick("SmartConfig rescue", lambda: menu, lambda k: None)
        menu = console.parse_menu(cleaned("q5-menu-rescue.raw").split("SmartConfig rescue: ")[0] + "\ngrub> ")
        self.assertTrue(menu.grub_prompt)
        with self.assertRaisesRegex(console.MenuError, "grub>"):
            console.pick("SmartConfig rescue", lambda: menu, lambda k: None)

    def test_the_last_header_is_the_menu(self):
        # Two boots in one text (a reset), or a submenu drawn after the
        # menu: the menu on the screen is the one under the last header.
        lines = ["GNU GRUB  version 2.06", "*Ubuntu", " Memory test", "",
                 "   The highlighted entry will be executed automatically in 5s.",
                 "GNU GRUB  version 2.12", " Ubuntu", "*SmartConfig rescue"]
        menu = console.parse_menu(lines)
        self.assertEqual((menu.version, menu.entries, menu.selected, menu.countdown),
                         ("2.12", ["Ubuntu", "SmartConfig rescue"], 1, None))

    def test_not_a_menu(self):
        self.assertIsNone(console.parse_menu(cleaned("q2-bad-fstab.raw")))
        # Help, countdown, box and header lines are no entries.
        menu = console.parse_menu(console.vgatext(fixture("vga-bios-menu.bin")))
        self.assertNotIn("Use the", " ".join(menu.entries))


class TestPick(unittest.TestCase):
    def test_serial_pick_follows_the_star(self):
        raw, cuts = q5_views()
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        view = [0]

        def guest(data):  # each key shows the next redraw of the capture
            before = cuts[view[0]]
            if data in (b"\x0e", b"\r"):
                view[0] = len(cuts) - 1 if data == b"\r" else view[0] + 1
            return serialmux.clean(raw[before:cuts[view[0]]])

        mux = FakeMux(tmp.name, serialmux.clean(raw[:cuts[0]]), guest)
        con = console.Console(mux=mux)
        read, press = console.serial_menu(con, 0)
        fc = FakeClock()
        menu = console.pick("SmartConfig rescue", read, press, sleep=fc.sleep, clock=fc.clock)
        self.assertEqual(menu.selected, 3)
        self.assertEqual([w for w, _ in mux.writes], [b"\x0e"] * 3)
        con.key("enter")
        hit = con.expect([r"SmartConfig rescue: root read-only, /etc/fstab ignored"], 1, start=0)
        self.assertIsNotNone(hit)
        self.assertIsNotNone(con.expect([r"Command line: .* ro .*fstab=no systemd\.unit=rescue\.target"], 1))

    def vga(self, screens, moves):
        """read and press over VGA dumps: moves[i] is how far key i moves
        the highlight (0: the key was lost)."""
        state = {"i": 0, "keys": [], "notes": []}

        def dump():
            return screens[state["i"]]

        def sendkey(k):
            n = moves[len(state["keys"])] if len(state["keys"]) < len(moves) else 1
            state["keys"].append(k)
            state["i"] = max(0, min(len(screens) - 1, state["i"] + (n if k == "down" else -n)))

        read, press = console.vga_menu(dump, sendkey, lambda kind, text: state["notes"].append((kind, text)))
        return read, press, state

    SCREENS = ("vga-bios-menu.bin", "vga-bios-menu-adv.bin", "vga-bios-menu-rescue.bin")

    def test_vga_pick(self):
        screens = [fixture(n) for n in self.SCREENS]
        read, press, st = self.vga(screens, [1, 1])
        fc = FakeClock()
        menu = console.pick("SmartConfig rescue", read, press, sleep=fc.sleep, clock=fc.clock)
        self.assertEqual(menu.selected, 2)
        self.assertEqual(st["keys"], ["down", "down"])
        self.assertEqual(st["notes"], [("sendkey", "down")] * 2)
        self.assertLess(fc.now, 2)

    def test_lost_key_is_sent_once_more(self):
        screens = [fixture(n) for n in self.SCREENS]
        read, press, st = self.vga(screens, [0, 1, 1])
        fc = FakeClock()
        menu = console.pick("SmartConfig rescue", read, press, sleep=fc.sleep, clock=fc.clock)
        self.assertEqual(menu.selected, 2)
        self.assertEqual(st["keys"], ["down"] * 3)
        self.assertGreaterEqual(fc.now, 5)  # waited the key's 5 s first

    def test_two_lost_keys_fail(self):
        screens = [fixture(n) for n in self.SCREENS]
        read, press, _ = self.vga(screens, [0, 0])
        fc = FakeClock()
        with self.assertRaisesRegex(console.MenuError, "did not move after 2 presses") as cm:
            console.pick("SmartConfig rescue", read, press, sleep=fc.sleep, clock=fc.clock)
        self.assertEqual(cm.exception.keys, ["down", "down"])

    def test_a_highlight_that_never_arrives_ends_at_max_moves(self):
        # Every key moves the highlight, but between the first two entries
        # only: pick stops after max_moves keys instead of pressing on.
        entries = ["Ubuntu", "Advanced options for Ubuntu", "SmartConfig rescue"]
        keys = []

        def press(k):
            self.assertLess(len(keys), 4, "a key after max_moves")
            keys.append(k)

        fc = FakeClock()
        with self.assertRaisesRegex(console.MenuError, "'SmartConfig rescue' is not highlighted after 4 keys") as cm:
            console.pick("SmartConfig rescue", lambda: console.Menu("2.12", entries, len(keys) % 2, None, False, False),
                         press, max_moves=4, sleep=fc.sleep, clock=fc.clock)
        self.assertEqual((keys, cm.exception.keys), (["down"] * 4, ["down"] * 4))

    def test_overshoot_comes_back_up(self):
        screens = [fixture(n) for n in self.SCREENS]
        read, press, st = self.vga(screens, [2, 1])
        fc = FakeClock()
        menu = console.pick("Advanced options for Ubuntu", read, press, sleep=fc.sleep, clock=fc.clock)
        self.assertEqual(menu.selected, 1)
        self.assertEqual(st["keys"], ["down", "up"])
        self.assertEqual(st["notes"][-1], ("sendkey", "up"))

    def test_menu_gone(self):
        screens = [fixture("vga-bios-menu.bin"), fixture("vga-bios-hidden.bin")]
        read, press, _ = self.vga(screens, [1])
        fc = FakeClock()
        with self.assertRaisesRegex(console.MenuError, "gone"):
            console.pick("SmartConfig rescue", read, press, sleep=fc.sleep, clock=fc.clock)
        fc = FakeClock()
        with self.assertRaisesRegex(console.MenuError, "0 menu entries"):
            console.pick("Windows", lambda: console.parse_menu(console.vgatext(screens[0])), press,
                         sleep=fc.sleep, clock=fc.clock)
        self.assertGreaterEqual(fc.now, 5)  # it waited for the menu to be drawn

    def test_half_drawn_menu(self):
        # A VGA dump between GRUB's two writes: the old entry plain, the new
        # one not highlighted yet. And a first read before the last entry.
        menu0, adv = fixture("vga-bios-menu.bin"), fixture("vga-bios-menu-adv.bin")
        between = bytearray(menu0)
        between[2 * (80 * 4 + 2)] = ord(" ")
        partial = bytearray(menu0)
        for r in (5, 6):
            for c in range(2, 78):
                partial[2 * (80 * r + c)] = ord(" ")
        self.assertIsNone(console.parse_menu(console.vgatext(bytes(between))).selected)
        self.assertEqual(console.parse_menu(console.vgatext(bytes(partial))).entries, ["Ubuntu"])
        seq = [bytes(partial), menu0, bytes(between), bytes(between), adv]
        state = {"i": 0, "keys": []}

        def read():
            buf = seq[min(state["i"], len(seq) - 1)]
            state["i"] += 1
            return console.parse_menu(console.vgatext(buf))

        fc = FakeClock()
        menu = console.pick("Advanced options for Ubuntu", read, state["keys"].append, sleep=fc.sleep,
                            clock=fc.clock)
        self.assertEqual((menu.selected, state["keys"]), (1, ["down"]))


# -- VGA and screendumps -------------------------------------------------------

class TestVga(unittest.TestCase):
    def test_s1_fixture(self):
        buf = fixture("vga-bios-menu.bin")
        self.assertEqual(hashlib.sha256(buf).hexdigest(),
                         "343588f3ff6cc21dbf6eda6ea2f740fc752a7f8cb3dc0503b4b7fc17b252dbf5")
        rows = console.vgatext(buf)
        self.assertEqual(len(rows), 25)
        self.assertEqual(rows[1].strip(), "GNU GRUB  version 2.12")
        self.assertEqual(rows[3][1] + rows[3][2] + rows[3][-1], "┌─┐")
        self.assertEqual(rows[4][:9], " │*Ubuntu")
        self.assertTrue(rows[4].endswith("│"))
        self.assertEqual(rows[17][1] + rows[17][-1], "└┘")
        self.assertIn("Use the ↑ and ↓ keys", rows[19])
        self.assertEqual(rows[22], "   The highlighted entry will be executed automatically in 30s.")
        attrs = console.vgaattrs(buf)
        self.assertEqual(attrs[4].count(0x70), 76)  # the highlighted row
        self.assertEqual(attrs[5].count(0x70), 0)

    def test_cp437_controls_and_window(self):
        cells = bytearray(b"\x00\x07" * 80 * 204 + b"\x00\x07" * 64)  # a 32 KiB window
        for i, b in enumerate(b"\x18\x19\x1e\x1f\x7f\xb3Press Enter for maintenance"):
            cells[2 * (80 * 99 + i)] = b
        rows = console.vgatext(bytes(cells))
        self.assertEqual(len(rows), 204)
        self.assertEqual(rows[99], "↑↓▲▼⌂│Press Enter for maintenance")
        self.assertEqual(rows[0], "")

    def test_observers_on_vga(self):
        obs = console.observer_lines(console.vgatext(fixture("vga-bios-hidden.bin")))
        self.assertEqual(obs[0], {"phase": "pre", "platform": "pc", "pending": "", "recordfail": "",
                                  "timeout": "0", "style": "hidden"})
        self.assertEqual(obs[1], {"phase": "post", "timeout": "0", "style": "hidden"})


def decode_png(png):
    """(width, height, rows of RGB bytes) of an 8-bit RGB PNG, filter 0."""
    assert png[:8] == b"\x89PNG\r\n\x1a\n"
    pos, chunks = 8, {}
    while pos < len(png):
        n, kind = struct.unpack(">I4s", png[pos:pos + 8])
        body = png[pos + 8:pos + 8 + n]
        crc, = struct.unpack(">I", png[pos + 8 + n:pos + 12 + n])
        assert crc == zlib.crc32(kind + body) & 0xFFFFFFFF, kind
        chunks[kind] = chunks.get(kind, b"") + body
        pos += 12 + n
    w, h, depth, ctype, _, _, _ = struct.unpack(">IIBBBBB", chunks[b"IHDR"])
    assert (depth, ctype) == (8, 2)
    raw = zlib.decompress(chunks[b"IDAT"])
    rows = []
    for y in range(h):
        line = raw[y * (1 + 3 * w):(y + 1) * (1 + 3 * w)]
        assert line[0] == 0
        rows.append(line[1:])
    assert b"IEND" in chunks
    return w, h, rows


class TestPng(unittest.TestCase):
    def test_ppm_to_png(self):
        # The pixel data starts with whitespace bytes, which a split()
        # of the header would eat.
        px = bytes([10, 32, 9, 255, 0, 0, 0, 255, 0, 0, 0, 255, 1, 2, 3, 4, 5, 6])
        ppm = b"P6\n# QEMU screendump\n3 2\n255\n" + px
        w, h, rows = decode_png(console.ppm_to_png(ppm))
        self.assertEqual((w, h), (3, 2))
        self.assertEqual(b"".join(rows), px)

    def test_maxval_and_errors(self):
        rows = decode_png(console.ppm_to_png(b"P6 1 1 15 " + bytes([15, 0, 5])))[2]
        self.assertEqual(rows, [bytes([255, 0, 85])])
        for bad in (b"P3 1 1 255 1 2 3", b"P6 2 2 255 \x00\x00\x00", b"P6 1 1 65535 \x00\x00\x00\x00\x00\x00",
                    b"P6 x"):
            with self.assertRaises(ValueError):
                console.ppm_to_png(bad)

    def test_cli(self):
        with tempfile.TemporaryDirectory() as tmp:
            src, dst = os.path.join(tmp, "s.ppm"), os.path.join(tmp, "s.png")
            with open(src, "wb") as f:
                f.write(b"P6 1 1 255\n\x01\x02\x03")
            self.assertEqual(console.main(["png", src, dst]), 0)
            with open(dst, "rb") as f:
                self.assertEqual(decode_png(f.read())[2], [b"\x01\x02\x03"])


# -- how a boot ended ----------------------------------------------------------

class TestClassify(unittest.TestCase):
    def test_login(self):
        text = cleaned("q2-login-shutdown.raw")
        login = text[:text.index("m4lab login: ") + len("m4lab login: ")]
        end = console.classify_boot(login, host="m4lab")
        self.assertEqual((end.kind, end.retry, end.after), ("login", None, []))
        self.assertIsNone(console.classify_boot(login))  # the lab's hostname is sclab
        end = console.classify_boot(login.replace("m4lab", "sclab"))
        self.assertEqual(end.kind, "login")

    def test_login_then_shutdown(self):
        # "m4lab login:" with the shutdown's "Stopping session" on its line.
        end = console.classify_boot(cleaned("q2-login-shutdown.raw"), host="m4lab")
        self.assertEqual((end.kind, end.after), ("login", ["shutdown"]))
        self.assertIn("Stopping session", end.line)

    def test_bad_fstab(self):
        end = console.classify_boot(cleaned("q2-bad-fstab.raw"), host="m4lab")
        self.assertEqual((end.kind, end.retry), ("emergency", None))
        self.assertEqual(end.after, ["login"])  # getty came up as well (outcome b or c)
        self.assertTrue(any(d.endswith("8f2a-5d6e7f8a9b0c.") for d in end.devices), end.devices)
        self.assertRegex(cleaned("q2-bad-fstab.raw"), r"Timed out waiting for device .*8f2a-5d6e7f8a9b0c")

    def test_slow_udev(self):
        end = console.classify_boot(cleaned("slow-udev-emergency.raw"), host="m4lab")
        self.assertEqual((end.kind, end.retry), ("emergency", "slow-udev"))
        self.assertEqual(len(end.devices), 3)
        # The same timeouts with the bad line's device as well: no retry.
        bad = "Timed out waiting for device dev-d…%s.\nx" % BAD_UUID[2:]
        text = cleaned("slow-udev-emergency.raw").replace("Timed out waiting for device dev-ttyS0", bad)
        self.assertIsNone(console.classify_boot(text, host="m4lab").retry)

    EMERGENCY = ("You are in emergency mode. After logging in, type \"journalctl -xb\" to view\n"
                 "system logs, \"systemctl reboot\" to reboot, or \"exit\"\nto continue bootup.\n\n"
                 "Press Enter for maintenance\n(or press Control-D to continue): ")

    @staticmethod
    def timed_out(device):
        return "[ TIME ] Timed out waiting for device %s.\n[DEPEND] Dependency failed for x.mount - /x.\n" % device

    def test_slow_udev_is_the_labs_devices_only(self):
        # slow-udev is retried as the lab's flake, so only what TCG is
        # known to starve may be called that.
        ttys0 = self.timed_out("dev-ttyS0.device - /dev/ttyS0")
        data = self.timed_out("dev-d…A.device - /dev/disk/by-label/DATA")
        for text, retry, devices in (
                (ttys0 + self.EMERGENCY, "slow-udev", 1),  # the serial console alone
                (data + self.EMERGENCY, None, 1),  # a device that is not the lab's: the boot's own failure
                (self.EMERGENCY, None, 0),  # no device timed out at all: something else broke
                (self.EMERGENCY + "\n" + ttys0, None, 0),  # timed out after the prompt: not why it came
        ):
            end = console.classify_boot(text)
            self.assertEqual((end.kind, end.retry, len(end.devices)), ("emergency", retry, devices), text)

    def test_the_mode_line_before_the_prompt_names_it(self):
        # rescue.target was on its way when local-fs.target failed: the
        # prompt that came is the emergency shell's.
        text = ("You are in rescue mode. After logging in, type \"journalctl -xb\" to view\n"
                + self.timed_out("dev-ttyS0.device - /dev/ttyS0") + self.EMERGENCY)
        end = console.classify_boot(text)
        self.assertEqual((end.kind, end.retry), ("emergency", "slow-udev"))
        self.assertEqual(console.classify_boot(self.EMERGENCY.replace("emergency", "rescue")).kind, "rescue")

    def test_panic(self):
        end = console.classify_boot(cleaned("q3c-panic-tcg.raw"), host="m4lab")
        self.assertEqual((end.kind, end.retry), ("panic", "panic"))
        self.assertIn("IO-APIC + timer", end.line)
        # The harmless "8254 timer not connected to IO-APIC" is no panic.
        end = console.classify_boot("[    0.267237] ..MP-BIOS bug: 8254 timer not connected to IO-APIC\n"
                                    "sclab login: ")
        self.assertEqual((end.kind, end.retry), ("login", None))

    def test_rescue_grub_and_nothing(self):
        end = console.classify_boot("You are in rescue mode. After logging in\n\nPress Enter for maintenance\n")
        self.assertEqual(end.kind, "rescue")
        end = console.classify_boot("Press Enter for maintenance\n")
        self.assertEqual(end.kind, "maintenance")
        end = console.classify_boot("error: no such device\ngrub> ")
        self.assertEqual((end.kind, end.retry), ("grub", "grub-prompt"))
        self.assertIsNone(console.classify_boot("still booting\n"))
        end = console.classify_boot("[  194.11] reboot: Power down\n")
        self.assertEqual(end.kind, "shutdown")

    def test_observers_on_serial(self):
        obs = console.observer_lines(cleaned("s2-uefi-menu-ctrln.raw"))
        self.assertEqual([o["phase"] for o in obs], ["pre", "post"])
        self.assertEqual((obs[0]["platform"], obs[0]["pending"], obs[0]["timeout"], obs[0]["style"]),
                         ("efi", "1", "0", "hidden"))
        self.assertEqual((obs[1]["timeout"], obs[1]["style"]), ("30", "menu"))

    def test_cli(self):
        with redirect_stdout(io.StringIO()) as out:
            rc = console.main(["classify", "--host", "m4lab", os.path.join(TESTDATA, "q3c-panic-tcg.raw")])
        self.assertEqual(rc, 0)
        self.assertIn("kind='panic'", out.getvalue())
        with redirect_stdout(io.StringIO()) as out:
            rc = console.main(["menu", os.path.join(TESTDATA, "vga-bios-menu.bin")])
        self.assertEqual(rc, 0)
        self.assertIn("SmartConfig rescue", out.getvalue())


# -- the rescue report against a golden ----------------------------------------

GOLDEN = """\
This boot:     <B3:8> (rescue), root read-only
Last healthy:  <YYYY-MM-DD HH:MM>, boot <B1:8>
Failed since:  <K boots>, last <MM-DD HH:MM>: never reached multi-user
scd:           not running

Changed since the last healthy boot, worst first:
<BAD> <HH:MM>  blocker fstab-source-missing, line <N>  /etc/fstab

To put /etc/fstab back:
  mount -o remount,rw /
  sc restore <GOOD>
  sync
  systemctl daemon-reload
  systemctl reboot
The menu shows once more: the first entry, Ubuntu, is the one.
"""

REPORT = """\
This boot:     5e6f7a8b (rescue), root read-only
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

BIND = {"GOOD": "1d5b0a", "BAD": "c7146c", "B1": "1a2b3c4d-9e8f-4a5b-8c7d-6e5f4a3b2c1d", "N": 4, "K": 1}


class TestGolden(unittest.TestCase):
    def lines(self, text=REPORT):
        return text.split("\n")

    def test_match(self):
        r = console.match_golden(self.lines(), GOLDEN, BIND)
        self.assertTrue(r.ok, r.problems)
        self.assertEqual(r.values["B3:8"], "5e6f7a8b")
        self.assertEqual(r.values["GOOD"], "1d5b0a")
        r = console.match_golden(self.lines(), GOLDEN)  # unbound: the values are read
        self.assertTrue(r.ok, r.problems)
        self.assertEqual((r.values["BAD"], r.values["N"], r.values["K boots"], r.values["B1:8"]),
                         ("c7146c", "4", "1 boot", "1a2b3c4d"))

    def test_bound_boots_and_counts(self):
        self.assertFalse(console.match_golden(self.lines(), GOLDEN, dict(BIND, K=2)).ok)
        two = REPORT.replace("Failed since:  1 boot,", "Failed since:  2 boots,")
        self.assertTrue(console.match_golden(self.lines(two), GOLDEN, dict(BIND, K=2)).ok)
        self.assertTrue(console.match_golden(self.lines(two), GOLDEN).ok)
        r = console.match_golden(self.lines(), GOLDEN, dict(BIND, B1="1a2b3c4e"))
        self.assertFalse(r.ok)
        self.assertIn("boot 1a2b3c4e", r.problems[0])
        self.assertEqual(console.short_boot("1a2b3c4d-9e8f-4a5b-8c7d-6e5f4a3b2c1d"), "1a2b3c4d")

    def test_wrong_id(self):
        wrong = REPORT.replace("sc restore 1d5b0a", "sc restore 43e4d2")
        r = console.match_golden(self.lines(wrong), GOLDEN, BIND)
        self.assertFalse(r.ok)
        self.assertIn("line 11", r.problems[-1])
        self.assertIn("sc restore 1d5b0a", r.problems[-1])
        r = console.match_golden(self.lines(), GOLDEN, dict(BIND, BAD="aaaaaa"))
        self.assertFalse(r.ok)

    def test_missing_sync(self):
        r = console.match_golden(self.lines(REPORT.replace("  sync\n", "")), GOLDEN, BIND)
        self.assertFalse(r.ok)
        n = len(console.golden_lines(GOLDEN))
        self.assertIn("%d lines, the golden has %d" % (n - 1, n), r.problems)
        self.assertTrue(any("line 12: expected '  sync'" in p for p in r.problems), r.problems)

    def test_81_columns(self):
        long_row = "c7146c 12:03  blocker fstab-source-missing, line 4  /etc/" + "x" * 24
        self.assertEqual(len(long_row), 81)
        report = REPORT.replace("c7146c 12:03  blocker fstab-source-missing, line 4  /etc/fstab", long_row)
        golden = GOLDEN.replace("/etc/fstab\n\nTo", "/etc/" + "x" * 24 + "\n\nTo")
        r = console.match_golden(self.lines(report), golden, BIND)
        self.assertFalse(r.ok)
        self.assertEqual(r.problems, ["line 7 is 81 columns, more than 80: %r" % long_row])

    def test_same_name_same_value(self):
        golden = "<BAD> x\n<BAD> y\n<ID> a\n<ID> b\n"
        self.assertTrue(console.match_golden(["c7146c x", "c7146c y", "111111 a", "222222 b"], golden).ok)
        self.assertFalse(console.match_golden(["c7146c x", "c7146d y", "111111 a", "222222 b"], golden).ok)

    def test_unknown_placeholder(self):
        with self.assertRaises(console.GoldenError):
            console.match_golden(["x"], "<WHATEVER>\n")
        self.assertTrue(console.match_golden(["a <none> b"], "a <none> b").ok)  # not a placeholder

    def test_block_from_the_console(self):
        lines = REPORT.split("\n")
        noisy = ["[  OK  ] Started systemd-journald.service - Journal Service.", "boot noise"] + lines[:5] + [
            "[  OK  ] Finished systemd-tmpfiles-setup.service - Create Volatile Files and Directories.",
            "         Starting systemd-update-utmp.service - Record System Boot/Shutdown in UTMP...",
            "[   21.199094] systemd[1]: something",
        ] + lines[5:] + [
            "You are in rescue mode. After logging in, type \"journalctl -xb\" to view",
            "system logs, \"systemctl reboot\" to reboot, or \"exit\"", "to continue bootup.", "",
            "Press Enter for maintenance", "(or press Control-D to continue): "]
        text = "\n".join(noisy)
        block = console.report_block(text)
        self.assertEqual(len(block.stripped), 3)
        r = console.match_golden(block.lines, GOLDEN, BIND)
        self.assertTrue(r.ok, r.problems)
        self.assertEqual(block.screen[0], lines[0])
        self.assertEqual(block.screen[-1], "Press Enter for maintenance")
        # The report (its last line is the blank before systemd's text),
        # systemd's three lines, a blank, the prompt.
        self.assertEqual(len(block.screen), len(lines) + 5)
        self.assertLessEqual(console.screen_rows(block.screen), 25)
        self.assertIsNone(console.report_block(text.split("Press Enter")[0]).screen)
        self.assertEqual(console.screen_rows(["", "x" * 80, "x" * 81]), 4)
        self.assertIsNone(console.report_block("This boot: x\n"))
        self.assertIsNone(console.report_block("You are in emergency mode"))

    def test_block_is_the_last_report_before_the_mode_line(self):
        # An earlier "This boot:" (sc status in the boot before, on the
        # same console) is not the rescue report; nor is one after it.
        earlier = REPORT.replace("5e6f7a8b (rescue), root read-only", "4d5e6f70")
        text = earlier + "sclab login: \n" + REPORT + "You are in rescue mode. After logging in\n" + \
            REPORT.replace("5e6f7a8b", "99999999")
        block = console.report_block(text)
        self.assertEqual(block.lines[0], REPORT.split("\n")[0])
        self.assertEqual(text[block.start:block.end], REPORT)
        self.assertTrue(console.match_golden(block.lines, GOLDEN, BIND).ok)

    # A value of each placeholder's form, at its widest.
    SAMPLE = {"GOOD": "1d5b0a", "BAD": "c7146c", "PRE": "a1b2c3", "ID": "b2c3d4", "B1": "1a2b3c4d",
              "B2": "2b3c4d5e", "B3": "3c4d5e6f", "CUR": "4d5e6f70", "BOOT": "5e6f7081", "N": "999",
              "K": "99", "K boots": "99 boots", "NUM": "999", "PID": "99999", "HH:MM": "12:00",
              "TIME": "12:00", "MM-DD HH:MM": "10-04 12:00", "YYYY-MM-DD HH:MM": "2026-10-04 12:00",
              "DATE": "2026-10-04 12:00"}

    def fill(self, line):
        return console.TOKEN.sub(lambda m: self.SAMPLE[m.group(1).replace(":8", "")], line)

    def test_goldens_from_go(self):
        names = sorted(n for n in os.listdir(TESTDATA) if n.startswith("console-") and n.endswith(".golden"))
        if not names:
            self.skipTest("no console-*.golden yet (TestStatusConsoleLab -update writes them)")
        bind = {"GOOD": "1d5b0a", "BAD": "c7146c", "B1": "1a2b3c4d-0000-4000-8000-000000000000", "N": 999, "K": 99}
        for name in names:
            with open(os.path.join(TESTDATA, name), encoding="utf-8") as f:
                golden = f.read()
            lines = console.golden_lines(golden)  # every placeholder is known
            self.assertTrue(lines[0].startswith("This boot:"), name)
            self.assertIn("  sc restore <GOOD>", lines, name)
            filled = [self.fill(line) for line in lines]
            for line in filled:
                self.assertLessEqual(len(line), 80, "%s: %r" % (name, line))
            r = console.match_golden(filled, golden, bind)
            self.assertTrue(r.ok, "%s: %s" % (name, r.problems))
            r = console.match_golden(filled, golden, dict(bind, GOOD="0d5b0a"))
            self.assertFalse(r.ok, name)


# -- vm.py's pure functions ------------------------------------------------------

# A made-up key: render_seed checks the form only.
KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7test0nly0key0for0unit0tests0000000000 sclab"
SHA = "6a81c37564db9b1ee84e141922625e1d7c5b389b99bb3c572e0243607d5bb4d2"


class TestVm(unittest.TestCase):
    def test_seed_has_no_password(self):
        user_data, meta_data = vm.render_seed("0123456789ab", KEY)
        for key in ("passwd", "chpasswd", "plain_text_passwd", "hashed_passwd"):
            self.assertNotRegex(user_data, r"(?m)^\s*(?:-\s*)?%s\s*:" % key)
        self.assertRegex(user_data, r"(?m)^\s*lock_passwd:\s*true\s*$")
        self.assertRegex(user_data, r"(?m)^\s*ssh_pwauth:\s*false\s*$")
        self.assertRegex(user_data, r"(?m)^\s*disable_root:\s*true\s*$")
        self.assertIn(KEY, user_data)
        self.assertRegex(user_data, r"(?m)^\s*hostname:\s*sclab\s*$")
        self.assertIn("SCLAB-PROVISIONED", user_data)
        self.assertRegex(meta_data, r"instance-id:\s*sclab-0123456789ab")
        self.assertNotRegex(user_data + meta_data, r"@[A-Z][A-Z0-9_]*@")

    def test_seed_takes_one_public_key_line(self):
        # user-data is YAML that cloud-init acts on as root: whatever the
        # key file holds beyond one ed25519 line would be a second key, or
        # more YAML.
        for bad in (KEY + "\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIother another", KEY + "\nruncmd: [[touch, /x]]",
                    KEY + " two words", KEY.replace("ssh-ed25519", "ssh-rsa"), KEY.replace("ssh-ed25519 ", ""),
                    "ssh-ed25519 AAAA'$(touch /x)' sclab", "", b"\n"):
            with self.assertRaisesRegex(vm.LabError, "not one ed25519 public key line", msg=bad):
                vm.render_seed("0123456789ab", bad)
        user_data, _ = vm.render_seed("0123456789ab", (KEY + "\n").encode())  # as key.pub is read
        self.assertIn(KEY, user_data)
        for bad in ("0123456789a", "0123456789AB", "0123456789abc", "../0123456789"):
            with self.assertRaisesRegex(vm.LabError, "bad reference hash", msg=bad):
                vm.render_seed(bad, KEY)

    def test_fill_template(self):
        text = "a: @A@\nlist:\n    @BLOCK@\nb: @A@ @B@\n"
        values = {"A": "1", "B": "2", "BLOCK": "x\n\n y\n", "UNUSED": "3"}
        self.assertEqual(vm.fill_template(text, values, blocks=("BLOCK",)), "a: 1\nlist:\n    x\n\n     y\nb: 1 2\n")
        # A token nobody filled would reach cloud-init as it is.
        del values["B"]
        with self.assertRaisesRegex(vm.LabError, "left unfilled: @B@$"):
            vm.fill_template(text, values, blocks=("BLOCK",))
        with self.assertRaisesRegex(vm.LabError, "left unfilled: @BLOCK@$"):  # a block not alone on its line
            vm.fill_template("k: @BLOCK@\n", values, blocks=("BLOCK",))
        with self.assertRaisesRegex(vm.LabError, "value for A has a newline"):
            vm.fill_template("a: @A@\n", {"A": "1\nb: 2"})

    def test_lab_conf_is_checked(self):
        text = read_file(vm.CONF_PATH).decode()
        conf = vm.parse_conf(text)
        self.assertRegex(conf["IMAGE_SHA256"], r"^[0-9a-f]{64}$")
        self.assertEqual(conf.int("SMP"), 2)
        pin = "IMAGE_SHA256=" + conf["IMAGE_SHA256"]
        for bad, why in ((text.replace(pin, pin[:-1]), "IMAGE_SHA256 is not a sha256"),  # a pin cut short pins nothing
                         (text.replace(pin, "IMAGE_SHA256=" + "g" * 64), "IMAGE_SHA256 is not a sha256"),
                         (text.replace(pin, "IMAGE_SHA256="), "missing IMAGE_SHA256"),
                         (text + pin + "\n", "IMAGE_SHA256 is set twice"),
                         (text.replace("SMP=2", "SMP=two"), "SMP=two is not a number")):
            with self.assertRaisesRegex(vm.LabError, why):
                vm.parse_conf(bad)

    def test_ssh_keeps_the_users_ssh_out(self):
        # The lab's key and nothing else of ssh's on this machine: no
        # config file, no agent, no other identity, no known_hosts line.
        seen = []
        with mock.patch.object(vm, "run_timed", lambda argv, timeout, input=None: seen.append(argv)):
            vm.ssh(2299, "/cache/key", "true", timeout=5)
            vm.scp_to(2299, "/cache/key", ["a", "b"], "/tmp/", timeout=5)
            vm.scp_from(2299, "/cache/key", "/etc/fstab", "out", timeout=5)
        seen.append(vm.ssh_argv(2299, "/cache/key"))  # vm.py ssh
        self.assertEqual([a[0] for a in seen], ["ssh", "scp", "scp", "ssh"])
        for argv in seen:
            options = {argv[i + 1] for i, a in enumerate(argv) if a == "-o"}
            self.assertLessEqual({"IdentitiesOnly=yes", "IdentityAgent=none", "UserKnownHostsFile=/dev/null",
                                  "BatchMode=yes"}, options, argv)
            self.assertEqual(argv[argv.index("-F") + 1], "/dev/null", argv)
            self.assertEqual([argv[i + 1] for i, a in enumerate(argv) if a == "-i"], ["/cache/key"], argv)
            self.assertEqual(argv[argv.index("-p" if argv[0] == "ssh" else "-P") + 1], "2299", argv)
        self.assertEqual(seen[0][-2:], ["owner@127.0.0.1", "true"])
        self.assertEqual(seen[1][-3:], ["a", "b", "owner@127.0.0.1:/tmp/"])
        self.assertEqual(seen[2][-2:], ["owner@127.0.0.1:/etc/fstab", "out"])

    def test_seed_server_is_local(self):
        with tempfile.TemporaryDirectory() as tmp:
            seed = vm.SeedServer({"user-data": "#cloud-config\n", "meta-data": b"instance-id: x\n"},
                                 os.path.join(tmp, "seed.log")).start()
            self.addCleanup(seed.stop)
            # The guest comes in through QEMU's 10.0.2.2: nothing else on the network needs it.
            self.assertEqual(seed.httpd.socket.getsockname(), ("127.0.0.1", seed.port))
            self.assertEqual(seed.guest_url, "http://10.0.2.2:%d/" % seed.port)

            def get(path, method="GET"):
                c = http.client.HTTPConnection("127.0.0.1", seed.port, timeout=5)
                self.addCleanup(c.close)
                c.request(method, path)
                r = c.getresponse()
                return r.status, r.read()

            self.assertEqual(get("/user-data"), (200, b"#cloud-config\n"))
            self.assertEqual(get("/meta-data?x=1"), (200, b"instance-id: x\n"))
            self.assertEqual(get("/user-data", "HEAD"), (200, b""))
            self.assertEqual(get("/vendor-data"), (404, b""))
            self.assertEqual((seed.fetched("user-data"), seed.fetched("vendor-data")), (True, False))
            seed.stop()
            self.assertFalse(vm.port_open(seed.port, 1))
            self.assertEqual(read_file(os.path.join(tmp, "seed.log")).count(b"GET /"), 3)

    # /proc as running_qemu reads it: pid -> (comm, real uid, command line), None for a process that is gone.
    PROC = {"101": ("qemu-system-x86", 1000, b"qemu-system-x86_64\0-name\0sclab-a\0-S\0"),
            "102": ("qemu-system-x86", 1001, b"qemu-system-x86_64\0-name\0theirs\0"),
            "103": ("bash", 1000, b"bash\0"),
            "104": None,
            "105": ("qemu-system-x86", 1000, None)}

    def test_running_qemu_is_this_users_only(self):
        # "One VM at a time" is about this user's: another user's QEMU
        # (uid 1001's) must not stop the lab, and is not the lab's to count.
        def proc_open(path, mode="r"):
            pid, name = re.match(r"^/proc/(\d+)/(status|cmdline)$", path).groups()
            if self.PROC[pid] is None or (name == "cmdline" and self.PROC[pid][2] is None):
                raise FileNotFoundError(path)
            comm, uid, cmdline = self.PROC[pid]
            if name == "cmdline":
                return io.BytesIO(cmdline)
            return io.StringIO("Name:\t%s\nUmask:\t0022\nState:\tS (sleeping)\nPid:\t%s\nUid:\t%d\t%d\t%d\t%d\n"
                               "Gid:\t100\t100\t100\t100\n" % (comm, pid, uid, uid, uid, uid))

        listdir = os.listdir
        with mock.patch.object(vm, "open", proc_open, create=True), \
                mock.patch.object(vm.os, "listdir", lambda d: sorted(self.PROC) + ["self", "uptime"] if d == "/proc"
                                  else listdir(d)):
            self.assertEqual(vm.running_qemu(1000), [(101, "qemu-system-x86_64 -name sclab-a -S"), (105, "")])
            self.assertEqual(vm.running_qemu(1001), [(102, "qemu-system-x86_64 -name theirs")])
            with mock.patch.object(vm.os, "getuid", lambda: 1001):
                self.assertEqual([pid for pid, _ in vm.running_qemu()], [102])

    def test_seed_quiets_writers_of_etc(self):
        """scd records every change under /etc and check 3.6 wants the
        owner's edit alone: the timers that rewrite files there are off
        (fwupd-refresh.timer rewrote /etc/fwupd/fwupd.conf in a bios run)."""
        user_data, _ = vm.render_seed("0123456789ab", KEY)
        off = re.findall(r"(?m)^\s*-\s*\[systemctl,\s*(?:disable|mask),\s*([^\]]*)\]", user_data)
        units = {u.strip() for line in off for u in line.split(",")}
        for unit in ("fwupd-refresh.timer", "apt-daily.timer", "apt-daily-upgrade.timer",
                     "unattended-upgrades.service", "snapd.service"):
            self.assertIn(unit, units)

    def test_qemu_args(self):
        conf = vm.load_conf()
        for mode, seed in (("uefi", None), ("bios", None), ("uefi", "http://10.0.2.2:8642/")):
            args = vm.qemu_args(conf, mode, 2299, "sclab-test", "disk.qcow2", "VARS.fd", seed)
            self.assertTrue(all(isinstance(a, str) for a in args), args)
            joined = " ".join(args)
            self.assertNotRegex(joined.lower(), r"kvm")
            self.assertTrue("-accel" in args and args[args.index("-accel") + 1].split(",")[0] == "tcg"
                            or re.search(r"accel=tcg", joined), args)
            self.assertEqual(args[args.index("-smp") + 1].split(",")[0], "2")
            self.assertEqual(args[args.index("-m") + 1], "2048")
            self.assertIn("-S", args)
            self.assertTrue(args[args.index("-machine") + 1].startswith("q35"))
            self.assertEqual(re.findall(r"hostfwd=([^,\s]+)", joined), ["tcp:127.0.0.1:2299-:22"])
            for ip in re.findall(r"\b\d{1,3}(?:\.\d{1,3}){3}\b", joined):
                self.assertTrue(ip == "127.0.0.1" or ip.startswith("10.0.2."), ip)
            self.assertNotIn("0.0.0.0", joined)
            self.assertEqual(args[args.index("-display") + 1], "none")
            pflash = [a for a in args if "if=pflash" in a]
            if mode == "uefi":
                self.assertEqual(len(pflash), 2, args)
                self.assertTrue(any("readonly=on" in a and "CODE" in a for a in pflash), pflash)
                self.assertEqual(args[args.index("-vga") + 1], "none")
            else:
                self.assertEqual(pflash, [])
                self.assertEqual(args[args.index("-vga") + 1], "std")

    def test_ref_hash_is_stable(self):
        parts = (SHA, b"#cloud-config\n", b"instance-id: sclab-@H12@\n", b"GRUB_TIMEOUT=0\n", KEY, "1")
        h = vm.ref_hash(*parts)
        self.assertRegex(h, r"^[0-9a-f]{64}$")
        self.assertEqual(vm.ref_hash(*parts), h)
        for i in range(len(parts)):
            changed = list(parts)
            changed[i] = parts[i] + (b"x" if isinstance(parts[i], bytes) else "x")
            self.assertNotEqual(vm.ref_hash(*changed), h, i)
        # Moving bytes from one part to the next changes it too.
        self.assertNotEqual(vm.ref_hash(SHA, b"#cloud-config", b"\ninstance-id: sclab-@H12@\n", *parts[3:]), h)
        # The same in another process, with another hash seed.
        code = "import sys; sys.path.insert(0, %r); import vm; print(vm.ref_hash(*%r))" % (HERE, parts)
        env = dict(os.environ, PYTHONHASHSEED="12345", PYTHONDONTWRITEBYTECODE="1")
        out = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, env=env, check=True)
        self.assertEqual(out.stdout.strip(), h)


def quiet(msg):
    pass


def mode_of(path):
    return os.stat(path).st_mode & 0o777


def read_file(path):
    with open(path, "rb") as f:
        return f.read()


class CacheCase(unittest.TestCase):
    """A test with a temp directory to make its cache in, lab.conf, and
    nothing of this machine's."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.conf = vm.load_conf()
        os.makedirs(self.path("xdg"))
        # Never this machine's QEMUs, its real per-user lock or its cache.
        for p in (mock.patch.object(vm, "running_qemu", lambda: []),
                  mock.patch.dict(os.environ, {"XDG_RUNTIME_DIR": self.path("xdg"),
                                               "SC_LAB_CACHE": self.path("no-cache")})):
            p.start()
            self.addCleanup(p.stop)

    def path(self, *parts):
        return os.path.join(self.tmp.name, *parts)

    def file(self, name, data=b"", mode=0o644):
        """Writes a file in the temp directory; its path."""
        p = self.path(name)
        with open(p, "wb") as f:
            f.write(data if isinstance(data, bytes) else data.encode())
        os.chmod(p, mode)
        return p

    def stub(self, name, body):
        """A stand-in for a tool the lab runs: a sh script, first on
        PATH, that logs its command line to calls() and then runs body."""
        d = self.path("bin")
        if not os.path.isdir(d):
            os.mkdir(d)
            p = mock.patch.dict(os.environ, {"PATH": d + os.pathsep + os.environ.get("PATH", os.defpath)})
            p.start()
            self.addCleanup(p.stop)
        self.file(os.path.join("bin", name), '#!/bin/sh\necho "%s $*" >>"%s"\n%s\n' % (name, self.path("calls"), body),
                  0o755)

    def calls(self):
        try:
            return read_file(self.path("calls")).decode().split("\n")[:-1]
        except FileNotFoundError:
            return []


class TestCacheAndLock(CacheCase):
    """The chunk D review: the lab chmods and cleans only a directory
    that is its own; one lab command per user, whatever the cache; the
    first signal is the only one that counts."""

    def test_a_foreign_directory_is_refused_untouched(self):
        proj = self.path("proj")
        os.makedirs(os.path.join(proj, "runs", "experiment-1"))
        open(os.path.join(proj, "thesis.docx.part"), "w").close()
        os.chmod(proj, 0o755)
        for fn in (lambda: vm.ensure_cache(proj), lambda: vm.gc(self.conf, cache=proj, log=lambda m: None)):
            with self.assertRaises(vm.LabError) as c:
                fn()
            self.assertIn("is not a lab cache", str(c.exception))
        self.assertEqual(os.stat(proj).st_mode & 0o777, 0o755)
        self.assertEqual(sorted(os.listdir(proj)), ["runs", "thesis.docx.part"])
        self.assertTrue(os.path.isdir(os.path.join(proj, "runs", "experiment-1")))

    def test_a_cache_is_made_marked_and_taken_over(self):
        new = vm.ensure_cache(self.path("new"))
        self.assertEqual(os.stat(new).st_mode & 0o777, 0o700)
        self.assertEqual(sorted(os.listdir(new)), [vm.MARKER, "images", "runs"])
        # A cache from before the marker: nothing but the lab's names.
        old = self.path("old")
        os.makedirs(os.path.join(old, "runs"))
        for f in ("lock", "key", "key.pub", "ref-0123456789ab.qcow2", "ref-0123456789ab.VARS.fd.part"):
            open(os.path.join(old, f), "w").close()
        vm.ensure_cache(old)
        self.assertTrue(os.path.exists(os.path.join(old, vm.MARKER)))
        # Marked, it stays the lab's whatever else lands in it.
        open(os.path.join(old, "notes.txt"), "w").close()
        vm.ensure_cache(old)

    def test_gc_removes_only_what_the_lab_makes(self):
        cache = vm.ensure_cache(self.path("c"))
        runs = ["20261005T1%05dZ-uefi-abc1234" % i for i in range(4)]
        for d in runs + ["experiment-1"]:
            os.makedirs(os.path.join(cache, "runs", d))
        pinned = os.path.basename(vm.image_path(self.conf, cache))
        for f in ("key.part", "ref-0123456789ab.qcow2.part", "thesis.docx.part", "notes.txt"):
            open(os.path.join(cache, f), "w").close()
        for f in (pinned, "old-release.img", "old-release.img.part", "holiday.jpg"):
            open(os.path.join(cache, "images", f), "w").close()
        os.makedirs(os.path.join(cache, "images", "raw.img"))
        would = vm.gc(self.conf, cache=cache, keep=2, dry_run=True, log=lambda m: None)
        self.assertEqual(sorted(os.listdir(os.path.join(cache, "runs"))), sorted(runs + ["experiment-1"]))  # a dry run
        removed = vm.gc(self.conf, cache=cache, keep=2, log=lambda m: None)
        self.assertEqual(removed, would)
        self.assertEqual(sorted(os.path.relpath(p, cache) for p in removed), sorted(
            ["runs/" + runs[0], "runs/" + runs[1], "key.part", "ref-0123456789ab.qcow2.part",
             "images/old-release.img", "images/old-release.img.part"]))
        self.assertEqual(sorted(os.listdir(os.path.join(cache, "runs"))), sorted(runs[2:] + ["experiment-1"]))  # the newest stay
        self.assertEqual(sorted(os.listdir(os.path.join(cache, "images"))), sorted([pinned, "holiday.jpg", "raw.img"]))
        for f in ("thesis.docx.part", "notes.txt"):
            self.assertTrue(os.path.exists(os.path.join(cache, f)), f)

    def test_an_old_cache_gets_its_mode_back(self):
        cache = vm.ensure_cache(self.path("c"))
        os.chmod(cache, 0o755)
        os.rmdir(os.path.join(cache, "runs"))
        vm.ensure_cache(cache)
        self.assertEqual(mode_of(cache), 0o700)
        self.assertEqual(mode_of(os.path.join(cache, "runs")), 0o700)

    def test_gc_keeps_the_reference_image_of_this_checkout(self):
        cache = vm.ensure_cache(self.path("c"))
        stale = [os.path.join(cache, "ref-0123456789ab." + ext) for ext in ("qcow2", "VARS.fd", "json")]
        for f in stale:
            open(f, "w").close()
        # Without the key nothing says which reference is this checkout's: none goes.
        self.assertEqual(vm.gc(self.conf, cache=cache, log=quiet), [])
        self.file(os.path.join("c", "key.pub"), KEY + "\n")
        ref = vm.ref_info(self.conf, cache)
        for k in ("qcow2", "vars", "json"):
            open(ref[k], "w").close()
        self.assertEqual(sorted(vm.gc(self.conf, cache=cache, log=quiet)), sorted(stale))
        self.assertEqual(sorted(f for f in os.listdir(cache) if f.startswith("ref-")),
                         sorted(os.path.basename(ref[k]) for k in ("qcow2", "vars", "json")))
        self.assertTrue(vm.find_ref(self.conf, cache)["ready"])

    def test_gc_keeps_keep_runs_and_a_live_up_json(self):
        cache = vm.ensure_cache(self.path("c"))
        self.conf["KEEP_RUNS"] = "2"
        runs = ["20261005T1%05dZ-bios-abc1234" % i for i in range(3)]
        for d in runs[1:]:
            os.makedirs(os.path.join(cache, "runs", d))
        # The oldest is a link to a directory elsewhere: the link goes, and nothing behind it.
        os.mkdir(self.path("elsewhere"))
        kept = self.file(os.path.join("elsewhere", "serial.raw"), "evidence")
        os.symlink(self.path("elsewhere"), os.path.join(cache, "runs", runs[0]))
        # A "vm.py up" that runs (by its command line, as vm.py looks for it), and its up.json.
        up = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)", "/x/lab/vm.py", "up", "bios"])
        self.addCleanup(up.wait)
        self.addCleanup(up.kill)
        state = self.file(os.path.join("c", "up.json"), json.dumps({"pid": up.pid}))
        with vm.lab_lock(cache):  # another lab command is at work: gc deletes nothing under it
            with self.assertRaisesRegex(vm.LabError, "another lab command holds"):
                vm.gc(self.conf, cache=cache, log=quiet)
        self.assertEqual(len(os.listdir(os.path.join(cache, "runs"))), 3)
        self.assertEqual(vm.gc(self.conf, cache=cache, log=quiet), [os.path.join(cache, "runs", runs[0])])
        self.assertEqual((sorted(os.listdir(os.path.join(cache, "runs"))), read_file(kept)), (runs[1:], b"evidence"))
        up.kill()
        up.wait()
        self.assertEqual(vm.gc(self.conf, cache=cache, log=quiet), [state])  # stale now
        self.assertFalse(os.path.exists(state))
        self.file(os.path.join("c", "up.json"), json.dumps({"pid": os.getpid()}))  # a pid that is another program's
        self.assertEqual(vm.gc(self.conf, cache=cache, log=quiet), [state])

    def test_the_lock_file(self):
        cache = vm.ensure_cache(self.path("c"))
        lock = os.path.join(cache, "lock")
        self.addCleanup(os.umask, os.umask(0o022))
        with vm.lab_lock(cache):
            for f in (lock, vm.user_lock_path()):
                self.assertEqual(mode_of(f), 0o600)
                self.assertEqual(read_file(f), b"%d\n" % os.getpid())
            with self.assertRaisesRegex(vm.LabError, r"holds %s \(pid %d\)" % (re.escape(lock), os.getpid())):
                with vm.lab_lock(cache):  # it says whom to look for
                    pass
        # A link where the lock goes is not followed: the lab truncates its lock and writes to it.
        victim = self.file("victim", "keep me\n")
        os.unlink(lock)
        os.symlink(victim, lock)
        with self.assertRaisesRegex(vm.LabError, "the lab's lock"):
            with vm.lab_lock(cache):
                pass
        self.assertEqual(read_file(victim), b"keep me\n")

    def test_one_command_per_user_whatever_the_cache(self):
        a, b = vm.ensure_cache(self.path("a")), vm.ensure_cache(self.path("b"))
        rt = self.path("run")
        os.makedirs(rt)
        with mock.patch.dict(os.environ, {"XDG_RUNTIME_DIR": rt}):
            self.assertEqual(vm.user_lock_path(), os.path.join(rt, "smartconfig-lab.lock"))
            with vm.lab_lock(a):
                with self.assertRaises(vm.LabError) as c:
                    with vm.lab_lock(b):
                        pass
                self.assertIn("smartconfig-lab.lock", str(c.exception))
                with self.assertRaises(vm.LabError):  # and one per cache, as before
                    with vm.lab_lock(a):
                        pass
            with vm.lab_lock(b):  # released
                pass
            os.chmod(os.path.join(rt, "smartconfig-lab.lock"), 0)
            if os.geteuid() != 0:  # another user's file in the way: said, not a traceback
                with self.assertRaises(vm.LabError):
                    with vm.lab_lock(b):
                        pass
        self.assertIsNone(vm.user_lock_path({"XDG_RUNTIME_DIR": self.path("none")}))
        self.assertIsNone(vm.user_lock_path({}))

    def test_the_guard_refuses_while_a_qemu_runs(self):
        cache = vm.ensure_cache(self.path("c"))
        with mock.patch.object(vm, "running_qemu", lambda: [(4242, "qemu-system-x86_64 -name other")]):
            with self.assertRaises(vm.LabError) as c:
                with vm.lab_lock(cache):
                    pass
            self.assertIn("pid 4242", str(c.exception))
            with vm.lab_lock(cache, guard=False):
                pass

    SIGNALS = r"""
import os, signal, sys, time
sys.path.insert(0, %r)
import vm
vm.install_signal_handlers()
print("hup", "ignored" if signal.getsignal(signal.SIGHUP) == signal.SIG_IGN else "handled", flush=True)
try:
    try:
        os.kill(os.getpid(), getattr(signal, sys.argv[1]))
        time.sleep(5)
    finally:
        # The teardown: the same signal again (timeout --foreground passes
        # it on a second time) and the others must not cut it.
        for s in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
            os.kill(os.getpid(), s)
        time.sleep(0.2)
        print("teardown done", flush=True)
except BaseException as e:
    print("ended by", type(e).__name__, getattr(e, "code", ""), flush=True)
"""

    def signals(self, sig, pre=None):
        r = subprocess.run([sys.executable, "-c", self.SIGNALS % HERE, sig], capture_output=True, text=True,
                           timeout=60, preexec_fn=pre)
        return r.returncode, r.stdout.split("\n")[:-1]

    def test_the_first_signal_is_the_only_one(self):
        for sig, end in (("SIGINT", "ended by KeyboardInterrupt "), ("SIGTERM", "ended by SystemExit 143"),
                         ("SIGHUP", "ended by SystemExit 129")):
            self.assertEqual(self.signals(sig), (0, ["hup handled", "teardown done", end]), sig)

    def test_an_ignored_hangup_stays_ignored(self):
        # nohup: SIGHUP is inherited as ignored, and the lab leaves it so.
        rc, out = self.signals("SIGTERM", pre=lambda: signal.signal(signal.SIGHUP, signal.SIG_IGN))
        self.assertEqual((rc, out), (0, ["hup ignored", "teardown done", "ended by SystemExit 143"]))


# -- vm.py: the pinned image, the key, the reference image ------------------------

class TestImage(CacheCase):
    """The pinned image: nothing gets into the cache, or is used from it,
    unless its sha256 is lab.conf's. curl is a stand-in; nothing is
    downloaded."""

    GOOD, OTHER = b"the pinned image\n", b"another image\n"
    # curl ... -o PART URL: copies $CURL_GIVES to PART, exits $CURL_RC.
    CURL = ('while [ $# -gt 1 ]; do [ "$1" != -o ] || out=$2; shift; done\n'
            '[ -z "$CURL_GIVES" ] || cp "$CURL_GIVES" "$out"\nexit "${CURL_RC:-0}"')

    def setUp(self):
        super().setUp()
        self.conf["IMAGE_SHA256"] = hashlib.sha256(self.GOOD).hexdigest()
        self.cache = vm.ensure_cache(self.path("c"))
        self.dest = vm.image_path(self.conf, self.cache)

    def images(self):
        return sorted(os.listdir(os.path.join(self.cache, "images")))

    def fetch(self, src=None):
        return vm.fetch_image(self.conf, self.cache, src, log=quiet)

    def test_a_copy_with_the_pin_is_kept_read_only(self):
        self.assertEqual(self.fetch(self.file("good.img", self.GOOD)), self.dest)
        self.assertEqual((read_file(self.dest), mode_of(self.dest)), (self.GOOD, 0o444))
        self.assertEqual(self.images(), [os.path.basename(self.dest)])  # no .part
        self.assertEqual(vm.check_image(self.conf, self.cache), self.dest)

    def test_a_copy_with_another_sha256_is_refused(self):
        with self.assertRaisesRegex(vm.LabError, "has sha256 %s; lab.conf pins %s" % (
                hashlib.sha256(self.OTHER).hexdigest(), self.conf["IMAGE_SHA256"])):
            self.fetch(self.file("other.img", self.OTHER))
        self.assertEqual(self.images(), [])  # neither the image nor its .part
        with self.assertRaisesRegex(vm.LabError, "cannot copy"):
            self.fetch(self.path("no-such.img"))
        self.assertEqual(self.images(), [])

    def test_an_image_in_the_cache_is_hashed_again(self):
        # Taken by its name alone, a changed image would be what boots.
        self.file(os.path.join("c", "images", os.path.basename(self.dest)), self.OTHER)
        with self.assertRaisesRegex(vm.LabError, "lab.conf pins"):
            vm.check_image(self.conf, self.cache)
        self.fetch(self.file("good.img", self.GOOD))
        self.assertEqual(read_file(self.dest), self.GOOD)
        # The pinned one is kept as it is: the source is not even read.
        self.fetch(self.path("no-such.img"))
        self.assertEqual(self.images(), [os.path.basename(self.dest)])
        os.unlink(self.dest)
        with self.assertRaisesRegex(vm.LabError, "no image .*: run make lab-image"):
            vm.check_image(self.conf, self.cache)

    def test_a_download_is_held_to_the_pin(self):
        self.stub("curl", self.CURL)
        good, other = self.file("good", self.GOOD), self.file("other", self.OTHER)
        with mock.patch.dict(os.environ, {"CURL_GIVES": other}):
            with self.assertRaisesRegex(vm.LabError, "lab.conf pins"):
                self.fetch()
        self.assertEqual(self.images(), [])
        with mock.patch.dict(os.environ, {"CURL_GIVES": good, "CURL_RC": "22"}):  # all of it there, but curl says no
            with self.assertRaisesRegex(vm.LabError, r"curl failed \(exit 22\)"):
                self.fetch()
        self.assertEqual(self.images(), [])
        with mock.patch.dict(os.environ, {"CURL_GIVES": good}):
            self.assertEqual(self.fetch(), self.dest)
        self.assertEqual((read_file(self.dest), mode_of(self.dest)), (self.GOOD, 0o444))
        self.assertEqual(self.images(), [os.path.basename(self.dest)])
        for call in self.calls():  # to the .part file, from the URL lab.conf names
            self.assertTrue(call.endswith(" -o %s.part %s" % (self.dest, vm.image_url(self.conf))), call)
        self.assertEqual(len(self.calls()), 3)


class TestKey(CacheCase):
    # ssh-keygen ... -f FILE: writes FILE and FILE.pub as a umask of 022 leaves them.
    KEYGEN = ('while [ $# -gt 1 ]; do [ "$1" != -f ] || f=$2; shift; done\n'
              '[ -z "$KEYGEN_SAYS" ] || { echo "$KEYGEN_SAYS" >&2; exit 1; }\n'
              'umask 022; echo PRIVATE >"$f"; echo "%s" >"$f.pub"' % KEY)

    def test_the_key_is_made_once_and_private(self):
        cache = vm.ensure_cache(self.path("c"))
        self.stub("ssh-keygen", self.KEYGEN)
        with mock.patch.dict(os.environ, {"KEYGEN_SAYS": "ssh-keygen: no entropy"}):
            with self.assertRaisesRegex(vm.LabError, "ssh-keygen failed: ssh-keygen: no entropy"):
                vm.ensure_key(cache, log=quiet)
        key, pub = vm.ensure_key(cache, log=quiet)
        self.assertEqual((key, pub), vm.key_paths(cache))
        self.assertEqual(mode_of(key), 0o600)  # whatever ssh-keygen left it as
        self.assertEqual(read_file(pub).decode().strip(), KEY)
        self.assertEqual(sorted(os.listdir(cache)), sorted([vm.MARKER, "images", "runs", "key", "key.pub"]))
        self.assertEqual(vm.ensure_key(cache, log=quiet), (key, pub))  # the same key from now on
        self.assertEqual(self.calls(), ["ssh-keygen -q -t ed25519 -N  -C sclab-lab -f %s.part" % key] * 2)


class FakeBoot:
    """What _await_provisioned needs of a Vm, its Qmp and its SerialTap:
    the console's text, and the SHUTDOWN event if the guest powered off."""

    def __init__(self, text, off=True, alive=True):
        self.text, self.off, self.up = text.encode(), off, alive
        self.qmp = self
        self.proc = types.SimpleNamespace(returncode=1)
        self.qemu_log = os.devnull

    def data(self):
        return self.text

    def wait_event(self, names, since=0, timeout=None):
        return {"event": "SHUTDOWN", "data": {"guest": True, "reason": "guest-shutdown"}} if self.off else None

    def wait_exit(self, timeout):
        return 0

    def alive(self):
        return self.up


class TestProvision(CacheCase):
    """The reference image, with the boot itself replaced: what
    provision() leaves in the cache, and what counts as a good boot."""

    IMAGE = b"the pinned image\n"

    def setUp(self):
        super().setUp()
        self.cache = vm.ensure_cache(self.path("c"))
        self.conf.update(IMAGE_SHA256=hashlib.sha256(self.IMAGE).hexdigest(), QEMU="sclab-test-qemu",
                         OVMF_CODE=self.file("CODE.fd", "code"), OVMF_VARS=self.file("VARS.fd", "vars"),
                         PROVISION_RETRIES="2")
        self.image = self.file(os.path.join("c", "images", os.path.basename(vm.image_path(self.conf, self.cache))),
                               self.IMAGE)
        self.file(os.path.join("c", "key"), "PRIVATE", 0o600)
        self.file(os.path.join("c", "key.pub"), KEY + "\n")
        for tool in ("sclab-test-qemu", "ssh-keygen"):  # they must be there; neither is run
            self.stub(tool, "exit 1")
        self.stub("qemu-img", 'exit "${QEMU_IMG_RC:-0}"')
        self.boots, self.panics = [], 0

        class Seed(vm.SeedServer):  # bound, but no guest will ask: no thread to serve it (and to wait for)
            def start(self):
                return self

        for p in (mock.patch.object(vm, "_provision_attempt", self.boot), mock.patch.object(vm, "SeedServer", Seed)):
            p.start()
            self.addCleanup(p.stop)

    def boot(self, conf, image, part, run, h12, seed, attempt, log):
        """A provisioning boot: the disk and the VARS it leaves, and a
        panic in the first self.panics of them."""
        self.boots.append(attempt)
        self.assertEqual(image, self.image)
        for f, data in ((part, b"disk %d" % attempt), (os.path.join(run, "VARS.fd"), b"vars %d" % attempt)):
            with open(f, "wb") as fo:
                fo.write(data)
        if attempt <= self.panics:
            return {"outcome": "panic", "why": "Kernel panic - not syncing"}
        return {"outcome": "ok", "kernel": conf["KERNEL"], "grub_no_timer_check": 1}

    def provision(self, **kw):
        return vm.provision(self.conf, self.cache, log=quiet, **kw)

    def refs(self):
        return sorted(f for f in os.listdir(self.cache) if f.startswith("ref-"))

    def test_three_read_only_files(self):
        ref = self.provision()
        self.assertTrue(ref["ready"])
        self.assertEqual(self.refs(), ["ref-%s.%s" % (ref["h12"], ext) for ext in ("VARS.fd", "json", "qcow2")])
        for k in ("qcow2", "vars", "json"):  # every run's overlay rests on them
            self.assertEqual(mode_of(ref[k]), 0o444, k)
        self.assertEqual((read_file(ref["qcow2"]), read_file(ref["vars"])), (b"disk 1", b"vars 1"))
        meta = json.loads(read_file(ref["json"]))
        self.assertEqual((meta["h"], meta["image_sha256"], meta["kernel"], meta["attempts"]),
                         (ref["h"], self.conf["IMAGE_SHA256"], self.conf["KERNEL"], 1))
        self.assertEqual(meta["ovmf_code_sha256"], hashlib.sha256(b"code").hexdigest())  # P.5 compares it
        self.assertEqual(self.calls(), ["qemu-img check %s.part" % ref["qcow2"]])
        self.assertFalse(os.path.exists(os.path.join(meta["run"], "VARS.fd")))
        self.provision()  # it is there: no boot
        self.assertEqual(self.boots, [1])
        self.provision(force=True)
        self.assertEqual((self.boots, mode_of(ref["qcow2"])), ([1, 1], 0o444))

    def test_the_qcow2_appears_last(self):
        # A reference image is one whose three files are there. The disk
        # is the one a run opens: it must never be there without the others.
        ref = vm.ref_info(self.conf, self.cache)
        there, replace = [], os.replace

        def watching(src, dst):
            if dst == ref["qcow2"]:
                there.append([os.path.exists(ref[k]) for k in ("vars", "json")])
            replace(src, dst)

        with mock.patch.object(vm.os, "replace", watching):
            self.provision()
        self.assertEqual(there, [[True, True]])

    def test_a_failed_qemu_img_check_leaves_nothing(self):
        with mock.patch.dict(os.environ, {"QEMU_IMG_RC": "2"}):  # corruption
            with self.assertRaisesRegex(vm.LabError, r"qemu-img check .* failed \(exit 2\)"):
                self.provision()
        self.assertEqual(self.refs(), [])  # no file of it, and no .part
        with self.assertRaisesRegex(vm.LabError, "no reference image"):
            vm.find_ref(self.conf, self.cache)
        with mock.patch.dict(os.environ, {"QEMU_IMG_RC": "3"}):  # leaked clusters only
            self.assertTrue(self.provision()["ready"])

    def test_a_panic_gets_a_new_disk_a_few_times(self):
        self.panics = 2
        ref = self.provision()
        self.assertEqual((self.boots, read_file(ref["qcow2"])), ([1, 2, 3], b"disk 3"))
        self.assertEqual(json.loads(read_file(ref["json"]))["attempts"], 3)
        self.panics = 9
        with self.assertRaisesRegex(vm.LabError, "the guest panicked 3 times"):
            self.provision(force=True)
        self.assertEqual((self.boots[3:], self.refs()), ([1, 2, 3], []))

    def test_only_the_pinned_image_is_booted(self):
        with open(self.image, "ab") as f:
            f.write(b"one more byte")
        with self.assertRaisesRegex(vm.LabError, "lab.conf pins"):
            self.provision()
        with self.assertRaisesRegex(vm.LabError, "lab.conf pins"):
            self.provision(force=True)
        self.assertEqual((self.boots, self.refs()), ([], []))

    def test_one_at_a_time(self):
        with vm.lab_lock(self.cache):
            with self.assertRaisesRegex(vm.LabError, "another lab command holds"):
                self.provision()
        self.assertEqual(self.boots, [])
        # Another provision ended between this one's look and its lock:
        # what it made is kept, not deleted and built once more.
        ref, lab_lock = vm.ref_info(self.conf, self.cache), vm.lab_lock

        @contextlib.contextmanager
        def after_the_other(cache, guard=True):
            for k in ("qcow2", "vars", "json"):
                self.file(os.path.join("c", os.path.basename(ref[k])), "theirs", 0o444)
            with lab_lock(cache, guard) as path:
                yield path

        with mock.patch.object(vm, "lab_lock", after_the_other):
            self.assertTrue(self.provision()["ready"])
        self.assertEqual((self.boots, read_file(ref["qcow2"])), ([], b"theirs"))

    CONSOLE = ("[    0.000000] Linux version %s (buildd@lcy02-amd64-001) #152-Ubuntu SMP\n"
               "sclab: grub.cfg no_timer_check lines: 3\n"
               "SCLAB-PROVISIONED sclab-0123456789ab after 412 s\n[  620.11] reboot: Power down\n")

    def awaited(self, text, **kw):
        boot = FakeBoot(text, **kw)
        return vm._await_provisioned(self.conf, boot, boot, 0, "0123456789ab", quiet)

    def test_a_good_boot_is_the_pinned_kernel_to_the_end(self):
        kernel = self.conf["KERNEL"]
        good = self.CONSOLE % kernel
        self.assertEqual(self.awaited(good), {"outcome": "ok", "kernel": kernel, "grub_no_timer_check": 3})
        # Another kernel in the image: what the lab measured on the pinned one no longer holds.
        with self.assertRaisesRegex(vm.LabError, "boots kernel 6.8.0-999-generic; lab.conf pins KERNEL=%s" % kernel):
            self.awaited(self.CONSOLE % "6.8.0-999-generic")
        self.assertEqual(self.awaited(good.replace("SCLAB-PROV", "Kernel panic - not syncing: x\nSCLAB-PROV")),
                         {"outcome": "panic", "why": "Kernel panic - not syncing"})
        for text, why in ((good.replace("SCLAB-PROVISIONED", "SCLAB-PROV"), "powered off .* before SCLAB-PROVISIONED"),
                          (good.replace("sclab-0123456789ab", "sclab-ba9876543210"), "before SCLAB-PROVISIONED"),
                          (good.replace("lines: 3", "lines: 0"), "left no_timer_check out of grub.cfg")):
            with self.assertRaisesRegex(vm.LabError, why):
                self.awaited(text)
        with self.assertRaisesRegex(vm.LabError, r"QEMU exited \(status 1\) without a SHUTDOWN event"):
            self.awaited(good, off=False, alive=False)
        self.conf["BUDGET_PROVISION"] = "0"
        with self.assertRaisesRegex(vm.LabError, "no SCLAB-PROVISIONED and power-off within 0 s"):
            self.awaited(good, off=False)

    def test_an_overlay_has_vars_of_its_own(self):
        ref = {"qcow2": self.file(os.path.join("c", "ref-0123456789ab.qcow2"), "disk", 0o444),
               "vars": self.file(os.path.join("c", "ref-0123456789ab.VARS.fd"), "vars", 0o444)}
        run = vm.new_run_dir(self.cache, "uefi", "abc1234")
        disk, vars_path = vm.make_overlay(ref, run, "uefi")
        # OVMF writes its variables: to the run's copy, never to the reference's.
        self.assertEqual((read_file(vars_path), mode_of(vars_path), mode_of(ref["vars"])), (b"vars", 0o600, 0o444))
        self.assertEqual(os.path.dirname(vars_path), run)
        self.assertEqual(self.calls(), ["qemu-img create -q -f qcow2 -b ../../ref-0123456789ab.qcow2 -F qcow2 " + disk])
        run = vm.new_run_dir(self.cache, "bios", "abc1234")
        self.assertEqual(vm.make_overlay(ref, run, "bios"), (os.path.join(run, "disk.qcow2"), None))
        self.assertEqual(os.listdir(run), ["evidence"])
        with mock.patch.dict(os.environ, {"QEMU_IMG_RC": "1"}):
            with self.assertRaisesRegex(vm.LabError, "qemu-img create"):
                vm.make_overlay(ref, run, "bios")


# -- vm.py: QMP, and QEMU as a child ----------------------------------------------

class FakeQmp:
    """QEMU's QMP monitor on a unix socket, for one client: the greeting,
    then for each command what answer(msg) returns ({"return": ...} or
    {"error": ...}; None: no reply). event() sends an event."""

    def __init__(self, path, answer):
        self.path, self.answer = path, answer
        self.got = []
        self.conn = None
        self._lock = threading.Lock()
        self.srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.srv.bind(path)
        self.srv.listen(1)
        self.thread = threading.Thread(target=self._serve, daemon=True)
        self.thread.start()

    def _serve(self):
        try:
            self.conn, _ = self.srv.accept()
            self.send({"QMP": {"version": {"qemu": {"major": 8, "minor": 2, "micro": 2}}, "capabilities": ["oob"]}})
            for line in self.conn.makefile("rb"):
                msg = json.loads(line)
                self.got.append(msg)
                reply = self.answer(msg)
                if reply is not None:
                    self.send(dict(reply, id=msg["id"]))
        except OSError:
            pass

    def send(self, msg):
        with self._lock:
            self.conn.sendall((json.dumps(msg) + "\r\n").encode())

    def event(self, name, **data):
        self.send({"event": name, "data": data, "timestamp": {"seconds": 1759680000, "microseconds": 0}})

    def close(self):
        for s in (self.conn, self.srv):
            if s is not None:
                try:
                    s.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass
                s.close()
        self.thread.join(5)


class TestQmp(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.qemu = FakeQmp(os.path.join(self.tmp.name, "qmp.sock"), self.answer)
        self.addCleanup(self.qemu.close)
        self.qmp = vm.Qmp(self.qemu.path, os.path.join(self.tmp.name, "qmp.log"))
        self.addCleanup(self.qmp.close)
        self.qmp.connect(timeout=10)

    def answer(self, msg):
        name = msg["execute"]
        if name == "query-status":
            return {"return": {"running": True, "status": "running"}}
        if name == "system_wakeup":
            return {"error": {"class": "GenericError", "desc": "Unable to wake up: guest is not in suspended state"}}
        if name == "stall":
            return None
        if name == "die":  # QEMU goes away with the command unanswered
            self.qemu.conn.shutdown(socket.SHUT_RDWR)
            return None
        return {"return": {}}

    def test_commands_and_their_replies(self):
        self.assertEqual(self.qmp.greeting["QMP"]["capabilities"], ["oob"])
        self.assertEqual([m["execute"] for m in self.qemu.got], ["qmp_capabilities"])
        self.assertEqual(self.qmp.cmd("query-status"), {"running": True, "status": "running"})
        self.assertEqual(self.qmp.cmd("send-key", {"keys": []}), {})
        self.assertEqual(self.qemu.got[-1], {"execute": "send-key", "arguments": {"keys": []}, "id": "sc3"})
        # QEMU's "no" is an error: a reset or a key it refused did not happen.
        with self.assertRaisesRegex(vm.QmpError, "QMP system_wakeup: GenericError: Unable to wake up"):
            self.qmp.cmd("system_wakeup")
        with self.assertRaisesRegex(vm.QmpError, "no QMP reply to stall in 0.2 s"):
            self.qmp.cmd("stall", timeout=0.2)
        self.assertEqual(self.qmp.cmd("query-status")["status"], "running")  # each reply goes to its command

    def test_wait_event_from_a_mark(self):
        # e2e marks, resets, and waits for the RESET that follows: one it
        # has seen already must not answer.
        self.qemu.event("RESET", guest=False, reason="host-qmp-system-reset")
        first = self.qmp.wait_event("RESET", timeout=10)
        self.assertEqual((first["seq"], first["data"]["reason"]), (0, "host-qmp-system-reset"))
        mark = self.qmp.mark()
        self.assertEqual(mark, 1)
        self.assertIsNone(self.qmp.wait_event("RESET", since=mark, timeout=0.1))
        self.qemu.event("STOP")
        self.qemu.event("RESET", guest=True, reason="guest-reset")
        ev = self.qmp.wait_event(("SHUTDOWN", "RESET"), since=mark, timeout=10)
        self.assertEqual((ev["seq"], ev["data"]["reason"]), (2, "guest-reset"))
        self.assertEqual([e["event"] for e in self.qmp.events_since(mark)], ["STOP", "RESET"])
        self.assertEqual([e["seq"] for e in self.qmp.events_since(0, "RESET")], [0, 2])

    def test_listeners_run_before_a_waiter_sees_the_event(self):
        # e2e's listener marks the serial log at a RESET, and whoever
        # waited for the RESET reads from that mark: it must be there.
        seen = []

        def broken(ev):
            raise RuntimeError("a listener's bug")

        self.qmp.listeners += [broken, lambda ev: seen.append((ev["event"], ev["seq"], len(self.qmp.events)))]
        self.qemu.event("RESET")
        self.assertIsNotNone(self.qmp.wait_event("RESET", timeout=10))
        self.assertEqual(seen, [("RESET", 0, 0)])  # called, and the event not yet in events
        self.qemu.event("SHUTDOWN")  # the reader lives on after the bug
        self.assertIsNotNone(self.qmp.wait_event("SHUTDOWN", timeout=10))
        self.assertEqual(seen[1:], [("SHUTDOWN", 1, 1)])
        self.qmp.close()
        self.assertIn(b"failed: RuntimeError", read_file(os.path.join(self.tmp.name, "qmp.log")))

    def test_qemu_gone(self):
        t = time.monotonic()
        with self.assertRaisesRegex(vm.QmpError, "QMP closed before the reply to die"):
            self.qmp.cmd("die", timeout=20)
        self.assertIsNone(self.qmp.wait_event("SHUTDOWN", timeout=20))  # none will come
        self.assertLess(time.monotonic() - t, 10)
        with self.assertRaisesRegex(vm.QmpError, "QMP is closed: cannot run cont"):
            self.qmp.cmd("cont")
        self.qmp.close()
        self.assertFalse(self.qmp._thread.is_alive())

    def test_an_event_sent_before_qemu_exits_is_not_lost_to_abort(self):
        # 5.2 of the fc47ea3 runs, both modes: the guest powered off, QEMU
        # sent SHUTDOWN and exited 0. e2e's listener marked the serial log
        # (mux.mark drains for 1 s while the serial loop reconnects), and
        # wait_event's abort (QEMU not alive) said None before the reader had
        # the event in events: "no SHUTDOWN after poweroff (QEMU exited)".
        self.qmp.listeners.append(lambda ev: time.sleep(1.5))
        self.qemu.event("SHUTDOWN", guest=True, reason="guest-shutdown")
        self.qemu.close()  # QEMU is gone the moment it has sent it
        t = time.monotonic()
        ev = self.qmp.wait_event("SHUTDOWN", timeout=20, abort=lambda: True)
        self.assertIsNotNone(ev, "the SHUTDOWN QEMU sent before exiting was lost")
        self.assertEqual((ev["seq"], ev["data"]["reason"]), (0, "guest-shutdown"))
        self.assertLess(time.monotonic() - t, 10)
        # Gone with nothing in flight: None, as soon as the reader is at EOF.
        # (wait_event returns the SHUTDOWN once it is in events, which can be
        # before the reader reads EOF: closed is asked only after this wait.
        # Asked before it, CI failed one run in two, and one in 25 here
        # under load.)
        self.assertIsNone(self.qmp.wait_event("RESET", timeout=20, abort=lambda: True))
        self.assertTrue(self.qmp.closed)
        self.assertLess(time.monotonic() - t, 10)


# A stand-in for QEMU: serial.sock and qmp.sock in its working directory, a
# QMP that says yes to every command, and "quit" ends it. With
# STAND_IN=deaf it does not end, by quit or by SIGTERM. It writes who it is
# to child.json first. Its file name is not a QEMU's: vm.running_qemu() of
# a lab that really runs on this machine does not count it.
STAND_IN = r"""#!%s
import ctypes, json, os, signal, socket, sys
how = os.environ.get("STAND_IN")
if how == "broken":
    sys.exit("stand-in: could not open the disk")
if how == "deaf":
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
pdeathsig = ctypes.c_int(-1)
ctypes.CDLL(None).prctl(2, ctypes.byref(pdeathsig), 0, 0, 0)  # PR_GET_PDEATHSIG
with open("child.json", "w") as f:
    json.dump({"pid": os.getpid(), "sid": os.getsid(0), "pgid": os.getpgid(0), "nice": os.nice(0),
               "pdeathsig": pdeathsig.value, "stdin": os.readlink("/proc/self/fd/0"), "argv": sys.argv[1:]}, f)
ser, qmp = socket.socket(socket.AF_UNIX), socket.socket(socket.AF_UNIX)
ser.bind("serial.sock")
ser.listen(1)
qmp.bind("qmp.sock")
qmp.listen(1)
c, _ = qmp.accept()
c.sendall(b'{"QMP": {"version": {}, "capabilities": []}}\r\n')
for line in c.makefile("rb"):
    m = json.loads(line)
    c.sendall((json.dumps({"return": {}, "id": m["id"]}) + "\r\n").encode())
    if m["execute"] == "quit" and how != "deaf":
        sys.exit(0)
while how == "deaf":
    signal.pause()
"""


class TestVmProcess(CacheCase):
    """Vm with a stand-in for QEMU: how the child is started, and that
    stop() leaves none."""

    def setUp(self):
        super().setUp()
        self.conf.update(QEMU=self.file("stand-in", STAND_IN % sys.executable, 0o755), NICE="3")
        os.mkdir(self.path("run"))
        self.said = []
        # The stand-in forwards no port: whatever else on this machine takes that one is not a leftover.
        p = mock.patch.object(vm, "port_open", lambda port, timeout=1.0: False)
        p.start()
        self.addCleanup(p.stop)

    def start(self, how=""):
        m = vm.Vm(self.conf, "bios", self.path("run"), self.path("disk.qcow2"), log=self.said.append)
        self.addCleanup(self.reap, m)
        with mock.patch.dict(os.environ, {"STAND_IN": how}):
            return m.start(timeout=30)

    @staticmethod
    def reap(m):
        if m.proc is not None and m.proc.poll() is None:
            m.proc.kill()
            m.proc.wait(10)
        if m.qmp is not None:
            m.qmp.close()

    def test_qemu_is_a_child_that_cannot_outlive_the_lab(self):
        m = self.start()
        child = json.loads(read_file(self.path("run", "child.json")))
        self.assertEqual((child["pid"], child["argv"]), (m.proc.pid, m.args[1:]))
        # A session of its own: Ctrl-C and a hangup of the terminal go to
        # the lab alone, which then stops QEMU in order.
        self.assertEqual((child["sid"], child["pgid"]), (m.proc.pid, m.proc.pid))
        self.assertNotEqual(child["sid"], os.getsid(0))
        # And should the lab die with no teardown, the kernel ends QEMU.
        self.assertEqual(child["pdeathsig"], signal.SIGKILL)
        self.assertEqual((child["nice"], child["stdin"]), (min(19, os.nice(0) + 3), "/dev/null"))
        self.assertEqual(m.leftovers(), ["QEMU pid %d still runs" % m.proc.pid, "the QMP reader thread still runs"])
        self.assertEqual(m.stop(grace=5), 0)  # by QMP's quit
        self.assertEqual((m.alive(), m.leftovers(), self.said), (False, [], []))
        self.assertEqual([f for f in os.listdir(self.path("run")) if f.endswith(".sock")], [])
        self.assertIn(b'"execute": "quit"', read_file(m.qmp_log))
        self.assertEqual(m.stop(), 0)  # once more does no harm

    def test_a_qemu_that_does_not_quit_is_killed(self):
        m = self.start("deaf")
        self.assertEqual(m.stop(grace=0.3), -signal.SIGKILL)
        self.assertEqual((m.alive(), m.leftovers()), (False, []))
        self.assertEqual(self.said, ["vm: QEMU pid %d did not quit in 0.3 s: SIGKILL" % m.proc.pid])

    def test_a_qemu_that_does_not_start(self):
        with self.assertRaisesRegex(vm.LabError, r"QEMU did not start \(it exited, exit 1\)") as c:
            self.start("broken")
        self.assertIn("stand-in: could not open the disk", str(c.exception))


if __name__ == "__main__":
    unittest.main()
