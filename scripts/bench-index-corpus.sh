#!/bin/sh
# Copy a live review store into oss/.bench/ (gitignored) so the index
# benchmarks can measure real data, and print the commands that use it.
#
#   oss/scripts/bench-index-corpus.sh [source versions.sqlite] [destination dir]
#
# The source is opened read-only and is never modified. With no source, the
# store of this checkout's git directory is used. Corpus data must not be
# committed.
set -eu

oss="$(cd "$(dirname "$0")/.." && pwd)"
src="${1:-$(git -C "$oss" rev-parse --git-common-dir)/baum/reviews/versions.sqlite}"
dst="${2:-$oss/.bench}"

[ -f "$src" ] || { echo "no store at $src" >&2; exit 1; }
mkdir -p "$dst"
rm -f "$dst/versions.sqlite"
sqlite3 -readonly "$src" ".backup '$dst/versions.sqlite'"
chmod 600 "$dst/versions.sqlite"
ls -l "$dst/versions.sqlite"

# oss/ is mounted at /oss in the oss-test container, and `just oss-go` does not
# forward the environment, so pass BAUM_BENCH_STORE through dev-compose.sh.
cat <<EOF

Measure (from the repository root, with $dst under oss/):
  BAUM_OP_RUN=0 BAUM_DEV_HEAVY=1 ./dev-compose.sh run --rm --no-deps \\
    -e BAUM_BENCH_STORE=/oss/.bench oss-test \\
    go test -run '^\$' -bench . -benchmem -count 5 ./internal/store/ ./internal/reviewindex/
Sizes:
  BAUM_OP_RUN=0 BAUM_DEV_HEAVY=1 ./dev-compose.sh run --rm --no-deps \\
    -e BAUM_BENCH_STORE=/oss/.bench oss-test \\
    go test -run TestBenchCorpusSize -v ./internal/store/
Synthetic corpus: omit BAUM_BENCH_STORE (add -e BAUM_BENCH_SIZE=1 for the size report).
EOF
