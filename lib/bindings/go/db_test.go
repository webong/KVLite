package kvlite

import (
	"errors"
	"testing"
	"time"
)

func TestInputValidation(t *testing.T) {
	for _, options := range [][]Option{{WithDriver("../rocksdb")}, {WithLibraryPath("")}, {nil}} {
		if _, err := Open("db", options...); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("Open options %v: expected invalid argument, got %v", options, err)
		}
	}
	if _, err := Open(""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty path: %v", err)
	}
	if _, err := Open("db", WithLibraryPath("/path/that/does/not/exist")); !errors.Is(err, ErrNativeLibrary) {
		t.Fatalf("missing explicit library: %v", err)
	}
	var db *DB
	if err := db.PutBytes([]byte("k"), []byte("v"), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("nil close: %v", err)
	}
	if _, err := Open("db", WithDriver("leveldb"), WithLibraryPath("missing")); !errors.Is(err, ErrNativeLibrary) {
		t.Fatalf("driver with missing library: %v", err)
	}
}

func TestTTLSecondsRoundUp(t *testing.T) {
	got := &recordingNative{}
	db := &DB{native: got}
	if err := db.PutBytes([]byte("k"), nil, 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got.ttl != 2 {
		t.Fatalf("ttl = %d, want 2", got.ttl)
	}
	if err := db.PutBytes([]byte("k"), nil, -time.Second); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("negative TTL: %v", err)
	}
	if err := db.PutBytes(nil, nil, 0); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty key: %v", err)
	}
}

type recordingNative struct{ ttl int64 }

func (r *recordingNative) put(_, _ []byte, ttl int64) error { r.ttl = ttl; return nil }
func (r *recordingNative) get(_ []byte) ([]byte, error)     { return nil, ErrNotFound }
func (r *recordingNative) delete(_ []byte) error            { return nil }
func (r *recordingNative) close() error                     { return nil }
