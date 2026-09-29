# KVLite examples

- [Embedded in six languages](embedded/README.md): Go, PHP, Python, JavaScript,
  Ruby, and Rust open a local engine bundle through the native C ABI. This is
  the default SQLite-like use case; no server is involved.
- [`basic/`](basic/): source-level Go implementation example, built with the
  RocksDB tag. This is for core/driver development, not the published Go
  binding API.
- [`python/client.py`](python/client.py) and [`node/client.mjs`](node/client.mjs):
  dependency-free clients for the optional HTTP transport. These require a
  running KVLite server and do not open a database in the client process.
