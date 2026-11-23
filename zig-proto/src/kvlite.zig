// KVLite ADR 0001 Zig prototype host.
//
// A NON-PRODUCTION shared library implementing the embedded subset of the
// ABI v1 C interface documented in capi/kvlite.h:
//
//     kvlite_abi_version, kvlite_open, kvlite_open_with_driver,
//     kvlite_open_with_backend (alias), kvlite_close, kvlite_put,
//     kvlite_get, kvlite_delete, kvlite_free
//     kvlite_raw_put, kvlite_raw_get, kvlite_raw_delete,
//     kvlite_raw_apply, kvlite_raw_multi_put, kvlite_raw_multi_get,
//     kvlite_raw_scan_open, kvlite_raw_scan_next, kvlite_raw_scan_close
//
// Engine: the vendored LMDB 0.9.35 sources (third_party/lmdb) — the exact
// engine source the Go host's extensions/lmdb driver compiles through cgo.
//
// Behavior mirrors the Go host (capi/main.go + src core) for the subset:
//   - status codes 0/1/2/3 and malloc'd error strings owned by the caller,
//   - malloc'd get buffers owned by the caller and released with
//     kvlite_free,
//   - empty keys and negative TTLs rejected as invalid arguments,
//   - ttl_seconds == 0 meaning no expiry, absolute read-time expiry with an
//     opportunistic delete of expired records,
//   - a per-directory driver manifest so a directory records its driver and
//     is never reopened through a different driver name,
//   - ever-increasing 64-bit handles in a lock-protected registry.
//
// Concurrency: LMDB serializes writers through its own writer lock (a
// blocking pthread mutex inside mdb.c), and read transactions are
// thread-agnostic under MDB_NOTLS, which both hosts enable. The only
// host-side lock is the handle-registry spin lock, whose critical section is
// a constant-time array update.
//
// It intentionally does NOT implement collections, codecs, module loading,
// or on-disk compatibility with the Go host: it writes its own envelope
// layout and its own manifest file name, so the two hosts refuse each
// other's directories by construction.
//
// System calls go through libc directly (extern "c") rather than std.posix:
// the C ABI surface is stable across Zig releases, which keeps this
// prototype buildable on newer toolchains than it was written for.

const std = @import("std");
const builtin = @import("builtin");
const lmdb = @import("lmdb.zig");
const ext = @import("ext_io");

// Debug: libc printf for tracing
pub extern "c" fn printf(format: [*:0]const u8, ...) c_int;

pub const abi_version: c_uint = 1;

pub const OK: c_int = 0;
pub const NOT_FOUND: c_int = 1;
pub const INVALID_ARGUMENT: c_int = 2;
pub const STORAGE_ERROR: c_int = 3;

const default_driver = "lmdb";

// Same default virtual map reservation as extensions/lmdb: LMDB's map is an
// address-space reservation, not an immediate allocation.
const default_map_size: usize = 16 << 30;

// Envelope layout for scalar logical values: "KVL1" magic, big-endian u64
// absolute expiry in Unix nanoseconds (0 = none), big-endian u16 codec name
// length, codec name, payload. Same shape as the Go core's envelope for the
// "bytes" codec; the surrounding keyspace still differs from the Go host.
const envelope_magic = [4]u8{ 'K', 'V', 'L', '1' };
const envelope_header_size = 14;
const bytes_codec = "bytes";
const kind_value: u8 = 1; // storage-key namespace prefix used by the Go core

const envelope_buffer_limit = 8192;
const error_buffer_limit = 512;
const path_buffer_limit = 4096;

const manifest_file = "KVLITE-ZIG-PROTO-MANIFEST.json";

// ---------------------------------------------------------------------------
// libc boundary: allocation, time, and a small filesystem surface. Error
// strings and returned values are malloc'd so the caller releases everything
// with kvlite_free, exactly like the Go host's C.CString/C.CBytes.

extern "c" fn malloc(size: usize) ?*anyopaque;
extern "c" fn realloc(pointer: ?*anyopaque, size: usize) ?*anyopaque;
extern "c" fn free(pointer: ?*anyopaque) void;
extern "c" fn mkdir(path: [*:0]const u8, mode: c_uint) c_int;
extern "c" fn access(path: [*:0]const u8, mode: c_int) c_int;
extern "c" fn open(path: [*:0]const u8, flags: c_int, ...) c_int;
extern "c" fn read(fd: c_int, buffer: [*]u8, count: usize) isize;
extern "c" fn write(fd: c_int, buffer: [*]const u8, count: usize) isize;
extern "c" fn close(fd: c_int) c_int;
extern "c" fn opendir(path: [*:0]const u8) ?*anyopaque;
extern "c" fn readdir(dir: *anyopaque) ?*anyopaque;
extern "c" fn closedir(dir: *anyopaque) c_int;
extern "c" fn clock_gettime(clock: c_int, timestamp: *Timestamp) c_int;

// Layout is identical on the supported 64-bit targets (macOS, Linux glibc).
const Timestamp = extern struct { seconds: isize, nanos: isize };

// clock_gettime clock ids and open(2) flag values, which differ by platform.
const CLOCK_REALTIME: c_int = 0;
const O_RDONLY: c_int = 0;
const O_WRONLY: c_int = 1;
const O_CREAT: c_int = if (builtin.os.tag == .macos) 0x200 else 0x40;
const O_EXCL: c_int = if (builtin.os.tag == .macos) 0x800 else 0x80;

fn mallocOrDie(size: usize) [*]u8 {
    const block = malloc(size) orelse @panic("kvlite-zig-proto: out of memory");
    return @as([*]u8, @ptrCast(block));
}

fn toFree(block: [*]u8) ?*anyopaque {
    return @ptrCast(block);
}

fn pathExists(path: []const u8, buffer: []u8) bool {
    if (path.len + 1 > buffer.len) return false;
    @memcpy(buffer[0..path.len], path);
    buffer[path.len] = 0;
    return access(cPath(buffer[0 .. path.len + 1]), 0) == 0;
}

// ---------------------------------------------------------------------------
// Error plumbing.

fn setError(out_error: ?*?[*:0]u8, comptime format: []const u8, args: anytype) void {
    if (out_error == null) return;
    var buffer: [error_buffer_limit]u8 = undefined;
    const text = std.fmt.bufPrint(&buffer, format, args) catch {
        out_error.?.* = copyCString("kvlite: internal error: message formatting failed");
        return;
    };
    out_error.?.* = copyCString(text);
}

fn copyCString(text: []const u8) [*:0]u8 {
    const block = mallocOrDie(text.len + 1);
    @memcpy(block[0..text.len], text);
    block[text.len] = 0;
    return @ptrCast(block);
}

// ---------------------------------------------------------------------------
// Driver-name validation, mirroring the Go binding's WithDriver rules:
// lowercase ASCII letters and digits, with '-'/'_' allowed after the first
// character.

fn validDriverName(name: []const u8) bool {
    if (name.len == 0) return false;
    for (name, 0..) |char, index| {
        const is_lower = char >= 'a' and char <= 'z';
        const is_digit = char >= '0' and char <= '9';
        if (is_lower or is_digit) continue;
        if (index != 0 and (char == '-' or char == '_')) continue;
        return false;
    }
    return true;
}

// ---------------------------------------------------------------------------
// Handle registry. Mirrors the Go host: handles start at 1 and increase
// forever, closed handles are never reused, and the table is lock-protected.
// A test-and-set spin lock protects the registry; critical sections are
// constant-time array updates so spinning is bounded; LMDB's blocking writer
// lock is what serializes the long operations.

const Database = struct {
    env: *lmdb.Env = undefined,
    dbi: lmdb.DBI = 0,
    max_key_size: usize = 0,
    map_size: usize = default_map_size,
};

var registry_lock = std.atomic.Value(bool).init(false);

fn registryAcquire() void {
    while (registry_lock.swap(true, .acquire)) {
        std.atomic.spinLoopHint();
    }
}

fn registryRelease() void {
    registry_lock.store(false, .release);
}

var registry_next: u64 = 1;
var registry_items: ?[*]?*Database = null;
var registry_len: usize = 0;
var registry_cap: usize = 0;

fn registryInsert(db: *Database) u64 {
    registryAcquire();
    defer registryRelease();
    const handle = registry_next;
    const slot = handle - 1;
    if (slot >= registry_cap) {
        const new_cap: usize = if (registry_cap == 0) 16 else registry_cap * 2;
        const byte_size = new_cap * @sizeOf(?*Database);
        const block = realloc(@ptrCast(registry_items), byte_size) orelse
            @panic("kvlite-zig-proto: out of memory growing the handle table");
        registry_items = @as([*]?*Database, @ptrCast(@alignCast(block)));
        registry_cap = new_cap;
    }
    registry_next += 1;
    registry_items.?[slot] = db;
    if (handle > registry_len) registry_len = @as(usize, @intCast(handle));
    return handle;
}

fn registryLookup(handle: u64) ?*Database {
    registryAcquire();
    defer registryRelease();
    if (handle == 0 or handle > registry_len) return null;
    return registry_items.?[handle - 1];
}

fn registryRemove(handle: u64) ?*Database {
    registryAcquire();
    defer registryRelease();
    if (handle == 0 or handle > registry_len) return null;
    const db = registry_items.?[handle - 1];
    if (db != null) registry_items.?[handle - 1] = null;
    return db;
}

