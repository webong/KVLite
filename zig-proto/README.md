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
- Does NOT implement raw_* ops, scans, batches, collections, codecs, or module loading
- Does NOT claim on-disk compatibility with Go host
- These are gaps to close toward a production-ready Zig host, not permanent scope exclusions
