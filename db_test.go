package kvlite

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFindLibraryPrefersDriverlessHost(t *testing.T) {
	home := t.TempDir()
	name := libraryName()
	host := filepath.Join(home, "host", "lib", name)
	linked := filepath.Join(home, "drivers", "leveldb", "lib", name)
	for _, path := range []string{host, linked} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KVLITE_LIBRARY_PATH", "")
	t.Setenv("KVLITE_HOME", home)
	t.Setenv("KVLITE_SYSTEM_MODULE_PATH", "")
	got, err := findLibrary(config{driver: "leveldb", explicitDriver: true})
	if err != nil || got != host {
		t.Fatalf("findLibrary() = %q, %v; want driverless host %q", got, err, host)
	}
}

func TestIntelMacGoBindingRefusesGoSharedLibrary(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "amd64" {
		t.Skip("Intel macOS Go-runtime safety guard")
	}
	t.Setenv("KVLITE_ALLOW_INTEL_DLOPEN", "")
	path := filepath.Join(t.TempDir(), libraryName())
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.TempDir(), WithLibraryPath(path)); !errors.Is(err, ErrNativeLibrary) {
		t.Fatalf("Open(Go-built C ABI on Intel macOS) = %v, want ErrNativeLibrary", err)
	}
}

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
