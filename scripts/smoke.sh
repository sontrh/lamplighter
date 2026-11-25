#!/usr/bin/env bash
# End-to-end check on a throwaway local chain, with real transactions:
#
#   1. the registry and the taproom: fund, run, refuse what must be refused;
#   2. the Go bot as a first-time executor;
#   3. the Last Orders auction: a realm that funds its own job, settled by the bot;
#   4. the bot against jobs written to cost it money (scripts/testdata/hostile).
#
# It uses its own ports and its own keybase and never touches a real network.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/env.sh"

RPC=127.0.0.1:36657
WEB=127.0.0.1:8899
NS=g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx
REG=gno.land/r/$NS/lamplighter/registry/v2
TAP=gno.land/r/$NS/lamplighter/taproom/v3
LAST=gno.land/r/$NS/lamplighter/lastorders/v1
BAD=gno.land/r/test/hostile
LOG="$(mktemp -t lamplighter-gnodev.XXXXXX)"
WORK="$(mktemp -d -t lamplighter-smoke.XXXXXX)"
BOT="$ROOT/.tools/bin/lamplighter-bot"

[[ -x "$BOT" ]] || {
	echo "the bot is not built; run: make bot"
	exit 1
}

# Throwaway keys. test1 is gnodev's well-known PUBLIC test key, prefunded on
# every dev chain; its mnemonic is in gnodev's own documentation. It is not a
# secret and must never hold real funds.
TEST1_MNEMONIC='source bonus chronic canvas draft south burst lottery vacant surface solve popular case indicate oppose farm nothing bullet exhibit title speed wink action roast'
have_key() { gnokey list -home "$GNOHOME" 2>/dev/null | grep " $1 " >/dev/null; }
have_key test1 || printf '%s\n\n\n' "$TEST1_MNEMONIC" | gnokey add test1 -recover -insecure-password-stdin -home "$GNOHOME" >/dev/null 2>&1
for name in lighter keeper; do
	have_key "$name" || printf '\n\n' | gnokey add "$name" -insecure-password-stdin -home "$GNOHOME" >/dev/null 2>&1
done
addr_of() { gnokey list -home "$GNOHOME" | sed -n "s/.* $1 .*addr: \(g1[a-z0-9]*\).*/\1/p"; }
LIGHTER="$(addr_of lighter)"
KEEPER="$(addr_of keeper)"

# The published taproom rotates every 300s, and its keeper is the namespace's
# own account. Waiting for slots of that length would take a quarter of an
# hour, and this chain has no key for that account, so the dev chain gets a
# copy that rotates every 30s and is kept by the throwaway keeper key. Those
# two constants are the only differences from the published code.
cp -r "$ROOT/gno.land/." "$WORK/"
tap_src="$WORK/r/$NS/lamplighter/taproom/v3/taproom.gno"
sed -e 's/rotateEvery int64 = 300$/rotateEvery int64 = 30/' \
	-e "s/keeper address = \"$NS\"\$/keeper address = \"$KEEPER\"/" \
	"$ROOT/gno.land/r/$NS/lamplighter/taproom/v3/taproom.gno" >"$tap_src"
grep -q 'rotateEvery int64 = 30$' "$tap_src" && grep -q "keeper address = \"$KEEPER\"\$" "$tap_src" || {
	echo "could not adapt the taproom for the dev chain"
	exit 1
}
mkdir -p "$WORK/r/test"
cp -r "$ROOT/scripts/testdata/hostile" "$WORK/r/test/hostile"

# The realms live in an address namespace, so the dev chain deploys them from
# that address, as a real network requires.
gnodev local -C "$WORK" \
	-node-rpc-listener "$RPC" -web-listener "$WEB" \
	-empty-blocks=true -empty-blocks-interval 1 \
	-add-account "$NS=10000000000ugnot" -deploy-key "$NS" \
	-add-account lighter=5000000ugnot -add-account keeper=5000000ugnot \
	-no-watch -interactive=false \
	-paths "$REG,$TAP,$LAST,$BAD" >"$LOG" 2>&1 &
DEV=$!
trap 'kill $DEV 2>/dev/null || true; wait $DEV 2>/dev/null || true; rm -rf "$WORK"' EXIT

# The RPC port answers (with 502) before the node behind it is up, so wait for
# gnodev's own ready line rather than for the port.
ready=0
for _ in $(seq 1 180); do
	if grep -q 'node is ready' "$LOG"; then
		ready=1
		break
	fi
	kill -0 $DEV 2>/dev/null || break
	sleep 1
done
if [[ $ready -ne 1 ]]; then
	echo "gnodev did not become ready, see $LOG"
	exit 1
fi

