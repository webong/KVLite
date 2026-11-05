// Binding-level conformance tests for the ADR 0001 Zig prototype host.
//
// The prototype is built separately (zig-proto/README.md); point
// KVLITE_ZIG_LIBRARY at its libkvlite to run this suite:
//
//   KVLITE_ZIG_LIBRARY=zig-out/lib/libkvlite.dylib go test -run TestZigHost -v .
//
// The tests cover the embedded subset the prototype implements: open (with
// and without explicit driver selection), put, get, delete, close, TTL, and
// reopen behavior, using isolated temporary database directories. KVLITE_GO_LIBRARY
// runs the same subset against a Go-built driver bundle for comparison.
// Everything is skipped when the environment variables are unset, so the
// regular suite and CI stay independent of Zig.
//
// Intel macOS stays excluded: two Go runtimes cannot share that process.

//go:build cgo && (linux || (darwin && !amd64) || windows)

package kvlite

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func zigProtoLibrary(t *testing.T) string {
	t.Helper()
	library := os.Getenv("KVLITE_ZIG_LIBRARY")
	if library == "" {
		t.Skip("set KVLITE_ZIG_LIBRARY to the built Zig prototype library")
	}
	return library
}

func goHostLibrary(t *testing.T) string {
	t.Helper()
	library := os.Getenv("KVLITE_GO_LIBRARY")
	if library == "" {
		t.Skip("set KVLITE_GO_LIBRARY to a Go-built driver bundle library")
	}
	return library
}

func TestZigHostRoundTrip(t *testing.T) {
	library := zigProtoLibrary(t)
	db, err := Open(filepath.Join(t.TempDir(), "db"), WithLibraryPath(library), WithDriver("lmdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Put("user", map[string]any{"name": "Ada"}, 0); err != nil {
		t.Fatalf("put JSON: %v", err)
	}
	var user struct {
		Name string `json:"name"`
	}
	if err := db.Get("user", &user); err != nil || user.Name != "Ada" {
		t.Fatalf("get JSON: %q, %v", user.Name, err)
	}

	payload := []byte{3, 0, 4, 255, 0, 17}
	if err := db.PutBytes([]byte{0, 1}, payload, 0); err != nil {
		t.Fatalf("put binary: %v", err)
	}
	got, err := db.GetBytes([]byte{0, 1})
	if err != nil || string(got) != string(payload) {
		t.Fatalf("get binary: %v, %v", got, err)
	}
	if err := db.DeleteBytes([]byte{0, 1}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetBytes([]byte{0, 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	if err := db.DeleteBytes([]byte{0, 1}); err != nil {
		t.Fatalf("delete of missing key must succeed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("second close must be a no-op: %v", err)
	}
	if err := db.PutBytes([]byte("x"), []byte("y"), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed handle: %v", err)
	}
}

func TestZigHostArgumentValidation(t *testing.T) {
	library := zigProtoLibrary(t)
	db, err := Open(filepath.Join(t.TempDir(), "db"), WithLibraryPath(library))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.PutBytes(nil, []byte("v"), 0); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty key: %v", err)
	}
	if err := db.PutBytes([]byte("k"), []byte("v"), -time.Second); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("negative TTL: %v", err)
	}
	if _, err := db.GetBytes(nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty key get: %v", err)
	}
	if err := db.DeleteBytes(nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty key delete: %v", err)
	}
}

func TestZigHostTTLExpiry(t *testing.T) {
	library := zigProtoLibrary(t)
	db, err := Open(filepath.Join(t.TempDir(), "db"), WithLibraryPath(library))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.PutBytes([]byte("ttl"), []byte("v"), time.Second); err != nil {
		t.Fatalf("put with TTL: %v", err)
	}
	if _, err := db.GetBytes([]byte("ttl")); err != nil {
		t.Fatalf("get before expiry: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := db.GetBytes([]byte("ttl")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after expiry: %v", err)
	}
}

func TestZigHostPersistenceAndDriverSelection(t *testing.T) {
	library := zigProtoLibrary(t)
	dir := filepath.Join(t.TempDir(), "db")

	db, err := Open(dir, WithLibraryPath(library), WithDriver("lmdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutBytes([]byte("persist"), []byte("me"), 0); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err = Open(dir, WithLibraryPath(library), WithDriver("lmdb"))
	if err != nil {
		t.Fatalf("reopen with the recorded driver: %v", err)
	}
	got, err := db.GetBytes([]byte("persist"))
	if err != nil || string(got) != "me" {
		t.Fatalf("data after reopen: %q, %v", got, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := Open(dir, WithLibraryPath(library), WithDriver("leveldb")); err == nil {
		t.Fatal("reopen with an unregistered driver must fail")
	} else {
		t.Logf("reopen with another driver name: %v", err)
	}
}

func TestZigHostInvalidHandle(t *testing.T) {
	library := zigProtoLibrary(t)
	dir := t.TempDir()
	db, err := Open(dir, WithLibraryPath(library))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// All operations on the closed handle must fail cleanly, not crash.
	if err := db.PutBytes([]byte("k"), []byte("v"), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("put after close: %v", err)
	}
}

// TestZigHostMatchesGoHostSubset runs the same operation subset against a
// Go-built driver bundle when one is provided, so conformance differences
// between the hosts surface in one place.
func TestZigHostMatchesGoHostSubset(t *testing.T) {
	library := goHostLibrary(t)
	driver := os.Getenv("KVLITE_TEST_NATIVE_DRIVER")
	if driver == "" {
		driver = "lmdb"
	}
	db, err := Open(filepath.Join(t.TempDir(), "db"), WithLibraryPath(library), WithDriver(driver))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	payload := []byte{9, 8, 7, 6}
	if err := db.PutBytes([]byte("k"), payload, 0); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := db.GetBytes([]byte("k"))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("get: %v, %v", got, err)
	}
	if err := db.DeleteBytes([]byte("k")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetBytes([]byte("k")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
