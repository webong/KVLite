package kvlite

import (
	"context"
	"errors"
	"sync"
)

// Engine is the small storage contract implemented by KVLite driver modules.
// Drivers receive and return raw KVLite records; codecs, TTL semantics, and
// collections stay in the engine-neutral core.
type Engine interface {
	Get(context.Context, []byte) ([]byte, bool, error)
	Put(context.Context, []byte, []byte) error
	Delete(context.Context, []byte) error
	// Apply commits every mutation in order, atomically: no proper subset may
	// become visible. A commit-time I/O error can leave the outcome unknown;
	// callers must inspect the database before retrying. An empty batch is a no-op.
	Apply(context.Context, []Mutation) error
	ScanPrefix(context.Context, []byte, func(key, value []byte) error) error
	Close() error
}

// Mutation is one raw KVLite record change. Delete removes Key; otherwise
// Value (including an empty value) replaces it. Engines must not retain the
// caller's slices after Apply returns.
type Mutation struct {
	Key    []byte `json:"key"`
	Value  []byte `json:"value,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}

func validateMutations(ctx context.Context, mutations []Mutation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, mutation := range mutations {
		if len(mutation.Key) == 0 {
			return ErrInvalidArgument
		}
	}
	return nil
}

var errAtomicListPushUnsupported = errors.New("kvlite: engine does not support atomic list push")
var errAtomicSetAddUnsupported = errors.New("kvlite: engine does not support atomic set add")
var errAtomicSetRemoveUnsupported = errors.New("kvlite: engine does not support atomic set remove")
var errAtomicHashDeleteUnsupported = errors.New("kvlite: engine does not support atomic hash delete")
var errAtomicReplaceUnsupported = errors.New("kvlite: engine does not support atomic logical replacement")
var errAtomicMultiReplaceUnsupported = errors.New("kvlite: engine does not support atomic multi-key replacement")
var errAtomicMultiDeleteUnsupported = errors.New("kvlite: engine does not support atomic multi-key deletion")

// guardedEngine keeps Close from racing an in-flight backend call. The DB's
// higher-level closed flag provides friendlier early errors; this guard owns
// the actual storage lifetime.
type guardedEngine struct {
	mu     sync.RWMutex
	inner  Engine
	closed bool
}

func (engine *guardedEngine) Get(ctx context.Context, key []byte) ([]byte, bool, error) {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.closed {
		return nil, false, ErrClosed
	}
	return engine.inner.Get(ctx, key)
}

func (engine *guardedEngine) Put(ctx context.Context, key, value []byte) error {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.closed {
		return ErrClosed
	}
	return engine.inner.Put(ctx, key, value)
}

func (engine *guardedEngine) Delete(ctx context.Context, key []byte) error {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.closed {
		return ErrClosed
	}
	return engine.inner.Delete(ctx, key)
}

func (engine *guardedEngine) Apply(ctx context.Context, mutations []Mutation) error {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.closed {
		return ErrClosed
	}
	if err := validateMutations(ctx, mutations); err != nil {
		return err
	}
	if len(mutations) == 0 {
		return nil
	}
	return engine.inner.Apply(ctx, mutations)
}

func (engine *guardedEngine) ScanPrefix(ctx context.Context, prefix []byte, callback func(key, value []byte) error) error {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.closed {
		return ErrClosed
	}
	return engine.inner.ScanPrefix(ctx, prefix, callback)
}

func (engine *guardedEngine) PushList(ctx context.Context, key []byte, items [][]byte, left bool) (int, error) {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.closed {
		return 0, ErrClosed
	}
	remote, ok := engine.inner.(interface {
		PushList(context.Context, []byte, [][]byte, bool) (int, error)
	})
	if !ok {
		return 0, errAtomicListPushUnsupported
	}
	return remote.PushList(ctx, key, items, left)
}

func (engine *guardedEngine) AddSet(ctx context.Context, name string, members []string) (int, error) {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.closed {
		return 0, ErrClosed
	}
	remote, ok := engine.inner.(interface {
		AddSet(context.Context, string, []string) (int, error)
	})
	if !ok {
		return 0, errAtomicSetAddUnsupported
	}
	return remote.AddSet(ctx, name, members)
}

func (engine *guardedEngine) RemoveSet(ctx context.Context, name string, members []string) (int, error) {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.closed {
		return 0, ErrClosed
	}
	remote, ok := engine.inner.(interface {
		RemoveSet(context.Context, string, []string) (int, error)
	})
	if !ok {
		return 0, errAtomicSetRemoveUnsupported
	}
	return remote.RemoveSet(ctx, name, members)
}

func (engine *guardedEngine) DeleteHashFields(ctx context.Context, name string, fields []string) (int, error) {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.closed {
		return 0, ErrClosed
	}
	remote, ok := engine.inner.(interface {
		DeleteHashFields(context.Context, string, []string) (int, error)
	})
	if !ok {
		return 0, errAtomicHashDeleteUnsupported
	}
	return remote.DeleteHashFields(ctx, name, fields)
}

func (engine *guardedEngine) ReplaceLogicalValue(ctx context.Context, key string, encoded []byte) error {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.closed {
		return ErrClosed
	}
	remote, ok := engine.inner.(interface {
		ReplaceLogicalValue(context.Context, string, []byte) error
	})
	if !ok {
		return errAtomicReplaceUnsupported
	}
	return remote.ReplaceLogicalValue(ctx, key, encoded)
}

func (engine *guardedEngine) ReplaceLogicalValues(ctx context.Context, values []LogicalValue, onlyIfAbsent bool) (bool, error) {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.closed {
		return false, ErrClosed
	}
	remote, ok := engine.inner.(interface {
		ReplaceLogicalValues(context.Context, []LogicalValue, bool) (bool, error)
	})
	if !ok {
		return false, errAtomicMultiReplaceUnsupported
	}
	return remote.ReplaceLogicalValues(ctx, values, onlyIfAbsent)
}

func (engine *guardedEngine) DeleteLogicalKeys(ctx context.Context, keys [][]byte) (int, error) {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.closed {
		return 0, ErrClosed
	}
	remote, ok := engine.inner.(interface {
		DeleteLogicalKeys(context.Context, [][]byte) (int, error)
	})
	if !ok {
		return 0, errAtomicMultiDeleteUnsupported
	}
	return remote.DeleteLogicalKeys(ctx, keys)
}

func (engine *guardedEngine) Close() error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closed {
		return nil
	}
	engine.closed = true
	return engine.inner.Close()
}
