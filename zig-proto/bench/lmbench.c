/*
 * lmbench — direct-engine baseline for the ADR 0001 comparison.
 *
 * Talks to the vendored LMDB 0.9.35 sources directly (no C ABI host, no
 * binding), using the same engine settings as both hosts: flags MDB_NOTLS,
 * mode 0600, 16 GiB virtual map, root database handle, one transaction per
 * operation.
 *
 * Write mode --put-mode:
 *   minimal    one mdb_put of the raw key/value per operation (what the Zig
 *              prototype's put does above the engine)
 *   core-like  the Go core's scalar-put engine pattern: point lookups of the
 *              three scalar storage keys plus two collection-prefix scans,
 *              then one transaction that deletes found old records and puts
 *              the new value envelope
 *
 * Reads and deletes always decode the same envelope shape both hosts store.
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <unistd.h>
#include "lmdb.h"
#include "workloads.h"

#define MAP_SIZE ((size_t)16 << 30)

typedef struct {
    MDB_env *env;
    MDB_dbi dbi;
    int put_mode_core_like;
} lmdb_ctx;

static int lmdb_check(int rc) {
    return rc == MDB_SUCCESS ? 0 : 1;
}

static void make_value_key(const char *key, size_t key_len, unsigned char *out) {
    out[0] = 1; /* kindValue */
    memcpy(out + 1, key, key_len);
}

static void namespace_prefix(unsigned char kind, const char *key, size_t key_len,
                             unsigned char *out, size_t *out_len) {
    out[0] = kind;
    uint32_t length = (uint32_t)key_len;
    out[1] = (unsigned char)(length >> 24);
    out[2] = (unsigned char)(length >> 16);
    out[3] = (unsigned char)(length >> 8);
    out[4] = (unsigned char)length;
    memcpy(out + 5, key, key_len);
    *out_len = 5 + key_len;
}

static void build_envelope(unsigned char *out, size_t *out_len, const char *value,
                           size_t value_len) {
    /* magic KVL1, expiry 0, codec "bytes", payload */
    memcpy(out, "KVL1", 4);
    memset(out + 4, 0, 8);
    out[12] = 0;
    out[13] = 5;
    memcpy(out + 14, "bytes", 5);
    if (value_len > 0) memcpy(out + 19, value, value_len);
    *out_len = 19 + value_len;
}

static int lmdb_open(void *ctx_ptr, const char *path, const char *driver) {
    (void)driver; /* the direct baseline has one engine */
    lmdb_ctx *ctx = (lmdb_ctx *)ctx_ptr;
    if (mdb_env_create(&ctx->env) != MDB_SUCCESS) return 1;
    if (mdb_env_set_mapsize(ctx->env, MAP_SIZE) != MDB_SUCCESS) return 1;
    if (mdb_env_open(ctx->env, path, MDB_NOTLS, 0600) != MDB_SUCCESS) return 1;
    MDB_txn *txn = NULL;
    if (mdb_txn_begin(ctx->env, NULL, MDB_RDONLY, &txn) != MDB_SUCCESS) return 1;
    if (mdb_dbi_open(txn, NULL, 0, &ctx->dbi) != MDB_SUCCESS) {
        mdb_txn_abort(txn);
        return 1;
    }
    mdb_txn_commit(txn);
    return 0;
}

