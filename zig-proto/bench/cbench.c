/*
 * cbench — drive any libkvlite C ABI v1 library through the embedded subset
 * for benchmarking and conformance: open/close/put/get/delete plus driver selection,
 * raw operations, atomic apply, and prefix scans.
 *
 * Usage:
 *   ./cbench <library> conformance              # ABI v1 conformance checks
 *   ./cbench <library> startup [--rounds N] [--db DIR] [--label L]
 *   ./cbench <library> run --workload W [--ops N] [--threads T]
 *                     [--value S] [--keys N] [--label L] [--db DIR]
 *
 * Workloads: put get rget getmiss del
 */

#include <dlfcn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <stdint.h>
#include "workloads.h"

static void median_of(const uint64_t *values, size_t count, uint64_t *median,
                      uint64_t *min, uint64_t *max) {
    uint64_t *copy = malloc(count * sizeof(uint64_t));
    memcpy(copy, values, count * sizeof(uint64_t));
    qsort(copy, count, sizeof(uint64_t), cmp_u64);
    *median = copy[count / 2];
    *min = copy[0];
    *max = copy[count - 1];
    free(copy);
}

typedef unsigned int (*abi_fn)(void);
typedef int (*open_fn)(const char *, const char *, unsigned long long *, char **);
typedef int (*close_fn)(unsigned long long, char **);
typedef int (*put_fn)(unsigned long long, const void *, size_t, const void *, size_t,
                      long long, char **);
typedef int (*get_fn)(unsigned long long, const void *, size_t, void **, size_t *, char **);
typedef int (*del_fn)(unsigned long long, const void *, size_t, char **);
typedef void (*free_fn)(void *);

// Raw operations
typedef int (*raw_put_fn)(unsigned long long, const void *, size_t, const void *, size_t, char **);
typedef int (*raw_get_fn)(unsigned long long, const void *, size_t, void **, size_t *, char **);
typedef int (*raw_del_fn)(unsigned long long, const void *, size_t, char **);
typedef int (*raw_apply_fn)(unsigned long long, const void *, size_t, char **);
typedef int (*raw_scan_open_fn)(unsigned long long, const void *, size_t, unsigned long long *, char **);
typedef int (*raw_scan_next_fn)(unsigned long long, void **, size_t *, void **, size_t *, char **);
typedef int (*raw_scan_close_fn)(unsigned long long, char **);

typedef struct {
    open_fn open_driver;
    close_fn close;
    put_fn put;
    get_fn get;
    del_fn del;
    free_fn free_value;
    raw_put_fn raw_put;
    raw_get_fn raw_get;
    raw_del_fn raw_del;
    raw_apply_fn raw_apply;
    raw_scan_open_fn raw_scan_open;
    raw_scan_next_fn raw_scan_next;
    raw_scan_close_fn raw_scan_close;
    unsigned long long handle;
} abi_ctx;

#define CHECK(cond, name, detail)                                                  \
    do {                                                                           \
        g_checks_total++;                                                          \
        if (cond) {                                                                \
            g_checks_passed++;                                                     \
            printf("check,name=%s,result=ok\n", (name));                           \
        } else {                                                                   \
            printf("check,name=%s,result=FAIL,detail=%s\n", (name),                \
                   (detail) ? (detail) : "?");                                     \
        }                                                                          \
    } while (0)

extern int g_checks_total;
extern int g_checks_passed;

static int abi_open(void *ctx_ptr, const char *path, const char *driver) {
    abi_ctx *ctx = (abi_ctx *)ctx_ptr;
    char *error = NULL;
    int status = ctx->open_driver(path, driver, &ctx->handle, &error);
    if (error != NULL) free(error);
    return status;
}

static int abi_put(void *ctx_ptr, const char *key, size_t key_len, const char *value,
                   size_t value_len) {
    abi_ctx *ctx = (abi_ctx *)ctx_ptr;
    char *error = NULL;
    int status = ctx->put(ctx->handle, key, key_len, value, value_len, 0, &error);
    if (error != NULL) free(error);
    return status;
}

