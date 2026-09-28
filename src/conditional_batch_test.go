package kvlite

import (
	"context"
	"errors"
	"testing"
)

func TestConditionalBatchValidatesReadsAndPrefixesBeforeOneApply(t *testing.T) {
	db, err := newDB(newMemoryEngine(), defaultConfig(), Backend(DriverMemory))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	store := db.Protocol()
	committer := store.(ConditionalBatchStore)
	for key, value := range map[string]string{"item:a": "one", "item:b": "two"} {
		if err := store.Put(ctx, []byte(key), []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	batch := ConditionalBatch{
		Reads: []ObservedRecord{{Key: []byte("item:a"), Value: []byte("one"), Found: true}},
		Prefixes: []ObservedPrefix{{Prefix: []byte("item:"), Records: []RawRecord{
			{Key: []byte("item:a"), Value: []byte("one")},
			{Key: []byte("item:b"), Value: []byte("two")},
		}}},
		Mutations: []Mutation{{Key: []byte("item:a"), Value: []byte("new")}, {Key: []byte("item:b"), Delete: true}},
	}
	if applied, err := committer.CompareAndApply(ctx, batch); err != nil || !applied {
		t.Fatalf("CompareAndApply = %v, %v", applied, err)
	}
	if value, found, err := store.Get(ctx, []byte("item:a")); err != nil || !found || string(value) != "new" {
		t.Fatalf("item:a = %q, %v, %v", value, found, err)
	}
	if _, found, err := store.Get(ctx, []byte("item:b")); err != nil || found {
		t.Fatalf("item:b found = %v, %v", found, err)
	}
	if applied, err := committer.CompareAndApply(ctx, batch); err != nil || applied {
		t.Fatalf("stale batch = %v, %v", applied, err)
	}
	if err := store.Put(ctx, []byte("item:c"), []byte("three")); err != nil {
		t.Fatal(err)
	}
	readOnly := ConditionalBatch{Prefixes: []ObservedPrefix{{Prefix: []byte("item:"), Records: []RawRecord{{Key: []byte("item:a"), Value: []byte("new")}}}}}
	if applied, err := committer.CompareAndApply(ctx, readOnly); err != nil || applied {
		t.Fatalf("stale read-only snapshot = %v, %v", applied, err)
	}
	if _, err := committer.CompareAndApply(ctx, ConditionalBatch{Prefixes: []ObservedPrefix{{Prefix: []byte("item:"), Records: []RawRecord{{Key: []byte("other"), Value: []byte("bad")}}}}}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("invalid prefix observation = %v, want ErrInvalidArgument", err)
	}
}