static int lmdb_put(void *ctx_ptr, const char *key, size_t key_len, const char *value,
                    size_t value_len) {
    lmdb_ctx *ctx = (lmdb_ctx *)ctx_ptr;
    size_t max_key = 1 + key_len + 5; /* storage key or prefix, both bounded */
    unsigned char *skey = malloc(max_key);
    unsigned char *prefix = malloc(max_key);

    int rc = 0;
    if (ctx->put_mode_core_like) {
        /* Point lookups for the three scalar representations. */
        MDB_txn *peek = NULL;
        if (mdb_txn_begin(ctx->env, NULL, MDB_RDONLY, &peek) != MDB_SUCCESS) {
            free(skey);
            free(prefix);
            return 1;
        }
        static const unsigned char kinds[3] = {1, 4, 5}; /* value, list, collection TTL */
        for (int i = 0; i < 3; i++) {
            unsigned char probe[1024];
            probe[0] = kinds[i];
            memcpy(probe + 1, key, key_len);
            MDB_val probe_key = {key_len + 1, probe};
            MDB_val probe_val = {0, NULL};
            rc |= mdb_get(peek, ctx->dbi, &probe_key, &probe_val);
        }
        /* Prefix scans for the two collection namespaces. */
        for (int i = 0; i < 2; i++) {
            size_t prefix_len = 0;
            namespace_prefix(i == 0 ? 2 : 3, key, key_len, prefix, &prefix_len);
            MDB_cursor *cursor = NULL;
            if (mdb_cursor_open(peek, ctx->dbi, &cursor) == MDB_SUCCESS) {
                MDB_val cursor_key = {prefix_len, prefix};
                MDB_val cursor_val = {0, NULL};
                if (mdb_cursor_get(cursor, &cursor_key, &cursor_val, MDB_SET_RANGE) == MDB_SUCCESS &&
                    cursor_key.mv_size >= prefix_len &&
                    memcmp(cursor_key.mv_data, prefix, prefix_len) == 0) {
                    rc |= 0; /* found an old collection record */
                }
                mdb_cursor_close(cursor);
            }
        }
        mdb_txn_abort(peek);
        if (rc != 0 && rc != MDB_NOTFOUND) {
            free(skey);
            free(prefix);
            return 1;
        }
    }

    MDB_txn *txn = NULL;
    if (mdb_txn_begin(ctx->env, NULL, 0, &txn) != MDB_SUCCESS) {
        free(skey);
        free(prefix);
        return 1;
    }
    make_value_key(key, key_len, skey);
    MDB_val key_val = {key_len + 1, skey};
    size_t envelope_len = 0;
    unsigned char *envelope = malloc(19 + value_len);
    build_envelope(envelope, &envelope_len, value, value_len);
    MDB_val value_val = {envelope_len, envelope};
    if (ctx->put_mode_core_like) {
        /* Replace the old scalar record and stale TTL record atomically. */
        rc |= mdb_del(txn, ctx->dbi, &key_val, NULL);
        unsigned char ttl_key[1024];
        size_t ttl_len = 0;
        namespace_prefix(5, key, key_len, ttl_key, &ttl_len);
        MDB_val ttl_val_key = {ttl_len, ttl_key};
        rc |= mdb_del(txn, ctx->dbi, &ttl_val_key, NULL);
    }
    rc |= mdb_put(txn, ctx->dbi, &key_val, &value_val, 0);
    free(envelope);
    free(skey);
    free(prefix);
    if (rc != MDB_SUCCESS && rc != MDB_NOTFOUND) {
        mdb_txn_abort(txn);
        return 1;
    }
    if (mdb_txn_commit(txn) != MDB_SUCCESS) return 1;
    return 0;
}

static int lmdb_get(void *ctx_ptr, const char *key, size_t key_len, char **out_value,
                    size_t *out_len) {
    lmdb_ctx *ctx = (lmdb_ctx *)ctx_ptr;
    unsigned char skey[2048];
    if (key_len + 1 > sizeof(skey)) return 1;
    make_value_key(key, key_len, skey);
    MDB_txn *txn = NULL;
    if (mdb_txn_begin(ctx->env, NULL, MDB_RDONLY, &txn) != MDB_SUCCESS) return 1;
    MDB_val key_val = {key_len + 1, skey};
    MDB_val data = {0, NULL};
    int rc = mdb_get(txn, ctx->dbi, &key_val, &data);
    if (rc != MDB_SUCCESS) {
        mdb_txn_abort(txn);
        return rc == MDB_NOTFOUND ? 1 : 3;
    }
    const unsigned char *stored = (const unsigned char *)data.mv_data;
    if (data.mv_size < 19 || memcmp(stored, "KVL1", 4) != 0) {
        mdb_txn_abort(txn);
        return 3;
    }
    size_t codec_len = ((size_t)stored[12] << 8) | stored[13];
    if (data.mv_size < 14 + codec_len) {
        mdb_txn_abort(txn);
        return 3;
    }
    size_t payload_len = data.mv_size - 14 - codec_len;
    unsigned char *copy = malloc(payload_len > 0 ? payload_len : 1);
    if (payload_len > 0) memcpy(copy, stored + 14 + codec_len, payload_len);
    mdb_txn_abort(txn);
    *out_value = (char *)copy;
    *out_len = payload_len;
    return 0;
}

