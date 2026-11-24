# Source this file to use the project's Gno toolchain (built by `make tools`).
#
# GNOHOME is project-local: it holds the package cache and the throwaway keys
# the local dev chain uses. It is NOT where your real gnokey keys live; pass
# gnokey its own -home when you sign for a real network.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export GNOROOT="$ROOT/.tools/gno-src"
export GNOHOME="$ROOT/.tools/gnohome"
export PATH="$ROOT/.tools/bin:$PATH"

if [ ! -x "$ROOT/.tools/bin/gno" ]; then
	echo "the Gno toolchain is not built yet; run: make tools" >&2
fi
