# KVLite for Go

The repository-root `github.com/webong/kvlite` package is the **Go binding**,
not the KVLite engine. It dynamically loads the installed driverless host
`libkvlite` and lets that host load the selected engine bundle
through KVLite's versioned C ABI. It has no dependency on the unpublished
`src/` implementation module or any storage-engine Go module.
Go is one client language; KVLite's implementation is distributed separately.

Once a versioned release is published from this repository:

```bash
go get github.com/webong/kvlite@v0.1.0
```

Install the KVLite host and a matching native driver bundle first, or set
`KVLITE_LIBRARY_PATH` to a `libkvlite.so` (Linux) or `libkvlite.dylib`
(macOS). `KVLITE_HOME` and `KVLITE_SYSTEM_MODULE_PATH` use the same installed
catalog as the other language bindings. The native bundles, not this Go module,
contains the storage implementation and any engine dependencies.

```go
import (
    "time"

    kvlite "github.com/webong/kvlite"
)

db, err := kvlite.Open("./app-data", kvlite.WithDriver("rocksdb"))
if err != nil { /* handle error */ }
defer db.Close()

err = db.Put("user:101", map[string]any{"name": "Ada"}, time.Hour)
var user struct { Name string `json:"name"` }
err = db.Get("user:101", &user)
```

`PutBytes`, `GetBytes`, and `DeleteBytes` are also available for application-owned
serialization. `errors.Is(err, kvlite.ErrNotFound)` detects a missing key.
Only one process can own a database directory; use a KVLite transport extension
when several processes need it.

This first embedded Go binding supports cgo on Linux and Apple Silicon macOS.
Intel macOS cannot safely load the Go-built C ABI into another Go runtime, so
`Open` fails with an actionable error there; use the KVLite CLI or an installed
HTTP/Redis transport. Other targets also need a tested native loader. The mock ABI
tests run from the repository root without RocksDB or the core source:

```bash
GOWORK=off go test ./...
```

For a **no-import** Go integration, call the installed `kvlite` CLI as a
separate process or use a standard Redis client against KVLite's optional
Redis server. An idiomatic in-process typed Go interface necessarily imports
this thin binding package, but never imports KVLite's engine source.
