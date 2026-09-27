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
4. a matching `native/<os>-<arch>` package asset or local `dist/dev` driver
   bundle.

The same installed driver catalog serves the CLI and all four embedded
bindings. A default-prefix online installation needs no per-language library
path; a custom prefix uses its `lib/kvlite` catalog in
`KVLITE_SYSTEM_MODULE_PATH`.

| Directory | Package name | Local API | Remote API | Binding test |
| --- | --- | --- | --- | --- |
| [`php/`](php/) | `webong/kvlite` | PHP FFI | JSON/HTTP | `composer --working-dir=lib/bindings/php test` |
| [`python/`](python/) | `kvlite` | `ctypes` | JSON/HTTP | `bash lib/bindings/python/tests/run.sh` |
| [`node/`](node/) | `@webong/kvlite` | N-API loader | JSON/HTTP | `npm --prefix lib/bindings/node test` |
| [`rust/`](rust/) | `kvlite` | `libloading` | OpenAPI/Redis boundary | `cargo test -p kvlite` |

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
codec. Packages are source-ready for Composer, PyPI, npm, and crates.io, but
are not yet claimed as published registry packages. Release CI builds a
self-contained RocksDB runtime bundle on Linux and macOS; real release assets
and registry publication still need validation. LevelDB is pure
Go inside KVLite, but its embedded selection still requires a current
driver bundle exporting `kvlite_open_with_driver` (or the ABI-compatible
`kvlite_open_with_backend` alias).
