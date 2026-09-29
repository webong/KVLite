# KVLite

KVLite is an embedded key-value database for Go, PHP, Python, JavaScript,
Ruby, and Rust. Your application opens a local database and chooses an engine
such as LevelDB or RocksDB. HTTP and Redis are optional extensions, not
required services.

> **Release status:** The language bindings are implemented but not yet
> published to package registries. In particular, the existing `kvlite`
> packages on npm and crates.io belong to unrelated projects. Do not install
> them expecting this KVLite. Our selected npm and Rust package name is
> `usekvlite`; we'll add copy-and-paste installation commands here after
> the first release is verified.

## What using KVLite looks like

For example, in Python:

```python
import kvlite

with kvlite.open("./app-data", driver="leveldb") as db:
    db.put("user:101", {"name": "Ada"}, ttl_seconds=3600)
    print(db.get("user:101"))
```

The same embedded workflow is implemented in every binding:

| Language | Runnable example | Binding |
| --- | --- | --- |
| Go | [example](examples/embedded/go/main.go) | [Go binding](lib/bindings/go/README.md) |
| PHP | [example](examples/embedded/php/app.php) | [PHP binding](lib/bindings/php/README.md) |
| Python | [example](examples/embedded/python/app.py) | [Python binding](lib/bindings/python/README.md) |
| JavaScript | [example](examples/embedded/node/app.mjs) | [Node binding](lib/bindings/node/README.md) |
| Ruby | [example](examples/embedded/ruby/app.rb) | [Ruby binding](lib/bindings/ruby/README.md) |
| Rust | [example](examples/embedded/rust/src/main.rs) | [Rust binding](lib/bindings/rust/README.md) |

Each binding uses KVLite's native library. Embedded use also needs an
installed engine bundle; the language package alone is not the database.
One process owns a database directory at a time.

See the [developer guide](DEVELOPERS.md) for building from source, extension
and server setup, and development tests. The
[publishing plan](packaging/PUBLISHING-RESEARCH.md) tracks what is needed
before package-manager installation can become the primary quickstart.
