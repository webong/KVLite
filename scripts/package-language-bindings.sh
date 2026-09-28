#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 STAGED_DIRECTORY DISTRIBUTION_DIRECTORY" >&2
  exit 2
fi

staged="$1"
distributions="$2"
: "${CARGO_TARGET_DIR:?set CARGO_TARGET_DIR to a temporary build directory}"
mkdir -p "$distributions/python" "$distributions/node" "$distributions/ruby" "$distributions/rust"
distributions="$(cd "$distributions" && pwd)"

# Go's distribution is a tagged source module in its own repository. Verify
# the staged tree independently of the implementation repository's go.work.
(cd "$staged/go" && GOWORK=off go test ./... && GOWORK=off go vet ./...)
composer --working-dir="$staged/php" validate --strict
python3 -m build --sdist --wheel "$staged/python" --outdir "$distributions/python"
npm pack "$staged/node" --pack-destination "$distributions/node"
(cd "$staged/ruby" && ruby -Ilib -e 'Dir["test/test_*.rb"].sort.each { |file| require File.expand_path(file) }')
ruby_version="$(cd "$staged/ruby" && ruby -Ilib -rkvlite/version -e 'print KVLite::VERSION')"
(cd "$staged/ruby" && gem build webong-kvlite.gemspec --strict --output "$distributions/ruby/webong-kvlite-${ruby_version}.gem")
cargo package --manifest-path "$staged/rust/Cargo.toml" --allow-dirty

shopt -s nullglob
crates=("$CARGO_TARGET_DIR"/package/kvlite-*.crate)
if [[ ${#crates[@]} -ne 1 ]]; then
  echo "expected one packaged KVLite crate in $CARGO_TARGET_DIR/package" >&2
  exit 1
fi
cp "${crates[0]}" "$distributions/rust/"
