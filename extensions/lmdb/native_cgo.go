//go:build cgo

package lmdb

import (
	"bytes"
	"context"
	"fmt"

	native "github.com/PowerDNS/lmdb-go/lmdb"
	"kvlite"
)

// LMDB's map size is a virtual address-space reservation, not an immediate
// disk allocation on the supported 64-bit platforms. KVLite fixes it at open
// time because automatic resize is unsafe while other processes may hold
// reader mappings. Override the default with
// WithDriverOption("lmdb.map_size", int64(bytes)).
const (
	defaultMapSize   = int64(16 << 30)
	mapSizeOptionKey = "lmdb.map_size"
	minimumMapSize   = int64(1 << 20)
)

func nativeAvailable() error { return nil }

// mapSizeFromOptions resolves the requested virtual map size. A value must be
// an integer byte count; the default keeps existing databases unchanged.
func mapSizeFromOptions(options kvlite.DriverOptions) (int64, error) {
	value, found := options.Values[mapSizeOptionKey]
	if !found {
		return defaultMapSize, nil
	}
	var mapSize int64
	switch typed := value.(type) {
	case int64:
		mapSize = typed
	case int:
		mapSize = int64(typed)
	default:
		return 0, fmt.Errorf("kvlite: %s must be an integer byte count, got %T", mapSizeOptionKey, value)
	}
	if mapSize < minimumMapSize {
		return 0, fmt.Errorf("kvlite: %s must be at least %s", mapSizeOptionKey, humanBytes(minimumMapSize))
	}
	return mapSize, nil
}

// humanBytes renders a byte count in the largest binary unit that divides
// evenly, which keeps the common power-of-two sizes readable.
func humanBytes(bytes int64) string {
	for _, unit := range []struct {
		size int64
		name string
	}{{1 << 30, "GiB"}, {1 << 20, "MiB"}, {1 << 10, "KiB"}} {
		if bytes >= unit.size && bytes%unit.size == 0 {
			return fmt.Sprintf("%d %s", bytes/unit.size, unit.name)
		}
	}
	return fmt.Sprintf("%d bytes", bytes)
}

func openNative(path string, options kvlite.DriverOptions) (kvlite.Engine, error) {
	mapSize, err := mapSizeFromOptions(options)
	if err != nil {
		return nil, err
	}
	env, err := native.NewEnv()
	if err != nil {
		return nil, fmt.Errorf("kvlite: create LMDB environment: %w", err)
	}
	if err := env.SetMapSize(mapSize); err != nil {
		_ = env.Close()
		return nil, fmt.Errorf("kvlite: configure LMDB map: %w", err)
	}
	if err := env.Open(path, 0, 0o600); err != nil {
		_ = env.Close()
		return nil, fmt.Errorf("kvlite: open LMDB: %w", err)
	}
	// Use the unnamed/root DBI, which needs no write transaction to initialize.
	var dbi native.DBI
	err = env.View(func(txn *native.Txn) error {
		var openErr error
		dbi, openErr = txn.OpenRoot(0)
		return openErr
	})
	if err != nil {
		_ = env.Close()
		return nil, fmt.Errorf("kvlite: open LMDB root database: %w", err)
	}
	return &engine{env: env, dbi: dbi, maxKeySize: env.MaxKeySize(), mapSize: mapSize}, nil
}

type engine struct {
	env        *native.Env
	dbi        native.DBI
	maxKeySize int
	mapSize    int64
}

// mapFullError reports exhaustion of the fixed virtual map and how to raise it.
func (e *engine) mapFullError(err error) error {
	return fmt.Errorf(
		"kvlite: LMDB map is full (%s virtual limit); reopen with %s(%q, int64(...)) for a larger map: %w",
		humanBytes(e.mapSize), "WithDriverOption", mapSizeOptionKey, err,
	)
}

func (e *engine) validateKey(key []byte) error {
	if len(key) > e.maxKeySize {
		return fmt.Errorf("kvlite: LMDB storage key is %d bytes; maximum is %d", len(key), e.maxKeySize)
	}
	return nil
}

func (e *engine) Get(ctx context.Context, key []byte) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := e.validateKey(key); err != nil {
		return nil, false, err
	}
	var value []byte
	err := e.env.View(func(txn *native.Txn) error {
		stored, err := txn.Get(e.dbi, key)
		if err != nil {
			return err
		}
		value = append([]byte(nil), stored...)
		return nil
	})
	if native.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func (e *engine) Put(ctx context.Context, key, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.validateKey(key); err != nil {
		return err
	}
	err := e.env.Update(func(txn *native.Txn) error { return txn.Put(e.dbi, key, value, 0) })
	if native.IsMapFull(err) {
		return e.mapFullError(err)
	}
	return err
}

func (e *engine) Delete(ctx context.Context, key []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.validateKey(key); err != nil {
		return err
	}
	err := e.env.Update(func(txn *native.Txn) error { return txn.Del(e.dbi, key, nil) })
	if native.IsNotFound(err) {
		return nil
	}
	return err
}

func (e *engine) Apply(ctx context.Context, mutations []kvlite.Mutation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := e.env.Update(func(txn *native.Txn) error {
		for _, mutation := range mutations {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(mutation.Key) == 0 {
				return kvlite.ErrInvalidArgument
			}
			if err := e.validateKey(mutation.Key); err != nil {
				return err
			}
			if mutation.Delete {
				if err := txn.Del(e.dbi, mutation.Key, nil); err != nil && !native.IsNotFound(err) {
					return err
				}
			} else if err := txn.Put(e.dbi, mutation.Key, mutation.Value, 0); err != nil {
				return err
			}
		}
		return nil
	})
	if native.IsMapFull(err) {
		return e.mapFullError(err)
	}
	return err
}

func (e *engine) ScanPrefix(ctx context.Context, prefix []byte, callback func(key, value []byte) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(prefix) > e.maxKeySize {
		return nil
	}
	return e.env.View(func(txn *native.Txn) error {
		cursor, err := txn.OpenCursor(e.dbi)
		if err != nil {
			return err
		}
		defer cursor.Close()
		var key, value []byte
		if len(prefix) == 0 {
			key, value, err = cursor.Get(nil, nil, native.First)
		} else {
			key, value, err = cursor.Get(prefix, nil, native.SetRange)
		}
		for err == nil && bytes.HasPrefix(key, prefix) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := callback(append([]byte(nil), key...), append([]byte(nil), value...)); err != nil {
				return err
			}
			key, value, err = cursor.Get(nil, nil, native.Next)
		}
		if native.IsNotFound(err) {
			return nil
		}
		return err
	})
}

func (e *engine) Close() error { return e.env.Close() }
