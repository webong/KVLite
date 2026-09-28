package kvlite

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"sort"
	"unicode/utf8"
)

const (
	logicalArchiveFormat  = "kvlite-logical"
	logicalArchiveVersion = 1
	importBatchSize       = 256
	importBatchBytes      = 4 << 20
)

// Logical archives are newline-delimited JSON. Binary keys and payloads are
// JSON base64 strings, so even Redis keys containing non-UTF-8 bytes survive a
// round trip. The format describes KVLite data, not an engine's on-disk layout.
type logicalArchiveRecord struct {
	Type      string                `json:"type"`
	Format    string                `json:"format,omitempty"`
	Version   int                   `json:"version,omitempty"`
	Key       []byte                `json:"key,omitempty"`
	Field     []byte                `json:"field,omitempty"`
	Member    []byte                `json:"member,omitempty"`
	Codec     string                `json:"codec,omitempty"`
	Payload   []byte                `json:"payload,omitempty"`
	ExpiresAt int64                 `json:"expires_at_ns,string,omitempty"`
	Items     []logicalArchiveValue `json:"items,omitempty"`
	Records   uint64                `json:"records,omitempty"`
	SHA256    string                `json:"sha256,omitempty"`
}

type logicalArchiveValue struct {
	Codec     string `json:"codec"`
	Payload   []byte `json:"payload,omitempty"`
	ExpiresAt int64  `json:"expires_at_ns,string,omitempty"`
}

type archiveWriter struct {
	out   io.Writer
	hash  hash.Hash
	count uint64
}

func (writer *archiveWriter) write(record logicalArchiveRecord) error {
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	n, err := writer.out.Write(line)
	if err != nil {
		return err
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	if record.Type != "header" && record.Type != "end" {
		_, _ = writer.hash.Write(line)
		writer.count++
	}
	return nil
}

// Export writes a versioned logical archive of all live KVLite records. It
// preserves codec identities and absolute expiry times without decoding user
// values. Writes through this DB handle are serialized for the export; remote
// handles are rejected because their scans cannot promise one owner snapshot.
// A failed export leaves an incomplete archive, which Import will reject.
func (db *DB) Export(ctx context.Context, out io.Writer) error {
	if out == nil {
		return fmt.Errorf("%w: archive writer is required", ErrInvalidArgument)
	}
	if err := db.ensureOpen(); err != nil {
		return err
	}
	if db.remote {
		return fmt.Errorf("%w: logical export requires an embedded owner", ErrUnsupportedOperation)
	}
	db.protocolMu.Lock()
	defer db.protocolMu.Unlock()

	now := db.cfg.now().UnixNano()
	expiries := make(map[string]int64)
	if err := db.engine.ScanPrefix(ctx, []byte{kindCollectionTTL}, func(key, value []byte) error {
		if len(value) != 8 || len(key) < 1 {
			return fmt.Errorf("kvlite: invalid collection expiry record")
		}
		expiry := int64(binary.BigEndian.Uint64(value))
		if expiry <= 0 {
			return fmt.Errorf("kvlite: invalid collection expiry timestamp")
		}
		expiries[string(key[1:])] = expiry
		return nil
	}); err != nil {
		return fmt.Errorf("kvlite: export: %w", err)
	}

	writer := archiveWriter{out: out, hash: sha256.New()}
	if err := writer.write(logicalArchiveRecord{Type: "header", Format: logicalArchiveFormat, Version: logicalArchiveVersion}); err != nil {
		return err
	}
	validation := newArchiveValidation()
	err := db.engine.ScanPrefix(ctx, nil, func(key, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(key) == 0 {
			return fmt.Errorf("kvlite: invalid empty storage key")
		}
		if key[0] == kindCollectionTTL {
			return nil
		}
		name, valid := logicalKeyFromStorage(key)
		if !valid {
			return fmt.Errorf("kvlite: unknown logical storage key")
		}
		if expiry := expiries[name]; expiry > 0 && now >= expiry {
			return nil
		}
		record := logicalArchiveRecord{Key: []byte(name)}
		switch key[0] {
		case kindValue, kindHash:
			item, err := unmarshalEnvelope(value)
			if err != nil {
				return err
			}
			if item.expiresAt > 0 && now >= item.expiresAt {
				return nil
			}
			if key[0] == kindValue {
				record.Type = "value"
			} else {
				record.Type = "hash"
				record.Field = append([]byte(nil), key[5+len(name):]...)
			}
			record.Codec, record.Payload, record.ExpiresAt = item.codec, item.payload, item.expiresAt
		case kindSet:
			if len(value) != 1 || value[0] != 1 {
				return fmt.Errorf("kvlite: invalid set member record")
			}
			record.Type = "set"
			record.Member = append([]byte(nil), key[5+len(name):]...)
		case kindList:
			items, err := decodeList(value)
			if err != nil {
				return err
			}
			record.Type = "list"
			for _, encoded := range items {
				item, err := unmarshalEnvelope(encoded)
				if err != nil {
					return err
				}
				if item.expiresAt <= 0 || now < item.expiresAt {
					record.Items = append(record.Items, logicalArchiveValue{Codec: item.codec, Payload: item.payload, ExpiresAt: item.expiresAt})
				}
			}
		default:
			return fmt.Errorf("kvlite: unknown logical storage kind %d", key[0])
		}
		if err := validation.add(record); err != nil {
			return err
		}
		return writer.write(record)
	})
	if err != nil {
		return fmt.Errorf("kvlite: export: %w", err)
	}
	keys := make([]string, 0, len(expiries))
	for key := range expiries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if expiry := expiries[key]; expiry > now && validation.kinds[key] != "" && validation.kinds[key] != "value" {
			record := logicalArchiveRecord{Type: "collection_ttl", Key: []byte(key), ExpiresAt: expiry}
			if err := validation.add(record); err != nil {
				return fmt.Errorf("kvlite: export: %w", err)
			}
			if err := writer.write(record); err != nil {
				return err
			}
		}
	}
	return writer.write(logicalArchiveRecord{Type: "end", Records: writer.count, SHA256: hex.EncodeToString(writer.hash.Sum(nil))})
}

