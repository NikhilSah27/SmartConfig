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
import hashlib
import io
import os
import re
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import unittest
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
    @unittest.skipUnless(has("render_seed"), "vm.render_seed(h12, pub) is not there yet")
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

    @unittest.skipUnless(has("render_seed"), "vm.render_seed(h12, pub) is not there yet")
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

    @unittest.skipUnless(has("qemu_args") and has("load_conf"),
                         "vm.qemu_args(conf, mode, ssh_port, name, disk, vars_path, seed_url) is not there yet")
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

    @unittest.skipUnless(has("ref_hash"), "vm.ref_hash(image_sha256, user_data_in, meta_data_in, "
                         "sclab_cfg, key_pub, provision_rev) is not there yet")
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


if __name__ == "__main__":
    unittest.main()
