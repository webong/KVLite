# KVLite for Ruby

`webong-kvlite` is a thin Ruby gem over KVLite's installed runtime. It does not
build or import the Go engine. `KVLite.open` loads an installed `libkvlite`
driver bundle through ABI v1 using Ruby's Fiddle; `KVLite.connect` uses the
optional JSON/HTTP server with Ruby's standard library.

This gem is prepared for RubyGems release but is not published yet. After its
first release, installation will be:

```bash
gem install webong-kvlite
```

For embedded use, install a matching KVLite native driver bundle first. Its
library is found through `KVLITE_LIBRARY_PATH`, `KVLITE_HOME`,
`KVLITE_SYSTEM_MODULE_PATH`, or standard user/system catalogs. You may also
pass an explicit `library_path:` to `open`.

```ruby
require "kvlite"

db = KVLite.open("./data", driver: "leveldb")
db.put("user:101", { name: "Ada" }, ttl_seconds: 3600)
p db.get("user:101")  # => {"name"=>"Ada"}
db.close
```

`put_bytes`, `get_bytes`, and `delete_bytes` accept binary Ruby strings for a
caller-owned codec. Missing keys raise `KVLite::NotFoundError`. One process
must own an embedded database directory. For a shared directory, enable the
optional HTTP server extension and connect to it:

```ruby
db = KVLite.connect("http://127.0.0.1:8089", token: ENV["KVLITE_TOKEN"], driver: "leveldb")
db.put("user:101", { name: "Ada" })
```

The gem includes no RocksDB binaries and no HTTP/Redis listener. Remote
clients can select only a driver that the server has installed and exposed.
Embedded loading is tested on Linux and macOS; other platforms require native
loader testing before being declared supported.

Run the source tests with `ruby -Ilib -e 'Dir["test/test_*.rb"].sort.each { |f| require File.expand_path(f) }'`.
