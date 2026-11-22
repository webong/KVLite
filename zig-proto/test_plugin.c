/*
 * test_plugin.c — a minimal ext.plugin/v1 guest plugin for KVLite.
 *
 * Exports the ext_plugin.h C ABI (abi_version/open/call/close) and handles
 * KVLite wire operations inside ext_plugin_call's JSON request/response.
 *
 * The ext wire protocol requires every invoke response to be:
 *   {"apiVersion":"ext.plugin/v1","id":"<request-id>","payload":{...}|null,"error":null|{code,message,...}}
 *
 * Build:
 *   gcc -dynamiclib -o test_plugin.dylib test_plugin.c
 *
 * Usage:
 *   kvlite_plugin_load("/abs/path/to/test_plugin.dylib") → handle
 *   kvlite_plugin_call(handle, "health", "{}", &resp, &resp_len)
 *   kvlite_plugin_close(handle)
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define EXT_PLUGIN_ABI_VERSION 1u
#define EXT_PLUGIN_HANDSHAKE 1u
#define EXT_PLUGIN_INVOKE 2u
#define EXT_PLUGIN_OK 0u
#define EXT_PLUGIN_INVALID 1u
#define EXT_PLUGIN_FAILED 3u

/* Minimal JSON string extraction: find "key":"value" and copy value into out. */
static const char *json_str(const char *json, const char *key, char *out, size_t outsz) {
    char search[128];
    snprintf(search, sizeof(search), "\"%s\":\"", key);
    const char *p = strstr(json, search);
    if (!p) return NULL;
    p += strlen(search);
    const char *end = strchr(p, '"');
    if (!end) return NULL;
    size_t n = end - p;
    if (n >= outsz) n = outsz - 1;
    memcpy(out, p, n);
    out[n] = '\0';
    return p;
}

static int json_bool(const char *json, const char *key) {
    char search[128];
    snprintf(search, sizeof(search), "\"%s\":", key);
    const char *p = strstr(json, search);
    if (!p) return 0;
    p += strlen(search);
    return (*p == 't');
}

typedef struct {
    char **keys;
    char **values;
    size_t count;
    size_t cap;
} Store;

static Store g_store = {0};

static void store_put(const char *key, const char *value) {
    for (size_t i = 0; i < g_store.count; i++) {
        if (strcmp(g_store.keys[i], key) == 0) {
            free(g_store.values[i]);
            g_store.values[i] = strdup(value);
            return;
        }
    }
    if (g_store.count == g_store.cap) {
        size_t ncap = g_store.cap == 0 ? 16 : g_store.cap * 2;
        g_store.keys = realloc(g_store.keys, ncap * sizeof(char *));
        g_store.values = realloc(g_store.values, ncap * sizeof(char *));
        g_store.cap = ncap;
    }
    g_store.keys[g_store.count] = strdup(key);
    g_store.values[g_store.count] = strdup(value);
    g_store.count++;
}

static const char *store_get(const char *key) {
    for (size_t i = 0; i < g_store.count; i++) {
        if (strcmp(g_store.keys[i], key) == 0) return g_store.values[i];
    }
    return NULL;
}

static int resp_set(unsigned char *response, unsigned int capacity,
                    unsigned int *response_len, const char *body) {
    size_t n = strlen(body);
    if (n >= capacity) return EXT_PLUGIN_FAILED;
    memcpy(response, body, n);
    *response_len = (unsigned int)n;
    return EXT_PLUGIN_OK;
}

/* Build an ext wire response: {"apiVersion":"ext.plugin/v1","id":"...","payload":...,"error":null} */
static int resp_ok(unsigned char *response, unsigned int capacity,
                   unsigned int *response_len, const char *req_id, const char *payload_json) {
    char body[4096];
    int n = snprintf(body, sizeof(body),
        "{\"apiVersion\":\"ext.plugin/v1\",\"id\":\"%s\",\"payload\":%s,\"error\":null}",
        req_id, payload_json ? payload_json : "null");
    if (n < 0 || (size_t)n >= sizeof(body)) return EXT_PLUGIN_FAILED;
    return resp_set(response, capacity, response_len, body);
}

/* Build an ext wire error response: {"apiVersion":"ext.plugin/v1","id":"...","payload":null,"error":{"code":"...","message":"..."}} */
static int resp_err(unsigned char *response, unsigned int capacity,
                    unsigned int *response_len, const char *req_id,
                    const char *code, const char *message) {
    char body[4096];
    int n = snprintf(body, sizeof(body),
        "{\"apiVersion\":\"ext.plugin/v1\",\"id\":\"%s\",\"payload\":null,"
        "\"error\":{\"code\":\"%s\",\"message\":\"%s\"}}",
        req_id, code, message);
    if (n < 0 || (size_t)n >= sizeof(body)) return EXT_PLUGIN_FAILED;
    return resp_set(response, capacity, response_len, body);
}