static int lmdb_del(void *ctx_ptr, const char *key, size_t key_len) {
    lmdb_ctx *ctx = (lmdb_ctx *)ctx_ptr;
    unsigned char skey[2048];
    if (key_len + 1 > sizeof(skey)) return 1;
    make_value_key(key, key_len, skey);
    MDB_txn *txn = NULL;
    if (mdb_txn_begin(ctx->env, NULL, 0, &txn) != MDB_SUCCESS) return 1;
    MDB_val key_val = {key_len + 1, skey};
    int rc = mdb_del(txn, ctx->dbi, &key_val, NULL);
    if (rc != MDB_SUCCESS && rc != MDB_NOTFOUND) {
        mdb_txn_abort(txn);
        return 1;
    }
    if (mdb_txn_commit(txn) != MDB_SUCCESS) return 1;
    return 0;
}

static void lmdb_free(void *pointer) { free(pointer); }

static void lmdb_close(void *ctx_ptr) {
    lmdb_ctx *ctx = (lmdb_ctx *)ctx_ptr;
    mdb_env_close(ctx->env);
}

int main(int argc, char **argv) {
    const char *workload = NULL;
    const char *label = "unset";
    const char *db = NULL;
    int put_mode_core_like = 0;
    size_t ops = 20000;
    int threads = 1;
    size_t value_size = 64;
    uint64_t keys = 0;

    for (int i = 1; i < argc; i++) {
        if (strcmp(argv[i], "--workload") == 0 && i + 1 < argc) workload = argv[++i];
        else if (strcmp(argv[i], "--ops") == 0 && i + 1 < argc) ops = (size_t)atoll(argv[++i]);
        else if (strcmp(argv[i], "--threads") == 0 && i + 1 < argc) threads = atoi(argv[++i]);
        else if (strcmp(argv[i], "--value") == 0 && i + 1 < argc) value_size = (size_t)atoll(argv[++i]);
        else if (strcmp(argv[i], "--keys") == 0 && i + 1 < argc) keys = strtoull(argv[++i], NULL, 10);
        else if (strcmp(argv[i], "--label") == 0 && i + 1 < argc) label = argv[++i];
        else if (strcmp(argv[i], "--db") == 0 && i + 1 < argc) db = argv[++i];
        else if (strcmp(argv[i], "--put-mode") == 0 && i + 1 < argc)
            put_mode_core_like = strcmp(argv[++i], "core-like") == 0;
    }
    if (workload == NULL) {
        fprintf(stderr,
                "usage: lmbench --workload put|get|rget|getmiss|del [--ops N] [--threads T]\n"
                "               [--value S] [--keys N] [--label L] [--db DIR]\n"
                "               [--put-mode minimal|core-like]\n");
        return 2;
    }
    if (keys == 0) keys = ops;
    printf("info,library=direct-lmdb-0.9.35,put_mode=%s\n",
           put_mode_core_like ? "core-like" : "minimal");

    const char *tmpl = "/tmp/kvlite-zig-proto-lm-XXXXXX";
    char *dir = db ? strdup(db) : strdup(tmpl);
    if (db == NULL && mkdtemp(dir) == NULL) {
        perror("mkdtemp");
        return 2;
    }

    lmdb_ctx ctx;
    memset(&ctx, 0, sizeof(ctx));
    ctx.put_mode_core_like = put_mode_core_like;
    if (lmdb_open(&ctx, dir, "lmdb") != 0) {
        fprintf(stderr, "lmdb open failed for %s\n", dir);
        return 2;
    }
    backend b = {&ctx, lmdb_open, lmdb_put, lmdb_get, lmdb_del, lmdb_free, lmdb_close};
    int result = run_workload(&b, label, workload, ops, threads, value_size, keys);
    lmdb_close(&ctx);

    if (db == NULL) {
        char command[512];
        snprintf(command, sizeof(command), "rm -rf \"%s\"", dir);
        if (system(command) != 0) fprintf(stderr, "cleanup of %s failed\n", dir);
    }
    free(dir);
    (void)lmdb_check;
    return result;
}
