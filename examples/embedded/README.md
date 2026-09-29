# Embedded KVLite in six languages

These examples open a KVLite database **inside the application process**. No
HTTP or Redis server is started. Each process must be the sole owner of its
database directory; run the examples one at a time. The language package is a
thin binding, while the installed native bundle supplies the storage engine.

From the repository root, build a local LevelDB bundle and make it discoverable
to every binding:

```bash
make release-c-shared RELEASE_VERSION=dev DRIVER=leveldb
export KVLITE_HOME="$(pwd)/dist/dev/$(go env GOHOSTOS)-$(go env GOHOSTARCH)"
```

Run the following commands from `examples/embedded`. Each program defaults to
its own `data` directory. Override `KVLITE_DB_PATH` to choose another path and
`KVLITE_DRIVER` to use another installed engine (for example, `rocksdb`). A
database directory opened with one driver must not be reopened with another.
The `KVLITE_HOME` setting above is for this source build; an installed release
bundle is discovered automatically, or through `KVLITE_SYSTEM_MODULE_PATH`.

| Language | Run from `examples/embedded` | Binding |
| --- | --- | --- |
| Go | `cd go && GOWORK=off go run .` | Repository-root Go module |
| PHP | `cd php && composer install && php -d ffi.enable=1 app.php` | Local Composer path package |
| Python | `PYTHONPATH=../../lib/bindings/python/src python3 python/app.py` | `usekvlite` source package |
| JavaScript | `cd node && npm install && node app.mjs` | Local npm package and N-API addon |
| Ruby | `ruby -I../../lib/bindings/ruby/lib ruby/app.rb` | Ruby gem source |
| Rust | `cd rust && cargo run` | Local Cargo path crate |

The Node install builds its native addon and needs Node headers, a C compiler,
Python, and `node-gyp`. PHP needs its FFI extension enabled. Go needs cgo;
embedded Go on Intel macOS is not supported because of the two Go runtimes.
The local LevelDB build needs a Go and C toolchain. Package registries are not
yet published, so these examples use source-checkout dependencies; the sample
code keeps the same public imports it will use with installed packages.

All six samples write a JSON user with a one-hour TTL, read it, then round-trip
raw bytes under a separate key. They print the read values and close the
embedded handle. The TTL is enforced at read time; it is not a promise of
immediate physical deletion. For several processes or workers sharing one
database directory, use an optional HTTP/Redis transport instead of opening
the directory in each process.
