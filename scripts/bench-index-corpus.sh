#!/bin/sh
# Copy a live review store into oss/.bench/ (gitignored) so the index
# benchmarks can measure real data, and print the commands that use it.
#
#   oss/scripts/bench-index-corpus.sh [source versions.sqlite] [destination dir]
#
# The source is opened read-only and is never modified. With no source, the
# store under this checkout's git common directory is used; a store that
# Store.Open placed in the XDG fallback ($XDG_DATA_HOME/baum/reviews/<digest>/)
# must be named explicitly. Corpus data must not be committed.
set -eu

oss="$(cd "$(dirname "$0")/.." && pwd)"
src="${1:-$(git -C "$oss" rev-parse --path-format=absolute --git-common-dir)/baum/reviews/versions.sqlite}"
dst="${2:-$oss/.bench}"

[ -f "$src" ] || { echo "no store at $src" >&2; exit 1; }
mkdir -p "$dst"
src_dir="$(cd "$(dirname "$src")" && pwd -P)"
dst="$(cd "$dst" && pwd -P)"
if [ "$src_dir" = "$dst" ]; then
	echo "destination $dst holds the source store; choose another directory" >&2
	exit 1
fi
case "$dst" in *"'"*) echo "destination path must not contain a single quote" >&2; exit 1 ;; esac
# Back up beside the destination and rename, so a failed copy never leaves a
# truncated corpus and nothing is removed before the new copy exists.
tmp="$dst/.versions.sqlite.$$"
trap 'rm -f "$tmp"' EXIT
sqlite3 -readonly "$src" ".backup '$tmp'"
chmod 600 "$tmp"
mv -f "$tmp" "$dst/versions.sqlite"
ls -l "$dst/versions.sqlite"

# oss/ is mounted at /oss in the oss-test container, and `just oss-go` does not
# forward the environment, so pass BAUM_BENCH_STORE through dev-compose.sh.
cat <<EOF

Measure (from the repository root, with $dst under oss/):
  BAUM_OP_RUN=0 BAUM_DEV_HEAVY=1 ./dev-compose.sh run --rm --no-deps \\
    -e BAUM_BENCH_STORE=/oss/.bench oss-test \\
    go test -run=NONE -bench . -benchmem -count 5 ./internal/store/ ./internal/reviewindex/
Sizes:
  BAUM_OP_RUN=0 BAUM_DEV_HEAVY=1 ./dev-compose.sh run --rm --no-deps \\
    -e BAUM_BENCH_STORE=/oss/.bench oss-test \\
    go test -run TestBenchCorpusSize -v ./internal/store/
Synthetic corpus: omit BAUM_BENCH_STORE (add -e BAUM_BENCH_SIZE=1 for the size report).
EOF
