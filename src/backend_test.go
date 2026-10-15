package kvlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const (
	testMemoryDriverName  DriverName = "test-memory"
	testOtherDriverName   DriverName = "test-other"
	testOptionsDriverName DriverName = "test-options"
)

var testDriverEngines = struct {
	sync.Mutex
	items map[string]*memoryEngine
}{items: make(map[string]*memoryEngine)}

// testOptionsSink records the DriverOptions the options driver last received.
var testOptionsSink = struct {
	sync.Mutex
	options DriverOptions
}{}

type testRegisteredDriver struct {
	name           DriverName
	implementation string
}

type testOptionsDriver struct{}

func init() {
	MustRegisterDriver(testRegisteredDriver{name: testMemoryDriverName, implementation: "kvlite-test-memory"})
	MustRegisterDriver(testRegisteredDriver{name: testOtherDriverName, implementation: "kvlite-test-other"})
	MustRegisterDriver(testOptionsDriver{})
}

func (testOptionsDriver) Info() DriverInfo {
	return DriverInfo{
		Driver:         testOptionsDriverName,
		Implementation: "kvlite-test-options",
		Format:         "v1",
		Version:        "test",
	}
}

func (testOptionsDriver) Available() error { return nil }

func (testOptionsDriver) Open(_ string, options DriverOptions) (Engine, error) {
	testOptionsSink.Lock()
	testOptionsSink.options = options
	testOptionsSink.Unlock()
	return newMemoryEngine(), nil
}

func (driver testRegisteredDriver) Info() DriverInfo {
	return DriverInfo{
		Driver:         driver.name,
		Implementation: driver.implementation,
		Format:         "v1",
		Version:        "test",
	}
}

func (testRegisteredDriver) Available() error { return nil }

func (driver testRegisteredDriver) Open(path string, _ DriverOptions) (Engine, error) {
	testDriverEngines.Lock()
	defer testDriverEngines.Unlock()
	storage := testDriverEngines.items[path]
	if storage == nil {
		storage = newMemoryEngine()
		testDriverEngines.items[path] = storage
	}
	return storage, nil
}

func TestOpenRegisteredDriverPersistsDataAndWritesManifest(t *testing.T) {
	path := t.TempDir()
	ctx := context.Background()

	db, err := Open(path, WithDriver(string(testMemoryDriverName)))
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Backend(); got != testMemoryDriverName {
		t.Fatalf("Backend() = %q, want %q", got, testMemoryDriverName)
	}
	if err := db.Put(ctx, "user:101", map[string]any{"name": "Ada"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(path, backendManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" {
		t.Fatal("backend manifest is empty")
	}
	manifest, found, err := readBackendManifest(path)
	if err != nil || !found {
		t.Fatalf("readBackendManifest() = %#v, %v, %v", manifest, found, err)
	}
	if manifest.Backend != testMemoryDriverName || manifest.Driver != "kvlite-test-memory" || manifest.DriverFormat != "v1" || manifest.DriverVersion != "test" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}

	reopened, err := Open(path, WithDriver(string(testMemoryDriverName)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	var user map[string]any
	if err := reopened.Get(ctx, "user:101", &user); err != nil {
		t.Fatal(err)
	}
	if user["name"] != "Ada" {
		t.Fatalf("reopened value = %#v", user)
	}
}

func TestBackendManifestRejectsDifferentDriver(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path, WithDriver(string(testMemoryDriverName)))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, otherDriver, err := registeredDriverFor(testOtherDriverName)
	if err != nil {
		t.Fatal(err)
	}
	_, err = prepareBackendManifest(path, otherDriver, false)
	if !errors.Is(err, ErrBackendMismatch) {
		t.Fatalf("prepareBackendManifest() error = %v, want ErrBackendMismatch", err)
	}
}

func TestOpenRejectsUnavailableBerkeleyDBWithoutTouchingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "berkeley")
	_, err := Open(path, WithDriver("berkeleydb"))
	if !errors.Is(err, ErrDriverNotInstalled) {
		t.Fatalf("Open() error = %v, want ErrDriverNotInstalled", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Open() created unavailable backend path: stat error = %v", statErr)
	}
}

func TestOpenDefaultDriverReportsInstalledButNotLoadedModule(t *testing.T) {
	root := t.TempDir()
	manifest := testExtensionManifest(string(DriverRocksDB))
	manifest.Kind = ModuleKindEngine
	manifest.Driver = DriverRocksDB
	manifest.Capabilities = []string{"embedded-storage", ModuleCapabilityAtomicBatch}
	writeTestModuleManifest(t, filepath.Join(root, "rocksdb"), manifest)

	t.Setenv("KVLITE_MODULE_PATH", root)
	t.Setenv("KVLITE_HOME", "")

	_, err := Open(t.TempDir())
	if !errors.Is(err, ErrDriverNotLoaded) {
		t.Fatalf("Open() error = %v, want ErrDriverNotLoaded", err)
	}
}

func TestOpenReportsInstalledDriverModuleWithoutLinkedAdapter(t *testing.T) {
	root := t.TempDir()
	manifest := testExtensionManifest("leveldb")
	manifest.Kind = ModuleKindEngine
	manifest.Driver = DriverLevelDB
	manifest.Capabilities = []string{"embedded-storage", ModuleCapabilityAtomicBatch}
	writeTestModuleManifest(t, filepath.Join(root, "leveldb"), manifest)

	t.Setenv("KVLITE_MODULE_PATH", root)
	t.Setenv("KVLITE_HOME", "")

	path := filepath.Join(t.TempDir(), "installed")
	_, err := Open(path, WithDriver(string(DriverLevelDB)))
	if !errors.Is(err, ErrDriverNotLoaded) {
		t.Fatalf("Open() error = %v, want ErrDriverNotLoaded", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Open() created unavailable backend path: stat error = %v", statErr)
	}
}

func TestWithDriverOptionReachesDriver(t *testing.T) {
	db, err := Open(t.TempDir(),
		WithDriver(string(testOptionsDriverName)),
		WithDriverOption("engine.example", int64(42)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	testOptionsSink.Lock()
	values := testOptionsSink.options.Values
	testOptionsSink.Unlock()
	if got := values["engine.example"]; got != int64(42) {
		t.Fatalf("driver option = %#v, want int64(42)", got)
	}
}

func TestWithDriverOptionRejectsInvalidArguments(t *testing.T) {
	testOptionsSink.Lock()
	testOptionsSink.options = DriverOptions{}
	testOptionsSink.Unlock()

	for name, option := range map[string]Option{
		"empty key": WithDriverOption("", 1),
		"blank key": WithDriverOption("   ", 1),
		"nil value": WithDriverOption("engine.example", nil),
	} {
		_, err := Open(t.TempDir(), WithDriver(string(testOptionsDriverName)), option)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("%s: Open() error = %v, want ErrInvalidArgument", name, err)
		}
	}
	testOptionsSink.Lock()
	leaked := testOptionsSink.options.Values
	testOptionsSink.Unlock()
	if leaked != nil {
		t.Fatalf("invalid options reached the driver: %#v", leaked)
	}
}

func TestDriversReportOnlyInstalledDrivers(t *testing.T) {
	info, err := DriverInfoFor(testMemoryDriverName)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Available || info.Driver != testMemoryDriverName {
		t.Fatalf("test driver info = %#v", info)
	}
	_, err = DriverInfoFor(DriverBerkeleyDB)
	if !errors.Is(err, ErrDriverNotInstalled) {
		t.Fatalf("DriverInfoFor(berkeleydb) error = %v, want ErrDriverNotInstalled", err)
	}
}
