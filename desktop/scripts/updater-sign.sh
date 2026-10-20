#!/usr/bin/env bash
# Signs updater bundles with the release key, as the Tauri CLI would:
#
#   scripts/updater-sign.sh VERSION FILE...
#
# Each FILE gets FILE.sig: the minisign signature, base64 encoded, whose
# trusted comment names the file and VERSION ("timestamp:…<tab>file:…<tab>
# version:…"). The app's updater checks that version against the one the
# manifest announces (requireSignedVersion). Each signature is then verified
# against the public key in tauri.conf.json, so signing with the wrong key
# fails here rather than on users' machines.
#
# TAURI_SIGNING_PRIVATE_KEY is the content of the key file `tauri signer
# generate` writes; TAURI_SIGNING_PRIVATE_KEY_PASSWORD its password. MINISIGN
# names the minisign binary (default: minisign on PATH; the release must use
# scripts/install-minisign.sh's and sets BAUM_RELEASE_SIGNING=1). UPDATER_PUBKEY overrides the configured
# public key (tests). Runs no build tooling.
set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: scripts/updater-sign.sh VERSION FILE..." >&2
  exit 2
fi
version=$1
shift
if [ -z "${TAURI_SIGNING_PRIVATE_KEY:-}" ]; then
  echo "updater-sign: TAURI_SIGNING_PRIVATE_KEY is not set" >&2
  exit 1
fi
# In the release (BAUM_RELEASE_SIGNING=1) only the pinned minisign that
# scripts/install-minisign.sh installed may run, never one found on PATH.
if [ "${BAUM_RELEASE_SIGNING:-}" = 1 ]; then
  case "${MINISIGN:-}" in
    /*) ;;
    *)
      echo "$(basename "$0"): MINISIGN must be the absolute path of the pinned minisign in the release" >&2
      exit 1
      ;;
  esac
  if [ ! -f "$MINISIGN" ] || [ ! -x "$MINISIGN" ]; then
    echo "$(basename "$0"): MINISIGN ($MINISIGN) is not an executable file" >&2
    exit 1
  fi
fi
minisign=${MINISIGN:-minisign}
here=$(cd "$(dirname "$0")/.." && pwd)
pubkey=${UPDATER_PUBKEY:-$(sed -n 's/.*"pubkey": *"\([A-Za-z0-9+/=]*\)".*/\1/p' "$here/tauri.conf.json")}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
(
  umask 077
  printf '%s' "$TAURI_SIGNING_PRIVATE_KEY" | base64 --decode >"$work/key"
)
printf '%s' "$pubkey" | base64 --decode >"$work/key.pub"

for file in "$@"; do
  name=$(basename "$file")
  comment=$(printf 'timestamp:%s\tfile:%s\tversion:%s' "$(date +%s)" "$name" "$version")
  # minisign reads the password from standard input when it is not a terminal.
  if ! printf '%s\n' "${TAURI_SIGNING_PRIVATE_KEY_PASSWORD:-}" |
    "$minisign" -S -s "$work/key" -m "$file" -x "$work/signature" -t "$comment" >"$work/log" 2>&1; then
    echo "updater-sign: signing $name failed:" >&2
    grep -v -i password "$work/log" >&2 || true
    exit 1
  fi
  if ! "$minisign" -V -q -p "$work/key.pub" -m "$file" -x "$work/signature" >/dev/null 2>&1; then
    echo "updater-sign: the signature of $name does not verify with the configured public key" >&2
    exit 1
  fi
  base64 <"$work/signature" | tr -d '\n' >"$file.sig"
  echo "signed $name for $version"
done
