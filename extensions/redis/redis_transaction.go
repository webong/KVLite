package kvliteredis

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"kvlite"
)

// redisTransaction is a command-local overlay. Reads are cached, writes are
// staged, and the HTTP owner validates the original observations before it
// applies the entire command in one engine batch. This keeps Redis semantics
// out of the optional HTTP module while making attached commands serializable.
type redisTransaction struct {
	kvlite.ProtocolStore
	committer kvlite.ConditionalBatchStore
	reads     map[string]kvlite.ObservedRecord
	prefixes  map[string]kvlite.ObservedPrefix
	writes    map[string]transactionWrite
	mutations []kvlite.Mutation
}

type transactionWrite struct {
	value []byte
	found bool
}

func newRedisTransaction(store kvlite.ProtocolStore, committer kvlite.ConditionalBatchStore) *redisTransaction {
	return &redisTransaction{
		ProtocolStore: store,
		committer:     committer,
		reads:         make(map[string]kvlite.ObservedRecord),
		prefixes:      make(map[string]kvlite.ObservedPrefix),
		writes:        make(map[string]transactionWrite),
	}
}

func (tx *redisTransaction) Get(ctx context.Context, key []byte) ([]byte, bool, error) {
	if write, ok := tx.writes[string(key)]; ok {
		return bytes.Clone(write.value), write.found, nil
	}
	if read, ok := tx.reads[string(key)]; ok {
		return bytes.Clone(read.Value), read.Found, nil
	}
	value, found, err := tx.ProtocolStore.Get(ctx, key)
	if err != nil {
		return nil, false, err
	}
	tx.reads[string(key)] = kvlite.ObservedRecord{Key: bytes.Clone(key), Value: bytes.Clone(value), Found: found}
	return bytes.Clone(value), found, nil
}

func (tx *redisTransaction) Put(_ context.Context, key, value []byte) error {
	copyKey, copyValue := bytes.Clone(key), bytes.Clone(value)
	tx.writes[string(key)] = transactionWrite{value: copyValue, found: true}
	tx.mutations = append(tx.mutations, kvlite.Mutation{Key: copyKey, Value: copyValue})
	return nil
}

func (tx *redisTransaction) Delete(_ context.Context, key []byte) error {
	tx.writes[string(key)] = transactionWrite{}
	tx.mutations = append(tx.mutations, kvlite.Mutation{Key: bytes.Clone(key), Delete: true})
	return nil
}

func (tx *redisTransaction) Apply(ctx context.Context, mutations []kvlite.Mutation) error {
	for _, mutation := range mutations {
		if mutation.Delete {
			if err := tx.Delete(ctx, mutation.Key); err != nil {
				return err
			}
		} else if err := tx.Put(ctx, mutation.Key, mutation.Value); err != nil {
			return err
		}
	}
	return nil
}

func (tx *redisTransaction) ScanPrefix(ctx context.Context, prefix []byte, callback func(key, value []byte) error) error {
	observed, ok := tx.prefixes[string(prefix)]
	if !ok {
		observed = kvlite.ObservedPrefix{Prefix: bytes.Clone(prefix), Records: make([]kvlite.RawRecord, 0)}
		if err := tx.ProtocolStore.ScanPrefix(ctx, prefix, func(key, value []byte) error {
			observed.Records = append(observed.Records, kvlite.RawRecord{Key: bytes.Clone(key), Value: bytes.Clone(value)})
			return nil
		}); err != nil {
			return err
		}
		tx.prefixes[string(prefix)] = observed
	}
	visible := make(map[string][]byte, len(observed.Records)+len(tx.writes))
	for _, record := range observed.Records {
		visible[string(record.Key)] = record.Value
	}
	for key, write := range tx.writes {
		if !bytes.HasPrefix([]byte(key), prefix) {
			continue
		}
		if write.found {
			visible[key] = write.value
		} else {
			delete(visible, key)
		}
	}
	keys := make([]string, 0, len(visible))
	for key := range visible {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := callback([]byte(key), bytes.Clone(visible[key])); err != nil {
			return err
		}
	}
	return nil
}

func (tx *redisTransaction) PushList(ctx context.Context, key []byte, items [][]byte, left bool) (int, error) {
	data, found, err := tx.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	var existing [][]byte
	if found {
		existing, err = tx.DecodeList(data)
		if err != nil {
			return 0, err
		}
	}
	if left {
		existing = append(items, existing...)
	} else {
		existing = append(existing, items...)
	}
	if err := tx.Put(ctx, key, tx.EncodeList(existing)); err != nil {
		return 0, err
	}
	return len(existing), nil
}