type archiveValidation struct {
	kinds    map[string]string
	seen     map[string]struct{}
	expiries map[string]int64
}

func newArchiveValidation() *archiveValidation {
	return &archiveValidation{
		kinds: make(map[string]string), seen: make(map[string]struct{}), expiries: make(map[string]int64),
	}
}

func (validation *archiveValidation) add(record logicalArchiveRecord) error {
	if err := validateArchiveRecord(record); err != nil {
		return err
	}
	name := string(record.Key)
	if record.Type == "collection_ttl" {
		if _, found := validation.expiries[name]; found {
			return fmt.Errorf("%w: duplicate collection expiry", ErrInvalidArgument)
		}
		validation.expiries[name] = record.ExpiresAt
		return nil
	}
	if old, found := validation.kinds[name]; found && old != record.Type {
		return fmt.Errorf("%w: mixed logical types for key %q", ErrInvalidArgument, name)
	}
	validation.kinds[name] = record.Type
	var storageKey []byte
	switch record.Type {
	case "value":
		storageKey = valueKey(name)
	case "hash":
		storageKey = namespacedKey(kindHash, name, string(record.Field))
	case "set":
		storageKey = namespacedKey(kindSet, name, string(record.Member))
	case "list":
		storageKey = listKey(name)
	}
	if _, found := validation.seen[string(storageKey)]; found {
		return fmt.Errorf("%w: duplicate logical record", ErrInvalidArgument)
	}
	validation.seen[string(storageKey)] = struct{}{}
	return nil
}

func validateArchiveRecord(record logicalArchiveRecord) error {
	invalid := func(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidArgument, reason) }
	if record.Format != "" || record.Version != 0 || record.Records != 0 || record.SHA256 != "" {
		return invalid("unexpected archive metadata in data record")
	}
	if record.ExpiresAt < 0 {
		return invalid("negative expiry")
	}
	switch record.Type {
	case "value", "hash":
		if record.Codec == "" || len(record.Codec) > 65535 || !utf8.ValidString(record.Codec) || len(record.Items) != 0 || len(record.Member) != 0 {
			return invalid("invalid value record")
		}
		if record.Type == "value" && len(record.Field) != 0 {
			return invalid("value record has a field")
		}
	case "set":
		if record.Codec != "" || len(record.Payload) != 0 || record.ExpiresAt != 0 || len(record.Items) != 0 || len(record.Field) != 0 {
			return invalid("invalid set record")
		}
	case "list":
		if record.Codec != "" || len(record.Payload) != 0 || record.ExpiresAt != 0 || len(record.Field) != 0 || len(record.Member) != 0 {
			return invalid("invalid list record")
		}
		for _, item := range record.Items {
			if item.Codec == "" || len(item.Codec) > 65535 || !utf8.ValidString(item.Codec) || item.ExpiresAt < 0 {
				return invalid("invalid list item")
			}
		}
	case "collection_ttl":
		if record.ExpiresAt <= 0 || record.Codec != "" || len(record.Payload) != 0 || len(record.Items) != 0 || len(record.Field) != 0 || len(record.Member) != 0 {
			return invalid("invalid collection expiry record")
		}
	default:
		return invalid("unknown logical record type")
	}
	return nil
}

