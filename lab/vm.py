#!/usr/bin/env python3
"""vm.py: the machine side of the SmartConfig QEMU rescue lab (M4 step 11).

The cache, the pinned cloud image, the lab key, the reference image
(provisioning), per-run overlays, QEMU under TCG, a QMP client, the lock
that allows one VM at a time, ssh and scp, and gc. lab/e2e.py imports it.
The command line is for setup and debugging:

  vm.py image [--from FILE]    put the pinned image in the cache: download it,
                               or copy FILE; either way its sha256 must be the pin
  vm.py provision [--force]    build the reference image ref-<h12>.qcow2 from it
                               (UEFI, cloud-init; about 30 min under TCG)
  vm.py up uefi|bios [--keep]  boot a throwaway overlay of the reference image;
                               stays in the foreground until the VM stops
  vm.py ssh [CMD ...]          ssh to the VM that "up" runs, as owner
  vm.py console                its serial console (Ctrl-] leaves)
  vm.py stop                   stop it ("up" tears it down)
  vm.py gc [--keep N] [-n]     remove old runs, stale reference images, .part files

Everything lives in the cache, ${SC_LAB_CACHE:-${XDG_CACHE_HOME:-~/.cache}/
smartconfig-lab}. No sudo, no KVM, nothing on the host outside the cache.
Python 3 standard library only; dev use only, never shipped.
"""
import argparse
import contextlib
import ctypes
import fcntl
import hashlib
import http.server
import json
import os
import re
import selectors
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time

LAB = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(LAB)
CONF_PATH = os.path.join(LAB, "lab.conf")

GUEST_USER = "owner"  # lab/cloud-init/user-data.in
GUEST_HOSTNAME = "sclab"  # lab/cloud-init/{user,meta}-data.in
MODES = ("uefi", "bios")
VGA_TEXT = 0xB8000  # the VGA text buffer: 80x25 cells of (character, attribute)

# ssh options for every call (design section 4, channel SSH). LogLevel=ERROR
# keeps "Permanently added ... to the list of known hosts" out of stderr,
# which some checks read.
SSH_OPTIONS = (
    "-F", "/dev/null",
    "-o", "IdentitiesOnly=yes",
    "-o", "IdentityAgent=none",
    "-o", "UserKnownHostsFile=/dev/null",
    "-o", "StrictHostKeyChecking=no",
    "-o", "BatchMode=yes",
    "-o", "ConnectTimeout=5",
    # 12 keepalives unanswered (60 s), not ssh's 3 (15 s): under TCG on a
    # loaded host a busy guest's sshd went silent past 15 s right after
    # update-grub, and ssh gave up with exit 255 and no word (LogLevel),
    # twice in a row (the 526f14b sign-off runs, bios 6.2). Every call has
    # its own timeout anyway.
    "-o", "ServerAliveInterval=5",
    "-o", "ServerAliveCountMax=12",
    "-o", "LogLevel=ERROR",
)

PANIC_RE = re.compile(rb"Kernel panic - not syncing|IO-APIC \+ timer doesn't work")

_REQUIRED = (
    "IMAGE_BASE_URL", "IMAGE_RELEASE", "IMAGE_NAME", "IMAGE_SHA256", "KERNEL",
    "PROVISION_REV", "DISK_SIZE", "QEMU", "OVMF_CODE", "OVMF_VARS",
    "SMP", "MEM", "NICE", "KEEP_RUNS", "BUDGET_PROVISION", "PROVISION_RETRIES",
)
_INTS = ("SMP", "MEM", "NICE", "KEEP_RUNS", "PROVISION_RETRIES", "SSH_PROBE_EVERY", "RESET_SETTLE")


class LabError(Exception):
    """A problem of the lab itself (a tool, the image, QEMU, the guest's
    plumbing). e2e reports it as [lab], never as an M4 failure."""


class QmpError(LabError):
    """QMP failed: an error reply, no reply in time, or QEMU went away."""


def log(msg):
    print(msg, flush=True)


# ---------------------------------------------------------------- lab.conf


class Conf(dict):
    """lab.conf as a dict of strings, with int() for numbers."""

    def int(self, key):
        try:
            return int(self[key])
        except KeyError:
            raise LabError("lab.conf has no %s" % key) from None
        except ValueError:
            raise LabError("lab.conf: %s=%s is not a number" % (key, self[key])) from None


def parse_conf(text, name="lab.conf"):
    """Parse KEY=VALUE lines; # comments and blank lines are skipped."""
    conf = Conf()
    for n, line in enumerate(text.splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        m = re.match(r"^([A-Z][A-Z0-9_]*)=(.*)$", line)
        if not m:
            raise LabError("%s:%d: not KEY=VALUE: %r" % (name, n, line))
        key, value = m.group(1), m.group(2).strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        if key in conf:
            raise LabError("%s:%d: %s is set twice" % (name, n, key))
        conf[key] = value
    missing = [k for k in _REQUIRED if not conf.get(k)]
    if missing:
        raise LabError("%s: missing %s" % (name, ", ".join(missing)))
    for k in conf:
        if k in _INTS or k.startswith(("BUDGET_", "RETRIES_")):
            conf.int(k)
    if not re.match(r"^[0-9a-f]{64}$", conf["IMAGE_SHA256"]):
        raise LabError("%s: IMAGE_SHA256 is not a sha256" % name)
    return conf


def load_conf(path=CONF_PATH):
    try:
        with open(path, encoding="utf-8") as f:
            text = f.read()
    except OSError as e:
        raise LabError("cannot read %s: %s" % (path, e)) from None
    return parse_conf(text, os.path.basename(path))


# ---------------------------------------------------------------- the cache


def cache_dir(env=None):
    """${SC_LAB_CACHE:-${XDG_CACHE_HOME:-~/.cache}/smartconfig-lab}, absolute."""
    env = os.environ if env is None else env
    d = env.get("SC_LAB_CACHE")
    if not d:
        base = env.get("XDG_CACHE_HOME") or os.path.join(os.path.expanduser("~"), ".cache")
        d = os.path.join(base, "smartconfig-lab")
    return os.path.abspath(d)


# The cache is the lab's own directory: ensure_cache chmods it and gc
# deletes in it. MARKER says a directory is one; a directory from before
# the marker is taken over only if every name in it is one the lab makes.
MARKER = ".smartconfig-lab"
_CACHE_NAME = re.compile(r"^(?:lock|key|key\.pub|key\.part|images|runs|up\.json|"
                         r"ref-[0-9a-f]{12}\.(?:qcow2|VARS\.fd|json)(?:\.part)?)$")
_RUN_NAME = re.compile(r"^\d{8}T\d{6}Z-[\w.-]+$")
_IMAGE_NAME = re.compile(r"^[\w.-]+\.img(?:\.part)?$")


def ensure_cache(cache=None):
    """Create the cache (mode 0700) with images/ and runs/; return its
    path. A directory that is there already must be a lab cache (MARKER,
    or nothing but the lab's names in it): SC_LAB_CACHE=~ or =. by
    mistake is refused before anything in it is touched."""
    cache = cache or cache_dir()
    marker = os.path.join(cache, MARKER)
    if os.path.isdir(cache) and not os.path.exists(marker):
        strange = sorted(f for f in os.listdir(cache) if not _CACHE_NAME.match(f))
        if strange:
            raise LabError("%s is not a lab cache: it has no %s and holds %s%s. The lab uses, and cleans, only a "
                           "directory of its own" % (cache, MARKER, ", ".join(strange[:3]),
                                                     " and %d more" % (len(strange) - 3) if len(strange) > 3 else ""))
    os.makedirs(cache, mode=0o700, exist_ok=True)
    os.chmod(cache, 0o700)
    for sub in ("images", "runs"):
        os.makedirs(os.path.join(cache, sub), mode=0o700, exist_ok=True)
    if not os.path.exists(marker):
        with open(marker, "w") as f:
            f.write("SmartConfig's QEMU lab cache (lab/README.md). make lab-clean deletes in here.\n")
    return cache


def _fsync_dir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def _unlink(path):
    with contextlib.suppress(FileNotFoundError):
        os.unlink(path)


def _write_file(path, data, mode=0o644):
    """Write data to path through a temp file and a rename."""
    tmp = path + ".part"
    _unlink(tmp)
    with open(tmp, "wb") as f:
        f.write(data if isinstance(data, bytes) else data.encode())
        f.flush()
        os.fsync(f.fileno())
    os.chmod(tmp, mode)
    os.replace(tmp, path)


def _need_tool(name):
    if not shutil.which(name):
        raise LabError("%s is not installed (the lab needs qemu-system-x86, qemu-utils, ovmf, "
                       "openssh-client and curl)" % name)


def _tail(path, size=2000):
    try:
        with open(path, "rb") as f:
            f.seek(0, os.SEEK_END)
            f.seek(max(0, f.tell() - size))
            return f.read().decode(errors="replace")
    except OSError:
        return ""


def _utc():
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def git_head(repo=REPO):
    """(HEAD sha, its first 7 characters, dirty?) of the repo; dirty
    counts untracked files too. Without git: ("unknown", "nogit", True)."""
    try:
        sha = subprocess.run(["git", "-C", repo, "rev-parse", "HEAD"], capture_output=True,
                             timeout=30, check=True).stdout.decode().strip()
        st = subprocess.run(["git", "-C", repo, "status", "--porcelain"], capture_output=True,
                            timeout=30, check=True).stdout
        return sha, sha[:7], bool(st.strip())
    except (OSError, subprocess.SubprocessError):
        return "unknown", "nogit", True


def new_run_dir(cache, mode, tag):
    """Create runs/<UTC>-<mode>-<tag>/ with an evidence/ directory."""
    runs = os.path.join(cache, "runs")
    os.makedirs(runs, mode=0o700, exist_ok=True)
    base = "%s-%s-%s" % (time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()), mode, tag)
    for n in range(1, 100):
        path = os.path.join(runs, base if n == 1 else "%s.%d" % (base, n))
        try:
            os.mkdir(path, 0o700)
        except FileExistsError:
            continue
        os.mkdir(os.path.join(path, "evidence"), 0o700)
        return path
    raise LabError("cannot make a run directory %s in %s" % (base, runs))


