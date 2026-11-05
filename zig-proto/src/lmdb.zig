// Minimal LMDB C bindings for the ADR 0001 Zig prototype.
//
// Only the calls the prototype subset needs: one environment, one root
// database, single-record get/put/delete in short transactions. The symbols
// resolve against the vendored LMDB 0.9.35 sources in ../third_party/lmdb,
// the same engine source the Go host's cgo driver compiles.

const std = @import("std");

pub const Env = opaque {};
pub const Txn = opaque {};
pub const DBI = c_uint;

pub const Val = extern struct {
    mv_size: usize,
    mv_data: ?*anyopaque,
};

// Return codes (lmdb.h).
pub const SUCCESS: c_int = 0;
pub const NOTFOUND: c_int = -30798;
pub const KEYEXIST: c_int = -30799;
pub const BAD_VALSIZE: c_int = -30795;
pub const MAP_FULL: c_int = -30792;

// Environment flags.
pub const NOTLS: c_uint = 0x100000;

// Transaction flags.
pub const RDONLY: c_uint = 0x20;

pub extern fn mdb_env_create(env: *?*Env) c_int;
pub extern fn mdb_env_set_mapsize(env: *Env, size: usize) c_int;
pub extern fn mdb_env_set_maxreaders(env: *Env, readers: c_uint) c_int;
pub extern fn mdb_env_open(env: *Env, path: [*:0]const u8, flags: c_uint, mode: c_uint) c_int;
pub extern fn mdb_env_close(env: *Env) void;
pub extern fn mdb_env_get_maxkeysize(env: *Env) c_int;

pub extern fn mdb_txn_begin(env: *Env, parent: ?*Txn, flags: c_uint, txn: *?*Txn) c_int;
pub extern fn mdb_txn_commit(txn: *Txn) c_int;
pub extern fn mdb_txn_abort(txn: *Txn) void;

pub extern fn mdb_dbi_open(txn: *Txn, name: ?[*:0]const u8, flags: c_uint, dbi: *DBI) c_int;

pub extern fn mdb_get(txn: *Txn, dbi: DBI, key: *Val, data: *Val) c_int;
pub extern fn mdb_put(txn: *Txn, dbi: DBI, key: *Val, data: *Val, flags: c_uint) c_int;
pub extern fn mdb_del(txn: *Txn, dbi: DBI, key: *Val, data: ?*Val) c_int;

// Cursor operations.
pub const Cursor = opaque {};

pub extern fn mdb_cursor_open(txn: *Txn, dbi: DBI, cursor: *?*Cursor) c_int;
pub extern fn mdb_cursor_close(cursor: *Cursor) void;
pub extern fn mdb_cursor_get(cursor: *Cursor, key: *Val, data: *Val, op: c_uint) c_int;
pub extern fn mdb_cursor_put(cursor: *Cursor, key: *Val, data: *Val, flags: c_uint) c_int;
pub extern fn mdb_cursor_del(cursor: *Cursor, flags: c_uint) c_int;
pub extern fn mdb_cursor_count(cursor: *Cursor, count: *usize) c_int;

// Cursor operations.
pub const FIRST: c_uint = 0x40000000;
pub const FIRST_DUP: c_uint = 0x02;
pub const GET_BOTH: c_uint = 0x03;
pub const GET_BOTH_RANGE: c_uint = 0x04;
pub const GET_CURRENT: c_uint = 0x05;
pub const GET_MULTIPLE: c_uint = 0x06;
pub const LAST: c_uint = 0x40000001;
pub const LAST_DUP: c_uint = 0x07;
pub const NEXT: c_uint = 0x08;
pub const NEXT_DUP: c_uint = 0x09;
pub const NEXT_MULTIPLE: c_uint = 0x0A;
pub const NEXT_NODUP: c_uint = 0x0B;
pub const PREV: c_uint = 0x0C;
pub const PREV_DUP: c_uint = 0x0D;
pub const PREV_MULTIPLE: c_uint = 0x0E;
pub const PREV_NODUP: c_uint = 0x0F;
pub const SET: c_uint = 0x10;
pub const SET_KEY: c_uint = 0x11;
pub const SET_RANGE: c_uint = 0x12;

pub extern fn mdb_strerror(rc: c_int) [*:0]const u8;

pub fn isError(rc: c_int) bool {
    return rc != SUCCESS;
}
