# LMDB vendored source notice

`lmdb.h`, `mdb.c`, and `midl.c` in this directory are the LMDB 0.9.35 C
sources exactly as bundled by `github.com/PowerDNS/lmdb-go v1.9.4`, the pinned
binding that the existing Go host's `extensions/lmdb` driver compiles through
cgo. Vendoring the identical source here keeps the engine implementation the
same on both sides of the ADR 0001 experiment: only the host language and
interface crossing differ, not the storage engine code.

LMDB is licensed under the OpenLDAP Public License 2.8; see
`LICENSE.mdb.md` (copied from lmdb-go, which carries the same license for its
bundled copy). The KVLite LMDB extension manifest already declares
`BSD-3-Clause AND OLDAP-2.8` for this engine.

This copy is part of the non-production ADR 0001 Zig prototype. It is not a
release artifact, is not built by the Makefile, and must not be reused as a
general vendored dependency.