# ---------------------------------------------------------------- the image


def image_url(conf):
    return "%s/%s/%s" % (conf["IMAGE_BASE_URL"].rstrip("/"), conf["IMAGE_RELEASE"], conf["IMAGE_NAME"])


def image_path(conf, cache):
    return os.path.join(cache, "images", "%s-%s" % (conf["IMAGE_RELEASE"], conf["IMAGE_NAME"]))


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 22), b""):
            h.update(chunk)
    return h.hexdigest()


def _copy_hashing(src, dst):
    h = hashlib.sha256()
    with open(src, "rb") as fi, open(dst, "wb") as fo:
        for chunk in iter(lambda: fi.read(1 << 22), b""):
            h.update(chunk)
            fo.write(chunk)
        fo.flush()
        os.fsync(fo.fileno())
    return h.hexdigest()


def fetch_image(conf, cache, src=None, log=log):
    """Put the pinned image at images/<release>-<name> (0444): download it
    with curl, or copy src. Either goes to a .part file first and is renamed
    only when its sha256 is the pin. An image already there is checked."""
    dest = image_path(conf, cache)
    pin = conf["IMAGE_SHA256"]
    if os.path.exists(dest):
        got = sha256_file(dest)
        if got == pin:
            os.chmod(dest, 0o444)
            log("image: %s (sha256 is the pin)" % dest)
            return dest
        log("image: %s has sha256 %s, not the pin: replacing it" % (dest, got))
        os.unlink(dest)
    part = dest + ".part"
    _unlink(part)
    try:
        if src:
            src = os.path.abspath(os.path.expanduser(src))
            log("image: copying %s" % src)
            try:
                got = _copy_hashing(src, part)
            except OSError as e:
                raise LabError("cannot copy %s: %s" % (src, e)) from None
            what = src
        else:
            _need_tool("curl")
            what = image_url(conf)
            log("image: downloading %s" % what)
            # Under 1 kB/s for two minutes is a download that stopped.
            r = subprocess.run(["curl", "-fL", "--retry", "3", "--connect-timeout", "30",
                                "--speed-limit", "1024", "--speed-time", "120", "-o", part, what])
            if r.returncode != 0:
                raise LabError("curl failed (exit %d) for %s" % (r.returncode, what))
            got = sha256_file(part)
        if got != pin:
            raise LabError("%s has sha256 %s; lab.conf pins %s" % (what, got, pin))
        os.chmod(part, 0o444)
        os.replace(part, dest)
        _fsync_dir(os.path.dirname(dest))
    except BaseException:
        _unlink(part)
        raise
    log("image: %s (sha256 is the pin)" % dest)
    return dest


def check_image(conf, cache):
    """The cached image's path, after checking its sha256 against the pin."""
    path = image_path(conf, cache)
    if not os.path.exists(path):
        raise LabError("no image %s: run make lab-image (or vm.py image --from FILE)" % path)
    got = sha256_file(path)
    if got != conf["IMAGE_SHA256"]:
        raise LabError("%s has sha256 %s; lab.conf pins %s" % (path, got, conf["IMAGE_SHA256"]))
    return path


# ---------------------------------------------------------------- the key


def key_paths(cache):
    """(private key, public key) paths in the cache."""
    return os.path.join(cache, "key"), os.path.join(cache, "key.pub")


def ensure_key(cache, log=log):
    """The lab's ed25519 key pair, generated once (no passphrase, 0600)."""
    key, pub = key_paths(cache)
    if os.path.exists(key) and os.path.exists(pub):
        return key, pub
    _need_tool("ssh-keygen")
    tmp = key + ".part"
    for p in (key, pub, tmp, tmp + ".pub"):
        _unlink(p)
    r = subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "sclab-lab", "-f", tmp],
                       stdin=subprocess.DEVNULL, capture_output=True, timeout=60)
    if r.returncode != 0:
        raise LabError("ssh-keygen failed: %s" % r.stderr.decode(errors="replace").strip())
    os.chmod(tmp, 0o600)
    os.replace(tmp + ".pub", pub)
    os.replace(tmp, key)
    log("key: new lab key %s" % key)
    return key, pub


# ---------------------------------------------------------------- the reference image


def _read(path):
    try:
        with open(path, "rb") as f:
            return f.read()
    except OSError as e:
        raise LabError("cannot read %s: %s" % (path, e)) from None


def ref_hash(image_sha256, user_data_in, meta_data_in, sclab_cfg, key_pub, provision_rev):
    """The reference image's hash (hex): the image's sha256, both cloud-init
    templates, 60-sclab.cfg, the public key and PROVISION_REV. Pure: the
    same inputs always give the same hash."""
    h = hashlib.sha256()
    parts = (
        ("image-sha256", image_sha256),
        ("user-data.in", user_data_in),
        ("meta-data.in", meta_data_in),
        ("60-sclab.cfg", sclab_cfg),
        ("key.pub", key_pub),
        ("PROVISION_REV", provision_rev),
    )
    for label, data in parts:
        if isinstance(data, str):
            data = data.encode()
        h.update(b"%s %d\n" % (label.encode(), len(data)))
        h.update(data)
        h.update(b"\n")
    return h.hexdigest()


def ref_info(conf, cache, lab=LAB):
    """The reference image this checkout wants: {h, h12, qcow2, vars, json,
    ready}. ready means all three files exist."""
    _, pub = key_paths(cache)
    if not os.path.exists(pub):
        raise LabError("no lab key in %s: run make lab-image" % cache)
    h = ref_hash(conf["IMAGE_SHA256"],
                 _read(os.path.join(lab, "cloud-init", "user-data.in")),
                 _read(os.path.join(lab, "cloud-init", "meta-data.in")),
                 _read(os.path.join(lab, "guest", "60-sclab.cfg")),
                 _read(pub), conf["PROVISION_REV"])
    h12 = h[:12]
    base = os.path.join(cache, "ref-" + h12)
    info = {"h": h, "h12": h12, "qcow2": base + ".qcow2", "vars": base + ".VARS.fd",
            "json": base + ".json"}
    info["ready"] = all(os.path.exists(info[k]) for k in ("qcow2", "vars", "json"))
    return info


def find_ref(conf, cache, lab=LAB):
    """ref_info() of a reference image that is ready, or LabError."""
    ref = ref_info(conf, cache, lab)
    if not ref["ready"]:
        raise LabError("no reference image %s for this checkout: run make lab-image" % ref["qcow2"])
    return ref


_TOKEN = re.compile(r"@[A-Z][A-Z0-9_]*@")


def fill_template(text, values, blocks=()):
    """Replace @KEY@ tokens. A key in blocks must stand alone on its line,
    and each line of its value gets that line's indentation. A token left
    over is an error."""
    for key, value in values.items():
        tok = "@%s@" % key
        if tok not in text:
            continue
        if key in blocks:
            pat = re.compile(r"(?m)^([ \t]*)%s[ \t]*$" % re.escape(tok))
            text = pat.sub(lambda m: "\n".join(m.group(1) + ln if ln else ""
                                               for ln in value.rstrip("\n").split("\n")), text)
        else:
            if "\n" in value:
                raise LabError("template value for %s has a newline" % key)
            text = text.replace(tok, value)
    left = _TOKEN.findall(text)
    if left:
        raise LabError("template tokens left unfilled: %s" % ", ".join(sorted(set(left))))
    return text