fails=0
check() { # check <label> <haystack> <needle>
	if [[ "$2" == *"$3"* ]]; then
		echo "  ok    $1"
	else
		echo "  FAIL  $1 (wanted: $3)"
		echo "$2" | head -6 | sed 's/^/        /'
		fails=$((fails + 1))
	fi
}
check_not() { # check_not <label> <haystack> <needle>
	if [[ "$2" != *"$3"* ]]; then
		echo "  ok    $1"
	else
		echo "  FAIL  $1 (did not want: $3)"
		echo "$2" | head -6 | sed 's/^/        /'
		fails=$((fails + 1))
	fi
}
tx() { # tx <key> <pkgpath> <func> [flags...]
	local key=$1 pkg=$2 fn=$3
	shift 3
	echo "" | gnokey maketx call -pkgpath "$pkg" -func "$fn" "$@" \
		-gas-fee 20000ugnot -gas-wanted 20000000 -broadcast -chainid dev \
		-remote "$RPC" -insecure-password-stdin -home "$GNOHOME" "$key" 2>&1 || true
}
call() { # call <key> <func> [flags...]: a call to the registry
	local key=$1
	shift
	tx "$key" "$REG" "$@"
}
storage() { sed -n 's/^STORAGE DELTA: *\(.*\)/\1/p' <<<"$1"; } # the state a transaction added or freed
q() { gnokey query vm/qeval -data "$1" -remote "$RPC" 2>&1 | sed -n 's/^data: //p'; }
num() { q "$1" | sed -n 's/^(\([0-9]*\) .*/\1/p'; }
page() { gnokey query vm/qrender -data "$1" -remote "$RPC" 2>&1; }
bal() { gnokey query "bank/balances/$1" -remote "$RPC" 2>&1 | sed -n 's/^data: "\([0-9]*\)ugnot"/\1/p'; }
bot() { # bot [flags...]: one pass of the Go bot, signing with the keeper key
	LAMPLIGHTER_PASSWORD="" "$BOT" -remote "http://$RPC" -chain-id dev -registry "$REG" \
		-home "$GNOHOME" -key keeper -once "$@" 2>&1 || true
}
wait_due() { # wait_due <job id>; the longest wait is an auction round of 60s
	for _ in $(seq 1 180); do
		[[ "$(q "$REG.IsDue($1)")" == "(true bool)" ]] && return 0
		sleep 1
	done
	echo "  FAIL  job $1 never became due"
	exit 1
}

BLOAT="$(num "$BAD.BloatJob()")"
MOODY="$(num "$BAD.MoodyJob()")"

echo "startup"
check "gnodev started without errors" "errors=$(grep -c ' E ' "$LOG" || true)" "errors=0"
check "the taproom posts nothing until it is opened" "$(q "$TAP.JobID()")" "(0 uint64)"
check "a stranger cannot open the taproom" "$(tx lighter "$TAP" Open)" "only the keeper can open the taproom"
check "the keeper opens the taproom" "$(tx keeper "$TAP" Open)" "OK!"
check "opening the taproom posted its job" "$(page "$REG:")" "Taproom: change the brew on tap"
check "the taproom opens only once" "$(tx keeper "$TAP" Open)" "the taproom is already open"
check "first brew is on tap" "$(q "$TAP.OnTap()")" "Gnomish Amber"
TAPJOB="$(num "$TAP.JobID()")"
check "an unfunded job is not offered to executors" "$(q "$REG.DueJobs(0, 0, 0)")" '("" string)'

echo "who may register"
script_out="$(echo "" | gnokey maketx run -gas-fee 20000ugnot -gas-wanted 20000000 -broadcast -chainid dev \
	-remote "$RPC" -insecure-password-stdin -home "$GNOHOME" test1 "$ROOT/scripts/testdata/register_from_script.gno" 2>&1 || true)"
check "a maketx run script cannot register a job" "$script_out" "only a deployed realm can register a job"

echo "refusals before funding"
out="$(call lighter Exec -args "$TAPJOB")"
check "a job that is neither due nor funded is refused" "$out" "Data: job is "
check "the tap did not change" "$(q "$TAP.Rotations()")" "(0 int64)"

echo "funding"
out="$(call test1 Fund -args "$TAPJOB" -send 500000ugnot)"
check "a patron funds the job" "$out" "OK!"
echo "        funding a job that had no oil changed the state by: $(storage "$out")"
check "funding with no coins is refused" "$(call test1 Fund -args "$TAPJOB")" "send only ugnot"
check "the balance is readable on chain" "$(q "$REG.Balance($TAPJOB)")" "(500000 int64)"