static int abi_get(void *ctx_ptr, const char *key, size_t key_len, char **out_value,
                   size_t *out_len) {
    abi_ctx *ctx = (abi_ctx *)ctx_ptr;
    char *error = NULL;
    int status = ctx->get(ctx->handle, key, key_len, (void **)out_value, out_len, &error);
    if (error != NULL) free(error);
    return status;
}

static int abi_del(void *ctx_ptr, const char *key, size_t key_len) {
    abi_ctx *ctx = (abi_ctx *)ctx_ptr;
    char *error = NULL;
    int status = ctx->del(ctx->handle, key, key_len, &error);
    if (error != NULL) free(error);
    return status;
}

static void abi_free(void *pointer) { free(pointer); }

static void abi_close(void *ctx_ptr) {
    abi_ctx *ctx = (abi_ctx *)ctx_ptr;
    char *error = NULL;
    ctx->close(ctx->handle, &error);
    if (error != NULL) free(error);
}

static char *make_temp_dir(void) {
    const char *tmpl = "/tmp/kvlite-zig-proto-XXXXXX";
    char *dir = strdup(tmpl);
    if (mkdtemp(dir) == NULL) {
        perror("mkdtemp");
        exit(2);
    }
    return dir;
}

static void print_host_info(const char *library) {
    void *handle = dlopen(library, RTLD_NOW | RTLD_LOCAL);
    if (handle == NULL) {
        fprintf(stderr, "dlopen %s: %s\n", library, dlerror());
        exit(2);
    }
    abi_fn abi = (abi_fn)dlsym(handle, "kvlite_abi_version");
    printf("info,library=%s,abi_version=%u\n", library, abi ? abi() : 0u);
    dlclose(handle);
}

static int cmd_run(const char *library, const char *workload, size_t ops, int threads,
                   size_t value_size, uint64_t keys, const char *label, const char *db) {
    void *handle = dlopen(library, RTLD_NOW | RTLD_LOCAL);
    if (handle == NULL) {
        fprintf(stderr, "dlopen %s: %s\n", library, dlerror());
        return 2;
    }
    open_fn open_driver = (open_fn)dlsym(handle, "kvlite_open_with_driver");
    close_fn close = (close_fn)dlsym(handle, "kvlite_close");
    put_fn put = (put_fn)dlsym(handle, "kvlite_put");
    get_fn get = (get_fn)dlsym(handle, "kvlite_get");
    del_fn del = (del_fn)dlsym(handle, "kvlite_delete");
    free_fn free_value = (free_fn)dlsym(handle, "kvlite_free");
    abi_fn abi = (abi_fn)dlsym(handle, "kvlite_abi_version");
    if (!open_driver || !close || !put || !get || !del || !free_value || !abi) {
        fprintf(stderr, "missing ABI v1 symbol in %s\n", library);
        return 2;
    }
    if (abi() != 1) {
        fprintf(stderr, "unexpected ABI version %u\n", abi());
        return 2;
    }

    char *dir = db ? strdup(db) : make_temp_dir();
    abi_ctx ctx = {open_driver, close, put, get, del, free_value, NULL, NULL, NULL, NULL, NULL, NULL, 0};
    char *error = NULL;
    int status = open_driver(dir, "lmdb", &ctx.handle, &error);
    if (error != NULL) {
        fprintf(stderr, "open %s: %s\n", dir, error);
        free(error);
        return 2;
    }
    if (status != 0) {
        fprintf(stderr, "open %s: status %d\n", dir, status);
        return 2;
    }

    backend b = {&ctx, abi_open, abi_put, abi_get, abi_del, abi_free, abi_close};
    int result = run_workload(&b, label, workload, ops, threads, value_size, keys);

    abi_close(&ctx);
    dlclose(handle);
    if (db == NULL) {
        char command[512];
        snprintf(command, sizeof(command), "rm -rf \"%s\"", dir);
        if (system(command) != 0) fprintf(stderr, "cleanup of %s failed\n", dir);
    }
    free(dir);
    return result;
}

