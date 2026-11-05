/*
 * Shared workload engine for the ADR 0001 KVLite benchmarks.
 *
 * cbench.c (C ABI via dlopen) and lmbench.c (direct vendored LMDB) both
 * implement the same `backend` interface and drive the same workloads, so a
 * comparison holds workload, data shapes, durability, and machine constant.
 *
 * Output is machine-readable CSV with one row per measurement, prefixed by a
 * record type: bench|startup|check|conformance|info.
 *
 * All databases are created under a fresh mkdtemp directory unless --db is
 * given; concurrent use is modeled as threads sharing one database handle
 * with disjoint key ranges, matching how KVLite's ABI is meant to be used.
 */

#ifndef KVLITE_BENCH_WORKLOADS_H
#define KVLITE_BENCH_WORKLOADS_H

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <time.h>
#include <pthread.h>

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

static int g_checks_total = 0;
static int g_checks_passed = 0;

typedef struct {
    void *ctx;
    /* All return 0 on success, nonzero status otherwise (see kvlite.h). */
    int (*open)(void *ctx, const char *path, const char *driver);
    int (*put)(void *ctx, const char *key, size_t key_len, const char *value, size_t value_len);
    int (*get)(void *ctx, const char *key, size_t key_len, char **out_value, size_t *out_len);
    int (*del)(void *ctx, const char *key, size_t key_len);
    void (*free_value)(void *pointer);
    void (*close)(void *ctx);
} backend;