func decodeLogicalArchive(ctx context.Context, in io.Reader, visit func(logicalArchiveRecord) error) error {
	reader := bufio.NewReader(in)
	line, err := readArchiveLine(reader)
	if err != nil {
		return fmt.Errorf("kvlite: archive header: %w", err)
	}
	var header logicalArchiveRecord
	if err := decodeArchiveLine(line, &header); err != nil {
		return fmt.Errorf("kvlite: archive header: %w", err)
	}
	if header.Type != "header" || header.Format != logicalArchiveFormat || header.Version != logicalArchiveVersion {
		return fmt.Errorf("%w: unsupported logical archive format or version", ErrInvalidArgument)
	}
	if len(header.Key) != 0 || len(header.Field) != 0 || len(header.Member) != 0 || header.Codec != "" || len(header.Payload) != 0 || header.ExpiresAt != 0 || len(header.Items) != 0 || header.Records != 0 || header.SHA256 != "" {
		return fmt.Errorf("%w: invalid logical archive header", ErrInvalidArgument)
	}
	hasher := sha256.New()
	var count uint64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := readArchiveLine(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("%w: archive is missing its end marker", ErrInvalidArgument)
			}
			return fmt.Errorf("kvlite: archive record: %w", err)
		}
		var record logicalArchiveRecord
		if err := decodeArchiveLine(line, &record); err != nil {
			return fmt.Errorf("kvlite: archive record: %w", err)
		}
		if record.Type == "end" {
			if record.Format != "" || record.Version != 0 || len(record.Key) != 0 || len(record.Field) != 0 || len(record.Member) != 0 || record.Codec != "" || len(record.Payload) != 0 || record.ExpiresAt != 0 || len(record.Items) != 0 {
				return fmt.Errorf("%w: invalid logical archive end marker", ErrInvalidArgument)
			}
			if record.Records != count || record.SHA256 != hex.EncodeToString(hasher.Sum(nil)) {
				return fmt.Errorf("%w: archive count or checksum mismatch", ErrInvalidArgument)
			}
			if _, err := readArchiveLine(reader); !errors.Is(err, io.EOF) {
				if err == nil {
					return fmt.Errorf("%w: data after archive end", ErrInvalidArgument)
				}
				return fmt.Errorf("kvlite: archive trailing data: %w", err)
			}
			return nil
		}
		_, _ = hasher.Write(line)
		count++
		if err := visit(record); err != nil {
			return err
		}
	}
}

func readArchiveLine(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadBytes('\n')
	if errors.Is(err, io.EOF) && len(line) > 0 {
		return line, nil
	}
	return line, err
}

func decodeArchiveLine(line []byte, record *logicalArchiveRecord) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(record); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: multiple JSON values on one archive line", ErrInvalidArgument)
		}
		return err
	}
	return nil
}

