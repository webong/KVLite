package kvlite

import (
	"context"
	"errors"
)

// logicalKeyMutations collects removals for every record that can make up one
// logical KVLite key. Callers apply them with any replacement in one batch.
//
// It deliberately uses exact lookups for scalar, list, and collection-TTL
// records: prefix matching those forms would also match sibling keys such as
// "user:10" while deleting "user:1".
func (db *DB) logicalKeyMutations(ctx context.Context, key string) ([]Mutation, error) {
	keys := make([][]byte, 0, 4)
	for _, storageKey := range [][]byte{valueKey(key), listKey(key), collectionTTLKey(key)} {
		_, found, err := db.engine.Get(ctx, storageKey)
		if err != nil {
			return nil, err
		}
		if found {
			keys = append(keys, storageKey)
		}
	}
	for _, prefix := range [][]byte{namespacePrefix(kindHash, key), namespacePrefix(kindSet, key)} {
		if err := db.engine.ScanPrefix(ctx, prefix, func(storageKey, _ []byte) error {
			keys = append(keys, append([]byte(nil), storageKey...))
			return nil
		}); err != nil {
			return nil, err
		}
	}
	mutations := make([]Mutation, 0, len(keys))
	for _, storageKey := range keys {
		mutations = append(mutations, Mutation{Key: storageKey, Delete: true})
	}
	return mutations, nil
}

func (db *DB) deleteLogicalKey(ctx context.Context, key string) (bool, error) {
	mutations, err := db.logicalKeyMutations(ctx, key)
	if err != nil || len(mutations) == 0 {
		return false, err
	}
	if err := db.engine.Apply(ctx, mutations); err != nil {
		return false, err
	}
	return true, nil
}

func (db *DB) deleteLogicalKeys(ctx context.Context, keys [][]byte) (int, error) {
	if remote, ok := db.engine.(interface {
		DeleteLogicalKeys(context.Context, [][]byte) (int, error)
	}); ok {
		if count, err := remote.DeleteLogicalKeys(ctx, keys); !errors.Is(err, errAtomicMultiDeleteUnsupported) {
			return count, err
		}
	}
	seen := make(map[string]struct{}, len(keys))
	mutations := make([]Mutation, 0)
	count := 0
	for _, rawKey := range keys {
		key := string(rawKey)
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		removals, err := db.logicalKeyMutations(ctx, key)
		if err != nil {
			return 0, err
		}
		if len(removals) > 0 {
			count++
			mutations = append(mutations, removals...)
		}
	}
	if err := db.engine.Apply(ctx, mutations); err != nil {
		return 0, err
	}
	return count, nil
}
