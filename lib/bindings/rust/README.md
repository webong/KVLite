# `usekvlite` for Rust (`use kvlite`)

The Rust crate is a small, safe wrapper over the stable KVLite C ABI. It
dynamically loads `libkvlite`, so it behaves like a local embedded database
library without linking your application directly to RocksDB.

Use it when one process owns the database directory. For workers or several
applications, run `kvlite serve` and use KVLite's JSON/HTTP OpenAPI contract or
its optional Redis endpoint instead.

## Install

The crates.io name `kvlite` already belongs to an unrelated project. This
binding's selected package name is `usekvlite`, but it is not yet published;
do not use `cargo add kvlite` for this KVLite. Its library crate remains
`kvlite`, so Rust source can continue using `use kvlite`. See the
[publishing plan](../../../packaging/PUBLISHING-RESEARCH.md).

Until publication, use the public Git repository with the Rust package
directory:

```toml
[dependencies]
usekvlite = { git = "https://github.com/webong/KVlite" }
```

## Embedded use

Install a matching driver bundle first. The crate finds the CLI's driver
catalog at standard prefixes, or from `KVLITE_SYSTEM_MODULE_PATH` for a custom
prefix. `KVLITE_LIBRARY_PATH` remains an exact-path override:

```bash
export KVLITE_LIBRARY_PATH=/opt/kvlite/lib/libkvlite.dylib # .so on Linux
```

```rust
use std::time::Duration;
use kvlite::Database;
use serde::{Deserialize, Serialize};

#[derive(Debug, Serialize, Deserialize)]
struct User { id: u64, name: String }

let mut db = Database::open_with_driver("./data", "leveldb")?;
db.put("user:101", &User { id: 101, name: "Ada".into() }, Some(Duration::from_secs(3600)))?;
let user: User = db.get("user:101")?;
db.close()?;
# Ok::<(), kvlite::Error>(())
```

Use `put_bytes()` and `get_bytes()` when the application owns a MessagePack,
protobuf, or other binary codec.

`Database::open()` and `Database::open_with_library()` use the native bundle's
default driver. Use `open_with_driver()` or `open_with_library_and_driver()`
for `leveldb`; the older
`*_with_backend()` names remain available for source compatibility. Explicit
selection needs a current `libkvlite` with `kvlite_open_with_driver` (or its
compatible `kvlite_open_with_backend` alias).

For remote Rust clients, generate a client from KVLite's OpenAPI document (or
use any HTTP/Redis client). Send `X-KVLite-Driver: leveldb` on HTTP requests;
the server accepts it only for an installed, server-owned driver/path mapping.

## Test

```bash
cargo test
```

The test compiles a small ABI-compatible C mock and exercises real dynamic
loading; RocksDB itself is not required for the binding test.
