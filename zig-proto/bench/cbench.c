/*
 * cbench — drive any libkvlite C ABI v1 library through the embedded subset
 * for benchmarking: open/close/put/get/delete plus driver selection.
 *
 * Usage:
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
#include "workloads.h"

typedef unsigned int (*abi_fn)(void);
typedef int (*open_fn)(const char *, const char *, unsigned long long *, char **);
typedef int (*close_fn)(unsigned long long, char **);
typedef int (*put_fn)(unsigned long long, const void *, size_t, const void *, size_t,
                      long long, char **);
typedef int (*get_fn)(unsigned long long, const void *, size_t, void **, size_t *, char **);
typedef int (*del_fn)(unsigned long long, const void *, size_t, char **);
typedef void (*free_fn)(void *);

typedef struct {
    open_fn open_driver;
    close_fn close;
    put_fn put;
    get_fn get;
    del_fn del;
    free_fn free_value;
    unsigned long long handle;
} abi_ctx;

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
    abi_ctx ctx = {open_driver, close, put, get, del, free_value, 0};
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

static void usage(void) {
    fprintf(stderr,
            "usage: cbench <library> startup [--rounds N] [--db DIR] [--label L]\n"
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