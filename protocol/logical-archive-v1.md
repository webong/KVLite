# KVLite logical archive v1

A logical archive is UTF-8 newline-delimited JSON. It is independent of
RocksDB, LevelDB, Berkeley DB, and the other physical engine formats. Each
object occupies one line. The first line is a header, followed by zero or more
data records, then one end marker. No content may follow the end marker.

```json
{"type":"header","format":"kvlite-logical","version":1}
{"type":"value","key":"dXNlcg==","codec":"bytes","payload":"SGVsbG8="}
{"type":"end","records":1,"sha256":"..."}
```

`key`, `field`, `member`, and `payload` are standard base64-encoded byte
strings, not UTF-8 key strings. Their omission represents empty bytes. Codec
names are nonempty UTF-8 strings; import preserves unknown codec names without
decoding their payloads. `expires_at_ns` is an optional quoted decimal Unix
nanosecond timestamp (`"0"`/omission means no expiry). It is a string so JSON
clients with IEEE-754 numbers can preserve all 64 bits.

Data record types:

| Type | Fields besides `type` and `key` | Meaning |
| --- | --- | --- |
| `value` | `codec`, `payload`, optional `expires_at_ns` | One scalar value |
| `hash` | `field`, `codec`, `payload`, optional `expires_at_ns` | One hash field |
| `set` | `member` | One set member |
| `list` | `items` array of `{codec, payload, expires_at_ns?}` | One ordered list, including an empty list |
| `collection_ttl` | `expires_at_ns` | Expiry of a hash, set, or list as a whole |

The end marker's `records` is the number of data-record lines. Its `sha256` is
the lowercase hex SHA-256 digest of the exact bytes of every data-record line
in order, including each line terminator, but excluding the header and end
marker. Writers may choose JSON field order and whitespace; import verifies
their bytes, not a Go-specific re-serialization.

Each scalar/list key appears once; a hash or set may have multiple records
with distinct fields/members. A logical key cannot mix types. Collection TTL
must refer to a collection in the same archive. Expired data is omitted on
export and skipped on import using the absolute expiry, so a delayed import
cannot revive it.

Import requires an empty embedded destination. It validates the complete
archive and checksum before writing, then commits bounded batches. Any failure
after writing begins may leave a partial destination; retry into a
fresh empty path. Remote import/export and a cross-client snapshot protocol
are not part of v1.