def render_seed(h12, pub, lab=LAB):
    """(user-data, meta-data) for the provisioning boot, from the templates
    in lab/cloud-init, lab/guest/60-sclab.cfg and the public key text."""
    pub = pub.decode() if isinstance(pub, bytes) else pub
    pub = pub.strip()
    if "\n" in pub or not re.match(r"^ssh-ed25519 [A-Za-z0-9+/=]+( \S+)?$", pub):
        raise LabError("the lab key is not one ed25519 public key line")
    if not re.match(r"^[0-9a-f]{12}$", h12):
        raise LabError("bad reference hash %r" % h12)
    cfg = _read(os.path.join(lab, "guest", "60-sclab.cfg")).decode()
    values = {"SSH_KEY": pub, "H12": h12, "SCLAB_CFG": cfg}
    ud = fill_template(_read(os.path.join(lab, "cloud-init", "user-data.in")).decode(), values,
                       blocks=("SCLAB_CFG",))
    md = fill_template(_read(os.path.join(lab, "cloud-init", "meta-data.in")).decode(), values)
    return ud, md


class SeedServer:
    """The NoCloud seed over http on 127.0.0.1:<free port>, in a thread. The
    guest reaches it as http://10.0.2.2:<port>/ (QEMU user networking)."""

    def __init__(self, files, log_path=None):
        self.files = {"/" + k: (v.encode() if isinstance(v, str) else v) for k, v in files.items()}
        self.hits = []
        self._log = open(log_path, "a", buffering=1) if log_path else None
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def _reply(self, body_too):
                path = self.path.split("?", 1)[0]
                body = outer.files.get(path)
                code = 200 if body is not None else 404
                outer.hits.append((time.time(), path, code))
                self.send_response(code)
                self.send_header("Content-Type", "text/plain; charset=utf-8")
                self.send_header("Content-Length", str(len(body or b"")))
                self.end_headers()
                if body_too and body:
                    self.wfile.write(body)

            def do_GET(self):
                self._reply(True)

            def do_HEAD(self):
                self._reply(False)

            def log_message(self, fmt, *args):
                if outer._log:
                    outer._log.write("%s %s\n" % (_utc(), fmt % args))

        self.httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.httpd.server_address[1]
        self.guest_url = "http://10.0.2.2:%d/" % self.port
        self._thread = threading.Thread(target=self.httpd.serve_forever, name="seed", daemon=True)

    def start(self):
        self._thread.start()
        return self

    def fetched(self, name):
        return any(p == "/" + name and c == 200 for _, p, c in self.hits)

    def stop(self):
        if self._thread.is_alive():
            self.httpd.shutdown()
        self.httpd.server_close()
        if self._log:
            self._log.close()
            self._log = None


def provision(conf, cache=None, force=False, log=log):
    """Build the reference image ref-<h12>.{qcow2,VARS.fd,json} (0444) if
    this checkout's hash has none yet (or force). One UEFI boot of an overlay
    on the pinned image, seeded by cloud-init from SeedServer, must print
    SCLAB-PROVISIONED and power off within BUDGET_PROVISION. A kernel panic
    throws the disk away and boots a new one, at most PROVISION_RETRIES
    times. The files appear by rename only after qemu-img check passes.
    Returns ref_info(). The logs stay in runs/<UTC>-provision-<h12>/."""
    cache = ensure_cache(cache)
    for tool in (conf["QEMU"], "qemu-img", "ssh-keygen"):
        _need_tool(tool)
    for k in ("OVMF_CODE", "OVMF_VARS"):
        if not os.path.exists(conf[k]):
            raise LabError("no %s (%s): install ovmf" % (k, conf[k]))
    image = check_image(conf, cache)
    ensure_key(cache, log)
    ref = ref_info(conf, cache)
    if ref["ready"] and not force:
        log("provision: %s is up to date" % ref["qcow2"])
        return ref
    with lab_lock(cache):
        ref = ref_info(conf, cache)
        if ref["ready"] and not force:
            log("provision: %s is up to date" % ref["qcow2"])
            return ref
        for k in ("qcow2", "vars", "json"):
            _unlink(ref[k])
        h12 = ref["h12"]
        run = new_run_dir(cache, "provision", h12)
        _, pub = key_paths(cache)
        ud, md = render_seed(h12, _read(pub))
        _write_file(os.path.join(run, "user-data"), ud)
        _write_file(os.path.join(run, "meta-data"), md)
        part = ref["qcow2"] + ".part"
        vars_part = ref["vars"] + ".part"
        json_part = ref["json"] + ".part"
        seed = SeedServer({"user-data": ud, "meta-data": md, "vendor-data": ""},
                          os.path.join(run, "seed.log")).start()
        log("provision: ref-%s, logs in %s" % (h12, run))
        started = time.monotonic()
        done = False
        try:
            tries = conf.int("PROVISION_RETRIES") + 1
            for attempt in range(1, tries + 1):
                facts = _provision_attempt(conf, image, part, run, h12, seed, attempt, log)
                if facts["outcome"] == "ok":
                    break
                log("provision: attempt %d: %s; a new disk" % (attempt, facts["why"]))
            else:
                raise LabError("provision: the guest panicked %d times; logs in %s" % (tries, run))
            r = subprocess.run(["qemu-img", "check", part], capture_output=True, timeout=600)
            out = (r.stdout + r.stderr).decode(errors="replace").strip()
            _write_file(os.path.join(run, "qemu-img-check.txt"), out + "\n")
            if r.returncode not in (0, 3):  # 3: leaked clusters only, no corruption
                raise LabError("qemu-img check %s failed (exit %d): %s" % (part, r.returncode, out))
            shutil.copyfile(os.path.join(run, "VARS.fd"), vars_part)
            meta = {
                "h": ref["h"], "h12": h12, "created": _utc(),
                "image": os.path.basename(image), "image_sha256": conf["IMAGE_SHA256"],
                "provision_rev": conf["PROVISION_REV"], "kernel": facts["kernel"],
                "grub_no_timer_check": facts["grub_no_timer_check"],
                "attempts": attempt, "seconds": int(time.monotonic() - started),
                "ovmf_code": conf["OVMF_CODE"], "ovmf_code_sha256": sha256_file(conf["OVMF_CODE"]),
                "seed_fetched": sorted({p for _, p, c in seed.hits if c == 200}),
                "run": run,
            }
            with open(json_part, "w") as f:
                json.dump(meta, f, indent=1, sort_keys=True)
                f.write("\n")
            for p in (part, vars_part, json_part):
                os.chmod(p, 0o444)
            # The qcow2 last: all three present means ready.
            os.replace(vars_part, ref["vars"])
            os.replace(json_part, ref["json"])
            os.replace(part, ref["qcow2"])
            _fsync_dir(cache)
            _unlink(os.path.join(run, "VARS.fd"))
            done = True
        finally:
            seed.stop()
            if not done:
                for p in (part, vars_part, json_part):
                    _unlink(p)
                log("provision: failed; logs in %s" % run)
    log("provision: %s ready (%d s, %d boot%s)" % (ref["qcow2"], meta["seconds"], attempt,
                                                    "" if attempt == 1 else "s"))
    return ref_info(conf, cache)


def _provision_attempt(conf, image, part, run, h12, seed, attempt, log):
    """One provisioning boot on a new disk. {"outcome": "ok", ...facts} or
    {"outcome": "panic", "why": ...}; anything else raises LabError."""
    _unlink(part)
    r = subprocess.run(["qemu-img", "create", "-q", "-f", "qcow2",
                        "-b", os.path.relpath(image, os.path.dirname(part)), "-F", "qcow2",
                        part, conf["DISK_SIZE"]], capture_output=True, timeout=120)
    if r.returncode != 0:
        raise LabError("qemu-img create %s: %s" % (part, r.stderr.decode(errors="replace").strip()))
    vars_path = os.path.join(run, "VARS.fd")
    shutil.copyfile(conf["OVMF_VARS"], vars_path)
    os.chmod(vars_path, 0o600)
    suffix = "" if attempt == 1 else "-%d" % attempt
    vm = Vm(conf, "uefi", run, part, vars_path, name="sclab-provision-" + h12,
            seed_url=seed.guest_url)
    tap = None
    raw = os.path.join(run, "serial%s.raw" % suffix)
    try:
        vm.start()
        tap = SerialTap(vm.serial_sock, raw, t0=vm.t0).start(alive=vm.alive)
        mark = vm.qmp.mark()
        vm.cont()
        log("provision: boot %d started (UEFI under TCG, often 10-30 min)" % attempt)
        return _await_provisioned(conf, vm, tap, mark, h12, log)
    finally:
        vm.stop()
        if tap:
            tap.close()
            _write_file(os.path.join(run, "serial%s.txt" % suffix), plain_text(tap.data()))


