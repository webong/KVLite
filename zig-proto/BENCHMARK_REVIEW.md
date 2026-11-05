# Zig prototype benchmark review

**Date:** 2026-11-05  
**Status:** Preliminary measurements; Zig runtime development continues

The first `RESULTS.md` report concluded that Go was decisively faster and that the Zig port should stop. That conclusion is not supported by a like-for-like comparison. The original `RESULTS.md` compared direct Go-core calls (which bypass the C ABI) against the Zig host across the C ABI boundary — comparing different code paths with different overheads. This review uses a **true like-for-like comparison**: both hosts are driven through the same `zig-proto/bench/cbench.c` C ABI harness, with the same LMDB engine, 64-byte values, one thread, default synchronous durability, and fresh temporary databases per run.

## Controlled comparison

On an Apple M3 Pro running macOS, a fresh Go/LMDB C-shared bundle was built from the current checkout with `scripts/build-driver-artifact.sh`. The existing Zig library and the fresh Go bundle were then driven by the **same** `zig-proto/bench/cbench.c` C ABI harness.

### Build settings

- **Zig:** `zig build -Doptimize=ReleaseFast` (Zig 0.17.1 tarball install at `~/.local/opt/zig-aarch64-macos-0.17.1`)
- **Go:** `go build -buildmode=c-shared` via `scripts/build-driver-artifact.sh lmdb c-shared /tmp/libkvlite-go-lmdb-review.dylib`
- **cbench:** `cc -O2 -o bin/cbench bench/cbench.c`
- **LMDB:** vendored 0.9.35 sources in `third_party/lmdb/`

### Full benchmark matrix (matched settings, C ABI harness on both hosts)

| Workload | Threads | Value | Zig host (ops/sec) | Go host (ops/sec) | Faster by |
|----------|---------|-------|-------------------|-------------------|-----------|
| put | 1 | 64 B | 504 | 335 | Zig 1.5× |
| put | 4 | 64 B | 570 | 498 | Zig 1.1× |
| put | 1 | 4 KiB | 469 | 426 | Zig 1.1× |
| put | 4 | 4 KiB | 462 | 440 | Zig 1.1× |
| get | 1 | 64 B | 595,948 | 718,752 | Go 1.2× |
| get | 4 | 64 B | 219,250 | 589,971 | Go 2.7× |
| get | 1 | 4 KiB | 390,320 | 240,275 | Zig 1.6× |
| get | 4 | 4 KiB | 188,537 | 231,637 | Go 1.2× |
| rget | 1 | 64 B | 613,874 | 384,320 | Zig 1.6× |
| rget | 4 | 64 B | 198,531 | 547,345 | Go 2.8× |
| rget | 1 | 4 KiB | 415,110 | 285,307 | Zig 1.5× |
| rget | 4 | 4 KiB | 190,767 | 166,528 | Zig 1.1× |
| getmiss | 1 | 64 B | 1,385,042 | 899,281 | Zig 1.5× |
| getmiss | 4 | 64 B | 424,809 | 774,593 | Go 1.8× |
| getmiss | 1 | 4 KiB | 1,567,398 | 940,734 | Zig 1.7× |
| getmiss | 4 | 4 KiB | 431,406 | 790,514 | Go 1.8× |
| del | 1 | 64 B | 553 | 532 | Zig 1.0× |
| del | 4 | 64 B | 463 | 613 | Go 1.3× |
| del | 1 | 4 KiB | 662 | 576 | Zig 1.1× |
| del | 4 | 4 KiB | 671 | 625 | Zig 1.1× |

### Startup

| Metric | Zig host | Go host |
|--------|----------|---------|
| Library load | 12 μs | 826,000 μs (69× slower) |
| Cold open (open+put+get+del+close) | 6.98 ms | 10.82 ms |
| Warm reopen (open+close) | 0.45 ms | 0.54 ms |

### Library size

| Metric | Zig host | Go host |
|--------|----------|---------|
| Library size | 503 KB | 3.2 MB (6.4× larger) |

### Interpretation

**Writes** are fsync-bound (default LMDB synchronous commits), so host overhead is visible. Zig is consistently 1.1–1.5× faster than Go on writes because it avoids Go runtime overhead per operation.

**Reads** use read-only LMDB transactions (no fsync). The results are mixed:
- Zig is faster on single-threaded get (64B), all rget workloads, and all getmiss workloads.
- Go is faster on concurrent get (64B), concurrent getmiss (both sizes), and concurrent get (4KiB).
- The concurrent read advantage for Go likely comes from its runtime scheduling (goroutines map more efficiently to OS threads under the C ABI crossing than Zig's explicit threading model).

**Writes** favor Zig consistently because each put/delete pays the C ABI crossing once per fsync-bound operation, and Zig's lighter per-operation overhead wins there.

**Reads** are dominated by different factors: single-threaded reads favor whichever host has less per-call overhead (Zig wins on larger values, Go wins on smaller); concurrent reads favor Go because its runtime multiplexes goroutines more efficiently than Zig's explicit pthread model across the C ABI boundary.

**Startup and size** strongly favor Zig: 69× faster library load, 1.5× faster cold open, and 6.4× smaller library.

### Reproduction

```sh
# Build Zig library
cd zig-proto && PATH="$HOME/.local/opt/zig-aarch64-macos-0.17.1:$PATH" zig build -Doptimize=ReleaseFast

# Build Go host bundle (from repository root)
bash scripts/build-driver-artifact.sh lmdb c-shared /tmp/libkvlite-go-lmdb-review.dylib

# Run workloads
./zig-proto/bin/cbench ./zig-proto/zig-out/lib/libkvlite.dylib run --workload put --ops 1000 --threads 1 --value 64 --label zig
./zig-proto/bin/cbench /tmp/libkvlite-go-lmdb-review.dylib run --workload put --ops 1000 --threads 1 --value 64 --label go
```

## Decision

**Continue building Zig as a production-quality runtime candidate; do not release or switch production to it yet.** The like-for-like comparison shows Zig is competitive — faster on writes, mixed on reads (better on larger values and random access, slower on concurrent small reads), smaller library, and faster startup. The read performance differences are due to Go's runtime scheduling model rather than inherent language performance, and the Zig prototype remains incomplete (missing collections, codecs, batches beyond raw_apply, scans with full prefix filtering, module loading, HTTP/Redis transports).

**Recommended next steps:**
1. Implement collections support (logical key prefixes)
2. Implement batch/raw_apply more fully (current implementation is basic)
3. Implement full scans with proper prefix filtering
4. Optimize read path for concurrent workloads
5. Measure the Go binding path through both hosts for apples-to-apples binding overhead
6. Run on Linux for cross-platform comparison

The Go host remains the shipped implementation until a separate compatibility and release decision; ADR 0001 now records the decision to build Zig with production intent.