// ---------------------------------------------------------------------------
// Ext plugin host registry (ext_io, github.com/webong/ext/pkg/ext-zig).
//
// KVLite loads plugins through ext's host infrastructure: a plugin is a C
// shared library exporting the ext_plugin.h ABI (open/call/close), loaded
// via ext.host.CShared and called through ext.host.Session. The KVLite
// wire protocol rides inside ext_plugin_call's JSON request/response.
//
// Plugin wire protocol (JSON, inside ext_plugin_call):
//
//   Request:  {"contract":{"name":"kvlite.kv","version":"v1"},"operation":"get","payload":{"key":"base64","collection":"users"}}
//   Response: {"value":"base64","found":true}  or  {"error":{"code":"not_found","message":"..."}}
//
// Operations: get, put, delete, scan, multi_get, multi_put, health
//
// KVLite keeps its own C ABI contract (capi/kvlite.h) as the public surface.
// ext is the plugin *loading* layer: callers open a KVLite directory with
// kvlite_open*, then optionally load plugins with kvlite_plugin_load* and
// call into them with kvlite_plugin_call*.

var plugin_registry_lock = std.atomic.Value(bool).init(false);

fn pluginRegistryAcquire() void {
    while (plugin_registry_lock.swap(true, .acquire)) {
        std.atomic.spinLoopHint();
    }
}

fn pluginRegistryRelease() void {
    plugin_registry_lock.store(false, .release);
}

var plugin_registry_next: u64 = 1;
var plugin_registry_items: ?[*]?*PluginSession = null;
var plugin_registry_len: usize = 0;
var plugin_registry_cap: usize = 0;

// A loaded ext plugin: dlopen'd library (kept resident) plus the ext host
// session used to call into it.
const PluginSession = struct {
    library: ext.host.CShared,
    session: ext.host.Session,
};
fn pluginRegistryInsert(ps: *PluginSession) u64 {
    pluginRegistryAcquire();
    defer pluginRegistryRelease();
    const handle = plugin_registry_next;
    const slot = handle - 1;
    if (slot >= plugin_registry_cap) {
        const new_cap: usize = if (plugin_registry_cap == 0) 16 else plugin_registry_cap * 2;
        const byte_size = new_cap * @sizeOf(?*PluginSession);
        const block = realloc(@ptrCast(plugin_registry_items), byte_size) orelse
            @panic("kvlite-zig-proto: out of memory growing the plugin handle table");
        plugin_registry_items = @as([*]?*PluginSession, @ptrCast(@alignCast(block)));
        plugin_registry_cap = new_cap;
    }
    plugin_registry_items.?[slot] = ps;
    plugin_registry_next += 1;
    if (handle > plugin_registry_len) plugin_registry_len = @as(usize, @intCast(handle));
    return handle;
}

fn pluginRegistryLookup(handle: u64) ?*PluginSession {
    pluginRegistryAcquire();
    defer pluginRegistryRelease();
    if (handle == 0 or handle > plugin_registry_len) return null;
    return plugin_registry_items.?[handle - 1];
}

fn pluginRegistryRemove(handle: u64) void {
    pluginRegistryAcquire();
    defer pluginRegistryRelease();
    if (handle == 0 or handle > plugin_registry_len) return;
    const ps = plugin_registry_items.?[handle - 1];
    if (ps != null) {
        plugin_registry_items.?[handle - 1] = null;
        ps.?.session.deinit();
        std.heap.page_allocator.destroy(ps.?);
    }
}

// ---------------------------------------------------------------------------
// Filesystem helpers (libc-backed, best-effort error reporting).

// Converts a stack buffer already containing a NUL-terminated string into a
// C string pointer. Callers must have written path[len] = 0.
fn cPath(buffer: []u8) [*:0]const u8 {
    return @ptrCast(buffer.ptr);
}

fn mkdirRecursive(path: []const u8, out_error: ?*?[*:0]u8) bool {
    if (path.len == 0 or path.len >= path_buffer_limit) {
        setError(out_error, "kvlite: database path is too long", .{});
        return false;
    }
    var buffer: [path_buffer_limit]u8 = undefined;
    var index: usize = 0;
    if (path[0] == '/') index = 1;
    while (index <= path.len) {
        const slash = std.mem.indexOfScalarPos(u8, path, index, '/') orelse path.len;
        if (slash > 0) {
            @memcpy(buffer[0..slash], path[0..slash]);
            buffer[slash] = 0;
            _ = mkdir(cPath(buffer[0 .. slash + 1]), 0o700); // EEXIST and beyond-parents are fine
        }
        if (slash == path.len) break;
        index = slash + 1;
    }
    if (!pathExists(path, &buffer)) {
        setError(out_error, "kvlite: create database directory: failed", .{});
        return false;
    }
    return true;
}

fn readSmallFile(path: []const u8, path_buffer: []u8, out_buffer: []u8) ?[]u8 {
    if (path.len + 1 > path_buffer.len) return null;
    @memcpy(path_buffer[0..path.len], path);
    path_buffer[path.len] = 0;
    const fd = open(cPath(path_buffer[0 .. path.len + 1]), O_RDONLY);
    if (fd < 0) return null;
    defer _ = close(fd);
    const count = read(fd, out_buffer.ptr, out_buffer.len);
    if (count < 0) return null;
    return out_buffer[0..@intCast(count)];
}

fn writeExclusiveFile(path: []const u8, path_buffer: []u8, contents: []const u8) bool {
    if (path.len + 1 > path_buffer.len) return false;
    @memcpy(path_buffer[0..path.len], path);
    path_buffer[path.len] = 0;
    const fd = open(cPath(path_buffer[0 .. path.len + 1]), O_WRONLY | O_CREAT | O_EXCL, @as(c_uint, 0o600));
    if (fd < 0) return false;
    defer _ = close(fd);
    var written: usize = 0;
    while (written < contents.len) {
        const count = write(fd, contents.ptr + written, contents.len - written);
        if (count <= 0) return false;
        written += @intCast(count);
    }
    return true;
}

fn directoryIsEmpty(path: []const u8, path_buffer: []u8) ?bool {
    if (path.len + 1 > path_buffer.len) return null;
    @memcpy(path_buffer[0..path.len], path);
    path_buffer[path.len] = 0;
    const dir = opendir(cPath(path_buffer[0 .. path.len + 1])) orelse return null;
    defer _ = closedir(dir);
    var count: usize = 0;
    while (readdir(dir) != null) count += 1;
    // readdir always lists "." and ".." on the supported platforms.
    return count <= 2;
}

// The prototype's own directory manifest. Deliberately a different file name
// and content from the Go host's KVLITE-MANIFEST.json so the two hosts cannot
// silently open each other's directories.
fn checkOrCreateManifest(
    path: []const u8,
    driver: []const u8,
    out_error: ?*?[*:0]u8,
) c_int {
    var manifest_buffer: [path_buffer_limit]u8 = undefined;
    const manifest_path = std.fmt.bufPrint(&manifest_buffer, "{s}/{s}", .{ path, manifest_file }) catch {
        setError(out_error, "kvlite: database path is too long", .{});
        return INVALID_ARGUMENT;
    };

    var probe_buffer: [path_buffer_limit]u8 = undefined;
    if (pathExists(manifest_path, &probe_buffer)) {
        var file_buffer: [1024]u8 = undefined;
        const contents = readSmallFile(manifest_path, &probe_buffer, &file_buffer) orelse {
            setError(out_error, "kvlite: read prototype manifest: failed", .{});
            return STORAGE_ERROR;
        };
        var recorded: []const u8 = "";
        if (std.mem.indexOf(u8, contents, "\"driver\":\"")) |start| {
            const name_start = start + "\"driver\":\"".len;
            if (std.mem.indexOfScalarPos(u8, contents, name_start, '"')) |end| {
                if (end - name_start <= 64) recorded = contents[name_start..end];
            }
        }
        if (!std.mem.eql(u8, recorded, driver)) {
            setError(
                out_error,
                "kvlite: database driver mismatch: path was initialized for driver \"{s}\", requested \"{s}\"",
                .{ recorded, driver },
            );
            return STORAGE_ERROR;
        }
        return OK;
    }

    var scan_buffer: [path_buffer_limit]u8 = undefined;
    const empty = directoryIsEmpty(path, &scan_buffer) orelse {
        setError(out_error, "kvlite: inspect database directory: failed", .{});
        return STORAGE_ERROR;
    };
    if (!empty) {
        setError(
            out_error,
            "kvlite: \"{s}\" is non-empty and has no {s}; migrate it into a new KVLite directory instead of selecting \"{s}\"",
            .{ path, manifest_file, driver },
        );
        return STORAGE_ERROR;
    }

    var text_buffer: [256]u8 = undefined;
    const text = std.fmt.bufPrint(
        &text_buffer,
        "{{\"kvlite_zig_proto\":1,\"driver\":\"{s}\",\"note\":\"ADR 0001 non-production prototype; not a KVLite on-disk database\"}}\n",
        .{driver},
    ) catch {
        setError(out_error, "kvlite: encode prototype manifest: failed", .{});
        return STORAGE_ERROR;
    };
    if (!writeExclusiveFile(manifest_path, &probe_buffer, text)) {
        setError(out_error, "kvlite: write prototype manifest: failed", .{});
        return STORAGE_ERROR;
    }
    return OK;
}

// ---------------------------------------------------------------------------
// Engine open.

