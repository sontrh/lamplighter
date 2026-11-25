#!/usr/bin/env bash
# Publish the realms to the Onyx testnet, open the taproom, and put some oil in
# its job.
#
# Onyx stores new code inert: a submitted package is parked until the chain's
# package approver enables it, which takes from about an hour to much longer
# (status: https://gpas.onyx.testnets.gno.land). So this script is run twice:
# once to submit, and again once the realms are active, to open the taproom
# and fund its job. The taproom's keeper is the namespace's account, so the
# key that publishes is the one that opens it.
#
# It is safe to run again. A package that is already active or parked is
# skipped, and if the node cannot say which, the script stops rather than
# guess: the chain accepts a resubmission from the same account and would
# charge for it again. After a testnet reset the same command republishes
# everything.
#
# It signs with a testnet hot key. The key's password is read from a file
# outside the repository straight into gnokey and is never printed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/env.sh"

KEYS="${TAVERN_KEYS:-$HOME/.gnome-tavern-keys}" # gnokey home; also holds the file "password"
KEY="${TAVERN_KEY:-tavern}"
CHAIN=onyx-1
RPC="${ONYX_RPC:-https://rpc.onyx.testnets.gno.land:443}"
WEB=https://onyx.testnets.gno.land
OIL="${OIL:-2000000ugnot}" # 40 rotations at 50000 ugnot each

NS="$(gnokey list -home "$KEYS" | sed -n "s/.* $KEY .*addr: \(g1[a-z0-9]*\).*/\1/p")"
[[ -n "$NS" ]] || {
	echo "no key named '$KEY' in $KEYS"
	exit 1
}
REG="gno.land/r/$NS/lamplighter/registry/v2"
TAP="gno.land/r/$NS/lamplighter/taproom/v3"
LAST="gno.land/r/$NS/lamplighter/lastorders/v1"
PACKAGES=("$REG" "$TAP" "$LAST") # in dependency order

[[ -d "$ROOT/$REG" ]] || {
	echo "the source tree is not under the namespace of key '$KEY' ($NS)"
	exit 1
}
[[ -r "$KEYS/password" ]] || {
	echo "$KEYS/password is missing: a file holding only the key's password"
	exit 1
}

# state prints active, parked or absent for a package path, and fails if the
# node's answers do not settle it.
state() {
	local files parked
	files="$(gnokey query vm/qfile -data "$1" -remote "$RPC" 2>&1 || true)"
	if grep -q '^data: ' <<<"$files" && grep -q 'gnomod.toml' <<<"$files"; then
		echo active
		return
	fi
	parked="$(gnokey query vm/qinertpaths -data "$1" -remote "$RPC" 2>&1 || true)"
	grep -q '^height: ' <<<"$parked" || return 1 # the node did not answer the question
	if sed 's/^data: //' <<<"$parked" | grep -Fxq "$1"; then
		echo parked
		return
	fi
	grep -q 'is not available' <<<"$files" || return 1 # some other error, not "no such package"
	echo absent
}

tx() { # tx <gnokey maketx args...>: broadcast, print a summary, fail unless it succeeded
	local out
	out="$(gnokey maketx "$@" -broadcast -chainid "$CHAIN" -remote "$RPC" \
		-insecure-password-stdin -home "$KEYS" "$KEY" <"$KEYS/password" 2>&1 || true)"
	out="$(grep -v -i 'enter password' <<<"$out" || true)"
	if [[ "$out" == *"OK!"* ]]; then
		grep -E 'OK!|GAS USED|HEIGHT|STORAGE|TOTAL TX COST|TX HASH' <<<"$out" | sed 's/^/    /' || true
		return 0
	fi
	echo "    transaction failed:"
	head -12 <<<"$out" | sed 's/^/    /'
	return 1
}

balance() { gnokey query "bank/balances/$NS" -remote "$RPC" 2>&1 | sed -n 's/^data: "\(.*\)"/\1/p'; }
num() { gnokey query vm/qeval -data "$1" -remote "$RPC" 2>&1 | sed -n 's/^data: (\([0-9]*\) .*/\1/p'; }

echo "key $KEY = $NS, balance $(balance) on $CHAIN"

waiting=0
for pkg in "${PACKAGES[@]}"; do
	st="$(state "$pkg")" || {
		echo "cannot tell whether $pkg is already submitted; try again later"
		exit 1
	}
	case "$st" in
	active) echo "active:  $pkg" ;;
	parked)
		echo "waiting for the approver: $pkg"
		waiting=1
		;;
	absent)
		echo "submitting $pkg"
		tx addpkg -pkgpath "$pkg" -pkgdir "$ROOT/$pkg" -gas-fee 100000ugnot -gas-wanted 100000000
		waiting=1
		;;
	esac
done

if [[ $waiting -eq 1 ]]; then
	echo
	echo "Submitted, not active yet. Run this script again once the approver has enabled the packages:"
	echo "  https://gpas.onyx.testnets.gno.land"
	exit 0
fi

job="$(num "$TAP.JobID()")"
[[ -n "$job" ]] || {
	echo "could not read the taproom's job id"
	exit 1
}
if [[ "$job" == "0" ]]; then
	echo "opening the taproom"
	tx call -pkgpath "$TAP" -func Open -gas-fee 20000ugnot -gas-wanted 20000000
	job="$(num "$TAP.JobID()")"
	[[ -n "$job" && "$job" != "0" ]] || {
		echo "the taproom did not open"
		exit 1
	}
fi
held="$(num "$REG.Balance($job)")"
[[ -n "$held" ]] || {
	echo "could not read the balance of job $job"
	exit 1
}
if [[ "$held" == "0" ]]; then
	echo "funding job $job with $OIL"
	tx call -pkgpath "$REG" -func Fund -args "$job" -send "$OIL" -gas-fee 20000ugnot -gas-wanted 20000000
else
	echo "job $job holds $held ugnot"
fi

echo
echo "balance now $(balance)"
echo "registry: $WEB/r/$NS/lamplighter/registry/v2"
echo "taproom:  $WEB/r/$NS/lamplighter/taproom/v3"
echo "auction:  $WEB/r/$NS/lamplighter/lastorders/v1"
