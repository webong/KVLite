#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#ifndef KVLITE_MOCK_ABI
#define KVLITE_MOCK_ABI 1
#endif

static unsigned char *stored_key;
static size_t stored_key_len;
static unsigned char *stored_value;
static size_t stored_value_len;
static int opened;

static int fail(char **error, int status, const char *message) {
    if (error != NULL) {
        size_t len = strlen(message) + 1;
        *error = malloc(len);
        if (*error != NULL) memcpy(*error, message, len);
    }
    return status;
}

unsigned int kvlite_abi_version(void) { return KVLITE_MOCK_ABI; }

int kvlite_open(const char *path, unsigned long long *handle, char **error) {
    if (path == NULL || *path == '\0' || handle == NULL)
        return fail(error, 2, "path is required");
    *handle = 1;
    opened = 1;
    return 0;
}

#ifndef KVLITE_MOCK_NO_DRIVER
int kvlite_open_with_backend(const char *path, const char *driver,
                             unsigned long long *handle, char **error) {
    if (driver == NULL || *driver == '\0') return fail(error, 2, "driver is required");
    return kvlite_open(path, handle, error);
}
#endif

int kvlite_close(unsigned long long handle, char **error) {
    if (handle != 1 || !opened) return fail(error, 2, "invalid handle");
    opened = 0;
    free(stored_key);
    free(stored_value);
    stored_key = NULL;
    stored_value = NULL;
    stored_key_len = stored_value_len = 0;
    return 0;
}

int kvlite_put(unsigned long long handle, const void *key, size_t key_len,
               const void *value, size_t value_len, long long ttl, char **error) {
    if (handle != 1 || !opened) return fail(error, 2, "invalid handle");
    if (key == NULL || key_len == 0 || ttl < 0)
        return fail(error, 2, "invalid put arguments");
    unsigned char *next_key = malloc(key_len);
    unsigned char *next_value = malloc(value_len == 0 ? 1 : value_len);
    if (next_key == NULL || next_value == NULL) {
        free(next_key);
        free(next_value);
        return fail(error, 3, "out of memory");
    }
    memcpy(next_key, key, key_len);
    if (value_len != 0) memcpy(next_value, value, value_len);
    free(stored_key);
    free(stored_value);
    stored_key = next_key;
    stored_key_len = key_len;
    stored_value = next_value;
    stored_value_len = value_len;
    return 0;
}

int kvlite_get(unsigned long long handle, const void *key, size_t key_len,
               void **value, size_t *value_len, char **error) {
    if (handle != 1 || !opened) return fail(error, 2, "invalid handle");
    if (stored_key == NULL || stored_key_len != key_len || memcmp(stored_key, key, key_len) != 0)
        return fail(error, 1, "not found");
    *value = malloc(stored_value_len == 0 ? 1 : stored_value_len);
    if (*value == NULL) return fail(error, 3, "out of memory");
    if (stored_value_len != 0) memcpy(*value, stored_value, stored_value_len);
    *value_len = stored_value_len;
    return 0;
}

int kvlite_delete(unsigned long long handle, const void *key, size_t key_len,
                  char **error) {
    if (handle != 1 || !opened) return fail(error, 2, "invalid handle");
    if (stored_key != NULL && stored_key_len == key_len && memcmp(stored_key, key, key_len) == 0) {
        free(stored_key);
        free(stored_value);
        stored_key = NULL;
        stored_value = NULL;
        stored_key_len = stored_value_len = 0;
    }
    return 0;
}

void kvlite_free(void *pointer) { free(pointer); }
