#!/usr/bin/env bash
# Writes ../third_party/rust.txt: the licences of the Rust crates compiled into
# the desktop app, for every release target, from Cargo.lock.
#
#   scripts/third-party-notices.sh [check]
#
# Run it from desktop/ in the desktop-test image (`just third-party-notices`
# does). With check it compares with the committed file instead of replacing
# it. cargo-about is pinned and installed into CARGO_HOME on first use. The
# rest of the notices are assembled by ../scripts/third-party-notices.sh.
set -euo pipefail

cargo_about=0.9.2

cd "$(dirname "$0")/.."

# cargo install puts the tool in CARGO_HOME/bin, which is not on the PATH of
# the desktop-test image.
export PATH="${CARGO_HOME:-$HOME/.cargo}/bin:$PATH"
if ! command -v cargo-about >/dev/null 2>&1; then
  cargo install --locked cargo-about --version "$cargo_about" --features cli
fi
if [ "$(cargo-about --version)" != "cargo-about $cargo_about" ]; then
  echo "third-party-notices: expected cargo-about $cargo_about, found $(cargo-about --version)" >&2
  exit 1
fi

out=../third_party/rust.txt
if [ "${1:-}" = check ]; then
  out=$(mktemp)
fi
cargo about generate --locked --fail about.hbs -o "$out"
# Some licence files in crates have CRLF line endings; the repository is LF.
sed -i 's/\r$//' "$out"
if [ "${1:-}" = check ]; then
  if ! cmp -s "$out" ../third_party/rust.txt; then
    echo "third_party/rust.txt is stale; run 'just third-party-notices' and commit the result" >&2
    rm -f "$out"
    exit 1
  fi
  rm -f "$out"
  echo "rust notices are current"
fi