echo "first run"
check "a job that is not due is refused even when funded" "$(call lighter Exec -args "$TAPJOB")" "job is not due yet"
wait_due "$TAPJOB"
check "a due, funded job is offered with its reward and due time" "$(q "$REG.DueJobs(0, 0, 0)")" "(\"$TAPJOB:50000:$(num "$REG.NextDue($TAPJOB)")\" string)"
before="$(bal "$LIGHTER")"
out="$(call lighter Exec -args "$TAPJOB" -max-deposit 10000ugnot)"
check "a stranger runs the job" "$out" "OK!"
check "the registry pays the reward" "$out" '"amount":50000'
check "the tap changed" "$(q "$TAP.OnTap()")" "Deep Root Stout"
after="$(bal "$LIGHTER")"
echo "        executor balance: $before -> $after ugnot (net $((after - before)) on the job's first ever run, fee 20000)"
check "the same slot cannot run twice" "$(call lighter Exec -args "$TAPJOB")" "job is not due yet"

echo "second run, a later transaction"
wait_due "$TAPJOB"
before="$(bal "$LIGHTER")"
out="$(call lighter Exec -args "$TAPJOB" -max-deposit 10000ugnot)"
check "the stored callback still works" "$out" "OK!"
check "the tap changed again" "$(q "$TAP.OnTap()")" "Mushroom Mead"
check "two rotations recorded" "$(q "$TAP.Rotations()")" "(2 int64)"
after="$(bal "$LIGHTER")"
echo "        executor balance: $before -> $after ugnot (net $((after - before)) for this run, fee 20000)"
if [[ $after -gt $before ]]; then
	echo "  ok    running the job is profitable"
else
	echo "  FAIL  running the job lost money"
	fails=$((fails + 1))
fi

echo "the Go bot, as an executor the registry has never seen"
out="$(bot)"
check "the bot starts and identifies itself" "$out" "lamplighter-bot (live): $KEEPER on dev"
check_not "nothing is due, so the bot touches no job" "$out" "job "
wait_due "$TAPJOB"
check "a dry run reports the run and its net gain" "$(bot -dry-run)" "job $TAPJOB: would run: fee "
check "a dry run changes nothing" "$(q "$TAP.Rotations()")" "(2 int64)"
check "a bot asking for more than the job pays skips it" "$(bot -min-profit 49000)" "job $TAPJOB: not worth it"
check "skipped runs change nothing" "$(q "$TAP.Rotations()")" "(2 int64)"
before="$(bal "$KEEPER")"
out="$(bot)"
check "the bot runs the job" "$out" "job $TAPJOB: ran at height"
check "the tap changed a third time" "$(q "$TAP.Rotations()")" "(3 int64)"
after="$(bal "$KEEPER")"
echo "        $(echo "$out" | sed -n 's/.*\(job [0-9]*: ran.*\)/\1/p')"
echo "        bot balance: $before -> $after ugnot (net $((after - before)) on its very first run)"
if [[ $after -gt $before ]]; then
	echo "  ok    a first-time executor makes money on its first run"
else
	echo "  FAIL  a first-time executor lost money on its first run"
	fails=$((fails + 1))
fi
check "the bot's own estimate matches its balance" "$out" "net $((after - before)) ugnot"
check "the dashboard lists the bot's run" "$(page "$REG:")" "| $KEEPER | 0.05 GNOT |"
check "the wrong chain id is caught at startup" "$(bot -chain-id nope)" 'serves chain "dev", not -chain-id "nope"'

echo "Last Orders: an auction that has to end on time"
TEST1="$(addr_of test1)"
out="$(tx test1 "$LAST" Open -args "Friday's last round" -args 60 -send 50000ugnot)"
check "a seller opens a round, and the realm posts and funds its own job in that call" "$out" "OK!"
echo "        opening a round changed the state by: $(storage "$out" | tr '\n' ' ')"
LJOB="$(num "$LAST.JobOf(1)")"
check "the round's job holds the fee the seller put up" "$(q "$REG.Balance($LJOB)")" "(50000 int64)"
check "the job is owned by the auction realm" "$(q "$REG.Owner($LJOB)")" "$LAST"
check "a first bid is taken" "$(tx lighter "$LAST" Bid -args 1 -args "To absent friends" -send 100000ugnot)" "OK!"
check "a bid that does not raise by 5% is refused" "$(tx keeper "$LAST" Bid -args 1 -args "Cheap" -send 104999ugnot)" "bid at least 105000 ugnot"
before="$(bal "$LIGHTER")"
check "a higher bid is taken" "$(tx keeper "$LAST" Bid -args 1 -args "To the lamplighters" -send 105000ugnot)" "OK!"
check "the outbid bidder is refunded at once" "$(bal "$LIGHTER")" "$((before + 100000))"
wait_due "$LJOB"
check "bidding is closed at closing time" "$(tx lighter "$LAST" Bid -args 1 -args "Too late" -send 500000ugnot)" "the round is closed"
seller_before="$(bal "$TEST1")"
before="$(bal "$KEEPER")"
out="$(bot)"
check "the bot settles the round" "$out" "job $LJOB: ran at height"
echo "        $(echo "$out" | sed -n 's/.*\(job [0-9]*: ran.*\)/\1/p')"
check "the winning bid reached the seller" "$(bal "$TEST1")" "$((seller_before + 105000))"
check "the round is settled" "$(q "$LAST.Settled(1)")" "(true bool)"
check "the job is done, and kept until its owner cleans it up" "$(q "$REG.Done($LJOB)")" "(true bool)"
check "the toast is on the wall" "$(page "$LAST:")" "To the lamplighters"
# The bot may have run the taproom's job in the same pass: add up every run.
earned=0
while read -r net; do
	earned=$((earned + net))
