package enginetest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/webong/kvlite"
)

const recoveryPathEnv = "KVLITE_ENGINETEST_RECOVERY_PATH"

// RunProcessExitRecovery verifies that a completed batch and logical
// replacement survive an abrupt process exit without DB.Close. It does not
// claim power-loss durability; that depends on the engine's sync policy.
// Call it from a dedicated, top-level test in a package that imports driver.
func RunProcessExitRecovery(t *testing.T, driver kvlite.DriverName) {
	t.Helper()
	if path := os.Getenv(recoveryPathEnv); path != "" {
		writeRecoveryFixture(t, path, driver)
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "data")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
	command.Env = append(os.Environ(), recoveryPathEnv+"="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("process-exit writer: %v: %s", err, output)
	}
	db, err := kvlite.Open(path, kvlite.WithDriver(string(driver)))
	if err != nil {
		t.Fatalf("reopen after process exit: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := db.Transport()
	assertRaw(t, store, []byte("recovery:a"), "new")
	assertRaw(t, store, []byte("recovery:b"), "second")
	var value string
	if err := db.Get(context.Background(), "recovery:logical", &value); err != nil || value != "scalar" {
		t.Fatalf("logical replacement after process exit = %q, %v", value, err)
	}
	if members, err := db.SMembers(context.Background(), "recovery:logical"); err != nil || len(members) != 0 {
		t.Fatalf("old collection after process exit = %v, %v", members, err)
	}
}

func writeRecoveryFixture(t *testing.T, path string, driver kvlite.DriverName) {
	t.Helper()
	db, err := kvlite.Open(path, kvlite.WithDriver(string(driver)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := db.Transport()
	if err := store.Put(ctx, []byte("recovery:a"), []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(ctx, []kvlite.Mutation{
		{Key: []byte("recovery:a"), Delete: true},
		{Key: []byte("recovery:b"), Value: []byte("second")},
		{Key: []byte("recovery:a"), Value: []byte("new")},
	}); err != nil {
		t.Fatal(err)
	}
	if added, err := db.SAdd(ctx, "recovery:logical", "member"); err != nil || added != 1 {
		t.Fatalf("SAdd = %d, %v", added, err)
	}
	if err := db.Put(ctx, "recovery:logical", "scalar"); err != nil {
		t.Fatal(err)
	}
}
