#!/usr/bin/env python3
"""serialmux.py: the lab's one client of QEMU's serial socket.

QEMU's socket chardev serves one client at a time, so this is the only
reader and writer of the guest's serial console. It keeps these logs in
the run directory:

  serial.raw   every byte from the guest, untouched
  serial.txt   the same through Cleaner: ANSI escapes and CRs out, cursor
               moves as newlines (what console.py's expect reads)
  serial.ts    serial.txt with "[+SSSS.SS] " (seconds since t0) per line
  input.log    every byte and key sent to the guest, one line per write:
               "+SECS KIND DATA", KIND serial (DATA in hex), sock (from
               input.sock, hex), sendkey (a QEMU key name), error
  marks.log    "+SECS LABEL raw=N txt=N input=N": the logs' sizes at a
               mark (QMP RESET or SHUTDOWN), where a boot's text starts
  mux.log      the mux's own events (connects, resets, client errors, and
               "gap": the mux's thread did not run for over GAP s, so the
               host stalled or was paused; Mux.gaps keeps them)

As a thread (lab/e2e.py):

  mux = Mux(run, t0=t0).start()    # attaches before QEMU's "cont"
  mux.write(b"\\x0e")               # one write, logged
  mux.mark("RESET")                # at each QMP RESET or SHUTDOWN event
  mux.stop()

From the command line, "serialmux.py RUNDIR" attaches the same way (t0
from RUNDIR/t0), forwards what clients write to RUNDIR/input.sock, and
exits when QEMU closes the serial socket. "serialmux.py --clean FILE"
prints a raw capture as serial.txt would hold it.

A reset of the serial connection (ConnectionResetError, BrokenPipeError)
never raises: the mux tries to reconnect for a few seconds, then stops
and says why in mux.log; a failed write is logged and returns False.
Python 3 standard library only.
"""
import collections
import os
import selectors
import signal
import socket
import sys
import threading
import time

LOGS = ("serial.raw", "serial.txt", "serial.ts")
# The loop wakes five times a second. A wake this late was not the guest's
# doing: the lab's own process did not run.
GAP = 10.0


class MuxError(Exception):
    """The serial socket could not be reached."""


class Cleaner:
    """The serial byte stream as plain text, for expect and for people.

    Escape sequences go: CSI (ESC [ ... final), OSC (ESC ] ... BEL or
    ESC \\), charset selection (ESC ( x) and two-byte escapes (ESC M,
    ESC 7, ESC =). So do CR, BS, BEL, DEL and the other C0 controls but
    LF and TAB. A cursor position (CSI H or f) becomes a newline: GRUB
    draws its menu with them, which then reads one entry per line
    ("*Ubuntu", " Advanced options for Ubuntu"). Bytes from 0x80 up pass
    as they are (UTF-8 box drawing). The state carries over between
    feeds, so a sequence may span two reads.
    """

    def __init__(self):
        self.state = "n"  # n: text, e: ESC, c: CSI, o: OSC, s: charset

    def feed(self, data):
        out = bytearray()
        st = self.state
        for ch in data:
            if st == "n":
                if ch == 0x1B:
                    st = "e"
                elif ch == 0x0A or ch == 0x09 or (ch >= 0x20 and ch != 0x7F):
                    out.append(ch)
            elif st == "e":
                if ch == 0x5B:  # [
                    st = "c"
                elif ch == 0x5D:  # ]
                    st = "o"
                elif ch in (0x28, 0x29, 0x2A, 0x2B):  # ( ) * +
                    st = "s"
                elif ch != 0x1B:  # ESC ESC: the second one starts anew
                    st = "n"
            elif st == "c":
                if 0x40 <= ch <= 0x7E:
                    st = "n"
                    if ch in (0x48, 0x66):  # H, f: cursor position
                        out.append(0x0A)
                elif ch == 0x1B:
                    st = "e"
            elif st == "o":
                if ch == 0x07:
                    st = "n"
                elif ch == 0x1B:  # ST is ESC \, which state e ends
                    st = "e"
            else:  # s: the charset byte
                st = "n"
        self.state = st
        return bytes(out)


class Stamper:
    """Puts "[+SSSS.SS] " (seconds since t0) before each line of text."""

    def __init__(self, t0, clock=time.time):
        self.t0 = t0
        self.clock = clock
        self.bol = True

    def feed(self, text):
        if not text:
            return b""
        stamp = ("[+%8.2f] " % (self.clock() - self.t0)).encode()
        out = bytearray()
        parts = text.split(b"\n")
        for i, part in enumerate(parts):
            last = i == len(parts) - 1
            if last and not part:
                break
            if self.bol:
                out += stamp
                self.bol = False
            out += part
            if not last:
                out += b"\n"
                self.bol = True
        return bytes(out)