func (tx *redisTransaction) logicalRemovals(ctx context.Context, key string) ([]kvlite.Mutation, error) {
	var mutations []kvlite.Mutation
	for _, rawKey := range [][]byte{tx.ValueKey(key), tx.ListKey(key), tx.CollectionTTLKey(key)} {
		if _, found, err := tx.Get(ctx, rawKey); err != nil {
			return nil, err
		} else if found {
			mutations = append(mutations, kvlite.Mutation{Key: rawKey, Delete: true})
		}
	}
	for _, prefix := range [][]byte{tx.HashPrefix(key), tx.SetPrefix(key)} {
		if err := tx.ScanPrefix(ctx, prefix, func(rawKey, _ []byte) error {
			mutations = append(mutations, kvlite.Mutation{Key: bytes.Clone(rawKey), Delete: true})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return mutations, nil
}

func (tx *redisTransaction) DeleteLogicalKey(ctx context.Context, key string) (bool, error) {
	removed, err := tx.logicalRemovals(ctx, key)
	if err != nil || len(removed) == 0 {
		return false, err
	}
	return true, tx.Apply(ctx, removed)
}

func (tx *redisTransaction) DeleteLogicalKeys(ctx context.Context, keys [][]byte) (int, error) {
	seen := make(map[string]struct{}, len(keys))
	count := 0
	for _, rawKey := range keys {
		key := string(rawKey)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		removed, err := tx.DeleteLogicalKey(ctx, key)
		if err != nil {
			return 0, err
		}
		if removed {
			count++
		}
	}
	return count, nil
}

func (tx *redisTransaction) ReplaceLogicalValue(ctx context.Context, key string, encoded []byte) error {
	_, err := tx.ReplaceLogicalValues(ctx, []kvlite.LogicalValue{{Key: []byte(key), Value: encoded}}, false)
	return err
}

func (tx *redisTransaction) ReplaceLogicalValues(ctx context.Context, values []kvlite.LogicalValue, onlyIfAbsent bool) (bool, error) {
	last := make(map[string][]byte, len(values))
	var keys []string
	for _, value := range values {
		if _, err := tx.DecodeRecord(value.Value); err != nil {
			return false, fmt.Errorf("%w: invalid logical value: %v", kvlite.ErrInvalidArgument, err)
		}
		key := string(value.Key)
		if _, seen := last[key]; !seen {
			keys = append(keys, key)
		}
		last[key] = bytes.Clone(value.Value)
	}
	removals := make(map[string][]kvlite.Mutation, len(keys))
	for _, key := range keys {
		removed, err := tx.logicalRemovals(ctx, key)
		if err != nil {
			return false, err
		}
		if onlyIfAbsent && len(removed) > 0 {
			return false, nil
		}
		removals[key] = removed
	}
	for _, key := range keys {
		if err := tx.Apply(ctx, removals[key]); err != nil {
			return false, err
		}
		if err := tx.Put(ctx, tx.ValueKey(key), last[key]); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (tx *redisTransaction) commit(ctx context.Context) (bool, error) {
	batch := kvlite.ConditionalBatch{Mutations: tx.mutations}
	keys := make([]string, 0, len(tx.reads))
	for key := range tx.reads {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		batch.Reads = append(batch.Reads, tx.reads[key])
	}
	keys = keys[:0]
	for prefix := range tx.prefixes {
		keys = append(keys, prefix)
	}
	sort.Strings(keys)
	for _, prefix := range keys {
		batch.Prefixes = append(batch.Prefixes, tx.prefixes[prefix])
	}
	return tx.committer.CompareAndApply(ctx, batch)
}

func (db *database) redisDispatchRemoteAtomic(args [][]byte, password string, session *redisSession) (respValue, bool) {
	if redisLocalCommand(strings.ToUpper(string(args[0]))) {
		return db.redisDispatch(args, password, session)
	}
	committer, ok := db.store.(kvlite.ConditionalBatchStore)
	if !ok {
		return redisErrorReply(kvlite.ErrModuleIncompatible), false
	}
	const maxAttempts = 16
	for attempt := 0; attempt < maxAttempts; attempt++ {
		tx := newRedisTransaction(db.store, committer)
		commandDB := newDatabase(tx)
		reply, quit := commandDB.redisDispatch(args, password, session)
		if reply.kind == respError || quit {
			return reply, quit
		}
		applied, err := tx.commit(context.Background())
		if err != nil {
			return redisErrorReply(err), false
		}
		if applied {
			return reply, false
		}
		time.Sleep(time.Duration(attempt+1) * 100 * time.Microsecond)
	}
	return respErrorString("TRYAGAIN concurrent update prevented an atomic command"), false
}

func redisLocalCommand(command string) bool {
	switch command {
	case "AUTH", "HELLO", "QUIT", "PING", "ECHO", "SELECT", "CLIENT", "COMMAND", "ROLE", "READONLY", "READWRITE", "CONFIG", "INFO", "TIME":
		return true
	default:
		return false
	}
}
