# Zig prototype benchmark review

**Date:** 2026-11-05
**Status:** Preliminary; no runtime migration decision

The first `RESULTS.md` report concluded that Go was decisively faster and that the Zig port should stop. That conclusion is not supported by a like-for-like comparison. In particular, the public Go binding calls `C.bridge_put`/`C.bridge_get` in `native_dynamic.go`; it does not bypass the C ABI. The reported ~4,670 Go-binding puts/sec has no corresponding raw row in that report, and its estimated nanosecond cost breakdowns were not measured. Direct Go-core, direct LMDB, Go-binding, and Zig-host numbers exercise different paths and cannot be attributed solely to implementation language.

## Controlled check

On an Apple M3 Pro running macOS, a fresh Go/LMDB C-shared bundle was built from the current checkout with `scripts/build-driver-artifact.sh`. The existing Zig library and the fresh Go bundle were then driven by the **same** `zig-proto/bench/cbench.c` C ABI harness, with LMDB, 64-byte values, one thread, and default synchronous durability. Every run used a new temporary database.

| Workload | Zig host | Fresh Go host | Interpretation |
| --- | ---: | ---: | --- |
| 300 puts | 336 ops/sec | 341 ops/sec | Effectively tied in this run; repeat 300-put checks ranged 336–356 for Zig and 341–342 for Go. |
| 100,000 cached gets from 300 keys | 974,023 ops/sec | 567,746 ops/sec | Zig was about 1.7× faster for this small, read-heavy subset. |

The write result is dominated by synchronous LMDB commits; a small throughput difference is not evidence of a faster host language. The read result is a useful signal, but the Zig prototype implements only a subset of the C ABI and KVLite semantics. It does not yet prove that a complete Zig runtime would retain this advantage.

To reproduce this check, first build the Zig library and C harness as described in `README.md`, then from the repository root:

```sh
review_dir=$(mktemp -d /tmp/kvlite-go-lmdb-review.XXXXXX)
bash scripts/build-driver-artifact.sh lmdb c-shared "$review_dir/libkvlite.dylib"
./zig-proto/bin/cbench ./zig-proto/zig-out/lib/libkvlite.dylib run --workload put --ops 300 --threads 1 --value 64 --label zig
./zig-proto/bin/cbench "$review_dir/libkvlite.dylib" run --workload put --ops 300 --threads 1 --value 64 --label go
./zig-proto/bin/cbench ./zig-proto/zig-out/lib/libkvlite.dylib run --workload get --ops 100000 --keys 300 --threads 1 --value 64 --label zig
./zig-proto/bin/cbench "$review_dir/libkvlite.dylib" run --workload get --ops 100000 --keys 300 --threads 1 --value 64 --label go
```

## Decision

**Continue the Zig prototype, but do not begin a full port yet.** Its smaller library and faster cached-read result are worth investigating. The next milestone is repeatable paired measurements across read, write, mixed, concurrent, and larger-value workloads, on more than one platform, with equivalent durability and increasingly matched KVLite semantics. Include the same language-binding path on both hosts, record raw output and build flags, and distinguish engine, host, and binding costs. Only then decide whether an incremental port earns its maintenance and compatibility cost. Until that decision, the Go host remains the production implementation and the ADR remains proposed.