done < <(echo "$out" | sed -n 's/.*: ran at height.* net \(-\{0,1\}[0-9]*\) ugnot$/\1/p')
check "the bot's estimates add up to its balance" "earned=$earned" "earned=$(($(bal "$KEEPER") - before))"
check "only the seller can reclaim the round's deposit" "$(tx lighter "$LAST" Reclaim -args 1)" "only the seller can reclaim"
out="$(tx test1 "$LAST" Reclaim -args 1)"
check "the seller cancels the finished job" "$out" "OK!"
echo "        reclaiming changed the state by: $(storage "$out" | tr '\n' ' ')"
check "the job is gone" "$(q "$REG.Exists($LJOB)")" "(false bool)"

echo "the bot against a job that grows its state at the runner's expense"
check "the hostile job is funded" "$(call test1 Fund -args "$BLOAT" -send 50000ugnot)" "OK!"
wait_due "$BLOAT"
before="$(bal "$KEEPER")"
out="$(bot)"
check "its deposit exceeds the bot's limit, so the simulation fails and nothing is sent" "$out" "job $BLOAT: would fail, skipped for 1m0s"
echo "        $(echo "$out" | sed -n "s/.*\(job $BLOAT: .*\)/\1/p" | cut -c1-150)"
out="$(bot -max-deposit 200000)"
check "with a limit wide enough to simulate it, the bot sees it does not pay" "$out" "job $BLOAT: not worth it"
check "the bot spent nothing on it" "$(bal "$KEEPER")" "$before"

echo "the bot against a job that passes simulation and fails in its block"
check "the hostile job is funded" "$(call test1 Fund -args "$MOODY" -send 400000ugnot)" "OK!"
seen_loss=0
for try in $(seq 1 20); do
	wait_due "$MOODY"
	before="$(bal "$KEEPER")"
	out="$(bot)"
	if [[ "$out" == *"job $MOODY: failed at height"* ]]; then
		seen_loss=1
		break
	fi
	sleep 1
done
if [[ $seen_loss -eq 1 ]]; then
	after="$(bal "$KEEPER")"
	echo "  ok    the failure in the real block was observed (attempt $try)"
	check "the bot names the callback's own reason" "$out" "job failed: not in the mood"
	check "the bot says what it cost" "$out" "and cost the fee of $((before - after))"
	check "the bot then leaves that job alone" "$out" "job $MOODY: leaving it alone for 10m0s"
	echo "        $(echo "$out" | sed -n "s/.*\(job $MOODY: failed.*\)/\1/p" | cut -c1-150)"
else
	echo "  FAIL  the job never failed in a real block in 20 attempts"
	fails=$((fails + 1))
fi

# With RENDER_DIR set, keep what the realms render at this point, as markdown.
if [[ -n "${RENDER_DIR:-}" ]]; then
	mkdir -p "$RENDER_DIR"
	page "$REG:" | sed -e '1,/^data: /{/^data: /!d;s/^data: //;}' >"$RENDER_DIR/registry.md"
	page "$REG:job/$TAPJOB" | sed -e '1,/^data: /{/^data: /!d;s/^data: //;}' >"$RENDER_DIR/job.md"
	page "$TAP:" | sed -e '1,/^data: /{/^data: /!d;s/^data: //;}' >"$RENDER_DIR/taproom.md"
	page "$LAST:" | sed -e '1,/^data: /{/^data: /!d;s/^data: //;}' >"$RENDER_DIR/lastorders.md"
fi

echo
if [[ $fails -eq 0 ]]; then
	rm -f "$LOG"
	echo "PASS"
else
	echo "FAILED: $fails check(s), gnodev log: $LOG"
	exit 1
fi
