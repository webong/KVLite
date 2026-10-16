# KVLite packaging contract

This directory holds the starting points for system package managers
(Homebrew, Debian, and friends). KVLite is built for this model: a small
extension-free host binary plus versioned, checksummed artifact directories
that are discovered at runtime. A package only moves files into place and
points discovery at them; no binary patching is ever required.

## The fast path: online installer

Most users should never touch what follows. Published releases carry
per-target tarballs plus a versioned installer script:

```sh
curl -fsSL https://github.com/webong/KVlite/releases/latest/download/kvlite-installer.sh | bash
```

The release workflow stamps the installer with its own version, so a script
downloaded from a specific prerelease installs that same prerelease by default.
Every push to `main` builds a canary. You can also manually run
`Build release artifacts` on `main` with `publish_canary=true`. It creates a
`v0.1.0-canary.N` GitHub prerelease only
after native and binding artifacts pass. Use that prerelease's
`kvlite-installer.sh` URL to try it; a canary is not selected by GitHub's
`releases/latest` URL. Registry uploads are separate opt-ins, not implied by
the GitHub prerelease.

That installs the pluggable-first base: host CLI plus protocol modules,
memory only, no persistent engine. Add one with `--driver leveldb`,
`badgerdb`, `boltdb`, `lmdb`, or `rocksdb` (not on Windows; see
[Windows](#windows-mingw-w64-tarballs-like-sqlite)). Useful flags: `--version`, `--prefix`, `--no-http`,
`--no-redis`, `--shell-rc`, `--yes`. The installer detects the platform,
verifies the tarball checksum before unpacking, installs through the same
`scripts/install.sh` path below, verifies every module, and prints the one
export it needs (`KVLITE_SYSTEM_MODULE_PATH`). A tampered download
is rejected before anything executes. `make test-install-online` covers the
whole flow against a local asset server, including tamper rejection.

Release assets also carry a keyless Sigstore build-provenance attestation, so
a checksum can be traced to the workflow run that produced it:

```sh
gh attestation verify kvlite-linux-amd64.tar.gz --repo webong/KVLite
```

The attestation covers the platform tarballs, their `.sha256` sidecars, the
stamped `kvlite-installer.sh`, and the binding packages attached to the
release; `release-artifacts.yml` attests them before publishing, so a failed
attestation aborts the release. Registry packages get their provenance from
their own registry publication instead.

## Package split

One installable package per bundle kind, so users install only what they run:

| Package (example name) | Contents | Depends on |
| --- | --- | --- |
| `kvlite` | driverless host `bin/kvlite` (core + memory), plus `include/kvlite.h` and the C-shared library | nothing |
| `kvlite-leveldb` | driver bundle (no CLI link) | `kvlite` (host drives it) |
| `kvlite-badgerdb` | pure-Go BadgerDB driver bundle | `kvlite` |
| `kvlite-boltdb` | pure-Go bbolt driver bundle | `kvlite` |
| `kvlite-lmdb` | LMDB CGo driver bundle with statically linked LMDB 0.9 source | `kvlite` |
| `kvlite-rocksdb` | same shape for RocksDB | `kvlite` + distro RocksDB/compression libs, or the `--bundle-runtime` output |
| `kvlite-http` | `bin/kvlite-http` + bundle | `kvlite` + any one driver package (or memory) |
| `kvlite-redis` | `bin/kvlite-redis` + bundle | `kvlite` + any one driver package (or memory) |

Berkeley DB is never a normal package: it stays a separately licensed,
explicitly enabled build (see `extensions/berkeleydb`).

## Layout

Install release trees with `scripts/install.sh`, which enforces this layout
and keeps every bundle-internal path relative (safe for `DESTDIR` staging
and relocated prefixes):

```text
<prefix>/bin/kvlite{,-http,-redis}   # symlinks into the catalog
<prefix>/lib/kvlite/drivers/*/       # full driver bundles, intact
<prefix>/lib/kvlite/modules/*/       # full protocol bundles, intact
<prefix>/include/kvlite.h            # reviewed C ABI header
<prefix>/share/doc/kvlite/<bundle>/  # third-party notices per bundle
```

Only one CLI can own `bin/kvlite`. When several driver packages are
installed, pick the primary with `install.sh --link-cli`; the others remain
reachable through `kvlite module run` (or the manager's alternatives
system, e.g. Debian `update-alternatives`).

Build, then install from source with:

```sh
make release RELEASE_VERSION=v0.1.0 DRIVER=leveldb
make release-http release-redis RELEASE_VERSION=v0.1.0
make install INSTALL_PREFIX=/usr/local INSTALL_VERSION=v0.1.0
```

Packagers staging a package pass `DESTDIR=/tmp/stage make install`; the
script verifies the assembled tree through the same discovery a user gets
whenever a driver CLI is present.

## Discovery

Point the catalog root at the install with the system tier:

```sh
export KVLITE_SYSTEM_MODULE_PATH="<prefix>/lib/kvlite"
```

Search order is `KVLITE_MODULE_PATH`, `KVLITE_HOME/{modules,drivers}`,
then `KVLITE_SYSTEM_MODULE_PATH`, so a user install always shadows the
system one. Daemons and services without a login shell should get the
variable from the service definition (systemd `Environment=`, launchd
`EnvironmentVariables`) rather than shell init. The grouping directories
`drivers/` and `modules/` are part of the discovery contract: a catalog
root is usable as a single search path (see `MODULES.md`).

## Build rules for recipes

- Build natively per OS/arch; never cross-compile. The release scripts
  refuse cross builds because RocksDB is a native dependency.
- `CGO_ENABLED=1` is required (the driver loader uses the system dynamic
  loader).
- Stamp the release version through (`make release RELEASE_VERSION=...`);
  manifests and `SHA256SUMS` carry it.
- Run `kvlite module verify <name>` against the staged tree after
  installing; `install.sh` already does this unless a driver package lands
  separately, in which case the last package installed must verify the
  assembled tree.

## Stamping recipes for a release

The recipes in `packaging/homebrew` and `packaging/debian` pin a version, so
they ship a placeholder (`0.1.0`, tag `v0.1.0`) until a packaged release
exists. Stamp them from the tagged commit before publishing:

```sh
make stamp-packaging RELEASE_VERSION=v0.2.0
# or: scripts/stamp-packaging.py v0.2.0 --revision "$(git rev-parse HEAD)"
```

That rewrites the tag and revision of every formula to one release and
prepends the matching Debian changelog entry; it edits nothing outside
`packaging/`, and re-running is a no-op. The release workflow runs
`scripts/stamp-packaging.py "$version" --check` so a release cannot ship
recipes still pointing at the placeholder. Every driver, protocol, and host
bundle that `packaging/debian/rules` builds must have a Debian package, a
Homebrew formula, and a row in the table above; `make test-packaging` covers
that agreement together with the Windows dependency-listing and checksum
formats, which are simulated there because the unix jobs never see them.

## Bring your own engine (composability)

KVLite ships shims, not engines — except where the engine is pure Go and
there is nothing else to ship. Each driver says exactly what it needs:

| Driver | Engine lives in | User installs |
| --- | --- | --- |
| `memory` | core itself | nothing |
| `leveldb` | the bundle (pure Go) | nothing, ever |
| `badgerdb` | the bundle (pure Go) | nothing beyond the bundle |
| `boltdb` | the bundle (pure Go, using bbolt) | nothing beyond the bundle |
| `lmdb` | the bundle (CGo, statically linked LMDB C source) | nothing beyond the bundle; include both bundled license notices |
| `rocksdb` | system **or** bundle, your choice | distro `rocksdb` **or** a `--bundle-runtime` package |
| `berkeleydb` | system only, always | a Berkeley DB you are entitled to use |

- **LevelDB is not composable and never will be**: engine and shim are the
  same compiled Go. There is no standalone LevelDB to bind.
- **RocksDB composes through dynamic linking.** A non-bundled build links
  `librocksdb.so` normally, and whatever provides a compatible library at
  runtime works. Compatibility is the shared-library SONAME major:
  KVLite supports `librocksdb.so.10` (`v10.8.3`–`v10.10.1` tested); the
  linker itself refuses anything else. A Debian package therefore depends
  on the distro RocksDB (e.g. `Depends: librocksdbX`), a Homebrew formula
  on `depends_on "rocksdb"`, and only LZ4 support is actually required at
  runtime. Prefer the `--bundle-runtime` package wherever the system
  library cannot be pinned.
- **Berkeley DB composes through its stable C API.** The adapter checks the
  API surface at compile time (`DB_VERSION_MAJOR`, Btree flags), not one
  exact release, and bundles no Oracle code — so an adapter-only shim
  bundle is publishable with the explicit license gate, while Oracle's
  library itself is never redistributed by this project. Mainstream
  managers barely carry modern Berkeley DB (Debian has only the ancient
  db5.3; Homebrew's is AGPL-gated), so in practice the owner builds or
  installs Oracle Berkeley DB 18.x themselves and points `BERKELEYDB_CFLAGS`
  / `BERKELEYDB_LDFLAGS` at it. See `extensions/berkeleydb/README.md`.

## Windows (MinGW-w64 tarballs, like SQLite)

Windows gets no shell installer and no system package from this project —
mirroring SQLite, which ships per-arch DLL and tools archives with published
checksums and no installer. The Windows story for KVLite is the same shape:

- Builds run in MSYS2/MINGW64 on a native Windows runner (Go 1.23+); see the
  `native-artifacts-windows` job in
  [`release-artifacts.yml`](../.github/workflows/release-artifacts.yml). They
  cannot use MSVC: cgo on Windows only drives a gcc-compatible compiler, and
  a library built by MSVC cannot be linked into a cgo binary — so the
  toolchain is MinGW-w64 for the Go driver packages and for anything C they
  link.
- The job builds `make release DRIVER=leveldb|badgerdb|boltdb|lmdb`, the
  driverless host bundle (`DRIVER=none`: CLI, `kvlite.dll`, `kvlite.h`), and
  `make release-http`/`release-redis`, then `make dist-tarballs`. Windows
  publishes the same `kvlite-VERSION-windows-amd64.tar.gz` plus `.sha256`
  sidecar as every other target — one archive format and one installer story
  across all five targets, rather than a separate zip convention.
- **RocksDB has no Windows bundle yet.** The pinned `grocksdb` links `-ldl`
  in its cgo flags, and MinGW-w64 ships no `libdl`, so its default link line
  cannot build for Windows. A Windows RocksDB bundle needs the
  `grocksdb_no_link` build tag with an explicit `CGO_LDFLAGS` plus the C++
  runtime and compression DLLs bundled beside the executables. Until that
  lands, the Windows job stays deliberately RocksDB-free and a Windows
  install offers the four other engines.
- Native dependencies need no path surgery: Windows looks in the
  application's own folder first, so `--bundle-runtime` copies DLLs beside
  the `.exe` files (`dumpbin` or `objdump` lists them). Users add one folder
  (`bin/`) to `PATH` and everything — including `kvlite.dll` loaded by
  language bindings — resolves. The mechanism is exercised on Windows by
  `make test-bundle-runtime`; `dumpbin -dependents` on the outputs remains
  the manual check. No Windows bundle needs it yet, because the four shipped
  drivers link their C statically.
- The Windows job verifies bundle layout, module descriptors, and that every
  built executable starts (a missing runtime DLL fails there, not in a user's
  shell). It does not yet point the Go binding at a built Windows bundle the
  way the Linux/macOS jobs do through `KVLITE_TEST_NATIVE_LIBRARY`: loading a
  Go-built driver module into a Go process is only proven on Linux and macOS
  ARM. The binding's `LoadLibrary` path is covered instead by the
  `native-binding-windows` job in [`test.yml`](../.github/workflows/test.yml)
  against an ABI-compatible C mock.
- Install location: `%ProgramFiles%\KVLite` (per-machine) or
  `%LocalAppData%\KVLite` (per-user), with `KVLITE_SYSTEM_MODULE_PATH`
  pointing at its `lib\kvlite` catalog. `scripts/install.sh` runs under Git
  Bash with `--target windows-amd64`; symlinks fall back to copies where
  privileges are missing.
- Community managers (winget, Chocolatey, Scoop) stay community efforts, as
  with SQLite — the release tarballs plus checksums are the complete input
  they need.

## Obligations that stay with the publisher

- **Notices.** `--bundle-runtime` refuses to build without `--notice-file`
  entries. Collect them with `scripts/collect-runtime-notices.sh` and review
  the set before publishing; distro compression-library texts in particular
  need a human pass per release.
- **Provenance.** Manifest checksums prove integrity (nothing runs or loads
  before `verify` passes), not provenance. The manager's own signatures
  cover transport; repository-level release signing is still open.
- **RocksDB range.** CI builds the adapter against both ends of the
  supported `v10.8.3`–`v10.10.1` range on every change; stay within that
  window, or extend it and re-run `make test-rocksdb-compat` locally first.
  The next 10.x line, `v11.x`, changes the shared-library SONAME major, so it
  is a packaging change rather than a range bump.