static int cmd_startup(const char *library, int rounds, const char *db, const char *label) {
    void *handle = dlopen(library, RTLD_NOW | RTLD_LOCAL);
    if (handle == NULL) {
        fprintf(stderr, "dlopen %s: %s\n", library, dlerror());
        return 2;
    }
    open_fn open_driver = (open_fn)dlsym(handle, "kvlite_open_with_driver");
    close_fn close = (close_fn)dlsym(handle, "kvlite_close");
    put_fn put = (put_fn)dlsym(handle, "kvlite_put");
    get_fn get = (get_fn)dlsym(handle, "kvlite_get");
    del_fn del = (del_fn)dlsym(handle, "kvlite_delete");
    free_fn free_value = (free_fn)dlsym(handle, "kvlite_free");
    if (!open_driver || !close || !put || !get || !del || !free_value) {
        fprintf(stderr, "missing ABI v1 symbol in %s\n", library);
        return 2;
    }

    dlclose(handle);
    uint64_t load_start = now_ns();
    handle = dlopen(library, RTLD_NOW | RTLD_LOCAL);
    uint64_t load_ns = now_ns() - load_start;
    printf("startup,label=%s,kind=library_load_ns,value=%llu\n", label,
           (unsigned long long)load_ns);

    char *base = db ? strdup(db) : make_temp_dir();
    uint64_t *cold = malloc((size_t)rounds * sizeof(uint64_t));
    uint64_t *warm = malloc((size_t)rounds * sizeof(uint64_t));
    char dir[1024];

    for (int r = 0; r < rounds; r++) {
        snprintf(dir, sizeof(dir), "%s/cold-%d", base, r);
        uint64_t start = now_ns();
        unsigned long long dbh = 0;
        char *error = NULL;
        int status = open_driver(dir, "lmdb", &dbh, &error);
        if (status == 0) {
            static const char *key = "startup-key";
            static const char *value = "startup-value";
            status = put(dbh, key, strlen(key), value, strlen(value), 0, &error);
            if (status == 0) {
                void *got = NULL;
                size_t got_len = 0;
                status = get(dbh, key, strlen(key), &got, &got_len, &error);
                if (status == 0) free_value(got);
            }
            if (status == 0) status = del(dbh, key, strlen(key), &error);
            if (error != NULL) free(error);
            status |= close(dbh, &error);
            if (error != NULL) free(error);
        }
        if (status != 0) {
            fprintf(stderr, "cold startup failed in %s\n", dir);
            return 2;
        }
        cold[r] = now_ns() - start;

        start = now_ns();
        dbh = 0;
        error = NULL;
        status = open_driver(dir, "lmdb", &dbh, &error);
        if (status == 0) {
            status = close(dbh, &error);
            if (error != NULL) free(error);
        }
        if (status != 0) {
            fprintf(stderr, "warm reopen failed in %s\n", dir);
            return 2;
        }
        warm[r] = now_ns() - start;
    }

    uint64_t cold_med, cold_min, cold_max, warm_med, warm_min, warm_max;
    median_of(cold, (size_t)rounds, &cold_med, &cold_min, &cold_max);
    median_of(warm, (size_t)rounds, &warm_med, &warm_min, &warm_max);
    printf("startup,label=%s,kind=cold_open_put_get_del_close,rounds=%d,"
           "median_us=%.1f,min_us=%.1f,max_us=%.1f\n",
           label, rounds, (double)cold_med / 1000.0, (double)cold_min / 1000.0,
           (double)cold_max / 1000.0);
    printf("startup,label=%s,kind=warm_reopen_close,rounds=%d,"
           "median_us=%.1f,min_us=%.1f,max_us=%.1f\n",
           label, rounds, (double)warm_med / 1000.0, (double)warm_min / 1000.0,
           (double)warm_max / 1000.0);

    free(cold);
    free(warm);
    if (db == NULL) {
        char command[512];
        snprintf(command, sizeof(command), "rm -rf \"%s\"", base);
        if (system(command) != 0) fprintf(stderr, "cleanup of %s failed\n", base);
    }
    free(base);
    return 0;
}

