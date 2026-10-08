# M5 plan: the package

Status: draft, 2026-10-08, for your read. Your call (2026-10-07): M5
starts after the M3/M4 follow-ups whether or not you have read this; where
a choice is yours, the plan goes on with the one marked (recommended) and
says so in the worklog, so it can be turned round later.

## 0. Summary

M5 turns the hand install of the README into one `.deb`, `smartconfig`,
that installs, upgrades, removes and purges cleanly on Ubuntu 24.04, and
proves that on a clean VM in the QEMU lab (both firmware modes) and on
this VM, replacing the hand install.

What goes in it (today's hand install, moved to packaged paths):

| File | Today (by hand) | In the package |
|---|---|---|
| `sc` | `/usr/local/sbin/sc` | `/usr/sbin/sc` |
| `scd.service`, `sc-boot-seen.service`, `sc-boot-ok.service` | `/etc/systemd/system/` | `/usr/lib/systemd/system/` |
| the rescue and emergency drop-in | `/etc/systemd/system/{rescue,emergency}.service.d/50-smartconfig.conf` | `/usr/lib/systemd/system/{rescue,emergency}.service.d/50-smartconfig.conf` |
| `42_smartconfig` | `/etc/grub.d/42_smartconfig` | the same (a conffile) |
| the store | `/var/lib/smartconfig` (scd makes it) | the same; kept on remove, asked about on purge (Q3) |
| docs | the repo | `/usr/share/doc/smartconfig/` (README, copyright, changelog) |

`/usr/sbin`, not `/usr/local/sbin`: a package never writes `/usr/local`
(Debian policy 9.1.2), and the rescue boot mounts only `/`, where `/usr`
is (merged-usr on 24.04); `/usr/local` on a disk of its own was a README
caveat that goes away.

## 1. Goals and non-goals

Goals:
1. `make deb` builds `dist/smartconfig_<version>_amd64.deb` from a clean
   tree, reproducibly (the same bytes twice: `SOURCE_DATE_EPOCH` from the
   commit, sorted members, root:root), with the static `sc` the lab
   tests.
2. Install: files in place, `daemon-reload`, the three units enabled
   (`deb-systemd-helper`, as dh_installsystemd would), scd started,
   `update-grub` run, the grub.cfg checked; the first verdict comes at
   the next boot, as today.
3. Upgrade (a newer `.deb` over an older one): scd restarted once, after
   the new `sc` is in place (scd is `Type=notify`: an old `sc` under a new
   unit never says ready); the store and the boots file untouched.
4. Remove: units stopped and disabled, `42_smartconfig` gone from
   grub.cfg (`update-grub`), the menu flag cleared, the store kept.
   Purge: as Q3 says.
5. From the hand install (this VM, and anyone who followed the README):
   the package takes over; hand-installed copies in `/etc/systemd/system`
   and `/usr/local/sbin/sc` that match a released build are moved aside,
   others are left and named (Q4).
6. The lab installs the `.deb` on its clean image instead of
   `install.sh` (a new `--deb` mode of `lab/e2e.py`, both firmware
   modes), runs the M4 scenario unchanged, then upgrade, remove and purge
   checks.
7. CI builds the `.deb` and checks its contents and control file.

Non-goals: an apt repository or PPA (M5 ships the file), signing,
other architectures than amd64 (arm64 later: `sc` builds for it), other
distributions than Ubuntu 24.04, a Debian source package.

## 2. How it is built (Q1)

(recommended) `dpkg-deb --root-owner-group --build` on a staged tree,
from a `make deb` target and a small script (`scripts/build-deb.sh`):
dpkg-deb is on every Ubuntu machine and CI runner, so nothing new is
added. The maintainer scripts are plain sh, written out (no debhelper).

The milestone named nfpm: one YAML file instead of the staging script,
but a new tool (a Go module tree, pinned and fetched with `go run
github.com/goreleaser/nfpm/v2/cmd/nfpm@vX`). The project's rule is no new
dependency without asking: that is Q1.

Version: `0.5.0` at the tag `m5`; between tags
`0.4.99+git<UTC date>.<sha7>` so a later build always sorts higher.
`sc version` prints the same.

## 3. The maintainer scripts

- `postinst configure`: `deb-systemd-helper` enable for the three units
  (`unmask` first), `systemctl daemon-reload`, `systemctl restart scd` on
  an upgrade (start on a first install), `update-grub`, then
  `grub-script-check /boot/grub/grub.cfg`; a failing `update-grub` is
  reported and does not fail the install (the system boots as before
  without the rescue entry). The hand install's copies are handled here
  (Q4).
- `prerm remove`: stop scd. `postrm remove`: `daemon-reload`,
  `update-grub`, `grub-editenv unset smartconfig_pending`.
  `postrm purge`: `deb-systemd-helper purge`, and the store as Q3 says.
- Every script `set -e`, idempotent (dpkg may run them again after a
  failure), and quiet when `/boot/grub` is not there (a container).

## 4. Paths that change

The units' `ExecStart=`, `ConditionFileIsExecutable=`,
`RequiresMountsFor=` and the drop-in's `ExecStartPre=` name
`/usr/sbin/sc`; the README's hand install goes (it points to the
package); `lab/guest/install.sh` stays for the hand-install mode of the
lab until the `.deb` mode has passed, then goes; `accept-m4.sh` reads the
path from the unit. The unit tests' expectations change with them.

