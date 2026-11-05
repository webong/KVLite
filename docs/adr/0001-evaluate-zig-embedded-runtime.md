---
status: proposed
---

# Evaluate a Zig implementation of the embedded runtime

KVLite is embedded-first and language-neutral. Its current host is implemented in Go and exported through the C ABI in `capi/kvlite.h`; the public Go binding and separately installed engine modules can each add native-interface crossings. We do not yet have KVLite benchmarks showing that those crossings, rather than serialization, copies, locking, or storage I/O, are the limiting cost. We will therefore evaluate a Zig implementation behind the existing C interface before deciding whether to port the core.

The experiment is a separate, non-production Zig host supporting one existing engine and the smallest useful operations: open, close, put, get, and delete. It must preserve the documented status codes, buffer ownership, error handling, and driver-selection behavior of those C ABI functions. It must use isolated test directories and must not claim compatibility with existing KVLite on-disk data until conformance tests prove it. Language bindings, transport extensions, and the Go host remain unchanged during the experiment.

Compare the Zig prototype against the current host using the same engine implementation where feasible, machine, durability settings, data sizes, and workload; label any engine differences rather than attributing them to the host language. Measure cold startup, library size, single-threaded and concurrent throughput and latency for tiny and larger values, allocations/copies, and the impact of the current Go binding's per-database mutex. Include direct engine and direct Go-core baselines to distinguish interface overhead from engine work. Record build and packaging effort for Linux and macOS, and run binding-level conformance tests for the supported operation subset against the Zig host.

We will consider an incremental port only if the prototype demonstrates a meaningful, repeatable advantage in a target embedded workload or distribution constraint, without losing correctness, portability, extension loading, or the existing C interface. Otherwise, keep Go as the runtime implementation and optimize measured hot spots. This ADR does not authorize replacing the Go host, changing the C or module ABI, migrating on-disk formats, or publishing a Zig-based release.
