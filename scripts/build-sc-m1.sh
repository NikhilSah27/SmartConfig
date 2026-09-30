#!/usr/bin/env bash
# Build the M1 sc (tag m1, else commit ec15ece) as OUT, for make m1-compat.
#   scripts/build-sc-m1.sh bin/sc-m1
# The source comes from git archive into a temp dir, so the checkout, its
# index and HEAD never change; the build is offline (GOPROXY=off).
set -euo pipefail

out=${1:?usage: $0 OUT}
case $out in /*) ;; *) out=$PWD/$out ;; esac
rev=m1
git rev-parse -q --verify "$rev^{commit}" >/dev/null || rev=ec15ece
src=$(mktemp -d)
trap 'rm -rf "$src"' EXIT
git archive "$rev" | tar -x -C "$src"
mkdir -p "$(dirname "$out")"
(cd "$src" && GOPROXY=off GOFLAGS=-mod=readonly CGO_ENABLED=0 go build -trimpath -o "$out" ./cmd/sc)
echo "built $out from $rev"
