#!/usr/bin/env bash
# Build the Gno toolchain this project is developed against into .tools/:
# gno, gnodev and gnokey at the exact version the Onyx testnet runs. Tools
# built from another version can disagree with the chain about the language,
# the standard packages and the transaction encoding.
#
# Needs git and Go 1.25.9 or newer on PATH (override the binary with GO=...).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GNO_VERSION="${GNO_VERSION:-v1.8.0-rc.1}"
GO="${GO:-go}"
SRC="$ROOT/.tools/gno-src"
BIN="$ROOT/.tools/bin"

command -v "$GO" >/dev/null || {
	echo "Go not found (looked for '$GO'); install Go 1.25.9+ or set GO=/path/to/go"
	exit 1
}

mkdir -p "$BIN"
if [[ ! -d "$SRC/.git" ]]; then
	git clone --quiet --depth 1 --branch "$GNO_VERSION" https://github.com/gnolang/gno.git "$SRC"
fi
have="$(git -C "$SRC" describe --tags --exact-match 2>/dev/null || true)"
if [[ "$have" != "$GNO_VERSION" ]] && ! git -C "$SRC" tag --points-at HEAD | grep -qx "$GNO_VERSION"; then
	echo "$SRC is not at $GNO_VERSION; remove it and run this again"
	exit 1
fi

echo "building gno, gnokey and gnodev $GNO_VERSION (a few minutes the first time)"
(cd "$SRC" && "$GO" build -o "$BIN/gno" ./gnovm/cmd/gno)
(cd "$SRC" && "$GO" build -o "$BIN/gnokey" ./gno.land/cmd/gnokey)
(cd "$SRC/contribs/gnodev" && "$GO" build -o "$BIN/gnodev" .)
echo "done: $BIN"