static int cmd_conformance(const char *library) {
    void *handle = dlopen(library, RTLD_NOW | RTLD_LOCAL);
    if (handle == NULL) {
        fprintf(stderr, "dlopen %s: %s\n", library, dlerror());
        return 2;
    }
    open_fn open_driver = (open_fn)dlsym(handle, "kvlite_open_with_driver");
    close_fn close = (close_fn)dlsym(handle, "kvlite_close");
    put_fn put = (put_fn)dlsym(handle, "kvlite_put");
    get_fn get = (get_fn)dlsym(handle, "kvlite_get");
    del_fn del = (del_fn)dlsym(handle, "kvlite_delete");
    free_fn free_value = (free_fn)dlsym(handle, "kvlite_free");
    abi_fn abi = (abi_fn)dlsym(handle, "kvlite_abi_version");
    raw_put_fn raw_put = (raw_put_fn)dlsym(handle, "kvlite_raw_put");
    raw_get_fn raw_get = (raw_get_fn)dlsym(handle, "kvlite_raw_get");
    raw_del_fn raw_del = (raw_del_fn)dlsym(handle, "kvlite_raw_delete");
    raw_apply_fn raw_apply = (raw_apply_fn)dlsym(handle, "kvlite_raw_apply");
    raw_scan_open_fn raw_scan_open = (raw_scan_open_fn)dlsym(handle, "kvlite_raw_scan_open");
    raw_scan_next_fn raw_scan_next = (raw_scan_next_fn)dlsym(handle, "kvlite_raw_scan_next");
    raw_scan_close_fn raw_scan_close = (raw_scan_close_fn)dlsym(handle, "kvlite_raw_scan_close");
    if (!open_driver || !close || !put || !get || !del || !free_value || !abi) {
        fprintf(stderr, "missing ABI v1 core symbol\n");
        return 2;
    }

    char detail[256];
    unsigned long long dbh = 0;

    CHECK(abi() == 1, "abi_version_is_1", "see info line");

    char *dir = make_temp_dir();
    char *error = NULL;
    int status = open_driver(dir, "lmdb", &dbh, &error);
    snprintf(detail, sizeof(detail), "status=%d", status);
    CHECK(status == 0, "open_with_driver_lmdb", detail);
    if (error) { free(error); error = NULL; }

    if (status == 0) {
        static const char *key = "alpha";
        static const char *value = "one";
        status = put(dbh, key, strlen(key), value, strlen(value), 0, &error);
        snprintf(detail, sizeof(detail), "status=%d", status);
        CHECK(status == 0, "put_ok", detail);
        void *got = NULL;
        size_t got_len = 0;
        status = get(dbh, key, strlen(key), &got, &got_len, &error);
        snprintf(detail, sizeof(detail), "status=%d", status);
        CHECK(status == 0 && got_len == strlen(value) &&
                  (got_len == 0 || memcmp(got, value, got_len) == 0),
              "get_round_trip", detail);
        if (got) free_value(got);
        if (error) { free(error); error = NULL; }

        status = get(dbh, "missing", 7, &got, &got_len, &error);
        snprintf(detail, sizeof(detail), "status=%d", status);
        CHECK(status == 1, "get_missing_is_not_found", detail);
        if (error) { free(error); error = NULL; }

        status = del(dbh, "missing", 7, &error);
        snprintf(detail, sizeof(detail), "status=%d", status);
        CHECK(status == 0, "delete_missing_succeeds", detail);
        if (error) { free(error); error = NULL; }

        status = del(dbh, key, strlen(key), &error);
        snprintf(detail, sizeof(detail), "status=%d", status);
        CHECK(status == 0, "delete_existing_succeeds", detail);
        if (error) { free(error); error = NULL; }
        status = get(dbh, key, strlen(key), &got, &got_len, &error);
        CHECK(status == 1, "get_after_delete_is_not_found", "status");
        if (error) { free(error); error = NULL; }
        if (got) free_value(got);

        status = put(dbh, key, 0, value, strlen(value), 0, &error);
        CHECK(status == 2, "empty_key_put_is_invalid", "status");
        if (error) { free(error); error = NULL; }
        status = put(dbh, key, strlen(key), value, strlen(value), -1, &error);
        CHECK(status == 2, "negative_ttl_is_invalid", "status");
        if (error) { free(error); error = NULL; }
        status = get(dbh, key, 0, &got, &got_len, &error);
        CHECK(status == 2, "empty_key_get_is_invalid", "status");
        if (error) { free(error); error = NULL; }
        status = del(dbh, key, 0, &error);
        CHECK(status == 2, "empty_key_delete_is_invalid", "status");
        if (error) { free(error); error = NULL; }
        status = get(dbh, key, strlen(key), NULL, &got_len, &error);
        CHECK(status == 2, "null_out_value_is_invalid", "status");
        if (error) { free(error); error = NULL; }
        status = put(999999, key, strlen(key), value, strlen(value), 0, &error);
        CHECK(status == 2, "invalid_handle_put_is_invalid", "status");
        if (error) { free(error); error = NULL; }
        status = close(999999, &error);
        CHECK(status == 2, "invalid_handle_close_is_invalid", "status");
        if (error) { free(error); error = NULL; }

        status = put(dbh, "empty", 5, "", 0, 0, &error);
        snprintf(detail, sizeof(detail), "status=%d", status);
        CHECK(status == 0, "put_zero_length_value", detail);
        if (error) { free(error); error = NULL; }
        got = NULL;
        got_len = 1;
        status = get(dbh, "empty", 5, &got, &got_len, &error);
        snprintf(detail, sizeof(detail), "status=%d,len=%zu", status, got_len);
        CHECK(status == 0 && got_len == 0 && got != NULL, "get_zero_length_value", detail);
        if (got) free_value(got);
        if (error) { free(error); error = NULL; }

        static const char bin_key[] = {0x00, 0x01, (char)0xff, 'k'};
        static const char bin_value[] = {(char)0xfe, 0x00, 'v', (char)0x80};
        status = put(dbh, bin_key, sizeof(bin_key), bin_value, sizeof(bin_value), 0, &error);
        CHECK(status == 0, "put_binary_key_value", detail);
        if (error) { free(error); error = NULL; }
        got = NULL;
        status = get(dbh, bin_key, sizeof(bin_key), &got, &got_len, &error);
        CHECK(status == 0 && got_len == sizeof(bin_value) &&
                  memcmp(got, bin_value, got_len) == 0,
              "get_binary_round_trip", detail);
        if (got) free_value(got);
        if (error) { free(error); error = NULL; }

        size_t large_len = 1 << 20;
        char *large = malloc(large_len);
        memset(large, 0x5a, large_len);
        status = put(dbh, "large", 5, large, large_len, 0, &error);
        CHECK(status == 0, "put_1mib_value", detail);
        if (error) { free(error); error = NULL; }
        got = NULL;
        status = get(dbh, "large", 5, &got, &got_len, &error);
        CHECK(status == 0 && got_len == large_len && memcmp(got, large, large_len) == 0,
              "get_1mib_round_trip", detail);
        if (got) free_value(got);
        free(large);
        if (error) { free(error); error = NULL; }

        status = put(dbh, "ttl", 3, "v", 1, 1, &error);
        CHECK(status == 0, "put_ttl_one_second", detail);
        if (error) { free(error); error = NULL; }
        got = NULL;
        status = get(dbh, "ttl", 3, &got, &got_len, &error);
        CHECK(status == 0, "get_before_ttl_expiry", detail);
        if (got) free_value(got);
        if (error) { free(error); error = NULL; }
        sleep(2);
        status = get(dbh, "ttl", 3, &got, &got_len, &error);
        CHECK(status == 1, "get_after_ttl_expiry_is_not_found", "status");
        if (error) { free(error); error = NULL; }
        if (got) free_value(got);

        status = open_driver(dir, "rocksdb", &dbh, &error);
        CHECK(status == 3, "open_unregistered_driver_is_storage_error", "status");
        if (error) { free(error); error = NULL; }
        status = open_driver(dir, "BAD NAME", &dbh, &error);
        CHECK(status == 2, "open_invalid_driver_name_is_invalid_argument", "status");
        if (error) { free(error); error = NULL; }
        status = open_driver(dir, NULL, &dbh, &error);
        CHECK(status == 2, "open_null_driver_name_is_invalid_argument", "status");
        if (error) { free(error); error = NULL; }
        status = open_driver("", "lmdb", &dbh, &error);
        CHECK(status == 2, "open_empty_path_is_invalid_argument", "status");
        if (error) { free(error); error = NULL; }
        status = open_driver(NULL, "lmdb", &dbh, &error);
        CHECK(status == 2, "open_null_path_is_invalid_argument", "status");
        if (error) { free(error); error = NULL; }

        status = close(dbh, &error);
        CHECK(status == 0, "close_ok", "status");
        if (error) { free(error); error = NULL; }
        status = close(dbh, &error);
        CHECK(status == 2, "double_close_is_invalid_argument", "status");
        if (error) { free(error); error = NULL; }

        dbh = 0;
        status = open_driver(dir, "lmdb", &dbh, &error);
        CHECK(status == 0, "reopen_same_driver", "status");
        if (error) { free(error); error = NULL; }
        if (status == 0) {
            got = NULL;
            status = get(dbh, "large", 5, &got, &got_len, &error);
            CHECK(status == 0 && got_len == (1 << 20), "data_survives_reopen", "status");
            if (got) free_value(got);
            if (error) { free(error); error = NULL; }
            close(dbh, &error);
            if (error) { free(error); error = NULL; }
        }

        dbh = 0;
        status = open_driver(dir, "leveldb", &dbh, &error);
        CHECK(status == 3, "reopen_other_driver_refused", "status");
        if (error) { free(error); error = NULL; }

        char foreign[1024];
        snprintf(foreign, sizeof(foreign), "%s-foreign", dir);
        char mkdir_command[1200];
        snprintf(mkdir_command, sizeof(mkdir_command), "mkdir -p \"%s\"", foreign);
        if (system(mkdir_command) != 0) { /* ignore */ }
        char touch_command[1200];
        snprintf(touch_command, sizeof(touch_command), "touch \"%s/README\"", foreign);
        if (system(touch_command) != 0) { /* ignore */ }
        dbh = 0;
        status = open_driver(foreign, "lmdb", &dbh, &error);
        CHECK(status == 3, "non_empty_unmanifested_dir_refused", "status");
        if (error) { free(error); error = NULL; }

        char cleanup[2400];
        snprintf(cleanup, sizeof(cleanup), "rm -rf \"%s\" \"%s\"", dir, foreign);
        if (system(cleanup) != 0) { /* ignore */ }
    } else {
        g_checks_total++;
        printf("check,name=skipped_after_open_failure,result=FAIL\n");
    }
    free(dir);

    if (raw_put && raw_get && raw_del && raw_apply && raw_scan_open && raw_scan_next && raw_scan_close) {
        dir = make_temp_dir();
        error = NULL;
        dbh = 0;
        status = open_driver(dir, "lmdb", &dbh, &error);
        if (status == 0 && error == NULL) {
            static const char raw_key[] = "raw-key";
            static const char raw_val[] = "raw-value";
            status = raw_put(dbh, raw_key, strlen(raw_key), raw_val, strlen(raw_val), &error);
            CHECK(status == 0, "raw_put_ok", "status");
            if (error) { free(error); error = NULL; }

            void *got = NULL;
            size_t got_len = 0;
            status = raw_get(dbh, raw_key, strlen(raw_key), &got, &got_len, &error);
            CHECK(status == 0 && got_len == strlen(raw_val) && memcmp(got, raw_val, got_len) == 0,
                  "raw_get_round_trip", "status");
            if (got) free_value(got);
            if (error) { free(error); error = NULL; }

            status = raw_del(dbh, "missing", 7, &error);
            CHECK(status == 0, "raw_delete_missing_succeeds", "status");
            if (error) { free(error); error = NULL; }

            status = raw_del(dbh, raw_key, strlen(raw_key), &error);
            CHECK(status == 0, "raw_delete_existing_succeeds", "status");
            if (error) { free(error); error = NULL; }
            status = raw_get(dbh, raw_key, strlen(raw_key), &got, &got_len, &error);
            CHECK(status == 1, "raw_get_after_delete_not_found", "status");
            if (error) { free(error); error = NULL; }
            if (got) free_value(got);

            {
                struct {
                    const void *key;
                    size_t key_length;
                    const void *value;
                    size_t value_length;
                    int is_delete;
                } mutations[3] = {
                    { "batch1", 6, "val1", 4, 0 },
                    { "batch2", 6, "val2", 4, 0 },
                    { "batch1", 6, NULL, 0, 1 },
                };
                status = raw_apply(dbh, mutations, 3, &error);
                CHECK(status == 0, "raw_apply_batch", "status");
                if (error) { free(error); error = NULL; }

                got = NULL;
                status = raw_get(dbh, "batch1", 6, &got, &got_len, &error);
                CHECK(status == 1, "raw_apply_delete_works", "status");
                if (error) { free(error); error = NULL; }
                if (got) free_value(got);

                got = NULL;
                status = raw_get(dbh, "batch2", 6, &got, &got_len, &error);
                CHECK(status == 0 && got_len == 4 && memcmp(got, "val2", 4) == 0,
                      "raw_apply_put_works", "status");
                if (got) free_value(got);
                if (error) { free(error); error = NULL; }
            }

            {
                unsigned long long cursor = 0;
                status = raw_scan_open(dbh, "batch", 5, &cursor, &error);
                CHECK(status == 0, "raw_scan_open_prefix", "status");
                if (error) { free(error); error = NULL; }

                int count = 0;
                while (1) {
                    void *skey = NULL;
                    size_t skey_len = 0;
                    void *sval = NULL;
                    size_t sval_len = 0;
                    status = raw_scan_next(cursor, &skey, &skey_len, &sval, &sval_len, &error);
                    if (status == 1) break;
                    CHECK(status == 0, "raw_scan_next_ok", "status");
                    count++;
                    if (skey) free_value(skey);
                    if (sval) free_value(sval);
                }
                CHECK(count == 1, "raw_scan_prefix_count", "count");
                if (error) { free(error); error = NULL; }

                status = raw_scan_close(cursor, &error);
                CHECK(status == 0, "raw_scan_close_ok", "status");
                if (error) { free(error); error = NULL; }
            }

            {
                unsigned long long cursor = 0;
                status = raw_scan_open(dbh, NULL, 0, &cursor, &error);
                CHECK(status == 0, "raw_scan_open_empty_prefix", "status");
                if (error) { free(error); error = NULL; }

                int count = 0;
                while (1) {
                    void *skey = NULL;
                    size_t skey_len = 0;
                    void *sval = NULL;
                    size_t sval_len = 0;
                    status = raw_scan_next(cursor, &skey, &skey_len, &sval, &sval_len, &error);
                    if (status == 1) break;
                    CHECK(status == 0, "raw_scan_next_all_ok", "status");
                    count++;
                    if (skey) free_value(skey);
                    if (sval) free_value(sval);
                }
                CHECK(count >= 1, "raw_scan_all_keys", "count");
                if (error) { free(error); error = NULL; }

                status = raw_scan_close(cursor, &error);
                CHECK(status == 0, "raw_scan_close_all_ok", "status");
                if (error) { free(error); error = NULL; }
            }

            status = raw_scan_close(999999, &error);
            CHECK(status == 2, "raw_scan_close_invalid_cursor", "status");
            if (error) { free(error); error = NULL; }

            close(dbh, &error);
            if (error) { free(error); error = NULL; }
        } else {
            g_checks_total++;
            printf("check,name=raw_skipped_after_open_failure,result=FAIL\n");
        }
        free(dir);
    } else {
        g_checks_total++;
        printf("check,name=raw_ops_not_implemented,result=SKIP\n");
    }

    free_value(NULL);
    g_checks_total++;
    g_checks_passed++;
    printf("check,name=free_null_noop,result=ok\n");

    printf("conformance,total=%d,passed=%d,result=%s\n", g_checks_total, g_checks_passed,
           g_checks_total == g_checks_passed ? "PASS" : "FAIL");
    dlclose(handle);
    return g_checks_total == g_checks_passed ? 0 : 1;
}

