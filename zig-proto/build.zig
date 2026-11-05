//! Build for the ADR 0001 KVLite Zig prototype host.
//!
//! Produces one shared library (libkvlite.dylib / libkvlite.so / kvlite.dll)
//! exporting the ABI v1 embedded subset, with the vendored LMDB 0.9.35
//! sources compiled in. This is NOT wired into the repository Makefile: the
//! prototype is deliberately outside the production build.
//!
//!   zig build -Doptimize=ReleaseFast          # → zig-out/lib/libkvlite.dylib
//!   zig build -Doptimize=ReleaseSafe
//!   zig build -Doptimize=Debug

const std = @import("std");

pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    // No preferred mode: `zig build` defaults to Debug, and both
    // -Doptimize=ReleaseFast and --release=fast select the release build.
    const optimize = b.standardOptimizeOption(.{});

    const lib = b.addLibrary(.{
        .name = "kvlite",
        .linkage = .dynamic,
        .root_module = b.createModule(.{
            .root_source_file = b.path("src/kvlite.zig"),
            .target = target,
            .optimize = optimize,
            .link_libc = true,
        }),
    });
    lib.root_module.addIncludePath(b.path("third_party/lmdb"));
    lib.root_module.addCSourceFiles(.{
        .files = &.{
            "third_party/lmdb/mdb.c",
            "third_party/lmdb/midl.c",
        },
        .flags = &.{
            "-fno-strict-aliasing",
            "-Wno-unknown-pragmas",
            "-Wno-unused-parameter",
        },
    });
    b.installArtifact(lib);
}
