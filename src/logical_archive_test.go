package kvlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func openArchiveMemory(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir(), WithDriver("memory"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestLogicalArchiveRoundTrip(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	now := start
	source := openArchiveMemory(t)
	source.cfg.now = func() time.Time { return now }
	if err := source.Put(ctx, "user", map[string]string{"name": "Ada"}, TTL(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := source.PutBytes(ctx, string([]byte{0xff, 0}), []byte{0, 1, 0xff}); err != nil {
		t.Fatal(err)
	}
	unknownCodec, err := marshalEnvelope("custom-codec", []byte{9, 8, 7}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.engine.Put(ctx, valueKey("custom"), unknownCodec); err != nil {
		t.Fatal(err)
	}
	if err := source.Put(ctx, "expired-value", "old", TTL(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := source.HSet(ctx, "profile", "name", "Ada", TTL(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := source.HSet(ctx, "profile", "old", "expired", TTL(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SAdd(ctx, "roles", "admin", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RPush(ctx, "queue", "first", "second"); err != nil {
		t.Fatal(err)
	}
	collectionExpiry := start.Add(4 * time.Hour).UnixNano()
	metadata := make([]byte, 8)
	binary.BigEndian.PutUint64(metadata, uint64(collectionExpiry))
	if err := source.engine.Put(ctx, collectionTTLKey("roles"), metadata); err != nil {
		t.Fatal(err)
	}
	now = start.Add(2 * time.Hour)
	var archive bytes.Buffer
	if err := source.Export(ctx, &archive); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(archive.String(), `"format":"kvlite-logical"`) {
		t.Fatalf("missing logical archive header: %s", archive.String())
	}
	destination := openArchiveMemory(t)
	destination.cfg.now = func() time.Time { return now }
	if err := destination.Import(ctx, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	var user map[string]string
	if err := destination.Get(ctx, "user", &user); err != nil || user["name"] != "Ada" {
		t.Fatalf("user = %#v, %v", user, err)
	}
	stored, err := destination.GetStoredValue(ctx, "user")
	if err != nil || stored.ExpiresAt != start.Add(3*time.Hour).UnixNano() {
		t.Fatalf("user expiry = %d, %v", stored.ExpiresAt, err)
	}
	value, err := destination.GetBytes(ctx, string([]byte{0xff, 0}))
	if err != nil || !bytes.Equal(value, []byte{0, 1, 0xff}) {
		t.Fatalf("binary value = %v, %v", value, err)
	}
	custom, err := destination.GetStoredValue(ctx, "custom")
	if err != nil || custom.Codec != "custom-codec" || !bytes.Equal(custom.Payload, []byte{9, 8, 7}) {
		t.Fatalf("custom codec value = %#v, %v", custom, err)
	}
	if _, err := destination.GetBytes(ctx, "expired-value"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired value: %v", err)
	}
	var fields map[string]string
	if err := destination.HGetAll(ctx, "profile", &fields); err != nil || len(fields) != 1 || fields["name"] != "Ada" {
		t.Fatalf("fields = %#v, %v", fields, err)
	}
	members, err := destination.SMembers(ctx, "roles")
	if err != nil || len(members) != 2 || members[0] != "" || members[1] != "admin" {
		t.Fatalf("members = %#v, %v", members, err)
	}
	var items []string
	if err := destination.LRange(ctx, "queue", 0, -1, &items); err != nil || fmt.Sprint(items) != "[first second]" {
		t.Fatalf("items = %#v, %v", items, err)
	}
	gotTTL, found, err := destination.engine.Get(ctx, collectionTTLKey("roles"))
	if err != nil || !found || binary.BigEndian.Uint64(gotTTL) != uint64(collectionExpiry) {
		t.Fatalf("collection TTL = %v, %v, %v", gotTTL, found, err)
	}
}

func TestLogicalArchiveRejectsInvalidInputBeforeWriting(t *testing.T) {
	ctx := context.Background()
	source := openArchiveMemory(t)
	if err := source.Put(ctx, "key", "value"); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := source.Export(ctx, &archive); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(archive.Bytes()), []byte("\n"))
	truncated := append([]byte(nil), lines[0]...)
	truncated = append(truncated, '\n')
	truncated = append(truncated, lines[1]...)
	truncated = append(truncated, '\n')
	tampered := bytes.Replace(archive.Bytes(), []byte(`"codec":"json"`), []byte(`"codec":"bytes"`), 1)
	for name, data := range map[string][]byte{"truncated": truncated, "checksum": tampered, "unsupported-version": bytes.Replace(archive.Bytes(), []byte(`"version":1`), []byte(`"version":2`), 1)} {
		t.Run(name, func(t *testing.T) {
			destination := openArchiveMemory(t)
			if err := destination.Import(ctx, bytes.NewReader(data)); err == nil {
				t.Fatal("invalid archive was accepted")
			}
			var count int
			if err := destination.engine.ScanPrefix(ctx, nil, func(_, _ []byte) error { count++; return nil }); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("invalid archive wrote %d records", count)
			}
		})
	}
}

func TestLogicalArchiveImportRequiresEmptyDestination(t *testing.T) {
	ctx := context.Background()
	source := openArchiveMemory(t)
	if err := source.Put(ctx, "source", 1); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := source.Export(ctx, &archive); err != nil {
		t.Fatal(err)
	}
	destination := openArchiveMemory(t)
	if err := destination.Put(ctx, "existing", 2); err != nil {
		t.Fatal(err)
	}
	if err := destination.Import(ctx, &archive); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("import nonempty destination: %v", err)
	}
	var existing int
	if err := destination.Get(ctx, "existing", &existing); err != nil || existing != 2 {
		t.Fatalf("existing value = %d, %v", existing, err)
	}
}

func TestLogicalArchiveDoesNotResurrectExpiredCollections(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	source := openArchiveMemory(t)
	source.cfg.now = func() time.Time { return start }
	if _, err := source.SAdd(ctx, "short-set", "member"); err != nil {
		t.Fatal(err)
	}
	metadata := make([]byte, 8)
	binary.BigEndian.PutUint64(metadata, uint64(start.Add(time.Hour).UnixNano()))
	if err := source.engine.Put(ctx, collectionTTLKey("short-set"), metadata); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := source.Export(ctx, &archive); err != nil {
		t.Fatal(err)
	}
	destination := openArchiveMemory(t)
	destination.cfg.now = func() time.Time { return start.Add(2 * time.Hour) }
	if err := destination.Import(ctx, &archive); err != nil {
		t.Fatal(err)
	}
	members, err := destination.SMembers(ctx, "short-set")
	if err != nil || len(members) != 0 {
		t.Fatalf("expired set imported: %#v, %v", members, err)
	}
}

func TestLogicalArchiveRejectsRemoteHandle(t *testing.T) {
	db, err := OpenWithEngine(&memoryDriverEngine{data: make(map[string][]byte)}, Backend(DriverMemory))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Export(context.Background(), &bytes.Buffer{}); !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("remote export: %v", err)
	}
	if err := db.Import(context.Background(), bytes.NewReader(nil)); !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("remote import: %v", err)
	}
}

func TestLogicalArchiveImportUsesMultipleBatches(t *testing.T) {
	ctx := context.Background()
	source := openArchiveMemory(t)
	for i := 0; i < importBatchSize+10; i++ {
		if err := source.Put(ctx, fmt.Sprintf("key-%03d", i), i); err != nil {
			t.Fatal(err)
		}
	}
	var archive bytes.Buffer
	if err := source.Export(ctx, &archive); err != nil {
		t.Fatal(err)
	}
	destination := openArchiveMemory(t)
	if err := destination.Import(ctx, &archive); err != nil {
		t.Fatal(err)
	}
	var last int
	if err := destination.Get(ctx, fmt.Sprintf("key-%03d", importBatchSize+9), &last); err != nil || last != importBatchSize+9 {
		t.Fatalf("last imported value = %d, %v", last, err)
	}
}

func TestLogicalArchiveRejectsMixedTypesBeforeWriting(t *testing.T) {
	var archive bytes.Buffer
	writer := archiveWriter{out: &archive, hash: sha256.New()}
	for _, record := range []logicalArchiveRecord{
		{Type: "header", Format: logicalArchiveFormat, Version: logicalArchiveVersion},
		{Type: "value", Key: []byte("same"), Codec: "bytes", Payload: []byte("one")},
		{Type: "set", Key: []byte("same"), Member: []byte("member")},
	} {
		if err := writer.write(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.write(logicalArchiveRecord{Type: "end", Records: writer.count, SHA256: fmt.Sprintf("%x", writer.hash.Sum(nil))}); err != nil {
		t.Fatal(err)
	}
	destination := openArchiveMemory(t)
	if err := destination.Import(context.Background(), &archive); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("mixed logical types: %v", err)
	}
	var count int
	if err := destination.engine.ScanPrefix(context.Background(), nil, func(_, _ []byte) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid archive wrote %d records", count)
	}
}

func TestLogicalArchiveAcceptsNonGoJSONFormattingWithRawChecksum(t *testing.T) {
	line := []byte("{ \"payload\":\"eA==\", \"codec\":\"bytes\", \"key\":\"aw==\", \"type\":\"value\" }\n")
	sum := sha256.Sum256(line)
	archive := fmt.Sprintf("{\"version\":1,\"format\":\"kvlite-logical\",\"type\":\"header\"}\n%s{\"sha256\":\"%x\",\"records\":1,\"type\":\"end\"}\n", line, sum)
	destination := openArchiveMemory(t)
	if err := destination.Import(context.Background(), strings.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	value, err := destination.GetBytes(context.Background(), "k")
	if err != nil || string(value) != "x" {
		t.Fatalf("imported payload = %q, %v", value, err)
	}
}
