---
status: accepted
---

# Build Zig as the KVLite host; guests may be in Go

KVLite is embedded-first and language-neutral. The **host** is implemented in Zig, exposing the C interface in `capi/kvlite.h`. **Guests** (plugins, engine drivers, codecs, transport stores) may be written in Go or any other language that implements ext's guest bindings, and are loaded by the Zig host through ext's plugin infrastructure (`pkg/ext-zig`, `ext.host.CShared`, `ext.host.Session`).

The Zig host implements the C ABI v1 subset (`kvlite_open`, `kvlite_close`, `kvlite_put`, `kvlite_get`, `kvlite_delete`, `kvlite_free`, raw operations, scans, multi ops, batch apply) backed by vendored LMDB 0.9.35. Plugins are C shared libraries exporting the ext C ABI (`ext_plugin_abi_version`, `ext_plugin_open`, `ext_plugin_call`, `ext_plugin_close`); KVLite's wire protocol rides inside `ext_plugin_call`'s JSON request/response. Go guests use ext's Go guest bindings (`pkg/ext-go`) to implement the same ext plugin ABI and are loaded identically.

Build incrementally behind the existing C interface. Each implemented operation must honor its documented status codes, buffer ownership, error handling, driver selection, concurrency, and persistence semantics, with conformance tests through the same interface used by language bindings. Extend from the initial LMDB slice toward engine-neutral storage, collections, TTL, raw operations, atomic batches, scans, and independently installable engine and transport extensions. Keep code, tests, build tooling, and packaging suitable for eventual production use, even while the candidate is incomplete.

Measure the Zig host against the Go host using the same engine implementation where feasible, machine, durability settings, data sizes, workload, and language-binding path. Record raw results and build flags; distinguish engine, host, and binding costs. Use benchmarks to guide implementation, not as a reason to leave the Zig implementation deliberately shallow.

The Zig host is **the** runtime candidate under this ADR. The Go host remains the shipped implementation until the Zig host passes the required correctness, compatibility, on-disk migration, platform, extension-loading, and binding checks and a separate release decision is made. Until compatibility is demonstrated, use isolated directories and do not open existing Go-host data with Zig. Do not silently change the C or module ABI.

## Host/Guest Language Split

| Component | Language | Mechanism |
|-----------|----------|-----------|
| Host (C ABI, registry, LMDB engine, plugin loader) | Zig | `zig-proto/`, `ext.host` |
| Guests / plugins | Go or any ext guest language | ext C ABI + ext guest bindings |
| Go host (existing, shipped) | Go | `kvlite/` root package |