static void usage(void) {
    fprintf(stderr,
            "usage: cbench <library> conformance\n"
            "       cbench <library> startup [--rounds N] [--db DIR] [--label L]\n"
            "       cbench <library> run --workload put|get|rget|getmiss|del\n"
            "             [--ops N] [--threads T] [--value S] [--keys N]\n"
            "             [--label L] [--db DIR]\n");
}

int main(int argc, char **argv) {
    if (argc < 3) {
        usage();
        return 2;
    }
    const char *library = argv[1];
    const char *command = argv[2];

    if (strcmp(command, "conformance") == 0) return cmd_conformance(library);
    if (strcmp(command, "startup") == 0) {
        print_host_info(library);
        int rounds = 25;
        const char *db = NULL;
        const char *label = "unset";
        for (int i = 3; i < argc; i++) {
            if (strcmp(argv[i], "--rounds") == 0 && i + 1 < argc) rounds = atoi(argv[++i]);
            else if (strcmp(argv[i], "--db") == 0 && i + 1 < argc) db = argv[++i];
            else if (strcmp(argv[i], "--label") == 0 && i + 1 < argc) label = argv[++i];
        }
        return cmd_startup(library, rounds, db, label);
    }
    if (strcmp(command, "run") == 0) {
        const char *workload = NULL;
        size_t ops = 20000;
        int threads = 1;
        size_t value_size = 64;
        uint64_t keys = 0;
        const char *label = "unset";
        const char *db = NULL;
        for (int i = 3; i < argc; i++) {
            if (strcmp(argv[i], "--workload") == 0 && i + 1 < argc) workload = argv[++i];
            else if (strcmp(argv[i], "--ops") == 0 && i + 1 < argc) ops = (size_t)atoll(argv[++i]);
            else if (strcmp(argv[i], "--threads") == 0 && i + 1 < argc) threads = atoi(argv[++i]);
            else if (strcmp(argv[i], "--value") == 0 && i + 1 < argc) value_size = (size_t)atoll(argv[++i]);
            else if (strcmp(argv[i], "--keys") == 0 && i + 1 < argc) keys = strtoull(argv[++i], NULL, 10);
            else if (strcmp(argv[i], "--label") == 0 && i + 1 < argc) label = argv[++i];
            else if (strcmp(argv[i], "--db") == 0 && i + 1 < argc) db = argv[++i];
        }
        if (workload == NULL) {
            usage();
            return 2;
        }
        if (keys == 0) keys = ops;
        print_host_info(library);
        return cmd_run(library, workload, ops, threads, value_size, keys, label, db);
    }
    usage();
    return 2;
}