def _await_provisioned(conf, vm, tap, mark, h12, log):
    budget = conf.int("BUDGET_PROVISION")
    deadline = time.monotonic() + budget
    done_re = re.compile(rb"SCLAB-PROVISIONED sclab-" + h12.encode() + rb" after [0-9]")
    kver_re = re.compile(rb"Linux version (\S+)")
    ntc_re = re.compile(rb"sclab: grub\.cfg no_timer_check lines: (\d+)")
    facts = {"outcome": None, "kernel": None, "grub_no_timer_check": None}
    done = False
    while True:
        data = tap.data()
        if facts["kernel"] is None:
            m = kver_re.search(data)
            if m:
                facts["kernel"] = m.group(1).decode(errors="replace")
                if facts["kernel"] != conf["KERNEL"]:
                    raise LabError("provision: the image boots kernel %s; lab.conf pins KERNEL=%s"
                                   % (facts["kernel"], conf["KERNEL"]))
        m = PANIC_RE.search(data)
        if m:
            return {"outcome": "panic", "why": m.group(0).decode(errors="replace")}
        m = ntc_re.search(data)
        if m:
            facts["grub_no_timer_check"] = int(m.group(1))
        if not done and done_re.search(data):
            done = True
            log("provision: cloud-init finished; waiting for the power-off")
        ev = vm.qmp.wait_event("SHUTDOWN", since=mark, timeout=0.5)
        if ev:
            vm.wait_exit(60)
            data = tap.data()
            m = ntc_re.search(data)
            if m:
                facts["grub_no_timer_check"] = int(m.group(1))
            if not done_re.search(data):
                raise LabError("provision: the guest powered off (%s) before SCLAB-PROVISIONED"
                               % json.dumps(ev["data"]))
            if facts["grub_no_timer_check"] == 0:
                raise LabError("provision: update-grub left no_timer_check out of grub.cfg")
            if facts["grub_no_timer_check"] is None:
                log("provision: warning: no grub.cfg line count on the console")
            facts["outcome"] = "ok"
            return facts
        if not vm.alive():
            raise LabError("provision: QEMU exited (status %s) without a SHUTDOWN event: %s"
                           % (vm.proc.returncode, _tail(vm.qemu_log, 600)))
        if time.monotonic() > deadline:
            raise LabError("provision: no SCLAB-PROVISIONED and power-off within %d s" % budget)


# ---------------------------------------------------------------- overlays


def make_overlay(ref, run_dir, mode):
    """runs/<run>/disk.qcow2 on ../../ref-<h12>.qcow2, plus a writable copy
    of the reference VARS for UEFI. Returns (disk, vars or None)."""
    if mode not in MODES:
        raise LabError("mode must be uefi or bios, not %r" % mode)
    disk = os.path.join(run_dir, "disk.qcow2")
    backing = os.path.relpath(ref["qcow2"], run_dir)
    r = subprocess.run(["qemu-img", "create", "-q", "-f", "qcow2", "-b", backing, "-F", "qcow2", disk],
                       capture_output=True, timeout=120)
    if r.returncode != 0:
        raise LabError("qemu-img create %s: %s" % (disk, r.stderr.decode(errors="replace").strip()))
    vars_path = None
    if mode == "uefi":
        vars_path = os.path.join(run_dir, "VARS.fd")
        shutil.copyfile(ref["vars"], vars_path)
        os.chmod(vars_path, 0o600)
    return disk, vars_path


def image_info(path):
    """qemu-img info of path as a dict (-U: QEMU may hold it open)."""
    r = subprocess.run(["qemu-img", "info", "-U", "--output=json", path], capture_output=True, timeout=60)
    if r.returncode != 0:
        raise LabError("qemu-img info %s: %s" % (path, r.stderr.decode(errors="replace").strip()))
    return json.loads(r.stdout)


def discard_disks(run_dir):
    """Remove a run's disk.qcow2 and VARS.fd (after a PASS)."""
    for name in ("disk.qcow2", "VARS.fd"):
        _unlink(os.path.join(run_dir, name))


# ---------------------------------------------------------------- the lock


def running_qemu(uid=None):
    """[(pid, command line)] of this uid's qemu-system-x86* processes."""
    uid = os.getuid() if uid is None else uid
    found = []
    for name in os.listdir("/proc"):
        if not name.isdigit():
            continue
        try:
            with open("/proc/%s/status" % name) as f:
                status = f.read()
        except OSError:
            continue
        comm = re.search(r"^Name:\s*(.*)$", status, re.M)
        ruid = re.search(r"^Uid:\s*(\d+)", status, re.M)
        if not comm or not ruid or int(ruid.group(1)) != uid:
            continue
        if not comm.group(1).startswith("qemu-system-x86"):
            continue
        try:
            with open("/proc/%s/cmdline" % name, "rb") as f:
                cmd = f.read().replace(b"\0", b" ").decode(errors="replace").strip()
        except OSError:
            cmd = ""
        found.append((int(name), cmd))
    return found


def user_lock_path(env=None):
    """A lock that does not depend on the cache: two lab commands with
    two SC_LAB_CACHEs are still one user's. In XDG_RUNTIME_DIR (a tmpfs of
    this user's, gone at logout); None where there is none."""
    d = (os.environ if env is None else env).get("XDG_RUNTIME_DIR")
    return os.path.join(d, "smartconfig-lab.lock") if d and os.path.isdir(d) else None


def _flock(path):
    try:
        fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_CLOEXEC | os.O_NOFOLLOW, 0o600)
    except OSError as e:
        raise LabError("the lab's lock %s: %s" % (path, e.strerror)) from None
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        holder = os.pread(fd, 32, 0).decode(errors="replace").strip()
        os.close(fd)
        raise LabError("another lab command holds %s (pid %s)" % (path, holder or "?")) from None
    os.ftruncate(fd, 0)
    os.pwrite(fd, b"%d\n" % os.getpid(), 0)
    return fd


@contextlib.contextmanager
def lab_lock(cache, guard=True):
    """Hold flock on <cache>/lock, and on this user's lock outside the
    cache (user_lock_path), for the block: one lab command at a time.
    With guard, refuse also while any qemu-system-x86 of this uid runs
    (one VM on this host at a time, ours or not). LabError if any fails."""
    path = os.path.join(cache, "lock")
    fds = [_flock(path)]
    try:
        user = user_lock_path()
        if user:
            fds.append(_flock(user))
        if guard:
            q = running_qemu()
            if q:
                raise LabError("one VM at a time, and a qemu-system-x86 of this user runs: %s"
                               % "; ".join("pid %d %s" % (p, c[:100]) for p, c in q))
        yield path
    finally:
        for fd in fds:
            os.close(fd)


def install_signal_handlers():
    """The first SIGINT, SIGTERM or SIGHUP raises KeyboardInterrupt or
    SystemExit, so try/finally teardowns run; from then on all three are
    ignored, so nothing cuts the teardown. make lab-e2e delivers every
    one twice (timeout --foreground passes on what the terminal already
    sent). A signal inherited as ignored (nohup) stays ignored. Call it
    from the main thread."""
    sigs = [s for s in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP) if signal.getsignal(s) != signal.SIG_IGN]

    def handler(signum, frame):
        for s in sigs:
            signal.signal(s, signal.SIG_IGN)
        if signum == signal.SIGINT:
            raise KeyboardInterrupt
        raise SystemExit(128 + signum)
    for s in sigs:
        signal.signal(s, handler)


# ---------------------------------------------------------------- ports, sockets