const OpenResult = union(enum) {
    ok: *Database,
    fail: c_int, // out_error already set
};

fn openDatabase(path: []const u8, driver: []const u8, out_error: ?*?[*:0]u8) OpenResult {
    if (!std.mem.eql(u8, driver, default_driver)) {
        // Same distinction the Go host makes: a syntactically valid name that
        // this bundle does not register is a missing driver, not a bad
        // argument. statusFor in capi/main.go maps ErrDriverNotInstalled to
        // KVLITE_STORAGE_ERROR.
        setError(out_error, "kvlite: storage driver is not installed: {s}", .{driver});
        return .{ .fail = STORAGE_ERROR };
    }

    if (!mkdirRecursive(path, out_error)) return .{ .fail = STORAGE_ERROR };

    const manifest_status = checkOrCreateManifest(path, driver, out_error);
    if (manifest_status != OK) return .{ .fail = manifest_status };

    var path_buffer: [path_buffer_limit]u8 = undefined;
    if (path.len >= path_buffer_limit) {
        setError(out_error, "kvlite: database path is too long", .{});
        return .{ .fail = INVALID_ARGUMENT };
    }
    @memcpy(path_buffer[0..path.len], path);
    path_buffer[path.len] = 0;
    const c_path: [*:0]const u8 = @ptrCast(&path_buffer);

    var env: ?*lmdb.Env = null;
    if (lmdb.isError(lmdb.mdb_env_create(&env)) or env == null) {
        setError(out_error, "kvlite: create LMDB environment: allocation failed", .{});
        return .{ .fail = STORAGE_ERROR };
    }

    const db = @as(*Database, @ptrCast(@alignCast(mallocOrDie(@sizeOf(Database)))));
    db.* = .{};
    db.env = env.?;

    var rc: c_int = 0;
    rc = lmdb.mdb_env_set_mapsize(db.env, db.map_size);
    if (lmdb.isError(rc)) {
        setError(out_error, "kvlite: configure LMDB map: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
        lmdb.mdb_env_close(db.env);
        free(toFree(@ptrCast(db)));
        return .{ .fail = STORAGE_ERROR };
    }
    // extensions/lmdb always adds MDB_NOTLS ("Without this flag LMDB would
    // not be practically usable in Go"); mode 0o600 matches env.Open there.
    rc = lmdb.mdb_env_open(db.env, c_path, lmdb.NOTLS, 0o600);
    if (lmdb.isError(rc)) {
        setError(out_error, "kvlite: open LMDB: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
        lmdb.mdb_env_close(db.env);
        free(toFree(@ptrCast(db)));
        return .{ .fail = STORAGE_ERROR };
    }
    db.max_key_size = @intCast(@max(0, lmdb.mdb_env_get_maxkeysize(db.env)));

    var txn: ?*lmdb.Txn = null;
    if (lmdb.isError(lmdb.mdb_txn_begin(db.env, null, lmdb.RDONLY, &txn)) or txn == null) {
        setError(out_error, "kvlite: open LMDB root database: transaction failed", .{});
        lmdb.mdb_env_close(db.env);
        free(toFree(@ptrCast(db)));
        return .{ .fail = STORAGE_ERROR };
    }
    var dbi: lmdb.DBI = 0;
    rc = lmdb.mdb_dbi_open(txn.?, null, 0, &dbi);
    if (lmdb.isError(rc)) {
        lmdb.mdb_txn_abort(txn.?);
        setError(
            out_error,
            "kvlite: open LMDB root database: {s}",
            .{std.mem.span(lmdb.mdb_strerror(rc))},
        );
        lmdb.mdb_env_close(db.env);
        free(toFree(@ptrCast(db)));
        return .{ .fail = STORAGE_ERROR };
    }
    _ = lmdb.mdb_txn_commit(txn.?);
    db.dbi = dbi;
    return .{ .ok = db };
}

// ---------------------------------------------------------------------------
// Storage-key and envelope helpers (Go core parity for scalar values).

fn currentUnixNanos() i64 {
    var ts: Timestamp = .{ .seconds = 0, .nanos = 0 };
    if (clock_gettime(CLOCK_REALTIME, &ts) != 0) return 0;
    return @as(i64, ts.seconds) * std.time.ns_per_s + @as(i64, ts.nanos);
}

const Envelope = struct {
    expires_at: i64,
    payload: []const u8,
};

fn decodeEnvelope(data: []const u8) ?Envelope {
    if (data.len < envelope_header_size) return null;
    if (!std.mem.eql(u8, data[0..4], &envelope_magic)) return null;
    const codec_length: usize = std.mem.readInt(u16, data[12..14], .big);
    if (data.len < envelope_header_size + codec_length) return null;
    const codec = data[envelope_header_size .. envelope_header_size + codec_length];
    if (!std.mem.eql(u8, codec, bytes_codec)) return null;
    return .{
        .expires_at = @bitCast(std.mem.readInt(u64, data[4..12], .big)),
        .payload = data[envelope_header_size + codec_length ..],
    };
}

fn mapStatusFor(rc: c_int, out_error: ?*?[*:0]u8) ?c_int {
    if (rc == lmdb.SUCCESS) return OK;
    if (rc == lmdb.MAP_FULL) {
        setError(
            out_error,
            "kvlite: LMDB map is full ({d} bytes virtual limit); reopen with a larger lmdb.map_size for a bigger store",
            .{default_map_size},
        );
        return STORAGE_ERROR;
    }
    return null;
}

// A growable key buffer: LMDB caps storage keys at 511 bytes by default, but
// the boundary checks come from the engine, so keep the host simple.
const KeyBuffer = struct {
    stack: [1024]u8 = undefined,
    heap: ?[*]u8 = null,

    fn get(self: *KeyBuffer, length: usize) []u8 {
        if (length <= self.stack.len) return self.stack[0..length];
        self.heap = mallocOrDie(length);
        return self.heap.?[0..length];
    }
    fn deinit(self: *KeyBuffer) void {
        if (self.heap) |block| {
            free(toFree(block));
            self.heap = null;
        }
    }
};

fn buildStorageKey(key: [*]const u8, key_length: usize, buffer: []u8) []u8 {
    buffer[0] = kind_value;
    @memcpy(buffer[1 .. 1 + key_length], key[0..key_length]);
    return buffer[0 .. 1 + key_length];
}

// ---------------------------------------------------------------------------
// C ABI exports.

fn abiVersionExport() callconv(.c) c_uint {
    return abi_version;
}

fn openExport(
    path: ?[*:0]const u8,
    out_handle: ?*u64,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    return openImpl(path, null, false, out_handle, out_error);
}

fn openWithBackendExport(
    path: ?[*:0]const u8,
    driver: ?[*:0]const u8,
    out_handle: ?*u64,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    return openImpl(path, driver, true, out_handle, out_error);
}

fn openWithDriverExport(
    path: ?[*:0]const u8,
    driver: ?[*:0]const u8,
    out_handle: ?*u64,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    return openImpl(path, driver, true, out_handle, out_error);
}

fn openImpl(
    path: ?[*:0]const u8,
    driver: ?[*:0]const u8,
    explicit_driver: bool,
    out_handle: ?*u64,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    if (path == null or out_handle == null) {
        setError(out_error, "kvlite: invalid argument: path and output handle are required", .{});
        return INVALID_ARGUMENT;
    }
    var driver_view: []const u8 = default_driver;
    if (explicit_driver) {
        // C.GoString(nil) is "" and the Go host then rejects the empty name.
        driver_view = if (driver != null) std.mem.span(driver.?) else "";
        if (!validDriverName(driver_view)) {
            setError(out_error, "kvlite: invalid argument: invalid driver name \"{s}\"", .{driver_view});
            return INVALID_ARGUMENT;
        }
    }
    const path_view = std.mem.span(path.?);
    if (path_view.len == 0) {
        setError(out_error, "kvlite: invalid argument: database path is required and cannot contain NUL", .{});
        return INVALID_ARGUMENT;
    }
    const db = switch (openDatabase(path_view, driver_view, out_error)) {
        .ok => |opened| opened,
        .fail => |status| return status,
    };
    out_handle.?.* = registryInsert(db);
    return OK;
}

fn closeExport(handle: u64, out_error: ?*?[*:0]u8) callconv(.c) c_int {
    const db = registryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid database handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    lmdb.mdb_env_close(db.env);
    free(toFree(@ptrCast(db)));
    return OK;
}

fn putExport(
    handle: u64,
    key: ?*const anyopaque,
    key_length: usize,
    value: ?*const anyopaque,
    value_length: usize,
    ttl_seconds: i64,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const db = registryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid database handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    if (key_length == 0) {
        setError(out_error, "kvlite: invalid argument: key is required", .{});
        return INVALID_ARGUMENT;
    }
    if (key == null) {
        setError(out_error, "kvlite: invalid argument: non-empty input has a nil pointer", .{});
        return INVALID_ARGUMENT;
    }
    if (value_length > 0 and value == null) {
        setError(out_error, "kvlite: invalid argument: non-empty input has a nil pointer", .{});
        return INVALID_ARGUMENT;
    }
    if (ttl_seconds < 0) {
        setError(out_error, "kvlite: invalid argument: ttl_seconds must be non-negative", .{});
        return INVALID_ARGUMENT;
    }
    // Go: maxSeconds = math.MaxInt64 / int64(time.Second).
    const max_ttl_seconds: i64 = std.math.maxInt(i64) / std.time.ns_per_s;
    if (ttl_seconds > max_ttl_seconds) {
        setError(out_error, "kvlite: invalid argument: ttl_seconds is too large", .{});
        return INVALID_ARGUMENT;
    }

    const storage_length = 1 + key_length;
    if (storage_length > db.max_key_size) {
        setError(
            out_error,
            "kvlite: LMDB storage key is {d} bytes; maximum is {d}",
            .{ storage_length, db.max_key_size },
        );
        return STORAGE_ERROR;
    }

    const expires_at: i64 =
        if (ttl_seconds == 0) 0 else currentUnixNanos() + ttl_seconds * std.time.ns_per_s;
    const envelope_length = envelope_header_size + bytes_codec.len + value_length;
    var stack_envelope: [envelope_buffer_limit]u8 = undefined;
    var heap_envelope: ?[*]u8 = null;
    defer if (heap_envelope) |block| free(toFree(block));
    var envelope: []u8 = undefined;
    if (envelope_length <= envelope_buffer_limit) {
        envelope = stack_envelope[0..envelope_length];
    } else {
        heap_envelope = mallocOrDie(envelope_length);
        envelope = heap_envelope.?[0..envelope_length];
    }
    @memcpy(envelope[0..4], &envelope_magic);
    std.mem.writeInt(u64, envelope[4..12], @bitCast(expires_at), .big);
    std.mem.writeInt(u16, envelope[12..14], @intCast(bytes_codec.len), .big);
    @memcpy(envelope[envelope_header_size .. envelope_header_size + bytes_codec.len], bytes_codec);
    const payload = envelope[envelope_header_size + bytes_codec.len ..];
    if (value_length > 0) {
        @memcpy(payload, @as([*]const u8, @ptrCast(value.?))[0..value_length]);
    }

    var key_buffer = KeyBuffer{};
    defer key_buffer.deinit();
    const storage_key = buildStorageKey(@ptrCast(key.?), key_length, key_buffer.get(storage_length));

    var key_val = lmdb.Val{ .mv_size = storage_key.len, .mv_data = storage_key.ptr };
    var value_val = lmdb.Val{ .mv_size = envelope.len, .mv_data = envelope.ptr };

    // Concurrent writers serialize inside LMDB's writer lock.
    var txn: ?*lmdb.Txn = null;
    if (lmdb.isError(lmdb.mdb_txn_begin(db.env, null, 0, &txn)) or txn == null) {
        setError(out_error, "kvlite: LMDB: begin write transaction failed", .{});
        return STORAGE_ERROR;
    }
    const rc = lmdb.mdb_put(txn.?, db.dbi, &key_val, &value_val, 0);
    if (lmdb.isError(rc)) {
        lmdb.mdb_txn_abort(txn.?);
        if (mapStatusFor(rc, out_error)) |status| return status;
        setError(out_error, "kvlite: LMDB put: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
        return STORAGE_ERROR;
    }
    if (lmdb.isError(lmdb.mdb_txn_commit(txn.?))) {
        setError(out_error, "kvlite: LMDB: commit write transaction failed", .{});
        return STORAGE_ERROR;
    }
    return OK;
}

fn getExport(
    handle: u64,
    key: ?*const anyopaque,
    key_length: usize,
    out_value: ?*?*anyopaque,
    out_length: ?*usize,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const db = registryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid database handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    if (out_value == null or out_length == null) {
        setError(out_error, "kvlite: invalid argument: output value and length are required", .{});
        return INVALID_ARGUMENT;
    }
    if (key_length == 0) {
        setError(out_error, "kvlite: invalid argument: key is required", .{});
        return INVALID_ARGUMENT;
    }
    if (key == null) {
        setError(out_error, "kvlite: invalid argument: non-empty input has a nil pointer", .{});
        return INVALID_ARGUMENT;
    }
    const storage_length = 1 + key_length;
    if (storage_length > db.max_key_size) {
        setError(
            out_error,
            "kvlite: LMDB storage key is {d} bytes; maximum is {d}",
            .{ storage_length, db.max_key_size },
        );
        return STORAGE_ERROR;
    }

    var key_buffer = KeyBuffer{};
    defer key_buffer.deinit();
    const storage_key = buildStorageKey(@ptrCast(key.?), key_length, key_buffer.get(storage_length));

    var key_val = lmdb.Val{ .mv_size = storage_key.len, .mv_data = storage_key.ptr };
    var data_val = lmdb.Val{ .mv_size = 0, .mv_data = null };

    var txn: ?*lmdb.Txn = null;
    if (lmdb.isError(lmdb.mdb_txn_begin(db.env, null, lmdb.RDONLY, &txn)) or txn == null) {
        setError(out_error, "kvlite: LMDB: begin read transaction failed", .{});
        return STORAGE_ERROR;
    }
    const rc = lmdb.mdb_get(txn.?, db.dbi, &key_val, &data_val);
    if (lmdb.isError(rc)) {
        lmdb.mdb_txn_abort(txn.?);
        if (rc == lmdb.NOTFOUND) {
            // The Go host attaches the message here too (statusFor runs
            // setError before the status mapping).
            setError(out_error, "kvlite: key not found", .{});
            return NOT_FOUND;
        }
        if (mapStatusFor(rc, out_error)) |status| return status;
        setError(out_error, "kvlite: LMDB get: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
        return STORAGE_ERROR;
    }

    const stored = @as([*]const u8, @ptrCast(data_val.mv_data.?))[0..data_val.mv_size];
    const envelope = decodeEnvelope(stored) orelse {
        lmdb.mdb_txn_abort(txn.?);
        setError(out_error, "kvlite: invalid value envelope", .{});
        return STORAGE_ERROR;
    };
    if (envelope.expires_at != 0 and currentUnixNanos() >= envelope.expires_at) {
        lmdb.mdb_txn_abort(txn.?);
        // Mirror the Go core: an expired value is never returned, and the
        // read deletes it opportunistically.
        var key_buffer2 = KeyBuffer{};
        defer key_buffer2.deinit();
        const storage_key2 = buildStorageKey(@ptrCast(key.?), key_length, key_buffer2.get(storage_length));
        deleteStorageKey(db, storage_key2);
        setError(out_error, "kvlite: key not found", .{});
        return NOT_FOUND;
    }
    const payload_length = envelope.payload.len;
    const block = mallocOrDie(@max(payload_length, 1));
    if (payload_length > 0) {
        @memcpy(block[0..payload_length], envelope.payload);
    }
    lmdb.mdb_txn_abort(txn.?);
    out_value.?.* = @ptrCast(block);
    out_length.?.* = payload_length;
    return OK;
}

fn deleteStorageKey(db: *Database, storage_key: []const u8) void {
    // Must copy the key because the caller's buffer may be deallocated.
    const key_copy = mallocOrDie(storage_key.len);
    @memcpy(key_copy[0..storage_key.len], storage_key);
    defer free(toFree(key_copy));

    var key_val = lmdb.Val{ .mv_size = storage_key.len, .mv_data = key_copy };
    var txn: ?*lmdb.Txn = null;
    if (lmdb.isError(lmdb.mdb_txn_begin(db.env, null, 0, &txn)) or txn == null) return;
    const rc = lmdb.mdb_del(txn.?, db.dbi, &key_val, null);
    if (lmdb.isError(rc)) {
        lmdb.mdb_txn_abort(txn.?);
        return;
    }
    _ = lmdb.mdb_txn_commit(txn.?);
}

fn deleteExport(
    handle: u64,
    key: ?*const anyopaque,
    key_length: usize,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const db = registryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid database handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    if (key_length == 0) {
        setError(out_error, "kvlite: invalid argument: key is required", .{});
        return INVALID_ARGUMENT;
    }
    if (key == null) {
        setError(out_error, "kvlite: invalid argument: non-empty input has a nil pointer", .{});
        return INVALID_ARGUMENT;
    }
    if (key_length > db.max_key_size) {
        setError(
            out_error,
            "kvlite: LMDB storage key is {d} bytes; maximum is {d}",
            .{ key_length, db.max_key_size },
        );
        return STORAGE_ERROR;
    }

    var key_buffer = KeyBuffer{};
    defer key_buffer.deinit();
    const storage_key = buildStorageKey(@ptrCast(key.?), key_length, key_buffer.get(key_length + 1));

    var key_val = lmdb.Val{ .mv_size = storage_key.len, .mv_data = storage_key.ptr };

    var txn: ?*lmdb.Txn = null;
    if (lmdb.isError(lmdb.mdb_txn_begin(db.env, null, 0, &txn)) or txn == null) {
        setError(out_error, "kvlite: LMDB: begin write transaction failed", .{});
        return STORAGE_ERROR;
    }
    const rc = lmdb.mdb_del(txn.?, db.dbi, &key_val, null);
    if (lmdb.isError(rc)) {
        if (rc == lmdb.NOTFOUND) {
            // Deleting a missing key succeeds (documented in kvlite.h); the
            // Go driver maps IsNotFound to nil the same way.
            lmdb.mdb_txn_abort(txn.?);
            return OK;
        }
        lmdb.mdb_txn_abort(txn.?);
        if (mapStatusFor(rc, out_error)) |status| return status;
        setError(out_error, "kvlite: LMDB delete: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
        return STORAGE_ERROR;
    }
    if (lmdb.isError(lmdb.mdb_txn_commit(txn.?))) {
        setError(out_error, "kvlite: LMDB: commit write transaction failed", .{});
        return STORAGE_ERROR;
    }
    return OK;
}

fn freeExport(pointer: ?*anyopaque) callconv(.c) void {
    free(pointer);
}

// ---------------------------------------------------------------------------
// Raw key operations (no envelopes, no TTL, no key mangling).
// These address the engine keyspace directly, used by trusted in-process
// bridges (e.g., TransportStore in the Go API).

fn rawPutExport(
    handle: u64,
    key: ?*const anyopaque,
    key_length: usize,
    value: ?*const anyopaque,
    value_length: usize,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const db = registryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid database handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    if (key_length == 0) {
        setError(out_error, "kvlite: invalid argument: key is required", .{});
        return INVALID_ARGUMENT;
    }
    if (key == null) {
        setError(out_error, "kvlite: invalid argument: non-empty input has a nil pointer", .{});
        return INVALID_ARGUMENT;
    }
    if (value_length > 0 and value == null) {
        setError(out_error, "kvlite: invalid argument: non-empty input has a nil pointer", .{});
        return INVALID_ARGUMENT;
    }
    if (key_length > db.max_key_size) {
        setError(
            out_error,
            "kvlite: LMDB storage key is {d} bytes; maximum is {d}",
            .{ key_length, db.max_key_size },
        );
        return STORAGE_ERROR;
    }

    var key_val = lmdb.Val{ .mv_size = key_length, .mv_data = @constCast(@ptrCast(key.?)) };
    var value_val = lmdb.Val{ .mv_size = value_length, .mv_data = @constCast(@ptrCast(value)) };

    var txn: ?*lmdb.Txn = null;
    if (lmdb.isError(lmdb.mdb_txn_begin(db.env, null, 0, &txn)) or txn == null) {
        setError(out_error, "kvlite: LMDB: begin write transaction failed", .{});
        return STORAGE_ERROR;
    }
    const rc = lmdb.mdb_put(txn.?, db.dbi, &key_val, &value_val, 0);
    if (lmdb.isError(rc)) {
        lmdb.mdb_txn_abort(txn.?);
        if (mapStatusFor(rc, out_error)) |status| return status;
        setError(out_error, "kvlite: LMDB raw_put: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
        return STORAGE_ERROR;
    }
    if (lmdb.isError(lmdb.mdb_txn_commit(txn.?))) {
        setError(out_error, "kvlite: LMDB: commit write transaction failed", .{});
        return STORAGE_ERROR;
    }
    return OK;
}

fn rawGetExport(
    handle: u64,
    key: ?*const anyopaque,
    key_length: usize,
    out_value: ?*?*anyopaque,
    out_length: ?*usize,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const db = registryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid database handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    if (out_value == null or out_length == null) {
        setError(out_error, "kvlite: invalid argument: output value and length are required", .{});
        return INVALID_ARGUMENT;
    }
    if (key_length == 0) {
        setError(out_error, "kvlite: invalid argument: key is required", .{});
        return INVALID_ARGUMENT;
    }
    if (key == null) {
        setError(out_error, "kvlite: invalid argument: non-empty input has a nil pointer", .{});
        return INVALID_ARGUMENT;
    }
    if (key_length > db.max_key_size) {
        setError(
            out_error,
            "kvlite: LMDB storage key is {d} bytes; maximum is {d}",
            .{ key_length, db.max_key_size },
        );
        return STORAGE_ERROR;
    }

    var key_val = lmdb.Val{ .mv_size = key_length, .mv_data = @constCast(@ptrCast(key.?)) };
    var data_val = lmdb.Val{ .mv_size = 0, .mv_data = null };

    var txn: ?*lmdb.Txn = null;
    if (lmdb.isError(lmdb.mdb_txn_begin(db.env, null, lmdb.RDONLY, &txn)) or txn == null) {
        setError(out_error, "kvlite: LMDB: begin read transaction failed", .{});
        return STORAGE_ERROR;
    }
    const rc = lmdb.mdb_get(txn.?, db.dbi, &key_val, &data_val);
    if (lmdb.isError(rc)) {
        lmdb.mdb_txn_abort(txn.?);
        if (rc == lmdb.NOTFOUND) {
            setError(out_error, "kvlite: key not found", .{});
            return NOT_FOUND;
        }
        if (mapStatusFor(rc, out_error)) |status| return status;
        setError(out_error, "kvlite: LMDB raw_get: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
        return STORAGE_ERROR;
    }

    const stored = @as([*]const u8, @ptrCast(data_val.mv_data.?))[0..data_val.mv_size];
    const block = mallocOrDie(@max(stored.len, 1));
    if (stored.len > 0) {
        @memcpy(block[0..stored.len], stored);
    }
    lmdb.mdb_txn_abort(txn.?);
    out_value.?.* = @ptrCast(block);
    out_length.?.* = stored.len;
    return OK;
}

fn rawDeleteExport(
    handle: u64,
    key: ?*const anyopaque,
    key_length: usize,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const db = registryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid database handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    if (key_length == 0) {
        setError(out_error, "kvlite: invalid argument: key is required", .{});
        return INVALID_ARGUMENT;
    }
    if (key == null) {
        setError(out_error, "kvlite: invalid argument: non-empty input has a nil pointer", .{});
        return INVALID_ARGUMENT;
    }
    if (key_length > db.max_key_size) {
        setError(
            out_error,
            "kvlite: LMDB storage key is {d} bytes; maximum is {d}",
            .{ key_length, db.max_key_size },
        );
        return STORAGE_ERROR;
    }

    var key_val = lmdb.Val{ .mv_size = key_length, .mv_data = @constCast(@ptrCast(key.?)) };

    var txn: ?*lmdb.Txn = null;
    if (lmdb.isError(lmdb.mdb_txn_begin(db.env, null, 0, &txn)) or txn == null) {
        setError(out_error, "kvlite: LMDB: begin write transaction failed", .{});
        return STORAGE_ERROR;
    }
    const rc = lmdb.mdb_del(txn.?, db.dbi, &key_val, null);
    if (lmdb.isError(rc)) {
        if (rc == lmdb.NOTFOUND) {
            // Deleting a missing key succeeds (documented in kvlite.h); the
            // Go driver maps IsNotFound to nil the same way.
            lmdb.mdb_txn_abort(txn.?);
            return OK;
        }
        lmdb.mdb_txn_abort(txn.?);
        if (mapStatusFor(rc, out_error)) |status| return status;
        setError(out_error, "kvlite: LMDB raw_delete: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
        return STORAGE_ERROR;
    }
    if (lmdb.isError(lmdb.mdb_txn_commit(txn.?))) {
        setError(out_error, "kvlite: LMDB: commit write transaction failed", .{});
        return STORAGE_ERROR;
    }
    return OK;
}

// Mutation struct for raw_apply (matches C ABI).
const Mutation = extern struct {
    key: ?*const anyopaque,
    key_length: usize,
    value: ?*const anyopaque,
    value_length: usize,
    is_delete: c_int,
};

fn rawApplyExport(
    handle: u64,
    mutations: ?*const Mutation,
    count: usize,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const db = registryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid database handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    if (count == 0) return OK;
    if (mutations == null) {
        setError(out_error, "kvlite: invalid argument: mutations array is required", .{});
        return INVALID_ARGUMENT;
    }

    var txn: ?*lmdb.Txn = null;
    if (lmdb.isError(lmdb.mdb_txn_begin(db.env, null, 0, &txn)) or txn == null) {
        setError(out_error, "kvlite: LMDB: begin write transaction failed", .{});
        return STORAGE_ERROR;
    }

    const mut_ptr = @as(*const Mutation, @ptrCast(mutations.?));
    var i: usize = 0;
    while (i < count) : (i += 1) {
        const mut = @as(*const Mutation, @ptrFromInt(@intFromPtr(mut_ptr) + i * @sizeOf(MultiGetResult)));
        if (mut.key_length == 0) {
            lmdb.mdb_txn_abort(txn.?);
            setError(out_error, "kvlite: invalid argument: mutation {d} has empty key", .{i});
            return INVALID_ARGUMENT;
        }
        if (mut.key == null) {
            lmdb.mdb_txn_abort(txn.?);
            setError(out_error, "kvlite: invalid argument: mutation {d} has nil key pointer", .{i});
            return INVALID_ARGUMENT;
        }
        if (mut.value_length > 0 and mut.value == null) {
            lmdb.mdb_txn_abort(txn.?);
            setError(out_error, "kvlite: invalid argument: mutation {d} has nil value pointer", .{i});
            return INVALID_ARGUMENT;
        }
        if (mut.key_length > db.max_key_size) {
            lmdb.mdb_txn_abort(txn.?);
            setError(
                out_error,
                "kvlite: mutation {d} key is {d} bytes; maximum is {d}",
                .{ i, mut.key_length, db.max_key_size },
            );
            return STORAGE_ERROR;
        }

        var key_val = lmdb.Val{ .mv_size = mut.key_length, .mv_data = @constCast(@ptrCast(mut.key.?)) };
        if (mut.is_delete != 0) {
            const rc = lmdb.mdb_del(txn.?, db.dbi, &key_val, null);
            if (lmdb.isError(rc)) {
                if (rc == lmdb.NOTFOUND) {
                    // Deleting a missing key succeeds (documented in kvlite.h); the
                    // Go driver maps IsNotFound to nil the same way.
                    lmdb.mdb_txn_abort(txn.?);
                    return OK;
                }
                lmdb.mdb_txn_abort(txn.?);
                if (mapStatusFor(rc, out_error)) |status| return status;
                setError(out_error, "kvlite: LMDB raw_apply delete: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
                return STORAGE_ERROR;
            }
        } else {
            var value_val = lmdb.Val{ .mv_size = mut.value_length, .mv_data = @constCast(@ptrCast(mut.value)) };
            const rc = lmdb.mdb_put(txn.?, db.dbi, &key_val, &value_val, 0);
            if (lmdb.isError(rc)) {
                lmdb.mdb_txn_abort(txn.?);
                if (mapStatusFor(rc, out_error)) |status| return status;
                setError(out_error, "kvlite: LMDB raw_apply put: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
                return STORAGE_ERROR;
            }
        }
    }

    if (lmdb.isError(lmdb.mdb_txn_commit(txn.?))) {
        setError(out_error, "kvlite: LMDB: commit write transaction failed", .{});
        return STORAGE_ERROR;
    }
    return OK;
}

// ---------------------------------------------------------------------------
// Prefix scans over the raw engine keyspace.
// Snapshot semantics: writes after scan_open are not visible.
// Cursor must be released with kvlite_raw_scan_close.

const ScanCursor = struct {
    cursor: ?*lmdb.Cursor,
    txn: ?*lmdb.Txn,
    prefix_len: usize,
    prefix: ?*u8,
    exhausted: bool,
    first_call: bool,
};

var scan_lock = std.atomic.Value(bool).init(false);

fn scanAcquire() void {
    while (scan_lock.swap(true, .acquire)) {
        std.atomic.spinLoopHint();
    }
}

fn scanRelease() void {
    scan_lock.store(false, .release);
}
var scan_next: u64 = 1;
var scan_items: ?[*]?*ScanCursor = null;
var scan_len: usize = 0;
var scan_cap: usize = 0;

fn scanInsert(sc: *ScanCursor) u64 {
    scanAcquire();
    defer scanRelease();
    const handle = scan_next;
    const slot = handle - 1;
    if (slot >= scan_cap) {
        const new_cap: usize = if (scan_cap == 0) 16 else scan_cap * 2;
        const byte_size = new_cap * @sizeOf(?*ScanCursor);
        const block = realloc(@ptrCast(scan_items), byte_size) orelse
            @panic("kvlite-zig-proto: out of memory growing the scan handle table");
        scan_items = @as([*]?*ScanCursor, @ptrCast(@alignCast(block)));
        scan_cap = new_cap;
    }
    scan_next += 1;
    scan_items.?[slot] = sc;
    if (handle > scan_len) scan_len = @as(usize, @intCast(handle));
    return handle;
}

fn scanLookup(handle: u64) ?*ScanCursor {
    scanAcquire();
    defer scanRelease();
    if (handle == 0 or handle > scan_len) return null;
    return scan_items.?[handle - 1];
}

fn scanRemove(handle: u64) ?*ScanCursor {
    scanAcquire();
    defer scanRelease();
    if (handle == 0 or handle > scan_len) return null;
    const sc = scan_items.?[handle - 1];
    if (sc != null) scan_items.?[handle - 1] = null;
    return sc;
}

fn rawScanOpenExport(
    handle: u64,
    prefix: ?*const anyopaque,
    prefix_length: usize,
    out_cursor: ?*u64,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const db = registryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid database handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    if (out_cursor == null) {
        setError(out_error, "kvlite: invalid argument: output cursor is required", .{});
        return INVALID_ARGUMENT;
    }
    if (prefix_length > 0 and prefix == null) {
        setError(out_error, "kvlite: invalid argument: non-empty prefix has nil pointer", .{});
        return INVALID_ARGUMENT;
    }
    if (prefix_length > db.max_key_size) {
        setError(
            out_error,
            "kvlite: scan prefix is {d} bytes; maximum is {d}",
            .{ prefix_length, db.max_key_size },
        );
        return STORAGE_ERROR;
    }

    var txn: ?*lmdb.Txn = null;
    if (lmdb.isError(lmdb.mdb_txn_begin(db.env, null, lmdb.RDONLY, &txn)) or txn == null) {
        setError(out_error, "kvlite: LMDB: begin read transaction failed", .{});
        return STORAGE_ERROR;
    }

    var cursor: ?*lmdb.Cursor = null;
    const rc_open = lmdb.mdb_cursor_open(txn.?, db.dbi, &cursor);
    if (lmdb.isError(rc_open)) {
        lmdb.mdb_txn_abort(txn.?);
        if (mapStatusFor(rc_open, out_error)) |status| return status;
        setError(out_error, "kvlite: LMDB cursor open: {s}", .{std.mem.span(lmdb.mdb_strerror(rc_open))});
        return STORAGE_ERROR;
    }

    // Position at prefix (or first if empty prefix).
    // We'll use SET_RANGE to find the first key >= prefix.
    // The caller iterates with raw_scan_next and checks prefix themselves.
    if (prefix_length > 0) {
        var key_val = lmdb.Val{ .mv_size = prefix_length, .mv_data = @constCast(@ptrCast(prefix.?)) };
        var data_val = lmdb.Val{ .mv_size = 0, .mv_data = null };
        const rc = lmdb.mdb_cursor_get(cursor.?, &key_val, &data_val, lmdb.SET_RANGE);
        if (lmdb.isError(rc) and rc != lmdb.NOTFOUND) {
            lmdb.mdb_cursor_close(cursor.?);
            lmdb.mdb_txn_abort(txn.?);
            if (mapStatusFor(rc, out_error)) |status| return status;
            setError(out_error, "kvlite: LMDB cursor position: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
            return STORAGE_ERROR;
        }
        // If NOTFOUND, cursor is at end; iteration will return NOT_FOUND immediately.
    } else {
        // Empty prefix: position at first key using FIRST
        var key_val = lmdb.Val{ .mv_size = 0, .mv_data = null };
        var data_val = lmdb.Val{ .mv_size = 0, .mv_data = null };
        const rc = lmdb.mdb_cursor_get(cursor.?, &key_val, &data_val, lmdb.FIRST);
        if (lmdb.isError(rc)) {
            lmdb.mdb_cursor_close(cursor.?);
            lmdb.mdb_txn_abort(txn.?);
            if (mapStatusFor(rc, out_error)) |status| return status;
            setError(out_error, "kvlite: LMDB cursor position: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
            return STORAGE_ERROR;
        }
    }

    // Store cursor and its read-only transaction together.
    var scan_prefix: ?*u8 = null;
    if (prefix_length > 0) {
        const buf = mallocOrDie(prefix_length);
        @memcpy(buf[0..prefix_length], @as([*]const u8, @ptrCast(prefix.?))[0..prefix_length]);
        scan_prefix = @ptrCast(buf);
    }
    const scan_db = @as(*ScanCursor, @ptrCast(@alignCast(mallocOrDie(@sizeOf(ScanCursor)))));
    scan_db.* = .{
        .cursor = cursor.?,
        .txn = txn.?,
        .prefix_len = prefix_length,
        .prefix = scan_prefix,
        .exhausted = false,
        .first_call = true,
    };
    out_cursor.?.* = scanInsert(scan_db);
    return OK;
}

fn rawScanNextExport(
    cursor_handle: u64,
    out_key: ?*?*anyopaque,
    out_key_length: ?*usize,
    out_value: ?*?*anyopaque,
    out_value_length: ?*usize,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const scan = scanLookup(cursor_handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid scan cursor handle {d}", .{cursor_handle});
        return INVALID_ARGUMENT;
    };
    if (out_key == null or out_key_length == null or out_value == null or out_value_length == null) {
        setError(out_error, "kvlite: invalid argument: output key/value and lengths are required", .{});
        return INVALID_ARGUMENT;
    }
    if (scan.exhausted) {
        setError(out_error, "kvlite: key not found", .{});
        return NOT_FOUND;
    }

    var key_val = lmdb.Val{ .mv_size = 0, .mv_data = null };
    var data_val = lmdb.Val{ .mv_size = 0, .mv_data = null };

    // First call uses GET_CURRENT since cursor is already positioned at first item.
    // Subsequent calls use NEXT.
    const op = if (scan.first_call) lmdb.GET_CURRENT else lmdb.NEXT;
    const rc = lmdb.mdb_cursor_get(scan.cursor.?, &key_val, &data_val, op);
    if (lmdb.isError(rc)) {
        if (rc == lmdb.NOTFOUND) {
            scan.exhausted = true;
            setError(out_error, "kvlite: key not found", .{});
            return NOT_FOUND;
        }
        if (mapStatusFor(rc, out_error)) |status| return status;
        setError(out_error, "kvlite: LMDB cursor get: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
        return STORAGE_ERROR;
    }

    // Check prefix match if prefix was provided.
    if (scan.prefix_len > 0) {
        const key_data = @as([*]const u8, @ptrCast(key_val.mv_data.?))[0..key_val.mv_size];
        if (key_data.len < scan.prefix_len) {
            scan.exhausted = true;
            setError(out_error, "kvlite: key not found", .{});
            return NOT_FOUND;
        }
        if (scan.prefix) |prefix_ptr| {
            const prefix_data = @as([*]u8, @ptrCast(prefix_ptr))[0..scan.prefix_len];
            if (!std.mem.eql(u8, key_data[0..scan.prefix_len], prefix_data)) {
                scan.exhausted = true;
                setError(out_error, "kvlite: key not found", .{});
                return NOT_FOUND;
            }
        }
    }

    // Copy key
    const key_block = mallocOrDie(key_val.mv_size);
    @memcpy(key_block[0..key_val.mv_size], @as([*]const u8, @ptrCast(key_val.mv_data.?))[0..key_val.mv_size]);
    out_key.?.* = @ptrCast(key_block);
    out_key_length.?.* = key_val.mv_size;

    // Copy value
    const val_block = mallocOrDie(@max(data_val.mv_size, 1));
    if (data_val.mv_size > 0) {
        @memcpy(val_block[0..data_val.mv_size], @as([*]const u8, @ptrCast(data_val.mv_data.?))[0..data_val.mv_size]);
    }
    out_value.?.* = @ptrCast(val_block);
    out_value_length.?.* = data_val.mv_size;

    return OK;
}

fn rawScanCloseExport(
    cursor_handle: u64,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const scan = scanRemove(cursor_handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid scan cursor handle {d}", .{cursor_handle});
        return INVALID_ARGUMENT;
    };
    if (scan.cursor) |c| {
        lmdb.mdb_cursor_close(c);
    }
    if (scan.txn) |t| {
        lmdb.mdb_txn_abort(t);
    }
    if (scan.prefix) |p| {
        free(p);
    }
    free(toFree(@ptrCast(scan)));
    return OK;
}

// MultiGetResult struct for raw_multi_get (matches C ABI).
const MultiGetResult = extern struct {
    key: ?*const anyopaque,
    key_length: usize,
    value: ?*anyopaque,    // allocated by host; release with kvlite_free
    value_length: usize,
    status: c_int,         // KVLITE_OK, KVLITE_NOT_FOUND, or KVLITE_STORAGE_ERROR
};

const MultiPutEntry = extern struct {
    key: ?*const anyopaque,
    key_length: usize,
    value: ?*const anyopaque,
    value_length: usize,
};

// A contiguous key-range assigned to one worker thread during raw_multi_get.
const ReadBatch = struct {
    db: *Database,
    results: *MultiGetResult,
    start: usize,
    end: usize,
    done: *std.atomic.Value(usize),
};

fn readWorker(
    db: *Database,
    results: *MultiGetResult,
    start: usize,
    end: usize,
    done: *std.atomic.Value(usize),
) void {
    var txn: ?*lmdb.Txn = null;
    if (lmdb.isError(lmdb.mdb_txn_begin(db.env, null, lmdb.NOTLS | lmdb.RDONLY, &txn)) or txn == null) {
        var i = start;
        while (i < end) : (i += 1) {
            const r = @as(*MultiGetResult, @ptrFromInt(@intFromPtr(results) + i * @sizeOf(MultiGetResult)));
            r.status = STORAGE_ERROR;
            r.value = null;
            r.value_length = 0;
        }
        _ = done.fetchAdd(1, .release);
        return;
    }

    var i = start;
    while (i < end) : (i += 1) {
        const r = @as(*MultiGetResult, @ptrFromInt(@intFromPtr(results) + i * @sizeOf(MultiGetResult)));
        if (r.key_length == 0 or r.key == null) {
            r.status = STORAGE_ERROR;
            r.value = null;
            r.value_length = 0;
            continue;
        }

        var key_val = lmdb.Val{ .mv_size = r.key_length, .mv_data = @constCast(@ptrCast(r.key.?)) };
        var data_val = lmdb.Val{ .mv_size = 0, .mv_data = null };
        const rc = lmdb.mdb_get(txn.?, db.dbi, &key_val, &data_val);
        if (lmdb.isError(rc)) {
            if (rc == lmdb.NOTFOUND) {
                r.status = NOT_FOUND;
                r.value = null;
                r.value_length = 0;
            } else {
                r.status = STORAGE_ERROR;
                r.value = null;
                r.value_length = 0;
            }
        } else {
            const payload_length = data_val.mv_size;
            const block = mallocOrDie(@max(payload_length, 1));
            if (payload_length > 0) {
                @memcpy(block[0..payload_length], @as([*]const u8, @ptrCast(data_val.mv_data.?))[0..payload_length]);
            }
            r.value = @ptrCast(block);
            r.value_length = payload_length;
            r.status = OK;
        }
    }

    lmdb.mdb_txn_abort(txn.?);
    _ = done.fetchAdd(1, .release);
}

fn rawMultiGetExport(
    handle: u64,
    results: ?*MultiGetResult,
    count: usize,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const db = registryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid database handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    if (count == 0) return OK;
    if (results == null) {
        setError(out_error, "kvlite: invalid argument: results array is required", .{});
        return INVALID_ARGUMENT;
    }

    // Bounds check: refuse absurdly large counts that could overflow pointer math.
    if (count > 1_000_000) {
        setError(out_error, "kvlite: invalid argument: count {d} exceeds maximum 1,000,000", .{count});
        return INVALID_ARGUMENT;
    }

    const mut_ptr = @as(*MultiGetResult, @ptrCast(@constCast(results.?)));
    var i: usize = 0;
    while (i < count) : (i += 1) {
        const r = @as(*MultiGetResult, @ptrFromInt(@intFromPtr(mut_ptr) + i * @sizeOf(MultiGetResult)));
        if (r.key_length == 0 or r.key == null) {
            // Mark invalid entries so the caller can see they were skipped.
            r.status = STORAGE_ERROR;
            r.value = null;
            r.value_length = 0;
            continue;
        }
        if (r.key_length > db.max_key_size) {
            setError(out_error, "kvlite: result[{d}] key is {d} bytes; maximum is {d}", .{ i, r.key_length, db.max_key_size });
            return STORAGE_ERROR;
        }
    }

    // Determine thread count: cap at 4, use 1 thread for small counts.
    const max_threads: usize = 4;
    const thread_count: usize = if (count <= max_threads) 1 else max_threads;

    var done = std.atomic.Value(usize).init(0);

    // Split key ranges across worker threads.
    var base: usize = 0;
    var remaining: usize = count;
    var t: usize = 0;
    while (t < thread_count) : (t += 1) {
        if (remaining == 0) break;
        const per_thread = (remaining + (thread_count - t) - 1) / (thread_count - t);
        const start = base;
        const end = base + per_thread;

        // Spawn worker; if spawning fails, fall back to inline execution.
        const spawn_result = std.Thread.spawn(.{}, readWorker, .{
            db,
            results.?,
            start,
            end,
            &done,
        });
        if (spawn_result) |_| {
            // Thread running; nothing to do here.
        } else |_| {
            // Spawn failed: process this range inline.
            readWorker(db, results.?, start, end, &done);
        }

        base = end;
        remaining -= per_thread;
    }

    // Spin-wait for all threads to signal completion via the atomic counter.
    // Do NOT key off result.status (OK == 0, so status == 0 means done, not pending).
    while (done.load(.acquire) < count) {
        std.atomic.spinLoopHint();
    }

    // Check for storage errors after all threads have completed.
    i = 0;
    while (i < count) : (i += 1) {
        const r = @as(*MultiGetResult, @ptrFromInt(@intFromPtr(mut_ptr) + i * @sizeOf(MultiGetResult)));
        if (r.status == STORAGE_ERROR) {
            setError(out_error, "kvlite: result[{d}] storage error", .{i});
            return STORAGE_ERROR;
        }
    }

    return OK;
}

fn rawMultiPutExport(
    handle: u64,
    mutations: ?*const Mutation,
    count: usize,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const db = registryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid database handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    if (count == 0) return OK;
    if (mutations == null) {
        setError(out_error, "kvlite: invalid argument: mutations array is required", .{});
        return INVALID_ARGUMENT;
    }

    var txn: ?*lmdb.Txn = null;
    if (lmdb.isError(lmdb.mdb_txn_begin(db.env, null, 0, &txn)) or txn == null) {
        setError(out_error, "kvlite: LMDB: begin write transaction failed", .{});
        return STORAGE_ERROR;
    }

    const mut_ptr = @as(*const Mutation, @ptrCast(mutations.?));
    var i: usize = 0;
    while (i < count) : (i += 1) {
        const mut = @as(*const Mutation, @ptrFromInt(@intFromPtr(mut_ptr) + i * @sizeOf(MultiGetResult)));
        if (mut.key_length == 0) {
            lmdb.mdb_txn_abort(txn.?);
            setError(out_error, "kvlite: invalid argument: mutation {d} has empty key", .{i});
            return INVALID_ARGUMENT;
        }
        if (mut.key == null) {
            lmdb.mdb_txn_abort(txn.?);
            setError(out_error, "kvlite: invalid argument: mutation {d} has nil key pointer", .{i});
            return INVALID_ARGUMENT;
        }
        if (mut.value_length > 0 and mut.value == null) {
            lmdb.mdb_txn_abort(txn.?);
            setError(out_error, "kvlite: invalid argument: mutation {d} has nil value pointer", .{i});
            return INVALID_ARGUMENT;
        }
        if (mut.key_length > db.max_key_size) {
            lmdb.mdb_txn_abort(txn.?);
            setError(
                out_error,
                "kvlite: mutation {d} key is {d} bytes; maximum is {d}",
                .{ i, mut.key_length, db.max_key_size },
            );
            return STORAGE_ERROR;
        }

        var key_val = lmdb.Val{ .mv_size = mut.key_length, .mv_data = @constCast(@ptrCast(mut.key.?)) };
        if (mut.is_delete != 0) {
            const rc = lmdb.mdb_del(txn.?, db.dbi, &key_val, null);
            if (lmdb.isError(rc)) {
                if (rc == lmdb.NOTFOUND) {
                    // Deleting a missing key succeeds (documented in kvlite.h); the
                    // Go driver maps IsNotFound to nil the same way.
                    lmdb.mdb_txn_abort(txn.?);
                    return OK;
                }
                lmdb.mdb_txn_abort(txn.?);
                if (mapStatusFor(rc, out_error)) |status| return status;
                setError(out_error, "kvlite: LMDB raw_apply delete: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
                return STORAGE_ERROR;
            }
        } else {
            var value_val = lmdb.Val{ .mv_size = mut.value_length, .mv_data = @constCast(@ptrCast(mut.value)) };
            const rc = lmdb.mdb_put(txn.?, db.dbi, &key_val, &value_val, 0);
            if (lmdb.isError(rc)) {
                lmdb.mdb_txn_abort(txn.?);
                if (mapStatusFor(rc, out_error)) |status| return status;
                setError(out_error, "kvlite: LMDB raw_apply put: {s}", .{std.mem.span(lmdb.mdb_strerror(rc))});
                return STORAGE_ERROR;
            }
        }
    }

    if (lmdb.isError(lmdb.mdb_txn_commit(txn.?))) {
        setError(out_error, "kvlite: LMDB: commit write transaction failed", .{});
        return STORAGE_ERROR;
    }
    return OK;
}

// ---------------------------------------------------------------------------
// Ext plugin host (ext_io, github.com/webong/ext/pkg/ext-zig).
//
// KVLite loads plugins through ext's host infrastructure. A plugin is a C
// shared library exporting the ext_plugin.h ABI (open/call/close), loaded
// via ext.host.CShared and called through ext.host.Session. The KVLite
// wire protocol rides inside ext_plugin_call's JSON request/response:
//
//   Request:  {"operation":"get","payload":{"key":"base64","collection":"users"}}
//   Response: {"value":"base64","found":true}  or  {"error":{"code":"not_found","message":"..."}}
//
// Operations: get, put, delete, scan, multi_get, multi_put, health
//
// KVLite keeps its own C ABI contract (capi/kvlite.h) as the public surface.
// ext is the plugin loading layer: callers open a KVLite directory with
// kvlite_open*, then optionally load plugins with kvlite_plugin_load* and
// call into them with kvlite_plugin_call*.

const kv_plugin_contract = ext.wire.Ref{
    .name = "kvlite.kv",
    .version = "v1",
};

// Response types for plugin calls, encoded via ext.wire.encode.
const PluginErrorBody = struct {
    code: []const u8,
    message: []const u8,
};
const PluginErrorResponse = struct {
    err: PluginErrorBody,
};

fn pluginLoadExport(
    path: ?[*:0]const u8,
    out_handle: ?*u64,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    if (path == null or out_handle == null) {
        setError(out_error, "kvlite: invalid argument: path and out_handle are required", .{});
        return INVALID_ARGUMENT;
    }
    const path_slice = std.mem.span(path.?);

    const library = ext.host.CShared.open(path_slice) catch |err| {
        setError(out_error, "kvlite: plugin load failed: {s}", .{@errorName(err)});
        return STORAGE_ERROR;
    };

    const ps = std.heap.page_allocator.create(PluginSession) catch |err| {
        setError(out_error, "kvlite: out of memory allocating plugin session: {s}", .{@errorName(err)});
        return STORAGE_ERROR;
    };
    ps.* = .{ .library = library, .session = undefined };

    const session = ext.host.Session.open(
        std.heap.page_allocator,
        .{
            .identity = .{ .id = "kvlite.plugin", .revision = "v1" },
            .contracts = &.{
                .{ .name = "kvlite.kv", .version = "v1", .operations = &.{
                    .{ .name = "get", .surface = "observation" },
                    .{ .name = "put", .surface = "mutation" },
                    .{ .name = "delete", .surface = "mutation" },
                    .{ .name = "scan", .surface = "observation" },
                    .{ .name = "multi_get", .surface = "observation" },
                    .{ .name = "multi_put", .surface = "mutation" },
                    .{ .name = "health", .surface = "observation" },
                }},
            },
        },
        pluginVerify,
        .{ .context = @ptrCast(@alignCast(ps)), .connect = pluginConnect },
        pluginAuthorize,
    ) catch |err| {
        std.heap.page_allocator.destroy(ps);
        setError(out_error, "kvlite: plugin handshake failed: {s}", .{@errorName(err)});
        return STORAGE_ERROR;
    };
    ps.session = session;

    const handle = pluginRegistryInsert(ps);
    out_handle.?.* = handle;
    return OK;
}

fn pluginConnect(ctx: *anyopaque) !ext.host.Backend {
    const self: *PluginSession = @ptrCast(@alignCast(ctx));
    return self.library.backend();
}

fn pluginVerify(d: ext.wire.Descriptor) !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    try ext.wire.validate(arena.allocator(), d);
}

fn pluginAuthorize(_: ext.wire.Request) !void {}

fn pluginCallExport(
    handle: u64,
    operation: ?[*:0]const u8,
    request: ?[*:0]const u8,
    out_response: ?*[*:0]u8,
    out_response_length: ?*usize,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const ps = pluginRegistryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid plugin handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    if (operation == null or request == null or out_response == null or out_response_length == null) {
        setError(out_error, "kvlite: invalid argument: operation, request, out_response, and out_response_length are required", .{});
        return INVALID_ARGUMENT;
    }
    const op = std.mem.span(operation.?);
    const request_json = std.mem.span(request.?);

    const payload = ext.wire.decode(std.heap.page_allocator, request_json) catch |err| {
        setError(out_error, "kvlite: invalid request JSON: {s}", .{@errorName(err)});
        return INVALID_ARGUMENT;
    };

    const result = ps.session.callRaw(
        std.heap.page_allocator,
        kv_plugin_contract,
        op,
        payload,
        5_000_000_000, // 5 second timeout
    ) catch |err| {
        setError(out_error, "kvlite: plugin call failed: {s}", .{@errorName(err)});
        return STORAGE_ERROR;
    };

    const response_json = switch (result) {
        .remote => |e| blk: {
            const err_resp = PluginErrorResponse{ .err = .{ .code = e.code, .message = e.message } };
            break :blk ext.wire.encode(std.heap.page_allocator, err_resp) catch |enc_err| {
                setError(out_error, "kvlite: failed to encode remote error: {s}", .{@errorName(enc_err)});
                return STORAGE_ERROR;
            };
        },
        .value => |v| blk: {
            break :blk ext.wire.encode(std.heap.page_allocator, v) catch |enc_err| {
                setError(out_error, "kvlite: failed to encode response: {s}", .{@errorName(enc_err)});
                return STORAGE_ERROR;
            };
        },
    };

    const block = mallocOrDie(@max(response_json.len, 1));
    @memcpy(block[0..response_json.len], response_json);
    if (out_response) |resp_ptr| {
        const resp_slot: *[*:0]u8 = @alignCast(@ptrCast(resp_ptr));
        resp_slot.* = @alignCast(@ptrCast(block));
    }
    out_response_length.?.* = response_json.len;
    return OK;
}

fn pluginCloseExport(
    handle: u64,
    out_error: ?*?[*:0]u8,
) callconv(.c) c_int {
    const ps = pluginRegistryLookup(handle) orelse {
        setError(out_error, "kvlite: invalid argument: invalid plugin handle {d}", .{handle});
        return INVALID_ARGUMENT;
    };
    pluginRegistryRemove(handle);
    _ = ps;
    return OK;
}

// ---------------------------------------------------------------------------
// C ABI exports.

comptime {
    @export(&abiVersionExport, .{ .name = "kvlite_abi_version" });
    @export(&openExport, .{ .name = "kvlite_open" });
    @export(&openWithBackendExport, .{ .name = "kvlite_open_with_backend" });
    @export(&openWithDriverExport, .{ .name = "kvlite_open_with_driver" });
    @export(&closeExport, .{ .name = "kvlite_close" });
    @export(&putExport, .{ .name = "kvlite_put" });
    @export(&getExport, .{ .name = "kvlite_get" });
    @export(&deleteExport, .{ .name = "kvlite_delete" });
    @export(&freeExport, .{ .name = "kvlite_free" });
    @export(&rawPutExport, .{ .name = "kvlite_raw_put" });
    @export(&rawGetExport, .{ .name = "kvlite_raw_get" });
    @export(&rawDeleteExport, .{ .name = "kvlite_raw_delete" });
    @export(&rawApplyExport, .{ .name = "kvlite_raw_apply" });
    @export(&rawScanOpenExport, .{ .name = "kvlite_raw_scan_open" });
    @export(&rawScanNextExport, .{ .name = "kvlite_raw_scan_next" });
    @export(&rawScanCloseExport, .{ .name = "kvlite_raw_scan_close" });
    @export(&rawMultiGetExport, .{ .name = "kvlite_raw_multi_get" });
    @export(&rawMultiPutExport, .{ .name = "kvlite_raw_multi_put" });
    @export(&pluginLoadExport, .{ .name = "kvlite_plugin_load" });
    @export(&pluginCallExport, .{ .name = "kvlite_plugin_call" });
    @export(&pluginCloseExport, .{ .name = "kvlite_plugin_close" });
}