#!/usr/bin/env bash
# Milestone 1 acceptance run. From the repo root, after `make build`:
#   sudo ./scripts/smoke.sh
# Uses the real /etc/hosts and a throwaway SC_HOME. /etc/hosts is put back
# from a plain copy on exit, whatever happens.
set -euo pipefail

SC=./bin/sc
HOSTS=/etc/hosts

fail() { echo "FAIL: $*" >&2; exit 1; }
step() { echo "== $*"; }

[ "$(id -u)" -eq 0 ] || fail "run as root: sudo $0"
[ -x "$SC" ] || fail "$SC missing, run: make build"

export SC_HOME
SC_HOME=$(mktemp -d /tmp/sc-smoke.XXXXXX)
backup="$SC_HOME.hosts"
cp -p "$HOSTS" "$backup"
cleanup() {
	cp -p "$backup" "$HOSTS"
	rm -rf "$SC_HOME" "$backup"
}
trap cleanup EXIT
grep -q smoke.invalid "$HOSTS" && fail "$HOSTS already contains smoke.invalid"

step "1. init"
"$SC" init

step "2. baseline snapshot"
id1=$("$SC" snapshot "$HOSTS" -q -m "baseline")
[[ "$id1" =~ ^[0-9a-f]{6}$ ]] || fail "snapshot -q printed '$id1'"
echo "$id1"

step "3. break $HOSTS"
echo "127.0.0.1 smoke.invalid" >>"$HOSTS"

step "4. snapshot the edit"
"$SC" snapshot "$HOSTS" -m "added smoke line"

step "5. diff $id1"
diff_out=$("$SC" diff "$id1")
echo "$diff_out"
grep -q '^+.*smoke\.invalid' <<<"$diff_out" || fail "diff has no + line with smoke.invalid"

step "6. restore $id1"
"$SC" restore "$id1"

step "7. smoke line gone"
if grep -q smoke.invalid "$HOSTS"; then fail "smoke.invalid still in $HOSTS"; fi

step "8. log $HOSTS"
log_out=$("$SC" log "$HOSTS")
echo "$log_out"
origins=$(awk 'NR>1 {print $4}' <<<"$log_out" | paste -sd' ')
[ "$origins" = "restore pre-restore manual manual" ] || fail "log origins: '$origins'"

step "9. mode and owner"
perms=$(stat -c '%a %U:%G' "$HOSTS")
echo "$perms"
[ "$perms" = "644 root:root" ] || fail "stat says '$perms'"

echo "PASS"
