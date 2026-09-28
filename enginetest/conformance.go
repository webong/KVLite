// Package enginetest contains reusable conformance checks for KVLite engine
// extensions. Driver authors can run the same observable storage contract.
package enginetest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/webong/kvlite"
)

// RunAtomicMutations checks ordered raw batches and logical multi-record
// operations through the public KVLite interface.
func RunAtomicMutations(t *testing.T, driver kvlite.DriverName) {
	t.Helper()
	db, err := kvlite.Open(t.TempDir(), kvlite.WithDriver(string(driver)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	store := db.Transport()
	a, b := []byte{0x7f, 'a'}, []byte{0x7f, 'b'}
	if err := store.Put(ctx, a, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(ctx, []kvlite.Mutation{
		{Key: a, Delete: true},
		{Key: b, Value: []byte("other")},
		{Key: a, Value: []byte("new")},
	}); err != nil {
		t.Fatal(err)
	}
	assertRaw(t, store, a, "new")
	assertRaw(t, store, b, "other")
	if err := store.Apply(ctx, []kvlite.Mutation{
		{Key: a, Value: []byte("should-not-appear")},
		{Key: nil, Value: []byte("invalid")},
	}); !errors.Is(err, kvlite.ErrInvalidArgument) {
		t.Fatalf("invalid batch error = %v", err)
	}
	assertRaw(t, store, a, "new")

	if added, err := db.SAdd(ctx, "roles", "admin", "author", "admin"); err != nil || added != 2 {
		t.Fatalf("SAdd = %d, %v", added, err)
	}
	if removed, err := db.SRemove(ctx, "roles", "admin", "admin"); err != nil || removed != 1 {
		t.Fatalf("SRemove = %d, %v", removed, err)
	}
	if members, err := db.SMembers(ctx, "roles"); err != nil || !slices.Equal(members, []string{"author"}) {
		t.Fatalf("SMembers = %v, %v", members, err)
	}
	if err := db.HSet(ctx, "profile", "a", "A"); err != nil {
		t.Fatal(err)
	}
	if err := db.HSet(ctx, "profile", "b", "B"); err != nil {
		t.Fatal(err)
	}
	if deleted, err := db.HDelete(ctx, "profile", "a", "b", "a"); err != nil || deleted != 2 {
		t.Fatalf("HDelete = %d, %v", deleted, err)
	}
	var fields map[string]string
	if err := db.HGetAll(ctx, "profile", &fields); err != nil || len(fields) != 0 {
		t.Fatalf("HGetAll = %v, %v", fields, err)
	}
	if err := db.Put(ctx, "roles", "scalar"); err != nil {
		t.Fatal(err)
	}
	if members, err := db.SMembers(ctx, "roles"); err != nil || len(members) != 0 {
		t.Fatalf("old set remains after replacement: %v, %v", members, err)
	}
	var value string
	if err := db.Get(ctx, "roles", &value); err != nil || value != "scalar" {
		t.Fatalf("Get replacement = %q, %v", value, err)
	}
}

// RunLogicalArchiveMigration checks that the driver can import a KVLite
// logical archive made by another engine and preserve it after reopening.
func RunLogicalArchiveMigration(t *testing.T, driver kvlite.DriverName) {
	t.Helper()
	ctx := context.Background()
	source, err := kvlite.Open(t.TempDir(), kvlite.WithDriver("memory"))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Put(ctx, "user", map[string]string{"name": "Ada"}, kvlite.TTL(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := source.HSet(ctx, "profile", "name", "Ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SAdd(ctx, "roles", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RPush(ctx, "jobs", "first", "second"); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := source.Export(ctx, &archive); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	destination, err := kvlite.Open(path, kvlite.WithDriver(string(driver)))
	if err != nil {
		t.Fatal(err)
	}
	if err := destination.Import(ctx, &archive); err != nil {
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := kvlite.Open(path, kvlite.WithDriver(string(driver)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	var user map[string]string
	if err := reopened.Get(ctx, "user", &user); err != nil || user["name"] != "Ada" {
		t.Fatalf("reopened user = %#v, %v", user, err)
	}
	var fields map[string]string
	if err := reopened.HGetAll(ctx, "profile", &fields); err != nil || fields["name"] != "Ada" {
		t.Fatalf("reopened fields = %#v, %v", fields, err)
	}
	if members, err := reopened.SMembers(ctx, "roles"); err != nil || !slices.Equal(members, []string{"admin"}) {
		t.Fatalf("reopened members = %#v, %v", members, err)
	}
	var jobs []string
	if err := reopened.LRange(ctx, "jobs", 0, -1, &jobs); err != nil || !slices.Equal(jobs, []string{"first", "second"}) {
		t.Fatalf("reopened jobs = %#v, %v", jobs, err)
	}
}

func assertRaw(t *testing.T, store kvlite.TransportStore, key []byte, want string) {
	t.Helper()
	got, found, err := store.Get(context.Background(), key)
	if err != nil || !found || string(got) != want {
		t.Fatalf("raw Get(%q) = %q, %t, %v; want %q", key, got, found, err, want)
	}
}
