# KVLite

KVLite is an embedded key-value database for Go, PHP, Python, JavaScript,
Ruby, and Rust. Pick an engine such as LevelDB or RocksDB; HTTP and Redis are
optional extensions.

## Try the canary

Preview assets are available for Linux x86-64 and macOS (Intel or Apple Silicon). Install
the native host and LevelDB engine, then the Python binding:

```sh
curl -fsSL https://github.com/webong/KVLite/releases/download/v0.1.0-canary.3658155118701/kvlite-installer.sh | bash -s -- --driver leveldb --prefix "$HOME/.local" --yes
export KVLITE_SYSTEM_MODULE_PATH="$HOME/.local/lib/kvlite"
python3 -m pip install 'https://github.com/webong/KVLite/releases/download/v0.1.0-canary.3658155118701/usekvlite-0.1.0.dev3658155118701-py3-none-any.whl'
```

```python
import kvlite

with kvlite.open("./app-data", driver="leveldb") as db:
    db.put("user:101", {"name": "Ada"})
    print(db.get("user:101"))  # {'name': 'Ada'}
```

The native engine bundle is required for embedded use; a language binding
alone is not a database. Only one process can own a database directory at a
time.

Using another language? See the [Go](lib/bindings/go/README.md),
[PHP](lib/bindings/php/README.md), [JavaScript](lib/bindings/node/README.md),
[Ruby](lib/bindings/ruby/README.md), or [Rust](lib/bindings/rust/README.md)
binding guide. Registry publication is still in progress; use the
[canary release](https://github.com/webong/KVLite/releases/tag/v0.1.0-canary.3658155118701)
for preview artifacts. For builds and architecture, see [DEVELOPERS.md](DEVELOPERS.md).
