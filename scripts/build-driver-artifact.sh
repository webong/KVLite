#!/usr/bin/env bash

# Build one engine-owned executable or C-shared bundle. The normal CLI and C
# ABI packages are driverless; an overlay imports just this bundle's engine.
set -euo pipefail

if (($# < 3 || $# > 4)); then
  echo 'Usage: build-driver-artifact.sh DRIVER cli|c-shared OUTPUT [--linked-extensions]' >&2
  exit 2
fi

driver="$1"
component="$2"
output="$3"
linked_extensions="${4:-}"
if [[ -n "$linked_extensions" && "$linked_extensions" != "--linked-extensions" ]]; then
  echo "unknown option: $linked_extensions" >&2
  exit 2
fi

case "$driver" in
  rocksdb) tags="rocksdb" ;;
  leveldb|badgerdb|boltdb|lmdb) tags="" ;;
  berkeleydb) tags="berkeleydb" ;;
  *) echo "unsupported engine bundle: $driver" >&2; exit 2 ;;
esac

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
case "$component" in
  cli)
    package="./cmd/kvlite"
    placeholder="$repo_root/cmd/kvlite/selected_driver.go"
    ;;
  c-shared)
    [[ -z "$linked_extensions" ]] || { echo '--linked-extensions is only for CLI bundles' >&2; exit 2; }
    package="./capi"
    placeholder="$repo_root/capi/selected_driver.go"
    ;;
  *) echo "unsupported artifact component: $component" >&2; exit 2 ;;
esac

source_file="$repo_root/extensions/$driver/bundle/import.go.txt"
[[ -f "$source_file" && -f "$placeholder" ]] || {
  echo "missing build source for $driver $component" >&2
  exit 2
}
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/kvlite-driver-build.XXXXXX")"
trap 'rm -rf "$temporary_dir"' EXIT
overlay="$temporary_dir/overlay.json"
overlay_sources=("$placeholder" "$source_file")
if [[ -n "$linked_extensions" ]]; then
  for transport in http redis; do
    overlay_sources+=(
      "$repo_root/cmd/kvlite/selected_$transport.go"
      "$repo_root/extensions/$transport/bundle/linked_cli.go.txt"
    )
  done
fi
python3 - "$overlay" "${overlay_sources[@]}" <<'PY'
import json
import sys

sources = sys.argv[2:]
with open(sys.argv[1], "w", encoding="utf-8") as output:
    json.dump({"Replace": dict(zip(sources[::2], sources[1::2]))}, output)
PY

cd "$repo_root"
if [[ "$component" == "c-shared" ]]; then
  go build -overlay "$overlay" -tags "$tags" -trimpath -buildvcs=false \
    -buildmode=c-shared -o "$output" "$package"
else
  go build -overlay "$overlay" -tags "$tags" -trimpath -buildvcs=false \
    -o "$output" "$package"
fi
