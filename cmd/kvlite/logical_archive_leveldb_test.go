//go:build kvlite_leveldb

package main

import (
	"context"
	"path/filepath"
	"testing"

	"kvlite"
)

func TestArchiveCLIExportsAndImportsPersistentDatabase(t *testing.T) {
	ctx := context.Background()
	sourcePath := filepath.Join(t.TempDir(), "source")
	source, err := kvlite.Open(sourcePath, kvlite.WithDriver("leveldb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Put(ctx, "user", map[string]string{"name": "Ada"}); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.jsonl")
	if code := run([]string{"export", "--path", sourcePath, "--driver", "leveldb", "--output", archive}); code != 0 {
		t.Fatalf("export exit code = %d", code)
	}
	destinationPath := filepath.Join(t.TempDir(), "destination")
	if code := run([]string{"import", "--path", destinationPath, "--driver", "leveldb", "--input", archive}); code != 0 {
		t.Fatalf("import exit code = %d", code)
	}
	destination, err := kvlite.Open(destinationPath, kvlite.WithDriver("leveldb"))
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	var user map[string]string
	if err := destination.Get(ctx, "user", &user); err != nil || user["name"] != "Ada" {
		t.Fatalf("imported user = %#v, %v", user, err)
	}
}
