# KVLite language bindings

These are real, thin packages over KVLite's public boundaries—not separate
database implementations. Every embedded binding uses ABI version 1 from
[`../../capi/kvlite.h`](../../capi/kvlite.h), calls `kvlite_abi_version()` on
load, and resolves a matching library in this order:

1. an explicit library path;
2. `KVLITE_LIBRARY_PATH`;
3. the selected driver (or sole installed bundle) in `KVLITE_HOME`, each
   `KVLITE_SYSTEM_MODULE_PATH` catalog, and standard user/system catalogs
   such as `~/.local/lib/kvlite` and `/usr/local/lib/kvlite`; then
4. for PHP/Python/Node/Rust, a matching `native/<os>-<arch>` package asset or
   local `dist/dev` driver bundle.

The same installed driver catalog serves the CLI and all six embedded
bindings. A default-prefix online installation needs no per-language library
path; a custom prefix uses its `lib/kvlite` catalog in
`KVLITE_SYSTEM_MODULE_PATH`.

| Directory | Package name | Local API | Remote API | Binding test |
| --- | --- | --- | --- | --- |
| [repository root](../../) | `github.com/webong/kvlite` | cgo dynamic C ABI | CLI or optional transport | `GOWORK=off go test ./...` at the root |
| [`php/`](php/) | `kvlite/kvlite` (`KVLite\KVLite`) | PHP FFI | JSON/HTTP | `composer --working-dir=lib/bindings/php test` |
| [`python/`](python/) | `usekvlite` (`import kvlite`) | `ctypes` | JSON/HTTP | `bash lib/bindings/python/tests/run.sh` |
| [`node/`](node/) | `kvlite` | N-API loader | JSON/HTTP | `npm --prefix lib/bindings/node test` |
| [`ruby/`](ruby/) | `kvlite` (`require "kvlite"`) | Fiddle C ABI | JSON/HTTP | `ruby -Ilib -e 'Dir["test/test_*.rb"].sort.each { |f| require File.expand_path(f) }'` inside `ruby/` |
| [`rust/`](rust/) | `kvlite` | `libloading` | OpenAPI/Redis boundary | `cargo test --manifest-path lib/bindings/rust/Cargo.toml` |

Use `open()` only when one process owns the selected local driver directory.
Embedded `open()` APIs accept an optional driver name such as `leveldb`; when
omitted, they use the native bundle's default driver (RocksDB for a RocksDB
bundle, LevelDB for a LevelDB-only bundle). For PHP-FPM, Node clusters, worker
fleets, or multiple applications, run `kvlite serve` and use the
package's `connect()` API, an OpenAPI-generated client, or a standard Redis
client against KVLite's optional Redis endpoint. HTTP `connect()` clients may
select a driver name; the server honours it only when it has an installed,
server-owned driver/path mapping.

The HTTP and Redis services are explicit server extensions linked by `kvlite
serve`; the embedded C ABI used by `open()` does not include or start either
listener.

The wrappers serialize normal values as JSON and each native wrapper also has a
raw byte API for applications that choose MessagePack, protobuf, or another
codec. Packages are source-ready for a dedicated Go module, Composer, PyPI,
npm, RubyGems, and crates.io, but are not yet published. Release CI builds a
self-contained RocksDB runtime bundle on Linux and macOS; real release assets
and registry publication still need validation. LevelDB is pure
Go inside KVLite, but its embedded selection still requires a current
driver bundle exporting `kvlite_open_with_driver` (or the ABI-compatible
`kvlite_open_with_backend` alias).

## Release CI

The pull-request binding workflow tests all six packages, stages source-only
release trees, and checks the Go module, Composer, wheel/sdist, npm tarball, Ruby gem, and crate
packages. On a stable `vX.Y.Z` tag, `release-artifacts.yml` first requires
the native Linux/macOS artifact matrix to pass. It retests the bindings,
builds matching packages from a temporary copy, and uploads package artifacts
before verifying all three native tarballs and creating one GitHub Release.
Registry jobs run only after that release is created. Existing releases are
never replaced, and the tagged source tree is never changed to stamp versions.

Non-Go registry publication is separately opt-in. Those variables default to
disabled; enable one only after its destination and protected GitHub
environment are ready. The Go binding follows this repository's release tag
directly:

| Destination | Repository variable | One-time setup |
| --- | --- | --- |
| Go `github.com/webong/kvlite` | tagged repository root | No split repository or publishing token. The root module contains only the thin Go binding; `src/` is a separate unpublished module. Tag this repository after release checks pass so the Go module proxy can index it. |
| PyPI `usekvlite` | `KVLITE_PUBLISH_PYPI=true` | Configure a PyPI trusted publisher for `webong/KVlite`, workflow `release-artifacts.yml`, environment `pypi`. |
| npm `kvlite` | `KVLITE_PUBLISH_NPM=true` | Verify ownership of the unscoped name and configure its npm trusted publisher for the same workflow and environment `npm`. |
| RubyGems `kvlite` | `KVLITE_PUBLISH_RUBY=true` | Verify ownership of the gem name, configure a pending RubyGems trusted publisher for `webong/KVlite`, workflow `release-artifacts.yml`, environment `rubygems`, then protect that environment. RubyGems uses OIDC; no registry token is stored. |
| crates.io `kvlite` | `KVLITE_PUBLISH_CRATES=true` | Confirm ownership of the crate name, bootstrap its first release manually, then configure crates.io trusted publishing for environment `crates-io`. |
| Packagist `kvlite/kvlite` | `KVLITE_PUBLISH_PHP=true` | Verify name ownership, create an empty dedicated PHP repository, set `KVLITE_PHP_SPLIT_REPO=webong/<repo>` and a repository-scoped `KVLITE_PHP_SPLIT_TOKEN` secret, then register that repository on Packagist with its GitHub update hook. The GitHub owner need not match the Composer vendor. |

Registry setup references: [PyPI trusted publishers](https://docs.pypi.org/trusted-publishers/),
[npm trusted publishers](https://docs.npmjs.com/trusted-publishers/),
[RubyGems trusted publishing](https://guides.rubygems.org/trusted-publishing/),
[crates.io trusted publishing](https://crates.io/docs/trusted-publishing), and
[Packagist package registration](https://packagist.org/about).

The Go binding has its module metadata at this repository root. Packagist
requires a separate repository root for its metadata, so the PHP job
mirrors the `lib/bindings/php` Git subtree to the dedicated repository's
`main` branch and matching tag. The push is non-forced and fails rather than
replacing a prior release. The other jobs use OIDC instead of registry tokens.
Protect `v*` tags and require reviewers on the publishing environments
before enabling the variables. Registry releases cannot be rolled back as one
transaction: if one job fails after another succeeds, resolve it at that
registry and do not reuse the same version blindly.

The public import/namespace is KVLite in every language. The Python install
name remains `usekvlite` because the bare `kvlite` PyPI project belongs to
someone else. Go modules require a repository locator, so the root Go
binding uses `github.com/webong/kvlite` while its package identifier is
`kvlite`. Composer requires a vendor/package pair; `kvlite/kvlite` uses a
product vendor rather than the repository owner's name. RubyGems, npm,
Packagist, and crates.io names and registry account ownership must be checked
before the first publish; building an artifact does not reserve a name. The
native driver bundle stays a separate install for embedded use.