// Import restores a logical archive into an empty embedded database. It first
// validates the entire stream (including its end marker and checksum) using a
// private temporary file, then writes bounded batches. Any failure after
// writing starts may leave a partial destination; discard that destination
// before retrying. Expired records are not resurrected during import.
func (db *DB) Import(ctx context.Context, in io.Reader) error {
	if in == nil {
		return fmt.Errorf("%w: archive reader is required", ErrInvalidArgument)
	}
	if err := db.ensureOpen(); err != nil {
		return err
	}
	if db.remote {
		return fmt.Errorf("%w: logical import requires an embedded owner", ErrUnsupportedOperation)
	}
	db.protocolMu.Lock()
	defer db.protocolMu.Unlock()
	stopScan := errors.New("kvlite: stop destination scan")
	occupied := false
	err := db.engine.ScanPrefix(ctx, nil, func(_, _ []byte) error { occupied = true; return stopScan })
	if occupied {
		return fmt.Errorf("%w: import destination must be empty", ErrInvalidArgument)
	}
	if err != nil {
		return err
	}

	staging, err := os.CreateTemp("", "kvlite-logical-import-*.jsonl")
	if err != nil {
		return err
	}
	defer os.Remove(staging.Name())
	defer staging.Close()
	if _, err := io.Copy(staging, &archiveContextReader{ctx: ctx, in: in}); err != nil {
		return fmt.Errorf("kvlite: stage import: %w", err)
	}
	if _, err := staging.Seek(0, io.SeekStart); err != nil {
		return err
	}
	validation := newArchiveValidation()
	if err := decodeLogicalArchive(ctx, staging, validation.add); err != nil {
		return err
	}
	for key, expiry := range validation.expiries {
		if validation.kinds[key] == "value" {
			return fmt.Errorf("%w: collection expiry on scalar key %q", ErrInvalidArgument, key)
		}
		if _, found := validation.kinds[key]; !found && expiry > 0 {
			return fmt.Errorf("%w: collection expiry without a collection", ErrInvalidArgument)
		}
	}
	if _, err := staging.Seek(0, io.SeekStart); err != nil {
		return err
	}
	now := db.cfg.now().UnixNano()
	batch := make([]Mutation, 0, importBatchSize)
	batchBytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := db.engine.Apply(ctx, batch); err != nil {
			return fmt.Errorf("kvlite: import batch (destination may be partial): %w", err)
		}
		batch = batch[:0]
		batchBytes = 0
		return nil
	}
	err = decodeLogicalArchive(ctx, staging, func(record logicalArchiveRecord) error {
		name := string(record.Key)
		if expiry := validation.expiries[name]; expiry > 0 && now >= expiry {
			return nil
		}
		mutation, keep, err := archiveMutation(record, now)
		if err != nil {
			return err
		}
		if !keep {
			return nil
		}
		mutationBytes := len(mutation.Key) + len(mutation.Value)
		if len(batch) > 0 && batchBytes+mutationBytes > importBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, mutation)
		batchBytes += mutationBytes
		if len(batch) == importBatchSize || batchBytes >= importBatchBytes {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	return flush()
}

type archiveContextReader struct {
	ctx context.Context
	in  io.Reader
}

func (reader *archiveContextReader) Read(data []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.in.Read(data)
}

func archiveMutation(record logicalArchiveRecord, now int64) (Mutation, bool, error) {
	name := string(record.Key)
	switch record.Type {
	case "value", "hash":
		if record.ExpiresAt > 0 && now >= record.ExpiresAt {
			return Mutation{}, false, nil
		}
		value, err := marshalEnvelope(record.Codec, record.Payload, record.ExpiresAt)
		if err != nil {
			return Mutation{}, false, err
		}
		if record.Type == "value" {
			return Mutation{Key: valueKey(name), Value: value}, true, nil
		}
		return Mutation{Key: namespacedKey(kindHash, name, string(record.Field)), Value: value}, true, nil
	case "set":
		return Mutation{Key: namespacedKey(kindSet, name, string(record.Member)), Value: []byte{1}}, true, nil
	case "list":
		items := make([][]byte, 0, len(record.Items))
		for _, item := range record.Items {
			if item.ExpiresAt > 0 && now >= item.ExpiresAt {
				continue
			}
			encoded, err := marshalEnvelope(item.Codec, item.Payload, item.ExpiresAt)
			if err != nil {
				return Mutation{}, false, err
			}
			items = append(items, encoded)
		}
		return Mutation{Key: listKey(name), Value: encodeList(items)}, true, nil
	case "collection_ttl":
		if now >= record.ExpiresAt {
			return Mutation{}, false, nil
		}
		value := make([]byte, 8)
		binary.BigEndian.PutUint64(value, uint64(record.ExpiresAt))
		return Mutation{Key: collectionTTLKey(name), Value: value}, true, nil
	default:
		return Mutation{}, false, fmt.Errorf("%w: unknown logical record type", ErrInvalidArgument)
	}
}
