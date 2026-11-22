# Zig runtime candidate — Build & Run

This is the first slice of a production-quality Zig runtime candidate under
[ADR 0001](../docs/adr/0001-evaluate-zig-embedded-runtime.md). It currently
implements only a C ABI v1 subset behind `capi/kvlite.h` with vendored LMDB
0.9.35. Build and test it to production standards, but do not deploy or release
it as a complete KVLite host yet.

## Prerequisites

- **Zig 0.17.1+** (not Homebrew; use the official tarball):
  ```bash
  mkdir -p ~/.local/opt
  cd ~/.local/opt
  curl -sL -o zig-0.17.1.tar.xz https://ziglang.org/download/0.17.1/zig-aarch64-macos-0.17.1.tar.xz
  tar xf zig-0.17.1.tar.xz
  rm zig-0.17.1.tar.xz
  export PATH="$HOME/.local/opt/zig-aarch64-macos-0.17.1:$PATH"
  ```

## Build the Library

```bash
cd zig-proto
zig build -Doptimize=ReleaseFast    # → zig-out/lib/libkvlite.dylib
# or
zig build -Doptimize=ReleaseSafe
# or
zig build -Doptimize=Debug
```

The library exports the ABI v1 subset:
```
kvlite_abi_version  kvlite_open  kvlite_open_with_driver
kvlite_open_with_backend  kvlite_close  kvlite_put
kvlite_get  kvlite_delete  kvlite_free
kvlite_raw_put  kvlite_raw_get  kvlite_raw_delete
kvlite_raw_apply  kvlite_raw_multi_put  kvlite_raw_multi_get
kvlite_raw_scan_open  kvlite_raw_scan_next  kvlite_raw_scan_close
kvlite_plugin_load  kvlite_plugin_call  kvlite_plugin_close
```

## Ext Plugin System

KVLite loads plugins through [ext](https://github.com/webong/ext)'s host
infrastructure (`github.com/webong/ext/pkg/ext-zig`, released as `ext-zig-v0.2.0`).
A plugin is a C shared library exporting the [ext C ABI](lib/plugin/include/ext_plugin.h)
(`ext_plugin_abi_version`, `ext_plugin_open`, `ext_plugin_call`, `ext_plugin_close`).
KVLite loads it via `ext.host.CShared` and calls into it through `ext.host.Session`.

KVLite's wire protocol rides inside `ext_plugin_call`'s JSON request/response:

```
Request:  {"operation":"get","payload":{"key":"base64","collection":"users"}}
Response: {"value":"base64","found":true}
          or {"error":{"code":"not_found","message":"..."}}
```

Operations: `get`, `put`, `delete`, `scan`, `multi_get`, `multi_put`, `health`.

KVLite keeps its own C ABI contract (`capi/kvlite.h`) as the public surface.
ext is the plugin *loading* layer.

### Build an Example Plugin

```bash
# Minimal example plugin (in-memory KV store)
gcc -dynamiclib -o test_plugin.dylib test_plugin.c
```

### Load and Call a Plugin

```bash
# From C:
kvlite_plugin_load("/abs/path/to/plugin.dylib", &handle, &error);
kvlite_plugin_call(handle, "health", "{}", &resp, &resp_len, &error);
kvlite_plugin_close(handle, &error);

# From the test host:
gcc -o test_host test_host.c -Lkvlite -lkvlite -ldl
DYLD_LIBRARY_PATH=kvlite ./test_host kvlite/libkvlite.dylib /abs/path/to/plugin.dylib
```

## Build Benchmark Tools

```bash
# C ABI harness (works with ANY libkvlite: Zig or Go)
cc -O2 -o bin/cbench bench/cbench.c

# Direct LMDB baseline (no C ABI host)
cc -O2 -Ithird_party/lmdb -o bin/lmbench \
  bench/lmbench.c third_party/lmdb/mdb.c third_party/lmdb/midl.c

# Go core baseline (in-process, no C ABI)
cd bench/gocore && GOWORK=off go build -o ../../bin/gocore .
```

## Run Conformance Tests

```bash
# C ABI conformance (36 checks)
./bin/cbench zig-out/lib/libkvlite.dylib conformance

# Go binding conformance (5 tests)
cd ..  # repo root
KVLITE_ZIG_LIBRARY=zig-proto/zig-out/lib/libkvlite.dylib \
  go test -run TestZigHost -v .
```

## Run Benchmarks

```bash
# Startup (25 rounds)
./bin/cbench zig-out/lib/libkvlite.dylib startup --rounds 25 --label zig-proto

# Workload matrix: put/get/rget/getmiss/del × 64B/4KiB × 1T/4T
for w in put get rget getmiss del; do
  for v in 64 4096; do
    for t in 1 4; do
      ./bin/cbench zig-out/lib/libkvlite.dylib run \
        --workload $w --ops 1000 --threads $t --value $v --label zig-proto
    done
  done
done

# Direct LMDB baseline (same workloads)
for w in put get rget getmiss del; do
  for v in 64 4096; do
    for t in 1 4; do
      ./bin/lmbench --workload $w --ops 1000 --threads $t --value $v --label direct-lmdb
    done
  done
done

# Go core baseline (in-process, no C ABI)
for w in put get rget del; do
  for v in 64 4096; do
    GOWORK=off ./bin/gocore --workload $w --ops 1000 --value $v --label gocore
  done
done

# Binding mutex impact (1 vs 4 CPU)
KVLITE_BENCH_LIBRARY=zig-proto/zig-out/lib/libkvlite.dylib \
  KVLITE_BENCH_LABEL=zig-proto \
  go test -run '^$' -bench 'BenchmarkBindingPut' -cpu 1,4 -benchtime 2s .
```

## Output Format

All tools emit CSV lines prefixed by record type:

```
info,library=...,abi_version=1
bench,label=...,workload=put,threads=1,value=64,ops=1000,seconds=7.5,ops_per_sec=133,mean_us=7520,p50_us=4099,p99_us=38583
startup,label=...,kind=cold_open_put_get_del_close,rounds=25,median_us=7438,min_us=4475,max_us=7970
check,name=...,result=ok
conformance,total=36,passed=36,result=PASS
```

## Results

See [BENCHMARK_REVIEW.md](BENCHMARK_REVIEW.md) for the corrected interpretation;
`RESULTS.md` retains the original measurements and superseded analysis.

## Notes

- Uses isolated temporary directories (`/tmp/kvlite-zig-proto-XXXXXX`) for every run
- Writes its own manifest file (`KVLITE-ZIG-PROTO-MANIFEST.json`) to prevent accidental interop with Go host databases
- Implements raw_* ops, scans, multi ops, batch apply, and ext plugin loading
- Does NOT implement collections, codecs, or module loading
- Does NOT claim on-disk compatibility with Go host
- These are gaps to close toward a production-ready Zig host, not permanent scope exclusions
