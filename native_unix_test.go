//go:build cgo && (linux || (darwin && !amd64))

package kvlite

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func buildMock(t *testing.T, extraFlags ...string) string {
	t.Helper()
	name := libraryName()
	output := filepath.Join(t.TempDir(), name)
	flags := []string{"-fPIC"}
	if runtime.GOOS == "darwin" {
		flags = append(flags, "-dynamiclib")
	} else {
		flags = append(flags, "-shared")
	}
	flags = append(flags, extraFlags...)
	flags = append(flags, "testdata/mock_kvlite.c", "-o", output)
	command := exec.Command("cc", flags...)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build mock: %v: %s", err, result)
	}
	return output
}

func TestNativeRoundTrip(t *testing.T) {
	library := buildMock(t)
	db, err := Open(filepath.Join(t.TempDir(), "db"), WithLibraryPath(library), WithDriver("leveldb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Put("user", map[string]any{"name": "Ada"}, 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	var user struct {
		Name string `json:"name"`
	}
	if err := db.Get("user", &user); err != nil || user.Name != "Ada" {
		t.Fatalf("get JSON: %q, %v", user.Name, err)
	}
	if err := db.PutBytes([]byte{0, 1}, []byte{3, 0, 4}, 0); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetBytes([]byte{0, 1})
	if err != nil || len(got) != 3 || got[1] != 0 {
		t.Fatalf("get binary: %v, %v", got, err)
	}
	if err := db.DeleteBytes([]byte{0, 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetBytes([]byte{0, 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Put("closed", 1, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed handle: %v", err)
	}
}

func TestNativeLibraryCompatibility(t *testing.T) {
	wrongABI := buildMock(t, "-DKVLITE_MOCK_ABI=2")
	if _, err := Open("db", WithLibraryPath(wrongABI)); !errors.Is(err, ErrNativeLibrary) {
		t.Fatalf("ABI mismatch: %v", err)
	}
	legacy := buildMock(t, "-DKVLITE_MOCK_NO_DRIVER=1")
	if _, err := Open("db", WithLibraryPath(legacy), WithDriver("leveldb")); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing driver selection symbol: %v", err)
	}
	db, err := Open("db", WithLibraryPath(legacy))
	if err != nil {
		t.Fatalf("legacy default open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogDiscovery(t *testing.T) {
	library := buildMock(t)
	root := t.TempDir()
	driverLibrary := filepath.Join(root, "drivers", "leveldb", "lib", libraryName())
	if err := os.MkdirAll(filepath.Dir(driverLibrary), 0o755); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(library)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(driverLibrary, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KVLITE_LIBRARY_PATH", "")
	t.Setenv("KVLITE_HOME", root)
	db, err := Open("db", WithDriver("leveldb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// CI passes a release-built LevelDB library here. The regular suite remains
// independent of KVLite source and native engine dependencies.
func TestInstalledNativeLibrary(t *testing.T) {
	library := os.Getenv("KVLITE_TEST_NATIVE_LIBRARY")
	if library == "" {
		t.Skip("set KVLITE_TEST_NATIVE_LIBRARY for release-bundle integration")
	}
	driver := os.Getenv("KVLITE_TEST_NATIVE_DRIVER")
	if driver == "" {
		driver = "leveldb"
	}
	db, err := Open(filepath.Join(t.TempDir(), "db"), WithLibraryPath(library), WithDriver(driver))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Put("integration", map[string]string{"source": "go-binding"}, 0); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := db.Get("integration", &got); err != nil || got["source"] != "go-binding" {
		t.Fatalf("native bundle round trip: %v, %v", got, err)
	}
}
