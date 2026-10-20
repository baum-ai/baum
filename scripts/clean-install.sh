#!/usr/bin/env bash
# Install baum the way a user does, from a module-proxy-shaped source tree, with
# no Node.js on PATH, then smoke test the installed binary.
#
# This proves that "go install github.com/baum-ai/baum/cmd/baum@version"
# needs nothing but Go: the viewer bundle under internal/viewer/dist is
# committed, so the module zip carries it. The zip is built from the committed
# files at HEAD only, as the module proxy builds it from a tag; uncommitted
# changes are not installed. Runs on Linux, macOS and Windows (Git Bash).
set -euo pipefail

module=github.com/baum-ai/baum
version=v0.0.0-cleaninstall

cd "$(dirname "$0")/.."
module_dir=$PWD

# The module is the repository root in the published repository and the oss/
# subdirectory in the private one.
repo_root=$(git rev-parse --show-toplevel)
subdir=$(git rev-parse --show-prefix)
subdir=${subdir%/}

# Go on Windows needs C:/... paths, not the /c/... form Git Bash uses.
native() {
  if command -v cygpath >/dev/null 2>&1; then
    cygpath -m "$1"
  else
    printf '%s\n' "$1"
  fi
}

work=$(mktemp -d)
server_pid=
cleanup() {
  if [ -n "$server_pid" ] && kill -0 "$server_pid" 2>/dev/null; then
    kill -9 "$server_pid" 2>/dev/null || true
  fi
  # The module cache is read-only unless -modcacherw is set; this also covers
  # a cache created without it.
  chmod -R u+w "$work" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }
fail() {
  echo "clean-install: $*" >&2
  exit 1
}

proxy_dir=$work/proxy/$module/@v
mkdir -p "$proxy_dir" "$work/bin" "$work/gopath"

step "build the module zip from the committed files at HEAD"
# The zip builder only accepts a repository whose .git is a directory, which a
# linked worktree's is not, so it reads a local clone. Nothing is checked out:
# the zip is built from the commit, as the module proxy builds it from a tag.
git clone --quiet --no-checkout "$repo_root" "$work/src"
go -C "$module_dir/scripts/zipmod" run . \
  "$module" "$version" "$(native "$work/src")" "$subdir" \
  "$(native "$proxy_dir")/$version.zip" internal/viewer/dist/index.html
git -C "$work/src" show "HEAD:${subdir:+$subdir/}go.mod" >"$proxy_dir/$version.mod"
printf '%s\n' "$version" >"$proxy_dir/list"
printf '{"Version":"%s","Time":"2024-01-01T00:00:00Z"}\n' "$version" >"$proxy_dir/$version.info"
echo "zip contains internal/viewer/dist/index.html"

