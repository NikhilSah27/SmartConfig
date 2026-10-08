#!/bin/sh
# version.sh: the package version of the checkout it runs in (M5 plan 2).
# 0.N.0 at the tag mN; after it, 0.N.99+git<count>.<commit time>.<sha7>,
# which a package manager sorts after 0.N.0 and before 0.(N+1).0. count,
# the commits since mN, comes first: a later commit sorts after an earlier
# one, in the same second too (the M5 review, A3). From the commit alone,
# not the clock: the same commit gives the same version. No mN tag before
# HEAD (a clone without its tags) is an error, not a version that sorts
# below every release.
set -e
if tag=$(git describe --exact-match --tags --match 'm[0-9]*' HEAD 2>/dev/null); then
	echo "0.${tag#m}.0"
	exit 0
fi
if ! last=$(git describe --tags --abbrev=0 --match 'm[0-9]*' HEAD 2>/dev/null); then
	echo "version.sh: no mN tag before HEAD: git fetch --tags (in a shallow clone: git fetch --unshallow --tags)" >&2
	exit 1
fi
count=$(git rev-list --count "$last..HEAD")
when=$(TZ=UTC git log -1 --format=%cd --date=format-local:%Y%m%d%H%M%S HEAD)
echo "0.${last#m}.99+git${count}.${when}.$(git rev-parse --short=7 HEAD)"
