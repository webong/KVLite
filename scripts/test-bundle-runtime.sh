#!/usr/bin/env bash
#
# Functional check for scripts/bundle-native-deps.sh using a synthetic C
# library. It proves a non-system dependency is copied into the bundle,
# re-pointed at a loader-relative path (Mach-O/ELF) or found beside the
# executable (PE), and executable from the bundle tree.
#
# Runs on the current native target:
#   darwin-*  needs cc, otool, install_name_tool
#   linux-*   needs cc and patchelf
#   windows-* needs cl and dumpbin, i.e. a Visual Studio developer shell
#             (CI provides them through ilammy/msvc-dev-cmd)
# A target whose toolchain is missing is skipped, never failed.

set -euo pipefail

fail() {
  printf 'bundle-runtime test: %s\n' "$*" >&2
  exit 1
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
cd "$repo_root"

target="$(go env GOHOSTOS)-$(go env GOHOSTARCH)"
os="${target%%-*}"
exe=""
lib_prefix="lib"
case "$os" in
  darwin)
    suffix="dylib"
    command -v cc >/dev/null 2>&1 || { printf 'bundle-runtime test: SKIP (no cc)\n' >&2; exit 0; }
    command -v install_name_tool >/dev/null 2>&1 || { printf 'bundle-runtime test: SKIP (no install_name_tool)\n' >&2; exit 0; }
    ;;
  linux)
    suffix="so"
    command -v cc >/dev/null 2>&1 || { printf 'bundle-runtime test: SKIP (no cc)\n' >&2; exit 0; }
    command -v patchelf >/dev/null 2>&1 || { printf 'bundle-runtime test: SKIP (no patchelf)\n' >&2; exit 0; }
    ;;
  windows)
    # MSVC names the DLL and its import library after -Fe, with no "lib"
    # prefix: an import table records that bare DLL name. The import library
    # only exists because dep.c exports its symbol.
    suffix="dll"
    exe=".exe"
    lib_prefix=""
    command -v cl >/dev/null 2>&1 || { printf 'bundle-runtime test: SKIP (no cl; run in a Visual Studio developer shell)\n' >&2; exit 0; }
    command -v dumpbin >/dev/null 2>&1 || { printf 'bundle-runtime test: SKIP (no dumpbin)\n' >&2; exit 0; }
    ;;
  *) printf 'bundle-runtime test: SKIP (unsupported target %s)\n' "$target" >&2; exit 0 ;;
esac
# `link_name` is what -l takes on Unix and the import library is named after
# on Windows; `declared_dependency` is how the dependency appears to the
# loader (and therefore in otool/ldd/dumpbin output).
link_name="kvlite_test_dep"
declared_dependency="$lib_prefix$link_name"
dependency_file="$declared_dependency.$suffix"

work_root="$(mktemp -d "${TMPDIR:-/tmp}/kvlite-bundle-runtime-test.XXXXXX")"
cleanup() {
  rm -rf "$work_root"
}
trap cleanup EXIT

cat > "$work_root/dep.c" <<'EOF'
#if defined(_WIN32)
#define KVLITE_TEST_EXPORT __declspec(dllexport)
#else
#define KVLITE_TEST_EXPORT
#endif

KVLITE_TEST_EXPORT int kvlite_test_dep_answer(void) {
  return 42;
}
EOF
cat > "$work_root/main.c" <<'EOF'
#include <stdio.h>
int kvlite_test_dep_answer(void);
int main(void) {
  printf("%d\n", kvlite_test_dep_answer());
  return 0;
}
EOF

# MSVC runs from work_root with bare relative names only: "-" replaces "/" in
# its flags and no POSIX path ever reaches it for MSYS to rewrite.
run_cl() {
  (cd "$work_root" && cl -nologo "$@" >/dev/null)
}

list_dependencies() {
  case "$os" in
    darwin) otool -L "$1" 2>/dev/null | awk 'NR > 1 { print $1 }' ;;
    linux) ldd "$1" 2>/dev/null || true ;;
    windows)
      # CRLF from the native tool would leave a trailing CR on every name.
      dumpbin -dependents "$1" 2>/dev/null | tr -d '\r' | \
        awk '/\.dll/ { for (i = 1; i <= NF; i++) if ($i ~ /\.dll$/i) print $i }' | sort -u
      ;;
  esac
}

