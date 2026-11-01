#!/bin/sh
# Runs the agent version matrix inside the agent-matrix container (just
# agent-matrix). See README.md.
set -eu

cd "$(dirname "$0")/../.."
here=scripts/agent-matrix

only_agent= only_version= only_latest=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --agent) only_agent=$2; shift 2 ;;
    --version) only_version=$2; shift 2 ;;
    --only-latest) only_latest=1; shift ;;
    *) echo "usage: run.sh [--agent claude-code|codex] [--version X] [--only-latest]" >&2; exit 2 ;;
  esac
done

cache=${AGENT_MATRIX_CACHE:-/tmp/agent-matrix}
# Test homes go under the cache, not /tmp: Codex refuses to create its helper
# binaries when its CODEX_HOME is in a temporary directory.
mkdir -p "$cache/homes"
export BAUM_MATRIX_HOMES=$cache/homes
work=$(mktemp -d)
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT
results=${AGENT_MATRIX_RESULTS:-$work/results.jsonl}
: > "$results"
export BAUM_MATRIX_RESULTS=$results

echo "== building baum"
mkdir -p "$work/bin"
CGO_ENABLED=0 go build -o "$work/bin/baum" ./cmd/baum
export BAUM_MATRIX_BAUM="$work/bin/baum"
export BAUM_MATRIX_REPO="$PWD"

package() {
  case "$1" in
    claude-code) echo @anthropic-ai/claude-code ;;
    codex) echo @openai/codex ;;
  esac
}
binary() {
  case "$1" in
    claude-code) echo claude ;;
    codex) echo codex ;;
  esac
}

seen_latest=
unexpected=0
grep -v '^#' "${AGENT_MATRIX_FILE:-$here/matrix.txt}" | while read -r agent version support note; do
  [ -n "$agent" ] || continue
  [ -z "$only_agent" ] || [ "$only_agent" = "$agent" ] || continue
  [ -z "$only_version" ] || [ "$only_version" = "$version" ] || continue
  if [ -n "$only_latest" ]; then
    case "$note" in latest*|"baum's declared floor"*) ;; *) continue ;; esac
  fi
  prefix=$cache/installs/$agent/$version
  if [ ! -x "$prefix/node_modules/.bin/$(binary "$agent")" ]; then
    echo "== installing $(package "$agent")@$version"
    rm -rf "$prefix"
    mkdir -p "$prefix"
    if ! npm install --prefix "$prefix" --no-audit --no-fund --loglevel=error "$(package "$agent")@$version" </dev/null; then
      rm -rf "$prefix"
      jq -nc --arg a "$agent" --arg v "$version" --arg s "$support" \
        '{agent: $a, version: $v, support: $s, check: "install", status: "fail", detail: "npm could not install this release"}' >> "$results"
      continue
    fi
  fi
  echo "== testing $agent $version ($support)"
  BAUM_MATRIX_AGENT=$agent BAUM_MATRIX_VERSION=$version BAUM_MATRIX_SUPPORT=$support \
    BAUM_MATRIX_PREFIX=$prefix BAUM_MATRIX_BIN="$prefix/node_modules/.bin/$(binary "$agent")" \
    go test -count=1 -run TestAgentMatrix -timeout 20m ./internal/agentmatrix || true
done

echo
sh "$here/report.sh" "$results"
if [ -n "${AGENT_MATRIX_KEEP:-}" ]; then cp "$results" "$AGENT_MATRIX_KEEP"; fi
# A supported release that fails a check fails the run.
jq -s -e 'map(select(.support == "supported" and .status == "fail")) | length == 0' "$results" >/dev/null