def free_port():
    """A TCP port on 127.0.0.1 that nothing listens on right now."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def port_open(port, timeout=1.0):
    """True when something accepts connections on 127.0.0.1:port."""
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=timeout):
            return True
    except OSError:
        return False


_DIRFDS = {}


def sock_addr(path):
    """An address for path that fits AF_UNIX's 108 bytes: path itself, or
    /proc/self/fd/<dir fd>/<name> (valid in this process only)."""
    if len(os.fsencode(path)) < 100:
        return path
    d, base = os.path.split(path)
    fd = _DIRFDS.get(d)
    if fd is None:
        fd = os.open(d, os.O_PATH | os.O_DIRECTORY | os.O_CLOEXEC)
        _DIRFDS[d] = fd
    return "/proc/self/fd/%d/%s" % (fd, base)


def _connect_unix(path, timeout, alive=None, what="socket"):
    deadline = time.monotonic() + timeout
    while True:
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            s.connect(sock_addr(path))
            return s
        except (FileNotFoundError, ConnectionRefusedError):
            s.close()
            if alive is not None and not alive():
                raise LabError("QEMU exited before its %s answered" % what) from None
            if time.monotonic() > deadline:
                raise LabError("no %s at %s after %s s" % (what, path, timeout)) from None
            time.sleep(0.05)


# ---------------------------------------------------------------- QMP


class Qmp:
    """A QMP client: commands from any thread, events from a reader thread.

    events is the list of every event so far, each a dict {seq, event, data,
    timestamp (QEMU's), t (time.time() when read)}. Each function in
    listeners is called with the event in the reader thread before the event
    is in events, so whatever a listener records (say, the serial position
    at a RESET) is there by the time wait_event() returns it. qmp.log gets
    one line per message: "+SECONDS > sent" or "+SECONDS < received"."""

    def __init__(self, path, log_path=None, t0=None):
        self.path = path
        self.t0 = time.time() if t0 is None else t0
        self.events = []
        self.listeners = []
        self.greeting = None
        self.closed = False
        self._cv = threading.Condition()
        self._replies = {}
        self._next = 0
        self._send_lock = threading.Lock()
        self._log_lock = threading.Lock()
        self._log = open(log_path, "a", buffering=1) if log_path else None
        self._sock = None
        self._rfile = None
        self._thread = None

    def _logline(self, direction, line):
        if self._log is None:
            return
        if isinstance(line, bytes):
            line = line.decode(errors="replace")
        with self._log_lock:
            if self._log is not None:
                self._log.write("+%.3f %s %s\n" % (time.time() - self.t0, direction, line.rstrip()))

    def connect(self, timeout=10.0, alive=None):
        """Connect, read the greeting, start the reader, negotiate."""
        self._logline("=", "connect %s %s" % (_utc(), self.path))
        s = _connect_unix(self.path, timeout, alive, "QMP socket")
        s.settimeout(timeout)
        self._sock = s
        self._rfile = s.makefile("rb")
        try:
            line = self._rfile.readline()
        except OSError as e:
            raise QmpError("no QMP greeting: %s" % e) from None
        if not line:
            raise QmpError("QMP closed before its greeting")
        self._logline("<", line)
        self.greeting = json.loads(line)
        s.settimeout(None)
        self._thread = threading.Thread(target=self._reader, name="qmp", daemon=True)
        self._thread.start()
        self.cmd("qmp_capabilities", timeout=timeout)
        return self

    def _reader(self):
        try:
            for line in self._rfile:
                self._logline("<", line)
                try:
                    msg = json.loads(line)
                except ValueError:
                    continue
                if "event" in msg:
                    ev = {"seq": None, "event": msg["event"], "data": msg.get("data", {}),
                          "timestamp": msg.get("timestamp"), "t": time.time()}
                    with self._cv:
                        ev["seq"] = len(self.events)
                    for fn in list(self.listeners):
                        try:
                            fn(ev)
                        except Exception as e:  # a listener's bug must not stop the reader
                            self._logline("!", "listener %r failed: %r" % (fn, e))
                    with self._cv:
                        self.events.append(ev)
                        self._cv.notify_all()
                elif "id" in msg:
                    with self._cv:
                        self._replies[msg["id"]] = msg
                        self._cv.notify_all()
        except (OSError, ValueError):
            pass
        finally:
            with self._cv:
                self.closed = True
                self._cv.notify_all()

    def cmd(self, name, args=None, timeout=10.0):
        """Run a QMP command; its "return" value, or QmpError."""
        with self._cv:
            if self.closed or self._sock is None:
                raise QmpError("QMP is closed: cannot run %s" % name)
            self._next += 1
            cid = "sc%d" % self._next
        msg = {"execute": name, "id": cid}
        if args:
            msg["arguments"] = args
        data = (json.dumps(msg) + "\n").encode()
        with self._send_lock:
            self._logline(">", data)
            try:
                self._sock.sendall(data)
            except OSError as e:
                raise QmpError("QMP %s: %s" % (name, e)) from None
        deadline = time.monotonic() + timeout
        with self._cv:
            while cid not in self._replies:
                if self.closed:
                    raise QmpError("QMP closed before the reply to %s" % name)
                left = deadline - time.monotonic()
                if left <= 0:
                    raise QmpError("no QMP reply to %s in %s s" % (name, timeout))
                self._cv.wait(left)
            reply = self._replies.pop(cid)
        if "error" in reply:
            err = reply["error"]
            raise QmpError("QMP %s: %s: %s" % (name, err.get("class"), err.get("desc")))
        return reply.get("return")

    def hmp(self, line, timeout=10.0):
        """An HMP command through QMP; its text output."""
        return self.cmd("human-monitor-command", {"command-line": line}, timeout)

    def mark(self):
        """The number of events so far: pass it as since= to wait_event."""
        with self._cv:
            return len(self.events)

    def events_since(self, since, names=None):
        with self._cv:
            evs = self.events[since:]
        if names is not None:
            names = (names,) if isinstance(names, str) else tuple(names)
            evs = [e for e in evs if e["event"] in names]
        return evs

    def _find(self, names, since):
        for ev in self.events[since:]:
            if ev["event"] in names:
                return ev
        return None

    def wait_event(self, names, since=0, timeout=None, abort=None, settle=5.0):
        """The first event named in names with seq >= since, waiting up to
        timeout seconds (None: no limit). None on timeout, abort() true, or
        a closed QMP without such an event.

        An event QEMU sent before it exited is not lost to abort: the reader
        runs the listeners before an event is in events (e2e's serial mark
        takes up to 1 s), and QEMU is gone the moment it sends SHUTDOWN. So
        when abort() says it is gone, the wait goes on until the reader has
        reached EOF, settle s at most, and looks at the events once more."""
        names = (names,) if isinstance(names, str) else tuple(names)
        deadline = None if timeout is None else time.monotonic() + timeout
        with self._cv:
            while True:
                ev = self._find(names, since)
                if ev is not None:
                    return ev
                if self.closed:
                    return None
                left = 1.0 if deadline is None else min(1.0, deadline - time.monotonic())
                if left <= 0:
                    return None
                self._cv.wait(left)
                if abort is not None and abort():
                    end = time.monotonic() + settle
                    while not self.closed and self._find(names, since) is None and time.monotonic() < end:
                        self._cv.wait(end - time.monotonic())
                    return self._find(names, since)

    def close(self):
        s = self._sock
        if s is not None:
            with contextlib.suppress(OSError):
                s.shutdown(socket.SHUT_RDWR)
            s.close()
        if self._thread is not None:
            self._thread.join(5)
        with contextlib.suppress(OSError, ValueError):
            if self._rfile is not None:
                self._rfile.close()
        with self._log_lock:
            if self._log is not None:
                self._log.close()
                self._log = None
        with self._cv:
            self.closed = True
            self._cv.notify_all()


# ---------------------------------------------------------------- QEMU


def _qemu_opt(value):
    return str(value).replace(",", ",,")


def qemu_args(conf, mode, ssh_port, name, disk, vars_path=None, seed_url=None):
    """The QEMU command line (design section 5): q35 under TCG, paused (-S),
    serial and QMP on unix sockets serial.sock and qmp.sock in QEMU's working
    directory, ssh forwarded from 127.0.0.1 only. uefi: OVMF CODE read-only
    plus vars_path, no display adapter. bios: SeaBIOS and a VGA adapter.
    seed_url adds the NoCloud seed for provisioning."""
    if mode not in MODES:
        raise LabError("mode must be uefi or bios, not %r" % mode)
    a = [conf["QEMU"], "-name", name, "-machine", "q35", "-accel", "tcg",
         "-smp", str(conf.int("SMP")), "-m", str(conf.int("MEM")), "-S"]
    if mode == "uefi":
        if not vars_path:
            raise LabError("uefi needs a VARS file")
        a += ["-drive", "if=pflash,format=raw,unit=0,readonly=on,file=" + _qemu_opt(conf["OVMF_CODE"]),
              "-drive", "if=pflash,format=raw,unit=1,file=" + _qemu_opt(vars_path)]
    a += ["-display", "none", "-vga", "none" if mode == "uefi" else "std"]
    a += ["-drive", "if=virtio,format=qcow2,file=" + _qemu_opt(disk),
          "-device", "virtio-rng-pci",
          "-nic", "user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:%d-:22" % int(ssh_port),
          "-chardev", "socket,id=ser0,path=serial.sock,server=on,wait=off",
          "-serial", "chardev:ser0",
          "-chardev", "socket,id=qmp0,path=qmp.sock,server=on,wait=off",
          "-mon", "chardev=qmp0,mode=control"]
    if seed_url:
        a += ["-smbios", "type=1,serial=ds=nocloud;s=" + _qemu_opt(seed_url)]
    return a


PR_SET_PDEATHSIG = 1
_SIGKILL = int(signal.SIGKILL)
try:
    _PRCTL = ctypes.CDLL(None, use_errno=True).prctl
except (OSError, AttributeError):
    _PRCTL = None


def _child_setup(nice, parent):
    def setup():
        if nice:
            os.nice(nice)
        if _PRCTL is not None:
            _PRCTL(PR_SET_PDEATHSIG, _SIGKILL, 0, 0, 0)
        if os.getppid() != parent:  # the parent died before prctl
            os._exit(1)
    return setup


class Vm:
    """One QEMU process, owned through Popen.

      vm = Vm(conf, mode, run_dir, disk, vars_path)
      vm.start()       # paused (-S), QMP connected: attach the serial reader
                       # to vm.serial_sock and QMP listeners now
      vm.cont()
      ...              # vm.qmp, vm.ssh_port, vm.system_reset(), vm.vga_dump()
      vm.stop()        # teardown; vm.leftovers() is then empty

    QEMU runs in run_dir (its sockets, qemu.log and qmp.log are there), under
    nice NICE, in its own session, and dies with SIGKILL when the thread that
    called start() exits (PR_SET_PDEATHSIG): call start() from the main
    thread."""

    def __init__(self, conf, mode, run_dir, disk, vars_path=None, name=None, ssh_port=None,
                 seed_url=None, log=log):
        if mode not in MODES:
            raise LabError("mode must be uefi or bios, not %r" % mode)
        self.conf = conf
        self.mode = mode
        self.run_dir = os.path.abspath(run_dir)
        self.disk = os.path.abspath(disk)
        self.vars_path = os.path.abspath(vars_path) if vars_path else None
        self.name = name or "sclab-" + os.path.basename(self.run_dir)
        self.ssh_port = ssh_port
        self.seed_url = seed_url
        self.serial_sock = os.path.join(self.run_dir, "serial.sock")
        self.qmp_sock = os.path.join(self.run_dir, "qmp.sock")
        self.qemu_log = os.path.join(self.run_dir, "qemu.log")
        self.qmp_log = os.path.join(self.run_dir, "qmp.log")
        self.args = None
        self.proc = None
        self.qmp = None
        self.t0 = None
        self._log = log

    def start(self, timeout=30.0):
        """Start QEMU paused and connect QMP. If the ssh forward cannot bind
        its port, once more with a new one. LabError when QEMU fails."""
        if self.proc is not None:
            raise LabError("this Vm was started already")
        _need_tool(self.conf["QEMU"])
        for attempt in (1, 2):
            if self.ssh_port is None or attempt == 2:
                self.ssh_port = free_port()
            for p in (self.serial_sock, self.qmp_sock):
                _unlink(p)
            self.args = qemu_args(self.conf, self.mode, self.ssh_port, self.name, self.disk,
                                  self.vars_path, self.seed_url)
            with open(self.qemu_log, "ab") as lf:
                lf.write(("== %s %s\n" % (_utc(), " ".join(shlex.quote(a) for a in self.args))).encode())
                lf.flush()
                self.t0 = time.time()
                _write_file(os.path.join(self.run_dir, "t0"), "%.6f\n" % self.t0)  # serialmux's clock
                self.proc = subprocess.Popen(
                    self.args, cwd=self.run_dir, stdin=subprocess.DEVNULL, stdout=lf,
                    stderr=subprocess.STDOUT, start_new_session=True, close_fds=True,
                    preexec_fn=_child_setup(self.conf.int("NICE"), os.getpid()))
            deadline = time.monotonic() + timeout
            while self.proc.poll() is None and time.monotonic() < deadline:
                if os.path.exists(self.serial_sock) and os.path.exists(self.qmp_sock):
                    break
                time.sleep(0.05)
            err = None
            if self.proc.poll() is None:
                if not (os.path.exists(self.serial_sock) and os.path.exists(self.qmp_sock)):
                    err = "QEMU made no serial and QMP sockets in %s s" % timeout
                else:
                    self.qmp = Qmp(self.qmp_sock, self.qmp_log, self.t0)
                    try:
                        self.qmp.connect(timeout, alive=self.alive)
                        return self
                    except LabError as e:
                        err = str(e)
                        self.qmp.close()
                        self.qmp = None
            if self.proc.poll() is None:
                self.proc.kill()
            rc = self.proc.wait(10)
            tail = _tail(self.qemu_log, 1200)
            self.proc = None
            if attempt == 1 and "host forwarding rule" in tail:
                self._log("vm: QEMU could not forward 127.0.0.1:%d; once more with a new port"
                          % self.ssh_port)
                continue
            raise LabError("QEMU did not start (%s, exit %s): %s" % (err or "it exited", rc, tail.strip()))
        raise LabError("unreachable")

    def cont(self):
        """Let the paused machine run."""
        self.qmp.cmd("cont")

    def alive(self):
        return self.proc is not None and self.proc.poll() is None

    def cpu_seconds(self):
        """The CPU time QEMU has used so far, user and system, in seconds."""
        with open("/proc/%d/stat" % self.proc.pid) as f:
            fields = f.read().rsplit(")", 1)[1].split()  # from field 3, the state
        return (int(fields[11]) + int(fields[12])) / os.sysconf("SC_CLK_TCK")

    def wait_exit(self, timeout):
        """QEMU's exit status, or None if it still runs after timeout s."""
        try:
            return self.proc.wait(timeout)
        except subprocess.TimeoutExpired:
            return None

    def system_reset(self):
        """A hard reset (the guest sees a reset button; QMP sends RESET)."""
        self.qmp.cmd("system_reset")

    def pmemsave(self, addr, size, path):
        """Save guest physical memory [addr, addr+size) to path; the bytes."""
        path = os.path.abspath(path)
        _unlink(path)
        self.qmp.cmd("pmemsave", {"val": addr, "size": size, "filename": path})
        deadline = time.monotonic() + 2
        data = b""
        while True:
            with contextlib.suppress(FileNotFoundError):
                with open(path, "rb") as f:
                    data = f.read()
            if len(data) >= size:
                return data
            if time.monotonic() > deadline:
                if data:
                    return data
                raise LabError("pmemsave wrote no %s" % path)
            time.sleep(0.02)

    def vga_dump(self, path=None, size=4000):
        """The VGA text buffer (BIOS mode): 4000 bytes are the 80x25 screen
        while SeaBIOS or GRUB own it. Once Linux scrolls its console the
        visible screen moves inside the 32 KiB window: dump 32768 and search."""
        return self.pmemsave(VGA_TEXT, size, path or os.path.join(self.run_dir, "vga.bin"))

    def sendkey(self, key, hold_ms=100):
        """Press one key on the emulated keyboard, by QEMU qcode ("down",
        "ret", ...). A list presses its keys together (a chord)."""
        keys = [key] if isinstance(key, str) else list(key)
        self.qmp.cmd("send-key", {"keys": [{"type": "qcode", "data": k} for k in keys],
                                  "hold-time": hold_ms})

    def screendump(self, path):
        """The display as a PPM file (BIOS mode)."""
        self.qmp.cmd("screendump", {"filename": os.path.abspath(path)})

    def kill(self):
        """SIGKILL this Vm's own QEMU process (and nothing else)."""
        if self.alive():
            self.proc.kill()
            self.proc.wait(10)

    def stop(self, grace=10.0):
        """Teardown (design section 5): QMP quit, up to grace s for QEMU to
        exit, then SIGKILL its own pid only; close QMP and join its thread.
        Safe to call more than once. QEMU's exit status (None if never
        started)."""
        rc = None
        if self.proc is not None:
            if self.proc.poll() is None and self.qmp is not None and not self.qmp.closed:
                with contextlib.suppress(LabError):
                    self.qmp.cmd("quit", timeout=5)
            try:
                rc = self.proc.wait(grace)
            except subprocess.TimeoutExpired:
                self._log("vm: QEMU pid %d did not quit in %s s: SIGKILL" % (self.proc.pid, grace))
                self.proc.kill()
                rc = self.proc.wait(10)
        if self.qmp is not None:
            self.qmp.close()
        for p in (self.serial_sock, self.qmp_sock):
            _unlink(p)
        return rc

    def leftovers(self):
        """What of this Vm still runs or listens (design T.1): [] when clean."""
        left = []
        if self.alive():
            left.append("QEMU pid %d still runs" % self.proc.pid)
        if self.ssh_port and port_open(self.ssh_port):
            left.append("127.0.0.1:%d still accepts connections" % self.ssh_port)
        for pid, cmd in running_qemu():
            if (" -name %s " % self.name) in (" %s " % cmd):
                left.append("pid %d still runs: %s" % (pid, cmd[:120]))
        if self.qmp is not None and self.qmp._thread is not None and self.qmp._thread.is_alive():
            left.append("the QMP reader thread still runs")
        return left


# ---------------------------------------------------------------- serial (provision, up)


_ANSI = re.compile(rb"\x1b\[[0-9;?]*[ -/]*[@-~]"  # CSI
                   rb"|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)"  # OSC
                   rb"|\x1b[()][0-9A-Za-z]|\x1b[@-_]")  # charset, other ESC x
_CTRL = re.compile(rb"[\x00-\x08\x0b-\x1f\x7f]")


def plain_text(raw):
    """Serial bytes as readable text, for the provision and up logs: cursor
    moves become newlines, other escapes, CRs and controls go."""
    raw = re.sub(rb"\x1b\[[0-9;]*H", b"\n", raw)
    return _CTRL.sub(b"", _ANSI.sub(b"", raw)).decode(errors="replace")


class SerialTap:
    """A small serial client for provisioning and "vm.py up" (e2e uses
    serialmux.py). It logs every byte to raw_path, keeps them for wait(),
    and with console_path serves interactive consoles on that unix socket:
    each gets the last 4 KiB and then the live output, and what it types goes
    to the guest (and to input_log)."""

    def __init__(self, sock_path, raw_path, console_path=None, input_log=None, t0=None):
        self.sock_path = sock_path
        self.raw_path = raw_path
        self.console_path = console_path
        self.input_log = input_log
        self.t0 = time.time() if t0 is None else t0
        self.eof = False
        self._buf = bytearray()
        self._lock = threading.Lock()
        self._stop = False
        self._ser = None
        self._lst = None
        self._clients = []
        self._raw = None
        self._thread = None
        self._wake_r, self._wake_w = socket.socketpair()

    def start(self, timeout=10.0, alive=None):
        self._ser = _connect_unix(self.sock_path, timeout, alive, "serial socket")
        self._raw = open(self.raw_path, "ab", buffering=0)
        if self.console_path:
            _unlink(self.console_path)
            self._lst = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self._lst.bind(sock_addr(self.console_path))
            os.chmod(self.console_path, 0o600)
            self._lst.listen(4)
        self._thread = threading.Thread(target=self._run, name="serialtap", daemon=True)
        self._thread.start()
        return self

    def _drop(self, sel, c):
        with contextlib.suppress(KeyError, ValueError):
            sel.unregister(c)
        if c in self._clients:
            self._clients.remove(c)
        c.close()

    def _run(self):
        sel = selectors.DefaultSelector()
        sel.register(self._ser, selectors.EVENT_READ, "ser")
        sel.register(self._wake_r, selectors.EVENT_READ, "wake")
        if self._lst is not None:
            sel.register(self._lst, selectors.EVENT_READ, "lst")
        try:
            while not self._stop:
                for key, _ in sel.select(1.0):
                    what = key.data
                    if what == "wake":
                        return
                    if what == "ser":
                        try:
                            data = self._ser.recv(65536)
                        except OSError:
                            data = b""
                        if not data:
                            self.eof = True
                            return
                        self._raw.write(data)
                        with self._lock:
                            self._buf += data
                        for c in list(self._clients):
                            try:
                                c.sendall(data)
                            except OSError:
                                self._drop(sel, c)
                    elif what == "lst":
                        c, _ = self._lst.accept()
                        c.settimeout(2)
                        with self._lock:
                            recent = bytes(self._buf[-4096:])
                        try:
                            c.sendall(recent)
                        except OSError:
                            c.close()
                            continue
                        self._clients.append(c)
                        sel.register(c, selectors.EVENT_READ, "cli")
                    else:
                        c = key.fileobj
                        try:
                            data = c.recv(4096)
                        except OSError:
                            data = b""
                        if not data:
                            self._drop(sel, c)
                            continue
                        self._ser.sendall(data)
                        if self.input_log:
                            with open(self.input_log, "a") as f:
                                f.write("+%.2f %s\n" % (time.time() - self.t0, data.hex()))
        finally:
            for c in list(self._clients):
                self._drop(sel, c)
            if self._lst is not None:
                self._lst.close()
                _unlink(self.console_path)
            sel.close()

    def data(self):
        """Every byte read so far."""
        with self._lock:
            return bytes(self._buf)

    def wait(self, patterns, start=0, timeout=None, abort=None):
        """(index, match) of the earliest match of any pattern (bytes regex)
        in data()[start:], or (None, None) on timeout, abort() or EOF."""
        pats = [re.compile(p.encode() if isinstance(p, str) else p) if isinstance(p, (str, bytes)) else p
                for p in patterns]
        deadline = None if timeout is None else time.monotonic() + timeout
        while True:
            data = self.data()
            best = None
            for i, p in enumerate(pats):
                m = p.search(data, start)
                if m and (best is None or m.start() < best[1].start()):
                    best = (i, m)
            if best:
                return best
            if self.eof or (deadline is not None and time.monotonic() > deadline) or \
                    (abort is not None and abort()):
                return None, None
            time.sleep(0.25)

    def close(self):
        self._stop = True
        with contextlib.suppress(OSError):
            self._wake_w.send(b"x")
        if self._thread is not None:
            self._thread.join(5)
        if self._ser is not None:
            self._ser.close()
        if self._raw is not None:
            self._raw.close()
        self._wake_r.close()
        self._wake_w.close()


# ---------------------------------------------------------------- ssh, scp


class Result:
    """What an ssh or scp call did: rc (124 on timeout), out and err as
    text, timed_out, secs, argv."""

    def __init__(self, argv, rc, out, err, timed_out, secs):
        self.argv = argv
        self.rc = rc
        self.out = out
        self.err = err
        self.timed_out = timed_out
        self.secs = secs

    @property
    def ok(self):
        return self.rc == 0

    def __repr__(self):
        return "Result(rc=%r, timed_out=%r, secs=%.1f, out=%r, err=%r)" % (
            self.rc, self.timed_out, self.secs, self.out[-200:], self.err[-200:])


def _text(b):
    if b is None:
        return ""
    return b.decode(errors="replace") if isinstance(b, bytes) else b


def run_timed(argv, timeout, input=None, cwd=None):
    """Run argv with a hard timeout (the child is killed then), in cwd: a Result."""
    t = time.monotonic()
    kw = {"stdin": subprocess.DEVNULL} if input is None else \
        {"input": input.encode() if isinstance(input, str) else input}
    if cwd is not None:
        kw["cwd"] = cwd
    try:
        cp = subprocess.run(argv, capture_output=True, timeout=timeout, **kw)
        return Result(argv, cp.returncode, _text(cp.stdout), _text(cp.stderr), False,
                      time.monotonic() - t)
    except subprocess.TimeoutExpired as e:
        return Result(argv, 124, _text(e.stdout), _text(e.stderr) + "(timed out after %s s)\n" % timeout,
                      True, time.monotonic() - t)
    except OSError as e:
        raise LabError("cannot run %s: %s" % (argv[0], e)) from None


def ssh_argv(port, key, user=GUEST_USER):
    """ssh with the lab's options to user@127.0.0.1:port; append the command."""
    return ["ssh"] + list(SSH_OPTIONS) + ["-i", key, "-p", str(int(port)), "%s@127.0.0.1" % user]


def ssh(port, key, command, timeout, input=None, user=GUEST_USER):
    """Run command (one shell string) in the guest; a Result. timeout is
    required: no ssh call may hang."""
    return run_timed(ssh_argv(port, key, user) + [command], timeout, input)


def scp_to(port, key, sources, dest, timeout, user=GUEST_USER):
    """Copy local files to dest in the guest."""
    sources = [sources] if isinstance(sources, str) else list(sources)
    argv = ["scp", "-q"] + list(SSH_OPTIONS) + ["-i", key, "-P", str(int(port))] + sources + \
        ["%s@127.0.0.1:%s" % (user, dest)]
    return run_timed(argv, timeout)


def scp_from(port, key, source, dest, timeout, user=GUEST_USER):
    """Copy one guest file to local dest."""
    argv = ["scp", "-q"] + list(SSH_OPTIONS) + ["-i", key, "-P", str(int(port)),
                                                "%s@127.0.0.1:%s" % (user, source), dest]
    return run_timed(argv, timeout)


def wait_ssh(port, key, timeout, interval=5.0, abort=None, user=GUEST_USER):
    """Probe with "ssh true" until it works: that Result, or None after
    timeout s or when abort() is true."""
    deadline = time.monotonic() + timeout
    while True:
        r = ssh(port, key, "true", timeout=max(5.0, min(20.0, deadline - time.monotonic())), user=user)
        if r.ok:
            return r
        if time.monotonic() + interval > deadline or (abort is not None and abort()):
            return None
        time.sleep(interval)


# ---------------------------------------------------------------- gc


def gc(conf, cache=None, keep=None, dry_run=False, log=log):
    """Remove run directories but the newest keep (KEEP_RUNS), reference
    images of other hashes, .part leftovers, images other than the pinned
    one, and a stale up.json. Only names the lab makes: anything else in
    the cache is left alone. Takes the lab lock. The removed paths."""
    cache = ensure_cache(cache)
    keep = conf.int("KEEP_RUNS") if keep is None else keep
    removed = []

    def rm(path):
        removed.append(path)
        log("gc: %s%s" % ("would remove " if dry_run else "remove ", path))
        if dry_run:
            return
        if os.path.isdir(path) and not os.path.islink(path):
            shutil.rmtree(path)
        else:
            _unlink(path)

    with lab_lock(cache):
        runs_dir = os.path.join(cache, "runs")
        runs = sorted(d for d in os.listdir(runs_dir)
                      if _RUN_NAME.match(d) and os.path.isdir(os.path.join(runs_dir, d)))
        for d in runs[:max(0, len(runs) - keep)]:
            rm(os.path.join(runs_dir, d))
        try:
            current = ref_info(conf, cache)["h12"]
        except LabError:
            current = None  # no key: no way to tell which reference is current
        for f in sorted(os.listdir(cache)):
            p = os.path.join(cache, f)
            m = re.match(r"^ref-([0-9a-f]{12})\.(qcow2|VARS\.fd|json)$", f)
            if (f.endswith(".part") and _CACHE_NAME.match(f)) or (m and current and m.group(1) != current):
                rm(p)
        pinned = os.path.basename(image_path(conf, cache))
        img_dir = os.path.join(cache, "images")
        for f in sorted(os.listdir(img_dir)):
            if f != pinned and _IMAGE_NAME.match(f) and not os.path.isdir(os.path.join(img_dir, f)):
                rm(os.path.join(img_dir, f))
        st = _read_up(cache)
        if st is not None and not _is_up_process(st.get("pid")):
            rm(os.path.join(cache, "up.json"))
    if not removed:
        log("gc: nothing to remove")
    return removed


# ---------------------------------------------------------------- the debug CLI


def _read_up(cache):
    try:
        with open(os.path.join(cache, "up.json")) as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def _is_up_process(pid):
    try:
        with open("/proc/%d/cmdline" % int(pid), "rb") as f:
            argv = f.read().split(b"\0")
    except (OSError, TypeError, ValueError):
        return False
    return any(a.endswith(b"vm.py") for a in argv) and b"up" in argv


def _up_state_or_fail(cache):
    st = _read_up(cache)
    if st is None or not _is_up_process(st.get("pid")):
        raise LabError("no VM is up: start one with vm.py up uefi|bios")
    return st


def cmd_image(conf, a):
    install_signal_handlers()  # a signal ends curl too (subprocess.run kills it) and removes the .part
    cache = ensure_cache()
    with lab_lock(cache, guard=False):
        fetch_image(conf, cache, a.src)


def cmd_provision(conf, a):
    install_signal_handlers()
    provision(conf, force=a.force)


def cmd_up(conf, a):
    install_signal_handlers()
    cache = ensure_cache()
    with lab_lock(cache):
        check_image(conf, cache)
        ref = find_ref(conf, cache)
        key, _ = key_paths(cache)
        _, git7, _ = git_head()
        run = new_run_dir(cache, "up-" + a.mode, git7)
        disk, vars_path = make_overlay(ref, run, a.mode)
        vm = Vm(conf, a.mode, run, disk, vars_path)
        tap = None
        state = os.path.join(cache, "up.json")
        try:
            vm.start()
            tap = SerialTap(vm.serial_sock, os.path.join(run, "serial.raw"),
                            console_path=os.path.join(run, "console.sock"),
                            input_log=os.path.join(run, "input.log"), t0=vm.t0).start(alive=vm.alive)
            vm.qmp.listeners.append(lambda ev: log("up: QMP %s %s" % (ev["event"], json.dumps(ev["data"]))))
            vm.cont()
            _write_file(state, json.dumps({
                "pid": os.getpid(), "qemu_pid": vm.proc.pid, "mode": a.mode, "run": run,
                "ssh_port": vm.ssh_port, "console": os.path.join(run, "console.sock"),
                "key": key, "started": _utc()}, indent=1) + "\n", 0o600)
            log("up: %s VM running (QEMU pid %d), run %s" % (a.mode, vm.proc.pid, run))
            log("up: vm.py ssh | vm.py console | vm.py stop (or Ctrl-C here)")
            while vm.alive():
                time.sleep(0.5)
            log("up: QEMU exited (status %s)" % vm.proc.returncode)
        except (KeyboardInterrupt, SystemExit):  # Ctrl-C, or SIGTERM from vm.py stop
            log("up: stopping")
        finally:
            vm.stop()
            if tap is not None:
                tap.close()
                _write_file(os.path.join(run, "serial.txt"), plain_text(tap.data()))
            _unlink(state)
            if not a.keep:
                discard_disks(run)
            log("up: stopped; logs in %s" % run)


def cmd_ssh(conf, a):
    cache = cache_dir()
    key, _ = key_paths(cache)
    port = a.port or _up_state_or_fail(cache)["ssh_port"]
    command = a.command[1:] if a.command[:1] == ["--"] else a.command
    argv = ssh_argv(port, key) + command
    os.execvp(argv[0], argv)


def cmd_console(conf, a):
    import termios
    import tty
    st = _up_state_or_fail(cache_dir())
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.connect(sock_addr(st["console"]))
    fd = sys.stdin.fileno()
    saved = termios.tcgetattr(fd) if os.isatty(fd) else None
    sys.stderr.write("console: %s serial; Ctrl-] leaves\r\n" % st["mode"])
    try:
        if saved is not None:
            tty.setraw(fd)
        sel = selectors.DefaultSelector()
        sel.register(s, selectors.EVENT_READ, "vm")
        sel.register(fd, selectors.EVENT_READ, "me")
        until = None  # after EOF on a piped stdin: show the output 2 s more
        while until is None or time.monotonic() < until:
            for key, _ in sel.select(0.2):
                if key.data == "vm":
                    data = s.recv(65536)
                    if not data:
                        return
                    os.write(sys.stdout.fileno(), data)
                else:
                    data = os.read(fd, 1024)
                    if b"\x1d" in data:
                        return
                    if not data:
                        sel.unregister(fd)
                        until = time.monotonic() + 2
                        continue
                    s.sendall(data)
    finally:
        if saved is not None:
            termios.tcsetattr(fd, termios.TCSADRAIN, saved)
        s.close()
        sys.stderr.write("\r\nconsole: left\n")


def cmd_stop(conf, a):
    cache = cache_dir()
    st = _read_up(cache)
    if st is None:
        log("stop: no VM is up")
        return
    pid = st.get("pid")
    if not _is_up_process(pid):
        _unlink(os.path.join(cache, "up.json"))
        log("stop: no VM is up (removed a stale up.json)")
        return
    os.kill(pid, signal.SIGTERM)
    deadline = time.monotonic() + 60
    while _is_up_process(pid) and time.monotonic() < deadline:
        time.sleep(0.2)
    if _is_up_process(pid):
        raise LabError("vm.py up (pid %d) did not stop in 60 s" % pid)
    log("stop: stopped")


def cmd_gc(conf, a):
    gc(conf, keep=a.keep, dry_run=a.dry_run)


def main(argv=None):
    ap = argparse.ArgumentParser(prog="vm.py", description="The SmartConfig QEMU lab's machines "
                                 "(see lab/README.md).")
    sub = ap.add_subparsers(dest="op", required=True)
    p = sub.add_parser("image", help="put the pinned cloud image in the cache")
    p.add_argument("--from", dest="src", metavar="FILE", help="copy FILE instead of downloading")
    p.set_defaults(fn=cmd_image)
    p = sub.add_parser("provision", help="build the reference image (about 30 min)")
    p.add_argument("--force", action="store_true", help="build it again even if it exists")
    p.set_defaults(fn=cmd_provision)
    p = sub.add_parser("up", help="boot a throwaway VM in the foreground")
    p.add_argument("mode", choices=MODES)
    p.add_argument("--keep", action="store_true", help="keep its disk afterwards")
    p.set_defaults(fn=cmd_up)
    p = sub.add_parser("ssh", help="ssh to the VM that up runs")
    p.add_argument("--port", type=int, help="this forwarded port instead")
    p.add_argument("command", nargs=argparse.REMAINDER)
    p.set_defaults(fn=cmd_ssh)
    p = sub.add_parser("console", help="the serial console of the VM that up runs")
    p.set_defaults(fn=cmd_console)
    p = sub.add_parser("stop", help="stop the VM that up runs")
    p.set_defaults(fn=cmd_stop)
    p = sub.add_parser("gc", help="remove old runs and stale cache files")
    p.add_argument("--keep", type=int, help="run directories to keep (default KEEP_RUNS)")
    p.add_argument("-n", "--dry-run", action="store_true", help="only say what would go")
    p.set_defaults(fn=cmd_gc)
    a = ap.parse_args(argv)
    try:
        a.fn(load_conf(), a)
    except LabError as e:
        print("vm.py: %s" % e, file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("vm.py: interrupted", file=sys.stderr)
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