static uint64_t now_ns(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

static int cmp_u64(const void *a, const void *b) {
    uint64_t x = *(const uint64_t *)a, y = *(const uint64_t *)b;
    return (x > y) - (x < y);
}

typedef struct {
    uint64_t *samples;
    size_t count;
} latency_series;

static void latency_report(const char *label, const char *workload, int threads,
                           size_t value_size, size_t ops, const latency_series *series,
                           int series_count, double seconds) {
    size_t total = 0;
    for (int i = 0; i < series_count; i++) total += series[i].count;
    uint64_t *merged = NULL;
    if (total > 0) {
        merged = malloc(total * sizeof(uint64_t));
        size_t at = 0;
        for (int i = 0; i < series_count; i++) {
            memcpy(merged + at, series[i].samples, series[i].count * sizeof(uint64_t));
            at += series[i].count;
        }
        qsort(merged, total, sizeof(uint64_t), cmp_u64);
    }
    double mean_us = 0.0, p50_us = 0.0, p99_us = 0.0;
    if (total > 0) {
        uint64_t sum = 0;
        for (size_t i = 0; i < total; i++) sum += merged[i];
        mean_us = (double)sum / (double)total / 1000.0;
        p50_us = (double)merged[total / 2] / 1000.0;
        p99_us = (double)merged[(total * 99) / 100] / 1000.0;
    }
    free(merged);
    printf("bench,label=%s,workload=%s,threads=%d,value=%zu,ops=%zu,"
           "seconds=%.3f,ops_per_sec=%.0f,mean_us=%.2f,p50_us=%.2f,p99_us=%.2f\n",
           label, workload, threads, value_size, ops, seconds,
           (double)ops / seconds, mean_us, p50_us, p99_us);
}

static void fill_value(char *value, size_t length, unsigned seed) {
    uint64_t state = 0x9e3779b97f4a7c15ull ^ (uint64_t)seed;
    for (size_t i = 0; i < length; i++) {
        state ^= state << 13;
        state ^= state >> 7;
        state ^= state << 17;
        value[i] = (char)(state >> 33);
    }
}

static int format_key(char *buffer, size_t buffer_size, uint64_t index) {
    return snprintf(buffer, buffer_size, "k%012llu", (unsigned long long)index);
}

typedef struct {
    backend *b;
    const char *workload;
    size_t ops_per_thread;
    size_t value_size;
    uint64_t key_space;
    unsigned thread_index;
    latency_series *series;
    int failed_ops;
} thread_job;

static void *thread_main(void *argument) {
    thread_job *job = (thread_job *)argument;
    job->series->samples = malloc(job->ops_per_thread * sizeof(uint64_t));
    job->series->count = 0;
    job->failed_ops = 0;
    uint64_t state = 0x2545f4914f6cdd1dull ^ (0x9e3779b9ull + job->thread_index);
    char key[64];
    char *value = malloc(job->value_size > 0 ? job->value_size : 1);
    fill_value(value, job->value_size, job->thread_index + 1);

    for (size_t i = 0; i < job->ops_per_thread; i++) {
        uint64_t start = now_ns();
        int status = -1;
        if (strcmp(job->workload, "put") == 0) {
            uint64_t index = (uint64_t)job->thread_index * job->ops_per_thread + i;
            format_key(key, sizeof(key), index);
            status = job->b->put(job->b->ctx, key, strlen(key), value, job->value_size);
        } else if (strcmp(job->workload, "get") == 0) {
            uint64_t index = (uint64_t)job->thread_index * job->ops_per_thread + i;
            format_key(key, sizeof(key), index);
            char *got = NULL;
            size_t got_len = 0;
            status = job->b->get(job->b->ctx, key, strlen(key), &got, &got_len);
            if (status == 0) {
                if (got_len != job->value_size) status = -100;
                job->b->free_value(got);
            }
        } else if (strcmp(job->workload, "rget") == 0) {
            state ^= state << 13; state ^= state >> 7; state ^= state << 17;
            uint64_t index = state % (job->key_space ? job->key_space : 1);
            format_key(key, sizeof(key), index);
            char *got = NULL;
            size_t got_len = 0;
            status = job->b->get(job->b->ctx, key, strlen(key), &got, &got_len);
            if (status == 0) {
                if (got_len != job->value_size) status = -100;
                job->b->free_value(got);
            }
        } else if (strcmp(job->workload, "getmiss") == 0) {
            state ^= state << 13; state ^= state >> 7; state ^= state << 17;
            uint64_t index = job->key_space + (state % (job->key_space ? job->key_space : 1));
            format_key(key, sizeof(key), index);
            char *got = NULL;
            size_t got_len = 0;
            status = job->b->get(job->b->ctx, key, strlen(key), &got, &got_len);
            if (status == 1) status = 0;
            if (status == 0 && got != NULL) job->b->free_value(got);
        } else if (strcmp(job->workload, "del") == 0) {
            uint64_t index = (uint64_t)job->thread_index * job->ops_per_thread + i;
            format_key(key, sizeof(key), index);
            status = job->b->del(job->b->ctx, key, strlen(key));
        }
        uint64_t elapsed = now_ns() - start;
        if (status != 0) job->failed_ops++;
        job->series->samples[job->series->count++] = elapsed;
    }
    free(value);
    return NULL;
}

static int populate(backend *b, size_t count, size_t value_size, double *populate_seconds) {
    char key[64];
    char *value = malloc(value_size > 0 ? value_size : 1);
    fill_value(value, value_size, 7);
    uint64_t start = now_ns();
    for (size_t i = 0; i < count; i++) {
        format_key(key, sizeof(key), (uint64_t)i);
        if (b->put(b->ctx, key, strlen(key), value, value_size) != 0) {
            fprintf(stderr, "populate failed at index %zu\n", i);
            free(value);
            return -1;
        }
    }
    if (populate_seconds) *populate_seconds = (double)(now_ns() - start) / 1e9;
    free(value);
    return 0;
}

static int run_workload(backend *b, const char *label, const char *workload,
                        size_t ops, int threads, size_t value_size, uint64_t key_space) {
    int need_populate = strcmp(workload, "get") == 0 || strcmp(workload, "rget") == 0 ||
                        strcmp(workload, "del") == 0;
    if (need_populate) {
        double populate_seconds = 0.0;
        if (populate(b, key_space, value_size, &populate_seconds) != 0) return 2;
        printf("info,populate_seconds=%.3f,keys=%llu,value=%zu\n",
               populate_seconds, (unsigned long long)key_space, value_size);
    }

    size_t per_thread = ops / (size_t)threads;
    if (per_thread == 0) per_thread = 1;

    pthread_t *thread_ids = calloc((size_t)threads, sizeof(pthread_t));
    thread_job *jobs = calloc((size_t)threads, sizeof(thread_job));
    latency_series *series = calloc((size_t)threads, sizeof(latency_series));

    uint64_t start = now_ns();
    for (int t = 0; t < threads; t++) {
        jobs[t].b = b;
        jobs[t].workload = workload;
        jobs[t].ops_per_thread = per_thread;
        jobs[t].value_size = value_size;
        jobs[t].key_space = key_space;
        jobs[t].thread_index = (unsigned)t;
        jobs[t].series = &series[t];
        pthread_create(&thread_ids[t], NULL, thread_main, &jobs[t]);
    }
    for (int t = 0; t < threads; t++) pthread_join(thread_ids[t], NULL);
    double seconds = (double)(now_ns() - start) / 1e9;

    int failed = 0;
    for (int t = 0; t < threads; t++) failed += jobs[t].failed_ops;
    latency_report(label, workload, threads, value_size, per_thread * (size_t)threads,
                   series, threads, seconds);
    for (int t = 0; t < threads; t++) free(series[t].samples);
    free(thread_ids);
    free(jobs);
    free(series);
    return failed > 0 ? 1 : 0;
}

#endif