/* Returns the plugin descriptor matching what KVLite's host expects. */
static const char *plugin_descriptor(void) {
    static char desc[2048];
    snprintf(desc, sizeof(desc),
        "{\"apiVersion\":\"ext.plugin/v1\","
        "\"identity\":{\"id\":\"kvlite.plugin\",\"revision\":\"v1\",\"version\":\"\"},"
        "\"contracts\":["
        "  {\"name\":\"kvlite.kv\",\"version\":\"v1\","
        "   \"operations\":["
        "     {\"name\":\"get\",\"surface\":\"observation\",\"progress\":false},"
        "     {\"name\":\"put\",\"surface\":\"mutation\",\"progress\":false},"
        "     {\"name\":\"delete\",\"surface\":\"mutation\",\"progress\":false},"
        "     {\"name\":\"scan\",\"surface\":\"observation\",\"progress\":false},"
        "     {\"name\":\"multi_get\",\"surface\":\"observation\",\"progress\":false},"
        "     {\"name\":\"multi_put\",\"surface\":\"mutation\",\"progress\":false},"
        "     {\"name\":\"health\",\"surface\":\"observation\",\"progress\":false}"
        "   ]"
        "  }"
        "],"
        "\"reverse\":[]"
        "}");
    return desc;
}

/* ext_plugin.h C ABI exports */
unsigned int ext_plugin_abi_version(void) {
    return EXT_PLUGIN_ABI_VERSION;
}

unsigned long long ext_plugin_open(void) {
    return 1u; /* single handle */
}

unsigned int ext_plugin_call(unsigned long long handle, unsigned int operation,
                             const unsigned char *request, unsigned int request_len,
                             unsigned char *response, unsigned int response_capacity,
                             unsigned int *response_len) {
    if (response_len == NULL) return EXT_PLUGIN_INVALID;
    *response_len = 0;

    if (operation == EXT_PLUGIN_HANDSHAKE) {
        return resp_set(response, response_capacity, response_len, plugin_descriptor());
    }

    if (operation != EXT_PLUGIN_INVOKE) return EXT_PLUGIN_INVALID;

    char *req = malloc(request_len + 1);
    if (req == NULL) return EXT_PLUGIN_FAILED;
    memcpy(req, request, request_len);
    req[request_len] = '\0';

    /* Extract request id */
    char req_id[256] = {0};
    if (!json_str(req, "id", req_id, sizeof(req_id))) {
        free(req);
        return resp_err(response, response_capacity, response_len, "unknown",
                        "invalid_request", "missing id");
    }

    /* Extract operation */
    char op[64] = {0};
    if (!json_str(req, "operation", op, sizeof(op))) {
        free(req);
        return resp_err(response, response_capacity, response_len, req_id,
                        "invalid_request", "missing operation");
    }

    int rc = EXT_PLUGIN_OK;

    if (strcmp(op, "health") == 0) {
        rc = resp_ok(response, response_capacity, response_len, req_id,
                     "{\"status\":\"healthy\",\"ready\":true}");
    } else if (strcmp(op, "get") == 0) {
        char key[256] = {0};
        if (!json_str(req, "key", key, sizeof(key))) {
            rc = resp_err(response, response_capacity, response_len, req_id,
                          "invalid_request", "missing key");
        } else {
            const char *val = store_get(key);
            if (val) {
                char payload[512];
                snprintf(payload, sizeof(payload), "{\"value\":\"%s\",\"found\":true}", val);
                rc = resp_ok(response, response_capacity, response_len, req_id, payload);
            } else {
                rc = resp_ok(response, response_capacity, response_len, req_id,
                             "{\"value\":null,\"found\":false}");
            }
        }
    } else if (strcmp(op, "put") == 0) {
        char key[256] = {0};
        char val[512] = {0};
        if (!json_str(req, "key", key, sizeof(key)) ||
            !json_str(req, "value", val, sizeof(val))) {
            rc = resp_err(response, response_capacity, response_len, req_id,
                          "invalid_request", "missing key or value");
        } else {
            store_put(key, val);
            rc = resp_ok(response, response_capacity, response_len, req_id, "{\"ok\":true}");
        }
    } else if (strcmp(op, "delete") == 0) {
        rc = resp_ok(response, response_capacity, response_len, req_id,
                     "{\"ok\":true,\"existed\":false}");
    } else if (strcmp(op, "scan") == 0) {
        rc = resp_ok(response, response_capacity, response_len, req_id,
                     "{\"items\":[],\"done\":true}");
    } else if (strcmp(op, "multi_get") == 0) {
        rc = resp_ok(response, response_capacity, response_len, req_id,
                     "{\"results\":[]}");
    } else if (strcmp(op, "multi_put") == 0) {
        rc = resp_ok(response, response_capacity, response_len, req_id,
                     "{\"results\":[]}");
    } else {
        rc = resp_err(response, response_capacity, response_len, req_id,
                      "unknown_operation", "unknown operation");
    }

    free(req);
    return rc;
}

void ext_plugin_close(unsigned long long handle) {
    (void)handle;
    for (size_t i = 0; i < g_store.count; i++) {
        free(g_store.keys[i]);
        free(g_store.values[i]);
    }
    free(g_store.keys);
    free(g_store.values);
    g_store.keys = NULL;
    g_store.values = NULL;
    g_store.count = 0;
    g_store.cap = 0;
}
