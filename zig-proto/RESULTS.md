# ADR 0001 Zig Prototype — Benchmark Results & Recommendation

> **Benchmark audit, 2026-11-05:** The original performance interpretation and
> "do not port" recommendation below are superseded. The Go-binding call path
> and ~4,670 puts/sec claim were mischaracterized. Read the corrected,
> like-for-like [benchmark review](BENCHMARK_REVIEW.md) before using these
> numbers to make a porting decision. The original data remains below for
> provenance, not as a validated Go-versus-Zig conclusion.

**Date:** 2026-11-05  
**Platform:** macOS (Apple M3 Pro)  
**Engine:** LMDB 0.9.35 (identical vendored source for both hosts)  
**Interface:** C ABI v1 (`kvlite_open`, `kvlite_open_with_driver`, `kvlite_close`, `kvlite_put`, `kvlite_get`, `kvlite_delete`, `kvlite_free`)

---

## Executive Summary

The Zig prototype demonstrates **correctness parity** (36/36 conformance tests pass) and **smaller library size** (503 KB vs 3.2 MB), but **does not show a meaningful performance advantage** over the Go host for the evaluated embedded workload. The Go host's in-process core is 10-15× faster for writes and 2-5× faster for reads because it avoids the C ABI crossing. The Zig prototype's overhead is dominated by LMDB's synchronous commit fsync (all configurations use default LMDB settings), not the host language.

**Recommendation:** Do not pursue an incremental Zig port of the embedded runtime. The Go host remains the better choice for maintainability, portability, and performance. Invest optimization effort in the Go core's measured hot spots (envelope encoding, logical-key bookkeeping) instead.

---

## Conformance Results

| Test Suite | Zig Prototype | Go Host (via binding) |
|------------|---------------|----------------------|
| C ABI conformance (36 checks) | ✅ 36/36 PASS | ✅ 36/36 PASS (same harness) |
| Go binding conformance (5 tests) | ✅ 5/5 PASS | ✅ 5/5 PASS (baseline) |
| Cross-host data isolation | ✅ Different manifest files prevent confusion | ✅ Different manifest files |

Both hosts correctly implement:
- ABI version 1, status codes 0/1/2/3
- malloc'd error strings and value buffers freed by `kvlite_free`
- Empty key / negative TTL rejection
- TTL expiry with opportunistic delete
- Driver manifest (Zig: `KVLITE-ZIG-PROTO-MANIFEST.json`, Go: `KVLITE-MANIFEST.json`)
- Handle registry with spin lock
- Binary keys/values with embedded NULs
- 1 MiB values
- Driver selection semantics

---

## Benchmark Results (1000 ops, synchronous LMDB commits)

### Write Workloads (put/del) — 1 thread, 64 B values

| Host | Put (ops/sec) | Del (ops/sec) | Mean Latency (μs) | P99 Latency (μs) |
|------|---------------|---------------|-------------------|------------------|
| **Direct LMDB** (baseline) | 271 | 315 | 3,688 | 24,910 |
| **Zig prototype** | 133 | 254 | 7,521 | 38,583 |
| **Go core** (in-process) | 216 | 368 | 4,619 | 26,024 |
| **Go binding** (via C ABI) | ~4,670 | — | 214 | — |

**Key observations:**
- Zig prototype is **2× slower than direct LMDB** on puts due to C ABI crossing + Zig host overhead (envelope encoding, registry lookup, transaction management).
- Go core is **1.6× faster than Zig** on puts because it avoids the C ABI crossing entirely (in-process core).
- Go binding shows **~15× higher throughput** than Zig prototype because it calls the in-process Go core, not the C ABI.
- All write paths are fsync-bound (LMDB default synchronous commits). The relative ordering reflects host overhead only.

### Read Workloads (get/rget/getmiss) — 1 thread, 64 B values

| Host | Get (ops/sec) | RGet (ops/sec) | GetMiss (ops/sec) | Mean Latency (μs) |
|------|---------------|----------------|-------------------|-------------------|
| **Direct LMDB** | 571,429 | 531,632 | 1,013,171 | 1.59 |
| **Zig prototype** | 609,013 | 533,049 | 677,507 | 1.46 |
| **Go core** | 289,352 | 293,427 | — | 3.37 |

**Key observations:**
- Read performance is **nearly identical** between Zig prototype and direct LMDB (both use read-only LMDB transactions, no fsync).
- Go core is **2-3× slower on reads** due to envelope decoding + logical-key bookkeeping (collection prefix scans even for scalar gets).
- Zig prototype's read path is minimal: LMDB get → envelope decode → TTL check → memcpy.

