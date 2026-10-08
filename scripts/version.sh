#!/bin/sh
# version.sh: the package version of the checkout it runs in (M5 plan 2).
# 0.N.0 at the tag mN; after it, 0.N.99+git<commit time>.<sha7>, which a
# package manager sorts after 0.N.0 and before 0.(N+1).0. The commit's
# own time, not the clock: the same commit gives the same version.
set -e
if tag=$(git describe --exact-match --tags --match 'm[0-9]*' HEAD 2>/dev/null); then
	echo "0.${tag#m}.0"
	exit 0
fi
last=$(git describe --tags --abbrev=0 --match 'm[0-9]*' HEAD 2>/dev/null || echo m0)
when=$(TZ=UTC git log -1 --format=%cd --date=format-local:%Y%m%d%H%M%S HEAD)
echo "0.${last#m}.99+git${when}.$(git rev-parse --short=7 HEAD)"