# A file URL is file:// plus an absolute path: file:///usr/... on Unix and
# file:///C:/... on Windows, where the native path has no leading slash.
proxy_path=$(native "$work/proxy")
case "$proxy_path" in
  /*) proxy_url=file://$proxy_path ;;
  *) proxy_url=file:///$proxy_path ;;
esac

# The install runs with a PATH of its own: a directory holding only go, so
# nothing the build could use Node from is reachable. A system directory would
# not do: Node is often installed beside git and curl. The proxy is a local
# directory, so go needs neither git nor curl here.
goroot=$(go env GOROOT)
# GOROOT is C:\... on Windows; Git Bash wants /c/...
if command -v cygpath >/dev/null 2>&1; then
  goroot_unix=$(cygpath -u "$goroot")
else
  goroot_unix=$goroot
fi
tools=$work/tools
mkdir -p "$tools"
link_tool() {
  local src=$1 name
  [ -f "$src" ] || src=$src.exe
  [ -f "$src" ] || fail "cannot find $1"
  name=$(basename "$src")
  # Git Bash copies where it cannot link; go finds its root through GOROOT.
  ln -s "$src" "$tools/$name" 2>/dev/null || cp "$src" "$tools/$name"
}
link_tool "$goroot_unix/bin/go"
clean_path=$tools

step "install with no Node on PATH"
(
  export PATH=$clean_path
  for tool in node pnpm npm; do
    if command -v "$tool" >/dev/null 2>&1; then
      fail "$tool is still on PATH: $(command -v "$tool")"
    fi
  done
  command -v go >/dev/null 2>&1 || fail "go is not on the Node-free PATH"
  echo "node, pnpm and npm are not on PATH"

  export GOROOT=$goroot
  export GOTOOLCHAIN=local
  export GOWORK=off
  # Nothing from the developer's Go environment may change what is verified:
  # GOPRIVATE would skip the local proxy and a user-level GOSUMDB=off would
  # disable checksum verification.
  export GOENV=off GOPRIVATE='' GONOPROXY='' GONOSUMCHECK='' GOINSECURE='' GOSUMDB=sum.golang.org
  export GOFLAGS=-modcacherw
  export GOPATH
  GOPATH=$(native "$work/gopath")
  export GOMODCACHE
  GOMODCACHE=$(native "$work/gopath/pkg/mod")
  export GOBIN
  GOBIN=$(native "$work/bin")
  # Third-party modules still come from the proxy and are checked against the
  # checksum database; only this one is local and unsigned.
  export GOPROXY="$proxy_url,https://proxy.golang.org"
  export GONOSUMDB=$module
  export CGO_ENABLED=0

  go install "$module/cmd/baum@$version"
)

binary=$work/bin/baum
[ -x "$binary" ] || binary=$work/bin/baum.exe
[ -x "$binary" ] || fail "go install produced no baum binary in $work/bin"
echo "installed $binary"

step "serve the sample review"
port=
for _ in 1 2 3 4 5; do
  candidate=$((20000 + RANDOM % 40000))
  : >"$work/stdout"
  : >"$work/stderr"
  "$binary" internal view --sample --no-open --port "$candidate" >"$work/stdout" 2>"$work/stderr" &
  server_pid=$!
  # The URL is printed alone on stdout once the server listens; an early exit
  # means the port was taken.
  for _ in $(seq 1 100); do
    if [ -s "$work/stdout" ] || ! kill -0 "$server_pid" 2>/dev/null; then break; fi
    sleep 0.2
  done
  if [ -s "$work/stdout" ]; then
    port=$candidate
    break
  fi
  wait "$server_pid" 2>/dev/null || true
  server_pid=
done
if [ -z "$port" ]; then
  cat "$work/stderr" >&2
  fail "baum internal view did not start"
fi
url=$(head -n 1 "$work/stdout" | tr -d '\r')
url=${url%/}
echo "serving $url"

fetch() { curl --silent --show-error --fail --max-time 20 "$@"; }

health=$(fetch "$url/api/health") || fail "/api/health failed"
echo "/api/health: $health"

fetch "$url/" >"$work/index.html" || fail "/ failed"
grep -q '<div id="root">' "$work/index.html" || fail "/ is not the viewer page"
echo "/: $(wc -c <"$work/index.html" | tr -d ' ') bytes"

asset=$(grep -m1 -o '/assets/[A-Za-z0-9_.-]*' "$work/index.html" || true)
[ -n "$asset" ] || fail "index.html references no /assets/ file"
fetch "$url$asset" >"$work/asset" || fail "$asset failed"
[ -s "$work/asset" ] || fail "$asset is empty"
echo "$asset: $(wc -c <"$work/asset" | tr -d ' ') bytes"

fetch "$url/api/document" >"$work/document.json" || fail "/api/document failed"
# The example as committed, like the zip, not the working tree's copy.
git -C "$work/src" show "HEAD:${subdir:+$subdir/}schema/review-example.json" >"$work/example.json"
cmp "$work/document.json" "$work/example.json" || fail "/api/document differs from schema/review-example.json"
echo "/api/document matches schema/review-example.json"

step "stop the server"
kill "$server_pid" 2>/dev/null || true
for _ in $(seq 1 50); do
  kill -0 "$server_pid" 2>/dev/null || break
  sleep 0.2
done
if kill -0 "$server_pid" 2>/dev/null; then
  fail "baum internal view is still running after kill"
fi
server_pid=
echo "baum internal view exited"

printf '\nclean-install: ok\n'