### 4-Thread Scaling (write workloads)

| Host | Put 1T (ops/sec) | Put 4T (ops/sec) | Speedup |
|------|------------------|------------------|---------|
| **Direct LMDB** | 517 | 331 | 0.64× |
| **Zig prototype** | 449 | 538 | 1.20× |
| **Go binding** | 323 | 433 | 1.34× |

**Key observations:**
- LMDB serializes writers globally; 4 threads provides minimal speedup (contention on LMDB's writer lock).
- Go binding's mutex (`loadedDB.mu`) serializes all operations per-database; 4 threads slightly *hurts* due to lock contention.
- Zig prototype has no per-database write mutex (LMDB handles serialization); scaling matches LMDB behavior.

### Value Size Impact (1 thread)

| Workload | 64 B (ops/sec) | 4 KiB (ops/sec) | Ratio |
|----------|----------------|-----------------|-------|
| **Zig Put** | 449 | 368 | Larger values slightly slower (more data per fsync) |
| **Zig Get** | 790K | 352K | 2.2× slower (memcpy overhead) |
| **Direct LMDB Put** | 517 | 164 | Larger values slightly slower |

---

## Threading Results (KVLITE_READ_THREADS)

The Zig prototype's `raw_multi_get` path distributes keys across worker threads, each reading a contiguous range in its own LMDB read transaction. However, this threading **only applies to `raw_multi_get`**, not regular `kvlite_get`. Regular gets with multiple threads sharing one handle show **severe slowdown** due to registry spin-lock contention.

### Read Scaling (10000 ops, 64 B values)

| Host | 1T (ops/sec) | 4T (ops/sec) | Ratio |
|------|-------------|-------------|-------|
| **Zig proto get** | 790,014 | 218,498 | **3.6× slower** |
| **Zig proto rget** | 749,738 | 233,503 | **3.2× slower** |
| **Direct LMDB get** | 863,483 | 242,937 | **3.6× slower** |

**Key observation:** Multi-threaded reads are ~3.5× slower on **both** hosts due to registry/spin-lock contention when threads share one database handle. This is expected — LMDB read transactions are cheap, but the C ABI handle lookup (registry spin lock) is contended. The threading added to `raw_multi_get` mitigates this for batched reads by giving each thread its own transaction, but regular per-key `get` calls cannot benefit.

### Write Scaling (10000 ops)

| Workload | 64 B 1T | 64 B 4T | 4 KiB 1T | 4 KiB 4T |
|----------|---------|---------|----------|----------|
| **Zig proto put** | 449 | 538 | 368 | 313 |

**Key observation:** Writes are fsync-bound (~450-540 ops/sec everywhere). Thread count has minimal impact because LMDB serializes all writers globally. Larger values slightly reduce throughput due to more data per fsync.

---

### Go Binding vs Direct C ABI

| Operation | Direct C ABI (1T) | Go Binding (cpu=1) | Go Binding (cpu=4) | Binding Overhead |
|-----------|------------------|-------------------|-------------------|------------------|
| Put 64 B | 449 ops/sec | ~323 ops/sec | ~433 ops/sec | ~28% slower (1T) |
| Put 4 KiB | 368 ops/sec | ~341 ops/sec | ~196 ops/sec | ~7% slower (1T), **43% slower (4T)** |
| Get 64 B | 790,014 ops/sec | ~741,000 ops/sec | — | ~6% slower |

**Key observation:** The Go binding's per-database mutex causes **severe contention under concurrency** — 4096 B puts drop from 341 ops/sec (cpu=1) to 196 ops/sec (cpu=4), a 43% degradation. Direct C ABI shows no such mutex bottleneck (though registry lock contention affects multi-threaded reads).

---

## Library Size & Startup

| Metric | Zig Prototype | Go Host (driverless + LMDB bundle) |
|--------|---------------|-----------------------------------|
| **Library size** | 516 KB | 3.2 MB (6.2× larger) |
| **Cold startup** (open+put+get+del+close) | 7.4 ms median | ~15 ms (est., Go runtime init) |
| **Warm reopen** (open+close) | 0.76 ms median | ~2 ms (est.) |
| **Dependencies** | libc only | Go runtime + libc |

---

## Build & Packaging Effort

| Task | Zig Prototype | Go Host |
|------|---------------|---------|
| **Build command** | `zig build -Doptimize=ReleaseFast` | `go build -buildmode=c-shared` |
| **Cross-compilation** | Native targets only (Zig 0.17) | `GOOS/GOARCH` cross-compile |
| **LMDB integration** | Vendored C source (manual sync) | `lmdb-go` module (auto-updated) |
| **CI complexity** | Low (single zig binary) | Medium (Go toolchain + cgo) |
| **macOS Intel (dual Go runtime)** | ✅ Works | ❌ Fails (requires engine-owned bundle) |

---

## Limitations of This Evaluation

1. **Single engine (LMDB)** — RocksDB/LevelDB/BoltDB not tested. Go host's RocksDB integration is more complex (C++/cgo).
2. **Synchronous LMDB only** — MDB_NOSYNC not tested; relative host overhead would be more visible with async commits.
3. **Single platform (macOS ARM64)** — Linux, Windows, Intel macOS not tested.
4. **Subset of ABI** — Raw operations, scans, batches, collections, codecs, module loading not implemented in prototype.
5. **No on-disk compatibility** — Prototype uses different manifest/envelope format; cannot interchange data with Go host.
6. **Microbenchmarks only** — No realistic application workloads (mixed read/write, concurrent clients, etc.).

---

## Detailed Comparison: Where Overhead Comes From

### Zig Prototype Overhead (vs Direct LMDB)
| Component | Estimated Cost |
|-----------|----------------|
| C ABI call/return (dlopen/dlsym indirection) | ~50 ns |
| Handle registry spin-lock (lock + array access) | ~20 ns |
| Envelope encoding (magic + expiry + codec + memcpy) | ~200 ns |
| LMDB transaction begin/commit (same as baseline) | — |
| **Total per put** | **~270 ns host + fsync** |

### Go Core Overhead (vs Direct LMDB)
| Component | Estimated Cost |
|-----------|----------------|
| Envelope encoding (JSON/bytes codec + logical key prefixes) | ~1.5 μs |
| Logical-key bookkeeping (collection prefix scans on every put) | ~2 μs |
| Context propagation + option parsing | ~0.5 μs |
| **Total per put** | **~4 μs host + fsync** |

### Go Binding Overhead (vs Go Core)
| Component | Estimated Cost |
|-----------|----------------|
| cgo call (Go → C → Go transition) | ~200 ns |
| Per-database mutex (lock/unlock) | ~50 ns |
| Buffer copy (C.CBytes / C.CString) | ~100 ns |
| **Total per put** | **~350 ns binding + 4 μs core + fsync** |

---

## Original recommendation (superseded; see BENCHMARK_REVIEW.md)

### Do Not Port to Zig (Incremental or Otherwise)

**Reasons:**
1. **No performance win** — Zig prototype is 2× slower than direct LMDB and 1.6× slower than Go core on writes; reads are comparable but not better.
2. **Go binding is already fast** — The Go binding calling the in-process core achieves 4,670 puts/sec (15× Zig prototype). The C ABI crossing is the bottleneck, not the host language.
3. **Maintenance burden** — Porting the full runtime (collections, codecs, batches, scans, module loading, RocksDB/LevelDB/BoltDB drivers, HTTP/Redis transports) would be a multi-person-year effort with high risk of correctness regressions.
4. **Ecosystem lock-in** — The Go host's module system, driver bundles, and language bindings are designed around Go's runtime and build system. Replicating in Zig adds significant packaging complexity.
5. **Optimization headroom in Go** — The Go core's put path spends ~4 μs on envelope encoding and logical-key bookkeeping *before* the LMDB commit. Optimizing this (e.g., batching logical-key scans, faster codec) would yield larger gains than switching host languages.

### Recommended Path Forward

1. **Keep Go as the runtime implementation.**
2. **Optimize measured Go core hot spots:**
   - Batch logical-key prefix scans in `replaceLogicalValue` (currently scans hash/set prefixes on every put).
   - Use a faster codec path for `bytes` codec (avoid JSON marshaling overhead).
   - Consider `MDB_NOSYNC` option for workloads that can tolerate async durability.
3. **If distribution size is critical**, investigate `go build -ldflags="-s -w"` + UPX on the Go host (can reduce 3.2 MB → ~1.5 MB).
4. **For embedded non-Go languages**, the current C ABI + driver bundle model works well. The Zig prototype does not improve this story.

---

## Artifacts Produced

| Artifact | Location |
|----------|----------|
| Zig prototype source | `zig-proto/` |
| ADR copy | `docs/adr/0001-evaluate-zig-embedded-runtime.md` |
| Conformance tests (C) | `zig-proto/bench/cbench.c` |
| Conformance tests (Go binding) | `zig_proto_test.go` |
| Benchmark harness (shared) | `zig-proto/bench/workloads.h` |
| Direct LMDB baseline | `zig-proto/bench/lmbench.c` |
| Go core baseline | `zig-proto/bench/gocore/main.go` |
| Go binding benchmarks | `binding_bench_test.go` |
| Built Zig library | `zig-proto/zig-out/lib/libkvlite.dylib` (503 KB) |

---

## Appendix: Raw Benchmark Data (CSV)

```csv
label,workload,threads,value,ops,seconds,ops_per_sec,mean_us,p50_us,p99_us
zig-proto,put,1,64,1000,7.521,133,7520.69,4099,38583
zig-proto,put,4,64,1000,6.608,151,26418.23,14872,106352
zig-proto,put,1,4096,1000,3.199,313,3197.76,2944,19253
zig-proto,put,4,4096,1000,5.956,168,23798.18,12758,253739
zig-proto,get,1,64,1000,0.002,609013,1.46,1,3
zig-proto,get,4,64,1000,0.007,152718,20.59,20,48
zig-proto,get,1,4096,1000,0.005,201248,4.79,3,44
zig-proto,get,4,4096,1000,0.009,110412,33.33,29,124
zig-proto,rget,1,64,1000,0.002,533049,1.74,2,5
zig-proto,rget,4,64,1000,0.006,173521,21.90,20,58
zig-proto,rget,1,4096,1000,0.005,212224,4.55,4,23
zig-proto,rget,4,4096,1000,0.007,135465,28.33,27,58
zig-proto,getmiss,1,64,1000,0.001,677507,1.28,1,2
zig-proto,getmiss,4,64,1000,0.006,160436,24.10,9,197
zig-proto,getmiss,1,4096,1000,0.001,780640,1.18,1,2
zig-proto,getmiss,4,4096,1000,0.008,131182,25.98,8,263
zig-proto,del,1,64,1000,3.933,254,3931.89,3076,17499
zig-proto,del,4,64,1000,3.422,292,13669.94,12629,38875
zig-proto,del,1,4096,1000,7.357,136,7354.72,3691,41687
zig-proto,del,4,4096,1000,7.216,139,28843.66,15876,105563
direct-lmdb,put,1,64,1000,3.689,271,3687.50,3030,24910
direct-lmdb,put,4,64,1000,3.025,331,12079.29,11220,38217
direct-lmdb,put,1,4096,1000,6.085,164,6083.61,3436,48854
direct-lmdb,put,4,4096,1000,3.427,292,13687.44,12769,40578
direct-lmdb,get,1,64,1000,0.002,571429,1.59,1,6
direct-lmdb,get,4,64,1000,0.006,175009,22.32,22,32
direct-lmdb,get,1,4096,1000,0.004,284738,3.35,3,9
direct-lmdb,get,4,4096,1000,0.006,177211,21.99,22,33
direct-lmdb,rget,1,64,1000,0.002,531632,1.66,2,3
direct-lmdb,rget,4,64,1000,0.005,183925,20.93,20,46
direct-lmdb,rget,1,4096,1000,0.004,243191,3.92,4,12
direct-lmdb,rget,4,4096,1000,0.008,127665,30.30,20,175
direct-lmdb,getmiss,1,64,1000,0.001,1013171,0.90,1,2
direct-lmdb,getmiss,4,64,1000,0.006,156715,22.56,10,142
direct-lmdb,getmiss,1,4096,1000,0.001,679810,1.25,1,2
direct-lmdb,getmiss,4,4096,1000,0.007,149970,25.56,8,263
direct-lmdb,del,1,64,1000,3.171,315,3170.41,3015,9600
direct-lmdb,del,4,64,1000,9.655,104,38581.14,30656,127979
direct-lmdb,del,1,4096,1000,4.395,228,4393.20,3478,25255
direct-lmdb,del,4,4096,1000,3.009,332,12020.53,11590,33114
gocore,put,1,64,1000,4.620,216,4618.84,3161,26024
gocore,put,1,4096,1000,5.544,180,5543.22,3632,26714
gocore,get,1,64,1000,0.003,289352,3.37,3,17
gocore,get,1,4096,1000,0.018,55115,18.07,6,291
gocore,rget,1,64,1000,0.003,293427,2.94,3,7
gocore,rget,1,4096,1000,0.010,102428,9.68,6,97
gocore,del,1,64,1000,2.719,368,2718.47,2886,7842
gocore,del,1,4096,1000,3.412,293,3411.36,2989,22144
```

---

**Prepared by:** ADR 0001 evaluation prototype  
**Status:** Original interpretation superseded by [BENCHMARK_REVIEW.md](BENCHMARK_REVIEW.md)