def clean(data):
    """data (bytes) as Cleaner turns it into serial.txt."""
    return Cleaner().feed(data)


# One input.log entry: seconds since t0, kind, data (hex for bytes), note.
Entry = collections.namedtuple("Entry", "t kind data note")

# A mark: the logs' sizes when it was taken. raw and txt are byte offsets
# into serial.raw and serial.txt, inp an index into input.log's entries.
Mark = collections.namedtuple("Mark", "label t raw txt inp")


def parse_input_log(text):
    """input.log's lines as Entry tuples (unparsable lines are skipped)."""
    out = []
    for line in text.splitlines():
        parts = line.split(" ", 3)
        if len(parts) < 2 or not parts[0].startswith("+"):
            continue
        try:
            t = float(parts[0][1:])
        except ValueError:
            continue
        out.append(Entry(t, parts[1], parts[2] if len(parts) > 2 else "",
                         parts[3] if len(parts) > 3 else ""))
    return out


def lone_escapes(entries):
    """The entries that sent an ESC byte not followed, in the same write,
    by "[" or "O" (an arrow key's sequence). GRUB takes a lone ESC to
    leave the menu, a second one for the grub> prompt: the lab never
    sends one, so any is a [lab] failure."""
    bad = []
    for e in entries:
        if e.kind not in ("serial", "sock"):
            continue
        try:
            data = bytes.fromhex(e.data)
        except ValueError:
            continue
        for i, b in enumerate(data):
            if b == 0x1B and data[i + 1:i + 2] not in (b"[", b"O"):
                bad.append(e)
                break
    return bad


def unix_path(path):
    """A path for an AF_UNIX socket: longer ones (the limit is 108 bytes)
    go through /proc/self/fd and a descriptor of the directory, which is
    returned too (keep it open while the path is in use; None if not)."""
    if len(os.fsencode(path)) < 100:
        return path, None
    fd = os.open(os.path.dirname(path) or ".", os.O_PATH | os.O_DIRECTORY)
    return "/proc/self/fd/%d/%s" % (fd, os.path.basename(path)), fd


