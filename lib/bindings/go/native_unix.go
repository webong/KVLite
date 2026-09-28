//go:build cgo && (linux || darwin)

package kvlite

/*
#cgo linux LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// These signatures are the embedded subset of capi/kvlite.h, ABI version 1.
typedef unsigned int (*abi_fn)(void);
typedef int (*open_fn)(const char *, unsigned long long *, char **);
typedef int (*open_driver_fn)(const char *, const char *, unsigned long long *, char **);
typedef int (*close_fn)(unsigned long long, char **);
typedef int (*put_fn)(unsigned long long, const void *, size_t, const void *, size_t, long long, char **);
typedef int (*get_fn)(unsigned long long, const void *, size_t, void **, size_t *, char **);
typedef int (*delete_fn)(unsigned long long, const void *, size_t, char **);
typedef void (*free_fn)(void *);

typedef struct {
    void *library;
    open_fn open;
    open_driver_fn open_driver;
    close_fn close;
    put_fn put;
    get_fn get;
    delete_fn delete_key;
    free_fn free_value;
} kvlite_module;

static kvlite_module *bridge_load(const char *path, char **error) {
    kvlite_module *m = calloc(1, sizeof(*m));
    if (m == NULL) {
        *error = strdup("out of memory while loading libkvlite");
        return NULL;
    }
    m->library = dlopen(path, RTLD_NOW | RTLD_LOCAL);
    if (m->library == NULL) {
        const char *why = dlerror();
        *error = strdup(why == NULL ? "dlopen failed" : why);
        free(m);
        return NULL;
    }
    abi_fn abi = (abi_fn)dlsym(m->library, "kvlite_abi_version");
    m->open = (open_fn)dlsym(m->library, "kvlite_open");
    m->open_driver = (open_driver_fn)dlsym(m->library, "kvlite_open_with_driver");
    if (m->open_driver == NULL) {
        m->open_driver = (open_driver_fn)dlsym(m->library, "kvlite_open_with_backend");
    }
    m->close = (close_fn)dlsym(m->library, "kvlite_close");
    m->put = (put_fn)dlsym(m->library, "kvlite_put");
    m->get = (get_fn)dlsym(m->library, "kvlite_get");
    m->delete_key = (delete_fn)dlsym(m->library, "kvlite_delete");
    m->free_value = (free_fn)dlsym(m->library, "kvlite_free");
    if (abi == NULL || m->open == NULL || m->close == NULL || m->put == NULL ||
        m->get == NULL || m->delete_key == NULL || m->free_value == NULL) {
        *error = strdup("libkvlite is missing a required ABI v1 symbol");
    } else if (abi() != 1) {
        *error = strdup("libkvlite ABI mismatch: Go binding requires ABI 1");
    }
    if (*error != NULL) {
        dlclose(m->library);
        free(m);
        return NULL;
    }
    // A Go c-shared runtime is process-lifetime. Keep this library resident;
    // unloading it after Close could invalidate runtime-owned memory/code.
    return m;
}

static int bridge_open(kvlite_module *m, const char *path, const char *driver,
                       int explicit_driver, unsigned long long *handle, char **error) {
    if (explicit_driver) {
        if (m->open_driver == NULL) {
            *error = strdup("libkvlite does not support explicit driver selection");
            return 2;
        }
        return m->open_driver(path, driver, handle, error);
    }
    return m->open(path, handle, error);
}

static int bridge_close(kvlite_module *m, unsigned long long handle, char **error) {
    return m->close(handle, error);
}

static int bridge_put(kvlite_module *m, unsigned long long handle,
                      const void *key, size_t key_len, const void *value,
                      size_t value_len, long long ttl, char **error) {
    return m->put(handle, key, key_len, value, value_len, ttl, error);
}

static int bridge_get(kvlite_module *m, unsigned long long handle,
                      const void *key, size_t key_len, void **value,
                      size_t *value_len, char **error) {
    return m->get(handle, key, key_len, value, value_len, error);
}

static int bridge_delete(kvlite_module *m, unsigned long long handle,
                         const void *key, size_t key_len, char **error) {
    return m->delete_key(handle, key, key_len, error);
}

static void bridge_free(kvlite_module *m, void *pointer) {
    if (pointer != NULL) m->free_value(pointer);
}
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

var moduleCache = struct {
	sync.Mutex
	loaded map[string]*C.kvlite_module
}{loaded: make(map[string]*C.kvlite_module)}

type loadedDB struct {
	mu     sync.Mutex
	module *C.kvlite_module
	handle C.ulonglong
	closed bool
}

func openNative(library, path, driver string, explicitDriver bool) (nativeDB, error) {
	moduleCache.Lock()
	module := moduleCache.loaded[library]
	if module == nil {
		cpath := C.CString(library)
		var message *C.char
		module = C.bridge_load(cpath, &message)
		C.free(unsafe.Pointer(cpath))
		if module == nil {
			why := "unable to load native library"
			if message != nil {
				why = C.GoString(message)
				C.free(unsafe.Pointer(message))
			}
			moduleCache.Unlock()
			return nil, fmt.Errorf("%w: %s: %s", ErrNativeLibrary, library, why)
		}
		moduleCache.loaded[library] = module
	}
	moduleCache.Unlock()

	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	cdriver := C.CString(driver)
	defer C.free(unsafe.Pointer(cdriver))
	var handle C.ulonglong
	var message *C.char
	explicit := C.int(0)
	if explicitDriver {
		explicit = 1
	}
	status := C.bridge_open(module, cpath, cdriver, explicit, &handle, &message)
	if err := nativeStatus(module, status, message); err != nil {
		return nil, err
	}
	return &loadedDB{module: module, handle: handle}, nil
}

func (db *loadedDB) put(key, value []byte, ttlSeconds int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	var message *C.char
	status := C.bridge_put(db.module, db.handle, bytePointer(key), C.size_t(len(key)),
		bytePointer(value), C.size_t(len(value)), C.longlong(ttlSeconds), &message)
	return nativeStatus(db.module, status, message)
}

func (db *loadedDB) get(key []byte) ([]byte, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil, ErrClosed
	}
	var value unsafe.Pointer
	var length C.size_t
	var message *C.char
	status := C.bridge_get(db.module, db.handle, bytePointer(key), C.size_t(len(key)), &value, &length, &message)
	defer C.bridge_free(db.module, value)
	if err := nativeStatus(db.module, status, message); err != nil {
		return nil, err
	}
	if uint64(length) > uint64(^uint(0)>>1) || (value == nil && length != 0) {
		return nil, fmt.Errorf("%w: invalid native value buffer", ErrStorage)
	}
	if length == 0 {
		return []byte{}, nil
	}
	return append([]byte(nil), unsafe.Slice((*byte)(value), int(length))...), nil
}

func (db *loadedDB) delete(key []byte) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	var message *C.char
	status := C.bridge_delete(db.module, db.handle, bytePointer(key), C.size_t(len(key)), &message)
	return nativeStatus(db.module, status, message)
}

func (db *loadedDB) close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil
	}
	var message *C.char
	status := C.bridge_close(db.module, db.handle, &message)
	if err := nativeStatus(db.module, status, message); err != nil {
		return err
	}
	db.closed = true
	return nil
}

func bytePointer(value []byte) unsafe.Pointer {
	if len(value) == 0 {
		return nil
	}
	return unsafe.Pointer(&value[0])
}

func nativeStatus(module *C.kvlite_module, status C.int, message *C.char) error {
	why := "KVLite native operation failed"
	if message != nil {
		why = C.GoString(message)
		C.bridge_free(module, unsafe.Pointer(message))
	}
	switch status {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("%w: %s", ErrNotFound, why)
	case 2:
		return fmt.Errorf("%w: %s", ErrInvalidArgument, why)
	default:
		return fmt.Errorf("%w: %s", ErrStorage, why)
	}
}