if [[ "$os" == windows ]]; then
  run_cl -LD -O2 -Fe:"$dependency_file" dep.c || fail "could not build synthetic dependency"
  [[ -f "$work_root/$dependency_file" ]] || fail "synthetic dependency was not produced"
  import_library=""
  for candidate in "$work_root"/*.lib; do
    if [[ -f "$candidate" ]]; then
      import_library="$(basename "$candidate")"
      break
    fi
  done
  [[ -n "$import_library" ]] || fail "no import library was produced beside the DLL"
else
  build_dependency() {
    if [[ "$os" == darwin ]]; then
      cc -dynamiclib -fPIC -O2 -o "$1" "$2"
    else
      cc -shared -fPIC -O2 -o "$1" "$2"
    fi
  }
  build_dependency "$work_root/$dependency_file" "$work_root/dep.c" || fail "could not build synthetic dependency"
  import_library=""
fi

# Link the program. Mach-O/ELF carry a real rpath like release binaries do, so
# the dependency resolves before bundling; PE imports are bare names already.
if [[ "$os" == windows ]]; then
  run_cl -O2 -Fe:"prog$exe" main.c "$import_library" || fail "could not build synthetic binary"
  program="$work_root/prog$exe"
else
  cc -O2 -o "$work_root/prog" "$work_root/main.c" -L"$work_root" \
    "-l$link_name" -Wl,-rpath,"$work_root" || fail "could not build synthetic binary"
  program="$work_root/prog"
fi

staging="$work_root/staging"
mkdir -p "$staging/bin" "$staging/lib"
cp "$program" "$staging/bin/prog$exe"

list_dependencies "$staging/bin/prog$exe" | grep -qi "$declared_dependency" || \
  fail "synthetic binary does not link the synthetic dependency"

KVLITE_BUNDLE_LIB_PATH="$work_root" \
  bash "$repo_root/scripts/bundle-native-deps.sh" "$staging" "$target" >/dev/null || fail "bundling failed"

bundle_dir="$staging/lib"
[[ "$os" == windows ]] && bundle_dir="$staging/bin"
[[ -f "$bundle_dir/$dependency_file" ]] || fail "dependency was not copied into $bundle_dir"

after="$(list_dependencies "$staging/bin/prog$exe")"
echo "$after" | grep -qi "$declared_dependency" || fail "bundled binary lost its dependency reference"
if [[ "$os" != windows ]] && echo "$after" | grep -q "$work_root/$dependency_file"; then
  fail "bundled binary still references the absolute dependency path"
fi

# The bundle must execute from its own tree and from elsewhere, which is only
# possible if the loader finds the bundled copy.
[[ "$("$staging/bin/prog$exe")" == "42" ]] || fail "bundled binary did not run from its own tree"
(cd "$repo_root" && [[ "$("$staging/bin/prog$exe")" == "42" ]]) || fail "bundled binary did not run from another directory"

# Bundling twice must be a stable no-op, not a duplicate failure.
bash "$repo_root/scripts/bundle-native-deps.sh" "$staging" "$target" >/dev/null || fail "re-bundling failed"
[[ "$("$staging/bin/prog$exe")" == "42" ]] || fail "re-bundled binary did not run"

if [[ "$os" != windows ]]; then
  # Bare dependency names (no directory part, as recorded for install-name
  # basenames such as RocksDB's) must resolve through KVLITE_BUNDLE_LIB_PATH.
  # The binary deliberately carries no rpath, so only the variable saves it.
  # Windows imports are always bare names, so the case above already covers it.
  mkdir -p "$work_root/deplibs"
  cp "$work_root/dep.c" "$work_root/bare.c"
  if [[ "$os" == darwin ]]; then
    cc -dynamiclib -fPIC -O2 -o "$work_root/deplibs/libbaredep.dylib" "$work_root/bare.c" || fail "could not build bare dependency"
    install_name_tool -id "libbaredep.dylib" "$work_root/deplibs/libbaredep.dylib" || fail "could not set bare install name"
  else
    cc -shared -fPIC -O2 -o "$work_root/deplibs/libbaredep.so" "$work_root/bare.c" || fail "could not build bare dependency"
  fi
  cc -O2 -o "$work_root/prog2" "$work_root/main.c" -L"$work_root/deplibs" -lbaredep || fail "could not build bare-linked binary"
  bare_staging="$work_root/bare-staging"
  mkdir -p "$bare_staging/bin"
  cp "$work_root/prog2" "$bare_staging/bin/prog2"
  KVLITE_BUNDLE_LIB_PATH="$work_root/deplibs" \
    bash "$repo_root/scripts/bundle-native-deps.sh" "$bare_staging" "$target" >/dev/null || fail "bare-name bundling failed"
  if [[ "$os" == darwin ]]; then
    bundled_bare="$bare_staging/lib/libbaredep.dylib"
  else
    bundled_bare="$bare_staging/lib/libbaredep.so"
  fi
  [[ -f "$bundled_bare" ]] || fail "bare dependency was not copied into lib/"
  [[ "$("$bare_staging/bin/prog2")" == "42" ]] || fail "bare-bundled binary did not run"
fi

# An unresolvable dependency must fail loudly with the library name, never
# silently produce an incomplete bundle. (Linux reports it through ldd;
# Windows searches beside the executable and then PATH. macOS records an
# absolute install name, so an unlinked-away library resolves there and this
# case does not exist.)
if [[ "$os" == linux || "$os" == windows ]]; then
  bad_staging="$work_root/bad-staging"
  mkdir -p "$bad_staging/bin"
  if [[ "$os" == windows ]]; then
    mv "$work_root/$dependency_file" "$work_root/hidden-$dependency_file"
    run_cl -O2 -Fe:"prog-unresolved$exe" main.c "$import_library" || fail "could not build unresolved binary"
    cp "$work_root/prog-unresolved$exe" "$bad_staging/bin/prog$exe"
  else
    cc -O2 -o "$work_root/prog-unresolved" "$work_root/main.c" -L"$work_root" \
      "-l$link_name" || fail "could not build unresolved binary"
    cp "$work_root/prog-unresolved" "$bad_staging/bin/prog"
  fi
  if bash "$repo_root/scripts/bundle-native-deps.sh" "$bad_staging" "$target" >/dev/null 2>&1; then
    fail "unresolvable dependency was accepted"
  fi
fi

printf 'bundle-runtime test: ok (synthetic dependency bundled and executable)\n' >&2