class Mux:
    """Owns the serial socket: logs what the guest sends, sends what the
    lab types. Thread-safe; see the module doc for the files."""

    def __init__(self, run, serial="serial.sock", t0=None, listen=False,
                 connect_timeout=60.0, reconnect=5.0, clock=time.time):
        self.run = os.path.abspath(run)
        self.serial = serial if os.path.isabs(serial) else os.path.join(self.run, serial)
        if t0 is None:
            t0 = read_t0(self.run, clock)
        self.t0 = t0
        self.listen = listen
        self.connect_timeout = connect_timeout
        self.reconnect = reconnect
        self.clock = clock
        self.closed = False
        self.why = None  # why the mux stopped
        self.marks = []
        self.gaps = []  # (seconds since t0, how long): see GAP
        self._idle = 0  # the loop's wakes with nothing to read from the guest
        self._cv = threading.Condition()
        self._wlock = threading.Lock()
        self._stop = threading.Event()
        self._raw = bytearray()
        self._txt = bytearray()
        self._inputs = []
        self._cleaner = Cleaner()
        self._stamper = Stamper(t0, clock)
        self._ser = None
        self._lst = None
        self._fds = []
        self._files = {}
        self._thread = None

    # -- set up and tear down -------------------------------------------

    def _open(self):
        os.makedirs(self.run, exist_ok=True)
        for name in LOGS + ("input.log", "marks.log", "mux.log"):
            self._files[name] = open(os.path.join(self.run, name), "ab", buffering=0)

    def _append(self, name, data):
        """Appends to one of the logs; once they are closed (a mark or a
        note racing the teardown), nothing."""
        f = self._files.get(name)
        if f is not None:
            try:
                f.write(data)
            except (OSError, ValueError):
                pass

    def _log(self, msg):
        self._append("mux.log", ("+%.2f %s\n" % (self.clock() - self.t0, msg)).encode())

    def _connect(self, timeout):
        """Connects to QEMU's serial socket, retrying until timeout."""
        path, fd = unix_path(self.serial)
        if fd is not None:
            self._fds.append(fd)
        deadline = time.monotonic() + timeout
        last = None
        while True:
            s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            try:
                s.connect(path)
                # A guest that stops reading must not hang a writer.
                s.settimeout(10)
                return s
            except OSError as e:
                s.close()
                last = e
            if self._stop.is_set() or time.monotonic() >= deadline:
                self._log("serial: no connection: %s" % last)
                return None
            time.sleep(0.1)

    def _listen(self):
        path = os.path.join(self.run, "input.sock")
        try:
            os.unlink(path)
        except FileNotFoundError:
            pass
        p, fd = unix_path(path)
        if fd is not None:
            self._fds.append(fd)
        lst = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        lst.bind(p)
        lst.listen(4)
        return lst

    def attach(self):
        """Opens the logs and connects; raises MuxError if QEMU's socket
        does not answer within connect_timeout."""
        self._open()
        ser = self._connect(self.connect_timeout)
        if ser is None:
            self._close("no serial socket")
            raise MuxError("serialmux: cannot connect to %s" % self.serial)
        self._ser = ser
        self._log("serial: connected")
        if self.listen:
            self._lst = self._listen()
        return self

    def start(self):
        """attach(), then log in a daemon thread. Returns self."""
        self.attach()
        self._thread = threading.Thread(target=self._loop, name="serialmux", daemon=True)
        self._thread.start()
        return self

    def run_forever(self):
        """attach(), then log in this thread until QEMU closes the socket."""
        self.attach()
        self._loop()

    def stop(self, timeout=5.0):
        """Ends the loop and closes the socket and the logs."""
        self._stop.set()
        if self._thread is not None:
            self._thread.join(timeout)
        if not self.closed:
            self._close("stopped")

    def alive(self):
        return not self.closed

    def _close(self, why):
        with self._wlock:
            for s in (self._ser, self._lst):
                if s is not None:
                    try:
                        s.close()
                    except OSError:
                        pass
            self._ser = None
            if self._lst is not None:
                try:
                    os.unlink(os.path.join(self.run, "input.sock"))
                except OSError:
                    pass
                self._lst = None
            self._log("closed: %s" % why)
            files, self._files = self._files, {}
            for f in files.values():
                try:
                    f.close()
                except OSError:
                    pass
            for fd in self._fds:
                try:
                    os.close(fd)
                except OSError:
                    pass
            self._fds = []
        with self._cv:
            self.closed = True
            self.why = self.why or why
            self._cv.notify_all()

    # -- the loop ----------------------------------------------------------

    def _ingest(self, data):
        txt = self._cleaner.feed(data)
        ts = self._stamper.feed(txt)
        self._append("serial.raw", data)
        self._append("serial.txt", txt)
        self._append("serial.ts", ts)
        with self._cv:
            self._raw += data
            self._txt += txt
            self._cv.notify_all()

    def _gap(self, waited, wall=0.0):
        """Keeps a wake of the loop that came over GAP s late. A wall clock
        that jumped alone (the machine was paused, and its monotonic clock
        with it) took no time from any budget: logged, not kept."""
        if waited > GAP:
            with self._cv:
                self.gaps.append((round(self.clock() - self.t0, 2), round(waited, 1)))
            self._log("gap: the mux did not run for %.1f s" % waited)
        elif wall - waited > GAP:
            self._log("pause: the wall clock jumped %.1f s; the monotonic clock did not" % (wall - waited))

    def _loop(self):
        sel = selectors.DefaultSelector()
        why = "stopped"
        try:
            sel.register(self._ser, selectors.EVENT_READ, "ser")
            if self._lst is not None:
                sel.register(self._lst, selectors.EVENT_READ, "lst")
            while not self._stop.is_set():
                t, w = time.monotonic(), time.time()
                ready = sel.select(0.2)
                self._gap(time.monotonic() - t, time.time() - w)
                if not any(key.data == "ser" for key, _ in ready):
                    with self._cv:
                        self._idle += 1
                        self._cv.notify_all()
                for key, _ in ready:
                    if key.data == "ser":
                        try:
                            data = self._ser.recv(65536)
                        except (ConnectionResetError, OSError) as e:
                            self._log("serial: %s" % e.__class__.__name__)
                            data = b""
                        if data:
                            self._ingest(data)
                            continue
                        # QEMU closed or reset the connection.
                        sel.unregister(self._ser)
                        with self._wlock:
                            self._ser.close()
                            self._ser = None
                        self._log("serial: closed by QEMU")
                        ser = self._connect(self.reconnect)
                        if ser is None:
                            if not self._stop.is_set():
                                why = "QEMU closed the serial socket"
                            return
                        with self._wlock:
                            self._ser = ser
                        self._log("serial: reconnected")
                        sel.register(ser, selectors.EVENT_READ, "ser")
                    elif key.data == "lst":
                        try:
                            c, _ = self._lst.accept()
                        except OSError:
                            continue
                        sel.register(c, selectors.EVENT_READ, "cli")
                    else:
                        c = key.fileobj
                        try:
                            data = c.recv(65536)
                        except (ConnectionResetError, OSError) as e:
                            self._log("client: %s" % e.__class__.__name__)
                            data = b""
                        if not data:
                            sel.unregister(c)
                            c.close()
                        else:
                            self.write(data, kind="sock")
        except Exception as e:  # never a traceback from the logger thread
            why = "error: %r" % (e,)
            self._log(why)
        finally:
            for key in list(sel.get_map().values()):
                if key.data == "cli":
                    key.fileobj.close()
            sel.close()
            self._close(why)

    # -- input -------------------------------------------------------------

    def _input(self, kind, data, note=""):
        t = self.clock() - self.t0
        e = Entry(round(t, 2), kind, data, note)
        line = "+%.2f %s %s" % (t, kind, data)
        if note:
            line += " " + note
        self._append("input.log", (line + "\n").encode())
        with self._cv:
            self._inputs.append(e)

    def write(self, data, pace=0.0, kind="serial"):
        """Sends data to the guest: in one write, or with pace (seconds)
        one byte at a time. Logs it to input.log first. Returns False
        (and logs why) if the socket is gone."""
        if isinstance(data, str):
            data = data.encode()
        with self._wlock:
            self._input(kind, data.hex())
            if self._ser is None:
                self._input("error", "", "%s: not connected" % kind)
                return False
            try:
                if pace:
                    for b in data:
                        self._ser.sendall(bytes([b]))
                        time.sleep(pace)
                else:
                    self._ser.sendall(data)
            except (BrokenPipeError, ConnectionResetError, OSError) as e:
                self._input("error", "", "%s: %s" % (kind, e.__class__.__name__))
                return False
        return True

    def note(self, kind, text):
        """Logs a key sent another way (QMP send-key) to input.log."""
        with self._wlock:
            self._input(kind, text)

    # -- reading -----------------------------------------------------------

    def txt(self, start=0, end=None):
        """serial.txt's bytes from start (to end)."""
        with self._cv:
            return bytes(self._txt[start:end])

    def raw(self, start=0, end=None):
        with self._cv:
            return bytes(self._raw[start:end])

    def size(self):
        with self._cv:
            return len(self._txt)

    def inputs(self, start=0):
        """input.log's entries from index start (a Mark's inp)."""
        with self._cv:
            return list(self._inputs[start:])

    def wait(self, size, timeout):
        """Waits until serial.txt is longer than size, the mux closed or
        timeout passed; returns the current size."""
        with self._cv:
            if len(self._txt) <= size and not self.closed:
                self._cv.wait(timeout)
            return len(self._txt)

    def mark(self, label, drain=1.0):
        """Records where the logs are now (a boot starts at a QMP RESET).
        The caller is another thread (QMP's reader): what the guest wrote
        before the event may still be unread here, and would land after
        the mark, in the next boot's text. So it waits, at most drain s,
        for the loop to find nothing more to read."""
        with self._cv:
            if drain and self._thread is not None and self._thread.is_alive() \
                    and threading.current_thread() is not self._thread:
                idle, deadline = self._idle, time.monotonic() + drain
                while self._idle == idle and not self.closed and time.monotonic() < deadline:
                    self._cv.wait(deadline - time.monotonic())
            m = Mark(label, round(self.clock() - self.t0, 2), len(self._raw),
                     len(self._txt), len(self._inputs))
            self.marks.append(m)
        line = "+%.2f %s raw=%d txt=%d input=%d\n" % (m.t, label, m.raw, m.txt, m.inp)
        self._append("marks.log", line.encode())
        return m


def read_t0(run, clock=time.time):
    try:
        with open(os.path.join(run, "t0")) as f:
            return float(f.read().strip())
    except (OSError, ValueError):
        return clock()


def main(argv):
    if len(argv) == 3 and argv[1] == "--clean":
        with open(argv[2], "rb") as f:
            sys.stdout.buffer.write(clean(f.read()))
        return 0
    if len(argv) != 2 or argv[1].startswith("-"):
        sys.stderr.write("usage: serialmux.py RUNDIR | serialmux.py --clean FILE\n")
        return 2
    mux = Mux(argv[1], listen=True)

    def leave(signum, frame):
        raise SystemExit(0)

    signal.signal(signal.SIGTERM, leave)
    signal.signal(signal.SIGHUP, leave)
    try:
        mux.run_forever()
    except MuxError as e:
        sys.stderr.write("%s\n" % e)
        return 1
    except KeyboardInterrupt:
        mux.stop()
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