## 5. Order of implementation

One step at a time, each with its tests, the full checks, a commit, a
worklog line, push and CI green.

1. `sc version` and the version string (from the build: `-ldflags -X`).
2. The packaged paths: units, drop-in, 42_smartconfig's comments, the
   unit tests (`/usr/sbin/sc`).
3. `scripts/build-deb.sh` and `make deb`: the staged tree, the control
   file, md5sums, conffiles, copyright, changelog; reproducible.
4. The maintainer scripts, with a Go test that runs them against a fake
   root and stub tools (as `TestLabGuestFacts` does for facts.sh).
5. CI: `make deb`, `dpkg-deb --info/--contents` checked against a list,
   the `.deb` as an artifact.
6. The lab's `--deb` mode: install with `apt install ./x.deb`, the M4
   scenario, then upgrade (a second build with a higher version), remove,
   purge (checks D.x).
7. The hand-install migration (Q4), in the lab too (a guest with the
   hand install first).
8. README and docs; a chunk review (two reviewers).
9. Sign-off: `make lab-e2e LAB_E2E_ARGS=--deb` both modes; on this VM:
   install the `.deb` over the hand install, the post-install checks, a
   reboot and its verdict; then remove and install again.

## 6. Acceptance

- `make deb` twice gives the same sha256.
- The lab's `--deb` run passes in both modes, D.x included.
- On this VM: the package installed over the hand install, scd running,
  the next boot's verdict ok, `sc status` exit 0, nothing left in
  `/etc/systemd/system` or `/usr/local/sbin` from the hand install
  (or named, per Q4).

## 7. Risks

- A `postinst` that fails half way leaves units enabled without `sc`:
  the scripts are ordered so each step can be run again, and the lab's
  remove/purge checks look for leftovers.
- `update-grub` in `postinst` on a machine with a broken GRUB config: it
  is reported, not fatal; the README says how to run it again.
- An upgrade from the hand install where `/etc/systemd/system/scd.service`
  shadows the packaged unit: the migration step (Q4) is what prevents a
  `Type=exec` copy running the new `sc`.

## 8. Questions for you

1. Build tool: dpkg-deb, no new dependency (recommended), or nfpm, as the
   milestone line says (a new pinned tool)?
2. Install path: `/usr/sbin/sc` (recommended: policy, and `/` is all the
   rescue boot mounts) or keep `/usr/local/sbin/sc`?
3. Purge: keep `/var/lib/smartconfig` (recommended: the history is the
   point of the tool; the purge says where it is and how to delete it),
   or delete it?
4. The hand install: the package moves aside the copies it recognises
   (recommended), or leaves all of them and only warns?
5. Package name `smartconfig` (recommended) or `sc`?
