#!/bin/sh
# build-deb.sh OUT: the package (M5 plan 2) from bin/sc and scripts/, as
# OUT (make deb: dist/smartconfig_<version>_amd64.deb). dpkg-deb only, no
# new tool; the same commit builds the same bytes (SOURCE_DATE_EPOCH from
# the commit, every file's time and owner fixed, the members sorted).
# Run it in the repo, after make build. SC_BIN, VERSION, DEB_MAINTAINER,
# SOURCE_DATE_EPOCH and REVISION, when set, stand for what it takes from
# bin/sc and git (the tests).
set -eu
out=$1
sc=${SC_BIN:-bin/sc}
[ -x "$sc" ] || { echo "build-deb.sh: no $sc (make build first)" >&2; exit 1; }
version=${VERSION:-$(sh scripts/version.sh)}
maintainer=${DEB_MAINTAINER:-$(git log -1 --format='%an <%ae>' HEAD)}
revision=${REVISION:-$(git rev-parse --short=12 HEAD)}
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct HEAD)}
export SOURCE_DATE_EPOCH
umask 022

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
r=$stage/root
install -D -m 0755 "$sc" "$r/usr/sbin/sc"
for u in scd.service sc-boot-seen.service sc-boot-ok.service; do
	install -D -m 0644 "scripts/$u" "$r/usr/lib/systemd/system/$u"
done
for s in rescue emergency; do
	install -D -m 0644 scripts/smartconfig-rescue.conf "$r/usr/lib/systemd/system/$s.service.d/50-smartconfig.conf"
done
install -D -m 0755 scripts/42_smartconfig "$r/etc/grub.d/42_smartconfig"
doc=$r/usr/share/doc/smartconfig
install -D -m 0644 README.md "$doc/README.md"
cat >"$doc/copyright" <<EOF
Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: SmartConfig
Source: https://github.com/NikhilSah27/SmartConfig

Files: *
Copyright: 2026 $maintainer
License: none
 No license has been chosen yet: all rights reserved.
EOF
chmod 0644 "$doc/copyright"
printf 'smartconfig (%s) noble; urgency=medium\n\n  * Built from %s.\n\n -- %s  %s\n' \
	"$version" "$revision" "$maintainer" "$(date -u -R -d "@$SOURCE_DATE_EPOCH")" |
	gzip -9n >"$doc/changelog.gz"
chmod 0644 "$doc/changelog.gz" # a native package's name for it (no Debian revision)

mkdir -p "$r/DEBIAN"
# Installed-Size as dpkg-gencontrol counts it: each file's (and link's)
# size in KiB, rounded up, and 1 for anything else; not du's blocks, which
# depend on the file system the stage is on (the M5 review, A4).
size=$(cd "$r" && find . -path ./DEBIAN -prune -o ! -name . -printf '%y %s\n' |
	awk '$1 == "f" || $1 == "l" { k += int(($2 + 1023) / 1024); next } { k += 1 } END { print k }')
cat >"$r/DEBIAN/control" <<EOF
Package: smartconfig
Version: $version
Architecture: amd64
Maintainer: $maintainer
Installed-Size: $size
Depends: systemd (>= 255), grub2-common
Section: admin
Priority: optional
Homepage: https://github.com/NikhilSah27/SmartConfig
Description: record and check every config file change, with a rescue boot
 scd records every change to the files under /etc that boot and access
 depend on, and checks them with the system's own validators; sc shows,
 compares and restores them. A GRUB entry boots to a root shell that says
 which change since the last healthy boot broke it and how to undo it.
EOF
echo /etc/grub.d/42_smartconfig >"$r/DEBIAN/conffiles"
# md5sums: every file but the conffile, as dh_md5sums has it (dpkg keeps
# a conffile's own sum).
(cd "$r" && find . -type f ! -path './DEBIAN/*' ! -path ./etc/grub.d/42_smartconfig | sed 's|^\./||' | LC_ALL=C sort | xargs md5sum) >"$r/DEBIAN/md5sums"
for f in preinst postinst prerm postrm; do
	if [ -f "scripts/deb/$f" ]; then install -m 0755 "scripts/deb/$f" "$r/DEBIAN/$f"; fi
done
chmod 0644 "$r/DEBIAN/control" "$r/DEBIAN/conffiles" "$r/DEBIAN/md5sums"
find "$r" -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +
mkdir -p "$(dirname "$out")"
dpkg-deb --root-owner-group -Zxz --build "$r" "$out" >/dev/null
echo "$out"
