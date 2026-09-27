package kvlite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// ObservedRecord is one raw KVLite record observed before a conditional batch.
// A missing record has Found false; Value is meaningful only when Found is true.
type ObservedRecord struct {
	Key   []byte `json:"key"`
	Value []byte `json:"value,omitempty"`
	Found bool   `json:"found"`
}

// RawRecord is a record returned by a prefix scan. Keys and values are binary
// safe; JSON transports encode them as base64.
type RawRecord struct {
	Key   []byte `json:"key"`
	Value []byte `json:"value"`
}

// ObservedPrefix is the complete set of records seen under one raw prefix.
type ObservedPrefix struct {
	Prefix  []byte      `json:"prefix"`
	Records []RawRecord `json:"records"`
}

// ConditionalBatch applies ordered mutations only if every observed record
// and prefix still matches. An empty Mutations slice validates a read-only
// snapshot without writing. A mismatch returns applied=false, not an error.
type ConditionalBatch struct {
	Reads     []ObservedRecord `json:"reads,omitempty"`
	Prefixes  []ObservedPrefix `json:"prefixes,omitempty"`
	Mutations []Mutation       `json:"mutations,omitempty"`
}

// ConditionalBatchStore is an optional protocol-store capability. It lets a
// protocol extension finish a multi-step command at the database owner. The
// owner validates observations and commits in one critical section.
type ConditionalBatchStore interface {
	CompareAndApply(context.Context, ConditionalBatch) (bool, error)
}

func (store dbProtocol) CompareAndApply(ctx context.Context, batch ConditionalBatch) (bool, error) {
	if err := store.db.ensureOpen(); err != nil {
		return false, err
	}
	if remote, ok := store.db.engine.(interface {
		CompareAndApply(context.Context, ConditionalBatch) (bool, error)
	}); ok {
		if applied, err := remote.CompareAndApply(ctx, batch); !errors.Is(err, errConditionalBatchUnsupported) {
			return applied, err
		}
	}
	if store.db.remote {
		return false, fmt.Errorf("%w: remote engine has no owner-side conditional batch", ErrModuleIncompatible)
	}
	store.db.protocolMu.Lock()
	defer store.db.protocolMu.Unlock()
	return store.db.compareAndApply(ctx, batch)
}

func (db *DB) compareAndApply(ctx context.Context, batch ConditionalBatch) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for _, observed := range batch.Reads {
		if len(observed.Key) == 0 {
			return false, fmt.Errorf("%w: observed record key is required", ErrInvalidArgument)
		}
		actual, found, err := db.engine.Get(ctx, observed.Key)
		if err != nil {
			return false, err
		}
		if found != observed.Found || (found && !bytes.Equal(actual, observed.Value)) {
			return false, nil
		}
	}
	for _, observed := range batch.Prefixes {
		expected := make(map[string][]byte, len(observed.Records))
		for _, record := range observed.Records {
			if !bytes.HasPrefix(record.Key, observed.Prefix) {
				return false, fmt.Errorf("%w: observed record is outside its prefix", ErrInvalidArgument)
			}
			key := string(record.Key)
			if _, duplicate := expected[key]; duplicate {
				return false, fmt.Errorf("%w: duplicate observed record", ErrInvalidArgument)
			}
			expected[key] = record.Value
		}
		matched := 0
		mismatch := false
		if err := db.engine.ScanPrefix(ctx, observed.Prefix, func(key, value []byte) error {
			want, found := expected[string(key)]
			if !found || !bytes.Equal(value, want) {
				mismatch = true
			}
			matched++
			return nil
		}); err != nil {
			return false, err
		}
		if mismatch || matched != len(expected) {
			return false, nil
		}
	}
	if len(batch.Mutations) == 0 {
		return true, nil
	}
	if err := db.engine.Apply(ctx, batch.Mutations); err != nil {
		return false, err
	}
	return true, nil
}
