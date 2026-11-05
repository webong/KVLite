---
status: accepted
---

# Build Zig as a production-quality embedded runtime candidate

KVLite is embedded-first and language-neutral. The current Go host exposes the C interface in `capi/kvlite.h`; a Zig host could implement that same interface without requiring bindings to change. We will develop Zig as a production-quality runtime candidate, not as disposable benchmark code. Performance evaluation is part of this work, but the implementation must also be maintainable, correct, and complete enough to serve KVLite's intended users.

Build incrementally behind the existing C interface. Each implemented operation must honor its documented status codes, buffer ownership, error handling, driver selection, concurrency, and persistence semantics, with conformance tests through the same interface used by language bindings. Extend from the initial LMDB slice toward engine-neutral storage, collections, TTL, raw operations, atomic batches, scans, and independently installable engine and transport extensions. Keep code, tests, build tooling, and packaging suitable for eventual production use, even while the candidate is incomplete.

Measure the candidate against the Go host using the same engine implementation where feasible, machine, durability settings, data sizes, workload, and language-binding path. Record raw results and build flags; distinguish engine, host, and binding costs. Use benchmarks to guide implementation and the eventual runtime choice, not as a reason to leave the Zig implementation deliberately shallow.

Building a production-quality candidate is **not** a production release or a switch of the default runtime. The Go host remains the shipped implementation until the Zig host passes the required correctness, compatibility, on-disk migration, platform, extension-loading, and binding checks and a separate release decision is made. Until compatibility is demonstrated, use isolated directories and do not open existing Go-host data with Zig. Do not silently change the C or module ABI.
